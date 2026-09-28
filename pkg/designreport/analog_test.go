package designreport

import (
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/analogsim"
)

func TestAnalogSection(t *testing.T) {
	v := 0.707
	out := &analogsim.Output{SchemaVersion: 1, Generator: analogsim.Generator, Ngspice: analogsim.NgspiceInfo{Available: true, Version: "ngspice-47", Runs: 5},
		Summary: analogsim.Summary{Blocks: 1, Simulated: 1, Targets: 1, Failing: 1, Changes: 1, Status: analogsim.StatusFail},
		Blocks: []*analogsim.Block{{ID: "A1", Class: analogsim.ClassSKLowPass, Title: "Sallen-Key 低通 U1", Parts: []string{"U1", "R1"}, Simulated: true, Status: analogsim.StatusFail,
			Metrics: []analogsim.Metric{{Name: "q", Label: "品质因数 Q", Value: 0.5, Method: "ngspice-ac", Status: analogsim.StatusFail,
				Target: &analogsim.Target{Metric: "q", Value: &v, TolPct: 5, Source: "spec"}}},
			Curves: []analogsim.Curve{{Name: "bode", Title: "频率响应", XLabel: "频率", XUnit: "Hz", LogX: true, X: []float64{10, 100, 1000, 10000},
				Series: []analogsim.Series{{Name: "幅度", Unit: "dB", Y: []float64{0, 0, -3, -40}}, {Name: "相位", Unit: "°", Axis: "right", Y: []float64{0, -10, -90, -170}}}}},
			Optimise: &analogsim.Optimisation{Status: "improved", Changes: []analogsim.Change{{Ref: "C1", From: "10nF", To: "22nF", Action: "replace-lcsc",
				Part: &analogsim.StockPart{LCSC: "C1", Value: "22nF"}, Before: map[string]float64{"q": 0.5}, After: map[string]float64{"q": 0.707}}}}}},
		Findings: []analogsim.Finding{{Severity: "error", Kind: "analog-target-miss", Block: "A1", Message: "Q miss"}}}
	rep := Build(&Inputs{Project: "t", Analog: out})
	a := rep.Analog
	if a == nil || len(a.Blocks) != 1 || len(a.Metrics) != 1 || len(a.Changes) != 1 || len(a.Charts) != 1 {
		t.Fatalf("section %+v", a)
	}
	if rep.Verdict.Status != VerdictFail {
		t.Errorf("analog error must fail the verdict without intent: %+v", rep.Verdict)
	}
	charts := Charts(rep)
	svg := charts["analog-a1-bode"]
	if !strings.Contains(svg, "<polyline") || !strings.Contains(svg, "频率响应") {
		t.Fatalf("bode chart: %.200s", svg)
	}
	html, err := RenderHTML(rep, charts)
	if err != nil || !strings.Contains(string(html), "3A 模拟电路仿真") || !strings.Contains(string(html), "22nF") {
		t.Fatalf("html: %v", err)
	}
	md, err := RenderMarkdown(rep)
	if err != nil || !strings.Contains(string(md), "charts/analog-a1-bode.svg") {
		t.Fatalf("md: %v", err)
	}
	// No analog input: one line, no missing-section entry.
	rep2 := Build(&Inputs{Project: "t"})
	md2, _ := RenderMarkdown(rep2)
	if !strings.Contains(string(md2), "未运行模拟仿真") {
		t.Error("absent analog section note missing")
	}
	for _, m := range rep2.Missing {
		if strings.HasPrefix(m.Section, "3A") {
			t.Error("absent analog must not count as a missing section")
		}
	}
}
