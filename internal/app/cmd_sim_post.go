package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/postsim"
)

// postSimOpts are the inputs of one `sim post-layout` run.
type postSimOpts struct {
	board, sim, intent, models, plan      string
	out, report, svgDir, feedback         string
	elmerDir, elmerResult                 string
	elmerCheck                            bool
	elmerTimeout                          time.Duration
	planes, scenarios                     []string
	irBudget                              string
	cell, ambient, hTop, hBottom, emis    float64
	kxy, kz, plating, viaDT, tempRise     float64
	margin, jMax, outerOz, innerOz, thick float64
	source                                string
}

func newSimPostLayoutCmd(stdout, stderr io.Writer) *cobra.Command {
	var o postSimOpts
	c := &cobra.Command{
		Use:     "post-layout",
		Aliases: []string{"post"},
		Short:   "Post-layout (设计后) verification on the real copper: IR drop, via currents, current density, thermal maps, part temperatures",
		Args:    cobra.NoArgs,
		Long: `Verify the FINISHED board: the copper read back from EasyEDA
(pcb dump --include-copper) — hand-routed or pcb auto — against the pin
currents and part dissipation of sim power. Offline; never touches the editor.

COPPER. Per copper layer the board is rasterised (--cell, default 0.5 mm,
coarsened only for huge boards; 5×5 sub-samples per cell): tracks and arcs,
poured fills (complex polygons with ARCs, thermal-relief spokes), static
fills, pads by shape, via rings. Board cutouts (MULTI fills, footprint NPTH)
are removed. An inner layer with no copper objects is a NEGATIVE PLANE the
dump cannot list: it is modelled as a solid plane of the ground net (outline
inset by copper-to-edge, antipads = other nets' copper + clearance) and
reported as an assumption — --plane 15=GND declares it, --plane 15=none
disables it. Stackup: layer count from the dump, copper weights and
thickness / prepreg from --intent (copper.outerOz/innerOz/stackup) or flags.

DC (per power / ground / switch net, per scenario — each KCL-consistent
scenario on its own, worst = maximum): tracks = 1-D resistors ρL/(w·t) split
at junctions/vias/pads and per cell; area copper = sheet cells; pads shorted
to the copper they overlap; vias = barrel R = ρh/(π(d+t)t) per layer span.
Reference = the supplying part (ground: the return entry); every other pad
draws/feeds its simulated current. Sparse CG (IC(0)). Outputs: voltage and
drop at every load pad, budget check (--ir-budget, default max(2 %, 30 mV)),
current density per track piece / sheet cell (A/mm²) and hot spots,
per-segment I²R, via currents vs IPC ampacity.

THERMAL (steady state, 2.5-D finite volume): per layer k_Cu·t·coverage +
FR-4 in-plane (--k-fr4-xy), through-plane FR-4 (--k-fr4-z) + via barrels,
natural convection on both faces (--h-top/--h-bottom, 8–12 W/m²K; optional
linearised radiation --emissivity). BOARD ONLY: no enclosure, fan or
airflow. Heat: sim parts[].powerW on each part's pads (by area) + the Joule
heat of the DC solve. Every scenario is solved; maps are the hottest.
Outputs: per-layer temperature and current-density SVG heat maps (outline,
parts, designators, legend), max board temperature, board temperature under
every part, Tj = Tboard + P·θJB (θJC) when power-models ratings carry
thetaJbCW/thetaJcCW (else "needs datasheet"), energy balance, copper
self-heating (Joule-only solve).

FEEDBACK. Tracks over IPC-2152 capacity × --margin at the intent tempRiseC
(or self-heating over it, or over --j-max), corners ≤ 95° whose crowded
current (×(1 + (180°−angle)/180°)) exceeds capacity, pour necks, vias over
50 % ampacity → post.json feedback[] with the exact segment, recommended
width / via count; --feedback merges them into a feedback.json
(pcb auto schema, kinds widen-segment / corner-crowding / via-bottleneck).

ELMER. --elmer-dir writes the same thermal model as an Elmer FEM deck
(mesh/ + case.sif, HeatSolver, Robin BCs, SaveScalars probes);
--elmer-check runs ElmerSolver when on PATH and compares the probes (board
max, hottest cell under each dissipating part) — otherwise "skipped";
--elmer-result reads a probes.dat from a later run.

VERDICT pass | warn | fail: fail = drop over budget, open load, via over
ampacity, Tj over Tj,max, board over 130 °C; warn = drop > 80 % budget, via
> 80 %, Tj > 80 % Tj,max, board > 105 °C (FR-4 Tg margin).`,
		Example: `  # live board: dump → verify → report
  pcbpilot pcb dump --include-copper --project ceshi --out board.json
  pcbpilot sim post-layout --board board.json --sim sim.json --intent intent.json \
      --out post.json --report post.md --svg-dir heatmaps/
  pcbpilot report design ... --post post.json --image heat:TOP=heatmaps/temp-TOP.svg

  # compare with pcb auto's routing-time IR estimate, write layout feedback
  pcbpilot sim post-layout --board board.json --sim sim.json --plan out/plan.json \
      --feedback out/feedback.json --out post.json

  # Elmer FEM cross-check (runs only when ElmerSolver is installed)
  pcbpilot sim post-layout --board board.json --sim sim.json --elmer-dir elmer/ --elmer-check`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.board == "" || o.sim == "" {
				return fmt.Errorf("--board and --sim are required")
			}
			res, err := runPostSim(o, stderr)
			if err != nil {
				return err
			}
			if o.out == "" && o.report == "" {
				blob, _ := json.MarshalIndent(res, "", "  ")
				_, err := stdout.Write(append(blob, '\n'))
				return err
			}
			worst := ""
			for _, n := range res.Nets {
				if n.Role == "power" && (worst == "" || n.WorstMV > 0) {
					worst += fmt.Sprintf(" %s %.2fmV", n.Net, n.WorstMV)
				}
			}
			maxC := 0.0
			if res.Thermal != nil {
				maxC = res.Thermal.MaxBoardC
			}
			fmt.Fprintf(stdout, "post-layout %s — board max %.1f °C;%s; %d feedback item(s)\n", strings.ToUpper(res.Verdict.Status), maxC, worst, len(res.Feedback))
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&o.board, "board", "", "board dump JSON (pcb dump --include-copper, or pcb auto's board.routed.json)")
	f.StringVar(&o.sim, "sim", "", "sim.json (pcbpilot sim power)")
	f.StringVar(&o.intent, "intent", "", "intent.json: copper weights, stackup, tempRiseC")
	f.StringVar(&o.models, "models-lib", "", "power-models.json with ratings (θJB/θJC/Tj,max; default: the installed skill's)")
	f.StringVar(&o.plan, "plan", "", "pcb auto plan.json: set its routing-time IR drop next to the post-layout drop")
	f.StringVar(&o.out, "out", "", "write post.json here")
	f.StringVar(&o.report, "report", "", "write the Markdown report here")
	f.StringVar(&o.svgDir, "svg-dir", "", "write per-layer temperature / current-density SVG heat maps here")
	f.StringVar(&o.feedback, "feedback", "", "merge the copper feedback into this feedback.json (created when missing)")
	f.StringVar(&o.elmerDir, "elmer-dir", "", "export the thermal model as an Elmer FEM deck here")
	f.BoolVar(&o.elmerCheck, "elmer-check", false, "run ElmerSolver on the deck when installed and compare (implies --elmer-dir, default <out dir>/elmer)")
	f.StringVar(&o.elmerResult, "elmer-result", "", "compare with the probes.dat of an earlier ElmerSolver run of the deck")
	f.DurationVar(&o.elmerTimeout, "elmer-timeout", 5*time.Minute, "ElmerSolver time limit")
	f.StringArrayVar(&o.planes, "plane", nil, "negative plane layer=NET (repeatable), e.g. 15=GND, IN1=GND, 16=none")
	f.StringSliceVar(&o.scenarios, "scenario", nil, "only these sim scenarios (default: every scenario except the merged worst)")
	f.StringVar(&o.irBudget, "ir-budget", "", "allowed DC drop on power nets, e.g. 2%,30mV (default max(2 %, 30 mV))")
	f.Float64Var(&o.cell, "cell", 0.5, "raster cell size (mm)")
	f.Float64Var(&o.ambient, "ambient", 25, "ambient temperature (°C)")
	f.Float64Var(&o.hTop, "h-top", 10, "top-face natural convection coefficient (W/m²K)")
	f.Float64Var(&o.hBottom, "h-bottom", 10, "bottom-face natural convection coefficient (W/m²K)")
	f.Float64Var(&o.emis, "emissivity", 0, "surface emissivity for linearised radiation (0 = off; solder mask ≈ 0.9)")
	f.Float64Var(&o.kxy, "k-fr4-xy", 0.3, "FR-4 in-plane conductivity (W/mK)")
	f.Float64Var(&o.kz, "k-fr4-z", 0.3, "FR-4 through-plane conductivity (W/mK)")
	f.Float64Var(&o.plating, "plating", 0.7, "via barrel plating (mil; JLC 18 µm ≈ 0.7)")
	f.Float64Var(&o.viaDT, "via-dt", 10, "temperature rise for the via ampacity (°C)")
	f.Float64Var(&o.tempRise, "temp-rise", 0, "allowed copper temperature rise for width feedback (°C; default intent copper.tempRiseC or 10)")
	f.Float64Var(&o.margin, "margin", 1.2, "current margin on IPC width / via count")
	f.Float64Var(&o.jMax, "j-max", 0, "absolute current-density limit (A/mm²; 0 = IPC-derived per width)")
	f.Float64Var(&o.outerOz, "outer-oz", 0, "outer copper weight (oz; default intent or 1)")
	f.Float64Var(&o.innerOz, "inner-oz", 0, "inner copper weight (oz; default intent or 0.5)")
	f.Float64Var(&o.thick, "board-thickness", 0, "board thickness (mm; default intent stackup or 1.6)")
	return c
}

// parsePlaneFlags reads LAYER=NET (layer id, IN<n>, TOP/BOTTOM).
func parsePlaneFlags(vals []string) (map[int]string, error) {
	if len(vals) == 0 {
		return nil, nil
	}
	out := map[int]string{}
	for _, v := range vals {
		l, n, ok := strings.Cut(v, "=")
		if !ok {
			return nil, fmt.Errorf("--plane %q: want LAYER=NET", v)
		}
		l = strings.ToUpper(strings.TrimSpace(l))
		id := 0
		switch {
		case l == "TOP":
			id = postsim.LayerTop
		case l == "BOTTOM":
			id = postsim.LayerBottom
		case strings.HasPrefix(l, "IN"):
			k, err := strconv.Atoi(strings.TrimPrefix(l, "IN"))
			if err != nil || k < 1 {
				return nil, fmt.Errorf("--plane %q: bad layer", v)
			}
			id = postsim.LayerInner1 + k - 1
		default:
			k, err := strconv.Atoi(strings.TrimPrefix(l, "L"))
			if err != nil {
				return nil, fmt.Errorf("--plane %q: bad layer", v)
			}
			id = k
		}
		out[id] = strings.TrimSpace(n)
	}
	return out, nil
}

func fileSHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// runPostSim executes one post-layout verification and writes its outputs.
func runPostSim(o postSimOpts, stderr io.Writer) (*postsim.Result, error) {
	boardRaw, err := os.ReadFile(o.board)
	if err != nil {
		return nil, fmt.Errorf("--board: %w", err)
	}
	simRaw, err := os.ReadFile(o.sim)
	if err != nil {
		return nil, fmt.Errorf("--sim: %w", err)
	}
	opt := postsim.DefaultOptions()
	opt.CellMm, opt.AmbientC, opt.HTop, opt.HBottom, opt.Emissivity = o.cell, o.ambient, o.hTop, o.hBottom, o.emis
	opt.KFR4XY, opt.KFR4Z, opt.PlatingMil, opt.ViaDeltaTC = o.kxy, o.kz, o.plating, o.viaDT
	if o.margin > 0 {
		opt.Margin = o.margin
	}
	opt.JMax, opt.Scenarios, opt.Source = o.jMax, o.scenarios, o.source
	opt.Stack = postsim.StackOptions{OuterOz: o.outerOz, InnerOz: o.innerOz, ThicknessMm: o.thick}
	if o.irBudget != "" {
		b, err := pcbauto.ParseIRBudget(o.irBudget)
		if err != nil {
			return nil, err
		}
		opt.Budget = b
	}
	if opt.Planes, err = parsePlaneFlags(o.planes); err != nil {
		return nil, err
	}
	if o.intent != "" {
		raw, err := os.ReadFile(o.intent)
		if err != nil {
			return nil, fmt.Errorf("--intent: %w", err)
		}
		if err := postsim.ApplyIntent(raw, &opt); err != nil {
			return nil, err
		}
	}
	if o.tempRise > 0 {
		opt.TempRiseC = o.tempRise
	}
	modelsPath := ""
	if p, err := powerModelsAsset.resolve(o.models); err == nil {
		if raw, err := os.ReadFile(p); err == nil {
			if opt.Ratings, err = postsim.ParseRatings(raw); err != nil {
				return nil, err
			}
			modelsPath = p
		}
	} else if o.models != "" {
		return nil, err
	}
	res, err := postsim.Run(boardRaw, simRaw, opt)
	if err != nil {
		return nil, err
	}
	res.Inputs.Board, res.Inputs.BoardSHA256 = o.board, fileSHA(boardRaw)
	res.Inputs.Sim, res.Inputs.SimSHA256 = o.sim, fileSHA(simRaw)
	res.Inputs.Intent, res.Inputs.Models = o.intent, modelsPath
	if o.plan != "" {
		raw, err := os.ReadFile(o.plan)
		if err != nil {
			return nil, fmt.Errorf("--plan: %w", err)
		}
		if err := res.CompareWithPlan(raw); err != nil {
			fmt.Fprintf(stderr, "⚠️  %v\n", err)
		}
	}
	svgDir := o.svgDir
	if svgDir != "" {
		if err := res.WriteMaps(svgDir); err != nil {
			return nil, err
		}
	}
	elmerDir := o.elmerDir
	if elmerDir == "" && (o.elmerCheck || o.elmerResult != "") {
		base := "."
		if o.out != "" {
			base = filepath.Dir(o.out)
		}
		elmerDir = filepath.Join(base, "elmer")
	}
	if elmerDir != "" {
		if err := res.ExportElmer(elmerDir); err != nil {
			fmt.Fprintf(stderr, "⚠️  elmer export: %v\n", err)
		} else {
			switch {
			case o.elmerResult != "":
				raw, err := os.ReadFile(o.elmerResult)
				if err != nil {
					return nil, fmt.Errorf("--elmer-result: %w", err)
				}
				res.CompareElmer(raw)
			case o.elmerCheck:
				res.RunElmer(o.elmerTimeout)
			}
		}
	}
	if o.feedback != "" {
		var prev []byte
		if b, err := os.ReadFile(o.feedback); err == nil {
			prev = b
		}
		fb, err := postsim.MergeFeedback(prev, res.Feedback)
		if err != nil {
			return nil, err
		}
		blob, _ := json.MarshalIndent(fb, "", "  ")
		if err := os.WriteFile(o.feedback, append(blob, '\n'), 0o644); err != nil {
			return nil, err
		}
	}
	if o.out != "" {
		blob, _ := json.MarshalIndent(res, "", "  ")
		if err := os.WriteFile(o.out, append(blob, '\n'), 0o644); err != nil {
			return nil, err
		}
	}
	if o.report != "" {
		f, err := os.Create(o.report)
		if err != nil {
			return nil, err
		}
		rel := ""
		if svgDir != "" {
			if r, err := filepath.Rel(filepath.Dir(o.report), svgDir); err == nil {
				rel = filepath.ToSlash(r)
			}
		}
		res.WriteMarkdown(f, rel)
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	return res, nil
}
