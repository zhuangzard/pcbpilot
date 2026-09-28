package pcbauto

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/pkg/safety"
)

func TestEdgeDefaults(t *testing.T) {
	for _, c := range []struct {
		kind                   string
		outer, inner, vcut, fb float64
	}{
		{"", 20, 30, 0, 8},
		{"routed", 20, 30, 0, 8},
		{"vcut", 20, 32, 20, 16},
		{"V-cut", 20, 32, 20, 16},
		{"mixed", 20, 32, 20, 16},
	} {
		o, i, v, f, why := EdgeDefaults(c.kind)
		if o != c.outer || i != c.inner || v != c.vcut || f != c.fb || len(why) == 0 {
			t.Errorf("%q: %v %v %v %v", c.kind, o, i, v, f)
		}
	}
	if _, err := NormEdgeKind("laser"); err == nil {
		t.Fatal("unknown edge kind accepted")
	}
	p := DefaultEdgePolicy("")
	// The live board rule is a floor, never a ceiling.
	p.RuleMil = 10
	if p.LayerReq(LayerTop) != 20 || p.LayerReq(LayerInner1) != 30 || p.LayerReq(LayerMulti) != 30 {
		t.Fatalf("layer req %v %v %v", p.LayerReq(LayerTop), p.LayerReq(LayerInner1), p.LayerReq(LayerMulti))
	}
	p.RuleMil = 40
	if p.LayerReq(LayerTop) != 40 {
		t.Fatal("a stricter live rule must win")
	}
}

// A hazardous domain keeps max(clearance, creepage) of reinforced
// insulation to the accessible edge — the pkg/safety numbers, not a
// constant.
func TestEdgeDomainDistanceFromSafety(t *testing.T) {
	st := safety.Standard{Name: "IEC62368-1", PollutionDegree: 2, MaterialGroup: "IIIa", AltitudeM: 2000, OvervoltageCategory: "II"}
	d, ok := EdgeDomainDistance("mains", 230, 325, st, "", "", 0)
	if !ok {
		t.Fatal("mains domain has no edge distance")
	}
	want := safety.Distances(safety.Pair{A: "domain", B: EdgeAccessible, WorkingVrms: 230, WorkingVpeak: 325, Insulation: "reinforced"}, st)
	wantMil := math.Ceil(math.Max(want.ClearanceMm, want.CreepageMm)/0.0254*10) / 10
	if d.Insulation != "reinforced" || d.Mil != wantMil || d.CreepageMm != want.CreepageMm || d.Mil <= EdgeOuterMil {
		t.Fatalf("mains edge %+v, want %.1f mil (%+v)", d, wantMil, want)
	}
	basic, _ := EdgeDomainDistance("mains", 230, 325, st, "basic", "", 0)
	if basic.Mil >= d.Mil {
		t.Fatalf("basic (enclosure provides the second MOP) %.1f ≥ reinforced %.1f", basic.Mil, d.Mil)
	}
	if _, ok := EdgeDomainDistance("SELV", 3.3, 3.3, st, "", "", 0); ok {
		t.Fatal("SELV domain must use the layer defaults")
	}
	// IPC-2221B is functional spacing: a touchable edge is judged under IEC 62368-1.
	ipc, _ := EdgeDomainDistance("hazardous", 400, 400, safety.Standard{Name: "IPC-2221B"}, "", "", 0)
	if ipc.Insulation != "reinforced" || ipc.Mil <= EdgeOuterMil {
		t.Fatalf("hazardous under IPC: %+v", ipc)
	}
}

func TestEdgeFromIntent(t *testing.T) {
	in := loadIntent(t, "iso-mains-selv.intent.json")
	b := mainsSelvBoard()
	b.Rules.EdgeClearance = 10
	p := EdgeFromIntent(in, b)
	if p.Source != "default" || p.ByDomain["MAINS"] == nil || p.ByDomain["SELV"] != nil {
		t.Fatalf("policy %+v", p)
	}
	m := p.ByDomain["MAINS"].Mil
	if p.NetReq("AC_L") != m || p.NetReq("LED_A") != m || p.NetReq("GND") != 0 || p.Req(LayerTop, "AC_L") != m || p.Req(LayerTop, "GND") != 20 {
		t.Fatalf("net reqs: AC_L %v GND %v", p.NetReq("AC_L"), p.NetReq("GND"))
	}
	// An intent "edge" field wins (V-cut, explicit domain distance).
	in.Edge = &IntentEdge{EdgeKind: "vcut", OuterMil: 25, ByDomain: map[string]*EdgeDomain{"MAINS": {Mil: 300, Insulation: "reinforced"}}}
	p = EdgeFromIntent(in, b)
	if p.Source != "intent" || p.Kind != EdgeVcut || p.LayerReq(LayerTop) != 25 || p.LayerReq(LayerInner1) != 32 || p.FabMinMil != 16 || p.NetReq("AC_N") != 300 {
		t.Fatalf("intent edge policy %+v", p)
	}
}

func TestInsetPolygon(t *testing.T) {
	sq := Rect{0, 0, 1000, 800}.Corners()
	in := InsetPolygon(sq, 30)
	if bb := PolyBounds(in); math.Abs(bb.MinX-30) > 1e-6 || math.Abs(bb.MaxY-770) > 1e-6 || len(in) != 4 {
		t.Fatalf("square inset %v", in)
	}
	// Rounded corners: radius 39.37 (the ESP32 board) > 30 keeps the arc;
	// radius 20 < 30 collapses the arc edges — both must stay ≥ d away.
	for _, r := range []float64{39.37, 20, 80} {
		for _, d := range []float64{20, 30, 60} {
			o := RoundedRect(Rect{0, 0, 1811.34, 1791.34}, r)
			got := InsetPolygon(o, d)
			if got == nil {
				t.Fatalf("r %.1f d %.1f: nil", r, d)
			}
			gap := polyEdgeGap(o, got)
			if gap < d-0.01 || gap > d+0.5 {
				t.Fatalf("r %.1f d %.1f: inset gap %.3f", r, d, gap)
			}
		}
	}
	// A concave (L-shaped) outline: the reflex corner is cut deeper, never shallower.
	L := []Point{{0, 0}, {1000, 0}, {1000, 400}, {400, 400}, {400, 1000}, {0, 1000}}
	got := InsetPolygon(L, 50)
	if got == nil || polyEdgeGap(L, got) < 50-0.01 {
		t.Fatalf("L inset %v", got)
	}
	if InsetPolygon(Rect{0, 0, 50, 50}.Corners(), 30) != nil {
		t.Fatal("a board narrower than 2d must leave no pour")
	}
	// Clip a rail rectangle poking past a rounded corner.
	bound := InsetPolygon(RoundedRect(Rect{0, 0, 1000, 1000}, 80), 20)
	clipped := ClipConvex(Rect{0, 0, 300, 300}.Corners(), bound)
	if clipped == nil || polyEdgeGap(RoundedRect(Rect{0, 0, 1000, 1000}, 80), clipped) < 20-0.01 {
		t.Fatalf("clipped rail %v", clipped)
	}
}

func TestEdgeBandCoversBand(t *testing.T) {
	for _, o := range [][]Point{
		RoundedRect(Rect{0, 0, 1811.34, 1791.34}, 39.37),
		{{0, 0}, {1000, 0}, {1000, 400}, {400, 400}, {400, 1000}, {0, 1000}},
	} {
		band := EdgeBand(o, 30)
		if len(band) == 0 {
			t.Fatal("no band")
		}
		// Every interior point within 30 mil of the edge is covered; points
		// ≥ 31 mil in are not.
		bb := PolyBounds(o)
		for x := bb.MinX + 1; x < bb.MaxX; x += 7 {
			for y := bb.MinY + 1; y < bb.MaxY; y += 7 {
				p := Point{x, y}
				if !PolyContains(o, p) {
					continue
				}
				d := PolyEdgeDist(o, p)
				in := false
				for _, q := range band {
					if PolyContains(q, p) {
						in = true
						break
					}
				}
				if d < 29.5 && !in {
					t.Fatalf("point %v (%.1f mil in) not covered", p, d)
				}
				if d > 31 && in {
					t.Fatalf("point %v (%.1f mil in) wrongly covered", p, d)
				}
			}
		}
		if len(o) > 20 && len(band) > 8 {
			t.Fatalf("rounded rectangle split into %d regions (want ≤ 8)", len(band))
		}
	}
}

// A poured fill whose ARC bulges toward the edge is measured on the arc,
// not on its chord.
func TestSourceContoursArcMeasured(t *testing.T) {
	// From (100,100) to (270,100) by a 180° counter-clockwise arc (the
	// connector's convention, same as flattenNetPathArc): it dips to y = 15.
	src := []any{100.0, 100.0, "ARC", 180.0, 270.0, 100.0, "L", 270.0, 400.0, 100.0, 400.0}
	cs := SourceContours(src)
	if len(cs) != 1 {
		t.Fatalf("contours %v", cs)
	}
	minY := math.Inf(1)
	for _, p := range cs[0] {
		minY = math.Min(minY, p.Y)
	}
	if math.Abs(minY-15) > 0.2 {
		t.Fatalf("arc lowest point y %.2f, want 15 (a CCW sweep left→right bulges below the chord)", minY)
	}
	it := edgeItem{kind: "pour", polys: cs, layers: []int{1}}
	d, _ := it.edgeDist(Rect{0, 0, 1000, 1000}.Corners())
	if math.Abs(d-15) > 0.2 {
		t.Fatalf("pour-to-edge %.2f, want 15", d)
	}
}

// The live ESP32 v0.5 board (save + reload dump): TOP/BOTTOM GND and the
// IN2 power zones poured 14.1 mil from the routed edge, and IN1 a negative
// GND plane pulled back only by the host's 10 mil rule. The rule must
// fire on every one of them.
func TestCheckEdgeFiresOnLiveESP32Dump(t *testing.T) {
	raw, err := os.ReadFile("testdata/esp32-v05-live.reload.json")
	if err != nil {
		t.Fatal(err)
	}
	chk, err := CheckEdgeSnapshot(raw, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	min := map[int]float64{}
	for _, l := range chk.Layers {
		min[l.Layer] = l.MinMil
	}
	if math.Abs(min[1]-14.1) > 0.05 || math.Abs(min[2]-14.1) > 0.05 || math.Abs(min[16]-14.1) > 0.05 || min[15] != 10 {
		t.Fatalf("per-layer minima %v", min)
	}
	errs := map[string]int{}
	for _, f := range chk.Findings {
		if f.Level == "ERROR" {
			errs[sprintf("%s L%d %s", f.Rule, f.Layer, f.Kind)]++
		}
	}
	for _, want := range []string{"copper-to-edge L1 pour", "copper-to-edge L2 pour", "copper-to-edge L16 pour", "plane-pullback L15 plane"} {
		if errs[want] == 0 {
			t.Fatalf("missing %s: %v", want, errs)
		}
	}
	// Mounting holes: the GND pours stop at the screw keep-out rings.
	if chk.HoleMin == nil || chk.HoleMin.MinMil < 20 {
		t.Fatalf("hole min %+v", chk.HoleMin)
	}
	for _, f := range chk.Findings {
		if f.Rule == "copper-to-hole" {
			t.Fatalf("unexpected hole finding %s", f.Message)
		}
	}
	// V-cut raises the outer requirement floor to 16 mil fab / 20 mil design.
	v, err := CheckEdgeSnapshot(raw, nil, "vcut")
	if err != nil || v.Policy.Kind != EdgeVcut || v.Policy.LayerReq(LayerInner1) != 32 {
		t.Fatalf("vcut policy %+v %v", v.Policy, err)
	}
}

// A fixed board (the engine's plan with the new defaults) passes.
func TestCheckEdgePassesOnFixedPlan(t *testing.T) {
	raw, err := os.ReadFile("testdata/esp32-v05-fixed.routed.json")
	if err != nil {
		t.Fatal(err)
	}
	chk, err := CheckEdgeSnapshot(raw, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range chk.Findings {
		t.Errorf("%s %s", f.Level, f.Message)
	}
	for _, l := range chk.Layers {
		if !l.Measured || l.MinMil < l.RequiredMil-0.05 {
			t.Fatalf("layer %d min %.2f < %.1f", l.Layer, l.MinMil, l.RequiredMil)
		}
	}
}

// The router keeps the per-layer edge band: outer copper ≥ 20 mil, inner
// (and every via, which reaches the inner layers) ≥ 30 mil; the planes are
// inset; the playbook pulls negative planes back with a
// no-inner-electrical band.
func TestEngineKeepsEdgeDistance(t *testing.T) {
	b := mainsSelvBoard()
	in := loadIntent(t, "iso-mains-selv.intent.json")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := Run(ctx, b, Options{Power: PowerSpec{Intent: in}, Stack: StackOptions{Force: 4}, Route: RouteOptions{Timeout: 20 * time.Second}, NoEscalate: true})
	if err != nil {
		t.Fatal(err)
	}
	pol := res.Analysis.Edge
	mains := pol.ByDomain["MAINS"].Mil
	for _, tr := range res.Route.Tracks {
		d := outlineSegDist(b.Outline, tr.A, tr.B) - tr.Width/2
		if req := pol.Req(tr.Layer, tr.Net); d < req-0.1 {
			t.Fatalf("track %s L%d %.1f mil from the edge < %.1f", tr.Net, tr.Layer, d, req)
		}
		if pol.NetReq(tr.Net) == mains && d < mains-0.1 {
			t.Fatalf("mains track %.1f < %.1f", d, mains)
		}
	}
	for _, v := range res.Route.Vias {
		if d := PolyEdgeDist(b.Outline, v.C) - v.Dia/2; d < pol.Req(LayerInner1, v.Net)-0.1 {
			t.Fatalf("via %s %.1f mil from the edge", v.Net, d)
		}
	}
	for _, pr := range res.Route.Planes {
		for _, poly := range pr.Polys {
			if g := polyEdgeGap(b.Outline, poly); g < pol.Req(pr.Layer, pr.Net)-0.01 {
				t.Fatalf("plane %s L%d %.2f mil from the edge < %.1f", pr.Net, pr.Layer, g, pol.Req(pr.Layer, pr.Net))
			}
		}
	}
	if res.Edge == nil || res.Edge.Errors() != 0 {
		t.Fatalf("plan edge check %+v", res.Edge)
	}
	for _, v := range res.DRC.Violations {
		if v.Kind == "edge" {
			t.Fatalf("edge DRC %+v", v)
		}
	}
	pb := BuildPlaybook(PlaybookInput{Board: b, Result: res, Name: "edge"})
	band := 0
	for _, st := range pb.Steps {
		if strings.HasPrefix(st.ID, "plane-edge-") {
			band++
			if rt, _ := st.Payload["ruleType"].([]string); len(rt) != 1 || rt[0] != "no-inner-electrical" {
				t.Fatalf("band rule %v", st.Payload["ruleType"])
			}
		}
	}
	hasPlane := false
	for _, l := range res.Stackup.Stack {
		hasPlane = hasPlane || l.Kind == KindPlane
	}
	if hasPlane && band == 0 {
		t.Fatal("negative planes without a no-inner-electrical edge band")
	}
}

// Engine DRC flags copper in the band (per layer class and per domain).
func TestDRCEdgePerLayerAndDomain(t *testing.T) {
	b := mainsSelvBoard()
	in := loadIntent(t, "iso-mains-selv.intent.json")
	an := Analyze(b, PowerSpec{Intent: in}, nil)
	st := &Stackup{Layers: 4, Stack: []StackLayer{{ID: 1, Kind: KindSignal}, {ID: 15, Kind: KindPlane, Nets: []string{"GND"}}, {ID: 16, Kind: KindSignal}, {ID: 2, Kind: KindSignal}}}
	tracks := []Track{
		{Net: "SDA", Layer: 1, A: Point{2900, 1975}, B: Point{2950, 1975}, Width: 10},  // 20 mil copper gap: OK outer
		{Net: "SCL", Layer: 16, A: Point{2900, 1975}, B: Point{2950, 1975}, Width: 10}, // 20 < 30 inner: edge
		{Net: "AC_L", Layer: 1, A: Point{100, 60}, B: Point{150, 60}, Width: 20},       // 50 mil < mains: edge
	}
	rep := CheckDRC(b, an, st, tracks, nil)
	var edges []string
	for _, v := range rep.Violations {
		if v.Kind == "edge" {
			edges = append(edges, v.NetA)
		}
	}
	if strings.Join(edges, ",") != "SCL,AC_L" {
		t.Fatalf("edge violations %v", edges)
	}
}
