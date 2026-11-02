package kicad

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBoardFootprintLibs(t *testing.T) {
	b := []byte(`(kicad_pcb (footprint "Resistor_SMD:R_0603" (layer "F.Cu")) (footprint "lcsc:X") (footprint "Resistor_SMD:R_0805") (footprint "NoLib"))`)
	if got := strings.Join(BoardFootprintLibs(b), ","); got != "Resistor_SMD,lcsc" {
		t.Fatal(got)
	}
}

// The private table keeps the user's entries verbatim for the board's
// libraries only, nested tables resolved, first entry winning.
func TestBoardConfigDirCutsTheUserTable(t *testing.T) {
	cfg := t.TempDir()
	ver := filepath.Join(cfg, FPLibVersion)
	_ = os.MkdirAll(ver, 0o755)
	nested := filepath.Join(cfg, "stock-fp-lib-table")
	_ = os.WriteFile(nested, []byte(`(fp_lib_table (version 7)
  (lib (name "Resistor_SMD")(type "KiCad")(uri "${KICAD10_FOOTPRINT_DIR}/Resistor_SMD.pretty")(options "")(descr "stock"))
  (lib (name "Capacitor_SMD")(type "KiCad")(uri "${KICAD10_FOOTPRINT_DIR}/Capacitor_SMD.pretty")(options "")(descr "stock")))`), 0o644)
	_ = os.WriteFile(filepath.Join(ver, "fp-lib-table"), []byte(`(fp_lib_table (version 7)
  (lib (name "Mine")(type "KiCad")(uri "D:/libs/mine.pretty")(options "")(descr "user"))
  (lib (name "KiCad")(type "Table")(uri "`+filepath.ToSlash(nested)+`")(options "")(descr "stock")))`), 0o644)
	_ = os.WriteFile(filepath.Join(ver, "kicad_common.json"), []byte(`{"environment":{"vars":{"MY":"x"}}}`), 0o644)
	t.Setenv("KICAD_CONFIG_HOME", cfg) // KiCadConfigDir reads it
	t.Setenv("PCBPILOT_CACHE_DIR", t.TempDir())
	if BoardCLIEnv([]byte(`(footprint "Mine:A")`)) != nil {
		t.Fatal("an explicit KICAD_CONFIG_HOME must win")
	}
	dir, err := boardConfigDir([]byte(`(kicad_pcb (footprint "Resistor_SMD:R") (footprint "Mine:A") (footprint "Gone:B"))`))
	if err != nil || dir == "" {
		t.Fatal(dir, err)
	}
	fp, _ := os.ReadFile(filepath.Join(dir, FPLibVersion, "fp-lib-table"))
	s := string(fp)
	if !strings.Contains(s, `(name "Mine")`) || !strings.Contains(s, `(name "Resistor_SMD") (type "KiCad") (uri "${KICAD10_FOOTPRINT_DIR}/Resistor_SMD.pretty")`) || strings.Contains(s, "Capacitor_SMD") || strings.Contains(s, "Gone") || strings.Contains(s, `"Table"`) {
		t.Fatalf("table:\n%s", s)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, FPLibVersion, "kicad_common.json")); !strings.Contains(string(b), `"MY"`) {
		t.Fatal("kicad_common.json not copied")
	}
	if _, err := ParseFPLibTableForTest(fp); err != nil {
		t.Fatal(err)
	}
}

// ParseFPLibTableForTest parses a table the way KiCad's own tables are read.
func ParseFPLibTableForTest(b []byte) (*sx, error) { return parseSexpr(b) }

// Live: DRC with the private board config reports exactly what DRC with
// the user's own configuration reports — including a library mismatch
// (a stock footprint with a changed pad) and a footprint whose library is
// in no table.
func TestBoardCLIEnvSameDRCLive(t *testing.T) {
	tools := bridgeLiveTools(t)
	if userKiCadConfigDir() == "" {
		t.Skip("no user fp-lib-table")
	}
	stock := StockFootprintDir()
	mod, err := os.ReadFile(filepath.Join(stock, "Resistor_SMD.pretty", "R_0603_1608Metric.kicad_mod"))
	if err != nil {
		t.Skip(err)
	}
	place := func(x string, mutate bool) string {
		var keep []string
		for _, l := range strings.Split(strings.ReplaceAll(string(mod), "\r\n", "\n"), "\n") {
			if strings.HasPrefix(l, "\t(version ") || strings.HasPrefix(l, "\t(generator") {
				continue
			}
			keep = append(keep, "\t"+l)
		}
		s := strings.Join(keep, "\n")
		s = strings.Replace(s, `(footprint "R_0603_1608Metric"`, `(footprint "Resistor_SMD:R_0603_1608Metric"`, 1)
		s = strings.Replace(s, "(layer \"F.Cu\")", "(layer \"F.Cu\")\n\t\t(uuid \"00000"+x+"-0000-4000-8000-000000000000\")\n\t\t(at "+x+" 60)", 1)
		if mutate {
			s = strings.Replace(s, "(size 0.8 0.95)", "(size 0.9 0.95)", 1)
		}
		return s
	}
	src, err := os.ReadFile("testdata/tiny.kicad_pcb")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimRight(string(src), "\n")
	body = strings.TrimSuffix(body, ")") + place("150", false) + "\n" + place("160", true) + "\n)\n"
	dir := t.TempDir()
	pcb := filepath.Join(dir, "libcheck.kicad_pcb")
	if err := os.WriteFile(pcb, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(isolate string) []string {
		t.Setenv(EnvIsolateConfig, isolate)
		rep, err := tools.DRCSeverity(pcb, filepath.Join(dir, "drc-"+isolate+".json"), "all")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		unconnected := 0
		for _, v := range rep.Violations {
			// Which two items a ratsnest gap names is KiCad's arbitrary
			// pick per run: compared by count.
			if v.Section == "unconnected_items" {
				unconnected++
				continue
			}
			objs := append([]string(nil), v.Objs...)
			sort.Strings(objs)
			out = append(out, v.Section+"|"+v.Rule+"|"+v.Severity+"|"+v.Message+"|"+strings.Join(objs, ";"))
		}
		sort.Strings(out)
		return append(out, fmt.Sprintf("unconnected_items %d", unconnected))
	}
	t.Setenv("PCBPILOT_CACHE_DIR", t.TempDir())
	if BoardCLIEnv([]byte(body)) == nil {
		t.Fatal("no board config")
	}
	user, private := run("0"), run("1")
	if strings.Join(user, "\n") != strings.Join(private, "\n") {
		t.Fatalf("DRC differs:\nuser:\n%s\nprivate:\n%s", strings.Join(user, "\n"), strings.Join(private, "\n"))
	}
	all := strings.Join(private, "\n")
	if !strings.Contains(all, "lib_footprint_mismatch") || !strings.Contains(all, "lib_footprint_issues") {
		t.Fatalf("expected both library checks:\n%s", all)
	}
	if strings.Count(all, "does not match copy in library 'Resistor_SMD'") != 1 {
		t.Fatalf("only the changed R_0603 mismatches:\n%s", all)
	}
}

// A disabled nested table contributes nothing: its libraries stay out of
// the private table, as they are out of KiCad's.
func TestBoardConfigDirDisabledNestedTable(t *testing.T) {
	cfg := t.TempDir()
	ver := filepath.Join(cfg, FPLibVersion)
	_ = os.MkdirAll(ver, 0o755)
	nested := filepath.Join(cfg, "nested-fp-lib-table")
	_ = os.WriteFile(nested, []byte(`(fp_lib_table (version 7) (lib (name "Child")(type "KiCad")(uri "D:/c.pretty")(options "")(descr "")))`), 0o644)
	_ = os.WriteFile(filepath.Join(ver, "fp-lib-table"), []byte(`(fp_lib_table (version 7)
  (lib (name "Off")(type "Table")(uri "`+filepath.ToSlash(nested)+`")(options "")(descr "")(disabled)))`), 0o644)
	t.Setenv("KICAD_CONFIG_HOME", cfg)
	t.Setenv("PCBPILOT_CACHE_DIR", t.TempDir())
	dir, err := boardConfigDir([]byte(`(footprint "Child:X")`))
	if err != nil {
		t.Fatal(err)
	}
	fp, _ := os.ReadFile(filepath.Join(dir, FPLibVersion, "fp-lib-table"))
	if strings.Contains(string(fp), "Child") {
		t.Fatalf("disabled table's child enabled: %s", fp)
	}
}

// A probe DRC (no footprints) runs with empty global tables that KiCad
// leaves empty (no stock table seeded) and does not take the ~100 s the
// stock library table costs.
func TestDRCNoLibrariesLive(t *testing.T) {
	tools := bridgeLiveTools(t)
	t.Setenv(EnvCacheDir, t.TempDir())
	isolatedOnce = sync.Once{}
	isolatedDir = ""
	dir := t.TempDir()
	pcb := filepath.Join(dir, "probe.kicad_pcb")
	_ = os.WriteFile(pcb, []byte(`(kicad_pcb (version 20241229) (generator "pcbnew") (general (thickness 1.6))
  (layers (0 "F.Cu" signal) (2 "B.Cu" signal) (25 "Edge.Cuts" user))
  (setup (pad_to_mask_clearance 0))
  (segment (start 0 0) (end 2 0) (width 0.1) (layer "F.Cu") (net "A") (uuid "00000000-0000-4000-8000-000000000001"))
  (segment (start 0 0.15) (end 2 0.15) (width 0.1) (layer "F.Cu") (net "B") (uuid "00000000-0000-4000-8000-000000000002")))
`), 0o644)
	start := time.Now()
	rep, err := tools.DRCNoLibraries(pcb, filepath.Join(dir, "drc.json"))
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts["clearance"] == 0 {
		t.Fatalf("the probe clearance error is missing: %+v", rep.Counts)
	}
	empty := LibTableText("fp_lib_table", nil)
	for _, v := range kicadSettingsVersions {
		b, err := os.ReadFile(filepath.Join(isolatedDir, v, "fp-lib-table"))
		if err != nil || string(b) != empty {
			t.Fatalf("%s fp-lib-table after the run: %q %v", v, b, err)
		}
	}
	if took > 45*time.Second {
		t.Fatalf("probe DRC took %s: the stock library table was loaded?", took)
	}
	t.Logf("probe DRC %s", took)
}
