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
	var playbook, outDir, gndNet, powerNet, simPath, scriptPath, widenCSV, waiverPath string
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
			var waivers []gateWaiver
			if waiverPath != "" {
				raw, err := os.ReadFile(waiverPath)
				if err != nil {
					return err
				}
				if err := json.Unmarshal(raw, &waivers); err != nil {
					return fmt.Errorf("parse --waivers %s: %w", waiverPath, err)
				}
				for i, w := range waivers {
					if w.Gate == "" || w.Match == "" || w.Reason == "" || w.By == "" {
						return fmt.Errorf("waiver %d: gate, match, reason and by are all required", i)
					}
				}
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

			// 1. Placement playbook.
			if playbook != "" {
				fmt.Fprintf(stderr, "apply: %s\n", playbook)
				ac := newApplyCmd(cfg, stderr, stderr)
				a := []string{playbook, "--yes", "--quiet"}
				if cfg.doc != "" {
					a = append(a, "--doc", cfg.doc)
				}
				if *window != "" {
					a = append(a, "--window", *window)
				}
				ac.SetArgs(a)
				ac.SetOut(stderr)
				ac.SetErr(stderr)
				if err := ac.Execute(); err != nil {
					return finish(fmt.Errorf("apply %s: %w", playbook, err))
				}
				summary["playbook"] = playbook
			}

			// 2. Route + import + repair.
			routed, sessions, err := runAutorouteFlow(cfg, *window, o, summary, stderr)
			if err != nil {
				return finish(err)
			}
			if !routed {
				return finish(fmt.Errorf("no router configured"))
			}

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
				summary["widened"] = len(ops)
				if err != nil {
					return finish(err)
				}
			}

			// 5. Rebuild / save / reload / DRC / pad-net diff.
			if noPost {
				return finish(nil)
			}
			post, err := postImportChecks(cfg, *window, schFiles, scriptPath, stderr)
			summary["post"] = post
			if err != nil {
				return finish(err)
			}

			// 6. Post-layout sim on the live copper.
			var simVerdict string
			var simReasons []string
			if simPath != "" {
				snap, err := fetchBoardSnapshot(cfg, *window, boardSnapshotOpts{withRules: true, withLayers: true, withCopper: true, withFootprintHoles: true})
				if err != nil {
					return finish(fmt.Errorf("board dump: %w", err))
				}
				boardPath := filepath.Join(outDir, "board-final.json")
				blob, _ := json.MarshalIndent(snap, "", "  ")
				if err := os.WriteFile(boardPath, append(blob, '\n'), 0o644); err != nil {
					return finish(err)
				}
				po := postSimOpts{board: boardPath, sim: simPath, intent: o.intentPath,
					out: filepath.Join(outDir, "post.json"), report: filepath.Join(outDir, "post.md"), svgDir: filepath.Join(outDir, "heatmaps"),
					cell: 0.5, ambient: 25, hTop: 10, hBottom: 10, kxy: 0.3, kz: 0.3, plating: 0.7, viaDT: 10, margin: 1.2,
					source: "live board after pcb auto route (pcb dump --include-copper)"}
				res, err := runPostSim(po, stderr)
				if err != nil {
					return finish(fmt.Errorf("sim post-layout: %w", err))
				}
				simVerdict, simReasons = res.Verdict.Status, res.Verdict.Reasons
				ps := map[string]any{"verdict": res.Verdict.Status, "reasons": res.Verdict.Reasons, "out": po.out, "report": po.report}
				if res.Thermal != nil {
					ps["maxBoardC"] = res.Thermal.MaxBoardC
				}
				summary["postSim"] = ps
			}

			// 7. Post-route gates: any failure fails the run.
			var unresolved *specctra.Reconcile
			if rep, ok := summary["repair"].(*sesRepairSummary); ok && rep != nil {
				unresolved = rep.Unresolved
			}
			gates, pass := postRouteGates(cfg, *window, o.intentPath, post, simVerdict, simReasons, unresolved, waivers, stderr)
			summary["gates"], summary["pass"] = gates, pass
			if !pass {
				var failed []string
				for _, g := range gates {
					if !g.Pass {
						failed = append(failed, g.Gate)
					}
				}
				summary["sessionsKept"] = sessions
				return finish(fmt.Errorf("post-route gate failed: %s", strings.Join(failed, ", ")))
			}
			if !o.keep {
				removeFiles(sessions)
			}
			return finish(nil)
		},
	}
	o.register(c.Flags(), "fastroute", 5, true)
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
	c.Flags().StringVar(&waiverPath, "waivers", "", "JSON list of signed waivers [{gate,match,reason,by}]: a failing gate passes only when every failing item matches one")
	c.Flags().StringVar(&simPath, "sim", "", "sim.json (pcbpilot sim power): run sim post-layout on the finished live board")
	return c
}
