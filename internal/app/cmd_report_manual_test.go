package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckNoManualNeedsWaiver(t *testing.T) {
	if err := checkNoManual(false, nil); err != nil {
		t.Fatal(err)
	}
	if err := checkNoManual(true, nil); err == nil {
		t.Fatal("--no-manual accepted without a waiver")
	}
	ws := []gateWaiver{{Gate: "board-manual", Match: "--no-manual", Reason: "bench board", By: "tz"}}
	if err := checkNoManual(true, ws); err != nil {
		t.Fatal(err)
	}
	g, run := runManualGate(manualGateOpts{noManual: true, waivers: ws}, io.Discard)
	if !g.Pass || run != nil || len(g.Waived) != 1 {
		t.Errorf("waived skip %+v", g)
	}
}

const tinyBoard = `{"components":[{"designator":"J1","device":"DC-005","x":50,"y":50,
 "pads":[{"padNumber":"1","net":"VIN","x":40,"y":50,"width":20,"height":20},{"padNumber":"2","net":"GND","x":60,"y":50,"width":20,"height":20}]}],
 "outline":{"points":[[0,0],[400,0],[400,300],[0,300]]},"copperLayers":2,"semanticSha256":"s1"}`

func TestManualGateMissingNotes(t *testing.T) {
	dir := t.TempDir()
	board := filepath.Join(dir, "board.json")
	if err := os.WriteFile(board, []byte(tinyBoard), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := `{"schemaVersion":1,"name":"Tiny","manual":{"out":"out/Tiny.html"}}`
	if err := os.WriteFile(filepath.Join(dir, "pcbpilot.project.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	g, run := runManualGate(manualGateOpts{board: board, projectConfig: dir, outDir: filepath.Join(dir, "gate"), date: "2026-10-06T00:00:00Z"}, io.Discard)
	if g.Pass || len(g.Items) == 0 || !strings.Contains(g.Items[0], "notes file missing") || !strings.Contains(g.Items[0], "pcbpilot.manual-notes.json") {
		t.Fatalf("gate %+v", g)
	}
	if run == nil || run.Version != 1 {
		t.Fatalf("run %+v", run)
	}
	for _, f := range []string{"gate/manual/Tiny_使用说明.html", "gate/manual/v1/Tiny_使用说明.html", "gate/manual/index.json", "out/Tiny.html"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Error(err)
		}
	}
	// unreadable board → generation failure item, no panic
	g, _ = runManualGate(manualGateOpts{board: filepath.Join(dir, "nope.json"), projectConfig: "none", outDir: dir}, io.Discard)
	if g.Pass || !strings.Contains(strings.Join(g.Items, " "), "generation failed") {
		t.Errorf("bad board %+v", g)
	}
}
