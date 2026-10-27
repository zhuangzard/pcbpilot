package kicad

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// EnvKiCadDemos points the round-trip corpus tests at KiCad's shipped demo
// projects; by default they are looked up next to kicad-cli. The demos are
// only read (netlist checks work on a temp copy) and never committed.
const EnvKiCadDemos = "PCBPILOT_KICAD_DEMOS"

func kicadDemosDir() string {
	var cands []string
	if v := os.Getenv(EnvKiCadDemos); v != "" {
		cands = append(cands, v)
	}
	if cli, err := KicadCLI(); err == nil {
		bin := filepath.Dir(cli)
		cands = append(cands, filepath.Join(bin, "..", "share", "kicad", "demos"), // Windows, Linux
			filepath.Join(bin, "..", "SharedSupport", "demos")) // macOS bundle
	}
	if runtime.GOOS == "windows" {
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			m, _ := filepath.Glob(filepath.Join(la, "Programs", "KiCad", "*", "share", "kicad", "demos"))
			cands = append(cands, m...)
		}
		m, _ := filepath.Glob(`C:\Program Files\KiCad\*\share\kicad\demos`)
		cands = append(cands, m...)
	}
	cands = append(cands, "/Applications/KiCad/KiCad.app/Contents/SharedSupport/demos", "/usr/share/kicad/demos")
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return ""
}

func demoFiles(t *testing.T, exts ...string) (string, []string) {
	t.Helper()
	if testing.Short() {
		t.Skip("KiCad demo corpus is slow; skipped with -short")
	}
	dir := kicadDemosDir()
	if dir == "" {
		t.Skipf("KiCad demos not found (install KiCad 10 or set %s)", EnvKiCadDemos)
	}
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			for _, e := range exts {
				if strings.HasSuffix(p, e) {
					out = append(out, p)
				}
			}
		}
		return nil
	})
	if len(out) == 0 {
		t.Skipf("no %v files under %s", exts, dir)
	}
	return dir, out
}

// identityEdits sets every field of every placed symbol to its current
// value and moves every symbol to where it already is. With KiCad's own
// formatting the edited text must equal the original byte for byte.
func identityEdits(t *testing.T, e *SchEditor) int {
	t.Helper()
	n := 0
	seen := map[string]bool{}
	for _, s := range e.root.list {
		ref := symRef(s)
		if s.head() != "symbol" || ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		props := map[string]bool{}
		for _, c := range s.list {
			if c.head() == "property" && len(c.list) > 2 && !props[c.list[1].atom] {
				props[c.list[1].atom] = true
				if c.list[1].atom == "Reference" && c.list[2].atom != ref {
					// A sheet used more than once: the field holds another
					// instance's reference than the first instance path, so
					// re-setting it would be a rename, not an identity edit.
					continue
				}
				if err := e.SetField(ref, c.list[1].atom, c.list[2].atom); err != nil {
					t.Errorf("SetField %s %s: %v", ref, c.list[1].atom, err)
				}
				n++
			}
		}
		if at := s.child("at"); at != nil {
			if err := e.MoveSymbol(ref, Pt{at.num(1), at.num(2)}, at.num(3)); err != nil {
				t.Errorf("MoveSymbol %s: %v", ref, err)
			}
			n++
		}
	}
	return n
}

// TestKiCadDemoCorpusRoundTrip reads every .kicad_sch / .kicad_pcb /
// .kicad_sym KiCad ships as a demo and checks: both readers parse it and
// agree; write → parse yields the identical tree (strings re-quoted with
// Q); a schematic opened with SchEditor and rendered with no edits, and with
// identity edits (every field re-set to its value, every symbol moved to
// its own position), is byte-identical to the file; ParseBoard reads every
// top-level footprint of a board.
func TestKiCadDemoCorpusRoundTrip(t *testing.T) {
	dir, files := demoFiles(t, ".kicad_sch", ".kicad_pcb", ".kicad_sym")
	edits := 0
	for _, p := range files {
		rel, _ := filepath.Rel(dir, p)
		t.Run(filepath.ToSlash(rel), func(t *testing.T) {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			src := string(b)
			root := checkSexprInput(t, src)
			if root == nil {
				t.Fatal("does not parse")
			}
			switch filepath.Ext(p) {
			case ".kicad_sch":
				e, err := OpenSchematic(src)
				if err != nil {
					t.Fatal(err)
				}
				if out, err := e.Render(); err != nil || out != src {
					t.Fatalf("no-op render changed the file (%v)", err)
				}
				edits += identityEdits(t, e)
				out, err := e.Render()
				if err != nil {
					t.Fatal(err)
				}
				if out != src {
					t.Fatalf("identity edits changed the file at byte %d", firstDiff(out, src))
				}
			case ".kicad_pcb":
				bd, err := ParseBoard(b)
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				for _, c := range root.list {
					if c.head() == "footprint" {
						want++
					}
				}
				if len(bd.Footprints) != want {
					t.Errorf("ParseBoard read %d footprints, file has %d", len(bd.Footprints), want)
				}
			}
		})
	}
	t.Logf("%d files, %d identity edits", len(files), edits)
}

func firstDiff(a, b string) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}

// TestKiCadDemoNetlistUnchanged edits a temp copy of every demo project:
// identity edits plus one free text per sheet, a netlist-neutral change
// that does alter the bytes. Each edited sheet must parse to the original
// tree plus that one item, and `kicad-cli sch export netlist` of the root
// must be unchanged (export date and unit-UUID order aside).
func TestKiCadDemoNetlistUnchanged(t *testing.T) {
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("runs kicad-cli twice per demo project (~15 min); set PCBPILOT_KICAD_LIVE=1")
	}
	dir, pros := demoFiles(t, ".kicad_pro")
	cli, err := KicadCLI()
	if err != nil {
		t.Skip(err)
	}
	checked := 0
	for _, pro := range pros {
		root := strings.TrimSuffix(pro, ".kicad_pro") + ".kicad_sch"
		if _, err := os.Stat(root); err != nil {
			continue
		}
		rel, _ := filepath.Rel(dir, pro)
		t.Run(filepath.ToSlash(rel), func(t *testing.T) {
			tmp := t.TempDir()
			sheets := copySchProject(t, filepath.Dir(pro), tmp)
			troot := filepath.Join(tmp, filepath.Base(root))
			before, err := exportNetlistText(cli, troot, filepath.Join(tmp, "before.net"))
			if err != nil {
				t.Skipf("kicad-cli cannot export the original: %v", err)
			}
			for _, sp := range sheets {
				b, err := os.ReadFile(sp)
				if err != nil {
					t.Fatal(err)
				}
				e, err := OpenSchematic(string(b))
				if err != nil {
					t.Fatal(err)
				}
				identityEdits(t, e)
				e.AddText("pcbpilot round trip", Pt{X: 12.7, Y: 12.7}, 0, 1.27)
				out, err := e.Render()
				if err != nil {
					t.Fatalf("%s: %v", sp, err)
				}
				orig, _ := parseSx(string(b))
				got, err := parseSx(out)
				if err != nil {
					t.Fatal(err)
				}
				var kept []*sexp
				for _, c := range got.list {
					if c.head() == "text" && len(c.list) > 1 && c.list[1].atom == "pcbpilot round trip" {
						continue
					}
					kept = append(kept, c)
				}
				if len(kept) != len(got.list)-1 || dumpSexp(&sexp{isList: true, list: kept}) != dumpSexp(orig) {
					t.Fatalf("%s: edited tree is not the original plus one text", sp)
				}
				if err := os.WriteFile(sp, []byte(out), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			after, err := exportNetlistText(cli, troot, filepath.Join(tmp, "after.net"))
			if err != nil {
				t.Fatalf("kicad-cli cannot export the edited copy: %v", err)
			}
			if before != after {
				t.Fatalf("netlist changed at byte %d", firstDiff(before, after))
			}
			checked++
		})
	}
	t.Logf("%d projects: netlist unchanged", checked)
}

// copySchProject copies a project's schematic-side files (sheets, project,
// libraries, lib tables) into dst, keeping the layout; it returns the
// copied sheets.
func copySchProject(t *testing.T, src, dst string) []string {
	t.Helper()
	var sheets []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := d.Name()
		ext := filepath.Ext(name)
		if ext != ".kicad_sch" && ext != ".kicad_pro" && ext != ".kicad_sym" && name != "sym-lib-table" {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if ext == ".kicad_sch" {
			sheets = append(sheets, out)
		}
		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return sheets
}

func exportNetlistText(cli, sch, out string) (string, error) {
	cmd, cancel := SchCLICommand(cli, "sch", "export", "netlist", "--format", "kicadsexpr", "-o", out, sch)
	defer cancel()
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", &exportErr{err, stderrTail(b)}
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return "", err
	}
	n, err := parseSx(string(b))
	if err != nil {
		return "", err
	}
	normalizeNetlist(n)
	return dumpSexp(n), nil
}

// normalizeNetlist drops the export date and sorts each component's
// (tstamps …): KiCad lists a multi-unit part's unit UUIDs in an order that
// shifts when unrelated items are added to the sheet.
func normalizeNetlist(n *sexp) {
	if !n.isList {
		return
	}
	kept := n.list[:0]
	for _, c := range n.list {
		if c.head() == "date" {
			continue
		}
		if c.head() == "tstamps" && len(c.list) > 1 {
			rest := c.list[1:]
			sort.Slice(rest, func(i, j int) bool { return rest[i].atom < rest[j].atom })
		}
		normalizeNetlist(c)
		kept = append(kept, c)
	}
	n.list = kept
}

type exportErr struct {
	err  error
	tail string
}

func (e *exportErr) Error() string { return e.err.Error() + ": " + e.tail }
