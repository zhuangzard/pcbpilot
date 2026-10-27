package kicad

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
)

// TestRunParityNamedProjectRequired: a project that was named but is
// missing, or cannot be read, makes parity CannotCheck (never a pass
// without the project's context). kicad-cli is a placeholder file: the
// check stops before running it.
func TestRunParityNamedProjectRequired(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "kicad-cli.exe")
	sch := filepath.Join(dir, "a.kicad_sch")
	pcb := filepath.Join(dir, "a.kicad_pcb")
	for _, p := range []string{cli, sch, pcb} {
		if err := os.WriteFile(p, []byte("(x)"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	unreadable := filepath.Join(dir, "dir.kicad_pro") // a directory: stat works, read does not
	if err := os.Mkdir(unreadable, 0o755); err != nil {
		t.Fatal(err)
	}
	tools := &Tools{CLI: cli}
	for name, pro := range map[string]string{"missing": filepath.Join(dir, "none.kicad_pro"), "unreadable": unreadable} {
		r := tools.RunParity(pcb, pro, []string{sch}, nil)
		if r.Status != gate.CannotCheck || !strings.Contains(r.Detail, "project") {
			t.Errorf("%s project: %+v", name, r)
		}
	}
}
