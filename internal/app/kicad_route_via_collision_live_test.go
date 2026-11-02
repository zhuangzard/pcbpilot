package app

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// viaLiveBoard is the tiny test board with the given project file.
func viaLiveBoard(t *testing.T, pro string) (string, *kicad.Tools) {
	t.Helper()
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1")
	}
	kt, err := kicad.Locate()
	if err != nil {
		t.Skip(err)
	}
	src, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "tiny.kicad_pcb"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if k := os.Getenv("PCBPILOT_LIVE_KEEP_DIR"); k != "" { // keep the files for a look
		dir = k
		_ = os.MkdirAll(dir, 0o755)
	}
	pcb := filepath.Join(dir, "vias.kicad_pcb")
	if err := os.WriteFile(pcb, src, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(boardSibling(pcb, ".kicad_pro"), []byte(pro), 0o644); err != nil {
		t.Fatal(err)
	}
	return pcb, kt
}

// The padstack collision end to end: two fractional class vias merge on one
// whole-um name, which is also the Default class via (the name whose drill
// KiCad's import ignores), with fixed vias already on the board. Each via is
// read back after the import and after the shrink: identity, net, span and
// size, and each router via lands on its own class via. Needs
// PCBPILOT_KICAD_LIVE=1 and PCBPILOT_LIVE_FASTROUTE.
func TestRouterViaPadstackCollisionKiCadLive(t *testing.T) {
	if os.Getenv("PCBPILOT_LIVE_FASTROUTE") == "" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 and PCBPILOT_LIVE_FASTROUTE (a fastroute executable)")
	}
	pcb, kt := viaLiveBoard(t, `{"board":{"design_settings":{"rules":{"min_via_annular_width":0.13,"min_through_hole_diameter":0.2,"min_via_diameter":0.4,"min_clearance":0.1}}},
 "net_settings":{"classes":[
  {"name":"Default","clearance":0.2,"track_width":0.25,"via_diameter":0.61,"via_drill":0.305,"priority":2147483647},
  {"name":"C_A","clearance":0.2,"track_width":0.25,"via_diameter":0.6096,"via_drill":0.3048,"priority":0},
  {"name":"C_GND","clearance":0.2,"track_width":0.25,"via_diameter":0.6098,"via_drill":0.3049,"priority":1}],
  "netclass_patterns":[{"netclass":"C_A","pattern":"A"},{"netclass":"C_GND","pattern":"GND"}]},
 "meta":{"filename":"vias.kicad_pro","version":3}}`)
	dir := filepath.Dir(pcb)
	r := &kicadRun{kt: kt, work: dir, summary: map[string]any{}, stderr: os.Stderr, o: kicadRouteOpts{outDir: dir}}

	// Fixed vias, off the parts: one at a fractional size that merges, one at
	// the Default via, one well off every class.
	in, _, err := r.snapshot(pcb)
	if err != nil {
		t.Fatal(err)
	}
	if in.Outline == nil {
		t.Fatal("no outline")
	}
	bb := in.Outline.BBox
	fixedAt := func(i int) (float64, float64) { return bb.MinX + 60 + float64(i)*60, bb.MinY + 60 }
	type size struct{ dia, drill float64 } // um
	fixedSize := []size{{609.6, 304.8}, {610, 305}, {900, 500}}
	fixedNet := []string{"A", "GND", "VCC"}
	var add []map[string]any
	for i, s := range fixedSize {
		x, y := fixedAt(i)
		add = append(add, map[string]any{"net": fixedNet[i], "x": x, "y": y, "diameter": s.dia / 25.4, "drill": s.drill / 25.4})
	}
	seeded := filepath.Join(dir, "seeded.kicad_pcb")
	if _, err := kt.Edit(pcb, map[string]any{"addVias": add}, seeded); err != nil {
		t.Fatal(err)
	}

	near := func(a, b float64) bool { return math.Abs(a-b) < 0.01 }
	um := func(v float64) float64 { return v * 25.4 }
	readVias := func(p string) []routeVia {
		t.Helper()
		s, _, err := r.snapshot(p)
		if err != nil {
			t.Fatal(err)
		}
		vs, err := snapshotVias(s)
		if err != nil {
			t.Fatal(err)
		}
		return vs
	}
	seededVias := readVias(seeded)
	if len(seededVias) != len(fixedSize) {
		t.Fatalf("seeded vias: %+v", seededVias)
	}
	// KiCad's import gives every via a new uuid: identity is net and position.
	for _, v := range seededVias {
		if v.From != 1 || v.To != 2 {
			t.Fatalf("seeded via span: %+v", v)
		}
	}
	fixedOf := func(v routeVia) (size, bool) {
		for _, f := range seededVias {
			if f.Net == v.Net && math.Hypot(f.X-v.X, f.Y-v.Y) < 0.1 {
				return size{um(f.Dia), um(f.Drill)}, true
			}
		}
		return size{}, false
	}
	wantFixed := func(stage string, vs []routeVia, sizes bool) {
		t.Helper()
		seen := 0
		for _, v := range vs {
			s, ok := fixedOf(v)
			if !ok {
				continue
			}
			seen++
			if viaSpan(v) != "through" || sizes && (!near(um(v.Dia), s.dia) || !near(um(v.Drill), s.drill)) {
				t.Errorf("%s: fixed via %s on %s is %.3f/%.3f um %s, was %.3f/%.3f um", stage, v.ID, v.Net, um(v.Dia), um(v.Drill), viaSpan(v), s.dia, s.drill)
			}
		}
		if seen != len(seededVias) {
			t.Errorf("%s: %d of %d fixed vias found again", stage, seen, len(seededVias))
		}
	}

	dsn := filepath.Join(dir, "route.dsn")
	if _, err := kt.ExportDSN(seeded, dsn); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"609.6:304.8", "609.8:304.9", "610:305"} {
		if !strings.Contains(string(text), name) {
			t.Fatalf("the test needs %s in KiCad's export:\n%s", name, text)
		}
	}
	routed, _, top := dsnRouterVia(string(text))
	if top == nil || strings.Count(routed, `(padstack "Via[0-1]_610:305_um"`) != 1 || strings.Contains(routed, "609.") {
		t.Fatalf("router via %+v:\n%s", top, routed)
	}
	if err := os.WriteFile(dsn, []byte(routed), 0o644); err != nil {
		t.Fatal(err)
	}
	ses, runs, err := runFastroute(fastrouteOpts{bin: os.Getenv("PCBPILOT_LIVE_FASTROUTE"), maxTime: 30 * time.Second, timeout: 3 * time.Minute}, dsn, filepath.Join(dir, "route"), os.Stderr)
	if err != nil {
		t.Fatalf("%v %+v", err, runs)
	}
	imported := filepath.Join(dir, "imported.kicad_pcb")
	if _, err := kt.ImportSES(seeded, ses, imported); err != nil {
		t.Fatal(err)
	}
	afterImport := readVias(imported)
	for _, v := range afterImport {
		t.Logf("imported %s on %s: %.3f/%.3f um, layers %d-%d", short12(v.ID), v.Net, um(v.Dia), um(v.Drill), v.From, v.To)
	}
	wantFixed("after the import", afterImport, false) // sizes are the session's here

	out, gates := r.routerViasToClass(seeded, imported)
	if len(gates) != 0 {
		t.Fatalf("router vias: %+v", gates)
	}
	final := readVias(out)
	wantFixed("after the shrink", final, true)
	class := map[string]size{"A": {609.6, 304.8}, "GND": {609.8, 304.9}}
	nets := map[string]int{}
	for _, v := range final {
		t.Logf("final %s on %s: %.3f/%.3f um, layers %d-%d", short12(v.ID), v.Net, um(v.Dia), um(v.Drill), v.From, v.To)
		if _, ok := fixedOf(v); ok {
			continue
		}
		nets[v.Net]++
		want, ok := class[v.Net]
		if !ok {
			want = size{610, 305}
		}
		if !near(um(v.Dia), want.dia) || !near(um(v.Drill), want.drill) || viaSpan(v) != "through" {
			t.Errorf("router via %s on %s: %.3f/%.3f um %s, want its class via %v", short12(v.ID), v.Net, um(v.Dia), um(v.Drill), viaSpan(v), want)
		}
	}
	if nets["A"] == 0 || nets["GND"] == 0 {
		t.Fatalf("the routed board needs router vias on both merged classes: %v", nets)
	}

	// The audit agrees, and calls the fixed vias what they are.
	snap, _, err := r.snapshot(out)
	if err != nil {
		t.Fatal(err)
	}
	if g := r.viaAuditGate(out, snap); !g.Pass {
		t.Fatalf("via audit: %+v", g)
	}
	prov := r.viaProvenanceOf()
	for _, v := range final {
		_, isFixed := fixedOf(v)
		if got := prov(v); isFixed != (got == viaFromInput) {
			t.Errorf("%s on %s: provenance %s, fixed %v", short12(v.ID), v.Net, got, isFixed)
		}
	}
	rep, err := kt.DRC(out, filepath.Join(dir, "drc.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range rep.Violations {
		switch v.Rule {
		case "annular_width", "drill_out_of_range", "hole_size", "via_diameter":
			t.Errorf("DRC %s: %s", v.Rule, v.Message)
		}
	}
}

// The repair's baseline comparison rests on KiCad keeping item uuids through an
// edit: a violation between two vias, with another via resized, is the same
// violation by rule and objects. Needs PCBPILOT_KICAD_LIVE=1.
func TestViaRepairDRCBaselineIdentityAcrossEditKiCadLive(t *testing.T) {
	pcb, kt := viaLiveBoard(t, `{"board":{"design_settings":{"rules":{"min_via_annular_width":0.05,"min_through_hole_diameter":0.15,"min_via_diameter":0.3,"min_clearance":0.1}}},
 "net_settings":{"classes":[{"name":"Default","clearance":0.2,"track_width":0.25,"via_diameter":0.6,"via_drill":0.3,"priority":2147483647}]},
 "meta":{"filename":"vias.kicad_pro","version":3}}`)
	dir := filepath.Dir(pcb)
	r := &kicadRun{kt: kt, work: dir, summary: map[string]any{}, stderr: os.Stderr}
	in, _, err := r.snapshot(pcb)
	if err != nil {
		t.Fatal(err)
	}
	bb := in.Outline.BBox
	x, y := bb.MinX+60, bb.MinY+60
	seeded := filepath.Join(dir, "seeded.kicad_pcb")
	if _, err := kt.Edit(pcb, map[string]any{"addVias": []map[string]any{
		{"net": "A", "x": x, "y": y, "diameter": 23.622, "drill": 11.811},
		{"net": "B", "x": x + 30, "y": y, "diameter": 23.622, "drill": 11.811}, // too close to the first
		{"net": "C", "x": x + 300, "y": y, "diameter": 20.0, "drill": 10.0},
	}}, seeded); err != nil {
		t.Fatal(err)
	}
	base, err := kt.DRC(seeded, filepath.Join(dir, "base.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := r.snapshot(seeded)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := snapshotVias(s)
	if err != nil {
		t.Fatal(err)
	}
	var free, pair string
	for _, v := range vs {
		switch v.Net {
		case "C":
			free = v.ID
		case "A":
			pair = v.ID
		}
	}
	named := false
	for _, v := range base.Violations {
		for _, o := range v.Objs {
			named = named || o == pair
		}
	}
	if !named {
		t.Fatalf("the test needs a DRC violation naming the via %s: %+v", pair, base.Violations)
	}
	out := filepath.Join(dir, "vias-repair.kicad_pcb")
	if _, err := kt.Edit(seeded, map[string]any{"setVia": []map[string]any{{"id": free, "diameter": 23.622, "drill": 11.811}}}, out); err != nil {
		t.Fatal(err)
	}
	post, err := kt.DRC(out, filepath.Join(dir, "post.json"))
	if err != nil {
		t.Fatal(err)
	}
	if d := newDRCViolations(base, post); len(d) != 0 {
		t.Fatalf("the same board's violations look new after an unrelated edit: %v\nbase %+v\npost %+v", d, base.Violations, post.Violations)
	}
}

// The TraceMaker candidate's commit on real KiCad, with the board it would have
// written: an off-size input via stays input, the via it added is its own and
// off-size is judged, whether or not its uuids survive. Needs PCBPILOT_KICAD_LIVE=1.
func TestTracemakerCommitViaProvenanceKiCadLive(t *testing.T) {
	pcb, kt := viaLiveBoard(t, `{"board":{"design_settings":{"rules":{"min_via_annular_width":0.05,"min_through_hole_diameter":0.15,"min_via_diameter":0.3,"min_clearance":0.1}}},
 "net_settings":{"classes":[{"name":"Default","clearance":0.2,"track_width":0.25,"via_diameter":0.6,"via_drill":0.3,"priority":2147483647}]},
 "meta":{"filename":"vias.kicad_pro","version":3}}`)
	dir := filepath.Dir(pcb)
	r := &kicadRun{kt: kt, work: dir, summary: map[string]any{}, stderr: os.Stderr, o: kicadRouteOpts{outDir: dir}}
	in, _, err := r.snapshot(pcb)
	if err != nil {
		t.Fatal(err)
	}
	bb := in.Outline.BBox
	x, y := bb.MinX+60, bb.MinY+60
	classed := filepath.Join(dir, "classed.kicad_pcb")
	if _, err := kt.Edit(pcb, map[string]any{"addVias": []map[string]any{{"net": "VCC", "x": x, "y": y, "diameter": 900 / 25.4, "drill": 500 / 25.4}}}, classed); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tracemaker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := kt.Edit(classed, map[string]any{"addVias": []map[string]any{{"net": "A", "x": x + 100, "y": y, "diameter": 800 / 25.4, "drill": 400 / 25.4}}}, filepath.Join(dir, "tracemaker", "out.kicad_pcb")); err != nil {
		t.Fatal(err)
	}
	c := r.tracemakerCandidate(classed, nil, 0)
	c.Res = &routeResult{Router: routerTracemaker}
	board, err := c.commit(c.Res)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Res.Gates) != 0 {
		t.Fatalf("%+v", c.Res.Gates)
	}
	r.useCandidate(c, nil)
	snap, _, err := r.snapshot(board)
	if err != nil {
		t.Fatal(err)
	}
	g := r.viaAuditGate(board, snap)
	vias, err := snapshotVias(snap)
	if err != nil {
		t.Fatal(err)
	}
	of := r.viaProvenanceOf()
	for _, v := range vias {
		want := viaFromInput
		if v.Net == "A" {
			want = viaFromTracemaker
		}
		if got := of(v); got != want {
			t.Errorf("via on %s: %s, want %s", v.Net, got, want)
		}
	}
	// The off-size input via is reported; the off-size TraceMaker via fails.
	if g.Pass || g.CannotCheck || len(g.Items) != 1 || !strings.Contains(g.Items[0], viaFromTracemaker) || len(g.Info) != 1 {
		t.Fatalf("%+v", g)
	}
}
