package app

// cmd_kicad_board.go — `pcbpilot kicad board`: headless board setup of a
// .kicad_pcb (new, outline, stackup, holes, keepout, zone, text). Geometry
// and arguments are checked in internal/kicad/board.go, the board-dependent
// checks and the pcbnew writes in board.py; this file parses flags, stages
// the input, runs the extra kicad-cli checks and commits through kfile.
//
// Write path: the input (and its .kicad_pro / .kicad_dru) is copied into a
// stage directory and board.py writes its result there. Only when every
// check passed is the result committed with a kfile transaction: --out
// creates a new board (+ project), --in-place replaces the input after the
// KiCad lock check and a compare-and-swap against the bytes that were
// staged (E_FILE_CHANGED when the file was saved meanwhile).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kfile"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// Exit codes of `kicad board` (also in the --help): 0 written, 2 refused
// input (nothing written), 4 locked / changed (kfile), 5 a check could not
// run, 6 a check failed (nothing written), 1 anything else.
const (
	boardExitInput = 2
)

// kicadBoardOpts are the flags every board subcommand shares.
type kicadBoardOpts struct {
	pcb, out, report, spec string
	inPlace, drc           bool
	// noDRC skips the default DRC of a copper/hole op: the result is
	// unverified (exit 5) and is committed only with forceUnverified.
	noDRC, forceUnverified bool
}

// boardDRCOps add or change copper or holes: their before/after DRC runs
// by default and decides (pcbpilot's own pre-checks only catch definite
// overlaps, never clearances).
var boardDRCOps = map[string]bool{"outline": true, "stackup": true, "holes": true, "keepout": true, "zone": true}

// kicadBoardTools are the KiCad side effects (stubbed in unit tests).
type kicadBoardTools interface {
	BoardOp(op string, spec kicad.BoardOpSpec, pcb, out string) (*kicad.BoardResult, error)
	Stats(pcb string) (*kicad.BoardStats, error)
	BoardDRC(pcb, out string) (*kicad.DRCReport, error)
}

// kicadBoardReport is the JSON every board subcommand prints.
type kicadBoardReport struct {
	OK       bool               `json:"ok"`
	Op       string             `json:"op"`
	PCB      string             `json:"pcb,omitempty"`
	Out      string             `json:"out,omitempty"`
	InPlace  bool               `json:"inPlace,omitempty"`
	Verdict  gate.Status        `json:"verdict"`
	Checks   []gate.Result      `json:"checks,omitempty"`
	Issues   []kicad.BoardIssue `json:"issues,omitempty"`
	Created  []kicad.BoardOpItem  `json:"created,omitempty"`
	Removed  []kicad.BoardOpItem  `json:"removed,omitempty"`
	Summary  map[string]any     `json:"summary,omitempty"`
	Stackup  []kicad.StackupRow `json:"stackup,omitempty"`
	Warnings []string           `json:"warnings,omitempty"`
	DRC      *kicadBoardDRC     `json:"drc,omitempty"`
	Written  []string           `json:"written,omitempty"`
	Revision string             `json:"revision,omitempty"`
	KiCad    string             `json:"kicadVersion,omitempty"`

	exit int
}

// kicadBoardDRC is the DRC comparison of --drc: error-level violations of
// the written board that the input did not have.
type kicadBoardDRC struct {
	Before int                `json:"errorsBefore"`
	After  int                `json:"errorsAfter"`
	New    []kicadBoardDRCNew `json:"new,omitempty"`
}

type kicadBoardDRCNew struct {
	kicad.Violation
	// Ours: the violation names an object this op created.
	Ours bool `json:"ours"`
}

const boardLong = `Headless board setup of a .kicad_pcb with KiCad's own Python (pcbnew), no GUI.

Units are mm in KiCad's frame (x right, y down: the PCB editor's coordinates).
Every op checks all of its input first (numbers, geometry, layer names against
the board's enabled layers, nets against the board's nets) and writes nothing
when anything is wrong; problems come with a code and a fix:

  E_BOARD_ARG / E_BOARD_UNITS / E_BOARD_GEOMETRY / E_BOARD_LAYER / E_BOARD_NET
  E_BOARD_OUTLINE_EXISTS / E_BOARD_NO_OUTLINE / E_BOARD_LAYER_IN_USE
  E_BOARD_OVERLAP / E_BOARD_REF_EXISTS / E_BOARD_OUT_EXISTS / E_BOARD_VERIFY
  E_KICAD_LOCKED / E_FILE_CHANGED / E_WRITER_BUSY (the kfile write path)

Writes: --out NEW.kicad_pcb (default; the input is never modified; the project
.kicad_pro / .kicad_dru come along) or --in-place (replaces --pcb after the KiCad
lock check, and only if the file still has the bytes the op was computed from).
The result is saved by pcbnew, loaded again and verified before it is committed;
outline, stackup and holes are also read back with kicad-cli pcb export stats.

DRC decides: outline, stackup, holes, keepout and zone run kicad-cli DRC on the
input and on the result (with the input's .kicad_pro / .kicad_dru: netclasses,
custom rules) and refuse any new error-level violation. pcbpilot's own checks
only catch definite overlaps (a drill through copper, a ring on another net's
copper); every clearance is DRC's. --no-drc skips it: the result is unverified
(exit 5) and is written only with --force-unverified. text and new run DRC only
with --drc. One JSON report on stdout (and --report FILE).

Exit status: 0 written, 2 input refused, 4 locked or changed, 5 a check could
not run or was skipped, 6 a check failed (nothing written in 2/4/5/6, except
--no-drc --force-unverified: written, exit 5), 1 other errors.`

func newKicadBoardCmd(stdout io.Writer) *cobra.Command {
	c := &cobra.Command{
		Use:   "board",
		Short: "Headless board setup: new board, outline/cutouts, stackup, mounting holes, keepouts, zones, text (mm)",
		Long:  boardLong,
	}
	c.AddCommand(newKicadBoardNewCmd(stdout), newKicadBoardOutlineCmd(stdout), newKicadBoardStackupCmd(stdout),
		newKicadBoardHolesCmd(stdout), newKicadBoardKeepoutCmd(stdout), newKicadBoardZoneCmd(stdout), newKicadBoardTextCmd(stdout))
	return c
}

// boardCommonFlags adds --pcb/--out/--in-place/--report/--drc/--spec.
func boardCommonFlags(c *cobra.Command, o *kicadBoardOpts, specHelp string) {
	c.Flags().StringVar(&o.pcb, "pcb", "", "input .kicad_pcb (required)")
	c.Flags().StringVar(&o.out, "out", "", "write the result to this new .kicad_pcb (with its .kicad_pro); the input is not modified")
	c.Flags().BoolVar(&o.inPlace, "in-place", false, "replace --pcb itself (KiCad lock check + compare-and-swap)")
	c.Flags().StringVar(&o.report, "report", "", "also write the JSON report to this file")
	if boardDRCOps[c.Name()] {
		c.Flags().BoolVar(&o.noDRC, "no-drc", false, "skip the before/after DRC (the result is unverified: exit 5, not written without --force-unverified)")
		c.Flags().BoolVar(&o.forceUnverified, "force-unverified", false, "with --no-drc: write the unverified result anyway (still exit 5)")
	} else {
		c.Flags().BoolVar(&o.drc, "drc", false, "run kicad-cli DRC before and after; fail on any new error-level violation")
	}
	if specHelp != "" {
		c.Flags().StringVar(&o.spec, "spec", "", specHelp)
	}
}

// boardRun builds the spec (from --spec or the flags) and runs the op,
// printing the report and returning the exit code as an error.
func boardRun(cmd *cobra.Command, stdout io.Writer, op string, o kicadBoardOpts, spec kicad.BoardOpSpec, opFlags []string, fromFlags func() error) error {
	rep := &kicadBoardReport{Op: op, PCB: o.pcb, Out: o.out, InPlace: o.inPlace}
	// A report that would land on a board, project, rules or lock file is
	// refused before anything runs, and then never written.
	if bad := boardReportAlias(o); bad != "" {
		boardFail(rep, &kicad.BoardError{Op: op, Issues: []kicad.BoardIssue{{Code: kicad.ErrBoardArg, Field: "report",
			Detail: "--report " + o.report + " is " + bad, Fix: "write the report to its own .json file"}}})
		return boardEmit(stdout, rep, kicadBoardOpts{})
	}
	err := func() error {
		if o.spec != "" {
			for _, f := range opFlags {
				if cmd.Flags().Changed(f) {
					return &kicad.BoardError{Op: op, Issues: []kicad.BoardIssue{{Code: kicad.ErrBoardArg, Field: f,
						Detail: "--" + f + " given together with --spec", Fix: "put everything in the spec file, or drop --spec"}}}
				}
			}
			data, err := os.ReadFile(o.spec)
			if err != nil {
				return &kicad.BoardError{Op: op, Issues: []kicad.BoardIssue{{Code: kicad.ErrBoardArg, Field: "spec", Detail: err.Error(), Fix: "give a readable JSON file"}}}
			}
			return kicad.ParseBoardSpec(op, data, spec)
		}
		return fromFlags()
	}()
	if err == nil {
		var tools kicadBoardTools
		if tools, err = boardLocate(); err == nil {
			runKicadBoard(rep, op, spec, o, tools)
		}
	}
	if err != nil {
		boardFail(rep, err)
	}
	return boardEmit(stdout, rep, o)
}

// boardLocate finds KiCad (stubbed in tests).
var boardLocate = func() (kicadBoardTools, error) {
	t, err := kicad.Locate()
	if err != nil {
		return nil, err
	}
	return t, nil
}

// boardSidecars are the files that travel with a board.
var boardSidecars = []string{".kicad_pro", ".kicad_dru"}

// boardReportAlias says why --report names a file the run reads or writes
// (a board, its project, rules, local settings or a KiCad lock), or "".
func boardReportAlias(o kicadBoardOpts) string {
	if o.report == "" {
		return ""
	}
	low := strings.ToLower(boardNormPath(o.report))
	for _, ext := range []string{".kicad_pcb", ".kicad_pro", ".kicad_dru", ".kicad_prl", ".kicad_sch", ".lck"} {
		if strings.HasSuffix(low, ext) {
			return "a KiCad file (" + ext + ")"
		}
	}
	var guarded []string
	for _, b := range []string{o.pcb, o.out} {
		if b == "" {
			continue
		}
		base := strings.TrimSuffix(b, filepath.Ext(b))
		for _, ext := range append([]string{".kicad_pcb", ".kicad_prl"}, boardSidecars...) {
			guarded = append(guarded, base+ext, kfile.LockPath(base+ext))
		}
		guarded = append(guarded, b, kfile.LockPath(b))
	}
	if o.spec != "" {
		guarded = append(guarded, o.spec)
	}
	for _, g := range guarded {
		if boardSamePath(o.report, g) {
			return "the same file as " + g
		}
	}
	return ""
}

// boardNormPath is p as the file system resolves it: absolute and clean;
// on Windows also without a \\?\ (or \\?\UNC\) prefix and with the
// trailing dots and spaces Win32 drops from every component, so
// "n.kicad_pcb." and "n.kicad_pcb " are n.kicad_pcb.
func boardNormPath(p string) string {
	return boardNormPathOS(p, runtime.GOOS)
}

func boardNormPathOS(p, goos string) string {
	if goos != "windows" {
		return filepath.Clean(mustAbs(p))
	}
	q := strings.ReplaceAll(p, "/", `\`)
	switch {
	case strings.HasPrefix(q, `\\?\UNC\`):
		q = `\\` + q[len(`\\?\UNC\`):]
	case strings.HasPrefix(q, `\\?\`), strings.HasPrefix(q, `\\.\`):
		q = q[len(`\\?\`):]
	}
	parts := strings.Split(q, `\`)
	for i, c := range parts {
		if c != "." && c != ".." {
			parts[i] = strings.TrimRight(c, ". ")
		}
	}
	return filepath.Clean(mustAbs(strings.Join(parts, `\`)))
}

// boardSamePath: a and b name one file — the same normalized absolute path
// (ignoring case on Windows and macOS) or, where both exist, the same file.
func boardSamePath(a, b string) bool {
	a, b = boardNormPath(a), boardNormPath(b)
	if a == b || ((runtime.GOOS == "windows" || runtime.GOOS == "darwin") && strings.EqualFold(a, b)) {
		return true
	}
	sa, ea := os.Stat(a)
	sb, eb := os.Stat(b)
	return ea == nil && eb == nil && os.SameFile(sa, sb)
}

// boardEmit prints (and with path writes) the report; the exit code rides
// on exitCodeError.
func boardEmit(stdout io.Writer, rep *kicadBoardReport, o kicadBoardOpts) error {
	path := o.report
	if rep.Verdict == "" {
		rep.Verdict = gate.CannotCheck
	}
	blob, _ := json.MarshalIndent(rep, "", "  ")
	blob = append(blob, '\n')
	if _, err := stdout.Write(blob); err != nil {
		return err
	}
	if path != "" {
		// Identity again, now that the run may have created files: the
		// report never lands on anything the run read or wrote.
		bad := boardReportAlias(o)
		for _, w := range rep.Written {
			if bad == "" && boardSamePath(path, w) {
				bad = "the same file as " + w
			}
		}
		if bad != "" {
			return fmt.Errorf("--report %s is %s: not written (the report is on stdout)", path, bad)
		}
		// kfile: a temp file beside the report, renamed over it.
		if _, err := kfile.WriteFile(path, blob, kfile.Opts{}); err != nil {
			return fmt.Errorf("--report %s: %w", path, err)
		}
	}
	if rep.exit != 0 {
		return exitCodeError{code: rep.exit}
	}
	return nil
}

// boardFail records err in the report with its code and exit status.
func boardFail(rep *kicadBoardReport, err error) {
	rep.OK = false
	var be *kicad.BoardError
	var ke *kfile.Error
	switch {
	case errors.As(err, &be):
		rep.Issues = append(rep.Issues, be.Issues...)
		rep.exit = boardExitInput
		rep.Verdict = gate.Fail
		for _, is := range be.Issues {
			if is.Code == kicad.ErrBoardVerify {
				rep.exit = gate.Fail.ExitCode()
			}
		}
	case errors.As(err, &ke):
		rep.Issues = append(rep.Issues, kicad.BoardIssue{Code: string(ke.Code), Field: ke.Path, Detail: ke.Detail, Fix: ke.Fix})
		rep.exit = kfile.ExitCode(err)
		rep.Verdict = gate.Fail
	default:
		rep.Issues = append(rep.Issues, kicad.BoardIssue{Code: "E_BOARD_RUN", Detail: err.Error(),
			Fix: "check the KiCad install ($PCBPILOT_KICAD_PYTHON / $PCBPILOT_KICAD_CLI) and the file paths"})
		rep.exit = 1
	}
}

// runKicadBoard does one op end to end and fills rep (rep.exit = status).
func runKicadBoard(rep *kicadBoardReport, op string, spec kicad.BoardOpSpec, o kicadBoardOpts, tools kicadBoardTools) {
	if err := boardCheckPaths(op, o); err != nil {
		boardFail(rep, err)
		return
	}
	if bad := spec.Validate(); len(bad) > 0 {
		boardFail(rep, &kicad.BoardError{Op: op, Issues: bad})
		return
	}
	target := o.out
	if o.inPlace {
		target = o.pcb
	}
	// Fail early on a KiCad lock (the commit checks again).
	if _, err := kfile.Guard([]string{target}, kfile.BreakNone, nil); err != nil {
		boardFail(rep, err)
		return
	}
	stage, err := os.MkdirTemp("", "pcbpilot-board")
	if err != nil {
		boardFail(rep, err)
		return
	}
	defer os.RemoveAll(stage)
	stagedIn, stagedOut := "", filepath.Join(stage, "out", filepath.Base(target))
	var in *boardStaged
	if op != "new" {
		if in, err = boardStageInput(o.pcb, filepath.Join(stage, "in")); err != nil {
			boardFail(rep, err)
			return
		}
		stagedIn = in.pcb
	}
	if err := os.MkdirAll(filepath.Dir(stagedOut), 0o755); err != nil {
		boardFail(rep, err)
		return
	}
	res, err := tools.BoardOp(op, spec, stagedIn, stagedOut)
	if res != nil {
		rep.Created, rep.Removed, rep.Summary, rep.Warnings, rep.KiCad = res.Created, res.Removed, res.Summary, res.Warnings, res.KiCad
	}
	if err != nil {
		boardFail(rep, err)
		if res != nil && res.Verify.Status == "fail" {
			rep.Checks = append(rep.Checks, gate.Result{Name: "pcbnew-reload", Status: gate.Fail, Detail: strings.Join(res.Verify.Detail, "; ")})
			rep.Verdict = gate.Fail
		}
		return
	}
	rep.Checks = append(rep.Checks, gate.Result{Name: "pcbnew-reload", Status: gate.Parse(res.Verify.Status), Detail: strings.Join(res.Verify.Detail, "; ")})
	if err := boardStageSidecars(stagedIn, stagedOut); err != nil {
		boardFail(rep, err)
		return
	}
	if st, ok := spec.(*kicad.StackupSpec); ok {
		rep.Stackup = st.Rows()
	}
	rep.Checks = append(rep.Checks, boardStatsChecks(op, spec, tools, stagedIn, stagedOut)...)
	unverified := false
	switch {
	case boardDRCOps[op] && o.noDRC:
		rep.Checks = append(rep.Checks, gate.Result{Name: "kicad-drc-no-new-errors", Status: gate.Skip,
			Detail: "--no-drc: clearances, netclasses and custom rules were not checked; the result is unverified"})
		unverified = o.forceUnverified
	case boardDRCOps[op] || o.drc:
		chk, d := boardDRCCheck(tools, op, stagedIn, stagedOut, res.Created, stage)
		rep.Checks, rep.DRC = append(rep.Checks, chk), d
	}
	rep.Verdict = gate.Aggregate(rep.Checks...)
	if unverified && rep.Verdict == gate.Skip {
		rep.exit = rep.Verdict.ExitCode()
		rep.Warnings = append(rep.Warnings, "--no-drc --force-unverified: written without DRC; run kicad-cli pcb drc before relying on it")
	} else if !rep.Verdict.OK() {
		rep.exit = rep.Verdict.ExitCode()
		rep.Issues = append(rep.Issues, kicad.BoardIssue{Code: kicad.ErrBoardVerify, Detail: "a check did not pass (see checks); nothing was written",
			Fix: "fix the cause named in the failing check and run the op again"})
		return
	}
	plan, err := boardCommit(o, target, stagedOut, in)
	if err != nil {
		rep.Verdict = gate.Fail
		boardFail(rep, err)
		return
	}
	for _, c := range plan.Changes {
		rep.Written = append(rep.Written, c.Path)
	}
	rep.Revision = plan.Revision
	rep.OK = !unverified
}

// boardCheckPaths: --pcb / --out / --in-place make sense for op.
func boardCheckPaths(op string, o kicadBoardOpts) error {
	bad := func(field, detail, fix string) error {
		return &kicad.BoardError{Op: op, Issues: []kicad.BoardIssue{{Code: kicad.ErrBoardArg, Field: field, Detail: detail, Fix: fix}}}
	}
	if o.forceUnverified && !o.noDRC {
		return bad("force-unverified", "--force-unverified only goes with --no-drc", "drop --force-unverified (the op is verified by DRC by default)")
	}
	if op == "new" {
		if o.pcb != "" || o.inPlace {
			return bad("pcb", "new makes a board: --pcb / --in-place do not apply", "give only --out NEW.kicad_pcb")
		}
	} else {
		if o.pcb == "" {
			return bad("pcb", "--pcb is required", "give the board to change: --pcb board.kicad_pcb")
		}
		if !strings.HasSuffix(o.pcb, ".kicad_pcb") {
			return bad("pcb", o.pcb+" is not a .kicad_pcb", "give a KiCad board file")
		}
		if st, err := os.Stat(o.pcb); err != nil || !st.Mode().IsRegular() {
			return bad("pcb", fmt.Sprintf("cannot read %s", o.pcb), "check the path")
		}
		if o.inPlace == (o.out != "") {
			return bad("out", "give exactly one of --out NEW.kicad_pcb and --in-place", "use --out for a new file (default workflow) or --in-place to replace --pcb")
		}
	}
	if o.inPlace {
		return nil
	}
	if o.out == "" {
		return bad("out", "--out is required", "give --out NEW.kicad_pcb")
	}
	if !strings.HasSuffix(o.out, ".kicad_pcb") {
		return bad("out", o.out+" does not end in .kicad_pcb", "name the output board X.kicad_pcb")
	}
	if o.pcb != "" && mustAbs(o.pcb) == mustAbs(o.out) {
		return bad("out", "--out is --pcb", "use --in-place to replace the input, or another --out")
	}
	base := strings.TrimSuffix(o.out, ".kicad_pcb")
	for _, p := range []string{o.out, base + ".kicad_pro", base + ".kicad_dru"} {
		if _, err := os.Lstat(p); err == nil {
			return &kicad.BoardError{Op: op, Issues: []kicad.BoardIssue{{Code: kicad.ErrBoardOutExists, Field: "out",
				Detail: p + " already exists (the result's board, project and rules are all new files)",
				Fix:    "choose a new --out, or change that board with --pcb " + o.out + " --in-place"}}}
		}
	}
	return nil
}

// boardStaged is the staged input: the board copy and the snapshot of
// every source file the run read (a missing sidecar as an absent
// snapshot), which the commit compares against the files once more.
type boardStaged struct {
	pcb   string
	board kfile.Snapshot
	side  []kfile.Snapshot
}

// boardStageInput copies pcb and its sidecars (exactly the bytes it read)
// into dir.
func boardStageInput(pcb, dir string) (*boardStaged, error) {
	data, snap, err := kfile.Read(pcb)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	st := &boardStaged{pcb: filepath.Join(dir, filepath.Base(pcb)), board: snap}
	if err := os.WriteFile(st.pcb, data, 0o644); err != nil {
		return nil, err
	}
	for _, ext := range boardSidecars {
		src := strings.TrimSuffix(pcb, ".kicad_pcb") + ext
		b, ss, err := kfile.Read(src)
		if os.IsNotExist(err) {
			st.side = append(st.side, kfile.Snapshot{Path: src})
			continue
		}
		if err != nil {
			return nil, err
		}
		st.side = append(st.side, ss)
		if err := os.WriteFile(strings.TrimSuffix(st.pcb, ".kicad_pcb")+ext, b, 0o644); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// boardStageSidecars puts the project files of the result next to the
// staged output BEFORE it is verified, so DRC sees the input's rules and
// the commit writes exactly the verified bytes: the staged input's
// .kicad_pro / .kicad_dru (whatever pcbnew wrote is replaced); for a new
// board pcbnew's project, or a minimal one.
func boardStageSidecars(stagedIn, stagedOut string) error {
	ob := strings.TrimSuffix(stagedOut, ".kicad_pcb")
	if stagedIn == "" {
		if _, err := os.Stat(ob + ".kicad_pro"); os.IsNotExist(err) {
			return os.WriteFile(ob+".kicad_pro", kicad.MinimalProject(stagedOut), 0o644)
		}
		return nil
	}
	ib := strings.TrimSuffix(stagedIn, ".kicad_pcb")
	if err := os.Remove(ob + ".kicad_prl"); err != nil && !os.IsNotExist(err) {
		return err // local view settings: not part of the result
	}
	for _, ext := range boardSidecars {
		if err := os.Remove(ob + ext); err != nil && !os.IsNotExist(err) {
			return err
		}
		b, err := os.ReadFile(ib + ext)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err := os.WriteFile(ob+ext, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// boardCommit writes the staged result with one kfile transaction:
// --in-place the board only (compare-and-swap against the staged bytes),
// --out the board and the staged sidecars (all new files). Every source
// file the run read must still hold the bytes it was staged from.
func boardCommit(o kicadBoardOpts, target, stagedOut string, in *boardStaged) (kfile.Plan, error) {
	t := kfile.NewTxn(filepath.Dir(mustAbs(target)))
	defer t.Abort()
	if o.inPlace {
		if err := t.StageFile(target, stagedOut, &in.board); err != nil {
			return kfile.Plan{}, err
		}
	} else {
		if err := t.StageFile(target, stagedOut, nil); err != nil {
			return kfile.Plan{}, err
		}
		for _, ext := range boardSidecars {
			src := strings.TrimSuffix(stagedOut, ".kicad_pcb") + ext
			if _, err := os.Stat(src); err != nil {
				continue
			}
			if err := t.StageFile(strings.TrimSuffix(target, ".kicad_pcb")+ext, src, nil); err != nil {
				return kfile.Plan{}, err
			}
		}
	}
	if in != nil {
		for _, s := range in.side {
			if err := s.Check(); err != nil {
				return kfile.Plan{}, err
			}
		}
		if !o.inPlace {
			if err := in.board.Check(); err != nil {
				return kfile.Plan{}, err
			}
		}
	}
	return t.Apply("", kfile.Opts{})
}

// boardStatsChecks reads the staged result back with kicad-cli (outline
// size, board thickness, hole counts).
func boardStatsChecks(op string, spec kicad.BoardOpSpec, tools kicadBoardTools, in, out string) []gate.Result {
	name := "kicad-cli-stats"
	cannot := func(err error) []gate.Result {
		return []gate.Result{{Name: name, Status: gate.CannotCheck, Detail: err.Error()}}
	}
	switch op {
	case "outline", "stackup", "holes":
	default:
		return nil
	}
	after, err := tools.Stats(out)
	if err != nil {
		return cannot(err)
	}
	var fails []string
	switch s := spec.(type) {
	case *kicad.OutlineSpec:
		if !after.HasOutline {
			fails = append(fails, "kicad-cli sees no board outline")
		} else if pts := s.Outer.Samples(); len(pts) > 0 {
			b := boardBBox(pts)
			if math.Abs(after.Width-(b[2]-b[0])) > 0.01 || math.Abs(after.Height-(b[3]-b[1])) > 0.01 {
				fails = append(fails, fmt.Sprintf("kicad-cli board size %.4f x %.4f mm, expected %.4f x %.4f mm", after.Width, after.Height, b[2]-b[0], b[3]-b[1]))
			}
		}
	case *kicad.StackupSpec:
		if math.Abs(after.Thickness-s.TotalThickness()) > 0.001 {
			fails = append(fails, fmt.Sprintf("kicad-cli board thickness %.4f mm, expected %.4f mm", after.Thickness, s.TotalThickness()))
		}
	case *kicad.HolesSpec:
		before, err := tools.Stats(in)
		if err != nil {
			return cannot(err)
		}
		npth, pth := 0, 0
		for _, h := range s.Holes {
			if h.Type == "pth" {
				pth++
			} else {
				npth++
			}
		}
		if after.NPTH-before.NPTH != npth || after.PTH-before.PTH != pth {
			fails = append(fails, fmt.Sprintf("kicad-cli counts %+d NPTH / %+d PTH pads, expected %+d / %+d",
				after.NPTH-before.NPTH, after.PTH-before.PTH, npth, pth))
		}
	}
	if len(fails) > 0 {
		return []gate.Result{{Name: name, Status: gate.Fail, Detail: strings.Join(fails, "; ")}}
	}
	return []gate.Result{{Name: name, Status: gate.Pass, Detail: fmt.Sprintf("outline %v, %.4f x %.4f mm, thickness %.4f mm, %d NPTH, %d PTH pads",
		after.HasOutline, after.Width, after.Height, after.Thickness, after.NPTH, after.PTH)}}
}

func boardBBox(pts []kicad.XY) [4]float64 {
	b := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, p := range pts {
		b[0], b[1], b[2], b[3] = math.Min(b[0], p[0]), math.Min(b[1], p[1]), math.Max(b[2], p[0]), math.Max(b[3], p[1])
	}
	return b
}

// boardDRCCheck compares error-level DRC of the staged input and result.
// A new board has no outline yet: its invalid_outline is expected, not new.
func boardDRCCheck(tools kicadBoardTools, op, in, out string, created []kicad.BoardOpItem, stage string) (gate.Result, *kicadBoardDRC) {
	name := "kicad-drc-no-new-errors"
	var before *kicad.DRCReport
	if in != "" {
		r, err := tools.BoardDRC(in, filepath.Join(stage, "drc-before.json"))
		if err != nil {
			return gate.Result{Name: name, Status: gate.CannotCheck, Detail: err.Error()}, nil
		}
		before = r
	}
	after, err := tools.BoardDRC(out, filepath.Join(stage, "drc-after.json"))
	if err != nil {
		return gate.Result{Name: name, Status: gate.CannotCheck, Detail: err.Error()}, nil
	}
	d := &kicadBoardDRC{After: len(after.Violations)}
	seen := map[string]int{}
	if before != nil {
		d.Before = len(before.Violations)
		for _, v := range before.Violations {
			seen[drcKey(v)]++
		}
	}
	ours := map[string]bool{}
	for _, c := range created {
		ours[c.UUID] = true
		for _, u := range c.Parts {
			ours[u] = true
		}
	}
	for _, v := range after.Violations {
		k := drcKey(v)
		if op == "new" && v.Rule == "invalid_outline" {
			continue
		}
		if seen[k] > 0 {
			seen[k]--
			continue
		}
		n := kicadBoardDRCNew{Violation: v}
		for _, id := range v.Objs {
			n.Ours = n.Ours || ours[id]
		}
		d.New = append(d.New, n)
	}
	if len(d.New) == 0 {
		return gate.Result{Name: name, Status: gate.Pass, Detail: fmt.Sprintf("%d error(s) before, %d after, none new", d.Before, d.After)}, d
	}
	var s []string
	for i, n := range d.New {
		if i == 8 {
			s = append(s, fmt.Sprintf("… %d more", len(d.New)-8))
			break
		}
		s = append(s, n.Rule+": "+n.Message)
	}
	return gate.Result{Name: name, Status: gate.Fail, Detail: fmt.Sprintf("%d new error(s): %s", len(d.New), strings.Join(s, "; "))}, d
}

// drcKey identifies a violation across the two runs: the rule and the
// items (uuids survive a save), else the rule and the message.
func drcKey(v kicad.Violation) string {
	if len(v.Objs) > 0 {
		ids := append([]string(nil), v.Objs...)
		sort.Strings(ids)
		return v.Section + "|" + v.Rule + "|" + strings.Join(ids, ",")
	}
	return v.Section + "|" + v.Rule + "|" + v.Message
}

// ── flag parsing ────────────────────────────────────────────────────────────

func argIssue(field, detail, fix string) *kicad.BoardError {
	return &kicad.BoardError{Issues: []kicad.BoardIssue{{Code: kicad.ErrBoardArg, Field: field, Detail: detail, Fix: fix}}}
}

// parseNums reads "a,b,c" (mm) with exactly n values (or n..m when m > n).
func parseNums(field, s string, n, m int) ([]float64, error) {
	parts := strings.Split(s, ",")
	if m < n {
		m = n
	}
	want := strconv.Itoa(n)
	if m > n {
		want = fmt.Sprintf("%d or %d", n, m)
	}
	if len(parts) < n || len(parts) > m {
		return nil, argIssue(field, fmt.Sprintf("%q: want %s comma-separated numbers (mm)", s, want), "e.g. --"+field+" "+strings.Repeat("1,", n-1)+"1")
	}
	out := make([]float64, len(parts))
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, argIssue(field, fmt.Sprintf("%q is not a number (mm, no unit suffix)", p), "write plain numbers in mm, e.g. 3.2")
		}
		out[i] = v
	}
	return out, nil
}

// parsePolygon reads "x,y x,y x,y" (or ';' between points).
func parsePolygon(field, s string) ([]kicad.XY, error) {
	var pts []kicad.XY
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ';' || r == '\t' }) {
		v, err := parseNums(field, p, 2, 2)
		if err != nil {
			return nil, err
		}
		pts = append(pts, kicad.XY{v[0], v[1]})
	}
	return pts, nil
}

// shapeFlags are --rect/--polygon/--circle for keepouts and zones.
type shapeFlags struct{ rect, polygon, circle string }

func (f *shapeFlags) add(c *cobra.Command) {
	c.Flags().StringVar(&f.rect, "rect", "", "rectangle X,Y,W,H[,R] (top-left corner, size, corner radius; mm)")
	c.Flags().StringVar(&f.polygon, "polygon", "", `polygon "x,y x,y x,y ..." (mm)`)
	c.Flags().StringVar(&f.circle, "circle", "", "circle X,Y,D (centre, diameter; mm)")
}

func (f shapeFlags) shape() (kicad.BoardShape, error) {
	var s kicad.BoardShape
	if f.rect != "" {
		v, err := parseNums("rect", f.rect, 4, 5)
		if err != nil {
			return s, err
		}
		s.Rect = &kicad.BoardRect{X: v[0], Y: v[1], Width: v[2], Height: v[3]}
		if len(v) == 5 {
			s.Rect.CornerRadius = v[4]
		}
	}
	if f.polygon != "" {
		pts, err := parsePolygon("polygon", f.polygon)
		if err != nil {
			return s, err
		}
		s.Polygon = pts
	}
	if f.circle != "" {
		v, err := parseNums("circle", f.circle, 3, 3)
		if err != nil {
			return s, err
		}
		s.Circle = &kicad.BoardCircle{X: v[0], Y: v[1], Diameter: v[2]}
	}
	return s, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ── subcommands ─────────────────────────────────────────────────────────────

func newKicadBoardNewCmd(stdout io.Writer) *cobra.Command {
	var o kicadBoardOpts
	spec := &kicad.NewBoardSpec{}
	c := &cobra.Command{
		Use:     "new",
		Short:   "Create an empty board (2, 4 or 6 copper layers) and its project file",
		Example: `  pcbpilot kicad board new --out work/board.kicad_pcb --layers 4`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return boardRun(cmd, stdout, "new", o, spec, nil, func() error { return nil })
		},
	}
	c.Flags().StringVar(&o.out, "out", "", "the new .kicad_pcb (must not exist)")
	c.Flags().StringVar(&o.report, "report", "", "also write the JSON report to this file")
	c.Flags().BoolVar(&o.drc, "drc", false, "run kicad-cli DRC on the new board")
	c.Flags().IntVar(&spec.Layers, "layers", 2, "copper layers: 2, 4 or 6")
	return c
}

func newKicadBoardOutlineCmd(stdout io.Writer) *cobra.Command {
	var o kicadBoardOpts
	var rect, origin, polygon, circle string
	var radius, width float64
	var replace bool
	var cRects, cCircles, cSlots, cPolys []string
	spec := &kicad.OutlineSpec{}
	c := &cobra.Command{
		Use:   "outline",
		Short: "Draw the board outline on Edge.Cuts (rectangle with corner radius, polygon or circle) and cutouts / slots",
		Long: `Draws the outline as Edge.Cuts segments and true arcs. An existing outline is
replaced only with --replace (every board-level Edge.Cuts drawing is removed
first). Without an outer shape the cutouts are added to the existing outline.
Cutouts must lie wholly inside the outline and apart from each other (a notch
in the edge belongs in the outline polygon). Checked after the write: pcbnew
builds one closed outline with the expected cutouts and bbox, and kicad-cli
reports the expected board size.

Spec file (--spec, mm):
  {"units": "mm", "rect": {"x": 0, "y": 0, "width": 80, "height": 50, "cornerRadius": 3},
   "cutouts": [{"slot": {"x1": 20, "y1": 25, "x2": 40, "y2": 25, "width": 2}},
               {"circle": {"x": 70, "y": 40, "diameter": 4}},
               {"rect": {"x": 5, "y": 30, "width": 8, "height": 6}},
               {"polygon": [[50, 10], [60, 10], [55, 18]]}],
   "replace": false, "lineWidth": 0.05}
"polygon" (a list of [x, y]) or "circle" replace "rect" for other board shapes.`,
		Example: `  pcbpilot kicad board outline --pcb board.kicad_pcb --out work/board.kicad_pcb --rect 80,50 --corner-radius 3
  pcbpilot kicad board outline --pcb work/board.kicad_pcb --in-place --replace --polygon "0,0 80,0 80,50 20,50 0,30"
  pcbpilot kicad board outline --pcb work/board.kicad_pcb --in-place --slot 20,25,40,25,2 --cutout-circle 70,40,4`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return boardRun(cmd, stdout, "outline", o, spec, []string{"rect", "origin", "corner-radius", "polygon", "circle", "cutout-rect", "cutout-circle", "slot", "cutout-polygon", "replace", "line-width"}, func() error {
				spec.Replace, spec.LineWidth = replace, width
				if rect != "" {
					wh, err := parseNums("rect", rect, 2, 2)
					if err != nil {
						return err
					}
					at, err := parseNums("origin", origin, 2, 2)
					if err != nil {
						return err
					}
					spec.Outer.Rect = &kicad.BoardRect{X: at[0], Y: at[1], Width: wh[0], Height: wh[1], CornerRadius: radius}
				} else if cmd.Flags().Changed("corner-radius") || cmd.Flags().Changed("origin") {
					return argIssue("rect", "--origin / --corner-radius need --rect W,H", "give --rect W,H")
				}
				sf := shapeFlags{polygon: polygon, circle: circle}
				s, err := sf.shape()
				if err != nil {
					return err
				}
				spec.Outer.Polygon, spec.Outer.Circle = s.Polygon, s.Circle
				for _, r := range cRects {
					s, err := shapeFlags{rect: r}.shape()
					if err != nil {
						return err
					}
					spec.Cutouts = append(spec.Cutouts, s)
				}
				for _, ci := range cCircles {
					s, err := shapeFlags{circle: ci}.shape()
					if err != nil {
						return err
					}
					spec.Cutouts = append(spec.Cutouts, s)
				}
				for _, sl := range cSlots {
					v, err := parseNums("slot", sl, 5, 5)
					if err != nil {
						return err
					}
					spec.Cutouts = append(spec.Cutouts, kicad.BoardShape{Slot: &kicad.BoardSlot{X1: v[0], Y1: v[1], X2: v[2], Y2: v[3], Width: v[4]}})
				}
				for _, p := range cPolys {
					pts, err := parsePolygon("cutout-polygon", p)
					if err != nil {
						return err
					}
					spec.Cutouts = append(spec.Cutouts, kicad.BoardShape{Polygon: pts})
				}
				return nil
			})
		},
	}
	boardCommonFlags(c, &o, "outline spec JSON (mm; see above) instead of the shape flags")
	c.Flags().StringVar(&rect, "rect", "", "rectangular board W,H (mm)")
	c.Flags().StringVar(&origin, "origin", "0,0", "top-left corner X,Y of --rect (mm)")
	c.Flags().Float64Var(&radius, "corner-radius", 0, "corner radius of --rect (mm)")
	c.Flags().StringVar(&polygon, "polygon", "", `polygon board "x,y x,y x,y ..." (mm)`)
	c.Flags().StringVar(&circle, "circle", "", "round board X,Y,D (mm)")
	c.Flags().StringArrayVar(&cRects, "cutout-rect", nil, "rectangular cutout X,Y,W,H[,R] (repeatable; mm)")
	c.Flags().StringArrayVar(&cCircles, "cutout-circle", nil, "round cutout X,Y,D (repeatable; mm)")
	c.Flags().StringArrayVar(&cSlots, "slot", nil, "slot X1,Y1,X2,Y2,W: centre line and width (repeatable; mm)")
	c.Flags().StringArrayVar(&cPolys, "cutout-polygon", nil, `polygon cutout "x,y x,y x,y" (repeatable; mm)`)
	c.Flags().BoolVar(&replace, "replace", false, "remove the existing board-level Edge.Cuts drawings first")
	c.Flags().Float64Var(&width, "line-width", 0, "Edge.Cuts line width (mm; default 0.05)")
	return c
}

func newKicadBoardStackupCmd(stdout io.Writer) *cobra.Command {
	var o kicadBoardOpts
	var diel string
	spec := &kicad.StackupSpec{}
	c := &cobra.Command{
		Use:   "stackup",
		Short: "Set copper layer count (2/4/6) and the physical stackup: copper/dielectric thickness, material, finish",
		Long: `Sets the copper layer count and writes the physical stackup the way KiCad 10
keeps it in the board's (setup (stackup ...)) section: silk, paste, mask, copper
and dielectric layers (core/prepreg alternating, FR4 by default), copper finish.
Give the board thickness (dielectrics split what copper and mask leave) or each
dielectric's thickness. Fewer copper layers than the board has are refused while
any track, zone, pad or drawing is still on a layer that would go
(E_BOARD_LAYER_IN_USE). Checked after the write: pcbnew reloads the board with
the layer count, a stackup and the thickness, and kicad-cli reports the same
board thickness. (KiCad 10's Python has no stackup API: the block is written
into the saved file and pcbnew then loads and saves it again.)

Spec file (--spec, mm):
  {"units": "mm", "layers": 4, "thickness": 1.6, "outerCopper": 0.035, "innerCopper": 0.0175,
   "dielectrics": [{"type": "prepreg", "thickness": 0.2104}, {"type": "core", "thickness": 1.065},
                   {"type": "prepreg", "thickness": 0.2104}],
   "material": "FR4", "epsilonR": 4.5, "lossTangent": 0.02, "finish": "ENIG"}
Finishes: None, HAL SnPb (hasl), HAL lead-free (lead-free-hasl), ENIG, ENEPIG,
Hard gold, Immersion tin/silver/gold/nickel, HT_OSP, OSP, User defined.`,
		Example: `  pcbpilot kicad board stackup --pcb work/board.kicad_pcb --in-place --layers 4 --thickness 1.6 --finish ENIG
  pcbpilot kicad board stackup --pcb work/board.kicad_pcb --in-place --layers 2 --dielectric 1.51 --outer-copper 0.035`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return boardRun(cmd, stdout, "stackup", o, spec, []string{"layers", "thickness", "outer-copper", "inner-copper", "dielectric", "material", "epsilon-r", "loss-tangent", "finish", "mask-color", "silk-color"}, func() error {
				if diel != "" {
					vals, err := parseNums("dielectric", diel, 1, 16)
					if err != nil {
						return err
					}
					for _, v := range vals {
						spec.Dielectrics = append(spec.Dielectrics, kicad.Dielectric{Thickness: v})
					}
				}
				return nil
			})
		},
	}
	boardCommonFlags(c, &o, "stackup spec JSON (mm; see above) instead of the flags")
	c.Flags().IntVar(&spec.Layers, "layers", 2, "copper layers: 2, 4 or 6")
	c.Flags().Float64Var(&spec.Thickness, "thickness", 0, "board thickness (mm; default 1.6, or the sum of --dielectric)")
	c.Flags().Float64Var(&spec.OuterCopper, "outer-copper", 0, "F.Cu / B.Cu thickness (mm; default 0.035 = 1 oz)")
	c.Flags().Float64Var(&spec.InnerCopper, "inner-copper", 0, "inner copper thickness (mm; default 0.0175 = 0.5 oz)")
	c.Flags().StringVar(&diel, "dielectric", "", "every dielectric thickness top to bottom, comma-separated (mm; layers-1 values)")
	c.Flags().StringVar(&spec.Material, "material", "", "dielectric material (default FR4)")
	c.Flags().Float64Var(&spec.EpsilonR, "epsilon-r", 0, "dielectric relative permittivity (default 4.5)")
	c.Flags().Float64Var(&spec.LossTangent, "loss-tangent", 0, "dielectric loss tangent (default 0.02)")
	c.Flags().StringVar(&spec.Finish, "finish", "", "copper finish (default None; e.g. ENIG, hasl, lead-free-hasl, OSP)")
	c.Flags().StringVar(&spec.MaskColor, "mask-color", "", "solder mask colour (default Green)")
	c.Flags().StringVar(&spec.SilkColor, "silk-color", "", "silkscreen colour (default White)")
	return c
}

func newKicadBoardHolesCmd(stdout io.Writer) *cobra.Command {
	var o kicadBoardOpts
	var at []string
	var drill, pad float64
	var typ, net, ref string
	spec := &kicad.HolesSpec{}
	c := &cobra.Command{
		Use:   "holes",
		Short: "Add mounting holes (NPTH, or PTH with a pad and an optional net such as GND)",
		Long: `Adds each hole as a locked, board-only footprint "pcbpilot:MountingHole_<drill>mm[_Pad<pad>mm]"
with one pad, a courtyard circle and its designator on F.Fab (next free H<n>
unless given). kicad place keeps such footprints fixed. Refused: a hole outside
the board outline or on a cutout, on an existing pad or another new hole, a net
the board does not have, an NPTH with a net or a pad, a PTH without a pad.
Checked after the write: pcbnew reloads every hole (attribute, drill, net,
locked) and kicad-cli counts the new NPTH / PTH pads.

Spec file (--spec, mm):
  {"units": "mm", "holes": [{"x": 4, "y": 4, "drill": 3.2},
                            {"x": 76, "y": 4, "drill": 3.2, "pad": 6.4, "type": "pth", "net": "GND", "ref": "H2"}]}`,
		Example: `  pcbpilot kicad board holes --pcb work/board.kicad_pcb --in-place --drill 3.2 --at 4,4 --at 76,4 --at 4,46 --at 76,46
  pcbpilot kicad board holes --pcb work/board.kicad_pcb --in-place --type pth --drill 3.2 --pad 6.4 --net GND --at 40,4`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return boardRun(cmd, stdout, "holes", o, spec, []string{"at", "drill", "pad", "type", "net", "ref"}, func() error {
				if ref != "" && len(at) != 1 {
					return argIssue("ref", "--ref names one hole but --at was given "+strconv.Itoa(len(at))+" times", "give one --at with --ref, or drop --ref")
				}
				for _, a := range at {
					v, err := parseNums("at", a, 2, 2)
					if err != nil {
						return err
					}
					spec.Holes = append(spec.Holes, kicad.HoleSpec{X: v[0], Y: v[1], Drill: drill, Pad: pad, Type: typ, Net: net, Ref: ref})
				}
				return nil
			})
		},
	}
	boardCommonFlags(c, &o, "holes spec JSON (mm; see above) instead of the flags")
	c.Flags().StringArrayVar(&at, "at", nil, "hole centre X,Y (repeatable; mm)")
	c.Flags().Float64Var(&drill, "drill", 0, "drill diameter (mm)")
	c.Flags().Float64Var(&pad, "pad", 0, "PTH copper pad diameter (mm)")
	c.Flags().StringVar(&typ, "type", "npth", "npth | pth")
	c.Flags().StringVar(&net, "net", "", "net of a PTH (must be on the board, e.g. GND)")
	c.Flags().StringVar(&ref, "ref", "", "designator of a single hole (default: next free H<n>)")
	return c
}

func newKicadBoardKeepoutCmd(stdout io.Writer) *cobra.Command {
	var o kicadBoardOpts
	var sf shapeFlags
	var layers, forbid string
	spec := &kicad.KeepoutSpec{}
	c := &cobra.Command{
		Use:   "keepout",
		Short: "Add a rule area (keepout): no tracks / vias / pads / pours / footprints on copper layers",
		Long: `Adds a KiCad rule area with the given shape on copper layers (--layers, default
every copper layer "*.Cu"). --forbid picks the rules (default all five:
tracks,vias,pads,pours,footprints). kicad place reads a footprint keepout as a
no-parts region. Checked after the write: pcbnew reloads the area with its layers
and rules.

Spec file (--spec, mm):
  {"units": "mm", "shape": {"rect": {"x": 60, "y": 5, "width": 15, "height": 10}},
   "layers": ["F.Cu", "B.Cu"], "forbid": ["tracks", "vias", "pours"], "name": "antenna"}`,
		Example: `  pcbpilot kicad board keepout --pcb work/board.kicad_pcb --in-place --rect 60,5,15,10 --name antenna
  pcbpilot kicad board keepout --pcb work/board.kicad_pcb --in-place --polygon "10,10 20,10 15,20" --layers F.Cu --forbid tracks,vias`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return boardRun(cmd, stdout, "keepout", o, spec, []string{"rect", "polygon", "circle", "layers", "forbid", "name"}, func() error {
				s, err := sf.shape()
				if err != nil {
					return err
				}
				spec.Shape, spec.Layers, spec.Forbid = s, splitList(layers), splitList(forbid)
				return nil
			})
		},
	}
	boardCommonFlags(c, &o, "keepout spec JSON (mm; see above) instead of the flags")
	sf.add(c)
	c.Flags().StringVar(&layers, "layers", "", `copper layers, comma-separated (default "*.Cu": all)`)
	c.Flags().StringVar(&forbid, "forbid", "", "rules: tracks,vias,pads,pours,footprints (default all)")
	c.Flags().StringVar(&spec.Name, "name", "", "rule area name")
	return c
}

func newKicadBoardZoneCmd(stdout io.Writer) *cobra.Command {
	var o kicadBoardOpts
	var sf shapeFlags
	var layers string
	var noFill bool
	spec := &kicad.ZoneSpec{}
	c := &cobra.Command{
		Use:   "zone",
		Short: "Add a copper pour (net, layers, clearance, thermals, priority) and fill it with pcbnew",
		Long: `Adds a copper zone on --net over --layers with the given shape or the board
outline (--board-outline). Pads connect by --connection (thermal: spokes of
--spoke-width across --thermal-gap; solid; none; tht-thermal: thermals for
through-hole pads only). Higher --priority fills first. The board's zones are
filled with pcbnew's zone filler (all of them, as KiCad's Fill All Zones does)
unless --no-fill. Checked after the write: pcbnew reloads the zone with its net,
priority and clearance; the filled area is in the summary.

Spec file (--spec, mm):
  {"units": "mm", "boardOutline": true, "net": "GND", "layers": ["B.Cu"], "clearance": 0.3,
   "minWidth": 0.25, "thermalGap": 0.5, "spokeWidth": 0.5, "connection": "thermal", "priority": 0}`,
		Example: `  pcbpilot kicad board zone --pcb work/board.kicad_pcb --in-place --board-outline --net GND --layers F.Cu,B.Cu
  pcbpilot kicad board zone --pcb work/board.kicad_pcb --in-place --rect 5,5,30,20 --net +3V3 --layers In2.Cu --priority 1 --connection solid`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return boardRun(cmd, stdout, "zone", o, spec, []string{"rect", "polygon", "circle", "board-outline", "net", "layers", "clearance", "min-width", "thermal-gap", "spoke-width", "connection", "priority", "no-fill", "name"}, func() error {
				s, err := sf.shape()
				if err != nil {
					return err
				}
				spec.Shape, spec.Layers = s, splitList(layers)
				if noFill {
					f := false
					spec.Fill = &f
				}
				return nil
			})
		},
	}
	boardCommonFlags(c, &o, "zone spec JSON (mm; see above) instead of the flags")
	sf.add(c)
	c.Flags().BoolVar(&spec.BoardOutline, "board-outline", false, "pour over the whole board outline")
	c.Flags().StringVar(&spec.Net, "net", "", "net of the pour (required; must be on the board)")
	c.Flags().StringVar(&layers, "layers", "", "copper layers, comma-separated (required)")
	c.Flags().Float64Var(&spec.Clearance, "clearance", 0, "zone clearance (mm; default 0.25)")
	c.Flags().Float64Var(&spec.MinWidth, "min-width", 0, "minimum fill width (mm; default 0.25)")
	c.Flags().Float64Var(&spec.ThermalGap, "thermal-gap", 0, "thermal relief gap (mm; default 0.5)")
	c.Flags().Float64Var(&spec.SpokeWidth, "spoke-width", 0, "thermal spoke width (mm; default 0.5)")
	c.Flags().StringVar(&spec.Connection, "connection", "", "pad connection: thermal (default) | solid | none | tht-thermal")
	c.Flags().IntVar(&spec.Priority, "priority", 0, "zone priority (higher fills first)")
	c.Flags().BoolVar(&noFill, "no-fill", false, "do not fill the zones")
	c.Flags().StringVar(&spec.Name, "name", "", "zone name")
	return c
}

func newKicadBoardTextCmd(stdout io.Writer) *cobra.Command {
	var o kicadBoardOpts
	var at, mirror string
	spec := &kicad.TextSpec{}
	c := &cobra.Command{
		Use:   "text",
		Short: "Add board text on a silkscreen or fab layer",
		Long: `Adds a board text (not a footprint field) at --at on F.Silkscreen (default),
B.Silkscreen, F.Fab or B.Fab (the legacy names F.SilkS / B.SilkS work too). Text on
a bottom layer is mirrored unless --mirror no. A size below the board's minimum
silk text height / thickness is reported as a warning (KiCad DRC flags it).

Spec file (--spec, mm):
  {"units": "mm", "text": "rev A", "x": 40, "y": 47, "layer": "F.Silkscreen", "size": 1.2,
   "thickness": 0.18, "rotation": 0, "justify": "center"}`,
		Example: `  pcbpilot kicad board text --pcb work/board.kicad_pcb --in-place --text "rev A" --at 40,47
  pcbpilot kicad board text --pcb work/board.kicad_pcb --in-place --text "GND" --at 10,10 --layer B.Silkscreen --size 1.5`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return boardRun(cmd, stdout, "text", o, spec, []string{"text", "at", "layer", "size", "thickness", "rotation", "justify", "mirror"}, func() error {
				if at == "" {
					return argIssue("at", "--at is required", "give the text anchor --at X,Y (mm)")
				}
				v, err := parseNums("at", at, 2, 2)
				if err != nil {
					return err
				}
				spec.X, spec.Y = v[0], v[1]
				switch mirror {
				case "", "auto":
				case "yes", "no":
					m := mirror == "yes"
					spec.Mirror = &m
				default:
					return argIssue("mirror", fmt.Sprintf("--mirror %q", mirror), "use auto, yes or no")
				}
				return nil
			})
		},
	}
	boardCommonFlags(c, &o, "text spec JSON (mm; see above) instead of the flags")
	c.Flags().StringVar(&spec.Text, "text", "", "the text (required)")
	c.Flags().StringVar(&at, "at", "", "anchor X,Y (mm)")
	c.Flags().StringVar(&spec.Layer, "layer", "", "F.Silkscreen (default) | B.Silkscreen | F.Fab | B.Fab")
	c.Flags().Float64Var(&spec.Size, "size", 0, "glyph height (mm; default 1)")
	c.Flags().Float64Var(&spec.Thickness, "thickness", 0, "stroke width (mm; default 0.15)")
	c.Flags().Float64Var(&spec.Rotation, "rotation", 0, "rotation (degrees, counter-clockwise)")
	c.Flags().StringVar(&spec.Justify, "justify", "", "left | center (default) | right")
	c.Flags().StringVar(&mirror, "mirror", "auto", "auto (mirrored on bottom layers) | yes | no")
	return c
}
