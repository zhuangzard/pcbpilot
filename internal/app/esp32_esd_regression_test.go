package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// The live-verified ESP32 mini (v0.5 E2E, 2026-09-27) with its intent: the
// USBLC6 (D3) must stay ON the USB pair's path. The stress/hs merge priced
// the whole BOTTOM layer ×4 for USB2, USB_DM detoured round D3 on TOP and
// the routed stub to the ESD array went 68 → 299 mil (joint 92.6 → 80.6).
// Inputs trimmed from artifacts/v05-live + artifacts/ceshi-e2e-20260924.
func TestESP32MiniIntentKeepsESDOnPath(t *testing.T) {
	if testing.Short() {
		t.Skip("places and routes a board")
	}
	d := "testdata/esp32-v05"
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	root := newRootCmd(&stdout, &stderr)
	root.SetArgs([]string{"--host", "127.0.0.1", "--ports", "9-9", "pcb", "auto", "run",
		"--board", filepath.Join(d, "board.json"), "--mech", filepath.Join(d, "mech.json"),
		"--intent", filepath.Join(d, "intent.json"), "--sim", filepath.Join(d, "sim.json"),
		"--groups", filepath.Join(d, "groups1.json"), "--groups", filepath.Join(d, "groups2.json"),
		"--place", "--layers", "4", "--seed", "3", "--no-feedback", "--out-dir", out})
	if err := root.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(out, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep pcbauto.Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	s := rep.Result.Route.Stats
	if s.Completion < 100 || len(rep.Result.DRC.Violations) > 0 {
		t.Fatalf("routing %.1f%%, DRC %d", s.Completion, len(rep.Result.DRC.Violations))
	}
	items := map[string]pcbauto.JointItem{}
	for _, it := range rep.Joint.Items {
		items[it.ID] = it
	}
	if esd := items["esd-stub"]; esd.Score < 89 {
		t.Errorf("esd-stub %.1f < 89: %s", esd.Score, esd.Detail)
	}
	// The USB pair (intent interface): the placer keeps its corridor clear
	// and turns the USBLC6 with the flow, so the pair checks (uncoupled
	// breakout, via and layer symmetry) pass — dev: 2/0 vias, 307/250 mil
	// uncoupled, 3 of 4 failed (2026-09-30).
	if dp, ok := items["diff-pair"]; !ok || dp.Score < 75 {
		t.Errorf("diff-pair %.1f < 75: %s", dp.Score, dp.Detail)
	}
	if rep.Joint.Overall < 92 {
		t.Errorf("joint %.1f < 92 (c1b32fd: 92.6)", rep.Joint.Overall)
	}
	if !strings.Contains(items["detour"].Detail, "7.7 in") {
		t.Logf("detour: %s", items["detour"].Detail)
	}
}
