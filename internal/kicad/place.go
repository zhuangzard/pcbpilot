// Package kicad drives KiCad's pcbnew python for pcbpilot.
package kicad

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

//go:embed place.py
var placePy []byte

// Pose is a footprint pose in KiCad's frame: mil, y down, orientation in
// degrees (CCW as drawn), side top|bottom.
type Pose struct {
	XMil        float64 `json:"xMil"`
	YMil        float64 `json:"yMil"`
	RotationDeg float64 `json:"rotationDeg"`
	Side        string  `json:"side"`
}

// PlaceResult is place.py's summary. Missing stays for older readers: a
// key that names no footprint now rejects the run (*PlaceError), so a
// written board always has it empty.
type PlaceResult struct {
	Moved         int      `json:"moved"`
	Flipped       int      `json:"flipped"`
	RemovedTracks int      `json:"removedTracks"`
	Missing       []string `json:"missing,omitempty"`
	Locked        []string `json:"locked,omitempty"`
}

// PoseFootprint names one footprint for a pose by exactly one typed selector:
// Ref matches designators only, UUID footprint uuids only (designators are
// free text, so no prefix convention could tell them apart).
type PoseFootprint struct {
	Ref  string `json:"ref,omitempty"`
	UUID string `json:"uuid,omitempty"`
}

func (f PoseFootprint) String() string {
	if f.UUID != "" {
		return "uuid " + f.UUID
	}
	return "ref " + f.Ref
}

// PoseTarget is one record of the poses file: a footprint and its pose.
type PoseTarget struct {
	PoseFootprint
	Pose
}

// PlaceAmbiguity is a selector that matches several footprints.
type PlaceAmbiguity struct {
	PoseFootprint
	Candidates []string `json:"candidates"` // footprint uuids
}

// PlacePosedTwice is one footprint that several records address.
type PlacePosedTwice struct {
	UUID      string          `json:"uuid"`
	Selectors []PoseFootprint `json:"selectors"`
}

// PlaceError is a rejected placement: outPath was not written. Invalid
// lists malformed records; Missing selectors name no footprint; Ambiguous
// ones several (pose one of them by UUID); PosedTwice footprints have more
// than one record.
type PlaceError struct {
	Msg        string            `json:"error"`
	Invalid    []string          `json:"invalid,omitempty"`
	Missing    []PoseFootprint   `json:"missing,omitempty"`
	Ambiguous  []PlaceAmbiguity  `json:"ambiguous,omitempty"`
	PosedTwice []PlacePosedTwice `json:"posedTwice,omitempty"`
}

func (e *PlaceError) Error() string { return "kicad place: " + e.Msg }

// Place writes outPath: pcbPath with every footprint in poses (keyed by
// designator only) moved, and flipped on a side change. See PlaceTargets.
func Place(pcbPath string, poses map[string]Pose, outPath string) (*PlaceResult, error) {
	targets := make([]PoseTarget, 0, len(poses))
	for _, ref := range slices.Sorted(maps.Keys(poses)) {
		targets = append(targets, PoseTarget{PoseFootprint: PoseFootprint{Ref: ref}, Pose: poses[ref]})
	}
	return PlaceTargets(pcbPath, targets, outPath)
}

// PlaceTargets writes outPath: pcbPath with every target footprint moved,
// and flipped on a side change. Locked footprints stay; tracks and vias
// are removed and copper zones unfilled (the placement invalidates them).
// A malformed record, a selector naming no footprint or several, or two
// records for one footprint reject the whole run (*PlaceError) and write
// nothing.
func PlaceTargets(pcbPath string, targets []PoseTarget, outPath string) (*PlaceResult, error) {
	dir, err := os.MkdirTemp("", "pcbpilot-kicad-place")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if targets == nil {
		targets = []PoseTarget{} // "poses": [] — null is not a record list
	}
	blob, err := json.Marshal(map[string]any{"pcbpilotPoses": 2, "poses": targets})
	if err != nil {
		return nil, err
	}
	pf := filepath.Join(dir, "poses.json")
	if err := os.WriteFile(pf, blob, 0o644); err != nil {
		return nil, err
	}
	out, runErr := runPlacePython(placePy, "place.py", "place", pcbPath, pf, outPath)
	line := ""
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, "PCBPILOT_RESULT "); ok {
			line = strings.TrimSpace(rest)
		}
	}
	var head struct {
		OK *bool `json:"ok"`
	}
	if line != "" && json.Unmarshal([]byte(line), &head) == nil && head.OK != nil && !*head.OK {
		var pe PlaceError
		if err := json.Unmarshal([]byte(line), &pe); err != nil {
			return nil, fmt.Errorf("place.py output: %w", err)
		}
		return nil, &pe
	}
	if runErr != nil {
		return nil, runErr
	}
	var r PlaceResult
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		return nil, fmt.Errorf("place.py output: %w", err)
	}
	return &r, nil
}

// placePythonBin is KiCad's bundled interpreter: $PCBPILOT_KICAD_PYTHON,
// else the newest Python.framework version in the macOS app bundle (the
// version changes across KiCad releases).
func placePythonBin() string {
	if p := os.Getenv("PCBPILOT_KICAD_PYTHON"); p != "" {
		return p
	}
	m, _ := filepath.Glob("/Applications/KiCad/KiCad.app/Contents/Frameworks/Python.framework/Versions/*/bin/python3")
	if len(m) == 0 {
		return "python3" // fails clearly unless pcbnew is importable
	}
	slices.SortFunc(m, func(a, b string) int { return compareVersionDirs(a, b) })
	return m[len(m)-1]
}

// compareVersionDirs orders .../Versions/X.Y/bin/python3 paths by X.Y
// numerically (3.10 after 3.9).
func compareVersionDirs(a, b string) int {
	va := strings.Split(filepath.Base(filepath.Dir(filepath.Dir(a))), ".")
	vb := strings.Split(filepath.Base(filepath.Dir(filepath.Dir(b))), ".")
	for i := 0; i < len(va) && i < len(vb); i++ {
		x, _ := strconv.Atoi(va[i])
		y, _ := strconv.Atoi(vb[i])
		if x != y {
			return x - y
		}
	}
	return len(va) - len(vb)
}

// runPlacePython runs an embedded script with KiCad's python and returns
// stdout, also on failure (a rejecting script explains itself there).
// pcbnew's wx asserts on stderr are noise unless the script fails.
func runPlacePython(script []byte, name string, args ...string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "pcbpilot-kicad")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, script, 0o644); err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(placePythonBin(), append([]string{path}, args...)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
