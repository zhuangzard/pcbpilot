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
	cfg := `{"schemaVersion":1,"name":"Tiny","manual":{"out":"out/Tiny.html","doc":"d1"}}`
	if err := os.WriteFile(filepath.Join(dir, "pcbpilot.project.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	g, run := runManualGate(manualGateOpts{board: board, projectConfig: dir, outDir: filepath.Join(dir, "gate"), doc: "d1", date: "2026-10-06T00:00:00Z"}, io.Discard)
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

// Gas Module V5: B's run with --project-config pcbpilot.project.B.json loaded
// ./pcbpilot.project.json (board A) and overwrote A's published manual. A
// config file is loaded itself, and manual.out is written only for the
// document the config names.
func TestManualGateProjectConfigFileAndDocGuard(t *testing.T) {
	dir := t.TempDir()
	board := filepath.Join(dir, "board.json")
	if err := os.WriteFile(board, []byte(tinyBoard), 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pcbpilot.project.json", `{"schemaVersion":1,"name":"BoardA","manual":{"out":"out/A.html","doc":"docA"}}`)
	write("pcbpilot.project.B.json", `{"schemaVersion":1,"name":"BoardB","manual":{"out":"out/B.html","doc":"docB"}}`)
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("out/A.html", "A manual")

	_, run := runManualGate(manualGateOpts{board: board, projectConfig: filepath.Join(dir, "pcbpilot.project.B.json"), outDir: filepath.Join(dir, "gateB"), doc: "docB"}, io.Discard)
	if run == nil || !strings.HasSuffix(run.Current, "BoardB_使用说明.html") || run.Extra != filepath.Join(dir, "out/B.html") {
		t.Fatalf("run %+v", run)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "out/A.html")); string(b) != "A manual" {
		t.Fatalf("A's manual changed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "out/B.html")); err != nil {
		t.Fatal(err)
	}
	// B's board run against A's config: A's copy is refused.
	g, run := runManualGate(manualGateOpts{board: board, projectConfig: dir, outDir: filepath.Join(dir, "gateX"), doc: "docB"}, io.Discard)
	if run == nil || run.Extra != "" || !strings.Contains(strings.Join(g.Items, "\n"), "manual.out") || g.Pass {
		t.Fatalf("doc guard: %+v %+v", g, run)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "out/A.html")); string(b) != "A manual" {
		t.Fatalf("A's manual overwritten: %q", b)
	}
	for _, c := range []struct{ cfg, board, want string }{{"", "d", "manual.doc is empty"}, {"d", "", "unknown"}, {"a", "b", "for document a"}, {"D1", "d1", ""}} {
		if got := manualOutRefusal(c.cfg, c.board); (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("manualOutRefusal(%q,%q) = %q", c.cfg, c.board, got)
		}
	}
}
