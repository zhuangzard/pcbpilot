package kicad

// boardenv.go — the KiCad configuration for board kicad-cli runs (DRC).
//
// KiCad chains the user's global fp-lib-table in front of the project one,
// and DRC's library checks (lib_footprint_issues / lib_footprint_mismatch)
// load every footprint library the board names. With the stock global
// table that is ~155 libraries — measured on a 1443-part board the full
// table cost ~2–3 min per run over a table holding only the board's own
// libraries, and an EMPTY table (the schematic isolation, SchCLIEnv) gets
// the library checks wrong: 199 lib_footprint_issues instead of 13 (every
// stock footprint "not in the library table").
//
// BoardCLIEnv therefore gives kicad-cli a private KICAD_CONFIG_HOME whose
// global fp-lib-table is the user's own table cut down to the libraries the
// board's footprints reference — the same entries, verbatim, nested
// "Table" entries resolved — with the user's kicad_common.json (path
// variables) copied. The project's own fp-lib-table is untouched (it sits
// next to the board). DRC output is identical to the user's configuration;
// only libraries the board never reads are left out.
//
// Inherit the environment unchanged (nil) when: PCBPILOT_KICAD_ISOLATE=0,
// KICAD_CONFIG_HOME is already set, no user fp-lib-table is found, or a
// nested table cannot be read — never guess a table.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var reBoardFootprintLib = regexp.MustCompile(`\(footprint\s+"([^":]+):`)

// BoardFootprintLibs are the library nicknames the board's footprints name.
func BoardFootprintLibs(board []byte) []string {
	seen := map[string]bool{}
	for _, m := range reBoardFootprintLib.FindAllSubmatch(board, -1) {
		seen[string(m[1])] = true
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// userKiCadConfigDir is the user's KiCad settings directory when it holds
// an fp-lib-table (KiCadConfigDir, fplib.go), else "".
func userKiCadConfigDir() string {
	d, err := KiCadConfigDir()
	if err != nil || d == "" || !fileExists(filepath.Join(d, "fp-lib-table")) {
		return ""
	}
	return d
}

// globalFPTable is the global fp-lib-table KiCad would use: the user's,
// else (no user table yet) the stock template KiCad seeds it from. The
// settings version directory name comes with it.
func globalFPTable() (table, common, version string) {
	if d := userKiCadConfigDir(); d != "" {
		return filepath.Join(d, "fp-lib-table"), filepath.Join(d, "kicad_common.json"), filepath.Base(d)
	}
	if st := StockFootprintDir(); st != "" {
		if t := filepath.Join(filepath.Dir(st), "template", "fp-lib-table"); fileExists(t) {
			return t, "", FPLibVersion
		}
	}
	return "", "", ""
}

var reEnvRef = regexp.MustCompile(`\$\{([A-Za-z0-9_]+)\}`)

// libTableEntries reads a library table: nickname → the entry re-written
// as one line, in table order (first wins, KiCad's rule). Nested "Table"
// entries are expanded in place; depth bounds a cycle.
func libTableEntries(path string, depth int, out map[string]string, order *[]string) error {
	if depth > 4 {
		return fmt.Errorf("%s: nested library tables too deep", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	root, err := parseSexpr(b)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	tbl := root
	if tbl.head() != "fp_lib_table" {
		tbl = root.child("fp_lib_table")
	}
	if tbl == nil {
		return fmt.Errorf("%s: not an fp_lib_table", path)
	}
	for _, lib := range tbl.children("lib") {
		get := func(k string) string {
			if c := lib.child(k); c != nil {
				return c.arg(0)
			}
			return ""
		}
		name, typ, uri := get("name"), get("type"), get("uri")
		if strings.EqualFold(typ, "Table") {
			if lib.child("disabled") != nil {
				// A disabled nested table disables every library in it:
				// its children must not reach the private table.
				continue
			}
			unresolved := ""
			nested := reEnvRef.ReplaceAllStringFunc(uri, func(m string) string {
				v := os.Getenv(reEnvRef.FindStringSubmatch(m)[1])
				if v == "" {
					unresolved = m
				}
				return v
			})
			if unresolved != "" {
				return fmt.Errorf("%s: nested table %q: %s is not set", path, name, unresolved)
			}
			if err := libTableEntries(nested, depth+1, out, order); err != nil {
				return err
			}
			continue
		}
		if name == "" || out[name] != "" {
			continue
		}
		line := fmt.Sprintf("\t(lib (name %s) (type %s) (uri %s) (options %s) (descr %s)", Q(name), Q(typ), Q(uri), Q(get("options")), Q(get("descr")))
		if lib.child("disabled") != nil {
			line += " (disabled)"
		}
		out[name] = line + ")"
		*order = append(*order, name)
	}
	return nil
}

// BoardCLIEnv is the environment for a board kicad-cli run on board (the
// .kicad_pcb bytes), or nil to inherit the parent's (see the file comment).
func BoardCLIEnv(board []byte) []string {
	if os.Getenv(EnvIsolateConfig) == "0" || os.Getenv("KICAD_CONFIG_HOME") != "" {
		return nil
	}
	dir, err := boardConfigDir(board)
	if err != nil || dir == "" {
		return nil
	}
	return append(os.Environ(), "KICAD_CONFIG_HOME="+dir)
}

// boardConfigDir makes (atomically, cached by content) the private config
// directory for board.
func boardConfigDir(board []byte) (string, error) {
	table, commonPath, version := globalFPTable()
	if table == "" {
		return "", nil
	}
	entries, order := map[string]string{}, []string{}
	if err := libTableEntries(table, 0, entries, &order); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("(fp_lib_table\n\t(version 7)\n")
	for _, n := range BoardFootprintLibs(board) {
		if e := entries[n]; e != "" {
			b.WriteString(e + "\n")
		}
	}
	b.WriteString(")\n")
	fp := b.String()
	var common []byte
	if commonPath != "" {
		common, _ = os.ReadFile(commonPath)
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s", version, fp, common)
	dir := filepath.Join(CacheRoot(), "kicad-config", "board-"+hex.EncodeToString(h.Sum(nil))[:16])
	ver := filepath.Join(dir, version)
	if b, err := os.ReadFile(filepath.Join(ver, "fp-lib-table")); err == nil && string(b) == fp {
		return dir, nil
	}
	// Every version directory gets the tables: a version directory without
	// them makes KiCad seed the stock table again.
	files := map[string]string{"fp-lib-table": fp, "sym-lib-table": LibTableText("sym_lib_table", nil)}
	if common != nil {
		files["kicad_common.json"] = string(common)
	}
	for _, sub := range append([]string{""}, kicadSettingsVersions...) {
		d := filepath.Join(dir, sub)
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
		for name, text := range files {
			if name == "kicad_common.json" && d != ver {
				continue
			}
			if err := writeAtomic(filepath.Join(d, name), text); err != nil {
				return "", err
			}
		}
	}
	return dir, nil
}

func writeAtomic(p, text string) error {
	if b, err := os.ReadFile(p); err == nil && string(b) == text {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.WriteString(text)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write %s: %v %v", p, werr, cerr)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		_ = os.Remove(tmp.Name())
		if b, rerr := os.ReadFile(p); rerr == nil && string(b) == text {
			return nil // a concurrent writer won
		}
		return err
	}
	return nil
}
