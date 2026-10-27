package postsim

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// A hierarchical KiCad board names a sheet-local net "/power/+3V3" while
// sim power, fed by the schematic's display names, calls it "+3V3". The
// sim net resolves to the board's identity (pkg/netname): no false
// sim-board-mismatch, and the net's copper is solved as before.
func TestSimNetsResolveToHierarchicalBoardNames(t *testing.T) {
	board, err := os.ReadFile("testdata/esp32mini/board.json")
	if err != nil {
		t.Fatal(err)
	}
	board = []byte(strings.ReplaceAll(string(board), `"+3V3"`, `"/power/+3V3"`))
	o := DefaultOptions()
	intent, err := os.ReadFile("testdata/esp32mini/intent.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyIntent(intent, &o); err != nil {
		t.Fatal(err)
	}
	r, err := Run(board, simFrom(t, "../../internal/app/testdata/esp32-v05"), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.Findings {
		if f.Kind == "sim-board-mismatch" {
			t.Fatalf("false sim-board-mismatch on a hierarchical net name: %s", f.Message)
		}
	}
	if n := netOf(t, r, "/power/+3V3"); n.Status != "ok" || math.Abs(n.WorstMV-2.70) > 0.05*2.70 {
		t.Errorf("/power/+3V3: %s %.3f mV, want the flat-name baseline ≈ 2.70 mV", n.Status, n.WorstMV)
	}
	if !containsPrefix(r.Assumptions, "sim nets named by their schematic display name") {
		t.Errorf("the rename is not recorded: %v", r.Assumptions)
	}
}

// A sim with no current anywhere and no dissipating part verifies nothing:
// the verdict is "unverified" (with a not-verified finding), never pass.
func TestNothingSimulatedIsUnverified(t *testing.T) {
	var sim powersim.Output
	if err := json.Unmarshal(simFrom(t, "../../internal/app/testdata/esp32-v05"), &sim); err != nil {
		t.Fatal(err)
	}
	for i := range sim.Results {
		for _, n := range sim.Results[i].Nets {
			n.CurrentA, n.ThermalCurrentA = 0, 0
			for j := range n.Pins {
				n.Pins[j].CurrentA = 0
			}
		}
		for _, p := range sim.Results[i].Parts {
			p.PowerW, p.ThermalW = 0, 0
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
	if r.Verdict.Status != "unverified" {
		t.Fatalf("verdict %s, want unverified: %v", r.Verdict.Status, r.Verdict.Reasons)
	}
	found := false
	for _, f := range r.Findings {
		found = found || f.Kind == "not-verified"
	}
	if !found {
		t.Errorf("no not-verified finding: %+v", r.Findings)
	}
}
