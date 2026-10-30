package kicad

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDRCTimeoutFor(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "s.kicad_pcb")
	big := filepath.Join(dir, "b.kicad_pcb")
	_ = os.WriteFile(small, []byte("(kicad_pcb)"), 0o644)
	_ = os.WriteFile(big, make([]byte, 15<<20), 0o644)
	t.Setenv(EnvDRCTimeout, "")
	if d := DRCTimeoutFor(small, 15*time.Minute); d != 15*time.Minute {
		t.Fatalf("small %s", d)
	}
	if d := DRCTimeoutFor(big, 15*time.Minute); d != 45*time.Minute {
		t.Fatalf("15 MB %s", d)
	}
	t.Setenv(EnvDRCTimeout, "90m")
	if d := DRCTimeoutFor(small, 15*time.Minute); d != 90*time.Minute {
		t.Fatalf("env %s", d)
	}
	err := drcTimeoutError("/b/board.kicad_pcb", 45*time.Minute)
	if !errors.Is(err, ErrDRCTimeout) || !errors.Is(err, ErrDRCReport) || !strings.Contains(err.Error(), "board.kicad_pcb timed out after 45m0s") || !strings.Contains(err.Error(), EnvDRCTimeout) {
		t.Fatalf("%v", err)
	}
}
