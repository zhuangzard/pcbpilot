package app

// cmd_kicad_route.go — `pcbpilot kicad route`: pcb auto route +
// runQualityGates for a KiCad board. Order (mirrors the EasyEDA flow):
//
//	design review → rules / netclasses (intent, insulation) → DSN →
//	fastroute → SES import → via arrays → widen to intent → pours →
//	widen nets → silkscreen placement → gates on board-final.json →
//	IR closure (≤ 2 rounds) → design report → release review
//
// Every calculation is the shared EasyEDA-neutral code; the bridge only
// reads (snapshot) and writes (edit / pours / netclasses) the board.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

type kicadRouteOpts struct {
	pcb, intent, sim, sch, outDir, waivers, fastrouteBin, widthBasis string
	ripUp                                                            bool
	fo                                                               fastrouteOpts
	minTraceSet                                                      bool
	// rules: explicit flags (0 = not given); ruleFlags = any was given.
	rules     kicad.Rules
	ruleFlags bool
	// pours: auto (only when the input board has no copper zones) | on | off.
	pours, gndNet, powerNet string
	gndLayers               []int
	powerLayer              int
	widenNets               string
	widenMax                float64
	silk                    silkTightOpts
	noSilkPlace             bool
	noManual                bool
	projectConfig           string
	requirements            []string
	reviewers               string
	noReview                bool
	reviewTimeout           time.Duration
	projectName, customer   string
	parallelMS              bool
}

func newKicadRouteCmd(stdout, stderr io.Writer) *cobra.Command {
	o := kicadRouteOpts{silk: defaultSilkTightOpts()}
	c := &cobra.Command{
		Use:   "route",
		Short: "Route and gate a .kicad_pcb like pcb auto route: review → intent netclasses → fastroute → via arrays / widen / pours / silk → every gate → IR closure → report → release review",
		Long: `Route a KiCad board end to end, offline (no EasyEDA, no daemon), with the same
calculations and gates as 'pcb auto route' + 'pcb gate':

 0. design review (review-panel, stage design) on --requirements + intent,
    sim, schematic connectivity: a passing review.json for the same inputs
    is reused, else the reviewers run. A failing review stops the run.
 1. work copy of --pcb (+ .kicad_pro / .kicad_dru); a board without a
    project gets one, with the board rules from the intent (copper, edge)
    and --clearance-mil / --track-mil / --via-dia-mil / --via-drill-mil /
    --edge-mil; --rip-up removes unlocked tracks and vias;
 2. pre-route gate: intent → KiCad netclasses PPn_W<width> (outer width,
    clearance; inner width and widthMil.min as .kicad_dru track_width
    rules); insulation pairs → domain netclasses PPD_<domain> with
    clearance + creepage rules in .kicad_dru, and the pair clearance on
    every net of the pair; DSN check per net (+0.2 mil clearance margin);
 3. fastroute: no-neck-down classes, min trace, intent pairs / length
    groups (skew), continuation runs, one multi-start=4 retry;
 4. SES import → zone fill → via arrays (planViaArrays, intent via
    counts) → widen to intent (planWidenToIntent, KiCad-DRC step-back)
    → pours (GND on TOP/IN1/BOTTOM, main power rail on IN2; --pours auto
    pours only a board without zones) → --widen-net → silkscreen placement
    (planSilkTight / group labels, ≤ 3 rounds);
 5. gates on <out-dir>/board-final.json (the pcb dump shape): kicad-drc
    (kicad-cli, errors; warnings listed in summary.drc), pad-net-diff
    (with --sch: board pads vs the schematic netlist), intent-rules,
    intent-widths (segment basis with --sim), intent-lengths,
    copper-to-edge, isolation, via-current, post-layout-sim (needs --sim),
    route-complete, silkscreen, board-manual;
 6. IR closure: when only post-layout-sim (± intent-widths, silkscreen,
    board-manual) fails, planWidenIR + re-gate, at most 2 rounds;
 7. report design → <out-dir>/report (gate design-report);
 8. release review (review-panel, stage layout) with the report and
    summary.json added to the evidence.

Exits non-zero when any gate fails; --waivers takes signed {gate,match,reason,by}
(--no-manual and --no-review need one). The input board is never written.`,
		Args: cobra.NoArgs,
		Example: `  pcbpilot kicad route --pcb GasV5_A.kicad_pcb --intent intent.json --sim sim.json \
      --requirements REQUIREMENTS.md --out-dir route/ --clearance-mil 5.98 --track-mil 10 --edge-mil 30`,
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
	f.StringVar(&o.pcb, "pcb", "", "input .kicad_pcb — required; never modified")
	f.StringVar(&o.intent, "intent", "", "intent.json (intent derive) — required")
	f.StringVar(&o.sim, "sim", "", "sim.json (sim power): post-layout simulation (post-layout-sim gate, segment width basis, IR closure); missing = that gate fails")
	f.StringVar(&o.sch, "sch", "", "root .kicad_sch: pad-net-diff gate against its netlist (kicad-cli sch export netlist) and the review evidence")
	f.StringVar(&o.outDir, "out-dir", "kicad-route", "output directory (routed.kicad_pcb, board-final.json, summary.json, drc*.json, report/, manual/, review-*/)")
	f.StringVar(&o.waivers, "waivers", "", "JSON list of signed waivers [{gate,match,reason,by}]")
	f.StringVar(&o.widthBasis, "width-basis", "", "intent-widths basis: segment (default with --sim) | net (default without)")
	f.BoolVar(&o.ripUp, "rip-up", false, "remove unlocked tracks and vias before routing (route from scratch)")
	f.StringVar(&o.fastrouteBin, "fastroute-bin", "", "fastroute executable (default: $FASTROUTE_BIN, ~/.pcbpilot/fastroute/current, PATH)")
	f.DurationVar(&o.fo.maxTime, "max-time", 0, "fastroute --max-time per run (0 = none)")
	f.IntVar(&o.fo.threads, "threads", 1, "fastroute autorouter/optimizer threads (1 also sets --multi-start=1)")
	f.IntVar(&o.fo.multiStart, "multi-start", 0, "fastroute --multi-start=N (0 = 1 with --threads 1)")
	f.BoolVar(&o.parallelMS, "parallel-multi-start", true, "run the multi-start=4 retry speculatively in parallel with the main fastroute run (needs ≥ 2 CPUs)")
	f.IntVar(&o.fo.rounds, "continue", 2, "fastroute continuation runs (--initial-session) while connections remain unrouted")
	f.DurationVar(&o.fo.timeout, "router-timeout", 45*time.Minute, "hard limit per fastroute run")
	f.Float64Var(&o.fo.minTraceUm, "min-trace-um", 0, "fastroute --router.min_trace_width_um (default: max(board minimum track width, min(narrowest widthMil.min, narrowest class width)))")
	f.Float64Var(&o.rules.ClearanceMil, "clearance-mil", 0, "board minimum clearance + Default netclass clearance (default for a board without .kicad_pro: intent copper.clearanceMil)")
	f.Float64Var(&o.rules.TrackMil, "track-mil", 0, "Default netclass track width")
	f.Float64Var(&o.rules.MinTrackMil, "min-track-mil", 0, "board minimum track width (default for a board without .kicad_pro: intent copper.minTrackMil)")
	f.Float64Var(&o.rules.ViaDiaMil, "via-dia-mil", 0, "Default netclass via diameter (default without .kicad_pro: intent copper.viaDiaMil)")
	f.Float64Var(&o.rules.ViaDrillMil, "via-drill-mil", 0, "Default netclass via drill (default without .kicad_pro: intent copper.viaDrillMil)")
	f.Float64Var(&o.rules.EdgeMil, "edge-mil", 0, "copper-to-board-edge clearance (default without .kicad_pro: max(intent edge.outerMil, edge.innerMil))")
	f.StringVar(&o.pours, "pours", "auto", "pours after the SES import: auto (only when the input has no copper zones) | on | off")
	f.StringVar(&o.gndNet, "gnd-net", "GND", "ground pour net")
	f.IntSliceVar(&o.gndLayers, "gnd-layers", nil, "ground pour layers, pcbpilot ids (default: 1,15,2 on 4+ layers, 1,2 on 2 layers)")
	f.StringVar(&o.powerNet, "power-net", "", "power pour net (default: the non-ground power net with the most pads)")
	f.IntVar(&o.powerLayer, "power-layer", -1, "power pour layer id (default 16 = In2 on 4+ layers; 0 = none)")
	f.StringVar(&o.widenNets, "widen-net", "", "nets to widen after routing, comma-separated (see 'pcb widen')")
	f.Float64Var(&o.widenMax, "widen-max-mil", 40, "maximum width for --widen-net and the IR closure (mil)")
	f.BoolVar(&o.noSilkPlace, "no-silk-place", false, "do not move designators before the silkscreen gate")
	f.StringArrayVar(&o.requirements, "requirements", nil, "project requirement document for the design review (repeatable; required unless --no-review)")
	f.StringVar(&o.reviewers, "reviewers", "codex,kimi,claude", "design review reviewer CLIs, comma-separated (all must pass)")
	f.BoolVar(&o.noReview, "no-review", false, "skip the design reviews; refused unless --waivers holds a signed {\"gate\":\"design-review\",\"match\":\"--no-review\"} entry")
	f.DurationVar(&o.reviewTimeout, "review-timeout", 20*time.Minute, "per-reviewer time limit")
	f.StringVar(&o.projectName, "project-name", "", "report / manual project name (default: the board file name)")
	f.StringVar(&o.customer, "customer", "", "customer name on the report cover")
	addSilkTightFlags(c, &o.silk, "silk-")
	addManualFlags(c, &o.noManual, &o.projectConfig)
	return c
}

const noReviewMatch = "--no-review"

// kicadRun is one kicad route execution.
type kicadRun struct {
	o       kicadRouteOpts
	kt      *kicad.Tools
	in      *designIntent
	waivers []gateWaiver
	summary map[string]any
	gates   []gateResult
	work    string
	step    int
	classes []kicad.NetClass
	route   *fastrouteRun
	conn    string // schematic connectivity JSON (with --sch)
	netlist *kicad.SchNetlist
	stderr  io.Writer
	// inputDRC: the input board's own DRC errors (rule|message), to mark
	// what routing did not cause.
	inputDRC map[string]bool
	// times: wall clock per stage (summary.timings).
	times []stageTime
	last  time.Time
}

type stageTime struct {
	Stage   string  `json:"stage"`
	Seconds float64 `json:"seconds"`
}

// lap records the wall clock since the previous lap as stage.
func (r *kicadRun) lap(stage string) {
	now := time.Now()
	if r.last.IsZero() {
		r.last = now
	}
	r.times = append(r.times, stageTime{Stage: stage, Seconds: math.Round(now.Sub(r.last).Seconds()*10) / 10})
	r.last = now
	r.summary["timings"] = r.times
}

func (r *kicadRun) add(g gateResult) {
	applyWaivers(&g, r.waivers)
	r.gates = append(r.gates, g)
	mark := "PASS"
	if !g.Pass {
		mark = "FAIL"
	}
	fmt.Fprintf(r.stderr, "gate %-16s %s  %s\n", g.Gate, mark, g.Detail)
}

// next is the path of the next work board.
func (r *kicadRun) next(name string) string {
	r.step++
	return filepath.Join(r.work, fmt.Sprintf("%02d-%s.kicad_pcb", r.step, name))
}

func (r *kicadRun) snapshot(pcb string) (*boardSnapshot, []byte, error) {
	raw, err := r.kt.Snapshot(pcb)
	if err != nil {
		return nil, nil, err
	}
	var s boardSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, nil, fmt.Errorf("decode KiCad snapshot: %w", err)
	}
	s.sanitizeOutline()
	return &s, raw, nil
}

func runKicadRoute(o kicadRouteOpts, stdout, stderr io.Writer) error {
	waivers, err := loadWaivers(o.waivers)
	if err != nil {
		return err
	}
	if err := checkNoManual(o.noManual, waivers); err != nil {
		return err
	}
	if o.noReview && !hasWaiver(waivers, "design-review", noReviewMatch) {
		return fmt.Errorf("--no-review skips the design-review gates: it needs a signed waiver in --waivers {\"gate\":\"design-review\",\"match\":%q,\"reason\":…,\"by\":…}", noReviewMatch)
	}
	if !o.noReview && len(o.requirements) == 0 {
		return fmt.Errorf("--requirements is required: the design review judges the design against the project's own requirements (or --no-review with a signed waiver)")
	}
	in, err := loadDesignIntent(o.intent)
	if err != nil {
		return fmt.Errorf("pre-route gate: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(o.outDir, "work"), 0o755); err != nil {
		return err
	}
	if o.projectName == "" {
		o.projectName = strings.TrimSuffix(filepath.Base(o.pcb), ".kicad_pcb")
	}
	r := &kicadRun{o: o, in: in, waivers: waivers, stderr: stderr, work: filepath.Join(o.outDir, "work"),
		summary: map[string]any{"pcb": o.pcb, "intent": o.intent, "sim": o.sim, "outDir": o.outDir}}
	start := time.Now()
	r.last = start
	err = r.run()
	pass := len(r.gates) > 0 && err == nil
	for _, g := range r.gates {
		pass = pass && g.Pass
	}
	r.summary["gates"], r.summary["pass"] = r.gates, pass
	r.summary["seconds"] = time.Since(start).Round(time.Second).Seconds()
	if err != nil {
		r.summary["error"] = err.Error()
	}
	_ = writeJSONFile(filepath.Join(o.outDir, "summary.json"), r.summary)
	_ = writeJSON(stdout, r.summary)
	if err != nil {
		return err
	}
	if !pass {
		return fmt.Errorf("kicad route: gate(s) failed: %s", failedGates(r.summary))
	}
	return nil
}

func hasWaiver(ws []gateWaiver, gate, match string) bool {
	for _, w := range ws {
		if w.Gate == gate && strings.Contains(match, w.Match) {
			return true
		}
	}
	return false
}

func (r *kicadRun) run() error {
	o := r.o
	kt, err := kicad.Locate()
	r.summary["kicad"] = kt
	if err != nil {
		return err
	}
	r.kt = kt
	if v, err := kt.Version(); err == nil {
		r.summary["kicadVersion"] = v
	}
	bin, err := resolveFastroute(o.fastrouteBin)
	if err != nil {
		return err
	}
	r.o.fo.bin = bin
	r.summary["fastroute"] = bin

	// Schematic netlist (pad-net diff, review evidence).
	if o.sch != "" {
		nl, err := kicad.ExportSchNetlist(o.sch)
		if err != nil {
			return err
		}
		doc, err := nl.ToConnectivityDoc(o.projectName)
		if err != nil {
			return err
		}
		r.netlist, r.conn = nl, filepath.Join(o.outDir, "sch-connectivity.json")
		if err := writeJSONFile(r.conn, doc); err != nil {
			return err
		}
		_ = writeJSONFile(filepath.Join(o.outDir, "sch-values.json"), schValues(nl))
	}

	r.lap("setup (KiCad, fastroute, schematic netlist)")
	// 0. Design review before anything is routed.
	ev := []string{o.intent}
	for _, p := range []string{o.sim, r.conn} {
		if p != "" {
			ev = append(ev, p)
		}
	}
	r.add(r.review("design", ev))
	if !r.gates[len(r.gates)-1].Pass {
		return nil // a failing design review stops the run (gates[] says why)
	}

	r.lap("design review")
	// 1. Work copy, project rules, rip-up.
	input := filepath.Join(r.work, "00-input.kicad_pcb")
	hasPro, err := copyKicadBoard(o.pcb, input)
	if err != nil {
		return err
	}
	board := input
	inSnap, _, err := r.snapshot(board)
	if err != nil {
		return err
	}
	rules := o.rules
	if !hasPro {
		if err := os.WriteFile(strings.TrimSuffix(input, ".kicad_pcb")+".kicad_pro", kicad.MinimalProject(input), 0o644); err != nil {
			return err
		}
		rules = mergeRules(intentBoardRules(o.intent), o.rules)
	}
	// The intent's edge distance is a safety floor on an existing project too.
	if e := intentBoardRules(o.intent).EdgeMil; e > 0 && inSnap.Rules != nil && inSnap.Rules.CopperToEdgeMil+reqEpsMil < e && rules.EdgeMil < e {
		rules.EdgeMil = e
	}
	if !hasPro || o.ruleFlags || rules.EdgeMil > 0 {
		out := r.next("rules")
		res, err := kt.SetRules(board, rules, out)
		r.summary["rules"] = map[string]any{"projectCreated": !hasPro, "rules": rules, "result": res}
		if err != nil {
			return err
		}
		board = out
		fmt.Fprintf(r.stderr, "rules: %+v (project created: %v)\n", rules, !hasPro)
	}
	if o.ripUp {
		out := r.next("ripup")
		res, err := kt.RipUp(board, out)
		r.summary["ripUp"] = res
		if err != nil {
			return err
		}
		board = out
	}
	r.lap("work copy, rules, rip-up")
	if inSnap, _, err = r.snapshot(board); err != nil {
		return err
	}
	if rep, err := kt.DRC(board, filepath.Join(r.work, "input-drc.json")); err == nil {
		r.inputDRC = map[string]bool{}
		for _, v := range rep.Violations {
			r.inputDRC[v.Rule+"|"+v.Message] = true
		}
		r.summary["inputDrc"] = map[string]any{"total": rep.Total, "counts": rep.Counts}
		fmt.Fprintf(r.stderr, "input board DRC (before routing): %d error(s) %v\n", rep.Total, rep.Counts)
	}

	r.lap("input DRC")
	// 2. Pre-route gate: intent + insulation → netclasses (+ .kicad_dru).
	reqs := kicadRequirements(r.in)
	iso := kicadIsolation(r.in)
	edges := kicadEdges(o.intent, r.in)
	r.summary["isolationRules"], r.summary["edgeRules"] = iso, edges
	classed := r.next("classed")
	ncRes, err := kt.NetclassesIso(board, reqs, iso, edges, classed)
	r.summary["netclasses"] = ncRes
	if err != nil {
		return fmt.Errorf("pre-route gate: netclasses: %w", err)
	}
	if len(ncRes.Mismatched) > 0 {
		return fmt.Errorf("pre-route gate: %d net(s) do not resolve to their pcbpilot netclass: %s", len(ncRes.Mismatched), strings.Join(ncRes.Mismatched, "; "))
	}
	if len(ncRes.MissingNets) > 0 {
		fmt.Fprintf(r.stderr, "warning: %d intent net(s) not on the board (ignored): %s\n", len(ncRes.MissingNets), strings.Join(ncRes.MissingNets, ", "))
	}
	for _, n := range ncRes.MissingNets {
		delete(reqs, n)
	}
	r.classes = ncRes.Classes
	fmt.Fprintf(r.stderr, "pre-route gate: %d net(s) in %d netclass(es), %d insulation pair(s), %d custom rule(s)\n", len(reqs), len(ncRes.Classes), len(iso), ncRes.DRURules)

	r.lap("netclasses + custom rules")
	// 3. DSN → prepare → fastroute.
	if err := r.doRoute(classed, reqs, inSnap); err != nil {
		return err
	}
	r.lap("fastroute (incl. multi-start)")
	ses, _ := r.summary["ses"].(string)
	imported := r.next("imported")
	imp, err := kt.ImportSES(classed, ses, imported)
	r.summary["sesImport"] = imp
	if err != nil {
		return err
	}
	board = imported

	r.lap("SES import + zone fill")
	// 4. Post-route copper: via arrays, widen to intent, pours, widen nets, silk.
	if board, err = r.viaArrays(board); err != nil {
		return err
	}
	r.lap("via arrays")
	reqsSp := intentRequirements(r.in)
	ops, nb, err := r.widen(board, "widen to the intent width", func(tr []specctra.Track, v []widenVia, p []boardPad, clr float64) []widenOp {
		return planWidenToIntent(tr, v, p, reqsSp, clr)
	})
	r.summary["widenToIntent"] = ops
	if err != nil {
		return err
	}
	board = nb
	r.lap("widen to intent (+ DRC step-back)")
	if ps := kicadPourPlan(o, inSnap); len(ps) > 0 {
		out := r.next("pours")
		res, err := kt.Pours(board, ps, out)
		r.summary["pours"] = map[string]any{"plan": ps, "result": res}
		if err != nil {
			return err
		}
		board = out
		fmt.Fprintf(r.stderr, "pours: %d zone(s) added and filled\n", len(ps))
		if board, err = r.starvedThermals(board); err != nil {
			return err
		}
	}
	r.lap("pours + zone fill + thermals")
	if o.widenNets != "" {
		nets := map[string]bool{}
		for _, n := range strings.Split(o.widenNets, ",") {
			if n = strings.TrimSpace(n); n != "" {
				nets[n] = true
			}
		}
		ops, nb, err := r.widen(board, fmt.Sprintf("widen nets to %.1f mil", o.widenMax), func(tr []specctra.Track, v []widenVia, p []boardPad, clr float64) []widenOp {
			return planWiden(tr, v, p, nets, o.widenMax, clr)
		})
		r.summary["widened"] = ops
		if err != nil {
			return err
		}
		board = nb
	}
	r.lap("widen nets")
	if !o.noSilkPlace {
		if board, err = r.silkPlace(board); err != nil {
			return err
		}
	}

	r.lap("silkscreen placement")
	// 5–6. Gates, IR closure.
	pass, err := r.qualityGates(board)
	if err != nil {
		return err
	}
	r.lap("gates")
	for round := 1; !pass && round <= irWidenRounds; round++ {
		ratios := irOverBudget(map[string]any{"gates": r.closureGates()})
		if ratios == nil {
			break
		}
		ops, nb, err := r.widen(board, fmt.Sprintf("IR closure round %d", round), func(tr []specctra.Track, v []widenVia, p []boardPad, clr float64) []widenOp {
			return planWidenIR(tr, v, p, ratios, o.widenMax, clr)
		})
		r.summary[fmt.Sprintf("irWiden%d", round)] = ops
		if err != nil {
			return err
		}
		if len(ops) == 0 {
			break
		}
		board = nb
		if pass, err = r.qualityGates(board); err != nil {
			return err
		}
	}

	r.lap("IR closure")
	// 7. Design report, 8. release review.
	r.add(r.designReport(pass))
	r.lap("design report")
	r.summary["gates"] = r.gates
	_ = writeJSONFile(filepath.Join(o.outDir, "summary.json"), r.summary)
	relEv := append([]string{}, ev...)
	relEv = append(relEv, filepath.Join(o.outDir, "summary.json"), filepath.Join(o.outDir, "board-final.json"))
	if rep, _ := r.summary["report"].(map[string]any); rep != nil {
		if p, _ := rep["json"].(string); p != "" {
			relEv = append(relEv, p)
		}
	}
	r.add(r.review("layout", relEv))
	r.lap("release review")

	// 9. The one release sign-off (same command for EasyEDA runs).
	so := signoffOpts{board: filepath.Join(o.outDir, "board-final.json"), intent: o.intent, sim: o.sim,
		review: filepath.Join(o.outDir, "review-design", "review.json"), outDir: filepath.Join(o.outDir, "signoff")}
	if r.conn != "" {
		so.connectivity = []string{r.conn}
	}
	if m, ok := r.summary["manual"].(*manualRun); ok && m != nil {
		so.manual = m.Current
	}
	if rep, _ := r.summary["report"].(map[string]any); rep != nil {
		so.report, _ = rep["json"].(string)
	}
	g := gateResult{Gate: "signoff"}
	if res, err := runSignoff(so, r.waivers, r.stderr); err != nil {
		g.Detail = "signoff: " + err.Error()
	} else {
		g.Pass = res.Pass
		g.Detail = "pcbpilot signoff: " + filepath.Join(so.outDir, "signoff.md")
		for _, x := range res.Gates {
			if !x.Pass {
				g.Items = append(g.Items, x.Gate+": "+x.Detail)
			}
		}
	}
	r.add(g)
	r.lap("signoff")
	return nil
}

// route exports the DSN, prepares it and runs fastroute (+ multi-start).
func (r *kicadRun) doRoute(classed string, reqs map[string]kicad.NetRequirement, inSnap *boardSnapshot) error {
	o := &r.o
	rawDSN := filepath.Join(o.outDir, "route-kicad.dsn")
	if _, err := r.kt.ExportDSN(classed, rawDSN); err != nil {
		return err
	}
	r.lap("DSN export")
	dsnText, err := os.ReadFile(rawDSN)
	if err != nil {
		return err
	}
	// Copper-to-edge: the intent's per-layer distance (pcbauto.EdgeFromIntent),
	// never below the board's own copper-to-edge rule.
	edge := kicad.EdgeKeepout{}
	if pi, err := loadEdgeIntent(o.intent); err == nil {
		p := pcbauto.EdgeFromIntent(pi, nil)
		edge = kicad.EdgeKeepout{OuterMil: p.LayerReq(pcbauto.LayerTop), InnerMil: p.LayerReq(pcbauto.LayerInner1)}
	}
	if inSnap.Rules != nil {
		edge.OuterMil = math.Max(edge.OuterMil, inSnap.Rules.CopperToEdgeMil)
		edge.InnerMil = math.Max(edge.InnerMil, inSnap.Rules.CopperToEdgeMil)
	}
	r.summary["edgeKeepout"] = edge
	prepared, prep, err := kicad.PrepareDSN(string(dsnText), r.classes, reqs, edge)
	r.summary["dsnRequirements"] = prep
	if err != nil {
		return fmt.Errorf("pre-route gate: %w", err)
	}
	if len(prep.Short) > 0 {
		return fmt.Errorf("pre-route gate: %d net requirement(s) not met in the DSN: %s", len(prep.Short), strings.Join(prep.Short, "; "))
	}
	dsnPath := filepath.Join(o.outDir, "route.dsn")
	if err := os.WriteFile(dsnPath, []byte(prepared), 0o644); err != nil {
		return err
	}
	r.summary["dsn"] = dsnPath
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
			o.fo.minTraceUm = math.Ceil(floor*25.4*10) / 10
		}
	}
	base := strings.TrimSuffix(dsnPath, ".dsn")
	pairs, tune := intentPairsAndTune(r.in)
	if pairs != "" {
		o.fo.pairsFile = base + "-pairs.txt"
		if err := os.WriteFile(o.fo.pairsFile, []byte(pairs), 0o644); err != nil {
			return err
		}
	}
	if tune != "" {
		o.fo.tuneFile = base + "-tune.txt"
		if err := os.WriteFile(o.fo.tuneFile, []byte(tune), 0o644); err != nil {
			return err
		}
	}
	r.summary["intentPairs"], r.summary["intentTune"] = pairs, tune
	fmt.Fprintf(r.stderr, "pre-route gate: DSN ok (%d no-neck-down class(es), %d inner rule(s), min trace %.1f µm)\n",
		len(prep.NoNeckdown), len(prep.InnerRules), o.fo.minTraceUm)
	r.lap("DSN prepare (requirements, keep-outs)")
	// The multi-start=4 retry is independent of the main run (a fresh run on
	// the same DSN): with a spare core it runs speculatively in parallel and
	// is used only when the main run leaves connections unrouted (PicoRick:
	// 112 s + 143 s sequential → max of the two).
	ms := o.fo
	ms.multiStart = 4
	parallel := o.parallelMS && runtime.NumCPU() >= 2
	var (
		msSes  string
		msRuns []fastrouteRun
		msErr  error
		wg     sync.WaitGroup
	)
	logw := io.Writer(&syncWriter{w: r.stderr})
	if parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msSes, msRuns, msErr = runFastroute(ms, dsnPath, base+"-ms", logw)
		}()
	}
	ses, runs, err := runFastroute(o.fo, dsnPath, base, logw)
	wg.Wait()
	r.summary["router"], r.summary["routerRuns"] = "fastroute", runs
	r.summary["multiStartParallel"] = parallel
	if err != nil {
		return err
	}
	if last := lastOK(runs); last != nil && last.Unrouted > 0 {
		if !parallel {
			fmt.Fprintf(r.stderr, "multi-start: %d connection(s) still unrouted; one fresh run with --multi-start=4\n", last.Unrouted)
			msSes, msRuns, msErr = runFastroute(ms, dsnPath, base+"-ms", logw)
		}
		r.summary["multiStartRuns"] = msRuns
		if got := lastOK(msRuns); msErr == nil && got != nil && runImproved(*last, *got) {
			ses, runs = msSes, msRuns
			fmt.Fprintf(r.stderr, "multi-start: kept (%d unrouted)\n", got.Unrouted)
		}
	} else if parallel {
		r.summary["multiStartRuns"] = msRuns
		r.summary["multiStartUnused"] = "main run routed everything; the speculative multi-start run was not used"
	}
	r.route = lastOK(runs)
	r.summary["routeFinal"], r.summary["ses"] = r.route, ses
	return nil
}

// copperOf decodes a snapshot's tracks, vias and pads.
func copperOf(s *boardSnapshot) ([]specctra.Track, []widenVia, []boardPad, error) {
	var tracks []specctra.Track
	var vias []widenVia
	if s.Copper == nil {
		return nil, nil, nil, fmt.Errorf("snapshot has no copper")
	}
	if err := decodeAny(s.Copper.Lines, &tracks); err != nil {
		return nil, nil, nil, err
	}
	if err := decodeAny(s.Copper.Vias, &vias); err != nil {
		return nil, nil, nil, err
	}
	var pads []boardPad
	for _, c := range s.Components {
		pads = append(pads, c.Pads...)
	}
	return tracks, vias, pads, nil
}

func clearanceOf(s *boardSnapshot) float64 {
	clr := 8.0
	if s.Rules != nil && s.Rules.ClearanceMil > 0 {
		clr = s.Rules.ClearanceMil
	}
	return clr + kicad.ClearanceMarginMil
}

// viaArrays adds the intent's via count at every power transition
// (planViaArrays; vias + stubs written through the bridge).
func (r *kicadRun) viaArrays(board string) (string, error) {
	needs := intentViaNeeds(r.in)
	if len(needs) == 0 {
		r.summary["viaArrays"] = &viaArrayPlan{}
		return board, nil
	}
	s, _, err := r.snapshot(board)
	if err != nil {
		return board, err
	}
	if s.Outline == nil {
		return board, fmt.Errorf("via arrays: no board outline")
	}
	tracks, vias, pads, err := copperOf(s)
	if err != nil {
		return board, err
	}
	edge := 20.0
	if s.Rules != nil && s.Rules.CopperToEdgeMil > edge {
		edge = s.Rules.CopperToEdgeMil
	}
	b := s.Outline.BBox
	bounds := layoutBBox{MinX: b.MinX + edge, MinY: b.MinY + edge, MaxX: b.MaxX - edge, MaxY: b.MaxY - edge}
	plan := planViaArrays(tracks, vias, pads, needs, clearanceOf(s), bounds)
	r.summary["viaArrays"] = &plan
	fmt.Fprintf(r.stderr, "via arrays: %d via(s) and %d stub(s) added, %d transition(s) short\n", len(plan.Vias), len(plan.Stubs), len(plan.Shortfall))
	if len(plan.Vias)+len(plan.Stubs) == 0 {
		return board, nil
	}
	ops := map[string]any{}
	var av, at []map[string]any
	for _, v := range plan.Vias {
		m := map[string]any{"net": v.Net, "x": v.X, "y": v.Y}
		if v.DiameterMil > 0 {
			m["diameter"] = v.DiameterMil
		}
		if n := needs[v.Net]; n.DrillMil > 0 {
			m["drill"] = n.DrillMil
		}
		av = append(av, m)
	}
	for _, t := range plan.Stubs {
		at = append(at, map[string]any{"net": t.Net, "layer": t.Layer, "startX": t.X1, "startY": t.Y1, "endX": t.X2, "endY": t.Y2, "lineWidth": t.Width})
	}
	ops["addVias"], ops["addTracks"] = av, at
	out := r.next("via-arrays")
	if _, err := r.kt.Edit(board, ops, out); err != nil {
		return board, err
	}
	return out, nil
}

// widen plans with plan on the board's copper (clearance + margin), sets the
// widths through the bridge, then steps every widened track KiCad's DRC
// names in a clearance violation back half way, then to its old width
// (widenLive's guard).
func (r *kicadRun) widen(board, what string, plan func([]specctra.Track, []widenVia, []boardPad, float64) []widenOp) ([]widenOp, string, error) {
	s, _, err := r.snapshot(board)
	if err != nil {
		return nil, board, err
	}
	tracks, vias, pads, err := copperOf(s)
	if err != nil {
		return nil, board, err
	}
	clr := clearanceOf(s)
	ops := plan(tracks, vias, pads, clr)
	fmt.Fprintf(r.stderr, "widen: %d track(s) can grow (%s, clearance %.2f mil)\n", len(ops), what, clr)
	if len(ops) == 0 {
		return ops, board, nil
	}
	set := func(ws map[string]float64) map[string]any {
		var l []map[string]any
		for id, w := range ws {
			l = append(l, map[string]any{"id": id, "width": w})
		}
		return map[string]any{"setWidth": l}
	}
	ws := map[string]float64{}
	for _, op := range ops {
		ws[op.Track.ID] = op.NewWidth
	}
	out := r.next("widen")
	if _, err := r.kt.Edit(board, set(ws), out); err != nil {
		return ops, board, err
	}
	for round := 0; round < 2; round++ {
		rep, err := r.kt.DRC(out, filepath.Join(r.work, fmt.Sprintf("%02d-widen-drc%d.json", r.step, round)))
		if err != nil {
			return ops, out, err
		}
		bad := map[string]bool{}
		for _, v := range rep.Violations {
			if v.Rule == "clearance" {
				for _, id := range v.Objs {
					bad[id] = true
				}
			}
		}
		back := map[string]float64{}
		for i := range ops {
			op := &ops[i]
			if !bad[op.Track.ID] || op.NewWidth <= op.Track.Width {
				continue
			}
			w := op.Track.Width
			if round == 0 {
				w = math.Floor((op.Track.Width+op.NewWidth)/2*100) / 100
			}
			op.SteppedBack = append(op.SteppedBack, fmt.Sprintf("%.2f→%.2f mil (KiCad DRC clearance)", op.NewWidth, w))
			op.NewWidth = w
			back[op.Track.ID] = w
		}
		if len(back) == 0 {
			break
		}
		fmt.Fprintf(r.stderr, "widen: %d widened track(s) in a KiCad DRC clearance violation step back (%s)\n", len(back), map[bool]string{true: "half way", false: "to the old width"}[round == 0])
		if _, err := r.kt.Edit(out, set(back), out); err != nil {
			return ops, out, err
		}
	}
	return ops, out, nil
}

// silkPlace moves designators next to their parts with the shared planner
// (planSilkTight + group labels), at most 3 rounds, until the silkscreen
// gate passes.
func (r *kicadRun) silkPlace(board string) (string, error) {
	opt := r.o.silk
	rep := &silkTightReport{}
	prev := ""
	for round := 1; round <= 3; round++ {
		s, _, err := r.snapshot(board)
		if err != nil {
			return board, err
		}
		labels, sc, font := silkTightInput(s, opt)
		rep.FontSize = font
		if g := silkGate(s, font, opt); g.Pass {
			rep.Gate = g
			break
		}
		placed, notes := planSilkTight(labels, sc, opt)
		var groups []silkGroup
		if !opt.NoGroups {
			var gnotes []string
			groups, placed, gnotes = planSilkGroups(labels, sc, placed, opt)
			rep.GroupNotes = append(rep.GroupNotes, gnotes...)
		}
		rep.Placed, rep.Notes, rep.Rounds = placed, notes, round
		byID := map[string]silkLabel{}
		for _, l := range labels {
			byID[l.ID] = l
		}
		var set, add []map[string]any
		var hide []string
		for _, p := range placed {
			if !p.Moved {
				continue
			}
			l := byID[p.ID]
			op := map[string]any{"id": p.ID, "x": p.Box.cx(), "y": p.Box.cy(), "rotation": p.Rot}
			if l.Font > 0 && l.Font < font {
				op["fontSize"] = font
			}
			if opt.LineWidth > 0 {
				op["lineWidth"] = opt.LineWidth
			}
			set = append(set, op)
		}
		lw := opt.LineWidth
		if lw <= 0 {
			lw = 6
		}
		for _, g := range groups {
			add = append(add, map[string]any{"text": g.Text, "x": g.Box.cx(), "y": g.Box.cy(), "layer": g.Layer, "rotation": g.Rot, "fontSize": font, "lineWidth": lw})
			hide = append(hide, g.IDs...)
			rep.Groups = append(rep.Groups, g)
		}
		fmt.Fprintf(r.stderr, "silk round %d: %d designator(s) moved, %d group label(s)\n", round, len(set), len(groups))
		if len(set)+len(add) == 0 {
			break
		}
		// No progress: the same moves as the last round (labels the planner
		// cannot resolve cycle between the same slots) — stop, skip the edit.
		key, _ := json.Marshal([]any{set, add})
		if string(key) == prev {
			fmt.Fprintf(r.stderr, "silk round %d repeats round %d; stopped\n", round, round-1)
			break
		}
		prev = string(key)
		rep.Moved += len(set)
		out := r.next("silk")
		if _, err := r.kt.Edit(board, map[string]any{"setText": set, "addTexts": add, "hideTexts": hide}, out); err != nil {
			return board, err
		}
		board = out
	}
	r.summary["silk"] = rep
	return board, nil
}

// qualityGates copies board to <out-dir>/routed.kicad_pcb and runs every
// gate on it and its board-final.json; gates of an earlier round are
// replaced (the design-review gate stays first).
func (r *kicadRun) qualityGates(board string) (bool, error) {
	o := r.o
	keep := r.gates[:0:0]
	for _, g := range r.gates {
		if g.Gate == "design-review" {
			keep = append(keep, g)
		}
	}
	r.gates = keep
	routed := filepath.Join(o.outDir, "routed.kicad_pcb")
	removeKicadBoard(routed)
	if _, err := copyKicadBoard(board, routed); err != nil {
		return false, err
	}
	r.summary["routed"] = routed

	// KiCad DRC (errors gate; warnings reported).
	// One kicad-cli run (all severities): the errors gate and the reported
	// warnings come from the same report.
	drcPath := filepath.Join(o.outDir, "drc.json")
	allPath := filepath.Join(o.outDir, "drc-all.json")
	all, err := r.kt.DRCSeverity(routed, allPath, "all")
	var drcRep *kicad.DRCReport
	if err != nil {
		r.add(gateResult{Gate: "kicad-drc", Detail: err.Error()})
	} else {
		rep, warn, nWarn := all.SplitErrors()
		drcRep = rep
		_ = writeJSONFile(drcPath, rep)
		g := kicadDRCGate(rep, drcPath)
		pre := map[string]int{}
		for _, v := range rep.Violations {
			if r.inputDRC[v.Rule+"|"+v.Message] {
				pre[v.Rule]++
			}
		}
		for k, n := range pre {
			g.Info = append(g.Info, fmt.Sprintf("%s: %d of these already in the input board (placement / footprints, not routing)", k, n))
		}
		sort.Strings(g.Info)
		r.add(g)
		_ = writeJSONFile(filepath.Join(o.outDir, "drc-flat.json"), kicadDRCFlat(rep))
		r.summary["drc"] = map[string]any{"file": drcPath, "allFile": allPath, "total": rep.Total, "counts": rep.Counts, "warnings": nWarn, "warningCounts": warn}
	}
	snap, raw, err := r.snapshot(routed)
	if err != nil {
		return false, err
	}
	boardPath, err := writeBoardFinal(snap, o.outDir)
	if err != nil {
		return false, err
	}
	r.summary["boardFinal"] = boardPath
	r.lap("gate: board-final snapshot")
	if r.netlist != nil {
		r.add(padNetDiffGate(r.netlist.PinNets(), boardPinNets(snap), o.sch, filepath.Join(o.outDir, "net-diff.json")))
	}
	r.add(kicadIntentRulesGate(raw, r.classes))

	qo := qualityGateOpts{intent: o.intent, sim: o.sim, outDir: o.outDir, waivers: r.waivers, widthBasis: o.widthBasis,
		source: "kicad route (" + routed + ")", silk: o.silk, noManual: o.noManual, projectConfig: o.projectConfig}
	verdict, reasons, postOut := "", []string(nil), ""
	var segNeed func(specctra.Track) (float64, bool)
	var viaOK func([]string, string) (bool, string)
	if o.sim != "" {
		ps, err := postSimForGates(boardPath, qo, r.summary, r.stderr)
		if err != nil {
			verdict, reasons = "fail", []string{err.Error()}
		} else {
			verdict, reasons, postOut, segNeed, viaOK = ps.res.Verdict.Status, ps.res.Verdict.Reasons, ps.out, ps.segNeed, ps.viaOK
		}
	}
	r.lap("gate: post-layout simulation")
	snapshotIntentGates(snap, r.in, o.intent, verdict, reasons, segNeed, viaOK, true, r.add)
	r.add(kicadRouteCompleteGate(r.route, drcRep, snap))
	r.lap("gate: intent / safety snapshot gates")
	for _, g := range tailGates(snap, boardPath, postOut, qo, o.projectName, "", r.summary, r.stderr) {
		r.gates = append(r.gates, g) // tailGates applied the waivers
		fmt.Fprintf(r.stderr, "gate %-16s %v  %s\n", g.Gate, map[bool]string{true: "PASS", false: "FAIL"}[g.Pass], g.Detail)
	}
	r.lap("gate: route-complete, silkscreen, board manual")
	pass := true
	for _, g := range r.gates {
		pass = pass && g.Pass
	}
	r.summary["gates"] = r.gates
	return pass, nil
}

// designReport publishes the versioned design report (report design) from
// the run's outputs; a failure to build it is a failed gate.
func (r *kicadRun) designReport(pass bool) gateResult {
	o := r.o
	g := gateResult{Gate: "design-report"}
	ro := designReportOpts{outDir: filepath.Join(o.outDir, "report"), version: "auto", project: o.projectName, customer: o.customer,
		host: "KiCad", intent: o.intent, sim: o.sim, board: filepath.Join(o.outDir, "board-final.json"),
		drc: filepath.Join(o.outDir, "drc-flat.json"), maxImageBytes: 0, projectConfig: o.projectConfig}
	if v, ok := r.summary["kicadVersion"].(string); ok {
		ro.host += " " + v
	}
	if r.netlist != nil {
		ro.netDiff = filepath.Join(o.outDir, "net-diff.json")
		ro.values = filepath.Join(o.outDir, "sch-values.json")
	}
	if p := filepath.Join(o.outDir, "post.json"); fileExists(p) {
		ro.post = p
	}
	if m, ok := r.summary["manual"].(*manualRun); ok && m != nil && m.Current != "" {
		ro.manual = m.Current
	}
	var missing []string
	for name, p := range map[string]string{"--sim": o.sim, "post.json": ro.post, "manual": ro.manual} {
		if p == "" {
			missing = append(missing, name)
		}
	}
	dir, rep, err := runDesignReport(ro, r.stderr)
	if err != nil {
		g.Detail = "report design: " + err.Error()
		return g
	}
	r.summary["report"] = map[string]any{"dir": dir, "html": filepath.Join(dir, "report.html"), "json": filepath.Join(dir, "report.json"), "verdict": rep.Verdict.Status, "version": rep.VersionLabel}
	g.Pass = len(missing) == 0
	g.Detail = fmt.Sprintf("%s published in %s (report verdict %s, run gates %s)", rep.VersionLabel, dir, rep.Verdict.Status, map[bool]string{true: "pass", false: "fail"}[pass])
	for _, m := range missing {
		g.Items = append(g.Items, "report input missing: "+m)
	}
	return g
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// review returns the design-review gate for stage: a stored passing
// review.json for exactly these inputs is reused, else the panel runs.
func (r *kicadRun) review(stage string, evidence []string) gateResult {
	o := r.o
	if o.noReview {
		g := gateResult{Gate: "design-review", Detail: "skipped by --no-review", Items: []string{"design review skipped (" + noReviewMatch + ")"}}
		applyWaivers(&g, r.waivers)
		return g
	}
	dir := filepath.Join(o.outDir, "review-"+stage)
	var reviewers []string
	for _, s := range strings.Split(o.reviewers, ",") {
		if s = strings.TrimSpace(s); s != "" {
			reviewers = append(reviewers, s)
		}
	}
	if sha, err := reviewInputSHA(stage, o.requirements, evidence, reviewMaxBytes); err == nil {
		var old reviewRecord
		if b, err := os.ReadFile(filepath.Join(dir, "review.json")); err == nil && json.Unmarshal(b, &old) == nil &&
			old.Stage == stage && old.InputSHA256 == sha && old.Gate.Pass {
			g := old.Gate
			g.Detail = "reused " + filepath.Join(dir, "review.json") + " (same inputs): " + g.Detail
			r.summary["review-"+stage] = filepath.Join(dir, "review.json")
			return g
		}
	}
	rec, err := runReviewPanel(stage, o.requirements, evidence, reviewers, dir, o.reviewTimeout, reviewMaxBytes, r.waivers, r.stderr)
	if err != nil {
		g := gateResult{Gate: "design-review", Detail: "review-panel: " + err.Error()}
		applyWaivers(&g, r.waivers)
		return g
	}
	r.summary["review-"+stage] = filepath.Join(dir, "review.json")
	return rec.Gate
}

// reviewMaxBytes is review-panel's default per-file prompt limit.
const reviewMaxBytes = 120000

// reviewInputSHA is the inputSha256 runReviewPanel would record.
func reviewInputSHA(stage string, reqFiles, evFiles []string, limit int) (string, error) {
	read := func(files []string) (map[string]string, error) {
		m := map[string]string{}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			m[filepath.Base(f)] = string(b)
		}
		return m, nil
	}
	reqs, err := read(reqFiles)
	if err != nil {
		return "", err
	}
	ev, err := read(evFiles)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(reviewPrompt(stage, reqs, ev, limit)))), nil
}

// kicadRouteCompleteGate is routeCompleteGate with one KiCad fact added: a
// connection fastroute left unrouted passes only when its net is poured on
// the board and KiCad's DRC (the board's real connectivity) reports no
// unconnected item at all — the pour carries it (Gas V5 A: GND C2.2–U8.115,
// a fine-pitch ground pin, joined by the TOP GND pour).
func kicadRouteCompleteGate(run *fastrouteRun, drc *kicad.DRCReport, snap *boardSnapshot) gateResult {
	g := routeCompleteGate(run)
	if g.Pass || run == nil || run.Fixable != 0 || run.Unrouted <= 0 || drc == nil || drc.Counts["unconnected_items"] != 0 || snap.Copper == nil {
		return g
	}
	poured := map[string]bool{}
	for _, p := range snap.Copper.Poured {
		if m, ok := p.(map[string]any); ok {
			poured[fmt.Sprint(m["net"])] = true
		}
	}
	data, err := os.ReadFile(run.Report)
	if err != nil {
		return g
	}
	var rep struct {
		Unrouted []struct {
			Net  string `json:"net"`
			From struct {
				Component, Pin string
			} `json:"from"`
			To struct {
				Component, Pin string
			} `json:"to"`
		} `json:"unrouted"`
	}
	if json.Unmarshal(data, &rep) != nil || len(rep.Unrouted) != run.Unrouted {
		return g
	}
	var items []string
	for _, u := range rep.Unrouted {
		if !poured[u.Net] {
			return g
		}
		items = append(items, fmt.Sprintf("%s %s.%s–%s.%s: unrouted by fastroute, joined by the %s pour (KiCad DRC: 0 unconnected items)", u.Net, u.From.Component, u.From.Pin, u.To.Component, u.To.Pin, u.Net))
	}
	g.Pass, g.Items = true, nil
	g.Info = items
	g.Detail += fmt.Sprintf("; every unrouted connection is on a poured net and KiCad reports 0 unconnected items")
	return g
}

var reStarvedPad = regexp.MustCompile(`[Pp]ad (\S+) \[[^\]]*\] of (\S+)`)

// starvedThermals: a pad whose thermal spokes reach only an isolated pour
// island (KiCad starved_thermal) is taken out of the pour (zone connection
// none) — its island then disappears and the pad keeps its tracks. Kept
// only when KiCad then reports no more unconnected items than before.
func (r *kicadRun) starvedThermals(board string) (string, error) {
	rep, err := r.kt.DRC(board, filepath.Join(r.work, fmt.Sprintf("%02d-pours-drc.json", r.step)))
	if err != nil {
		return board, err
	}
	seen := map[string]bool{}
	var ops []map[string]any
	for _, v := range rep.Violations {
		if v.Rule != "starved_thermal" {
			continue
		}
		if m := reStarvedPad.FindStringSubmatch(v.Message); m != nil && !seen[m[2]+"."+m[1]] {
			seen[m[2]+"."+m[1]] = true
			ops = append(ops, map[string]any{"ref": m[2], "pad": m[1]})
		}
	}
	if len(ops) == 0 {
		return board, nil
	}
	out := r.next("thermals")
	if _, err := r.kt.Edit(board, map[string]any{"padZoneNone": ops}, out); err != nil {
		return board, err
	}
	after, err := r.kt.DRC(out, filepath.Join(r.work, fmt.Sprintf("%02d-thermals-drc.json", r.step)))
	if err != nil || after.Counts["unconnected_items"] > rep.Counts["unconnected_items"] {
		fmt.Fprintf(r.stderr, "thermals: taking %d starved pad(s) out of the pour would disconnect copper; kept\n", len(ops))
		r.summary["starvedThermals"] = map[string]any{"pads": ops, "applied": false}
		return board, nil
	}
	fmt.Fprintf(r.stderr, "thermals: %d pad(s) whose spokes reached only an isolated pour island taken out of the pour\n", len(ops))
	r.summary["starvedThermals"] = map[string]any{"pads": ops, "applied": true}
	return out, nil
}

// placementDRC are KiCad DRC rules that copper widening cannot cause or fix
// (footprint placement and library data).
var placementDRC = map[string]bool{"courtyards_overlap": true, "malformed_courtyard": true, "missing_courtyard": true,
	"lib_footprint_issues": true, "lib_footprint_mismatch": true, "footprint_type_mismatch": true, "duplicate_footprints": true,
	"extra_footprint": true, "missing_footprint": true, "footprint": true, "footprint_symbol_mismatch": true}

// closureGates is r.gates as the IR closure judges them: a kicad-drc gate
// failing only on placement / footprint rules does not hold the closure
// back (widening cannot change it; the gate still fails the run). The
// design-review gate is not copper either.
func (r *kicadRun) closureGates() []gateResult {
	var out []gateResult
	for _, g := range r.gates {
		if g.Gate == "design-review" {
			continue
		}
		if g.Gate == "kicad-drc" && !g.Pass {
			onlyPlacement := true
			if c, ok := r.summary["drc"].(map[string]any); ok {
				if counts, ok := c["counts"].(map[string]int); ok {
					for rule := range counts {
						onlyPlacement = onlyPlacement && placementDRC[rule]
					}
				}
			}
			if onlyPlacement {
				continue
			}
		}
		out = append(out, g)
	}
	return out
}

// boardPinNets maps "REF.PIN" → net of every board pad that has a net.
func boardPinNets(snap *boardSnapshot) map[string]string {
	out := map[string]string{}
	for _, c := range snap.Components {
		for _, p := range c.Pads {
			if p.Net != "" {
				out[c.Designator+"."+p.Number] = p.Net
			}
		}
	}
	return out
}

// padNetDiffGate compares the schematic's pin→net partition with the
// board's pad nets (kicad.ComparePinNets); the result also goes to path.
func padNetDiffGate(sch, board map[string]string, source, path string) gateResult {
	d := kicad.ComparePinNets(sch, board, nil)
	if path != "" {
		_ = writeJSONFile(path, map[string]any{"passed": d.Equal, "compare": d, "diffs": padNetDiffItems(d)})
	}
	g := gateResult{Gate: "pad-net-diff", Pass: d.Equal,
		Detail: fmt.Sprintf("schematic %d net(s) / %d pin(s) vs board %d net(s) / %d pad(s): %d net(s) identical (%s)", d.NetsA, d.PinsA, d.NetsB, d.PinsB, d.NetsEqual, source),
		Items:  padNetDiffItems(d)}
	for _, rn := range d.RenamedNets {
		g.Info = append(g.Info, "same pins, renamed: "+rn)
	}
	return g
}

func padNetDiffItems(d kicad.NetCompare) []string {
	var items []string
	for _, n := range d.Mismatched {
		items = append(items, "schematic net "+n+": pin set differs on the board")
	}
	for _, p := range d.OnlyA {
		items = append(items, p+": in the schematic, no such pad (with a net) on the board")
	}
	for _, p := range d.OnlyB {
		items = append(items, p+": board pad with a net, not in the schematic")
	}
	return items
}

// schValues is {"parts":{ref:{value,lcsc,mpn}}} from the schematic fields.
func schValues(nl *kicad.SchNetlist) map[string]any {
	parts := map[string]any{}
	for _, c := range nl.Components {
		if strings.HasPrefix(c.Ref, "#") {
			continue
		}
		v := map[string]string{"value": c.Value}
		for k, x := range c.Fields {
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "lcsc", "lcsc part", "lcsc part #", "jlcpcb part #", "jlcpcb part":
				v["lcsc"] = x
			case "mpn", "manufacturer part", "manufacturer part number":
				v["mpn"] = x
			}
		}
		parts[c.Ref] = v
	}
	return map[string]any{"parts": parts}
}

// syncWriter serialises writes from concurrent router runs.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
