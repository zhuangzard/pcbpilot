package app

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// esp32-v05/board.json is a pre-layout snapshot with no board outline. A
// route-only run (no --place) with its mech spec used to put the autoSize
// M3 corner holes on the corners of the part envelope — MH3 over SW1.1 —
// and write them into the playbook; now it refuses and says why.
func TestRouteOnlyMechHoleOnPartRefused(t *testing.T) {
	d := "testdata/esp32-v05"
	var stdout, stderr bytes.Buffer
	root := newRootCmd(&stdout, &stderr)
	root.SetArgs([]string{"--host", "127.0.0.1", "--ports", "9-9", "pcb", "auto", "run",
		"--board", filepath.Join(d, "board.json"), "--mech", filepath.Join(d, "mech.json"),
		"--layers", "4", "--seed", "1", "--no-feedback", "--timeout", "5s", "--out-dir", t.TempDir()})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "SW1.") || !strings.Contains(err.Error(), "--place") {
		t.Fatalf("want a route-only mech-hole refusal naming SW1, got %v", err)
	}
}

// Without an outline the routing grid is the part envelope + 2 cells; the
// via claim disk (ceil rounding) reached one cell past the ring nodeOK
// checked and indexed off the grid (panic in nodeCong). Route-only without
// mech must now complete.
func TestRouteOnlyNoOutlineNoPanic(t *testing.T) {
	if testing.Short() {
		t.Skip("routes a board")
	}
	d := "testdata/esp32-v05"
	var stdout, stderr bytes.Buffer
	root := newRootCmd(&stdout, &stderr)
	root.SetArgs([]string{"--host", "127.0.0.1", "--ports", "9-9", "pcb", "auto", "run",
		"--board", filepath.Join(d, "board.json"),
		"--intent", filepath.Join(d, "intent.json"), "--sim", filepath.Join(d, "sim.json"),
		"--layers", "4", "--seed", "1", "--no-feedback", "--timeout", "30s", "--out-dir", t.TempDir()})
	if err := root.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
}
