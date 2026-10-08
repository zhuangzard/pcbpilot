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
	f.Float64Var(&o.fo.minTraceUm, "min-trace-um", 0, "fastroute --router.min_trace_width_um (default: max(narrowest widthMil.min, board minimum track width))")
	return c
}

// kicadSiblings are the files that travel with a board.
var kicadSiblings = []string{".kicad_pcb", ".kicad_pro", ".kicad_dru"}

// copyKicadBoard copies src (+ its .kicad_pro / .kicad_dru) to dst (a
// .kicad_pcb path); a missing .kicad_pro is an error (DRC needs the rules).
func copyKicadBoard(src, dst string) error {
	sb, db := strings.TrimSuffix(src, ".kicad_pcb"), strings.TrimSuffix(dst, ".kicad_pcb")
	for _, ext := range kicadSiblings {
		data, err := os.ReadFile(sb + ext)
		if err != nil {
			if ext == ".kicad_dru" && os.IsNotExist(err) {
				_ = os.Remove(db + ext)
				continue
			}
			return fmt.Errorf("copy %s: %w", sb+ext, err)
		}
		if err := os.WriteFile(db+ext, data, 0o644); err != nil {
			return err
		}
	}
	return nil
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
	if err := copyKicadBoard(o.pcb, input); err != nil {
		return finish(err)
	}
	board := input
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
		floor := prep.MinTraceMil
		if snapRaw, serr := kt.Snapshot(classed); serr == nil {
			var s struct {
				Rules *boardRules `json:"rules"`
			}
			if json.Unmarshal(snapRaw, &s) == nil && s.Rules != nil {
				floor = math.Max(floor, s.Rules.TrackWidthMinMil)
			}
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
	summary["routed"] = routed
	fmt.Fprintf(stderr, "routed board: %s\n", routed)

	// 5. Gates on the routed board.
	drcPath := filepath.Join(o.outDir, "drc.json")
	rep, err := kt.DRC(routed, drcPath)
	if err != nil {
		add(gateResult{Gate: "kicad-drc", Detail: err.Error()})
	} else {
		add(kicadDRCGate(rep, drcPath))
		summary["drc"] = map[string]any{"file": drcPath, "total": rep.Total, "counts": rep.Counts}
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
