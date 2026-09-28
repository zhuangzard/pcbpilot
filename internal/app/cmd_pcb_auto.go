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
	"github.com/zhuangzard/pcbpilot/pkg/designreport"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// newPcbAutoCmd wires the offline pcbauto engine: circuit understanding,
// electrical analysis, stackup decision, placement, routing and checks. It
// never writes the editor; `run` emits an `pcbpilot apply` playbook.
func newPcbAutoCmd(cfg *appConfig, window *string, stdout, stderr io.Writer, aliasParents ...*cobra.Command) *cobra.Command {
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
		sim, irBudget      string
		pinCaps            string
		intent             string
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
		c.Flags().StringVar(&in.sim, "sim", "", "simulated per-pin currents (pcbpilot sim power JSON, schemaVersion 1): nets it lists are sized from the \"worst\" scenario (else the per-pin maximum), power copper is tapered per segment to its own current, fan-out vias are counted per pad, and the routed copper is solved for DC IR drop")
		c.Flags().StringVar(&in.intent, "intent", "", "intent.json (pcbpilot intent derive): declared per-net width/current/vias/clearance and net classes win over --power/--sim/inference (source \"intent\"); its domains and insulation pairs (IEC 62368-1 / 60601-1 MOOP·MOPP / 61010-1 / IPC-2221B) drive the domain zones, the isolation band (= pair creepage), per-pair clearance in routing and DRC, milled slots under bridge parts whose pad rows are closer than the creepage (pcb.fill.create on MULTI = board cutout), no-pour regions, and the post-route creepage check")
		c.Flags().StringVar(&in.irBudget, "ir-budget", "", "with --sim: allowed DC drop on power nets, e.g. 2%,30mV (default: the larger of 2% of the rail and 30 mV; rails above 5 V use the percentage)")
		c.Flags().StringArrayVar(&in.groups, "groups", nil, "schematic module ownership (repeatable): a sch composition JSON (modules[].placements[].designator) or {\"groups\":[{id,core,members}]} — makes each module's parts follow its core; port protection stays at its connector")
		c.Flags().IntVar(&in.layers, "layers", 0, "force the copper layer count (0 = decide)")
		c.Flags().IntVar(&in.maxLayers, "max-layers", 6, "cost cap for the layer decision")
		c.Flags().Float64Var(&in.grid, "grid", 0, "routing grid in mil (0 = derived from the rules)")
		c.Flags().DurationVar(&in.timeout, "timeout", 4*time.Minute, "routing time budget")
		c.Flags().StringVar(&in.pinCaps, "pin-caps", "", "extra pin-capability table merged over the built-in one (schema: .agents/skills/pcbpilot/references/pin-capabilities.json) — adds STM32/AT32/other remappable parts for schematic pin-swap feedback")
	}
	loadCaps := func() (*pcbauto.PinCapTable, error) {
		t := pcbauto.DefaultPinCaps()
		if in.pinCaps == "" {
			return t, nil
		}
		raw, err := os.ReadFile(in.pinCaps)
		if err != nil {
			return nil, err
		}
		o, err := pcbauto.ParsePinCaps(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", in.pinCaps, err)
		}
		t.Merge(o)
		return t, nil
	}
	// feedback derives the schematic proposals of a routed board and logs
	// the headline to stderr.
	feedback := func(cmd *cobra.Command, b *pcbauto.Board, rep *pcbauto.Report, opts pcbauto.Options, verify, loop int, source string) (*pcbauto.Feedback, error) {
		caps, err := loadCaps()
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), in.timeout*time.Duration(verify+3*loop+1))
		defer cancel()
		fb, err := pcbauto.BuildFeedback(ctx, b, rep.Result, rep.Joint, pcbauto.FeedbackOptions{Caps: caps, Circuit: rep.Circuit, Route: opts,
			Verify: verify, Loop: loop, Source: source, Log: func(f string, a ...any) { fmt.Fprintf(stderr, f+"\n", a...) }})
		if err != nil {
			return nil, err
		}
		applyable := 0
		for _, it := range fb.Items {
			if it.Applyable {
				applyable++
			}
		}
		fmt.Fprintf(stderr, "feedback: %d schematic proposal(s) (%d applyable pin swaps), %d rejected by re-route; hard=%v\n", len(fb.Items), applyable, len(fb.Rejected), fb.Difficulty.Hard)
		if lp := fb.Loop; lp != nil {
			fmt.Fprintf(stderr, "feedback loop: %d tries, %d accepted swap(s); joint %.1f → %.1f, vias %.0f → %.0f\n", len(lp.Passes), len(lp.Accepted), lp.Before["joint"], lp.After["joint"], lp.Before["vias"], lp.After["vias"])
		}
		return fb, nil
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
	var boardRaw []byte // the snapshot the run started from (board.routed.json)
	load := func() (*pcbauto.Board, *pcbauto.MechSpec, pcbauto.PowerSpec, error) {
		var power pcbauto.PowerSpec
		var raw []byte
		var err error
		defer func() { boardRaw = raw }()
		if in.board != "" {
			raw, err = os.ReadFile(in.board)
		} else {
			var snap *boardSnapshot
			snap, err = fetchBoardSnapshot(cfg, *window, boardSnapshotOpts{withRules: true, withLayers: true, withCopper: true, withFootprintHoles: true})
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
		if in.sim != "" {
			sraw, err := os.ReadFile(in.sim)
			if err != nil {
				return nil, nil, power, err
			}
			if power.Sim, err = pcbauto.ParseSim(sraw); err != nil {
				return nil, nil, power, err
			}
		}
		if in.intent != "" {
			iraw, err := os.ReadFile(in.intent)
			if err != nil {
				return nil, nil, power, err
			}
			if power.Intent, err = pcbauto.ParseIntent(iraw); err != nil {
				return nil, nil, power, err
			}
		}
		if in.irBudget != "" {
			bud, err := pcbauto.ParseIRBudget(in.irBudget)
			if err != nil {
				return nil, nil, power, err
			}
			power.IRBudget = &bud
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
		var outDir, replaceJournal, reportDir, reportName string
		var place, noRoute, refine, macro bool
		var only []string
		var seed int64
		var loops int
		var noFeedback bool
		var fbVerify, fbLoop int
		c := &cobra.Command{
			Use:   "run",
			Short: "Full pipeline: analyse → stackup → (place) → route → DRC/SI → plan.json + playbook.json + preview.svg + report.md",
			Example: `  pcbpilot pcb auto run --board board.json --out-dir out/
  pcbpilot pcb auto run --board board.json --mech mech.json --place --out-dir out/
  pcbpilot pcb auto run --board board.json --intent intent.json --place --out-dir out/
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
				outlineBefore := append([]pcbauto.Point(nil), b.Outline...)
				var frame *pcbauto.FrameSearch
				if place && pcbauto.NeedsAutoFrame(mech) {
					// autoSize without a size: search the smallest frame the
					// placer fills cleanly, then run as a fixed-size board.
					fa := pcbauto.Analyze(b, power, nil)
					fc, err := understand(b, fa)
					if err != nil {
						return err
					}
					if mech, frame, err = pcbauto.AutoFrame(b, fa, fc, mech, pcbauto.PlaceOptions{Seed: seed, Macro: macro}); err != nil {
						return err
					}
					fmt.Fprintf(stderr, "autoSize: frame %.1f × %.1f %s (%d trials)\n", frame.Width, frame.Height, frame.Units, len(frame.Trials))
				}
				var mc *pcbauto.Mechanics
				if mech != nil {
					apply := pcbauto.ApplyMech
					if !place {
						// Route-only: the playbook moves no part, so the model
						// must keep the measured poses.
						apply = pcbauto.ApplyMechInPlace
					}
					if mc, err = apply(b, mech); err != nil {
						return err
					}
				}
				newHoles, newKeeps := b.Holes[holesBefore:], b.Keepouts[keepBefore:]
				outlineChanged := mech != nil
				if mech != nil && !place {
					// Route-only on a board that already carries the mechanics:
					// write only what is genuinely new.
					newHoles, newKeeps = pcbauto.DropExistingMech(b, holesBefore, keepBefore)
					outlineChanged = !pcbauto.SameOutline(outlineBefore, b.Outline, 0.5)
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
					// Every pass places (the annealer's own 90 s default) and then
					// routes: the placement time was missing from the budget, so a
					// slow high-voltage board hit "context deadline exceeded" in its
					// first pass and the command returned nothing.
					budget = (in.timeout*3 + 90*time.Second) * time.Duration(loops)
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
					lr, err := pcbauto.PlaceRoute(ctx, b, pre, rep.Circuit, mc, pcbauto.PlaceOptions{Seed: seed, Refine: refine, Macro: macro, Only: only}, opts, pcbauto.LoopOptions{Passes: loops, Budget: in.timeout * time.Duration(loops+1)})
					if err != nil {
						return err
					}
					rep.Placement, rep.Result, rep.Loop, looped = lr.Place, lr.Result, lr.Passes, true
					for _, lp := range lr.Passes {
						fmt.Fprintf(stderr, "loop pass %d: routed %.1f%%, DRC %d, joint %.1f, inflated %d %s\n", lp.Pass, lp.Completion, lp.DRC, lp.Joint, lp.Inflated, lp.Note)
					}
					fmt.Fprintf(stderr, "loop: best pass %d\n", lr.Best)
				} else if place {
					rep.Placement, err = pcbauto.Place(b, pre, rep.Circuit, mc, pcbauto.PlaceOptions{Seed: seed, Refine: refine, Macro: macro, Only: only})
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
					// Signal completion alone read "100%" while two +5V plane
					// connections were open (ESP32 E2E): report both.
					plane := ""
					if j := rep.Joint; j.PlanePads > 0 {
						plane = fmt.Sprintf(", plane connections %d/%d", j.PlanePads-j.PlaneOpen, j.PlanePads)
					}
					fmt.Fprintf(stderr, "routing: %d layers, signal %.1f%% (%d/%d)%s, vias %d+%d fan-out, DRC violations %d, %.1fs\n",
						rep.Result.Stackup.Layers, s.Completion, s.Routed, s.Connections, plane, s.Vias, s.FanoutVias,
						len(rep.Result.DRC.Violations), float64(s.Millis)/1000)
					if pw := rep.Result.Route.Power; pw != nil {
						var parts []string
						for _, n := range pw.Nets {
							if n.Role != pcbauto.RolePower {
								continue
							}
							parts = append(parts, fmt.Sprintf("%s %.1f/%.0f mV %s", n.Net, n.WorstMV, n.BudgetMV, n.Status))
						}
						fmt.Fprintf(stderr, "ir-drop (%s, budget %s): %s\n", pw.Scenario, pw.Budget, strings.Join(parts, ", "))
					}
					if iso := rep.Result.Isolation; iso != nil && len(iso.Pairs) > 0 {
						fmt.Fprintf(stderr, "isolation (%s): %d pair(s), %d slot(s), %d no-pour region(s), %d clearance/creepage finding(s)\n",
							iso.Standard, len(iso.Pairs), len(iso.Slots), len(iso.Moats), len(iso.Findings))
						for _, n := range iso.Notes {
							fmt.Fprintf(stderr, "isolation: %s\n", n)
						}
						if len(iso.Infeasible) > 0 {
							fmt.Fprintf(stderr, "isolation: INFEASIBLE — %d bridge part(s) cannot meet the pair with any slot/routing/placement (change the part); plan.json result.isolation.infeasible\n", len(iso.Infeasible))
						}
					}
					if !noFeedback || fbLoop > 0 {
						if rep.Feedback, err = feedback(cmd, b, rep, opts, fbVerify, fbLoop, "pcb auto run"); err != nil {
							return err
						}
					}
				}
				pb := pcbauto.BuildPlaybook(pcbauto.PlaybookInput{Board: b, Original: original, Result: rep.Result, Placement: rep.Placement,
					Circuit: rep.Circuit, OutlineChanged: outlineChanged, NewHoles: newHoles, NewKeepouts: newKeeps,
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
				files := map[string]func(io.Writer) error{
					"plan.json":     js(rep),
					"playbook.json": js(pb),
					"preview.svg":   func(w io.Writer) error { return pcbauto.RenderSVG(w, b, rep.Circuit, rep.Result.Stackup, rr) },
					"report.md":     func(w io.Writer) error { rep.WriteMarkdown(w); return nil },
				}
				names := "plan.json,playbook.json,preview.svg,report.md"
				if rep.Feedback != nil {
					files["feedback.json"] = js(rep.Feedback)
					names += ",feedback.json"
				}
				if rr != nil && len(boardRaw) > 0 {
					// The placed + routed board as a pcb dump: `pcb check --intent
					// --board board.routed.json` judges it offline.
					routed, err := pcbauto.ExportRoutedSnapshot(boardRaw, b, rep.Result)
					if err != nil {
						return err
					}
					files["board.routed.json"] = func(w io.Writer) error { _, err := w.Write(append(routed, '\n')); return err }
					names += ",board.routed.json"
				}
				for name, fn := range files {
					if err := write(name, fn); err != nil {
						return err
					}
				}
				fmt.Fprintf(stdout, "wrote %s/{%s} (%d playbook steps)\n", outDir, names, len(pb.Steps))
				if reportDir != "" {
					// P11: publish the next design-report version from what this
					// run has (intent, sim, this plan dir, the board dump).
					dir, dr, err := runDesignReport(designReportOpts{outDir: reportDir, version: "auto", project: reportName,
						intent: in.intent, sim: in.sim, planDir: outDir, board: in.board, maxImageBytes: designreport.DefaultMaxImageBytes}, stderr)
					if err != nil {
						return fmt.Errorf("design report: %w", err)
					}
					fmt.Fprintf(stdout, "wrote %s/{report.html,report.md,report.json} — %s %s\n", dir, dr.VersionLabel, dr.Verdict.Status)
				}
				return nil
			},
		}
		addInputs(c)
		c.Flags().StringVar(&outDir, "out-dir", "", "directory for plan.json, playbook.json, preview.svg, report.md")
		c.Flags().StringVar(&reportDir, "report-dir", "", "also publish the next design-report version here (reports/<name>/; see 'pcbpilot report design'): intent/sim/board/this plan; add DRC/check/images later with 'report design'")
		c.Flags().StringVar(&reportName, "report-name", "", "project name on the report cover (default: the report dir name)")
		c.Flags().StringVar(&replaceJournal, "replace", "", "apply journal of the previous pcbauto playbook: its captured holes/keep-outs (MECH_*) are deleted first, so a re-plan does not stack a second set")
		c.Flags().BoolVar(&place, "place", false, "run the placer (mechanics, domain zones, blocks) before routing")
		c.Flags().BoolVar(&refine, "refine", false, "with --place: refine the current placement instead of constructing one")
		c.Flags().BoolVar(&macro, "macro", false, "with --place: experimental two-stage placement — freeze each core with its critical auxiliaries as a rigid macro, anneal macros + the rest, then polish (8-board A/B 2026-09-25: worse than the default single-stage anneal on 7/8)")
		c.Flags().StringSliceVar(&only, "only", nil, "with --place --refine: move only these designators (local adjustment of a confirmed layout), e.g. --only C7,D3")
		c.Flags().BoolVar(&noRoute, "no-route", false, "stop after placement / stackup")
		c.Flags().Int64Var(&seed, "seed", 0, "placement random seed (runs are reproducible per seed)")
		c.Flags().BoolVar(&noFeedback, "no-feedback", false, "skip the schematic feedback (feedback.json / report section 回推原理图的建议)")
		c.Flags().IntVar(&fbVerify, "feedback-verify", 3, "re-route up to N pin-swap candidates on a board copy to verify their gain (0 = ratsnest estimate only; skipped when the base routing took > 60 s)")
		c.Flags().IntVar(&fbLoop, "feedback-loop", 0, "closed loop PCB→schematic: up to N passes that apply the best pin swap to an in-memory copy, re-route and keep it only when the joint score improves; the accepted swaps are the recommendation (plan/playbook stay those of the unmodified board)")
		c.Flags().IntVar(&loops, "loops", 3, "with --place: place↔route loop passes — parts near unrouted pads / DRC points are inflated and re-placed; 0 = one placement then route")
		group.AddCommand(c)
	}
	group.AddCommand(newPcbAutoBenchCmd(stdout, stderr))

	// ── feedback ─────────────────────────────────────────────────────────
	mkFeedback := func() *cobra.Command {
		var planPath, outPath string
		var verify, loop int
		c := &cobra.Command{
			Use:   "feedback",
			Short: "Recompute schematic feedback (pin swaps, decap ownership, IR drop, package) from an existing plan.json (offline)",
			Long: `Reads the plan.json of a 'pcb auto run' plus the board it was run on
(--board, the same dump) and recomputes feedback.json: evidence-backed
schematic changes that make routing easier — MCU GPIO / header pin swaps
(verified by re-routing a board copy), missing or shared decoupling caps,
IR-drop remedies (with --sim in the original run), package suggestions.
Nothing is written to EasyEDA; apply a pin swap with 'pcbpilot sch pin-swap'.`,
			Example: `  pcbpilot pcb feedback --plan out/plan.json --board board.json --power power.json --out out/feedback.json
  pcbpilot pcb feedback --plan out/plan.json --board board.json --verify 5 --loop 3`,
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if planPath == "" || in.board == "" {
					return fmt.Errorf("--plan and --board are required (the plan does not carry the pads)")
				}
				b, mech, power, err := load()
				if err != nil {
					return err
				}
				raw, err := os.ReadFile(planPath)
				if err != nil {
					return err
				}
				var rep pcbauto.Report
				if err := json.Unmarshal(raw, &rep); err != nil {
					return fmt.Errorf("%s: %w", planPath, err)
				}
				if rep.Result == nil || rep.Result.Route == nil || rep.Result.Stackup == nil {
					return fmt.Errorf("%s has no routing result (was it run with --no-route?)", planPath)
				}
				if pl := rep.Placement; pl != nil {
					for _, p := range pl.Placements {
						if part := b.Part(p.Ref); part != nil {
							part.MoveTo(pcbauto.Point{X: p.X, Y: p.Y}, p.Rot)
						}
					}
					if len(pl.Outline) >= 3 {
						b.Outline = pl.Outline
					}
				}
				if mech != nil {
					if _, err := pcbauto.ApplyMechInPlace(b, mech); err != nil {
						return err
					}
				}
				rep.Result.Analysis = pcbauto.Analyze(b, power, rep.Result.Stackup)
				if rep.Circuit == nil {
					if rep.Circuit, err = understand(b, rep.Result.Analysis); err != nil {
						return err
					}
				}
				if rep.Joint == nil {
					rep.Joint = pcbauto.Joint(b, rep.Result.Analysis, rep.Circuit, rep.Result.Stackup, rep.Result.Route, rep.Result.DRC, pcbauto.JointOptions{PlacementScore: -1})
				}
				opts := pcbauto.Options{Power: power, Stack: pcbauto.StackOptions{Force: rep.Result.Stackup.Layers, MaxLayers: in.maxLayers},
					Route: pcbauto.RouteOptions{GridMil: in.grid, Timeout: in.timeout}}
				fb, err := feedback(cmd, b, &rep, opts, verify, loop, "pcb feedback")
				if err != nil {
					return err
				}
				var w io.Writer = stdout
				if outPath != "" {
					f, err := os.Create(outPath)
					if err != nil {
						return err
					}
					defer f.Close()
					w = f
				}
				enc := json.NewEncoder(w)
				enc.SetIndent("", "  ")
				if err := enc.Encode(fb); err != nil {
					return err
				}
				if outPath != "" {
					fmt.Fprintf(stdout, "wrote %s (%d items)\n", outPath, len(fb.Items))
				}
				return nil
			},
		}
		addInputs(c)
		c.Flags().StringVar(&planPath, "plan", "", "plan.json of a previous 'pcb auto run' (required)")
		c.Flags().StringVar(&outPath, "out", "", "write feedback.json here (default stdout)")
		c.Flags().IntVar(&verify, "verify", 3, "re-route up to N pin-swap candidates to verify their gain (0 = ratsnest estimate only)")
		c.Flags().IntVar(&loop, "loop", 0, "accept-if-better loop passes (see pcb auto run --feedback-loop)")
		return c
	}
	group.AddCommand(mkFeedback())
	for _, p := range aliasParents {
		p.AddCommand(mkFeedback()) // 'pcb feedback' = 'pcb auto feedback'
	}
	return group
}
