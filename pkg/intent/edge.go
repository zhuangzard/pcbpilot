package intent

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// buildEdge states the board-edge safety distance: the outer / inner
// defaults for the edge kind (spec.edge may raise or lower them, never
// below the fab floor) and, for every hazardous / mains / patient domain,
// the insulation distance to an accessible surface — the board edge and
// metal mounting holes — from SafetyDistances (clearance and creepage: the
// straight path to the edge is both the air gap and the surface path).
func (c *ctx) buildEdge() {
	se := c.spec.Edge
	if se == nil {
		se = &SpecEdge{}
	}
	kind, err := pcbauto.NormEdgeKind(se.EdgeKind)
	if err != nil {
		kind = pcbauto.EdgeRouted
	}
	outer, inner, vcut, fab, why := pcbauto.EdgeDefaults(kind)
	e := &Edge{EdgeKind: kind, OuterMil: outer, InnerMil: inner, VcutMil: vcut, Why: why}
	set := func(dst *float64, v float64, name string) {
		if v <= 0 {
			return
		}
		v = math.Max(v, fab)
		e.Why = append(e.Why, fmt.Sprintf("spec.edge.%s = %s mil (default %s)", name, trimFloat(v, 3), trimFloat(*dst, 3)))
		*dst = v
	}
	set(&e.OuterMil, se.OuterMil, "outerMil")
	set(&e.InnerMil, se.InnerMil, "innerMil")
	set(&e.VcutMil, se.VcutMil, "vcutMil")
	if kind == pcbauto.EdgeRouted {
		e.VcutMil = 0
	}
	edgeDom := &Domain{ID: "EDGE", Kind: "SELV"}
	st := c.out.Standard
	for _, d := range c.out.Domains {
		hazard := hazardKind(d.Kind)
		if !hazard && d.Kind != "patient" {
			continue
		}
		ins := strings.ToLower(se.Insulation)
		if ins == "" {
			ins = "reinforced"
			if !hazard {
				ins = "basic"
			}
		}
		p := Pair{A: "domain:" + d.ID, B: pcbauto.EdgeAccessible, WorkingVrms: d.WorkingVrms, WorkingVpeak: d.WorkingVpeak, Insulation: ins}
		if st.MOP != "" {
			p.MOP, p.MOPCount = st.MOP, 1
			if ins == "reinforced" || ins == "double" {
				p.MOPCount = 2
			}
		}
		var tw string
		p.Transient, p.MainsVrms, tw = c.pairTransient(d, edgeDom)
		cl, cr, _, _, ref, pwhy := SafetyDistances(p, st)
		ed := &EdgeDomain{ClearanceMm: cl, CreepageMm: cr, Insulation: ins}
		ed.Mil = math.Ceil(math.Max(cl, cr)/0.0254*10) / 10
		ed.Why = append(ed.Why, fmt.Sprintf("%s (%s, %s Vrms / %s V peak) to the board edge and metal mounting holes — accessible or earthed surfaces: %s insulation, clearance %.2f mm, creepage %.2f mm (%s); the straight copper-to-edge path is both the air gap and the surface path, so it keeps the larger (%.1f mil)",
			d.ID, d.Kind, trimFloat(d.WorkingVrms, 3), trimFloat(d.WorkingVpeak, 3), ins, cl, cr, ref, ed.Mil))
		if se.Insulation == "" && hazard {
			ed.Why = append(ed.Why, "reinforced because the edge is treated as accessible; spec.edge.insulation \"basic\" when the enclosure or protective earthing provides the other means of protection")
		}
		if tw != "" {
			ed.Why = append(ed.Why, tw)
		}
		ed.Why = append(ed.Why, pwhy...)
		for key, v := range se.DomainMil {
			if key != d.ID && !has(d.Nets, key) {
				continue
			}
			if v > ed.Mil {
				ed.Why = append(ed.Why, fmt.Sprintf("spec.edge.domainMil[%s] raises it to %s mil", key, trimFloat(v, 3)))
				ed.Mil = v
			} else {
				ed.Why = append(ed.Why, fmt.Sprintf("spec.edge.domainMil[%s] = %s mil ignored: below the insulation distance", key, trimFloat(v, 3)))
			}
		}
		if e.ByDomain == nil {
			e.ByDomain = map[string]*EdgeDomain{}
		}
		e.ByDomain[d.ID] = ed
	}
	if len(e.ByDomain) > 0 {
		ids := make([]string, 0, len(e.ByDomain))
		for id := range e.ByDomain {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		e.Why = append(e.Why, fmt.Sprintf("insulated domains keep their own edge distance: %s (byDomain)", strings.Join(ids, ", ")))
	} else {
		e.Why = append(e.Why, "no hazardous, mains or patient domain: SELV copper uses the layer defaults")
	}
	c.out.Edge = e
}
