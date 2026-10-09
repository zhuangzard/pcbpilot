// Package kicad drives KiCad's pcbnew python for pcbpilot.
package kicad

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
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

// PlaceResult is place.py's summary.
type PlaceResult struct {
	Moved         int      `json:"moved"`
	Flipped       int      `json:"flipped"`
	RemovedTracks int      `json:"removedTracks"`
	Missing       []string `json:"missing,omitempty"`
	Locked        []string `json:"locked,omitempty"`
}

// Place writes outPath: pcbPath with every designator in poses moved (and
// flipped on a side change). Locked footprints stay; tracks and vias are
// removed and copper zones unfilled (the placement invalidates them).
func Place(pcbPath string, poses map[string]Pose, outPath string) (*PlaceResult, error) {
	dir, err := os.MkdirTemp("", "pcbpilot-kicad-place")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	blob, _ := json.Marshal(poses)
	pf := filepath.Join(dir, "poses.json")
	if err := os.WriteFile(pf, blob, 0o644); err != nil {
		return nil, err
	}
	out, err := runPlacePython(placePy, "place.py", "place", pcbPath, pf, outPath)
	if err != nil {
		return nil, err
	}
	var r PlaceResult
	line := ""
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, "PCBPILOT_RESULT "); ok {
			line = rest
		}
	}
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

// runPlacePython runs an embedded script with KiCad's python and returns stdout.
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
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
