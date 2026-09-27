package pcbauto

import (
	"math"
	"sort"
	"strings"
)

// HSClass is the high-speed rule set of an interface family. Values are
// common design-guide numbers (USB-IF, PCI-SIG, HDMI, MIPI D-PHY, IEEE 802.3,
// JEDEC DDR layout guides); product-specific guides may be stricter, and an
// intent (spec.hsInterfaces) may override skew, group tolerance and vias.
type HSClass struct {
	Name       string  `json:"name"`
	DiffOhm    float64 `json:"diffOhm,omitempty"`
	SEOhm      float64 `json:"seOhm,omitempty"`
	MaxSkewMil float64 `json:"maxSkewMil,omitempty"` // intra-pair length mismatch
	MaxVias    int     `json:"maxVias"`
	MaxLenIn   float64 `json:"maxLenIn,omitempty"`
	Reference  bool    `json:"needsReferencePlane"`
	// GroupSkewMil is the default length tolerance of a length group of this
	// interface (inter-pair / lane-to-lane / byte lane); 0 = no default.
	GroupSkewMil float64 `json:"groupSkewMil,omitempty"`
	// RateGbps is the per-lane bit rate the class is sized for; ≥ 1 Gbps
	// cannot run without an adjacent reference plane.
	RateGbps float64 `json:"rateGbps,omitempty"`
}

// hsClassTable is ordered most specific first: a PCIe or SATA "TXP" is not
// Ethernet, a USB3 connector's D+/D- pair ("USB3_OTG0_DP") is USB 2.0, and
// "HDMI" contains "DM".
var hsClassTable = []struct {
	iface []string // interface names (intent / spec.hsInterfaces) mapping here
	match func(n string) bool
	class HSClass
}{
	{[]string{"USB3", "USB3.0", "USB-SS", "SUPERSPEED"}, func(n string) bool {
		return hasAny(n, "SSTX", "SSRX", "_SS", "SS_", "SUPERSPEED") || strings.Contains(n, "USB3") && !usb2Data(n)
	}, HSClass{Name: "USB3", DiffOhm: 90, MaxSkewMil: 5, MaxVias: 2, MaxLenIn: 6, Reference: true, RateGbps: 5}},
	{[]string{"PCIE", "PCI-E", "PCIEXPRESS"}, func(n string) bool { return hasAny(n, "PCIE", "PCI_E") },
		HSClass{Name: "PCIe", DiffOhm: 85, MaxSkewMil: 5, MaxVias: 2, MaxLenIn: 8, Reference: true, RateGbps: 5}},
	{[]string{"SATA"}, func(n string) bool { return strings.Contains(n, "SATA") },
		HSClass{Name: "SATA", DiffOhm: 100, MaxSkewMil: 5, MaxVias: 2, MaxLenIn: 6, Reference: true, RateGbps: 6}},
	{[]string{"HDMI", "TMDS", "DVI"}, func(n string) bool { return hasAny(n, "HDMI", "TMDS") },
		HSClass{Name: "HDMI", DiffOhm: 100, MaxSkewMil: 5, MaxVias: 2, MaxLenIn: 6, Reference: true, GroupSkewMil: 100, RateGbps: 3.4}},
	{[]string{"MIPI", "MIPI D-PHY", "DSI", "CSI"}, func(n string) bool { return hasAny(n, "MIPI", "DSI", "CSI") },
		HSClass{Name: "MIPI D-PHY", DiffOhm: 100, MaxSkewMil: 10, MaxVias: 2, MaxLenIn: 6, Reference: true, GroupSkewMil: 50, RateGbps: 1.5}},
	{[]string{"LVDS"}, func(n string) bool { return strings.Contains(n, "LVDS") },
		HSClass{Name: "LVDS", DiffOhm: 100, MaxSkewMil: 10, MaxVias: 2, Reference: true, GroupSkewMil: 50, RateGbps: 1}},
	{[]string{"DDR", "DDR3", "DDR4", "LPDDR4"}, func(n string) bool { return hasAny(n, "DDR", "DQS", "DDR_CK", "DRAM") },
		HSClass{Name: "DDR", DiffOhm: 100, MaxSkewMil: 5, MaxVias: 2, MaxLenIn: 3, Reference: true, GroupSkewMil: 25, RateGbps: 1.6}},
	{[]string{"ETH", "ETHERNET", "MDI", "RJ45"}, func(n string) bool {
		return hasAny(n, "ETH", "RJ45", "MDI", "TRD", "TXP", "TXN", "RXP", "RXN")
	}, HSClass{Name: "Ethernet", DiffOhm: 100, MaxSkewMil: 50, MaxVias: 2, MaxLenIn: 4, Reference: true, RateGbps: 0.125}},
	{[]string{"USB", "USB2", "USB2.0"}, func(n string) bool { return hasAny(n, "USB", "D+", "D-", "DP", "DM") },
		HSClass{Name: "USB2", DiffOhm: 90, MaxSkewMil: 100, MaxVias: 2, MaxLenIn: 8, Reference: true, RateGbps: 0.48}},
	{[]string{"CAN", "RS485", "CAN/RS485", "CAN/RS-485"}, func(n string) bool { return hasAny(n, "CAN", "485") },
		HSClass{Name: "CAN/RS-485", DiffOhm: 120, MaxSkewMil: 500, MaxVias: 4}},
}

// genericDiff is the class of a differential pair no family recognises.
var genericDiff = HSClass{Name: "diff", DiffOhm: 100, MaxSkewMil: 25, MaxVias: 2, Reference: true}

func hasAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// usb2Data reports a USB 2.0 data line name (…_DP / …_DM / D+ / D-): on a
// USB3 connector the D+/D- pair runs at 480 Mb/s, not 5 Gb/s.
func usb2Data(n string) bool {
	return reUSBData.MatchString(n) || strings.HasSuffix(n, "DP") || strings.HasSuffix(n, "DM") || strings.HasSuffix(n, "DN")
}

// HSClassForInterface resolves an interface name (as intent derive writes it
// or a spec declares it: "USB3", "PCIE", "HDMI", …) to its class; nil when
// the name is not a known family.
func HSClassForInterface(iface string) *HSClass {
	u := upper(strings.TrimSpace(iface))
	if u == "" {
		return nil
	}
	for _, hc := range hsClassTable {
		for _, name := range hc.iface {
			if u == name || u == upper(hc.class.Name) {
				c := hc.class
				return &c
			}
		}
	}
	return nil
}

// classifyDiffName classifies a differential net by its name.
func classifyDiffName(net string) *HSClass {
	n := upper(net)
	for _, hc := range hsClassTable {
		if hc.match(n) {
			c := hc.class
			return &c
		}
	}
	c := genericDiff
	return &c
}

// ClassifyHSName classifies a differential net by its declared interface
// (when known) else by its name. It never returns nil.
func ClassifyHSName(iface, net string) *HSClass {
	if hc := HSClassForInterface(iface); hc != nil {
		return hc
	}
	return classifyDiffName(net)
}

// ClassifyHS returns the high-speed class of a net, or nil. A net plan from
// an intent carries its interface and the declared limits, which win over
// the family defaults.
func ClassifyHS(np *NetPlan) *HSClass {
	if np.Role == RoleDiff {
		hc := ClassifyHSName(np.Interface, np.Net)
		if np.MaxSkewMil > 0 {
			hc.MaxSkewMil = np.MaxSkewMil
		}
		if np.MaxVias > 0 {
			hc.MaxVias = np.MaxVias
		}
		if np.LengthTolMil > 0 {
			hc.GroupSkewMil = np.LengthTolMil
		}
		return hc
	}
	switch np.Role {
	case RoleClock:
		return &HSClass{Name: "clock", SEOhm: 50, MaxVias: 2, MaxLenIn: 2, Reference: true}
	case RoleRF:
		return &HSClass{Name: "RF", SEOhm: 50, MaxVias: 0, MaxLenIn: 1, Reference: true}
	}
	if np.LengthGroup != "" {
		// A single-ended member of a declared length group (DDR DQ,
		// address/command): its interface sets the vias and tolerance.
		hc := HSClassForInterface(np.Interface)
		if hc == nil {
			hc = &HSClass{Name: "group", MaxVias: 2, Reference: true}
		}
		hc.DiffOhm, hc.SEOhm, hc.MaxSkewMil = 0, 50, 0
		if np.MaxVias > 0 {
			hc.MaxVias = np.MaxVias
		}
		if np.LengthTolMil > 0 {
			hc.GroupSkewMil = np.LengthTolMil
		}
		return hc
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

// SIGroup is a measured length group (intent lengthGroup): a pair counts
// as one unit at the mean length of its two members.
type SIGroup struct {
	Name      string   `json:"name"`
	Units     []string `json:"units"`
	MinMil    float64  `json:"minMil"`
	MaxMil    float64  `json:"maxMil"`
	SpreadMil float64  `json:"spreadMil"`
	TolMil    float64  `json:"tolMil"` // 0 = no tolerance declared (reported only)
}

// SIReport is the post-route signal-integrity check.
type SIReport struct {
	Nets     []SINet     `json:"nets"`
	Pairs    []SIPair    `json:"pairs"`
	Groups   []SIGroup   `json:"groups,omitempty"`
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
			if f := noReference(np.Net, hc, st, tracks[np.Net]); f != nil {
				rep.Findings = append(rep.Findings, *f)
			}
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
	rep.Groups, rep.Findings = checkGroups(an, measured, rep.Findings)
	sort.Slice(rep.Nets, func(i, j int) bool { return rep.Nets[i].Net < rep.Nets[j].Net })
	return rep
}

// checkGroups measures every length group with at least two units (a pair
// is one unit at its mean length) against the group tolerance.
func checkGroups(an *Analysis, measured map[string]*SINet, fs []SIFinding) ([]SIGroup, []SIFinding) {
	members := map[string][]*NetPlan{}
	for _, np := range an.Nets {
		if np.LengthGroup != "" && measured[np.Net] != nil {
			members[np.LengthGroup] = append(members[np.LengthGroup], np)
		}
	}
	names := make([]string, 0, len(members))
	for g := range members {
		names = append(names, g)
	}
	sort.Strings(names)
	var out []SIGroup
	for _, g := range names {
		in := map[string]bool{}
		for _, np := range members[g] {
			in[np.Net] = true
		}
		seen := map[string]bool{}
		type unit struct {
			name string
			l    float64
			ok   bool
		}
		var units []unit
		var hc *HSClass
		for _, np := range members[g] {
			if seen[np.Net] {
				continue
			}
			seen[np.Net] = true
			if hc == nil {
				hc = ClassifyHS(np)
			}
			u := unit{name: np.Net, l: measured[np.Net].LengthMil, ok: measured[np.Net].LengthMil > 0}
			if in[np.PairWith] && !seen[np.PairWith] {
				seen[np.PairWith] = true
				pl := measured[np.PairWith].LengthMil
				u.name += "/" + np.PairWith
				u.l = (u.l + pl) / 2
				u.ok = u.ok && pl > 0
			}
			units = append(units, u)
		}
		if len(units) < 2 {
			continue
		}
		sort.Slice(units, func(i, j int) bool { return units[i].name < units[j].name })
		sg := SIGroup{Name: g, MinMil: math.Inf(1)}
		if hc != nil {
			sg.TolMil = hc.GroupSkewMil
		}
		unrouted := 0
		for _, u := range units {
			sg.Units = append(sg.Units, u.name)
			if !u.ok {
				unrouted++
				continue
			}
			sg.MinMil, sg.MaxMil = math.Min(sg.MinMil, u.l), math.Max(sg.MaxMil, u.l)
		}
		if math.IsInf(sg.MinMil, 1) {
			sg.MinMil = 0
		}
		sg.MinMil, sg.MaxMil = math.Round(sg.MinMil), math.Round(sg.MaxMil)
		sg.SpreadMil = sg.MaxMil - sg.MinMil
		out = append(out, sg)
		if sg.TolMil > 0 && unrouted == 0 && sg.SpreadMil > sg.TolMil {
			fs = append(fs, SIFinding{Net: g, Kind: "group-skew", Value: sg.SpreadMil, Limit: sg.TolMil,
				Fix: "lengthen the short members with a serpentine near their source, or re-place to equalise the escape lengths"})
		}
	}
	return out, fs
}

// noReference reports a track of an impedance-controlled net on a layer with
// no adjacent plane (a 2-layer board: the far side is 1.6 mm away, the
// impedance is uncontrolled and the return path undefined).
func noReference(net string, hc *HSClass, st *Stackup, ts []Track) *SIFinding {
	if st == nil || len(st.Stack) == 0 {
		return nil
	}
	hasPlaneNext := func(layer int) bool {
		for i, l := range st.Stack {
			if l.ID != layer {
				continue
			}
			for _, j := range []int{i - 1, i + 1} {
				if j >= 0 && j < len(st.Stack) && st.Stack[j].Kind == KindPlane {
					return true
				}
			}
			return false
		}
		return true // unknown layer: do not guess
	}
	off := 0.0
	for _, t := range ts {
		if !hasPlaneNext(t.Layer) {
			off += t.A.Dist(t.B)
		}
	}
	if off < 1 {
		return nil
	}
	return &SIFinding{Net: net, Kind: "no-reference", Value: math.Round(off), Limit: 0,
		Fix: sprintf("%s needs an adjacent reference plane: use a 4+ layer stackup (signal next to GND)", hc.Name)}
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
