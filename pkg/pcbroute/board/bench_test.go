package board_test

import (
	"math"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/board"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/dsn"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// TestBenchConnectivity is the M4 bench acceptance (PLAN.md M4) on a local
// design that is never committed: PCBPILOT_BENCH_DSN=<file.dsn> and
// PCBPILOT_BENCH_SES=<a session routed on it, used only as input, CLEANROOM
// §4>. The session is loaded into the DB, and the DB's union-find islands
// must equal those of pcbauto's referee (CheckDRCStrict's connectivity check)
// on the same copper, net by net.
func TestBenchConnectivity(t *testing.T) {
	dp, sp := os.Getenv("PCBPILOT_BENCH_DSN"), os.Getenv("PCBPILOT_BENCH_SES")
	if dp == "" || sp == "" {
		t.Skip("PCBPILOT_BENCH_DSN and PCBPILOT_BENCH_SES not set")
	}
	src, err := os.ReadFile(dp)
	if err != nil {
		t.Fatal(err)
	}
	ses, err := os.ReadFile(sp)
	if err != nil {
		t.Fatal(err)
	}
	bb, rb := board.NewBuilder(), rules.NewBuilder()
	if err := dsn.Read(src, bb, pcbGeneric{rb}); err != nil {
		t.Fatal(err)
	}
	rs, err := rb.Build()
	if err != nil {
		t.Fatal(err)
	}
	d, err := bb.Build(rs)
	if err != nil {
		t.Fatal(err)
	}
	open0 := 0
	for _, g := range islands(d) {
		open0 += len(g) - 1
	}
	if open0 != len(d.Connections()) {
		t.Errorf("before the session: %d open (Σ islands−1) but %d connections", open0, len(d.Connections()))
	}
	items, err := dsn.ReadSES(ses, d, rs)
	if err != nil {
		t.Fatal(err)
	}
	tx := d.Begin()
	for _, it := range items {
		tx.Add(it)
	}
	tx.Commit()

	ours := islands(d)
	b, tracks, vias, spans, boxed := toPcbauto(d, len(rs.Layers()))
	ref := map[string]pcbauto.Unrouted{}
	for _, u := range pcbauto.CheckDRCStrict(b, nil, nil, tracks, vias).Disconnected {
		ref[u.Net] = u
	}
	var oursOpen, refOpen, mismatch int
	for n := geom.NetID(1); int(n) <= d.NumNets(); n++ {
		name := d.NetName(n)
		groups := ours[n]
		u, open := ref[name]
		refN := 1
		if open {
			refN = atoi(strings.Fields(u.Reason)[0])
		}
		if len(groups) < 1 {
			continue
		}
		oursOpen += len(groups) - 1
		refOpen += refN - 1
		if len(groups) != refN || open && !restMatches(groups, u.Pads) {
			mismatch++
			t.Errorf("net %s: DB %d islands %v; referee %d islands, rest %v", name, len(groups), groups, refN, u.Pads)
		}
	}
	t.Logf("%s + %s: items=%d session items=%d connections=%d; referee pads: %d partial spans as all layers, %d shapes boxed",
		dp, sp, count(d), len(items), len(d.Connections()), spans, boxed)
	t.Logf("open connections (Σ islands−1): DB %d, referee %d; nets that differ: %d", oursOpen, refOpen, mismatch)
}

// pcbGeneric works around a reader/resolver mismatch found here: dsn.Read
// expands a pcb-scope (rule (clear …)) that also has typed values into
// per-pair values without a Generic entry, which rules.Book.Build rejects.
// Connectivity does not depend on clearances, so the bench restores a
// Generic value equal to the largest pcb-scope clearance.
type pcbGeneric struct{ *rules.Book }

func (p pcbGeneric) Set(s rules.Scope, rs rules.RuleSet) {
	if _, ok := rs.Clearance[rules.Generic]; s.Kind == rules.ScopePCB && len(rs.Clearance) > 0 && !ok {
		var v int64
		for _, c := range rs.Clearance {
			v = max(v, c)
		}
		rs.Clearance[rules.Generic] = v
	}
	p.Book.Set(s, rs)
}

// islands groups each net's pins (by Ref) into copper islands with
// View.Connected. Nets with fewer than two pins are left out, as the referee
// does.
func islands(d board.DB) map[geom.NetID][][]string {
	first := map[string]board.Item{}
	var order []string
	d.Items(func(it board.Item) bool {
		if it.Kind == board.Pad && it.Net != 0 {
			if _, ok := first[it.Ref]; !ok {
				first[it.Ref] = it
				order = append(order, it.Ref)
			}
		}
		return true
	})
	byNet := map[geom.NetID][]board.Item{}
	for _, ref := range order {
		it := first[ref]
		byNet[it.Net] = append(byNet[it.Net], it)
	}
	out := map[geom.NetID][][]string{}
	for n, pins := range byNet {
		if len(pins) < 2 {
			continue
		}
		var reps []board.Item
		var groups [][]string
	next:
		for _, p := range pins {
			for i, r := range reps {
				if d.Connected(r.ID, p.ID) {
					groups[i] = append(groups[i], p.Ref)
					continue next
				}
			}
			reps = append(reps, p)
			groups = append(groups, []string{p.Ref})
		}
		for _, g := range groups {
			slices.Sort(g)
		}
		out[n] = groups
	}
	return out
}

// restMatches reports whether the referee's "rest" pads (every pad outside
// its first island) are exactly the pins outside one of our islands.
func restMatches(groups [][]string, rest []string) bool {
	var all []string
	for _, g := range groups {
		all = append(all, g...)
	}
	got := make([]string, len(rest))
	for i, k := range rest {
		got[i] = strings.TrimSuffix(k, ".")
	}
	slices.Sort(got)
	for _, g := range groups {
		want := slices.DeleteFunc(slices.Clone(all), func(s string) bool { return slices.Contains(g, s) })
		slices.Sort(want)
		if slices.Equal(want, got) {
			return true
		}
	}
	return false
}

const nmPerMil = 25400.0

func mil(v int64) float64           { return float64(v) / nmPerMil }
func milPt(p geom.Pt) pcbauto.Point { return pcbauto.Point{X: mil(p.X), Y: mil(p.Y)} }
func pcbLayer(l geom.LayerID, n int) int {
	switch {
	case l == 0:
		return pcbauto.LayerTop
	case int(l) == n-1:
		return pcbauto.LayerBottom
	}
	return pcbauto.LayerInner1 + int(l) - 1
}

// toPcbauto converts the DB's copper to pcbauto's model in mil: one Pad per
// pin (its largest shape; LayerMulti when the pin spans every layer), tracks
// and through vias. Pins on some but not all layers count in spans; pin
// shapes that are not a Rect, Circle, capsule or 4-point rectangle are
// boxed and count in boxed.
func toPcbauto(d board.DB, nlayers int) (b *pcbauto.Board, tracks []pcbauto.Track, vias []pcbauto.Via, spans, boxed int) {
	b = &pcbauto.Board{CopperLayers: nlayers}
	pins := map[string][]board.Item{}
	var order []string
	d.Items(func(it board.Item) bool {
		switch it.Kind {
		case board.Pad:
			if _, ok := pins[it.Ref]; !ok {
				order = append(order, it.Ref)
			}
			pins[it.Ref] = append(pins[it.Ref], it)
		case board.Track:
			s := it.Shape.(geom.Seg)
			tracks = append(tracks, pcbauto.Track{Net: d.NetName(it.Net), Layer: pcbLayer(it.From, nlayers),
				A: milPt(s.A), B: milPt(s.B), Width: mil(2 * s.HalfW)})
		case board.Via:
			c := it.Shape.(geom.Circle)
			vias = append(vias, pcbauto.Via{Net: d.NetName(it.Net), C: milPt(c.C), Dia: mil(2 * c.R)})
		}
		return true
	})
	for _, ref := range order {
		its := pins[ref]
		lo, hi := its[0].From, its[0].To
		big := its[0]
		for _, it := range its[1:] {
			lo, hi = min(lo, it.From), max(hi, it.To)
			if area(it.Shape.Bounds()) > area(big.Shape.Bounds()) {
				big = it
			}
		}
		layer := pcbauto.LayerMulti
		if lo == hi {
			layer = pcbLayer(lo, nlayers)
		} else if lo != 0 || int(hi) != nlayers-1 {
			spans++
		}
		box, exact := orientedBox(big.Shape)
		if !exact {
			boxed++
		}
		b.Parts = append(b.Parts, &pcbauto.Part{Ref: ref, Pads: []*pcbauto.Pad{
			{Part: ref, Net: d.NetName(big.Net), Layer: layer, Box: box}}})
	}
	return b, tracks, vias, spans, boxed
}

func area(r geom.Rect) float64 { return float64(r.MaxX-r.MinX) * float64(r.MaxY-r.MinY) }

func orientedBox(s geom.Shape) (pcbauto.OrientedBox, bool) {
	switch s := s.(type) {
	case geom.Rect: // half-open: the closed pad ends at Max-1
		return pcbauto.OrientedBox{C: pcbauto.Point{X: mil(s.MinX+s.MaxX-1) / 2, Y: mil(s.MinY+s.MaxY-1) / 2},
			W: mil(s.MaxX - 1 - s.MinX), H: mil(s.MaxY - 1 - s.MinY)}, true
	case geom.Circle:
		return pcbauto.OrientedBox{C: milPt(s.C), W: mil(2 * s.R), H: mil(2 * s.R), Round: true}, true
	case geom.Seg:
		dx, dy := mil(s.B.X-s.A.X), mil(s.B.Y-s.A.Y)
		return pcbauto.OrientedBox{C: pcbauto.Point{X: mil(s.A.X+s.B.X) / 2, Y: mil(s.A.Y+s.B.Y) / 2},
			W: math.Hypot(dx, dy) + mil(2*s.HalfW), H: mil(2 * s.HalfW), Rot: math.Atan2(dy, dx) * 180 / math.Pi, Round: true}, true
	case geom.Poly:
		if p := s.Pts; len(p) == 4 {
			e0, e1 := milPt(p[1]).Sub(milPt(p[0])), milPt(p[2]).Sub(milPt(p[1]))
			if math.Abs(e0.X*e1.X+e0.Y*e1.Y) < 1e-6*(math.Hypot(e0.X, e0.Y)*math.Hypot(e1.X, e1.Y)+1e-12) {
				c := milPt(p[0]).Add(milPt(p[2])).Scale(0.5)
				return pcbauto.OrientedBox{C: c, W: math.Hypot(e0.X, e0.Y), H: math.Hypot(e1.X, e1.Y), Rot: math.Atan2(e0.Y, e0.X) * 180 / math.Pi}, true
			}
		}
	}
	r := s.Bounds()
	return pcbauto.OrientedBox{C: pcbauto.Point{X: mil(r.MinX+r.MaxX-1) / 2, Y: mil(r.MinY+r.MaxY-1) / 2},
		W: mil(r.MaxX - 1 - r.MinX), H: mil(r.MaxY - 1 - r.MinY)}, false
}

func count(v board.View) (n int) {
	v.Items(func(board.Item) bool { n++; return true })
	return n
}

func atoi(s string) (n int) {
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
