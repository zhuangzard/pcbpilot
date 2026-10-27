package app

// cmd_kicad_pcb_sync.go — `pcbpilot kicad pcb-sync`: KiCad's "Update PCB
// from Schematic" without the GUI. The schematic netlist (a fresh kicad-cli
// export, or --netlist) is matched against the board (bridge sync-read),
// internal/kicad.BuildSyncPlan computes the edits, the bridge applies them
// into a stage directory beside the target (sync-apply), three gates verify
// the staged board, and a kfile transaction commits it.
//
// Gates (internal/gate; never a pass when not verified):
//   - pad-net: every linked footprint's pad nets equal the schematic's pin
//     nets (ComparePinNets / padNetDiffGate over the staged board).
//   - schematic-parity: kicad-cli pcb drc --schematic-parity on a copy of
//     the staged board beside a copy of the sheets; any parity item fails,
//     except an extra footprint the plan flagged as an orphan.
//   - idempotent: a new plan from the staged board has no op.
//
// Exit codes: 0 pass (or no drift for --check), 1 other error / refusal,
// 2 usage, 3 --check found drift, 4 locked / changed / revision mismatch
// (nothing written), 5 a gate could not run (or --no-parity), 6 a gate
// failed (--out keeps the output; --in-place leaves the board unchanged).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kfile"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

const (
	pcbSyncSchema     = "pcbpilot.kicad.pcb-sync/1"
	pcbSyncPlanSchema = "pcbpilot.kicad.pcb-sync.plan/1"

	errSyncBridge       = "E_BRIDGE"
	errSyncVerifyPadNet = "E_VERIFY_PADNET"
	errSyncVerifyParity = "E_VERIFY_PARITY"
	errSyncCannotCheck  = "E_CANNOT_CHECK"
)

// pcbSyncTamper, when set (tests only), replaces the plan the bridge
// applies — the verify gates still judge against the schematic, so a
// tampered plan must make them fail.
var pcbSyncTamper func(*kicad.SyncPlan) *kicad.SyncPlan

type pcbSyncOpts struct {
	sch, pcb, out          string
	inPlace                bool
	netlist, netlistFormat string
	dryRun, check          bool
	planOut, applyPlan     string
	revision               string
	match, orphans         string
	update, fieldsIgnore   []string
	renameNets, create     bool
	staging                string
	stagingGap             float64
	report                 string
	breakLock              string
	noParity               bool
	forceUnverified        bool

	env     *kfile.Env     // lock identity (tests); nil = this machine
	tools   *kicad.Tools   // tests; nil = kicad.Locate()
	backend pcbSyncBackend // tests: replaces the KiCad bridge and kicad-cli (needs --netlist)
}

// pcbSyncBackend is what pcb-sync needs from KiCad (*kicad.Tools).
type pcbSyncBackend interface {
	SyncRead(pcb string) (*kicad.SyncInventory, error)
	FPInfo(refs []kicad.FPRef) (map[string]kicad.SyncFPInfo, []kicad.SyncError, error)
	SyncApply(pcb string, plan *kicad.SyncPlan, stage, base string) (*kicad.SyncApplyResult, error)
	RunParity(pcb, pro string, sheets []string, expected func(kicad.Violation) bool) kicad.ParityResult
}

// pcbSyncBeforeApply, when set (tests only), runs after the plan is made
// and before the source files are re-checked for the apply.
var pcbSyncBeforeApply func()

func newKicadPcbSyncCmd(stdout, stderr io.Writer) *cobra.Command {
	o := pcbSyncOpts{}
	c := &cobra.Command{
		Use:   "pcb-sync --sch ROOT.kicad_sch --pcb BOARD.kicad_pcb (--out NEW.kicad_pcb | --in-place)",
		Short: "Update PCB from Schematic, headless: add / swap / update / re-net footprints from the schematic, verified",
		Long: `Bring a KiCad board in line with its schematic — what KiCad's "Update PCB from
Schematic" does, without the GUI:

  - footprints of new symbols are added (staged in rows right of the board
    outline, for kicad place to move), changed footprints swapped (position,
    rotation, side, lock, field layout and link kept), references, values,
    fields, sheet names and dnp / exclude-from-BOM flags updated;
  - every pad gets its schematic net; a board net whose pads all move to one
    new schematic name is renamed with its tracks, vias and zones;
  - footprints no symbol claims are flagged (--orphans remove deletes them;
    locked and board-only ones never). Tracks and zones are never moved or
    deleted: copper left without its pad is reported (orphanCopper,
    danglingCopper).

Footprints are matched by symbol path (sheet path + unit UUID), then by a
reference unique on both sides (--match). Footprint libraries come from the
project fp-lib-table over the user's global one. The netlist is a fresh
kicad-cli export of --sch unless --netlist is given.

Nothing is guessed: unannotated or duplicate references, two footprints on one
symbol path, an empty Footprint field, a library that does not resolve or a
footprint that does not load refuse the whole sync (exit 1), and nothing is
written.

The edit is applied to a stage directory beside the target and verified
there: pad-net (pads vs schematic pins), schematic-parity (kicad-cli pcb drc
--schematic-parity on a copy) and idempotent (re-planning finds nothing). Then
a transaction commits it: KiCad lock files are honoured (--break-lock), a board
changed since it was read is refused. --in-place commits only when all three
gates passed (--no-parity needs --force-unverified to commit).

Modes: --dry-run and --check (exit 3 on drift) only read; --plan-out writes the
plan with its revision, --apply-plan P --revision R applies it only if the
files still give that revision.

Exit: 0 pass, 1 error/refused, 2 usage, 3 drift (--check), 4 locked / changed /
revision mismatch, 5 not verified (cannot-check, --no-parity), 6 verify failed.
Report: --report (default <target>.pcb-sync.json), also printed on stdout.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPcbSync(o, stdout, stderr)
		},
	}
	f := c.Flags()
	f.StringVar(&o.sch, "sch", "", "root schematic sheet (.kicad_sch)")
	f.StringVar(&o.pcb, "pcb", "", "board to sync (.kicad_pcb); with --create it may not exist yet")
	f.StringVar(&o.out, "out", "", "write the synced board here (the input board is untouched)")
	f.BoolVar(&o.inPlace, "in-place", false, "write the synced board over --pcb (only when pad-net, schematic-parity and idempotent all passed)")
	f.StringVar(&o.netlist, "netlist", "", "use this kicad-cli netlist export instead of exporting --sch")
	f.StringVar(&o.netlistFormat, "netlist-format", "", "kicadxml | kicadsexpr (default: kicadxml for the export, detected for --netlist)")
	f.BoolVar(&o.dryRun, "dry-run", false, "plan and report only; write nothing but the report")
	f.BoolVar(&o.check, "check", false, "exit 3 when the board is not in line with the schematic (read only)")
	f.StringVar(&o.planOut, "plan-out", "", "write the plan (with its revision) here and stop")
	f.StringVar(&o.applyPlan, "apply-plan", "", "apply a plan written by --plan-out (needs --revision)")
	f.StringVar(&o.revision, "revision", "", "the revision of --apply-plan; refused when the files no longer give it")
	f.StringVar(&o.match, "match", kicad.SyncMatchPathOrRef, "footprint matching: path-then-ref | path | ref")
	f.StringVar(&o.orphans, "orphans", "flag", "footprints no symbol claims: flag | remove (locked ones are never removed)")
	f.StringSliceVar(&o.update, "update", nil, "what to update: values,footprints,fields,refs,dnp (default all; pad nets always)")
	f.StringSliceVar(&o.fieldsIgnore, "fields-ignore", nil, "footprint fields never set or removed")
	f.BoolVar(&o.renameNets, "rename-nets", true, "rename a board net (with its copper) when all its pads move to one new name")
	f.BoolVar(&o.create, "create", false, "start a new board when --pcb does not exist")
	f.StringVar(&o.staging, "staging", "auto", "where new footprints go: auto (10 mm right of the outline) or X,Y in mm")
	f.Float64Var(&o.stagingGap, "staging-gap", 2, "gap between staged footprints (mm)")
	f.StringVar(&o.report, "report", "", "report path (default <target>.pcb-sync.json)")
	f.StringVar(&o.breakLock, "break-lock", "none", "KiCad lock files that may be removed: none | stale (this user, this host, no KiCad running) | any")
	f.BoolVar(&o.noParity, "no-parity", false, "skip the schematic-parity gate (the result is then not verified: exit 5; --in-place then commits only with --force-unverified)")
	f.BoolVar(&o.forceUnverified, "force-unverified", false, "with --in-place --no-parity: commit although schematic parity was not checked (exit 5 still)")
	return c
}

type pcbSyncGate struct {
	Name   string      `json:"name"`
	Status gate.Status `json:"status"`
	Detail string      `json:"detail"`
	Items  []string    `json:"items,omitempty"`
	Info   []string    `json:"info,omitempty"`
}

type pcbSyncLock struct {
	Path  string          `json:"path"`
	State kfile.LockState `json:"state"`
	Kind  string          `json:"kind"`
	User  string          `json:"user,omitempty"`
	Host  string          `json:"host,omitempty"`
	Broke bool            `json:"broke"`
}

type pcbSyncFile struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	BaseSHA string `json:"baseSha,omitempty"`
	NewSHA  string `json:"newSha,omitempty"`
}

type pcbSyncReport struct {
	Schema       string `json:"schema"`
	OK           bool   `json:"ok"`
	Verdict      string `json:"verdict"`
	Mode         string `json:"mode"`
	Sch          string `json:"sch"`
	PCB          string `json:"pcb"`
	Out          string `json:"out,omitempty"`
	InPlace      bool   `json:"inPlace"`
	Created      bool   `json:"created,omitempty"`
	Committed    bool   `json:"committed"`
	Revision     string `json:"revision,omitempty"`
	KiCadVersion string `json:"kicadVersion,omitempty"`
	Netlist      struct {
		Source     string `json:"source"`
		Format     string `json:"format"`
		Components int    `json:"components"`
		Nets       int    `json:"nets"`
	} `json:"netlist"`
	Libs struct {
		Project string                    `json:"project,omitempty"`
		Global  string                    `json:"global,omitempty"`
		Vars    map[string]kicad.FPLibVar `json:"vars,omitempty"`
	} `json:"libs"`
	Match          kicad.SyncMatch    `json:"match"`
	Ops            []kicad.SyncOp     `json:"ops"`
	Counts         kicad.SyncCounts   `json:"counts"`
	Staged         []kicad.SyncStaged `json:"staged"`
	OrphanCopper   []kicad.SyncCopper `json:"orphanCopper"`
	DanglingCopper []kicad.SyncCopper `json:"danglingCopper"`
	Locks          []pcbSyncLock      `json:"locks"`
	Files          []pcbSyncFile      `json:"files"`
	Gates          []pcbSyncGate      `json:"gates"`
	Errors         []kicad.SyncError  `json:"errors"`
	Warnings       []string           `json:"warnings,omitempty"`
}

// pcbSyncPlanFile is what --plan-out writes and --apply-plan reads.
type pcbSyncPlanFile struct {
	Schema   string         `json:"schema"`
	Revision string         `json:"revision"`
	Sch      string         `json:"sch"`
	PCB      string         `json:"pcb"`
	Ops      []kicad.SyncOp `json:"ops"`
}

func sameFilePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(aa, bb)
	}
	return aa == bb
}

func boardSibling(pcb, ext string) string { return strings.TrimSuffix(pcb, ".kicad_pcb") + ext }

func runPcbSync(o pcbSyncOpts, stdout, stderr io.Writer) error {
	usage := func(f string, a ...any) error {
		fmt.Fprintf(stderr, "pcb-sync: "+f+"\n", a...)
		return exitCodeError{2}
	}
	// Flags.
	switch {
	case o.sch == "":
		return usage("--sch is required")
	case o.pcb == "":
		return usage("--pcb is required")
	case !strings.HasSuffix(o.pcb, ".kicad_pcb"):
		return usage("--pcb %s is not a .kicad_pcb", o.pcb)
	case o.out != "" && !strings.HasSuffix(o.out, ".kicad_pcb"):
		return usage("--out %s is not a .kicad_pcb", o.out)
	case o.out != "" && o.inPlace:
		return usage("give --out or --in-place, not both")
	case o.out != "" && sameFilePath(o.out, o.pcb):
		return usage("--out is the input board; use --in-place")
	case o.check && (o.dryRun || o.planOut != "" || o.applyPlan != ""):
		return usage("--check only reads; it does not combine with --dry-run, --plan-out or --apply-plan")
	case o.planOut != "" && o.applyPlan != "":
		return usage("--plan-out and --apply-plan are two separate runs")
	case o.dryRun && o.applyPlan != "":
		return usage("--dry-run does not combine with --apply-plan")
	case o.applyPlan != "" && o.revision == "":
		return usage("--apply-plan needs --revision (printed by --plan-out)")
	case o.revision != "" && o.applyPlan == "":
		return usage("--revision goes with --apply-plan")
	case o.forceUnverified && !(o.noParity && o.inPlace):
		return usage("--force-unverified only goes with --in-place --no-parity")
	}
	readOnly := o.check || o.dryRun || o.planOut != ""
	if !readOnly && o.out == "" && !o.inPlace {
		return usage("give --out NEW.kicad_pcb or --in-place (or --dry-run / --check / --plan-out)")
	}
	// Read-only runs print the report and write it only to an explicit
	// --report: --check must not fail because KiCad has the project open.
	reportPath := o.report
	if reportPath == "" && !readOnly {
		t := o.pcb
		if o.out != "" {
			t = o.out
		}
		reportPath = boardSibling(t, ".pcb-sync.json")
	}
	if why := pcbSyncOutputAlias(o, reportPath); why != "" {
		return usage("%s", why)
	}
	// The report and plan files as they are now (absent, or pcb-sync's own):
	// written later only over exactly this state (kfile Base).
	reportSnap, why := pcbSyncOwnOutput("--report", reportPath)
	if why != "" {
		return usage("%s", why)
	}
	planSnap, why := pcbSyncOwnOutput("--plan-out", o.planOut)
	if why != "" {
		return usage("%s", why)
	}
	policy, err := kfile.ParseBreakPolicy(o.breakLock)
	if err != nil {
		return usage("%v", err)
	}
	switch o.match {
	case kicad.SyncMatchPathOrRef, kicad.SyncMatchPath, kicad.SyncMatchRef:
	default:
		return usage("--match %q: want path-then-ref, path or ref", o.match)
	}
	if o.orphans != "flag" && o.orphans != "remove" {
		return usage("--orphans %q: want flag or remove", o.orphans)
	}
	switch o.netlistFormat {
	case "", kicad.NetlistKicadXML, kicad.NetlistKicadSexpr:
	default:
		return usage("--netlist-format %q: want kicadxml or kicadsexpr", o.netlistFormat)
	}
	sopts := kicad.SyncOptions{Match: o.match, Orphans: o.orphans, RenameNets: o.renameNets, StagingGap: o.stagingGap}
	if len(o.update) > 0 {
		sopts.Update = map[string]bool{}
		for _, u := range o.update {
			u = strings.TrimSpace(u)
			if !slices.Contains(kicad.SyncUpdateAll, u) {
				return usage("--update %q: want values, footprints, fields, refs or dnp", u)
			}
			sopts.Update[u] = true
		}
	}
	if len(o.fieldsIgnore) > 0 {
		sopts.FieldsIgnore = map[string]bool{}
		for _, f := range o.fieldsIgnore {
			sopts.FieldsIgnore[strings.TrimSpace(f)] = true
		}
	}
	if o.staging != "" && o.staging != "auto" {
		xs, ys, ok := strings.Cut(o.staging, ",")
		x, err1 := strconv.ParseFloat(strings.TrimSpace(xs), 64)
		y, err2 := strconv.ParseFloat(strings.TrimSpace(ys), 64)
		if !ok || err1 != nil || err2 != nil {
			return usage("--staging %q: want auto or X,Y (mm)", o.staging)
		}
		sopts.Staging = &[2]float64{x, y}
	}
	if o.stagingGap < 0 {
		return usage("--staging-gap must not be negative")
	}
	boardExists := fileExists(o.pcb)
	if !boardExists && !o.create {
		return usage("board %s not found (pass --create to start a new board)", o.pcb)
	}
	if !fileExists(o.sch) {
		return usage("schematic %s not found", o.sch)
	}

	target := o.pcb
	if o.out != "" {
		target = o.out
	}
	rep := &pcbSyncReport{Schema: pcbSyncSchema, Sch: o.sch, PCB: o.pcb, Out: o.out, InPlace: o.inPlace, Created: !boardExists,
		Ops: []kicad.SyncOp{}, Staged: []kicad.SyncStaged{}, OrphanCopper: []kicad.SyncCopper{}, DanglingCopper: []kicad.SyncCopper{},
		Locks: []pcbSyncLock{}, Files: []pcbSyncFile{}, Gates: []pcbSyncGate{}, Errors: []kicad.SyncError{},
		Match: kicad.SyncMatch{UnmatchedSymbols: []string{}, UnmatchedFootprints: []string{}}}
	switch {
	case o.check:
		rep.Mode = "check"
	case o.dryRun:
		rep.Mode = "dry-run"
	case o.planOut != "":
		rep.Mode = "plan"
	case o.applyPlan != "":
		rep.Mode = "apply-plan"
	default:
		rep.Mode = "apply"
	}
	env := o.env
	finish := func(verdict string, code int) error {
		rep.Verdict = verdict
		rep.OK = code == 0
		b, _ := json.MarshalIndent(rep, "", "  ")
		b = append(b, '\n')
		// The report goes through kfile with the KiCad lock check (and it
		// can name no input or output: pcbSyncOutputAlias). ok is true on
		// stdout only when the report was written too.
		if reportPath != "" {
			_ = os.MkdirAll(filepath.Dir(reportPath), 0o755)
		}
		if reportPath == "" {
			// stdout only
		} else if _, err := kfile.WriteFile(reportPath, b, kfile.Opts{Env: env, Base: &reportSnap}); err != nil {
			fmt.Fprintf(stderr, "pcb-sync: cannot write the report %s: %v\n", reportPath, err)
			if code == 0 {
				if code = kfile.ExitCode(err); code == 0 {
					code = 1
				}
			}
			rep.OK = false
			b, _ = json.MarshalIndent(rep, "", "  ")
			b = append(b, '\n')
		}
		_, _ = stdout.Write(b)
		if code == 0 {
			return nil
		}
		return exitCodeError{code}
	}
	fail := func(code, path, detail, fix string, exit int) error {
		rep.Errors = append(rep.Errors, kicad.SyncError{Code: code, Path: path, Detail: detail, Fix: fix})
		v := "error"
		switch exit {
		case 4:
			v = "refused"
		case 5:
			v = string(gate.CannotCheck)
		}
		return finish(v, exit)
	}
	fileErr := func(err error) error {
		var ke *kfile.Error
		if errors.As(err, &ke) {
			rep.addLocks(ke.Locks)
			return fail(string(ke.Code), ke.Path, ke.Detail, ke.Fix, kfile.ExitCode(err))
		}
		return fail("E_IO", "", err.Error(), "", 1)
	}

	// Tools.
	var t pcbSyncBackend = o.backend
	cli := ""
	if t == nil {
		kt := o.tools
		if kt == nil {
			kt, _ = kicad.Locate()
		}
		if kt == nil || !fileExists(kt.Python) {
			return fail(errSyncBridge, "", "KiCad's Python (pcbnew) was not found", "install KiCad 10 or set "+kicad.EnvPython, 1)
		}
		t, cli = kt, kt.CLI
	}

	if o.out != "" && !readOnly {
		if err := os.MkdirAll(filepath.Dir(o.out), 0o755); err != nil {
			return fail("E_IO", o.out, err.Error(), "", 1)
		}
	}

	// Locks. A write target that is open in KiCad refuses the run before
	// anything is computed (and Apply checks again before each rename).
	if readOnly {
		if locks, err := kfile.CheckLocks([]string{o.pcb}, env); err == nil && len(locks) > 0 {
			rep.addLocks(locks)
			rep.Warnings = append(rep.Warnings, "sourceOpenInKiCad: the board is open in KiCad; edits not saved there are not in the file")
		}
	} else {
		locks, err := kfile.Guard([]string{target}, policy, env)
		rep.addLocks(locks)
		if err != nil {
			return fileErr(err)
		}
		if o.out != "" && boardExists {
			if src, err := kfile.CheckLocks([]string{o.pcb}, env); err == nil && len(src) > 0 {
				rep.addLocks(src)
				rep.Warnings = append(rep.Warnings, "sourceOpenInKiCad: the input board is open in KiCad; edits not saved there are not in the file")
			}
		}
	}

	// The board as read now: every write is checked against these hashes.
	// The source files' bytes are kept: the bridge loads exactly these
	// bytes (copied into the stage), --out publishes exactly these project
	// settings, and the files are re-checked against them right before the
	// apply and before the commit.
	var boardSnap, proSnap, druSnap kfile.Snapshot
	var boardData, proData, druData []byte
	if boardExists {
		if boardData, boardSnap, err = kfile.Read(o.pcb); err != nil {
			return fileErr(err)
		}
	} else if boardSnap, err = kfile.Stat(o.pcb); err != nil { // --create: absent, and must stay so
		return fileErr(err)
	}
	readOpt := func(p string) ([]byte, kfile.Snapshot, error) {
		st, err := kfile.Stat(p)
		if err != nil || !st.Exists {
			return nil, st, err
		}
		return kfile.Read(p)
	}
	if proData, proSnap, err = readOpt(boardSibling(o.pcb, ".kicad_pro")); err != nil {
		return fileErr(err)
	}
	if druData, druSnap, err = readOpt(boardSibling(o.pcb, ".kicad_dru")); err != nil {
		return fileErr(err)
	}
	// The schematic: the root sheet and every sheet it references
	// (recursively, ../ included), plus the project next to the root, read
	// once, hashed, and copied with their layout; the netlist export and
	// the parity check use the copy. Re-checked with the board.
	sheets, err := kicad.SchematicSheets(o.sch)
	if err != nil {
		if r, ok := kicad.IsSyncRefusal(err); ok {
			rep.Errors = append(rep.Errors, r.Errors...)
			return finish("refused", 1)
		}
		return fail(kicad.ErrSheetPath, o.sch, err.Error(), "", 1)
	}
	var inputSnaps []kfile.Snapshot
	for _, sh := range sheets {
		inputSnaps = append(inputSnaps, kfile.Snapshot{Path: sh.Path, Exists: true, Size: int64(len(sh.Data)), SHA256: kfileSHA(sh.Data)})
	}
	schCopyDir, err := os.MkdirTemp("", "pcbpilot-sync-sch-")
	if err != nil {
		return fileErr(err)
	}
	defer os.RemoveAll(schCopyDir)
	sheetCopies, err := kicad.CopySheetTree(sheets, schCopyDir)
	if err != nil {
		return fileErr(err)
	}
	// The schematic's project (next to the root sheet; its name may differ
	// from the board's): present or absent, it is an input — a stat or read
	// failure is an error, and one created later is a change.
	schPro := strings.TrimSuffix(sheets[0].Path, filepath.Ext(sheets[0].Path)) + ".kicad_pro"
	schProData, schProSnap, err := readOpt(schPro)
	if err != nil {
		return fileErr(err)
	}
	inputSnaps = append(inputSnaps, schProSnap)
	if schProData != nil {
		if err := os.WriteFile(strings.TrimSuffix(sheetCopies[0], filepath.Ext(sheetCopies[0]))+".kicad_pro", schProData, 0o644); err != nil {
			return fileErr(err)
		}
	}
	if o.netlist != "" {
		sn, err := kfile.Stat(o.netlist)
		if err != nil {
			return fileErr(err)
		}
		inputSnaps = append(inputSnaps, sn)
	}
	sourceUnchanged := func() error {
		for _, sn := range append([]kfile.Snapshot{boardSnap, proSnap, druSnap}, inputSnaps...) {
			if sn.Path == "" {
				continue
			}
			if err := sn.Check(); err != nil {
				return err
			}
		}
		return nil
	}
	targetSnap := boardSnap
	targetProSnap, targetProData := proSnap, proData
	targetDruSnap, targetDruData := druSnap, druData
	if o.out != "" {
		if targetSnap, err = kfile.Stat(o.out); err != nil {
			return fileErr(err)
		}
		if targetProData, targetProSnap, err = readOpt(boardSibling(o.out, ".kicad_pro")); err != nil {
			return fileErr(err)
		}
		if targetDruData, targetDruSnap, err = readOpt(boardSibling(o.out, ".kicad_dru")); err != nil {
			return fileErr(err)
		}
	}
	// The project and rules the result will carry: the source's when it has
	// them, else (--out) the target's, which are kept. Rename safety, the
	// parity check and the commit all use exactly these bytes.
	effPro, effDru := proData, druData
	if o.out != "" {
		if effPro == nil {
			effPro = targetProData
		}
		if effDru == nil {
			effDru = targetDruData
		}
	}
	targetUnchanged := func() error {
		for _, sn := range []kfile.Snapshot{targetSnap, targetProSnap, targetDruSnap} {
			if sn.Path != "" {
				if err := sn.Check(); err != nil {
					return err
				}
			}
		}
		return nil
	}

	// Netlist.
	format := o.netlistFormat
	var data []byte
	if o.netlist != "" {
		if data, err = os.ReadFile(o.netlist); err != nil {
			return fail(kicad.ErrNetlistExport, o.netlist, err.Error(), "", 1)
		}
		rep.Netlist.Source = o.netlist
	} else {
		if format == "" {
			format = kicad.NetlistKicadXML
		}
		if cli == "" || !fileExists(cli) {
			return fail(kicad.ErrNetlistExport, o.sch, "kicad-cli not found ("+cli+")", "install KiCad 10, set "+kicad.EnvCLI+", or pass --netlist", 1)
		}
		if data, err = kicad.ExportSyncNetlist(cli, sheetCopies[0], format); err != nil {
			return fail(kicad.ErrNetlistExport, o.sch, err.Error(), "open the schematic in KiCad once to check it loads, or pass --netlist", 1)
		}
		rep.Netlist.Source = "kicad-cli sch export netlist " + o.sch
	}
	nl, err := kicad.ReadSyncNetlist(data, format, filepath.Dir(sheetCopies[0]))
	if err != nil {
		return fail(kicad.ErrNetlistExport, o.netlist, err.Error(), "re-export the netlist with kicad-cli sch export netlist", 1)
	}
	if format == "" {
		format = kicad.NetlistKicadXML
		if strings.HasPrefix(strings.TrimSpace(string(data)), "(") {
			format = kicad.NetlistKicadSexpr
		}
	}
	rep.Netlist.Format, rep.Netlist.Components, rep.Netlist.Nets = format, len(nl.Components), len(nl.Nets)

	// Board inventory.
	inv := &kicad.SyncInventory{}
	if boardExists {
		if inv, err = t.SyncRead(o.pcb); err != nil {
			return fail(errSyncBridge, o.pcb, err.Error(), "check the board opens in KiCad 10", 1)
		}
		if err := boardSnap.Check(); err != nil {
			return fileErr(err)
		}
		rep.KiCadVersion = inv.KiCadVersion
		if !strings.HasPrefix(inv.KiCadVersion, "10.") {
			return fail(errSyncCannotCheck, o.pcb, "pcbnew "+inv.KiCadVersion+": pcb-sync is verified against KiCad 10 only",
				"use KiCad 10 ("+kicad.EnvPython+")", 5)
		}
	}

	// Footprint libraries (the user's real tables).
	libs, err := kicad.LoadFPLibTables(filepath.Dir(o.pcb))
	if err != nil {
		var le *kicad.FPLibError
		if errors.As(err, &le) {
			rep.Errors = append(rep.Errors, kicad.SyncError{Code: le.Code, Path: le.Path, Var: le.Var, Detail: le.Detail, Fix: le.Fix})
			return finish("refused", 1)
		}
		return fail(kicad.ErrFPLibUnresolved, "", err.Error(), "", 1)
	}
	rep.Libs.Project, rep.Libs.Global, rep.Libs.Vars = libs.ProjectTable, libs.GlobalTable, libs.Vars
	// The library configuration (project / global / nested tables,
	// kicad_common.json) decides which footprint files are installed: it is
	// snapshotted with the other inputs, re-checked with them and in the
	// revision.
	// Snapshots of exactly the bytes the loader parsed (not a later re-read).
	var libSnaps []kfile.Snapshot
	for _, f := range libs.Files {
		libSnaps = append(libSnaps, kfile.Snapshot{Path: f.Path, Exists: f.Exists, Size: f.Size, SHA256: f.SHA256})
	}
	inputSnaps = append(inputSnaps, libSnaps...)
	info := func(refs []kicad.FPRef) (map[string]kicad.SyncFPInfo, []kicad.SyncError) {
		// Each library footprint is hashed before and after the bridge
		// loads it: the plan describes exactly those bytes.
		before := map[string]string{}
		for _, r := range refs {
			before[r.Lib+":"+r.Name] = fileSHAOrEmpty(r.Path)
		}
		m, bad, err := t.FPInfo(refs)
		if err != nil {
			bad = append(bad, kicad.SyncError{Code: errSyncBridge, Detail: err.Error()})
		}
		for _, r := range refs {
			id := r.Lib + ":" + r.Name
			fi, ok := m[id]
			if !ok {
				continue
			}
			if sha := fileSHAOrEmpty(r.Path); sha == "" || sha != before[id] {
				bad = append(bad, kicad.SyncError{Code: string(kfile.CodeChanged), Path: r.Path,
					Detail: "library footprint " + id + " changed while it was being read", Fix: "run pcb-sync again"})
				continue
			}
			fi.SHA256 = before[id]
			m[id] = fi
		}
		return m, bad
	}
	plan, err := kicad.BuildSyncPlan(kicad.SyncInput{Netlist: nl, Inventory: inv, Libs: libs, Info: info, Opts: sopts})
	if err != nil {
		if r, ok := kicad.IsSyncRefusal(err); ok {
			rep.Errors = append(rep.Errors, r.Errors...)
			for _, e := range r.Errors {
				if e.Code == string(kfile.CodeChanged) {
					return finish("refused", 4) // a file changed while it was read
				}
			}
			return finish("refused", 1)
		}
		return fail(errSyncBridge, "", err.Error(), "", 1)
	}
	rep.fill(plan)
	if refs := kicad.RenameReferences(effPro, effDru, plan); len(refs) > 0 {
		rep.Errors = append(rep.Errors, refs...)
		return finish("refused", 1)
	}
	state := func(what string, b []byte) string {
		if b == nil {
			return what + ":absent"
		}
		return what + ":" + kfileSHA(b)
	}
	snapState := func(what string, sn kfile.Snapshot) string {
		if !sn.Exists {
			return what + ":absent"
		}
		return what + ":" + sn.SHA256
	}
	extra := append([]string{state("project", effPro), state("rules", effDru),
		snapState("target-board", targetSnap), snapState("target-project", targetProSnap), snapState("target-rules", targetDruSnap)},
		libConfigStates(libSnaps)...)
	rep.Revision = kicad.SyncRevision(boardSnap.SHA256, proSnap.SHA256, sopts, plan, extra...)

	// A read-only answer speaks only for the inputs it was computed from:
	// every snapshot (board, project, rules, sheets, netlist, target files)
	// and every library footprint planned with is re-checked before it is
	// returned (and before a plan is written).
	if readOnly {
		if err := sourceUnchanged(); err != nil {
			return fileErr(err)
		}
		if err := targetUnchanged(); err != nil {
			return fileErr(err)
		}
		for _, op := range plan.Ops {
			if (op.Op != "addFootprint" && op.Op != "swapFootprint") || op.FPSha == "" {
				continue
			}
			src := filepath.Join(op.LibDir, op.Name+".kicad_mod")
			if sha := fileSHAOrEmpty(src); sha != op.FPSha {
				return fail(string(kfile.CodeChanged), src, fmt.Sprintf("library footprint %s changed while it was planned (sha256 %s, planned %s)", op.FPID, short12(sha), short12(op.FPSha)),
					"run pcb-sync again", 4)
			}
		}
	}
	// Findings of the plan (a netlist pin without its pad) mean the board
	// cannot be brought in line: --check, --dry-run and --plan-out fail.
	if readOnly && len(plan.Errors) > 0 {
		return finish("plan-errors", 1)
	}
	switch {
	case o.check:
		if plan.Drift() {
			return finish("drift", 3)
		}
		return finish("in-sync", 0)
	case o.dryRun:
		return finish("planned", 0)
	case o.planOut != "":
		pf := pcbSyncPlanFile{Schema: pcbSyncPlanSchema, Revision: rep.Revision, Sch: o.sch, PCB: o.pcb, Ops: plan.Ops}
		b, _ := json.MarshalIndent(pf, "", "  ")
		_ = os.MkdirAll(filepath.Dir(o.planOut), 0o755)
		if _, err := kfile.WriteFile(o.planOut, append(b, '\n'), kfile.Opts{Env: env, Base: &planSnap}); err != nil {
			return fileErr(err)
		}
		return finish("planned", 0)
	case o.applyPlan != "":
		b, err := os.ReadFile(o.applyPlan)
		var pf pcbSyncPlanFile
		if err == nil {
			err = json.Unmarshal(b, &pf)
		}
		if err != nil || pf.Schema != pcbSyncPlanSchema {
			return usage("--apply-plan %s is not a %s file (%v)", o.applyPlan, pcbSyncPlanSchema, err)
		}
		if pf.Revision != o.revision {
			return fail(string(kfile.CodeRevision), o.applyPlan, fmt.Sprintf("--revision %s is not the plan's revision %s", short12(o.revision), short12(pf.Revision)),
				"pass the revision printed with the plan", 4)
		}
		if rep.Revision != o.revision {
			return fail(string(kfile.CodeRevision), o.pcb, fmt.Sprintf("the plan has revision %s, but the board, schematic and options now give %s", short12(o.revision), short12(rep.Revision)),
				"the files changed after the plan was made; make a new plan and review it", 4)
		}
	}

	// Apply into a stage directory beside the target.
	staged, stagedPro := o.pcb, boardSibling(o.pcb, ".kicad_pro")
	wrote := false
	// An in-place run with nothing to change verifies a staged copy of the
	// bytes read (not the live board) and writes nothing.
	noop := o.inPlace && len(plan.Mutating()) == 0
	{
		stage, err := kicad.NewStageDir(target)
		if err != nil {
			return fileErr(err)
		}
		defer os.RemoveAll(stage)
		base := strings.TrimSuffix(filepath.Base(target), ".kicad_pcb")
		src := ""
		if boardExists {
			src = o.pcb
		}
		applied := plan
		if pcbSyncTamper != nil {
			applied = pcbSyncTamper(plan)
		}
		if pcbSyncBeforeApply != nil {
			pcbSyncBeforeApply()
		}
		// Library footprints: the bytes planned with (fpSha) are copied into
		// the stage and loaded from there; a library changed since → refused.
		libStaged := *applied
		libStaged.Ops = append([]kicad.SyncOp(nil), applied.Ops...)
		for i, op := range libStaged.Ops {
			if (op.Op != "addFootprint" && op.Op != "swapFootprint") || op.Info {
				continue
			}
			src := filepath.Join(op.LibDir, op.Name+".kicad_mod")
			b, err := os.ReadFile(src)
			if err != nil {
				return fileErr(err)
			}
			if op.FPSha == "" || kfileSHA(b) != op.FPSha {
				return fail(string(kfile.CodeChanged), src, fmt.Sprintf("library footprint %s changed since it was planned (sha256 %s, planned %s)", op.FPID, short12(kfileSHA(b)), short12(op.FPSha)),
					"make a new plan and review it", 4)
			}
			lib := filepath.Join(stage, fmt.Sprintf("lib-%03d.pretty", i))
			if err := os.MkdirAll(lib, 0o700); err != nil {
				return fileErr(err)
			}
			if err := os.WriteFile(filepath.Join(lib, op.Name+".kicad_mod"), b, 0o600); err != nil {
				return fileErr(err)
			}
			libStaged.Ops[i].LibDir = lib
		}
		applied = &libStaged
		if src != "" {
			// The bridge loads the bytes that were read and planned on:
			// copies of the board, project and rules in the stage.
			src = filepath.Join(stage, "src-"+base+".kicad_pcb")
			for _, f := range []struct {
				ext  string
				data []byte
			}{{".kicad_pcb", boardData}, {".kicad_pro", proData}, {".kicad_dru", druData}} {
				if f.data == nil {
					continue
				}
				if err := os.WriteFile(boardSibling(src, f.ext), f.data, 0o600); err != nil {
					return fileErr(err)
				}
			}
		}
		if err := sourceUnchanged(); err != nil {
			return fileErr(err)
		}
		var res *kicad.SyncApplyResult
		if noop {
			if err := os.WriteFile(filepath.Join(stage, base+".kicad_pcb"), boardData, 0o600); err != nil {
				return fileErr(err)
			}
			res = &kicad.SyncApplyResult{KiCadVersion: rep.KiCadVersion}
		} else if res, err = t.SyncApply(src, applied, stage, base); err != nil {
			rep.Errors = append(rep.Errors, kicad.SyncError{Code: errSyncBridge, Path: target, Detail: err.Error(),
				Fix: "nothing was written; see the problems listed"})
			return finish("error", 1)
		}
		if rep.KiCadVersion == "" {
			rep.KiCadVersion = res.KiCadVersion
		}
		if !strings.HasPrefix(res.KiCadVersion, "10.") {
			return fail(errSyncCannotCheck, target, "pcbnew "+res.KiCadVersion+" applied the plan: pcb-sync is verified against KiCad 10 only",
				"use KiCad 10 ("+kicad.EnvPython+")", 5)
		}
		staged, stagedPro = filepath.Join(stage, base+".kicad_pcb"), filepath.Join(stage, base+".kicad_pro")
		// SaveBoard writes its own .kicad_pro; the project published (and
		// used by the parity check) is the source's, byte for byte.
		// Without a source project, an existing target project is kept
		// (and checked with), never replaced by SaveBoard's default one.
		if effPro != nil {
			if err := os.WriteFile(stagedPro, effPro, 0o600); err != nil {
				return fileErr(err)
			}
		}
		// The effective rules sit beside the staged board (parity runs with
		// them); SaveBoard writes none.
		stagedDru := filepath.Join(stage, base+".kicad_dru")
		_ = os.Remove(stagedDru)
		if effDru != nil {
			if err := os.WriteFile(stagedDru, effDru, 0o600); err != nil {
				return fileErr(err)
			}
		}
		wrote = !noop
	}

	// Verify the staged board.
	// The staged board has a project whenever one is published (the
	// effective one) or the bridge saved one; parity must then get it.
	if effPro == nil && noop {
		stagedPro = ""
	}
	gates, padNet, idem := pcbSyncVerify(t, o, sopts, nl, libs, info, plan, staged, stagedPro, sheetCopies)
	rep.Gates = gates
	overall := gate.Aggregate(gateResults(gates)...)
	for _, g := range gates {
		switch {
		case g.Status == gate.Fail && g.Name == "pad-net":
			rep.Errors = append(rep.Errors, kicad.SyncError{Code: errSyncVerifyPadNet, Path: target, Detail: g.Detail})
		case g.Status == gate.Fail && g.Name == "schematic-parity":
			rep.Errors = append(rep.Errors, kicad.SyncError{Code: errSyncVerifyParity, Path: target, Detail: g.Detail})
		case g.Status == gate.Fail:
			rep.Errors = append(rep.Errors, kicad.SyncError{Code: "E_VERIFY_IDEMPOTENT", Path: target, Detail: g.Detail})
		case g.Status == gate.CannotCheck:
			rep.Errors = append(rep.Errors, kicad.SyncError{Code: errSyncCannotCheck, Path: target, Detail: g.Name + ": " + g.Detail})
		}
	}

	// Commit. --in-place only when pad-net and idempotent passed and parity
	// did not fail; --out keeps its output whenever the staged board could
	// be read back.
	commit := wrote && padNet != gate.CannotCheck
	if o.inPlace {
		parity := gate.CannotCheck
		for _, g := range gates {
			if g.Name == "schematic-parity" {
				parity = g.Status
			}
		}
		commit = wrote && padNet == gate.Pass && idem == gate.Pass &&
			(parity == gate.Pass || (parity == gate.Skip && o.forceUnverified))
		if wrote && !commit {
			rep.Warnings = append(rep.Warnings, "not committed: a gate failed, could not run or was skipped (--force-unverified commits with --no-parity), so the board is unchanged")
		}
	}
	if commit {
		if err := sourceUnchanged(); err != nil {
			return fileErr(err)
		}
		if err := targetUnchanged(); err != nil {
			return fileErr(err)
		}
		txn := kfile.NewTxn(filepath.Dir(target))
		if err := txn.StageFile(target, staged, &targetSnap); err != nil {
			txn.Abort()
			return fileErr(err)
		}
		tpro := boardSibling(target, ".kicad_pro")
		if fileExists(stagedPro) && (!targetProSnap.Exists || (!o.inPlace && proData != nil)) {
			if err := txn.StageFile(tpro, stagedPro, &targetProSnap); err != nil {
				txn.Abort()
				return fileErr(err)
			}
		}
		// --out publishes the source's rules over any the target had (base
		// hash checked); without source rules the target's stay.
		if tdru := boardSibling(target, ".kicad_dru"); o.out != "" && druData != nil && !bytes.Equal(druData, targetDruData) {
			if err := txn.Stage(tdru, druData, &targetDruSnap); err != nil {
				txn.Abort()
				return fileErr(err)
			}
		}
		p, err := txn.Apply("", kfile.Opts{Locks: policy, Env: env})
		rep.addLocks(p.Locks)
		for _, c := range p.Changes {
			rep.Files = append(rep.Files, pcbSyncFile{Path: c.Path, Kind: string(c.Kind), BaseSHA: c.Base.SHA256, NewSHA: c.NewSHA})
		}
		if err != nil {
			txn.Abort()
			return fileErr(err)
		}
		rep.Committed = p.Applied
	}
	// Nothing committed (a no-op, or verification only): the result still
	// speaks for the inputs only if none changed while it ran.
	if !rep.Committed {
		if err := sourceUnchanged(); err != nil {
			return fileErr(err)
		}
		if err := targetUnchanged(); err != nil {
			return fileErr(err)
		}
	}
	return finish(string(overall), overall.ExitCode())
}

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func (r *pcbSyncReport) addLocks(ls []kfile.LockInfo) {
	for _, l := range ls {
		dup := false
		for i := range r.Locks {
			if r.Locks[i].Path == l.Path {
				r.Locks[i].Broke = r.Locks[i].Broke || l.Broke
				dup = true
			}
		}
		if !dup {
			r.Locks = append(r.Locks, pcbSyncLock{Path: l.Path, State: l.State, Kind: l.Kind, User: l.User, Host: l.Host, Broke: l.Broke})
		}
	}
}

func (r *pcbSyncReport) fill(p *kicad.SyncPlan) {
	r.Ops, r.Match, r.Counts, r.Staged = p.Ops, p.Match, p.Counts, p.Staged
	r.OrphanCopper, r.DanglingCopper = p.OrphanCopper, p.DanglingCopper
	r.Errors = append(r.Errors, p.Errors...)
}

func gateResults(gs []pcbSyncGate) []gate.Result {
	out := make([]gate.Result, len(gs))
	for i, g := range gs {
		out[i] = gate.Result{Name: g.Name, Status: g.Status, Detail: g.Detail}
	}
	return out
}

// pcbSyncVerify runs the three gates on the staged board.
func pcbSyncVerify(t pcbSyncBackend, o pcbSyncOpts, sopts kicad.SyncOptions, nl *kicad.SyncNetlist, libs kicad.FPResolver,
	info func([]kicad.FPRef) (map[string]kicad.SyncFPInfo, []kicad.SyncError), plan *kicad.SyncPlan, staged, stagedPro string, sheets []string) ([]pcbSyncGate, gate.Status, gate.Status) {
	var gates []pcbSyncGate
	inv, err := t.SyncRead(staged)
	if err != nil {
		cc := "the staged board could not be read back: " + err.Error()
		gates = append(gates, pcbSyncGate{Name: "pad-net", Status: gate.CannotCheck, Detail: cc},
			pcbSyncGate{Name: "idempotent", Status: gate.CannotCheck, Detail: cc})
	} else {
		gates = append(gates, pcbSyncPadNetGate(nl, inv))
		p2, err := kicad.BuildSyncPlan(kicad.SyncInput{Netlist: nl, Inventory: inv, Libs: libs, Info: info, Opts: sopts})
		switch {
		case err != nil:
			// A refusal of the synced board fails; a bridge that could not
			// load a footprint for the re-plan means it could not be checked.
			st := gate.CannotCheck
			if r, ok := kicad.IsSyncRefusal(err); ok {
				st = gate.Fail
				for _, e := range r.Errors {
					if e.Code == errSyncBridge {
						st = gate.CannotCheck
					}
				}
			}
			gates = append(gates, pcbSyncGate{Name: "idempotent", Status: st, Detail: "re-planning the synced board: " + err.Error()})
		case len(p2.Mutating()) > 0:
			g := pcbSyncGate{Name: "idempotent", Status: gate.Fail,
				Detail: fmt.Sprintf("re-planning the synced board still finds %d op(s)", len(p2.Mutating()))}
			for _, op := range p2.Mutating() {
				g.Items = append(g.Items, fmt.Sprintf("%s %s %s%s", op.Op, op.Ref, op.Field, op.Pad))
			}
			gates = append(gates, g)
		default:
			gates = append(gates, pcbSyncGate{Name: "idempotent", Status: gate.Pass, Detail: "re-planning the synced board finds no op"})
		}
	}
	if o.noParity {
		gates = append(gates, pcbSyncGate{Name: "schematic-parity", Status: gate.Skip, Detail: "--no-parity: not checked"})
	} else {
		flagged := map[string]bool{}
		for _, op := range plan.Ops {
			if op.Op == "orphan" && op.Action == "flag" {
				flagged[op.Ref] = true
			}
		}
		pr := t.RunParity(staged, stagedPro, sheets, func(v kicad.Violation) bool {
			if v.Rule != "extra_footprint" {
				return false
			}
			for ref := range flagged {
				if regexp.MustCompile(`Footprint ` + regexp.QuoteMeta(ref) + `\b`).MatchString(v.Message) {
					return true
				}
			}
			return false
		})
		gates = append(gates, pcbSyncGate{Name: "schematic-parity", Status: pr.Status, Detail: pr.Detail, Items: pr.Items, Info: pr.Info})
	}
	// Order: pad-net, schematic-parity, idempotent.
	order := map[string]int{"pad-net": 0, "schematic-parity": 1, "idempotent": 2}
	for i := 1; i < len(gates); i++ {
		for j := i; j > 0 && order[gates[j].Name] < order[gates[j-1].Name]; j-- {
			gates[j], gates[j-1] = gates[j-1], gates[j]
		}
	}
	var pad, idem gate.Status = gate.CannotCheck, gate.CannotCheck
	for _, g := range gates {
		switch g.Name {
		case "pad-net":
			pad = g.Status
		case "idempotent":
			idem = g.Status
		}
	}
	return gates, pad, idem
}

// pcbSyncPadNetGate compares the schematic's pin nets (symbols on the
// board, power symbols excluded) with the pads of the footprints linked to
// them (by symbol path) on the staged board. Pads without a net and
// non-plated holes are not pins.
func pcbSyncPadNetGate(nl *kicad.SyncNetlist, inv *kicad.SyncInventory) pcbSyncGate {
	on := nl.OnBoard()
	sch := map[string]string{}
	keys := map[string]bool{}
	for i := range on.Components {
		c := &on.Components[i]
		if strings.HasPrefix(c.Ref, "#") {
			continue
		}
		for _, k := range c.Keys() {
			keys[k] = true
		}
	}
	for _, n := range on.Nets {
		for _, d := range n.Nodes {
			if !strings.HasPrefix(d.Ref, "#") {
				sch[d.Ref+"."+d.Pin] = n.Name
			}
		}
	}
	// Every numbered copper pad counts, unconnected ones included: pads
	// sharing a number must all carry the pin's net (a second "1" left
	// without a net is a difference, not a pad to skip).
	board := map[string]string{}
	seen := map[string]string{}
	var conflicts []string
	for _, f := range inv.Footprints {
		if f.BoardOnly || !keys[f.Path] {
			continue
		}
		for _, p := range f.Pads {
			if p.Number == "" || p.Type == "npth" {
				continue
			}
			k := f.Ref + "." + p.Number
			if prev, ok := seen[k]; ok && prev != p.Net {
				conflicts = append(conflicts, fmt.Sprintf("%s: pads numbered %s are on %q and %q", f.Ref, p.Number, prev, p.Net))
				continue
			}
			seen[k] = p.Net
			if want, ok := sch[k]; ok && p.Net == "" {
				conflicts = append(conflicts, fmt.Sprintf("%s pad %s has no net; the schematic puts it on %s", f.Ref, p.Number, want))
			}
			if p.Net != "" {
				board[k] = p.Net
			}
		}
	}
	g := padNetDiffGate(sch, board, "staged board vs schematic netlist", "")
	st := gate.FromBool(g.Pass && len(conflicts) == 0)
	return pcbSyncGate{Name: "pad-net", Status: st, Detail: g.Detail, Items: append(g.Items, conflicts...), Info: g.Info}
}

// pcbSyncOutputAlias explains why a file pcb-sync writes besides the board
// (the report, --plan-out) is unsafe: it names — by path (case-insensitive
// where the filesystem is), link or hard link — the input board or its
// project / rules / local settings, a schematic sheet, the netlist, the
// plan being applied, the output board or its project, or the other
// output; or it has a KiCad extension. "" = safe.
func pcbSyncOutputAlias(o pcbSyncOpts, report string) string {
	type named struct{ what, path string }
	var in []named
	for _, b := range []struct{ what, pcb string }{{"--pcb", o.pcb}, {"--out", o.out}} {
		if b.pcb == "" {
			continue
		}
		for _, ext := range []string{".kicad_pcb", ".kicad_pro", ".kicad_dru", ".kicad_prl"} {
			in = append(in, named{b.what + " " + ext, boardSibling(b.pcb, ext)})
		}
	}
	in = append(in, named{"--sch", o.sch})
	if sheets, err := kicad.SchematicSheets(o.sch); err == nil {
		for _, sh := range sheets {
			in = append(in, named{"schematic sheet", sh.Path})
		}
	}
	if root := filepath.Dir(o.sch); o.sch != "" {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && p != root && (strings.HasPrefix(d.Name(), ".") || strings.HasSuffix(d.Name(), ".pretty")) {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".kicad_sch") {
				in = append(in, named{"schematic sheet", p})
			}
			return nil
		})
	}
	for _, f := range []named{{"--netlist", o.netlist}, {"--apply-plan", o.applyPlan}} {
		if f.path != "" {
			in = append(in, f)
		}
	}
	var outs []named
	if report != "" {
		outs = append(outs, named{"--report", report})
	}
	if o.planOut != "" {
		outs = append(outs, named{"--plan-out", o.planOut})
	}
	for i, out := range outs {
		if strings.HasPrefix(strings.ToLower(filepath.Ext(out.path)), ".kicad_") {
			return fmt.Sprintf("%s %s has a KiCad file extension; write it elsewhere", out.what, out.path)
		}
		for _, f := range in {
			if pathAlias(out.path, f.path) {
				return fmt.Sprintf("%s %s is the %s (%s): pcb-sync never writes over an input or output file", out.what, out.path, f.what, f.path)
			}
		}
		for _, other := range outs[:i] {
			if pathAlias(out.path, other.path) {
				return fmt.Sprintf("%s and %s are the same file %s", other.what, out.what, out.path)
			}
		}
		if !strings.EqualFold(filepath.Ext(out.path), ".json") {
			return fmt.Sprintf("%s %s must end in .json", out.what, out.path)
		}
	}
	return ""
}

// pcbSyncOwnOutput reads a report / plan path once: absent, or a pcb-sync
// report or plan (schema field); its snapshot is the Base the file is later
// written over. "" path = nothing to check.
func pcbSyncOwnOutput(what, path string) (kfile.Snapshot, string) {
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
	if json.Unmarshal(b, &head) != nil || (head.Schema != pcbSyncSchema && head.Schema != pcbSyncPlanSchema) {
		return sn, fmt.Sprintf("%s %s exists and is not a pcb-sync report or plan: pcb-sync only replaces its own files", what, path)
	}
	return sn, ""
}

// pathAlias: a and b are one file — the same cleaned absolute path (case
// folded on Windows and macOS) or, when both exist, the same file
// (os.SameFile: links, hard links, 8.3 names).
func pathAlias(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if sameFilePath(a, b) {
		return true
	}
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

func kfileSHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fileSHAOrEmpty is the sha256 of a file, "" when it cannot be read.
func fileSHAOrEmpty(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return kfileSHA(b)
}

// libConfigStates names each footprint-library config file and its
// content state for the revision.
func libConfigStates(sns []kfile.Snapshot) []string {
	out := make([]string, 0, len(sns))
	for _, sn := range sns {
		st := "absent"
		if sn.Exists {
			st = sn.SHA256
		}
		out = append(out, "libconfig:"+filepath.ToSlash(sn.Path)+":"+st)
	}
	return out
}
