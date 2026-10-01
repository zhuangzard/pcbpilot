package schaes

import (
	"encoding/json"
	"os"
	"testing"
)

// Synthetic wired pages derived from a REAL measured placement (esp32-v05
// MCU page: real bboxes, real pin coordinates, real nets). The canonical
// snapshot has no wires, so the wiring is generated here deterministically in
// two drafting styles; degradations are then applied one knob at a time.

const esp32MCU = "../../internal/app/testdata/esp32-v05/sch-950ae6609e91d753.json"
const esp32PWR = "../../internal/app/testdata/esp32-v05/sch-905bb85957eaf435.json"

func loadSnap(t testing.TB, path string) *Snapshot {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func cloneSnap(s *Snapshot) *Snapshot {
	raw, _ := json.Marshal(s)
	var c Snapshot
	_ = json.Unmarshal(raw, &c)
	for i := range c.Parts {
		c.Parts[i].HasBox = s.Parts[i].HasBox
	}
	return &c
}

// stubLabel: every connected pin gets a 20-unit outward stub ending in a flag
// (power/ground) or a net port (signals) whose body continues the stub — the
// connect_pin style.
func stubLabel(base *Snapshot) *Snapshot {
	s := cloneSnap(base)
	s.Source, s.HasWires, s.HasMarkers, s.Notes = "synthetic-stub-label", true, true, nil
	for _, p := range s.Parts {
		for _, q := range p.Pins {
			if q.Net == "" || q.NC || q.Dir == "" {
				continue
			}
			v := dirVec[q.Dir]
			end := Pt{q.X + 20*v.X, q.Y + 20*v.Y}
			s.Wires = append(s.Wires, Wire{Net: q.Net, Pts: []Pt{{q.X, q.Y}, end}})
			kind := KindNetPort
			switch {
			case IsGroundNet(q.Net):
				kind = KindGround
			case IsPowerNet(q.Net):
				kind = KindPower
			}
			s.Markers = append(s.Markers, Marker{Kind: kind, Net: q.Net, Anchor: end, Dir: q.Dir, Box: predictMarkerBox(kind, q.Net, end, q.Dir), Estimated: true})
		}
	}
	return s
}

// pointToPoint: signal nets are chained pin→pin with L-shaped wires (the
// "everything is a wire" style); power/ground still use flags.
func pointToPoint(base *Snapshot) *Snapshot {
	s := cloneSnap(base)
	s.Source, s.HasWires, s.HasMarkers, s.Notes = "synthetic-point-to-point", true, true, nil
	byNet := map[string][]Pt{}
	for _, p := range s.Parts {
		for _, q := range p.Pins {
			if q.Net == "" || q.NC || q.Dir == "" {
				continue
			}
			if IsGroundNet(q.Net) || IsPowerNet(q.Net) {
				v := dirVec[q.Dir]
				end := Pt{q.X + 20*v.X, q.Y + 20*v.Y}
				kind := KindPower
				if IsGroundNet(q.Net) {
					kind = KindGround
				}
				s.Wires = append(s.Wires, Wire{Net: q.Net, Pts: []Pt{{q.X, q.Y}, end}})
				s.Markers = append(s.Markers, Marker{Kind: kind, Net: q.Net, Anchor: end, Dir: q.Dir, Box: predictMarkerBox(kind, q.Net, end, q.Dir), Estimated: true})
				continue
			}
			byNet[q.Net] = append(byNet[q.Net], Pt{q.X, q.Y})
		}
	}
	for _, n := range sortedKeys(byNet) {
		pts := byNet[n]
		for i := 1; i < len(pts); i++ {
			a, b := pts[i-1], pts[i]
			s.Wires = append(s.Wires, Wire{Net: n, Pts: []Pt{a, {b.X, a.Y}, b}})
		}
	}
	return s
}

type degrade func(s *Snapshot, k int)

// jog replaces k stubs by a stub with a sideways S-jog (2 extra bends).
func jog(s *Snapshot, k int) {
	n := 0
	for i := range s.Wires {
		if n >= k {
			return
		}
		w := &s.Wires[i]
		if len(w.Pts) != 2 {
			continue
		}
		a, b := w.Pts[0], w.Pts[1]
		m1 := Pt{(a.X + b.X) / 2, (a.Y + b.Y) / 2}
		off := Pt{5, 0}
		if a.Y == b.Y {
			off = Pt{0, 5}
		}
		// a → m1 → m1+off → (m1+off advanced) → b+off → b would add overlap; keep a clean S:
		w.Pts = []Pt{a, m1, {m1.X + off.X, m1.Y + off.Y}, {b.X + off.X, b.Y + off.Y}}
		// re-anchor the marker that sat on b
		for j := range s.Markers {
			if near(s.Markers[j].Anchor, b) {
				s.Markers[j].Anchor = Pt{b.X + off.X, b.Y + off.Y}
				s.Markers[j].Box = predictMarkerBox(s.Markers[j].Kind, s.Markers[j].Net, s.Markers[j].Anchor, s.Markers[j].Dir)
			}
		}
		n++
	}
}

// crossings adds k free horizontal wires of a foreign net across the page.
func crossings(s *Snapshot, k int) {
	var b Box
	for _, p := range s.Parts {
		b = b.Union(p.Box)
	}
	for i := 0; i < k; i++ {
		y := b.MinY - 40
		x := b.MinX + 40 + float64(i)*100
		s.Wires = append(s.Wires, Wire{Net: "XFOREIGN", Pts: []Pt{{x - 30, y}, {x + 30, y}}})
		s.Wires = append(s.Wires, Wire{Net: "YFOREIGN", Pts: []Pt{{x, y - 20}, {x, y + 20}}})
	}
}

// offGrid nudges k small parts (with their pins, stubs and markers) by 2 units.
func offGrid(s *Snapshot, k int) {
	n := 0
	for i := range s.Parts {
		if n >= k {
			return
		}
		p := &s.Parts[i]
		if len(p.Pins) > 3 {
			continue
		}
		moved := map[[2]int64]bool{}
		for q := range p.Pins {
			moved[qkey(Pt{p.Pins[q].X, p.Pins[q].Y})] = true
			p.Pins[q].X += 2
		}
		p.Box = Box{p.Box.MinX + 2, p.Box.MinY, p.Box.MaxX + 2, p.Box.MaxY}
		p.X += 2
		for w := range s.Wires {
			pts := s.Wires[w].Pts
			if !moved[qkey(pts[0])] || len(pts) != 2 {
				continue
			}
			for m := range s.Markers {
				if near(s.Markers[m].Anchor, pts[1]) {
					s.Markers[m].Anchor.X += 2
					s.Markers[m].Box = predictMarkerBox(s.Markers[m].Kind, s.Markers[m].Net, s.Markers[m].Anchor, s.Markers[m].Dir)
				}
			}
			pts[0].X += 2
			pts[1].X += 2
		}
		n++
	}
}

// flip reverses k markers (body pointing back at its own wire).
func flip(s *Snapshot, k int) {
	for i := 0; i < len(s.Markers) && i < k; i++ {
		m := &s.Markers[i]
		m.Dir = opposite(m.Dir)
		m.Box = predictMarkerBox(m.Kind, m.Net, m.Anchor, m.Dir)
	}
}

// fourWay adds k plus-shaped same-net junctions (4 arms meeting at a point).
func fourWay(s *Snapshot, k int) {
	var b Box
	for _, p := range s.Parts {
		b = b.Union(p.Box)
	}
	for i := 0; i < k; i++ {
		c := Pt{b.MaxX + 60 + float64(i)*60, b.MinY - 60}
		for _, d := range []Pt{{20, 0}, {-20, 0}, {0, 20}, {0, -20}} {
			s.Wires = append(s.Wires, Wire{Net: "J4", Pts: []Pt{c, {c.X + d.X, c.Y + d.Y}}})
		}
	}
}

// stack moves k markers onto the first marker's box (label overlaps).
func stack(s *Snapshot, k int) {
	if len(s.Markers) == 0 {
		return
	}
	for i := 1; i < len(s.Markers) && i <= k; i++ {
		s.Markers[i].Box = s.Markers[0].Box
	}
}

func TestSyntheticStylesOrderIsSane(t *testing.T) {
	base := loadSnap(t, esp32MCU)
	good := Analyze(drafted(base), nil)
	clean := Analyze(stubLabel(base), nil)
	p2p := Analyze(pointToPoint(base), nil)
	t.Logf("drafted %.1f %v | stub-label %.1f %v | point-to-point %.1f %v", good.Score, good.Groups, clean.Score, clean.Groups, p2p.Score, p2p.Groups)
	if !(good.Score > clean.Score && clean.Score > p2p.Score) {
		t.Fatalf("expected drafted > stub-label > point-to-point, got %.1f / %.1f / %.1f", good.Score, clean.Score, p2p.Score)
	}
	if !(p2p.Metric("W2").Score < clean.Metric("W2").Score) {
		t.Fatalf("point-to-point must cross more: W2 %.1f vs %.1f", p2p.Metric("W2").Score, clean.Metric("W2").Score)
	}
	if !(p2p.Metric("W7").Score <= clean.Metric("W7").Score) {
		t.Fatalf("point-to-point must not run through fewer bodies")
	}
	if !(p2p.Groups[GroupWiring] < clean.Groups[GroupWiring]) {
		t.Fatalf("wiring group: p2p %.1f should be below stub-label %.1f", p2p.Groups[GroupWiring], clean.Groups[GroupWiring])
	}
}

// Monotonicity: one knob at a time, more degradation never scores better, and
// the full dose scores strictly worse on the targeted metric and overall.
func TestSyntheticDegradationsAreMonotone(t *testing.T) {
	base := loadSnap(t, esp32MCU)
	// pure knobs touch only their metric's geometry, so the overall score is
	// asserted too; off-grid physically moves parts (overlap / alignment can
	// legitimately change), so only its targeted metric is asserted.
	cases := []struct {
		name   string
		metric string
		fn     degrade
		pure   bool
	}{
		{"jog", "W1", jog, true},
		{"crossings", "W2", crossings, true},
		{"four-way", "W3", fourWay, true},
		{"off-grid", "W8", offGrid, false},
		{"flip", "L2", flip, true},
		{"stack", "L6", stack, true},
	}
	for _, c := range cases {
		prevM, prevS := 101.0, 101.0
		var first, last *Report
		for k := 0; k <= 4; k++ {
			s := drafted(base)
			c.fn(s, k)
			r := Analyze(s, nil)
			m := r.Metric(c.metric)
			if m.Skipped {
				t.Fatalf("%s: %s skipped: %s", c.name, c.metric, m.Reason)
			}
			if m.Score > prevM+1e-9 {
				t.Fatalf("%s k=%d: %s rose %.1f → %.1f", c.name, k, c.metric, prevM, m.Score)
			}
			// share metrics (W8 grid share) may move by a fraction of a
			// point when a knob adds vertices; overall stays within 0.5.
			if c.pure && r.Score > prevS+0.5 {
				t.Fatalf("%s k=%d: overall rose %.1f → %.1f", c.name, k, prevS, r.Score)
			}
			prevM, prevS = m.Score, r.Score
			if k == 0 {
				first = r
			}
			last = r
		}
		if !(last.Metric(c.metric).Score < first.Metric(c.metric).Score) || c.pure && !(last.Score < first.Score) {
			t.Fatalf("%s: full dose not strictly worse (%s %.1f→%.1f, overall %.1f→%.1f)", c.name, c.metric,
				first.Metric(c.metric).Score, last.Metric(c.metric).Score, first.Score, last.Score)
		}
		t.Logf("%-9s %s %.1f → %.1f   overall %.1f → %.1f", c.name, c.metric, first.Metric(c.metric).Score, last.Metric(c.metric).Score, first.Score, last.Score)
	}
}

// Report-only invariant: the analysis never mutates its input.
func TestAnalyzeDoesNotMutate(t *testing.T) {
	s := stubLabel(loadSnap(t, esp32MCU))
	before, _ := json.Marshal(s)
	Analyze(s, &Profile{Name: "auto"})
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		t.Fatal("Analyze mutated the snapshot")
	}
}
