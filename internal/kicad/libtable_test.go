package kicad

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureLibTableEntry_CreateNew(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "sym-lib-table")

	changed, err := EnsureLibTableEntry(table, "sym_lib_table", "mylib", "lib/mylib.kicad_sym")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("expected changed=true for new file")
	}

	data, err := os.ReadFile(table)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	checks := []string{
		"(sym_lib_table",
		"(version 7)",
		`(name "mylib")`,
		`(uri "${KIPRJMOD}/lib/mylib.kicad_sym")`,
		`(type "KiCad")`,
	}
	for _, c := range checks {
		if !strings.Contains(text, c) {
			t.Errorf("missing %q in:\n%s", c, text)
		}
	}
}

func TestEnsureLibTableEntry_Idempotent(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "fp-lib-table")

	_, err := EnsureLibTableEntry(table, "fp_lib_table", "myfp", "lib/myfp.pretty")
	if err != nil {
		t.Fatal(err)
	}

	// Second call should be no-op.
	changed, err := EnsureLibTableEntry(table, "fp_lib_table", "myfp", "lib/myfp.pretty")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("expected changed=false for identical entry")
	}
}

func TestEnsureLibTableEntry_UpdateURI(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "sym-lib-table")

	_, _ = EnsureLibTableEntry(table, "sym_lib_table", "mylib", "old/path.kicad_sym")

	changed, err := EnsureLibTableEntry(table, "sym_lib_table", "mylib", "new/path.kicad_sym")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("expected changed=true for URI update")
	}

	data, _ := os.ReadFile(table)
	if !strings.Contains(string(data), "new/path.kicad_sym") {
		t.Error("URI was not updated")
	}
}

func TestEnsureLibTableEntry_AddToExisting(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "sym-lib-table")

	_, _ = EnsureLibTableEntry(table, "sym_lib_table", "lib1", "lib/lib1.kicad_sym")
	_, err := EnsureLibTableEntry(table, "sym_lib_table", "lib2", "lib/lib2.kicad_sym")
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(table)
	text := string(data)
	if !strings.Contains(text, `"lib1"`) || !strings.Contains(text, `"lib2"`) {
		t.Errorf("expected both entries, got:\n%s", text)
	}
}

func TestEnsureLibTableEntry_BadKind(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "test-table")

	_, err := EnsureLibTableEntry(table, "bad_table", "x", "y")
	if err == nil || !strings.Contains(err.Error(), "sym_lib_table or fp_lib_table") {
		t.Errorf("expected kind validation error, got: %v", err)
	}
}

func TestEnsureLibTableEntry_EmptyNickname(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "test-table")

	_, err := EnsureLibTableEntry(table, "sym_lib_table", "", "path")
	if err == nil || !strings.Contains(err.Error(), "nickname") {
		t.Errorf("expected nickname error, got: %v", err)
	}
}

func TestRemoveLibTableEntry(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "sym-lib-table")

	_, _ = EnsureLibTableEntry(table, "sym_lib_table", "lib1", "lib/lib1.kicad_sym")
	_, _ = EnsureLibTableEntry(table, "sym_lib_table", "lib2", "lib/lib2.kicad_sym")

	changed, err := RemoveLibTableEntry(table, "sym_lib_table", "lib1")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("expected changed=true")
	}

	data, _ := os.ReadFile(table)
	text := string(data)
	if strings.Contains(text, `"lib1"`) {
		t.Error("lib1 was not removed")
	}
	if !strings.Contains(text, `"lib2"`) {
		t.Error("lib2 should still exist")
	}
}

func TestRemoveLibTableEntry_NotFound(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "sym-lib-table")

	_, _ = EnsureLibTableEntry(table, "sym_lib_table", "lib1", "lib/lib1.kicad_sym")

	changed, err := RemoveLibTableEntry(table, "sym_lib_table", "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("expected changed=false for non-existent nickname")
	}
}

func TestRemoveLibTableEntry_NoFile(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "nonexistent-table")

	changed, err := RemoveLibTableEntry(table, "sym_lib_table", "lib1")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("expected changed=false for non-existent file")
	}
}
