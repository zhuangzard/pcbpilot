package kicad

// libtable.go — incremental update of KiCad sym-lib-table / fp-lib-table files.
// Adds or updates a library entry with a ${KIPRJMOD}-relative path.
// Preserves existing entries and formatting as much as possible.

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kfile"
)

// sxArg returns the i-th atom argument of a sexp node (0 = first after head).
func sxArg(n *sexp, i int) string {
	idx := i + 1 // skip head
	if n == nil || idx >= len(n.list) {
		return ""
	}
	return n.list[idx].atom
}

// ValidateLibName rejects names that contain path separators, traversal
// sequences, absolute/drive/UNC prefixes, Windows-invalid characters
// (: * ? " < > |), control characters, or Windows reserved base names
// (CON, PRN, AUX, NUL, COM1-9, LPT1-9) even with extensions.
func ValidateLibName(name string) error {
	if name == "" {
		return fmt.Errorf("library name is empty")
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("library name %q contains path separators", name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("library name %q is a traversal component", name)
	}
	// Reject Windows-invalid filename characters and control chars.
	for _, c := range name {
		if c < 0x20 {
			return fmt.Errorf("library name %q contains control character U+%04X", name, c)
		}
		if strings.ContainsRune(`:*?"<>|`, c) {
			return fmt.Errorf("library name %q contains Windows-invalid character %q", name, string(c))
		}
	}
	// Windows reserved names: base name before any '.' extension.
	// "CON.txt", "LPT1.log" etc. are equally reserved on NTFS.
	base := strings.ToUpper(name)
	if dotIdx := strings.IndexByte(base, '.'); dotIdx >= 0 {
		base = base[:dotIdx]
	}
	base = strings.TrimRight(base, " ")
	for _, r := range []string{"CON", "PRN", "AUX", "NUL"} {
		if base == r {
			return fmt.Errorf("library name %q is a Windows reserved name", name)
		}
	}
	for _, prefix := range []string{"COM", "LPT"} {
		if len(base) == 4 && strings.HasPrefix(base, prefix) && base[3] >= '1' && base[3] <= '9' {
			return fmt.Errorf("library name %q is a Windows reserved name", name)
		}
	}
	return nil
}

// ValidateRelPath rejects relative paths that escape the project root.
// It normalises forward slashes, rejects absolute/drive/UNC paths, and
// checks that the cleaned path stays below root.
func ValidateRelPath(relPath string) error {
	if relPath == "" {
		return fmt.Errorf("path is empty")
	}
	// Normalise backslashes to forward slashes platform-independently
	// (on Unix filepath.Clean does not treat backslash as a separator).
	norm := strings.ReplaceAll(relPath, `\`, "/")
	// Reject absolute, drive-letter, and UNC paths.
	if filepath.IsAbs(norm) || (len(norm) >= 2 && norm[1] == ':') ||
		strings.HasPrefix(norm, "//") {
		return fmt.Errorf("path %q is absolute; a project-relative path is required", relPath)
	}
	// Clean using forward-slash path logic and check for traversal.
	cleaned := path.Clean(norm)
	if strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return fmt.Errorf("path %q escapes the project root", relPath)
	}
	return nil
}

// EnsureLibTableEntry ensures that a library nickname with the given URI
// exists in the table file. If the file does not exist, it is created.
// If the nickname exists with a different URI, it is updated.
// Returns true if the file was changed.
//
// tableKind is "sym_lib_table" or "fp_lib_table".
// relURI is the library path relative to ${KIPRJMOD}, e.g. "lib/mylib.kicad_sym".
func EnsureLibTableEntry(tablePath, tableKind, nickname, relURI string) (bool, error) {
	if nickname == "" {
		return false, fmt.Errorf("nickname is required")
	}
	if relURI == "" {
		return false, fmt.Errorf("relURI is required")
	}
	if err := ValidateRelPath(relURI); err != nil {
		return false, fmt.Errorf("relURI: %w", err)
	}
	switch tableKind {
	case "sym_lib_table", "fp_lib_table":
	default:
		return false, fmt.Errorf("tableKind must be sym_lib_table or fp_lib_table, got %q", tableKind)
	}

	// Normalise the relURI to forward slashes platform-independently.
	normURI := path.Clean(strings.ReplaceAll(relURI, `\`, "/"))
	uri := "${KIPRJMOD}/" + normURI

	// Read existing table if present.
	data, err := os.ReadFile(tablePath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", tablePath, err)
	}

	if os.IsNotExist(err) || len(data) == 0 {
		// Create a new table with this single entry.
		text := fmt.Sprintf("(%s\n\t(version 7)\n\t(lib (name %s) (type \"KiCad\") (uri %s) (options \"\") (descr \"pcbpilot\"))\n)\n",
			tableKind, Q(nickname), Q(uri))
		_, werr := kfile.WriteFile(tablePath, []byte(text), kfile.Opts{NoLockCheck: true})
		return true, werr
	}

	// Parse existing.
	root, perr := parseSx(string(data))
	if perr != nil {
		return false, fmt.Errorf("parse %s: %w", tablePath, perr)
	}
	if root.head() != tableKind {
		return false, fmt.Errorf("%s: top-level form is %q, want %s", tablePath, root.head(), tableKind)
	}

	// Find the existing lib with this nickname.
	for _, ch := range root.list {
		if ch.head() != "lib" {
			continue
		}
		nameNode := ch.child("name")
		if nameNode == nil {
			continue
		}
		if sxArg(nameNode, 0) != nickname {
			continue
		}
		// Found: check if URI matches.
		uriNode := ch.child("uri")
		if uriNode != nil && sxArg(uriNode, 0) == uri {
			return false, nil // already correct
		}
		// Update or create the URI node. If the entry had no uri child
		// (malformed), add one rather than silently succeeding without
		// establishing the registration. (Kimi finding #5)
		if uriNode != nil && len(uriNode.list) > 1 {
			uriNode.list[1] = &sexp{atom: uri, quoted: true}
		} else {
			newURI, perr := parseSx(fmt.Sprintf("(uri %s)", Q(uri)))
			if perr != nil {
				return false, fmt.Errorf("internal: %w", perr)
			}
			ch.list = append(ch.list, newURI)
		}
		out := sexpSerializeTable(root, tableKind)
		_, werr := kfile.WriteFile(tablePath, []byte(out), kfile.Opts{NoLockCheck: true})
		return true, werr
	}

	// Not found: append a new entry.
	entryText := fmt.Sprintf("(lib (name %s) (type \"KiCad\") (uri %s) (options \"\") (descr \"pcbpilot\"))",
		Q(nickname), Q(uri))
	entryNode, perr := parseSx(entryText)
	if perr != nil {
		return false, fmt.Errorf("internal: %w", perr)
	}

	root.list = append(root.list, entryNode)
	out := sexpSerializeTable(root, tableKind)
	_, werr := kfile.WriteFile(tablePath, []byte(out), kfile.Opts{NoLockCheck: true})
	return true, werr
}

// sexpSerializeTable renders a library table with canonical formatting.
func sexpSerializeTable(root *sexp, kind string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "(%s\n", kind)
	for _, ch := range root.list {
		// Skip the head atom of the root list.
		if ch.atom == kind && len(ch.list) == 0 {
			continue
		}
		b.WriteString("\t")
		b.WriteString(sexpSerialize(ch))
		b.WriteString("\n")
	}
	b.WriteString(")\n")
	return b.String()
}

// RemoveLibTableEntry removes a library nickname from a table file.
// Returns true if the file was changed.
func RemoveLibTableEntry(tablePath, tableKind, nickname string) (bool, error) {
	data, err := os.ReadFile(tablePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	root, perr := parseSx(string(data))
	if perr != nil {
		return false, fmt.Errorf("parse %s: %w", tablePath, perr)
	}

	found := false
	var kept []*sexp
	for _, ch := range root.list {
		if ch.head() == "lib" {
			nameNode := ch.child("name")
			if nameNode != nil && sxArg(nameNode, 0) == nickname {
				found = true
				continue
			}
		}
		kept = append(kept, ch)
	}

	if !found {
		return false, nil
	}

	root.list = kept
	out := sexpSerializeTable(root, tableKind)
	_, werr := kfile.WriteFile(tablePath, []byte(out), kfile.Opts{NoLockCheck: true})
	return true, werr
}
