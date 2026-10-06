package rules

import "github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"

// Neck is the neck-down rule of a net at a pad on a layer (04 §3.5, decision
// D2: this is the only source of the zone). MinWidth is the floor: the
// resolved MinWidth (default: the full width, i.e. no neck-down), never below
// the layer's fab minimum or the width that carries the intent current at
// twice the target rise. Zone is measured from the pad edge: NeckZone if a
// scope sets it, otherwise the pad's longer side, at most 3 mm. Zone 0 means
// no neck-down: forbidden by intent (NoNeckDown, ControlledImpedance), no
// floor below the width, or a zero-length pad side.
func (t *table) Neck(net geom.NetID, layer geom.LayerID, pad PadSize) Neck {
	r := t.row(t.info(net), layer)
	if !r.neck {
		return Neck{MinWidth: r.width}
	}
	zone := r.zone
	if zone < 0 {
		zone = min(max(pad.Long, 0), maxNeckZone)
	}
	if zone == 0 {
		return Neck{MinWidth: r.width}
	}
	return Neck{MinWidth: r.neckMin, Zone: zone}
}

// WidthAt is the track width a net may use at distFromPadEdge from a pad's
// edge (04 §3.5): inside the neck zone it is the pad's narrow side clamped to
// [MinWidth, width], so a neck is never wider than the pad nor narrower than
// the floor; outside, the full width.
func WidthAt(r Resolver, net geom.NetID, layer geom.LayerID, pad PadSize, distFromPadEdge int64) int64 {
	w := r.Width(net, layer)
	n := r.Neck(net, layer, pad)
	if n.Zone == 0 || distFromPadEdge > n.Zone {
		return w
	}
	return min(max(pad.Narrow, n.MinWidth), w)
}
