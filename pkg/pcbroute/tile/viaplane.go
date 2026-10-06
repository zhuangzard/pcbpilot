package tile

import (
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// ViaKey is the via-plane key of net for via type v: the land radius and the
// net's clearance row as a via over v's layers.
func (s *Set) ViaKey(net geom.NetID, v rules.ViaType) InflationKey {
	from, to := min(v.From, v.To), max(v.From, v.To)
	return InflationKey{HalfWidth: half(v.Pad), Class: s.row(rules.Via, from, to, net)}
}

// ViaPlane returns the via plane of via type v for key k (see ViaKey),
// building it on first use (spec 01 §3.3). It is one plane for the whole
// span: every obstacle on any layer from v.From to v.To, inflated by the via
// land radius plus the largest clearance a via of a net of row k.Class needs to
// it, so a Space point is a via site that is free on all spanned layers (the
// intersection of the per-layer free space). Keep-outs that forbid vias are
// obstacles, ones that forbid only tracks are not; pours on plane layers are
// skipped because the via's antipad there is the pour's rule (spec 01 §3.5,
// §4).
func (s *Set) ViaPlane(v rules.ViaType, k InflationKey) *Plane {
	pk := planeKey{Layer: v.From, Via: v.Name, Key: k}
	if b := s.planes[pk]; b != nil {
		return b.plane
	}
	b := &built{from: min(v.From, v.To), to: max(v.From, v.To), obj: rules.Via, half: k.HalfWidth, clr: map[clrKey]int64{}}
	for n := 1; n <= s.view.NumNets(); n++ {
		if s.ViaKey(geom.NetID(n), v).Class == k.Class {
			b.nets = append(b.nets, geom.NetID(n))
		}
	}
	return s.build(pk, b)
}
