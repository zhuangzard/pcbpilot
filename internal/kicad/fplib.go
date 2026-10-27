package kicad

// fplib.go — footprint library resolution for `kicad pcb-sync`: the
// project fp-lib-table layered over the user's global one, KiCad path
// variables expanded, and "Lib:Name" resolved to a .kicad_mod file.
//
// This reads the user's REAL tables (never the isolated empty config
// kicadenv.go gives schematic kicad-cli runs): a board's footprints come
// from wherever the user's KiCad finds them.
//
// Lookup follows KiCad's library table semantics: the project table is
// searched first and the global table is its fallback, so a project
// nickname hides the same global one; a disabled row is skipped and the
// lookup falls through to the next table; a row of type "Table" splices
// the rows of the referenced table in at its own position.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// Footprint library error codes (report errors[].code).
const (
	ErrFPLibType       = "E_FP_LIB_TYPE"       // a library type pcbpilot cannot read (only KiCad .pretty)
	ErrFPLibUnresolved = "E_FP_LIB_UNRESOLVED" // unknown nickname, undefined path variable or missing directory
	ErrFPNotFound      = "E_FP_NOT_FOUND"      // the library resolved but holds no such footprint
)

// FPLibVersion is the KiCad settings version whose tables are read.
const FPLibVersion = "10.0"

// FootprintDirVar is the variable stock fp-lib-table rows are written against.
const FootprintDirVar = "KICAD10_FOOTPRINT_DIR"

// FPLibError is one footprint library problem, with the variable or
// nickname it is about and a fix hint.
type FPLibError struct {
	Code   string `json:"code"`
	Lib    string `json:"lib,omitempty"`  // library nickname
	Name   string `json:"name,omitempty"` // footprint name
	Var    string `json:"var,omitempty"`  // the undefined path variable
	Path   string `json:"path,omitempty"` // table, directory or file involved
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func (e *FPLibError) Error() string { return e.Code + ": " + e.Detail }

// FPLibVar is one path variable value and where it came from: "project"
// (KIPRJMOD), "env", "kicad_common" (Preferences > Configure Paths),
// "install" (the stock footprint directory) or "alias:<VAR>" (a legacy
// KICADn_FOOTPRINT_DIR answered by the current version's value).
type FPLibVar struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// FPLib is one library row after variable expansion.
type FPLib struct {
	Nickname string      `json:"nickname"`
	Type     string      `json:"type"`
	URI      string      `json:"uri"`           // as written in the table
	Dir      string      `json:"dir,omitempty"` // expanded absolute directory ("" when Err)
	Scope    string      `json:"scope"`         // "project" | "global"
	Table    string      `json:"table"`         // the fp-lib-table file holding the row
	Disabled bool        `json:"disabled,omitempty"`
	Err      *FPLibError `json:"error,omitempty"` // why the row cannot be used (type, variable)
}

// FPLibTables is the loaded project + global footprint library tables.
type FPLibTables struct {
	ProjectDir   string              `json:"projectDir"`
	ProjectTable string              `json:"project,omitempty"` // "" when the project has none
	GlobalTable  string              `json:"global,omitempty"`  // "" when none was found
	GlobalSeeded bool                `json:"globalSeeded,omitempty"`
	Vars         map[string]FPLibVar `json:"vars"`               // every variable a row referenced
	Problems     []FPLibError        `json:"problems,omitempty"` // unreadable tables (nested or top level)
	Libs         []FPLib             `json:"-"`                  // all rows, lookup order
}

// FPRef is a resolved footprint.
type FPRef struct {
	Lib    string `json:"lib"`
	Name   string `json:"name"`
	LibDir string `json:"libDir"`
	Path   string `json:"path"` // <LibDir>/<Name>.kicad_mod
	Scope  string `json:"scope"`
}

// fpLibEnv is what table loading reads from the outside world; tests
// replace it.
type fpLibEnv struct {
	lookupEnv func(string) (string, bool)
	goos      string
	home      string
	stockDir  func() string // KiCad's bundled footprint directory ("" if unknown)
}

func defaultFPLibEnv() fpLibEnv {
	home, _ := os.UserHomeDir()
	return fpLibEnv{lookupEnv: os.LookupEnv, goos: runtime.GOOS, home: home, stockDir: StockFootprintDir}
}

func (e fpLibEnv) env(name string) string {
	v, _ := e.lookupEnv(name)
	return strings.TrimSpace(v)
}

// LoadFPLibTables loads <projDir>/fp-lib-table over the user's global
// fp-lib-table. A missing project table is fine; a missing global table is
// read from KiCad's stock template, as KiCad does on first start. Rows that
// cannot be used (unsupported type, undefined variable) are kept with Err so
// Resolve can name the cause; unreadable tables land in Problems.
func LoadFPLibTables(projDir string) (*FPLibTables, error) {
	return loadFPLibTables(projDir, defaultFPLibEnv())
}

func loadFPLibTables(projDir string, env fpLibEnv) (*FPLibTables, error) {
	if projDir == "" {
		return nil, errors.New("fp-lib-table: project directory is required")
	}
	abs, err := filepath.Abs(projDir)
	if err != nil {
		return nil, fmt.Errorf("fp-lib-table: %w", err)
	}
	t := &FPLibTables{ProjectDir: abs, Vars: map[string]FPLibVar{}}
	cfg := kicadConfigDir(env)
	user := readKiCadCommonVars(filepath.Join(cfg, "kicad_common.json"))
	lookup := func(name string) (FPLibVar, bool) { return resolveFPVar(name, abs, user, env) }

	if p := filepath.Join(abs, "fp-lib-table"); fileExists(p) {
		t.ProjectTable = p
		t.loadTable(p, "project", lookup, map[string]bool{})
	}
	global := filepath.Join(cfg, "fp-lib-table")
	if !fileExists(global) {
		global = ""
		if sd := env.stockDir(); sd != "" {
			if p := filepath.Join(filepath.Dir(sd), "template", "fp-lib-table"); fileExists(p) {
				global, t.GlobalSeeded = p, true
			}
		}
	}
	if global != "" {
		t.GlobalTable = global
		t.loadTable(global, "global", lookup, map[string]bool{})
	}
	return t, nil
}

// loadTable appends the rows of one table file (recursing into nested
// "Table" rows) in lookup order.
func (t *FPLibTables) loadTable(path, scope string, lookup func(string) (FPLibVar, bool), seen map[string]bool) {
	key := strings.ToLower(filepath.Clean(path))
	if seen[key] {
		t.Problems = append(t.Problems, FPLibError{Code: ErrFPLibUnresolved, Path: path,
			Detail: "fp-lib-table nests itself", Fix: "remove the recursive Table row"})
		return
	}
	seen[key] = true
	defer delete(seen, key)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Problems = append(t.Problems, FPLibError{Code: ErrFPLibUnresolved, Path: path,
			Detail: "cannot read fp-lib-table: " + err.Error(), Fix: "check the table path in KiCad's Footprint Libraries dialog"})
		return
	}
	root, err := parseSexpr(b)
	if err != nil || root.head() != "fp_lib_table" {
		if err == nil {
			err = fmt.Errorf("top-level form is %q, want fp_lib_table", root.head())
		}
		t.Problems = append(t.Problems, FPLibError{Code: ErrFPLibUnresolved, Path: path,
			Detail: "cannot parse fp-lib-table: " + err.Error(), Fix: "repair or re-save the table from KiCad"})
		return
	}
	dir := filepath.Dir(path)
	for _, row := range root.children("lib") {
		lib := FPLib{Nickname: row.child("name").arg(0), Type: row.child("type").arg(0),
			URI: row.child("uri").arg(0), Scope: scope, Table: path, Disabled: row.child("disabled") != nil}
		if lib.Nickname == "" {
			continue
		}
		expanded, missing := expandFPVars(lib.URI, func(name string) (string, bool) {
			v, ok := lookup(name)
			if ok {
				t.Vars[name] = v
			}
			return v.Value, ok
		})
		if missing != "" {
			lib.Err = &FPLibError{Code: ErrFPLibUnresolved, Lib: lib.Nickname, Var: missing, Path: path,
				Detail: fmt.Sprintf("library %q uses ${%s}, which is not defined", lib.Nickname, missing),
				Fix:    fmt.Sprintf("define %s in KiCad Preferences > Configure Paths or the environment", missing)}
		} else if expanded != "" {
			lib.Dir = filepath.Clean(filepath.FromSlash(expanded))
			if !filepath.IsAbs(lib.Dir) {
				lib.Dir = filepath.Join(dir, lib.Dir)
			}
		}
		switch strings.ToLower(lib.Type) {
		case "table":
			if lib.Disabled {
				continue
			}
			if lib.Err != nil {
				t.Problems = append(t.Problems, *lib.Err)
				continue
			}
			t.loadTable(lib.Dir, scope, lookup, seen)
			continue
		case "kicad":
		default:
			if lib.Err == nil {
				lib.Err = &FPLibError{Code: ErrFPLibType, Lib: lib.Nickname, Path: path,
					Detail: fmt.Sprintf("library %q has type %q; pcbpilot reads only KiCad (.pretty) libraries", lib.Nickname, lib.Type),
					Fix:    "convert the library to a .pretty folder in KiCad's Footprint Editor"}
			}
		}
		t.Libs = append(t.Libs, lib)
	}
}

// Lookup returns the row a nickname resolves to: the first enabled row in
// lookup order (project rows before global ones). disabled reports that
// only disabled rows carry the nickname.
func (t *FPLibTables) Lookup(nickname string) (lib *FPLib, disabled bool) {
	for i := range t.Libs {
		if t.Libs[i].Nickname != nickname {
			continue
		}
		if t.Libs[i].Disabled {
			disabled = true
			continue
		}
		return &t.Libs[i], false
	}
	return nil, disabled
}

// Resolve maps a footprint id "Lib:Name" to its .kicad_mod file. Errors are
// *FPLibError with code E_FP_LIB_UNRESOLVED (nickname, variable or folder),
// E_FP_LIB_TYPE or E_FP_NOT_FOUND.
func (t *FPLibTables) Resolve(id string) (FPRef, error) {
	nick, name, ok := strings.Cut(id, ":")
	if !ok || nick == "" || name == "" {
		return FPRef{}, &FPLibError{Code: ErrFPLibUnresolved, Lib: nick, Name: name,
			Detail: fmt.Sprintf("footprint id %q is not Lib:Name", id),
			Fix:    "set the symbol's Footprint field to Library:Footprint"}
	}
	lib, disabled := t.Lookup(nick)
	if lib == nil {
		detail := fmt.Sprintf("library %q is in neither the project nor the global fp-lib-table", nick)
		fix := "add the library in KiCad Preferences > Manage Footprint Libraries, or fix the Footprint field"
		if disabled {
			detail = fmt.Sprintf("library %q is disabled in every fp-lib-table that lists it", nick)
			fix = "enable the library in KiCad Preferences > Manage Footprint Libraries"
		}
		return FPRef{}, &FPLibError{Code: ErrFPLibUnresolved, Lib: nick, Name: name, Detail: detail, Fix: fix}
	}
	if lib.Err != nil {
		e := *lib.Err
		e.Name = name
		return FPRef{}, &e
	}
	if st, err := os.Stat(lib.Dir); err != nil || !st.IsDir() {
		return FPRef{}, &FPLibError{Code: ErrFPLibUnresolved, Lib: nick, Name: name, Path: lib.Dir,
			Detail: fmt.Sprintf("library %q points at %s, which is not a directory", nick, lib.Dir),
			Fix:    "fix the library URI (" + lib.URI + ") in " + lib.Table}
	}
	p := filepath.Join(lib.Dir, name+".kicad_mod")
	if !fileExists(p) {
		return FPRef{}, &FPLibError{Code: ErrFPNotFound, Lib: nick, Name: name, Path: p,
			Detail: fmt.Sprintf("library %q (%s) has no footprint %q", nick, lib.Dir, name),
			Fix:    "check the footprint name in the symbol's Footprint field"}
	}
	return FPRef{Lib: nick, Name: name, LibDir: lib.Dir, Path: p, Scope: lib.Scope}, nil
}

// Nicknames lists the enabled, usable nicknames per scope (report libs).
func (t *FPLibTables) Nicknames(scope string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range t.Libs {
		if l.Scope == scope && !l.Disabled && !seen[l.Nickname] {
			seen[l.Nickname] = true
			out = append(out, l.Nickname)
		}
	}
	sort.Strings(out)
	return out
}

// KiCadConfigDir is the user's KiCad settings directory for FPLibVersion:
// $KICAD_CONFIG_HOME[/10.0], else %APPDATA%\kicad\10.0 (Windows),
// ~/Library/Preferences/kicad/10.0 (macOS), $XDG_CONFIG_HOME or
// ~/.config + /kicad/10.0 (Linux).
func KiCadConfigDir() string { return kicadConfigDir(defaultFPLibEnv()) }

func kicadConfigDir(env fpLibEnv) string {
	if v := env.env("KICAD_CONFIG_HOME"); v != "" {
		// KiCad appends the settings version to KICAD_CONFIG_HOME; accept a
		// flat directory too (tables directly inside it).
		if d := filepath.Join(v, FPLibVersion); dirExists(d) || !fileExists(filepath.Join(v, "fp-lib-table")) {
			return d
		}
		return v
	}
	switch env.goos {
	case "windows":
		base := env.env("APPDATA")
		if base == "" {
			base = filepath.Join(env.home, "AppData", "Roaming")
		}
		return filepath.Join(base, "kicad", FPLibVersion)
	case "darwin":
		return filepath.Join(env.home, "Library", "Preferences", "kicad", FPLibVersion)
	default:
		base := env.env("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(env.home, ".config")
		}
		return filepath.Join(base, "kicad", FPLibVersion)
	}
}

// readKiCadCommonVars reads environment.vars from kicad_common.json (the
// Configure Paths dialog); absent or unreadable means none.
func readKiCadCommonVars(path string) map[string]string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		Environment struct {
			Vars map[string]string `json:"vars"`
		} `json:"environment"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	return doc.Environment.Vars
}

var legacyFPDirVar = regexp.MustCompile(`^KICAD\d+_FOOTPRINT_DIR$`)

// resolveFPVar answers one path variable: KIPRJMOD is the project; then
// the process environment (KiCad lets it override the settings), the user
// variables of kicad_common.json, and for the footprint directory KiCad's
// install. A legacy KICADn_FOOTPRINT_DIR (old project tables, e.g. the
// video demo's KICAD6_*) falls back to the current version's value.
func resolveFPVar(name, projDir string, user map[string]string, env fpLibEnv) (FPLibVar, bool) {
	if name == "KIPRJMOD" {
		return FPLibVar{Value: projDir, Source: "project"}, true
	}
	if v := env.env(name); v != "" {
		return FPLibVar{Value: v, Source: "env"}, true
	}
	if v := strings.TrimSpace(user[name]); v != "" {
		return FPLibVar{Value: v, Source: "kicad_common"}, true
	}
	if name == FootprintDirVar {
		if d := env.stockDir(); d != "" {
			return FPLibVar{Value: d, Source: "install"}, true
		}
		return FPLibVar{}, false
	}
	if legacyFPDirVar.MatchString(name) {
		if v, ok := resolveFPVar(FootprintDirVar, projDir, user, env); ok {
			return FPLibVar{Value: v.Value, Source: "alias:" + FootprintDirVar}, true
		}
	}
	return FPLibVar{}, false
}

var fpVarRef = regexp.MustCompile(`\$\{([^}]*)\}|\$\(([^)]*)\)`)

// expandFPVars expands ${VAR} and $(VAR) (KiCad accepts both). missing is
// the first variable lookup could not answer ("" when all expanded).
func expandFPVars(s string, lookup func(string) (string, bool)) (out, missing string) {
	for depth := 0; depth < 8 && fpVarRef.MatchString(s); depth++ {
		s = fpVarRef.ReplaceAllStringFunc(s, func(m string) string {
			sub := fpVarRef.FindStringSubmatch(m)
			name := sub[1] + sub[2]
			if v, ok := lookup(name); ok {
				return v
			}
			if missing == "" {
				missing = name
			}
			return ""
		})
		if missing != "" {
			return "", missing
		}
	}
	return s, ""
}

// StockFootprintDir is KiCad's bundled footprint directory ("" if absent),
// found from $PCBPILOT_KICAD_APP, the kicad-cli in use and the default
// install root.
func StockFootprintDir() string {
	var roots []string
	if app := os.Getenv(EnvApp); app != "" {
		roots = append(roots, app)
	}
	for _, f := range []func() (string, error){ResolveFabCLI, KicadCLI} {
		if cli, err := f(); err == nil && cli != "" {
			// <root>/bin/kicad-cli(.exe), <app>/Contents/MacOS/kicad-cli
			roots = append(roots, filepath.Dir(filepath.Dir(cli)))
		}
	}
	roots = append(roots, DefaultApp())
	var cands []string
	for _, r := range roots {
		cands = append(cands, filepath.Join(r, "share", "kicad", "footprints"),
			filepath.Join(r, "SharedSupport", "footprints"), filepath.Join(r, "Contents", "SharedSupport", "footprints"))
	}
	if runtime.GOOS == "linux" {
		cands = append(cands, "/usr/share/kicad/footprints", "/usr/local/share/kicad/footprints")
	}
	for _, c := range cands {
		if dirExists(c) {
			return c
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
