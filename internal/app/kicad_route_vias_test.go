package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
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
