package designreport

import (
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/safety"
)

// edgeInfo is the board-edge safety distance for the report: the policy
// (intent "edge", else the defaults) and the measured copper-to-edge
// minimum per layer — from the save/reload dump when given, else the board
// dump, else the pcb auto plan.
type edgeInfo struct {
	pol *pcbauto.EdgePolicy
	chk *pcbauto.EdgeCheck
	src string // reload-board | board | plan | ""
}

func (c *ctx) edge() *edgeInfo {
	if c.edgeC == nil {
		c.edgeC = c.computeEdge()
	}
	return c.edgeC
}

func (c *ctx) computeEdge() *edgeInfo {
	var pi *pcbauto.Intent
	if it := c.in.Intent; it != nil {
		pi = &pcbauto.Intent{Edge: it.Edge, Nets: map[string]*pcbauto.IntentNet{},
			Standard: safety.Standard{Name: it.Standard.Name, Insulation: it.Standard.Insulation, MOP: it.Standard.MOP, MOPCount: it.Standard.MOPCount,
				PollutionDegree: it.Standard.PollutionDegree, MaterialGroup: it.Standard.MaterialGroup, AltitudeM: it.Standard.AltitudeM,
				OvervoltageCategory: it.Standard.OvervoltageCategory, Coated: it.Standard.Coated}}
		for _, d := range it.Domains {
			pi.Domains = append(pi.Domains, pcbauto.IntentDomain{ID: d.ID, Kind: d.Kind, Nets: d.Nets, WorkingVrms: d.WorkingVrms, WorkingVpeak: d.WorkingVpeak})
		}
		for n, np := range it.Nets {
			pi.Nets[n] = &pcbauto.IntentNet{Domain: np.Domain}
		}
	}
	e := &edgeInfo{pol: pcbauto.EdgeFromIntent(pi, nil)}
	for _, cand := range []struct {
		b   *Board
		src string
	}{{c.in.ReloadBoard, "reload-board"}, {c.in.Board, "board"}} {
		if cand.b == nil || len(cand.b.raw) == 0 {
			continue
		}
		if chk, err := pcbauto.CheckEdgeSnapshot(cand.b.raw, pi, ""); err == nil {
			e.chk, e.src, e.pol = chk, cand.src, chk.Policy
			return e
		}
	}
	if pl := c.in.Plan; pl != nil && pl.Result != nil && pl.Result.Edge != nil {
		e.chk, e.src = pl.Result.Edge, "plan"
		if e.chk.Policy != nil {
			e.pol = e.chk.Policy
		}
	}
	return e
}

// requirement is "外层 ≥ X mil / 内层 ≥ Y mil（kind）".
func (e *edgeInfo) requirement() string {
	p := e.pol
	s := sprintf("外层 ≥ %s mil（%s mm）/ 内层·平面 ≥ %s mil（%s mm），%s 板边，来源 %s",
		f1(p.LayerReq(pcbauto.LayerTop)), f2(p.LayerReq(pcbauto.LayerTop)*0.0254), f1(p.LayerReq(pcbauto.LayerInner1)), f2(p.LayerReq(pcbauto.LayerInner1)*0.0254), p.Kind, p.Source)
	return s
}

// measured is "L1 14.1 / L15 10 …" (mil) with the governing item.
func (e *edgeInfo) measured() string {
	if e.chk == nil {
		return "未测（需 --board / --reload-board 或 plan.json）"
	}
	var parts []string
	for _, l := range e.chk.Layers {
		if !l.Measured {
			continue
		}
		v := f2(l.MinMil) + " mil"
		if l.MinMil < 0 {
			v = "越出板框"
		}
		parts = append(parts, sprintf("L%d %s（%s，≥ %s）", l.Layer, v, l.Kind, f1(l.RequiredMil)))
	}
	if h := e.chk.HoleMin; h != nil && h.Measured {
		parts = append(parts, sprintf("安装孔壁 %s mil", f2(h.MinMil)))
	}
	if len(parts) == 0 {
		return "无铜"
	}
	return strings.Join(parts, "；") + "（" + e.src + "）"
}

func (e *edgeInfo) errors() int {
	if e.chk == nil {
		return 0
	}
	return e.chk.Errors()
}
