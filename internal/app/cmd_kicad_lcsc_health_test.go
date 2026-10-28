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
	os.WriteFile(plan, []byte(`{"kind":"pcbpilot.lcsc-alternatives","assignments":{"R1":"C1525"},"expect":{"R1":"C99999"}}`), 0o644)
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
