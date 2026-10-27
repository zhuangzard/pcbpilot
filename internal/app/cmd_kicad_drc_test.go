package app

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// fakeKiCadDRC is `kicad-cli pcb drc … -o OUT`: it copies
// $PCBPILOT_FAKE_KICAD_DRC to OUT and exits with $PCBPILOT_FAKE_EXIT.
func fakeKiCadDRC(args []string) int {
	out := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-o" {
			out = args[i+1]
		}
	}
	b, err := os.ReadFile(os.Getenv("PCBPILOT_FAKE_KICAD_DRC"))
	if err != nil || out == "" || os.WriteFile(out, b, 0o644) != nil {
		return 1
	}
	code, _ := strconv.Atoi(os.Getenv("PCBPILOT_FAKE_EXIT"))
	return code
}

// The route's kicad-drc step (DRCSeverity, then kicadDRCCannotCheck on an
// error): a malformed report or a failed kicad-cli never passes the gate,
// not even with a waiver that matches everything.
func TestKicadRouteDRCGateMalformedReport(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	kt := &kicad.Tools{CLI: exe}
	dir := t.TempDir()
	run := func(fixture, code string) (*kicad.DRCReport, error) {
		abs, _ := filepath.Abs(filepath.Join("..", "kicad", "testdata", fixture))
		t.Setenv("PCBPILOT_FAKE_KICAD_DRC", abs)
		t.Setenv("PCBPILOT_FAKE_EXIT", code)
		return kt.DRCSeverity(filepath.Join(dir, "routed.kicad_pcb"), filepath.Join(dir, "drc-all.json"), "all")
	}
	rep, err := run("drc/clean.json", "0")
	if err != nil {
		t.Fatal(err)
	}
	if errs, _, _ := rep.SplitErrors(); !kicadDRCGate(errs, "drc.json").Pass {
		t.Fatal("clean report must pass")
	}
	for _, tc := range []struct{ fixture, code string }{
		{"drc/null-entry.json", "0"},
		{"drc/truncated.json", "0"},
		{"drc/empty.json", "0"},
		{"drc/missing-violations.json", "0"},
		{"drc/wrong-type.json", "0"},
		{"drc/clean.json", "2"}, // kicad-cli failed after writing a clean-looking file
	} {
		rep, err := run(tc.fixture, tc.code)
		if err == nil {
			t.Errorf("%s (exit %s): read as a report %+v", tc.fixture, tc.code, rep)
			continue
		}
		g := kicadDRCCannotCheck(err)
		applyWaivers(&g, []gateWaiver{{Gate: "kicad-drc", Match: ":", Reason: "test", By: "t"}})
		if g.Pass || g.Gate != "kicad-drc" {
			t.Errorf("%s (exit %s): gate %+v", tc.fixture, tc.code, g)
		}
	}
}
