package app

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// --sides flips the named parts on a staged copy first, places that copy,
// and removes it; parts already on their side are left out of the flip.
func TestRunKicadPlaceSidesFlipsBeforePlacing(t *testing.T) {
	dir := t.TempDir()
	pcb := filepath.Join(dir, "in.kicad_pcb")
	sides := filepath.Join(dir, "sides.json")
	if err := os.WriteFile(sides, []byte(`{"bottom": ["C1", "R3"], "top": ["R1"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out", "board.kicad_pcb")
	// The staged copy: board.kicad_pcb in a directory made for the run.
	isStaged := func(p string) bool {
		return filepath.Base(p) == "board.kicad_pcb" && strings.HasPrefix(filepath.Base(filepath.Dir(p)), "board.sides-staged-")
	}
	staged := ""
	// A file of the user's under the old fixed staging name is left alone.
	old := filepath.Join(dir, "out", "board.sides-staged.kicad_pcb")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The staged copy's snapshot: C1 now on the bottom.
	flipped := func() []byte {
		var snap map[string]any
		if err := json.Unmarshal(tinyKicadSnapshot(), &snap); err != nil {
			t.Fatal(err)
		}
		for _, c := range snap["components"].([]any) {
			m := c.(map[string]any)
			if m["designator"] == "C1" {
				m["layer"] = 2
				for _, p := range m["pads"].([]any) {
					p.(map[string]any)["layer"] = 2
				}
			}
		}
		raw, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	var calls []string
	var flips, final map[string]kicad.Pose
	deps := kicadPlaceDeps{
		snapshot: func(p string) ([]byte, error) {
			calls = append(calls, "snapshot "+filepath.Base(p))
			if isStaged(p) {
				return flipped(), nil
			}
			return tinyKicadSnapshot(), nil
		},
		place: func(src string, poses map[string]kicad.Pose, dst string) (*kicad.PlaceResult, error) {
			if isStaged(dst) {
				calls = append(calls, "place "+filepath.Base(src)+" -> staged")
				staged = dst
				flips = poses
				if err := os.WriteFile(staged, []byte("(kicad_pcb)"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else if dst == out {
				calls = append(calls, "place staged -> "+filepath.Base(dst))
				final = poses
				if src != staged {
					t.Fatalf("the placement was written from %s, not from the flipped copy", src)
				}
			}
			return &kicad.PlaceResult{Moved: len(poses)}, nil
		},
	}
	rep, err := runKicadPlace(kicadPlaceOpts{pcb: pcb, out: out, sides: sides, placeSeeds: 1, candidates: 1, seed: 1}, deps, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(flips) != 1 {
		t.Fatalf("flipped %v, want C1 alone (R3 is on the bottom, R1 on the top already)", flips)
	}
	if c := flips["C1"]; c.Side != "bottom" || c.XMil != 400 || c.YMil != 300 || c.RotationDeg != 0 {
		t.Fatalf("C1 flipped with pose %+v, want where it stands, bottom", c)
	}
	if final["C1"].Side != "bottom" || final["R3"].Side != "bottom" || final["R1"].Side != "top" {
		t.Fatalf("sides written: C1 %q R3 %q R1 %q", final["C1"].Side, final["R3"].Side, final["R1"].Side)
	}
	if want := "snapshot in.kicad_pcb|place in.kicad_pcb -> staged|snapshot board.kicad_pcb"; !strings.HasPrefix(strings.Join(calls, "|"), want) {
		t.Fatalf("calls %v", calls)
	}
	if _, err := os.Stat(filepath.Dir(staged)); staged == "" || !os.IsNotExist(err) {
		t.Fatal("the staged copy's directory was left behind")
	}
	if got, err := os.ReadFile(old); err != nil || string(got) != "mine" {
		t.Fatalf("a file named like the old staging copy was touched: %q, %v", got, err)
	}
	if rep.PCB != pcb {
		t.Fatalf("report names %s as the input", rep.PCB)
	}
	if n := strings.Join(rep.Notes, "\n"); !strings.Contains(n, "--sides: 1 part(s) flipped before the placement (1 to the bottom, 0 to the top); 2 named part(s) were already on their side") {
		t.Fatalf("notes: %s", n)
	}
}

// A part that cannot take the side asked — locked, unknown, named for both
// sides — refuses the run before anything is written.
func TestRunKicadPlaceSidesRefusesWhatCannotFlip(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want, mech string }{
		"locked":  {`{"bottom": ["J1"]}`, "J1 (locked or a mounting hole", ""},
		"unknown": {`{"bottom": ["Q99"]}`, "Q99 (no single footprint", ""},
		"both":    {`{"bottom": ["C1"], "top": ["C1"]}`, "C1 (named for both sides)", ""},
		"key":     {`{"back": ["C1"]}`, `key "back" is not "top" or "bottom"`, ""},
		"shape":   {`["C1"]`, `want {"top": [designators], "bottom": [designators]}`, ""},
		"mech":    {`{"bottom": ["C1"]}`, "C1 (fixed by --mech", `{"fixed": [{"ref": "C1", "x": 5, "y": 5, "rot": 0}]}`},
	} {
		dir := t.TempDir()
		mech := ""
		if tc.mech != "" {
			mech = filepath.Join(dir, "mech.json")
			if err := os.WriteFile(mech, []byte(tc.mech), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		sides := filepath.Join(dir, "sides.json")
		if err := os.WriteFile(sides, []byte(tc.doc), 0o644); err != nil {
			t.Fatal(err)
		}
		deps := kicadPlaceDeps{
			snapshot: func(string) ([]byte, error) { return tinyKicadSnapshot(), nil },
			place: func(string, map[string]kicad.Pose, string) (*kicad.PlaceResult, error) {
				t.Fatalf("%s: a board was written", name)
				return nil, nil
			},
		}
		_, err := runKicadPlace(kicadPlaceOpts{pcb: filepath.Join(dir, "in.kicad_pcb"), out: filepath.Join(dir, "o.kicad_pcb"), sides: sides, mech: mech, placeSeeds: 1, seed: 1}, deps, io.Discard)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %v, want %q", name, err, tc.want)
		}
	}
}
