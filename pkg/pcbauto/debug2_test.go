package pcbauto

import (
	"os"
	"testing"
	"time"
)

func TestDebugAccess(t *testing.T) {
	if os.Getenv("PCBAUTO_DEBUG") == "" {
		t.Skip()
	}
	b := loadFixture(t, os.Getenv("PCBAUTO_DEBUG"))
	_, an, res, _ := runPipeline(t, b, b.CopperLayers, 60*time.Second)
	seen := 0
	for _, u := range res.Unrouted {
		if u.Reason != "pad-inaccessible" || seen > 12 {
			continue
		}
		seen++
		for _, k := range u.Pads {
			var pd *Pad
			for _, p := range b.Parts {
				for _, q := range p.Pads {
					if q.Key() == k {
						pd = q
					}
				}
			}
			if pd != nil {
				p := an.Plan(u.Net, b.Rules)
				t.Logf("%s %s layer=%d box=%.1fx%.1f rot=%.0f round=%v | net w=%.1f clr=%.1f role=%s", u.Net, k, pd.Layer, pd.Box.W, pd.Box.H, pd.Box.Rot, pd.Box.Round, p.WidthMil, p.ClearanceMil, p.Role)
			}
		}
	}
	t.Logf("grid %.1f mil, bounds %+v", res.Stats.GridMil, b.Bounds())
}

func TestDebugPadAccess(t *testing.T) {
	if os.Getenv("PCBAUTO_DEBUG") == "" || os.Getenv("PCBAUTO_PAD") == "" {
		t.Skip()
	}
	b := loadFixture(t, os.Getenv("PCBAUTO_DEBUG"))
	pre := Analyze(b, PowerSpec{}, nil)
	st := DecideStackup(b, pre, StackOptions{Force: b.CopperLayers})
	an := Analyze(b, PowerSpec{}, st)
	var got *router
	auditHook = func(phase string, r *router) {
		if phase == "fanout" {
			got = r
		}
	}
	defer func() { auditHook = nil }()
	_, _ = Route(t.Context(), b, st, an, RouteOptions{Timeout: 5 * time.Second})
	r := got
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			if pd.Key() != os.Getenv("PCBAUTO_PAD") {
				continue
			}
			n := r.byName[pd.Net]
			li := r.gr.layerIndex(pd.Layer)
			x, y := r.gr.cellOf(pd.Box.C)
			t.Logf("pad %s net=%s w=%.1f radius=%.1f neckR=%.1f share=%.1f", pd.Key(), n.name, n.width, n.radius, n.neckR, n.share)
			offs, inner := r.gr.ring(n.radius)
			for k, o := range offs[:inner] {
				j := r.gr.idx(li, x+o[0], y+o[1])
				if r.gr.flags[j] != 0 || (r.gr.pad[j] != -1 && r.gr.pad[j] != n.id) {
					t.Logf("inner %d off=%v flags=%d pad=%d (own %d)", k, o, r.gr.flags[j], r.gr.pad[j], n.id)
				}
			}
			c := r.gr.center(x, y)
			bx, by := int((c.X-r.gr.ox)/padBucket), int((c.Y-r.gr.oy)/padBucket)
			for _, e := range r.pbuckets[by*r.pbW+bx] {
				if e.net != n.id && e.pd.OnLayer(pd.Layer) && e.pd.Box.Dist(c) < 11 {
					t.Logf("blocker %s net=%s box=%+v dist=%.2f", e.pd.Key(), e.pd.Net, e.pd.Box, e.pd.Box.Dist(c))
				}
			}
			t.Logf("center=%+v pad=%+v", c, pd.Box)
			t.Logf("padsClear=%v nodeRadius=%.1f", r.padsClear(n, pd.Layer, r.gr.center(x, y), n.width/2), r.nodeRadius(n, li, x, y))
		}
	}
}

func TestDebugPreRepair(t *testing.T) {
	if os.Getenv("PCBAUTO_DEBUG") == "" {
		t.Skip()
	}
	b := loadFixture(t, os.Getenv("PCBAUTO_DEBUG"))
	pre := Analyze(b, PowerSpec{}, nil)
	st := DecideStackup(b, pre, StackOptions{Force: b.CopperLayers})
	an := Analyze(b, PowerSpec{}, st)
	res, _ := Route(t.Context(), b, st, an, RouteOptions{Timeout: 4 * time.Minute, NoRepair: true})
	drc := CheckDRC(b, an, st, res.Tracks, res.Vias)
	t.Logf("pre-repair %v", drc.ByKind)
	idx := map[string][]Track{}
	for _, tr := range res.Tracks {
		idx[tr.Net] = append(idx[tr.Net], tr)
	}
	for i, v := range drc.Violations {
		if i >= 6 {
			break
		}
		t.Logf("%+v", v)
		for _, net := range []string{v.NetA, v.NetB} {
			for _, tr := range idx[net] {
				if PointSegDist(v.At, tr.A, tr.B) < 30 && tr.Layer == v.Layer {
					t.Logf("   %s L%d %s (%.1f,%.1f)-(%.1f,%.1f) w=%.1f", net, tr.Layer, tr.Kind, tr.A.X, tr.A.Y, tr.B.X, tr.B.Y, tr.Width)
				}
			}
		}
	}
}

func TestDebugOverlaps(t *testing.T) {
	if os.Getenv("PCBAUTO_DEBUG") == "" {
		t.Skip()
	}
	b := loadFixture(t, os.Getenv("PCBAUTO_DEBUG"))
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	if _, err := Place(b, an, c, nil, PlaceOptions{}); err != nil {
		t.Fatal(err)
	}
	for i, p := range b.Parts {
		for _, q := range b.Parts[i+1:] {
			if collide(p, q) && p.Body().OverlapArea(q.Body()) > 1 {
				t.Logf("overlap %s(fixed=%v side=%d) %s(fixed=%v side=%d) area=%.0f", p.Ref, p.Fixed, p.Side, q.Ref, q.Fixed, q.Side, p.Body().OverlapArea(q.Body()))
			}
		}
	}
}

func TestDebugStuck(t *testing.T) {
	if os.Getenv("PCBAUTO_DEBUG") == "" {
		t.Skip()
	}
	b := loadFixture(t, os.Getenv("PCBAUTO_DEBUG"))
	out, _ := Run(t.Context(), b, Options{Stack: StackOptions{Force: b.CopperLayers}, Route: RouteOptions{Timeout: time.Minute}})
	for _, u := range out.Route.Unrouted {
		t.Logf("unrouted %+v", u)
		for _, k := range u.Pads {
			for _, p := range b.Parts {
				for _, pd := range p.Pads {
					if pd.Key() == k {
						t.Logf("  pad %s layer=%d box=%+v", k, pd.Layer, pd.Box)
					}
				}
			}
		}
	}
	for _, n := range out.Route.Notes {
		t.Logf("note %s", n)
	}
}
