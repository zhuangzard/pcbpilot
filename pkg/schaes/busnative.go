package schaes

// busnative.go — native bus naming and the bus-member connectivity check.
//
// A native bus (sch_PrimitiveBus) is drawing only: the extension API has no
// bus-entry primitive, so every member is tapped with an ordinary wire plus a
// same-name net label / net port. The bus is never connectivity evidence.
// CheckBuses verifies that claim on a page: every member a bus name implies
// must exist as a real net on a pin AND carry its own label/port, so no member
// is "connected" only by the bus.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// NativeBusName is the native bus name of a candidate group, or "" when the
// group must stay a virtual label lane:
//
//   - indexed groups: NAME[a:b] (the candidate's suggested name, e.g. D[0:7]);
//   - protocol groups (SPI/I2C/UART/SDIO …) and differential pairs: "". Live on
//     V3 3.2.149 (2026-10-04) sch_PrimitiveBus.create returns an empty result
//     for any name without a [a:b] range ("U0_UART" failed, "U0_UART[0:1]"
//     was created), and a range name would imply member nets NAME0..NAMEn that
//     a protocol group (U0RXD/U0TXD) does not have. See NativeBusSkipReason.
func NativeBusName(c BusCandidate) string {
	if c.Kind == "indexed" && reBusRange.MatchString(c.Suggested) {
		return c.Suggested
	}
	return ""
}

// NativeBusSkipReason says why a group gets no native bus ("" when it does).
func NativeBusSkipReason(c BusCandidate) string {
	switch {
	case NativeBusName(c) != "":
		return ""
	case c.Kind == "usb" || c.Kind == "mipi":
		return "differential pair: kept as a parallel pair, never a bus"
	default:
		return "non-indexed " + c.Kind + " group: the host requires a NAME[a:b] bus name (live V3 3.2.149), so it stays a virtual label lane"
	}
}

var reBusRange = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*?)\[(\d+)(?::|\.\.)(\d+)\]$`)

// BusMemberNets resolves the member nets a bus name implies on this page.
// A candidate group whose NativeBusName (or suggested name) equals the bus
// name wins; otherwise NAME[a:b] / NAME[a..b] expands to NAMEa…NAMEb (or
// NAME_a…, whichever spelling exists on the page). ok=false: the name maps to
// no member set.
func BusMemberNets(name string, cands []BusCandidate, pageNets []string) ([]string, bool) {
	for _, c := range cands {
		if n := NativeBusName(c); n != "" && (strings.EqualFold(n, name) || strings.EqualFold(c.Suggested, name)) {
			return append([]string(nil), c.Members...), true
		}
	}
	m := reBusRange.FindStringSubmatch(strings.TrimSpace(name))
	if m == nil {
		return nil, false
	}
	a, _ := strconv.Atoi(m[2])
	b, _ := strconv.Atoi(m[3])
	if a > b {
		a, b = b, a
	}
	if b-a > 255 {
		return nil, false
	}
	have := map[string]string{}
	for _, n := range pageNets {
		have[strings.ToUpper(n)] = n
	}
	best := []string(nil)
	bestHits := -1
	for _, sep := range []string{"", "_"} {
		var mem []string
		hits := 0
		for i := a; i <= b; i++ {
			want := strings.ToUpper(m[1]) + sep + strconv.Itoa(i)
			if n, ok := have[want]; ok {
				mem = append(mem, n)
				hits++
			} else {
				mem = append(mem, m[1]+sep+strconv.Itoa(i))
			}
		}
		if hits > bestHits {
			best, bestHits = mem, hits
		}
	}
	return best, true
}

// BusFinding is one problem of a native bus on the page.
type BusFinding struct {
	Severity string `json:"severity"` // error | warn
	Rule     string `json:"rule"`
	Net      string `json:"net,omitempty"`
	At       *Pt    `json:"at,omitempty"`
	Note     string `json:"note"`
}

// BusCheck is the member connectivity check of one native bus.
type BusCheck struct {
	BusID    string       `json:"busId,omitempty"`
	Name     string       `json:"name"`
	Members  []string     `json:"members,omitempty"`
	OK       bool         `json:"ok"`
	Findings []BusFinding `json:"findings,omitempty"`
}

// CheckBuses checks every native bus on the snapshot:
//
//   - bus-name-unmapped (warn): the name implies no member set;
//   - member-without-pin (error): an implied member net is on no pin;
//   - member-without-label (error): a member has no net label / net port of
//     its own on the page, so it would be "connected" only by the bus;
//   - bus-touches-wire / bus-touches-pin (warn): a bus segment meets a wire
//     or pin; the host may draw a tap there, but the bus still proves nothing.
//
// Markers are needed for the label rule; a source without markers reports
// the rule as not measurable instead of passing it.
func CheckBuses(s *Snapshot) []BusCheck {
	if len(s.Buses) == 0 {
		return nil
	}
	nets := SnapshotNets(s)
	cands := DetectBusCandidates(nets)
	pinNet := map[string]bool{}
	for _, p := range s.Parts {
		for _, q := range p.Pins {
			if q.Net != "" {
				pinNet[q.Net] = true
			}
		}
	}
	labelled := map[string]bool{}
	for _, m := range s.Markers {
		if m.Kind == KindNetPort || m.Kind == KindNetLabel {
			labelled[m.Net] = true
		}
	}
	// On a live page a V3 net label is the stub wire's Name attribute, not a
	// marker component: a member whose pins read the net back and whose wire
	// carries the name is named by its own tap (the bus touches nothing).
	wireNamed := map[string]bool{}
	if s.Source == "components-list" {
		for _, w := range s.Wires {
			if w.Net != "" {
				wireNamed[w.Net] = true
			}
		}
	}
	var out []BusCheck
	for _, b := range s.Buses {
		c := BusCheck{BusID: b.ID, Name: b.Name, OK: true}
		mem, ok := BusMemberNets(b.Name, cands, nets)
		if !ok {
			c.Findings = append(c.Findings, BusFinding{Severity: "warn", Rule: "bus-name-unmapped",
				Note: fmt.Sprintf("bus name %q maps to no member group (NAME[a:b] or a detected protocol group); members cannot be checked", b.Name)})
		}
		c.Members = mem
		for _, n := range mem {
			if !pinNet[n] {
				c.Findings = append(c.Findings, BusFinding{Severity: "error", Rule: "member-without-pin", Net: n,
					Note: "bus names member " + n + " but no pin carries that net"})
				continue
			}
			switch {
			case !s.HasMarkers:
				c.Findings = append(c.Findings, BusFinding{Severity: "warn", Rule: "member-label-unmeasured", Net: n,
					Note: "source carries no markers: member label presence not measured"})
			case !labelled[n] && wireNamed[n]:
				c.Findings = append(c.Findings, BusFinding{Severity: "warn", Rule: "member-label-via-wire-name", Net: n,
					Note: "member " + n + " has no label/port component; its pins and wire read the net back (V3 net label = wire Name)"})
			case !labelled[n]:
				c.Findings = append(c.Findings, BusFinding{Severity: "error", Rule: "member-without-label", Net: n,
					Note: "member " + n + " has no net label / net port of its own: it would be connected only by the bus (no bus entries exist; the bus is never connectivity)"})
			}
		}
		for _, seg := range busSegments(b) {
			for _, w := range s.Wires {
				for k := 1; k < len(w.Pts); k++ {
					if at, hit := segmentsTouch(seg[0], seg[1], w.Pts[k-1], w.Pts[k]); hit {
						c.Findings = append(c.Findings, BusFinding{Severity: "warn", Rule: "bus-touches-wire", Net: w.Net, At: ptr(at),
							Note: "bus segment meets a wire; the host may treat it as a tap — member connectivity must still come from its label"})
					}
				}
			}
			for _, p := range s.Parts {
				for _, q := range p.Pins {
					pt := Pt{q.X, q.Y}
					if onSeg(pt, seg[0], seg[1]) {
						c.Findings = append(c.Findings, BusFinding{Severity: "warn", Rule: "bus-touches-pin", Net: q.Net, At: ptr(pt),
							Note: "bus segment passes through pin " + p.Ref + "." + q.Number})
					}
				}
			}
		}
		for _, f := range c.Findings {
			if f.Severity == "error" {
				c.OK = false
			}
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func busSegments(b Bus) [][2]Pt {
	var out [][2]Pt
	for _, l := range b.Pts {
		for k := 1; k < len(l); k++ {
			if l[k] != l[k-1] {
				out = append(out, [2]Pt{l[k-1], l[k]})
			}
		}
	}
	return out
}

func onSeg(p, a, b Pt) bool {
	const eps = 1e-6
	minX, maxX := min2(a.X, b.X), max2(a.X, b.X)
	minY, maxY := min2(a.Y, b.Y), max2(a.Y, b.Y)
	if p.X < minX-eps || p.X > maxX+eps || p.Y < minY-eps || p.Y > maxY+eps {
		return false
	}
	cross := (b.X-a.X)*(p.Y-a.Y) - (b.Y-a.Y)*(p.X-a.X)
	return cross < eps && cross > -eps
}

// segmentsTouch reports any contact of two orthogonal segments (crossing,
// T, end contact or collinear overlap) and one contact point.
func segmentsTouch(a, b, c, d Pt) (Pt, bool) {
	for _, q := range []Pt{c, d} {
		if onSeg(q, a, b) {
			return q, true
		}
	}
	for _, q := range []Pt{a, b} {
		if onSeg(q, c, d) {
			return q, true
		}
	}
	// strict interior crossing of a horizontal and a vertical segment
	h1, h2 := a.Y == b.Y, c.Y == d.Y
	if h1 != h2 {
		hA, hB, vA, vB := a, b, c, d
		if !h1 {
			hA, hB, vA, vB = c, d, a, b
		}
		x, y := vA.X, hA.Y
		if x > min2(hA.X, hB.X) && x < max2(hA.X, hB.X) && y > min2(vA.Y, vB.Y) && y < max2(vA.Y, vB.Y) {
			return Pt{x, y}, true
		}
	}
	return Pt{}, false
}

func min2(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func max2(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// busCredit reports whether a native bus on the page stands for candidate c
// (N3): its name is the candidate's native bus name (or the legacy prefix
// match) and — when the source carries markers — every member has its own
// label/port, so the bus is not standing in for missing connectivity.
func (a *analyzer) busCredit(c BusCandidate) (string, string) {
	want := NativeBusName(c)
	for _, b := range a.s.Buses {
		named := want != "" && strings.EqualFold(b.Name, want)
		legacy := c.Key != "" && strings.HasPrefix(strings.ToUpper(b.Name), strings.ToUpper(strings.TrimRight(c.Key, "_")))
		if !named && !legacy {
			continue
		}
		if a.s.HasMarkers {
			labelled := map[string]bool{}
			for _, m := range a.s.Markers {
				if m.Kind == KindNetPort || m.Kind == KindNetLabel {
					labelled[m.Net] = true
				}
			}
			for _, n := range c.Members {
				if !labelled[n] {
					return "", fmt.Sprintf("native bus %s not credited: member %s has no label/port of its own", b.Name, n)
				}
			}
		}
		return b.Name, ""
	}
	return "", ""
}
