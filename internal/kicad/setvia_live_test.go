package kicad

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bridge edit setVia: a via gets the exact diameter / drill (mil → nm);
// a track id or drill ≥ diameter is refused.
func TestEditSetViaLive(t *testing.T) {
	tools := bridgeLiveTools(t)
	src, err := os.ReadFile("testdata/tiny.kicad_pcb")
	if err != nil {
		t.Fatal(err)
	}
	via := "\t(via\n\t\t(at 40 40)\n\t\t(size 0.7)\n\t\t(drill 0.4)\n\t\t(layers \"F.Cu\" \"B.Cu\")\n\t\t(net \"GND\")\n\t\t(uuid \"00000000-0000-4000-8000-0000000000aa\")\n\t)\n"
	body := strings.TrimSuffix(strings.TrimRight(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n"), ")") + via + ")\n"
	dir := t.TempDir()
	in := filepath.Join(dir, "v.kicad_pcb")
	_ = os.WriteFile(in, []byte(body), 0o644)
	out := filepath.Join(dir, "o.kicad_pcb")
	if _, err := tools.Edit(in, map[string]any{"setVia": []map[string]any{{"id": "00000000-0000-4000-8000-0000000000aa", "diameter": 24.0, "drill": 12.0}}}, out); err != nil {
		t.Fatal(err)
	}
	raw, err := tools.Snapshot(out)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Copper struct {
			Vias []struct {
				ID    string  `json:"primitiveId"`
				Dia   float64 `json:"diameter"`
				Drill float64 `json:"holeDiameter"`
			} `json:"vias"`
		} `json:"copper"`
	}
	_ = json.Unmarshal(raw, &s)
	ok := false
	for _, v := range s.Copper.Vias {
		if strings.HasPrefix(v.ID, "00000000-0000-4000-8000-0000000000aa") {
			ok = math.Abs(v.Dia-24) < 1e-3 && math.Abs(v.Drill-12) < 1e-3
		}
	}
	if !ok {
		t.Fatalf("via not resized: %+v", s.Copper.Vias)
	}
	if _, err := tools.Edit(in, map[string]any{"setVia": []map[string]any{{"id": "00000000-0000-4000-8000-0000000000aa", "diameter": 12.0, "drill": 12.0}}}, filepath.Join(dir, "bad.kicad_pcb")); err == nil {
		t.Fatal("drill >= diameter accepted")
	}
}
