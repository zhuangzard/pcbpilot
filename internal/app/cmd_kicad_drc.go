package app

// cmd_kicad_drc.go — `pcbpilot kicad drc`: KiCad's DRC as a gate. kicad-cli
// runs on a private copy of the board (kicad.RunDRC: the user's project is
// never touched, the KiCad configuration is the isolated one), the report is
// read strictly (kicad.ParseDRCWith), every violation is attributed to the
// board objects it names (kicad.BoardIndex), the project's exclusions and
// severities are judged by the shared policy (kicad.EvalDRCPolicy, the same
// one the kicad route kicad-drc gate uses), and against a baseline only the
// new violations count.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

const kicadDRCReportKind = "pcbpilot-kicad-drc"

// drcCheck is one check of a tri-state report: the gate verdict plus the
// items that decide it.
type drcCheck struct {
	gate.Result
	Items  []string `json:"items,omitempty"`  // failing, not waived
	Waived []string `json:"waived,omitempty"` // accepted by a signed waiver
	Info   []string `json:"info,omitempty"`
}

func cannotCheck(name, detail string) drcCheck {
	return drcCheck{Result: gate.Result{Name: name, Status: gate.CannotCheck, Detail: detail}}
}

// judged sets the status from the items left after waivers.
func (c *drcCheck) judged(items []string, ws []gateWaiver) {
	c.Items, c.Waived = waiveItems(c.Name, items, ws)
	c.Status = gate.FromBool(len(c.Items) == 0)
	if len(c.Waived) > 0 {
		c.Detail += fmt.Sprintf("; %d accepted by signed waiver(s)", len(c.Waived))
	}
}

// waiveItems splits items into those no waiver matches and the waived ones.
// A waiver applies to the check of its gate name; "kicad-drc" waivers also
// cover the policy and parity checks (the kicad route waiver files).
func waiveItems(check string, items []string, ws []gateWaiver) (left, waived []string) {
	for _, it := range items {
		hit := ""
		for _, w := range ws {
			if (w.Gate == check || w.Gate == "kicad-drc") && w.Match != "" && strings.Contains(it, w.Match) {
				hit = fmt.Sprintf("%s — waived: %s (%s)", it, w.Reason, w.By)
				break
			}
		}
		if hit == "" {
			left = append(left, it)
		} else {
			waived = append(waived, hit)
		}
	}
	return left, waived
}

func checkResults(cs []drcCheck) []gate.Result {
	out := make([]gate.Result, len(cs))
	for i, c := range cs {
		out[i] = c.Result
	}
	return out
}

type kicadDRCPolicyReport struct {
	*kicad.DRCPolicy
	*kicad.DRCPolicyResult
	Error string `json:"error,omitempty"`
}

type kicadDRCBaseline struct {
	Source    string `json:"source"`
	Kind      string `json:"kind"` // pcb | report | kicad-json
	PCBSha256 string `json:"pcbSha256,omitempty"`
	kicad.DRCDiff
	// NewPolicy: policy items not in the baseline's (nil when the
	// baseline does not say, a raw kicad-cli report).
	NewPolicy []string `json:"newPolicy,omitempty"`
	Notes     []string `json:"notes,omitempty"`
}

// kicadDRCReport is `kicad drc`'s JSON report (and a valid --baseline).
type kicadDRCReport struct {
	Kind            string                 `json:"kind"`
	Version         int                    `json:"version"`
	Verdict         gate.Status            `json:"verdict"`
	ExitCode        int                    `json:"exitCode"`
	Checks          []drcCheck             `json:"checks"`
	PCB             string                 `json:"pcb"`
	PCBSha256       string                 `json:"pcbSha256,omitempty"`
	KiCadVersion    string                 `json:"kicadVersion,omitempty"`
	SchematicParity bool                   `json:"schematicParity"`
	Schematic       string                 `json:"schematic,omitempty"` // --sch: the root compared with
	Errors          int                    `json:"errors"`
	Counts          map[string]int         `json:"counts"`
	Warnings        int                    `json:"warnings"`
	WarningCounts   map[string]int         `json:"warningCounts"`
	OwnerClasses    map[string]int         `json:"ownerClasses"`
	Violations      []kicad.OwnedViolation `json:"violations"`
	Excluded        []kicad.OwnedViolation `json:"excluded"`
	Policy          *kicadDRCPolicyReport  `json:"policy,omitempty"`
	Baseline        *kicadDRCBaseline      `json:"baseline,omitempty"`
	Run             *kicad.DRCRun          `json:"run,omitempty"`
	Notes           []string               `json:"notes,omitempty"`
	Files           map[string]string      `json:"files,omitempty"`
}

type kicadDRCOptions struct {
	pcb, cli                  string
	parity                    bool
	sch                       string // root schematic for parity ("" = <board>.kicad_sch beside the board)
	baselineJSON, baselinePCB string
	waivers                   []gateWaiver
	tolMm                     float64
	outDir                    string // raw reports are kept here when set
}

func newKicadDRCCmd(stdout, stderr io.Writer) *cobra.Command {
	var o kicadDRCOptions
	var waiverPath string
	c := &cobra.Command{
		Use:   "drc",
		Short: "KiCad DRC as a tri-state gate: owners per violation, project exclusions policy, new-vs-baseline (JSON, exit 0/6/5)",
		Long: `Run kicad-cli pcb drc (JSON, all severities) on a temporary copy of --pcb and its
.kicad_pro / .kicad_dru (with --schematic-parity also the project's root schematic
<board>.kicad_sch and its sheets; --sch ROOT.kicad_sch names another root, for a
board written outside its project, e.g. kicad place --out, and implies parity), with pcbpilot's isolated KiCad configuration (empty
global library tables; the project's own fp-lib-table / sym-lib-table still resolve).
The user's files are never written, and nothing is left in the project directory.

Every violation names its owners: the board object of each item uuid (a pad, silk
line or courtyard belongs to its footprint) and a class — environment (library
tables), schematic-parity, board-outline (an Edge.Cuts object), routing (track / via /
zone), footprint (one footprint's own objects), placement (two or more footprints),
graphic, unresolved.

Checks (each pass / fail / cannot-check):
  kicad-drc          error-severity items of violations + unconnected_items, and
                     warnings of the electrical / fabrication rules (hole_to_hole,
                     connection_width, copper_sliver, a downgraded clearance …)
  kicad-drc-policy   .kicad_pro exclusions / ignore / error→warning of an electrical or
                     fabrication rule (shorts, clearances, unconnected, parity, sizes,
                     outline …) fail; placement-class rules are listed as silenced
  schematic-parity   (--schematic-parity) every parity item, any severity
Other warnings are listed, not gated. A signed waiver ({"gate":"kicad-drc","match":…,
"reason":…,"by":…}, --waivers) accepts each item whose line contains match.

With --baseline (a previous report of this command, or a raw kicad-cli DRC JSON) or
--baseline-pcb (DRC is run on that board's copy too), only NEW items count: a
violation matches a baseline one by rule, owners and the same offending objects (item
uuids), each within --tolerance-mm of its baseline position (without uuids: every item
in order within it); a warning that became an error is new. "resolved" lists the
baseline ones that disappeared. A raw kicad-cli baseline is
attributed with the current board (deleted objects stay unresolved) and carries no
project policy, so the policy check is judged in full.

Exit: 0 pass, 6 fail, 5 cannot-check (kicad-cli failed, unreadable report or baseline).
The JSON report goes to stdout; --out DIR also keeps drc-report.json and the raw
kicad-cli report(s).`,
		Args: cobra.NoArgs,
		Example: `  pcbpilot kicad drc --pcb board.kicad_pcb --schematic-parity
  pcbpilot kicad drc --pcb board.kicad_pcb --baseline-pcb ../rev-a/board.kicad_pcb --out drc/
  pcbpilot kicad drc --pcb board.kicad_pcb --baseline drc/drc-report.json --waivers waivers.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.pcb == "" {
				return fmt.Errorf("--pcb is required")
			}
			if o.baselineJSON != "" && o.baselinePCB != "" {
				return fmt.Errorf("--baseline and --baseline-pcb are exclusive")
			}
			if o.sch != "" {
				if !strings.HasSuffix(strings.ToLower(o.sch), ".kicad_sch") {
					return fmt.Errorf("--sch %s is not a .kicad_sch", o.sch)
				}
				o.parity = true // --sch names what to compare with: parity is implied
			}
			if o.tolMm <= 0 {
				return fmt.Errorf("--tolerance-mm must be positive")
			}
			ws, err := loadWaivers(waiverPath)
			if err != nil {
				return err
			}
			o.waivers = ws
			if o.cli, err = kicad.ResolveFabCLI(); err != nil {
				return err
			}
			if o.outDir != "" {
				if err := os.MkdirAll(o.outDir, 0o755); err != nil {
					return err
				}
			}
			rep := runKicadDRC(o)
			if o.outDir != "" {
				p := filepath.Join(o.outDir, "drc-report.json")
				rep.Files["report"] = p
				if err := writeJSONFile(p, rep); err != nil {
					return err
				}
			}
			if err := writeJSON(stdout, rep); err != nil {
				return err
			}
			printDRCSummary(stderr, "kicad drc", rep.Verdict, rep.Checks, rep.Baseline)
			if code := rep.Verdict.ExitCode(); code != 0 {
				return exitCodeError{code}
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&o.pcb, "pcb", "", "board file (.kicad_pcb, required)")
	f.BoolVar(&o.parity, "schematic-parity", false, "also compare with the project's schematic (<board>.kicad_sch next to the board, or --sch)")
	f.StringVar(&o.sch, "sch", "", "root schematic to compare with (implies --schematic-parity): for a board written outside its project directory")
	f.StringVar(&o.baselineJSON, "baseline", "", "report only violations not in this report (a `kicad drc` report or a kicad-cli DRC JSON)")
	f.StringVar(&o.baselinePCB, "baseline-pcb", "", "report only violations not on this older board (DRC runs on its copy too)")
	f.StringVar(&waiverPath, "waivers", "", "signed waiver file (JSON list of {gate, match, reason, by})")
	f.Float64Var(&o.tolMm, "tolerance-mm", 0.5, "position tolerance for matching a baseline violation (mm)")
	f.StringVar(&o.outDir, "out", "", "also write drc-report.json and the raw kicad-cli report(s) here")
	return c
}

// printDRCSummary writes one line per check to w.
func printDRCSummary(w io.Writer, what string, v gate.Status, cs []drcCheck, b *kicadDRCBaseline) {
	fmt.Fprintf(w, "%s: %s\n", what, strings.ToUpper(string(v)))
	for _, c := range cs {
		fmt.Fprintf(w, "  %-18s %-12s %s\n", c.Name, c.Status, c.Detail)
	}
	if b != nil {
		fmt.Fprintf(w, "  baseline %s: %d new, %d resolved, %d matched\n", b.Source, len(b.New), len(b.Resolved), b.Matched)
	}
}

// runKicadDRC runs the checks; a failure to run is a cannot-check verdict,
// never an error.
func runKicadDRC(o kicadDRCOptions) *kicadDRCReport {
	rep := &kicadDRCReport{Kind: kicadDRCReportKind, Version: 1, PCB: o.pcb, SchematicParity: o.parity, Schematic: o.sch,
		Counts: map[string]int{}, WarningCounts: map[string]int{}, OwnerClasses: map[string]int{},
		Violations: []kicad.OwnedViolation{}, Excluded: []kicad.OwnedViolation{}, Files: map[string]string{}}
	// The raw report paths are fixed before the runs start: the two
	// goroutines below only read them.
	curRaw, oldRaw := "", ""
	if o.outDir != "" {
		curRaw = filepath.Join(o.outDir, "drc.json")
		rep.Files["drc"] = curRaw
		if o.baselinePCB != "" {
			oldRaw = filepath.Join(o.outDir, "baseline-drc.json")
			rep.Files["baseline-drc"] = oldRaw
		}
	}
	var cur, old *kicad.DRCRun
	var curErr, oldErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		cur, curErr = kicad.RunDRC(kicad.DRCRunOptions{PCB: o.pcb, SchematicParity: o.parity, Schematic: o.sch, CLI: o.cli, SaveRaw: curRaw})
	}()
	if o.baselinePCB != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			old, oldErr = kicad.RunDRC(kicad.DRCRunOptions{PCB: o.baselinePCB, SchematicParity: o.parity, Schematic: o.sch, CLI: o.cli, SaveRaw: oldRaw})
		}()
	}
	wg.Wait()
	judgeKicadDRC(rep, o, cur, curErr, old, oldErr)
	return rep
}

// judgeKicadDRC fills rep from finished runs (old: the --baseline-pcb run).
func judgeKicadDRC(rep *kicadDRCReport, o kicadDRCOptions, cur *kicad.DRCRun, curErr error, old *kicad.DRCRun, oldErr error) {
	defer func() {
		rep.Verdict = gate.Aggregate(checkResults(rep.Checks)...)
		rep.ExitCode = rep.Verdict.ExitCode()
	}()
	if curErr != nil {
		rep.Checks = append(rep.Checks, cannotCheck("kicad-drc", "cannot check: "+curErr.Error()))
		return
	}
	rep.Run, rep.PCB, rep.PCBSha256 = cur, cur.PCB, cur.PCBSha256
	rep.KiCadVersion = cur.Report.KiCadVersion
	rep.Notes = append(rep.Notes, cur.Notes...)
	all := cur.Index.Own(cur.Report.Violations)
	rep.Violations, rep.Excluded = all, cur.Index.Own(cur.Report.ExcludedItems)
	for _, v := range all {
		rep.OwnerClasses[v.Owner.Class]++
		if v.Severity == "" || v.Severity == "error" {
			rep.Errors++
			rep.Counts[v.Rule]++
		} else {
			rep.Warnings++
			rep.WarningCounts[v.Rule]++
		}
	}
	pol := &kicadDRCPolicyReport{DRCPolicy: cur.Policy}
	if cur.PolicyErr != nil {
		pol.Error = cur.PolicyErr.Error()
	} else {
		pol.DRCPolicyResult = kicad.EvalDRCPolicy(cur.Policy, cur.Report.ExcludedItems)
	}
	rep.Policy = pol

	// Baseline: what counts is what is not in it.
	judged := all
	var basePolicy []string
	baseKnown := false
	if o.baselinePCB != "" || o.baselineJSON != "" {
		b := &kicadDRCBaseline{}
		var base []kicad.OwnedViolation
		var err error
		if o.baselinePCB != "" {
			b.Source, b.Kind = o.baselinePCB, "pcb"
			if err = oldErr; err == nil {
				b.PCBSha256 = old.PCBSha256
				base = old.Index.Own(old.Report.Violations)
				if old.PolicyErr != nil {
					err = fmt.Errorf("baseline project policy: %w", old.PolicyErr)
				} else if r := kicad.EvalDRCPolicy(old.Policy, old.Report.ExcludedItems); r != nil {
					basePolicy = r.Hard
				}
				baseKnown = true
			}
		} else {
			b.Source = o.baselineJSON
			base, basePolicy, baseKnown, b.Kind, b.PCBSha256, b.Notes, err = loadDRCBaseline(o.baselineJSON, cur.Index)
		}
		if err != nil {
			rep.Baseline = b
			rep.Checks = append(rep.Checks, cannotCheck("baseline", "cannot check against the baseline: "+err.Error()))
			return
		}
		b.DRCDiff = kicad.DiffDRC(base, all, o.tolMm)
		judged = b.New
		rep.Baseline = b
	}

	// Errors gate; so do warnings of the electrical / fabrication rules
	// (kicad.DRCGatesWarning: hole_to_hole, connection_width, a downgraded
	// solder_mask_bridge …): a fab builds what the board says, whatever
	// severity the project gave the check.
	var errItems, parityItems []string
	nErr, nFabWarn, nWarn := 0, 0, 0
	for _, v := range judged {
		switch {
		case v.Section == "schematic_parity":
			parityItems = append(parityItems, v.Item())
		case v.Severity == "" || v.Severity == "error":
			nErr++
			errItems = append(errItems, v.Item())
		case kicad.DRCGatesWarning(v.Rule):
			nFabWarn++
			errItems = append(errItems, v.Item())
		default:
			nWarn++
		}
	}
	scope := "in the board"
	if rep.Baseline != nil {
		scope = fmt.Sprintf("new vs the baseline (%d matched, %d resolved)", rep.Baseline.Matched, len(rep.Baseline.Resolved))
	}
	g := drcCheck{Result: gate.Result{Name: "kicad-drc", Detail: fmt.Sprintf("%d error(s) and %d electrical / fabrication warning(s) %s, from kicad-cli pcb drc (KiCad %s); %d other warning(s) listed, not gated", nErr, nFabWarn, scope, rep.KiCadVersion, nWarn)}}
	g.judged(errItems, o.waivers)
	if counts := sortedCounts(rep.Counts); len(counts) > 0 {
		g.Info = append(g.Info, "errors in the board: "+strings.Join(counts, ", "))
	}
	rep.Checks = append(rep.Checks, g)

	pc := drcCheck{Result: gate.Result{Name: "kicad-drc-policy"}}
	switch {
	case pol.Error != "":
		pc = cannotCheck("kicad-drc-policy", "cannot read the project's DRC exclusions / severities: "+pol.Error)
	case cur.Policy == nil:
		pc.Status, pc.Detail = gate.Pass, "no .kicad_pro: nothing excluded or silenced"
	default:
		res := pol.DRCPolicyResult
		items := res.Hard
		if rep.Baseline != nil && baseKnown {
			// A multiset: a second identical silencing is new too.
			inBase := map[string]int{}
			for _, h := range basePolicy {
				inBase[h]++
			}
			items = nil
			for _, h := range res.Hard {
				if inBase[h] > 0 {
					inBase[h]--
					continue
				}
				items = append(items, h)
			}
			rep.Baseline.NewPolicy = items
		}
		pc.Detail = fmt.Sprintf("%d exclusion(s), %d ignored, %d downgraded rule(s) in the project", len(cur.Policy.Exclusions), len(cur.Policy.Ignored), len(cur.Policy.Downgraded))
		if d := res.HardDetail(); d != "" {
			pc.Detail += "; " + d
			if rep.Baseline != nil && baseKnown {
				pc.Detail += fmt.Sprintf(" (%d not in the baseline)", len(items))
			}
		}
		if d := res.SilencedDetail(); d != "" {
			pc.Detail += "; " + d
		}
		pc.Info = res.Info
		pc.judged(items, o.waivers)
	}
	rep.Checks = append(rep.Checks, pc)

	if o.parity {
		sp := drcCheck{Result: gate.Result{Name: "schematic-parity", Detail: fmt.Sprintf("%d schematic parity item(s) %s (any severity)", len(parityItems), scope)}}
		sp.judged(parityItems, o.waivers)
		rep.Checks = append(rep.Checks, sp)
	}
}

func sortedCounts(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, fmt.Sprintf("%s %d", k, v))
	}
	sort.Strings(out)
	return out
}

// loadDRCBaseline reads --baseline: a `kicad drc` report (its owners and
// policy items), a `kicad fab --preflight` report (its drc part), or a raw
// kicad-cli DRC JSON (strictly parsed, owners from the current board).
func loadDRCBaseline(path string, cur *kicad.BoardIndex) (vs []kicad.OwnedViolation, policy []string, policyKnown bool, kind, sha string, notes []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, false, "", "", nil, err
	}
	var head struct {
		Kind string          `json:"kind"`
		DRC  json.RawMessage `json:"drc"`
	}
	_ = json.Unmarshal(data, &head)
	if head.Kind == kicadFabPreflightKind && len(head.DRC) > 0 {
		data, head.Kind = head.DRC, kicadDRCReportKind
	}
	if head.Kind == kicadDRCReportKind {
		var r kicadDRCReport
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, nil, false, "", "", nil, fmt.Errorf("%s: %w", path, err)
		}
		if r.Run == nil {
			return nil, nil, false, "", "", nil, errors.New(path + ": that report has no DRC result (its DRC could not run); use a report whose kicad-drc check ran")
		}
		if r.Policy != nil && r.Policy.DRCPolicyResult != nil {
			policy, policyKnown = r.Policy.Hard, true
		}
		return r.Violations, policy, policyKnown, "report", r.PCBSha256, nil, nil
	}
	rep, err := kicad.ParseDRC(data)
	if err != nil {
		return nil, nil, false, "", "", nil, fmt.Errorf("%s: %w", path, err)
	}
	notes = []string{"raw kicad-cli report: owners resolved against the current board (objects deleted since are unresolved); it carries no project policy, so kicad-drc-policy is judged in full"}
	return cur.Own(rep.Violations), nil, false, "kicad-json", "", notes, nil
}
