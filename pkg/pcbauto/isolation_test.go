package pcbauto

import (
	"context"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func loadIntent(t *testing.T, name string) *Intent {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	in, err := ParseIntent(raw)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// mainsSelvBoard is a synthetic 230 Vac → SELV board: a mains terminal,
// fuse and opto LED resistor on the mains side, a transformer and an
// optocoupler across the barrier, an MCU on the SELV side. The opto's pad
// rows are 120 mil (3.05 mm) apart face to face: enough for the reinforced
// clearance (3.0 mm) but short of the reinforced creepage (4.6 mm), so it
// needs a slot; the transformer's rows (330 mil) do not.
func mainsSelvBoard() *Board {
	mk := func(ref, dev string, at Point, fixed bool, pads ...*Pad) *Part {
		p := &Part{Ref: ref, Device: dev, Pos: at, Fixed: fixed}
		for _, pd := range pads {
			pd.Box.C = pd.Box.C.Add(at)
		}
		p.Pads = pads
		return p
	}
	smd := func(n, net string, x, y, w, h float64) *Pad {
		return &Pad{Number: n, Net: net, Layer: LayerTop, Box: OrientedBox{C: Point{x, y}, W: w, H: h}}
	}
	tht := func(n, net string, x, y, d float64) *Pad {
		return &Pad{Number: n, Net: net, Layer: LayerMulti, Box: OrientedBox{C: Point{x, y}, W: d, H: d, Round: true}}
	}
	b := &Board{Rules: DefaultRules(), Outline: Rect{0, 0, 3000, 2000}.Corners(), CopperLayers: 2}
	b.Parts = []*Part{
		mk("J1", "KF301-2P", Point{250, 1000}, true, tht("1", "AC_L", 0, -100, 80), tht("2", "AC_N", 0, 100, 80)),
		mk("F1", "FUSE", Point{600, 1300}, false, smd("1", "AC_L", -90, 0, 60, 80), smd("2", "L_F", 90, 0, 60, 80)),
		mk("R1", "100k", Point{600, 700}, false, smd("1", "L_F", -50, 0, 40, 50), smd("2", "LED_A", 50, 0, 40, 50)),
		mk("T1", "EE13", Point{1300, 1400}, false,
			tht("1", "L_F", -200, -100, 70), tht("2", "AC_N", -200, 100, 70), tht("3", "+3V3", 200, -100, 70), tht("4", "GND", 200, 100, 70)),
		mk("U1", "PC817", Point{1300, 700}, false,
			smd("1", "LED_A", -90, 50, 60, 30), smd("2", "AC_N", -90, -50, 60, 30), smd("3", "GND", 90, -50, 60, 30), smd("4", "ZC", 90, 50, 60, 30)),
		mk("R2", "10k", Point{2000, 700}, false, smd("1", "ZC", -50, 0, 40, 50), smd("2", "+3V3", 50, 0, 40, 50)),
		mk("U2", "STM32G030", Point{2400, 1000}, false,
			smd("1", "+3V3", -60, 100, 40, 60), smd("2", "GND", -60, 0, 40, 60), smd("3", "ZC", -60, -100, 40, 60),
			smd("4", "SDA", 60, 100, 40, 60), smd("5", "SCL", 60, 0, 40, 60), smd("6", "NRST", 60, -100, 40, 60)),
		mk("C1", "100nF", Point{2400, 1400}, false, smd("1", "+3V3", -40, 0, 40, 50), smd("2", "GND", 40, 0, 40, 50)),
		mk("R3", "4.7k", Point{2700, 1300}, false, smd("1", "SDA", -50, 0, 40, 50), smd("2", "+3V3", 50, 0, 40, 50)),
		mk("R4", "4.7k", Point{2700, 1000}, false, smd("1", "SCL", -50, 0, 40, 50), smd("2", "+3V3", 50, 0, 40, 50)),
		mk("R5", "10k", Point{2700, 700}, false, smd("1", "NRST", -50, 0, 40, 50), smd("2", "+3V3", 50, 0, 40, 50)),
	}
	if err := b.Index(); err != nil {
		panic(err)
	}
	return b
}

func TestIntentUnderstandMainsSelv(t *testing.T) {
	b := mainsSelvBoard()
	in := loadIntent(t, "iso-mains-selv.intent.json")
	an := Analyze(b, PowerSpec{Intent: in}, nil)
	if an.Iso == nil || len(an.Iso.Pairs) != 1 {
		t.Fatalf("iso rules %+v", an.Iso)
	}
	ip := an.Iso.Pairs[0]
	// IEC 62368-1 reinforced 230 V, OVC II, PD2, MG IIIa: 3.0 mm / 4.6 mm.
	if math.Abs(ip.ClearanceMil-118.11) > 0.01 || math.Abs(ip.CreepageMil-181.1) > 0.01 || ip.Source != "computed" {
		t.Fatalf("pair %+v", ip)
	}
	c := Understand(b, an)
	if len(c.Barriers) != 1 {
		t.Fatalf("barriers %+v", c.Barriers)
	}
	br := c.Barriers[0]
	if !containsStr(br.Bridges, "U1") || !containsStr(br.Bridges, "T1") {
		t.Fatalf("bridges %v", br.Bridges)
	}
	if !reflect.DeepEqual(br.SlotUnder, []string{"U1"}) {
		t.Fatalf("slot under %v (want only U1)", br.SlotUnder)
	}
	if c.DomainOf["R1"] != "MAINS" || c.DomainOf["U2"] != "SELV" || c.DomainOf["U1"] != "" {
		t.Fatalf("domains %v", c.DomainOf)
	}
	// Per-net intent: declared widths win, class width is a floor.
	if p := an.ByNet["+3V3"]; p.Source != "intent" || p.WidthMil != 20 || p.InnerWidthMil != 16 {
		t.Fatalf("+3V3 plan %+v", p)
	}
	if p := an.ByNet["AC_L"]; p.WidthMil != 30 || p.ViasPerTransition != 2 || p.ClearanceMil < 20 {
		t.Fatalf("AC_L plan %+v", p)
	}
	if p := an.ByNet["L_F"]; p.WidthMil < 25 || p.ClearanceMil < 20 {
		t.Fatalf("L_F class plan %+v", p)
	}
}

// An intent that declares its own (larger) distances wins, and a smaller
// declaration than the standard computes is reported.
func TestIntentDeclaredDistances(t *testing.T) {
	b := mainsSelvBoard()
	in := loadIntent(t, "iso-mains-selv.intent.json")
	in.Pairs[0].ClearanceMm, in.Pairs[0].CreepageMm = 5.5, 8.0
	an := Analyze(b, PowerSpec{Intent: in}, nil)
	ip := an.Iso.Pairs[0]
	if ip.Source != "intent" || math.Abs(ip.CreepageMil-314.96) > 0.01 || len(an.Iso.Notes) != 0 {
		t.Fatalf("declared %+v notes %v", ip, an.Iso.Notes)
	}
	in.Pairs[0].ClearanceMm, in.Pairs[0].CreepageMm = 2, 3
	an = Analyze(b, PowerSpec{Intent: in}, nil)
	if len(an.Iso.Notes) != 2 {
		t.Fatalf("under-declaration not reported: %v", an.Iso.Notes)
	}
}

func TestIntentPlaceRouteMainsSelv(t *testing.T) {
	b := mainsSelvBoard()
	in := loadIntent(t, "iso-mains-selv.intent.json")
	power := PowerSpec{Intent: in}
	pre := Analyze(b, power, nil)
	c := Understand(b, pre)
	original := map[string]Placement{}
	for _, p := range b.Parts {
		original[p.Ref] = Placement{Ref: p.Ref, ID: p.Ref, X: p.Pos.X, Y: p.Pos.Y, Rot: p.Rotation}
	}
	pr, err := Place(b, pre, c, nil, PlaceOptions{Seed: 1, Timeout: 6 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	// Isolation band = the pair creepage (surface distance governs on a
	// board), opened only where the bridges sit.
	if len(pr.Barriers) == 0 {
		t.Fatalf("no isolation band: notes %v", pr.Notes)
	}
	for _, k := range pr.Barriers {
		if w := PolyBounds(k.Poly).W(); math.Abs(w-181.1) > 0.5 {
			t.Fatalf("band %s width %.1f, want 181.1 (4.6 mm creepage)", k.Name, w)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := Run(ctx, b, Options{Power: power, Stack: StackOptions{Force: 2}, Route: RouteOptions{Timeout: 20 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	iso := res.Isolation
	if iso == nil {
		t.Fatal("no isolation report")
	}
	t.Logf("routed %.1f%% (%d/%d), DRC %d %v, slots %d, moats %d, findings %d",
		res.Route.Stats.Completion, res.Route.Stats.Routed, res.Route.Stats.Connections, len(res.DRC.Violations), res.DRC.ByKind,
		len(iso.Slots), len(iso.Moats), len(iso.Findings))
	for _, f := range iso.Findings {
		t.Logf("finding %s", f.Message)
	}
	if len(iso.Slots) != 1 || iso.Slots[0].Ref != "U1" {
		t.Fatalf("slots %+v (want one under U1)", iso.Slots)
	}
	s := iso.Slots[0]
	if s.WidthMil < 39.37 || s.LengthMil <= PolyBounds(padsOf(b.Part("U1"))).H() {
		t.Fatalf("slot too small: %+v", s)
	}
	// Per-pair clearance respected by the routed copper, and creepage met
	// (the opto's own rows credited through the slot).
	if len(iso.Findings) != 0 {
		t.Fatalf("%d isolation findings on the routed board", len(iso.Findings))
	}
	if res.Route.Stats.Completion < 90 {
		t.Errorf("completion %.1f%% < 90%%", res.Route.Stats.Completion)
	}
	for _, v := range res.DRC.Violations {
		t.Errorf("DRC %s %s|%s gap %.1f < %.1f", v.Kind, v.NetA, v.NetB, v.Gap, v.Required)
	}
	// Brute-force cross-domain distance of the routed copper.
	clr := pre.Iso.Pairs[0].ClearanceMil
	for _, tr := range res.Route.Tracks {
		for _, o := range res.Route.Tracks {
			if pre.Iso.NetPair(tr.Net, o.Net) == nil || (tr.Layer != o.Layer) {
				continue
			}
			if d := SegSegDist(tr.A, tr.B, o.A, o.B) - tr.Width/2 - o.Width/2; d < clr-0.01 {
				t.Fatalf("%s ↔ %s tracks %.1f mil < %.1f", tr.Net, o.Net, d, clr)
			}
		}
	}
	// The playbook mills the slot (MULTI fill = board cutout) and writes the
	// band as live regions.
	pb := BuildPlaybook(PlaybookInput{Board: b, Original: original, Result: res, Placement: pr, Circuit: c, Name: "iso"})
	var slot, band bool
	for _, st := range pb.Steps {
		if strings.HasPrefix(st.ID, "iso-slot-") && st.Action == "pcb.fill.create" && st.Payload["layer"] == LayerMulti {
			slot = true
			if !strings.HasPrefix(func() string {
				for k := range st.Capture {
					return k
				}
				return ""
			}(), MechFillVar) {
				t.Errorf("slot step not captured for --replace: %+v", st.Capture)
			}
		}
		if strings.HasPrefix(st.ID, "iso-band-") && st.Action == "pcb.region.create" {
			band = true
		}
	}
	if !slot || !band {
		t.Fatalf("playbook slot=%v band=%v", slot, band)
	}
}

func padsOf(p *Part) []Point {
	var out []Point
	for _, pd := range p.Pads {
		out = append(out, pd.Box.Bounds().Corners()...)
	}
	return out
}

// Route-only (no placement): the fence alone keeps the domains apart and
// the playbook writes no-pour moats instead of placement strips.
func TestIntentRouteOnlyFenceAndMoat(t *testing.T) {
	b := mainsSelvBoard()
	in := loadIntent(t, "iso-mains-selv.intent.json")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := Run(ctx, b, Options{Power: PowerSpec{Intent: in}, Stack: StackOptions{Force: 2}, Route: RouteOptions{Timeout: 15 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	iso := res.Isolation
	t.Logf("routed %.1f%%, findings %d, moats %d", res.Route.Stats.Completion, len(iso.Findings), len(iso.Moats))
	for _, f := range iso.Findings {
		if !strings.HasPrefix(f.ItemA, "U1.") && !strings.HasPrefix(f.ItemA, "T1.") || f.Kind != "iso-creepage" {
			t.Errorf("routed copper violates the pair: %s", f.Message)
		}
	}
	if len(iso.Moats) == 0 {
		t.Fatal("no no-pour moat traced")
	}
	pb := BuildPlaybook(PlaybookInput{Board: b, Result: res, Name: "iso"})
	moat := false
	for _, st := range pb.Steps {
		if strings.HasPrefix(st.ID, "iso-moat-") {
			moat = true
			if r := st.Payload["ruleType"].([]string); len(r) != 1 || r[0] != "no-pours" {
				t.Fatalf("moat rules %v", r)
			}
		}
	}
	if !moat {
		t.Fatal("playbook has no moat region")
	}
}

// A deliberately violating board: a SELV (GND) track 40 mil from the mains
// terminal and an optocoupler whose rows are closer than the creepage.
func TestCheckIsolationSnapshotFlagsViolations(t *testing.T) {
	in := loadIntent(t, "iso-mains-selv.intent.json")
	raw, err := os.ReadFile("testdata/iso-violation.dump.json")
	if err != nil {
		t.Fatal(err)
	}
	chk, err := CheckIsolationSnapshot(raw, in)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	var trackClr, optoCreep bool
	for _, f := range chk.Findings {
		kinds[f.Kind]++
		if f.Kind == "iso-clearance" && (f.ItemA == "J1.1" && f.ItemB == "track#1") {
			trackClr = f.GapMil < 45 && f.GapMil > 35
		}
		if f.Kind == "iso-creepage" && strings.HasPrefix(f.ItemA, "U1.") && strings.HasPrefix(f.ItemB, "U1.") {
			optoCreep = true
		}
	}
	if !trackClr || !optoCreep || chk.Tracks != 2 {
		for _, f := range chk.Findings {
			t.Logf("%s", f.Message)
		}
		t.Fatalf("want the GND track clearance and the opto creepage flagged: %v", kinds)
	}
	// With a 1 mm slot between the opto rows its creepage is credited; the
	// track's clearance (an air path) still fails.
	raw, _ = os.ReadFile("testdata/iso-violation-slotted.dump.json")
	chk, err = CheckIsolationSnapshot(raw, in)
	if err != nil {
		t.Fatal(err)
	}
	if chk.Cutouts != 1 {
		t.Fatalf("cutouts %d", chk.Cutouts)
	}
	for _, f := range chk.Findings {
		if strings.HasPrefix(f.ItemA, "U1.") && strings.HasPrefix(f.ItemB, "U1.") {
			t.Fatalf("slotted opto still flagged: %s", f.Message)
		}
	}
	found := false
	for _, f := range chk.Findings {
		found = found || f.Kind == "iso-clearance" && f.ItemB == "track#1"
	}
	if !found {
		t.Fatal("track clearance lost with the slot")
	}
}

// ESP32 mini is all SELV: an intent with one domain and no pairs must not
// add a band, a slot or change any net plan it does not declare.
func TestIntentAllSELVUnaffected(t *testing.T) {
	raw, err := os.ReadFile("testdata/esp32-mini-layout.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := FromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	var nets []string
	for _, n := range b.Nets() {
		nets = append(nets, n.Name)
	}
	in := &Intent{Domains: []IntentDomain{{ID: "SELV_5V", Kind: "SELV", Nets: nets, WorkingVrms: 5, WorkingVpeak: 5}}}
	base := Analyze(b, PowerSpec{}, nil)
	an := Analyze(b, PowerSpec{Intent: in}, nil)
	for _, p := range base.Nets {
		q := an.ByNet[p.Net]
		if q.WidthMil != p.WidthMil || q.ClearanceMil != p.ClearanceMil || q.ViasPerTransition != p.ViasPerTransition || q.Role != p.Role {
			t.Fatalf("%s plan changed: %+v → %+v", p.Net, p, q)
		}
	}
	c := Understand(b, an)
	if len(c.Barriers) != 0 || len(c.Bridge) != 0 {
		t.Fatalf("barriers %+v bridges %v", c.Barriers, c.Bridge)
	}
	if slots, notes := PlanIsoSlots(b, an.Iso); len(slots) != 0 || len(notes) != 0 {
		t.Fatalf("slots %v notes %v", slots, notes)
	}
	if f := buildIsoField(b, an.Iso, isoFieldCell); f != nil {
		t.Fatal("territory field built without pairs")
	}
	// The placer draws a band only between two or more referenced domains.
	doms := 0
	for _, d := range c.Domains {
		if d.ID != "UNREFERENCED" {
			doms++
		}
	}
	if doms != 1 || c.DomainOf["U1"] != "SELV_5V" {
		t.Fatalf("domains %+v", c.Domains)
	}
	rep := isolationReport(b, an, nil, nil, nil, nil)
	if rep == nil || len(rep.Findings) != 0 || len(rep.Moats) != 0 || len(rep.Slots) != 0 {
		t.Fatalf("report %+v", rep)
	}
	pb := BuildPlaybook(PlaybookInput{Board: b, Result: &Result{Stackup: &Stackup{Layers: 2}, Isolation: rep}, Name: "esp32"})
	for _, st := range pb.Steps {
		if strings.HasPrefix(st.ID, "iso-") {
			t.Fatalf("isolation step on an all-SELV board: %+v", st)
		}
	}
}

func TestEDTExact(t *testing.T) {
	W, H := 7, 5
	set := make([]bool, W*H)
	set[2*W+3] = true
	d := edt(set, W, H)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			want := math.Hypot(float64(x-3), float64(y-2))
			if math.Abs(float64(d[y*W+x])-want) > 1e-5 {
				t.Fatalf("edt(%d,%d) = %v, want %v", x, y, d[y*W+x], want)
			}
		}
	}
}

func TestSurfacePathAroundSlot(t *testing.T) {
	a := Rect{0, 0, 10, 100}.Corners()
	b := Rect{110, 0, 120, 100}.Corners()
	d, pa, pb := polyDist(a, b)
	if d != 100 {
		t.Fatalf("straight %v", d)
	}
	slot := Rect{50, -50, 70, 150}.Corners()
	path, credited := surfacePath(a, b, pa, pb, d, [][]Point{slot})
	// Around the slot end: (10,0)→(50,-50)→(70,-50)→(110,0).
	want := 2*math.Hypot(40, 50) + 20
	if !credited || math.Abs(path-want) > 0.2 {
		t.Fatalf("path %v credited %v, want %v", path, credited, want)
	}
}

func TestParseIntentValidation(t *testing.T) {
	for _, bad := range []string{
		`{"domains":[{"id":"A"},{"id":"A"}]}`,
		`{"domains":[{"id":"A"}],"pairs":[{"a":"domain:A","b":"domain:B"}]}`,
		`{"domains":[{"id":"A"},{"id":"B"}],"pairs":[{"a":"domain:A","b":"domain:B","creepageMm":-1}]}`,
		`{"domains":[{"id":"A"}],"pairs":[{"a":"domain:A","b":"domain:A"}]}`,
	} {
		if _, err := ParseIntent([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, err := ParseIntent([]byte(`{"domains":[{"id":"A"},{"id":"B"}],"pairs":[{"a":"domain:A","b":"B"}]}`)); err != nil {
		t.Fatal(err)
	}
}
