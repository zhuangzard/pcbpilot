package designreport

import (
	"bytes"
	"embed"
	"encoding/base64"
	htmltpl "html/template"
	"math"
	"strings"
	texttpl "text/template"
)

// Templates holds the embedded copy of the Skill's design-report templates
// (.agents/skills/pcbpilot/templates/design-report/*.tmpl — the canonical
// source; TestTemplatesMatchSkill keeps both identical).
//
//go:embed templates/*.tmpl
var Templates embed.FS

func commonFuncs() map[string]any {
	return map[string]any{
		"f1":  func(v float64) string { return f1(v) },
		"f2":  func(v float64) string { return f2(v) },
		"f3":  func(v float64) string { return f3(v) },
		"f4":  func(v float64) string { return trimF4(v) },
		"num": func(v float64) string { return trimF4(v) },
		"opt": func(v float64) string {
			if v == 0 {
				return "—"
			}
			return trimF4(v)
		},
		"watt": func(v float64) string { return fW(v) },
		"pct100": func(v float64) string {
			if v == 0 {
				return "—"
			}
			return f1(v*100) + " %"
		},
		"eff": func(load, in float64) string {
			if in <= 0 {
				return "—"
			}
			return f1(load/in*100) + " %"
		},
		"margin": func(p *float64) string {
			if p == nil {
				return "—"
			}
			return f1(*p) + " %"
		},
		"short":    short,
		"join":     strings.Join,
		"statusZh": statusZh,
		"missing":  missingFor,
		"md":       mdEsc,
	}
}

func trimF4(v float64) string {
	if v != 0 && math.Abs(v) < 0.001 {
		return sprintf("%.3g", v)
	}
	return strf(round(v, 4))
}

func strf(v float64) string { return sprintf("%g", v) }

// missingFor returns the recorded reason for a section number prefix.
func missingFor(r *Report, sec string) string {
	for _, m := range r.Missing {
		if m.Section == sec || strings.HasPrefix(m.Section, sec+" ") {
			return m.Reason
		}
	}
	return "输入缺失"
}

func verdictClass(s string) string {
	switch s {
	case VerdictPass:
		return "PASS"
	case VerdictFail:
		return "FAIL"
	}
	return "WARN"
}

func statusCls(s string) string {
	switch strings.ToUpper(s) {
	case "PASS":
		return "PASS"
	case "FAIL", "ERROR":
		return "FAIL"
	case "WARN":
		return "WARN"
	}
	return "NA"
}

// DataURI is the base64 data URI of an embedded image.
func DataURI(img Image) string {
	return "data:" + img.Mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
}

// RenderHTML renders the self-contained report.html (charts inline, images
// as data URIs).
func RenderHTML(r *Report, charts map[string]string) ([]byte, error) {
	src, err := Templates.ReadFile("templates/report.html.tmpl")
	if err != nil {
		return nil, err
	}
	fm := htmltpl.FuncMap(commonFuncs())
	fm["chart"] = func(name string) htmltpl.HTML { return htmltpl.HTML(charts[name]) }
	fm["img"] = func(img Image) htmltpl.URL { return htmltpl.URL(DataURI(img)) }
	fm["verdictClass"] = verdictClass
	fm["statusCls"] = statusCls
	t, err := htmltpl.New("report.html").Funcs(fm).Parse(string(src))
	if err != nil {
		return nil, err
	}
	if t, err = parsePartial(t, "templates/analog.html.tmpl"); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, map[string]any{"R": r}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderMarkdown renders report.md; chart files are charts/<name>.svg and
// images ../assets/<asset>.
func RenderMarkdown(r *Report) ([]byte, error) {
	src, err := Templates.ReadFile("templates/report.md.tmpl")
	if err != nil {
		return nil, err
	}
	fm := texttpl.FuncMap(commonFuncs())
	fm["imgpath"] = func(img Image) string { return "../assets/" + img.Asset }
	t, err := texttpl.New("report.md").Funcs(fm).Parse(string(src))
	if err != nil {
		return nil, err
	}
	if part, err := Templates.ReadFile("templates/analog.md.tmpl"); err != nil {
		return nil, err
	} else if t, err = t.Parse(string(part)); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, map[string]any{"R": r}); err != nil {
		return nil, err
	}
	// Collapse runs of blank lines the template control flow leaves.
	out := buf.String()
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return []byte(out), nil
}

// parsePartial adds the {{define}} blocks of a partial template file.
func parsePartial(t *htmltpl.Template, name string) (*htmltpl.Template, error) {
	part, err := Templates.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return t.Parse(string(part))
}
