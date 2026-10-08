package boardmanual

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/analogsim"
	"github.com/zhuangzard/pcbpilot/pkg/postsim"
)

// HeatMap is one embedded heat-map picture.
type HeatMap struct {
	Layer, Kind, DataURI string
}

// SimSection is "仿真结论 / 工作条件": post-layout thermal, IR drop, via
// current, power balance and analog results with one verdict line each.
type SimSection struct {
	HasPost, HasAnalog bool
	PostBoardSHA       string
	PostFileSHA        string
	PostVerdict        string
	PostReasons        []string
	Verdicts           []SimVerdict

	// thermal
	AmbientC      float64
	Boundary      string
	ThermalBasis  string
	Scenarios     []ScenarioTemp
	HotC          float64
	HotWhere      string
	Layers        []postsim.LayerTemp
	TopParts      []PartTemp
	CopperRiseC   float64
	AllowedAmb    string
	MissingLimits []string
	Maps          []HeatMap

	// IR drop
	IR []IRRow
	// vias
	ViaCount int
	Vias     []ViaUse
	// power
	Power []PowerRow
	// analog
	Analog       []AnalogRow
	AnalogStatus string
}

// SimVerdict is one item's one-line conclusion.
type SimVerdict struct {
	Item, Status, Text string
}

// ScenarioTemp is the hottest board temperature of one scenario.
type ScenarioTemp struct {
	Scenario      string
	MaxC          float64
	PartsW, LossW float64
}

// PartTemp is one part's board temperature.
type PartTemp struct {
	Ref, Device, Side, Status string
	PowerW, BoardMaxC, TjC    float64
}

// IRRow is one rail's IR drop.
type IRRow struct {
	Net, Scenario, WorstPad, Status string
	CurrentA, WorstMV, BudgetMV     float64
	UsePct                          float64
	Valve                           bool
}

// ViaUse is one via's current utilisation.
type ViaUse struct {
	Net                       string
	XMM, YMM                  float64
	CurrentA, AmpacityA, Used float64
}

// PowerRow is one scenario's power balance (W).
type PowerRow struct {
	Scenario                    string
	InputW, LoadW, LossW, Other float64
	InputA                      float64
}

// AnalogRow is one analog block with its key metrics vs target.
type AnalogRow struct {
	ID, Title, Class, Status string
	Metrics                  []AnalogMetric
}

// AnalogMetric is one metric value vs target.
type AnalogMetric struct {
	Label, Value, Target, Status string
}

func (c *ctx) buildSim() {
	s := &c.m.Sim
	if p := c.in.Post; p != nil {
		s.HasPost = true
		s.PostBoardSHA = firstNonEmpty(p.Inputs.BoardSemantic, p.Inputs.BoardSHA256)
		s.PostFileSHA = p.Inputs.BoardSHA256
		s.PostVerdict, s.PostReasons = p.Verdict.Status, p.Verdict.Reasons
		c.simThermal(p)
		c.simIR(p)
		c.simVias(p)
	}
	c.simPower()
	if a := c.in.Analog; a != nil {
		s.HasAnalog = true
		c.simAnalog(a)
	}
}

func (c *ctx) verdict(item, status, f string, a ...any) {
	c.m.Sim.Verdicts = append(c.m.Sim.Verdicts, SimVerdict{Item: item, Status: status, Text: fmt.Sprintf(f, a...)})
}

func (c *ctx) simThermal(p *postsim.Result) {
	s := &c.m.Sim
	s.AmbientC = p.Settings.AmbientC
	for _, a := range p.Assumptions {
		switch {
		case strings.HasPrefix(a, "boundary:"):
			s.Boundary = strings.TrimSpace(strings.TrimPrefix(a, "boundary:"))
		case strings.HasPrefix(a, "thermal:"):
			s.ThermalBasis = strings.TrimSpace(strings.TrimPrefix(a, "thermal:"))
		}
	}
	t := p.Thermal
	if t == nil {
		c.verdict(c.t("simThermal"), "N/A", "%s", c.t("simNoThermal"))
		return
	}
	for _, ps := range t.PerScenario {
		s.Scenarios = append(s.Scenarios, ScenarioTemp{Scenario: ps.Scenario, MaxC: ps.MaxC, PartsW: ps.PartsW, LossW: ps.LossW})
	}
	s.HotC = t.MaxBoardC
	x, y := c.mm(t.MaxAt.X, t.MaxAt.Y)
	s.HotWhere = fmt.Sprintf("%s (%s, %s) mm", t.MaxAt.Layer, trimNum(x), trimNum(y))
	if t.MaxAt.What != "" {
		s.HotWhere += " · " + t.MaxAt.What
	}
	if t.Scenario != "" {
		s.HotWhere += " · " + t.Scenario
	}
	s.Layers = t.Layers
	s.CopperRiseC = t.MaxCopperRiseC
	parts := append([]postsim.PartThermal(nil), t.Parts...)
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].BoardMaxC > parts[j].BoardMaxC })
	for i, pt := range parts {
		if i == 10 {
			break
		}
		s.TopParts = append(s.TopParts, PartTemp{Ref: pt.Ref, Device: pt.Device, Side: pt.Side, Status: pt.Status, PowerW: pt.PowerW, BoardMaxC: pt.BoardMaxC, TjC: pt.TjC})
	}
	// allowed ambient: the rise is assumed independent of the ambient
	// (losses do not grow with temperature), so Ta,max = Ta + (limit − T).
	limit := p.Settings.BoardLimitC
	best, by := math.Inf(1), ""
	if limit > 0 {
		best, by = s.AmbientC+limit-t.MaxBoardC, fmt.Sprintf(c.t("ambByBoard"), trimNum(limit))
	}
	for _, pt := range t.Parts {
		switch {
		case pt.TjMaxC > 0 && pt.TjC > 0:
			if v := s.AmbientC + pt.TjMaxC - pt.TjC; v < best {
				best, by = v, fmt.Sprintf(c.t("ambByPart"), pt.Ref, trimNum(pt.TjMaxC))
			}
		case pt.PowerW > 0.01:
			s.MissingLimits = append(s.MissingLimits, pt.Ref)
		}
	}
	sortNat(s.MissingLimits)
	if !math.IsInf(best, 1) {
		s.AllowedAmb = fmt.Sprintf("≤ %.0f °C（%s）", math.Floor(best), by)
	}
	status := "PASS"
	if limit > 0 && t.MaxBoardC > limit {
		status = "FAIL"
	}
	txt := fmt.Sprintf(c.t("vThermal"), trimNum(t.MaxBoardC), s.HotWhere, trimNum(s.AmbientC))
	if s.AllowedAmb != "" {
		txt += "；" + c.t("allowedAmb") + " " + s.AllowedAmb
	}
	if len(s.MissingLimits) > 0 {
		txt += "；" + fmt.Sprintf(c.t("ambMissing"), strings.Join(s.MissingLimits, ", "))
	}
	c.verdict(c.t("simThermal"), status, "%s", txt)
}

func (c *ctx) simIR(p *postsim.Result) {
	s := &c.m.Sim
	worst, worstNet, fails := 0.0, "", 0
	for _, n := range p.Nets {
		if n == nil || n.BudgetMV <= 0 {
			continue
		}
		r := IRRow{Net: n.Net, Scenario: n.Scenario, WorstPad: n.WorstPad, Status: n.Status, CurrentA: n.CurrentA, WorstMV: n.WorstMV, BudgetMV: n.BudgetMV,
			UsePct: math.Round(n.WorstMV/n.BudgetMV*1000) / 10, Valve: strings.HasSuffix(n.Net, "_DRV")}
		if r.Status != "ok" {
			fails++
		}
		if r.UsePct > worst {
			worst, worstNet = r.UsePct, r.Net
		}
		s.IR = append(s.IR, r)
	}
	sort.SliceStable(s.IR, func(i, j int) bool {
		if s.IR[i].Valve != s.IR[j].Valve {
			return !s.IR[i].Valve
		}
		return natLess(s.IR[i].Net, s.IR[j].Net)
	})
	status := "PASS"
	if fails > 0 {
		status = "FAIL"
	}
	c.verdict(c.t("simIR"), status, c.t("vIR"), len(s.IR), fails, worstNet, trimNum(worst))
}

func (c *ctx) simVias(p *postsim.Result) {
	s := &c.m.Sim
	vs := append([]postsim.ViaResult(nil), p.Vias...)
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].UsePct > vs[j].UsePct })
	s.ViaCount = len(vs)
	over := 0
	for _, v := range vs {
		if v.UsePct > 100 {
			over++
		}
	}
	for i, v := range vs {
		if i == 5 {
			break
		}
		x, y := c.mm(v.X, v.Y)
		s.Vias = append(s.Vias, ViaUse{Net: v.Net, XMM: x, YMM: y, CurrentA: v.CurrentA, AmpacityA: v.AmpacityA, Used: v.UsePct})
	}
	status, top := "PASS", 0.0
	if len(s.Vias) > 0 {
		top = s.Vias[0].Used
	}
	if over > 0 {
		status = "FAIL"
	}
	c.verdict(c.t("simVia"), status, c.t("vVia"), s.ViaCount, trimNum(top), over)
}

func (c *ctx) simPower() {
	s := &c.m.Sim
	if c.in.Sim == nil {
		return
	}
	ref := c.m.Power.InputRef
	for _, r := range c.in.Sim.Results {
		if r.Scenario == "worst" {
			continue // per-pin maxima of different scenarios: no power balance
		}
		row := PowerRow{Scenario: r.Scenario}
		if p := c.in.Board.Part(ref); p != nil {
			for _, pd := range p.Pads {
				if a, d := c.pinCurrentIn(&r, ref, pd.PadNumber); d == "source" && r.Nets[pd.Net] != nil {
					row.InputA += a
					row.InputW += a * r.Nets[pd.Net].Voltage
				}
			}
		}
		for _, pr := range r.Parts {
			if pr == nil {
				continue
			}
			if pr.OffBoard {
				row.LoadW += pr.PowerW
			} else if pr.PowerW > 0 {
				row.LossW += pr.PowerW
			}
		}
		row.Other = row.InputW - row.LoadW - row.LossW
		s.Power = append(s.Power, row)
	}
	if len(s.Power) == 0 {
		return
	}
	var parts []string
	for _, r := range s.Power {
		parts = append(parts, fmt.Sprintf(c.t("vPowerScen"), r.Scenario, trimNum(round2(r.InputW)), trimNum(round2(r.LoadW)), trimNum(round2(r.LossW))))
	}
	txt := strings.Join(parts, "；")
	if c.m.Power.Recommend != "" {
		txt += "；" + c.t("recSupply") + " " + c.m.Power.Recommend
	}
	c.verdict(c.t("simPower"), "INFO", "%s", txt)
}

func fmtMetric(v float64, unit string) string {
	s := fmt.Sprintf("%.4g", v)
	if unit != "" && unit != "V/V" {
		s += " " + unit
	}
	return s
}

func targetText(t *analogsim.Target, unit string) string {
	if t == nil {
		return "—"
	}
	switch {
	case t.Value != nil && t.TolPct > 0:
		return fmt.Sprintf("%s ± %s %%", fmtMetric(*t.Value, unit), trimNum(t.TolPct))
	case t.Value != nil:
		return fmtMetric(*t.Value, unit)
	case t.Min != nil && t.Max != nil:
		return fmt.Sprintf("%s … %s", fmtMetric(*t.Min, unit), fmtMetric(*t.Max, unit))
	case t.Min != nil:
		return "≥ " + fmtMetric(*t.Min, unit)
	case t.Max != nil:
		return "≤ " + fmtMetric(*t.Max, unit)
	}
	return "—"
}

func (c *ctx) simAnalog(a *analogsim.Output) {
	s := &c.m.Sim
	s.AnalogStatus = a.Summary.Status
	for _, b := range a.Blocks {
		if b == nil {
			continue
		}
		row := AnalogRow{ID: b.ID, Title: b.Title, Class: b.Class, Status: "PASS"}
		for _, mt := range b.Metrics {
			if mt.Target == nil && mt.Status == "" {
				continue
			}
			row.Metrics = append(row.Metrics, AnalogMetric{Label: firstNonEmpty(mt.Label, mt.Name), Value: fmtMetric(mt.Value, mt.Unit), Target: targetText(mt.Target, mt.Unit), Status: mt.Status})
			if mt.Status == "FAIL" {
				row.Status = "FAIL"
			} else if mt.Status == "WARN" && row.Status != "FAIL" {
				row.Status = "WARN"
			}
		}
		if len(row.Metrics) == 0 {
			row.Status = "INFO"
		}
		s.Analog = append(s.Analog, row)
	}
	c.verdict(c.t("simAnalog"), firstNonEmpty(a.Summary.Status, "INFO"), c.t("vAnalog"), a.Summary.Blocks, a.Summary.Targets, a.Summary.Met, a.Summary.Failing)
}
