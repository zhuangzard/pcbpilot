package app

// cmd_kicad.go — `pcbpilot kicad`: the KiCad backend (KiCad 10, decided
// 2026-10-09). Boards are .kicad_pcb files read and written through KiCad's
// own Python (internal/kicad/bridge.py) and checked with kicad-cli; routing
// stays the external fastroute process (GPLv3, never linked or bundled).
//
// KiCad's native DSN/SES round trip needs none of the EasyEDA repairs
// (dsn-fix, ses-repair, reconcile). What it does need: the intent's widths
// and clearances written as netclasses BEFORE the DSN export, and the
// no-neck-down classes and minimum trace passed to fastroute (PoC
// 2026-10-09: without them fastroute necked tracks below the class width).

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

func newKicadCmd(stdout, stderr io.Writer) *cobra.Command {
	c := &cobra.Command{
		Use:   "kicad",
		Short: "KiCad backend: snapshot, route (fastroute) and gate .kicad_pcb boards through KiCad's Python and kicad-cli",
		Long: `Work on KiCad 10 boards (.kicad_pcb + .kicad_pro) without the EasyEDA connector.
KiCad is found at $PCBPILOT_KICAD_APP (default /Applications/KiCad/KiCad.app on
macOS); $PCBPILOT_KICAD_PYTHON / $PCBPILOT_KICAD_CLI override the two tools.
Input boards are never modified: every write goes to --out-dir.`,
	}
	c.AddCommand(newKicadSnapshotCmd(stdout), newKicadRouteCmd(stdout, stderr))
	return c
}

func newKicadSnapshotCmd(stdout io.Writer) *cobra.Command {
	var out string
	c := &cobra.Command{
		Use:   "snapshot <board.kicad_pcb>",
		Short: "Board snapshot JSON (mil, y-up; the pcb dump shape) of a .kicad_pcb",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := kicad.Snapshot(args[0])
			if err != nil {
				return err
			}
			if out != "" {
				return os.WriteFile(out, append(raw, '\n'), 0o644)
			}
			_, err = stdout.Write(append(raw, '\n'))
			return err
		},
	}
	c.Flags().StringVar(&out, "out", "", "write the snapshot here instead of stdout")
	return c
}

type kicadRouteOpts struct {
	pcb, intent, sim, outDir, waivers, fastrouteBin, widthBasis string
	ripUp                                                       bool
	fo                                                          fastrouteOpts
	minTraceSet                                                 bool
	// rules: explicit flags (0 = not given); ruleFlags = any was given.
	rules     kicad.Rules
	ruleFlags bool
	// pours: auto (only when the input board has no copper zones) | on | off.
	pours, gndNet, powerNet string
	gndLayers               []int
	powerLayer              int
}

func newKicadRouteCmd(stdout, stderr io.Writer) *cobra.Command {
	var o kicadRouteOpts
	c := &cobra.Command{
		Use:   "route",
		Short: "Route a .kicad_pcb with fastroute under the intent: netclasses → DSN → fastroute → SES → zone fill → kicad-cli DRC → gates",
		Long: `Route a KiCad board end to end, offline (no EasyEDA, no daemon):

  1. copy --pcb (+ .kicad_pro, .kicad_dru) into <out-dir>/work; --rip-up
     removes the unlocked tracks and vias first;
  2. pre-route gate: the intent's per-net widths and clearances become
     KiCad netclasses (PPn_W<width>, highest priority; existing wider values
     are kept). Inner widths: KiCad netclasses have one width, so the inner
     width goes (a) into the DSN as a layer_rule on the inner layers and
     (b) into <board>.kicad_dru as a (layer inner) track_width rule (opt =
     inner width, min = widthMil.min); widthMil.min also becomes an
     all-layer track_width min rule. Every net is re-checked in the DSN;
  3. KiCad's native DSN export → fastroute (no-neck-down classes, intent
     pairs/length groups, min trace = max(narrowest widthMil.min, board
     minimum track width)), continuation runs while connections remain;
  4. ImportSpecctraSES into <out-dir>/routed.kicad_pcb (.kicad_pro and
     .kicad_dru alongside) → zone fill;
  5. gates (summary.json gates[], like pcb auto route):
       route-complete   the imported session: 0 unrouted, 0 fixable violations
       kicad-drc        kicad-cli pcb drc --severity-error: 0 violations,
                        0 unconnected items (each violation listed)
       intent-widths    checkIntentWidths on the routed board's snapshot:
                        outer/inner width per net, narrower only as a pin
                        neck-down or inside the net's own pour, never below
                        widthMil.min. Basis: segment (each segment's
                        simulated current) when --sim is given and the
                        post-layout simulation runs, else net
       intent-lengths   (when the intent has length groups / skews)

Exits non-zero when any gate fails. --waivers takes signed {gate,match,reason,by}.
fastroute is GPLv3 and never downloaded: see 'pcb autoroute --help'.`,
		Args: cobra.NoArgs,
		Example: `  pcbpilot kicad route --pcb board.kicad_pcb --intent intent.json --out-dir route/
  pcbpilot kicad route --pcb board.kicad_pcb --intent intent.json --sim sim.json --rip-up --max-time 5m`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.pcb == "" || o.intent == "" {
				return fmt.Errorf("--pcb and --intent are required: the intent is the pre-route gate for widths, clearances and neck-down")
			}
			o.minTraceSet = cmd.Flags().Changed("min-trace-um")
			for _, n := range []string{"clearance-mil", "track-mil", "min-track-mil", "via-dia-mil", "via-drill-mil", "edge-mil"} {
				o.ruleFlags = o.ruleFlags || cmd.Flags().Changed(n)
			}
			if o.pours != "auto" && o.pours != "on" && o.pours != "off" {
				return fmt.Errorf("--pours must be auto, on or off")
			}
			if o.widthBasis == "" {
				o.widthBasis = "net"
				if o.sim != "" {
					o.widthBasis = "segment"
				}
			}
			if o.widthBasis != "net" && o.widthBasis != "segment" {
				return fmt.Errorf("--width-basis must be net or segment")
			}
			return runKicadRoute(o, stdout, stderr)
		},
	}
	f := c.Flags()
	f.StringVar(&o.pcb, "pcb", "", "input .kicad_pcb (its .kicad_pro must sit next to it) — required; never modified")
	f.StringVar(&o.intent, "intent", "", "intent.json (intent derive) — required")
	f.StringVar(&o.sim, "sim", "", "sim.json (sim power): post-layout simulation on the routed copper for the segment width basis")
	f.StringVar(&o.outDir, "out-dir", "kicad-route", "output directory (routed.kicad_pcb, summary.json, drc.json, …)")
	f.StringVar(&o.waivers, "waivers", "", "JSON list of signed waivers [{gate,match,reason,by}]")
	f.StringVar(&o.widthBasis, "width-basis", "", "intent-widths basis: segment (default with --sim) | net (default without)")
	f.BoolVar(&o.ripUp, "rip-up", false, "remove unlocked tracks and vias before routing (route from scratch)")
	f.StringVar(&o.fastrouteBin, "fastroute-bin", "", "fastroute executable (default: $FASTROUTE_BIN, ~/.pcbpilot/fastroute/current, PATH)")
	f.DurationVar(&o.fo.maxTime, "max-time", 0, "fastroute --max-time per run (0 = none)")
	f.IntVar(&o.fo.threads, "threads", 1, "fastroute autorouter/optimizer threads (1 also sets --multi-start=1)")
	f.IntVar(&o.fo.multiStart, "multi-start", 0, "fastroute --multi-start=N (0 = 1 with --threads 1)")
	f.IntVar(&o.fo.rounds, "continue", 2, "fastroute continuation runs (--initial-session) while connections remain unrouted")
	f.DurationVar(&o.fo.timeout, "router-timeout", 45*time.Minute, "hard limit per fastroute run")
	f.Float64Var(&o.fo.minTraceUm, "min-trace-um", 0, "fastroute --router.min_trace_width_um (default: max(board minimum track width, min(narrowest widthMil.min, narrowest class width)))")
	f.Float64Var(&o.rules.ClearanceMil, "clearance-mil", 0, "board minimum clearance + Default netclass clearance (default for a board without .kicad_pro: intent copper.clearanceMil)")
	f.Float64Var(&o.rules.TrackMil, "track-mil", 0, "Default netclass track width")
	f.Float64Var(&o.rules.MinTrackMil, "min-track-mil", 0, "board minimum track width (default for a board without .kicad_pro: intent copper.minTrackMil)")
	f.Float64Var(&o.rules.ViaDiaMil, "via-dia-mil", 0, "Default netclass via diameter (default without .kicad_pro: intent copper.viaDiaMil)")
	f.Float64Var(&o.rules.ViaDrillMil, "via-drill-mil", 0, "Default netclass via drill (default without .kicad_pro: intent copper.viaDrillMil)")
	f.Float64Var(&o.rules.EdgeMil, "edge-mil", 0, "copper-to-board-edge clearance (default without .kicad_pro: max(intent edge.outerMil, edge.innerMil))")
	f.StringVar(&o.pours, "pours", "auto", "board-outline pours after the SES import: auto (only when the input has no copper zones) | on | off")
	f.StringVar(&o.gndNet, "gnd-net", "GND", "ground pour net")
	f.IntSliceVar(&o.gndLayers, "gnd-layers", nil, "ground pour layers, pcbpilot ids (default: 1,15,2 on 4+ layers, 1,2 on 2 layers — as pcb auto route)")
	f.StringVar(&o.powerNet, "power-net", "", "power pour net (default: the non-ground power net with the most pads)")
	f.IntVar(&o.powerLayer, "power-layer", -1, "power pour layer id (default 16 = In2 on 4+ layers, none on 2 layers; 0 = none)")
	return c
}

// kicadSiblings are the files that travel with a board.
var kicadSiblings = []string{".kicad_pcb", ".kicad_pro", ".kicad_dru"}

// copyKicadBoard copies src (+ its .kicad_pro / .kicad_dru when present)
// to dst (a .kicad_pcb path) and reports whether a .kicad_pro came along.
func copyKicadBoard(src, dst string) (hasPro bool, err error) {
	sb, db := strings.TrimSuffix(src, ".kicad_pcb"), strings.TrimSuffix(dst, ".kicad_pcb")
	for _, ext := range kicadSiblings {
		data, err := os.ReadFile(sb + ext)
		if err != nil {
			if ext != ".kicad_pcb" && os.IsNotExist(err) {
				_ = os.Remove(db + ext)
				continue
			}
			return false, fmt.Errorf("copy %s: %w", sb+ext, err)
		}
		if err := os.WriteFile(db+ext, data, 0o644); err != nil {
			return false, err
		}
		hasPro = hasPro || ext == ".kicad_pro"
	}
	return hasPro, nil
}

// intentBoardRules reads the board-wide rules of an intent file (copper
// block and edge) for a board that has no .kicad_pro yet.
func intentBoardRules(path string) kicad.Rules {
	var raw struct {
		Copper struct {
			MinTrackMil  float64 `json:"minTrackMil"`
			ClearanceMil float64 `json:"clearanceMil"`
			ViaDrillMil  float64 `json:"viaDrillMil"`
			ViaDiaMil    float64 `json:"viaDiaMil"`
		} `json:"copper"`
		Edge struct {
			OuterMil float64 `json:"outerMil"`
			InnerMil float64 `json:"innerMil"`
		} `json:"edge"`
	}
	data, _ := os.ReadFile(path)
	_ = json.Unmarshal(data, &raw)
	return kicad.Rules{ClearanceMil: raw.Copper.ClearanceMil, MinTrackMil: raw.Copper.MinTrackMil,
		ViaDiaMil: raw.Copper.ViaDiaMil, ViaDrillMil: raw.Copper.ViaDrillMil,
		EdgeMil: math.Max(raw.Edge.OuterMil, raw.Edge.InnerMil)}
}

// mergeRules: explicit values win over the defaults.
func mergeRules(def, explicit kicad.Rules) kicad.Rules {
	pick := func(a, b float64) float64 {
		if b > 0 {
			return b
		}
		return a
	}
	return kicad.Rules{ClearanceMil: pick(def.ClearanceMil, explicit.ClearanceMil), TrackMil: pick(def.TrackMil, explicit.TrackMil),
		MinTrackMil: pick(def.MinTrackMil, explicit.MinTrackMil), ViaDiaMil: pick(def.ViaDiaMil, explicit.ViaDiaMil),
		ViaDrillMil: pick(def.ViaDrillMil, explicit.ViaDrillMil), EdgeMil: pick(def.EdgeMil, explicit.EdgeMil)}
}

func removeKicadBoard(pcb string) {
	b := strings.TrimSuffix(pcb, ".kicad_pcb")
	for _, ext := range append(kicadSiblings, ".kicad_prl") {
		_ = os.Remove(b + ext)
	}
}

func runKicadRoute(o kicadRouteOpts, stdout, stderr io.Writer) error {
	waivers, err := loadWaivers(o.waivers)
	if err != nil {
		return err
	}
	in, err := loadDesignIntent(o.intent)
	if err != nil {
		return fmt.Errorf("pre-route gate: %w", err)
	}
	work := filepath.Join(o.outDir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	summary := map[string]any{"pcb": o.pcb, "intent": o.intent, "outDir": o.outDir}
	if o.sim != "" {
		summary["sim"] = o.sim
	}
	start := time.Now()
	var gates []gateResult
	finish := func(err error) error {
		pass := len(gates) > 0
		for _, g := range gates {
			pass = pass && g.Pass
		}
		summary["gates"], summary["pass"] = gates, pass && err == nil
		summary["seconds"] = time.Since(start).Round(time.Second).Seconds()
		if err != nil {
			summary["error"] = err.Error()
		}
		if f, ferr := os.Create(filepath.Join(o.outDir, "summary.json")); ferr == nil {
			_ = writeJSON(f, summary)
			f.Close()
		}
		_ = writeJSON(stdout, summary)
		if err != nil {
			return err
		}
		if !pass {
			return fmt.Errorf("kicad route: gate(s) failed: %s", failedGates(summary))
		}
		return nil
	}
	add := func(g gateResult) {
		applyWaivers(&g, waivers)
		gates = append(gates, g)
		mark := "PASS"
		if !g.Pass {
			mark = "FAIL"
		}
		fmt.Fprintf(stderr, "gate %-15s %s  %s\n", g.Gate, mark, g.Detail)
	}

	kt, err := kicad.Locate()
	summary["kicad"] = kt
	if err != nil {
		return finish(err)
	}
	bin, err := resolveFastroute(o.fastrouteBin)
	if err != nil {
		return finish(err)
	}
	o.fo.bin = bin
	summary["fastroute"] = bin

	// 1. Work copy (the input is never written).
	input := filepath.Join(work, "input.kicad_pcb")
	hasPro, err := copyKicadBoard(o.pcb, input)
	if err != nil {
		return finish(err)
	}
	board := input
	// A board without a project gets one, with the board rules from the
	// intent (copper block, edge) and the explicit flags; explicit flags
	// also update an existing project.
	if !hasPro || o.ruleFlags {
		r := o.rules
		if !hasPro {
			if err := os.WriteFile(strings.TrimSuffix(input, ".kicad_pcb")+".kicad_pro", kicad.MinimalProject(input), 0o644); err != nil {
				return finish(err)
			}
			r = mergeRules(intentBoardRules(o.intent), o.rules)
			fmt.Fprintf(stderr, "project: %s has no .kicad_pro — created one with %+v\n", o.pcb, r)
		}
		ruled := filepath.Join(work, "ruled.kicad_pcb")
		res, err := kt.SetRules(board, r, ruled)
		summary["rules"] = map[string]any{"created": !hasPro, "rules": r, "result": res}
		if err != nil {
			return finish(err)
		}
		board = ruled
	}
	inSnapRaw, err := kt.Snapshot(board)
	if err != nil {
		return finish(err)
	}
	var inSnap boardSnapshot
	if err := json.Unmarshal(inSnapRaw, &inSnap); err != nil {
		return finish(err)
	}
	if o.ripUp {
		ripped := filepath.Join(work, "ripped.kicad_pcb")
		res, err := kt.RipUp(board, ripped)
		summary["ripUp"] = res
		if err != nil {
			return finish(err)
		}
		board = ripped
		fmt.Fprintf(stderr, "rip-up: %s\n", res)
	}

	// 2. Pre-route gate: intent → netclasses (+ .kicad_dru).
	reqs := map[string]kicad.NetRequirement{}
	for net, r := range intentRequirements(in) {
		reqs[net] = kicad.NetRequirement{OuterMil: r.OuterMil, InnerMil: r.InnerMil, MinMil: r.MinMil, ClearanceMil: r.ClearanceMil}
	}
	classed := filepath.Join(work, "classed.kicad_pcb")
	ncRes, err := kt.Netclasses(board, reqs, classed)
	summary["netclasses"] = ncRes
	if err != nil {
		return finish(fmt.Errorf("pre-route gate: netclasses: %w", err))
	}
	if len(ncRes.Mismatched) > 0 {
		return finish(fmt.Errorf("pre-route gate: %d net(s) do not resolve to their pcbpilot netclass: %s", len(ncRes.Mismatched), strings.Join(ncRes.Mismatched, "; ")))
	}
	if len(ncRes.MissingNets) > 0 {
		fmt.Fprintf(stderr, "warning: %d intent net(s) not on the board (ignored): %s\n", len(ncRes.MissingNets), strings.Join(ncRes.MissingNets, ", "))
	}
	for _, n := range ncRes.MissingNets {
		delete(reqs, n)
	}
	fmt.Fprintf(stderr, "pre-route gate: %d net(s) in %d netclass(es), %d custom rule(s) in %s\n", len(reqs), len(ncRes.Classes), ncRes.DRURules, ncRes.DRU)

	// 3. DSN → prepare → fastroute.
	rawDSN := filepath.Join(o.outDir, "route-kicad.dsn")
	if _, err := kt.ExportDSN(classed, rawDSN); err != nil {
		return finish(err)
	}
	dsnText, err := os.ReadFile(rawDSN)
	if err != nil {
		return finish(err)
	}
	prepared, prep, err := kicad.PrepareDSN(string(dsnText), ncRes.Classes, reqs)
	summary["dsnRequirements"] = prep
	if err != nil {
		return finish(fmt.Errorf("pre-route gate: %w", err))
	}
	if len(prep.Short) > 0 {
		return finish(fmt.Errorf("pre-route gate: %d net requirement(s) not met in the DSN: %s", len(prep.Short), strings.Join(prep.Short, "; ")))
	}
	dsnPath := filepath.Join(o.outDir, "route.dsn")
	if err := os.WriteFile(dsnPath, []byte(prepared), 0o644); err != nil {
		return finish(err)
	}
	summary["dsn"] = dsnPath
	o.fo.noNeckdown = prep.NoNeckdown
	if !o.minTraceSet {
		// fastroute has one global neck-down floor: the narrowest widthMil.min,
		// never above the narrowest class width (that class could not route
		// at its own width) and never below the board minimum.
		floor := prep.MinTraceMil
		if prep.NarrowestClassMil > 0 && (floor == 0 || floor > prep.NarrowestClassMil) {
			floor = prep.NarrowestClassMil
		}
		if inSnap.Rules != nil {
			floor = math.Max(floor, inSnap.Rules.TrackWidthMinMil)
		}
		if floor > 0 {
			// Round up so fastroute never goes a hair below the floor.
			o.fo.minTraceUm = math.Ceil(floor*25.4*10) / 10
		}
	}
	base := strings.TrimSuffix(dsnPath, ".dsn")
	pairs, tune := intentPairsAndTune(in)
	if pairs != "" {
		o.fo.pairsFile = base + "-pairs.txt"
		if err := os.WriteFile(o.fo.pairsFile, []byte(pairs), 0o644); err != nil {
			return finish(err)
		}
	}
	if tune != "" {
		o.fo.tuneFile = base + "-tune.txt"
		if err := os.WriteFile(o.fo.tuneFile, []byte(tune), 0o644); err != nil {
			return finish(err)
		}
	}
	summary["intentPairs"], summary["intentTune"] = pairs, tune
	fmt.Fprintf(stderr, "pre-route gate: DSN ok (%d no-neck-down class(es), %d inner rule(s), min trace %.1f µm)\n",
		len(prep.NoNeckdown), len(prep.InnerRules), o.fo.minTraceUm)

	ses, runs, err := runFastroute(o.fo, dsnPath, base, stderr)
	summary["router"], summary["routerRuns"] = "fastroute", runs
	if err != nil {
		return finish(err)
	}
	if last := lastOK(runs); last != nil && last.Unrouted > 0 {
		// One fresh multi-start run (shuffled net orders), kept only if better.
		ms := o.fo
		ms.multiStart = 4
		fmt.Fprintf(stderr, "multi-start: %d connection(s) still unrouted; one fresh run with --multi-start=4\n", last.Unrouted)
		s2, r2, err2 := runFastroute(ms, dsnPath, base+"-ms", stderr)
		summary["multiStartRuns"] = r2
		if got := lastOK(r2); err2 == nil && got != nil && runImproved(*last, *got) {
			ses, runs = s2, r2
		}
	}
	final := lastOK(runs)
	summary["routeFinal"], summary["ses"] = final, ses
	add(routeCompleteGate(final))

	// 4. Import → zone fill → routed board.
	routed := filepath.Join(o.outDir, "routed.kicad_pcb")
	removeKicadBoard(routed)
	imp, err := kt.ImportSES(classed, ses, routed)
	summary["sesImport"] = imp
	if err != nil {
		return finish(err)
	}
	if _, err := os.Stat(strings.TrimSuffix(routed, ".kicad_pcb") + ".kicad_pro"); err != nil {
		if err := copyFile(strings.TrimSuffix(classed, ".kicad_pcb")+".kicad_pro", strings.TrimSuffix(routed, ".kicad_pcb")+".kicad_pro"); err != nil {
			return finish(err)
		}
	}
	if ps := kicadPourPlan(o, &inSnap); len(ps) > 0 {
		poured := filepath.Join(work, "poured.kicad_pcb")
		res, err := kt.Pours(routed, ps, poured)
		summary["pours"] = map[string]any{"plan": ps, "result": res}
		if err != nil {
			return finish(err)
		}
		removeKicadBoard(routed)
		if _, err := copyKicadBoard(poured, routed); err != nil {
			return finish(err)
		}
		fmt.Fprintf(stderr, "pours: %d zone(s) added and filled\n", len(ps))
	}
	summary["routed"] = routed
	fmt.Fprintf(stderr, "routed board: %s\n", routed)

	// 5. Gates on the routed board.
	drcPath := filepath.Join(o.outDir, "drc.json")
	rep, err := kt.DRC(routed, drcPath)
	if err != nil {
		add(gateResult{Gate: "kicad-drc", Detail: err.Error()})
	} else {
		add(kicadDRCGate(rep, drcPath))
		drcSum := map[string]any{"file": drcPath, "total": rep.Total, "counts": rep.Counts}
		// Warnings (silkscreen overlaps, …) are reported, not gated.
		allPath := filepath.Join(o.outDir, "drc-all.json")
		if all, aerr := kt.DRCSeverity(routed, allPath, "all"); aerr == nil {
			warn := map[string]int{}
			n := 0
			for _, v := range all.Violations {
				if v.Severity != "error" {
					warn[v.Rule]++
					n++
				}
			}
			drcSum["warnings"], drcSum["warningCounts"], drcSum["allFile"] = n, warn, allPath
		}
		summary["drc"] = drcSum
	}

	snapRaw, err := kt.Snapshot(routed)
	if err != nil {
		add(gateResult{Gate: "intent-widths", Detail: "routed board snapshot: " + err.Error()})
		return finish(nil)
	}
	snapPath := filepath.Join(o.outDir, "board-routed.json")
	_ = os.WriteFile(snapPath, append(snapRaw, '\n'), 0o644)
	summary["snapshot"] = snapPath
	var segNeed func(specctra.Track) (float64, bool)
	basis := "net current"
	if o.widthBasis == "segment" {
		if o.sim == "" {
			summary["widthBasisNote"] = "segment basis needs --sim; net basis used"
		} else {
			po := postSimOpts{board: snapPath, sim: o.sim, intent: o.intent,
				out: filepath.Join(o.outDir, "post.json"), report: filepath.Join(o.outDir, "post.md"), svgDir: filepath.Join(o.outDir, "heatmaps"),
				cell: 0.5, ambient: 25, hTop: 10, hBottom: 10, kxy: 0.3, kz: 0.3, plating: 0.7, viaDT: 10, margin: 1.2,
				source: "kicad route (" + routed + ")"}
			res, serr := runPostSim(po, stderr)
			if serr != nil {
				summary["widthBasisNote"] = "post-layout simulation failed (" + serr.Error() + "); net basis used"
			} else {
				summary["postSim"] = map[string]any{"verdict": res.Verdict.Status, "reasons": res.Verdict.Reasons, "out": po.out, "report": po.report}
				segNeed = segmentWidthNeed(in, res)
				basis = "each segment's simulated current below half the net current, else the net current"
			}
		}
	}
	g, lengths, err := kicadIntentGates(snapRaw, in, segNeed, basis)
	if err != nil {
		add(gateResult{Gate: "intent-widths", Detail: err.Error()})
		return finish(nil)
	}
	add(g)
	if lengths != nil {
		add(*lengths)
	}
	return finish(nil)
}

// kicadDRCGate: 0 DRC errors, 0 unconnected items, every one listed.
func kicadDRCGate(rep *kicad.DRCReport, path string) gateResult {
	g := gateResult{Gate: "kicad-drc", Pass: rep.Total == 0,
		Detail: fmt.Sprintf("%d error(s) from kicad-cli pcb drc (%s)", rep.Total, path)}
	g.Items = append(g.Items, rep.SortedCounts()...)
	for _, v := range rep.Violations {
		g.Items = append(g.Items, drcItem(drcFlatViolation{Rule: v.Rule, ObjType: v.ObjType, Net: v.Net, X: v.X, Y: v.Y,
			Layer: v.Layer, Objs: v.Objs, Message: v.Message}))
	}
	return g
}

// kicadIntentGates runs checkIntentWidths (and checkIntentLengths) on a
// KiCad snapshot: its tracks are copper.lines in the pcb dump shape and its
// layer ids follow pcbpilot's numbering (inner >= 15), so the EasyEDA gate
// code applies unchanged. Arcs are not width-checked (as on EasyEDA).
func kicadIntentGates(snapRaw []byte, in *designIntent, segNeed func(specctra.Track) (float64, bool), basis string) (gateResult, *gateResult, error) {
	var snap boardSnapshot
	if err := json.Unmarshal(snapRaw, &snap); err != nil {
		return gateResult{}, nil, fmt.Errorf("decode snapshot: %w", err)
	}
	if snap.Copper == nil {
		return gateResult{}, nil, fmt.Errorf("snapshot has no copper")
	}
	var tracks []specctra.Track
	if err := decodeAny(snap.Copper.Lines, &tracks); err != nil {
		return gateResult{}, nil, fmt.Errorf("decode tracks: %w", err)
	}
	var pads []boardPad
	for _, c := range snap.Components {
		pads = append(pads, c.Pads...)
	}
	vs := checkIntentWidths(tracks, pads, intentRequirements(in), pouredLookup(snap.Copper.Poured), segNeed)
	g := gateResult{Gate: "intent-widths", Pass: len(vs) == 0,
		Detail: fmt.Sprintf("%d track(s) below the intent width (basis: %s) outside pin neck-downs and own-net pours, or below the minimum", len(vs), basis),
		Items:  summarizeWidthViolations(vs)}
	if items, n := checkIntentLengths(in, tracks); n > 0 {
		lg := gateResult{Gate: "intent-lengths", Pass: len(items) == 0,
			Detail: fmt.Sprintf("%d length group(s)/pair(s): routed length spread within tolerance", n), Items: items}
		return g, &lg, nil
	}
	return g, nil, nil
}

// kicadPourPlan: the pcb auto route pour recipe (GND on TOP/IN1/BOTTOM, the
// main power rail on IN2 for 4+ layers; GND on TOP/BOTTOM for 2) on a board
// without copper zones (--pours auto), or always (--pours on).
func kicadPourPlan(o kicadRouteOpts, in *boardSnapshot) []kicad.Pour {
	if o.pours == "off" {
		return nil
	}
	if o.pours == "auto" && in.Copper != nil && len(in.Copper.Pours) > 0 {
		return nil
	}
	gnd, power := defaultPourLayers(in.CopperLayers)
	if len(o.gndLayers) > 0 {
		gnd = o.gndLayers
	}
	if o.powerLayer >= 0 {
		power = o.powerLayer
	}
	clr := 0.0
	if in.Rules != nil {
		clr = in.Rules.ClearanceMil
	}
	has := map[string]bool{}
	for _, c := range in.Components {
		for _, p := range c.Pads {
			has[p.Net] = true
		}
	}
	var ps []kicad.Pour
	if has[o.gndNet] {
		for _, l := range gnd {
			ps = append(ps, kicad.Pour{Net: o.gndNet, Layer: l, ClearanceMil: clr})
		}
	}
	net := o.powerNet
	if net == "" {
		net = mainPowerRail(in.toCheckPads())
	}
	if power > 0 && net != "" && has[net] {
		ps = append(ps, kicad.Pour{Net: net, Layer: power, ClearanceMil: clr})
	}
	return ps
}
