package specctra

import (
	"math"
	"os"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFixDSN_QuotesClassNetNames(t *testing.T) {
	out, rep, err := FixDSN(readFixture(t, "easyeda-export.dsn"), FixOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.QuotedClasses != 2 {
		t.Fatalf("QuotedClasses = %d, want 2", rep.QuotedClasses)
	}
	for _, want := range []string{`(class +12V "+12V"`, `(class GND "GND"`, `(class  ''`} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if strings.Contains(out, `'GND'`) || strings.Contains(out, `'+12V'`) {
		t.Error("single-quoted class net name survived")
	}
}

func TestFixDSN_LayersInStackOrderWithMissingInner(t *testing.T) {
	out, rep, err := FixDSN(readFixture(t, "easyeda-export.dsn"), FixOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rep.LayerOrder, ","); got != "TopLayer,Inner1,Inner2,BottomLayer" {
		t.Fatalf("LayerOrder = %s", got)
	}
	if strings.Join(rep.AddedLayers, ",") != "Inner1" {
		t.Fatalf("AddedLayers = %v", rep.AddedLayers)
	}
	iTop, i1, i2, iBot := strings.Index(out, "(layer TopLayer"), strings.Index(out, "(layer Inner1"), strings.Index(out, "(layer Inner2"), strings.Index(out, "(layer BottomLayer")
	if !(iTop < i1 && i1 < i2 && i2 < iBot) {
		t.Fatalf("layer declarations out of order: %d %d %d %d", iTop, i1, i2, iBot)
	}
	if strings.Contains(out, "(plane") {
		t.Error("plane emitted without PlaneNet")
	}
	if _, err := parseSexpr(out); err != nil {
		t.Fatalf("patched DSN no longer parses: %v", err)
	}
}

func TestFixDSN_PlaneOption(t *testing.T) {
	out, rep, err := FixDSN(readFixture(t, "easyeda-export.dsn"), FixOptions{PlaneNet: "GND"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.PlaneNet != "GND" {
		t.Fatalf("PlaneNet = %q", rep.PlaneNet)
	}
	if !strings.Contains(out, "(layer Inner1\n      (type power)") {
		t.Error("missing inner layer not declared as power")
	}
	if !strings.Contains(out, "(plane GND (polygon Inner1 0 1000 0 0 0 0 500 1000 500 1000 0))") {
		t.Error("GND plane over the board outline not emitted")
	}
}

func TestFixDSN_PatchesThroughHolePadstacksOnly(t *testing.T) {
	out, rep, err := FixDSN(readFixture(t, "easyeda-export.dsn"), FixOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.PatchedPadstacks != 2 { // via0 + pth; psmd is SMD
		t.Fatalf("PatchedPadstacks = %d, want 2", rep.PatchedPadstacks)
	}
	for _, want := range []string{
		"      (shape(circle Inner2 24))\n      (shape(circle Inner1 24))\n",
		"      (shape(circle Inner2 78.74 0 0))\n      (shape(circle Inner1 78.74 0 0))\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	smd := out[strings.Index(out, "(padstack psmd"):strings.Index(out, "(padstack pth")]
	if strings.Contains(smd, "Inner") {
		t.Error("SMD padstack got an inner shape")
	}
	// Idempotent: a second pass changes nothing more.
	again, rep2, err := FixDSN(out, FixOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.PatchedPadstacks != 0 || rep2.QuotedClasses != 0 || len(rep2.AddedLayers) != 0 || again != out {
		t.Errorf("second pass not a no-op: %+v", rep2)
	}
}

func TestFixDSN_EdgeKeepouts(t *testing.T) {
	out, rep, err := FixDSN(readFixture(t, "easyeda-export.dsn"), FixOptions{EdgeOuterMil: 20, EdgeInnerMil: 30})
	if err != nil {
		t.Fatal(err)
	}
	if rep.EdgeKeepouts != 16 { // 4 edges x 4 layers
		t.Fatalf("EdgeKeepouts = %d, want 16", rep.EdgeKeepouts)
	}
	// Bottom edge (1000,0)->(0,0) of a clockwise outline: band goes inward (+y).
	for _, want := range []string{
		`(keepout "pcbpilot_edge_TopLayer_0" (polygon TopLayer 0 1000 0 0 0 0 20 1000 20 1000 0))`,
		`(keepout "pcbpilot_edge_Inner1_0" (polygon Inner1 0 1000 0 0 0 0 30 1000 30 1000 0))`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %s", want)
		}
	}
	if !strings.Contains(out, `"region_keepout_1"`) {
		t.Error("existing keepout dropped")
	}
}

func TestFixDSN_FixedEscapes(t *testing.T) {
	esc := []Escape{{Net: "GND", Layer: "TopLayer", WidthMil: 10, Path: [][2]float64{{4776.1, 313.9}, {4710.65, 313.9}}, Via: true}}
	out, rep, err := FixDSN(readFixture(t, "easyeda-export.dsn"), FixOptions{Escapes: esc})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Escapes != 1 {
		t.Fatalf("Escapes = %d", rep.Escapes)
	}
	for _, want := range []string{
		"(wire (path TopLayer 10 4776.1 313.9 4710.65 313.9) (net GND) (type fix))",
		"(via via0 4710.65 313.9 (net GND) (type fix))",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %s", want)
		}
	}
	fixed, err := ParseFixedWiring(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixed.Segments) != 1 || len(fixed.Vias) != 1 || fixed.Vias[0].Net != "GND" {
		t.Fatalf("fixed wiring read back = %+v", fixed)
	}
	if d := ViaDiameterMil(out, "via0"); d != 24 {
		t.Errorf("via0 diameter = %v, want 24", d)
	}
	if _, _, err := FixDSN(readFixture(t, "easyeda-export.dsn"), FixOptions{Escapes: []Escape{{Net: "GND", Layer: "Inner9", WidthMil: 10, Path: [][2]float64{{0, 0}, {1, 0}}}}}); err == nil {
		t.Error("escape on an undeclared layer accepted")
	}
}

func TestParseSES_RecordedFastrouteSession(t *testing.T) {
	w, err := ParseSES(readFixture(t, "fastroute-excerpt.ses"))
	if err != nil {
		t.Fatal(err)
	}
	// GND: 1 + 1 + 2 segments; CANL: 2 + 3; RS_C1N (quoted name): 1.
	if len(w.Segments) != 10 {
		t.Fatalf("segments = %d, want 10", len(w.Segments))
	}
	if len(w.Vias) != 1 || w.Vias[0].At != [2]float64{4551.69, 180.645} {
		t.Fatalf("vias = %+v", w.Vias)
	}
	last := w.Segments[len(w.Segments)-1]
	if last.Net != "RS_C1N" || last.WidthMil != 6 {
		t.Fatalf("quoted net segment = %+v", last)
	}
}

// The importer merges the 21.65 mil GND trunk and the 11.02 mil neck-down
// piece into one 21.65 mil track and moves Inner1/Inner2 tracks to 21/22.
func TestPlanImportRepair_LayersAndNeckDownWidths(t *testing.T) {
	w, err := ParseSES(readFixture(t, "fastroute-excerpt.ses"))
	if err != nil {
		t.Fatal(err)
	}
	imported := []Track{
		{ID: "merged", Net: "GND", Layer: 1, X1: 4540.309, Y1: 727.325, X2: 4776.095, Y2: 727.325, Width: 21.65},
		{ID: "in1", Net: "GND", Layer: 21, X1: 1920.51, Y1: 2245.175, X2: 1928.93, Y2: 2245.175, Width: 21.65},
		{ID: "in2", Net: "CANL", Layer: 22, X1: 296.12, Y1: 2370.09, X2: 536.21, Y2: 2130, Width: 6},
		{ID: "ok", Net: "CANL", Layer: 1, X1: 625, Y1: 2130, X2: 524.113, Y2: 2130, Width: 6},
		{ID: "stray", Net: "CANL", Layer: 1, X1: 0, Y1: 0, X2: 50, Y2: 0, Width: 6},
		{ID: "locked", Net: "GND", Layer: 22, X1: 0, Y1: 0, X2: 50, Y2: 0, Width: 6, Locked: true},
	}
	plan := PlanImportRepair(imported, w)
	if plan.LayerMoves != 2 || plan.WidthRestores != 1 || len(plan.Fixes) != 3 {
		t.Fatalf("plan = %+v", plan)
	}
	byID := map[string]TrackFix{}
	for _, f := range plan.Fixes {
		byID[f.Delete.ID] = f
	}
	if f := byID["in1"]; len(f.Create) != 1 || f.Create[0].Layer != 15 || f.Create[0].Width != 21.65 {
		t.Errorf("Inner1 fix = %+v", f)
	}
	if f := byID["in2"]; len(f.Create) != 1 || f.Create[0].Layer != 16 || f.Create[0].Width != 6 {
		t.Errorf("Inner2 fix = %+v", f)
	}
	m := byID["merged"]
	if len(m.Create) != 2 {
		t.Fatalf("merged track recreated as %d pieces, want 2: %+v", len(m.Create), m.Create)
	}
	trunk, neck := m.Create[0], m.Create[1]
	if trunk.Width != 21.65 || math.Abs(trunk.X2-4724.36) > 1e-6 || trunk.Layer != 1 {
		t.Errorf("trunk = %+v", trunk)
	}
	if math.Abs(neck.Width-11.018) > 1e-9 || neck.X1 != trunk.X2 || neck.X2 != 4776.095 {
		t.Errorf("neck-down = %+v", neck)
	}
	if len(plan.Unmatched) != 1 || plan.Unmatched[0].ID != "stray" {
		t.Errorf("unmatched = %+v", plan.Unmatched)
	}
}

// fastroute can emit one segment twice at two widths (recorded: GND
// 21.65 and 16.236 mil on the same ends). A track that is exactly one of
// them was imported faithfully and must not be rewritten.
func TestPlanImportRepair_FaithfulDuplicateUntouched(t *testing.T) {
	w := &Wiring{Segments: []Segment{
		{Net: "GND", Layer: "TopLayer", WidthMil: 21.65, A: [2]float64{1089.058, 1462.56}, B: [2]float64{1105, 1462.56}},
		{Net: "GND", Layer: "TopLayer", WidthMil: 16.236, A: [2]float64{1105, 1462.56}, B: [2]float64{1089.058, 1462.56}},
	}}
	tracks := []Track{
		{ID: "wide", Net: "GND", Layer: 1, X1: 1089.058, Y1: 1462.56, X2: 1105, Y2: 1462.56, Width: 21.65},
		{ID: "narrow", Net: "GND", Layer: 1, X1: 1105, Y1: 1462.56, X2: 1089.058, Y2: 1462.56, Width: 16.236},
	}
	if plan := PlanImportRepair(tracks, w); len(plan.Fixes) != 0 {
		t.Fatalf("faithful duplicate rewritten: %+v", plan.Fixes)
	}
}

// Recorded: a Top GND stub lies on the same line as an Inner1 GND trunk, so
// a trunk imported on layer 21 matches segments on two layers.
func TestPlanImportRepair_WrongLayerWithOverlapOnAnotherLayer(t *testing.T) {
	w := &Wiring{Segments: []Segment{
		{Net: "GND", Layer: "TopLayer", WidthMil: 21.65, A: [2]float64{4855, 655.65}, B: [2]float64{4855, 589.525}},
		{Net: "GND", Layer: "Inner1", WidthMil: 21.65, A: [2]float64{4855, 556.75}, B: [2]float64{4855, 655.65}},
	}}
	plan := PlanImportRepair([]Track{{ID: "t", Net: "GND", Layer: 21, X1: 4855, Y1: 556.75, X2: 4855, Y2: 655.65, Width: 21.65}}, w)
	if len(plan.Fixes) != 1 || len(plan.Fixes[0].Create) != 1 || plan.Fixes[0].Create[0].Layer != 15 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanFixedWiring_SkipsWhatIsAlreadyOnBoard(t *testing.T) {
	fixed := &Wiring{
		Segments: []Segment{
			{Net: "GND", Layer: "TopLayer", WidthMil: 10, A: [2]float64{4776.1, 313.9}, B: [2]float64{4710.65, 313.9}},
			{Net: "GND", Layer: "TopLayer", WidthMil: 10, A: [2]float64{4776.1, 412.4}, B: [2]float64{4710.65, 412.4}},
		},
		Vias: []Via{{Net: "GND", At: [2]float64{4710.65, 313.9}}, {Net: "GND", At: [2]float64{4710.65, 412.4}}},
	}
	tracks := []Track{{Net: "GND", Layer: 1, X1: 4710.65, Y1: 313.9, X2: 4776.1, Y2: 313.9, Width: 10}}
	nt, nv := PlanFixedWiring(fixed, tracks, [][2]float64{{4710.65, 313.9}}, []string{"GND"}, 24)
	if len(nt) != 1 || nt[0].Y1 != 412.4 {
		t.Errorf("tracks to create = %+v", nt)
	}
	if len(nv) != 1 || nv[0].Y != 412.4 || nv[0].DiameterMil != 24 {
		t.Errorf("vias to create = %+v", nv)
	}
}

// Recorded on Gas Module v9 B: SV3_DRV routed the same segment on Inner1 and
// Inner2; the importer put them on 21 and 22. Both must be moved, by the
// importer's offset, instead of being left on layers the board lacks.
func TestPlanImportRepair_SameSegmentOnBothInnerLayers(t *testing.T) {
	w := &Wiring{Segments: []Segment{
		{Net: "SV3_DRV", Layer: "Inner1", WidthMil: 10, A: [2]float64{3600.8, 2420.5}, B: [2]float64{3554.4, 2420.5}},
		{Net: "SV3_DRV", Layer: "Inner2", WidthMil: 10, A: [2]float64{3600.8, 2420.5}, B: [2]float64{3554.4, 2420.5}},
	}}
	tracks := []Track{
		{ID: "a", Net: "SV3_DRV", Layer: 21, X1: 3600.8, Y1: 2420.5, X2: 3554.4, Y2: 2420.5, Width: 10},
		{ID: "b", Net: "SV3_DRV", Layer: 22, X1: 3600.8, Y1: 2420.5, X2: 3554.4, Y2: 2420.5, Width: 10},
	}
	plan := PlanImportRepair(tracks, w)
	if len(plan.Unmatched) != 0 || len(plan.Fixes) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	got := map[string]int{}
	for _, f := range plan.Fixes {
		got[f.Delete.ID] = f.Create[0].Layer
	}
	if got["a"] != 15 || got["b"] != 16 {
		t.Fatalf("layers = %v, want a→15 b→16", got)
	}
}

func TestPlanReconcile(t *testing.T) {
	ses := &Wiring{
		Segments: []Segment{
			{Net: "+3V3", Layer: "TopLayer", WidthMil: 10, A: [2]float64{2055.8, 1002.5}, B: [2]float64{1837.7, 1002.5}},
			{Net: "+3V3", Layer: "Inner2", WidthMil: 10, A: [2]float64{1837.7, 1002.5}, B: [2]float64{1803.6, 1036.6}},
			{Net: "+3V3", Layer: "TopLayer", WidthMil: 10, A: [2]float64{1900, 1002.5}, B: [2]float64{2000, 1002.5}}, // inside the merged track above
		},
		Vias: []Via{{Net: "+3V3", At: [2]float64{1837.7, 1002.5}}},
	}
	tracks := []Track{
		{ID: "top", Net: "+3V3", Layer: 1, X1: 2055.8, Y1: 1002.5, X2: 1837.7, Y2: 1002.5, Width: 10},
		{ID: "stray", Net: "+3V3", Layer: 22, X1: 1837.7, Y1: 1002.5, X2: 1803.6, Y2: 1036.6, Width: 10},
	}
	r := PlanReconcile(ses, tracks, nil, nil, CopperLayerIDs(4), 24)
	if len(r.MissingTracks) != 1 || r.MissingTracks[0].Layer != 16 {
		t.Fatalf("missing tracks = %+v", r.MissingTracks)
	}
	if len(r.MissingVias) != 1 || r.MissingVias[0].DiameterMil != 24 {
		t.Fatalf("missing vias = %+v", r.MissingVias)
	}
	if len(r.Stray) != 1 || r.Stray[0].ID != "stray" {
		t.Fatalf("stray = %+v", r.Stray)
	}
	// Once the via and the Inner2 piece exist nothing is missing.
	tracks = append(tracks, Track{Net: "+3V3", Layer: 16, X1: 1837.7, Y1: 1002.5, X2: 1803.6, Y2: 1036.6, Width: 10})
	r = PlanReconcile(ses, tracks, [][2]float64{{1837.7, 1002.5}}, []string{"+3V3"}, CopperLayerIDs(4), 24)
	if len(r.MissingTracks) != 0 || len(r.MissingVias) != 0 {
		t.Fatalf("still missing: %+v", r)
	}
}

// Gas Module v10: EasyEDA split a GND session segment into collinear pieces
// of different widths; it is present. A partly covered segment gets only
// its uncovered stretch.
func TestPlanReconcileCollinearPieces(t *testing.T) {
	ses := &Wiring{Segments: []Segment{{Net: "GND", Layer: "TopLayer", WidthMil: 21.65, A: [2]float64{992.3, 1788.5}, B: [2]float64{840, 1788.5}}}}
	split := []Track{
		{Net: "GND", Layer: 1, X1: 989.1, Y1: 1788.5, X2: 840, Y2: 1788.5, Width: 21.7},
		{Net: "GND", Layer: 1, X1: 992.3, Y1: 1788.5, X2: 989.1, Y2: 1788.5, Width: 16.24},
	}
	if r := PlanReconcile(ses, split, nil, nil, CopperLayerIDs(4), 24); len(r.MissingTracks) != 0 {
		t.Fatalf("split segment reported missing: %+v", r.MissingTracks)
	}
	part := []Track{{Net: "GND", Layer: 1, X1: 940, Y1: 1788.5, X2: 840, Y2: 1788.5, Width: 21.65}}
	r := PlanReconcile(ses, part, nil, nil, CopperLayerIDs(4), 24)
	if len(r.MissingTracks) != 1 || r.MissingTracks[0].X1 != 992.3 || math.Abs(r.MissingTracks[0].X2-940.6) > 0.01 {
		t.Fatalf("partial coverage: %+v", r.MissingTracks)
	}
}

// Gas Module V5 B v17: EasyEDA dropped a 1.1 mil GND stub between two
// collinear 21.65 mil tracks 2.2 mil apart; their round ends overlap, so the
// copper is continuous. A real gap (10 mil) is still missing.
func TestPlanReconcileBridgedGap(t *testing.T) {
	ses := &Wiring{Segments: []Segment{{Net: "GND", Layer: "TopLayer", WidthMil: 21.65, A: [2]float64{1367.6, 1387.6}, B: [2]float64{1368.7, 1387.6}}}}
	tracks := []Track{
		{Net: "GND", Layer: 1, X1: 1350, Y1: 1387.6, X2: 1367.034, Y2: 1387.6, Width: 21.65},
		{Net: "GND", Layer: 1, X1: 1382.1, Y1: 1387.6, X2: 1369.254, Y2: 1387.6, Width: 21.65},
	}
	if r := PlanReconcile(ses, tracks, nil, nil, CopperLayerIDs(4), 24); len(r.MissingTracks) != 0 {
		t.Fatalf("bridged stub reported missing: %+v", r.MissingTracks)
	}
	ses.Segments[0].A, ses.Segments[0].B = [2]float64{1360, 1387.6}, [2]float64{1380, 1387.6}
	tracks[0].X2, tracks[1].X2 = 1365, 1375
	if r := PlanReconcile(ses, tracks, nil, nil, CopperLayerIDs(4), 24); len(r.MissingTracks) != 1 {
		t.Fatalf("10 mil gap: %+v", r.MissingTracks)
	}
}

// Gas Module V5 B v18: EasyEDA dropped a 12.5 mil 40 mil-wide SV1_DRV stub
// whose area a via and two 40 mil tracks already cover; it is present. The
// same stub with the covering track removed is missing.
func TestPlanReconcileCoveredStub(t *testing.T) {
	ses := &Wiring{Segments: []Segment{{Net: "SV1_DRV", Layer: "TopLayer", WidthMil: 40, A: [2]float64{3082.8, 1652.5}, B: [2]float64{3082.8, 1640}}}}
	tracks := []Track{
		{Net: "SV1_DRV", Layer: 1, X1: 3090, Y1: 1645.3, X2: 3082.8, Y2: 1652.5, Width: 40},
		{Net: "SV1_DRV", Layer: 1, X1: 3082.8, Y1: 1640, X2: 3081.4, Y2: 1638.7, Width: 40},
	}
	vias := [][2]float64{{3082.8, 1652.5}}
	nets := []string{"SV1_DRV"}
	if r := PlanReconcile(ses, tracks, vias, nets, CopperLayerIDs(4), 24); len(r.MissingTracks) != 0 {
		t.Fatalf("covered stub reported missing: %+v", r.MissingTracks)
	}
	if r := PlanReconcile(ses, tracks[1:], nil, nil, CopperLayerIDs(4), 24); len(r.MissingTracks) != 1 {
		t.Fatalf("uncovered stub: %+v", r.MissingTracks)
	}
}

// Gas Module V5 B v22: fastroute 0.1.7 wrote a one-point +12V path at a
// T-junction of three 41.34 mil tracks; ses-repair aborted ("malformed
// path"). The dot is skipped and listed; the tracks parse.
func TestParseSESOnePointPath(t *testing.T) {
	src := `(session "b" (base_design "b")
  (routes (resolution mil 1000)
    (network_out
      (net "+12V"
        (wire (path Inner1 41340 3052804 2435932 3080562 2463690 3080562 2502360))
        (wire (path Inner1 41340 3185227 2253717 3185227 2303508 3052804 2435932))
        (wire (path Inner1 21650
            3052804 2435932
          ))
      ))))`
	w, err := ParseSES(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Segments) != 4 || len(w.Dots) != 1 || !strings.Contains(w.Dots[0], "+12V Inner1 (3052.804,2435.932) 21.65 mil") {
		t.Fatalf("segments %d dots %v", len(w.Segments), w.Dots)
	}
	if _, err := ParseSES(strings.Replace(src, "3052804 2435932\n", "3052804\n", 1)); err == nil {
		t.Fatal("a three-atom path must stay an error")
	}
}

func TestReportMilPerUnit(t *testing.T) {
	for dsn, want := range map[string]float64{
		"(PCB board (resolution mil 10) (unit mil))":                        1000,
		"(pcb x (parser (host_cad \"KiCad\")) (resolution um 10) (unit um)": 1000 / 25.4,
		"(pcb x (resolution mm 1000))":                                      1000 * 1000 / 25.4,
		"(pcb x)":                                                           1000,
	} {
		if got := ReportMilPerUnit(dsn); math.Abs(got-want) > 1e-9 {
			t.Errorf("%s: %v, want %v", dsn, got, want)
		}
	}
}

// A connector pad on the bottom edge opens a full-depth window in that
// band; the other bands and the rest of the bottom band stay.
func TestEdgeBandsExceptConnectorWindow(t *testing.T) {
	sq := [][2]float64{{0, 0}, {1000, 0}, {1000, 1000}, {0, 1000}}
	full, cut0 := EdgeBandsExcept(sq, 30, nil)
	if len(full) != 4 || cut0 != 0 {
		t.Fatalf("plain bands %d %d", len(full), cut0)
	}
	pad := [4]float64{480, -10, 520, 20} // pad over the edge, grown
	qs, cut := EdgeBandsExcept(sq, 30, [][4]float64{pad, {2000, 2000, 2010, 2010}})
	if cut != 1 || len(qs) != 5 {
		t.Fatalf("got %d quads, %d windows", len(qs), cut)
	}
	for _, q := range qs {
		minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
		for _, p := range q {
			minX, maxX, minY, maxY = math.Min(minX, p[0]), math.Max(maxX, p[0]), math.Min(minY, p[1]), math.Max(maxY, p[1])
		}
		if maxY <= 30+1e-9 && minY >= -1e-9 && minX < 520-1e-6 && maxX > 480+1e-6 && maxX-minX < 1000 {
			t.Fatalf("band piece %v covers the connector window", q)
		}
	}
	// Two overlapping pads make one window.
	qs, cut = EdgeBandsExcept(sq, 30, [][4]float64{pad, {510, -5, 560, 25}})
	if cut != 1 || len(qs) != 5 {
		t.Fatalf("merged: %d quads, %d windows", len(qs), cut)
	}
}
