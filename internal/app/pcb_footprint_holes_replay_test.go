package app

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// TestReplayAugmentBoardWithFootprintHoles is an offline replay helper, not a
// regression: it adds footprintHoles[] to an OLD `pcb dump` (captured before
// the connector exposed footprint refs) using the project's epro2 archive —
// the PCB document's ATTR Footprint rows give each component's footprint
// instance uuid, the FOOTPRINT documents give the source. Run with
//
//	PCBPILOT_FPHOLES_EPRO2=<project.epro2> PCBPILOT_FPHOLES_BOARD=<dump.json> \
//	PCBPILOT_FPHOLES_OUT=<augmented.json> go test ./internal/app -run ReplayAugment
func TestReplayAugmentBoardWithFootprintHoles(t *testing.T) {
	archive, board, out := os.Getenv("PCBPILOT_FPHOLES_EPRO2"), os.Getenv("PCBPILOT_FPHOLES_BOARD"), os.Getenv("PCBPILOT_FPHOLES_OUT")
	if archive == "" || board == "" || out == "" {
		t.Skip("offline replay helper: set PCBPILOT_FPHOLES_EPRO2/BOARD/OUT")
	}
	zr, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var text string
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, ".epru") {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			text = string(b)
		}
	}
	if text == "" {
		t.Fatal("no .epru in archive")
	}
	sources := map[string]string{}
	compFootprint := map[string]string{} // PCB component id → footprint instance uuid
	cur, inPCB := "", false
	for _, line := range strings.Split(text, "\n") {
		i := strings.Index(line, "||")
		if i < 0 {
			continue
		}
		var h struct{ Type string }
		_ = json.Unmarshal([]byte(line[:i]), &h)
		body := strings.TrimSuffix(strings.TrimSpace(line[i+2:]), "|")
		if h.Type == "DOCHEAD" {
			var p struct{ DocType, UUID string }
			_ = json.Unmarshal([]byte(body), &p)
			cur, inPCB = "", p.DocType == "PCB"
			if p.DocType == "FOOTPRINT" {
				cur = p.UUID
			}
		}
		if cur != "" {
			sources[cur] += line + "\n"
		}
		if inPCB && h.Type == "ATTR" {
			var p struct {
				ParentID string `json:"parentId"`
				Key      string `json:"key"`
				Value    string `json:"value"`
			}
			if json.Unmarshal([]byte(body), &p) == nil && p.Key == "Footprint" {
				compFootprint[p.ParentID] = p.Value
			}
		}
	}
	f, err := os.Open(board)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := loadBoardSnapshotFile(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	for i := range snap.Components {
		snap.Components[i].FootprintUUID = compFootprint[snap.Components[i].ID]
	}
	holes, notes := footprintHolesFromSources(snap.Components, sources)
	for _, n := range notes {
		t.Log(n)
	}
	snap.FootprintHoles = holes
	for _, h := range holes {
		t.Logf("%s %s %s (%.2f,%.2f) Ø%.2f %s", h.Owner, h.SourceID, h.Shape, h.X, h.Y, h.Dia, h.Transform)
	}
	blob, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, blob, 0o644); err != nil {
		t.Fatal(err)
	}
}
