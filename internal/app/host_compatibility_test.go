package app

import (
	"strings"
	"testing"
)

func TestEvaluateHostVersionV3AndV4Lines(t *testing.T) {
	cases := []struct {
		version string
		want    string
	}{
		{"2.2.40", versionSevWarn},
		{"3.1.9", versionSevWarn},
		{"3.2.149.88089769", versionSevOK},
		{"3.2.203", versionSevOK},
		{"4.0.9", versionSevWarn},
		{"4.1.59", versionSevWarn},
		{"4.1.60", versionSevOK},
		{"4.2.0.123456", versionSevOK},
		{"5.0.0", versionSevWarn},
		{"unknown", versionSevSkipped},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			got := evaluateHostVersion("w1", tc.version)
			if got.Severity != tc.want {
				t.Fatalf("severity=%q want=%q: %+v", got.Severity, tc.want, got)
			}
		})
	}
}

func TestHostCompatibilityFromHealthWorstWindowWins(t *testing.T) {
	rep := hostCompatibilityFromHealth([]byte(`{"windows":[
	  {"windowId":"v4","easyedaVersion":"4.1.60"},
	  {"windowId":"v3","easyedaVersion":"3.2.203"},
	  {"windowId":"old","easyedaVersion":"4.0.1"}]}`))
	if rep.Verdict != versionSevWarn || len(rep.Findings) != 3 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if !strings.Contains(hostCompatibilitySummary(rep), "未处于已验证版本") {
		t.Fatalf("summary: %s", hostCompatibilitySummary(rep))
	}
	for _, f := range rep.Findings {
		if f.WindowID == "v3" && !strings.Contains(f.Reason, "V3") {
			t.Fatalf("V3 line must be named for provenance: %+v", f)
		}
	}
}

func TestHostCompatibilityIsSeparateFromExtensionAPIEngine(t *testing.T) {
	rep := hostCompatibilityFromHealth([]byte(`{"windows":[{"easyedaVersion":"3.2.149"}]}`))
	if rep.Baseline != "3.2.0" || rep.Verdict != versionSevOK {
		t.Fatalf("unexpected host declaration: %+v", rep)
	}
}
