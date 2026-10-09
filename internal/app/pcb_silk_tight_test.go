package app

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"testing"
)

// applyPlanToSnap writes the planned boxes back as the silk readback would
// show them (offline stand-in for the live round trip).
func applyPlanToSnap(snap *boardSnapshot, placed []silkPlaced, font float64) {
	by := map[string]silkPlaced{}
	for _, p := range placed {
		by[p.ID] = p
	}
	for i, t := range snap.Silk {
		if p, ok := by[t.ID]; ok && p.Moved {
			b := pcbRect(p.Box)
			snap.Silk[i].BBox = &b
			snap.Silk[i].Rotation = float64(p.Rot)
			if t.FontSize < font {
				snap.Silk[i].FontSize = font
			}
		}
	}
}

// PCBPILOT_SILK_DUMP=board.json (a dump with silk, e.g. pcb auto run's
// board-start.json): plan, apply offline, gate.
func TestSilkTightDump(t *testing.T) {
	path := os.Getenv("PCBPILOT_SILK_DUMP")
	if path == "" {
		t.Skip("PCBPILOT_SILK_DUMP not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snap boardSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PCBPILOT_SILK_RESET") != "" {
		// An exported placement leaves the designators where they were;
		// live, they move with their parts. Park them off the board so
		// none is kept or blocks another.
		for i, t := range snap.Silk {
			if isVisibleDesignator(t) {
				b := *t.BBox
				b.MinX, b.MaxX = b.MinX+100000, b.MaxX+100000
				snap.Silk[i].BBox = &b
			}
		}
	}
	opt := defaultSilkTightOpts()
	labels, sc, font := silkTightInput(&snap, opt)
	before := silkGate(&snap, font, opt)
	placed, notes := planSilkTight(labels, sc, opt)
	how := map[string]int{}
	for _, p := range placed {
		how[strings.SplitN(p.How, "+", 2)[0]+map[bool]string{true: "+rot", false: ""}[strings.HasSuffix(p.How, "rotated")]]++
	}
	groups, placed, gnotes := planSilkGroups(labels, sc, placed, opt)
	notes = append(notes, gnotes...)
	applyPlanToSnap(&snap, placed, font)
	hide := map[string]bool{}
	for _, g := range groups {
		for _, id := range g.IDs {
			hide[id] = true
		}
		b := pcbRect(g.Box)
		snap.Silk = append(snap.Silk, pcbSilkText{ID: "g" + g.Text, Kind: "string", Text: g.Text, Layer: g.Layer, FontSize: font, BBox: &b})
	}
	for i := range snap.Silk {
		if hide[snap.Silk[i].ID] {
			snap.Silk[i].Hidden = true
		}
	}
	after := silkGate(&snap, font, opt)
	t.Logf("labels %d font %.1f; before: %s; after: %s; how %v", len(labels), font, before.Detail, after.Detail, how)
	for _, n := range notes {
		t.Log("note:", n)
	}
	for i, it := range after.Items {
		if i < 15 {
			t.Log("after:", it)
		}
	}
}

// silkTestBoard: 2000×2000 board, parts given as (ref, bbox, pads) with a
// 45 mil designator ("Rn" ≈ 70×45) parked far away.
func silkTestBoard(parts ...boardComp) *boardSnapshot {
	snap := &boardSnapshot{Outline: &boardOutline{BBox: layoutBBox{0, 0, 2000, 2000}}}
	for i, c := range parts {
		c.ID = "c" + c.Designator
		c.Layer = pcbSideTop
		snap.Components = append(snap.Components, c)
		b := pcbRect{MinX: 1800, MinY: float64(100 + 60*i), MaxX: 1800 + 22*float64(len(c.Designator)), MaxY: float64(145 + 60*i)}
		snap.Silk = append(snap.Silk, pcbSilkText{ID: "s" + c.Designator, Kind: "attribute", Key: "Designator", Text: c.Designator,
			Layer: silkTopLayer, FontSize: 45, LineWidth: 6, CompID: c.ID, BBox: &b, X: b.MinX, Y: b.MinY})
	}
	return snap
}

func part0603(ref string, cx, cy float64) boardComp {
	return boardComp{Designator: ref, BBox: &layoutBBox{cx - 60, cy - 33, cx + 60, cy + 33},
		Pads: []boardPad{{Layer: pcbSideTop, X: cx - 30, Y: cy, W: 31, H: 35}, {Layer: pcbSideTop, X: cx + 30, Y: cy, W: 31, H: 35}}}
}

func planOf(t *testing.T, snap *boardSnapshot) (map[string]silkPlaced, []string, float64) {
	t.Helper()
	opt := defaultSilkTightOpts()
	labels, sc, font := silkTightInput(snap, opt)
	placed, notes := planSilkTight(labels, sc, opt)
	m := map[string]silkPlaced{}
	for _, p := range placed {
		m[p.Ref] = p
	}
	return m, notes, font
}

// A lone part gets its label on the first side at the 5 mil gap.
func TestSilkTightNearestSide(t *testing.T) {
	snap := silkTestBoard(part0603("R1", 1000, 1000))
	m, _, _ := planOf(t, snap)
	p := m["R1"]
	own := silkBox{940, 967, 1060, 1033}
	if p.How != "top" || p.Rot != 0 || math.Abs(p.Box.dist(own)-5) > 1e-6 {
		t.Fatalf("R1 %+v", p)
	}
	applyPlanToSnap(snap, []silkPlaced{p}, 45)
	if g := silkGate(snap, 45, defaultSilkTightOpts()); !g.Pass {
		t.Fatalf("gate: %+v", g)
	}
}

// Neighbours above and below leave no room for the 88 mil "R101" lying
// down, the side gaps are too narrow for it but wide enough standing: the
// label turns 90° (reads from the right) beside the part.
func TestSilkTightRotates(t *testing.T) {
	snap := silkTestBoard(part0603("R101", 1000, 1000), part0603("R2", 1000, 1072), part0603("R3", 1000, 928),
		part0603("R8", 815, 1000), part0603("R9", 1185, 1000))
	m, _, _ := planOf(t, snap)
	if p := m["R101"]; p.Rot != 90 || !strings.HasSuffix(p.How, "rotated") {
		t.Fatalf("R101 %+v", p)
	}
}

// A tight row of caps (pitch 80 mil < label length): the labels go as an
// ordered row beside the group, each over its own cap.
func TestSilkTightGroupRow(t *testing.T) {
	var ps []boardComp
	for i := 0; i < 5; i++ {
		c := part0603(fmt.Sprintf("C%d", 21+i), 700+80*float64(i), 1000)
		c.BBox = &layoutBBox{c.BBox.MinX + 25, c.BBox.MinY, c.BBox.MaxX - 25, c.BBox.MaxY}
		c.Pads = []boardPad{{Layer: pcbSideTop, X: 700 + 80*float64(i), Y: 985, W: 30, H: 25}, {Layer: pcbSideTop, X: 700 + 80*float64(i), Y: 1015, W: 30, H: 25}}
		ps = append(ps, c)
	}
	// a wall of parts right below the row
	for i := 0; i < 5; i++ {
		ps = append(ps, part0603(fmt.Sprintf("U%d", i+1), 700+130*float64(i)-40, 900))
	}
	snap := silkTestBoard(ps...)
	m, notes, _ := planOf(t, snap)
	for i := 0; i < 5; i++ {
		ref := fmt.Sprintf("C%d", 21+i)
		p := m[ref]
		if p.How == "unresolved" {
			t.Fatalf("%s unresolved: %v", ref, notes)
		}
		if p.Rot == 90 && math.Abs(p.Box.cx()-(700+80*float64(i))) > 1e-6 {
			t.Fatalf("%s not over its cap: %+v", ref, p)
		}
	}
	var got []silkPlaced
	for _, p := range m {
		got = append(got, p)
	}
	applyPlanToSnap(snap, got, 45)
	if g := silkGate(snap, 45, defaultSilkTightOpts()); !g.Pass {
		t.Fatalf("gate: %+v", g.Items)
	}
}

// Text is never shrunk below the fab minimum: a 50 mil label keeps 50; a
// 40 mil one is planned at the project size (45, the most common) but the
// gate accepts it down to MinFont (JLC 0.8 mm = 31.5 mil) with an info line;
// below that it fails.
func TestSilkTightFontFloor(t *testing.T) {
	snap := silkTestBoard(part0603("R1", 500, 500), part0603("R2", 1000, 1000), part0603("R3", 1400, 1400), part0603("R4", 300, 1400))
	snap.Silk[0].FontSize = 50
	snap.Silk[2].FontSize = 40
	opt := defaultSilkTightOpts()
	labels, _, font := silkTightInput(snap, opt)
	if font != 45 {
		t.Fatalf("project font %v", font)
	}
	if labels[0].Hgt != 45 || labels[2].Hgt <= 45 {
		t.Fatalf("sizes %+v %+v", labels[0], labels[2])
	}
	g := silkGate(snap, font, opt)
	if strings.Contains(strings.Join(g.Items, "\n"), "mil below") || !strings.Contains(strings.Join(g.Info, "\n"), "R3 text 40.0 mil: below the project size") {
		t.Fatalf("40 mil ≥ MinFont must pass with info: items %v info %v", g.Items, g.Info)
	}
	snap.Silk[2].FontSize = 30
	g = silkGate(snap, font, opt)
	if g.Pass || !strings.Contains(strings.Join(g.Items, "\n"), "below the fab minimum 31.5") {
		t.Fatalf("30 mil < MinFont must fail: %+v", g.Items)
	}
	opt.MinFont = 0
	snap.Silk[2].FontSize = 40
	if g = silkGate(snap, font, opt); !strings.Contains(strings.Join(g.Items, "\n"), "below the project size") {
		t.Fatalf("MinFont 0 = never shrink: %+v", g.Items)
	}
}

// A label with no slot at the project size but one at the fab minimum is
// planned there (+small, Font = MinFont); never smaller.
func TestSilkTightShrinksToFabMinimum(t *testing.T) {
	// R1 (940..1060 × 967..1033) sits in a 48 mil moat: walls of other
	// parts on all four sides. The 45 mil label (44×45) needs 5 + 45; at
	// 31.5 mil it needs 5 + 31.5.
	wall := func(ref string, x0, y0, x1, y1 float64) boardComp {
		return boardComp{Designator: ref, BBox: &layoutBBox{x0, y0, x1, y1}}
	}
	snap := silkTestBoard(part0603("R1", 1000, 1000), wall("U1", 800, 1081, 1200, 1200), wall("U2", 800, 800, 1200, 919),
		wall("U3", 700, 919, 892, 1081), wall("U4", 1108, 919, 1300, 1081))
	opt := defaultSilkTightOpts()
	labels, sc, _ := silkTightInput(snap, opt)
	placed, notes := planSilkTight(labels, sc, opt)
	var p silkPlaced
	for _, q := range placed {
		if q.Ref == "R1" {
			p = q
		}
	}
	if p.Font != opt.MinFont || !strings.HasSuffix(p.How, "+small") || p.Box.h()+p.Box.w() > 1.01*(labels[0].Len+labels[0].Hgt)*opt.MinFont/45 {
		t.Fatalf("R1 %+v (notes %v)", p, notes)
	}
	opt.MinFont = 0
	placed, _ = planSilkTight(labels, sc, opt)
	for _, q := range placed {
		if q.Font != 0 {
			t.Fatalf("MinFont 0 shrank %+v", q)
		}
	}
}

// The gate reads the readback: overlap, pad, outside, distance, stroke.
func TestSilkGateFailures(t *testing.T) {
	snap := silkTestBoard(part0603("R1", 1000, 1000), part0603("R2", 1300, 1000))
	set := func(i int, b pcbRect) { snap.Silk[i].BBox = &b }
	set(0, pcbRect{MinX: 980, MinY: 990, MaxX: 1050, MaxY: 1035}) // over R1's pad
	set(1, pcbRect{MinX: 1960, MinY: 500, MaxX: 2030, MaxY: 545}) // off board, far from R2
	snap.Silk[1].LineWidth = 4
	g := silkGate(snap, 45, defaultSilkTightOpts())
	all := strings.Join(g.Items, "\n")
	for _, want := range []string{"R1 (1015.0,1012.5) overlaps a pad", "R2 (1995.0,522.5) crosses the board edge", "from its footprint", "stroke 4.00 mil below"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	if g.Pass {
		t.Fatal("gate passed")
	}
}

func TestSilkGroupLabel(t *testing.T) {
	ls := func(refs ...string) []silkLabel {
		var o []silkLabel
		for _, r := range refs {
			o = append(o, silkLabel{Ref: r})
		}
		return o
	}
	if g := groupLabel(ls("C24", "C21", "C22", "C23")); g != "C21–C24" {
		t.Fatal(g)
	}
	if g := groupLabel(ls("R15", "R13", "R14", "R17")); g != "R13/R14/R15/R17" {
		t.Fatal(g)
	}
}

// placeGroup lines a cluster's labels up beside the row, each centred on its
// own part, in part order.
func TestSilkPlaceGroupAligned(t *testing.T) {
	var ps []boardComp
	for i := 0; i < 4; i++ {
		ps = append(ps, part0603(fmt.Sprintf("C%d", 21+i), 700+60*float64(i), 1000))
	}
	snap := silkTestBoard(ps...)
	opt := defaultSilkTightOpts()
	labels, sc, _ := silkTightInput(snap, opt)
	ms := silkCluster(labels[2], labels)
	if refsOf(ms) != "C21,C22,C23,C24" {
		t.Fatalf("cluster %s", refsOf(ms))
	}
	layerOf := map[string]int{}
	for _, l := range labels {
		layerOf[l.ID] = l.Layer
	}
	grp, ok := placeGroup(ms, sc, nil, layerOf, opt)
	if !ok || len(grp) != 4 {
		t.Fatalf("group %v %+v", ok, grp)
	}
	for i, p := range grp {
		if p.How != "group-row" || p.Rot != 90 || math.Abs(p.Box.cx()-(700+60*float64(i))) > 1e-6 {
			t.Fatalf("%d %+v", i, p)
		}
	}
}

func TestSilkGroupText(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"R65", "R62", "R63"}, "R62/R63/R65"},
		{[]string{"C24", "C22", "C23", "C21"}, "C21–C24"},
		{[]string{"R63", "C72", "R62", "C52"}, "C52/C72/R62/R63"},
	} {
		got := groupText(c.in)
		if got != c.want {
			t.Errorf("groupText(%v) = %q, want %q", c.in, got, c.want)
		}
		back := groupRefs(got)
		sort.Strings(back)
		in := append([]string{}, c.in...)
		sort.Strings(in)
		if strings.Join(back, ",") != strings.Join(in, ",") {
			t.Errorf("groupRefs(%q) = %v", got, back)
		}
	}
	for _, s := range []string{"VCC", "R1", "LOGO v2", "C1-X3"} {
		if groupRefs(s) != nil {
			t.Errorf("groupRefs(%q) = %v", s, groupRefs(s))
		}
	}
}

// Two parts boxed in on every side within 30 mil get one group label within
// 80 mil; their designators are hidden. The gate accepts the hidden ones
// named by the label and fails one hidden with no label.
func TestSilkGroupDrawnAndGate(t *testing.T) {
	ps := []boardComp{part0603("R62", 1000, 1000), part0603("R63", 1000, 1070)}
	// a ring of big parts 12 mil around the pair
	for _, w := range [][4]float64{{870, 925, 1130, 961}, {870, 1109, 1130, 1145}, {870, 961, 928, 1109}, {1072, 961, 1130, 1109}} {
		ps = append(ps, boardComp{Designator: fmt.Sprintf("U%d", len(ps)), BBox: &layoutBBox{w[0], w[1], w[2], w[3]}})
	}
	snap := silkTestBoard(ps...)
	opt := defaultSilkTightOpts()
	labels, sc, font := silkTightInput(snap, opt)
	placed, _ := planSilkTight(labels, sc, opt)
	groups, rest, notes := planSilkGroups(labels, sc, placed, opt)
	if len(groups) != 1 || groups[0].Text != "R62/R63" && groups[0].Text != "R62–R63" {
		t.Fatalf("groups %+v notes %v", groups, notes)
	}
	g := groups[0]
	if d := g.Box.dist(g.Members); d > opt.GroupMaxDist || d < opt.MaxDist {
		t.Fatalf("group label %.1f mil from its parts", d)
	}
	for _, p := range rest {
		if p.Ref == "R62" || p.Ref == "R63" {
			t.Fatalf("member still placed: %+v", p)
		}
	}
	// Readback after apply: members hidden, the group string drawn.
	applyPlanToSnap(snap, rest, font)
	for i := range snap.Silk {
		if snap.Silk[i].Text == "R62" || snap.Silk[i].Text == "R63" {
			snap.Silk[i].Hidden = true
		}
	}
	b := pcbRect(g.Box)
	snap.Silk = append(snap.Silk, pcbSilkText{ID: "grp", Kind: "string", Text: g.Text, Layer: silkTopLayer, FontSize: 45, LineWidth: 6, BBox: &b})
	gr := silkGate(snap, font, opt)
	if !gr.Pass || len(gr.Info) != 2 {
		t.Fatalf("gate %+v", gr)
	}
	// A hidden designator no label names fails.
	snap.Silk = snap.Silk[:len(snap.Silk)-1]
	if gr := silkGate(snap, font, opt); gr.Pass || !strings.Contains(strings.Join(gr.Items, "\n"), "R62 (1000.0,1000.0) designator hidden") {
		t.Fatalf("gate without group label %+v", gr.Items)
	}
}

// The gate's items do not depend on the order the host lists silk in.
func TestSilkGateDeterministic(t *testing.T) {
	snap := silkTestBoard(part0603("R1", 1000, 1000), part0603("R2", 1300, 1000), part0603("R3", 1600, 1000))
	for i := range snap.Silk {
		b := pcbRect{MinX: 1960, MinY: float64(500 + 10*i), MaxX: 2030, MaxY: float64(545 + 10*i)} // all off the edge, overlapping
		snap.Silk[i].BBox = &b
	}
	a := silkGate(snap, 45, defaultSilkTightOpts())
	for i, j := 0, len(snap.Silk)-1; i < j; i, j = i+1, j-1 {
		snap.Silk[i], snap.Silk[j] = snap.Silk[j], snap.Silk[i]
	}
	b := silkGate(snap, 45, defaultSilkTightOpts())
	if strings.Join(a.Items, "\n") != strings.Join(b.Items, "\n") {
		t.Fatalf("order-dependent:\n%s\n--\n%s", strings.Join(a.Items, "\n"), strings.Join(b.Items, "\n"))
	}
}
