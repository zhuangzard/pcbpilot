package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

const fracViaDSN = `(pcb x
  (structure
    (via "Via[0-5]_600:300_um" "Via[0-5]_609.6:304.8_um" "Via[0-5]_700.024:400.05_um")
  )
  (library
    (padstack "Via[0-5]_600:300_um"
      (shape (circle F.Cu 600))
      (attach off)
    )
    (padstack "Via[0-5]_609.6:304.8_um"
      (shape (circle F.Cu 609.6))
      (shape (circle In1.Cu 609.6))
      (attach off)
    )
    (padstack "Via[0-5]_610:305_um"
      (shape (circle F.Cu 610))
      (attach off)
    )
    (padstack "Via[0-5]_700.024:400.05_um"
      (shape (circle F.Cu 700.024))
      (attach off)
    )
  )
  (network
    (class a N1 (circuit (use_via "Via[0-5]_609.6:304.8_um")))
    (class b N2 (circuit (use_via "Via[0-5]_700.024:400.05_um")))
  )
  (wiring
    (via "Via[0-5]_700.024:400.05_um" 1 2 (net N2) (type fix))
  )
)
`

// A fractional-µm via name is what fastroute does not honour: every via
// padstack goes to whole µm, up, with its references.
func TestDSNWholeUmVias(t *testing.T) {
	out := dsnWholeUmVias(fracViaDSN)
	if strings.Contains(out, "609.6") || strings.Contains(out, "700.024") || strings.Contains(out, "400.05") {
		t.Fatalf("fractional via left:\n%s", out)
	}
	if strings.Count(out, `(padstack "Via[0-5]_610:305_um"`) != 1 {
		t.Fatalf("609.6 must merge into the existing 610:305 padstack:\n%s", out)
	}
	if !strings.Contains(out, `(padstack "Via[0-5]_701:401_um"`) || !strings.Contains(out, "(circle F.Cu 701)") {
		t.Fatalf("701:401 padstack:\n%s", out)
	}
	if !strings.Contains(out, `(use_via "Via[0-5]_610:305_um")`) || !strings.Contains(out, `(via "Via[0-5]_701:401_um" 1 2`) {
		t.Fatalf("references:\n%s", out)
	}
	routed, order, top := dsnRouterVia(fracViaDSN)
	if top == nil || top.dia != 701 || top.drill != 401 || len(order) != 1 || !strings.Contains(routed, `(via "Via[0-5]_701:401_um")`) {
		t.Fatalf("router via %+v %v", top, order)
	}
	// Whole-µm names stay byte-identical.
	whole := `(pcb (structure (via "Via[0-1]_600:300_um")) (library (padstack "Via[0-1]_600:300_um" (shape (circle F.Cu 600)))))`
	if got := dsnWholeUmVias(whole); got != whole {
		t.Fatalf("changed: %s", got)
	}
}

// A router via rounded up to whole µm is not "already the class via".
func TestClassViaPlanShrinksWholeUmVia(t *testing.T) {
	vias := []routeVia{{ID: "v", Net: "A", Dia: 610000 / nmPerMil, Drill: 305000 / nmPerMil}}
	ops, fail, unknown, n := classViaPlan(vias, nil, func(string) (int64, int64, bool) { return 609600, 304800, true })
	if n != 1 || len(ops) != 1 || len(fail)+len(unknown) != 0 {
		t.Fatalf("%v %v %v %d", ops, fail, unknown, n)
	}
}

// fakeRepairTools: the router via is 0.6/0.4 on a 0.7/0.4 class.
func fakeRepairTools(extra string, rep *kicad.DRCReport, derr error) *fakeViaTools {
	return &fakeViaTools{
		after: `{"copper":{"lines":[` + extra + `],"vias":[{"primitiveId":"v1","net":"A","x":100,"y":100,"diameter":23.622,"holeDiameter":15.748}]}}`,
		drc:   rep, drcErr: derr,
		rb: &kicad.RulesReadback{Nets: map[string]kicad.EffectiveNM{
			"A": {ViaDiameter: 700000, ViaDrill: 400000, Clearance: 200000},
			"B": {ViaDiameter: 700000, ViaDrill: 400000, Clearance: 200000},
		}},
	}
}

func TestRepairRouterVias(t *testing.T) {
	next := func(s string) string { return s + ".kicad_pcb" }
	farTrack := `{"primitiveId":"t","net":"B","layer":1,"startX":200,"startY":200,"endX":300,"endY":200,"lineWidth":8}`
	nearTrack := `{"primitiveId":"t","net":"B","layer":1,"startX":100,"startY":118,"endX":300,"endY":118,"lineWidth":8}`
	cases := []struct {
		name       string
		f          *fakeViaTools
		wantBoard  string
		wantPass   bool
		wantDetail string
	}{
		{"grown", fakeRepairTools(farTrack, &kicad.DRCReport{}, nil), "vias-repair.kicad_pcb", true, "grown"},
		{"geometry refuses", fakeRepairTools(nearTrack, &kicad.DRCReport{}, nil), "imported", false, "mil to a track of B"},
		{"DRC names the via", func() *fakeViaTools {
			f := fakeRepairTools(farTrack, &kicad.DRCReport{Violations: []kicad.Violation{{Rule: "clearance", Message: "too close", Objs: []string{"v1"}}}}, nil)
			f.base = &kicad.DRCReport{}
			return f
		}(), "imported", false, "DRC: clearance"},
		{"DRC cannot run", fakeRepairTools(farTrack, nil, errors.New("kicad-cli died")), "imported", false, "cannot check: DRC"},
	}
	for _, c := range cases {
		s := map[string]any{}
		board, gates := sizeRouterVias(c.f, "classed", "imported", next, s, io.Discard)
		if board != c.wantBoard || len(gates) != 1 || gates[0].Pass != c.wantPass {
			t.Fatalf("%s: board %s gates %+v", c.name, board, gates)
		}
		if !strings.Contains(gates[0].Detail+strings.Join(gates[0].Items, "|")+strings.Join(gates[0].Info, "|"), c.wantDetail) {
			t.Fatalf("%s: %+v", c.name, gates[0])
		}
	}
	// Missing rule coverage: no clearance known, no write.
	f := fakeRepairTools(farTrack, &kicad.DRCReport{}, nil)
	f.rb.Nets["A"] = kicad.EffectiveNM{ViaDiameter: 700000, ViaDrill: 400000}
	board, gates := sizeRouterVias(f, "classed", "imported", next, map[string]any{}, io.Discard)
	if board != "imported" || f.edited != nil || len(gates) != 1 || gates[0].Pass || !strings.Contains(gates[0].Items[0], "cannot check: no clearance rule") {
		t.Fatalf("%s %v %+v", board, f.edited, gates)
	}
}

// A repaired DRC is compared with the one before the repair: a new violation
// that lists only the neighbour is as much the repair's doing as one naming the
// via; what the board already reported is not.
func TestRepairRouterViasDRCBaseline(t *testing.T) {
	next := func(s string) string { return s + ".kicad_pcb" }
	farTrack := `{"primitiveId":"t","net":"B","layer":1,"startX":200,"startY":200,"endX":300,"endY":200,"lineWidth":8}`
	neighbour := kicad.Violation{Rule: "clearance", Message: "Clearance violation (netclass 'A' clearance 0.2000 mm; actual 0.1000 mm)", Objs: []string{"t", "p9"}}
	rep := func(vs ...kicad.Violation) *kicad.DRCReport { return &kicad.DRCReport{Violations: vs} }
	worse := neighbour
	worse.Message = "Clearance violation (netclass 'A' clearance 0.2000 mm; actual 0.0500 mm)"
	better := neighbour
	better.Message = "Clearance violation (netclass 'A' clearance 0.2000 mm; actual 0.1500 mm)"
	reworded := neighbour
	reworded.Message = "Clearance violation (netclass 'B' clearance 0.2000 mm; actual 0.1000 mm)"
	relimit := neighbour
	relimit.Message = "Clearance violation (netclass 'A' clearance 0.1000 mm; actual 0.0900 mm)"
	viaOwn := kicad.Violation{Rule: "clearance", Message: "Clearance violation (netclass 'A' clearance 0.2000 mm; actual 0.1000 mm)", Objs: []string{"v1", "t"}}
	viaOwnWorse := viaOwn
	viaOwnWorse.Message = "Clearance violation (netclass 'A' clearance 0.2000 mm; actual 0.0500 mm)"
	viaOwnBetter := viaOwn
	viaOwnBetter.Message = "Clearance violation (netclass 'A' clearance 0.2000 mm; actual 0.1500 mm)"
	ring := kicad.Violation{Rule: "annular_width", Message: "Annular width (board setting min 0.1300 mm; actual 0.1000 mm)", Objs: []string{"v1"}}
	reversed := viaOwn
	reversed.Objs = []string{"t", "v1"}
	noObjs := kicad.Violation{Rule: "items_not_allowed", Message: "somewhere"}
	unconn := kicad.Violation{Rule: "unconnected_items", Section: "unconnected_items", Message: "open"}
	for _, c := range []struct {
		name       string
		base, post *kicad.DRCReport
		wantBoard  string
		wantPass   bool
		want       string
	}{
		{"new, neighbour only", rep(), rep(neighbour), "imported", false, "naming no grown via"},
		{"already on the board", rep(neighbour), rep(neighbour), "vias-repair.kicad_pcb", true, "grown"},
		{"one more of the same", rep(neighbour), rep(neighbour, neighbour), "imported", false, "naming no grown via"},
		{"worse than before", rep(neighbour), rep(worse), "imported", false, "worse than: Clearance"},
		{"better than before", rep(neighbour), rep(better), "vias-repair.kicad_pcb", true, "grown"},
		{"text changed, same measurement", rep(neighbour), rep(reworded), "vias-repair.kicad_pcb", true, "grown"},
		{"limit changed fails closed", rep(neighbour), rep(relimit), "imported", false, "worse than: Clearance"},
		{"baseline names the candidate, unchanged", rep(viaOwn), rep(viaOwn), "vias-repair.kicad_pcb", true, "grown"},
		{"same objects in another order", rep(viaOwn), rep(reversed), "vias-repair.kicad_pcb", true, "grown"},
		{"baseline names the candidate, better", rep(viaOwn), rep(viaOwnBetter), "vias-repair.kicad_pcb", true, "grown"},
		{"baseline names the candidate, worse", rep(viaOwn), rep(viaOwnWorse), "imported", false, "DRC: clearance"},
		{"new, names the candidate", rep(), rep(viaOwn), "imported", false, "DRC: clearance"},
		{"the repair clears one", rep(ring), rep(), "vias-repair.kicad_pcb", true, "grown"},
		{"no objects, new", rep(), rep(noObjs), "imported", false, "naming no grown via"},
		{"no objects, unchanged", rep(noObjs), rep(noObjs), "vias-repair.kicad_pcb", true, "grown"},
		{"unconnected is not the repair's", rep(), rep(unconn), "vias-repair.kicad_pcb", true, "grown"},
	} {
		f := fakeRepairTools(farTrack, c.post, nil)
		f.base = c.base
		board, gates := sizeRouterVias(f, "classed", "imported", next, map[string]any{}, io.Discard)
		if board != c.wantBoard || len(gates) != 1 || gates[0].Pass != c.wantPass || gates[0].CannotCheck {
			t.Fatalf("%s: board %s gates %+v", c.name, board, gates)
		}
		if !strings.Contains(strings.Join(append(append([]string{}, gates[0].Items...), gates[0].Info...), "|"), c.want) {
			t.Fatalf("%s: %+v", c.name, gates[0])
		}
	}
	// A DRC that does not run, before or after the write, is cannot-check.
	f := fakeRepairTools(farTrack, nil, nil)
	f.baseErr = errors.New("kicad-cli died")
	board, gates := sizeRouterVias(f, "classed", "imported", next, map[string]any{}, io.Discard)
	if board != "imported" || f.edited != nil || len(gates) != 1 || gates[0].Pass || !gates[0].CannotCheck {
		t.Fatalf("baseline: %s %v %+v", board, f.edited, gates)
	}
	f = fakeRepairTools(farTrack, nil, errors.New("kicad-cli died"))
	f.base = rep()
	board, gates = sizeRouterVias(f, "classed", "imported", next, map[string]any{}, io.Discard)
	if board != "imported" || len(gates) != 1 || gates[0].Pass || !gates[0].CannotCheck {
		t.Fatalf("repaired: %s %+v", board, gates)
	}
}

// DRC names one via and lists a neighbour: that via leaves, and the retry
// judges the rest afresh. The vias grown stay on the gate's record.
func TestRepairRouterViasRetryWithoutNamedVia(t *testing.T) {
	two := `{"copper":{"lines":[],"vias":[{"primitiveId":"v1","net":"A","x":100,"y":100,"diameter":23.622,"holeDiameter":15.748},{"primitiveId":"v2","net":"A","x":900,"y":900,"diameter":23.622,"holeDiameter":15.748}]}}`
	f := fakeRepairTools("", nil, nil)
	f.after, f.base = two, &kicad.DRCReport{}
	f.drcSeq = []*kicad.DRCReport{{Violations: []kicad.Violation{
		{Rule: "clearance", Message: "too close", Objs: []string{"v1", "t"}},
		{Rule: "clearance", Message: "too close", Objs: []string{"t"}},
	}}, {}}
	board, gates := sizeRouterVias(f, "classed", "imported", func(s string) string { return s + ".kicad_pcb" }, map[string]any{}, io.Discard)
	if board != "vias-repair.kicad_pcb" || len(gates) != 1 || gates[0].Pass || gates[0].CannotCheck || len(gates[0].Items) != 1 || !strings.Contains(gates[0].Items[0], "DRC: clearance") || len(gates[0].Info) != 1 {
		t.Fatalf("%s %+v", board, gates)
	}
}

func TestViaRepairDRCWorsened(t *testing.T) {
	m := func(limit, actual string) string { return "x (clearance " + limit + " mm; actual " + actual + " mm)" }
	for _, c := range []struct {
		was, now string
		want     bool
	}{
		{m("0.2000", "0.1000"), m("0.2000", "0.0999"), true},
		{m("0.2000", "0.1000"), m("0.2000", "0.1000"), false},
		{m("0.2000", "0.1000"), m("0.2000", "0.1500"), false},
		{m("0.2000", "0.1000"), m("0.20", "0.10"), false},                        // formatted differently
		{"x (max 5.0 mm; actual 6.0 mm)", "x (max 5.0 mm; actual 7.0 mm)", true}, // a maximum
		{"x (max 5.0 mm; actual 6.0 mm)", "x (max 5.0 mm; actual 5.5 mm)", false},
		{m("0.2000", "0.1000"), "x (clearance 7.8 mil; actual 3.9 mil)", true}, // unit changed
		{m("0.2000", "0.1000"), "no measurement", true},
		{"no measurement", m("0.2000", "0.1000"), true},
		{"words", "other words", false},
	} {
		if got := drcWorsened(c.was, c.now); got != c.want {
			t.Errorf("%q -> %q: %v", c.was, c.now, got)
		}
	}
}

func auditRB() *kicad.RulesReadback {
	return &kicad.RulesReadback{Nets: map[string]kicad.EffectiveNM{
		"GND": {Class: "C7", ViaDiameter: 700000, ViaDrill: 400000},
		"SIG": {Class: "Default", ViaDiameter: 600000, ViaDrill: 300000},
	}}
}

func auditVia(id, net string, diaUm, drillUm float64) routeVia {
	return routeVia{ID: id, Net: net, Dia: diaUm * 1000 / nmPerMil, Drill: drillUm * 1000 / nmPerMil, From: 1, To: 2}
}

func TestAuditViasLegalityAndPreference(t *testing.T) {
	lim := viaLimits{drill: 250000, annular: 130000, diameter: 500000}
	from := map[string]string{"ok": viaFromRouter, "ring": viaFromRouter, "esc": viaFromEscape, "arr": viaFromArray, "old": viaFromInput, "who": viaFromUnknown, "oldring": viaFromInput}
	prov := func(v routeVia) string { return from[v.ID] }
	a := auditVias([]routeVia{
		auditVia("ok", "GND", 700, 400),
		auditVia("ring", "GND", 600, 400),    // the 0.6/0.4 of the bug: illegal, and off size
		auditVia("esc", "GND", 600, 300),     // escape: excepted, legal
		auditVia("arr", "SIG", 700, 400),     // via array off size, not declared
		auditVia("old", "GND", 600, 300),     // pre-existing: reported, does not fail
		auditVia("oldring", "GND", 500, 300), // pre-existing illegal: fails, not edited
		auditVia("who", "SIG", 700, 400),     // unknown provenance
	}, auditRB(), lim, prov, viaPolicy{})
	has := func(l []string, id string) bool { return strings.Contains(strings.Join(l, "\n"), short12(id)+" ") }
	if !has(a.Illegal, "ring") || !has(a.Illegal, "oldring") || has(a.Illegal, "ok") || has(a.Illegal, "esc") {
		t.Fatalf("illegal %v", a.Illegal)
	}
	if !has(a.NonConform, "arr") || !has(a.NonConform, "who") || !has(a.NonConform, "ring") || has(a.NonConform, "esc") || has(a.NonConform, "old") {
		t.Fatalf("non-conforming %v", a.NonConform)
	}
	if !has(a.Excepted, "esc") || !has(a.PreExisting, "old") || len(a.Rows) != 7 {
		t.Fatalf("%+v", a)
	}
	var g gateResult
	viaAuditVerdict(&g, a)
	if g.Pass || !strings.Contains(g.Detail, "break a manufacturing minimum") {
		t.Fatalf("%+v", g)
	}
	// Declared stitching lifts the array via, never the unknown one or legality.
	pol := viaPolicy{Exceptions: []viaException{{Kind: "stitching", Nets: []string{"S*", "GND"}, Reason: "shield row"}}}
	a = auditVias([]routeVia{auditVia("arr", "SIG", 700, 400), auditVia("who", "SIG", 700, 400), auditVia("ring", "GND", 600, 400)}, auditRB(), lim, prov, pol)
	if has(a.NonConform, "arr") || !has(a.NonConform, "who") || !has(a.Excepted, "arr") || !has(a.Illegal, "ring") {
		t.Fatalf("%+v", a)
	}
}

func TestAuditViasNonThroughAndUnknown(t *testing.T) {
	blind := auditVia("b1", "SIG", 300, 100)
	blind.To = 15
	from := func(routeVia) string { return viaFromRouter }
	a := auditVias([]routeVia{blind}, auditRB(), viaLimits{drill: 250000, annular: 130000}, from, viaPolicy{})
	if len(a.CannotCheck) != 1 || a.Rows[0].Legal != "cannot-check" || a.Rows[0].Span != "non-through" {
		t.Fatalf("%+v", a)
	}
	pol := viaPolicy{Exceptions: []viaException{{Kind: "microvia", Reason: "HDI", MinDrillMm: 0.1, MinAnnularMm: 0.075}}}
	if a = auditVias([]routeVia{blind}, auditRB(), viaLimits{drill: 250000, annular: 130000}, from, pol); len(a.Illegal)+len(a.CannotCheck) != 0 || a.Rows[0].Exception == "" {
		t.Fatalf("%+v", a)
	}
	if a = auditVias([]routeVia{auditVia("n", "NOCLASS", 600, 300)}, auditRB(), viaLimits{}, from, viaPolicy{}); len(a.CannotCheck) != 1 {
		t.Fatalf("%+v", a)
	}
}

// Unknown provenance gets no exemption, and only a confirmed through via is
// judged by the board's limits: any other span is cannot-check unless declared.
func TestAuditViasSpanAndProvenance(t *testing.T) {
	pol := viaPolicy{Exceptions: []viaException{{Kind: "blind-buried", Reason: "HDI", MinDrillMm: 0.1, MinAnnularMm: 0.075}}}
	lim := viaLimits{drill: 250000, annular: 130000}
	mk := func(from, to int) routeVia {
		v := auditVia("v", "SIG", 600, 300)
		v.From, v.To = from, to
		return v
	}
	prov := func(p string) func(routeVia) string { return func(routeVia) string { return p } }
	for _, c := range []struct {
		name     string
		v        routeVia
		prov     string
		pol      viaPolicy
		span     string
		legal    string
		excepted bool
	}{
		{"both ids missing", mk(0, 0), viaFromRouter, viaPolicy{}, "unknown", "cannot-check", false},
		{"from zero", mk(0, 2), viaFromRouter, viaPolicy{}, "unknown", "cannot-check", false},
		{"to zero", mk(1, 0), viaFromRouter, viaPolicy{}, "unknown", "cannot-check", false},
		{"input board, missing ids", mk(0, 0), viaFromInput, viaPolicy{}, "unknown", "cannot-check", false},
		{"declared span unknown", mk(0, 0), viaFromRouter, pol, "unknown", "yes", true},
		{"declared non-through", mk(1, 15), viaFromRouter, pol, "non-through", "yes", true},
		{"undeclared non-through", mk(1, 15), viaFromRouter, viaPolicy{}, "non-through", "cannot-check", false},
		{"unknown provenance, non-through, declared", mk(1, 15), viaFromUnknown, pol, "non-through", "cannot-check", false},
		{"unknown provenance, span unknown, declared", mk(0, 0), viaFromUnknown, pol, "unknown", "cannot-check", false},
		{"through", mk(1, 2), viaFromRouter, viaPolicy{}, "through", "yes", false},
		{"through, reversed", mk(2, 1), viaFromRouter, viaPolicy{}, "through", "yes", false},
		{"through, unknown provenance", mk(1, 2), viaFromUnknown, viaPolicy{}, "through", "yes", false},
	} {
		a := auditVias([]routeVia{c.v}, auditRB(), lim, prov(c.prov), c.pol)
		row := a.Rows[0]
		if row.Span != c.span || row.Legal != c.legal || (row.Exception != "") != c.excepted || (len(a.CannotCheck) > 0) != (c.legal == "cannot-check") {
			t.Errorf("%s: %+v cannot-check %v", c.name, row, a.CannotCheck)
		}
	}
	// A missing id never decodes as through, from the snapshot's own JSON.
	var snap boardSnapshot
	if err := json.Unmarshal([]byte(`{"copper":{"vias":[{"primitiveId":"v","net":"SIG","diameter":23.622,"holeDiameter":11.811}]}}`), &snap); err != nil {
		t.Fatal(err)
	}
	vias, err := snapshotVias(&snap)
	if err != nil {
		t.Fatal(err)
	}
	if a := auditVias(vias, auditRB(), lim, prov(viaFromRouter), viaPolicy{}); a.Rows[0].Span != "unknown" || len(a.CannotCheck) != 1 {
		t.Fatalf("%+v", a)
	}
}

// via-audit.json states millimetres: a 600 um via is 0.6, not 600.
func TestViaAuditRowUnits(t *testing.T) {
	a := auditVias([]routeVia{auditVia("v", "GND", 600, 300), auditVia("w", "GND", 700, 400)}, auditRB(), viaLimits{}, func(routeVia) string { return viaFromRouter }, viaPolicy{})
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Vias []map[string]any `json:"vias"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]float64{"diameterMm": 0.6, "drillMm": 0.3, "annularMm": 0.15} {
		if got, _ := out.Vias[0][k].(float64); got != want {
			t.Errorf("%s = %v, want %v", k, out.Vias[0][k], want)
		}
	}
	if got, _ := out.Vias[1]["diameterMm"].(float64); got != 0.7 {
		t.Errorf("diameterMm = %v", out.Vias[1]["diameterMm"])
	}
	if !strings.Contains(strings.Join(a.NonConform, "|"), "0.6000/0.3000 mm differs from class C7 via 0.7000/0.4000 mm") {
		t.Errorf("%v", a.NonConform)
	}
}

// The audit's preferred size uses the same 100 nm as the sizing step.
func TestAuditViasPreferredToleranceBoundary(t *testing.T) {
	via := func(dDia, dDrill float64) routeVia {
		return routeVia{ID: "v", Net: "GND", Dia: (700000 + dDia) / nmPerMil, Drill: (400000 + dDrill) / nmPerMil, From: 1, To: 2}
	}
	for _, c := range []struct {
		d, h float64
		want string
	}{
		{0, 0, "conforms"}, {-100, 0, "conforms"}, {100, 100, "conforms"}, {0, -100, "conforms"},
		{-101, 0, "differs"}, {101, 0, "differs"}, {0, -101, "differs"}, {-300, 0, "differs"}, {-499, -499, "differs"},
	} {
		a := auditVias([]routeVia{via(c.d, c.h)}, auditRB(), viaLimits{}, func(routeVia) string { return viaFromRouter }, viaPolicy{})
		if a.Rows[0].Preferred != c.want || (len(a.NonConform) > 0) != (c.want == "differs") {
			t.Errorf("%v/%v: %+v", c.d, c.h, a.Rows[0])
		}
	}
}

// A drill that leaves no copper is a known failure on any span, with the span's
// cannot-check evidence kept beside it.
func TestAuditViasDrillNotSmallerOnUnknownSpan(t *testing.T) {
	from := func(p string) func(routeVia) string { return func(routeVia) string { return p } }
	for name, c := range map[string]struct {
		dia, drill float64
		from, to   int
		prov       string
	}{
		"span unknown":              {300, 300, 0, 0, viaFromRouter},
		"span unknown, larger":      {300, 400, 0, 0, viaFromRouter},
		"undeclared non-through":    {300, 300, 1, 15, viaFromRouter},
		"unknown provenance":        {300, 300, 0, 0, viaFromUnknown},
		"input board, span unknown": {300, 300, 0, 2, viaFromInput},
	} {
		v := auditVia("v", "SIG", c.dia, c.drill)
		v.From, v.To = c.from, c.to
		a := auditVias([]routeVia{v}, auditRB(), viaLimits{}, from(c.prov), viaPolicy{})
		if a.Rows[0].Legal != "no" || len(a.Illegal) != 1 || len(a.CannotCheck) != 1 {
			t.Errorf("%s: %+v", name, a)
		}
		g := gateResult{Gate: "via-audit"}
		viaAuditVerdict(&g, a)
		var stderr bytes.Buffer
		r := &kicadRun{stderr: &stderr, summary: map[string]any{}, reg: newGateRegistry(&stderr, nil)}
		r.add(g)
		if r.reg.ExitCode() != 6 || !strings.Contains(strings.Join(g.Items, "|"), "not smaller") || len(g.Unchecked) != 1 {
			t.Errorf("%s: exit %d %+v", name, r.reg.ExitCode(), g)
		}
	}
	// The minimums still wait for limits that apply: no made-up failure.
	v := auditVia("v", "SIG", 300, 100)
	v.From, v.To = 0, 0
	a := auditVias([]routeVia{v}, auditRB(), viaLimits{drill: 250000}, from(viaFromRouter), viaPolicy{})
	if a.Rows[0].Legal != "cannot-check" || len(a.Illegal) != 0 {
		t.Fatalf("%+v", a)
	}
}

// Legality uses the sizing step's 100 nm: a via it calls the class via is not
// below a manufacturing minimum that equals that class via.
func TestAuditViasLegalityToleranceBoundary(t *testing.T) {
	via := func(dia, drill int64) routeVia {
		return routeVia{ID: "v", Net: "GND", Dia: float64(dia) / nmPerMil, Drill: float64(drill) / nmPerMil, From: 1, To: 2}
	}
	prov := func(routeVia) string { return viaFromRouter }
	for _, c := range []struct {
		name       string
		dia, drill int64
		lim        viaLimits
		legal      string
	}{
		{"diameter at", 700000, 400000, viaLimits{diameter: 700000}, "yes"},
		{"diameter 100 under", 699900, 400000, viaLimits{diameter: 700000}, "yes"},
		{"diameter 101 under", 699899, 400000, viaLimits{diameter: 700000}, "no"},
		{"drill 50 under", 700000, 399950, viaLimits{drill: 400000}, "yes"},
		{"drill 100 under", 700000, 399900, viaLimits{drill: 400000}, "yes"},
		{"drill 101 under", 700000, 399899, viaLimits{drill: 400000}, "no"},
		{"annular 100 under", 700000, 400000, viaLimits{annular: 150100}, "yes"},
		{"annular 101 under", 700000, 400000, viaLimits{annular: 150101}, "no"},
	} {
		a := auditVias([]routeVia{via(c.dia, c.drill)}, auditRB(), c.lim, prov, viaPolicy{})
		if a.Rows[0].Legal != c.legal || (len(a.Illegal) > 0) != (c.legal == "no") {
			t.Errorf("%s: %+v %v", c.name, a.Rows[0], a.Illegal)
		}
		// The sizing step agrees on the same via, for a class equal to the minimum.
		short := []routeVia{via(c.dia, c.drill)}
		if c.lim.annular == 0 {
			cd, ch := max(c.lim.diameter, c.dia), max(c.lim.drill, c.drill)
			if got := len(viaGrowCandidates(short, nil, func(string) (int64, int64, bool) { return cd, ch, true })) == 0; got != (c.legal == "yes") {
				t.Errorf("%s: sizing says reachable=%v, audit %s", c.name, got, c.legal)
			}
		}
	}
}

// A partial waiver recomputes the status from what is left, through r.add.
func TestViaAuditPartialWaiver(t *testing.T) {
	spanless := auditVia("span", "SIG", 600, 300)
	spanless.From, spanless.To = 0, 0
	ring := auditVia("ring", "GND", 600, 400)
	a := auditVias([]routeVia{spanless, ring}, auditRB(), viaLimits{annular: 130000}, func(routeVia) string { return viaFromRouter }, viaPolicy{})
	if len(a.Illegal) != 1 || len(a.CannotCheck) != 1 {
		t.Fatalf("%+v", a)
	}
	base := gateResult{Gate: "via-audit"}
	viaAuditVerdict(&base, a)
	run := func(ws ...gateWaiver) (*kicadRun, gateResult) {
		var stderr bytes.Buffer
		r := &kicadRun{stderr: &stderr, summary: map[string]any{}, waivers: ws, reg: newGateRegistry(&stderr, ws)}
		g := base
		r.add(g)
		return r, r.gates[0]
	}
	w := func(match string) gateWaiver {
		return gateWaiver{Gate: "via-audit", Match: match, Reason: "reviewed", By: "test"}
	}
	for _, c := range []struct {
		name   string
		ws     []gateWaiver
		status gate.Status
		exit   int
		left   int
	}{
		{"no waiver", nil, gate.Fail, 6, 3},
		{"every failure waived, unknown span left", []gateWaiver{w("break"), w("ring"), w("short"), w(short12("ring"))}, gate.CannotCheck, 5, 1},
		{"unknown span waived, failures left", []gateWaiver{w("via span unknown")}, gate.Fail, 6, 2},
		{"one of two failures waived", []gateWaiver{w("annular ring")}, gate.Fail, 6, 2},
		{"all waived", []gateWaiver{w(" via ")}, gate.Pass, 0, 0},
	} {
		r, g := run(c.ws...)
		if got := statusOf(t, r.reg, "via-audit"); got != c.status || r.reg.ExitCode() != c.exit {
			t.Errorf("%s: status %s exit %d %+v", c.name, got, r.reg.ExitCode(), g)
		}
		if c.status != gate.Pass && len(g.Items) != c.left {
			t.Errorf("%s: left %v", c.name, g.Items)
		}
		if g.CannotCheck != (c.status == gate.CannotCheck) {
			t.Errorf("%s: CannotCheck %v", c.name, g.CannotCheck)
		}
		if c.ws != nil && len(g.Waived) == 0 {
			t.Errorf("%s: nothing recorded as waived: %+v", c.name, g)
		}
	}
}

// The same through router-vias: a known failure and a via without a class via.
func TestRouterViasPartialWaiver(t *testing.T) {
	gate1 := problemGates(func(pass bool, detail string, items ...string) []gateResult {
		return []gateResult{{Gate: "router-vias", Pass: pass, Detail: detail, Items: items}}
	}, []string{"A via 1: cannot be shrunk"}, []string{"NOCLASS via 2: no class via"})
	if len(gate1) != 1 || gate1[0].CannotCheck {
		t.Fatalf("%+v", gate1)
	}
	var stderr bytes.Buffer
	ws := []gateWaiver{{Gate: "router-vias", Match: "A via 1", Reason: "reviewed", By: "test"}}
	r := &kicadRun{stderr: &stderr, summary: map[string]any{}, waivers: ws, reg: newGateRegistry(&stderr, ws)}
	r.add(gate1[0])
	if r.reg.ExitCode() != 5 || !r.gates[0].CannotCheck || len(r.gates[0].Items) != 1 {
		t.Fatalf("exit %d %+v", r.reg.ExitCode(), r.gates[0])
	}
}

func TestViaLimitsFromDRU(t *testing.T) {
	v := int64(150000)
	lim := viaLimits{annular: 100000}
	un := viaLimitsFromDRU([]kicad.DRURule{
		{Name: "all", Constraints: []kicad.DRUConstraint{{Type: "annular_width", Min: &v}, {Type: "clearance", Min: &v}}},
		{Name: "some", Condition: "A.NetClass == 'x'", Constraints: []kicad.DRUConstraint{{Type: "hole_size", Min: &v}}},
		{Name: "expr", Constraints: []kicad.DRUConstraint{{Type: "via_diameter", Unparsed: true}}},
	}, &lim)
	if lim.annular != v || lim.drill != 0 || len(un) != 2 {
		t.Fatalf("%+v %v", lim, un)
	}
}

func TestViaPolicyValidate(t *testing.T) {
	for _, e := range []viaException{
		{Kind: "magic", Reason: "x", Nets: []string{"A"}},
		{Kind: "stitching", Reason: "x"},
		{Kind: "stitching", Nets: []string{"A"}},
		{Kind: "microvia", Reason: "x"},
		{Kind: "neckdown-escape", Reason: "x", Nets: []string{"["}},
	} {
		if e.validate() == nil {
			t.Fatalf("%+v passed", e)
		}
	}
}

// The real path with fractional class vias: KiCad exports fractional names,
// dsnRouterVia makes them whole µm, fastroute keeps each class via, KiCad
// imports them and the shrink lands exactly on the class via; the audit sees
// every via legal and at size. Needs PCBPILOT_KICAD_LIVE=1 and
// PCBPILOT_LIVE_FASTROUTE.
func TestRouterViaFractionalClassKiCadLive(t *testing.T) {
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" || os.Getenv("PCBPILOT_LIVE_FASTROUTE") == "" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 and PCBPILOT_LIVE_FASTROUTE (a fastroute executable)")
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
	pcb := filepath.Join(dir, "vias.kicad_pcb")
	if err := os.WriteFile(pcb, src, 0o644); err != nil {
		t.Fatal(err)
	}
	pro := `{"board":{"design_settings":{"rules":{"min_via_annular_width":0.13,"min_through_hole_diameter":0.2,"min_via_diameter":0.4,"min_clearance":0.1}}},
 "net_settings":{"classes":[
  {"name":"Default","clearance":0.2,"track_width":0.25,"via_diameter":0.6,"via_drill":0.3,"priority":2147483647},
  {"name":"C_A","clearance":0.2,"track_width":0.25,"via_diameter":0.6096,"via_drill":0.3048,"priority":0},
  {"name":"C_GND","clearance":0.2,"track_width":0.25,"via_diameter":0.700024,"via_drill":0.40005,"priority":1}],
  "netclass_patterns":[{"netclass":"C_A","pattern":"A"},{"netclass":"C_GND","pattern":"GND"}]},
 "meta":{"filename":"vias.kicad_pro","version":3}}`
	if err := os.WriteFile(boardSibling(pcb, ".kicad_pro"), []byte(pro), 0o644); err != nil {
		t.Fatal(err)
	}
	dsn := filepath.Join(dir, "route.dsn")
	if _, err := kt.ExportDSN(pcb, dsn); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(dsn)
	if !strings.Contains(string(text), "609.6:304.8") {
		t.Fatalf("the test needs a fractional via name in KiCad's export")
	}
	routed, _, top := dsnRouterVia(string(text))
	if top == nil || top.dia != 701 || top.drill != 401 {
		t.Fatalf("router via %+v", top)
	}
	if err := os.WriteFile(dsn, []byte(routed), 0o644); err != nil {
		t.Fatal(err)
	}
	ses, runs, err := runFastroute(fastrouteOpts{bin: os.Getenv("PCBPILOT_LIVE_FASTROUTE"), maxTime: 30 * time.Second, timeout: 3 * time.Minute}, dsn, filepath.Join(dir, "route"), os.Stderr)
	if err != nil {
		t.Fatalf("%v %+v", err, runs)
	}
	body, _ := os.ReadFile(ses)
	if !strings.Contains(string(body), "610:305") {
		t.Fatalf("no class via in the session:\n%s", fmt.Sprint(len(body)))
	}
	imported := filepath.Join(dir, "imported.kicad_pcb")
	if _, err := kt.ImportSES(pcb, ses, imported); err != nil {
		t.Fatal(err)
	}
	r := &kicadRun{kt: kt, work: dir, summary: map[string]any{}, stderr: os.Stderr, o: kicadRouteOpts{outDir: dir}}
	out, gates := r.routerViasToClass(pcb, imported)
	if len(gates) != 0 {
		t.Fatalf("router vias: %+v", gates)
	}
	final, raw, err := r.snapshot(out)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw
	g := r.viaAuditGate(out, final)
	if !g.Pass {
		t.Fatalf("via audit: %+v", g)
	}
	b, err := os.ReadFile(filepath.Join(dir, "via-audit.json"))
	if err != nil {
		t.Fatal(err)
	}
	var a viaAudit
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	nets := map[string]int{}
	for _, row := range a.Rows {
		nets[row.Net]++
		if row.Provenance != viaFromRouter || row.Span != "through" || row.Legal != "yes" || row.Preferred != "conforms" {
			t.Fatalf("%+v", row)
		}
	}
	if nets["A"] == 0 {
		t.Fatalf("net A needs vias on the test board: %v", nets)
	}
	rep, err := kt.DRC(out, filepath.Join(dir, "drc.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range rep.Violations {
		switch v.Rule {
		case "annular_width", "drill_out_of_range", "hole_size", "via_diameter":
			t.Fatalf("DRC %s: %s", v.Rule, v.Message)
		}
	}
}
