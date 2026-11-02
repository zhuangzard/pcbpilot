package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
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

// Any named illegal placement fails, also when the overlap metric is 0:
// a part outside the board only, or outside its zone only.
func TestKicadPlaceVerdictUsesTheNamedList(t *testing.T) {
	for _, c := range []struct {
		illegal []string
		metric  int
		want    gate.Status
		n       int
	}{
		{[]string{"outside the board R3"}, 0, gate.Fail, 1},
		{[]string{"outside its zone U2"}, 0, gate.Fail, 1},
		{[]string{"overlap J1 J2 (40 mil²)", "outside its zone U2"}, 1, gate.Fail, 2},
		{nil, 0, gate.Pass, 0},
	} {
		if v, n := kicadPlaceVerdict(c.illegal, c.metric); v != c.want || n != c.n {
			t.Errorf("%v / %d: verdict %q (%d), want %q (%d)", c.illegal, c.metric, v, n, c.want, c.n)
		}
	}
}

// The input's .kicad_dru (custom rules) and .kicad_pro travel to every
// written board; a stale .kicad_dru at the output goes when the input has
// none.
func TestKicadPlaceCopiesTheRules(t *testing.T) {
	dir := t.TempDir()
	pcb := filepath.Join(dir, "in.kicad_pcb")
	dru := "(version 1)\n(rule pcbpilot-iso (constraint clearance (min 2mm)))\n"
	os.WriteFile(filepath.Join(dir, "in.kicad_pro"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(dir, "in.kicad_dru"), []byte(dru), 0o644)
	out := filepath.Join(dir, "out", "b.kicad_pcb")
	deps := kicadPlaceDeps{
		snapshot: func(string) ([]byte, error) { return tinyKicadSnapshot(), nil },
		place: func(string, map[string]kicad.Pose, string) (*kicad.PlaceResult, error) {
			return &kicad.PlaceResult{}, nil
		},
	}
	rep, err := runKicadPlace(kicadPlaceOpts{pcb: pcb, out: out, placeSeeds: 2, candidates: 2, seed: 1}, deps, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range rep.Seeds {
		if s.File == "" {
			continue
		}
		got, err := os.ReadFile(strings.TrimSuffix(s.File, ".kicad_pcb") + ".kicad_dru")
		if err != nil || string(got) != dru {
			t.Fatalf("%s: .kicad_dru %q (%v), want the input's rules", s.File, got, err)
		}
		if _, err := os.Stat(strings.TrimSuffix(s.File, ".kicad_pcb") + ".kicad_pro"); err != nil {
			t.Fatalf("%s: no .kicad_pro", s.File)
		}
	}
	os.Remove(filepath.Join(dir, "in.kicad_dru"))
	if _, err := runKicadPlace(kicadPlaceOpts{pcb: pcb, out: out, placeSeeds: 1, candidates: 1, seed: 1}, deps, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "b.kicad_dru")); !os.IsNotExist(err) {
		t.Fatal("a stale .kicad_dru stayed beside the output")
	}
}

// --mech coordinates are the KiCad board's: a spec of the board's size
// keeps the outline and every pose lands on the board; another size is
// refused, not moved to 0,0.
func TestKicadPlaceMechKeepsTheKicadOutline(t *testing.T) {
	dir := t.TempDir()
	var poses map[string]kicad.Pose
	deps := kicadPlaceDeps{
		snapshot: func(string) ([]byte, error) { return tinyKicadSnapshot(), nil },
		place: func(_ string, p map[string]kicad.Pose, _ string) (*kicad.PlaceResult, error) {
			poses = p
			return &kicad.PlaceResult{}, nil
		},
	}
	mech := filepath.Join(dir, "mech.json")
	os.WriteFile(mech, []byte(`{"units":"mil","board":{"width":2000,"height":1600}}`), 0o644)
	o := kicadPlaceOpts{pcb: filepath.Join(dir, "in.kicad_pcb"), out: filepath.Join(dir, "out", "b.kicad_pcb"), mech: mech, placeSeeds: 1, candidates: 1, seed: 1}
	if _, err := runKicadPlace(o, deps, io.Discard); err != nil {
		t.Fatal(err)
	}
	for ref, p := range poses {
		// tinyKicadSnapshot's board: x 0..2000, KiCad y 0..1600.
		if p.XMil < 0 || p.XMil > 2000 || p.YMil < 0 || p.YMil > 1600 {
			t.Fatalf("%s placed at %.0f,%.0f, off the KiCad board", ref, p.XMil, p.YMil)
		}
	}
	os.WriteFile(mech, []byte(`{"units":"mil","board":{"width":2600,"height":1600}}`), 0o644)
	if _, err := runKicadPlace(o, deps, io.Discard); err == nil || !strings.Contains(err.Error(), "replaceOutline") {
		t.Fatalf("a mech board of another size was accepted: %v", err)
	}
}

// kicad place never replaces the outline it writes unchanged: a 50 x 50 mm
// board with a 100 x 100 mm --mech is refused, with or without
// replaceOutline, and points at kicad board outline.
func TestKicadPlaceRefusesAnotherOutline(t *testing.T) {
	var snap map[string]any
	if err := json.Unmarshal(tinyKicadSnapshot(), &snap); err != nil {
		t.Fatal(err)
	}
	s := 50 / 0.0254
	snap["outline"] = map[string]any{"bbox": map[string]any{"minX": 0, "minY": -s, "maxX": s, "maxY": 0},
		"points": [][2]float64{{0, 0}, {s, 0}, {s, -s}, {0, -s}}, "source": "polygon"}
	raw, _ := json.Marshal(snap)
	dir := t.TempDir()
	deps := kicadPlaceDeps{
		snapshot: func(string) ([]byte, error) { return raw, nil },
		place: func(string, map[string]kicad.Pose, string) (*kicad.PlaceResult, error) {
			t.Fatal("a board was written")
			return nil, nil
		},
	}
	mech := filepath.Join(dir, "mech.json")
	for _, js := range []string{`{"board":{"width":100,"height":100}}`, `{"board":{"width":100,"height":100,"replaceOutline":true}}`} {
		os.WriteFile(mech, []byte(js), 0o644)
		_, err := runKicadPlace(kicadPlaceOpts{pcb: filepath.Join(dir, "in.kicad_pcb"), out: filepath.Join(dir, "out", "b.kicad_pcb"), mech: mech, placeSeeds: 1, candidates: 1, seed: 1}, deps, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "kicad board outline") {
			t.Fatalf("%s: %v, want a refusal pointing at kicad board outline", js, err)
		}
	}
}

// A destination .kicad_pro stays when the source has none (only a stale
// .kicad_dru goes), and a stale .kicad_dru that cannot be removed is an
// error, not a silent leftover rule file.
func TestCopyKicadSidecarsKeepsProAndReportsRemoval(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a", "in.kicad_pcb"), filepath.Join(dir, "b", "out.kicad_pcb")
	os.MkdirAll(filepath.Dir(src), 0o755)
	os.MkdirAll(filepath.Dir(dst), 0o755)
	os.WriteFile(filepath.Join(dir, "b", "out.kicad_pro"), []byte(`{"keep":1}`), 0o644)
	os.WriteFile(filepath.Join(dir, "b", "out.kicad_dru"), []byte("(version 1)"), 0o644)
	hasPro, err := copyKicadSidecars(src, dst)
	if err != nil || !hasPro {
		t.Fatalf("hasPro %v err %v", hasPro, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "b", "out.kicad_pro")); string(got) != `{"keep":1}` {
		t.Fatalf("destination .kicad_pro %q, want it kept", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "b", "out.kicad_dru")); !os.IsNotExist(err) {
		t.Fatal("stale .kicad_dru kept")
	}
	// A stale "rules file" that cannot be removed (here a non-empty
	// directory, on every OS) fails the copy.
	stuck := filepath.Join(dir, "b", "out.kicad_dru")
	os.MkdirAll(filepath.Join(stuck, "x"), 0o755)
	if _, err := copyKicadSidecars(src, dst); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("unremovable stale .kicad_dru: %v, want an error", err)
	}
}
