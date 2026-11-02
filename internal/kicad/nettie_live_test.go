package kicad

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The snapshot carries a net-tie footprint's pad groups (kicad rules'
// footprint-pad-clearance skips only those touching pads).
func TestSnapshotNetTiePadGroupsLive(t *testing.T) {
	tools := bridgeLiveTools(t)
	mod, err := os.ReadFile(filepath.Join(StockFootprintDir(), "NetTie.pretty", "NetTie-2_SMD_Pad0.5mm.kicad_mod"))
	if err != nil {
		t.Skip(err)
	}
	var keep []string
	for _, l := range strings.Split(strings.ReplaceAll(string(mod), "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(l, "\t(version ") && !strings.HasPrefix(l, "\t(generator") {
			keep = append(keep, "\t"+l)
		}
	}
	fp := strings.Replace(strings.Join(keep, "\n"), `(footprint "NetTie-2_SMD_Pad0.5mm"`, `(footprint "NetTie:NetTie-2_SMD_Pad0.5mm"`, 1)
	fp = strings.Replace(fp, "(layer \"F.Cu\")", "(layer \"F.Cu\")\n\t\t(uuid \"00000000-0000-4000-8000-000000000123\")\n\t\t(at 150 60)", 1)
	src, err := os.ReadFile("testdata/tiny.kicad_pcb")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimSuffix(strings.TrimRight(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n"), ")") + fp + "\n)\n"
	pcb := filepath.Join(t.TempDir(), "nettie.kicad_pcb")
	if err := os.WriteFile(pcb, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := tools.Snapshot(pcb)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Components []struct {
			Footprint string     `json:"footprint"`
			Ties      [][]string `json:"netTiePadGroups"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range s.Components {
		if strings.HasPrefix(c.Footprint, "NetTie:") {
			found = true
			if !reflect.DeepEqual(c.Ties, [][]string{{"1", "2"}}) {
				t.Fatalf("net-tie groups %v", c.Ties)
			}
		} else if c.Ties != nil {
			t.Fatalf("%s has net-tie groups %v", c.Footprint, c.Ties)
		}
	}
	if !found {
		t.Fatal("no net-tie footprint in the snapshot")
	}
}
