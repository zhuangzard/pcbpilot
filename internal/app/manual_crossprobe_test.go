package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func TestManualCrossProbeFlagsGoTogether(t *testing.T) {
	dir := t.TempDir()
	board := filepath.Join(dir, "board.json")
	if err := os.WriteFile(board, []byte(tinyBoard), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runBoardManual(boardManualOpts{board: board, out: filepath.Join(dir, "m.html"), lang: "zh", kicadSch: "x.kicad_sch"})
	if err == nil || !strings.Contains(err.Error(), "--kicad-sch and --kicad-pcb go together") {
		t.Fatalf("err = %v", err)
	}
	if kicadLensPCB("", "routed.kicad_pcb") != "" || kicadLensPCB("a.kicad_sch", "routed.kicad_pcb") != "routed.kicad_pcb" {
		t.Error("kicadLensPCB")
	}
}

// TestManualCrossProbeKiCad builds the lens of the fab fixture end to end
// (KiCad snapshot + kicad-cli plots): its schematic holds R1 (root) and U1
// (sub-sheet) only, so the gate names every netted footprint without a
// symbol and leaves the mounting hole H1 (exclude_from_bom) alone.
func TestManualCrossProbeKiCad(t *testing.T) {
	if _, err := kicad.Locate(); err != nil {
		t.Skip(err)
	}
	// kicad-cli writes a .kicad_prl next to the board: work on a copy
	fab := t.TempDir() + "/"
	for _, f := range []string{"fab_root.kicad_sch", "fab_sub.kicad_sch", "fab_board.kicad_pcb"} {
		b, err := os.ReadFile("../kicad/testdata/fab/" + f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fab+f, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := kicad.Snapshot(fab + "fab_board.kicad_pcb")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	board := filepath.Join(dir, "board.json")
	if err := os.WriteFile(board, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	g, run := runManualGate(manualGateOpts{board: board, projectConfig: "none", outDir: dir, date: "2026-10-09T00:00:00Z",
		kicadSch: fab + "fab_root.kicad_sch", kicadPcb: fab + "fab_board.kicad_pcb"}, io.Discard)
	if run == nil {
		t.Fatalf("manual not generated: %+v", g)
	}
	items := strings.Join(g.Items, "\n")
	for _, ref := range []string{"D1", "J1", "Q1", "R2", "R3"} {
		if !strings.Contains(items, "cross-probe: "+ref+" is on the PCB but has no schematic symbol") {
			t.Errorf("gate does not name %s:\n%s", ref, items)
		}
	}
	for _, ref := range []string{"R1 ", "U1 ", "H1 "} {
		if strings.Contains(items, "cross-probe: "+ref) {
			t.Errorf("gate names %s:\n%s", ref, items)
		}
	}
	html, err := os.ReadFile(run.Current)
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	for _, w := range []string{`id="xp-lens"`, `"ref":"U1"`, `"schPage":1`, `"name":"Sub"`, `"name":"F.Silkscreen"`, "data:image/svg+xml;base64,"} {
		if !strings.Contains(s, w) {
			t.Errorf("manual lacks %s", w)
		}
	}
}
