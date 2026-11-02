package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		{"DRC names the via", fakeRepairTools(farTrack, &kicad.DRCReport{Violations: []kicad.Violation{{Rule: "clearance", Message: "too close", Objs: []string{"v1"}}}}, nil), "imported", false, "DRC: clearance"},
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
