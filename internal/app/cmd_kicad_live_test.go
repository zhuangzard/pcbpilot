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
	src := filepath.Join("..", "kicad", "testdata", "tiny.kicad_pcb")
	dir := t.TempDir()
	pcb := filepath.Join(dir, "tiny.kicad_pcb")
	if err := copyKicadBoard(src, pcb); err != nil {
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
	o := kicadRouteOpts{pcb: pcb, intent: intent, outDir: out, fastrouteBin: fr, widthBasis: "net"}
	o.fo.threads, o.fo.rounds, o.fo.timeout = 1, 2, 5*time.Minute
	var stdout, stderr bytes.Buffer
	err = runKicadRoute(o, &stdout, &stderr)
	t.Log(stderr.String())
	if err != nil {
		t.Fatalf("kicad route: %v\n%s", err, stdout.String())
	}
	var sum struct {
		Pass  bool         `json:"pass"`
		Gates []gateResult `json:"gates"`
		Final *struct {
			Unrouted int `json:"unrouted"`
		} `json:"routeFinal"`
		DRC struct {
			Total int `json:"total"`
		} `json:"drc"`
	}
	data, err := os.ReadFile(filepath.Join(out, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &sum); err != nil {
		t.Fatal(err)
	}
	if !sum.Pass || sum.Final == nil || sum.Final.Unrouted != 0 || sum.DRC.Total != 0 || len(sum.Gates) < 3 {
		t.Fatalf("summary: %s", data)
	}
	// The input board is untouched.
	a, _ := os.ReadFile(src)
	b, _ := os.ReadFile(pcb)
	if !bytes.Equal(a, b) {
		t.Fatal("input board was modified")
	}
}
