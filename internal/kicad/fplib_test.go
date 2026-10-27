package kicad

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fpFixture is a synthetic KiCad setup: a config dir (global table +
// kicad_common.json), a stock footprint dir and a project dir.
type fpFixture struct {
	t                    *testing.T
	root, cfg, stock, pj string
	env                  map[string]string
}

func newFPFixture(t *testing.T) *fpFixture {
	t.Helper()
	root := t.TempDir()
	f := &fpFixture{t: t, root: root, cfg: filepath.Join(root, "cfg"), stock: filepath.Join(root, "share", "kicad", "footprints"),
		pj: filepath.Join(root, "proj")}
	for _, d := range []string{filepath.Join(f.cfg, FPLibVersion), f.stock, f.pj} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.env = map[string]string{"KICAD_CONFIG_HOME": f.cfg}
	return f
}

func (f *fpFixture) write(path, text string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// lib makes <dir>/<name>.pretty holding the given footprints.
func (f *fpFixture) lib(dir string, fps ...string) string {
	f.t.Helper()
	for _, fp := range fps {
		f.write(filepath.Join(dir, fp+".kicad_mod"), "(footprint \""+fp+"\")\n")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return dir
}

func (f *fpFixture) load() *FPLibTables {
	f.t.Helper()
	env := fpLibEnv{
		lookupEnv: func(k string) (string, bool) { v, ok := f.env[k]; return v, ok },
		goos:      runtime.GOOS, home: f.root,
		stockDir: func() string { return f.stock },
	}
	tabs, err := loadFPLibTables(f.pj, env)
	if err != nil {
		f.t.Fatal(err)
	}
	return tabs
}

func table(rows ...string) string {
	return "(fp_lib_table\n\t(version 7)\n\t" + strings.Join(rows, "\n\t") + "\n)\n"
}

func row(name, typ, uri string, extra ...string) string {
	return `(lib (name "` + name + `")(type "` + typ + `")(uri "` + uri + `")(options "")(descr "")` + strings.Join(extra, "") + `)`
}

func fpErrCode(t *testing.T, err error) *FPLibError {
	t.Helper()
	var fe *FPLibError
	if !errors.As(err, &fe) {
		t.Fatalf("want *FPLibError, got %v", err)
	}
	return fe
}

func TestFPLibVariables(t *testing.T) {
	f := newFPFixture(t)
	f.lib(filepath.Join(f.stock, "Resistor_SMD.pretty"), "R_0603")
	f.lib(filepath.Join(f.pj, "local.pretty"), "Local1")
	f.lib(filepath.Join(f.pj, "paren.pretty"), "Paren1")
	userDir := f.lib(filepath.Join(f.root, "user", "mine.pretty"), "Mine1")
	envDir := f.lib(filepath.Join(f.root, "envlib", "env.pretty"), "Env1")
	f.write(filepath.Join(f.cfg, FPLibVersion, "kicad_common.json"),
		`{"environment":{"vars":{"MY_LIBS":`+jsonString(filepath.ToSlash(filepath.Dir(userDir)))+`,"ENV_LIBS":"/nowhere"}}}`)
	f.env["ENV_LIBS"] = filepath.Dir(envDir) // the environment beats kicad_common.json
	f.write(filepath.Join(f.cfg, FPLibVersion, "fp-lib-table"), table(
		row("Resistor_SMD", "KiCad", "${KICAD10_FOOTPRINT_DIR}/Resistor_SMD.pretty"),
		row("Legacy_R", "KiCad", "${KICAD6_FOOTPRINT_DIR}/Resistor_SMD.pretty"),
		row("Mine", "KiCad", "${MY_LIBS}/mine.pretty"),
		row("Env", "KiCad", "${ENV_LIBS}/env.pretty"),
	))
	f.write(filepath.Join(f.pj, "fp-lib-table"), table(
		row("Local", "KiCad", "${KIPRJMOD}/local.pretty"),
		row("Paren", "KiCad", "$(KIPRJMOD)/paren.pretty"), // interf_u demo spelling
	))
	tabs := f.load()
	for id, want := range map[string]string{
		"Resistor_SMD:R_0603": filepath.Join(f.stock, "Resistor_SMD.pretty", "R_0603.kicad_mod"),
		"Legacy_R:R_0603":     filepath.Join(f.stock, "Resistor_SMD.pretty", "R_0603.kicad_mod"),
		"Local:Local1":        filepath.Join(f.pj, "local.pretty", "Local1.kicad_mod"),
		"Paren:Paren1":        filepath.Join(f.pj, "paren.pretty", "Paren1.kicad_mod"),
		"Mine:Mine1":          filepath.Join(userDir, "Mine1.kicad_mod"),
		"Env:Env1":            filepath.Join(envDir, "Env1.kicad_mod"),
	} {
		ref, err := tabs.Resolve(id)
		if err != nil {
			t.Errorf("Resolve(%s): %v", id, err)
			continue
		}
		if ref.Path != want {
			t.Errorf("Resolve(%s) = %s, want %s", id, ref.Path, want)
		}
	}
	srcs := map[string]string{"KICAD10_FOOTPRINT_DIR": "install", "KICAD6_FOOTPRINT_DIR": "alias:KICAD10_FOOTPRINT_DIR",
		"KIPRJMOD": "project", "MY_LIBS": "kicad_common", "ENV_LIBS": "env"}
	for name, src := range srcs {
		if got := tabs.Vars[name].Source; got != src {
			t.Errorf("Vars[%s].Source = %q, want %q", name, got, src)
		}
	}
	// KICAD10_FOOTPRINT_DIR from kicad_common.json beats the install.
	other := f.lib(filepath.Join(f.root, "other", "Resistor_SMD.pretty"), "R_0603")
	f.write(filepath.Join(f.cfg, FPLibVersion, "kicad_common.json"),
		`{"environment":{"vars":{"KICAD10_FOOTPRINT_DIR":`+jsonString(filepath.Dir(other))+`}}}`)
	if ref, err := f.load().Resolve("Resistor_SMD:R_0603"); err != nil || ref.LibDir != other {
		t.Errorf("kicad_common KICAD10_FOOTPRINT_DIR: got %+v, %v; want dir %s", ref, err, other)
	}
}

func jsonString(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }

func TestFPLibProjectOverridesGlobalAndDisabled(t *testing.T) {
	f := newFPFixture(t)
	g := f.lib(filepath.Join(f.root, "g", "Same.pretty"), "X", "GOnly")
	p := f.lib(filepath.Join(f.pj, "Same.pretty"), "X")
	gd := f.lib(filepath.Join(f.root, "g", "Dis.pretty"), "D")
	f.lib(filepath.Join(f.pj, "Dis.pretty"), "D")
	f.write(filepath.Join(f.cfg, FPLibVersion, "fp-lib-table"), table(
		row("Same", "KiCad", filepath.ToSlash(g)),
		row("Dis", "KiCad", filepath.ToSlash(gd)),
		row("Off", "KiCad", filepath.ToSlash(gd), "(disabled)"),
	))
	f.write(filepath.Join(f.pj, "fp-lib-table"), table(
		row("Same", "KiCad", "${KIPRJMOD}/Same.pretty"),
		row("Dis", "KiCad", "${KIPRJMOD}/Dis.pretty", "(disabled)"),
	))
	tabs := f.load()
	if ref, err := tabs.Resolve("Same:X"); err != nil || ref.LibDir != p || ref.Scope != "project" {
		t.Errorf("project row must hide the global one: %+v %v", ref, err)
	}
	// The project "Same" hides the global library entirely (KiCad does not
	// merge libraries of the same nickname).
	if _, err := tabs.Resolve("Same:GOnly"); fpErrCode(t, err).Code != ErrFPNotFound {
		t.Errorf("Same:GOnly: %v, want %s", err, ErrFPNotFound)
	}
	// A disabled project row falls through to the global table.
	if ref, err := tabs.Resolve("Dis:D"); err != nil || ref.LibDir != gd || ref.Scope != "global" {
		t.Errorf("disabled project row must fall through: %+v %v", ref, err)
	}
	_, err := tabs.Resolve("Off:D")
	if fe := fpErrCode(t, err); fe.Code != ErrFPLibUnresolved || !strings.Contains(fe.Detail, "disabled") {
		t.Errorf("Off:D: %+v", fe)
	}
	if got := tabs.Nicknames("global"); strings.Join(got, ",") != "Dis,Same" {
		t.Errorf("Nicknames(global) = %v", got)
	}
}

func TestFPLibNestedTable(t *testing.T) {
	f := newFPFixture(t)
	f.lib(filepath.Join(f.stock, "Capacitor_SMD.pretty"), "C_0603")
	inner := filepath.Join(f.root, "template", "fp-lib-table")
	f.write(inner, table(
		row("Capacitor_SMD", "KiCad", "${KICAD10_FOOTPRINT_DIR}/Capacitor_SMD.pretty"),
		row("Deeper", "Table", filepath.ToSlash(filepath.Join(f.root, "deeper-table"))),
	))
	f.write(filepath.Join(f.root, "deeper-table"), table(
		row("Deep", "KiCad", filepath.ToSlash(f.lib(filepath.Join(f.root, "deep.pretty"), "D1"))),
		row("Loop", "Table", filepath.ToSlash(inner)), // cycle back to the parent
	))
	f.write(filepath.Join(f.cfg, FPLibVersion, "fp-lib-table"), table(
		row("KiCad", "Table", filepath.ToSlash(inner)),
		row("SkipMe", "Table", "/no/such/table", "(disabled)"),
	))
	tabs := f.load()
	for _, id := range []string{"Capacitor_SMD:C_0603", "Deep:D1"} {
		if ref, err := tabs.Resolve(id); err != nil || ref.Scope != "global" {
			t.Errorf("Resolve(%s) through nested tables: %+v %v", id, ref, err)
		}
	}
	if _, disabled := tabs.Lookup("KiCad"); disabled {
		t.Errorf("a Table row is not itself a library")
	}
	if len(tabs.Problems) != 1 || !strings.Contains(tabs.Problems[0].Detail, "nests itself") {
		t.Errorf("want one cycle problem, got %+v", tabs.Problems)
	}
}

func TestFPLibErrors(t *testing.T) {
	f := newFPFixture(t)
	f.lib(filepath.Join(f.pj, "ok.pretty"), "Here")
	f.write(filepath.Join(f.pj, "fp-lib-table"), table(
		row("Undef", "KiCad", "${NO_SUCH_VAR}/x.pretty"),
		row("Eagle", "Eagle", "${KIPRJMOD}/x.lbr"),
		row("Gone", "KiCad", "${KIPRJMOD}/gone.pretty"),
		row("Ok", "KiCad", "${KIPRJMOD}/ok.pretty"),
	))
	// No global table and no template: only the project table counts.
	f.stock = filepath.Join(f.root, "nostock")
	tabs := f.load()
	if tabs.GlobalTable != "" {
		t.Errorf("GlobalTable = %q, want none", tabs.GlobalTable)
	}
	cases := []struct{ id, code, varName string }{
		{"Undef:X", ErrFPLibUnresolved, "NO_SUCH_VAR"},
		{"Eagle:X", ErrFPLibType, ""},
		{"Gone:X", ErrFPLibUnresolved, ""},
		{"Nope:X", ErrFPLibUnresolved, ""},
		{"Ok:Missing", ErrFPNotFound, ""},
		{"NoColon", ErrFPLibUnresolved, ""},
		{"Ok:", ErrFPLibUnresolved, ""},
	}
	for _, c := range cases {
		_, err := tabs.Resolve(c.id)
		fe := fpErrCode(t, err)
		if fe.Code != c.code || fe.Var != c.varName || fe.Fix == "" {
			t.Errorf("Resolve(%s) = %+v, want code %s var %q and a fix", c.id, fe, c.code, c.varName)
		}
	}
	if !strings.Contains(fpErrCode(t, func() error { _, e := tabs.Resolve("Undef:X"); return e }()).Detail, "${NO_SUCH_VAR}") {
		t.Errorf("unresolved detail must name the variable")
	}
	if _, err := tabs.Resolve("Ok:Here"); err != nil {
		t.Errorf("Ok:Here: %v", err)
	}
}

func TestFPLibGlobalSeededFromTemplate(t *testing.T) {
	f := newFPFixture(t)
	f.lib(filepath.Join(f.stock, "Diode_SMD.pretty"), "D_0603")
	f.write(filepath.Join(filepath.Dir(f.stock), "template", "fp-lib-table"),
		table(row("Diode_SMD", "KiCad", "${KICAD10_FOOTPRINT_DIR}/Diode_SMD.pretty")))
	tabs := f.load()
	if !tabs.GlobalSeeded {
		t.Fatalf("missing global table must fall back to the stock template")
	}
	if _, err := tabs.Resolve("Diode_SMD:D_0603"); err != nil {
		t.Error(err)
	}
}

func TestKiCadConfigDir(t *testing.T) {
	root := t.TempDir()
	flat := filepath.Join(root, "flat")
	if err := os.MkdirAll(flat, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flat, "fp-lib-table"), []byte(table()), 0o644); err != nil {
		t.Fatal(err)
	}
	mk := func(goos string, env map[string]string) fpLibEnv {
		return fpLibEnv{lookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }, goos: goos, home: "/home/u",
			stockDir: func() string { return "" }}
	}
	cases := []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"windows", map[string]string{"APPDATA": `C:\AppData\Roaming`}, filepath.Join(`C:\AppData\Roaming`, "kicad", "10.0")},
		{"darwin", nil, filepath.Join("/home/u", "Library", "Preferences", "kicad", "10.0")},
		{"linux", nil, filepath.Join("/home/u", ".config", "kicad", "10.0")},
		{"linux", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, filepath.Join("/xdg", "kicad", "10.0")},
		{"linux", map[string]string{"KICAD_CONFIG_HOME": "/kch"}, filepath.Join("/kch", "10.0")},
		{"windows", map[string]string{"KICAD_CONFIG_HOME": flat}, flat}, // tables directly inside
	}
	for _, c := range cases {
		if got := kicadConfigDir(mk(c.goos, c.env)); got != c.want {
			t.Errorf("%s %v: %s, want %s", c.goos, c.env, got, c.want)
		}
	}
}

// TestFPLibRealKiCadDemos reads this machine's real global table and
// KiCad's shipped demo projects (read-only): every footprint the ecc83
// board uses must resolve, project ("Footprints:") and stock alike, and the
// video demo's legacy ${KICAD6_FOOTPRINT_DIR} rows must resolve too.
func TestFPLibRealKiCadDemos(t *testing.T) {
	stock := StockFootprintDir()
	if stock == "" {
		t.Skip("KiCad stock footprints not installed")
	}
	demos := filepath.Join(filepath.Dir(stock), "demos")
	if !dirExists(filepath.Join(demos, "ecc83")) {
		t.Skip("KiCad demos not installed")
	}
	tabs, err := LoadFPLibTables(filepath.Join(demos, "ecc83"))
	if err != nil {
		t.Fatal(err)
	}
	if tabs.GlobalTable == "" {
		t.Skip("no global fp-lib-table")
	}
	bd, err := ReadBoard(filepath.Join(demos, "ecc83", "ecc83-pp.kicad_pcb"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bd.Footprints) == 0 {
		t.Fatal("ecc83 board has no footprints")
	}
	for _, fp := range bd.Footprints {
		ref, err := tabs.Resolve(fp.LibID)
		if err != nil {
			t.Errorf("%s %s: %v", fp.Ref, fp.LibID, err)
			continue
		}
		if wantScope := map[bool]string{true: "project", false: "global"}[strings.HasPrefix(fp.LibID, "Footprints:")]; ref.Scope != wantScope {
			t.Errorf("%s: scope %s, want %s", fp.LibID, ref.Scope, wantScope)
		}
	}
	vt, err := LoadFPLibTables(filepath.Join(demos, "video"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vt.Resolve("Connector_PinHeader_2.54mm:PinHeader_1x02_P2.54mm_Vertical"); err != nil {
		t.Errorf("video demo legacy KICAD6 row: %v", err)
	}
}
