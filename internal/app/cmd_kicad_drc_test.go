package app

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// fakeDRCTools points kicad.Tools at this test binary as kicad-cli; each
// call writes fixture (under internal/kicad/testdata) and exits with code.
func fakeDRCTools(t *testing.T, fixture, code string) *kicad.Tools {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(filepath.Join("..", "kicad", "testdata", fixture))
	t.Setenv("PCBPILOT_FAKE_KICAD_DRC", abs)
	t.Setenv("PCBPILOT_FAKE_EXIT", code)
	return &kicad.Tools{CLI: exe}
}

var uncheckableDRC = []struct{ fixture, code string }{
	{"drc/null-entry.json", "0"},
	{"drc/truncated.json", "0"},
	{"drc/empty.json", "0"},
	{"drc/missing-violations.json", "0"},
	{"drc/wrong-type.json", "0"},
	{"drc/item-empty.json", "0"},
	{"drc/clean.json", "2"}, // kicad-cli failed after writing a clean-looking file
}

func fakeKicadRun(t *testing.T, kt *kicad.Tools) *kicadRun {
	t.Helper()
	dir := t.TempDir()
	return &kicadRun{o: kicadRouteOpts{outDir: dir}, kt: kt, work: dir, summary: map[string]any{}, stderr: io.Discard,
		// A waiver matching anything: it must still not pass a DRC that could not be checked.
		waivers: []gateWaiver{{Gate: "kicad-drc", Match: ":", Reason: "test", By: "t"}, {Gate: "kicad-drc", Match: " ", Reason: "test", By: "t"}}}
}

// The route's own kicad-drc step (routedDRCGate, with the run's waivers):
// a malformed report or a failed kicad-cli is a failing gate, kept by the
// IR closure, never a pass.
func TestKicadRouteDRCGateMalformedReport(t *testing.T) {
	r := fakeKicadRun(t, fakeDRCTools(t, "drc/clean.json", "0"))
	if all, errs := r.routedDRCGate(filepath.Join(r.work, "routed.kicad_pcb")); all == nil || errs == nil || len(r.gates) != 1 || !r.gates[0].Pass {
		t.Fatalf("clean report: %+v", r.gates)
	}
	for _, tc := range uncheckableDRC {
		r := fakeKicadRun(t, fakeDRCTools(t, tc.fixture, tc.code))
		all, errs := r.routedDRCGate(filepath.Join(r.work, "routed.kicad_pcb"))
		if all != nil || errs != nil || len(r.gates) != 1 {
			t.Fatalf("%s (exit %s): reports %v %v gates %+v", tc.fixture, tc.code, all, errs, r.gates)
		}
		g := r.gates[0]
		if g.Gate != "kicad-drc" || g.Pass || !strings.Contains(g.Detail, "cannot check") {
			t.Errorf("%s (exit %s): gate %+v", tc.fixture, tc.code, g)
		}
		if len(r.closureGates()) != 1 {
			t.Errorf("%s (exit %s): IR closure dropped the cannot-check gate", tc.fixture, tc.code)
		}
	}
}

// The other kicad-cli DRC consumers: the candidate quick check, the
// starved-thermal pass and the input-board baseline.
func TestKicadRouteDRCConsumersMalformedReport(t *testing.T) {
	for _, tc := range uncheckableDRC {
		r := fakeKicadRun(t, fakeDRCTools(t, tc.fixture, tc.code))
		c := &routeCandidate{Name: "x", Board: filepath.Join(r.work, "x.kicad_pcb"), Res: &routeResult{}}
		r.quickCheckCandidate(c)
		if c.Quick || !strings.Contains(c.QuickNote, "DRC did not run") {
			t.Errorf("%s (exit %s): quick check %v %q", tc.fixture, tc.code, c.Quick, c.QuickNote)
		}
		if _, err := r.starvedThermals(c.Board); err == nil {
			t.Errorf("%s (exit %s): starved-thermal pass ran on an unreadable report", tc.fixture, tc.code)
		}
		if _, err := r.kt.DRC(c.Board, filepath.Join(r.work, "input-drc.json")); err == nil {
			t.Errorf("%s (exit %s): input DRC read", tc.fixture, tc.code)
		}
	}
}
