package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// TestKicadLive drives the installed KiCad on the committed tiny fixture
// board. Skipped unless PCBPILOT_KICAD_LIVE=1; the routing half also needs
// PCBPILOT_FASTROUTE_LIVE=<fastroute binary>.
func TestKicadLive(t *testing.T) {
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 to run against the installed KiCad")
	}
	kt, err := kicad.Locate()
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join("..", "kicad", "testdata", "tiny-route.kicad_pcb")
	dir := t.TempDir()
	pcb := filepath.Join(dir, "tiny-route.kicad_pcb")
	if _, err := copyKicadBoard(src, pcb); err != nil {
		t.Fatal(err)
	}

	raw, err := kt.Snapshot(pcb)
	if err != nil {
		t.Fatal(err)
	}
	var snap boardSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Components) != 7 || snap.CopperLayers != 2 || snap.Outline == nil || snap.Outline.Source != "polygon" {
		t.Fatalf("snapshot: %d comps, %d layers, outline %+v", len(snap.Components), snap.CopperLayers, snap.Outline)
	}
	if _, err := pcbauto.FromSnapshot(raw); err != nil {
		t.Fatalf("pcbauto.FromSnapshot: %v", err)
	}

	fr := os.Getenv("PCBPILOT_FASTROUTE_LIVE")
	if fr == "" {
		t.Skip("set PCBPILOT_FASTROUTE_LIVE=<fastroute binary> to route")
	}
	intent := filepath.Join(dir, "intent.json")
	in := `{"nets":{"VCC":{"role":"power","widthMil":{"outer":20,"inner":20,"min":20},"clearanceMil":8},
	"GND":{"role":"ground","widthMil":{"outer":16,"inner":16,"min":10},"clearanceMil":8},
	"SIG_A":{"role":"signal","widthMil":{"outer":8,"inner":8,"min":6},"clearanceMil":6}}}`
	if err := os.WriteFile(intent, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "route")
	waivers := filepath.Join(dir, "waivers.json")
	if err := os.WriteFile(waivers, []byte(`[{"gate":"design-review","match":"--no-review","reason":"live unit test","by":"test"},
		{"gate":"board-manual","match":"--no-manual","reason":"live unit test","by":"test"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	o := kicadRouteOpts{pcb: pcb, intent: intent, outDir: out, fastrouteBin: fr, widthBasis: "net", pours: "auto", gndNet: "GND", powerLayer: -1,
		silk: defaultSilkTightOpts(), noReview: true, noManual: true, waivers: waivers, projectName: "tiny", projectConfig: "none", widenMax: 40}
	o.fo.threads, o.fo.rounds, o.fo.timeout = 1, 2, 5*time.Minute
	var stdout, stderr bytes.Buffer
	err = runKicadRoute(o, &stdout, &stderr)
	t.Log(stderr.String())
	// No --sim: post-layout-sim fails by design, so the run fails; the
	// routing gates must pass.
	data, rerr := os.ReadFile(filepath.Join(out, "summary.json"))
	if rerr != nil {
		t.Fatalf("no summary (%v): %v", err, rerr)
	}
	var sum struct {
		Gates []gateResult `json:"gates"`
		Final *struct {
			Unrouted int `json:"unrouted"`
		} `json:"routeFinal"`
		DRC struct {
			Total int `json:"total"`
		} `json:"drc"`
	}
	if err := json.Unmarshal(data, &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Final == nil || sum.Final.Unrouted != 0 || sum.DRC.Total != 0 {
		t.Fatalf("summary: %s", data)
	}
	got := map[string]bool{}
	for _, g := range sum.Gates {
		got[g.Gate] = g.Pass
	}
	for _, g := range []string{"design-review", "route-complete", "kicad-drc", "intent-rules", "intent-widths", "copper-to-edge", "isolation", "via-current"} {
		if p, ok := got[g]; !ok || !p {
			t.Errorf("gate %s: present %v pass %v\n%s", g, ok, p, data)
		}
	}
	if p, ok := got["post-layout-sim"]; !ok || p {
		t.Errorf("post-layout-sim without --sim must be present and fail")
	}
	for _, f := range []string{"routed.kicad_pcb", "routed.kicad_pro", "board-final.json", "drc.json", "report"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("missing output %s", f)
		}
	}
	// The input board is untouched.
	a, _ := os.ReadFile(src)
	b, _ := os.ReadFile(pcb)
	if !bytes.Equal(a, b) {
		t.Fatal("input board was modified")
	}
}
