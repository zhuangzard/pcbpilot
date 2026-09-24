package app

import (
	"encoding/json"
	"fmt"
	"github.com/zhuangzard/pcbpilot/internal/protocol"
	"strconv"
	"strings"
)

// pcbpilot supports two host lines: EasyEDA Pro V3 (3.2.x, verified on the
// 3.2.149 desktop and pro.easyeda.com builds) and V4 (4.x, verified on 4.1.60
// Web). Both load the same connector: the extension API engine is 3.2 on
// both. Known per-line behavior differences are detected or measured at run
// time (netflag rotation probe, measured designator poses), so the line is
// reported for provenance, not used to refuse writes. The editor product version is
// deliberately kept separate from extension/extension.json's engines.eda:
// that field describes the extension API engine and is still 3.2 in the
// official V4 SDK template.
const (
	hostBaselineVersion    = "3.2.0"
	hostRecommendedVersion = "3.2.149 / 4.1.60"
)

type hostCompatibilityFinding struct {
	WindowID string `json:"windowId,omitempty"`
	Version  string `json:"version,omitempty"`
	Severity string `json:"severity"` // ok | warn | block | skipped
	// Line and Features come from protocol.ParseHostProfile: which host line
	// this window is and which version-gated capabilities it has.
	Line     string          `json:"line,omitempty"`
	Features map[string]bool `json:"features,omitempty"`
	Reason   string          `json:"reason"`
	Fix      string          `json:"fix,omitempty"`
}

type hostCompatibilityReport struct {
	Baseline    string                     `json:"baseline"`
	Recommended string                     `json:"recommended"`
	Verdict     string                     `json:"verdict"`
	Findings    []hostCompatibilityFinding `json:"findings,omitempty"`
}

func hostCompatibilityFromHealth(raw []byte) hostCompatibilityReport {
	rep := hostCompatibilityReport{Baseline: hostBaselineVersion, Recommended: hostRecommendedVersion, Verdict: versionSevSkipped}
	var parsed struct {
		Windows []struct {
			WindowID       string `json:"windowId"`
			EasyEDAVersion string `json:"easyedaVersion"`
		} `json:"windows"`
	}
	if json.Unmarshal(raw, &parsed) != nil || len(parsed.Windows) == 0 {
		return rep
	}
	findings := make([]versionFinding, 0, len(parsed.Windows))
	for _, w := range parsed.Windows {
		f := evaluateHostVersion(w.WindowID, w.EasyEDAVersion)
		rep.Findings = append(rep.Findings, f)
		findings = append(findings, versionFinding{Severity: f.Severity})
	}
	rep.Verdict = worstSeverity(findings)
	return rep
}

func evaluateHostVersion(windowID, raw string) hostCompatibilityFinding {
	profile := protocol.ParseHostProfile(raw)
	f := hostCompatibilityFinding{WindowID: strings.TrimSpace(windowID), Version: strings.TrimSpace(raw), Line: profile.Line, Features: profile.Features}
	parts, ok := productVersionNumbers(raw)
	if !ok {
		f.Severity = versionSevSkipped
		f.Reason = "宿主未上报可比较的 EasyEDA 产品版本，无法确认宿主线"
		return f
	}
	core := fmt.Sprintf("%d.%d.%d", parts[0], parts[1], parts[2])
	switch {
	case parts[0] < 3 || parts[0] == 3 && parts[1] < 2:
		f.Severity = versionSevWarn
		f.Reason = fmt.Sprintf("EasyEDA Pro %s 低于已验证的 V3 3.2 线", core)
		f.Fix = "升级到 EasyEDA Pro 3.2.149 或 V4 4.1.60；未升级前写入须 save → reload → readback 验收。"
	case parts[0] > 4:
		f.Severity = versionSevWarn
		f.Reason = fmt.Sprintf("EasyEDA Pro %s 是未验证的新大版本，需按新大版本重新验收", core)
		f.Fix = "在完成该大版本的 save → reload → readback 回归前，不要把它当作已验证宿主。"
	case parts[0] == 3:
		f.Severity = versionSevOK
		f.Reason = fmt.Sprintf("EasyEDA Pro %s 属于支持的 V3 线（报告须注明 V3）", core)
	case compareSemverNumbers(parts, [3]int{4, 1, 60}) < 0:
		f.Severity = versionSevWarn
		f.Reason = fmt.Sprintf("EasyEDA Pro %s 属于 V4，但低于已验证的 4.1.60", core)
		f.Fix = "建议升级到 EasyEDA Pro 4.1.60 或更新 V4 版本。"
	default:
		f.Severity = versionSevOK
		f.Reason = fmt.Sprintf("EasyEDA Pro %s 属于支持的 V4 线（报告须注明 V4）", core)
	}
	return f
}

func productVersionNumbers(raw string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(raw), "v"), ".")
	// EasyEDA product builds may append a fourth numeric build id, for example
	// 3.2.149.88089769. Product compatibility is decided by the first 3 fields.
	if len(parts) < 3 {
		return out, false
	}
	for i, p := range parts[:3] {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func compareSemverNumbers(a, b [3]int) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

func hostCompatibilitySummary(rep hostCompatibilityReport) string {
	switch rep.Verdict {
	case versionSevBlock:
		return "✗ EasyEDA 宿主:不受支持；见 hostCompatibility.findings"
	case versionSevWarn:
		return "⚠ EasyEDA 宿主:未处于已验证版本（V3 3.2.149 / V4 4.1.60）；见 hostCompatibility.findings"
	case versionSevOK:
		return "✓ EasyEDA 宿主:受支持（V3 3.2 线或 V4 线；报告注明宿主版本）"
	default:
		return "· EasyEDA 宿主:未判定（无窗口或版本不可读）"
	}
}
