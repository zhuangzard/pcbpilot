package protocol

import (
	"strconv"
	"strings"
)

// HostProfile is the single place that turns an EasyEDA Pro product version
// into a host line and the version-gated capabilities pcbpilot relies on.
// pcbpilot supports both lines (V3 3.2.x and V4); behavior that is not tied to
// a documented version (designator re-layout on rotation, netflag rotation
// storage) is measured or probed at run time instead of listed here.
type HostProfile struct {
	Version  string          `json:"version"`
	Line     string          `json:"line"` // v3 | v4 | unknown
	Semver   [3]int          `json:"-"`
	Features map[string]bool `json:"features"`
}

// hostFeatureRules: feature -> minimum version per line (nil = not on that
// line). Sources: @jlceda/pro-api-types 0.4.25 "since" annotations and the
// official V4.1.60 update record (2026-09-21).
var hostFeatureRules = map[string]struct{ v3, v4 *[3]int }{
	// sch_PrimitiveAttribute.createNetLabel: "ADD since EDA v4"; V3 can hang.
	"nativeNetLabel": {nil, &[3]int{4, 0, 0}},
	// V4 "符号引脚文本属性图元": pin-level attributes, parent "<id>-e<n>".
	"pinLevelAttributes": {nil, &[3]int{4, 0, 0}},
	// V4 "多符号和多器件 / 多封装": plural symbol/footprint/device variants.
	"multiVariantDevices": {nil, &[3]int{4, 0, 0}},
	// V4 reports an unset attribute style as null; V3 as undefined.
	"unsetStyleAsNull": {nil, &[3]int{4, 0, 0}},
	// sys_Environment.getEditorCurrentVersion(onlySemantic?): 3.2.176 / 4.1.13.
	"editorVersionApi": {&[3]int{3, 2, 176}, &[3]int{4, 1, 13}},
	// sch_ManufactureData.getPngFile/getSvgFile, sys_FileManager.getSchematicFile.
	"schematicImageExport": {&[3]int{3, 2, 183}, &[3]int{4, 1, 23}},
}

// ParseHostProfile never guesses: an unparsable version is line "unknown" with
// every version-gated feature off.
func ParseHostProfile(version string) HostProfile {
	p := HostProfile{Version: strings.TrimSpace(version), Line: "unknown", Features: map[string]bool{}}
	parts := strings.Split(strings.TrimPrefix(p.Version, "v"), ".")
	ok := len(parts) >= 3
	for i := 0; ok && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		ok = err == nil && n >= 0
		p.Semver[i] = n
	}
	if ok {
		switch p.Semver[0] {
		case 3:
			p.Line = "v3"
		case 4:
			p.Line = "v4"
		}
	}
	for name, rule := range hostFeatureRules {
		min := rule.v3
		if p.Line == "v4" {
			min = rule.v4
		}
		p.Features[name] = p.Line != "unknown" && min != nil && !semverLess(p.Semver, *min)
	}
	return p
}

func (p HostProfile) Has(feature string) bool { return p.Features[feature] }

func semverLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
