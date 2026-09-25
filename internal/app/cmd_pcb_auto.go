package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// newPcbAutoCmd wires the offline pcbauto engine: circuit understanding,
// electrical analysis, stackup decision, placement, routing and checks. It
// never writes the editor; `run` emits an `pcbpilot apply` playbook.
func newPcbAutoCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	group := &cobra.Command{
		Use:   "auto",
		Short: "Electrical-aware auto design: analyse, decide layers, place, route, check (offline engine → apply playbook)",
		Long: `The pcbauto engine reads a measured board — a 'pcb dump --include-copper'
snapshot (--board) or the live editor — plus an optional mechanical spec
(--mech) and power budget (--power), and decides:

  • circuit understanding: functional blocks, inter-block signals, voltage
    domains, isolation barriers (creepage/clearance, slots)
  • per-net voltage/current → IPC-2221 track width, IPC-2221B clearance,
    vias per layer change, impedance widths for diff/RF nets
  • layer count and stackup (2/4/6) with planes and split power planes
  • placement honouring the mechanics and domain zones (optional, --place)
  • plane fan-out + negotiated-congestion multi-layer routing, length
    tuning, independent exact DRC and signal-integrity checks

Nothing is written to EasyEDA. 'run' writes plan.json, playbook.json,
preview.svg and report.md; execute with 'pcbpilot apply playbook.json'.`,
	}
	type inputs struct {
		board, mech, power string
		groups             []string
		layers, maxLayers  int
		grid               float64
		timeout            time.Duration
	}
	var in inputs
	addInputs := func(c *cobra.Command) {
		c.Flags().StringVar(&in.board, "board", "", "pcb dump JSON (omit to read the live editor)")
		c.Flags().StringVar(&in.mech, "mech", "", "mechanical spec JSON (outline, holes, fixed/edge parts, keepouts, zones)")
		c.Flags().StringVar(&in.power, "power", "", "power spec JSON (rails with voltage/currentA, diffPairs, tempRiseC)")
		c.Flags().StringArrayVar(&in.groups, "groups", nil, "schematic module ownership (repeatable): a sch composition JSON (modules[].placements[].designator) or {\"groups\":[{id,core,members}]} — makes each module's parts follow its core; port protection stays at its connector")
		c.Flags().IntVar(&in.layers, "layers", 0, "force the copper layer count (0 = decide)")
		c.Flags().IntVar(&in.maxLayers, "max-layers", 6, "cost cap for the layer decision")
		c.Flags().Float64Var(&in.grid, "grid", 0, "routing grid in mil (0 = derived from the rules)")
		c.Flags().DurationVar(&in.timeout, "timeout", 4*time.Minute, "routing time budget")
	}
	// understand infers the circuit and applies schematic module ownership.
	understand := func(b *pcbauto.Board, an *pcbauto.Analysis) (*pcbauto.Circuit, error) {
		c := pcbauto.Understand(b, an)
		for _, f := range in.groups {
			raw, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			gs, err := pcbauto.ParseGroups(raw)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			c.Notes = append(c.Notes, pcbauto.ApplyGroups(c, b, an, gs)...)
		}
		return c, nil
	}
	load := func() (*pcbauto.Board, *pcbauto.MechSpec, pcbauto.PowerSpec, error) {
		var power pcbauto.PowerSpec
		var raw []byte
		var err error
		if in.board != "" {
			raw, err = os.ReadFile(in.board)
		} else {
			var snap *boardSnapshot
			snap, err = fetchBoardSnapshot(cfg, *window, boardSnapshotOpts{withRules: true, withLayers: true, withCopper: true})
			if err == nil {
				raw, err = json.Marshal(snap)
			}
		}
		if err != nil {
			return nil, nil, power, err
		}
		b, err := pcbauto.FromSnapshot(raw)
		if err != nil {
			return nil, nil, power, err
		}
		// Block-declared connector openings: a symmetric screw terminal's
		// wire entry is not in its pads (the ESP32 demo's KF301 went on the
		// left edge with its entry facing inward).
		for _, p := range b.Parts {
			if x, y, ok := connOpeningFor(p.Device); ok {
				p.Opening = pcbauto.Point{X: x, Y: y}
			}
		}
		var mech *pcbauto.MechSpec
		if in.mech != "" {
			mraw, err := os.ReadFile(in.mech)
			if err != nil {
				return nil, nil, power, err
			}
			if mech, err = pcbauto.ParseMech(mraw); err != nil {
				return nil, nil, power, err
			}
		}
		if in.power != "" {
			praw, err := os.ReadFile(in.power)
			if err != nil {
				return nil, nil, power, err
			}
			if err := json.Unmarshal(praw, &power); err != nil {
				return nil, nil, power, fmt.Errorf("power spec: %w", err)
			}
		}
		return b, mech, power, nil
	}

	// ── analyze ──────────────────────────────────────────────────────────
	{
		var asJSON bool
		c := &cobra.Command{
			Use:   "analyze",
			Short: "Understand the circuit and decide widths, clearances, domains and layer count (no placement/routing)",
			Example: `  pcbpilot pcb auto analyze --board board.json
  pcbpilot pcb auto analyze --board board.json --power power.json --json`,
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				b, mech, power, err := load()
				if err != nil {
					return err
				}
				var mc *pcbauto.Mechanics
				if mech != nil {
					if mc, err = pcbauto.ApplyMech(b, mech); err != nil {
						return err
					}
				}
				pre := pcbauto.Analyze(b, power, nil)
				st := pcbauto.DecideStackup(b, pre, pcbauto.StackOptions{Force: in.layers, MaxLayers: in.maxLayers})
				an := pcbauto.Analyze(b, power, st)
				circ, err := understand(b, an)
				if err != nil {
					return err
				}
				rep := &pcbauto.Report{Result: &pcbauto.Result{Analysis: an, Stackup: st}, Circuit: circ, Mechanics: mc}
				if asJSON {
					enc := json.NewEncoder(stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(rep)
				}
				rep.WriteMarkdown(stdout)
				return nil
			},
		}
		addInputs(c)
		c.Flags().BoolVar(&asJSON, "json", false, "print JSON instead of the Markdown report")
		group.AddCommand(c)
	}

	// ── run ──────────────────────────────────────────────────────────────
	{
		var outDir, replaceJournal string
		var place, noRoute, refine bool
		var seed int64
		var loops int
		c := &cobra.Command{
			Use:   "run",
			Short: "Full pipeline: analyse → stackup → (place) → route → DRC/SI → plan.json + playbook.json + preview.svg + report.md",
			Example: `  pcbpilot pcb auto run --board board.json --out-dir out/
  pcbpilot pcb auto run --board board.json --mech mech.json --place --out-dir out/
  pcbpilot apply out/playbook.json --project demo --dry-run`,
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if outDir == "" {
					return fmt.Errorf("--out-dir is required")
				}
				if err := os.MkdirAll(outDir, 0o755); err != nil {
					return err
				}
				b, mech, power, err := load()
				if err != nil {
					return err
				}
				original := map[string]pcbauto.Placement{}
				for _, p := range b.Parts {
					original[p.Ref] = pcbauto.Placement{Ref: p.Ref, ID: p.ID, X: p.Pos.X, Y: p.Pos.Y, Rot: p.Rotation, Side: p.Side}
				}
				var replace pcbauto.MechReplace
				if replaceJournal != "" {
					raw, err := os.ReadFile(replaceJournal)
					if err != nil {
						return err
					}
					replace = pcbauto.MechFromJournal(raw)
					dh, dk := pcbauto.DropReplaced(b, replace)
					fmt.Fprintf(stderr, "replace: %d hole fill(s) and %d region(s) captured in %s are deleted first (%d holes, %d keep-outs dropped from the board model)\n",
						len(replace.Fills), len(replace.Regions), replaceJournal, dh, dk)
				}
				holesBefore, keepBefore := len(b.Holes), len(b.Keepouts)
				var frame *pcbauto.FrameSearch
				if place && pcbauto.NeedsAutoFrame(mech) {
					// autoSize without a size: search the smallest frame the
					// placer fills cleanly, then run as a fixed-size board.
					fa := pcbauto.Analyze(b, power, nil)
					fc, err := understand(b, fa)
					if err != nil {
						return err
					}
					if mech, frame, err = pcbauto.AutoFrame(b, fa, fc, mech, pcbauto.PlaceOptions{Seed: seed}); err != nil {
						return err
					}
					fmt.Fprintf(stderr, "autoSize: frame %.1f × %.1f %s (%d trials)\n", frame.Width, frame.Height, frame.Units, len(frame.Trials))
				}
				var mc *pcbauto.Mechanics
				if mech != nil {
					if mc, err = pcbauto.ApplyMech(b, mech); err != nil {
						return err
					}
				}
				rep := &pcbauto.Report{Mechanics: mc, Frame: frame}
				pre := pcbauto.Analyze(b, power, nil)
				if rep.Circuit, err = understand(b, pre); err != nil {
					return err
				}
				opts := pcbauto.Options{Power: power, Stack: pcbauto.StackOptions{Force: in.layers, MaxLayers: in.maxLayers},
					Route: pcbauto.RouteOptions{GridMil: in.grid, Timeout: in.timeout}}
				budget := in.timeout * 3
				if place && !noRoute && loops > 0 {
					budget = in.timeout * time.Duration(3*loops)
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), budget)
				defer cancel()
				// Stage 0 — physical feasibility and stackup before placement:
				// the placer works inside the decision, the post-placement check
				// may only add layers.
				if place && in.layers == 0 {
					stage := pcbauto.DecideStackup(b, pre, pcbauto.StackOptions{MaxLayers: in.maxLayers, Barriers: rep.Circuit.Barriers})
					rep.Stage0 = stage.Feasibility
					if b.CopperLayers == 0 {
						b.CopperLayers = stage.Layers
					}
					opts.Stack.MinLayers = stage.Layers
					fmt.Fprintf(stderr, "stage 0: %d layers (%s)\n", stage.Layers, strings.Join(stage.Feasibility.Reasons[len(stage.Feasibility.Reasons)-1:], ""))
					for _, n := range stage.Feasibility.Negotiation {
						fmt.Fprintf(stderr, "stage 0 negotiation: %s\n", n)
					}
				}
				looped := false
				if place && !noRoute && loops > 0 {
					lr, err := pcbauto.PlaceRoute(ctx, b, pre, rep.Circuit, mc, pcbauto.PlaceOptions{Seed: seed, Refine: refine}, opts, pcbauto.LoopOptions{Passes: loops, Budget: in.timeout * time.Duration(loops+1)})
					if err != nil {
						return err
					}
					rep.Placement, rep.Result, rep.Loop, looped = lr.Place, lr.Result, lr.Passes, true
					for _, lp := range lr.Passes {
						fmt.Fprintf(stderr, "loop pass %d: routed %.1f%%, DRC %d, joint %.1f, inflated %d %s\n", lp.Pass, lp.Completion, lp.DRC, lp.Joint, lp.Inflated, lp.Note)
					}
					fmt.Fprintf(stderr, "loop: best pass %d\n", lr.Best)
				} else if place {
					rep.Placement, err = pcbauto.Place(b, pre, rep.Circuit, mc, pcbauto.PlaceOptions{Seed: seed, Refine: refine})
					if err != nil {
						return err
					}
					fmt.Fprintf(stderr, "placement: wirelength %.1f→%.1f in, overlaps %d, out-of-zone %d\n",
						rep.Placement.Metrics.StartWireIn, rep.Placement.Metrics.WirelengthIn, rep.Placement.Metrics.Overlaps, rep.Placement.Metrics.OutOfZone)
				}
				if noRoute {
					an := pcbauto.Analyze(b, power, nil)
					st := pcbauto.DecideStackup(b, an, opts.Stack)
					rep.Result = &pcbauto.Result{Analysis: pcbauto.Analyze(b, power, st), Stackup: st}
				} else {
					if !looped {
						if rep.Result, err = pcbauto.Run(ctx, b, opts); err != nil {
							return err
						}
					}
					rep.SI = pcbauto.CheckSI(b, rep.Result.Analysis, rep.Result.Stackup, rep.Result.Route)
					overlaps := 0
					if rep.Placement != nil {
						overlaps = rep.Placement.Metrics.Overlaps
					}
					rep.Joint = pcbauto.Joint(b, rep.Result.Analysis, rep.Circuit, rep.Result.Stackup, rep.Result.Route, rep.Result.DRC,
						pcbauto.JointOptions{PlacementScore: -1, Overlaps: overlaps})
					fmt.Fprintf(stderr, "joint score %.1f (completion ×%.2f, quality %.0f)\n", rep.Joint.Overall, rep.Joint.CompletionFactor, rep.Joint.Quality)
					s := rep.Result.Route.Stats
					fmt.Fprintf(stderr, "routing: %d layers, %.1f%% (%d/%d), vias %d+%d fan-out, DRC violations %d, %.1fs\n",
						rep.Result.Stackup.Layers, s.Completion, s.Routed, s.Connections, s.Vias, s.FanoutVias,
						len(rep.Result.DRC.Violations), float64(s.Millis)/1000)
				}
				pb := pcbauto.BuildPlaybook(pcbauto.PlaybookInput{Board: b, Original: original, Result: rep.Result, Placement: rep.Placement,
					Circuit: rep.Circuit, OutlineChanged: mech != nil, NewHoles: b.Holes[holesBefore:], NewKeepouts: b.Keepouts[keepBefore:],
					Replace: replace, Name: "pcbauto " + filepath.Base(outDir)})
				write := func(name string, fn func(io.Writer) error) error {
					f, err := os.Create(filepath.Join(outDir, name))
					if err != nil {
						return err
					}
					defer f.Close()
					return fn(f)
				}
				js := func(v any) func(io.Writer) error {
					return func(w io.Writer) error {
						enc := json.NewEncoder(w)
						enc.SetIndent("", "  ")
						return enc.Encode(v)
					}
				}
				var rr *pcbauto.RouteResult
				if rep.Result != nil {
					rr = rep.Result.Route
				}
				for name, fn := range map[string]func(io.Writer) error{
					"plan.json":     js(rep),
					"playbook.json": js(pb),
					"preview.svg":   func(w io.Writer) error { return pcbauto.RenderSVG(w, b, rep.Circuit, rep.Result.Stackup, rr) },
					"report.md":     func(w io.Writer) error { rep.WriteMarkdown(w); return nil },
				} {
					if err := write(name, fn); err != nil {
						return err
					}
				}
				fmt.Fprintf(stdout, "wrote %s/{plan.json,playbook.json,preview.svg,report.md} (%d playbook steps)\n", outDir, len(pb.Steps))
				return nil
			},
		}
		addInputs(c)
		c.Flags().StringVar(&outDir, "out-dir", "", "directory for plan.json, playbook.json, preview.svg, report.md")
		c.Flags().StringVar(&replaceJournal, "replace", "", "apply journal of the previous pcbauto playbook: its captured holes/keep-outs (MECH_*) are deleted first, so a re-plan does not stack a second set")
		c.Flags().BoolVar(&place, "place", false, "run the placer (mechanics, domain zones, blocks) before routing")
		c.Flags().BoolVar(&refine, "refine", false, "with --place: refine the current placement instead of constructing one")
		c.Flags().BoolVar(&noRoute, "no-route", false, "stop after placement / stackup")
		c.Flags().Int64Var(&seed, "seed", 0, "placement random seed (runs are reproducible per seed)")
		c.Flags().IntVar(&loops, "loops", 3, "with --place: place↔route loop passes — parts near unrouted pads / DRC points are inflated and re-placed; 0 = one placement then route")
		group.AddCommand(c)
	}
	group.AddCommand(newPcbAutoBenchCmd(stdout, stderr))
	return group
}
