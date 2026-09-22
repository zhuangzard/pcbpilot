package pcbauto

import (
	"math"
	"sort"
	"strings"
)

// HSClass is the high-speed rule set of an interface family. Values are
// common design-guide numbers (USB-IF, MIPI D-PHY, HDMI, IEEE 802.3 layout
// guides); product-specific guides may be stricter.
type HSClass struct {
	Name       string  `json:"name"`
	DiffOhm    float64 `json:"diffOhm,omitempty"`
	SEOhm      float64 `json:"seOhm,omitempty"`
	MaxSkewMil float64 `json:"maxSkewMil,omitempty"` // intra-pair length mismatch
	MaxVias    int     `json:"maxVias"`
	MaxLenIn   float64 `json:"maxLenIn,omitempty"`
	Reference  bool    `json:"needsReferencePlane"`
}

var hsClasses = []struct {
	match []string
	class HSClass
}{
	{[]string{"SSTX", "SSRX", "USB3", "_SS"}, HSClass{Name: "USB3", DiffOhm: 90, MaxSkewMil: 5, MaxVias: 2, MaxLenIn: 6, Reference: true}},
	{[]string{"PCIE", "PCI_E"}, HSClass{Name: "PCIe", DiffOhm: 85, MaxSkewMil: 5, MaxVias: 2, MaxLenIn: 8, Reference: true}},
	{[]string{"HDMI", "TMDS"}, HSClass{Name: "HDMI", DiffOhm: 100, MaxSkewMil: 5, MaxVias: 2, MaxLenIn: 6, Reference: true}},
	{[]string{"MIPI", "DSI", "CSI"}, HSClass{Name: "MIPI D-PHY", DiffOhm: 100, MaxSkewMil: 10, MaxVias: 2, MaxLenIn: 6, Reference: true}},
	{[]string{"ETH", "RJ45", "MDI", "TXP", "TXN", "RXP", "RXN"}, HSClass{Name: "Ethernet", DiffOhm: 100, MaxSkewMil: 50, MaxVias: 2, MaxLenIn: 4, Reference: true}},
	{[]string{"LVDS"}, HSClass{Name: "LVDS", DiffOhm: 100, MaxSkewMil: 10, MaxVias: 2, Reference: true}},
	{[]string{"USB", "D+", "D-", "DP", "DM"}, HSClass{Name: "USB2", DiffOhm: 90, MaxSkewMil: 100, MaxVias: 2, MaxLenIn: 8, Reference: true}},
	{[]string{"CAN", "485"}, HSClass{Name: "CAN/RS-485", DiffOhm: 120, MaxSkewMil: 500, MaxVias: 4}},
}

// ClassifyHS returns the high-speed class of a net, or nil.
func ClassifyHS(np *NetPlan) *HSClass {
	n := upper(np.Net)
	if np.Role == RoleDiff {
		for _, hc := range hsClasses {
			for _, m := range hc.match {
				if strings.Contains(n, m) {
					c := hc.class
					return &c
				}
			}
		}
		return &HSClass{Name: "diff", DiffOhm: 100, MaxSkewMil: 25, MaxVias: 2, Reference: true}
	}
	switch np.Role {
	case RoleClock:
		return &HSClass{Name: "clock", SEOhm: 50, MaxVias: 2, MaxLenIn: 2, Reference: true}
	case RoleRF:
		return &HSClass{Name: "RF", SEOhm: 50, MaxVias: 0, MaxLenIn: 1, Reference: true}
	}
	return nil
}

// SINet is the measured routing of one high-speed net.
type SINet struct {
	Net       string  `json:"net"`
	Class     string  `json:"class"`
	LengthMil float64 `json:"lengthMil"`
	Vias      int     `json:"vias"`
	Layers    []int   `json:"layers"`
}

// SIPair is a measured differential pair.
type SIPair struct {
	P        string  `json:"p"`
	N        string  `json:"n"`
	SkewMil  float64 `json:"skewMil"`
	LimitMil float64 `json:"limitMil"`
}

// SIFinding is one signal-integrity issue.
type SIFinding struct {
	Net   string  `json:"net"`
	Kind  string  `json:"kind"` // skew | vias | length | split-crossing | layer-change
	Value float64 `json:"value"`
	Limit float64 `json:"limit"`
	At    *Point  `json:"at,omitempty"`
	Fix   string  `json:"fix"`
}

// SIReport is the post-route signal-integrity check.
type SIReport struct {
	Nets     []SINet     `json:"nets"`
	Pairs    []SIPair    `json:"pairs"`
	Findings []SIFinding `json:"findings"`
}

// CheckSI measures high-speed nets after routing: length, vias, intra-pair
// skew and reference-plane continuity (a track over a split in its adjacent
// plane loses its return path — the classic EMI/SI failure).
func CheckSI(b *Board, an *Analysis, st *Stackup, rr *RouteResult) *SIReport {
	rep := &SIReport{}
	tracks := map[string][]Track{}
	vias := map[string]int{}
	for _, t := range rr.Tracks {
		tracks[t.Net] = append(tracks[t.Net], t)
	}
	for _, v := range rr.Vias {
		if v.Kind == "route" {
			vias[v.Net]++
		}
	}
	regions := map[int][]PlaneRegion{}
	for _, pr := range rr.Planes {
		regions[pr.Layer] = append(regions[pr.Layer], pr)
	}
	measured := map[string]*SINet{}
	for _, np := range an.Nets {
		hc := ClassifyHS(np)
		if hc == nil {
			continue
		}
		sn := &SINet{Net: np.Net, Class: hc.Name, Vias: vias[np.Net]}
		layerSeen := map[int]bool{}
		for _, t := range tracks[np.Net] {
			sn.LengthMil += t.A.Dist(t.B)
			if !layerSeen[t.Layer] {
				layerSeen[t.Layer] = true
				sn.Layers = append(sn.Layers, t.Layer)
			}
		}
		sn.LengthMil = math.Round(sn.LengthMil)
		sort.Ints(sn.Layers)
		measured[np.Net] = sn
		rep.Nets = append(rep.Nets, *sn)
		if hc.MaxVias >= 0 && sn.Vias > hc.MaxVias {
			rep.Findings = append(rep.Findings, SIFinding{Net: np.Net, Kind: "vias", Value: float64(sn.Vias), Limit: float64(hc.MaxVias),
				Fix: "re-route on one layer or add a GND stitching via beside each signal via"})
		}
		if hc.MaxLenIn > 0 && sn.LengthMil > hc.MaxLenIn*1000 {
			rep.Findings = append(rep.Findings, SIFinding{Net: np.Net, Kind: "length", Value: sn.LengthMil, Limit: hc.MaxLenIn * 1000,
				Fix: "move the endpoints closer in placement"})
		}
		if hc.Reference {
			rep.Findings = append(rep.Findings, splitCrossings(np.Net, st, tracks[np.Net], regions)...)
		}
	}
	done := map[string]bool{}
	for _, np := range an.Nets {
		if np.PairWith == "" || done[np.Net] {
			continue
		}
		done[np.Net], done[np.PairWith] = true, true
		a, bb := measured[np.Net], measured[np.PairWith]
		if a == nil || bb == nil || a.LengthMil == 0 || bb.LengthMil == 0 {
			continue
		}
		hc := ClassifyHS(np)
		pr := SIPair{P: np.Net, N: np.PairWith, SkewMil: math.Abs(a.LengthMil - bb.LengthMil), LimitMil: hc.MaxSkewMil}
		rep.Pairs = append(rep.Pairs, pr)
		if pr.SkewMil > pr.LimitMil {
			rep.Findings = append(rep.Findings, SIFinding{Net: np.Net + "/" + np.PairWith, Kind: "skew", Value: pr.SkewMil, Limit: pr.LimitMil,
				Fix: "add a serpentine on the shorter member near the mismatch source (pcb eq-group / length tuning)"})
		}
	}
	sort.Slice(rep.Nets, func(i, j int) bool { return rep.Nets[i].Net < rep.Nets[j].Net })
	return rep
}

// splitCrossings samples a net's tracks on layers whose adjacent reference
// is a split plane and reports where the region under the track changes.
func splitCrossings(net string, st *Stackup, ts []Track, regions map[int][]PlaneRegion) []SIFinding {
	var out []SIFinding
	adj := func(layer int) (int, bool) {
		for i, l := range st.Stack {
			if l.ID != layer {
				continue
			}
			// Prefer the nearest plane layer; a split one matters.
			for _, j := range []int{i + 1, i - 1} {
				if j >= 0 && j < len(st.Stack) && st.Stack[j].Kind == KindPlane {
					return st.Stack[j].ID, true
				}
			}
		}
		return 0, false
	}
	regionAt := func(layer int, p Point) string {
		for _, pr := range regions[layer] {
			for _, poly := range pr.Polys {
				if PolyContains(poly, p) {
					return pr.Net
				}
			}
		}
		return ""
	}
	reported := 0
	for _, t := range ts {
		ref, ok := adj(t.Layer)
		if !ok || len(regions[ref]) < 2 {
			continue
		}
		steps := int(math.Ceil(t.A.Dist(t.B)/20)) + 1
		prev := ""
		for s := 0; s <= steps && reported < 3; s++ {
			p := t.A.Add(t.B.Sub(t.A).Scale(float64(s) / float64(steps)))
			cur := regionAt(ref, p)
			if s > 0 && cur != prev {
				pp := p
				out = append(out, SIFinding{Net: net, Kind: "split-crossing", At: &pp, Value: float64(ref),
					Fix: "route this segment over one plane region, or add a stitching capacitor across the split next to the crossing"})
				reported++
			}
			prev = cur
		}
	}
	return out
}
