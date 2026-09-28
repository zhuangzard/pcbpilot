// Package postsim is the post-layout (设计后) simulation: it verifies the
// FINISHED board — the copper read back from EasyEDA by `pcb dump
// --include-copper` (or pcb auto's own routed result in the same shape) —
// against the circuit currents and part dissipation of `sim power`.
//
// Two coupled steady-state solves on one raster of the real copper:
//
//  1. DC electrical, per power / ground / switch net and per scenario: the
//     supplying part's pads are the reference, every other pad draws or feeds
//     its simulated current; outputs drop at every load pad, current density
//     (track pieces and sheet cells), via barrel currents vs IPC ampacity and
//     the Joule heat per cell.
//  2. Thermal, 2.5-D finite volume per copper layer: copper coverage, FR-4,
//     via barrels, top/bottom convection (+ optional linearised radiation);
//     sources = part dissipation on its pads + Joule heat.
//
// No editor, daemon or filesystem dependency. Units: board mil, SI inside.
package postsim

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// Rating is the thermal part of a power-models.json "ratings" entry.
type Rating struct {
	ThetaJbCW float64 `json:"thetaJbCW,omitempty"`
	ThetaJcCW float64 `json:"thetaJcCW,omitempty"`
	ThetaJaCW float64 `json:"thetaJaCW,omitempty"`
	TjMaxC    float64 `json:"tjMaxC,omitempty"`
	Source    string  `json:"source,omitempty"`
}

// Options are the run parameters; start from DefaultOptions.
type Options struct {
	CellMm     float64
	Sub        int
	MaxCells   int
	AmbientC   float64
	HTop       float64
	HBottom    float64
	Emissivity float64
	KFR4XY     float64
	KFR4Z      float64
	PlatingMil float64
	ViaDeltaTC float64
	Budget     pcbauto.IRBudget
	Scenarios  []string
	Planes     map[int]string // explicit plane layers (net "" / "none" = no plane)
	Stack      StackOptions
	Ratings    map[string]Rating // power-model id → thermal ratings
	BoardWarnC float64
	BoardFailC float64
	TjWarnPct  float64
	Source     string
	TempRiseC  float64 // allowed copper temperature rise (intent copper.tempRiseC)
	Margin     float64 // width / via margin on the IPC current
	JMax       float64 // optional absolute current-density limit (A/mm²), 0 = IPC-derived
}

// DefaultOptions: 0.5 mm cells, 25 °C still air (h = 10 W/m²K per face),
// FR-4 0.3 W/mK, 0.7 mil via plating (JLC 18 µm), IR budget max(2 %, 30 mV).
func DefaultOptions() Options {
	return Options{CellMm: 0.5, Sub: 5, MaxCells: 60000, AmbientC: 25, HTop: 10, HBottom: 10,
		KFR4XY: 0.3, KFR4Z: 0.3, PlatingMil: 0.7, ViaDeltaTC: 10, Budget: pcbauto.DefaultIRBudget(),
		BoardWarnC: 105, BoardFailC: 130, TjWarnPct: 80, TempRiseC: 10, Margin: 1.2}
}

// ApplyIntent takes the stackup (copper {outerOz, innerOz, stackup}) and the
// allowed copper temperature rise (copper.tempRiseC) from intent.json.
// Explicit values already in o win.
func ApplyIntent(raw []byte, o *Options) error {
	var d struct {
		Copper *struct {
			OuterOz   float64 `json:"outerOz"`
			InnerOz   float64 `json:"innerOz"`
			Stackup   string  `json:"stackup"`
			TempRiseC float64 `json:"tempRiseC"`
		} `json:"copper"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("intent: %w", err)
	}
	if d.Copper == nil {
		return nil
	}
	if o.Stack.OuterOz <= 0 {
		o.Stack.OuterOz = d.Copper.OuterOz
	}
	if o.Stack.InnerOz <= 0 {
		o.Stack.InnerOz = d.Copper.InnerOz
	}
	if o.Stack.Description == "" {
		o.Stack.Description = d.Copper.Stackup
	}
	if d.Copper.TempRiseC > 0 {
		o.TempRiseC = d.Copper.TempRiseC
	}
	return nil
}

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }

// ParseRatings reads the thermal ratings of a power-models.json.
func ParseRatings(raw []byte) (map[string]Rating, error) {
	var d struct {
		Models []struct {
			ID      string  `json:"id"`
			Ratings *Rating `json:"ratings"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("power models: %w", err)
	}
	out := map[string]Rating{}
	for _, m := range d.Models {
		if m.ID != "" && m.Ratings != nil {
			out[m.ID] = *m.Ratings
		}
	}
	return out, nil
}

type eCase struct {
	scenario string
	ref      string
	refs     []int
	inj      map[int]float64
	pins     []caseePin
	currentA float64
}

type caseePin struct {
	key  string
	dir  string
	i    float64
	node int
}

// Run verifies boardRaw (pcb dump JSON) against simRaw (sim power JSON).
func Run(boardRaw, simRaw []byte, o Options) (*Result, error) {
	b, err := ParseBoard(boardRaw)
	if err != nil {
		return nil, err
	}
	var sim powersim.Output
	if err := json.Unmarshal(simRaw, &sim); err != nil {
		return nil, fmt.Errorf("sim: %w", err)
	}
	if len(sim.Results) == 0 {
		return nil, fmt.Errorf("sim: no results (run pcbpilot sim power)")
	}
	res := &Result{SchemaVersion: 1, Generator: "pcbpilot sim post-layout", board: b}
	res.Inputs.BoardSemantic = b.Semantic
	res.Inputs.Source = o.Source
	res.Assumptions = append(res.Assumptions, b.Notes...)
	if !b.Copper {
		res.Findings = append(res.Findings, Finding{Severity: "warn", Kind: "no-copper",
			Message: "board dump has no copper: re-dump with pcb dump --include-copper"})
	}

	// Scenarios.
	var scen []*powersim.Result
	var worst *powersim.Result
	want := map[string]bool{}
	for _, s := range o.Scenarios {
		want[s] = true
	}
	for i := range sim.Results {
		r := &sim.Results[i]
		if r.Scenario == "worst" {
			worst = r
			if want["worst"] {
				scen = append(scen, r)
			}
			continue
		}
		if len(want) == 0 || want[r.Scenario] {
			scen = append(scen, r)
		}
	}
	merged := false
	if len(scen) == 0 && worst != nil {
		scen, merged = []*powersim.Result{worst}, true
	}
	for _, r := range scen {
		if r.Scenario == "worst" {
			merged = true
		}
	}
	if len(scen) == 0 {
		return nil, fmt.Errorf("sim: no scenario matches %v", o.Scenarios)
	}
	res.Mode = "scenarios"
	if merged {
		res.Mode = "merged-envelope"
		res.Assumptions = append(res.Assumptions, "sim file has only the merged worst result (per-pin maxima, not KCL-consistent): each supply is solved as the sole source and the per-pad maxima kept; part powers are the per-part maxima (conservative when sources are mutually exclusive)")
	}
	for _, r := range scen {
		res.Scenarios = append(res.Scenarios, r.Scenario)
	}

	// Roles and ground nets.
	role := map[string]string{}
	pinCount := map[string]int{}
	for _, r := range scen {
		for n, nr := range r.Nets {
			if role[n] == "" || nr.Role == "power" || nr.Role == "ground" {
				role[n] = nr.Role
			}
			pinCount[n] = max(pinCount[n], len(nr.Pins))
		}
	}

	// Planes.
	res.Settings.Planes = map[string]string{}
	if o.Planes != nil {
		ids := sortedKeys(o.Planes)
		for _, l := range ids {
			net := o.Planes[l]
			if net == "" || strings.EqualFold(net, "none") {
				continue
			}
			b.AddPlane(l, net)
			res.Settings.Planes[LayerName(l)] = net
			res.Assumptions = append(res.Assumptions, fmt.Sprintf("%s declared a negative plane of %s (--plane): outline inset %.0f mil, antipads = other nets' copper + %.1f mil clearance", LayerName(l), net, b.Rules.CopperToEdgeMil, b.Rules.ClearanceMil))
		}
	}
	for _, l := range b.EmptyInnerLayers() {
		if _, set := o.Planes[l]; set || !b.Copper {
			continue
		}
		gnd := ""
		for n, r := range role {
			if r == "ground" && (gnd == "" || pinCount[n] > pinCount[gnd]) {
				gnd = n
			}
		}
		if gnd == "" {
			res.Findings = append(res.Findings, Finding{Severity: "warn", Kind: "empty-layer",
				Message: fmt.Sprintf("%s carries no copper in the dump and no ground net is known: modelled as bare dielectric (declare --plane %d=NET if it is a negative plane)", LayerName(l), l)})
			continue
		}
		b.AddPlane(l, gnd)
		res.Settings.Planes[LayerName(l)] = gnd
		res.Assumptions = append(res.Assumptions, fmt.Sprintf("%s has no copper objects in the dump (a negative/内电层 plane is not listed by pcb dump): assumed a solid %s plane — board outline inset %.0f mil (copper-to-edge), antipads around every other-net via / THT pad / cutout = its copper + %.1f mil clearance, same-net vias connect directly (thermal-relief spokes ignored). Override with --plane %d=NET or --plane %d=none", LayerName(l), gnd, b.Rules.CopperToEdgeMil, b.Rules.ClearanceMil, l, l))
	}

	// Stackup + raster.
	st := BuildStackup(b.Layers, o.Stack)
	for k, l := range st.Layers {
		for _, a := range b.Areas {
			if a.Kind == "plane" && a.Layer == l.ID {
				st.Layers[k].Kind, st.Layers[k].Plane = "plane", a.Net
			}
		}
	}
	res.Stackup = st
	g := NewGrid(b, o.CellMm/MilMm, o.Sub, o.MaxCells)
	g.Rasterise(b, st)
	res.grid = g
	inside := 0
	for _, in := range g.Inside {
		if in {
			inside++
		}
	}
	res.Grid = GridInfo{CellMm: round(g.Cell*MilMm, 4), NX: g.NX, NY: g.NY, Sub: g.Sub, Cells: inside}
	if math.Abs(g.Cell*MilMm-o.CellMm) > 1e-6 {
		res.Assumptions = append(res.Assumptions, fmt.Sprintf("cell coarsened from %.3g mm to %.3g mm to keep ≤ %d cells per layer", o.CellMm, g.Cell*MilMm, o.MaxCells))
	}

	hTop, hBot := o.HTop, o.HBottom
	res.Assumptions = append(res.Assumptions, fmt.Sprintf("boundary: the bare board in still air — natural convection on both faces (top %.1f, bottom %.1f W/m²K), no enclosure, fan or airflow (no CFD); board edges adiabatic", hTop, hBot))
	res.Settings = Settings{AmbientC: o.AmbientC, HTop: round(hTop, 2), HBottom: round(hBot, 2), Emissivity: o.Emissivity, KFR4XY: o.KFR4XY, KFR4Z: o.KFR4Z, PlatingMil: o.PlatingMil, ViaDeltaTC: o.ViaDeltaTC,
		IRBudget: o.Budget.String(), BoardLimitC: o.BoardWarnC, TjWarnPct: o.TjWarnPct, Planes: res.Settings.Planes}

	nl, nc := len(st.Layers), g.cells()
	res.JMap = make([][]float64, nl)
	for k := range res.JMap {
		res.JMap[k] = make([]float64, nc)
	}
	heat := map[string][]float64{} // scenario → per k*nc+c Joule heat (W)
	for _, r := range scen {
		heat[r.Scenario] = make([]float64, nl*nc)
	}
	platingMm := o.PlatingMil * MilMm
	viaWorst := map[*Via]*ViaResult{}
	var kept []keptNet

	// Nets to solve.
	var nets []string
	for n, r := range role {
		if r != "power" && r != "ground" && r != "switch" {
			continue
		}
		anyI := false
		for _, s := range scen {
			if nr := s.Nets[n]; nr != nil && nr.CurrentA > 0 {
				anyI = true
			}
		}
		if anyI {
			nets = append(nets, n)
		}
	}
	sort.Slice(nets, func(i, j int) bool {
		ri, rj := roleRank(role[nets[i]]), roleRank(role[nets[j]])
		if ri != rj {
			return ri < rj
		}
		return nets[i] < nets[j]
	})
	for _, net := range nets {
		nres := &NetResult{Net: net, Role: role[net]}
		res.Nets = append(res.Nets, nres)
		if _, ok := g.netIdx[net]; !ok {
			nres.Status = "no-copper"
			nres.Notes = append(nres.Notes, "net has no copper on the board")
			continue
		}
		en := buildNet(b, g, st, net, platingMm)
		nres.ViaCount = len(en.vias)
		nres.TrackPieces = len(en.segs)
		for _, cv := range en.cov {
			for _, v := range cv {
				if v > 0 {
					nres.SheetCells++
				}
			}
		}
		padWorst := map[string]*PadResult{}
		type caseOut struct {
			c     *eCase
			sol   *eSolution
			worst float64
			wpad  string
			loss  float64
			nomV  float64
		}
		// Solve every scenario's cases in parallel (independent systems).
		presolved := map[*eCase]*eSolution{}
		caseList := map[string][]*eCase{}
		noteList := map[string][]string{}
		var mu sync.Mutex
		var wgE sync.WaitGroup
		for _, r := range scen {
			nr := r.Nets[net]
			if nr == nil {
				continue
			}
			cs, ns := buildCases(nr, en, b, r.Scenario, merged)
			caseList[r.Scenario], noteList[r.Scenario] = cs, ns
			for _, c := range cs {
				wgE.Add(1)
				go func(c *eCase) {
					defer wgE.Done()
					sol := en.solve(c.refs, c.inj)
					mu.Lock()
					presolved[c] = sol
					mu.Unlock()
				}(c)
			}
		}
		wgE.Wait()
		var best *caseOut
		for _, r := range scen {
			nr := r.Nets[net]
			if nr == nil {
				continue
			}
			cases, notes := caseList[r.Scenario], noteList[r.Scenario]
			for _, s := range notes {
				if !contains(nres.Notes, s) {
					nres.Notes = append(nres.Notes, s)
				}
			}
			var scenBest *caseOut
			for _, c := range cases {
				sol := presolved[c]
				co := &caseOut{c: c, sol: sol, nomV: nr.Voltage}
				for _, p := range c.pins {
					d := math.Abs(sol.phi[p.node])
					pr := PadResult{Pad: p.key, Dir: p.dir, CurrentA: round(p.i, 6), Scenario: r.Scenario}
					if math.IsNaN(sol.phi[p.node]) {
						pr.DropMV = -1
					} else {
						pr.DropMV = round(d*1000, 3)
						pr.VoltageV = round(nr.Voltage-sol.phi[p.node], 5)
						if d > co.worst {
							co.worst, co.wpad = d, p.key
						}
					}
					if old := padWorst[p.key]; old == nil || pr.DropMV < 0 || old.DropMV >= 0 && pr.DropMV > old.DropMV {
						if old == nil || old.DropMV >= 0 {
							cp := pr
							padWorst[p.key] = &cp
						}
					}
				}
				for _, e := range en.edges {
					if i := sol.current(e); i != 0 {
						co.loss += i * i / e.g
					}
				}
				if scenBest == nil || co.loss > scenBest.loss || co.worst > scenBest.worst {
					if scenBest == nil || co.worst >= scenBest.worst {
						scenBest = co
					}
				}
				// Via currents (every case).
				for _, e := range en.edges {
					if e.kind != kindVia {
						continue
					}
					v := en.vias[e.idx]
					i := math.Abs(sol.current(e))
					if vr := viaWorst[v]; vr == nil || i > vr.CurrentA {
						amp := viaAmpacity(v.Drill, o.PlatingMil, o.ViaDeltaTC)
						viaWorst[v] = &ViaResult{ID: v.ID, Net: v.Net, X: round(v.C.X, 2), Y: round(v.C.Y, 2), DrillMil: v.Drill,
							CurrentA: i, AmpacityA: round(amp, 3), UsePct: round(100*i/amp, 1), Scenario: r.Scenario}
					}
				}
				nres.PerScenario = append(nres.PerScenario, ScenarioDrop{Scenario: r.Scenario, Reference: c.ref, CurrentA: round(c.currentA, 6),
					WorstMV: round(co.worst*1000, 3), WorstPad: co.wpad, LossMW: round(co.loss*1000, 4), Iter: sol.iter})
			}
			if scenBest != nil {
				// Joule heat of this scenario (the case with the largest drop).
				en.deposit(scenBest.sol, g, heat[r.Scenario])
				if best == nil || scenBest.worst > best.worst {
					best = scenBest
				}
			}
		}
		for _, p := range padWorst {
			nres.Pads = append(nres.Pads, *p)
		}
		sort.Slice(nres.Pads, func(i, j int) bool { return nres.Pads[i].Pad < nres.Pads[j].Pad })
		if best == nil {
			nres.Status = "no-reference"
			continue
		}
		nres.Scenario, nres.Reference, nres.NominalV = best.c.scenario, best.c.ref, best.nomV
		nres.CurrentA = round(best.c.currentA, 6)
		nres.WorstMV, nres.WorstPad = round(best.worst*1000, 3), best.wpad
		nres.LossMW = round(best.loss*1000, 4)
		en.details(best.sol, g, st, nres, res.JMap, o)
		kept = append(kept, keptNet{en: en, sol: best.sol, nres: nres})
		open := 0
		for i := range nres.Pads {
			p := &nres.Pads[i]
			p.OK = p.DropMV >= 0
			if nres.Role == "power" {
				nres.BudgetMV = round(o.Budget.Volts(nres.NominalV)*1000, 2)
				p.OK = p.OK && p.DropMV <= nres.BudgetMV
			}
			if p.DropMV < 0 && p.CurrentA > 0 {
				open++
			}
		}
		switch {
		case open > 0:
			nres.Status = "open"
		case nres.Role == "power" && nres.WorstMV > nres.BudgetMV:
			nres.Status = "over-budget"
		case nres.Role == "power":
			nres.Status = "ok"
		default:
			nres.Status = "info"
		}
		for _, v := range en.vias {
			if vr := viaWorst[v]; vr != nil && vr.CurrentA > nres.MaxViaA {
				nres.MaxViaA = vr.CurrentA
			}
		}
		nres.MaxViaA = round(nres.MaxViaA, 5)
	}
	for _, vr := range viaWorst {
		vr.CurrentA = round(vr.CurrentA, 5)
		res.Vias = append(res.Vias, *vr)
	}
	sort.Slice(res.Vias, func(i, j int) bool {
		if res.Vias[i].CurrentA != res.Vias[j].CurrentA {
			return res.Vias[i].CurrentA > res.Vias[j].CurrentA
		}
		return res.Vias[i].ID < res.Vias[j].ID
	})

	// Thermal.
	to := ThermalOptions{AmbientC: o.AmbientC, HTop: hTop, HBottom: hBot, Emissivity: o.Emissivity,
		KFR4XY: o.KFR4XY, KFR4Z: o.KFR4Z, PlatingMm: platingMm}
	tm := buildThermal(b, g, st, to)
	res.Thermal = runThermal(tm, b, g, st, scen, heat, to, o, res)
	// Copper self-heating: the Joule heat of the scenario with the largest
	// copper loss, alone, on the same board.
	var rise []float64
	bestJ, bestS := 0.0, ""
	for _, r := range scen {
		s := 0.0
		for _, w := range heat[r.Scenario] {
			s += w
		}
		if s > bestJ {
			bestJ, bestS = s, r.Scenario
		}
	}
	if bestJ > 0 {
		q := make([]float64, len(tm.cells))
		for kc, w := range heat[bestS] {
			if i := tm.idx[kc]; i >= 0 {
				q[i] += w
			}
		}
		th, _, _, _ := tm.solve(q, to)
		rise = make([]float64, len(tm.idx))
		for kc, i := range tm.idx {
			if i >= 0 {
				rise[kc] = th[i]
			}
		}
		res.Thermal.JouleScenario = bestS
		for _, t := range th {
			res.Thermal.MaxCopperRiseC = math.Max(res.Thermal.MaxCopperRiseC, t)
		}
		res.Thermal.MaxCopperRiseC = round(res.Thermal.MaxCopperRiseC, 3)
	}
	res.buildFeedback(kept, g, st, rise, o)
	for _, n := range res.Nets {
		n.MaxTraceRiseC = round(n.MaxTraceRiseC, 3)
	}

	res.Limits = Limits{TempRiseC: o.TempRiseC, Margin: o.Margin, JMaxAmm2: o.JMax}
	res.Model = modelNotes(o, st)
	res.findings(o)
	return res, nil
}

func roleRank(r string) int {
	switch r {
	case "power":
		return 0
	case "switch":
		return 1
	case "ground":
		return 2
	}
	return 3
}

// buildCases turns one scenario's pin currents into solve cases.
func buildCases(nr *powersim.NetResult, en *eNet, b *Board, scenario string, merged bool) ([]*eCase, []string) {
	var notes []string
	refDir := "source"
	if nr.Role == "ground" {
		refDir = "sink"
	}
	partI := map[string]float64{}
	entry := map[string]bool{}
	for _, p := range nr.Pins {
		if p.Dir == refDir {
			partI[p.Ref] += p.CurrentA
			if p.Kind == "connector-source" {
				entry[p.Ref] = true
			}
		}
	}
	maxI := 0.0
	for _, v := range partI {
		maxI = math.Max(maxI, v)
	}
	var supplies []string
	for p, v := range partI {
		if v <= 0 || len(entry) > 0 && !entry[p] {
			continue
		}
		if merged && v < 0.05*maxI {
			continue
		}
		supplies = append(supplies, p)
	}
	sort.Slice(supplies, func(i, j int) bool {
		if partI[supplies[i]] != partI[supplies[j]] {
			return partI[supplies[i]] > partI[supplies[j]]
		}
		return supplies[i] < supplies[j]
	})
	if !merged && len(supplies) > 1 {
		supplies = supplies[:1] // KCL-consistent: the largest is the reference, the others feed their own current
	}
	isSupply := map[string]bool{}
	for _, s := range supplies {
		isSupply[s] = true
	}
	var out []*eCase
	for _, sp := range supplies {
		c := &eCase{scenario: scenario, ref: sp, inj: map[int]float64{}, currentA: partI[sp]}
		for _, p := range nr.Pins {
			pd := b.PadByPin(p.Ref, p.Pin)
			var nd int
			ok := false
			if pd != nil {
				nd, ok = en.pad[pd]
			}
			switch {
			case p.Dir == refDir && p.Ref == sp:
				if ok {
					c.refs = append(c.refs, nd)
				}
				continue
			case p.Dir == refDir && merged && isSupply[p.Ref]:
				continue // another supply: idle in this case
			case p.Dir != "sink" && p.Dir != "source" || p.CurrentA <= 0:
				continue
			}
			if !ok {
				s := fmt.Sprintf("pin %s.%s (%s %.3g A) has no pad on the board", p.Ref, p.Pin, p.Dir, p.CurrentA)
				notes = append(notes, s)
				continue
			}
			i := p.CurrentA
			if p.Dir == "source" {
				i = -i
			}
			c.inj[nd] += i
			c.pins = append(c.pins, caseePin{key: p.Ref + "." + p.Pin, dir: p.Dir, i: p.CurrentA, node: nd})
		}
		if len(c.refs) == 0 {
			notes = append(notes, fmt.Sprintf("reference %s has no pad of this net on the board", sp))
			continue
		}
		out = append(out, c)
	}
	return out, notes
}

// viaAmpacity is the IPC-2221 external-curve current of a via barrel
// (cross-section π(d+t)t, mil²) at ΔT; IPC-2152 treats a buried conductor
// about like an external one, which is the usual via-current practice.
func viaAmpacity(drillMil, platingMil, dT float64) float64 {
	a := math.Pi * (drillMil + platingMil) * platingMil
	return 0.048 * math.Pow(dT, 0.44) * math.Pow(a, 0.725)
}

// trackAmpacity is the IPC-2221 external-curve current of a track.
func trackAmpacity(wMil, tMil, dT float64) float64 {
	return 0.048 * math.Pow(dT, 0.44) * math.Pow(wMil*tMil, 0.725)
}

// deposit adds the solution's Joule heat to q (k*nc+c, W).
func (n *eNet) deposit(sol *eSolution, g *Grid, q []float64) {
	for _, e := range n.edges {
		i := sol.current(e)
		if i == 0 {
			continue
		}
		p := i * i / e.g
		switch e.kind {
		case kindSheet:
			q[e.k*n.nc+e.cellA] += p / 2
			q[e.k*n.nc+e.cellB] += p / 2
		case kindTrack, kindVia:
			if e.cellA < 0 {
				continue
			}
			if e.kind == kindVia {
				q[e.k*n.nc+e.cellA] += p / 2
				q[(e.k+1)*n.nc+e.cellA] += p / 2
			} else {
				q[e.k*n.nc+e.cellA] += p
			}
		}
	}
}

// details fills current density, hot spots and top segments of the worst
// case, and merges its current-density map.
func (n *eNet) details(sol *eSolution, g *Grid, st *Stackup, nres *NetResult, jmap [][]float64, o Options) {
	hMm := g.Cell * MilMm
	var hs []Hotspot
	var segs []Segment
	for _, s := range n.segs {
		e := n.edges[s.Edge]
		i := math.Abs(sol.current(e))
		tMm := st.Layers[s.K].CuMm
		wMm := s.W * MilMm
		j := i / (wMm * tMm)
		if c := g.cellAt(lerp(s.A, s.B, 0.5)); c >= 0 && j > jmap[s.K][c] {
			jmap[s.K][c] = j
		}
		if j > nres.MaxJAmm2 {
			nres.MaxJAmm2 = j
		}
		mid := lerp(s.A, s.B, 0.5)
		hs = append(hs, Hotspot{Layer: st.Layers[s.K].Name, X: round(mid.X, 1), Y: round(mid.Y, 1), Value: j, Kind: "track", WidthMil: s.W, CurrentA: i})
		segs = append(segs, Segment{Layer: st.Layers[s.K].Name, A: rp(s.A), B: rp(s.B), WidthMil: s.W, LengthMil: round(dist(s.A, s.B), 2),
			CurrentA: round(i, 5), JAmm2: round(j, 3), PowerMW: round(i*i/e.g*1000, 5), DropMV: round(i/e.g*1000, 4),
			CapacityA: round(trackAmpacity(s.W, tMm/MilMm, o.ViaDeltaTC), 3)})
	}
	// Sheet cells: average the currents of the links on each side.
	nl := len(st.Layers)
	ix := make([][]float64, nl)
	iy := make([][]float64, nl)
	for k := range ix {
		ix[k] = make([]float64, n.nc)
		iy[k] = make([]float64, n.nc)
	}
	for _, e := range n.edges {
		if e.kind != kindSheet {
			continue
		}
		i := sol.current(e)
		if e.dir == 0 {
			ix[e.k][e.cellA] += i / 2
			ix[e.k][e.cellB] += i / 2
		} else {
			iy[e.k][e.cellA] += i / 2
			iy[e.k][e.cellB] += i / 2
		}
	}
	for k := 0; k < nl; k++ {
		tMm := st.Layers[k].CuMm
		for c := 0; c < n.nc; c++ {
			cov := n.cov[k][c]
			if cov <= 0 {
				continue
			}
			i := math.Hypot(ix[k][c], iy[k][c])
			if i == 0 {
				continue
			}
			j := i / (hMm * cov * tMm)
			if j > jmap[k][c] {
				jmap[k][c] = j
			}
			if j > nres.MaxJAmm2 {
				nres.MaxJAmm2 = j
			}
			p := g.cellCentre(c)
			hs = append(hs, Hotspot{Layer: st.Layers[k].Name, X: round(p.X, 1), Y: round(p.Y, 1), Value: j, Kind: "sheet", CurrentA: i})
		}
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].Value > hs[j].Value })
	for _, h := range hs {
		if len(nres.Hotspots) >= 5 {
			break
		}
		near := false
		for _, o := range nres.Hotspots {
			if o.Layer == h.Layer && math.Hypot(o.X-h.X, o.Y-h.Y) < 60 {
				near = true
				break
			}
		}
		if !near {
			h.Value = round(h.Value, 3)
			h.CurrentA = round(h.CurrentA, 5)
			nres.Hotspots = append(nres.Hotspots, h)
		}
	}
	nres.MaxJAmm2 = round(nres.MaxJAmm2, 3)
	sort.Slice(segs, func(i, j int) bool { return segs[i].PowerMW > segs[j].PowerMW })
	if len(segs) > 8 {
		segs = segs[:8]
	}
	nres.Segments = segs
	// Track necks over their IPC current.
	for _, s := range n.segs {
		e := n.edges[s.Edge]
		i := math.Abs(sol.current(e))
		if capA := trackAmpacity(s.W, st.Layers[s.K].CuMm/MilMm, o.ViaDeltaTC); i > capA {
			nres.Notes = append(nres.Notes, fmt.Sprintf("track %s %.1f mil at (%.0f,%.0f) carries %.3f A > IPC %.3f A (ΔT %.0f °C)",
				st.Layers[s.K].Name, s.W, s.A.X, s.A.Y, i, capA, o.ViaDeltaTC))
		}
	}
}

// runThermal solves every scenario and keeps the hottest for the maps.
func runThermal(tm *thermalModel, b *Board, g *Grid, st *Stackup, scen []*powersim.Result, joule map[string][]float64, to ThermalOptions, o Options, res *Result) *ThermalResult {
	nl, nc := len(st.Layers), g.cells()
	tr := &ThermalResult{}
	// Cells under each part's pads (on its side; THT on the side too).
	type partCells struct {
		part  *Part
		subs  map[int]int // k*nc+c → sub-sample count
		total int
	}
	pcs := map[string]*partCells{}
	for _, p := range b.Parts {
		pc := &partCells{part: p, subs: map[int]int{}}
		k := st.index(p.Side)
		if k < 0 {
			k = 0
		}
		for _, pd := range p.Pads {
			kk := k
			if pd.Layer != LayerMulti {
				if x := st.index(pd.Layer); x >= 0 {
					kk = x
				}
			}
			g.paintPad(pd, 0, func(si int) {
				c := g.cellOfSub(si)
				if g.Inside[c] {
					pc.subs[kk*nc+c]++
					pc.total++
				}
			})
		}
		if pc.total == 0 && p.BBox.valid() {
			x0, y0, x1, y1 := g.subRange(p.BBox)
			for sy := y0; sy <= y1; sy++ {
				for sx := x0; sx <= x1; sx++ {
					c := g.cellOfSub(sy*g.subNX() + sx)
					if g.Inside[c] {
						pc.subs[k*nc+c]++
						pc.total++
					}
				}
			}
		}
		pcs[p.Ref] = pc
	}
	res.thermal = tm
	res.partCells = map[string][]int{}
	for ref, pc := range pcs {
		for kc := range pc.subs {
			res.partCells[ref] = append(res.partCells[ref], kc)
		}
		sort.Ints(res.partCells[ref])
	}
	bestMax := math.Inf(-1)
	var hot []float64
	partBest := map[string]*PartThermal{}
	type thSol struct {
		q              []float64
		partsW, jouleW float64
		theta, loss    []float64
		iters          int
	}
	sols := make([]*thSol, len(scen))
	for si, r := range scen {
		q := make([]float64, len(tm.cells))
		partsW, jouleW := 0.0, 0.0
		for ref, pr := range r.Parts {
			if pr == nil || pr.PowerW <= 0 {
				continue
			}
			pc := pcs[ref]
			if pc == nil || pc.total == 0 {
				msg := fmt.Sprintf("%s dissipates %.3g W but has no footprint on the board: left out of the thermal solve", ref, pr.PowerW)
				if !contains(res.Assumptions, msg) {
					res.Assumptions = append(res.Assumptions, msg)
				}
				continue
			}
			for kc, n := range pc.subs {
				if i := tm.idx[kc]; i >= 0 {
					q[i] += pr.PowerW * float64(n) / float64(pc.total)
				}
			}
			partsW += pr.PowerW
		}
		for kc, w := range joule[r.Scenario] {
			if w == 0 {
				continue
			}
			if i := tm.idx[kc]; i >= 0 {
				q[i] += w
				jouleW += w
			}
		}
		sols[si] = &thSol{q: q, partsW: partsW, jouleW: jouleW}
	}
	var wg sync.WaitGroup
	for _, sl := range sols {
		wg.Add(1)
		go func(sl *thSol) {
			defer wg.Done()
			sl.theta, sl.loss, sl.iters, _ = tm.solve(sl.q, to)
		}(sl)
	}
	wg.Wait()
	for si, r := range scen {
		sl := sols[si]
		partsW, jouleW, theta, loss, iters := sl.partsW, sl.jouleW, sl.theta, sl.loss, sl.iters
		sumLoss := 0.0
		for _, l := range loss {
			sumLoss += l
		}
		mx := 0.0
		for _, t := range theta {
			mx = math.Max(mx, t)
		}
		sh := ScenarioHeat{Scenario: r.Scenario, PartsW: round(partsW, 5), JouleW: round(jouleW, 6), MaxC: round(o.AmbientC+mx, 2),
			LossW: round(sumLoss, 5), Iter: iters}
		if tot := partsW + jouleW; tot > 0 {
			sh.ErrPct = round(100*(sumLoss-tot)/tot, 4)
		}
		tr.PerScenario = append(tr.PerScenario, sh)
		if mx > bestMax {
			bestMax, hot = mx, theta
			res.hotQ, res.hotTheta = sl.q, theta
			tr.Scenario, tr.PartsW, tr.JouleW, tr.LossW = r.Scenario, sh.PartsW, sh.JouleW, sh.LossW
			tr.TotalW = round(partsW+jouleW, 5)
			tr.BalanceErrPct = sh.ErrPct
		}
		// Parts in this scenario.
		for _, p := range b.Parts {
			pc := pcs[p.Ref]
			if pc == nil || pc.total == 0 {
				continue
			}
			pw := 0.0
			if pr := r.Parts[p.Ref]; pr != nil && pr.PowerW > 0 {
				pw = pr.PowerW
			}
			tmax, tsum, n := math.Inf(-1), 0.0, 0
			for kc := range pc.subs {
				if i := tm.idx[kc]; i >= 0 {
					t := o.AmbientC + theta[i]
					tmax = math.Max(tmax, t)
					tsum += t
					n++
				}
			}
			if n == 0 {
				continue
			}
			pt := &PartThermal{Ref: p.Ref, Device: p.Device, Side: LayerName(p.Side), PowerW: round(pw, 6), Scenario: r.Scenario,
				BoardMaxC: round(tmax, 2), BoardMeanC: round(tsum/float64(n), 2)}
			rt, hasR := o.Ratings[modelOf(r, p.Ref)]
			switch {
			case hasR && rt.ThetaJbCW > 0:
				pt.ThetaCW, pt.ThetaKind = rt.ThetaJbCW, "θJB"
			case hasR && rt.ThetaJcCW > 0:
				pt.ThetaCW, pt.ThetaKind = rt.ThetaJcCW, "θJC"
			}
			if pt.ThetaCW > 0 {
				pt.TjC = round(tmax+pw*pt.ThetaCW, 2)
				pt.TjMaxC = rt.TjMaxC
			}
			old := partBest[p.Ref]
			if old == nil || junctionOrBoard(pt) > junctionOrBoard(old) {
				partBest[p.Ref] = pt
			}
		}
	}
	tr.MaxBoardC = round(o.AmbientC+bestMax, 2)
	// Maps + layer stats of the hottest scenario.
	res.TempMap = make([][]float64, nl)
	for k := 0; k < nl; k++ {
		m := make([]float64, nc)
		lt := LayerTemp{Layer: st.Layers[k].Name, MaxC: math.Inf(-1)}
		sum, n := 0.0, 0
		for c := 0; c < nc; c++ {
			m[c] = math.NaN()
			if i := tm.idx[k*nc+c]; i >= 0 {
				t := o.AmbientC + hot[i]
				m[c] = t
				sum += t
				n++
				if t > lt.MaxC {
					p := g.cellCentre(c)
					lt.MaxC, lt.X, lt.Y = t, round(p.X, 1), round(p.Y, 1)
				}
			}
		}
		lt.MaxC = round(lt.MaxC, 2)
		if n > 0 {
			lt.MeanC = round(sum/float64(n), 2)
		}
		res.TempMap[k] = m
		tr.Layers = append(tr.Layers, lt)
		if lt.MaxC >= tr.MaxBoardC-1e-9 && tr.MaxAt.Layer == "" {
			tr.MaxAt = Hotspot{Layer: lt.Layer, X: lt.X, Y: lt.Y, Value: lt.MaxC, Kind: "cell"}
		}
	}
	// Which part sits on the hottest spot.
	for _, p := range b.Parts {
		if p.BBox.valid() && tr.MaxAt.X >= p.BBox.MinX && tr.MaxAt.X <= p.BBox.MaxX && tr.MaxAt.Y >= p.BBox.MinY && tr.MaxAt.Y <= p.BBox.MaxY {
			tr.MaxAt.What = p.Ref
			break
		}
	}
	for _, pt := range partBest {
		if pt.PowerW <= 0 && pt.BoardMaxC < o.AmbientC+0.5*(tr.MaxBoardC-o.AmbientC) {
			continue // unpowered and cool: not interesting
		}
		switch {
		case pt.ThetaCW > 0 && pt.TjMaxC > 0:
			pt.TjPct = round(100*pt.TjC/pt.TjMaxC, 1)
			pt.Status = "ok"
			if pt.TjC > pt.TjMaxC {
				pt.Status = "fail"
			} else if pt.TjC > o.TjWarnPct/100*pt.TjMaxC {
				pt.Status = "warn"
			}
		case pt.ThetaCW > 0:
			pt.Status = "ok"
			pt.Note = "Tj,max not in the model ratings (needs datasheet)"
		case pt.PowerW > 0:
			pt.Status = "needs-datasheet"
			pt.Note = "no θJB/θJC in power-models ratings: board temperature only"
		default:
			pt.Status = "ok"
			pt.Note = "no dissipation: heated by neighbours"
		}
		tr.Parts = append(tr.Parts, *pt)
	}
	sort.Slice(tr.Parts, func(i, j int) bool {
		if tr.Parts[i].BoardMaxC != tr.Parts[j].BoardMaxC {
			return tr.Parts[i].BoardMaxC > tr.Parts[j].BoardMaxC
		}
		return tr.Parts[i].Ref < tr.Parts[j].Ref
	})
	return tr
}

func junctionOrBoard(p *PartThermal) float64 {
	if p.TjC > 0 {
		return p.TjC
	}
	return p.BoardMaxC
}

func modelOf(r *powersim.Result, ref string) string {
	if pr := r.Parts[ref]; pr != nil {
		return pr.ModelID
	}
	return ""
}

func (res *Result) findings(o Options) {
	add := func(sev, kind, msg string, refs, nets []string) {
		res.Findings = append(res.Findings, Finding{Severity: sev, Kind: kind, Message: msg, Refs: refs, Nets: nets})
	}
	for _, n := range res.Nets {
		switch n.Status {
		case "over-budget":
			add("fail", "ir-drop", fmt.Sprintf("%s drops %.2f mV at %s (%s) — over the %.1f mV budget", n.Net, n.WorstMV, n.WorstPad, n.Scenario, n.BudgetMV), nil, []string{n.Net})
		case "open":
			var pads []string
			for _, p := range n.Pads {
				if p.DropMV < 0 && p.CurrentA > 0 {
					pads = append(pads, p.Pad)
				}
			}
			add("fail", "open", fmt.Sprintf("%s: no copper path from %s to %s", n.Net, n.Reference, strings.Join(pads, ", ")), nil, []string{n.Net})
		case "ok":
			if n.BudgetMV > 0 && n.WorstMV > 0.8*n.BudgetMV {
				add("warn", "ir-drop", fmt.Sprintf("%s drop %.2f mV is %.0f %% of the %.1f mV budget", n.Net, n.WorstMV, 100*n.WorstMV/n.BudgetMV, n.BudgetMV), nil, []string{n.Net})
			}
		case "no-reference":
			add("warn", "no-reference", fmt.Sprintf("%s: the supplying part has no pad of this net on the board", n.Net), nil, []string{n.Net})
		}
		for _, s := range n.Notes {
			if strings.HasPrefix(s, "track ") {
				add("warn", "track-current", n.Net+": "+s, nil, []string{n.Net})
			}
		}
	}
	for _, v := range res.Vias {
		switch {
		case v.CurrentA > v.AmpacityA:
			add("fail", "via-current", fmt.Sprintf("via %s (%s, %.1f mil drill) at (%.0f,%.0f) carries %.3f A > IPC ampacity %.2f A (ΔT %.0f °C)", v.ID, v.Net, v.DrillMil, v.X, v.Y, v.CurrentA, v.AmpacityA, o.ViaDeltaTC), nil, []string{v.Net})
		case v.CurrentA > 0.8*v.AmpacityA:
			add("warn", "via-current", fmt.Sprintf("via %s (%s) at (%.0f,%.0f) carries %.3f A = %.0f %% of its %.2f A ampacity", v.ID, v.Net, v.X, v.Y, v.CurrentA, v.UsePct, v.AmpacityA), nil, []string{v.Net})
		}
	}
	if t := res.Thermal; t != nil {
		switch {
		case t.MaxBoardC > o.BoardFailC:
			add("fail", "board-temperature", fmt.Sprintf("board reaches %.1f °C (%s, %s) > %.0f °C (FR-4 Tg)", t.MaxBoardC, t.MaxAt.Layer, t.Scenario, o.BoardFailC), nil, nil)
		case t.MaxBoardC > o.BoardWarnC:
			add("warn", "board-temperature", fmt.Sprintf("board reaches %.1f °C (%s, %s) > %.0f °C (FR-4 Tg margin)", t.MaxBoardC, t.MaxAt.Layer, t.Scenario, o.BoardWarnC), nil, nil)
		}
		if math.Abs(t.BalanceErrPct) > 1 {
			add("warn", "energy-balance", fmt.Sprintf("thermal energy balance off by %.2f %%", t.BalanceErrPct), nil, nil)
		}
		for _, p := range t.Parts {
			switch p.Status {
			case "fail":
				add("fail", "junction", fmt.Sprintf("%s Tj ≈ %.1f °C > Tj,max %.0f °C (%s, board %.1f °C + %.3g W × %s %.0f °C/W)", p.Ref, p.TjC, p.TjMaxC, p.Scenario, p.BoardMaxC, p.PowerW, p.ThetaKind, p.ThetaCW), []string{p.Ref}, nil)
			case "warn":
				add("warn", "junction", fmt.Sprintf("%s Tj ≈ %.1f °C = %.0f %% of Tj,max %.0f °C", p.Ref, p.TjC, p.TjPct, p.TjMaxC), []string{p.Ref}, nil)
			case "needs-datasheet":
				add("info", "needs-datasheet", fmt.Sprintf("%s (%.3g W, board %.1f °C): no θJB/θJC rating — junction not estimated", p.Ref, p.PowerW, p.BoardMaxC), []string{p.Ref}, nil)
			}
		}
	}
	v := Verdict{Status: "pass"}
	for _, f := range res.Findings {
		switch f.Severity {
		case "fail":
			v.Status = "fail"
			v.Reasons = append(v.Reasons, f.Message)
		case "warn":
			if v.Status == "pass" {
				v.Status = "warn"
			}
			v.Reasons = append(v.Reasons, f.Message)
		}
	}
	res.Verdict = v
}

func modelNotes(o Options, st *Stackup) []string {
	var cu []string
	for _, l := range st.Layers {
		cu = append(cu, fmt.Sprintf("%s %.1f µm @ z %.3f mm", l.Name, l.CuMm*1000, l.ZMm))
	}
	return []string{
		"copper from the board dump: tracks/arcs = 1-D resistors ρL/(w·t) split at junctions/vias/pads and at every cell; poured fills, static fills, planes = sheet cells G = (t/ρ)·min(harmonic coverage, shared-edge copper fraction); pads shorted to the sheet they overlap",
		fmt.Sprintf("ρ = %.3g Ω·m (20 °C); via barrel R = ρ·h/(π(d+t)t), plating t = %.2g mil, h = layer-centre distance", RhoCu, o.PlatingMil),
		"stackup: " + strings.Join(cu, "; ") + fmt.Sprintf("; dielectrics %v mm (%s)", roundAll(st.DielMm, 4), st.Source),
		"each scenario of the sim file is solved on its own (KCL-consistent): the supplying part (power: source pins; ground: return entry, connector-source first) is the 0 V reference, every other pad draws (+I) or feeds (−I) its simulated current; worst = maximum over scenarios",
		fmt.Sprintf("via ampacity = IPC-2221 external curve on the barrel cross-section π(d+t)t at ΔT %.0f °C (IPC-2152: internal ≈ external)", o.ViaDeltaTC),
		fmt.Sprintf("thermal: per layer k_Cu 385 W/mK × t × coverage + FR-4 %.2g W/mK in-plane over half of each adjacent dielectric; FR-4 %.2g W/mK through-plane + via barrels; convection top/bottom; part heat on its pads by area; Joule heat from the DC solve; board edges adiabatic; part bodies do not convect separately (their top face shares the board's h)", o.KFR4XY, o.KFR4Z),
		"Tj = board temperature under the part + P·θJB (θJC when only that is rated); without a rating only the board temperature is reported",
	}
}

func sortedKeys(m map[int]string) []int {
	var out []int
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func round(v float64, d int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	p := math.Pow(10, float64(d))
	return math.Round(v*p) / p
}

func roundAll(v []float64, d int) []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = round(x, d)
	}
	return out
}

func rp(p Point) Point { return Point{round(p.X, 2), round(p.Y, 2)} }

// Compare reads pcb auto's plan.json (result.route.power) and lines its
// IR-drop estimate up with the post-layout result.
func (res *Result) CompareWithPlan(planRaw []byte) error {
	var d struct {
		Result struct {
			Route struct {
				Power *struct {
					Nets []struct {
						Net      string  `json:"net"`
						WorstMV  float64 `json:"worstMV"`
						WorstPad string  `json:"worstPad"`
					} `json:"nets"`
				} `json:"power"`
			} `json:"route"`
		} `json:"result"`
	}
	if err := json.Unmarshal(planRaw, &d); err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if d.Result.Route.Power == nil {
		return fmt.Errorf("plan: no result.route.power (pcb auto run without --sim)")
	}
	post := map[string]*NetResult{}
	for _, n := range res.Nets {
		post[n.Net] = n
	}
	res.Compare = nil
	for _, a := range d.Result.Route.Power.Nets {
		p := post[a.Net]
		if p == nil {
			continue
		}
		res.Compare = append(res.Compare, CompareRow{Net: a.Net, AutoMV: round(a.WorstMV, 3), AutoPad: a.WorstPad,
			PostMV: p.WorstMV, PostPad: p.WorstPad, DeltaMV: round(p.WorstMV-a.WorstMV, 3), Scenario: p.Scenario})
	}
	return nil
}
