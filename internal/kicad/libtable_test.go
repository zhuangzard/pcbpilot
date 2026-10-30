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

// ---- Regression tests for WP5 review findings ----

// Finding #5 / #11: path traversal in lib names rejected.
func TestValidateLibName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"normal", "mylib", false},
		{"with-dash", "my-lib", false},
		{"empty", "", true},
		{"slash", "my/lib", true},
		{"backslash", `my\lib`, true},
		{"dot-dot", "..", true},
		{"dot", ".", true},
		{"CON", "CON", true},
		{"con-lower", "con", true},
		{"COM1", "COM1", true},
		{"NUL", "NUL", true},
		// Kimi finding #7: Windows-invalid characters.
		{"colon", "my:lib", true},
		{"asterisk", "my*lib", true},
		{"question", "my?lib", true},
		{"quote", `my"lib`, true},
		{"lt", "my<lib", true},
		{"gt", "my>lib", true},
		{"pipe", "my|lib", true},
		{"control", "my\x01lib", true},
		// Reserved name with extension (e.g. CON.txt → NTFS alternate stream).
		{"CON.txt", "CON.txt", true},
		{"LPT1.log", "LPT1.log", true},
		{"NUL.sym", "nul.sym", true},
		// Allowed: names that merely start with reserved prefix but aren't exact.
		{"CONX", "CONX", false},
		{"COM10", "COM10", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateLibName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateLibName(%q) = %v, wantErr=%v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// Finding #11: relURI validation in EnsureLibTableEntry.
func TestEnsureLibTableEntry_TraversalRejected(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "sym-lib-table")

	_, err := EnsureLibTableEntry(table, "sym_lib_table", "evil", "../../etc/passwd")
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Errorf("expected traversal rejection, got: %v", err)
	}
}

func TestEnsureLibTableEntry_AbsoluteRejected(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "sym-lib-table")

	_, err := EnsureLibTableEntry(table, "sym_lib_table", "abs", "/etc/lib.kicad_sym")
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("expected absolute path rejection, got: %v", err)
	}
}

// Kimi R2: backslash traversal must be rejected even on Unix.
func TestValidateRelPath_BackslashTraversal(t *testing.T) {
	err := ValidateRelPath(`..\..\outside`)
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Errorf("expected traversal rejection for backslash path, got: %v", err)
	}
}

// Finding #11: relURI normalised with forward slashes.
func TestEnsureLibTableEntry_ForwardSlashNormalized(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "sym-lib-table")

	_, err := EnsureLibTableEntry(table, "sym_lib_table", "norm", "lib/mylib.kicad_sym")
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(table)
	if !strings.Contains(string(data), "${KIPRJMOD}/lib/mylib.kicad_sym") {
		t.Errorf("expected normalized path with forward slashes, got:\n%s", string(data))
	}
}

// Finding #12: LibTableTextMerge preserves non-generated rows.
func TestLibTableTextMerge_PreservesExisting(t *testing.T) {
	dir := t.TempDir()
	existingPath := filepath.Join(dir, "sym-lib-table")

	// Write an existing table with a user-registered library.
	existing := `(sym_lib_table
	(version 7)
	(lib (name "userlib") (type "KiCad") (uri "${KIPRJMOD}/custom/userlib.kicad_sym") (options "") (descr "user"))
)
`
	os.WriteFile(existingPath, []byte(existing), 0o644)

	// Generate with sch-build libs.
	genLibs := map[string]string{
		"pcbpilot": "pcbpilot.kicad_sym",
	}
	result, err := LibTableTextMerge("sym_lib_table", genLibs, existingPath)
	if err != nil {
		t.Fatal(err)
	}

	// Must contain both the generated and the user lib.
	if !strings.Contains(result, `"pcbpilot"`) {
		t.Error("missing generated lib pcbpilot")
	}
	if !strings.Contains(result, `"userlib"`) {
		t.Error("user-registered lib userlib was lost during rewrite")
	}
}

// Finding #12: generated entries replace colliding user entries.
func TestLibTableTextMerge_ReplacesCollision(t *testing.T) {
	dir := t.TempDir()
	existingPath := filepath.Join(dir, "sym-lib-table")

	existing := `(sym_lib_table
	(version 7)
	(lib (name "mylib") (type "KiCad") (uri "${KIPRJMOD}/old/mylib.kicad_sym") (options "") (descr "old"))
)
`
	os.WriteFile(existingPath, []byte(existing), 0o644)

	genLibs := map[string]string{
		"mylib": "mylib.kicad_sym",
	}
	result, err := LibTableTextMerge("sym_lib_table", genLibs, existingPath)
	if err != nil {
		t.Fatal(err)
	}

	// Should have only ONE entry for mylib (the generated one).
	count := strings.Count(result, `"mylib"`)
	if count != 1 {
		t.Errorf("expected exactly 1 entry for mylib, got %d occurrences", count)
	}
	if !strings.Contains(result, "pcbpilot sch-build") {
		t.Error("should use the generated entry, not the old one")
	}
}

// LibTableTextMerge returns error on malformed existing table.
func TestLibTableTextMerge_MalformedExistingError(t *testing.T) {
	dir := t.TempDir()
	existingPath := filepath.Join(dir, "sym-lib-table")
	os.WriteFile(existingPath, []byte("(((( not valid s-expression"), 0o644)

	_, err := LibTableTextMerge("sym_lib_table", map[string]string{"a": "a.kicad_sym"}, existingPath)
	if err == nil {
		t.Error("expected error for malformed existing table")
	}
}

// Regression: lib entry without uri child must get one added, not silently succeed.
// (Kimi finding #5)
func TestEnsureLibTableEntry_MissingURIAdded(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "sym-lib-table")

	// Write a malformed table: lib entry has no (uri ...) child.
	malformed := `(sym_lib_table
	(version 7)
	(lib (name "broken") (type "KiCad") (options "") (descr "no uri"))
)
`
	os.WriteFile(table, []byte(malformed), 0o644)

	changed, err := EnsureLibTableEntry(table, "sym_lib_table", "broken", "lib/fixed.kicad_sym")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("expected changed=true when adding missing uri")
	}

	data, _ := os.ReadFile(table)
	text := string(data)
	if !strings.Contains(text, "fixed.kicad_sym") {
		t.Errorf("expected uri to be written, got:\n%s", text)
	}
}

// Kimi R3: wrong-kind existing table must be rejected.
func TestLibTableTextMerge_WrongKindError(t *testing.T) {
	dir := t.TempDir()
	existingPath := filepath.Join(dir, "sym-lib-table")
	os.WriteFile(existingPath, []byte(`(fp_lib_table (version 7) (lib (name "user") (type "KiCad") (uri "/fp")))`), 0o644)

	_, err := LibTableTextMerge("sym_lib_table", map[string]string{"a": "a.kicad_sym"}, existingPath)
	if err == nil {
		t.Error("expected error for wrong-kind existing table")
	}
}
