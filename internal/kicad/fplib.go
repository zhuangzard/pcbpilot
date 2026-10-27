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
//
// Fail closed: an unreadable top-level table (or one whose existence
// cannot be checked) is a load error, and a nested
// table that cannot be read blocks every nickname not found before it (it
// might have defined the nickname), so a broken table never silently lets
// a lookup fall through to a different library of the same name.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	ErrFPLibUnresolved = "E_FP_LIB_UNRESOLVED" // unknown nickname, bad variable, bad table or missing directory
	ErrFPNotFound      = "E_FP_NOT_FOUND"      // the library resolved but holds no such footprint

	// WarnFPLibLegacyVar: a legacy KICADn_FOOTPRINT_DIR was answered with
	// the KiCad 10 value (FPLibTables.Warnings, not an error).
	WarnFPLibLegacyVar = "W_FP_LIB_LEGACY_VAR"
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
	Var    string `json:"var,omitempty"`  // the undefined or cyclic path variable
	Path   string `json:"path,omitempty"` // table, directory or file involved
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func (e *FPLibError) Error() string { return e.Code + ": " + e.Detail }

// FPLibVar is one path variable value and where it came from: "project"
// (KIPRJMOD), "env", "kicad_common" (Preferences > Configure Paths),
// "install" (the stock footprint directory) or "alias:<VAR>" (a legacy
// KICADn_FOOTPRINT_DIR answered by the current version's value; always
// paired with a WarnFPLibLegacyVar warning).
type FPLibVar struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// FPLib is one library row after variable expansion. A Blocked row stands
// for a nested table that could not be loaded: its nicknames are unknown.
type FPLib struct {
	Nickname string      `json:"nickname"`
	Type     string      `json:"type"`
	URI      string      `json:"uri"`           // as written in the table
	Dir      string      `json:"dir,omitempty"` // expanded absolute directory ("" when Err)
	Scope    string      `json:"scope"`         // "project" | "global"
	Table    string      `json:"table"`         // the fp-lib-table file holding the row
	Disabled bool        `json:"disabled,omitempty"`
	Blocked  bool        `json:"blocked,omitempty"`
	Err      *FPLibError `json:"error,omitempty"` // why the row cannot be used (type, variable, table)
}

// FPLibTables is the loaded project + global footprint library tables.
type FPLibTables struct {
	ProjectDir   string              `json:"projectDir"`
	ProjectTable string              `json:"project,omitempty"` // "" when the project has none
	GlobalTable  string              `json:"global,omitempty"`  // "" when none was found
	GlobalSeeded bool                `json:"globalSeeded,omitempty"`
	Vars         map[string]FPLibVar `json:"vars"`               // every variable a row referenced
	Problems     []FPLibError        `json:"problems,omitempty"` // nested tables that failed, table cycles
	Warnings     []FPLibError        `json:"warnings,omitempty"` // e.g. legacy variable fallbacks
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
// read from KiCad's stock template, as KiCad does on first start. A table
// that exists but cannot be read or parsed is an *FPLibError
// (E_FP_LIB_UNRESOLVED). Rows that cannot be used (unsupported type, bad
// variable, unloadable nested table) are kept with Err so Resolve names the
// cause.
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
	cfg, ferr := kicadConfigDir(env)
	if ferr != nil {
		return nil, ferr
	}
	user, ferr := readKiCadCommonVars(filepath.Join(cfg, "kicad_common.json"))
	if ferr != nil {
		return nil, ferr
	}
	lookup := func(name string) (FPLibVar, bool) { return resolveFPVar(name, abs, user, env) }

	p := filepath.Join(abs, "fp-lib-table")
	present, ferr := tablePresent(p)
	if ferr != nil {
		return nil, ferr
	}
	if present {
		t.ProjectTable = p
		if err := t.loadTable(p, "project", lookup, nil); err != nil {
			return nil, err
		}
	}
	global := filepath.Join(cfg, "fp-lib-table")
	if present, ferr = tablePresent(global); ferr != nil {
		return nil, ferr
	}
	if !present {
		global = ""
		if sd := env.stockDir(); sd != "" {
			p := filepath.Join(filepath.Dir(sd), "template", "fp-lib-table")
			ok, ferr := strictPresent(p, "KiCad's template fp-lib-table")
			if ferr != nil {
				return nil, ferr
			}
			if ok {
				global, t.GlobalSeeded = p, true
			}
		}
	}
	if global != "" {
		t.GlobalTable = global
		if err := t.loadTable(global, "global", lookup, nil); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// readTable reads and parses one fp-lib-table file.
func readTable(path string) (*sx, *FPLibError) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, &FPLibError{Code: ErrFPLibUnresolved, Path: path,
			Detail: "cannot read fp-lib-table: " + err.Error(), Fix: "check the table path in KiCad's Footprint Libraries dialog"}
	}
	root, err := parseSexpr(b)
	if err == nil && root.head() != "fp_lib_table" {
		err = fmt.Errorf("top-level form is %q, want fp_lib_table", root.head())
	}
	if err != nil {
		return nil, &FPLibError{Code: ErrFPLibUnresolved, Path: path,
			Detail: "cannot parse fp-lib-table: " + err.Error(), Fix: "repair or re-save the table from KiCad"}
	}
	return root, nil
}

// loadTable appends the rows of one table file (recursing into nested
// "Table" rows) in lookup order. It fails only when the file itself cannot
// be read or parsed; a failing nested table becomes a Blocked row.
//
// stack holds the tables being loaded above this one; a table that is the
// same file (os.SameFile: case variants, symlinks, "dir/../dir" spellings)
// as one of them is a cycle.
func (t *FPLibTables) loadTable(path, scope string, lookup func(string) (FPLibVar, bool), stack []os.FileInfo) *FPLibError {
	st, err := fpStat(path)
	if err != nil {
		return &FPLibError{Code: ErrFPLibUnresolved, Path: path,
			Detail: "cannot read fp-lib-table: " + err.Error(), Fix: "check the table path in KiCad's Footprint Libraries dialog"}
	}
	for _, up := range stack {
		if os.SameFile(up, st) {
			// The cycle's rows are already loaded above it: nothing is unknown.
			t.Problems = append(t.Problems, FPLibError{Code: ErrFPLibUnresolved, Path: path,
				Detail: "fp-lib-table nests itself", Fix: "remove the recursive Table row"})
			return nil
		}
	}
	stack = append(stack[:len(stack):len(stack)], st)
	root, ferr := readTable(path)
	if ferr != nil {
		return ferr
	}
	dir := filepath.Dir(path)
	x := &fpVarExpander{lookup: lookup, t: t}
	for _, row := range root.children("lib") {
		lib := FPLib{Nickname: row.child("name").arg(0), Type: row.child("type").arg(0),
			URI: row.child("uri").arg(0), Scope: scope, Table: path, Disabled: row.child("disabled") != nil}
		if lib.Nickname == "" {
			continue
		}
		expanded, verr := x.expand(lib.URI)
		if verr != nil {
			verr.Lib, verr.Path = lib.Nickname, path
			verr.Detail = fmt.Sprintf("library %q: %s", lib.Nickname, verr.Detail)
			lib.Err = verr
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
			if lib.Err == nil {
				lib.Err = t.loadTable(lib.Dir, scope, lookup, stack)
			}
			if lib.Err != nil {
				e := *lib.Err
				e.Detail = fmt.Sprintf("nested table %q (%s): %s", lib.Nickname, lib.URI, e.Detail)
				lib.Err, lib.Blocked = &e, true
				t.Problems = append(t.Problems, e)
				t.Libs = append(t.Libs, lib)
			}
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
	return nil
}

// find returns the row a nickname resolves to: the first enabled row in
// lookup order (project rows before global ones). disabled reports that
// only disabled rows carry the nickname; blocked is the first unloadable
// nested table met before a match, which makes the answer unknown.
func (t *FPLibTables) find(nickname string) (lib, blocked *FPLib, disabled bool) {
	for i := range t.Libs {
		l := &t.Libs[i]
		if l.Blocked {
			if blocked == nil {
				blocked = l
			}
			continue
		}
		if l.Nickname != nickname {
			continue
		}
		if l.Disabled {
			disabled = true
			continue
		}
		return l, blocked, false
	}
	return nil, blocked, disabled
}

// validFootprintName rejects names that are not a single file name inside
// the library folder (separators, traversal, absolute or volume names).
func validFootprintName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:\x00") {
		return false
	}
	return !filepath.IsAbs(name) && filepath.VolumeName(name) == ""
}

// Resolve maps a footprint id "Lib:Name" to its .kicad_mod file. Errors are
// *FPLibError with code E_FP_LIB_UNRESOLVED (malformed id, nickname,
// variable, table or folder), E_FP_LIB_TYPE or E_FP_NOT_FOUND.
func (t *FPLibTables) Resolve(id string) (FPRef, error) {
	nick, name, ok := strings.Cut(id, ":")
	if !ok || nick == "" || !validFootprintName(name) {
		return FPRef{}, &FPLibError{Code: ErrFPLibUnresolved, Lib: nick, Name: name,
			Detail: fmt.Sprintf("footprint id %q is not Lib:Name with a plain footprint name", id),
			Fix:    "set the symbol's Footprint field to Library:Footprint"}
	}
	lib, blocked, disabled := t.find(nick)
	if blocked != nil {
		e := *blocked.Err
		e.Lib, e.Name = nick, name
		e.Detail = fmt.Sprintf("cannot tell where library %q is: %s", nick, blocked.Err.Detail)
		return FPRef{}, &e
	}
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
	if !dirExists(lib.Dir) {
		return FPRef{}, &FPLibError{Code: ErrFPLibUnresolved, Lib: nick, Name: name, Path: lib.Dir,
			Detail: fmt.Sprintf("library %q points at %s, which is not a directory", nick, lib.Dir),
			Fix:    "fix the library URI (" + lib.URI + ") in " + lib.Table}
	}
	p := filepath.Join(lib.Dir, name+".kicad_mod")
	if rel, err := filepath.Rel(lib.Dir, p); err != nil || rel != filepath.Base(p) {
		return FPRef{}, &FPLibError{Code: ErrFPLibUnresolved, Lib: nick, Name: name, Path: p,
			Detail: fmt.Sprintf("footprint %q escapes library %q", name, nick), Fix: "use a plain footprint name"}
	}
	if !fileExists(p) {
		return FPRef{}, &FPLibError{Code: ErrFPNotFound, Lib: nick, Name: name, Path: p,
			Detail: fmt.Sprintf("library %q (%s) has no footprint %q", nick, lib.Dir, name),
			Fix:    "check the footprint name in the symbol's Footprint field"}
	}
	return FPRef{Lib: nick, Name: name, LibDir: lib.Dir, Path: p, Scope: lib.Scope}, nil
}

// Nicknames lists the enabled nicknames per scope (report libs).
func (t *FPLibTables) Nicknames(scope string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range t.Libs {
		if l.Scope == scope && !l.Disabled && !l.Blocked && !seen[l.Nickname] {
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
// A settings path that exists but cannot be checked is an *FPLibError.
func KiCadConfigDir() (string, error) {
	d, err := kicadConfigDir(defaultFPLibEnv())
	if err != nil {
		return "", err
	}
	return d, nil
}

func kicadConfigDir(env fpLibEnv) (string, *FPLibError) {
	if v := env.env("KICAD_CONFIG_HOME"); v != "" {
		// KiCad appends the settings version to KICAD_CONFIG_HOME; accept a
		// flat directory (tables directly inside it) only when there is no
		// version directory at all — a broken one is an error, not a
		// reason to fall back.
		d := filepath.Join(v, FPLibVersion)
		ok, err := strictPresent(d, "KiCad settings directory")
		if err != nil || ok {
			return d, err
		}
		flat, err := tablePresent(filepath.Join(v, "fp-lib-table"))
		if err != nil {
			return "", err
		}
		if flat {
			return v, nil
		}
		return d, nil
	}
	switch env.goos {
	case "windows":
		base := env.env("APPDATA")
		if base == "" {
			base = filepath.Join(env.home, "AppData", "Roaming")
		}
		return filepath.Join(base, "kicad", FPLibVersion), nil
	case "darwin":
		return filepath.Join(env.home, "Library", "Preferences", "kicad", FPLibVersion), nil
	default:
		base := env.env("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(env.home, ".config")
		}
		return filepath.Join(base, "kicad", FPLibVersion), nil
	}
}

// readKiCadCommonVars reads environment.vars from kicad_common.json (the
// Configure Paths dialog). Only an absent file means "no user variables";
// an unreadable or invalid file is an error (its variables may decide
// where libraries are).
func readKiCadCommonVars(path string) (map[string]string, *FPLibError) {
	ok, ferr := strictPresent(path, "kicad_common.json")
	if ferr != nil || !ok {
		return nil, ferr
	}
	b, err := fpReadFile(path)
	if err != nil {
		return nil, &FPLibError{Code: ErrFPLibUnresolved, Path: path,
			Detail: "cannot read kicad_common.json: " + err.Error(), Fix: "check the file's permissions"}
	}
	var doc struct {
		Environment struct {
			Vars map[string]string `json:"vars"`
		} `json:"environment"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, &FPLibError{Code: ErrFPLibUnresolved, Path: path,
			Detail: "cannot parse kicad_common.json: " + err.Error(), Fix: "repair the file or re-save Preferences > Configure Paths in KiCad"}
	}
	return doc.Environment.Vars, nil
}

var legacyFPDirVar = regexp.MustCompile(`^KICAD\d+_FOOTPRINT_DIR$`)

// resolveFPVar answers one path variable: KIPRJMOD is the project; then
// the process environment (KiCad lets it override the settings), the user
// variables of kicad_common.json, and for the footprint directory KiCad's
// install.
//
// A legacy KICADn_FOOTPRINT_DIR (old project tables; the shipped video
// demo still uses ${KICAD6_FOOTPRINT_DIR}) that nobody defines is answered
// with the KiCad 10 value, source "alias:KICAD10_FOOTPRINT_DIR", and the
// caller records a WarnFPLibLegacyVar warning. KiCad 10 itself treats these
// names specially — kicommon.dll (10.0.6) carries the pattern
// `^\$\{KICAD\d+_FOOTPRINT_DIR\}`, "KICAD*_FOOTPRINT_DIR" and the literal
// KICAD6/7/8_FOOTPRINT_DIR names — but its exact rule is not documented, so
// the substitution is reported rather than silent.
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

// fpVarExpander expands ${VAR} and $(VAR) (KiCad accepts both), including
// references inside variable values, and records each variable used in
// t.Vars (and a warning per legacy alias).
type fpVarExpander struct {
	lookup func(string) (FPLibVar, bool)
	t      *FPLibTables
}

// expand returns the fully expanded text, or an E_FP_LIB_UNRESOLVED error
// naming the variable that is undefined or part of a reference cycle, or
// the text when a "${" / "$(" reference is left unterminated.
func (x *fpVarExpander) expand(s string) (string, *FPLibError) {
	out, err := x.expandIn(s, nil)
	if err == nil && (strings.Contains(out, "${") || strings.Contains(out, "$(")) {
		err = &FPLibError{Code: ErrFPLibUnresolved, Detail: fmt.Sprintf("unterminated variable reference in %q", s),
			Fix: "close the ${...} reference in the library URI"}
	}
	return out, err
}

func (x *fpVarExpander) expandIn(s string, stack []string) (string, *FPLibError) {
	var b strings.Builder
	last := 0
	for _, m := range fpVarRef.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(s[last:m[0]])
		last = m[1]
		var name string
		if m[2] >= 0 {
			name = s[m[2]:m[3]] // ${NAME}
		} else {
			name = s[m[4]:m[5]] // $(NAME)
		}
		for _, on := range stack {
			if on == name {
				chain := strings.Join(append(append([]string{}, stack...), name), " -> ")
				return "", &FPLibError{Code: ErrFPLibUnresolved, Var: name,
					Detail: fmt.Sprintf("${%s} refers to itself (%s)", name, chain),
					Fix:    fmt.Sprintf("give %s a value without a reference back to itself in KiCad Preferences > Configure Paths or the environment", name)}
			}
		}
		v, ok := x.lookup(name)
		if !ok {
			return "", &FPLibError{Code: ErrFPLibUnresolved, Var: name,
				Detail: fmt.Sprintf("uses ${%s}, which is not defined", name),
				Fix:    fmt.Sprintf("define %s in KiCad Preferences > Configure Paths or the environment", name)}
		}
		val, err := x.expandIn(v.Value, append(stack, name))
		if err != nil {
			return "", err
		}
		x.record(name, v)
		b.WriteString(val)
	}
	b.WriteString(s[last:])
	return b.String(), nil
}

func (x *fpVarExpander) record(name string, v FPLibVar) {
	if _, done := x.t.Vars[name]; done {
		return
	}
	x.t.Vars[name] = v
	if strings.HasPrefix(v.Source, "alias:") {
		x.t.Warnings = append(x.t.Warnings, FPLibError{Code: WarnFPLibLegacyVar, Var: name, Path: v.Value,
			Detail: fmt.Sprintf("${%s} is not defined; used %s (%s) instead", name, strings.TrimPrefix(v.Source, "alias:"), v.Value),
			Fix:    fmt.Sprintf("define %s, or update the library URIs to ${%s}", name, FootprintDirVar)})
	}
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

// fpStat / fpLstat / fpReadFile are the os functions; tests inject failures.
var (
	fpStat     = os.Stat
	fpLstat    = os.Lstat
	fpReadFile = os.ReadFile
)

// tablePresent reports whether a top-level fp-lib-table exists. Only a
// path that does not exist at all counts as absent; a dangling symlink, a
// permission or I/O error is an error, so a table KiCad would read is never
// silently replaced by the next fallback.
func tablePresent(p string) (bool, *FPLibError) { return strictPresent(p, "fp-lib-table") }

// strictPresent is tablePresent for any settings path (what names it in
// the error).
func strictPresent(p, what string) (bool, *FPLibError) {
	_, err := fpStat(p)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		if _, lerr := fpLstat(p); lerr == nil {
			return false, &FPLibError{Code: ErrFPLibUnresolved, Path: p,
				Detail: what + " is a symlink to a missing file", Fix: "repair or remove the symlink"}
		} else if !errors.Is(lerr, fs.ErrNotExist) {
			return false, &FPLibError{Code: ErrFPLibUnresolved, Path: p,
				Detail: "cannot check " + what + ": " + lerr.Error(), Fix: "check the file's permissions"}
		}
		return false, nil
	}
	return false, &FPLibError{Code: ErrFPLibUnresolved, Path: p,
		Detail: "cannot check " + what + ": " + err.Error(), Fix: "check the file's permissions"}
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
