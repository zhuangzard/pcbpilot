package app

// cmd_pcb_fastroute.go — the EasyEDA side of an external DSN→SES round-trip:
//
//   - `pcb dsn-fix`    offline repairs of EasyEDA's getDsnFile export;
//   - `pcb ses-repair` live repairs after importAutoRouteSes;
//   - the `--router fastroute` preset and post-import checks of `pcb autoroute`.
//
// fastroute (github.com/parisxmas/fastroute, a Rust port of Freerouting) is
// GPLv3. pcbpilot is MIT, so it is only ever run as a separate process found
// on disk; nothing here bundles, links or downloads it.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

// ── DSN fix flags (shared by dsn-fix and autoroute) ─────────────────────────

type dsnFixFlags struct {
	copperLayers int
	edgeOuterMil float64
	edgeInnerMil float64
	gndPlane     bool
	planeNet     string
	escapesFile  string
}

func (f *dsnFixFlags) register(fs *pflag.FlagSet) {
	fs.IntVar(&f.copperLayers, "copper-layers", 0, "board copper layer count (0 = the live board in autoroute, else the DSN's highest InnerN)")
	fs.Float64Var(&f.edgeOuterMil, "edge-outer-mil", 20, "board-edge copper keep-out on Top/Bottom (mil; 0 = none)")
	fs.Float64Var(&f.edgeInnerMil, "edge-inner-mil", 30, "board-edge copper keep-out on inner layers (mil; 0 = none)")
	fs.BoolVar(&f.gndPlane, "gnd-plane", false, "declare inner layers missing from the export as a plane of --plane-net (default: route that net as traces)")
	fs.StringVar(&f.planeNet, "plane-net", "GND", "net of the plane used with --gnd-plane")
	fs.StringVar(&f.escapesFile, "escapes", "", "JSON list of fixed escapes [{net,layer,widthMil,path:[[x,y],..],via}] (mil, DSN layer names)")
}

func (f *dsnFixFlags) options() (specctra.FixOptions, error) {
	opt := specctra.FixOptions{
		CopperLayers: f.copperLayers,
		EdgeOuterMil: f.edgeOuterMil,
		EdgeInnerMil: f.edgeInnerMil,
	}
	if f.gndPlane {
		opt.PlaneNet = f.planeNet
	}
	if f.escapesFile != "" {
		data, err := os.ReadFile(f.escapesFile)
		if err != nil {
			return opt, fmt.Errorf("read --escapes: %w", err)
		}
		if err := json.Unmarshal(data, &opt.Escapes); err != nil {
			return opt, fmt.Errorf("parse --escapes %s: %w", f.escapesFile, err)
		}
	}
	return opt, nil
}

// viaToPourNote is EasyEDA behaviour every fastroute/plane user hits.
const viaToPourNote = "EasyEDA native DRC does not count a via that only touches a copper pour as connected. " +
	"Do not rely on via-to-pour connectivity: route every net, GND included, with real tracks (the default; --gnd-plane opts out)."

func newPcbDsnFixCmd(stdout, stderr io.Writer) *cobra.Command {
	var fx dsnFixFlags
	var outPath string
	c := &cobra.Command{
		Use:   "dsn-fix <in.dsn>",
		Short: "Repair an EasyEDA Specctra DSN export for an external router (offline)",
		Long: `Repair the defects of EasyEDA's DSN export before handing it to Freerouting
or fastroute. Pure file transform; no editor needed.

  • class net names written as 'NET' → "NET" (otherwise every net-class
    width and clearance is ignored);
  • inner layers missing from the export are added and the layers are
    declared in stack order Top, Inner1..N, Bottom;
  • through-hole padstacks that only list Top/Bottom/InnerK get a shape on
    every missing inner layer;
  • board-edge copper keep-out bands (default 20 mil outer / 30 mil inner);
  • --gnd-plane: declare the missing inner layer as a --plane-net plane
    instead of routing that net as traces;
  • --escapes: pre-routed (type fix) stubs + vias for pins the router
    cannot escape (e.g. GND pins between fine-pitch signal pins).

` + viaToPourNote,
		Args: cobra.ExactArgs(1),
		Example: `  pcbpilot pcb dsn-fix board.dsn --out board-fixed.dsn
  pcbpilot pcb dsn-fix board.dsn --out board-fixed.dsn --escapes u8-gnd-escapes.json
  pcbpilot pcb dsn-fix board.dsn --out board-plane.dsn --gnd-plane --plane-net GND`,
		RunE: func(cmd *cobra.Command, args []string) error {
			src, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			opt, err := fx.options()
			if err != nil {
				return err
			}
			out, rep, err := specctra.FixDSN(string(src), opt)
			if err != nil {
				return err
			}
			if outPath == "" {
				outPath = strings.TrimSuffix(args[0], filepath.Ext(args[0])) + "-fixed.dsn"
			}
			if err := os.WriteFile(outPath, []byte(out), 0o644); err != nil {
				return err
			}
			if opt.PlaneNet != "" {
				fmt.Fprintln(stderr, "note: "+viaToPourNote)
			}
			return writeJSON(stdout, map[string]any{"out": outPath, "fix": rep})
		},
	}
	fx.register(c.Flags())
	c.Flags().StringVar(&outPath, "out", "", "output DSN (default <in>-fixed.dsn)")
	return c
}

// ── SES import repair (live) ────────────────────────────────────────────────

type sesRepairSummary struct {
	DryRun        bool                `json:"dryRun"`
	LayerMoves    int                 `json:"layerMoves"`
	WidthRestores int                 `json:"widthRestores"`
	TracksDeleted int                 `json:"tracksDeleted"`
	TracksCreated int                 `json:"tracksCreated"`
	Unmatched     int                 `json:"unmatchedTracks"`
	FixedTracks   int                 `json:"fixedWiringTracks"`
	FixedVias     int                 `json:"fixedWiringVias"`
	Fixes         []specctra.TrackFix `json:"fixes,omitempty"`
	FixedCreate   []specctra.NewTrack `json:"fixedWiringCreate,omitempty"`
	FixedViaList  []specctra.NewVia   `json:"fixedWiringViaCreate,omitempty"`
	Failures      []map[string]string `json:"failures,omitempty"`
	UnmatchedList []specctra.Track    `json:"unmatched,omitempty"`
}

func listPcbTracks(cfg *appConfig, window string) ([]specctra.Track, error) {
	res, err := requestActionTimed(cfg, "pcb.line.list", window, map[string]any{}, 2*time.Minute)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(res.Result["lines"])
	var tracks []specctra.Track
	if err := json.Unmarshal(raw, &tracks); err != nil {
		return nil, fmt.Errorf("decode pcb.line.list: %w", err)
	}
	return tracks, nil
}

func listPcbVias(cfg *appConfig, window string) ([][2]float64, []string, error) {
	res, err := requestActionTimed(cfg, "pcb.via.list", window, map[string]any{}, 2*time.Minute)
	if err != nil {
		return nil, nil, err
	}
	raw, _ := json.Marshal(res.Result["vias"])
	var rows []struct {
		Net string  `json:"net"`
		X   float64 `json:"x"`
		Y   float64 `json:"y"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, nil, fmt.Errorf("decode pcb.via.list: %w", err)
	}
	pts := make([][2]float64, 0, len(rows))
	nets := make([]string, 0, len(rows))
	for _, r := range rows {
		pts = append(pts, [2]float64{r.X, r.Y})
		nets = append(nets, r.Net)
	}
	return pts, nets, nil
}

func createPcbTrack(cfg *appConfig, window string, t specctra.NewTrack) error {
	_, err := requestAction(cfg, "pcb.line.create", window, map[string]any{
		"net": t.Net, "layer": t.Layer, "lineWidth": t.Width,
		"startX": t.X1, "startY": t.Y1, "endX": t.X2, "endY": t.Y2,
	})
	return err
}

// repairImportedSession fixes the EasyEDA importAutoRouteSes defects on the
// live board: inner tracks on the wrong layer id, neck-down widths replaced
// by the net-rule width (collinear merges keep the widest), and the DSN's
// fixed wiring that never reaches the session. New copper is created before
// the replaced track is deleted, so a failed call never loses a connection.
func repairImportedSession(cfg *appConfig, window string, ses, dsn string, dryRun bool, stderr io.Writer) (*sesRepairSummary, error) {
	wiring, err := specctra.ParseSES(ses)
	if err != nil {
		return nil, err
	}
	tracks, err := listPcbTracks(cfg, window)
	if err != nil {
		return nil, err
	}
	plan := specctra.PlanImportRepair(tracks, wiring)
	sum := &sesRepairSummary{
		DryRun: dryRun, LayerMoves: plan.LayerMoves, WidthRestores: plan.WidthRestores,
		Unmatched: len(plan.Unmatched), Fixes: plan.Fixes, UnmatchedList: plan.Unmatched,
	}
	fmt.Fprintf(stderr, "ses-repair: %d track(s) to replace (%d layer move(s), %d width restore(s)), %d unmatched\n",
		len(plan.Fixes), plan.LayerMoves, plan.WidthRestores, len(plan.Unmatched))
	if !dryRun {
		var del []string
		for _, f := range plan.Fixes {
			ok := true
			for _, nt := range f.Create {
				if err := createPcbTrack(cfg, window, nt); err != nil {
					sum.Failures = append(sum.Failures, map[string]string{"track": f.Delete.ID, "error": err.Error()})
					ok = false
					break
				}
				sum.TracksCreated++
			}
			if ok {
				del = append(del, f.Delete.ID)
			}
		}
		for i := 0; i < len(del); i += 200 {
			chunk := del[i:min(i+200, len(del))]
			if _, err := requestActionTimed(cfg, "pcb.route.delete", window,
				map[string]any{"primitiveIds": chunk, "kind": "track"}, 5*time.Minute); err != nil {
				return sum, fmt.Errorf("delete replaced tracks: %w", err)
			}
			sum.TracksDeleted += len(chunk)
		}
	}

	if dsn == "" {
		return sum, nil
	}
	fixed, err := specctra.ParseFixedWiring(dsn)
	if err != nil {
		return sum, err
	}
	if len(fixed.Segments) == 0 && len(fixed.Vias) == 0 {
		return sum, nil
	}
	if !dryRun {
		if tracks, err = listPcbTracks(cfg, window); err != nil {
			return sum, err
		}
	}
	vpts, vnets, err := listPcbVias(cfg, window)
	if err != nil {
		return sum, err
	}
	diam := 0.0
	if len(fixed.Vias) > 0 {
		diam = specctra.ViaDiameterMil(dsn, fixed.Vias[0].Padstack)
	}
	nt, nv := specctra.PlanFixedWiring(fixed, tracks, vpts, vnets, diam)
	sum.FixedCreate, sum.FixedViaList = nt, nv
	fmt.Fprintf(stderr, "ses-repair: re-creating %d fixed DSN track(s) and %d via(s) missing from the session\n", len(nt), len(nv))
	if dryRun {
		return sum, nil
	}
	for _, t := range nt {
		if err := createPcbTrack(cfg, window, t); err != nil {
			sum.Failures = append(sum.Failures, map[string]string{"fixedTrack": t.Net, "error": err.Error()})
			continue
		}
		sum.FixedTracks++
	}
	for _, v := range nv {
		payload := map[string]any{"x": v.X, "y": v.Y, "net": v.Net}
		if v.DiameterMil > 0 {
			payload["diameter"] = v.DiameterMil
		}
		if _, err := requestAction(cfg, "pcb.via.create", window, payload); err != nil {
			sum.Failures = append(sum.Failures, map[string]string{"fixedVia": v.Net, "error": err.Error()})
			continue
		}
		sum.FixedVias++
	}
	return sum, nil
}

// ── post-import checks ──────────────────────────────────────────────────────

type postImportSummary struct {
	PourRebuilt      bool           `json:"pourRebuilt"`
	Saved            bool           `json:"saved"`
	Reloaded         bool           `json:"reloaded"`
	DRCPassed        bool           `json:"drcPassed"`
	DRCTotal         int            `json:"drcViolations"`
	DRCCounts        map[string]int `json:"drcCounts,omitempty"`
	ConnectionErrors int            `json:"connectionErrors"`
	PadNetDiff       any            `json:"padNetDiff"`
	Notes            []string       `json:"notes"`
}

var padNetDiffAsset = skillAsset{
	name:     "pad-net-diff.py",
	rels:     []string{"pcbpilot/scripts/pad-net-diff.py"},
	flagHint: "--pad-net-diff-script",
}

// postImportChecks: pour rebuild → save → reload → pour rebuild → native DRC
// → pad-net diff against the schematic connectivity (when given).
func postImportChecks(cfg *appConfig, window string, schFiles []string, scriptPath string, stderr io.Writer) (*postImportSummary, error) {
	sum := &postImportSummary{Notes: []string{viaToPourNote}}
	step := func(name string) { fmt.Fprintf(stderr, "post-import: %s\n", name) }

	step("pour rebuild")
	if _, err := requestActionTimed(cfg, "pcb.pour.rebuild", window, map[string]any{}, 20*time.Minute); err != nil {
		return sum, fmt.Errorf("pour rebuild: %w", err)
	}
	step("save")
	if _, err := requestActionTimed(cfg, "pcb.save", window, nil, 5*time.Minute); err != nil {
		return sum, fmt.Errorf("save: %w", err)
	}
	sum.Saved = true
	step("reload")
	_, active, win, err := discoverDocs(cfg, window)
	if err != nil {
		return sum, fmt.Errorf("reload: %w", err)
	}
	if active == "" {
		return sum, fmt.Errorf("reload: no active document")
	}
	if _, err := reloadDocumentByUUID(cfg, win, active); err != nil {
		return sum, fmt.Errorf("reload: %w", err)
	}
	sum.Reloaded = true
	step("pour rebuild (after reload)")
	if _, err := requestActionTimed(cfg, "pcb.pour.rebuild", window, map[string]any{}, 20*time.Minute); err != nil {
		return sum, fmt.Errorf("pour rebuild after reload: %w", err)
	}
	sum.PourRebuilt = true

	step("native DRC")
	res, err := requestActionTimed(cfg, "pcb.drc.check", window, nil, 20*time.Minute)
	if err != nil {
		return sum, drcTimeoutHint(err, stderr)
	}
	flat := flattenDrcResult(res.Result)
	sum.DRCPassed, sum.DRCTotal, sum.DRCCounts = flat.Passed, flat.Total, flat.Counts
	sum.ConnectionErrors = flat.Counts["Connection Error"]
	if sum.ConnectionErrors > 0 {
		sum.Notes = append(sum.Notes, fmt.Sprintf(
			"%d Connection Error(s): check whether they are vias/pins joined only through a pour — EasyEDA DRC reports those even though the copper touches.",
			sum.ConnectionErrors))
	}

	if len(schFiles) == 0 {
		sum.PadNetDiff = map[string]any{"skipped": true,
			"hint": "pass --sch-connectivity <page.json> (output of `pcbpilot sch connectivity`, one per schematic page of this board)"}
		return sum, nil
	}
	step("pad-net diff")
	script, err := padNetDiffAsset.resolve(scriptPath)
	if err != nil {
		return sum, err
	}
	snap, err := fetchBoardSnapshot(cfg, window, boardSnapshotOpts{})
	if err != nil {
		return sum, fmt.Errorf("board dump for pad-net diff: %w", err)
	}
	tmp, err := os.CreateTemp("", "pcbpilot-board-*.json")
	if err != nil {
		return sum, err
	}
	defer os.Remove(tmp.Name())
	if err := json.NewEncoder(tmp).Encode(snap); err != nil {
		tmp.Close()
		return sum, err
	}
	tmp.Close()
	args := []string{"--pcb", tmp.Name(), "--json"}
	for _, f := range schFiles {
		args = append(args, "--sch", f)
	}
	cmd, err := pythonCommand(script, args...)
	if err != nil {
		return sum, err
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, stderr
	runErr := cmd.Run()
	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		return sum, fmt.Errorf("pad-net-diff.py: %w", runErr)
	}
	var diff any
	if err := json.Unmarshal(out.Bytes(), &diff); err != nil {
		return sum, fmt.Errorf("pad-net-diff.py output: %w", err)
	}
	sum.PadNetDiff = diff
	return sum, nil
}

func newPcbSesRepairCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var dsnPath, scriptPath string
	var dryRun, noPost bool
	var schFiles []string
	c := &cobra.Command{
		Use:   "ses-repair <routed.ses>",
		Short: "Repair the active PCB after 'pcb import-autoroute' of a Specctra session",
		Long: `Compare the tracks EasyEDA's importAutoRouteSes created with the session
file and fix its known defects on the live board:

  • Inner1/Inner2 tracks land on layer ids 21/22: recreated on 15/16 (in
    general on the session's layer: InnerN → 14+N);
  • neck-down segment widths are replaced by the net-rule width and collinear
    same-net segments are merged keeping the widest: the merged track is
    deleted and recreated piece by piece with the routed widths;
  • --dsn: the DSN's (type fix) wiring (e.g. reserved escapes) is not in the
    session, so missing fixed tracks/vias are created.

Then (unless --no-post): pour rebuild → save → reload → pour rebuild →
native DRC → pad-net diff (with --sch-connectivity).

` + viaToPourNote,
		Args: cobra.ExactArgs(1),
		Example: `  pcbpilot pcb import-autoroute board.ses
  pcbpilot pcb ses-repair board.ses --dsn board-fixed.dsn --sch-connectivity p1.json --sch-connectivity p2.json
  pcbpilot pcb ses-repair board.ses --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ses, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			dsn := ""
			if dsnPath != "" {
				b, err := os.ReadFile(dsnPath)
				if err != nil {
					return err
				}
				dsn = string(b)
			}
			rep, err := repairImportedSession(cfg, *window, string(ses), dsn, dryRun, stderr)
			if err != nil {
				return err
			}
			out := map[string]any{"repair": rep}
			if !dryRun && !noPost {
				post, perr := postImportChecks(cfg, *window, schFiles, scriptPath, stderr)
				out["post"] = post
				if perr != nil {
					_ = writeJSON(stdout, out)
					return perr
				}
			}
			return writeJSON(stdout, out)
		},
	}
	c.Flags().StringVar(&dsnPath, "dsn", "", "the DSN the session was routed from (re-creates its fixed wiring)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "plan only; change nothing")
	c.Flags().BoolVar(&noPost, "no-post", false, "skip pour rebuild / save / reload / DRC / pad-net diff")
	c.Flags().StringArrayVar(&schFiles, "sch-connectivity", nil, "schematic connectivity JSON for the pad-net diff (repeat per page)")
	c.Flags().StringVar(&scriptPath, "pad-net-diff-script", "", "path to pad-net-diff.py (auto-detected if omitted)")
	return c
}

// ── fastroute preset ────────────────────────────────────────────────────────

const fastrouteInstallHint = "fastroute is not installed (GPLv3, run as a separate process; pcbpilot never downloads it). " +
	"Install it yourself: scripts/install-fastroute.sh (downloads one release asset and verifies SHA256SUMS.txt), " +
	"or see .agents/skills/pcbpilot/references/pcb-routing.md#external-router-fastroute; then put it on PATH, set FASTROUTE_BIN, or pass --fastroute-bin."

// resolveFastroute finds the binary: explicit flag, $FASTROUTE_BIN, PATH.
func resolveFastroute(explicit string) (string, error) {
	for _, p := range []string{explicit, os.Getenv("FASTROUTE_BIN")} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("fastroute binary %s: %w", p, err)
		}
		return p, nil
	}
	if p, err := exec.LookPath("fastroute"); err == nil {
		return p, nil
	}
	return "", errors.New(fastrouteInstallHint)
}

type fastrouteOpts struct {
	bin        string
	multiStart int
	minTraceUm float64
	maxTime    time.Duration
	rounds     int
	timeout    time.Duration
}

// fastrouteArgs builds one fastroute invocation.
func fastrouteArgs(o fastrouteOpts, dsn, ses, report, initial string) []string {
	args := []string{"-de", dsn, "-do", ses, "--report=" + report, "--diagnose"}
	if o.multiStart > 0 {
		args = append(args, "--multi-start="+strconv.Itoa(o.multiStart))
	}
	if o.minTraceUm > 0 {
		args = append(args, "--router.min_trace_width_um="+strconv.FormatFloat(o.minTraceUm, 'f', -1, 64))
	}
	if o.maxTime > 0 {
		args = append(args, "--max-time="+strconv.Itoa(int(o.maxTime.Seconds())))
	}
	if initial != "" {
		args = append(args, "--initial-session="+initial)
	}
	return args
}

type fastrouteRun struct {
	Round      int     `json:"round"`
	Session    string  `json:"session"`
	Report     string  `json:"report"`
	Seconds    float64 `json:"seconds"`
	Unrouted   int     `json:"unrouted"`
	Violations int     `json:"violations"`
}

// readFastrouteReport extracts the counts of a fastroute --report file.
func readFastrouteReport(path string) (unrouted, violations int, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	var r struct {
		Stats struct {
			Unrouted   *int `json:"unrouted"`
			Violations int  `json:"violations"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return 0, 0, fmt.Errorf("parse fastroute report %s: %w", path, err)
	}
	if r.Stats.Unrouted == nil {
		return 0, 0, fmt.Errorf("fastroute report %s has no stats.unrouted", path)
	}
	return *r.Stats.Unrouted, r.Stats.Violations, nil
}

// runFastroute routes dsn, then continues from the last session with
// --initial-session while connections remain unrouted (at most o.rounds
// continuation runs). It returns the last session written.
func runFastroute(o fastrouteOpts, dsn, base string, stderr io.Writer) (string, []fastrouteRun, error) {
	var runs []fastrouteRun
	initial := ""
	for round := 0; round <= o.rounds; round++ {
		ses := fmt.Sprintf("%s.r%d.ses", base, round)
		report := fmt.Sprintf("%s.r%d-report.json", base, round)
		args := fastrouteArgs(o, dsn, ses, report, initial)
		fmt.Fprintf(stderr, "fastroute round %d: %s %s\n", round, o.bin, strings.Join(args, " "))
		ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
		cmd := exec.CommandContext(ctx, o.bin, args...)
		cmd.Stdout, cmd.Stderr = stderr, stderr
		// fastroute writes its best board on SIGTERM; give it time to.
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
		cmd.WaitDelay = 30 * time.Second
		start := time.Now()
		err := cmd.Run()
		cancel()
		run := fastrouteRun{Round: round, Session: ses, Report: report, Seconds: time.Since(start).Round(time.Second).Seconds()}
		if _, serr := os.Stat(ses); serr != nil {
			if err == nil {
				err = fmt.Errorf("fastroute wrote no session %s", ses)
			}
			return "", runs, fmt.Errorf("fastroute round %d: %w", round, err)
		}
		if err != nil {
			fmt.Fprintf(stderr, "fastroute round %d ended with %v; using its best session so far\n", round, err)
		}
		u, v, rerr := readFastrouteReport(report)
		if rerr != nil {
			runs = append(runs, run)
			return ses, runs, rerr
		}
		run.Unrouted, run.Violations = u, v
		runs = append(runs, run)
		fmt.Fprintf(stderr, "fastroute round %d: %d unrouted, %d violation(s), %.0f s\n", round, u, v, run.Seconds)
		if u == 0 || err != nil {
			return ses, runs, nil
		}
		initial = ses
	}
	return runs[len(runs)-1].Session, runs, nil
}

// ── autoroute ───────────────────────────────────────────────────────────────

func newPcbAutorouteCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var routerCmd, fastrouteBin, scriptPath string
	var keep, rawDSN, noRepair, noPost, ripUp bool
	var fx dsnFixFlags
	var fo fastrouteOpts
	var schFiles []string
	var forceReason, forceUnsafeReason string
	c := &cobra.Command{
		Use:   "autoroute",
		Short: "Auto-route the active PCB via an external router (DSN→fix→route→SES→import→repair→DRC)",
		Long: `Orchestrate an external-router round-trip on the active PCB:

  1. export the DSN and repair EasyEDA's export defects (see 'pcb dsn-fix';
     --raw-dsn skips);
  2. route it: --router fastroute (preset) or --router '<cmd> {in} {out}'
     (or FREEROUTING_CMD); without a router the DSN is exported and the
     command stops;
  3. import the SES, then repair EasyEDA's importer defects (see
     'pcb ses-repair'; --no-repair skips);
  4. pour rebuild → save → reload → pour rebuild → native DRC → pad-net diff
     (--sch-connectivity) (--no-post skips).

The router is never bundled or downloaded. fastroute
(github.com/parisxmas/fastroute, GPLv3) runs as a separate process found via
--fastroute-bin, $FASTROUTE_BIN or PATH; install it with
scripts/install-fastroute.sh, which verifies the release SHA256SUMS.txt.
With the preset, continuation runs (--initial-session) are added while
connections remain unrouted (--continue).

Measured on a 4-layer 150x110 mm board (199 parts, 463 connections):
fastroute ~131 s for a first full route, 510–566 s with --multi-start 8,
continuation runs closing the rest; pcb auto took 594–1840 s per variant.

A summary JSON is printed to stdout; progress goes to stderr.

` + viaToPourNote,
		Args: cobra.NoArgs,
		Example: `  pcbpilot pcb autoroute --router fastroute --rip-up --sch-connectivity p1.json
  pcbpilot pcb autoroute --router fastroute --multi-start 8 --escapes u8-gnd-escapes.json
  pcbpilot pcb autoroute --router 'java -jar freerouting.jar -de {in} -do {out}'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			summary := map[string]any{}
			emit := func(err error) error {
				_ = writeJSON(stdout, summary)
				return err
			}
			preset := routerCmd == "fastroute"
			if preset {
				bin, err := resolveFastroute(fastrouteBin)
				if err != nil {
					return err
				}
				fo.bin = bin
				if !cmd.Flags().Changed("router-timeout") {
					fo.timeout = 45 * time.Minute
				}
			}

			// 1. Export, then fix.
			res, err := requestActionTimed(cfg, "pcb.export.dsn", *window, map[string]any{}, 5*time.Minute)
			if err != nil {
				return err
			}
			dsnPath := ""
			for _, a := range res.Artifacts {
				if a.Path != "" {
					dsnPath = a.Path
					break
				}
			}
			if dsnPath == "" {
				return fmt.Errorf("export-dsn returned no file (PCB empty or no nets? run `pcb import-changes` first)")
			}
			fmt.Fprintf(stderr, "DSN exported: %s\n", dsnPath)
			summary["dsn"] = dsnPath
			dsnBytes, err := os.ReadFile(dsnPath)
			if err != nil {
				return err
			}
			dsnText := string(dsnBytes)
			if !rawDSN {
				opt, err := fx.options()
				if err != nil {
					return err
				}
				if opt.CopperLayers == 0 {
					if n, lerr := fetchCopperLayerCount(cfg, *window); lerr == nil {
						opt.CopperLayers = n
					}
				}
				fixed, rep, err := specctra.FixDSN(dsnText, opt)
				if err != nil {
					return fmt.Errorf("dsn-fix: %w", err)
				}
				dsnPath = strings.TrimSuffix(dsnPath, ".dsn") + "-fixed.dsn"
				if err := os.WriteFile(dsnPath, []byte(fixed), 0o644); err != nil {
					return err
				}
				dsnText = fixed
				summary["dsnFixed"], summary["dsnFix"] = dsnPath, rep
				fmt.Fprintf(stderr, "DSN fixed: %s (%d class name(s) re-quoted, layers %v added, %d padstack(s) patched, %d edge keep-out(s), %d escape(s))\n",
					dsnPath, rep.QuotedClasses, rep.AddedLayers, rep.PatchedPadstacks, rep.EdgeKeepouts, rep.Escapes)
				if opt.PlaneNet != "" {
					fmt.Fprintln(stderr, "warning: --gnd-plane — "+viaToPourNote)
				}
			}

			// 2. Route.
			base := strings.TrimSuffix(dsnPath, ".dsn")
			var sesPath string
			var sessions []string
			if !keep {
				defer func() {
					for _, f := range sessions {
						_ = os.Remove(f)
					}
				}()
			}
			if preset {
				ses, runs, err := runFastroute(fo, dsnPath, base, stderr)
				summary["router"], summary["routerRuns"] = "fastroute", runs
				for _, r := range runs {
					sessions = append(sessions, r.Session)
				}
				if err != nil {
					return emit(err)
				}
				sesPath = ses
				if n := len(runs); n > 0 && runs[n-1].Unrouted > 0 {
					fmt.Fprintf(stderr, "warning: %d connection(s) still unrouted after %d run(s); importing the best session\n", runs[n-1].Unrouted, n)
				}
			} else {
				tmpl := routerCmd
				if tmpl == "" {
					tmpl = os.Getenv("FREEROUTING_CMD")
				}
				if tmpl == "" {
					fmt.Fprintf(stderr, "no --router / FREEROUTING_CMD set — DSN exported, stopping.\n"+
						"  route it externally (e.g. --router fastroute), then: pcbpilot pcb import-autoroute <file.ses> && pcbpilot pcb ses-repair <file.ses> --dsn %s\n", dsnPath)
					return emit(nil)
				}
				sesPath = base + ".ses"
				runStr := strings.NewReplacer("{in}", dsnPath, "{out}", sesPath).Replace(tmpl)
				fmt.Fprintf(stderr, "routing: %s\n", runStr)
				summary["router"] = runStr
				routerCtx, cancelRouter := context.WithTimeout(context.Background(), fo.timeout)
				defer cancelRouter()
				if err := runExternalRouter(routerCtx, runStr, stderr); err != nil {
					if routerCtx.Err() != nil {
						return fmt.Errorf("external router timed out after %s: %w", fo.timeout, routerCtx.Err())
					}
					return fmt.Errorf("external router failed: %w", err)
				}
				if _, err := os.Stat(sesPath); err != nil {
					return fmt.Errorf("router produced no SES at %s (check the command's {out})", sesPath)
				}
			}
			summary["ses"] = sesPath
			if !preset {
				sessions = append(sessions, sesPath)
			}

			// 3. Import, then repair.
			data, err := os.ReadFile(sesPath)
			if err != nil {
				return emit(fmt.Errorf("read SES: %w", err))
			}
			if ripUp {
				fmt.Fprintln(stderr, "rip-up: removing existing unlocked routing before import")
				if _, err := requestActionTimed(cfg, "pcb.route.rip_up", *window, map[string]any{}, 10*time.Minute); err != nil {
					return emit(fmt.Errorf("rip-up: %w", err))
				}
			}
			fmt.Fprintf(stderr, "importing SES (%d bytes) → tracks/vias\n", len(data))
			if _, err := requestActionTimed(cfg, "pcb.import_autoroute", *window, map[string]any{
				"fileBase64": base64.StdEncoding.EncodeToString(data),
				"format":     "ses",
				"fileName":   filepath.Base(sesPath),
			}, 30*time.Minute); err != nil {
				return emit(fmt.Errorf("import SES: %w", err))
			}
			if !noRepair {
				rep, err := repairImportedSession(cfg, *window, string(data), dsnText, false, stderr)
				summary["repair"] = rep
				if err != nil {
					return emit(fmt.Errorf("ses-repair: %w", err))
				}
			}

			// 4. Post-import checks.
			if noPost {
				fmt.Fprintln(stderr, "--no-post: skipped pour rebuild / save / reload / DRC; run them before trusting the board")
				return emit(nil)
			}
			post, err := postImportChecks(cfg, *window, schFiles, scriptPath, stderr)
			summary["post"] = post
			return emit(err)
		},
	}
	c.Flags().StringVar(&routerCmd, "router", "", "'fastroute' (preset) or an external router command with {in}/{out} (or FREEROUTING_CMD env)")
	c.Flags().StringVar(&fastrouteBin, "fastroute-bin", "", "fastroute executable (default: $FASTROUTE_BIN, then PATH)")
	c.Flags().IntVar(&fo.multiStart, "multi-start", 0, "fastroute --multi-start=N (0 = fastroute default)")
	c.Flags().Float64Var(&fo.minTraceUm, "min-trace-um", 152, "fastroute --router.min_trace_width_um: never neck down below this (0 = not passed)")
	c.Flags().DurationVar(&fo.maxTime, "max-time", 0, "fastroute --max-time per run (0 = none)")
	c.Flags().IntVar(&fo.rounds, "continue", 2, "fastroute continuation runs (--initial-session) while connections remain unrouted")
	c.Flags().DurationVar(&fo.timeout, "router-timeout", 10*time.Minute, "hard limit per router run (default 45m with --router fastroute)")
	c.Flags().BoolVar(&rawDSN, "raw-dsn", false, "route the unmodified EasyEDA DSN (skip the export fixes)")
	c.Flags().BoolVar(&noRepair, "no-repair", false, "skip the SES import repair")
	c.Flags().BoolVar(&noPost, "no-post", false, "skip pour rebuild / save / reload / DRC / pad-net diff")
	c.Flags().BoolVar(&ripUp, "rip-up", false, "rip up existing unlocked routing before importing (the session already contains it)")
	c.Flags().StringArrayVar(&schFiles, "sch-connectivity", nil, "schematic connectivity JSON for the pad-net diff (repeat per page)")
	c.Flags().StringVar(&scriptPath, "pad-net-diff-script", "", "path to pad-net-diff.py (auto-detected if omitted)")
	c.Flags().BoolVar(&keep, "keep", false, "keep the routed SES file(s)")
	fx.register(c.Flags())
	c.Flags().StringVar(&forceReason, "force", "", "deprecated compatibility option; workflow stages no longer gate routing")
	c.Flags().StringVar(&forceUnsafeReason, "force-unsafe", "", "deprecated compatibility option; workflow stages no longer gate routing")
	return c
}
