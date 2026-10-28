package kicad

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
)

func i64(v int64) *int64 { return &v }
func bp(v bool) *bool    { return &v }

// fakeCatalog answers from maps; a code in errs is "could not ask".
type fakeCatalog struct {
	byCode  map[string]*CatalogPart
	errs    map[string]string
	search  map[string][]CatalogPart
	lookErr error
	queries []string
}

func (f *fakeCatalog) Lookup(codes []string) (map[string]CatalogLookup, error) {
	if f.lookErr != nil {
		return nil, f.lookErr
	}
	out := map[string]CatalogLookup{}
	for _, c := range codes {
		switch {
		case f.errs[c] != "":
			out[c] = CatalogLookup{Err: f.errs[c]}
		case f.byCode[c] != nil:
			out[c] = CatalogLookup{Part: f.byCode[c]}
		default:
			out[c] = CatalogLookup{}
		}
	}
	return out, nil
}

func (f *fakeCatalog) Search(q string, qty int) ([]CatalogPart, error) {
	f.queries = append(f.queries, q)
	return f.search[q], nil
}

func resistor(code, mpn, pkg string, ohms, tol, power string, base bool, stock int64) *CatalogPart {
	return &CatalogPart{LCSC: code, MPN: mpn, Package: pkg, Category: "Chip Resistor - Surface Mount", Base: bp(base),
		Stock: i64(stock), Buyable: bp(true), Loss: i64(10),
		Attrs: map[string]string{"Resistance": ohms, "Tolerance": tol, "Power(Watts)": power, "Type": "Thick Film Resistor"}}
}

func partsOf(rows ...[4]string) []Part { // ref, value, footprint, lcsc
	var ps []Part
	for _, r := range rows {
		ps = append(ps, Part{Ref: r[0], Value: r[1], Footprint: r[2], LCSC: r[3]})
	}
	return ps
}

func checkByName(t *testing.T, rep *HealthReport, name string) HealthCheck {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %s", name)
	return HealthCheck{}
}

const r0402 = "Resistor_SMD:R_0402_1005Metric"

func TestHealthAllGood(t *testing.T) {
	cat := &fakeCatalog{byCode: map[string]*CatalogPart{
		"C25744": resistor("C25744", "0402WGF1002TCE", "0402", "10kΩ", "±1%", "62.5mW", true, 1000000),
	}}
	rep := RunHealth(HealthInput{Parts: partsOf([4]string{"R1", "10k", r0402, "C25744"}, [4]string{"R3", "10k", r0402, "C25744"}), Boards: 5, Catalog: cat})
	if rep.Verdict != gate.Pass || rep.ExitCode != 0 {
		for _, c := range rep.Checks {
			t.Logf("%s %s %s %v", c.Name, c.Status, c.Detail, c.Items)
		}
		t.Fatalf("verdict %s", rep.Verdict)
	}
	if len(rep.Lines) != 1 || rep.Lines[0].Placements != 2 || *rep.Lines[0].Need != 20 || rep.Lines[0].Library != "basic" {
		t.Errorf("lines %+v", rep.Lines)
	}
	if c := checkByName(t, rep, "duplicate-mpn"); c.Status != gate.Pass {
		t.Errorf("dup %+v", c)
	}
}

func TestHealthRisks(t *testing.T) {
	disc := resistor("C2", "OLD", "0402", "1kΩ", "±1%", "62.5mW", true, 5000)
	disc.Buyable, disc.NoBuyReason, disc.AlternativesCode = bp(false), "discontinued", "C25744"
	low := resistor("C3", "LOW", "0402", "2kΩ", "±1%", "62.5mW", true, 3)
	ext := resistor("C4", "EXT", "0402", "4kΩ", "±1%", "62.5mW", false, 90000)
	wrongPkg := resistor("C5", "WP", "0603", "5kΩ", "±1%", "62.5mW", true, 90000)
	dupA := resistor("C6", "SAME-MPN", "0402", "6kΩ", "±1%", "62.5mW", true, 90000)
	dupB := resistor("C7", "same_mpn", "0402", "6kΩ", "±5%", "62.5mW", true, 90000)
	cat := &fakeCatalog{byCode: map[string]*CatalogPart{"C2": disc, "C3": low, "C4": ext, "C5": wrongPkg, "C6": dupA, "C7": dupB},
		errs: map[string]string{"C8": "timeout"}}
	parts := partsOf(
		[4]string{"R1", "1k", r0402, "C2"}, [4]string{"R2", "2k", r0402, "C3"}, [4]string{"R3", "4k", r0402, "C4"},
		[4]string{"R4", "5k", r0402, "C5"}, [4]string{"R5", "6k", r0402, "C6"}, [4]string{"R6", "6k", r0402, "C7"},
		[4]string{"R7", "9k", r0402, "C99"}, [4]string{"R8", "8k", r0402, "C8"},
		[4]string{"R9", "1", r0402, ""}, [4]string{"R10", "2", r0402, "AO3400A"},
		[4]string{"J1", "conn", "Connector:Odd", "C5"},
	)
	rep := RunHealth(HealthInput{Parts: parts, Boards: 1, Catalog: cat})
	if rep.Verdict != gate.Fail || rep.ExitCode != 6 {
		t.Fatalf("verdict %s", rep.Verdict)
	}
	want := map[string]gate.Status{
		"lcsc-present": gate.Fail, "lcsc-valid": gate.Fail, "catalog-found": gate.Fail, "availability": gate.Fail,
		"stock": gate.Fail, "extended-library": gate.Warn, "footprint-package": gate.Fail, "duplicate-mpn": gate.Warn,
	}
	for name, st := range want {
		if got := checkByName(t, rep, name); got.Status != st {
			t.Errorf("%s = %s (%s) %v", name, got.Status, got.Detail, got.Items)
		}
	}
	if !checkByName(t, rep, "extended-library").Advisory {
		t.Error("extended-library is advisory")
	}
	av := strings.Join(checkByName(t, rep, "availability").Items, "\n")
	if !strings.Contains(av, "discontinued") || !strings.Contains(av, "C25744") {
		t.Errorf("availability evidence: %s", av)
	}
	st := strings.Join(checkByName(t, rep, "stock").Items, "\n")
	if !strings.Contains(st, "C3") || !strings.Contains(st, "stock 3 < need 11") {
		t.Errorf("stock evidence: %s", st)
	}
	if !strings.Contains(strings.Join(checkByName(t, rep, "footprint-package").Items, "\n"), "R4") {
		t.Errorf("package evidence: %v", checkByName(t, rep, "footprint-package").Items)
	}
	if !strings.Contains(strings.Join(checkByName(t, rep, "catalog-found").Items, "\n"), "C8: could not ask") {
		t.Errorf("per-code error lost: %v", checkByName(t, rep, "catalog-found").Items)
	}
	if !strings.Contains(strings.Join(checkByName(t, rep, "duplicate-mpn").Items, "\n"), "C6, C7") {
		t.Errorf("dup evidence: %v", checkByName(t, rep, "duplicate-mpn").Items)
	}
}

func TestHealthOfflineNeverPasses(t *testing.T) {
	cat := &fakeCatalog{lookErr: errors.New("network unreachable")}
	rep := RunHealth(HealthInput{Parts: partsOf([4]string{"R1", "10k", r0402, "C25744"}), Boards: 1, Catalog: cat})
	if rep.Verdict != gate.CannotCheck || rep.ExitCode != 5 {
		t.Fatalf("verdict %s exit %d", rep.Verdict, rep.ExitCode)
	}
	for _, name := range []string{"catalog-found", "availability", "stock", "footprint-package", "duplicate-mpn"} {
		if st := checkByName(t, rep, name).Status; st != gate.CannotCheck {
			t.Errorf("%s = %s, want cannot-check", name, st)
		}
	}
	if st := checkByName(t, rep, "extended-library").Status; st == gate.Pass {
		t.Error("extended-library must not pass without a catalog")
	}
}

func TestHealthOfflineStillFailsOnKnownProblems(t *testing.T) {
	// fail outranks cannot-check: a missing number is a fact, offline or not.
	cat := &fakeCatalog{lookErr: errors.New("offline")}
	rep := RunHealth(HealthInput{Parts: partsOf([4]string{"R1", "10k", r0402, ""}, [4]string{"R2", "10k", r0402, "C25744"}), Boards: 1, Catalog: cat})
	if rep.Verdict != gate.Fail {
		t.Fatalf("verdict %s", rep.Verdict)
	}
}

func TestHealthNoAssembledParts(t *testing.T) {
	ps := partsOf([4]string{"R1", "10k", r0402, "C25744"})
	ps[0].DNP = true
	rep := RunHealth(HealthInput{Parts: ps, Boards: 1, Catalog: &fakeCatalog{}})
	if rep.Verdict != gate.CannotCheck {
		t.Fatalf("verdict %s", rep.Verdict)
	}
	if checkByName(t, rep, "lcsc-present").Status != gate.CannotCheck {
		t.Error("lcsc-present must be cannot-check with nothing assembled")
	}
}

func TestHealthMissingStockFigureIsNotZero(t *testing.T) {
	p := resistor("C1", "X", "0402", "1kΩ", "±1%", "62.5mW", true, 0)
	p.Stock, p.Buyable = nil, nil
	rep := RunHealth(HealthInput{Parts: partsOf([4]string{"R1", "1k", r0402, "C1"}), Boards: 1, Catalog: &fakeCatalog{byCode: map[string]*CatalogPart{"C1": p}}})
	if checkByName(t, rep, "stock").Status != gate.CannotCheck || checkByName(t, rep, "availability").Status != gate.CannotCheck {
		t.Errorf("missing fields must be cannot-check: %+v", rep.Checks)
	}
}

func TestDesignMPNWinsOverCatalog(t *testing.T) {
	a := resistor("C1", "CATALOG-A", "0402", "1kΩ", "±1%", "62.5mW", true, 9999)
	b := resistor("C2", "CATALOG-B", "0402", "1kΩ", "±1%", "62.5mW", true, 9999)
	rep := RunHealth(HealthInput{
		Parts:     partsOf([4]string{"R1", "1k", r0402, "C1"}, [4]string{"R2", "1k", r0402, "C2"}),
		DesignMPN: map[string]string{"R1": "RC0402FR-071KL", "R2": "rc0402fr-07 1kl"},
		Boards:    1, Catalog: &fakeCatalog{byCode: map[string]*CatalogPart{"C1": a, "C2": b}}})
	if c := checkByName(t, rep, "duplicate-mpn"); c.Status != gate.Warn {
		t.Errorf("design MPN fields normalise equal: %+v", c)
	}
}

func TestPackageCompat(t *testing.T) {
	cases := []struct {
		fp, cat string
		want    Compat
	}{
		{"Resistor_SMD:R_0402_1005Metric", "0402", CompatMatch},
		{"Capacitor_SMD:C_0201_0603Metric", "0201", CompatMatch}, // 0603 is the metric code here
		{"Capacitor_SMD:C_0201_0603Metric", "0603", CompatMismatch},
		{"LED_SMD:LED_0603_1608Metric", "0805", CompatMismatch},
		{"Resistor_SMD:R_1608Metric", "0603", CompatMatch},
		{"Package_TO_SOT_SMD:SOT-23", "SOT-23", CompatMatch},
		{"Package_TO_SOT_SMD:SOT-23", "SOT-23-3", CompatMatch},
		{"Package_TO_SOT_SMD:SOT-23", "SOT-23-5", CompatMismatch},
		{"Package_TO_SOT_SMD:SOT-23-5", "SOT-25", CompatMatch},
		{"Package_TO_SOT_SMD:SOT-23-6", "SOT-23-5", CompatMismatch},
		{"Package_SO:SOIC-8_3.9x4.9mm_P1.27mm", "SOP-8", CompatMatch},
		{"Package_SO:SOIC-8_3.9x4.9mm_P1.27mm", "SOP-16", CompatMismatch},
		{"Package_SO:TSSOP-16_4.4x5mm_P0.65mm", "SOP-16", CompatMismatch},
		{"Package_TO_SOT_SMD:SOT-223-3_TabPin2", "SOT-223", CompatUnknown}, // pin count on one side only
		{"Package_TO_SOT_SMD:SOT-223", "SOT-223", CompatMatch},
		{"Resistor_SMD:R_0603_1608Metric", "SOT-23", CompatMismatch},
		{"Connector:Odd", "Plugin,D3.7xL8.7mm", CompatUnknown},
		{"Resistor_SMD:R_0402_1005Metric", "0402_x4", CompatUnknown},
		{"Resistor_SMD:R_0402_1005Metric", "", CompatUnknown},
	}
	for _, c := range cases {
		if got, why := PackageCompat(c.fp, c.cat); got != c.want {
			t.Errorf("%s vs %q = %s (%s), want %s", c.fp, c.cat, got, why, c.want)
		}
	}
}

func TestParseQuantity(t *testing.T) {
	for in, want := range map[string]Quantity{
		"10kΩ": {1e4, "Ω"}, "100nF": {1e-7, "F"}, "4.7µF": {4.7e-6, "F"}, "4.7uF": {4.7e-6, "F"}, "16V": {16, "V"},
		"62.5mW": {0.0625, "W"}, "±1%": {1, "%"}, "100mΩ": {0.1, "Ω"}, "2MΩ": {2e6, "Ω"}, "0Ω": {0, "Ω"},
	} {
		got, ok := ParseQuantity(in)
		if !ok || !sameQty(got, want) {
			t.Errorf("%q = %+v %v, want %+v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "X7R", "+80%,-20%", "1/8W", "10k-20k", "m"} {
		if _, ok := ParseQuantity(bad); ok {
			t.Errorf("%q must not parse", bad)
		}
	}
	a, _ := ParseQuantity("1mΩ")
	b, _ := ParseQuantity("1MΩ")
	if sameQty(a, b) {
		t.Error("mΩ and MΩ differ")
	}
}

func TestLoadPlanAndVerify(t *testing.T) {
	d := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(d, "plan.json")
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	a, e, err := LoadPlan(write("\xef\xbb\xbf" + `{"kind":"pcbpilot.lcsc-alternatives","assignments":{"R1":"c25744"},"expect":{"R1":"C99"}}`))
	if err != nil || a["R1"] != "C25744" || e["R1"] != "C99" {
		t.Fatalf("%v %v %v", a, e, err)
	}
	for name, body := range map[string]string{
		"no expect": `{"assignments":{"R1":"C1"}}`, "empty": `{"assignments":{},"expect":{}}`,
		"bad value": `{"assignments":{"R1":"25744"},"expect":{"R1":""}}`, "kind": `{"kind":"x","assignments":{"R1":"C1"},"expect":{"R1":""}}`,
		"missing ref expect": `{"assignments":{"R1":"C1"},"expect":{"R2":""}}`,
	} {
		if _, _, err := LoadPlan(write(body)); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	parts := partsOf([4]string{"R1", "1k", r0402, "C99"}, [4]string{"R2", "1k", r0402, "C5"})
	if bad := VerifyExpect(parts, map[string]string{"R1": "C99", "R2": "C5"}); len(bad) != 0 {
		t.Errorf("%v", bad)
	}
	bad := VerifyExpect(parts, map[string]string{"R1": "C1", "R9": "C5"})
	if len(bad) != 2 {
		t.Errorf("%v", bad)
	}
}
