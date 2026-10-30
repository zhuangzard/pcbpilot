package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

type appFakeCatalog struct {
	parts map[string]*kicad.CatalogPart
	err   error
}

func (f appFakeCatalog) Lookup(codes []string) (map[string]kicad.CatalogLookup, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]kicad.CatalogLookup{}
	for _, c := range codes {
		out[c] = kicad.CatalogLookup{Part: f.parts[c]}
	}
	return out, nil
}

func (appFakeCatalog) Search(string, int) ([]kicad.CatalogPart, error) { return nil, nil }

func withCatalog(t *testing.T, c kicad.Catalog) {
	t.Helper()
	old := newLcscCatalog
	newLcscCatalog = func(string) (kicad.Catalog, error) { return c, nil }
	t.Cleanup(func() { newLcscCatalog = old })
}

func tf(b bool) *bool { return &b }

func TestKiCadLcscHealthJSONAndExit(t *testing.T) {
	stock := int64(1000000)
	withCatalog(t, appFakeCatalog{parts: map[string]*kicad.CatalogPart{
		"C25744": {LCSC: "C25744", MPN: "0402WGF1002TCE", Package: "0402", Base: tf(true), Stock: &stock, Buyable: tf(true)},
	}})
	out, errOut, err := runKiCadCmd(t, "lcsc", "--pcb", kicadFixBoard, "--health", "--qty", "4")
	var ec exitCodeError
	if !errors.As(err, &ec) || ec.code != 6 {
		t.Fatalf("want exit 6, got %v", err)
	}
	var rep kicad.HealthReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("%v\n%s", jerr, out)
	}
	if rep.Kind != kicad.HealthReportKind || rep.Boards != 4 || rep.Verdict != "fail" || rep.ExitCode != 6 {
		t.Errorf("%+v", rep)
	}
	got := map[string]string{}
	for _, c := range rep.Checks {
		got[c.Name] = string(c.Status)
	}
	if got["lcsc-present"] != "fail" || got["lcsc-valid"] != "fail" || got["catalog-found"] != "fail" {
		t.Errorf("checks %v", got)
	}
	if !strings.Contains(errOut, "FAIL") || !strings.Contains(errOut, "lcsc-present") {
		t.Errorf("stderr summary: %s", errOut)
	}
}

func TestKiCadLcscHealthOfflineIsExit5(t *testing.T) {
	withCatalog(t, appFakeCatalog{err: errors.New("no network")})
	// A board whose only assembled part has a valid number: nothing known to be wrong, nothing verifiable.
	dir := t.TempDir()
	b, _ := os.ReadFile(kicadFixBoard)
	pcb := filepath.Join(dir, "b.kicad_pcb")
	os.WriteFile(pcb, b, 0o644)
	_, _, err := runKiCadCmd(t, "lcsc", "--pcb", pcb, "--health")
	var ec exitCodeError
	if !errors.As(err, &ec) || (ec.code != 6 && ec.code != 5) {
		t.Fatalf("want a non-zero gate exit, got %v", err)
	}
}

func TestKiCadLcscHealthNeedsPCB(t *testing.T) {
	withCatalog(t, appFakeCatalog{})
	if _, _, err := runKiCadCmd(t, "lcsc", "--health"); err == nil || !strings.Contains(err.Error(), "--pcb") {
		t.Errorf("%v", err)
	}
	if _, _, err := runKiCadCmd(t, "lcsc", "--pcb", kicadFixBoard, "--health", "--alternatives"); err == nil {
		t.Error("two modes must be rejected")
	}
	if _, _, err := runKiCadCmd(t, "lcsc", "--pcb", kicadFixBoard, "--health", "--qty", "0"); err == nil {
		t.Error("--qty 0 boards must be rejected")
	}
}

func TestKiCadLcscAlternativesPlanAndStaleGuard(t *testing.T) {
	zero := int64(0)
	stock := int64(1000000)
	withCatalog(t, appFakeCatalog{parts: map[string]*kicad.CatalogPart{
		"C25744": {LCSC: "C25744", MPN: "OLD", Package: "0402", Base: tf(true), Stock: &zero, Buyable: tf(true), Category: "Chip Resistor - Surface Mount",
			Attrs: map[string]string{"Resistance": "10kΩ"}},
		"C2286": {LCSC: "C2286", MPN: "LED", Package: "0603", Base: tf(true), Stock: &stock, Buyable: tf(true)},
		"C2337": {LCSC: "C2337", MPN: "HDR", Package: "2.54mm", Base: tf(true), Stock: &stock, Buyable: tf(true)},
	}})
	out, _, err := runKiCadCmd(t, "lcsc", "--pcb", kicadFixBoard, "--alternatives")
	var ec exitCodeError
	// R1/R3 are out of stock and the fake search returns nothing: a blocking line without a proposal.
	if !errors.As(err, &ec) || ec.code != 6 {
		t.Fatalf("want exit 6, got %v", err)
	}
	var rep kicad.AltReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil || rep.Kind != kicad.AltReportKind {
		t.Fatalf("%v\n%s", jerr, out)
	}
	if len(rep.Assignments) != 0 {
		t.Errorf("nothing may be proposed: %v", rep.Assignments)
	}

	// A plan that expects numbers the board does not carry is refused before anything is written.
	plan := filepath.Join(t.TempDir(), "plan.json")
	os.WriteFile(plan, []byte(`{"kind":"pcbpilot.lcsc-alternatives","boards":1,"fingerprint":"x","assignments":{"R1":"C1525"},"expect":{"R1":"C99999"}}`), 0o644)
	_, _, err = runKiCadCmd(t, "lcsc", "--pcb", kicadFixBoard, "--set", "@"+plan)
	if err == nil || !strings.Contains(err.Error(), "no longer matches plan") || !strings.Contains(err.Error(), "R1") {
		t.Errorf("stale plan: %v", err)
	}
	_, _, err = runKiCadCmd(t, "lcsc", "--sch", "x.kicad_sch", "--set", "@"+plan)
	if err == nil || !strings.Contains(err.Error(), "needs --pcb") {
		t.Errorf("plan without --pcb: %v", err)
	}
}

// Real catalog through parts-select.py; skips when offline or no python.
func TestLiveScriptCatalogLookup(t *testing.T) {
	cat, err := newLcscCatalog("")
	if err != nil {
		t.Skip("parts-select.py / python unavailable: ", err)
	}
	res, err := cat.Lookup([]string{"C25744", "C99999999"})
	if err != nil {
		t.Skip("catalog helper failed: ", err)
	}
	if res["C25744"].Err != "" {
		t.Skip("catalog unreachable: ", res["C25744"].Err)
	}
	p := res["C25744"].Part
	if p == nil || p.LCSC != "C25744" || p.Stock == nil || p.Attrs["Resistance"] == "" {
		t.Fatalf("C25744: %+v", res["C25744"])
	}
	if r := res["C99999999"]; r.Part != nil || r.Err != "" {
		t.Errorf("an unknown number is 'not found', never another part: %+v", r)
	}
}

// ── review round 2 ─────────────────────────────────────────────────────────

type searchFakeCatalog struct {
	appFakeCatalog
	hits map[string][]kicad.CatalogPart
}

func (f searchFakeCatalog) Search(q string, _ int) ([]kicad.CatalogPart, error) {
	return f.hits[q], nil
}

// Finding 7: --offline is honoured by --health / --alternatives (no catalog is built or asked).
func TestKiCadLcscOfflineHealthAndAlternatives(t *testing.T) {
	old := newLcscCatalog
	newLcscCatalog = func(string) (kicad.Catalog, error) {
		t.Error("--offline must not build or query the live catalog")
		return nil, errors.New("must not be called")
	}
	t.Cleanup(func() { newLcscCatalog = old })
	for _, mode := range []string{"--health", "--alternatives"} {
		out, _, err := runKiCadCmd(t, "lcsc", "--pcb", kicadFixBoard, mode, "--offline")
		var ec exitCodeError
		if !errors.As(err, &ec) || (ec.code != 5 && ec.code != 6) {
			t.Fatalf("%s --offline: want a gate exit, got %v", mode, err)
		}
		if mode == "--health" {
			var rep kicad.HealthReport
			if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
				t.Fatal(jerr)
			}
			for _, c := range rep.Checks {
				if (c.Name == "stock" || c.Name == "availability" || c.Name == "catalog-found") && c.Status != "cannot-check" {
					t.Errorf("offline %s = %s", c.Name, c.Status)
				}
			}
			if rep.Verdict == "pass" {
				t.Error("offline can never pass")
			}
		} else {
			var rep kicad.AltReport
			if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
				t.Fatal(jerr)
			}
			if len(rep.Assignments) != 0 {
				t.Errorf("offline proposes nothing: %v", rep.Assignments)
			}
		}
	}
}

// Finding 2 at command level: a plan is bound to the substitution-relevant data.
func TestKiCadLcscPlanFingerprintGuard(t *testing.T) {
	zero, stock, loss := int64(0), int64(1000000), int64(10)
	attrs := map[string]string{"Resistance": "10kΩ", "Tolerance": "±1%", "Power(Watts)": "62.5mW", "Type": "Thick Film Resistor"}
	cat := "Chip Resistor - Surface Mount"
	good := kicad.CatalogPart{LCSC: "C777", MPN: "GOOD", Package: "0402", Base: tf(true), Stock: &stock, Loss: &loss, Buyable: tf(true), Category: cat, Attrs: attrs}
	withCatalog(t, searchFakeCatalog{
		appFakeCatalog: appFakeCatalog{parts: map[string]*kicad.CatalogPart{
			"C25744": {LCSC: "C25744", MPN: "OLD", Package: "0402", Base: tf(true), Stock: &zero, Loss: &loss, Buyable: tf(true), Category: cat, Attrs: attrs},
		}},
		hits: map[string][]kicad.CatalogPart{"10kΩ 0402": {good}},
	})
	b, err := os.ReadFile(kicadFixBoard)
	if err != nil {
		t.Fatal(err)
	}
	orig := string(b)
	dir := t.TempDir()
	pcb := filepath.Join(dir, "b.kicad_pcb")
	write := func(s string) {
		if err := os.WriteFile(pcb, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(orig)
	out, _, _ := runKiCadCmd(t, "lcsc", "--pcb", pcb, "--alternatives", "--qty", "3")
	var rep kicad.AltReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("%v\n%s", jerr, out)
	}
	if rep.Assignments["R1"] != "C777" || rep.Assignments["R3"] != "C777" || rep.Fingerprint == "" || rep.Boards != 3 {
		t.Fatalf("plan: %+v", rep)
	}
	planPath := filepath.Join(dir, "plan.json")
	os.WriteFile(planPath, []byte(out), 0o644)

	// The plan matches the unchanged design.
	pl, err := kicad.LoadPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPlans(pcb, "", map[string]*kicad.Plan{planPath: pl}, 100, false); err != nil {
		t.Fatalf("unchanged design must verify: %v", err)
	}
	if err := verifyPlans(pcb, "", map[string]*kicad.Plan{planPath: pl}, 3, true); err != nil {
		t.Fatalf("--qty equal to the plan's boards: %v", err)
	}

	r3 := strings.Index(orig, `(property "Reference" "R3"`)
	start := strings.LastIndex(orig[:r3], "(footprint ")
	end := start + strings.Index(orig[start+1:], "\n\t(footprint ") + 1
	r3Block := orig[start:end]
	mutations := map[string]string{
		"value":     strings.Replace(orig, `(property "Value" "10k"`, `(property "Value" "4k7"`, 1),
		"footprint": strings.Replace(orig, `(footprint "Resistor_SMD:R_0402_1005Metric"`, `(footprint "Resistor_SMD:R_0603_1608Metric"`, 1),
		"placement": orig[:end] + "\n\t" + strings.NewReplacer(`"R3"`, `"R9"`, `"C25744"`, `"C777"`).Replace(r3Block) + orig[end:],
	}
	for name, mut := range mutations {
		if mut == orig {
			t.Fatalf("%s: mutation did not apply", name)
		}
		write(mut)
		_, _, err := runKiCadCmd(t, "lcsc", "--pcb", pcb, "--set", "@"+planPath)
		if err == nil || !strings.Contains(err.Error(), "no longer matches plan") || !strings.Contains(err.Error(), "fingerprint") {
			t.Errorf("%s: stale plan must be refused, got %v", name, err)
		}
	}
	write(orig)
	_, _, err = runKiCadCmd(t, "lcsc", "--pcb", pcb, "--set", "@"+planPath, "--qty", "7")
	if err == nil || !strings.Contains(err.Error(), "differs from the 3 board") {
		t.Errorf("--qty mismatch: %v", err)
	}
}

func TestResolveSetArgsRejectsCombiningPlans(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "p.json")
	os.WriteFile(plan, []byte(`{"boards":1,"fingerprint":"x","assignments":{"R1":"C9"},"expect":{"R1":"C1"}}`), 0o644)
	plan2 := filepath.Join(dir, "q.json")
	os.WriteFile(plan2, []byte(`{"boards":1,"fingerprint":"y","assignments":{"R2":"C9"},"expect":{"R2":"C2"}}`), 0o644)
	if _, _, err := resolveSetArgs("x.kicad_pcb", []string{"@" + plan, "@" + plan2}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Errorf("two plans: %v", err)
	}
	if _, _, err := resolveSetArgs("x.kicad_pcb", []string{"@" + plan, "R5=C7"}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Errorf("plan + plain: %v", err)
	}
	if _, _, err := resolveSetArgs("x.kicad_pcb", []string{"R5=C7", "R6=C8"}); err != nil {
		t.Errorf("plain only: %v", err)
	}
}
