package app

// cmd_kicad_rules.go — `pcbpilot kicad rules`: design intent (+ simulation,
// declared currents, the board stackup) → KiCad design rules on a project,
// verified with KiCad itself.
//
// One derivation with kicad route (kicad_rules_derive.go): the intent's net
// requirements, insulation pairs and domain edges, widths raised to the
// current-derived width, one bridge netclasses call that floors everything
// at the board's own minimums. kicad rules adds the pre-defined sizes and a
// .kicad_dru block of track_width minimums on every layer
// (kicad_rules_block.go), works on a stage copy, reads the result back with
// pcbnew (bridge rules-read) and proves the rules resolve with kicad-cli DRC
// on probe tracks before anything is published.
//
// Modes (exactly one): --plan (read only; --plan-out writes the plan with
// its revision), --apply (plan + publish), --apply-plan P --revision R
// (publish a reviewed plan if the files still give it), --check (read-only
// gate on an existing, possibly routed, board).
//
// Exit: 0 pass, 6 fail, 5 cannot-check (or an incomplete plan), 4 KiCad has
// the project open / a file changed during the run / revision mismatch /
// another writer, 1 usage or tool error.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kfile"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

const (
	rulesSchema     = "pcbpilot.kicad.rules/1"
	rulesPlanSchema = "pcbpilot.kicad.rules.plan/1"

	// Report error codes.
	errRulesUsage      = "E_USAGE"
	errRulesInput      = "E_INPUT"
	errRulesTool       = "E_KICAD_TOOL"
	errRulesKiCadOpen  = "KICAD_OPEN"
	errRulesIncomplete = "E_PLAN_INCOMPLETE"
	errRulesConflict   = "E_USER_RULE_CONFLICT"
	errRulesVerify     = "E_VERIFY"
	errRulesReadback   = "E_VERIFY_READBACK"
	errRulesRollback   = "E_ROLLBACK"
)

type kicadRulesOpts struct {
	pcb, intent, sim   string
	requirements       []string
	plan, apply, check bool
	planOut, applyPlan string
	revision, report   string
	replace            []string
	stage              string
	env                *kfile.Env   // tests: lock identity
	backend            rulesBackend // tests: replaces KiCad
	beforeCommit       func()       // tests: runs after the plan, before the commit
	beforeReadback     func()       // tests: runs after the commit, before the readback
}

// rulesBackend is what kicad rules needs from KiCad (*kicad.Tools).
type rulesBackend interface {
	Snapshot(pcb string) ([]byte, error)
	RulesRead(pcb string) (*kicad.RulesReadback, error)
	NetclassesReq(pcb string, req kicad.NetclassRequest, out string) (*kicad.NetclassFull, error)
	RulesProbe(pcb string, tracks []kicad.ProbeTrack, out string) ([]string, error)
	DRC(pcb, out string) (*kicad.DRCReport, error)
	Version() (string, error)
}

func newKicadRulesCmd(stdout, stderr io.Writer) *cobra.Command {
	o := kicadRulesOpts{}
	c := &cobra.Command{
		Use:   "rules --pcb BOARD.kicad_pcb --intent intent.json (--plan | --apply | --apply-plan P --revision R | --check)",
		Short: "Design intent + simulation → KiCad netclasses, .kicad_dru rules and pre-defined sizes, verified with KiCad (plan / apply / check)",
		Long: `Turn the design intent (and the power simulation, declared currents and the board
stackup) into KiCad design rules on a project, verifiably — the same derivation
kicad route uses for its pre-route gate:

  - every intent net gets a netclass PPn_W<width>: outer width, clearance, via
    (KiCad netclass), inner width as a (layer inner) track_width rule; the
    width is max(intent widthMil, the IPC-2221/2152 width of the net's current
    on the stackup's copper, 0.05 mm grid). The current is the larger of the
    --sim DC current (a switch node's ripple RMS) and a declared peak / RMS
    (intent peakA / rmsA, --requirements JSON {"nets": {NET: {"peakA","rmsA"}}});
    without either, the intent's own simulated / declared currentA. A power,
    ground or switch net with current but no number (or a pulsed net without
    peakA / rmsA) is cannot-check: its intent width still applies. A current
    width above an impedance-controlled or paired net's width fails
    (incompatible requirements).
  - insulation pairs → domain classes PPD_<domain> with clearance + creepage
    rules; the pair clearance is also put on every net of the pair (route's
    behaviour, an over-constraint kept for compatibility);
  - nothing goes below the board's own minimums (track, clearance, via, hole,
    annular ring), the floors kicad route applies;
  - a .kicad_dru block "pcbpilot-rules" makes the floor a DRC minimum on every
    layer (a netclass width is only a routing default): the intent minimum
    (neck-down limit) when the net has one, else the full width;
  - KiCad's pre-defined track widths and via sizes get the classes' sizes (a
    GUI convenience, never counted as a rule).

Everything is computed on a stage copy, read back with pcbnew (effective class
and sizes per net) and proven with kicad-cli DRC on probe tracks just below
each floor and inside each pair clearance (a .kicad_dru KiCad cannot parse is
ignored by DRC without an error — the probes catch that).

Modes (exactly one):
  --plan                 read only: the rules it would write, per net with the
                         reason; --plan-out FILE writes the plan and its revision
  --apply                plan, then publish .kicad_pro + .kicad_dru with a kfile
                         transaction (KiCad lock check, base hashes), read back
                         with KiCad, roll back on any mismatch
  --apply-plan P --revision R
                         publish a reviewed plan only if the files still give R
  --check                read-only gate on an existing (routed) board

Checks (each tri-state, reported separately): plan-coverage, staged-readback /
readback (apply) or effective-rules (check), and in --check copper-widths,
via-size, via-current (route's gate) and the advisory placement proxy
(isolation-placement: courtyard edge-to-edge of same-side footprints of two
insulation domains — a placement check, not copper clearance; DRC with the
pair rules covers copper). --stage placed skips the copper checks.

User netclasses and rules that do not conflict are kept. A conflict (a user
class pcbpilot's class would outrank to a different value, a user class with a
pcbpilot name, a user .kicad_dru rule a pcbpilot rule would weaken) refuses the
apply unless --replace NAME names it (a replaced user rule is removed from the
.kicad_dru; a replaced user class is outranked, or removed when it carries a
pcbpilot name).

Exit: 0 pass, 6 fail, 5 cannot-check / incomplete plan, 4 KICAD_OPEN /
E_FILE_CHANGED / E_REVISION_MISMATCH / E_WRITER_BUSY, 1 usage or tool error.
JSON report on stdout (and --report FILE.json); diagnostics on stderr.`,
		Args: cobra.NoArgs,
		Example: `  pcbpilot kicad rules --pcb board.kicad_pcb --intent intent.json --sim sim.json --plan --plan-out rules.plan.json
  pcbpilot kicad rules --pcb board.kicad_pcb --intent intent.json --sim sim.json --apply-plan rules.plan.json --revision <rev>
  pcbpilot kicad rules --pcb board.kicad_pcb --intent intent.json --sim sim.json --check --report rules-check.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKicadRules(o, stdout, stderr)
		},
	}
	f := c.Flags()
	f.StringVar(&o.pcb, "pcb", "", "board (.kicad_pcb; its .kicad_pro / .kicad_dru next to it) — required")
	f.StringVar(&o.intent, "intent", "", "intent.json (intent derive) — required")
	f.StringVar(&o.sim, "sim", "", "sim.json (sim power): per-net current for the width basis")
	f.StringArrayVar(&o.requirements, "requirements", nil, "requirement file (repeatable): bound to the plan by hash; a .json with {\"nets\": {NET: {\"peakA\",\"rmsA\"}}} declares currents")
	f.BoolVar(&o.plan, "plan", false, "read only: print the rules it would write")
	f.BoolVar(&o.apply, "apply", false, "plan and write the rules (verified, rolled back on any mismatch)")
	f.BoolVar(&o.check, "check", false, "read only: gate an existing (routed) board against the plan")
	f.StringVar(&o.planOut, "plan-out", "", "with --plan: write the plan (with its revision) to this .json")
	f.StringVar(&o.applyPlan, "apply-plan", "", "write a plan made by --plan --plan-out (needs --revision)")
	f.StringVar(&o.revision, "revision", "", "the revision of --apply-plan; refused when the files no longer give it")
	f.StringVar(&o.report, "report", "", "also write the JSON report to this .json")
	f.StringArrayVar(&o.replace, "replace", nil, "a conflicting user netclass or .kicad_dru rule pcbpilot may replace, by name (repeatable)")
	f.StringVar(&o.stage, "stage", "routed", "--check: routed (copper checked) | placed (copper checks not applicable)")
	return c
}

// ── report ──────────────────────────────────────────────────────────────────

type rulesCheck struct {
	Name     string      `json:"name"`
	Status   gate.Status `json:"status"`
	Advisory bool        `json:"advisory,omitempty"`
	Detail   string      `json:"detail"`
	Items    []string    `json:"items,omitempty"`
	Info     []string    `json:"info,omitempty"`
}

type rulesError struct {
	Code   string `json:"code"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

type rulesInput struct {
	Role   string `json:"role"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	SHA256 string `json:"sha256,omitempty"`
}

// rulesNetPlan is one net of the plan.
type rulesNetPlan struct {
	rulesNetBasis
	Class        string  `json:"class"`
	Domain       string  `json:"domain,omitempty"`
	OuterMil     float64 `json:"outerMil"`
	InnerMil     float64 `json:"innerMil"`
	MinMil       float64 `json:"minMil"`
	OuterFloor   float64 `json:"outerFloorMil"`
	InnerFloor   float64 `json:"innerFloorMil,omitempty"`
	ClearanceMil float64 `json:"clearanceMil"`
	ViaDiaMil    float64 `json:"viaDiaMil"`
	ViaDrillMil  float64 `json:"viaDrillMil"`
	Via          struct {
		IntentCount int     `json:"intentCountPerTransition"`
		NeedCount   int     `json:"currentNeedsPerTransition,omitempty"`
		PerViaA     float64 `json:"perViaA,omitempty"`
		Basis       string  `json:"basis"`
	} `json:"via"`
	Floors []string `json:"floors,omitempty"`
}

type rulesIsoPlan struct {
	A           string   `json:"a"`
	B           string   `json:"b"`
	ClassA      string   `json:"classA"`
	ClassB      string   `json:"classB"`
	ClearanceMm float64  `json:"clearanceMm"`
	CreepageMm  float64  `json:"creepageMm,omitempty"`
	Bridges     []string `json:"bridges,omitempty"`
	Note        string   `json:"note"`
}

type rulesConflict struct {
	Kind     string `json:"kind"` // class-override | class-name | dru-weakened | dru-name
	Name     string `json:"name"`
	Detail   string `json:"detail"`
	Replaced bool   `json:"replaced"`
}

type rulesFile struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	BaseSHA string `json:"baseSha,omitempty"`
	NewSHA  string `json:"newSha,omitempty"`
}

type rulesReport struct {
	Schema       string             `json:"schema"`
	OK           bool               `json:"ok"`
	Verdict      gate.Status        `json:"verdict"`
	Mode         string             `json:"mode"`
	PCB          string             `json:"pcb"`
	Revision     string             `json:"revision,omitempty"`
	RequestSHA   string             `json:"requestSha256,omitempty"`
	KiCadVersion string             `json:"kicadVersion,omitempty"`
	Inputs       []rulesInput       `json:"inputs"`
	Copper       *rulesCopper       `json:"copper,omitempty"`
	Minimums     *kicad.Minimums    `json:"boardMinimumsNm,omitempty"`
	Nets         []rulesNetPlan     `json:"nets"`
	Classes      []kicad.NetClass   `json:"classes"`
	Isolation    []rulesIsoPlan     `json:"isolation"`
	DRURules     []rulesBlockRule   `json:"druTrackRules"`
	DRURouteRule int                `json:"druRouteRules"`
	Sizes        *kicad.SizesResult `json:"predefinedSizes,omitempty"`
	Conflicts    []rulesConflict    `json:"conflicts"`
	Checks       []rulesCheck       `json:"checks"`
	Files        []rulesFile        `json:"files"`
	Committed    bool               `json:"committed"`
	RolledBack   bool               `json:"rolledBack,omitempty"`
	Errors       []rulesError       `json:"errors"`
	Warnings     []string           `json:"warnings,omitempty"`
	Assumptions  []string           `json:"assumptions,omitempty"`
}

// rulesPlanFile is what --plan-out writes and --apply-plan reads.
type rulesPlanFile struct {
	Schema   string       `json:"schema"`
	Revision string       `json:"revision"`
	PCB      string       `json:"pcb"`
	Inputs   []rulesInput `json:"inputs"`
	Report   *rulesReport `json:"plan"`
}

func (r *rulesReport) check(c rulesCheck) { r.Checks = append(r.Checks, c) }

func (r *rulesReport) results() []gate.Result {
	out := make([]gate.Result, len(r.Checks))
	for i, c := range r.Checks {
		out[i] = gate.Result{Name: c.Name, Status: c.Status, Advisory: c.Advisory, Detail: c.Detail}
	}
	return out
}

func (r *rulesReport) checkStatus(name string) gate.Status {
	for _, c := range r.Checks {
		if c.Name == name {
			return c.Status
		}
	}
	return ""
}

// ── run ─────────────────────────────────────────────────────────────────────

// rulesRun is one execution.
type rulesRun struct {
	o       kicadRulesOpts
	rep     *rulesReport
	t       rulesBackend
	stderr  io.Writer
	stage   string
	snaps   []kfile.Snapshot // every input as read
	data    map[string][]byte
	pro     string
	dru     string
	staged  string // staged result board (with its .kicad_pro / .kicad_dru)
	stIn    string // staged input board
	in      *designIntent
	snapRaw []byte
	snap    *rulesSnap
	rb0     *kicad.RulesReadback
	full    *kicad.NetclassFull
	ri      kicadRulesInputs
	bases   []rulesNetBasis
	block   []rulesBlockRule
	druRule []kicad.DRURule // staged result's custom rules
	// rbStaged: pcbnew's readback of the verified stage.
	rbStaged  *kicad.RulesReadback
	probeRuns int
}

func runKicadRules(o kicadRulesOpts, stdout, stderr io.Writer) error {
	usage := func(f string, a ...any) error {
		fmt.Fprintf(stderr, "kicad rules: "+f+"\n", a...)
		return exitCodeError{1}
	}
	modes := 0
	for _, m := range []bool{o.plan, o.apply, o.check, o.applyPlan != ""} {
		if m {
			modes++
		}
	}
	switch {
	case modes != 1:
		return usage("give exactly one of --plan, --apply, --apply-plan and --check")
	case o.pcb == "" || o.intent == "":
		return usage("--pcb and --intent are required")
	case !strings.HasSuffix(o.pcb, ".kicad_pcb"):
		return usage("--pcb %s is not a .kicad_pcb", o.pcb)
	case o.planOut != "" && !o.plan:
		return usage("--plan-out goes with --plan")
	case o.applyPlan != "" && o.revision == "":
		return usage("--apply-plan needs --revision (printed by --plan --plan-out)")
	case o.revision != "" && o.applyPlan == "":
		return usage("--revision goes with --apply-plan")
	case o.stage != "routed" && o.stage != "placed":
		return usage("--stage must be routed or placed")
	case o.stage != "routed" && !o.check:
		return usage("--stage goes with --check")
	}
	if !fileExists(o.pcb) {
		return usage("board %s not found", o.pcb)
	}
	if why := rulesOutputAlias(o); why != "" {
		return usage("%s", why)
	}
	reportSnap, why := rulesOwnOutput("--report", o.report)
	if why != "" {
		return usage("%s", why)
	}
	planSnap, why := rulesOwnOutput("--plan-out", o.planOut)
	if why != "" {
		return usage("%s", why)
	}
	mode := map[bool]string{true: "plan"}[o.plan]
	switch {
	case o.apply:
		mode = "apply"
	case o.applyPlan != "":
		mode = "apply-plan"
	case o.check:
		mode = "check"
	}
	rep := &rulesReport{Schema: rulesSchema, Mode: mode, PCB: o.pcb, Inputs: []rulesInput{}, Nets: []rulesNetPlan{}, Classes: []kicad.NetClass{},
		Isolation: []rulesIsoPlan{}, DRURules: []rulesBlockRule{}, Conflicts: []rulesConflict{}, Checks: []rulesCheck{}, Files: []rulesFile{}, Errors: []rulesError{}}
	r := &rulesRun{o: o, rep: rep, stderr: stderr, data: map[string][]byte{},
		pro: boardSibling(o.pcb, ".kicad_pro"), dru: boardSibling(o.pcb, ".kicad_dru")}
	code := r.run(planSnap)
	return r.finish(code, reportSnap, stdout)
}

// finish sets the verdict, writes the report (atomically, only over its own
// earlier report) and prints it.
func (r *rulesRun) finish(code int, reportSnap kfile.Snapshot, stdout io.Writer) error {
	rep := r.rep
	if rep.Verdict == "" {
		rep.Verdict = gate.Aggregate(rep.results()...)
		if code == 0 {
			code = rep.Verdict.ExitCode()
		}
	}
	rep.OK = code == 0
	b, _ := json.MarshalIndent(rep, "", "  ")
	b = append(b, '\n')
	if r.o.report != "" {
		_ = os.MkdirAll(filepath.Dir(r.o.report), 0o755)
		if _, err := kfile.WriteFile(r.o.report, b, kfile.Opts{Env: r.o.env, Base: &reportSnap, NoLockCheck: true}); err != nil {
			fmt.Fprintf(r.stderr, "kicad rules: cannot write the report %s: %v\n", r.o.report, err)
			if code == 0 {
				if code = kfile.ExitCode(err); code == 0 {
					code = 1
				}
			}
			rep.OK = false
			b, _ = json.MarshalIndent(rep, "", "  ")
			b = append(b, '\n')
		}
	}
	_, _ = stdout.Write(b)
	if r.stage != "" {
		_ = os.RemoveAll(r.stage)
	}
	if code == 0 {
		return nil
	}
	return exitCodeError{code}
}

// fail records an error and returns its exit code; the verdict follows the
// exit (4 refused, 5 cannot-check, 6 fail, else error → fail).
func (r *rulesRun) fail(code, path, detail, fix string, exit int) int {
	r.rep.Errors = append(r.rep.Errors, rulesError{Code: code, Path: path, Detail: detail, Fix: fix})
	switch exit {
	case 5:
		r.rep.Verdict = gate.CannotCheck
	default:
		r.rep.Verdict = gate.Fail
	}
	fmt.Fprintf(r.stderr, "kicad rules: %s %s: %s\n", code, path, detail)
	return exit
}

// fileErr maps a kfile / IO error to the report (KiCad locks are KICAD_OPEN).
func (r *rulesRun) fileErr(err error) int {
	var ke *kfile.Error
	if errors.As(err, &ke) {
		code := string(ke.Code)
		if ke.Code == kfile.CodeLocked {
			code = errRulesKiCadOpen
		}
		return r.fail(code, ke.Path, ke.Detail, ke.Fix, kfile.ExitCode(err))
	}
	return r.fail("E_IO", "", err.Error(), "", 1)
}

func (r *rulesRun) run(planSnap kfile.Snapshot) int {
	o, rep := r.o, r.rep
	write := o.apply || o.applyPlan != ""
	// Tools.
	r.t = o.backend
	if r.t == nil {
		kt, err := kicad.Locate()
		if err != nil {
			return r.fail(errRulesTool, "", err.Error(), "install KiCad 10 or set "+kicad.EnvApp+" / "+kicad.EnvPython+" / "+kicad.EnvCLI, 1)
		}
		r.t = kt
	}
	if v, err := r.t.Version(); err == nil {
		rep.KiCadVersion = v
	}
	// KiCad locks: a write refuses at once when KiCad has the project open;
	// a read-only run only warns (unsaved GUI edits are not in the files).
	targets := []string{o.pcb, r.pro, r.dru}
	if write {
		if _, err := kfile.Guard(targets, kfile.BreakNone, o.env); err != nil {
			return r.fileErr(err)
		}
	} else if locks, err := kfile.CheckLocks(targets, o.env); err == nil && len(locks) > 0 {
		rep.Warnings = append(rep.Warnings, "sourceOpenInKiCad: the project is open in KiCad; edits not saved there are not in the files")
	}
	// Inputs, read once and hashed.
	if code := r.readInputs(); code != 0 {
		return code
	}
	stage, err := os.MkdirTemp("", "pcbpilot-rules-")
	if err != nil {
		return r.fileErr(err)
	}
	r.stage = stage
	if code := r.derive(); code != 0 {
		return code
	}
	rep.Revision = r.revision()
	r.writeStamp()
	// Staged verification: pcbnew readback + DRC probes on the staged result.
	r.verifyStaged()

	switch {
	case o.plan:
		if o.planOut != "" {
			pf := rulesPlanFile{Schema: rulesPlanSchema, Revision: rep.Revision, PCB: o.pcb, Inputs: rep.Inputs, Report: rep}
			b, _ := json.MarshalIndent(pf, "", "  ")
			if _, err := kfile.WriteFile(o.planOut, append(b, '\n'), kfile.Opts{Env: o.env, Base: &planSnap, NoLockCheck: true}); err != nil {
				return r.fileErr(err)
			}
		}
		return r.inputsUnchanged()
	case o.check:
		r.checkBoard()
		return r.inputsUnchanged()
	}
	// apply / apply-plan.
	if o.applyPlan != "" {
		b, err := os.ReadFile(o.applyPlan)
		if err != nil {
			return r.fail(errRulesInput, o.applyPlan, err.Error(), "", 1)
		}
		var pf rulesPlanFile
		if err := json.Unmarshal(b, &pf); err != nil || pf.Schema != rulesPlanSchema {
			return r.fail(errRulesInput, o.applyPlan, "not a kicad rules plan (schema "+rulesPlanSchema+")", "make one with --plan --plan-out", 1)
		}
		if pf.Revision != o.revision {
			return r.fail(string(kfile.CodeRevision), o.applyPlan, fmt.Sprintf("--revision %s is not the plan's revision %s", short12(o.revision), short12(pf.Revision)), "pass the revision --plan printed", 4)
		}
		if pf.Revision != rep.Revision {
			return r.fail(string(kfile.CodeRevision), o.pcb, fmt.Sprintf("the plan has revision %s, but the files and options now give %s", short12(pf.Revision), short12(rep.Revision)),
				"the inputs changed since the plan was made; make and review a new plan", 4)
		}
	}
	if code := r.refuseIncomplete(); code != 0 {
		return code
	}
	return r.publish()
}

// readInputs reads every input once (bytes kept for the stage copy).
func (r *rulesRun) readInputs() int {
	o := r.o
	add := func(role, path string, required bool) int {
		if path == "" {
			return 0
		}
		b, sn, err := kfile.Read(path)
		if errors.Is(err, os.ErrNotExist) && !required {
			sn = kfile.Snapshot{Path: path}
			err = nil
		}
		if err != nil {
			return r.fail(errRulesInput, path, err.Error(), "", 1)
		}
		r.snaps = append(r.snaps, sn)
		r.data[path] = b
		r.rep.Inputs = append(r.rep.Inputs, rulesInput{Role: role, Path: path, Exists: sn.Exists, SHA256: sn.SHA256})
		return 0
	}
	for _, in := range []struct {
		role, path string
		req        bool
	}{{"pcb", o.pcb, true}, {"project", r.pro, false}, {"rules", r.dru, false}, {"intent", o.intent, true}, {"sim", o.sim, true}} {
		if c := add(in.role, in.path, in.req); c != 0 {
			return c
		}
	}
	for _, p := range o.requirements {
		if c := add("requirements", p, true); c != 0 {
			return c
		}
	}
	return 0
}

// inputsUnchanged: the answer speaks for the inputs only if none changed
// while the run read them (E_FILE_CHANGED, exit 4).
func (r *rulesRun) inputsUnchanged() int {
	for _, sn := range r.snaps {
		if err := sn.Check(); err != nil {
			return r.fileErr(err)
		}
	}
	return 0
}

// derive stages the input, reads it with KiCad and computes the plan on
// the stage copy.
func (r *rulesRun) derive() int {
	o, rep := r.o, r.rep
	in, err := parseDesignIntent(r.data[o.intent])
	if err != nil {
		return r.fail(errRulesInput, o.intent, err.Error(), "", 1)
	}
	var sp *pcbauto.SimPower
	if o.sim != "" {
		if sp, err = pcbauto.ParseSim(r.data[o.sim]); err != nil {
			return r.fail(errRulesInput, o.sim, err.Error(), "give a sim power result (schemaVersion 1)", 1)
		}
	}
	decl, notes, err := loadDeclaredCurrents(o.requirements)
	if err != nil {
		return r.fail(errRulesInput, "", err.Error(), "", 1)
	}
	rep.Assumptions = append(rep.Assumptions, notes...)
	stack, err := kicad.ReadStackupCopper(r.data[o.pcb])
	if err != nil {
		return r.fail(errRulesInput, o.pcb, err.Error(), "", 1)
	}
	// Stage copy of exactly the bytes read.
	inDir := filepath.Join(r.stage, "in")
	if err := os.MkdirAll(inDir, 0o755); err != nil {
		return r.fileErr(err)
	}
	r.stIn = filepath.Join(inDir, filepath.Base(o.pcb))
	for _, f := range []string{o.pcb, r.pro, r.dru} {
		if b, ok := r.data[f]; ok && b != nil {
			if err := os.WriteFile(boardSibling(r.stIn, filepath.Ext(f)), b, 0o644); err != nil {
				return r.fileErr(err)
			}
		}
	}
	// --replace: the named user .kicad_dru rules leave the stage copy.
	druText := string(r.data[r.dru])
	if len(o.replace) > 0 && druText != "" {
		nt, removed, err := removeUserDRURules(druText, o.replace)
		if err != nil {
			return r.fail(errRulesInput, r.dru, err.Error(), "", 1)
		}
		if len(removed) > 0 {
			if err := os.WriteFile(boardSibling(r.stIn, ".kicad_dru"), []byte(nt), 0o644); err != nil {
				return r.fileErr(err)
			}
		}
	}
	if r.snapRaw, err = r.t.Snapshot(r.stIn); err != nil {
		return r.fail(errRulesTool, o.pcb, "board snapshot: "+err.Error(), "", 1)
	}
	if r.snap, err = decodeRulesSnap(r.snapRaw); err != nil {
		return r.fail(errRulesTool, o.pcb, err.Error(), "", 1)
	}
	if r.rb0, err = r.t.RulesRead(r.stIn); err != nil {
		return r.fail(errRulesTool, o.pcb, "rules readback: "+err.Error(), "", 1)
	}
	rep.Minimums = &r.rb0.Minimums
	names := make([]string, 0, len(r.rb0.Nets))
	for n := range r.rb0.Nets {
		names = append(names, n)
	}
	sort.Strings(names)
	ob, renamed, err := in.onBoard(names)
	if err != nil {
		return r.fail(errRulesInput, o.intent, err.Error(), "", 1)
	}
	if len(renamed) > 0 {
		rep.Warnings = append(rep.Warnings, "intent nets resolved to the board's net names: "+strings.Join(renamed, ", "))
	}
	r.in = ob
	cu := copperBasis(stack, ob)
	rep.Copper = &cu
	rep.Assumptions = append(rep.Assumptions, cu.Assumptions...)
	widths, bases := planRulesWidths(ob, sp, decl, cu)
	r.bases = bases
	r.ri = deriveKicadRules(ob, o.intent, widths)
	rep.RequestSHA = r.ri.sha()
	st, _ := parseRulesStamp(string(r.data[r.dru]))
	out := filepath.Join(r.stage, "out", filepath.Base(o.pcb))
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return r.fileErr(err)
	}
	full, err := applyKicadNetclasses(r.t, r.stIn, r.ri, &st.Owned, out)
	if err != nil {
		if full == nil {
			return r.fail(errRulesTool, o.pcb, err.Error(), "", 1)
		}
		rep.check(rulesCheck{Name: "staged-readback", Status: gate.Fail, Detail: err.Error(), Items: full.Mismatched})
	}
	r.full, r.staged = full, out
	rep.Classes, rep.Sizes, rep.DRURouteRule = full.Classes, full.Sizes, full.DRURules
	for _, m := range full.MissingNets {
		rep.Warnings = append(rep.Warnings, "intent net not on the board (no rule): "+m)
	}
	for _, m := range full.MissingDomainNets {
		rep.Warnings = append(rep.Warnings, "insulation / edge domain net not on the board (no rule covers it): "+m)
	}
	rep.Warnings = append(rep.Warnings, full.Kept...)
	for _, f := range full.ViaFloored {
		rep.Assumptions = append(rep.Assumptions, "board minimum floor: "+f)
	}
	r.block = rulesBlockRules(full.ClassesNM, r.rb0.CopperLayers)
	rep.DRURules = r.block
	r.planNets()
	r.planIsolation()
	r.conflicts()
	if !o.check {
		uc := rulesCheck{Name: "user-rules", Status: gate.Pass, Detail: "no user netclass or .kicad_dru rule conflicts with the plan"}
		for _, c := range rep.Conflicts {
			if c.Replaced {
				uc.Info = append(uc.Info, c.Name+" (replaced by --replace): "+c.Detail)
				continue
			}
			uc.Status = gate.Fail
			uc.Items = append(uc.Items, c.Kind+" "+c.Name+": "+c.Detail)
		}
		if uc.Status == gate.Fail {
			uc.Detail = fmt.Sprintf("%d user rule(s) conflict with the plan: apply refuses unless --replace names them", len(uc.Items))
		}
		rep.check(uc)
	}
	return 0
}

// revision identifies the plan: every input's hash, the derivation, the
// classes and rules it writes, the options that change the result and the
// KiCad version.
func (r *rulesRun) revision() string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00kicad %s\x00", rulesPlanSchema, r.rep.KiCadVersion)
	for _, in := range r.rep.Inputs {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", in.Role, filepath.ToSlash(in.Path), in.SHA256)
	}
	rep := sortedStrings(r.o.replace)
	b, _ := json.Marshal(map[string]any{"request": r.ri.request(nil).Body(), "classes": r.full.ClassesNM, "sizes": r.full.Sizes,
		"block": r.block, "replace": rep, "nets": r.rep.Nets, "conflicts": r.rep.Conflicts})
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func sortedStrings(v []string) []string {
	out := append([]string(nil), v...)
	sort.Strings(out)
	return out
}

// writeStamp puts the rules block into the staged .kicad_dru.
func (r *rulesRun) writeStamp() {
	owned := kicad.SizeSet{}
	if r.full.Sizes != nil {
		owned = r.full.Sizes.Owned
	}
	dru := boardSibling(r.staged, ".kicad_dru")
	old, _ := os.ReadFile(dru)
	text, err := placeRulesBlock(string(old), rulesBlockText(r.block, r.rep.RequestSHA, owned))
	if err == nil {
		err = os.WriteFile(dru, []byte(text), 0o644)
	}
	if err == nil {
		r.druRule, err = kicad.ParseDRU(text)
	}
	if err != nil {
		r.rep.check(rulesCheck{Name: "staged-readback", Status: gate.Fail, Detail: "staged .kicad_dru: " + err.Error()})
	}
}

// refuseIncomplete: no partial writes — an incomplete or failing plan, an
// unresolved conflict or a staged result KiCad does not confirm writes nothing.
func (r *rulesRun) refuseIncomplete() int {
	var unresolved []string
	for _, c := range r.rep.Conflicts {
		if !c.Replaced {
			unresolved = append(unresolved, c.Name+": "+c.Detail)
		}
	}
	if len(unresolved) > 0 {
		return r.fail(errRulesConflict, r.o.pcb, strings.Join(unresolved, "; "),
			"keep the user rule (change the intent instead), or let pcbpilot replace it with --replace <name>", 6)
	}
	for _, name := range []string{"plan-coverage", "staged-readback"} {
		switch st := r.rep.checkStatus(name); st {
		case gate.Pass:
		case gate.Fail:
			return r.fail(map[string]string{"plan-coverage": errRulesIncomplete, "staged-readback": errRulesVerify}[name], r.o.pcb,
				name+" failed: nothing written (see checks)", "fix the cause named in the check and run again", 6)
		default:
			return r.fail(errRulesIncomplete, r.o.pcb, fmt.Sprintf("%s is %s: the plan is incomplete, nothing written", name, st),
				"give the missing evidence (--sim, declared currents) or fix the KiCad tools, then run again", 5)
		}
	}
	return 0
}

// publish commits the staged .kicad_pro / .kicad_dru with one transaction
// under the project writer lock, held through the readback and a rollback.
func (r *rulesRun) publish() int {
	o, rep := r.o, r.rep
	root := filepath.Dir(mustAbs(o.pcb))
	w, err := kfile.AcquireWriter(root, o.env)
	if err != nil {
		return r.fileErr(err)
	}
	defer w.Release()
	if o.beforeCommit != nil {
		o.beforeCommit()
	}
	if c := r.inputsUnchanged(); c != 0 {
		return c
	}
	snapOf := func(p string) *kfile.Snapshot {
		for i := range r.snaps {
			if r.snaps[i].Path == p {
				return &r.snaps[i]
			}
		}
		return nil
	}
	txn := kfile.NewTxn(root)
	stagedFiles := map[string]string{r.pro: boardSibling(r.staged, ".kicad_pro"), r.dru: boardSibling(r.staged, ".kicad_dru")}
	for _, target := range []string{r.pro, r.dru} {
		src := stagedFiles[target]
		nb, err := os.ReadFile(src)
		if err != nil {
			txn.Abort()
			return r.fail(errRulesVerify, src, "the staged file is missing: "+err.Error(), "", 1)
		}
		if old := r.data[target]; old != nil && string(old) == string(nb) {
			continue
		}
		if err := txn.Stage(target, nb, snapOf(target)); err != nil {
			txn.Abort()
			return r.fileErr(err)
		}
	}
	p, err := txn.Apply("", kfile.Opts{Env: o.env, Writer: w})
	for _, c := range p.Changes {
		rep.Files = append(rep.Files, rulesFile{Path: c.Path, Kind: string(c.Kind), BaseSHA: c.Base.SHA256, NewSHA: c.NewSHA})
	}
	if err != nil {
		txn.Abort()
		return r.fileErr(err)
	}
	rep.Committed = p.Applied
	if len(p.Changes) == 0 {
		rep.Warnings = append(rep.Warnings, "the project already carries exactly these rules: nothing written")
	}
	if o.beforeReadback != nil {
		o.beforeReadback()
	}
	// Readback with KiCad on the published files.
	chk := r.readbackPublished()
	rep.check(chk)
	if chk.Status == gate.Pass {
		return 0
	}
	// Roll back what this run wrote (never over an intervening edit: the
	// base of each restore is what this run wrote).
	rb := kfile.NewTxn(root)
	for _, c := range p.Changes {
		now := kfile.Snapshot{Path: c.Path, Exists: true, SHA256: c.NewSHA}
		if st, err := kfile.Stat(c.Path); err == nil {
			now.Size = st.Size
		}
		var err error
		if c.Base.Exists {
			err = rb.Stage(c.Path, r.data[r.inputPathFor(c.Path)], &now)
		} else {
			err = rb.Delete(c.Path, &now)
		}
		if err != nil {
			rb.Abort()
			return r.fail(errRulesRollback, c.Path, "readback failed and the rollback could not be staged: "+err.Error(), "restore the file from version control", 1)
		}
	}
	if _, err := rb.Apply("", kfile.Opts{Env: o.env, Writer: w}); err != nil {
		rb.Abort()
		return r.fail(errRulesRollback, root, "readback failed and the rollback failed: "+err.Error(), "restore .kicad_pro / .kicad_dru from version control", 1)
	}
	rep.RolledBack, rep.Committed = true, false
	return r.fail(errRulesReadback, o.pcb, "KiCad's readback of the written project does not match the plan; the files were restored", "see the readback check", 6)
}

// inputPathFor maps a committed absolute path back to the input path key.
func (r *rulesRun) inputPathFor(abs string) string {
	for p := range r.data {
		if sameFilePath(p, abs) {
			return p
		}
	}
	return abs
}

// rulesOutputAlias explains why --report / --plan-out is unsafe: a KiCad
// file extension, not .json, or the same file as an input or the other
// output. "" = safe.
func rulesOutputAlias(o kicadRulesOpts) string {
	type named struct{ what, path string }
	var in []named
	for _, ext := range []string{".kicad_pcb", ".kicad_pro", ".kicad_dru", ".kicad_prl"} {
		in = append(in, named{"board " + ext, boardSibling(o.pcb, ext)}, named{"KiCad lock", kfile.LockPath(boardSibling(o.pcb, ext))})
	}
	for _, f := range []named{{"--intent", o.intent}, {"--sim", o.sim}, {"--apply-plan", o.applyPlan}} {
		if f.path != "" {
			in = append(in, f)
		}
	}
	for _, p := range o.requirements {
		in = append(in, named{"--requirements", p})
	}
	var outs []named
	for _, f := range []named{{"--report", o.report}, {"--plan-out", o.planOut}} {
		if f.path != "" {
			outs = append(outs, f)
		}
	}
	for i, out := range outs {
		low := strings.ToLower(boardNormPath(out.path))
		for _, ext := range []string{".kicad_pcb", ".kicad_pro", ".kicad_dru", ".kicad_prl", ".kicad_sch", ".lck"} {
			if strings.HasSuffix(low, ext) {
				return fmt.Sprintf("%s %s has a KiCad file extension; write it elsewhere", out.what, out.path)
			}
		}
		if !strings.HasSuffix(low, ".json") {
			return fmt.Sprintf("%s %s must end in .json", out.what, out.path)
		}
		for _, f := range in {
			if boardSamePath(out.path, f.path) {
				return fmt.Sprintf("%s %s is the %s (%s): kicad rules never writes over an input", out.what, out.path, f.what, f.path)
			}
		}
		for _, other := range outs[:i] {
			if boardSamePath(out.path, other.path) {
				return fmt.Sprintf("%s and %s are the same file %s", other.what, out.what, out.path)
			}
		}
	}
	return ""
}

// rulesOwnOutput: an output path is absent or a kicad rules report / plan.
func rulesOwnOutput(what, path string) (kfile.Snapshot, string) {
	if path == "" {
		return kfile.Snapshot{}, ""
	}
	b, sn, err := kfile.Read(path)
	if errors.Is(err, os.ErrNotExist) {
		return kfile.Snapshot{Path: path}, ""
	}
	if err != nil {
		return sn, fmt.Sprintf("%s %s: %v", what, path, err)
	}
	var head struct {
		Schema string `json:"schema"`
	}
	if json.Unmarshal(b, &head) != nil || (head.Schema != rulesSchema && head.Schema != rulesPlanSchema) {
		return sn, fmt.Sprintf("%s %s exists and is not a kicad rules report or plan: kicad rules only replaces its own files", what, path)
	}
	return sn, ""
}
