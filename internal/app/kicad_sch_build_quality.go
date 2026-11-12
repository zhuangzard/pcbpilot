package app

// kicad_sch_build_quality.go — how a zone drawing joins its pins, read from
// the layout itself: the share of in-zone connections made by real wires
// (vs. net labels / supply symbols), and how many small parts (≤ 3 pins)
// are wired to another part's pin. layoutZones uses it to choose among the
// clean candidate drawings of a zone; the layout-quality gate
// (kicad_sch_build_watchdog.go) reports it per zone.

import (
	"math"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// sbZoneQuality is the connection statistics of one zone drawing.
type sbZoneQuality struct {
	SigConns   int      `json:"signalConnections"` // Σ(pins − 1) over the zone's signal nets
	SigWired   int      `json:"signalWired"`       // of which joined by a wire
	PwrConns   int      `json:"supplyConnections"`
	PwrWired   int      `json:"supplyWired"`
	Small      int      `json:"smallParts"` // ≤ 3-pin parts with a connected signal pin
	SmallWired int      `json:"smallWired"` // wired to another part's pin
	AreaMM2    float64  `json:"areaMm2"`
	LongestMM  float64  `json:"longestWireMm"`
	Unwired    []string `json:"-"` // the small parts not wired to another part
}

// LabelShare is the share of signal connections made by labels (0 = all
// wired; 0 when the zone has none).
func (q sbZoneQuality) LabelShare() float64 {
	if q.SigConns == 0 {
		return 0
	}
	return 1 - float64(q.SigWired)/float64(q.SigConns)
}

// AttachedShare is the share of small parts wired to another part (1 when
// the zone has none).
func (q sbZoneQuality) AttachedShare() float64 {
	if q.Small == 0 {
		return 1
	}
	return float64(q.SmallWired) / float64(q.Small)
}

// sbZoneQualityOf measures zl (zone-relative): pins are joined when they
// share a wire island (a wire end on another wire or on a pin connects, as
// in KiCad).
func sbZoneQualityOf(d *sbDesign, zi *sbZoneIn, zl *sbZoneLayout, m *sbMeasure) sbZoneQuality {
	var q sbZoneQuality
	q.AreaMM2 = zl.Box.W() * zl.Box.H()
	segs := zl.Wires
	type seg struct{ a, b kicad.Pt }
	var ss []seg
	for _, w := range segs {
		for i := 0; i+1 < len(w); i++ {
			ss = append(ss, seg{w[i], w[i+1]})
			q.LongestMM = math.Max(q.LongestMM, math.Hypot(w[i].X-w[i+1].X, w[i].Y-w[i+1].Y))
		}
	}
	parent := make([]int, len(ss))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	on := func(p kicad.Pt, s seg) bool {
		const e = 1e-3
		if math.Abs(s.a.X-s.b.X) < e {
			return math.Abs(p.X-s.a.X) < e && p.Y >= math.Min(s.a.Y, s.b.Y)-e && p.Y <= math.Max(s.a.Y, s.b.Y)+e
		}
		if math.Abs(s.a.Y-s.b.Y) < e {
			return math.Abs(p.Y-s.a.Y) < e && p.X >= math.Min(s.a.X, s.b.X)-e && p.X <= math.Max(s.a.X, s.b.X)+e
		}
		return sbNear(p, s.a) || sbNear(p, s.b)
	}
	for i := range ss {
		for j := i + 1; j < len(ss); j++ {
			if on(ss[i].a, ss[j]) || on(ss[i].b, ss[j]) || on(ss[j].a, ss[i]) || on(ss[j].b, ss[i]) {
				parent[find(i)] = find(j)
			}
		}
	}
	// a pin's island: the root of a wire through it, else a key of its own
	// (coincident pins share one)
	type pin struct {
		ref, net string
		isl      int
	}
	var pins []pin
	solo := map[[2]int64]int{}
	next := len(ss)
	for _, p := range zi.Parts {
		for _, pose := range zl.Parts[p.Ref] {
			s, err := m.get(p, max(pose.Unit, 1))
			if err != nil {
				continue
			}
			for _, sp := range s.Pins {
				n, ok := d.PinNet[sbPinKey(p.Ref, sp.Number)]
				if !ok {
					continue
				}
				at := sbXform(sp.At, pose)
				isl := -1
				for i, sg := range ss {
					if on(at, sg) {
						isl = find(i)
						break
					}
				}
				if isl < 0 {
					k := [2]int64{int64(math.Round(at.X * 1000)), int64(math.Round(at.Y * 1000))}
					if v, ok := solo[k]; ok {
						isl = v
					} else {
						isl, solo[k] = next, next
						next++
					}
				}
				pins = append(pins, pin{p.Ref, n, isl})
			}
		}
	}
	byNet := map[string]map[int]bool{}
	cnt := map[string]int{}
	islRefs := map[int]map[string]bool{}
	for _, p := range pins {
		if byNet[p.net] == nil {
			byNet[p.net] = map[int]bool{}
		}
		byNet[p.net][p.isl] = true
		cnt[p.net]++
		if islRefs[p.isl] == nil {
			islRefs[p.isl] = map[string]bool{}
		}
		islRefs[p.isl][p.ref] = true
	}
	for n, c := range cnt {
		if c < 2 {
			continue
		}
		conns, wired := c-1, c-len(byNet[n])
		if k := d.NetKind[n]; k == "power" || k == "ground" {
			q.PwrConns += conns
			q.PwrWired += wired
		} else {
			q.SigConns += conns
			q.SigWired += wired
		}
	}
	for _, p := range zi.Parts {
		if len(p.LPins) > 3 {
			continue
		}
		connected, wired := false, false
		for _, pp := range pins {
			if pp.ref != p.Ref {
				continue
			}
			// a signal pin makes it count: a decoupling cap or a bleeder
			// between supplies is drawn with supply symbols by design
			if k := d.NetKind[pp.net]; k != "power" && k != "ground" {
				connected = true
			}
			for r := range islRefs[pp.isl] {
				if r != p.Ref {
					wired = true
				}
			}
		}
		if connected {
			q.Small++
			if wired {
				q.SmallWired++
			} else {
				q.Unwired = append(q.Unwired, p.Ref)
			}
		}
	}
	return q
}

func sbNear(a, b kicad.Pt) bool { return math.Abs(a.X-b.X) < 1e-3 && math.Abs(a.Y-b.Y) < 1e-3 }

// sbZoneCost ranks clean candidate drawings of one zone (lower is better):
// label-joined signal connections, unwired small parts, and area relative
// to the smallest candidate.
func sbZoneCost(q sbZoneQuality, minArea float64) float64 {
	c := q.LabelShare() + 0.5*(1-q.AttachedShare())
	if minArea > 0 {
		c += 0.5 * (q.AreaMM2/minArea - 1)
	}
	return c
}
