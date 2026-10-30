package kicad

// kicad_roundtrip_test.go — KiCad load/round-trip checks for authored symbols
// and footprints. Gated on kicad-cli availability; skipped when not installed
// or hanging. Uses a 20-second watchdog per invocation (macOS Full Disk Access
// can make kicad-cli hang).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// kicadCLI finds kicad-cli; returns "" if not available.
func kicadCLI() string {
	p, err := exec.LookPath("kicad-cli")
	if err != nil {
		return ""
	}
	return p
}

// runWithWatchdog runs a command with a 20-second timeout.
func runWithWatchdog(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Skipf("kicad-cli timed out (macOS FDA?), skipping live check")
	}
	return string(out), err
}

func TestKiCadRoundTrip_Symbol(t *testing.T) {
	cli := kicadCLI()
	if cli == "" {
		t.Skip("kicad-cli not found, skipping live KiCad check")
	}

	spec := SymbolSpec{
		Name:      "RT_Test",
		Reference: "U",
		Value:     "RT_Test",
		Pins: []SymbolPin{
			{Number: "1", Name: "VCC", Type: "power_in", Side: "left"},
			{Number: "2", Name: "GND", Type: "power_in", Side: "right"},
		},
	}
	symText, err := AuthorSymbol(spec)
	if err != nil {
		t.Fatal(err)
	}
	lib := AuthorSymbolLib(map[string]string{spec.Name: symText})

	dir := t.TempDir()
	path := filepath.Join(dir, "test.kicad_sym")
	if err := os.WriteFile(path, []byte(lib), 0o644); err != nil {
		t.Fatal(err)
	}

	// kicad-cli sym upgrade validates and rewrites the file.
	out, err := runWithWatchdog(t, cli, "sym", "upgrade", path)
	if err != nil {
		t.Fatalf("kicad-cli sym upgrade failed: %v\n%s", err, out)
	}

	// Verify the file is still readable after upgrade.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "kicad_symbol_lib") {
		t.Error("upgraded file missing kicad_symbol_lib header")
	}
	if !strings.Contains(string(data), "RT_Test") {
		t.Error("upgraded file missing our symbol")
	}
}

func TestKiCadRoundTrip_Footprint(t *testing.T) {
	cli := kicadCLI()
	if cli == "" {
		t.Skip("kicad-cli not found, skipping live KiCad check")
	}

	spec := FootprintSpec{
		Name: "RT_FP_Test",
		Pads: []PadSpec{
			{Number: "1", Type: "smd", Shape: "roundrect", At: [3]float64{-0.5, 0, 0}, Size: [2]float64{0.6, 0.5},
				Layers: []string{"F.Cu", "F.Paste", "F.Mask"}},
			{Number: "2", Type: "smd", Shape: "roundrect", At: [3]float64{0.5, 0, 0}, Size: [2]float64{0.6, 0.5},
				Layers: []string{"F.Cu", "F.Paste", "F.Mask"}},
		},
	}
	fpText, err := AuthorFootprint(spec)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	prettyDir := filepath.Join(dir, "test.pretty")
	os.MkdirAll(prettyDir, 0o755)
	path := filepath.Join(prettyDir, "RT_FP_Test.kicad_mod")
	if err := os.WriteFile(path, []byte(fpText), 0o644); err != nil {
		t.Fatal(err)
	}

	// kicad-cli fp upgrade validates and rewrites the footprint library.
	// Must pass the .pretty directory (not the single file) to avoid
	// "Output path must be specified to convert legacy and non-KiCad libraries".
	out, err := runWithWatchdog(t, cli, "fp", "upgrade", prettyDir)
	if err != nil {
		t.Fatalf("kicad-cli fp upgrade failed: %v\n%s", err, out)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "footprint") {
		t.Error("upgraded file missing footprint header")
	}
	if !strings.Contains(string(data), "RT_FP_Test") {
		t.Error("upgraded file missing our footprint")
	}
}

func TestKiCadRoundTrip_MultiUnit(t *testing.T) {
	cli := kicadCLI()
	if cli == "" {
		t.Skip("kicad-cli not found, skipping live KiCad check")
	}

	spec := SymbolSpec{
		Name:      "RT_DualOp",
		Reference: "U",
		Units:     2,
		Pins: []SymbolPin{
			{Number: "1", Name: "OUT_A", Type: "output", Side: "right", Unit: 1},
			{Number: "2", Name: "IN-_A", Type: "input", Side: "left", Unit: 1},
			{Number: "3", Name: "IN+_A", Type: "input", Side: "left", Unit: 1},
			{Number: "5", Name: "IN+_B", Type: "input", Side: "left", Unit: 2},
			{Number: "6", Name: "IN-_B", Type: "input", Side: "left", Unit: 2},
			{Number: "7", Name: "OUT_B", Type: "output", Side: "right", Unit: 2},
			{Number: "4", Name: "VCC", Type: "power_in", Side: "top", Unit: 0},
			{Number: "8", Name: "GND", Type: "power_in", Side: "bottom", Unit: 0},
		},
	}
	symText, err := AuthorSymbol(spec)
	if err != nil {
		t.Fatal(err)
	}
	lib := AuthorSymbolLib(map[string]string{spec.Name: symText})

	dir := t.TempDir()
	path := filepath.Join(dir, "multi.kicad_sym")
	if err := os.WriteFile(path, []byte(lib), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runWithWatchdog(t, cli, "sym", "upgrade", path)
	if err != nil {
		t.Fatalf("kicad-cli sym upgrade failed for multi-unit: %v\n%s", err, out)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "RT_DualOp") {
		t.Error("multi-unit symbol lost after upgrade")
	}
}
