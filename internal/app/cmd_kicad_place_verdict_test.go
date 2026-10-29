package app

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func placeTiny(t *testing.T, snap []byte) (*kicadPlaceReport, string) {
	t.Helper()
	dir := t.TempDir()
	var stderr bytes.Buffer
	rep, err := runKicadPlace(kicadPlaceOpts{pcb: filepath.Join(dir, "in.kicad_pcb"), out: filepath.Join(dir, "out", "b.kicad_pcb"), placeSeeds: 2, candidates: 1, seed: 1},
		kicadPlaceDeps{
			snapshot: func(string) ([]byte, error) { return snap, nil },
			place: func(string, map[string]kicad.Pose, string) (*kicad.PlaceResult, error) {
				return &kicad.PlaceResult{}, nil
			},
		}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	return rep, stderr.String()
}

// A legal placement passes, with every zone's room in the report.
func TestKicadPlaceVerdictPass(t *testing.T) {
	rep, _ := placeTiny(t, tinyKicadSnapshot())
	if rep.Verdict != gate.Pass || rep.Verdict.ExitCode() != 0 || len(rep.Illegal) != 0 {
		t.Fatalf("verdict %q illegal %v, want pass", rep.Verdict, rep.Illegal)
	}
	if len(rep.ZoneFill) == 0 || rep.ZoneFill[0].Domain != "board" || rep.ZoneFill[0].OfferMm2 <= 0 || rep.ZoneFill[0].NeedMm2 <= 0 {
		t.Fatalf("zoneFill %+v", rep.ZoneFill)
	}
}

// Two locked footprints on top of each other cannot be legalised: the run
// fails (exit 6, like kicad drc), names the pair and warns on stderr.
func TestKicadPlaceVerdictFailNamesTheOverlap(t *testing.T) {
	var snap map[string]any
	if err := json.Unmarshal(tinyKicadSnapshot(), &snap); err != nil {
		t.Fatal(err)
	}
	comps := snap["components"].([]any)
	var j1 map[string]any
	for _, c := range comps {
		if m := c.(map[string]any); m["designator"] == "J1" {
			j1 = m
		}
	}
	raw, _ := json.Marshal(j1)
	var j2 map[string]any
	json.Unmarshal(raw, &j2)
	j2["designator"], j2["primitiveId"], j2["device"] = "J2", "id-J2", "J2"
	pads := j2["pads"].([]any)
	for _, p := range pads {
		p.(map[string]any)["net"] = ""
	}
	snap["components"] = append(comps, j2)
	blob, _ := json.Marshal(snap)
	rep, stderr := placeTiny(t, blob)
	if rep.Verdict != gate.Fail || rep.Verdict.ExitCode() != 6 {
		t.Fatalf("verdict %q, want fail (exit 6)", rep.Verdict)
	}
	if len(rep.Illegal) == 0 || !strings.Contains(strings.Join(rep.Illegal, ";"), "overlap J1 J2") {
		t.Fatalf("illegal %v, want the J1/J2 overlap", rep.Illegal)
	}
	if !strings.Contains(stderr, "WARNING best seed") || !strings.Contains(stderr, "overlap J1 J2") {
		t.Fatalf("stderr lacks the warning:\n%s", stderr)
	}
}
