package app

// cmd_kicad.go — `pcbpilot kicad`: the KiCad backend (KiCad 10, decided
// 2026-10-09). Boards are .kicad_pcb files read and written through KiCad's
// own Python (internal/kicad/bridge.py) and checked with kicad-cli; routing
// stays the external fastroute process (GPLv3, never linked or bundled).
//
// The route command is the KiCad twin of `pcb auto route` + runQualityGates:
// every EDA-neutral calculation (intent requirements, via arrays, widen to
// intent, pours, silk placement, post-layout sim, the snapshot gates, IR
// closure, board manual, design report, design review) is the shared code;
// only reading and writing the board goes through the bridge. KiCad's
// native DSN/SES round trip needs none of the EasyEDA repairs (dsn-fix,
// ses-repair, reconcile).

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

func newKicadCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	c := &cobra.Command{
		Use:   "kicad",
		Short: "Design in KiCad: board setup (outline/stackup/holes/zones), placement, schematic sheets / import / netlist, snapshot, route + gates (fastroute), DRC gate, JLC fab output + preflight, LCSC parts",
		Long: `Work on KiCad 10 boards (.kicad_pcb + .kicad_pro) and schematics without the EasyEDA
connector (user decision 2026-10-09; EasyEDA only receives the finished project
via project import). KiCad is found at $PCBPILOT_KICAD_APP (default
/Applications/KiCad/KiCad.app on macOS); $PCBPILOT_KICAD_PYTHON / $PCBPILOT_KICAD_CLI
override the two tools. Input boards are never modified by route: every write
goes to --out-dir.`,
	}
	c.AddCommand(
		newKicadPlaceCmd(stdout, stderr),
		newKicadBoardCmd(stdout),
		newKicadSchFitCmd(stdout, stderr),
		newKiCadFabCmd(stdout, stderr),
		newKicadDRCCmd(stdout, stderr),
		newKiCadLcscCmd(stdout, stderr),
		newKicadSchImportCmd(stdout, stderr),
		newKicadSchNetlistCmd(stdout, stderr),
		newKicadSchBuildCmd(stdout, stderr),
		newKicadSchEditCmd(stdout, stderr),
		newKicadSchCheckpointCmd(stdout),
		newKicadSchReadCmd(stdout),
		newKicadSnapshotCmd(stdout),
		newKicadRouteCmd(stdout, stderr),
		newKicadSchCheckCmd(stdout, stderr),
		newKicadRenderCmd(stdout),
	)
	return c
}

func newKicadSnapshotCmd(stdout io.Writer) *cobra.Command {
	var out string
	c := &cobra.Command{
		Use:   "snapshot <board.kicad_pcb>",
		Short: "Board snapshot JSON (mil, y-up; the pcb dump --include-copper shape) of a .kicad_pcb",
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

func removeKicadBoard(pcb string) {
	b := strings.TrimSuffix(pcb, ".kicad_pcb")
	for _, ext := range append(kicadSiblings, ".kicad_prl") {
		_ = os.Remove(b + ext)
	}
}

// intentBoardRules reads the board-wide rules of an intent file (copper
// block) and the board edge distance: max(outer, inner) of the intent's
// edge policy (pcbauto.EdgeFromIntent — the defaults 20 / 30 mil when the
// intent has no edge block), because KiCad has one copper-to-edge rule for
// every layer.
func intentBoardRules(path string) kicad.Rules {
	var raw struct {
		Copper struct {
			MinTrackMil  float64 `json:"minTrackMil"`
			ClearanceMil float64 `json:"clearanceMil"`
			ViaDrillMil  float64 `json:"viaDrillMil"`
			ViaDiaMil    float64 `json:"viaDiaMil"`
		} `json:"copper"`
	}
	data, _ := os.ReadFile(path)
	_ = json.Unmarshal(data, &raw)
	r := kicad.Rules{ClearanceMil: raw.Copper.ClearanceMil, MinTrackMil: raw.Copper.MinTrackMil,
		ViaDiaMil: raw.Copper.ViaDiaMil, ViaDrillMil: raw.Copper.ViaDrillMil}
	if pi, err := loadEdgeIntent(path); err == nil {
		p := pcbauto.EdgeFromIntent(pi, nil)
		r.EdgeMil = math.Max(p.LayerReq(pcbauto.LayerTop), p.LayerReq(pcbauto.LayerInner1))
	}
	return r
}

// kicadEdges are the insulated domains' edge distances as bridge edge rules.
func kicadEdges(path string, in *designIntent) []map[string]any {
	pi, err := loadEdgeIntent(path)
	if err != nil {
		return nil
	}
	p := pcbauto.EdgeFromIntent(pi, nil)
	var out []map[string]any
	for _, d := range in.Domains {
		if ed := p.ByDomain[d.ID]; ed != nil && ed.Mil > 0 {
			out = append(out, map[string]any{"domain": d.ID, "nets": d.Nets, "mil": ed.Mil})
		}
	}
	return out
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

// kicadRuleFloors are a project's own design-rule minimums and its Default
// netclass via, in integer nm (0 = none), from the .kicad_pro
// (board.design_settings.rules and net_settings.classes, where KiCad 6+
// keeps them).
type kicadRuleFloors struct {
	ViaDia     int64 `json:"minViaDiameterNm"`
	ViaDrill   int64 `json:"minThroughHoleNm"`
	ViaAnnular int64 `json:"minViaAnnularNm"`
	Track      int64 `json:"minTrackNm"`
	// DefaultViaDia / DefaultViaDrill: the Default netclass via, the
	// partner of a single --via-dia-mil or --via-drill-mil.
	DefaultViaDia   int64 `json:"defaultViaDiameterNm"`
	DefaultViaDrill int64 `json:"defaultViaDrillNm"`
}

func nmOfMm(mm float64) int64   { return int64(math.Round(mm * 1e6)) }
func nmOfMil(mil float64) int64 { return int64(math.Round(mil * 25400)) }
func mmOfNm(v int64) string     { return strconv.FormatFloat(float64(v)/1e6, 'f', -1, 64) + " mm" }

// readKicadRuleFloors reads the minimums and the Default via from a .kicad_pro.
func readKicadRuleFloors(pro string) (kicadRuleFloors, error) {
	data, err := os.ReadFile(pro)
	if err != nil {
		return kicadRuleFloors{}, err
	}
	var raw struct {
		Board struct {
			DesignSettings struct {
				Rules struct {
					MinViaDiameter         float64 `json:"min_via_diameter"`
					MinThroughHoleDiameter float64 `json:"min_through_hole_diameter"`
					MinViaAnnularWidth     float64 `json:"min_via_annular_width"`
					MinTrackWidth          float64 `json:"min_track_width"`
				} `json:"rules"`
			} `json:"design_settings"`
		} `json:"board"`
		NetSettings struct {
			Classes []struct {
				Name        string  `json:"name"`
				ViaDiameter float64 `json:"via_diameter"`
				ViaDrill    float64 `json:"via_drill"`
			} `json:"classes"`
		} `json:"net_settings"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return kicadRuleFloors{}, fmt.Errorf("%s: %w", pro, err)
	}
	r := raw.Board.DesignSettings.Rules
	f := kicadRuleFloors{ViaDia: nmOfMm(r.MinViaDiameter), ViaDrill: nmOfMm(r.MinThroughHoleDiameter),
		ViaAnnular: nmOfMm(r.MinViaAnnularWidth), Track: nmOfMm(r.MinTrackWidth)}
	for _, c := range raw.NetSettings.Classes {
		if c.Name == "Default" {
			f.DefaultViaDia, f.DefaultViaDrill = nmOfMm(c.ViaDiameter), nmOfMm(c.ViaDrill)
		}
	}
	return f, nil
}

// checkRuleFloors refuses explicit Default-netclass values below the
// board's own minimums: KiCad's DRC would reject every via / track the
// router makes with them (interf_u: 0.3 mm drill vs min hole 0.5 mm gave
// 16 drill_out_of_range + 16 via_diameter errors). Each via flag is checked
// with its partner as it will be written: the other flag, else the Default
// netclass value, so a single flag cannot make a pair whose annular ring is
// below the minimum. --min-track-mil replaces the board's minimum track
// width, so --track-mil is checked against it when given. Integer nm.
func checkRuleFloors(r kicad.Rules, f kicadRuleFloors) error {
	var bad []string
	dia, drill := f.DefaultViaDia, f.DefaultViaDrill
	if r.ViaDiaMil > 0 {
		dia = nmOfMil(r.ViaDiaMil)
		if dia < f.ViaDia {
			bad = append(bad, fmt.Sprintf("--via-dia-mil %g (%s) is below the board's min via diameter %s", r.ViaDiaMil, mmOfNm(dia), mmOfNm(f.ViaDia)))
		}
	}
	if r.ViaDrillMil > 0 {
		drill = nmOfMil(r.ViaDrillMil)
		if drill < f.ViaDrill {
			bad = append(bad, fmt.Sprintf("--via-drill-mil %g (%s) is below the board's min through-hole %s", r.ViaDrillMil, mmOfNm(drill), mmOfNm(f.ViaDrill)))
		}
	}
	if (r.ViaDiaMil > 0 || r.ViaDrillMil > 0) && dia > 0 && drill > 0 && dia < drill+2*f.ViaAnnular {
		src := map[bool]string{true: "flag", false: "Default netclass"}
		bad = append(bad, fmt.Sprintf("via %s (%s) / drill %s (%s) leaves an annular ring of %s, below the board's min %s",
			mmOfNm(dia), src[r.ViaDiaMil > 0], mmOfNm(drill), src[r.ViaDrillMil > 0], mmOfNm((dia-drill)/2), mmOfNm(f.ViaAnnular)))
	}
	floor := f.Track
	if r.MinTrackMil > 0 {
		floor = nmOfMil(r.MinTrackMil)
	}
	if r.TrackMil > 0 && nmOfMil(r.TrackMil) < floor {
		bad = append(bad, fmt.Sprintf("--track-mil %g (%s) is below the board's min track width %s", r.TrackMil, mmOfNm(nmOfMil(r.TrackMil)), mmOfNm(floor)))
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("RULE_BELOW_BOARD_MINIMUM: %s (fix: use values at or above the board's design rules in its .kicad_pro, or omit the flag to use the board's own netclass sizes)", strings.Join(bad, "; "))
}

// kicadIsolation turns the intent's insulation pairs into the bridge's
// domain classes and clearance / creepage rules.
func kicadIsolation(in *designIntent) []map[string]any {
	nets := map[string][]string{}
	for _, d := range in.Domains {
		nets[d.ID] = d.Nets
	}
	var out []map[string]any
	for _, p := range in.Pairs {
		if p.ClearanceMm <= 0 && p.CreepageMm <= 0 {
			continue
		}
		out = append(out, map[string]any{"a": p.A, "b": p.B, "aNets": nets[p.A], "bNets": nets[p.B],
			"clearanceMm": p.ClearanceMm, "creepageMm": p.CreepageMm})
	}
	return out
}

// kicadRequirements: intentRequirements, with every net of an insulation
// pair raised to the pair clearance (fastroute knows one clearance per
// class; KiCad's DRC checks the pair rule itself).
func kicadRequirements(in *designIntent) map[string]kicad.NetRequirement {
	reqs := map[string]kicad.NetRequirement{}
	// The intent's via per net: its netClasses entry (largest when a net is
	// in several). The bridge raises it to the net's class and the board
	// minimums; it never lowers either.
	viaDia, viaDrill := map[string]float64{}, map[string]float64{}
	for _, c := range in.NetClasses {
		for _, n := range c.Nets {
			viaDia[n], viaDrill[n] = math.Max(viaDia[n], c.ViaDiaMil), math.Max(viaDrill[n], c.ViaDrillMil)
		}
	}
	for net, r := range intentRequirements(in) {
		reqs[net] = kicad.NetRequirement{OuterMil: r.OuterMil, InnerMil: r.InnerMil, MinMil: r.MinMil, ClearanceMil: r.ClearanceMil,
			ViaDiaMil: viaDia[net], ViaDrillMil: viaDrill[net]}
	}
	nets := map[string][]string{}
	for _, d := range in.Domains {
		nets[d.ID] = d.Nets
	}
	for _, p := range in.Pairs {
		clr := p.ClearanceMm / 0.0254
		if clr <= 0 {
			continue
		}
		for _, n := range append(append([]string{}, nets[p.A]...), nets[p.B]...) {
			if r, ok := reqs[n]; ok && r.ClearanceMil < clr {
				r.ClearanceMil = clr
				reqs[n] = r
			}
		}
	}
	return reqs
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

// kicadDRCCannotCheck is the kicad-drc gate when kicad-cli produced no
// readable report: failed, with no items (so no waiver can pass it).
func kicadDRCCannotCheck(err error) gateResult {
	return gateResult{Gate: "kicad-drc", Detail: "cannot check: " + err.Error()}
}

// kicadDRCGate: 0 DRC errors, 0 unconnected items, every one listed.
func kicadDRCGate(rep *kicad.DRCReport, path string) gateResult {
	g := gateResult{Gate: "kicad-drc", Pass: rep.Total == 0,
		Detail: fmt.Sprintf("%d error(s) from kicad-cli pcb drc (%s)", rep.Total, path)}
	g.Items = append(g.Items, rep.SortedCounts()...)
	for _, v := range rep.Violations {
		g.Items = append(g.Items, drcItem(kicadFlat(v)))
	}
	return g
}

// drcSilenced is one error-level check the project silences without
// failing the gate (summary.drcPolicy.silenced).
type drcSilenced = kicad.DRCSilenced

// applyDRCPolicy adds the project's DRC policy (kicad.EvalDRCPolicy: its
// .kicad_pro drc_exclusions and rule_severities) to the kicad-drc gate. A
// silenced hard rule is a failing item that only a
// {"gate":"kicad-drc","match":...} waiver can accept; a soft rule is
// returned as silenced and named in the detail and info; a warning-level
// rule is info. excluded are the report's excluded items (kicad-cli
// --severity-all), for their positions.
func applyDRCPolicy(g *gateResult, pol *kicad.DRCPolicy, excluded []kicad.Violation) []drcSilenced {
	res := kicad.EvalDRCPolicy(pol, excluded)
	if res == nil {
		return nil
	}
	g.Items = append(g.Items, res.Hard...)
	g.Info = append(g.Info, res.Info...)
	if d := res.HardDetail(); d != "" {
		g.Pass = false
		g.Detail += "; " + d
	}
	if d := res.SilencedDetail(); d != "" {
		g.Detail += "; " + d
	}
	return res.Silenced
}

func kicadFlat(v kicad.Violation) drcFlatViolation {
	return drcFlatViolation{Rule: v.Rule, ObjType: v.ObjType, Net: v.Net, X: v.X, Y: v.Y, Layer: v.Layer, Objs: v.Objs, Message: v.Message}
}

// kicadDRCFlat is the report in the `pcb drc --json` shape (report design --drc).
func kicadDRCFlat(rep *kicad.DRCReport) drcFlatReport {
	out := drcFlatReport{Passed: rep.Total == 0, Total: rep.Total, Counts: rep.Counts}
	for _, v := range rep.Violations {
		out.Violations = append(out.Violations, kicadFlat(v))
	}
	return out
}

// kicadIntentRulesGate checks that every intent net resolves to the
// pcbpilot netclass written for it (the KiCad counterpart of intent-rules:
// the board's rules still carry the intent).
func kicadIntentRulesGate(snapRaw []byte, classes []kicad.NetClass) gateResult {
	var s struct {
		NetClassOf map[string]string `json:"netClassOf"`
		NetClasses []struct {
			Name      string   `json:"name"`
			TrackMil  *float64 `json:"trackWidthMil"`
			Clearance *float64 `json:"clearanceMil"`
		} `json:"netclasses"`
	}
	g := gateResult{Gate: "intent-rules"}
	if err := json.Unmarshal(snapRaw, &s); err != nil {
		g.Detail = err.Error()
		return g
	}
	have := map[string][2]float64{}
	for _, c := range s.NetClasses {
		var w, cl float64
		if c.TrackMil != nil {
			w = *c.TrackMil
		}
		if c.Clearance != nil {
			cl = *c.Clearance
		}
		have[c.Name] = [2]float64{w, cl}
	}
	n := 0
	for _, c := range classes {
		hv, ok := have[c.Name]
		if !ok {
			g.Items = append(g.Items, fmt.Sprintf("netclass %s missing from the routed board's project", c.Name))
			continue
		}
		if hv[0]+reqEpsMil < c.TrackWidthMil || hv[1]+reqEpsMil < c.ClearanceMil {
			g.Items = append(g.Items, fmt.Sprintf("netclass %s: width %.2f / clearance %.2f mil < %.2f / %.2f", c.Name, hv[0], hv[1], c.TrackWidthMil, c.ClearanceMil))
		}
		for _, net := range c.Nets {
			n++
			if eff := s.NetClassOf[net]; strings.Split(eff, ",")[0] != c.Name {
				g.Items = append(g.Items, fmt.Sprintf("%s: effective netclass %q, want %s first", net, eff, c.Name))
			}
		}
	}
	g.Pass = len(g.Items) == 0
	g.Detail = fmt.Sprintf("%d intent net(s) in %d pcbpilot netclass(es) on the routed board", n, len(classes))
	return g
}

const reqEpsMil = 0.01
