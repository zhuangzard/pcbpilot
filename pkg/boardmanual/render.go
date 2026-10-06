package boardmanual

import (
	"bytes"
	"embed"
	"fmt"
	htmltpl "html/template"
	"strings"
)

// Templates holds the embedded copy of the Skill's board-manual template
// (.agents/skills/pcbpilot/templates/board-manual/manual.html.tmpl — the
// canonical source; TestTemplateMatchesSkill keeps both identical).
//
//go:embed templates/*.tmpl
var Templates embed.FS

// RenderHTML renders the manual as one self-contained HTML document.
func RenderHTML(m *Manual) ([]byte, error) {
	src, err := Templates.ReadFile("templates/manual.html.tmpl")
	if err != nil {
		return nil, err
	}
	funcs := htmltpl.FuncMap{
		"t":    func(k string) string { return label(m.Lang, k) },
		"svg":  func(s string) htmltpl.HTML { return htmltpl.HTML(s) }, // built from escaped text only
		"mm":   func(v float64) string { return trimNum(v) },
		"join": strings.Join,
		"role": func(r string) string { return label(m.Lang, "role."+r) },
		"dash": func(s string) string {
			if strings.TrimSpace(s) == "" {
				return "—"
			}
			return s
		},
		"todo": func(s string) bool { return strings.Contains(strings.ToUpper(s), "TODO") },
		"add":  func(a, b int) int { return a + b },
		"sum": func(xs ...int) int {
			n := 0
			for _, x := range xs {
				n += x
			}
			return n
		},
		"ledFill": ledColor,
		"short": func(s string) string {
			if len(s) > 12 {
				return s[:12] + "…"
			}
			return s
		},
		"sprintf": fmt.Sprintf,
		"dataurl": func(s string) htmltpl.URL { return htmltpl.URL(s) }, // data: URIs built from our own SVG/CSV
		"mmil":    func(mil float64) string { return trimNum(round2(mil * MilToMM)) },
	}
	t, err := htmltpl.New("manual").Funcs(funcs).Parse(string(src))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
