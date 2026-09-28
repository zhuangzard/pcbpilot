package designreport

import (
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// treeInfo is the power tree recovered from part kinds and pin directions.
type treeInfo struct {
	producers map[string][]string // net → producing parts
	input     map[string]string   // ref → input rail
	output    map[string]string   // ref → output rail
	col       map[string]int
	kind      map[string]string // ref → source | or-diode | regulator | load
	order     []string
}

func isRegulator(kind string) bool { return kind == powersim.KindBuck || kind == powersim.KindLDO }

func (c *ctx) railLike(net string) bool {
	en := c.elec[net]
	return en != nil && (en.Role == "power" || en.Role == "switch") && !c.isGround(net)
}

// sourcePinNet is the non-ground net a part's current leaves from.
func (c *ctx) pinsDir(ref, dir string) []string {
	var out []string
	for _, net := range sortedKeys(c.elec) {
		if c.isGround(net) {
			continue
		}
		for _, p := range c.elec[net].Pins {
			if p.Ref == ref && p.Dir == dir {
				out = append(out, net)
				break
			}
		}
	}
	return out
}

func (c *ctx) buildTree() *treeInfo {
	t := &treeInfo{producers: map[string][]string{}, input: map[string]string{}, output: map[string]string{}, col: map[string]int{}, kind: map[string]string{}}
	if c.src == "" {
		return t
	}
	for _, ref := range c.refs {
		p := c.parts[ref]
		switch {
		case p.Kind == powersim.KindSource:
			for _, n := range p.nets() {
				if c.railLike(n) {
					t.output[ref], t.kind[ref] = n, "source"
					break
				}
			}
		case p.Kind == powersim.KindDiode:
			a := p.pinNet("A", "ANODE", "+")
			k := p.pinNet("K", "CATHODE", "-")
			if a == "" || k == "" || !c.railLike(a) || !c.railLike(k) {
				continue
			}
			t.input[ref], t.output[ref], t.kind[ref] = a, k, "or-diode"
		case isRegulator(p.Kind):
			in := p.pinNet("IN", "VIN", "INPUT", "VCC", "PVIN")
			if in == "" {
				if s := c.pinsDir(ref, "sink"); len(s) > 0 {
					in = s[0]
				}
			}
			out := p.pinNet("OUT", "VOUT", "OUTPUT")
			if out == "" {
				for _, n := range c.pinsDir(ref, "source") {
					if c.elec[n].Role == "switch" {
						out = c.throughInductor(n)
					} else {
						out = n
					}
				}
			}
			if in == "" || out == "" {
				continue
			}
			t.input[ref], t.output[ref], t.kind[ref] = in, out, "regulator"
		case p.Kind == powersim.KindLoad || p.Kind == powersim.KindICSmall:
			for _, n := range c.pinsDir(ref, "sink") {
				if c.elec[n].Role == "power" {
					t.input[ref], t.kind[ref] = n, "load"
					break
				}
			}
		}
	}
	for _, ref := range sortedKeys(t.output) {
		t.producers[t.output[ref]] = append(t.producers[t.output[ref]], ref)
	}
	for net := range t.producers {
		sort.Slice(t.producers[net], func(i, j int) bool { return natLess(t.producers[net][i], t.producers[net][j]) })
	}
	// Columns: sources 0, converters one right of their input's producer.
	for pass := 0; pass < 8; pass++ {
		for _, ref := range sortedKeys(t.kind) {
			switch t.kind[ref] {
			case "source":
				t.col[ref] = 0
			case "or-diode", "regulator":
				col := 1
				for _, pr := range t.producers[t.input[ref]] {
					if t.col[pr]+1 > col {
						col = t.col[pr] + 1
					}
				}
				t.col[ref] = col
			}
		}
	}
	maxCol := 0
	for _, v := range t.col {
		if v > maxCol {
			maxCol = v
		}
	}
	for ref, k := range t.kind {
		if k == "load" {
			t.col[ref] = maxCol + 1
		}
	}
	for _, ref := range c.refs {
		if _, ok := t.kind[ref]; ok {
			t.order = append(t.order, ref)
		}
	}
	return t
}

// railOrder sorts power rails upstream → downstream (column of the rail's
// producer in the power tree, then name).
func (c *ctx) railOrder(nets []string) []string {
	depth := func(n string) int {
		d := 99
		for _, pr := range c.tree.producers[n] {
			if c.tree.col[pr] < d {
				d = c.tree.col[pr]
			}
		}
		return d
	}
	out := append([]string(nil), nets...)
	sort.SliceStable(out, func(i, j int) bool {
		if di, dj := depth(out[i]), depth(out[j]); di != dj {
			return di < dj
		}
		return out[i] < out[j]
	})
	return out
}

// throughInductor follows a switch node through its inductor to the rail.
func (c *ctx) throughInductor(sw string) string {
	for _, ref := range c.refs {
		p := c.parts[ref]
		if p.Kind != powersim.KindInductor {
			continue
		}
		ns := p.nets()
		if len(ns) == 2 {
			if ns[0] == sw {
				return ns[1]
			}
			if ns[1] == sw {
				return ns[0]
			}
		}
	}
	return ""
}

func (c *ctx) buildRequirements() {
	it := c.in.Intent
	if it == nil {
		c.missing("2 需求与设计意图", "intent.json 未提供（pcbpilot intent derive）")
		return
	}
	s := it.Standard
	req := &RequirementsSection{Pairs: len(it.Pairs)}
	defaulted := map[string]bool{}
	for _, d := range s.Defaulted {
		defaulted[d] = true
	}
	note := func(k string) string {
		if defaulted[k] {
			return "工程默认值（规格书未声明）"
		}
		return "规格书声明"
	}
	req.Standard = []KV{
		{"安全标准 Standard", s.Name, note("name")},
		{"绝缘等级 Insulation", s.Insulation, note("insulation")},
		{"污染等级 Pollution degree", sprintf("PD%d", s.PollutionDegree), note("pollutionDegree")},
		{"材料组 Material group", s.MaterialGroup, note("materialGroup")},
		{"海拔 Altitude", sprintf("%g m", s.AltitudeM), note("altitudeM")},
		{"过电压类别 OVC", s.OvervoltageCategory, note("overvoltageCategory")},
		{"三防涂覆 Coated", map[bool]string{true: "是", false: "否"}[s.Coated], note("coated")},
	}
	if s.MOP != "" {
		req.Standard = append(req.Standard, KV{"防护措施 MOP", sprintf("%d× %s", s.MOPCount, s.MOP), note("mop")})
	}
	if cu := it.Copper; cu != nil {
		req.Copper = []KV{
			{"层数 Layers", sprintf("%d", cu.Layers), ""},
			{"铜厚 外/内", sprintf("%g oz / %g oz", cu.OuterOz, cu.InnerOz), ""},
			{"温升 ΔT", sprintf("%g °C", cu.TempRiseC), "IPC-2221/2152 线宽计算的温升"},
			{"叠层 Stackup", cu.Stackup, ""},
			{"参考高度 h / εr", sprintf("%g mil / %g", cu.RefHeightMil, cu.Er), "阻抗计算用"},
			{"工艺最小线宽/间距", sprintf("%g / %g mil", cu.MinTrackMil, cu.ClearanceMil), ""},
			{"过孔 钻孔/外径", sprintf("%g / %g mil", cu.ViaDrillMil, cu.ViaDiaMil), ""},
		}
	}
	for _, b := range it.Blocks {
		fn := b.Function
		if b.SubFunction != "" {
			fn += " / " + b.SubFunction
		}
		req.Blocks = append(req.Blocks, BlockRow{ID: b.ID, Function: fn, Core: b.Core, Parts: strings.Join(b.Parts, ", "), Summary: b.Summary, PowerW: b.PowerW})
	}
	for _, d := range it.Domains {
		req.Domains = append(req.Domains, DomainRow{ID: d.ID, Kind: d.Kind, Reference: d.Reference, WorkingVrms: d.WorkingVrms, WorkingVpeak: d.WorkingVpeak, Nets: len(d.Nets), Parts: len(d.Parts)})
	}
	for _, f := range it.Findings {
		msg := f.Message
		if len(f.Refs) > 0 {
			msg = "[" + strings.Join(f.Refs, ",") + "] " + msg
		}
		req.Findings = append(req.Findings, Risk{Severity: f.Severity, Source: "intent:" + f.Kind, Message: msg, Suggestion: f.Suggestion})
	}
	c.rep.Requirements = req
}

func (c *ctx) buildPower() {
	s := c.in.Sim
	if s == nil || c.worst == nil {
		c.missing("3 电源仿真", "sim.json 未提供（pcbpilot sim power / intent derive --sim-out）")
		return
	}
	w := c.worst
	ps := &PowerSection{Converged: w.Converged, Confidence: map[string]int{}, Assumptions: w.Assumptions, Warnings: w.Warnings}
	for _, r := range c.scen {
		ps.Scenarios = append(ps.Scenarios, r.Scenario)
	}
	// Rails.
	for _, net := range sortedKeys(w.Nets) {
		nr := w.Nets[net]
		if nr.Role != "power" || nr.Floating {
			continue
		}
		en := c.elec[net]
		row := RailRow{Net: net, Role: nr.Role, VNom: en.VNom, VMin: en.VMin, VMax: en.VMax, IMaxA: nr.CurrentA, Worst: nr.Scenario}
		for _, r := range c.scen {
			v, i := 0.0, 0.0
			if x := r.Nets[net]; x != nil {
				v, i = x.Voltage, x.CurrentA
			}
			row.Voltages = append(row.Voltages, round(v, 4))
			row.Currents = append(row.Currents, round(i, 6))
		}
		ps.Rails = append(ps.Rails, row)
		ps.RailPower = append(ps.RailPower, RailPowerRow{Net: net, V: nr.Voltage, IA: nr.CurrentA, PowerW: round(nr.Voltage*nr.CurrentA, 6)})
	}
	sort.SliceStable(ps.RailPower, func(i, j int) bool { return ps.RailPower[i].PowerW > ps.RailPower[j].PowerW })
	// Scenario power balance.
	for _, r := range c.scen {
		sp := ScenarioPower{Scenario: r.Scenario}
		for _, ref := range sortedKeys(r.Parts) {
			pr := r.Parts[ref]
			switch pr.Model {
			case powersim.KindSource:
				sp.SuppliedW += pr.SuppliedW
			case powersim.KindLoad, powersim.KindICSmall, powersim.KindLED:
				sp.LoadW += pr.PowerW
			default:
				if pr.PowerW > 0 {
					sp.LossW += pr.PowerW
				}
			}
		}
		sp.SuppliedW, sp.LoadW, sp.LossW = round(sp.SuppliedW, 5), round(sp.LoadW, 5), round(sp.LossW, 5)
		ps.ScenarioPwr = append(ps.ScenarioPwr, sp)
	}
	for _, ref := range c.refs {
		pr := w.Parts[ref]
		if pr == nil || pr.Model == powersim.KindSource || math.Abs(pr.PowerW) < 1e-5 {
			continue
		}
		ps.PartPower = append(ps.PartPower, PartPowerRow{Ref: ref, Device: c.device(ref), Kind: pr.Model, PowerW: pr.PowerW, Scenario: pr.Scenario})
	}
	sort.SliceStable(ps.PartPower, func(i, j int) bool { return ps.PartPower[i].PowerW > ps.PartPower[j].PowerW })
	// Tree.
	t := c.tree
	for _, ref := range t.order {
		n := TreeNode{Ref: ref, Kind: t.kind[ref], Col: t.col[ref], Label: ref}
		if d := c.device(ref); d != "" {
			n.Label = ref + " " + d
		}
		pr := w.Parts[ref]
		switch n.Kind {
		case "source":
			if en := c.elec[t.output[ref]]; en != nil && pr != nil {
				n.Detail = sprintf("%s %s · %s", t.output[ref], fV(en.VMax), fW(pr.SuppliedW))
			}
		case "or-diode":
			i := c.partCurrent(ref)
			if pr != nil && i > 0 {
				n.Detail = sprintf("If %s · Vf≈%s · %s", fA(i), fV(pr.PowerW/i), fW(pr.PowerW))
			}
		case "regulator":
			if pr != nil {
				n.Detail = sprintf("%s→%s · Iout %s · η %s · loss %s", fV(pr.VinV), fV(pr.VoutV), fA(pr.OutputA), fPct(pr.Efficiency*100), fW(pr.PowerW))
			}
		case "load":
			if pr != nil {
				n.Detail = sprintf("%s · %s", fA(c.partCurrent(ref)), fW(pr.PowerW))
			}
		}
		ps.Tree.Nodes = append(ps.Tree.Nodes, n)
	}
	for _, ref := range t.order {
		in := t.input[ref]
		if in == "" {
			continue
		}
		for _, pr := range t.producers[in] {
			en := c.elec[in]
			label := in
			if en != nil {
				label = sprintf("%s %s / %s", in, fV(en.VNom), fA(en.IA))
			}
			ps.Tree.Edges = append(ps.Tree.Edges, TreeEdge{From: pr, To: ref, Net: in, Label: label})
		}
	}
	// Regulators per scenario.
	for _, ref := range c.refs {
		p := c.parts[ref]
		if !isRegulator(p.Kind) {
			continue
		}
		for _, r := range c.scen {
			pr := r.Parts[ref]
			if pr == nil {
				continue
			}
			row := RegulatorRow{Ref: ref, Device: p.Device, Kind: p.Kind, Scenario: r.Scenario, Mode: pr.Mode, VinV: pr.VinV, VoutV: pr.VoutV,
				IoutA: pr.OutputA, IinA: pr.InputA, Eff: pr.Efficiency, LossW: pr.PowerW}
			for _, re := range r.Ripple {
				if re.Regulator == ref && re.Duty > 0 {
					row.Duty = re.Duty
				}
			}
			if row.Duty == 0 && p.Kind == powersim.KindBuck && pr.VinV > 0 && pr.Efficiency > 0 {
				row.Duty = round(math.Min(1, pr.VoutV/(pr.Efficiency*pr.VinV)), 4)
			}
			if r.Scenario == "peak" || len(c.scen) == 1 {
				row.Notes = pr.Notes
			}
			ps.Regulators = append(ps.Regulators, row)
		}
	}
	for _, key := range sortedKeys(w.Ripple) {
		re := w.Ripple[key]
		kind := "switch-node"
		if p := c.parts[key]; p != nil {
			kind = p.Kind
		}
		ps.Ripple = append(ps.Ripple, RippleRow{Key: key, Kind: kind, Regulator: re.Regulator, IPeakA: re.IPeakA, IRmsA: re.IRmsA, IAvgA: re.IAvgA, DeltaIA: re.DeltaIA, Duty: re.Duty, Scenario: re.Scenario})
	}
	for _, m := range s.Models {
		ps.Models = append(ps.Models, ModelRow{Ref: m.Ref, Device: c.device(m.Ref), Kind: m.Kind, ModelID: m.ModelID, Match: m.Match, Confidence: m.Confidence, Source: m.Source, Assumed: m.Confidence == "assumed"})
		ps.Confidence[m.Confidence]++
	}
	sort.SliceStable(ps.Models, func(i, j int) bool { return natLess(ps.Models[i].Ref, ps.Models[j].Ref) })
	c.rep.Power = ps
}
