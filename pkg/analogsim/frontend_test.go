package analogsim

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

const skillRefs = "../../.agents/skills/pcbpilot/references"

func loadFixture(t *testing.T, dir string, conn ...string) *powersim.Design {
	t.Helper()
	var docs []*powersim.ConnDoc
	for _, c := range conn {
		b, err := os.ReadFile(filepath.Join(dir, c))
		if err != nil {
			t.Fatal(err)
		}
		d, err := powersim.ParseConnectivity(b)
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, d)
	}
	vb, err := os.ReadFile(filepath.Join(dir, "values.json"))
	if err != nil {
		t.Fatal(err)
	}
	vals, err := powersim.ParseValues(vb)
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := powersim.BuildDesign(docs, vals)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func skillLib(t *testing.T) *Library {
	t.Helper()
	lib, err := LoadLibrary(filepath.Join(skillRefs, "spice-models", "analog-models.json"))
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

func TestFrontendDump(t *testing.T) {
	if os.Getenv("ANALOG_DUMP") == "" {
		t.Skip("set ANALOG_DUMP=1")
	}
	d := loadFixture(t, "../../testdata/analog/frontend", "sch-frontend.json")
	sb, _ := os.ReadFile("../../testdata/analog/frontend/spec.json")
	spec, err := ParseSpec(sb)
	if err != nil {
		t.Fatal(err)
	}
	stock, _ := LoadStock(filepath.Join(skillRefs, "standard-parts.json"))
	out, err := Run(d, skillLib(t), Options{Spec: spec, Stock: stock, Optimise: true, MCRuns: 50, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range out.Blocks {
		b.Curves = nil
	}
	js, _ := json.MarshalIndent(out, "", " ")
	os.WriteFile(os.Getenv("ANALOG_DUMP"), js, 0o644)
}

func esp32Design(t *testing.T) (*powersim.Design, *powersim.Output, powersim.Libraries) {
	t.Helper()
	d := loadFixture(t, "../powersim/testdata/esp32mini", "sch-905bb85957eaf435.json", "sch-950ae6609e91d753.json")
	plib, err := powersim.LoadLibrary(filepath.Join(skillRefs, "power-models.json"))
	if err != nil {
		t.Fatal(err)
	}
	libs := powersim.Libraries{plib}
	sim, _, err := powersim.Simulate(d, libs, powersim.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return d, sim, libs
}

func TestESP32Dump(t *testing.T) {
	if os.Getenv("ANALOG_DUMP") == "" {
		t.Skip("set ANALOG_DUMP=1")
	}
	d, sim, libs := esp32Design(t)
	stock, _ := LoadStock(filepath.Join(skillRefs, "standard-parts.json"))
	out, err := Run(d, skillLib(t), Options{Stock: stock, Optimise: true, MCRuns: 50, PowerSim: sim, PowerLibs: libs, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range out.Blocks {
		b.Curves = nil
	}
	js, _ := json.MarshalIndent(out, "", " ")
	os.WriteFile(os.Getenv("ANALOG_DUMP"), js, 0o644)
}
