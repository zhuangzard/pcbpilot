package kicad

import (
	"os/exec"
	"testing"
)

// plainPython is any working Python 3 (not KiCad's): python3, else python.
// A name that resolves but does not run (the Windows Store stub) is skipped.
func plainPython(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python3", "python"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if exec.Command(p, "-c", "import sys; sys.exit(0 if sys.version_info >= (3, 8) else 1)").Run() == nil {
			return p
		}
	}
	t.Skip("no Python 3.8+ on PATH (python3 / python)")
	return ""
}

// TestBridgePyUnit runs bridge_test.py: bridge.py's validation and indexing
// contract (containers, scalar types, nets, layers, ids, footprint and text
// keys) against a fake pcbnew, so the default test run covers it without
// KiCad. The live tests (PCBPILOT_KICAD_LIVE=1) cover the real pcbnew side.
func TestBridgePyUnit(t *testing.T) {
	cmd := exec.Command(plainPython(t), "-B", "bridge_test.py")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bridge_test.py: %v\n%s", err, out)
	}
}
