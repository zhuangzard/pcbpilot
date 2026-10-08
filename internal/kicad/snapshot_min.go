// MINIMAL stand-in — replaced by kicad/core at merge (its bridge.py
// `snapshot` and Snapshot). Kept small: only what `kicad place` needs.

package kicad

import (
	"bytes"
	_ "embed"
	"fmt"
)

//go:embed snapshot_min.py
var snapshotMinPy []byte

// Snapshot reads a .kicad_pcb with KiCad's bundled python and returns a pcb
// dump JSON for pcbauto.FromSnapshot (mil, y-up).
func Snapshot(pcbPath string) ([]byte, error) {
	out, err := runPlacePython(snapshotMinPy, "snapshot_min.py", pcbPath)
	if err != nil {
		return nil, err
	}
	// SWIG may print notices on stdout: the dump is the one JSON object line.
	for _, l := range bytes.Split(out, []byte("\n")) {
		if bytes.HasPrefix(l, []byte("{")) {
			return l, nil
		}
	}
	return nil, fmt.Errorf("snapshot_min.py: no JSON on stdout")
}
