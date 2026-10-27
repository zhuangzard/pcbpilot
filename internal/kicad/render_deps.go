package kicad

// render_deps.go — the files a `kicad render` temp copy needs: the project
// directory's KiCad files plus every sheet, library and drawing sheet the
// project reaches outside it.
//
// Relative references (a sheet "../common/power.kicad_sch", a library
// "${KIPRJMOD}/../libs/x.kicad_sym") keep working because the copy mirrors
// the directory layout below the common ancestor of the project and all of
// them, so no file is rewritten (a rewritten Sheetfile would print in the
// sheet symbol). Libraries named by an absolute path or a KiCad path
// variable are only read by kicad-cli and stay where they are. Anything that
// cannot be found — an absolute sheet path, an undefined variable, a missing
// file — refuses the render (RENDER_DEPENDENCY_UNRESOLVED) instead of
// plotting a page with holes.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// renderDeps is the dependency closure of one render input.
type renderDeps struct {
	ProjDir    string   // absolute
	Root       string   // common ancestor of ProjDir and every mirrored file
	Mirror     []string // absolute files / directories copied below Root
	InPlace    []string // libraries read where they are (absolute or KiCad variable)
	Unresolved []string
}

var kicadBuiltinVarRE = regexp.MustCompile(`^KICAD\d*_[A-Z0-9_]+$`)

func (d *renderDeps) mirror(p string) {
	for _, m := range d.Mirror {
		if m == p {
			return
		}
	}
	d.Mirror = append(d.Mirror, p)
}

// collectRenderDeps resolves the closure of in (.kicad_sch root or
// .kicad_pcb).
func collectRenderDeps(in string) (*renderDeps, error) {
	abs, err := filepath.Abs(in)
	if err != nil {
		return nil, err
	}
	d := &renderDeps{ProjDir: filepath.Dir(abs)}
	if strings.EqualFold(filepath.Ext(abs), ".kicad_sch") {
		d.walkSheets(abs, map[string]bool{}, 0)
	}
	lookup := func(name string) (FPLibVar, bool) {
		if name == "KIPRJMOD" {
			return FPLibVar{Value: d.ProjDir, Source: "project"}, true
		}
		if v, ok := os.LookupEnv(name); ok && strings.TrimSpace(v) != "" {
			return FPLibVar{Value: strings.TrimSpace(v), Source: "env"}, true
		}
		return FPLibVar{}, false
	}
	for _, t := range []struct{ file, head string }{{"sym-lib-table", "sym_lib_table"}, {"fp-lib-table", "fp_lib_table"}} {
		d.libTable(filepath.Join(d.ProjDir, t.file), t.head, lookup)
	}
	d.worksheets(strings.TrimSuffix(abs, filepath.Ext(abs))+".kicad_pro", lookup)
	d.Root = d.ProjDir
	for _, m := range d.Mirror {
		d.Root = commonDir(d.Root, m)
	}
	if d.Root == "" {
		d.Unresolved = append(d.Unresolved, "dependencies on different drives: "+strings.Join(d.Mirror, ", "))
	}
	sort.Strings(d.Unresolved)
	return d, nil
}

// walkSheets adds every sheet file below file; a relative Sheetfile is
// resolved against the parent sheet's directory (as ParseSchHierarchy does).
func (d *renderDeps) walkSheets(file string, seen map[string]bool, depth int) {
	if seen[file] || depth > 16 {
		return
	}
	seen[file] = true
	d.mirror(file)
	src, err := os.ReadFile(file)
	if err != nil {
		d.Unresolved = append(d.Unresolved, fmt.Sprintf("sheet %s: %v", file, err))
		return
	}
	n, err := parseSx(string(src))
	if err != nil {
		d.Unresolved = append(d.Unresolved, fmt.Sprintf("sheet %s: %v", file, err))
		return
	}
	for _, c := range n.list {
		if c.head() != "sheet" {
			continue
		}
		sub := propVal(c, "Sheetfile")
		switch {
		case sub == "":
			continue
		case strings.Contains(sub, "${") || strings.Contains(sub, "$("):
			d.Unresolved = append(d.Unresolved, fmt.Sprintf("sheet %q in %s: path variables in a sheet file name are not resolved", sub, filepath.Base(file)))
			continue
		case filepath.IsAbs(sub) || filepath.IsAbs(filepath.FromSlash(sub)) || strings.HasPrefix(sub, `\`) || strings.HasPrefix(sub, "/"):
			d.Unresolved = append(d.Unresolved, fmt.Sprintf("sheet %q in %s: absolute sheet path (make it relative to the project)", sub, filepath.Base(file)))
			continue
		}
		p := filepath.Join(filepath.Dir(file), filepath.FromSlash(sub))
		if _, err := os.Stat(p); err != nil {
			d.Unresolved = append(d.Unresolved, fmt.Sprintf("sheet %q in %s: not found next to it", sub, filepath.Base(file)))
			continue
		}
		d.walkSheets(p, seen, depth+1)
	}
}

// libTable checks every enabled row of a project library table.
func (d *renderDeps) libTable(path, head string, lookup func(string) (FPLibVar, bool)) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		d.Unresolved = append(d.Unresolved, fmt.Sprintf("%s: %v", filepath.Base(path), err))
		return
	}
	root, err := parseSexpr(b)
	if err == nil && root.head() != head {
		err = fmt.Errorf("top-level form is %q, want %s", root.head(), head)
	}
	if err != nil {
		d.Unresolved = append(d.Unresolved, fmt.Sprintf("%s: %v", filepath.Base(path), err))
		return
	}
	x := &fpVarExpander{lookup: lookup, t: &FPLibTables{Vars: map[string]FPLibVar{}}}
	for _, row := range root.children("lib") {
		name, uri := row.child("name").arg(0), row.child("uri").arg(0)
		if name == "" || row.child("disabled") != nil || uri == "" {
			continue
		}
		where := fmt.Sprintf("%s library %q (%s)", filepath.Base(path), name, uri)
		d.dependency(where, uri, x, filepath.Dir(path), false)
	}
}

// worksheets adds the drawing sheets a .kicad_pro names
// (page_layout_descr_file, schematic and board).
func (d *renderDeps) worksheets(pro string, lookup func(string) (FPLibVar, bool)) {
	b, err := os.ReadFile(pro)
	if err != nil {
		return // no project file: KiCad's default drawing sheet
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return
	}
	x := &fpVarExpander{lookup: lookup, t: &FPLibTables{Vars: map[string]FPLibVar{}}}
	for _, sec := range []string{"schematic", "pcbnew"} {
		m, _ := doc[sec].(map[string]any)
		ws, _ := m["page_layout_descr_file"].(string)
		if ws != "" {
			d.dependency(fmt.Sprintf("%s drawing sheet (%s)", filepath.Base(pro), ws), ws, x, d.ProjDir, true)
		}
	}
}

// dependency resolves one referenced path: relative (or ${KIPRJMOD}-based)
// → mirrored; absolute or another variable → read in place unless mirror
// is required. A KiCad built-in variable (${KICAD10_SYMBOL_DIR}) that the
// environment does not define is KiCad's own install and is not checked.
func (d *renderDeps) dependency(where, ref string, x *fpVarExpander, base string, mustMirror bool) {
	p, verr := x.expand(ref)
	if verr != nil {
		if verr.Var != "" && kicadBuiltinVarRE.MatchString(verr.Var) {
			return
		}
		d.Unresolved = append(d.Unresolved, fmt.Sprintf("%s: %s", where, verr.Detail))
		return
	}
	p = filepath.Clean(filepath.FromSlash(p))
	projectRelative := !filepath.IsAbs(p) || strings.HasPrefix(ref, "${KIPRJMOD}") || strings.HasPrefix(ref, "$(KIPRJMOD)")
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	if _, err := os.Stat(p); err != nil {
		d.Unresolved = append(d.Unresolved, fmt.Sprintf("%s: %s not found", where, p))
		return
	}
	switch {
	case projectRelative:
		d.mirror(p)
	case mustMirror:
		d.Unresolved = append(d.Unresolved, fmt.Sprintf("%s: absolute path (make it relative to the project)", where))
	default:
		d.InPlace = append(d.InPlace, p)
	}
}

// commonDir is the deepest directory holding both a (a directory) and b,
// "" when they share no root (different drives).
func commonDir(a, b string) string {
	for dir := a; ; {
		if rel, err := filepath.Rel(dir, b); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return dir
		}
		up := filepath.Dir(dir)
		if up == dir {
			return ""
		}
		dir = up
	}
}

// copyRenderDeps copies the project directory's KiCad files (and 3D models
// when models) plus every mirrored dependency below dst, keeping their
// position relative to d.Root, and returns where the project directory
// landed.
func copyRenderDeps(d *renderDeps, dst string, models bool) (string, error) {
	var total int64
	done := map[string]bool{}
	place := func(src string) (string, error) {
		rel, err := filepath.Rel(d.Root, src)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%s is outside %s", src, d.Root)
		}
		return filepath.Join(dst, rel), nil
	}
	copyOne := func(src string) error {
		if done[src] {
			return nil
		}
		done[src] = true
		info, err := os.Stat(src)
		if err != nil {
			return err
		}
		if total += info.Size(); total > renderCopyLimit {
			return renderErr("RENDER_PROJECT_TOO_BIG", "render a directory holding only the project", "the project and its dependencies hold more than 1 GiB of KiCad files")
		}
		to, err := place(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		return copyFile(src, to)
	}
	walk := func(dir string, filter bool) error {
		return filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := e.Name()
			if e.IsDir() {
				if p != dir && (strings.HasPrefix(name, ".") || strings.HasSuffix(name, "-backups")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !e.Type().IsRegular() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(name))
			if filter && !renderCopyExt[ext] && !(models && renderModelExt[ext]) && name != "sym-lib-table" && name != "fp-lib-table" {
				return nil
			}
			return copyOne(p)
		})
	}
	if err := walk(d.ProjDir, true); err != nil {
		return "", err
	}
	for _, m := range d.Mirror {
		st, err := os.Stat(m)
		if err != nil {
			return "", err
		}
		if st.IsDir() { // a .pretty library
			err = walk(m, false)
		} else {
			err = copyOne(m)
		}
		if err != nil {
			return "", err
		}
	}
	return place(d.ProjDir)
}
