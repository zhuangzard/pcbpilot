package rules

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

// umPerOz is the copper thickness of one ounce, as pcbauto sizes it
// (1.378 mil, about 35 µm).
const umPerOz = 1.378 * 25.4

// reqEps is the mil tolerance of a "min >= outer" comparison, the same one
// internal/pcb/specctra uses for its net requirements.
const reqEps = 0.005

// PcbautoExtra carries what a pcbauto.Analysis does not keep.
type PcbautoExtra struct {
	// Intent supplies widthMil.min per net: the neck-down floor, and
	// NoNeckDown where min >= outer (decision Q6).
	Intent *pcbauto.Intent
	Coated bool // outer clearance floors use B4
	// NetIDs numbers the nets; nil numbers the analysed nets 1… in name order.
	NetIDs map[string]geom.NetID
}

// FromPcbauto fills a Book from pcbauto's decisions: the board rules become
// the pcb scope, every NetPlan's widths (outer, inner) and clearance its net
// scope, its via size its use_via, its current and voltage its Intent, and
// the stack-up the layers (top = LayerID 0). It returns the net numbering.
func FromPcbauto(b *pcbauto.Board, st *pcbauto.Stackup, a *pcbauto.Analysis, x PcbautoExtra) (*Book, map[string]geom.NetID, error) {
	if b == nil || a == nil {
		return nil, nil, fmt.Errorf("rules: FromPcbauto needs a board and an analysis")
	}
	r := b.Rules
	bk := NewBuilder()

	easyIdx := map[int]geom.LayerID{} // EasyEDA layer id → LayerID
	var inner []geom.LayerID
	if st != nil && len(st.Stack) > 0 {
		for i, sl := range st.Stack {
			l := Layer{ID: geom.LayerID(i), Name: sl.Name, Outer: sl.Outer, MinWidth: mil(r.MinTrack)}
			if sl.Kind == pcbauto.KindPlane {
				l.Kind = Power
			}
			bk.AddLayer(withCopper(l, r))
			easyIdx[sl.ID] = l.ID
			if !l.Outer {
				inner = append(inner, l.ID)
			}
		}
	} else {
		n := max(b.CopperLayers, 2)
		for i := range n {
			l := Layer{ID: geom.LayerID(i), Outer: i == 0 || i == n-1, MinWidth: mil(r.MinTrack)}
			switch {
			case i == 0:
				l.Name, easyIdx[pcbauto.LayerTop] = "TOP", 0
			case i == n-1:
				l.Name, easyIdx[pcbauto.LayerBottom] = "BOTTOM", l.ID
			default:
				l.Name = fmt.Sprintf("IN%d", i)
				easyIdx[pcbauto.LayerInner1+i-1] = l.ID
				inner = append(inner, l.ID)
			}
			bk.AddLayer(withCopper(l, r))
		}
	}
	last := geom.LayerID(len(bk.layers) - 1)

	ids := x.NetIDs
	if ids == nil {
		names := make([]string, 0, len(a.Nets))
		for _, np := range a.Nets {
			names = append(names, np.Net)
		}
		sort.Strings(names)
		ids = map[string]geom.NetID{}
		for i, n := range names {
			ids[n] = geom.NetID(i + 1)
		}
	}

	// Neck-down floor without a per-net min: the narrowest widthMil.min of
	// any net, as the DSN requirements path uses it, else the fab minimum.
	floor := r.MinTrack
	if x.Intent != nil {
		narrowest := 0.0
		for _, n := range x.Intent.Nets {
			if m := n.WidthMil.Min; m > 0 && (narrowest == 0 || m < narrowest) {
				narrowest = m
			}
		}
		if narrowest > 0 {
			floor = math.Max(narrowest, r.MinTrack)
		}
	}
	viaName := func(drill, dia float64) string { return fmt.Sprintf("via%gx%g", dia, drill) }
	vias := map[string]bool{}
	addVia := func(drill, dia float64) string {
		name := viaName(drill, dia)
		if !vias[name] {
			vias[name] = true
			bk.AddVia(ViaType{Name: name, Pad: mil(dia), Drill: mil(drill), From: 0, To: last})
		}
		return name
	}
	boardVia := addVia(r.ViaDrill, r.ViaDia)
	bk.Set(Scope{Kind: ScopePCB}, RuleSet{
		Width: ptr(mil(r.TrackWidth)), MinWidth: ptr(mil(floor)),
		Clearance: map[ClrType]int64{Generic: mil(r.Clearance)}, UseVia: []string{boardVia},
	})
	edge := r.EdgeClearance
	if e := a.Edge; e != nil {
		// One value for every layer: the larger of the outer and inner bands.
		edge = math.Max(math.Max(e.OuterMil, e.InnerMil), e.RuleMil)
	}
	if edge > 0 {
		bk.SetEdge(mil(edge)) // otherwise DefaultEdge (D3)
	}

	mains := map[string]bool{}
	if a.Iso != nil {
		for _, d := range a.Iso.Domains {
			if k := strings.ToLower(d.Kind); k == "mains" || k == "hazardous" {
				for _, n := range d.Nets {
					mains[strings.ToUpper(n)] = true
				}
			}
		}
	}
	for _, np := range a.Nets {
		id, ok := ids[np.Net]
		if !ok {
			continue
		}
		rs := RuleSet{Width: ptr(mil(np.WidthMil)), Clearance: map[ClrType]int64{Generic: mil(np.ClearanceMil)}}
		if np.Via != nil && np.Via.DrillMil > 0 && np.Via.DiaMil > np.Via.DrillMil {
			rs.UseVia = []string{addVia(np.Via.DrillMil, np.Via.DiaMil)}
		}
		in := Intent{
			CurrentA: np.CurrentA, TempRiseC: a.TempRiseC,
			VoltageV: np.Voltage, VoltageKnown: np.Voltage != 0 || np.Role == pcbauto.RoleGround,
			Coated: x.Coated, Mains: mains[strings.ToUpper(np.Net)],
			ControlledImpedance: np.ImpedanceOhm > 0 || np.Role == pcbauto.RoleDiff || np.Role == pcbauto.RoleRF,
		}
		if in.CurrentA <= 0 {
			in.CurrentA = np.PeakA
		}
		if n := intentNet(x.Intent, np.Net); n != nil && n.WidthMil.Min > 0 {
			rs.MinWidth = ptr(mil(math.Max(n.WidthMil.Min, r.MinTrack)))
			in.NoNeckDown = n.WidthMil.Outer > 0 && n.WidthMil.Min+reqEps >= n.WidthMil.Outer
		}
		bk.Set(Scope{Kind: ScopeNet, Net: id}, rs)
		for _, l := range inner {
			bk.Set(Scope{Kind: ScopeNetLayer, Net: id, Layer: l}, RuleSet{Width: ptr(mil(np.InnerWidthMil))})
		}
		bk.SetIntent(id, in)
	}

	for _, k := range b.Keepouts {
		if !k.NoCopper && !k.NoVias || len(k.Poly) < 3 {
			continue
		}
		kind := KeepoutAll
		if !k.NoCopper {
			kind = KeepoutVia
		}
		poly := geom.Poly{Pts: make([]geom.Pt, len(k.Poly))}
		for i, p := range k.Poly {
			poly.Pts[i] = geom.Pt{X: mil(p.X), Y: mil(p.Y)}
		}
		if len(k.Layers) == 0 {
			bk.AddKeepout(Keepout{Shape: poly, Layer: geom.AllLayers, Kind: kind})
			continue
		}
		for _, el := range k.Layers {
			if l, ok := easyIdx[el]; ok {
				bk.AddKeepout(Keepout{Shape: poly, Layer: l, Kind: kind})
			}
		}
	}
	return bk, ids, nil
}

// withCopper sets a layer's finished copper from the board's copper weights.
func withCopper(l Layer, r pcbauto.Rules) Layer {
	oz := r.InnerCopperOz
	if l.Outer {
		oz = r.CopperOz
	}
	l.CopperUm = oz * umPerOz
	return l
}

// intentNet finds a net's intent record, case-insensitively like pcbauto.
func intentNet(in *pcbauto.Intent, net string) *pcbauto.IntentNet {
	if in == nil {
		return nil
	}
	if n := in.Nets[net]; n != nil {
		return n
	}
	for name, n := range in.Nets {
		if strings.EqualFold(name, net) {
			return n
		}
	}
	return nil
}

// mil converts mil to nanometres.
func mil(v float64) int64 { return int64(math.Round(v * nmPerMil)) }
