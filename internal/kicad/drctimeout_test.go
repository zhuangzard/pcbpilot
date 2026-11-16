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
	if !errors.Is(err, ErrDRCTimeout) || !errors.Is(err, ErrDRCReport) || !strings.Contains(err.Error(), "board.kicad_pcb timed out after 45m0s") ||
		!strings.Contains(err.Error(), EnvDRCTimeout) || !strings.Contains(err.Error(), DRCTimeoutFlag+" <duration>") {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "custom rules file board") {
		t.Fatalf("a board without custom rules names a rules file: %v", err)
	}
}

// The limit grows with the custom rules file next to the board (5 min per
// 100 kB), stops at 2 h, and a timeout names a large rules file with its
// size; --timeout (DRCTimeoutWith's override) wins over the environment.
func TestDRCTimeoutCustomRules(t *testing.T) {
	dir := t.TempDir()
	pcb := filepath.Join(dir, "demo.kicad_pcb")
	dru := filepath.Join(dir, "demo.kicad_dru")
	_ = os.WriteFile(pcb, []byte("(kicad_pcb)"), 0o644)
	t.Setenv(EnvDRCTimeout, "")
	for _, c := range []struct {
		kB   int
		want time.Duration
	}{{0, 30 * time.Minute}, {99, 30 * time.Minute}, {100, 35 * time.Minute}, {400, 50 * time.Minute}, {4000, 2 * time.Hour}} {
		_ = os.WriteFile(dru, make([]byte, c.kB<<10), 0o644)
		if d := DRCTimeoutFor(pcb, 30*time.Minute); d != c.want {
			t.Errorf("%d kB of rules: %s, want %s", c.kB, d, c.want)
		}
	}
	// Another board's rules file does not count.
	other := filepath.Join(dir, "other.kicad_pcb")
	_ = os.WriteFile(other, []byte("(kicad_pcb)"), 0o644)
	if d := DRCTimeoutFor(other, 30*time.Minute); d != 30*time.Minute {
		t.Errorf("board without its own rules file: %s", d)
	}
	_ = os.WriteFile(dru, make([]byte, 400<<10), 0o644)
	if msg := drcTimeoutError(pcb, 50*time.Minute).Error(); !strings.Contains(msg, "custom rules file demo.kicad_dru is 400 kB") {
		t.Errorf("large rules file not named: %s", msg)
	}
	_ = os.WriteFile(dru, make([]byte, 20<<10), 0o644)
	if msg := drcTimeoutError(pcb, 30*time.Minute).Error(); strings.Contains(msg, "demo.kicad_dru") {
		t.Errorf("small rules file named: %s", msg)
	}
	// flag > environment > computed
	if d := DRCTimeoutWith(pcb, 30*time.Minute, 0); d != 30*time.Minute {
		t.Errorf("computed: %s", d)
	}
	t.Setenv(EnvDRCTimeout, "90m")
	if d := DRCTimeoutWith(pcb, 30*time.Minute, 0); d != 90*time.Minute {
		t.Errorf("environment: %s", d)
	}
	if d := DRCTimeoutWith(pcb, 30*time.Minute, 7*time.Minute); d != 7*time.Minute {
		t.Errorf("flag over environment: %s", d)
	}
	t.Setenv(EnvDRCTimeout, "-5m")
	if d := DRCTimeoutWith(pcb, 30*time.Minute, 0); d != 30*time.Minute {
		t.Errorf("a non-positive environment value must be ignored: %s", d)
	}
}

// RunDRC stops kicad-cli at DRCRunOptions.Timeout even when the
// environment allows more, and reports a timeout (no report = not checked).
func TestRunDRCTimeoutOption(t *testing.T) {
	dir := t.TempDir()
	pcb := filepath.Join(dir, "demo.kicad_pcb")
	if err := os.WriteFile(pcb, []byte("(kicad_pcb (version 20241229) (generator \"pcbnew\"))\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo.kicad_dru"), make([]byte, 150<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := fakeDRC(t, "drc/clean.json", "0")
	t.Setenv("PCBPILOT_FAKE_DRC_SLEEP_MS", "20000")
	t.Setenv(EnvDRCTimeout, "1h")
	start := time.Now()
	_, err := RunDRC(DRCRunOptions{PCB: pcb, CLI: tools.CLI, Timeout: 500 * time.Millisecond})
	if !errors.Is(err, ErrDRCTimeout) || !errors.Is(err, ErrDRCReport) {
		t.Fatalf("want a timeout, got %v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Errorf("the run was not stopped at its limit (%s)", time.Since(start))
	}
	for _, want := range []string{"timed out after 500ms", "demo.kicad_dru is 150 kB", DRCTimeoutFlag, EnvDRCTimeout} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	// Without the sleep the same call passes: the limit, not the fixture, failed it.
	t.Setenv("PCBPILOT_FAKE_DRC_SLEEP_MS", "")
	if run, err := RunDRC(DRCRunOptions{PCB: pcb, CLI: tools.CLI, Timeout: time.Minute}); err != nil || run.Report == nil {
		t.Fatalf("plain run: %v", err)
	}
}
