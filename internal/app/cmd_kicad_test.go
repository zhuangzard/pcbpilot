package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/postsim"
)

// The KiCad snapshot fixture decodes with every existing snapshot reader.
func TestKicadSnapshotFixtureDecodes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := loadBoardSnapshotFile(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Components) != 2 || snap.Outline == nil || snap.Outline.Source != "polygon" || snap.CopperLayers != 2 {
		t.Fatalf("boardSnapshot: %+v", snap)
	}
	if p := snap.Components[1].Pads[0]; p.Layer != pcbLayerMulti || !p.isThroughHole() {
		t.Fatalf("THT pad: %+v", p)
	}
	if r := snap.Rules.toPcbRules(); r.trackWidthMinMil < 5.9 || r.source != "kicad" {
		t.Fatalf("rules: %+v", r)
	}
	b, err := pcbauto.FromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Parts) != 2 || len(b.Holes) != 1 || len(b.Outline) != 4 {
		t.Fatalf("pcbauto: %d parts %d holes %d outline", len(b.Parts), len(b.Holes), len(b.Outline))
	}
	pb, err := postsim.ParseBoard(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !pb.Copper || len(pb.Tracks) != 4 || len(pb.Vias) != 1 || pb.PadByPin("J1", "1") == nil || pb.PadByPin("J1", "1").Drill < 39 {
		t.Fatalf("postsim: copper %v tracks %d vias %d", pb.Copper, len(pb.Tracks), len(pb.Vias))
	}
}

func TestKicadIntentGates(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := parseDesignIntent([]byte(`{"nets":{
		"VCC":{"role":"power","widthMil":{"outer":20,"inner":20,"min":8}},
		"SIG_A":{"role":"signal","widthMil":{"outer":6,"inner":6,"min":6},"lengthGroup":"g","lengthTolMil":5},
		"GND":{"role":"ground","widthMil":{"outer":30,"inner":30,"min":10}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	g, lengths, err := kicadIntentGates(raw, in, nil, "net current")
	if err != nil {
		t.Fatal(err)
	}
	// t-vcc-neck: 10 < 20 within 50 mil of R1.1 (neck-down, allowed);
	// t-vcc-trunk: 10 < 20 away from any pad → violation; t-vcc-wide ok.
	if g.Pass || len(g.Items) != 1 || !strings.HasPrefix(g.Items[0], "VCC: 1 track(s), worst 10.00 < 20.00 mil on layer 1") {
		t.Fatalf("intent-widths: %+v", g)
	}
	if lengths != nil {
		t.Fatalf("a one-net length group is not checked: %+v", lengths)
	}
}

func TestKicadDRCGate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "drc.json"))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := kicad.ParseDRC(data)
	if err != nil {
		t.Fatal(err)
	}
	g := kicadDRCGate(rep, "drc.json")
	if g.Pass || g.Gate != "kicad-drc" || !strings.HasPrefix(g.Detail, "3 error(s)") {
		t.Fatalf("%+v", g)
	}
	joined := strings.Join(g.Items, "\n")
	for _, want := range []string{"clearance: 1", "unconnected_items: 1", "clearance [Track to Via] F.Cu net VCC / GND at (1000.0,-2000.0) objs u-track-1,u-via-1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	applyWaivers(&g, []gateWaiver{{Gate: "kicad-drc", Match: ":", Reason: "test", By: "t"}})
	if !g.Pass {
		t.Fatal("waiver covering every item must pass the gate")
	}
	empty, _ := json.Marshal(map[string]any{"violations": []any{}, "unconnected_items": []any{}, "coordinate_units": "mm"})
	rep, _ = kicad.ParseDRC(empty)
	if g := kicadDRCGate(rep, "x"); !g.Pass {
		t.Fatalf("clean report fails: %+v", g)
	}
}

func TestCopyKicadBoard(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.kicad_pcb")
	for _, ext := range []string{".kicad_pcb", ".kicad_pro"} {
		if err := os.WriteFile(filepath.Join(dir, "a"+ext), []byte(ext), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dst := filepath.Join(dir, "w", "b.kicad_pcb")
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	if err := copyKicadBoard(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "w", "b.kicad_pro")); string(b) != ".kicad_pro" {
		t.Fatalf("pro not copied: %q", b)
	}
	_ = os.Remove(filepath.Join(dir, "a.kicad_pro"))
	if err := copyKicadBoard(src, dst); err == nil {
		t.Fatal("missing .kicad_pro must fail")
	}
}
