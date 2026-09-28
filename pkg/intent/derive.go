package intent

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// ctx carries the derivation state.
type ctx struct {
	in    Input
	d     *powersim.Design
	sim   *powersim.Output
	spec  Spec
	rules pcbauto.Rules

	layers    int
	outerOz   float64
	innerOz   float64
	tempRise  float64
	refH, er  float64
	stackName string

	board *pcbauto.Board
	an    *pcbauto.Analysis
	circ  *pcbauto.Circuit

	worst *powersim.Result
	scens []*powersim.Result // solved scenarios (no "worst")

	parts   map[string]*powersim.Part
	simKind map[string]string          // ref → sim model kind
	modelID map[string]string          // ref → bound model id
	conf    map[string]string          // ref → model confidence
	model   map[string]*powersim.Model // ref → library model (ratings)
	netPins map[string][]pinRef        // net → pins (design order)
	mains   map[string]bool            // AC line nets

	out *Intent

	blockOfPart  map[string]string // ref → intent block id
	blockByID    map[string]*Block
	domOfPcb     map[string]*Domain    // pcbauto domain id → intent domain
	domOfNet     map[string]string     // net → intent domain id
	domSpec      map[string]SpecDomain // intent domain id → matching spec.domains entry
	floats       map[string]floatInfo  // nets riding on a switch node
	netBlock     map[string]string     // net → intent block id
	volts        map[string]Voltage    // net → voltage envelope
	uncontrolled []string              // diff nets whose impedance the stackup cannot hold
}

type pinRef struct {
	Ref, Pin, Name string
}

// Derive builds the design intent.
func Derive(in Input) (*Intent, error) {
	c := &ctx{in: in}
	if in.Spec != nil {
		c.spec = *in.Spec
	}
	switch {
	case in.Design == nil && in.Sim == nil:
		return nil, fmt.Errorf("intent: need a schematic design and/or a sim document")
	case in.Design == nil:
		c.d = DesignFromSim(in.Sim)
	default:
		c.d = in.Design
	}
	c.sim = in.Sim
	if c.sim == nil {
		out, _, err := powersim.Simulate(c.d, in.Libs, in.SimOptions)
		if err != nil {
			return nil, fmt.Errorf("intent: power simulation: %w", err)
		}
		c.sim = out
	}
	if err := c.setup(); err != nil {
		return nil, err
	}
	c.out = &Intent{SchemaVersion: SchemaVersion, Generator: Generator, Sources: in.Sources,
		Nets: map[string]*NetPlan{}, Blocks: []*Block{}, Domains: []*Domain{}, Pairs: []*Pair{}, NetClasses: []*NetClass{}, Findings: []*Finding{}}
	if c.out.Sources.Schematic == nil {
		c.out.Sources.Schematic = []string{}
	}
	c.out.Copper = &Copper{Layers: c.layers, OuterOz: c.outerOz, InnerOz: c.innerOz, TempRiseC: c.tempRise, RefHeightMil: c.refH, Er: c.er,
		Stackup: c.stackName, MinTrackMil: c.rules.TrackWidth, ClearanceMil: c.rules.Clearance, ViaDrillMil: c.rules.ViaDrill, ViaDiaMil: c.rules.ViaDia}
	c.out.Simulation = c.simInfo()
	c.out.Definitions = &Definitions{
		Voltage:   "V over the solved DC scenarios: nom = typical, min/max = envelope, peak = highest instantaneous (switch node swings to Vin; AC = √2·Vrms)",
		CurrentA:  "sizing current (A): simulated worst-case DC net current, switch-node ripple RMS, or a declared/heuristic budget (currentSource)",
		WidthMil:  "IPC-2221/2152 width for currentA at tempRiseC: outer on outerOz, inner on innerOz; min = narrowest allowed neck (largest single-pin branch current, class floor); diff/RF = impedance width",
		Clearance: "IPC-2221B B2 (B4 when coated) for the net's peak voltage to its own reference, never below the fab clearance; domain-to-domain insulation is in pairs[]",
		Pairs:     "insulation between domains that an isolation part bridges; numbers from SafetyDistances(pair, standard)",
	}
	c.buildDomains()
	c.out.Standard = resolveStandard(c.spec.Standard, c.hasHazard())
	c.buildBlocks()
	c.buildNets()
	c.assignDomainsToBlocks()
	c.buildPairs()
	c.buildNetClasses()
	c.buildFindings()
	return c.out, nil
}

func (c *ctx) setup() error {
	c.parts = map[string]*powersim.Part{}
	c.netPins = map[string][]pinRef{}
	for _, p := range c.d.Parts {
		c.parts[p.Ref] = p
		for _, pin := range p.Pins {
			if pin.Net != "" {
				c.netPins[pin.Net] = append(c.netPins[pin.Net], pinRef{p.Ref, pin.Number, pin.Name})
			}
		}
	}
	c.worst = pickResult(c.sim, "worst")
	for i := range c.sim.Results {
		if r := &c.sim.Results[i]; r.Scenario != "worst" {
			c.scens = append(c.scens, r)
		}
	}
	if c.worst == nil {
		c.worst = c.scens[len(c.scens)-1]
	}
	c.simKind, c.modelID, c.conf, c.model = map[string]string{}, map[string]string{}, map[string]string{}, map[string]*powersim.Model{}
	for _, m := range c.sim.Models {
		c.simKind[m.Ref], c.modelID[m.Ref], c.conf[m.Ref] = m.Kind, m.ModelID, m.Confidence
	}
	for _, p := range c.d.Parts {
		if mr := c.in.Libs.Match(p); mr != nil {
			c.model[p.Ref] = mr.Model
			continue
		}
		if id := c.modelID[p.Ref]; id != "" {
			for _, lib := range c.in.Libs {
				for i := range lib.Models {
					if lib.Models[i].ID == id {
						c.model[p.Ref] = &lib.Models[i]
					}
				}
			}
		}
	}
	// Stackup / copper.
	c.layers = c.spec.Layers
	if c.layers <= 0 {
		c.layers = 4
	}
	c.rules = pcbauto.DefaultRules()
	c.outerOz, c.innerOz = 1, 0.5
	if c.spec.OuterOz > 0 {
		c.outerOz = c.spec.OuterOz
	}
	if c.spec.InnerOz > 0 {
		c.innerOz = c.spec.InnerOz
	}
	c.rules.CopperOz, c.rules.InnerCopperOz = c.outerOz, c.innerOz
	if r := c.spec.Rules; r != nil {
		setIf(&c.rules.Clearance, r.ClearanceMil)
		setIf(&c.rules.TrackWidth, r.TrackMil)
		setIf(&c.rules.ViaDrill, r.ViaDrillMil)
		setIf(&c.rules.ViaDia, r.ViaDiaMil)
		if c.rules.MinTrack > c.rules.TrackWidth {
			c.rules.MinTrack = c.rules.TrackWidth
		}
	}
	c.tempRise = c.spec.TempRiseC
	if c.tempRise <= 0 {
		c.tempRise = 10
	}
	c.refH, c.er, c.stackName = 8.4, 4.05, "JLC04161H-7628 (4-layer 1.6 mm, L1→L2 prepreg 0.2104 mm)"
	switch {
	case c.layers == 2:
		c.refH, c.stackName = c.rules.BoardThickMil, "JLC 2-layer 1.6 mm FR4 (no adjacent reference plane)"
	case c.layers >= 6:
		c.refH, c.er, c.stackName = 3.5, 4.1, "JLC 6+-layer (thin prepreg ≈ 0.09 mm, engineering default)"
	}
	c.mains = map[string]bool{}
	if c.spec.Mains != nil {
		for _, n := range c.spec.Mains.Nets {
			c.mains[n] = true
		}
	}
	for net := range c.netPins {
		if reMains.MatchString(strings.ToUpper(strings.TrimSpace(net))) {
			c.mains[net] = true
		}
	}
	c.propagateMains()
	// pcbauto understanding on a geometry-free board.
	b, err := buildBoard(c.d, c.rules, c.layers)
	if err != nil {
		return err
	}
	c.board = b
	c.rules = b.Rules
	raw, _ := json.Marshal(c.sim)
	sp, err := pcbauto.ParseSim(raw)
	if err != nil {
		return err
	}
	ps := pcbauto.PowerSpec{TempRiseC: c.tempRise, Sim: sp, DiffOhm: 90}
	if c.spec.Standard != nil {
		ps.Coated = c.spec.Standard.Coated
	}
	for _, r := range c.spec.Rails {
		if r.CurrentA > 0 || r.Voltage != 0 {
			ps.Rails = append(ps.Rails, pcbauto.PowerRail{Net: r.Net, Voltage: r.Voltage, CurrentA: r.CurrentA})
		}
	}
	for _, hs := range c.spec.HSInterfaces {
		ps.DiffPairs = append(ps.DiffPairs, hs.Pairs...)
	}
	var st *pcbauto.Stackup
	if c.layers == 2 {
		st = &pcbauto.Stackup{Layers: 2}
	} else {
		st = &pcbauto.Stackup{Layers: c.layers, RefHeightMil: c.refH, Er: c.er}
	}
	c.an = pcbauto.Analyze(b, ps, st)
	c.circ = pcbauto.Understand(b, c.an)
	return nil
}

func setIf(dst *float64, v float64) {
	if v > 0 {
		*dst = v
	}
}

func (c *ctx) simInfo() *SimInfo {
	si := &SimInfo{Generator: c.sim.Generator, Scenarios: c.sim.Scenarios, Converged: true}
	seen := map[string]bool{}
	for _, r := range c.sim.Results {
		si.Converged = si.Converged && r.Converged
		if r.Scenario != c.worst.Scenario {
			continue
		}
		for _, w := range r.Warnings {
			if !seen[w] {
				seen[w] = true
				si.Warnings = append(si.Warnings, w)
			}
		}
		si.Assumptions = append(si.Assumptions, r.Assumptions...)
	}
	return si
}

// ---- small helpers -----------------------------------------------------------

// scenarioVoltages returns the net's voltage in each solved scenario where
// it is not floating.
func (c *ctx) scenarioVoltages(net string) (vs []float64, typ float64, hasTyp bool) {
	for _, r := range c.scens {
		nr := r.Nets[net]
		if nr == nil || nr.Floating {
			continue
		}
		vs = append(vs, nr.Voltage)
		if r.Scenario == "typical" {
			typ, hasTyp = nr.Voltage, true
		}
	}
	return
}

// part result in the worst scenario.
func (c *ctx) partW(ref string) *powersim.PartResult {
	if c.worst == nil {
		return nil
	}
	return c.worst.Parts[ref]
}

func (c *ctx) partScen(scen, ref string) *powersim.PartResult {
	for _, r := range c.scens {
		if r.Scenario == scen {
			return r.Parts[ref]
		}
	}
	return nil
}

func (c *ctx) netW(net string) *powersim.NetResult {
	if c.worst == nil {
		return nil
	}
	return c.worst.Nets[net]
}

// isGround reports a ground net (sim role or name).
func (c *ctx) isGround(net string) bool {
	if nr := c.netW(net); nr != nil && nr.Role == "ground" {
		return true
	}
	return c.an.Plan(net, c.rules).Role == pcbauto.RoleGround
}

func (c *ctx) isPowerNet(net string) bool {
	if nr := c.netW(net); nr != nil && (nr.Role == "power") {
		return true
	}
	return c.an.Plan(net, c.rules).Role == pcbauto.RolePower
}

// partNets lists a part's distinct nets (pin order).
func (c *ctx) partNets(ref string) []string {
	var out []string
	seen := map[string]bool{}
	if p := c.parts[ref]; p != nil {
		for _, pin := range p.Pins {
			if pin.Net != "" && !seen[pin.Net] {
				seen[pin.Net] = true
				out = append(out, pin.Net)
			}
		}
	}
	return out
}

// pinNet returns the net of the first pin whose name or number matches.
func (c *ctx) pinNet(ref string, names ...string) string {
	p := c.parts[ref]
	if p == nil {
		return ""
	}
	for _, n := range names {
		for _, pin := range p.Pins {
			if strings.EqualFold(pin.Name, n) || pin.Number == n {
				return pin.Net
			}
		}
	}
	return ""
}

// partsOn lists refs with a pin on net.
func (c *ctx) partsOn(net string) []string {
	var out []string
	seen := map[string]bool{}
	for _, pr := range c.netPins[net] {
		if !seen[pr.Ref] {
			seen[pr.Ref] = true
			out = append(out, pr.Ref)
		}
	}
	return out
}

func (c *ctx) kind(ref string) pcbauto.PartKind { return c.circ.Kinds[ref] }

// capacitance of a capacitor part in farads (0 when unknown).
func (c *ctx) capacitance(ref string) float64 {
	p := c.parts[ref]
	if p == nil {
		return 0
	}
	if v, ok := powersim.ParseValue(p.Value); ok && v > 0 && v < 1 {
		return v
	}
	if m := reCapDesc.FindStringSubmatch(p.Description); m != nil {
		if v, ok := powersim.ParseValue(m[1] + "F"); ok {
			return v
		}
	}
	return 0
}

// capsOn returns the capacitors between net and ground and their total C.
// unknown counts capacitors whose value could not be read.
func (c *ctx) capsOn(net string) (refs []string, total float64, unknown int) {
	for _, ref := range c.partsOn(net) {
		if c.kind(ref) != pcbauto.KindCapacitor {
			continue
		}
		ns := c.partNets(ref)
		if len(ns) == 2 && (c.isGround(ns[0]) || c.isGround(ns[1])) {
			refs = append(refs, ref)
			v := c.capacitance(ref)
			if v == 0 {
				unknown++
			}
			total += v
		}
	}
	return sortRefs(refs), total, unknown
}

func fmtV(v float64) string { return trimFloat(v, 3) + " V" }
func fmtA(a float64) string {
	if a < 0.1 {
		return trimFloat(a*1e3, 3) + " mA"
	}
	return trimFloat(a, 3) + " A"
}
func fmtW(w float64) string {
	if math.Abs(w) < 0.1 {
		return trimFloat(w*1e3, 3) + " mW"
	}
	return trimFloat(w, 3) + " W"
}

func fmtF(f float64) string {
	switch {
	case f >= 1e-6:
		return trimFloat(f*1e6, 3) + " µF"
	case f >= 1e-9:
		return trimFloat(f*1e9, 3) + " nF"
	}
	return trimFloat(f*1e12, 3) + " pF"
}

// trimFloat prints v with up to sig significant decimals, no trailing zeros.
func trimFloat(v float64, sig int) string {
	s := fmt.Sprintf("%.*f", sig, v)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		s = "0"
	}
	return s
}

func round(v float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.Round(v*p) / p
}

func sortedKeys[M ~map[string]V, V any](m M) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func has(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

var (
	reSeriesRef  = regexp.MustCompile(`^(F|FU|PTC|NTC|RT|TH|L|FB|LF|CMC|RV|MOV|VDR|ZNR|R)[0-9]`)
	reContactPin = regexp.MustCompile(`(?i)^(COM\d?|NO\d?|NC\d?)$`)
)

// propagateMains extends the AC line set through series parts (fuse,
// thermistor, choke, varistor, resistor) and relay contacts: a fused L is
// still line voltage. Capacitors are not crossed (a Y-cap reaches the
// secondary ground on purpose).
func (c *ctx) propagateMains() {
	if len(c.mains) == 0 {
		return
	}
	for changed := true; changed; {
		changed = false
		for _, p := range c.d.Parts {
			var group []string
			switch {
			case reSeriesRef.MatchString(strings.ToUpper(p.Ref)):
				for _, pin := range p.Pins {
					group = append(group, pin.Net)
				}
			case strings.HasPrefix(strings.ToUpper(p.Ref), "K"):
				for _, pin := range p.Pins {
					if reContactPin.MatchString(pin.Name) {
						group = append(group, pin.Net)
					}
				}
			}
			hot := false
			for _, n := range group {
				hot = hot || c.mains[n]
			}
			if !hot {
				continue
			}
			for _, n := range group {
				if n != "" && !c.mains[n] && !reGroundName.MatchString(n) {
					c.mains[n] = true
					changed = true
				}
			}
		}
	}
}

var reGroundName = regexp.MustCompile(`(?i)^([A-Z0-9]+_)?(A|D|P|S|C|E)?GND[A-Z0-9_]*$|^VSS[A-Z0-9_]*$|^GROUND$|^0V$`)
