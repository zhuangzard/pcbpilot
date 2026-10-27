package app

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// kicadDemos is KiCad's shipped demo directory ($PCBPILOT_KICAD_DEMOS, else
// next to kicad-cli: <root>/share/kicad/demos, macOS <app>/../demos).
func kicadDemos(t *testing.T, cli string) string {
	t.Helper()
	cands := []string{os.Getenv("PCBPILOT_KICAD_DEMOS"), filepath.Join(filepath.Dir(filepath.Dir(cli)), "share", "kicad", "demos")}
	if runtime.GOOS == "darwin" {
		cands = append(cands, "/Applications/KiCad/demos")
	}
	for _, d := range cands {
		if fi, err := os.Stat(d); d != "" && err == nil && fi.IsDir() {
			return d
		}
	}
	t.Skip("KiCad demos not found (set PCBPILOT_KICAD_DEMOS)")
	return ""
}

// copyDemo copies a shipped demo project (without 3D models) into a
// temporary directory: the demos are read-only and are never checked in place.
func copyDemo(t *testing.T, demos, name string) string {
	t.Helper()
	src := filepath.Join(demos, name)
	dst := filepath.Join(t.TempDir(), name)
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() && (strings.HasSuffix(fi.Name(), ".3dshapes") || fi.Name() == "3d_lib") {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(src, p)
		if fi.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// treeHash lists every file of dir with its content hash.
func treeHash(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			s := sha256.Sum256(b)
			out[p] = hex.EncodeToString(s[:])
		}
		return nil
	})
	return out
}

func sameTree(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestKicadDRCLiveDemos runs `kicad drc` on copies of KiCad's demos.
// Skipped unless PCBPILOT_KICAD_LIVE=1.
func TestKicadDRCLiveDemos(t *testing.T) {
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 to run against the installed KiCad")
	}
	cli, err := kicad.ResolveFabCLI()
	if err != nil {
		t.Fatal(err)
	}
	demos := kicadDemos(t, cli)
	cases := []struct {
		demo, board string
		check       func(t *testing.T, r *kicadDRCReport)
	}{
		{"pic_programmer", "pic_programmer", func(t *testing.T, r *kicadDRCReport) {
			// Clean but for library-table warnings (environment).
			if r.Verdict != gate.Pass || r.Errors != 0 || r.OwnerClasses["environment"] != r.Warnings {
				t.Errorf("verdict %s errors %d classes %v", r.Verdict, r.Errors, r.OwnerClasses)
			}
		}},
		{"interf_u", "interf_u", func(t *testing.T, r *kicadDRCReport) {
			s := checkStatus(r.Checks)
			if s["kicad-drc"] != gate.Fail || r.Counts["copper_edge_clearance"] != 1 || r.Counts["starved_thermal"] != 2 {
				t.Errorf("checks %v counts %v", s, r.Counts)
			}
			for _, v := range r.Violations {
				if v.Rule == "copper_edge_clearance" && v.Owner.Class != "board-outline" || v.Rule == "starved_thermal" && v.Owner.Class != "routing" {
					t.Errorf("%s owned by %s", v.Rule, v.Owner.Class)
				}
			}
		}},
		{"cm5_minima", "CM5_MINIMA_3", func(t *testing.T, r *kicadDRCReport) {
			// Its exclusions: courtyard overlaps (soft: silenced) and dangling
			// vias (info); the downgraded solder_mask_bridge is a hard item.
			if len(r.Excluded) != 5 || r.Policy == nil || r.Policy.DRCPolicyResult == nil {
				t.Fatalf("excluded %d policy %+v", len(r.Excluded), r.Policy)
			}
			silenced := map[string]bool{}
			for _, s := range r.Policy.Silenced {
				silenced[s.Rule+"/"+s.How] = true
			}
			if !silenced["courtyards_overlap/excluded"] || checkStatus(r.Checks)["kicad-drc-policy"] != gate.Fail ||
				!strings.Contains(strings.Join(r.Policy.Hard, "\n"), "DOWNGRADED solder_mask_bridge") {
				t.Errorf("policy %+v", r.Policy.DRCPolicyResult)
			}
			for _, v := range r.Excluded {
				if v.Rule == "courtyards_overlap" && v.Owner.Class != "placement" {
					t.Errorf("excluded courtyard overlap owned by %s", v.Owner.Class)
				}
			}
		}},
		{"video", "video", func(t *testing.T, r *kicadDRCReport) {
			s := checkStatus(r.Checks)
			if s["kicad-drc"] != gate.Pass || s["schematic-parity"] != gate.Fail || r.OwnerClasses["schematic-parity"] == 0 {
				t.Errorf("checks %v classes %v", s, r.OwnerClasses)
			}
		}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.demo, func(t *testing.T) {
			t.Parallel()
			dir := copyDemo(t, demos, c.demo)
			before := treeHash(t, dir)
			r := runKicadDRC(kicadDRCOptions{pcb: filepath.Join(dir, c.board+".kicad_pcb"), cli: cli, parity: true, tolMm: 0.5})
			if r.Run == nil {
				t.Fatalf("DRC did not run: %+v", r.Checks)
			}
			if !sameTree(before, treeHash(t, dir)) {
				t.Error("kicad drc changed the project directory")
			}
			c.check(t, r)
		})
	}
}

// TestKicadDRCLiveBaseline moves one footprint onto another in a copy (KiCad
// Python) and checks that every new violation involves it.
func TestKicadDRCLiveBaseline(t *testing.T) {
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 to run against the installed KiCad")
	}
	cli, err := kicad.ResolveFabCLI()
	if err != nil {
		t.Fatal(err)
	}
	py, err := kicad.ResolveFabPython()
	if err != nil {
		t.Skip(err)
	}
	demos := kicadDemos(t, cli)
	old, cur := copyDemo(t, demos, "interf_u"), copyDemo(t, demos, "interf_u")
	script := filepath.Join(t.TempDir(), "move.py")
	_ = os.WriteFile(script, []byte(`import sys, pcbnew
b = pcbnew.LoadBoard(sys.argv[1])
b.FindFootprintByReference("R1").SetPosition(b.FindFootprintByReference("C1").GetPosition())
b.Save(sys.argv[1])
`), 0o644)
	if out, err := exec.Command(py, script, filepath.Join(cur, "interf_u.kicad_pcb")).CombinedOutput(); err != nil {
		t.Fatalf("move: %v\n%s", err, out)
	}
	r := runKicadDRC(kicadDRCOptions{pcb: filepath.Join(cur, "interf_u.kicad_pcb"), cli: cli, parity: true, tolMm: 0.5,
		baselinePCB: filepath.Join(old, "interf_u.kicad_pcb")})
	if r.Baseline == nil || len(r.Baseline.New) == 0 || r.Baseline.Matched == 0 {
		t.Fatalf("baseline %+v checks %+v", r.Baseline, r.Checks)
	}
	// Matching is by the offending objects, so a conflict KiCad now reports
	// at another object of the same owners (a zone against another Edge.Cuts
	// segment) is new too; it must then have left a resolved twin.
	twin := map[string]int{}
	for _, v := range r.Baseline.Resolved {
		twin[v.Rule+"|"+v.Owner.Key]++
	}
	for _, v := range r.Baseline.New {
		if contains(v.Owner.Refs, "R1") {
			continue
		}
		if k := v.Rule + "|" + v.Owner.Key; twin[k] > 0 {
			twin[k]--
			t.Logf("re-reported at another object: %s", v.Item())
			continue
		}
		t.Errorf("new violation not involving the moved R1: %s", v.Item())
	}
	if checkStatus(r.Checks)["kicad-drc"] != gate.Fail {
		t.Errorf("checks %+v", r.Checks)
	}
	// The unchanged copy against itself: nothing new, nothing resolved.
	r = runKicadDRC(kicadDRCOptions{pcb: filepath.Join(old, "interf_u.kicad_pcb"), cli: cli, parity: true, tolMm: 0.5,
		baselinePCB: filepath.Join(old, "interf_u.kicad_pcb")})
	if len(r.Baseline.New) != 0 || len(r.Baseline.Resolved) != 0 || r.Verdict != gate.Pass {
		t.Fatalf("self baseline: %+v %+v", r.Baseline, r.Checks)
	}
}

// TestKicadFabPreflightLive: read-only preflight on interf_u with a contract.
func TestKicadFabPreflightLive(t *testing.T) {
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 to run against the installed KiCad")
	}
	cli, err := kicad.ResolveFabCLI()
	if err != nil {
		t.Fatal(err)
	}
	dir := copyDemo(t, kicadDemos(t, cli), "interf_u")
	contract := filepath.Join(t.TempDir(), "contract.json")
	_ = os.WriteFile(contract, []byte(`{"vendor":"JLCPCB","layers":2,"assembly":{"sides":"top"},
	  "limits":{"minTrackMm":0.127,"minSpaceMm":0.127,"minDrillMm":0.3,"minAnnularMm":0.13},
	  "sources":[{"item":"limits","url":"https://example.com/capabilities","retrieved":"2026-10-27"},
	             {"item":"assembly","url":"https://example.com/quote","retrieved":"2026-10-27"}]}`), 0o644)
	before := treeHash(t, dir)
	r := runFabPreflight(fabPreflightOptions{pcb: filepath.Join(dir, "interf_u.kicad_pcb"), sch: filepath.Join(dir, "interf_u.kicad_sch"), contract: contract, cli: cli})
	if !sameTree(before, treeHash(t, dir)) {
		t.Error("preflight changed the project directory")
	}
	s := checkStatus(r.Checks)
	want := map[string]gate.Status{"kicad-drc": gate.Fail, "board-outline": gate.Pass, "layer-count": gate.Pass, "min-track": gate.Pass,
		"min-annular": gate.Fail, "assembly-sides": gate.Pass, "lcsc": gate.Fail, "bom-cpl": gate.Pass, "schematic-parity": gate.Fail}
	for k, v := range want {
		if s[k] != v {
			t.Errorf("%s: %s, want %s", k, s[k], v)
		}
	}
	if r.Verdict != gate.Fail || r.Assembly == nil || r.Assembly.Assembled == 0 || r.Assembly.WithLCSC != 0 {
		t.Errorf("verdict %s assembly %+v", r.Verdict, r.Assembly)
	}
}
