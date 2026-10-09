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
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	fs.StringVar(&f.escapesFile, "escapes", "", "JSON fixed escapes: [{net,layer,widthMil,path:[[x,y],..],via}] or bare [[start],[end]] pairs (= 10 mil GND on TopLayer + via at the end); mil, DSN layer names")
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
			// Short form: bare [[start],[end]] pairs = a 10 mil GND stub on
			// TopLayer ending in a via (the Gas Module v6A3-escapes.json shape).
			opt.Escapes = nil
			var pairs [][][2]float64
			if json.Unmarshal(data, &pairs) != nil {
				return opt, fmt.Errorf("parse --escapes %s: %w", f.escapesFile, err)
			}
			for _, p := range pairs {
				opt.Escapes = append(opt.Escapes, specctra.Escape{Net: "GND", Layer: "TopLayer", WidthMil: 10, Path: p, Via: true})
			}
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
	// Reconcile is the pass against the session after the repair: missing
	// pieces and vias created, stray tracks (layers the board lacks) deleted;
	// Unresolved is what a second comparison still finds missing.
	Reconcile  *specctra.Reconcile `json:"reconcile,omitempty"`
	Unresolved *specctra.Reconcile `json:"unresolved,omitempty"`
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

// replaceTracks deletes tracks, then creates their replacements. The order
// matters: EasyEDA merges a new track into an existing same-net, same-layer
// track it overlaps and keeps only the widest (probed live 2026-10-06 on
// 3.2.149: a wider copy absorbs the old track and its id disappears; a
// narrower copy, or a narrow piece inside a wide track, is itself absorbed).
// Creating first therefore made `pcb widen` delete ids that no longer
// existed and made the neck-down restore lose the narrow pieces, then delete
// the wide track. Collinear pieces that only touch end to end do not merge.
// When a replacement piece cannot be created, the original track is
// recreated and the failure reported.
func replaceTracks(cfg *appConfig, window string, fixes []specctra.TrackFix) (created int, failures []map[string]string, err error) {
	del := make([]string, len(fixes))
	for i, f := range fixes {
		del[i] = f.Delete.ID
	}
	for i := 0; i < len(del); i += 200 {
		chunk := del[i:min(i+200, len(del))]
		if _, err := requestActionTimed(cfg, "pcb.route.delete", window,
			map[string]any{"primitiveIds": chunk, "kind": "track"}, 5*time.Minute); err != nil {
			return 0, nil, fmt.Errorf("delete replaced tracks: %w", err)
		}
	}
	for _, f := range fixes {
		var ferr error
		for _, nt := range f.Create {
			if ferr = createPcbTrack(cfg, window, nt); ferr != nil {
				break
			}
			created++
		}
		if ferr == nil {
			continue
		}
		// Put the original back so no connection is lost; it overlaps the
		// pieces already created and EasyEDA merges them.
		o := f.Delete
		restored := createPcbTrack(cfg, window, specctra.NewTrack{Net: o.Net, Layer: o.Layer, X1: o.X1, Y1: o.Y1, X2: o.X2, Y2: o.Y2, Width: o.Width}) == nil
		failures = append(failures, map[string]string{"track": o.ID, "net": o.Net, "error": ferr.Error(), "restored": fmt.Sprint(restored)})
	}
	return created, failures, nil
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
// fixed wiring that never reaches the session. The replaced tracks are
// deleted before the new pieces are created (see replaceTracks).
func repairImportedSession(cfg *appConfig, window string, ses, dsn string, dryRun bool, stderr io.Writer) (*sesRepairSummary, error) {
	sum, err := repairImportSteps(cfg, window, ses, dsn, dryRun, stderr)
	if err != nil || dryRun {
		return sum, err
	}
	err = reconcileWithSession(cfg, window, ses, dsn, sum, stderr)
	return sum, err
}

// reconcileWithSession makes the board carry every segment and via of the
// session: after the importer and the repair, compare, create what is
// missing, delete tracks on layers the board does not have, then compare
// once more and record whatever is still missing as unresolved.
func reconcileWithSession(cfg *appConfig, window, ses, dsn string, sum *sesRepairSummary, stderr io.Writer) error {
	wiring, err := specctra.ParseSES(ses)
	if err != nil {
		return err
	}
	copper, err := fetchCopperLayerCount(cfg, window)
	if err != nil {
		return fmt.Errorf("reconcile: copper layer count: %w", err)
	}
	ids := specctra.CopperLayerIDs(copper)
	viaDia := 0.0
	if dsn != "" {
		viaDia = specctra.ViaDiameterMil(dsn, specctra.ViaPadstack(dsn))
	}
	compare := func() (specctra.Reconcile, error) {
		if err := saveAndReload(cfg, window); err != nil {
			return specctra.Reconcile{}, err
		}
		tracks, err := listPcbTracks(cfg, window)
		if err != nil {
			return specctra.Reconcile{}, err
		}
		vpts, vnets, err := listPcbVias(cfg, window)
		if err != nil {
			return specctra.Reconcile{}, err
		}
		return specctra.PlanReconcile(wiring, tracks, vpts, vnets, ids, viaDia), nil
	}
	r, err := compare()
	if err != nil {
		return err
	}
	sum.Reconcile = &r
	fmt.Fprintf(stderr, "reconcile: %d session segment(s) and %d via(s) missing on the board, %d stray track(s) on non-copper layers\n",
		len(r.MissingTracks), len(r.MissingVias), len(r.Stray))
	if len(r.MissingTracks)+len(r.MissingVias)+len(r.Stray) == 0 {
		return nil
	}
	for _, v := range r.MissingVias {
		payload := map[string]any{"x": v.X, "y": v.Y, "net": v.Net}
		if v.DiameterMil > 0 {
			payload["diameter"] = v.DiameterMil
		}
		if _, err := requestAction(cfg, "pcb.via.create", window, payload); err != nil {
			sum.Failures = append(sum.Failures, map[string]string{"reconcileVia": v.Net, "error": err.Error()})
		}
	}
	for _, t := range r.MissingTracks {
		if err := createPcbTrack(cfg, window, t); err != nil {
			sum.Failures = append(sum.Failures, map[string]string{"reconcileTrack": t.Net, "error": err.Error()})
		}
	}
	if len(r.Stray) > 0 {
		ids := make([]string, len(r.Stray))
		for i, t := range r.Stray {
			ids[i] = t.ID
		}
		if _, err := requestActionTimed(cfg, "pcb.route.delete", window, map[string]any{"primitiveIds": ids, "kind": "track"}, 5*time.Minute); err != nil {
			sum.Failures = append(sum.Failures, map[string]string{"strayDelete": strings.Join(ids, ","), "error": err.Error()})
		}
	}
	again, err := compare()
	if err != nil {
		return err
	}
	if len(again.MissingTracks)+len(again.MissingVias)+len(again.Stray) > 0 {
		sum.Unresolved = &again
		fmt.Fprintf(stderr, "reconcile: still %d segment(s), %d via(s) missing and %d stray track(s) after the fix\n",
			len(again.MissingTracks), len(again.MissingVias), len(again.Stray))
	}
	return nil
}

func repairImportSteps(cfg *appConfig, window string, ses, dsn string, dryRun bool, stderr io.Writer) (*sesRepairSummary, error) {
	wiring, err := specctra.ParseSES(ses)
	if err != nil {
		return nil, err
	}
	for _, d := range wiring.Dots {
		fmt.Fprintf(stderr, "ses-repair: skipped a one-point path (no length): %s\n", d)
	}
	if !dryRun {
		if err := saveAndReload(cfg, window); err != nil {
			return nil, err
		}
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
		n, failures, err := replaceTracks(cfg, window, plan.Fixes)
		sum.TracksDeleted, sum.TracksCreated, sum.Failures = len(plan.Fixes), n, failures
		if err != nil {
			return sum, err
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
	if !dryRun && len(plan.Fixes) > 0 {
		if err := saveAndReload(cfg, window); err != nil {
			return sum, err
		}
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
	PourRebuilt bool           `json:"pourRebuilt"`
	Saved       bool           `json:"saved"`
	Reloaded    bool           `json:"reloaded"`
	DRCPassed   bool           `json:"drcPassed"`
	DRCTotal    int            `json:"drcViolations"`
	DRCCounts   map[string]int `json:"drcCounts,omitempty"`
	// DRCList is every native violation (class, objects, net, layer, mil).
	DRCList          []drcFlatViolation `json:"drcList,omitempty"`
	ConnectionErrors int                `json:"connectionErrors"`
	PadNetDiff       any                `json:"padNetDiff"`
	Notes            []string           `json:"notes"`
}

var padNetDiffAsset = skillAsset{
	name:     "pad-net-diff.py",
	rels:     []string{"pcbpilot/scripts/pad-net-diff.py"},
	flagHint: "--pad-net-diff-script",
}

// saveAndReload saves the active PCB and closes + reopens it. After writes
// the editor may serve stale reads (staleRisk) until a reload: a line list
// taken right after deleting tracks still returned the deleted ids (Gas
// Module V5 B 2026-10-06, pcb widen). Every read that drives a delete goes
// after one of these.
func saveAndReload(cfg *appConfig, window string) error {
	if err := saveRetrying(cfg, window, "pcb.save", saveRetryBudget, nil); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	_, active, win, err := discoverDocs(cfg, window)
	if err != nil {
		return fmt.Errorf("reload: %w", err)
	}
	if active == "" {
		return fmt.Errorf("reload: no active document")
	}
	if _, err := reloadDocumentByUUID(cfg, win, active); err != nil {
		return fmt.Errorf("reload: %w", err)
	}
	return nil
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
	step("save + reload")
	if err := saveAndReload(cfg, window); err != nil {
		return sum, err
	}
	sum.Saved, sum.Reloaded = true, true
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
	sum.DRCPassed, sum.DRCTotal, sum.DRCCounts, sum.DRCList = flat.Passed, flat.Total, flat.Counts, flat.Violations
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

// fastrouteLookPath is exec.LookPath; tests replace it so `pcb auto run`
// keeps the internal router whatever is installed on the machine.
var fastrouteLookPath = exec.LookPath

// resolveFastroute finds the binary: explicit flag, $FASTROUTE_BIN, the
// adopted version (~/.pcbpilot/fastroute/current), PATH.
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
	// The version adopted by scripts/fastroute-upgrade-check.sh.
	if home, err := os.UserHomeDir(); err == nil {
		if p, err := filepath.EvalSymlinks(filepath.Join(home, ".pcbpilot", "fastroute", "current")); err == nil {
			return p, nil
		}
	}
	if p, err := fastrouteLookPath("fastroute"); err == nil {
		return p, nil
	}
	return "", errors.New(fastrouteInstallHint)
}

type fastrouteOpts struct {
	bin        string
	multiStart int
	minTraceUm float64
	noNeckdown []string
	threads    int
	// noOptimizer: --router.optimizer.enabled=false. optThreshold > 0:
	// --router.optimizer.optimization_improvement_threshold (percent per
	// pass below which the optimizer stops).
	noOptimizer  bool
	optThreshold float64
	pairsFile    string
	tuneFile     string
	maxTime      time.Duration
	rounds       int
	timeout      time.Duration
}

// fastrouteArgs builds one fastroute invocation.
func fastrouteArgs(o fastrouteOpts, dsn, ses, report, initial string) []string {
	args := []string{"-de", dsn, "-do", ses, "--report=" + report, "--diagnose"}
	ms := o.multiStart
	if ms == 0 && o.threads == 1 {
		ms = 1 // fastroute's default multi-start (4) runs its orders in parallel
	}
	if ms > 0 {
		args = append(args, "--multi-start="+strconv.Itoa(ms))
	}
	if o.minTraceUm > 0 {
		args = append(args, "--router.min_trace_width_um="+strconv.FormatFloat(o.minTraceUm, 'f', -1, 64))
	}
	if o.threads > 0 {
		n := strconv.Itoa(o.threads)
		args = append(args, "--router.autorouter.max_threads="+n, "--router.optimizer.max_threads="+n)
	}
	if o.noOptimizer {
		args = append(args, "--router.optimizer.enabled=false")
	}
	if o.optThreshold > 0 {
		args = append(args, "--router.optimizer.optimization_improvement_threshold="+strconv.FormatFloat(o.optThreshold, 'f', -1, 64))
	}
	if o.pairsFile != "" {
		args = append(args, "--pairs="+o.pairsFile)
	}
	if o.tuneFile != "" {
		args = append(args, "--tune="+o.tuneFile)
	}
	if len(o.noNeckdown) > 0 {
		args = append(args, "--no-neckdown-classes="+strings.Join(o.noNeckdown, ","))
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
	// Status is ok | crashed | timeout. A run whose report could not be read
	// has unknown counts (-1), never 0.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Retry marks the single-threaded rerun after a crash.
	Retry bool `json:"singleThreadRetry,omitempty"`
	// Fixable are the clearance violations the router itself marks fixable
	// (pre-existing pin-pin overlaps are unfixable); they reach native DRC.
	Fixable     int      `json:"fixableViolations"`
	FixableList []string `json:"fixableList,omitempty"`
	// Blocked: unrouted connections fastroute --diagnose classed "blocked"
	// (unroutable alone on the board; the rest are congestion).
	Blocked     int      `json:"blocked"`
	BlockedList []string `json:"blockedList,omitempty"`
	// Version, UnroutedList, BlockedConns feed routeResult (fastrouteResult).
	Version      string      `json:"version,omitempty"`
	UnroutedList []routeConn `json:"unroutedList,omitempty"`
	BlockedConns []routeConn `json:"-"`
}

// frReport is what pcbpilot reads from a fastroute --report file.
type frReport struct {
	Unrouted, Violations, Fixable int
	FixableList                   []string
	// Blocked: unrouted connections --diagnose found unroutable even alone
	// on the loaded board (pins, keep-outs, fixed wiring) — placement or
	// escapes must change; rerunning the router does not help them.
	Blocked     int
	BlockedList []string
	Version     string
	Conns       []routeConn // every unrouted connection
	BlockedC    []routeConn
}

// readFastrouteReport extracts the counts of a fastroute --report file. Its
// xy are DSN units / 1000 with y negated (inches on an EasyEDA DSN, mm on a
// KiCad DSN); milPerUnit (specctra.ReportMilPerUnit) converts them, and
// FixableList gives them in board mil.
func readFastrouteReport(path string, milPerUnit float64) (frReport, error) {
	var out frReport
	data, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	type item struct {
		Kind      string `json:"kind"`
		Net       string `json:"net"`
		Component string `json:"component"`
		Pin       string `json:"pin"`
	}
	var r struct {
		Fastroute string `json:"fastroute"`
		Stats     struct {
			Unrouted   *int `json:"unrouted"`
			Violations int  `json:"violations"`
		} `json:"stats"`
		Unrouted []struct {
			Net       string `json:"net"`
			From      item   `json:"from"`
			To        item   `json:"to"`
			Diagnosis struct {
				Class string `json:"class"`
			} `json:"diagnosis"`
		} `json:"unrouted"`
		Clearance []struct {
			Layer     string     `json:"layer"`
			XY        [2]float64 `json:"xy"`
			Unfixable bool       `json:"unfixable"`
			First     item       `json:"first"`
			Second    item       `json:"second"`
		} `json:"clearance_violations"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return out, fmt.Errorf("parse fastroute report %s: %w", path, err)
	}
	if r.Stats.Unrouted == nil {
		return out, fmt.Errorf("fastroute report %s has no stats.unrouted", path)
	}
	out.Unrouted, out.Violations = *r.Stats.Unrouted, r.Stats.Violations
	out.Version = r.Fastroute
	for _, u := range r.Unrouted {
		c := routeConn{Net: u.Net, From: u.From.Component + "." + u.From.Pin, To: u.To.Component + "." + u.To.Pin}
		out.Conns = append(out.Conns, c)
		if u.Diagnosis.Class == "blocked" {
			out.BlockedC = append(out.BlockedC, c)
			out.Blocked++
			out.BlockedList = append(out.BlockedList, fmt.Sprintf("%s %s.%s–%s.%s", u.Net, u.From.Component, u.From.Pin, u.To.Component, u.To.Pin))
		}
	}
	for _, c := range r.Clearance {
		if c.Unfixable {
			continue
		}
		out.Fixable++
		out.FixableList = append(out.FixableList, fmt.Sprintf("%s at (%.1f, %.1f) mil: %s %s / %s %s",
			c.Layer, c.XY[0]*milPerUnit, -c.XY[1]*milPerUnit, c.First.Kind, c.First.Net, c.Second.Kind, c.Second.Net))
	}
	return out, nil
}

// runFastroute routes dsn, then continues from the last session with
// --initial-session while connections remain unrouted or fixable clearance
// violations remain — at most o.rounds continuation runs, and only while a
// run improves on the previous one. A run that crashes (fastroute 0.1.7
// panics in its parallel autorouter: "MinAreaTree ... free list corrupted",
// Gas Module v9) is repeated once single-threaded; its counts are unknown
// until a report exists. It returns the last good session.
func runFastroute(o fastrouteOpts, dsn, base string, stderr io.Writer) (string, []fastrouteRun, error) {
	var runs []fastrouteRun
	initial := ""
	for round := 0; round <= o.rounds; round++ {
		ses := fmt.Sprintf("%s.r%d.ses", base, round)
		report := fmt.Sprintf("%s.r%d-report.json", base, round)
		run := fastrouteOnce(o, dsn, ses, report, initial, round, stderr)
		if run.Status == "crashed" {
			runs = append(runs, run)
			single := o
			single.multiStart, single.threads = 1, 1
			fmt.Fprintf(stderr, "fastroute round %d crashed (%s); retrying single-threaded\n", round, run.Error)
			run = fastrouteOnce(single, dsn, ses, report, initial, round, stderr)
			run.Retry = true
		}
		runs = append(runs, run)
		if run.Status != "ok" {
			// No trustworthy result from this round: fall back to the last
			// good one, or fail when there is none.
			for i := len(runs) - 1; i >= 0; i-- {
				if runs[i].Status == "ok" {
					fmt.Fprintf(stderr, "fastroute round %d failed (%s); using round %d\n", round, run.Error, runs[i].Round)
					return runs[i].Session, runs, nil
				}
			}
			return "", runs, fmt.Errorf("fastroute round %d: %s", round, run.Error)
		}
		fmt.Fprintf(stderr, "fastroute round %d: %d unrouted, %d violation(s) (%d fixable), %.0f s\n", round, run.Unrouted, run.Violations, run.Fixable, run.Seconds)
		if run.Unrouted == 0 && run.Fixable == 0 {
			return ses, runs, nil
		}
		if run.Fixable == 0 && run.Blocked >= run.Unrouted {
			// --diagnose: every remaining connection is blocked by geometry
			// (pins, keep-outs, fixed wiring) — another router run cannot
			// route it; placement / escapes must change.
			fmt.Fprintf(stderr, "fastroute round %d: all %d unrouted connection(s) are blocked (--diagnose); no continuation run\n", round, run.Unrouted)
			return ses, runs, nil
		}
		if prev := lastOK(runs[:len(runs)-1]); prev != nil && !runImproved(*prev, run) {
			fmt.Fprintf(stderr, "fastroute round %d did not improve on round %d; stopping\n", round, prev.Round)
			return ses, runs, nil
		}
		initial = ses
	}
	return lastOK(runs).Session, runs, nil
}

// reportMilPerUnit reads the DSN's unit (specctra.ReportMilPerUnit); an
// unreadable DSN is taken as mil (EasyEDA).
func reportMilPerUnit(dsnPath string) float64 {
	f, err := os.Open(dsnPath)
	if err != nil {
		return 1000
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := io.ReadFull(f, buf)
	return specctra.ReportMilPerUnit(string(buf[:n]))
}

func lastOK(runs []fastrouteRun) *fastrouteRun {
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Status == "ok" {
			return &runs[i]
		}
	}
	return nil
}

// fastrouteOnce runs fastroute once. Status ok needs a clean exit (or a
// timeout that still wrote its report) plus a readable report and session.
func fastrouteOnce(o fastrouteOpts, dsn, ses, report, initial string, round int, stderr io.Writer) fastrouteRun {
	_ = os.Remove(report)
	args := fastrouteArgs(o, dsn, ses, report, initial)
	fmt.Fprintf(stderr, "fastroute round %d: %s %s\n", round, o.bin, strings.Join(args, " "))
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.bin, args...)
	cmd.Stdout, cmd.Stderr = stderr, stderr
	// fastroute writes its best board on SIGINT/SIGTERM; give it time to.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 30 * time.Second
	start := time.Now()
	err := cmd.Run()
	run := fastrouteRun{Round: round, Session: ses, Report: report, Seconds: time.Since(start).Round(time.Second).Seconds(),
		Status: "ok", Unrouted: -1, Violations: -1, Fixable: -1}
	switch {
	case err != nil && ctx.Err() != nil:
		run.Status, run.Error = "timeout", fmt.Sprintf("timed out after %s", o.timeout)
	case err != nil:
		run.Status, run.Error = "crashed", err.Error()
	}
	rep, rerr := readFastrouteReport(report, reportMilPerUnit(dsn))
	if _, serr := os.Stat(ses); serr != nil && rerr == nil {
		rerr = fmt.Errorf("no session %s", ses)
	}
	if rerr != nil {
		if run.Status == "ok" {
			run.Status = "crashed"
		}
		if run.Error == "" {
			run.Error = rerr.Error()
		} else {
			run.Error += "; " + rerr.Error()
		}
		return run
	}
	// A timed-out run that wrote its report is a usable best-so-far result.
	run.Status = "ok"
	run.Unrouted, run.Violations, run.Fixable, run.FixableList = rep.Unrouted, rep.Violations, rep.Fixable, rep.FixableList
	run.Blocked, run.BlockedList = rep.Blocked, rep.BlockedList
	run.Version, run.UnroutedList, run.BlockedConns = rep.Version, rep.Conns, rep.BlockedC
	return run
}

// runImproved: fewer unrouted, or as many unrouted and fewer fixable violations.
func runImproved(prev, cur fastrouteRun) bool {
	return cur.Unrouted < prev.Unrouted || (cur.Unrouted == prev.Unrouted && cur.Fixable < prev.Fixable)
}

// ── autoroute ───────────────────────────────────────────────────────────────

// autorouteOpts are the knobs shared by `pcb autoroute` and `pcb auto route`.
type autorouteOpts struct {
	routerCmd    string
	fastrouteBin string
	fo           fastrouteOpts
	timeoutSet   bool
	fx           dsnFixFlags
	rawDSN       bool
	noRepair     bool
	ripUp        bool
	keep         bool
	noAutoEsc    bool
	noPreEscape  bool
	escapeRounds int
	intentPath   string
	minTraceSet  bool
}

// register adds the flags; router, rounds and ripUp are the command's defaults.
func (o *autorouteOpts) register(fs *pflag.FlagSet, router string, rounds int, ripUp bool) {
	fs.StringVar(&o.routerCmd, "router", router, "'fastroute' (preset) or an external router command with {in}/{out} (or FREEROUTING_CMD env)")
	fs.StringVar(&o.fastrouteBin, "fastroute-bin", "", "fastroute executable (default: $FASTROUTE_BIN, then PATH)")
	fs.IntVar(&o.fo.multiStart, "multi-start", 0, "fastroute --multi-start=N (0 = 1 with --threads 1, else fastroute's default)")
	fs.Float64Var(&o.fo.minTraceUm, "min-trace-um", 152, "fastroute --router.min_trace_width_um: never neck down below this (0 = not passed; with --intent the intent's narrowest widthMil.min is used unless this is set)")
	fs.StringVar(&o.intentPath, "intent", "", "intent.json (intent derive): pre-route gate — write its rules (pcb rules apply), raise the DSN net classes to every net's outer/inner width and clearance, forbid neck-down where widthMil.min = outer; route only when every net passes")
	fs.DurationVar(&o.fo.maxTime, "max-time", 0, "fastroute --max-time per run (0 = none)")
	fs.IntVar(&o.fo.threads, "threads", 1, "fastroute autorouter and optimizer threads; 1 (default) also sets --multi-start=1 unless given. fastroute 0.1.7 panics in its parallel autorouter (MinAreaTree free list corrupted) — Gas Module v9: 8 threads crashed after 244 s, 1 thread routed in 102 s. 0 = fastroute default; a crashed run is always retried with 1")
	fs.IntVar(&o.fo.rounds, "continue", rounds, "fastroute continuation runs (--initial-session) while connections remain unrouted")
	fs.DurationVar(&o.fo.timeout, "router-timeout", 10*time.Minute, "hard limit per router run (default 45m with --router fastroute)")
	fs.BoolVar(&o.rawDSN, "raw-dsn", false, "route the unmodified EasyEDA DSN (skip the export fixes)")
	fs.BoolVar(&o.noRepair, "no-repair", false, "skip the SES import repair")
	fs.BoolVar(&o.ripUp, "rip-up", ripUp, "rip up existing unlocked routing before importing (the session already contains it)")
	fs.BoolVar(&o.keep, "keep", false, "keep the routed SES file(s)")
	fs.BoolVar(&o.noAutoEsc, "no-auto-escapes", false, "fastroute: do not add inward escapes for ground pins it reports blocked on fine-pitch parts")
	fs.BoolVar(&o.noPreEscape, "no-pre-escape-gnd", false, "fastroute: do not reserve inward escapes for every ground pin of fine-pitch parts with more than 64 pins before the first route")
	fs.IntVar(&o.escapeRounds, "escape-rounds", 3, "fastroute: at most this many re-routes adding escapes for newly blocked ground pins (each kept only if better)")
	o.fx.register(fs)
}

// runAutorouteFlow: export DSN → fix → route → import SES → repair. It fills
// summary as it goes, so a caller can print what happened before a failure.
// routed=false means no router was configured (DSN exported only).
func runAutorouteFlow(cfg *appConfig, window string, o autorouteOpts, summary map[string]any, stderr io.Writer) (routed bool, sessions []string, err error) {
	preset := o.routerCmd == "fastroute"
	if preset {
		bin, err := resolveFastroute(o.fastrouteBin)
		if err != nil {
			return false, sessions, err
		}
		o.fo.bin = bin
		if !o.timeoutSet {
			o.fo.timeout = 45 * time.Minute
		}
	}

	// 0. Negative (PLANE) inner layers are left out of EasyEDA's DSN and
	// cannot carry tracks. Routing GND as traces needs them SIGNAL; pours go
	// on them afterwards.
	if !o.rawDSN && !o.fx.gndPlane {
		if planes, perr := fetchPcbPlaneLayers(cfg, window); perr == nil && len(planes) > 0 {
			var layers []map[string]any
			for _, pl := range planes {
				layers = append(layers, map[string]any{"id": pl.Layer, "type": "signal", "name": pl.Name})
			}
			fmt.Fprintf(stderr, "stackup: %d PLANE inner layer(s) set to SIGNAL so they can be routed and poured\n", len(layers))
			if _, err := requestActionTimed(cfg, "pcb.stackup.set", window, map[string]any{"layers": layers}, 2*time.Minute); err != nil {
				return false, sessions, fmt.Errorf("set inner layers to signal: %w", err)
			}
			summary["planesToSignal"] = layers
		}
	}

	// Pre-route gate: the intent (schematic + simulation) is written to the
	// board's native rules before anything is exported.
	var reqs map[string]specctra.NetRequirement
	var intent *designIntent
	if o.intentPath != "" {
		in, err := loadDesignIntent(o.intentPath)
		if err != nil {
			return false, sessions, fmt.Errorf("pre-route gate: %w", err)
		}
		call := func(action string, payload any) (map[string]any, error) {
			res, err := requestAction(cfg, action, window, payload)
			if err != nil {
				return nil, err
			}
			if res.Result == nil {
				return map[string]any{}, nil
			}
			return res.Result, nil
		}
		rrep, err := runIntentRules(in, "apply", false, call, stderr)
		summary["intentRules"] = map[string]any{"status": rrep.Status, "verified": rrep.Verified, "plan": rrep.Plan}
		if err != nil {
			return false, sessions, fmt.Errorf("pre-route gate: pcb rules apply --intent %s: %w", o.intentPath, err)
		}
		fmt.Fprintf(stderr, "pre-route gate: intent rules %s\n", rrep.Status)
		reqs, intent = intentRequirements(in), in
	} else if preset {
		fmt.Fprintln(stderr, "warning: no --intent — net widths/clearances come from whatever rules the board has; the intent is not enforced")
	}

	// 1. Export from a saved + reloaded board (placement and stackup writes
	// may still be served stale otherwise), then fix.
	if err := saveAndReload(cfg, window); err != nil {
		return false, sessions, err
	}
	res, err := requestActionTimed(cfg, "pcb.export.dsn", window, map[string]any{}, 5*time.Minute)
	if err != nil {
		return false, sessions, err
	}
	dsnPath := ""
	for _, a := range res.Artifacts {
		if a.Path != "" {
			dsnPath = a.Path
			break
		}
	}
	if dsnPath == "" {
		return false, sessions, fmt.Errorf("export-dsn returned no file (PCB empty or no nets? run `pcb import-changes` first)")
	}
	fmt.Fprintf(stderr, "DSN exported: %s\n", dsnPath)
	summary["dsn"] = dsnPath
	dsnBytes, err := os.ReadFile(dsnPath)
	if err != nil {
		return false, sessions, err
	}
	dsnText := string(dsnBytes)
	rawText := dsnText
	var fixOpt specctra.FixOptions
	var escEnv *escapeEnv
	if !o.rawDSN {
		opt, err := o.fx.options()
		if err != nil {
			return false, sessions, err
		}
		if opt.CopperLayers == 0 {
			if n, lerr := fetchCopperLayerCount(cfg, window); lerr == nil {
				opt.CopperLayers = n
			}
		}
		if opt.EdgeOuterMil > 0 || opt.EdgeInnerMil > 0 {
			// Edge-mounted connector pads stay routable through the bands.
			if snap, err := fetchBoardSnapshot(cfg, window, boardSnapshotOpts{}); err == nil {
				clr := 6.0
				if r := fetchPcbRules(cfg, window).clearanceMil; r > 0 {
					clr = r
				}
				var names []string
				opt.EdgeExempt, names = edgeExemptBoxes(snap, math.Max(opt.EdgeOuterMil, opt.EdgeInnerMil), clr+2)
				summary["edgeExemptPads"] = names
			} else {
				fmt.Fprintf(stderr, "edge keep-out: board unreadable (%v); no connector windows\n", err)
			}
		}
		if preset && !o.noPreEscape {
			if pre, skipped, err := plannedPreEscapes(cfg, window, dsnText, opt.Escapes); err != nil {
				fmt.Fprintf(stderr, "pre-escapes: %v; skipped\n", err)
			} else {
				opt.Escapes = append(opt.Escapes, pre...)
				summary["preEscapes"] = map[string]any{"planned": len(pre), "skipped": skipped}
				fmt.Fprintf(stderr, "pre-escapes: %d ground pin(s) of fine-pitch parts reserved (%d skipped)\n", len(pre), len(skipped))
			}
		}
		if len(reqs) > 0 {
			if esc, skipped, env, err := plannedIntentEscapes(cfg, window, dsnText, reqs, opt.Escapes); err != nil {
				fmt.Fprintf(stderr, "intent escapes: %v; skipped\n", err)
			} else {
				opt.Escapes = append(opt.Escapes, esc...)
				escEnv = env
				summary["intentEscapes"] = map[string]any{"planned": esc, "skipped": skipped}
				fmt.Fprintf(stderr, "intent escapes: %d pad(s) get a fixed escape stub (full width does not leave the pad), %d cannot reach widthMil.min\n", len(esc), len(skipped))
			}
		}
		fixed, rep, rq, err := prepareDSN(dsnText, opt, reqs)
		if err != nil {
			return false, sessions, err
		}
		if rq != nil {
			summary["dsnRequirements"] = rq
			o.fo.noNeckdown = rq.NoNeckdown
			if !o.minTraceSet && rq.MinTraceMil > 0 {
				o.fo.minTraceUm = math.Round(rq.MinTraceMil*25.4*10) / 10
			}
			fmt.Fprintf(stderr, "pre-route gate: %d net requirement(s) met in the DSN (%d class(es) raised, %d added, %d without neck-down, min trace %.1f mil)\n",
				rq.Nets, rq.Classes, rq.NewClasses, len(rq.NoNeckdown), rq.MinTraceMil)
		}
		if intent != nil {
			pairs, tune := intentPairsAndTune(intent)
			base := strings.TrimSuffix(dsnPath, ".dsn")
			if pairs != "" {
				o.fo.pairsFile = base + "-pairs.txt"
				if err := os.WriteFile(o.fo.pairsFile, []byte(pairs), 0o644); err != nil {
					return false, sessions, err
				}
			}
			if tune != "" {
				o.fo.tuneFile = base + "-tune.txt"
				if err := os.WriteFile(o.fo.tuneFile, []byte(tune), 0o644); err != nil {
					return false, sessions, err
				}
			}
			summary["intentPairs"], summary["intentTune"] = pairs, tune
		}
		dsnPath = strings.TrimSuffix(dsnPath, ".dsn") + "-fixed.dsn"
		if err := os.WriteFile(dsnPath, []byte(fixed), 0o644); err != nil {
			return false, sessions, err
		}
		dsnText, fixOpt = fixed, opt
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
	if preset {
		ses, runs, err := runFastroute(o.fo, dsnPath, base, stderr)
		summary["router"], summary["routerRuns"] = "fastroute", runs
		for _, r := range runs {
			sessions = append(sessions, r.Session)
		}
		if err != nil {
			return false, sessions, err
		}
		sesPath = ses
		if last := lastOK(runs); last != nil && last.Unrouted > 0 && !o.noAutoEsc && !o.rawDSN {
			if p, d, t, r2, ok := retryWithEscapes(cfg, window, o, rawText, dsnText, fixOpt, reqs, base, *last, summary, &sessions, stderr); ok {
				sesPath, dsnPath, dsnText, runs = p, d, t, r2
			}
		}
		if last := lastOK(runs); last != nil && last.Unrouted > 0 {
			// Last resort before giving up: a fresh multi-start run (shuffled
			// net orders) on the DSN that routed best; kept only if better.
			ms := o.fo
			ms.multiStart = 4
			fmt.Fprintf(stderr, "multi-start: %d connection(s) still unrouted; one fresh run with --multi-start=4\n", last.Unrouted)
			s2, r2, err2 := runFastroute(ms, dsnPath, strings.TrimSuffix(dsnPath, ".dsn")+"-ms", stderr)
			for _, x := range r2 {
				sessions = append(sessions, x.Session)
			}
			summary["multiStartRuns"] = r2
			if got := lastOK(r2); err2 == nil && got != nil && runImproved(*last, *got) {
				sesPath, runs = s2, r2
				fmt.Fprintf(stderr, "multi-start: kept (%d unrouted)\n", got.Unrouted)
			}
		}
		if last := lastOK(runs); last != nil && last.Unrouted > 0 {
			fmt.Fprintf(stderr, "warning: %d connection(s) still unrouted after %d run(s); importing the best session\n", last.Unrouted, len(runs))
		}
		// The imported session's counts (after the multi-start retry): the
		// route-complete gate of pcb auto route judges them through the
		// router-agnostic routeResult (v18 B imported 1 unrouted silently).
		summary["routeFinal"] = lastOK(runs)
		rr := fastrouteResult(lastOK(runs), o.fo, dsnPath)
		if rr != nil && len(rr.Blocked) > 0 && escEnv != nil {
			rr.PlacementHints = placementHints(rr.Blocked, escEnv, reqs)
		}
		summary["routeResult"] = rr
	} else {
		tmpl := o.routerCmd
		if tmpl == "" {
			tmpl = os.Getenv("FREEROUTING_CMD")
		}
		if tmpl == "" {
			fmt.Fprintf(stderr, "no --router / FREEROUTING_CMD set — DSN exported, stopping.\n"+
				"  route it externally (e.g. --router fastroute), then: pcbpilot pcb import-autoroute <file.ses> && pcbpilot pcb ses-repair <file.ses> --dsn %s\n", dsnPath)
			return false, sessions, nil
		}
		sesPath = base + ".ses"
		sessions = append(sessions, sesPath)
		runStr := strings.NewReplacer("{in}", dsnPath, "{out}", sesPath).Replace(tmpl)
		fmt.Fprintf(stderr, "routing: %s\n", runStr)
		summary["router"] = runStr
		routerCtx, cancelRouter := context.WithTimeout(context.Background(), o.fo.timeout)
		defer cancelRouter()
		if err := runExternalRouter(routerCtx, runStr, stderr); err != nil {
			if routerCtx.Err() != nil {
				return false, sessions, fmt.Errorf("external router timed out after %s: %w", o.fo.timeout, routerCtx.Err())
			}
			return false, sessions, fmt.Errorf("external router failed: %w", err)
		}
		if _, err := os.Stat(sesPath); err != nil {
			return false, sessions, fmt.Errorf("router produced no SES at %s (check the command's {out})", sesPath)
		}
	}
	summary["ses"] = sesPath

	// 3. Import, then repair.
	data, err := os.ReadFile(sesPath)
	if err != nil {
		return false, sessions, fmt.Errorf("read SES: %w", err)
	}
	if o.ripUp {
		fmt.Fprintln(stderr, "rip-up: removing existing unlocked routing before import")
		if _, err := requestActionTimed(cfg, "pcb.route.rip_up", window, map[string]any{}, 10*time.Minute); err != nil {
			return false, sessions, fmt.Errorf("rip-up: %w", err)
		}
	}
	fmt.Fprintf(stderr, "importing SES (%d bytes) → tracks/vias\n", len(data))
	if _, err := requestActionTimed(cfg, "pcb.import_autoroute", window, map[string]any{
		"fileBase64": base64.StdEncoding.EncodeToString(data),
		"format":     "ses",
		"fileName":   filepath.Base(sesPath),
	}, 30*time.Minute); err != nil {
		return false, sessions, fmt.Errorf("import SES: %w", err)
	}
	if !o.noRepair {
		rep, err := repairImportedSession(cfg, window, string(data), dsnText, false, stderr)
		summary["repair"] = rep
		if err != nil {
			return false, sessions, fmt.Errorf("ses-repair: %w", err)
		}
	}
	return true, sessions, nil
}

// intentRequirements turns the intent's per-net widths and clearances into
// DSN requirements (inner width defaults to the outer one).
func intentRequirements(in *designIntent) map[string]specctra.NetRequirement {
	out := map[string]specctra.NetRequirement{}
	for name, n := range in.Nets {
		if n == nil || n.WidthMil.Outer <= 0 {
			continue
		}
		r := specctra.NetRequirement{OuterMil: n.WidthMil.Outer, InnerMil: n.WidthMil.Inner, MinMil: n.WidthMil.Min, ClearanceMil: n.ClearanceMil}
		if r.InnerMil <= 0 {
			r.InnerMil = r.OuterMil
		}
		out[name] = r
	}
	return out
}

// intentPairsAndTune writes the intent's differential pairs and length
// groups in fastroute's --pairs / --tune formats (mm). A pair with
// maxSkewMil also becomes a length group of its two nets with that
// tolerance. Empty strings when the intent has none.
func intentPairsAndTune(in *designIntent) (pairs, tune string) {
	mm := func(mil float64) string { return strconv.FormatFloat(math.Round(mil*0.0254*1000)/1000, 'f', -1, 64) }
	var pb, tb strings.Builder
	seen := map[string]bool{}
	groups := map[string][]string{}
	tol := map[string]float64{}
	for _, name := range in.sortedNetNames() {
		n := in.Nets[name]
		if n.DiffPair != "" && in.Nets[n.DiffPair] != nil {
			a, b := name, n.DiffPair
			if b < a {
				a, b = b, a
			}
			if !seen[a+"|"+b] {
				seen[a+"|"+b] = true
				fmt.Fprintf(&pb, "pair %s %s", a, b)
				if n.PairGapMil > 0 {
					fmt.Fprintf(&pb, " gap=%s", mm(n.PairGapMil))
				}
				// fastroute >= 0.1.13 meanders the shorter net of a pair
				// down to skew= (its default is 0.1 mm).
				if n.MaxSkewMil > 0 {
					fmt.Fprintf(&pb, " skew=%s", mm(n.MaxSkewMil))
				}
				pb.WriteString("\n")
				if n.MaxSkewMil > 0 {
					g := "pair_" + a + "_" + b
					groups[g] = []string{a, b}
					tol[g] = n.MaxSkewMil
				}
			}
		}
		if n.LengthGroup != "" {
			groups[n.LengthGroup] = append(groups[n.LengthGroup], name)
			if n.LengthTolMil > 0 && (tol[n.LengthGroup] == 0 || n.LengthTolMil < tol[n.LengthGroup]) {
				tol[n.LengthGroup] = n.LengthTolMil
			}
		}
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)
	for _, g := range names {
		if len(groups[g]) < 2 {
			continue
		}
		fmt.Fprintf(&tb, "group %s", g)
		if tol[g] > 0 {
			fmt.Fprintf(&tb, " tolerance=%s", mm(tol[g]))
		}
		tb.WriteString("\n")
		for _, n := range groups[g] {
			fmt.Fprintf(&tb, "  %s\n", n)
		}
	}
	return pb.String(), tb.String()
}

// prepareDSN applies the export fixes and, with requirements, raises the net
// classes to them and re-checks every net: any shortfall stops routing.
func prepareDSN(raw string, opt specctra.FixOptions, reqs map[string]specctra.NetRequirement) (string, specctra.FixReport, *specctra.RequirementReport, error) {
	text, rep, err := specctra.FixDSN(raw, opt)
	if err != nil {
		return "", rep, nil, fmt.Errorf("dsn-fix: %w", err)
	}
	// Route a hair wider than the rules: EasyEDA's DRC rounds tighter.
	text = specctra.AddClearanceMargin(text, specctra.ClearanceMarginMil)
	if reqs == nil {
		return text, rep, nil, nil
	}
	text, rq, err := specctra.ApplyNetRequirements(text, reqs)
	if err != nil {
		return "", rep, nil, fmt.Errorf("pre-route gate: %w", err)
	}
	short, err := specctra.CheckNetRequirements(text, reqs)
	if err != nil {
		return "", rep, &rq, fmt.Errorf("pre-route gate: %w", err)
	}
	if len(short) > 0 {
		return "", rep, &rq, fmt.Errorf("pre-route gate: %d net requirement(s) not met in the DSN: %s", len(short), strings.Join(short, "; "))
	}
	return text, rep, &rq, nil
}

func removeFiles(paths []string) {
	for _, f := range paths {
		_ = os.Remove(f)
	}
}

// escapeContext reads what escape planning needs from the live board and
// the DSN: pads, the clearance rule and the via diameter.
func escapeContext(cfg *appConfig, window, dsn string) ([]pcbPadP, float64, float64, error) {
	pads, err := fetchPcbPads(cfg, window)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("pads unreadable: %w", err)
	}
	clr := fetchPcbRules(cfg, window).clearanceMil
	if clr <= 0 {
		clr = 6
	}
	via := specctra.ViaDiameterMil(dsn, specctra.ViaPadstack(dsn))
	if via <= 0 {
		via = 24
	}
	return pads, clr, via, nil
}

// plannedIntentEscapes plans the intent pad escapes (planIntentEscapes) on
// the live board, around the escapes already planned (have), as DSN fixed
// wires (EasyEDA DSN: mil, TopLayer / BottomLayer). The escape env is
// returned for the placement hints.
func plannedIntentEscapes(cfg *appConfig, window, dsn string, reqs map[string]specctra.NetRequirement, have []specctra.Escape) ([]specctra.Escape, []string, *escapeEnv, error) {
	pads, clr, via, err := escapeContext(cfg, window, dsn)
	if err != nil {
		return nil, nil, nil, err
	}
	env := &escapeEnv{pads: pads, clr: clr + specctra.ClearanceMarginMil, margin: specctra.ClearanceMarginMil, netClr: map[string]float64{}, viaDia: via}
	for n, r := range reqs {
		env.netClr[strings.ToUpper(n)] = r.ClearanceMil
	}
	for _, e := range have {
		if len(e.Path) >= 2 {
			l := 1
			if e.Layer == "BottomLayer" {
				l = 2
			}
			end := e.Path[len(e.Path)-1]
			env.planned = append(env.planned, intentEscape{Net: e.Net, Layer: l, WidthMil: e.WidthMil, From: e.Path[0], To: end})
			if e.Via {
				env.vias = append(env.vias, widenVia{Net: e.Net, X: end[0], Y: end[1], Diameter: via})
			}
		}
	}
	esc, skipped := planIntentEscapes(env, reqs, escapeSeeded(have))
	name := func(l int) string {
		if l == 2 {
			return "BottomLayer"
		}
		return "TopLayer"
	}
	out := make([]specctra.Escape, len(esc))
	for i, e := range esc {
		out[i] = e.specctraEscape(name)
	}
	return out, skipped, env, nil
}

// plannedPreEscapes reserves escapes for every ground pin of fine-pitch
// parts with many pins (TQFP-144 GND pins on Gas Module V5) before the first
// route: reacting to blocked pins one at a time only moved the congestion to
// the next ground pin (v8: 5 → 4 unrouted).
func plannedPreEscapes(cfg *appConfig, window, dsn string, have []specctra.Escape) ([]specctra.Escape, []string, error) {
	pads, clr, via, err := escapeContext(cfg, window, dsn)
	if err != nil {
		return nil, nil, err
	}
	esc, skipped := planPreEscapes(pads, have, isGndNetName, via, clr, 10)
	return esc, skipped, nil
}

// retryWithEscapes: while the last run still reports blocked ground pins on
// fine-pitch parts, add escapes for them (around those already in the DSN)
// and route again from scratch, up to o.escapeRounds times; a round is kept
// only when it improves on the best so far. ok=true when any round was kept.
// ses-repair re-creates the escapes after import (they are not in the SES).
func retryWithEscapes(cfg *appConfig, window string, o autorouteOpts, rawText, dsnText string, fixOpt specctra.FixOptions, reqs map[string]specctra.NetRequirement, base string,
	last fastrouteRun, summary map[string]any, sessions *[]string, stderr io.Writer) (ses, dsnPath, fixed string, runs []fastrouteRun, ok bool) {
	pads, clr, via, err := escapeContext(cfg, window, dsnText)
	if err != nil {
		fmt.Fprintf(stderr, "auto-escapes: %v; skipped\n", err)
		return
	}
	var rounds []map[string]any
	defer func() { summary["autoEscapes"] = rounds }()
	opt := fixOpt
	best := last
	for round := 1; round <= o.escapeRounds && best.Unrouted > 0; round++ {
		blocked, err := readFastrouteBlocked(best.Report, specctra.ReportMilPerUnit(dsnText))
		if err != nil || len(blocked) == 0 {
			return
		}
		esc, skipped := planInwardEscapes(blocked, pads, opt.Escapes, isGndNetName, via, clr, 10)
		info := map[string]any{"round": round, "blockedEndpoints": len(blocked), "planned": esc, "skipped": skipped}
		rounds = append(rounds, info)
		if len(esc) == 0 {
			return
		}
		fmt.Fprintf(stderr, "auto-escapes round %d: %d blocked ground pin(s) get an inward stub + via; routing again\n", round, len(esc))
		next := opt
		next.Escapes = append(append([]specctra.Escape(nil), opt.Escapes...), esc...)
		text, _, _, err := prepareDSN(rawText, next, reqs)
		if err != nil {
			info["error"] = err.Error()
			return
		}
		path := fmt.Sprintf("%s-esc%d.dsn", base, round)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			info["error"] = err.Error()
			return
		}
		s, r, err := runFastroute(o.fo, path, fmt.Sprintf("%s-esc%d", base, round), stderr)
		for _, x := range r {
			*sessions = append(*sessions, x.Session)
		}
		info["dsn"], info["runs"] = path, r
		got := lastOK(r)
		if err != nil || got == nil || !runImproved(best, *got) {
			fmt.Fprintf(stderr, "auto-escapes round %d: no improvement; stopping\n", round)
			info["used"] = false
			return
		}
		info["used"] = true
		opt, best = next, *got
		ses, dsnPath, fixed, runs, ok = s, path, text, r, true
	}
	return
}

func newPcbAutorouteCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var o autorouteOpts
	var scriptPath string
	var noPost bool
	var schFiles []string
	var forceReason, forceUnsafeReason string
	c := &cobra.Command{
		Use:   "autoroute",
		Short: "Auto-route the active PCB via an external router (DSN→fix→route→SES→import→repair→DRC)",
		Long: `Orchestrate an external-router round-trip on the active PCB:

  1. export the DSN and repair EasyEDA's export defects (see 'pcb dsn-fix';
     --raw-dsn skips); PLANE inner layers are set to SIGNAL first unless
     --gnd-plane;
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

For a whole board from placement to post-layout sim use 'pcb auto route'.
A summary JSON is printed to stdout; progress goes to stderr.

` + viaToPourNote,
		Args: cobra.NoArgs,
		Example: `  pcbpilot pcb autoroute --router fastroute --rip-up --sch-connectivity p1.json
  pcbpilot pcb autoroute --router fastroute --multi-start 8 --escapes u8-gnd-escapes.json
  pcbpilot pcb autoroute --router 'java -jar freerouting.jar -de {in} -do {out}'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.timeoutSet = cmd.Flags().Changed("router-timeout")
			o.minTraceSet = cmd.Flags().Changed("min-trace-um")
			summary := map[string]any{}
			routed, sessions, err := runAutorouteFlow(cfg, *window, o, summary, stderr)
			if err != nil || !routed {
				_ = writeJSON(stdout, summary)
				return err
			}
			if noPost {
				fmt.Fprintln(stderr, "--no-post: skipped pour rebuild / save / reload / DRC; run them before trusting the board")
				return writeJSON(stdout, summary)
			}
			post, err := postImportChecks(cfg, *window, schFiles, scriptPath, stderr)
			summary["post"] = post
			if err == nil && !o.keep {
				removeFiles(sessions) // kept on any failure, for the diff
			}
			_ = writeJSON(stdout, summary)
			return err
		},
	}
	o.register(c.Flags(), "", 2, false)
	c.Flags().BoolVar(&noPost, "no-post", false, "skip pour rebuild / save / reload / DRC / pad-net diff")
	c.Flags().StringArrayVar(&schFiles, "sch-connectivity", nil, "schematic connectivity JSON for the pad-net diff (repeat per page)")
	c.Flags().StringVar(&scriptPath, "pad-net-diff-script", "", "path to pad-net-diff.py (auto-detected if omitted)")
	c.Flags().StringVar(&forceReason, "force", "", "deprecated compatibility option; workflow stages no longer gate routing")
	c.Flags().StringVar(&forceUnsafeReason, "force-unsafe", "", "deprecated compatibility option; workflow stages no longer gate routing")
	return c
}
