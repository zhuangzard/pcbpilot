package designreport

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/analogsim"
)

// AnalogSection is §3A "模拟电路仿真 Analog SPICE" (report.json key
// "analog"): the analog blocks `pcbpilot sim analog` found, their ngspice
// metrics against the targets, tolerance spread and the proposed value
// changes. The full analog.json and the ngspice netlists / outputs travel in
// the report package under data/analog/ (see AnalogPackageFiles).
type AnalogSection struct {
	Generator string            `json:"generator"`
	Ngspice   string            `json:"ngspice"`
	Status    string            `json:"status"` // PASS | WARN | FAIL | INFO
	KeyNums   []KV              `json:"keyNumbers"`
	Blocks    []AnalogBlockRow  `json:"blocks"`
	Metrics   []AnalogMetricRow `json:"metrics"`
	Tolerance []AnalogMCRow     `json:"tolerance,omitempty"`
	Changes   []AnalogChangeRow `json:"changes,omitempty"`
	Findings  []Risk            `json:"findings,omitempty"`
	Charts    []AnalogChart     `json:"charts,omitempty"`
	Data      []string          `json:"data,omitempty"` // package-relative files (data/analog/…)
	Notes     []string          `json:"notes,omitempty"`

	out *analogsim.Output
}

// AnalogBlockRow is one analog block.
type AnalogBlockRow struct {
	ID        string `json:"id"`
	Class     string `json:"class"`
	ClassZh   string `json:"classZh"`
	Title     string `json:"title"`
	Topology  string `json:"topology"`
	Model     string `json:"model,omitempty"`
	Simulated bool   `json:"simulated"`
	Status    string `json:"status"`
	Parts     string `json:"parts"`
	Netlist   string `json:"netlist,omitempty"`
}

// AnalogMetricRow is one target-vs-simulated line.
type AnalogMetricRow struct {
	Block    string `json:"block"`
	Metric   string `json:"metric"`
	Label    string `json:"label"`
	Value    string `json:"value"`
	Analytic string `json:"analytic,omitempty"`
	Method   string `json:"method"`
	Target   string `json:"target,omitempty"`
	Source   string `json:"source,omitempty"`
	Status   string `json:"status,omitempty"`
}

// AnalogMCRow is one Monte-Carlo statistic.
type AnalogMCRow struct {
	Block   string `json:"block"`
	Phase   string `json:"phase"` // 当前 | 修改后
	Label   string `json:"label"`
	Nominal string `json:"nominal"`
	Min     string `json:"min"`
	Max     string `json:"max"`
	Std     string `json:"std"`
	InSpec  string `json:"inSpec"`
	Yield   string `json:"yield"`
	Method  string `json:"method"`
	Runs    int    `json:"runs"`
}

// AnalogChangeRow is one proposed value change.
type AnalogChangeRow struct {
	Block  string `json:"block"`
	Ref    string `json:"ref"`
	From   string `json:"from"`
	To     string `json:"to"`
	Action string `json:"action"`
	Part   string `json:"part"`
	Effect string `json:"effect"`
}

// AnalogChart names one inline chart.
type AnalogChart struct {
	Name  string `json:"name"` // chart file stem (charts/<name>.svg)
	Block string `json:"block"`
	Title string `json:"title"`
}

// AnalogPackageFile is a file the report package carries under data/analog/.
type AnalogPackageFile struct {
	Src  string // path on disk
	Dest string // package-relative path
}

// AnalogPackageFiles lists analog.json and every ngspice artifact it names
// (resolved against its workDir) for the report package's data/analog/.
func AnalogPackageFiles(out *analogsim.Output, analogPath string) []AnalogPackageFile {
	if out == nil {
		return nil
	}
	var files []AnalogPackageFile
	if analogPath != "" {
		files = append(files, AnalogPackageFile{Src: analogPath, Dest: "data/analog/analog.json"})
	}
	if out.WorkDir == "" {
		return files
	}
	// A relative work dir is relative to the analog.json folder (sim analog
	// records it that way).
	dir := out.WorkDir
	if !filepath.IsAbs(dir) && analogPath != "" {
		dir = filepath.Join(filepath.Dir(analogPath), filepath.FromSlash(dir))
	}
	seen := map[string]bool{}
	for _, a := range out.Artifacts {
		if a.Path == "" || seen[a.Path] {
			continue
		}
		seen[a.Path] = true
		files = append(files, AnalogPackageFile{Src: filepath.Join(dir, a.Path), Dest: "data/analog/ngspice/" + filepath.Base(a.Path)})
	}
	return files
}

// buildAnalog fills §3A from Inputs.Analog.
func (c *ctx) buildAnalog() {
	out := c.in.Analog
	if out == nil {
		return
	}
	s := &AnalogSection{Generator: out.Generator, Status: out.Summary.Status, out: out}
	if out.Ngspice.Available {
		s.Ngspice = fmt.Sprintf("%s（%d 次运行）", out.Ngspice.Version, out.Ngspice.Runs)
	} else {
		s.Ngspice = "未安装 — " + analogsim.MissingNgspiceNote
	}
	sm := out.Summary
	s.KeyNums = append(s.KeyNums,
		KV{"模拟块", sprintf("%d 个（仿真 %d）", sm.Blocks, sm.Simulated), ""},
		KV{"目标", sprintf("%d 项：满足 %d，未满足 %d", sm.Targets, sm.Met, sm.Failing), ""},
		KV{"建议修改", sprintf("%d 个元件值", sm.Changes), "原理图修改，须用户确认"},
	)
	for _, b := range out.Blocks {
		row := AnalogBlockRow{ID: b.ID, Class: b.Class, ClassZh: analogsim.ClassLabel(b.Class), Title: b.Title, Topology: b.Topology,
			Simulated: b.Simulated, Status: b.Status, Parts: strings.Join(b.Parts, ", ")}
		if b.Netlist != "" {
			row.Netlist = "data/analog/ngspice/" + filepath.Base(b.Netlist)
		}
		if m := b.Model; m != nil {
			row.Model = sprintf("%s（%s）", m.ID, m.Confidence)
		}
		s.Blocks = append(s.Blocks, row)
		for _, m := range b.Metrics {
			if m.Target == nil && !keyAnalogMetric(m.Name) {
				continue
			}
			r := AnalogMetricRow{Block: b.ID, Metric: m.Name, Label: m.Label, Value: analogsim.FormatMetric(m.Value, m.Unit), Method: m.Method, Status: m.Status}
			if m.Analytic != nil {
				r.Analytic = analogsim.FormatMetric(*m.Analytic, m.Unit)
			}
			if m.Target != nil {
				r.Target, r.Source = analogsim.FormatTarget(m.Target, m.Unit), m.Target.Source
			}
			s.Metrics = append(s.Metrics, r)
		}
		s.Tolerance = append(s.Tolerance, mcRows(b.ID, "当前", b.Tolerance)...)
		if o := b.Optimise; o != nil && o.Status == "improved" {
			s.Tolerance = append(s.Tolerance, mcRows(b.ID, "修改后", o.ToleranceAfter)...)
			for _, ch := range o.Changes {
				part := "需选型：" + ch.SearchHint
				if ch.Part != nil {
					part = ch.Part.LCSC + " " + ch.Part.Value
				}
				var eff []string
				for _, k := range sortedKeys(ch.Before) {
					l, u := analogsim.MetricLabel(k)
					eff = append(eff, sprintf("%s %s → %s", l, analogsim.FormatMetric(ch.Before[k], u), analogsim.FormatMetric(ch.After[k], u)))
				}
				s.Changes = append(s.Changes, AnalogChangeRow{Block: b.ID, Ref: ch.Ref, From: ch.From, To: ch.To, Action: ch.Action, Part: part, Effect: strings.Join(eff, "；")})
			}
		}
		for _, cv := range b.Curves {
			s.Charts = append(s.Charts, AnalogChart{Name: chartName(b.ID, cv.Name), Block: b.ID, Title: b.ID + " " + cv.Title})
		}
		s.Notes = append(s.Notes, b.Notes...)
	}
	for _, f := range out.Findings {
		s.Findings = append(s.Findings, Risk{Severity: f.Severity, Source: "analog " + f.Block, Message: f.Message, Suggestion: f.Suggestion})
	}
	if len(out.Blocks) == 0 {
		s.Notes = append(s.Notes, "未识别到模拟电路块")
	}
	for _, f := range AnalogPackageFiles(out, "analog.json") {
		s.Data = append(s.Data, f.Dest)
	}
	c.rep.Analog = s
}

func keyAnalogMetric(n string) bool {
	switch n {
	case "gain", "fcHz", "f0Hz", "q", "phaseMarginDeg", "overshootPct", "settleErrorLsb", "voutV", "ikA", "clPF", "freqErrorPpm", "delayS",
		"hysteresisV", "thresholdRiseV", "thresholdFallV", "icPeakA", "overdrive", "peakingDB", "outMaxV":
		return true
	}
	return false
}

func mcRows(block, phase string, t *analogsim.MCResult) []AnalogMCRow {
	if t == nil {
		return nil
	}
	y := "—"
	if t.YieldPc != nil {
		y = f1(*t.YieldPc) + " %"
	}
	var out []AnalogMCRow
	for _, k := range sortedKeys(t.Stats) {
		st := t.Stats[k]
		l, u := analogsim.MetricLabel(k)
		in := "—"
		if st.InSpec != nil {
			in = f1(*st.InSpec) + " %"
		}
		out = append(out, AnalogMCRow{Block: block, Phase: phase, Label: l, Nominal: analogsim.FormatMetric(st.Nominal, u), Min: analogsim.FormatMetric(st.Min, u),
			Max: analogsim.FormatMetric(st.Max, u), Std: analogsim.FormatMetric(st.Std, u), InSpec: in, Yield: y, Method: t.Method, Runs: t.Runs})
	}
	return out
}

func chartName(block, curve string) string {
	return "analog-" + strings.ToLower(block) + "-" + curve
}

// analogRisks feeds analog findings into the executive summary unless the
// intent already carries them (intent derive runs sim analog).
func (c *ctx) analogRisks() []Risk {
	if c.rep.Analog == nil || c.intentHasAnalog() {
		return nil
	}
	var out []Risk
	for _, f := range c.rep.Analog.Findings {
		if f.Severity == "error" || f.Severity == "warn" {
			out = append(out, f)
		}
	}
	return out
}

func (c *ctx) intentHasAnalog() bool {
	if it := c.in.Intent; it != nil {
		for _, f := range it.Findings {
			if strings.HasPrefix(f.Kind, "analog-") {
				return true
			}
		}
	}
	return false
}

// analogVerdict adds analog errors/warnings to the verdict (same rule as
// analogRisks: counted once).
func (c *ctx) analogVerdict(fail, warn func(string)) {
	a := c.rep.Analog
	if a == nil || c.intentHasAnalog() {
		return
	}
	n := map[string]int{}
	for _, f := range a.Findings {
		n[f.Severity]++
	}
	if n["error"] > 0 {
		fail(sprintf("模拟仿真有 %d 条 error 级发现", n["error"]))
	}
	if n["warn"] > 0 {
		warn(sprintf("模拟仿真有 %d 条 warn 级发现", n["warn"]))
	}
}

// AnalogCharts renders every analog curve as an inline SVG chart keyed by
// chart file stem (analog-<block>-<curve>).
func AnalogCharts(out *analogsim.Output) map[string]string {
	charts := map[string]string{}
	if out == nil {
		return charts
	}
	for _, b := range out.Blocks {
		for _, cv := range b.Curves {
			charts[chartName(b.ID, cv.Name)] = LineChartSVG(b.ID+" "+cv.Title, cv)
		}
	}
	return charts
}

func analogCharts(r *Report) map[string]string {
	if r.Analog == nil {
		return nil
	}
	return AnalogCharts(r.Analog.out)
}

// LineChartSVG draws one analog curve: log or linear x, left axis for the
// main series, right axis for series marked Axis "right" (phase, current),
// "after" traces dashed, marks as dashed reference lines / points.
func LineChartSVG(title string, cv analogsim.Curve) string {
	var b strings.Builder
	w, h := 720, 340
	left, right, top, bottom := 64, 64, 64, 52
	pw, ph := float64(w-left-right), float64(h-top-bottom)
	if len(cv.X) < 2 {
		svgOpen(&b, w, 120, title, "无数据")
		b.WriteString(`</svg>`)
		return b.String()
	}
	var lser, rser []analogsim.Series
	for _, s := range cv.Series {
		if s.Axis == "right" {
			rser = append(rser, s)
		} else {
			lser = append(lser, s)
		}
	}
	rng := func(ss []analogsim.Series, marks bool) (float64, float64) {
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, s := range ss {
			for _, v := range s.Y {
				if !math.IsNaN(v) && !math.IsInf(v, 0) {
					lo, hi = math.Min(lo, v), math.Max(hi, v)
				}
			}
		}
		if marks {
			for _, m := range cv.Marks {
				if m.Kind == "y" || m.Kind == "point" {
					lo, hi = math.Min(lo, m.Y), math.Max(hi, m.Y)
				}
			}
		}
		if math.IsInf(lo, 0) {
			return 0, 1
		}
		if hi-lo < 1e-12 {
			d := math.Max(math.Abs(hi)*0.1, 1e-3)
			lo, hi = lo-d, hi+d
		}
		pad := (hi - lo) * 0.05
		return lo - pad, hi + pad
	}
	x0, x1 := cv.X[0], cv.X[len(cv.X)-1]
	logX := cv.LogX && x0 > 0
	xp := func(x float64) float64 {
		if logX {
			return float64(left) + pw*(math.Log10(x)-math.Log10(x0))/(math.Log10(x1)-math.Log10(x0))
		}
		return float64(left) + pw*(x-x0)/(x1-x0)
	}
	llo, lhi := rng(lser, true)
	lt := niceTicks(llo, lhi, 5)
	llo, lhi = math.Min(llo, lt[0]), math.Max(lhi, lt[len(lt)-1])
	yl := func(v float64) float64 { return float64(top) + ph*(1-(v-llo)/(lhi-llo)) }
	var rlo, rhi float64
	var rt []float64
	if len(rser) > 0 {
		rlo, rhi = rng(rser, false)
		rt = niceTicks(rlo, rhi, 5)
		rlo, rhi = math.Min(rlo, rt[0]), math.Max(rhi, rt[len(rt)-1])
	}
	yr := func(v float64) float64 { return float64(top) + ph*(1-(v-rlo)/(rhi-rlo)) }
	svgOpen(&b, w, h, title, sprintf("折线图：%d 条曲线，横轴 %s（%s）%s", len(cv.Series), cv.XLabel, cv.XUnit, map[bool]string{true: "对数", false: ""}[logX]))
	// Legend.
	lx := 16
	for i, s := range cv.Series {
		dash := ""
		if s.Style == "after" {
			dash = ` stroke-dasharray="6 3"`
		}
		name := s.Name
		if s.Unit != "" {
			name += " (" + s.Unit + ")"
		}
		if s.Axis == "right" {
			name += " →"
		}
		fmt.Fprintf(&b, `<line class="k%d" x1="%d" x2="%d" y1="42" y2="42" stroke-width="2.5"%s/><text class="sm" x="%d" y="46">%s</text>`, i%8+1, lx, lx+18, dash, lx+22, esc(name))
		lx += 34 + textW(name)
	}
	// Grid + axes.
	for _, t := range lt {
		fmt.Fprintf(&b, `<line class="grid" x1="%d" x2="%d" y1="%.1f" y2="%.1f"/><text class="m sm" x="%d" y="%.1f" text-anchor="end">%s</text>`, left, w-right, yl(t), yl(t), left-6, yl(t)+4, esc(siTick(t)))
	}
	for _, t := range rt {
		fmt.Fprintf(&b, `<text class="m sm" x="%d" y="%.1f">%s</text>`, w-right+6, yr(t)+4, esc(siTick(t)))
	}
	var xt []float64
	if logX {
		for d := math.Floor(math.Log10(x0)); d <= math.Ceil(math.Log10(x1)); d++ {
			if v := math.Pow(10, d); v >= x0*0.999 && v <= x1*1.001 {
				xt = append(xt, v)
			}
		}
	} else {
		xt = niceTicks(x0, x1, 6)
	}
	for _, t := range xt {
		if t < x0-1e-15 || t > x1+1e-15 {
			continue
		}
		fmt.Fprintf(&b, `<line class="grid" x1="%.1f" x2="%.1f" y1="%d" y2="%d"/><text class="m sm" x="%.1f" y="%d" text-anchor="middle">%s</text>`, xp(t), xp(t), top, h-bottom, xp(t), h-bottom+16, esc(analogsim.FormatSI(t)+cv.XUnit))
	}
	fmt.Fprintf(&b, `<line class="axis" x1="%d" x2="%d" y1="%d" y2="%d"/>`, left, w-right, h-bottom, h-bottom)
	fmt.Fprintf(&b, `<text class="m sm" x="%d" y="%d" text-anchor="middle">%s</text>`, left+int(pw)/2, h-8, esc(cv.XLabel+" ("+cv.XUnit+")"))
	if len(lser) > 0 {
		fmt.Fprintf(&b, `<text class="m sm" transform="translate(14 %d) rotate(-90)" text-anchor="middle">%s</text>`, top+int(ph)/2, esc(lser[0].Unit))
	}
	if len(rser) > 0 {
		fmt.Fprintf(&b, `<text class="m sm" transform="translate(%d %d) rotate(90)" text-anchor="middle">%s</text>`, w-10, top+int(ph)/2, esc(rser[0].Unit))
	}
	// Marks.
	for _, m := range cv.Marks {
		switch m.Kind {
		case "y":
			if m.Y >= llo && m.Y <= lhi {
				fmt.Fprintf(&b, `<line class="thr2" x1="%d" x2="%d" y1="%.1f" y2="%.1f"><title>%s</title></line><text class="m sm lbl" x="%d" y="%.1f" text-anchor="end">%s</text>`, left, w-right, yl(m.Y), yl(m.Y), esc(m.Label), w-right-4, yl(m.Y)-4, esc(m.Label))
			}
		case "x":
			fmt.Fprintf(&b, `<line class="thr2" x1="%.1f" x2="%.1f" y1="%d" y2="%d"><title>%s</title></line>`, xp(m.X), xp(m.X), top, h-bottom, esc(m.Label))
		case "point":
			fmt.Fprintf(&b, `<circle class="s4" cx="%.1f" cy="%.1f" r="4"><title>%s</title></circle>`, xp(m.X), yl(m.Y), esc(m.Label+sprintf(" (%s, %s)", analogsim.FormatSI(m.X), analogsim.FormatSI(m.Y))))
		}
	}
	// Traces.
	for i, s := range cv.Series {
		y := yl
		if s.Axis == "right" {
			y = yr
		}
		var pts []string
		for k, v := range s.Y {
			if k >= len(cv.X) || math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			pts = append(pts, sprintf("%.1f,%.1f", xp(cv.X[k]), y(v)))
		}
		dash := ""
		if s.Style == "after" {
			dash = ` stroke-dasharray="6 3"`
		} else if s.Axis == "right" && logX {
			dash = ` stroke-dasharray="5 3"`
		}
		fmt.Fprintf(&b, `<polyline class="k%d" fill="none" stroke-width="1.8"%s points="%s"><title>%s</title></polyline>`, i%8+1, dash, strings.Join(pts, " "), esc(s.Name))
	}
	b.WriteString(`</svg>`)
	return b.String()
}

func siTick(v float64) string {
	if v == 0 {
		return "0"
	}
	if math.Abs(v) >= 1 && math.Abs(v) < 1000 {
		return trimF(v)
	}
	return analogsim.FormatSI(v)
}
