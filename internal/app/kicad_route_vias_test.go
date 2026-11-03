package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

const testViaDSN = `(pcb x (structure (layer F.Cu) (via "Via[0-5]_800:200_um" "Via[0-5]_700:400_um" "Via[0-5]_600:300_um")
 (rule (width 200)))
  (library
    (padstack "Via[0-5]_600:300_um"
      (shape (circle F.Cu 600))
      (shape (circle B.Cu 600))
      (attach off)
    )
    (padstack "Via[0-5]_800:200_um"
      (shape (circle F.Cu 800))
      (shape (circle B.Cu 800))
      (attach off)
    )
    (padstack "Via[0-5]_700:400_um"
      (shape (circle F.Cu 700))
      (shape (circle B.Cu 700))
      (attach off)
    )
  ))`

// Mixed classes 0.8/0.2 and 0.7/0.4: fastroute's padstack is 0.8/0.4 (the
// largest diameter AND the largest drill), added and listed first.
func TestDSNRouterViaComponentwiseMax(t *testing.T) {
	out, order, top := dsnRouterVia(testViaDSN)
	if top == nil || top.dia != 800 || top.drill != 400 || top.name != `"Via[0-5]_800:400_um"` {
		t.Fatalf("router via %+v", top)
	}
	if len(order) != 1 || order[0] != top.name {
		t.Fatalf("order %v", order)
	}
	if !strings.Contains(out, `(via "Via[0-5]_800:400_um")`) || !strings.Contains(out, `(padstack "Via[0-5]_700:400_um"`) {
		t.Fatalf("via list:\n%s", out)
	}
	at := strings.Index(out, `(padstack "Via[0-5]_800:400_um"`)
	if at < 0 || !strings.Contains(out[at:at+200], "(circle F.Cu 800)") || !strings.Contains(out[at:at+200], "(circle B.Cu 800)") {
		t.Fatalf("padstack:\n%s", out)
	}
	if strings.Count(out, `(padstack "Via[0-5]_800:200_um"`) != 1 {
		t.Fatal("source padstack changed")
	}
	// The maximum already exists: no new padstack, it moves first.
	have := strings.Replace(testViaDSN, `"Via[0-5]_700:400_um"`, `"Via[0-5]_800:400_um"`, -1)
	have = strings.Replace(have, "(circle F.Cu 700)", "(circle F.Cu 800)", 1)
	out, order, top = dsnRouterVia(have)
	if top.name != `"Via[0-5]_800:400_um"` || order[0] != top.name || strings.Count(out, `(padstack "Via[0-5]_800:400_um"`) != 1 {
		t.Fatalf("existing max: %v %+v", order, top)
	}
	// An unreadable size leaves the DSN alone.
	odd := `(pcb x (structure (via "MyVia" "Via[0-5]_600:300_um")))`
	if got, o, v := dsnRouterVia(odd); got != odd || o != nil || v != nil {
		t.Fatalf("changed %s", got)
	}
}

// Every class via is reached by shrinking the componentwise maximum, with
// its own annular ring (1.0/0.5 → 1.0/0.2 = 0.4 mm ring, → 0.8/0.5 = 0.15
// mm); a via that cannot shrink to its class, or a net without a class via,
// is a problem, never skipped.
func TestClassViaPlan(t *testing.T) {
	mil := func(um float64) float64 { return um / 25.4 }
	class := map[string][2]int64{"A": {1000000, 200000}, "B": {800000, 500000}, "D": {1000000, 500000}}
	eff := func(net string) (int64, int64, bool) {
		c, ok := class[net]
		return c[0], c[1], ok
	}
	vias := []routeVia{
		{ID: "a", Net: "A", Dia: mil(1000), Drill: mil(500)},
		{ID: "b", Net: "B", Dia: mil(1000), Drill: mil(500)},
		{ID: "d", Net: "D", Dia: mil(1000), Drill: mil(500)}, // already its class
		{ID: "x", Net: "B", Dia: mil(1000), Drill: mil(400)}, // drill below the class: unreachable
		{ID: "u", Net: "NOCLASS", Dia: mil(1000), Drill: mil(500)},
		{ID: "old", Net: "A", Dia: mil(600), Drill: mil(300)}, // not a router via
	}
	ops, fail, unknown, n := classViaPlan(vias, map[string]bool{"old": true}, eff)
	if n != 5 || len(ops) != 2 || len(fail) != 1 || len(unknown) != 1 {
		t.Fatalf("ops %v fail %v unknown %v n %d", ops, fail, unknown, n)
	}
	for _, op := range ops {
		c := class[map[string]string{"a": "A", "b": "B"}[op["id"].(string)]]
		dia, drill := op["diameter"].(float64)*25400, op["drill"].(float64)*25400
		if math.Abs(dia-float64(c[0])) > 1 || math.Abs(drill-float64(c[1])) > 1 {
			t.Fatalf("op %v != class %v", op, c)
		}
		if ring := (dia - drill) / 2; ring < 149999 {
			t.Fatalf("ring %v", ring)
		}
	}
	if !strings.Contains(fail[0], "cannot be shrunk") || !strings.Contains(unknown[0], "NOCLASS") {
		t.Fatalf("%v %v", fail, unknown)
	}
}

// fakeViaTools answers sizeRouterVias' KiCad calls; failOn names the one
// that fails ("snapshot-before", "snapshot-after", "rules-read", "edit").
type fakeViaTools struct {
	failOn string
	edited map[string]any
	// before / after replace the snapshots' JSON when set.
	before, after string
	// rb replaces the class readback, drc / drcErr answer DRC (repair path).
	rb     *kicad.RulesReadback
	drc    *kicad.DRCReport
	drcErr error
	// base / baseErr, when set, answer the DRC of the board before the repair
	// (a path without "repair"); drcSeq answers the repaired boards in turn.
	base    *kicad.DRCReport
	baseErr error
	drcSeq  []*kicad.DRCReport
}

func (f *fakeViaTools) DRC(pcb, out string) (*kicad.DRCReport, error) {
	if !strings.Contains(pcb, "repair") {
		if f.baseErr != nil {
			return nil, f.baseErr
		}
		if f.base != nil {
			return f.base, nil
		}
	}
	if len(f.drcSeq) > 0 {
		r := f.drcSeq[0]
		f.drcSeq = f.drcSeq[1:]
		return r, nil
	}
	return f.drc, f.drcErr
}

func (f *fakeViaTools) Snapshot(pcb string) ([]byte, error) {
	if pcb == "classed" && f.failOn == "snapshot-before" || pcb == "imported" && f.failOn == "snapshot-after" {
		return nil, errors.New("pcbnew crashed")
	}
	if pcb == "classed" {
		if f.before != "" {
			return []byte(f.before), nil
		}
		return []byte(`{"copper":{"vias":[]}}`), nil
	}
	if f.after != "" {
		return []byte(f.after), nil
	}
	return []byte(`{"copper":{"vias":[{"primitiveId":"v1","net":"A","diameter":31.4961,"holeDiameter":15.748}]}}`), nil // 0.8/0.4 mm
}

func (f *fakeViaTools) RulesRead(pcb string) (*kicad.RulesReadback, error) {
	if f.failOn == "rules-read" {
		return nil, errors.New("bridge rules-read failed")
	}
	if f.rb != nil {
		return f.rb, nil
	}
	return &kicad.RulesReadback{Nets: map[string]kicad.EffectiveNM{"A": {ViaDiameter: 800000, ViaDrill: 200000}}}, nil
}

func (f *fakeViaTools) Edit(pcb string, ops map[string]any, out string) (json.RawMessage, error) {
	if f.failOn == "edit" {
		return nil, errors.New("bridge edit rejected")
	}
	f.edited = ops
	return json.RawMessage(`{}`), nil
}

// Every way the router vias can go unsized is a router-vias gate on the
// result (cannot-check without data, failing when the vias are known
// wrong); the board returned is never claimed resized.
func TestSizeRouterViasErrorPaths(t *testing.T) {
	next := func(string) string { return "resized" }
	for _, c := range []struct {
		failOn, wantBoard, wantDetail string
	}{
		{"snapshot-before", "imported", "cannot check: the board before the import"},
		{"snapshot-after", "imported", "cannot check: the imported board"},
		{"rules-read", "imported", "cannot check: KiCad's class readback"},
		{"edit", "imported", "the resize could not be written"},
		{"", "resized", ""},
	} {
		f := &fakeViaTools{failOn: c.failOn}
		board, gates := sizeRouterVias(f, "classed", "imported", next, map[string]any{}, io.Discard)
		if board != c.wantBoard {
			t.Fatalf("%s: board %s", c.failOn, board)
		}
		if c.wantDetail == "" {
			if len(gates) != 0 || f.edited == nil {
				t.Fatalf("ok path: gates %+v edit %v", gates, f.edited)
			}
			continue
		}
		if len(gates) == 0 || gates[0].Gate != "router-vias" || gates[0].Pass || !strings.Contains(gates[0].Detail, c.wantDetail) {
			t.Fatalf("%s: gates %+v", c.failOn, gates)
		}
	}
}

// A vias list that does not decode (or a via without an id or size) on
// either snapshot is a cannot-check gate with no via edits — never an empty
// list that hides router vias or makes a board's own vias look new.
func TestSizeRouterViasMalformedSnapshots(t *testing.T) {
	next := func(string) string { return "resized" }
	for _, c := range []struct{ name, before, after, want string }{
		{"before not a list", `{"copper":{"vias":{"x":1}}}`, "", "the board before the import"},
		{"before via without id", `{"copper":{"vias":[{"net":"A","diameter":31.5,"holeDiameter":15.7}]}}`, "", "the board before the import"},
		{"before without copper", `{}`, "", "the board before the import"},
		{"before vias absent", `{"copper":{"lines":[]}}`, "", "the board before the import"},
		{"before vias null", `{"copper":{"vias":null}}`, "", "the board before the import"},
		{"after vias absent", "", `{"copper":{"lines":[]}}`, "the imported board"},
		{"after vias null", "", `{"copper":{"vias":null}}`, "the imported board"},
		{"after wrong type", "", `{"copper":{"vias":[{"primitiveId":"v1","net":"A","diameter":"big","holeDiameter":15.7}]}}`, "the imported board"},
		{"after without size", "", `{"copper":{"vias":[{"primitiveId":"v1","net":"A"}]}}`, "the imported board"},
	} {
		f := &fakeViaTools{before: c.before, after: c.after}
		board, gates := sizeRouterVias(f, "classed", "imported", next, map[string]any{}, io.Discard)
		if board != "imported" || f.edited != nil {
			t.Fatalf("%s: board %s edits %v", c.name, board, f.edited)
		}
		if len(gates) != 1 || gates[0].Gate != "router-vias" || gates[0].Pass || !strings.Contains(gates[0].Detail, "cannot check: "+c.want) || !strings.Contains(gates[0].Detail, "none resized") {
			t.Fatalf("%s: gates %+v", c.name, gates)
		}
	}
}

// Only an explicit [] is "no vias": the ok path still runs on it.
func TestSnapshotViasExplicitEmpty(t *testing.T) {
	var s boardSnapshot
	if err := json.Unmarshal([]byte(`{"copper":{"vias":[]}}`), &s); err != nil {
		t.Fatal(err)
	}
	if vs, err := snapshotVias(&s); err != nil || len(vs) != 0 {
		t.Fatalf("explicit []: %v %v", vs, err)
	}
	for _, j := range []string{`{"copper":{"vias":null}}`, `{"copper":{}}`} {
		var s boardSnapshot
		_ = json.Unmarshal([]byte(j), &s)
		if _, err := snapshotVias(&s); err == nil {
			t.Fatalf("%s accepted as empty", j)
		}
	}
}

// The class via tolerance is 100 nm everywhere: reachability, the shrink plan
// and the repair's candidates agree at the boundary, in both dimensions.
func TestClassViaToleranceBoundary(t *testing.T) {
	const cd, ch = int64(700000), int64(400000)
	eff := func(string) (int64, int64, bool) { return cd, ch, true }
	via := func(dia, drill int64) routeVia {
		return routeVia{ID: "v", Net: "A", Dia: float64(dia) / nmPerMil, Drill: float64(drill) / nmPerMil}
	}
	for _, c := range []struct {
		name          string
		dia, drill    int64
		ops, fail, gr int
	}{
		{"exact", cd, ch, 0, 0, 0},
		{"100 nm short in diameter", cd - 100, ch, 0, 0, 0},
		{"101 nm short in diameter", cd - 101, ch, 0, 1, 1},
		{"100 nm short in drill", cd, ch - 100, 0, 0, 0},
		{"101 nm short in drill", cd, ch - 101, 0, 1, 1},
		{"300 nm short", cd - 300, ch - 300, 0, 1, 1},
		{"100 nm over", cd + 100, ch + 100, 0, 0, 0},
		{"101 nm over shrinks", cd + 101, ch, 1, 0, 0},
		{"rounded up to whole um shrinks", 701000, 401000, 1, 0, 0},
	} {
		v := []routeVia{via(c.dia, c.drill)}
		ops, fail, unknown, _ := classViaPlan(v, nil, eff)
		gr := viaGrowCandidates(v, nil, eff)
		if len(ops) != c.ops || len(fail) != c.fail || len(unknown) != 0 || len(gr) != c.gr {
			t.Errorf("%s: ops %v fail %v unknown %v grow %d", c.name, ops, fail, unknown, len(gr))
		}
	}
	// A fractional class via after its shrink, read back at the snapshot's 4
	// decimals of a mil, is the class via: no further op, no failure.
	round := func(nm int64) float64 { return math.Round(float64(nm)/nmPerMil*1e4) / 1e4 }
	for _, c := range [][2]int64{{609600, 304800}, {700024, 400050}, {599999, 299999}} {
		v := []routeVia{{ID: "v", Net: "A", Dia: round(c[0]), Drill: round(c[1])}}
		ops, fail, _, _ := classViaPlan(v, nil, func(string) (int64, int64, bool) { return c[0], c[1], true })
		if len(ops)+len(fail) != 0 {
			t.Errorf("%v read back: ops %v fail %v", c, ops, fail)
		}
	}
}

// A via 300 nm short used to pass unnoticed; it now goes through the checked
// repair like any other via below its class.
func TestSizeRouterViasRepairsSmallShortfall(t *testing.T) {
	f := fakeRepairTools("", &kicad.DRCReport{}, nil)
	f.after = fmt.Sprintf(`{"copper":{"lines":[],"vias":[{"primitiveId":"v1","net":"A","x":100,"y":100,"diameter":%v,"holeDiameter":%v}]}}`, 699700/nmPerMil, 400000/nmPerMil)
	board, gates := sizeRouterVias(f, "classed", "imported", func(s string) string { return s + ".kicad_pcb" }, map[string]any{}, io.Discard)
	if board != "vias-repair.kicad_pcb" || len(gates) != 1 || !gates[0].Pass || f.edited == nil {
		t.Fatalf("%s %+v %v", board, gates, f.edited)
	}
}

// Each circle is rounded on its own: a layer-specific padstack keeps its larger
// inner shape, and a smaller shape is not blown up to the name's diameter.
func TestDSNWholeUmViasPerLayerShapes(t *testing.T) {
	dsn := `(pcb x
  (structure (via "Via[0-3]_609.6:304.8_um"))
  (library
    (padstack "Via[0-3]_609.6:304.8_um"
      (shape (circle F.Cu 609.6))
      (shape (circle In1.Cu 800))
      (shape (circle In2.Cu 750.2))
      (shape (circle B.Cu 500.5))
      (attach off)
    )
  )
)
`
	out := dsnWholeUmVias(dsn)
	for _, want := range []string{`(padstack "Via[0-3]_610:305_um"`, "(circle F.Cu 610)", "(circle In1.Cu 800)", "(circle In2.Cu 751)", "(circle B.Cu 501)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// The router via built from it keeps the inner shape too.
	routed, _, top := dsnRouterVia(dsn)
	if top == nil || !strings.Contains(routed, "(circle In1.Cu 800)") {
		t.Fatalf("%+v\n%s", top, routed)
	}
}

// A padstack merged into another on the same whole-um name never shrinks: the
// one left has the larger circle on each layer, and the layers only it had.
func TestDSNWholeUmViasMergeKeepsLargerShapes(t *testing.T) {
	dsn := `(pcb x
  (structure (via "Via[0-3]_609.6:304.8_um" "Via[0-3]_609.9:304.9_um" "Via[0-3]_610:305_um"))
  (library
    (padstack "Via[0-3]_609.6:304.8_um"
      (shape (circle F.Cu 609.6))
      (shape (circle In1.Cu 800))
      (attach off)
    )
    (padstack "Via[0-3]_609.9:304.9_um"
      (shape (circle F.Cu 609.9))
      (shape (circle B.Cu 900))
      (attach off)
    )
    (padstack "Via[0-3]_610:305_um"
      (shape (circle F.Cu 610))
      (shape (circle In1.Cu 610))
      (attach off)
    )
  )
)
`
	out := dsnWholeUmVias(dsn)
	if strings.Count(out, "(padstack ") != 1 {
		t.Fatalf("not merged:\n%s", out)
	}
	for _, want := range []string{"(circle F.Cu 610)", "(circle In1.Cu 800)", "(circle B.Cu 900)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// Two fractional padstacks and no whole-um one: the first holds the name.
	dsn2 := strings.Replace(dsn, `    (padstack "Via[0-3]_610:305_um"
      (shape (circle F.Cu 610))
      (shape (circle In1.Cu 610))
      (attach off)
    )
`, "", 1)
	out = dsnWholeUmVias(dsn2)
	if strings.Count(out, "(padstack ") != 1 || !strings.Contains(out, `(padstack "Via[0-3]_610:305_um"`) || !strings.Contains(out, "(circle In1.Cu 800)") || !strings.Contains(out, "(circle B.Cu 900)") {
		t.Fatalf("%s", out)
	}
}

// KiCad's import replaces every via's uuid and resizes the board's own: they are
// found again by net and position, put back at their own size, and neither
// shrunk to a class nor counted as router vias.
func TestSizeRouterViasKeepsInputVias(t *testing.T) {
	const big, bigDrill = 35.4331, 19.685 // 900/500 um
	f := &fakeViaTools{
		before: fmt.Sprintf(`{"copper":{"vias":[
			{"primitiveId":"in-big","net":"A","x":100,"y":100,"diameter":%v,"holeDiameter":%v},
			{"primitiveId":"in-frac","net":"A","x":300,"y":100,"diameter":24,"holeDiameter":12},
			{"primitiveId":"in-same","net":"B","x":500,"y":100,"diameter":24,"holeDiameter":12},
			{"primitiveId":"in-gone","net":"A","x":700,"y":100,"diameter":24,"holeDiameter":12}]}}`, big, bigDrill),
		after: fmt.Sprintf(`{"copper":{"vias":[
			{"primitiveId":"new1","net":"A","x":100,"y":100,"diameter":%v,"holeDiameter":%v},
			{"primitiveId":"new2","net":"A","x":300,"y":100,"diameter":24.0157,"holeDiameter":12.0079},
			{"primitiveId":"in-same","net":"B","x":500,"y":100,"diameter":24,"holeDiameter":12},
			{"primitiveId":"other-net","net":"B","x":700,"y":100,"diameter":31.4961,"holeDiameter":15.748},
			{"primitiveId":"router","net":"A","x":900,"y":100,"diameter":31.4961,"holeDiameter":15.748}]}}`, big, bigDrill),
		rb: &kicad.RulesReadback{Nets: map[string]kicad.EffectiveNM{"A": {ViaDiameter: 609600, ViaDrill: 304800}, "B": {ViaDiameter: 609600, ViaDrill: 304800}}},
	}
	summary := map[string]any{}
	prov := &viaProvenance{}
	board, gates := sizeRouterViasProv(f, "classed", "imported", func(s string) string { return s }, summary, io.Discard, prov)
	if board != "vias-to-class" {
		t.Fatalf("board %s", board)
	}
	ops := map[string]map[string]any{}
	for _, op := range f.edited["setVia"].([]map[string]any) {
		ops[op["id"].(string)] = op
	}
	if len(ops) != 3 || ops["new1"] != nil || ops["in-same"] != nil {
		t.Fatalf("ops %v", ops)
	}
	if op := ops["new2"]; op == nil || op["diameter"] != 24.0 || op["drill"] != 12.0 {
		t.Fatalf("input via not restored: %v", op)
	}
	if ops["router"] == nil || ops["other-net"] == nil { // router vias, one where an input via of another net was
		t.Fatalf("ops %v", ops)
	}
	stats := summary["routerVias"].(map[string]any)
	if stats["routerVias"] != 2 || stats["shrunkToClass"] != 2 || stats["inputViasRestored"] != 1 || stats["inputViasGone"] != 1 {
		t.Fatalf("%v", stats)
	}
	if !prov.input["new1"] || !prov.input["new2"] || !prov.input["in-same"] || !prov.router["router"] || prov.input["router"] {
		t.Fatalf("%+v", prov)
	}
	// An input via that is gone is a known failure.
	if len(gates) != 1 || gates[0].Pass || gates[0].CannotCheck || !strings.Contains(gates[0].Items[0], "in-gone") {
		t.Fatalf("%+v", gates)
	}
}

func matchVia(id, net string, x, y float64, from, to int, diaUm, drillUm float64) routeVia {
	return routeVia{ID: id, Net: net, X: x, Y: y, From: from, To: to, Dia: diaUm * 1000 / nmPerMil, Drill: drillUm * 1000 / nmPerMil}
}

func noClassDrill(string) (int64, bool) { return 0, false }

// One to one, on the same span, exact coordinates first; anything else is
// cannot-check with no classification.
func TestMatchInputViasStackedAndCompeting(t *testing.T) {
	// Stacked vias of one net at one spot: each keeps its own size by its span.
	m := matchInputVias(
		[]routeVia{matchVia("b1", "A", 100, 100, 1, 15, 300, 150), matchVia("b2", "A", 100, 100, 15, 2, 500, 250)},
		[]routeVia{matchVia("n2", "A", 100, 100, 2, 15, 500, 250), matchVia("n1", "A", 100, 100, 15, 1, 300, 150)}, noClassDrill)
	if !m.input["n1"] || !m.input["n2"] || len(m.restore)+len(m.gone)+len(m.unchecked) != 0 {
		t.Fatalf("stacked: %+v", m)
	}
	// A different span at the spot is not the input via.
	m = matchInputVias([]routeVia{matchVia("b", "A", 100, 100, 1, 2, 600, 300)}, []routeVia{matchVia("n", "A", 100, 100, 1, 15, 600, 300)}, noClassDrill)
	if m.input["n"] || len(m.gone) != 1 {
		t.Fatalf("span: %+v", m)
	}
	// A router via nearby does not compete with the one at the exact place.
	m = matchInputVias([]routeVia{matchVia("b", "A", 100, 100, 1, 2, 900, 500)},
		[]routeVia{matchVia("near", "A", 100.3, 100, 1, 2, 600, 300), matchVia("n", "A", 100, 100, 1, 2, 900, 500)}, noClassDrill)
	if !m.input["n"] || m.input["near"] || m.ambiguous["near"] || len(m.unchecked) != 0 {
		t.Fatalf("exact first: %+v", m)
	}
	// Two within the tolerance, none exact: not a guess.
	m = matchInputVias([]routeVia{matchVia("b", "A", 100, 100, 1, 2, 600, 300)},
		[]routeVia{matchVia("x", "A", 100.3, 100, 1, 2, 600, 300), matchVia("y", "A", 99.7, 100, 1, 2, 600, 300)}, noClassDrill)
	if len(m.input) != 0 || !m.ambiguous["x"] || !m.ambiguous["y"] || len(m.unchecked) != 1 || len(m.gone) != 0 {
		t.Fatalf("two near: %+v", m)
	}
	// Two input vias stacked on one span, with one via left: which one is unknown.
	m = matchInputVias([]routeVia{matchVia("b1", "A", 100, 100, 1, 2, 600, 300), matchVia("b2", "A", 100, 100, 1, 2, 800, 400)},
		[]routeVia{matchVia("n", "A", 100, 100, 1, 2, 800, 400)}, noClassDrill)
	if len(m.input) != 0 || !m.ambiguous["n"] || len(m.unchecked) != 2 {
		t.Fatalf("one for two: %+v", m)
	}
	// Two and two, same span and spot: no pairing can be shown.
	m = matchInputVias([]routeVia{matchVia("b1", "A", 100, 100, 1, 2, 600, 300), matchVia("b2", "A", 100, 100, 1, 2, 800, 400)},
		[]routeVia{matchVia("n1", "A", 100, 100, 1, 2, 800, 400), matchVia("n2", "A", 100, 100, 1, 2, 600, 300)}, noClassDrill)
	if len(m.input) != 0 || len(m.ambiguous) != 2 || len(m.restore) != 0 {
		t.Fatalf("two for two: %+v", m)
	}
	// An id that came through with another net is not called input.
	m = matchInputVias([]routeVia{matchVia("b", "A", 100, 100, 1, 2, 600, 300)}, []routeVia{matchVia("b", "B", 100, 100, 1, 2, 600, 300)}, noClassDrill)
	if m.input["b"] || !m.ambiguous["b"] || len(m.unchecked) != 1 {
		t.Fatalf("id: %+v", m)
	}
}

// At an input via's coordinates, a size the import cannot make of it may be a
// router via that replaced it: neither input, nor sized as a router via.
func TestMatchInputViasRouterReplacement(t *testing.T) {
	class := func(string) (int64, bool) { return 400000, true }
	for _, c := range []struct {
		name          string
		b, a          routeVia
		input, restor bool
	}{
		{"same size", matchVia("b", "A", 1, 1, 1, 2, 900, 500), matchVia("n", "A", 1, 1, 1, 2, 900, 500), true, false},
		{"rounded up", matchVia("b", "A", 1, 1, 1, 2, 609.6, 304.8), matchVia("n", "A", 1, 1, 1, 2, 610, 305), true, true},
		{"class drill", matchVia("b", "A", 1, 1, 1, 2, 600, 300), matchVia("n", "A", 1, 1, 1, 2, 600, 400), true, true},
		{"smaller", matchVia("b", "A", 1, 1, 1, 2, 900, 500), matchVia("n", "A", 1, 1, 1, 2, 600, 300), false, false},
		{"larger", matchVia("b", "A", 1, 1, 1, 2, 600, 300), matchVia("n", "A", 1, 1, 1, 2, 800, 400), false, false},
		{"over a whole um", matchVia("b", "A", 1, 1, 1, 2, 609.6, 304.8), matchVia("n", "A", 1, 1, 1, 2, 611, 305), false, false},
		{"between original and ceiling, diameter", matchVia("b", "A", 1, 1, 1, 2, 609.6, 304.8), matchVia("n", "A", 1, 1, 1, 2, 609.8, 304.8), false, false},
		{"between original and ceiling, drill", matchVia("b", "A", 1, 1, 1, 2, 609.6, 304.4), matchVia("n", "A", 1, 1, 1, 2, 609.6, 304.7), false, false},
		{"diameter rounded, drill kept", matchVia("b", "A", 1, 1, 1, 2, 609.6, 304.8), matchVia("n", "A", 1, 1, 1, 2, 610, 304.8), true, true},
		{"drill rounded, diameter kept", matchVia("b", "A", 1, 1, 1, 2, 609.6, 304.8), matchVia("n", "A", 1, 1, 1, 2, 609.6, 305), true, true},
		{"just above a whole um rounds up", matchVia("b", "A", 1, 1, 1, 2, 600.05, 300.05), matchVia("n", "A", 1, 1, 1, 2, 601, 301), true, true},
		{"just above, own size", matchVia("b", "A", 1, 1, 1, 2, 600.05, 300.05), matchVia("n", "A", 1, 1, 1, 2, 600.05, 300.05), true, false},
		{"100 nm above rounds up", matchVia("b", "A", 1, 1, 1, 2, 600.1, 300), matchVia("n", "A", 1, 1, 1, 2, 601, 300), true, true},
		{"just above is not one further up", matchVia("b", "A", 1, 1, 1, 2, 600.05, 300), matchVia("n", "A", 1, 1, 1, 2, 602, 300), false, false},
		{"a whole um stays", matchVia("b", "A", 1, 1, 1, 2, 600, 300), matchVia("n", "A", 1, 1, 1, 2, 601, 300), false, false},
	} {
		m := matchInputVias([]routeVia{c.b}, []routeVia{c.a}, class)
		if m.input["n"] != c.input || m.ambiguous["n"] == c.input || (len(m.restore) > 0) != c.restor || len(m.gone) != 0 || (len(m.unchecked) > 0) == c.input {
			t.Errorf("%s: %+v", c.name, m)
		}
	}
}

// The ceiling is the one the DSN gets, and the snapshot's rounding of a whole um
// (305000 nm reads as 12.0079 mil, 305001 nm) does not push it one further.
func TestViaSizeFromImportCeilingMatchesDSN(t *testing.T) {
	for _, um := range []string{"600.05", "600.1", "609.6", "700.024", "400.05"} {
		name := `"Via[0-1]_` + um + `:` + um + `_um"`
		out := dsnWholeUmVias(`(pcb (library (padstack ` + name + ` (shape (circle F.Cu ` + um + `)))))`)
		m := reDSNViaPadstack.FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("%s: %s", um, out)
		}
		was, _ := strconv.ParseFloat(um, 64)
		got, _ := strconv.ParseFloat(m[2], 64)
		b, a := matchVia("b", "A", 0, 0, 1, 2, was, was), matchVia("n", "A", 0, 0, 1, 2, got, got)
		if !viaSizeFromImport(b, a, noClassDrill) {
			t.Errorf("%s -> %s not accepted", um, m[2])
		}
	}
	snap := func(nm float64) float64 { return math.Round(nm/nmPerMil*1e4) / 1e4 }
	b := routeVia{Net: "A", Dia: snap(610000), Drill: snap(305000)}
	if int64(math.Round(b.Drill*nmPerMil)) <= 305000 {
		t.Fatalf("the test needs a reading above the whole um: %v", b.Drill*nmPerMil)
	}
	if !viaSizeFromImport(b, routeVia{Net: "A", Dia: snap(610000), Drill: snap(305000)}, noClassDrill) {
		t.Fatal("own size")
	}
	a := matchVia("n", "A", 0, 0, 1, 2, 610, 305.5)
	if viaSizeFromImport(b, a, noClassDrill) {
		t.Fatal("between a reading and its ceiling")
	}
}

// The pre-write check looks only at copper on layers the via spans; DRC still
// has the last word.
func TestViaGrowConflictLayers(t *testing.T) {
	rb := &kicad.RulesReadback{Nets: map[string]kicad.EffectiveNM{
		"A": {ViaDiameter: 700000, ViaDrill: 400000, Clearance: 200000},
		"B": {ViaDiameter: 700000, ViaDrill: 400000, Clearance: 200000},
	}}
	const f, b, in1, in2, in3 = 1, 2, 15, 16, 17
	grow := func(from, to int) viaGrow {
		return viaGrow{v: routeVia{ID: "v", Net: "A", X: 100, Y: 100, From: from, To: to}, classDia: 700000, drill: 400000}
	}
	track := func(layer int) []specctra.Track {
		return []specctra.Track{{Net: "B", Layer: layer, X1: 100, Y1: 110, X2: 300, Y2: 110, Width: 8}}
	}
	for _, c := range []struct {
		name  string
		g     viaGrow
		layer int
		want  bool
	}{
		{"blind F-In1, track on F", grow(f, in1), f, true},
		{"blind F-In1, track on In1", grow(f, in1), in1, true},
		{"blind F-In1, track on In2", grow(f, in1), in2, false},
		{"blind F-In1, track on B", grow(f, in1), b, false},
		{"reversed ids", grow(in1, f), b, false},
		{"buried In1-In2, track on F", grow(in1, in2), f, false},
		{"buried In1-In2, track on B", grow(in1, in2), b, false},
		{"buried In1-In3, track on In2", grow(in1, in3), in2, true},
		{"blind In2-B, track on In1", grow(in2, b), in1, false},
		{"blind In2-B, track on In3", grow(in2, b), in3, true},
		{"through, track on an inner layer", grow(f, b), in2, true},
		{"span unknown counts as all", grow(0, 0), b, true},
		{"track layer unknown counts", grow(f, in1), 0, true},
	} {
		why, _ := viaGrowConflict(c.g, track(c.layer), nil, nil, rb, nil)
		if (why != "") != c.want {
			t.Errorf("%s: %q", c.name, why)
		}
	}
	for _, c := range []struct {
		name     string
		g        viaGrow
		from, to int
		want     bool
	}{
		{"same span", grow(f, in1), f, in1, true},
		{"share a layer", grow(f, in2), in1, in3, true},
		{"stacked, no shared layer", grow(f, in1), in2, b, false},
		{"other via through", grow(f, in1), f, b, true},
		{"other via's span unknown", grow(f, in1), 0, 0, true},
	} {
		v := []widenVia{{Net: "B", X: 100, Y: 120, Diameter: 24, From: c.from, To: c.to}}
		why, _ := viaGrowConflict(c.g, nil, v, nil, rb, nil)
		if (why != "") != c.want {
			t.Errorf("%s: %q", c.name, why)
		}
	}
	for _, c := range []struct {
		name  string
		g     viaGrow
		layer int
		want  bool
	}{
		{"top pad, blind from the top", grow(f, in1), pcbSideTop, true},
		{"bottom pad, blind from the top", grow(f, in1), pcbSideBottom, false},
		{"top pad, buried", grow(in1, in2), pcbSideTop, false},
		{"through-hole pad, buried", grow(in1, in2), pcbLayerMulti, true},
	} {
		p := []boardPad{{Net: "B", Number: "1", Layer: c.layer, X: 100, Y: 125, W: 30, H: 30}}
		why, _ := viaGrowConflict(c.g, nil, nil, p, rb, nil)
		if (why != "") != c.want {
			t.Errorf("%s: %q", c.name, why)
		}
	}
	// Through the repair: a track on another layer at the same XY is not in the
	// way, and the snapshot's via layers reach the check. DRC still runs.
	f2 := fakeRepairTools(`{"primitiveId":"t","net":"B","layer":2,"startX":100,"startY":110,"endX":300,"endY":110,"lineWidth":8}`, &kicad.DRCReport{}, nil)
	f2.after = `{"copper":{"lines":[{"primitiveId":"t","net":"B","layer":2,"startX":100,"startY":110,"endX":300,"endY":110,"lineWidth":8}],
		"vias":[{"primitiveId":"v1","net":"A","x":100,"y":100,"fromLayer":1,"toLayer":15,"diameter":23.622,"holeDiameter":15.748},
		{"primitiveId":"o","net":"B","x":100,"y":120,"fromLayer":16,"toLayer":2,"diameter":24,"holeDiameter":12}]}}`
	f2.before = `{"copper":{"vias":[{"primitiveId":"o","net":"B","x":100,"y":120,"fromLayer":16,"toLayer":2,"diameter":24,"holeDiameter":12}]}}`
	board, gates := sizeRouterVias(f2, "classed", "imported", func(s string) string { return s + ".kicad_pcb" }, map[string]any{}, io.Discard)
	if board != "vias-repair.kicad_pcb" || len(gates) != 1 || !gates[0].Pass {
		t.Fatalf("%s %+v", board, gates)
	}
}

// Through sizeRouterVias: a replacement is left alone, with provenance unknown
// and a cannot-check gate, and is not shrunk to its class.
func TestSizeRouterViasReplacementIsCannotCheck(t *testing.T) {
	f := &fakeViaTools{
		before: `{"copper":{"vias":[{"primitiveId":"b","net":"A","x":1,"y":1,"fromLayer":1,"toLayer":2,"diameter":35.4331,"holeDiameter":19.685}]}}`,
		after:  `{"copper":{"vias":[{"primitiveId":"n","net":"A","x":1,"y":1,"fromLayer":1,"toLayer":2,"diameter":31.4961,"holeDiameter":15.748}]}}`,
		rb:     &kicad.RulesReadback{Nets: map[string]kicad.EffectiveNM{"A": {ViaDiameter: 609600, ViaDrill: 304800}}},
	}
	prov := &viaProvenance{}
	board, gates := sizeRouterViasProv(f, "classed", "imported", func(s string) string { return s }, map[string]any{}, io.Discard, prov)
	if board != "imported" || f.edited != nil || prov.input["n"] || prov.router["n"] {
		t.Fatalf("%s %v %+v", board, f.edited, prov)
	}
	if len(gates) != 1 || !gates[0].CannotCheck || !strings.Contains(gates[0].Items[0], "may be a router via") {
		t.Fatalf("%+v", gates)
	}
}

// The TraceMaker path: input vias are recognised whatever their size, its own
// vias are recorded as its own and judged honestly, declared exceptions apply,
// and nothing is resized.
func TestReconcileTracemakerVias(t *testing.T) {
	via := func(id, net string, x float64, dia, drill float64) string {
		return fmt.Sprintf(`{"primitiveId":%q,"net":%q,"x":%v,"y":100,"fromLayer":1,"toLayer":2,"diameter":%v,"holeDiameter":%v}`, id, net, x, dia/25.4, drill/25.4)
	}
	before := []string{via("offsize", "GND", 100, 900, 500)}
	after := []string{via("offsize", "GND", 100, 900, 500), via("okay", "GND", 300, 700, 400), via("wrong", "GND", 500, 600, 300), via("stitch", "SIG", 700, 800, 400)}
	f := &fakeViaTools{
		before: `{"copper":{"vias":[` + strings.Join(before, ",") + `]}}`,
		after:  `{"copper":{"vias":[` + strings.Join(after, ",") + `]}}`,
	}
	summary := map[string]any{}
	prov, gates := reconcileCandidateVias(f, "classed", "routed", viaFromTracemaker, summary)
	if len(gates) != 0 || f.edited != nil {
		t.Fatalf("%+v %v", gates, f.edited)
	}
	if st := summary["routerVias"].(map[string]any); st["backend"] != viaFromTracemaker || st["created"] != 3 || st["inputVias"] != 1 {
		t.Fatalf("%v", st)
	}
	r := &kicadRun{viaProv: prov, summary: map[string]any{}, stderr: io.Discard}
	var s boardSnapshot
	if err := json.Unmarshal([]byte(f.after), &s); err != nil {
		t.Fatal(err)
	}
	vias, err := snapshotVias(&s)
	if err != nil {
		t.Fatal(err)
	}
	pol := viaPolicy{Exceptions: []viaException{{Kind: "stitching", Nets: []string{"SIG"}, Reason: "shield row"}}}
	a := auditVias(vias, auditRB(), viaLimits{drill: 250000, annular: 100000}, r.viaProvenanceOf(), pol)
	byID := map[string]viaAuditRow{}
	for _, row := range a.Rows {
		byID[row.ID] = row
	}
	if byID["offsize"].Provenance != viaFromInput || byID["okay"].Provenance != viaFromTracemaker || byID["wrong"].Provenance != viaFromTracemaker || byID["stitch"].Provenance != viaFromTracemaker {
		t.Fatalf("%+v", byID)
	}
	if len(a.PreExisting) != 1 || !strings.Contains(a.PreExisting[0], short12("offsize")) {
		t.Errorf("off-size input via: %+v", a.PreExisting)
	}
	if len(a.NonConform) != 1 || !strings.Contains(a.NonConform[0], short12("wrong")) {
		t.Errorf("off-size TraceMaker via: %+v", a.NonConform)
	}
	if len(a.Excepted) != 1 || !strings.Contains(a.Excepted[0], short12("stitch")) {
		t.Errorf("declared exception: %+v", a.Excepted)
	}
	if len(a.Illegal)+len(a.CannotCheck) != 0 {
		t.Errorf("%+v", a)
	}
	// Reads that fail, and an input via gone, are gates.
	_, gates = reconcileCandidateVias(&fakeViaTools{failOn: "snapshot-before"}, "classed", "routed", viaFromTracemaker, map[string]any{})
	if len(gates) != 1 || gates[0].Pass || !gates[0].CannotCheck {
		t.Fatalf("%+v", gates)
	}
	f = &fakeViaTools{before: `{"copper":{"vias":[` + before[0] + `]}}`, after: `{"copper":{"vias":[]}}`}
	_, gates = reconcileCandidateVias(f, "classed", "routed", viaFromTracemaker, map[string]any{})
	if len(gates) != 1 || gates[0].Pass || gates[0].CannotCheck {
		t.Fatalf("%+v", gates)
	}
}

// The run's provenance is the one of the candidate it is judging.
func TestUseCandidateSwitchesViaProvenance(t *testing.T) {
	r := &kicadRun{summary: map[string]any{}, stderr: io.Discard}
	fr := &routeCandidate{Name: routerFastroute, Res: &routeResult{}, prov: &viaProvenance{}}
	fr.prov.record(nil, nil, []routeVia{{ID: "v"}})
	tm := &routeCandidate{Name: routerTracemaker, Res: &routeResult{}, prov: &viaProvenance{created: viaFromTracemaker}}
	tm.prov.record(map[string]bool{"v": true}, map[string]bool{"v": true}, []routeVia{{ID: "v"}, {ID: "w"}})
	r.useCandidate(fr, nil)
	if got := r.viaProvenanceOf()(routeVia{ID: "v"}); got != viaFromRouter {
		t.Fatal(got)
	}
	r.useCandidate(tm, nil)
	of := r.viaProvenanceOf()
	if of(routeVia{ID: "v"}) != viaFromInput || of(routeVia{ID: "w"}) != viaFromTracemaker {
		t.Fatalf("%s %s", of(routeVia{ID: "v"}), of(routeVia{ID: "w"}))
	}
}
