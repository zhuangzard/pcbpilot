package app

// cmd_pcb_auto_route.go — `pcb auto route`: the whole board after `pcb auto
// run --router fastroute`. pcbpilot places, sets the stackup, pours and
// verifies; the external router (fastroute by default) routes.
//
//   apply placement playbook → export DSN → fix → fastroute (+ continuation
//   runs until 0 unrouted) → import + repair → pours (GND on TOP/IN1/BOTTOM,
//   main power rail on IN2) → optional widen → pour rebuild → save → reload →
//   pour rebuild → native DRC → pad-net diff → sim post-layout.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

// fitPour pours net on layer over the board outline inset by that layer's
// board-edge safety distance, replacing pours of the same net on the layer
// (the `pcb pour-fit` recipe).
func fitPour(cfg *appConfig, window, net string, layer int, intentPath string, stderr io.Writer) (map[string]any, error) {
	edge := pourEdgeOpts{intentPath: intentPath}
	pol, err := edge.edgePolicy(cfg, window)
	if err != nil {
		return nil, err
	}
	inset := edge.insetFor(pol, layer, net, stderr)
	points, _, err := pourBoundary(cfg, window, inset)
	if err != nil {
		return nil, err
	}
	cleared := 0
	if lr, err := requestAction(cfg, "pcb.pour.list", window, nil); err == nil {
		var ids []any
		pours, _ := lr.Result["pours"].([]any)
		for _, pi := range pours {
			if pm, ok := pi.(map[string]any); ok && asString(pm["net"]) == net && pourOnLayer(pm, layer) {
				if id := asString(pm["primitiveId"]); id != "" {
					ids = append(ids, id)
				}
			}
		}
		if len(ids) > 0 {
			if _, err := requestAction(cfg, "pcb.pour.delete", window, map[string]any{"primitiveIds": ids}); err == nil {
				cleared = len(ids)
			}
		}
	}
	if _, err := requestAction(cfg, "pcb.pour.create", window, map[string]any{"points": points, "net": net, "layer": layer}); err != nil {
		return nil, fmt.Errorf("pour %s on layer %d: %w", net, layer, err)
	}
	return map[string]any{"net": net, "layer": layer, "inset": inset, "cleared": cleared}, nil
}

// mainPowerRail picks the non-ground power net with the most pads.
func mainPowerRail(pads []pcbPadP) string {
	count := map[string]int{}
	for _, p := range pads {
		if isGlobalNet(p.Net) && !isGndNetName(p.Net) {
			count[p.Net]++
		}
	}
	nets := make([]string, 0, len(count))
	for n := range count {
		nets = append(nets, n)
	}
	sort.Slice(nets, func(i, j int) bool {
		if count[nets[i]] != count[nets[j]] {
			return count[nets[i]] > count[nets[j]]
		}
		return nets[i] < nets[j]
	})
	if len(nets) == 0 {
		return ""
	}
	return nets[0]
}

// defaultPourLayers: GND on TOP, IN1 and BOTTOM and the main rail on IN2 for
// 4+ layers (the Gas Module V5 recipe); GND on TOP and BOTTOM for 2 layers.
func defaultPourLayers(copper int) (gnd []int, power int) {
	if copper >= 4 {
		return []int{1, 15, 2}, 16
	}
	return []int{1, 2}, 0
}

func newPcbAutoRouteCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var o autorouteOpts
	var playbook, outDir, gndNet, powerNet, simPath, scriptPath, widenCSV, waiverPath, widthBasis, candDir string
	var trialTime time.Duration
	silkOpt := defaultSilkTightOpts()
	var gndLayers []int
	var powerLayer int
	var widenMax float64
	var noPours, noPost bool
	var schFiles []string
	c := &cobra.Command{
		Use:   "route",
		Short: "Finish a placed board with an external router: apply placement → fastroute → repair → pours → DRC → pad-net diff → post-layout sim",
		Long: `Run the live half of the pcb auto flow when routing is done by fastroute
(the default router; 'pcb auto run --router fastroute' writes a placement-only
playbook):

  1. --playbook: apply it (pcbpilot apply --yes);
  2. export DSN → fix → fastroute, with continuation runs (--initial-session)
     until 0 unrouted or --continue runs → rip-up + import → repair
     (see 'pcb autoroute');
  3. pours: --gnd-net on --gnd-layers (default TOP, IN1, BOTTOM) and the
     main power rail (--power-net, default the non-GND rail with most pads)
     on --power-layer (default IN2), all as pours on SIGNAL layers;
  4. --widen-net: widen those nets up to --widen-max-mil (see 'pcb widen');
  5. pour rebuild → save → reload → pour rebuild → native DRC → pad-net diff
     (--sch-connectivity);
  6. --sim: dump the live board (with copper) and run sim post-layout.

Everything is written to --out-dir (summary.json, board-final.json, post.*).
fastroute is never downloaded: see 'pcb autoroute --help'.

` + viaToPourNote,
		Args: cobra.NoArgs,
		Example: `  pcbpilot pcb auto run --board board.json --mech mech.json --place --out-dir out/   # placement only (fastroute installed)
  pcbpilot pcb auto route --playbook out/playbook.json --out-dir out/live \
      --escapes escapes.json --sch-connectivity p1.json --sch-connectivity p2.json \
      --widen-net SV1_DRV,SV2_DRV --sim sim.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A starved daemon must not end a run of hours (see daemonWaitBudget).
			daemonWaitBudget = 2 * time.Minute
			o.timeoutSet = cmd.Flags().Changed("router-timeout")
			o.minTraceSet = cmd.Flags().Changed("min-trace-um")
			if o.intentPath == "" {
				return fmt.Errorf("--intent is required: the intent (intent derive: schematic + simulation) is the pre-route gate for widths, clearances and neck-down")
			}
			if simPath == "" || len(schFiles) == 0 {
				return fmt.Errorf("--sim and --sch-connectivity are required: post-layout simulation and the pad-net diff are post-route gates")
			}
			if noPost {
				return fmt.Errorf("--no-post skips the post-route gates; use 'pcb autoroute' for an ungated run")
			}
			if widthBasis != "net" && widthBasis != "segment" {
				return fmt.Errorf("--width-basis must be net or segment")
			}
			waivers, err := loadWaivers(waiverPath)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				return err
			}
			summary := map[string]any{}
			start := time.Now()
			finish := func(err error) error {
				summary["seconds"] = time.Since(start).Round(time.Second).Seconds()
				if err != nil {
					summary["error"] = err.Error()
				}
				if f, ferr := os.Create(filepath.Join(outDir, "summary.json")); ferr == nil {
					_ = writeJSON(f, summary)
					f.Close()
				}
				_ = writeJSON(stdout, summary)
				return err
			}

			// 1. Placement playbook (and, with --candidates, trial-route the
			// runner-up placements and keep the most routable).
			applySeq := 0
			apply := func(path string) error {
				// Re-applying a playbook must not add its holes / keep-outs
				// a second time (v22 B rerun: Slot Region to Slot Region at
				// every mounting hole).
				if p2, skipped, err := withoutExistingMech(cfg, *window, path, outDir); err != nil {
					return err
				} else if len(skipped) > 0 {
					fmt.Fprintf(stderr, "apply: %d mechanics step(s) already on the board, skipped: %s\n", len(skipped), strings.Join(skipped, ", "))
					path = p2
				}
				fmt.Fprintf(stderr, "apply: %s\n", path)
				ac := newApplyCmd(cfg, stderr, stderr)
				// Every apply keeps its own journal: a playbook applied before
				// (a candidate trial) left "ok" rows that --resume counted as
				// done, so the final re-apply skipped steps 102–188 and routed a
				// half-moved board (v23 B live3: 44 unrouted).
				applySeq++
				journal := fmt.Sprintf("%s.run%d.journal.jsonl", strings.TrimSuffix(path, ".json"), applySeq)
				_ = os.Remove(journal)
				a := []string{path, "--yes", "--quiet", "--journal", journal}
				if cfg.doc != "" {
					a = append(a, "--doc", cfg.doc)
				}
				if *window != "" {
					a = append(a, "--window", *window)
				}
				ac.SetArgs(a)
				ac.SetOut(stderr)
				ac.SetErr(stderr)
				err := ac.Execute()
				// A step that failed under load (v23 B: place-C7 of 188, the
				// daemon starved) resumes from the journal instead of ending
				// the run; the ok steps are not repeated.
				for retry := 1; err != nil && retry <= applyResumes; retry++ {
					fmt.Fprintf(stderr, "apply %s failed (%v); resuming from the journal in 30 s (%d/%d)\n", path, err, retry, applyResumes)
					time.Sleep(30 * time.Second)
					rc := newApplyCmd(cfg, stderr, stderr)
					rc.SetArgs(append(append([]string{}, a...), "--resume"))
					rc.SetOut(stderr)
					rc.SetErr(stderr)
					err = rc.Execute()
				}
				if err != nil {
					return fmt.Errorf("apply %s: %w", path, err)
				}
				return nil
			}
			if playbook != "" {
				if err := apply(playbook); err != nil {
					return finish(err)
				}
				summary["playbook"] = playbook
			}
			// Routing left on the board (an aborted run's raw import) would
			// enter the DSN of every trial and the final route as existing
			// wiring (v22 B rerun: 23–33 unrouted, 2252 violations): clear
			// the unlocked routing before the first export.
			if o.ripUp {
				fmt.Fprintln(stderr, "rip-up: removing unlocked routing before the trials and the DSN export")
				if _, err := requestActionTimed(cfg, "pcb.route.rip_up", *window, map[string]any{}, 10*time.Minute); err != nil {
					return finish(fmt.Errorf("rip-up: %w", err))
				}
			}
			if candDir != "" {
				if playbook == "" {
					return finish(fmt.Errorf("--candidates needs --playbook (the best placement, applied first)"))
				}
				chosen, trials, err := trialCandidates(cfg, *window, o, playbook, candDir, outDir, trialTime, apply, stderr)
				summary["candidateTrials"] = trials
				if err != nil {
					return finish(err)
				}
				summary["playbook"] = chosen
			}

			// 2. Route + import + repair.
			routed, sessions, err := runAutorouteFlow(cfg, *window, o, summary, stderr)
			if err != nil {
				return finish(err)
			}
			if !routed {
				return finish(fmt.Errorf("no router configured"))
			}

			in, err := loadDesignIntent(o.intentPath)
			if err != nil {
				return finish(err)
			}
			// 2b. Via arrays: every power transition gets the intent's via count.
			va, err := applyViaArrays(cfg, *window, in, stderr)
			summary["viaArrays"] = va
			if err != nil {
				return finish(err)
			}

			// 2c. Widen every under-width track towards its intent width.
			wi, err := widenToIntent(cfg, *window, in, stderr)
			summary["widenToIntent"] = wi
			if err != nil {
				return finish(err)
			}
			widened := len(wi)

			// 3. Pours.
			if !noPours {
				copper, err := fetchCopperLayerCount(cfg, *window)
				if err != nil {
					return finish(fmt.Errorf("copper layer count: %w", err))
				}
				defG, defP := defaultPourLayers(copper)
				if !cmd.Flags().Changed("gnd-layers") {
					gndLayers = defG
				}
				if !cmd.Flags().Changed("power-layer") {
					powerLayer = defP
				}
				var pours []map[string]any
				for _, l := range gndLayers {
					fmt.Fprintf(stderr, "pour: %s on layer %d\n", gndNet, l)
					p, err := fitPour(cfg, *window, gndNet, l, o.intentPath, stderr)
					if err != nil {
						return finish(err)
					}
					pours = append(pours, p)
				}
				if powerLayer > 0 {
					rail := powerNet
					if rail == "" {
						pads, err := fetchPcbPads(cfg, *window)
						if err != nil {
							return finish(fmt.Errorf("read pads for the power rail: %w", err))
						}
						rail = mainPowerRail(pads)
					}
					if rail == "" {
						fmt.Fprintln(stderr, "pour: no non-ground power net found; power layer left without a pour")
					} else {
						fmt.Fprintf(stderr, "pour: %s on layer %d\n", rail, powerLayer)
						p, err := fitPour(cfg, *window, rail, powerLayer, o.intentPath, stderr)
						if err != nil {
							return finish(err)
						}
						pours = append(pours, p)
					}
				}
				summary["pours"] = pours
			}

			// 4. Widen.
			if widenCSV != "" {
				nets := map[string]bool{}
				for _, n := range strings.Split(widenCSV, ",") {
					if n = strings.TrimSpace(n); n != "" {
						nets[n] = true
					}
				}
				ops, err := widenNets(cfg, *window, nets, widenMax, 0, false, stderr)
				summary["widened"] = ops
				if err != nil {
					return finish(err)
				}
				widened += len(ops)
			}
			// Widening deletes and recreates tracks: check the board still
			// carries the whole session.
			{
				if rep, ok := summary["repair"].(*sesRepairSummary); ok && rep != nil && widened > 0 {
					sesPath, _ := summary["ses"].(string)
					dsnPath, _ := summary["dsnFixed"].(string)
					sesText, err1 := os.ReadFile(sesPath)
					dsnText, err2 := os.ReadFile(dsnPath)
					if err1 != nil || err2 != nil {
						return finish(fmt.Errorf("reconcile after widen: %v %v", err1, err2))
					}
					rep.Unresolved = nil
					if err := reconcileWithSession(cfg, *window, string(sesText), string(dsnText), rep, stderr); err != nil {
						return finish(fmt.Errorf("reconcile after widen: %w", err))
					}
				}
			}

			// 5–7. Rebuild / save / reload / DRC / pad-net diff, post-layout
			// sim, post-route gates: any failure fails the run.
			if noPost {
				return finish(nil)
			}
			var unresolved *specctra.Reconcile
			if rep, ok := summary["repair"].(*sesRepairSummary); ok && rep != nil {
				unresolved = rep.Unresolved
			}
			// Designators next to their own parts (never shrunk); the
			// silkscreen gate below judges the readback.
			silkRep, err := runSilkTight(cfg, *window, silkOpt, 3, false, stderr)
			summary["silk"] = silkRep
			if err != nil {
				return finish(fmt.Errorf("silk placement: %w", err))
			}
			gateOpts := qualityGateOpts{intent: o.intentPath, sim: simPath, sch: schFiles, script: scriptPath,
				outDir: outDir, waivers: waivers, sessionChecked: true, unresolved: unresolved, widthBasis: widthBasis, source: "live board after pcb auto route", silk: silkOpt, routeChecked: summary["router"] == "fastroute"}
			gateOpts.route, _ = summary["routeFinal"].(*fastrouteRun)
			pass, err := runQualityGates(cfg, *window, gateOpts, summary, stderr)
			if err != nil {
				return finish(err)
			}
			// IR closure: the width gate sizes by current, the drop gate
			// also by length (Gas Module V5 B v17: SV1_DRV 0.34 A on a
			// 1551 mil, 10 mil inner track dropped 57 mV of 30). Widen the
			// over-budget nets by drop/budget and judge every gate again.
			for round := 1; !pass && round <= irWidenRounds; round++ {
				ratios := irOverBudget(summary)
				if ratios == nil {
					break
				}
				ops, err := widenLive(cfg, *window, func(tr []specctra.Track, v []widenVia, p []boardPad, clr float64) []widenOp {
					return planWidenIR(tr, v, p, ratios, widenMax, clr)
				}, fmt.Sprintf("IR closure round %d", round), 0, false, stderr)
				summary[fmt.Sprintf("irWiden%d", round)] = ops
				if err != nil {
					return finish(err)
				}
				if len(ops) == 0 {
					break
				}
				if rep, ok := summary["repair"].(*sesRepairSummary); ok && rep != nil {
					sesText, err1 := os.ReadFile(summary["ses"].(string))
					dsnText, err2 := os.ReadFile(summary["dsnFixed"].(string))
					if err1 != nil || err2 != nil {
						return finish(fmt.Errorf("reconcile after IR widen: %v %v", err1, err2))
					}
					rep.Unresolved = nil
					if err := reconcileWithSession(cfg, *window, string(sesText), string(dsnText), rep, stderr); err != nil {
						return finish(fmt.Errorf("reconcile after IR widen: %w", err))
					}
					gateOpts.unresolved = rep.Unresolved
				}
				if pass, err = runQualityGates(cfg, *window, gateOpts, summary, stderr); err != nil {
					return finish(err)
				}
			}
			if !pass {
				summary["sessionsKept"] = sessions
				return finish(fmt.Errorf("post-route gate failed: %s", failedGates(summary)))
			}
			if !o.keep {
				removeFiles(sessions)
			}
			return finish(nil)
		},
	}
	o.register(c.Flags(), "fastroute", 5, true)
	c.Flags().StringVar(&candDir, "candidates", "", "directory of runner-up placement playbooks (pcb auto run writes <out-dir>/candidates): each is applied and trial-routed with fastroute for --trial-time, the most routable placement is kept (fewest unrouted, then fixable violations)")
	c.Flags().DurationVar(&trialTime, "trial-time", 2*time.Minute, "fastroute time per candidate trial")
	c.Flags().StringVar(&playbook, "playbook", "", "placement playbook from 'pcb auto run' to apply first")
	c.Flags().StringVar(&outDir, "out-dir", "pcb-auto-route", "directory for summary.json, board-final.json and post-layout results")
	c.Flags().StringVar(&gndNet, "gnd-net", "GND", "ground net to pour")
	c.Flags().IntSliceVar(&gndLayers, "gnd-layers", nil, "layers for the ground pour (default 1,15,2 on 4+ layers, 1,2 on 2 layers)")
	c.Flags().StringVar(&powerNet, "power-net", "", "power rail poured on --power-layer (default: the non-GND rail with most pads)")
	c.Flags().IntVar(&powerLayer, "power-layer", 0, "layer for the power rail pour (default 16 on 4+ layers; 0 = none)")
	c.Flags().BoolVar(&noPours, "no-pours", false, "skip the pours")
	c.Flags().StringVar(&widenCSV, "widen-net", "", "nets to widen after routing, comma-separated (see 'pcb widen')")
	c.Flags().Float64Var(&widenMax, "widen-max-mil", 40, "maximum width for --widen-net (mil)")
	c.Flags().BoolVar(&noPost, "no-post", false, "skip pour rebuild / save / reload / DRC / pad-net diff / post-layout sim")
	c.Flags().StringArrayVar(&schFiles, "sch-connectivity", nil, "schematic connectivity JSON for the pad-net diff (repeat per page)")
	c.Flags().StringVar(&scriptPath, "pad-net-diff-script", "", "path to pad-net-diff.py (auto-detected if omitted)")
	c.Flags().StringVar(&widthBasis, "width-basis", "segment", widthBasisHelp)
	c.Flags().StringVar(&waiverPath, "waivers", "", "JSON list of signed waivers [{gate,match,reason,by}]: a failing gate passes only when every failing item matches one")
	c.Flags().StringVar(&simPath, "sim", "", "sim.json (pcbpilot sim power): run sim post-layout on the finished live board")
	addSilkTightFlags(c, &silkOpt, "silk-")
	return c
}

// candidateTrial is one placement's trial route.
type candidateTrial struct {
	Playbook string  `json:"playbook"`
	Unrouted int     `json:"unrouted"`
	Fixable  int     `json:"fixableViolations"`
	// LoopIR is Σ current × pad span (A·mil) of the intent's nets carrying
	// ≥ 0.2 A: the tie-break among equally routable placements (Gas Module
	// V5 B v20: seed-1 and seed-9 both routed, the kept one had long drains).
	LoopIR  float64 `json:"loopIR"`
	Seconds float64 `json:"seconds"`
	Error   string  `json:"error,omitempty"`
}

// trialCandidates trial-routes the applied best placement and every runner-up
// in candDir (their place-* steps only: poses are absolute and the mechanics
// are already on the board), then re-applies the most routable one. A
// geometrically better placement is not always more routable (Gas Module V5
// B: the seed-7 window left 3–4 connections unrouted).
func trialCandidates(cfg *appConfig, window string, o autorouteOpts, best, candDir, outDir string, budget time.Duration,
	apply func(string) error, stderr io.Writer) (string, []candidateTrial, error) {
	paths := []string{best}
	ms, _ := filepath.Glob(filepath.Join(candDir, "*", "playbook.json"))
	sort.Strings(ms)
	paths = append(paths, ms...)
	trialDir := filepath.Join(outDir, "trials")
	if err := os.MkdirAll(trialDir, 0o755); err != nil {
		return best, nil, err
	}
	placeOnly := func(path string, i int) (string, error) {
		pb, _, err := loadPlaybook(path)
		if err != nil {
			return "", err
		}
		cp := *pb
		cp.Steps = nil
		for _, st := range pb.Steps {
			if strings.HasPrefix(st.ID, "place-") || st.ID == "save" {
				cp.Steps = append(cp.Steps, st)
			}
		}
		out := filepath.Join(trialDir, fmt.Sprintf("place-%d.json", i))
		blob, _ := json.MarshalIndent(&cp, "", "  ")
		return out, os.WriteFile(out, append(blob, '\n'), 0o644)
	}
	var currents map[string]float64
	if o.intentPath != "" {
		if in, err := loadDesignIntent(o.intentPath); err == nil {
			currents = intentLoopCurrents(in)
		}
	}
	var trials []candidateTrial
	bestIdx := -1
	last := 0
	for i, p := range paths {
		tr := candidateTrial{Playbook: p, Unrouted: -1, Fixable: -1}
		if i > 0 {
			po, err := placeOnly(p, i)
			if err == nil {
				err = apply(po)
			}
			if err != nil {
				tr.Error = err.Error()
				trials = append(trials, tr)
				continue
			}
			last = i
		}
		if currents != nil {
			if snap, err := fetchBoardSnapshot(cfg, window, boardSnapshotOpts{}); err == nil {
				tr.LoopIR = loopIR(snap, currents)
			}
		}
		run, err := trialRoute(cfg, window, o, budget, filepath.Join(trialDir, fmt.Sprintf("t%d", i)), stderr)
		tr.Seconds = run.Seconds
		if err != nil {
			tr.Error = err.Error()
		} else {
			tr.Unrouted, tr.Fixable = run.Unrouted, run.Fixable
		}
		fmt.Fprintf(stderr, "candidate %d (%s): %d unrouted, %d fixable, loop I·span %.0f A·mil after %.0f s\n", i, filepath.Base(filepath.Dir(p)), tr.Unrouted, tr.Fixable, tr.LoopIR, tr.Seconds)
		trials = append(trials, tr)
		if tr.Error == "" && (bestIdx < 0 || betterTrial(tr, trials[bestIdx])) {
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return best, trials, fmt.Errorf("no candidate placement could be trial-routed")
	}
	if bestIdx != last {
		po, err := placeOnly(paths[bestIdx], bestIdx)
		if err == nil {
			err = apply(po)
		}
		if err != nil {
			return paths[bestIdx], trials, err
		}
	}
	fmt.Fprintf(stderr, "candidates: keeping %s (%d unrouted in the trial)\n", paths[bestIdx], trials[bestIdx].Unrouted)
	return paths[bestIdx], trials, nil
}

// trialRoute exports the live board, prepares the DSN exactly as the real
// flow does (fixes, intent requirements, pre-escapes) and runs fastroute
// once for budget.
func trialRoute(cfg *appConfig, window string, o autorouteOpts, budget time.Duration, base string, stderr io.Writer) (fastrouteRun, error) {
	bin, err := resolveFastroute(o.fastrouteBin)
	if err != nil {
		return fastrouteRun{}, err
	}
	if err := saveAndReload(cfg, window); err != nil {
		return fastrouteRun{}, err
	}
	res, err := requestActionTimed(cfg, "pcb.export.dsn", window, map[string]any{}, 5*time.Minute)
	if err != nil {
		return fastrouteRun{}, err
	}
	src := ""
	for _, a := range res.Artifacts {
		if a.Path != "" {
			src = a.Path
			break
		}
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return fastrouteRun{}, fmt.Errorf("trial export: %w", err)
	}
	opt, err := o.fx.options()
	if err != nil {
		return fastrouteRun{}, err
	}
	if n, lerr := fetchCopperLayerCount(cfg, window); lerr == nil && opt.CopperLayers == 0 {
		opt.CopperLayers = n
	}
	if !o.noPreEscape {
		if pre, _, err := plannedPreEscapes(cfg, window, string(raw), opt.Escapes); err == nil {
			opt.Escapes = append(opt.Escapes, pre...)
		}
	}
	var reqs map[string]specctra.NetRequirement
	if o.intentPath != "" {
		in, err := loadDesignIntent(o.intentPath)
		if err != nil {
			return fastrouteRun{}, err
		}
		reqs = intentRequirements(in)
	}
	text, _, rq, err := prepareDSN(string(raw), opt, reqs)
	if err != nil {
		return fastrouteRun{}, err
	}
	fo := o.fo
	fo.bin, fo.rounds, fo.maxTime, fo.timeout = bin, 0, budget, budget+5*time.Minute
	if rq != nil {
		fo.noNeckdown = rq.NoNeckdown
		if !o.minTraceSet && rq.MinTraceMil > 0 {
			fo.minTraceUm = math.Round(rq.MinTraceMil*25.4*10) / 10
		}
	}
	dsn := base + ".dsn"
	if err := os.WriteFile(dsn, []byte(text), 0o644); err != nil {
		return fastrouteRun{}, err
	}
	_, runs, err := runFastroute(fo, dsn, base, stderr)
	if err != nil {
		return fastrouteRun{}, err
	}
	if r := lastOK(runs); r != nil {
		return *r, nil
	}
	return fastrouteRun{}, fmt.Errorf("trial produced no result")
}

// betterTrial ranks trial-routed placements: fewest unrouted, then fewest
// fixable violations, then the shortest high-current loops (LoopIR).
func betterTrial(a, b candidateTrial) bool {
	if a.Unrouted != b.Unrouted {
		return a.Unrouted < b.Unrouted
	}
	if a.Fixable != b.Fixable {
		return a.Fixable < b.Fixable
	}
	return a.LoopIR < b.LoopIR
}

// intentLoopCurrents: the intent's non-ground nets carrying ≥ 0.2 A.
func intentLoopCurrents(in *designIntent) map[string]float64 {
	out := map[string]float64{}
	for name, n := range in.Nets {
		if n.CurrentA >= 0.2 && !strings.EqualFold(n.Role, "ground") {
			out[name] = n.CurrentA
		}
	}
	return out
}

// loopIR is Σ current × half-perimeter of the net's pads (A·mil): the IR
// drop and loss a placement commits its high-current nets to.
func loopIR(snap *boardSnapshot, currents map[string]float64) float64 {
	type span struct{ minX, minY, maxX, maxY float64 }
	sp := map[string]*span{}
	for _, c := range snap.Components {
		for _, p := range c.Pads {
			if _, ok := currents[p.Net]; !ok {
				continue
			}
			s := sp[p.Net]
			if s == nil {
				sp[p.Net] = &span{p.X, p.Y, p.X, p.Y}
				continue
			}
			s.minX, s.minY = math.Min(s.minX, p.X), math.Min(s.minY, p.Y)
			s.maxX, s.maxY = math.Max(s.maxX, p.X), math.Max(s.maxY, p.Y)
		}
	}
	total := 0.0
	for net, s := range sp {
		total += currents[net] * ((s.maxX - s.minX) + (s.maxY - s.minY))
	}
	return total
}

// withoutExistingMech returns a copy of the playbook (in outDir) without the
// MULTI-layer fill / region steps whose shape the board already carries
// (bounding box within 1 mil), and their ids. ("", nil) when nothing is on
// the board yet.
func withoutExistingMech(cfg *appConfig, window, path, outDir string) (string, []string, error) {
	pb, _, err := loadPlaybook(path)
	if err != nil {
		return "", nil, err
	}
	snap, err := fetchBoardSnapshot(cfg, window, boardSnapshotOpts{withCopper: true})
	if err != nil || snap.Copper == nil {
		return "", nil, err
	}
	steps, skipped := dropExistingMechSteps(pb.Steps, snap.Copper.Fills, snap.Copper.Regions)
	if len(skipped) == 0 {
		return "", nil, nil
	}
	pb.Steps = steps
	out := filepath.Join(outDir, strings.TrimSuffix(filepath.Base(path), ".json")+".nomech.json")
	blob, _ := json.MarshalIndent(pb, "", "  ")
	return out, skipped, os.WriteFile(out, append(blob, '\n'), 0o644)
}

// dropExistingMechSteps removes pcb.fill.create / pcb.region.create steps on
// the MULTI layer whose points' box matches a live fill's / region's box.
//
// Shapes the playbook deletes itself (its --replace steps) do not count: a
// first apply deletes the old holes and creates identical new ones.
func dropExistingMechSteps(steps []playbookStep, fills, regions []any) ([]playbookStep, []string) {
	deleted := map[string]bool{}
	for _, st := range steps {
		if st.Action == "pcb.fill.delete" || st.Action == "pcb.region.delete" {
			ids, _ := st.Payload["primitiveIds"].([]any)
			for _, id := range ids {
				if s, ok := id.(string); ok {
					deleted[s] = true
				}
			}
		}
	}
	boxes := func(items []any) [][4]float64 {
		var out [][4]float64
		for _, it := range items {
			m, _ := it.(map[string]any)
			if id, _ := m["primitiveId"].(string); deleted[id] {
				continue
			}
			bb, _ := m["bbox"].(map[string]any)
			if l, _ := asFloatOK(m["layer"]); bb == nil || int(l) != pcbLayerMulti {
				continue
			}
			x0, _ := asFloatOK(bb["minX"])
			y0, _ := asFloatOK(bb["minY"])
			x1, _ := asFloatOK(bb["maxX"])
			y1, _ := asFloatOK(bb["maxY"])
			out = append(out, [4]float64{x0, y0, x1, y1})
		}
		return out
	}
	live := map[string][][4]float64{"pcb.fill.create": boxes(fills), "pcb.region.create": boxes(regions)}
	var kept []playbookStep
	var skipped []string
	for _, st := range steps {
		have, mech := live[st.Action]
		if l, _ := asFloatOK(st.Payload["layer"]); !mech || int(l) != pcbLayerMulti {
			kept = append(kept, st)
			continue
		}
		pts, _ := st.Payload["points"].([]any)
		b := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
		for _, p := range pts {
			xy, _ := p.([]any)
			if len(xy) < 2 {
				continue
			}
			x, _ := asFloatOK(xy[0])
			y, _ := asFloatOK(xy[1])
			b = [4]float64{math.Min(b[0], x), math.Min(b[1], y), math.Max(b[2], x), math.Max(b[3], y)}
		}
		dup := false
		for _, h := range have {
			// The host's box includes the outline stroke (±0.5 mil).
			if math.Abs((h[0]+h[2])/2-(b[0]+b[2])/2) <= 1 && math.Abs((h[1]+h[3])/2-(b[1]+b[3])/2) <= 1 &&
				math.Abs((h[2]-h[0])-(b[2]-b[0])) <= 2 && math.Abs((h[3]-h[1])-(b[3]-b[1])) <= 2 {
				dup = true
				break
			}
		}
		if dup {
			skipped = append(skipped, st.ID)
			continue
		}
		kept = append(kept, st)
	}
	return kept, skipped
}

// applyResumes bounds how often pcb auto route resumes a failed playbook.
const applyResumes = 2
