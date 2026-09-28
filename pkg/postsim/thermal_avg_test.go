package postsim

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// simFrom runs sim power on a connectivity + values fixture and returns the
// sim.json bytes, exactly as `pcbpilot sim power --out` writes them.
func simFrom(t *testing.T, dir string) []byte {
	t.Helper()
	var pages []*powersim.ConnDoc
	for _, f := range []string{"sch-905bb85957eaf435.json", "sch-950ae6609e91d753.json"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		d, err := powersim.ParseConnectivity(b)
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, d)
	}
	vb, err := os.ReadFile(filepath.Join(dir, "values.json"))
	if err != nil {
		t.Fatal(err)
	}
	vals, err := powersim.ParseValues(vb)
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := powersim.BuildDesign(pages, vals)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := powersim.LoadLibrary("../../.agents/skills/pcbpilot/references/power-models.json")
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := powersim.Simulate(d, powersim.Libraries{lib}, powersim.Options{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestESP32MiniThermalAverage is the regression for the E2E "192 °C board":
// the live ESP32-S3 mini board with the sim of its own schematic revision
// (internal/app/testdata/esp32-v05). Steady-state heat uses the time-averaged
// power (ESP32 3.3 V × 0.1 A average, not its 0.5 A TX burst), so the board
// sits near 39 °C; IR drop and via current keep the peak currents and must
// not move from the TestESP32MiniPostLayout baseline.
func TestESP32MiniThermalAverage(t *testing.T) {
	read := func(n string) []byte {
		b, err := os.ReadFile("testdata/esp32mini/" + n)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	o := DefaultOptions()
	if err := ApplyIntent(read("intent.json"), &o); err != nil {
		t.Fatal(err)
	}
	r, err := Run(read("board.json"), simFrom(t, "../../internal/app/testdata/esp32-v05"), o)
	if err != nil {
		t.Fatal(err)
	}
	for net, mv := range map[string]float64{"+3V3": 2.70, "VSYS_5V": 13.61, "USB_VBUS": 17.07, "+5V_TERM": 0.99} {
		if n := netOf(t, r, net); n.Status != "ok" || math.Abs(n.WorstMV-mv) > 0.05*mv {
			t.Errorf("%s: %s %.3f mV, want the peak-current baseline ≈ %.2f mV", net, n.Status, n.WorstMV, mv)
		}
	}
	if len(r.Vias) == 0 || math.Abs(r.Vias[0].CurrentA-0.5014) > 0.01 {
		t.Errorf("top via %+v: via current must stay at the peak", r.Vias[0])
	}
	th := r.Thermal
	// ≈ 0.49 W on the ~45 × 45 mm board (h 10 W/m²K both faces): ~+14 °C.
	if th.MaxBoardC < 33 || th.MaxBoardC > 45 || math.Abs(th.BalanceErrPct) > 1 {
		t.Errorf("thermal max %.2f °C (%s), balance %.4f %%: want ≈ 39 °C", th.MaxBoardC, th.Scenario, th.BalanceErrPct)
	}
	if th.PartsW > 0.6 || th.PartsW < 0.4 {
		t.Errorf("thermal parts power %.3f W, want the time-averaged ≈ 0.49 W", th.PartsW)
	}
	for _, p := range th.Parts {
		if p.Ref == "U3" && math.Abs(p.PowerW-3.318*0.1) > 0.01 {
			t.Errorf("U3 (ESP32) thermal power %.4f W, want its average 0.33 W", p.PowerW)
		}
	}
	if r.Verdict.Status != "pass" {
		t.Errorf("verdict %s: %v", r.Verdict.Status, r.Verdict.Reasons)
	}
	if !containsPrefix(r.Assumptions, "thermal: time-averaged heat") {
		t.Errorf("missing the thermal-basis assumption: %v", r.Assumptions)
	}
	t.Logf("board max %.1f °C (%s) from %.3f W", th.MaxBoardC, th.Scenario, th.PartsW)
}

// TestSimBoardMismatch: the E2E fed the sim of an earlier schematic revision
// (ESP32 = U1, buck = U4) with the live board (buck = U1, ESP32 = U3). The
// 1.66 W ESP32 burst landed on the SOT-23 buck footprint (192 °C) and every
// renamed net silently dropped its current. Now it fails, naming why.
func TestSimBoardMismatch(t *testing.T) {
	read := func(n string) []byte {
		b, err := os.ReadFile("testdata/esp32mini/" + n)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	o := DefaultOptions()
	if err := ApplyIntent(read("intent.json"), &o); err != nil {
		t.Fatal(err)
	}
	r, err := Run(read("board.json"), simFrom(t, "../../pkg/powersim/testdata/esp32mini"), o)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict.Status != "fail" {
		t.Fatalf("verdict %s, want fail", r.Verdict.Status)
	}
	var ident, pins bool
	for _, f := range r.Findings {
		if f.Kind != "sim-board-mismatch" || f.Severity != "fail" {
			continue
		}
		if strings.Contains(f.Message, "U1: sim ESP32-S3-WROOM-1, board SY8089A1AAC") {
			ident = true
		}
		if strings.Contains(f.Message, "no matching pad") {
			pins = true
		}
	}
	if !ident || !pins {
		t.Fatalf("designator mismatch %v, pin mismatch %v: %+v", ident, pins, r.Findings)
	}
}

func containsPrefix(xs []string, p string) bool {
	for _, x := range xs {
		if strings.HasPrefix(x, p) {
			return true
		}
	}
	return false
}

// A what-if value change (LED resistor R7 1 kΩ → 330 Ω) simulated before it
// reaches the PCB: same footprint, different MPN — a warning that the board
// is not synced, not a design-revision mismatch.
func TestSimBoardValueChangeWarns(t *testing.T) {
	var sim powersim.Output
	if err := json.Unmarshal(simFrom(t, "../../internal/app/testdata/esp32-v05"), &sim); err != nil {
		t.Fatal(err)
	}
	for i := range sim.Results {
		if p := sim.Results[i].Parts["R7"]; p != nil {
			p.MPN = "0402WGF3300TCE"
		}
	}
	raw, _ := json.Marshal(sim)
	board, err := os.ReadFile("testdata/esp32mini/board.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Run(board, raw, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var warn bool
	for _, f := range r.Findings {
		if f.Kind == "sim-board-mismatch" {
			t.Fatalf("value change reported as a revision mismatch: %s", f.Message)
		}
		if f.Kind == "sim-board-bom" && f.Severity == "warn" && strings.Contains(f.Message, "R7") {
			warn = true
		}
	}
	if !warn {
		t.Fatalf("no sim-board-bom warning: %+v", r.Findings)
	}
}
