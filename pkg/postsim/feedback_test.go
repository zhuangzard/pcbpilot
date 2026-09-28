package postsim

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func kinds(r *Result) map[string]int {
	m := map[string]int{}
	for _, it := range r.Feedback {
		m[it.Kind]++
	}
	return m
}

// A 5 mil track at 1.5 A must be widened; the recommended width carries the
// current with the margin.
func TestFeedbackWidenSegment(t *testing.T) {
	b := newTB(1400, 300, 2)
	b.part("S", 1, pad("1", "VIN", 1, 200, 150, 10, 10))
	b.part("L", 1, pad("1", "VIN", 1, 1200, 150, 10, 10))
	b.line("VIN", 1, 200, 150, 1200, 150, 5)
	sim := simDoc(map[string][][4]any{"VIN": {{"S", "1", "source", 1.5}, {"L", "1", "sink", 1.5}}}, nil, nil)
	r, err := Run(b.json(), sim, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if kinds(r)[FBWiden] != 1 {
		t.Fatalf("feedback %v, want one widen-segment", kinds(r))
	}
	it := r.Feedback[0]
	need := it.Evidence.Metrics["recommendMil"]
	if it.Severity != "high" || need <= 5 {
		t.Fatalf("item %+v", it)
	}
	if capA := trackAmpacity(need, OzMm/MilMm, 10); capA < 1.5*1.2-1e-6 {
		t.Errorf("recommended %.1f mil carries %.3f A < 1.8 A", need, capA)
	}
	t.Logf("%s: %s", it.Title, it.Proposal.Summary)
}

// A 90° corner on 10 mil at 0.7 A: 0.7 × 1.5 = 1.05 A > 0.886 A capacity,
// the straight legs are fine (0.7 × 1.2 < capacity).
func TestFeedbackCornerCrowding(t *testing.T) {
	b := newTB(1400, 1400, 2)
	b.part("S", 1, pad("1", "VIN", 1, 200, 200, 10, 10))
	b.part("L", 1, pad("1", "VIN", 1, 1200, 1200, 10, 10))
	b.line("VIN", 1, 200, 200, 1200, 200, 10)
	b.line("VIN", 1, 1200, 200, 1200, 1200, 10)
	sim := simDoc(map[string][][4]any{"VIN": {{"S", "1", "source", 0.7}, {"L", "1", "sink", 0.7}}}, nil, nil)
	r, err := Run(b.json(), sim, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	k := kinds(r)
	if k[FBCorner] != 1 || k[FBWiden] != 0 {
		t.Fatalf("feedback %v, want exactly one corner-crowding", k)
	}
	for _, it := range r.Feedback {
		if it.Kind == FBCorner {
			if a := it.Evidence.Metrics["angleDeg"]; math.Abs(a-90) > 0.5 {
				t.Errorf("angle %.1f", a)
			}
			if p := it.Evidence.Points[0]; math.Hypot(p.X-1200, p.Y-200) > 1 {
				t.Errorf("corner at %+v", p)
			}
		}
	}
}

// One 12 mil via carrying 2 A (> 1.48 A ampacity): via-bottleneck high with
// ≥ 2 vias, and a failing verdict.
func TestFeedbackViaBottleneck(t *testing.T) {
	b := newTB(1400, 300, 2)
	b.part("S", 1, pad("1", "VIN", 1, 200, 150, 60, 60))
	b.part("L", 2, pad("1", "VIN", 2, 1200, 150, 60, 60))
	b.line("VIN", 1, 200, 150, 700, 150, 60)
	b.via("VIN", 700, 150, 24, 12)
	b.line("VIN", 2, 700, 150, 1200, 150, 60)
	sim := simDoc(map[string][][4]any{"VIN": {{"S", "1", "source", 2.0}, {"L", "1", "sink", 2.0}}}, nil, nil)
	r, err := Run(b.json(), sim, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if kinds(r)[FBVia] != 1 {
		t.Fatalf("feedback %v", kinds(r))
	}
	for _, it := range r.Feedback {
		if it.Kind == FBVia && (it.Severity != "high" || it.Evidence.Metrics["viasNeeded"] < 2) {
			t.Errorf("via item %+v", it)
		}
	}
	if r.Verdict.Status != "fail" {
		t.Errorf("verdict %s, want fail (via over ampacity)", r.Verdict.Status)
	}
}

// Merging into a pcb auto feedback.json keeps its items and replaces older
// post-* items.
func TestMergeFeedback(t *testing.T) {
	prev := []byte(`{"schemaVersion":1,"source":"pcb auto run","items":[{"id":"fb-1","kind":"mcu-pin-swap"},{"id":"post-widen-segment-1","kind":"widen-segment"}],"rules":["x"]}`)
	b := newTB(1400, 300, 2)
	b.part("S", 1, pad("1", "VIN", 1, 200, 150, 10, 10))
	b.part("L", 1, pad("1", "VIN", 1, 1200, 150, 10, 10))
	b.line("VIN", 1, 200, 150, 1200, 150, 5)
	sim := simDoc(map[string][][4]any{"VIN": {{"S", "1", "source", 1.5}, {"L", "1", "sink", 1.5}}}, nil, nil)
	r, _ := Run(b.json(), sim, DefaultOptions())
	fb, err := MergeFeedback(prev, r.Feedback)
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.Items) != 2 || fb.Items[0].ID != "fb-1" || fb.Items[1].ID != "post-widen-segment-1" || fb.Source != "pcb auto run" || len(fb.Rules) != 2 {
		blob, _ := json.Marshal(fb)
		t.Fatalf("merged %s", blob)
	}
}

// An inner layer without copper objects becomes an assumed ground plane
// that carries the return current; --plane none turns it off.
func TestNegativePlaneInference(t *testing.T) {
	b := newTB(1400, 600, 4)
	b.part("S", 1, pad("1", "GND", 1, 200, 300, 40, 40))
	b.part("L", 1, pad("1", "GND", 1, 1200, 300, 40, 40))
	b.via("GND", 200, 300, 24, 12)
	b.via("GND", 1200, 300, 24, 12)
	b.via("SIG", 700, 300, 24, 12) // another net: antipad in the plane
	sim := simDoc(map[string][][4]any{"GND": {{"S", "1", "sink", 1.0}, {"L", "1", "source", 1.0}}}, map[string]string{"GND": "ground"}, nil)
	r, err := Run(b.json(), sim, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if r.Settings.Planes["IN1"] != "GND" || r.Settings.Planes["IN2"] != "GND" {
		t.Fatalf("planes %v", r.Settings.Planes)
	}
	n := netOf(t, r, "GND")
	if n.Status != "info" || n.WorstMV <= 0 || n.WorstMV > 5 {
		t.Fatalf("GND through the planes: %+v", n)
	}
	o := DefaultOptions()
	o.Planes = map[int]string{15: "none", 16: "none"}
	r2, _ := Run(b.json(), sim, o)
	if n2 := netOf(t, r2, "GND"); n2.Status != "open" {
		t.Fatalf("without planes GND must be open, got %s", n2.Status)
	}
}

// The ARC convention of the dump: positive sweep = counter-clockwise (y-up).
func TestArcConvention(t *testing.T) {
	pts := arcPoints(Point{409.25, 1551.03}, Point{413.35, 1546.93}, 90)
	c := Point{413.35, 1551.03}
	for _, p := range pts {
		if math.Abs(dist(p, c)-4.1) > 1e-6 {
			t.Fatalf("arc point %+v not on the circle about %+v", p, c)
		}
	}
	mid := pts[len(pts)/2-1]
	if mid.X > 411.3 || mid.Y > 1549 {
		t.Fatalf("arc bulges the wrong way: %+v", mid)
	}
}

// The Elmer deck is well formed: counts in the header match the files,
// every element / boundary node exists, the body heat adds up to the
// solved power, and a fake probes.dat compares.
func TestElmerDeck(t *testing.T) {
	bj, sim := thermalPlate(1000, 600, 500, 300, 60, 0.5)
	o := DefaultOptions()
	r, err := Run(bj, sim, o)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := r.ExportElmer(dir); err != nil {
		t.Fatal(err)
	}
	e := r.Elmer
	hdr, _ := os.ReadFile(filepath.Join(dir, "mesh", "mesh.header"))
	f := strings.Fields(string(hdr))
	if len(f) < 3 || f[0] != itoa(e.Nodes) || f[1] != itoa(e.Elements) || f[2] != itoa(e.Boundary) {
		t.Fatalf("header %q vs %d/%d/%d", hdr, e.Nodes, e.Elements, e.Boundary)
	}
	lines := func(name string) []string {
		b, _ := os.ReadFile(filepath.Join(dir, "mesh", name))
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	if n := len(lines("mesh.nodes")); n != e.Nodes {
		t.Fatalf("nodes %d vs %d", n, e.Nodes)
	}
	for _, l := range lines("mesh.elements") {
		fs := strings.Fields(l)
		if len(fs) != 11 || fs[2] != "808" {
			t.Fatalf("element line %q", l)
		}
		for _, id := range fs[3:] {
			if v := atoi(id); v < 1 || v > e.Nodes {
				t.Fatalf("element node %s out of range", id)
			}
		}
	}
	top, bot := 0, 0
	for _, l := range lines("mesh.boundary") {
		fs := strings.Fields(l)
		if len(fs) != 9 || fs[4] != "404" {
			t.Fatalf("boundary line %q", l)
		}
		switch fs[1] {
		case "1":
			top++
		case "2":
			bot++
		}
	}
	if top != bot || top == 0 {
		t.Fatalf("top %d bottom %d faces", top, bot)
	}
	sif, _ := os.ReadFile(filepath.Join(dir, "case.sif"))
	if !bytes.Contains(sif, []byte(`Procedure = "HeatSolve" "HeatSolver"`)) || !bytes.Contains(sif, []byte("Heat Transfer Coefficient")) ||
		len(regexp.MustCompile(`(?m)^Body [0-9]+$`).FindAll(sif, -1)) != e.Bodies {
		t.Fatalf("case.sif malformed (%d bodies)", e.Bodies)
	}
	// Σ body heat × volume = the solved power (bodies conserve it).
	d, _ := r.elmerDeck()
	vol := map[int]float64{}
	h := r.grid.Cell * MilMm * 1e-3
	span := r.Stackup.spanM(0, 1)
	for _, el := range d.elems {
		vol[el[0]-1] += h * h * span
	}
	tot := 0.0
	for i, bd := range d.bodies {
		tot += bd.q * vol[i]
	}
	near(t, "Elmer deck total heat (W)", tot, r.Thermal.TotalW, 0.001)
	// Fake Elmer output: the model values +0.3 °C → agree; +30 °C → disagree.
	var row []string
	for _, p := range e.Probes {
		row = append(row, ftoa(p.ModelC+0.3))
	}
	r.CompareElmer([]byte("1 2 3\n" + strings.Join(row, " ") + "\n"))
	if e.Status != "agree" || math.Abs(e.MaxDiffC-0.3) > 1e-6 {
		t.Fatalf("status %s diff %.3f", e.Status, e.MaxDiffC)
	}
	row = row[:0]
	for _, p := range e.Probes {
		row = append(row, ftoa(p.ModelC+30))
	}
	r.CompareElmer([]byte(strings.Join(row, " ")))
	if e.Status != "disagree" || r.Verdict.Status == "pass" {
		t.Fatalf("status %s verdict %s", e.Status, r.Verdict.Status)
	}
	r.CompareElmer([]byte("1"))
	if e.Status != "error" {
		t.Fatalf("short file: %s", e.Status)
	}
	// Without ElmerSolver on PATH the check is skipped, never faked.
	t.Setenv("PATH", t.TempDir())
	r.RunElmer(0)
	if e.Status != "skipped" {
		t.Fatalf("status %s", e.Status)
	}
}

func itoa(v int) string { b, _ := json.Marshal(v); return string(b) }

func atoi(s string) int {
	var v int
	_ = json.Unmarshal([]byte(s), &v)
	return v
}
