package kicad

// libtable.go — incremental update of KiCad sym-lib-table / fp-lib-table files.
// Adds or updates a library entry with a ${KIPRJMOD}-relative path.
// Preserves existing entries and formatting as much as possible.

import (
	"fmt"
	"os"
	"strings"
)

// sxArg returns the i-th atom argument of a sexp node (0 = first after head).
func sxArg(n *sexp, i int) string {
	idx := i + 1 // skip head
	if n == nil || idx >= len(n.list) {
		return ""
	}
	return n.list[idx].atom
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
	switch tableKind {
	case "sym_lib_table", "fp_lib_table":
	default:
		return false, fmt.Errorf("tableKind must be sym_lib_table or fp_lib_table, got %q", tableKind)
	}

	uri := "${KIPRJMOD}/" + relURI

	// Read existing table if present.
	data, err := os.ReadFile(tablePath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", tablePath, err)
	}

	if os.IsNotExist(err) || len(data) == 0 {
		// Create a new table with this single entry.
		text := fmt.Sprintf("(%s\n\t(version 7)\n\t(lib (name %s) (type \"KiCad\") (uri %s) (options \"\") (descr \"pcbpilot\"))\n)\n",
			tableKind, Q(nickname), Q(uri))
		return true, os.WriteFile(tablePath, []byte(text), 0o644)
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
		// Update URI.
		if uriNode != nil && len(uriNode.list) > 1 {
			uriNode.list[1] = &sexp{atom: uri, quoted: true}
		}
		out := sexpSerializeTable(root, tableKind)
		return true, os.WriteFile(tablePath, []byte(out), 0o644)
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
	return true, os.WriteFile(tablePath, []byte(out), 0o644)
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
	return true, os.WriteFile(tablePath, []byte(out), 0o644)
}
