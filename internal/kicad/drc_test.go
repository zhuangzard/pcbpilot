package kicad

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readDRCFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseDRCValid(t *testing.T) {
	rep, err := ParseDRC(readDRCFixture(t, "drc/clean.json"))
	if err != nil || rep.Total != 0 {
		t.Fatalf("clean report: %+v %v", rep, err)
	}
	rep, err = ParseDRC(readDRCFixture(t, "drc.json"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total != 3 || rep.Counts["clearance"] != 1 || rep.Counts["unconnected_items"] != 1 || rep.Counts["track_width"] != 1 {
		t.Fatalf("report with violations: %+v", rep)
	}
	// drc.json carries schematic_parity: [] so it also passes the parity variant.
	if _, err := ParseDRCWith(readDRCFixture(t, "drc.json"), DRCParseOptions{SchematicParity: true}); err != nil {
		t.Fatal(err)
	}
}

// A report pcbpilot cannot read must be an error, never a clean board.
func TestParseDRCRejectsUncheckable(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"drc/empty.json", "empty file"},
		{"drc/blank.json", "empty file"},
		{"drc/truncated.json", "not a JSON object"},
		{"drc/top-array.json", "not a JSON object"},
		{"drc/missing-violations.json", `no "violations" section`},
		{"drc/missing-unconnected.json", `no "unconnected_items" section`},
		{"drc/wrong-type.json", `"violations" is an object, not an array`},
		{"drc/null-section.json", `"unconnected_items" is null, not an array`},
		{"drc/bad-entry.json", `"violations" entries`},
	} {
		rep, err := ParseDRC(readDRCFixture(t, tc.file))
		if !errors.Is(err, ErrDRCReport) || rep != nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: rep %+v err %v (want %q)", tc.file, rep, err, tc.want)
		}
	}
	for _, s := range []string{"null", `"x"`, `{"violations": [], "unconnected_items": [], "coordinate_units": "furlong"}`} {
		if _, err := ParseDRC([]byte(s)); !errors.Is(err, ErrDRCReport) {
			t.Errorf("%s: want ErrDRCReport, got %v", s, err)
		}
	}
}

func TestParseDRCSchematicParityRequired(t *testing.T) {
	clean := readDRCFixture(t, "drc/clean.json") // no schematic_parity key
	if _, err := ParseDRC(clean); err != nil {
		t.Fatalf("parity not requested: %v", err)
	}
	if _, err := ParseDRCWith(clean, DRCParseOptions{SchematicParity: true}); !errors.Is(err, ErrDRCReport) || !strings.Contains(err.Error(), "schematic_parity") {
		t.Fatalf("requested parity missing: %v", err)
	}
	// Present but malformed is rejected even when not requested.
	bad := []byte(`{"violations": [], "unconnected_items": [], "schematic_parity": "none"}`)
	if _, err := ParseDRC(bad); !errors.Is(err, ErrDRCReport) {
		t.Fatalf("malformed parity section: %v", err)
	}
}

// DRCSeverity must not parse a file kicad-cli failed to write, nor accept
// one when kicad-cli exits non-zero.
func TestDRCSeverityCLIFailure(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "drc.json")
	if err := os.WriteFile(out, readDRCFixture(t, "drc/clean.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := &Tools{CLI: filepath.Join(dir, "no-such-kicad-cli")}
	rep, err := tools.DRCSeverity(filepath.Join(dir, "x.kicad_pcb"), out, "error")
	if !errors.Is(err, ErrDRCReport) || rep != nil {
		t.Fatalf("missing kicad-cli read as %+v %v", rep, err)
	}
	if _, serr := os.Stat(out); !os.IsNotExist(serr) {
		t.Fatalf("a stale report from an earlier run must be removed first: %v", serr)
	}
}
