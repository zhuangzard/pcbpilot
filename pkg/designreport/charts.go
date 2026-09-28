package designreport

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Charts are pure inline SVG (no script, no external font or CDN). Colours
// are CSS custom properties with light defaults and a prefers-color-scheme
// dark override inside every SVG, so a chart reads the same inline in the
// HTML report and as a standalone file next to report.md. Every mark carries
// a <title> tooltip and every chart has a table twin in the report.

// Categorical order (validated reference palette) and status colours.
var seriesLight = []string{"#2a78d6", "#eb6834", "#1baf7a", "#eda100", "#e87ba4", "#008300", "#4a3aa7", "#e34948"}
var seriesDark = []string{"#3987e5", "#d95926", "#199e70", "#c98500", "#d55181", "#008300", "#9085e9", "#e66767"}

func chartStyle() string {
	var b strings.Builder
	b.WriteString(`<style>.rc{--rc-bg:#fcfcfb;--rc-ink:#0b0b0b;--rc-ink2:#52514e;--rc-grid:#e6e5e0;--rc-axis:#b9b8b2;--rc-box:#f3f2ee;--rc-ok:#0ca30c;--rc-warn:#fab219;--rc-bad:#d03b3b;--rc-unk:#9a9994;`)
	for i, c := range seriesLight {
		fmt.Fprintf(&b, "--rc-s%d:%s;", i+1, c)
	}
	b.WriteString(`}@media (prefers-color-scheme:dark){.rc{--rc-bg:#1a1a19;--rc-ink:#ffffff;--rc-ink2:#c3c2b7;--rc-grid:#34332f;--rc-axis:#5d5c57;--rc-box:#262624;`)
	for i, c := range seriesDark {
		fmt.Fprintf(&b, "--rc-s%d:%s;", i+1, c)
	}
	b.WriteString(`}}@media print{.rc{--rc-bg:#ffffff;--rc-ink:#000000;--rc-ink2:#333333;--rc-box:#f4f4f4}}`)
	b.WriteString(`.rc text{font-family:system-ui,-apple-system,"PingFang SC","Microsoft YaHei","Noto Sans CJK SC",sans-serif;font-size:12px;fill:var(--rc-ink)}`)
	b.WriteString(`.rc .m{fill:var(--rc-ink2)}.rc .sm{font-size:11px}.rc .ttl{font-size:14px;font-weight:600}.rc .grid{stroke:var(--rc-grid);stroke-width:1}.rc .axis{stroke:var(--rc-axis);stroke-width:1}`)
	b.WriteString(`.rc .bg{fill:var(--rc-bg)}.rc .ok{fill:var(--rc-ok)}.rc .warn{fill:var(--rc-warn)}.rc .bad{fill:var(--rc-bad)}.rc .unk{fill:var(--rc-unk)}.rc .box{fill:var(--rc-box);stroke:var(--rc-axis)}.rc .lbl{paint-order:stroke;stroke:var(--rc-bg);stroke-width:4px;stroke-linejoin:round}.rc .edge{fill:none;stroke:var(--rc-ink2);stroke-width:1.5}.rc .thr{stroke:var(--rc-bad);stroke-width:1.5;stroke-dasharray:4 3}.rc .thr2{stroke:var(--rc-warn);stroke-width:1.5;stroke-dasharray:4 3}`)
	for i := range seriesLight {
		fmt.Fprintf(&b, ".rc .s%d{fill:var(--rc-s%d)}.rc .k%d{stroke:var(--rc-s%d)}", i+1, i+1, i+1, i+1)
	}
	b.WriteString(`</style>`)
	return b.String()
}

func esc(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#39;")
		default:
			if r < 0x20 && r != '\n' && r != '\t' {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

func svgOpen(b *strings.Builder, w, h int, title, desc string) {
	fmt.Fprintf(b, `<svg xmlns="http://www.w3.org/2000/svg" class="rc" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="%s">`, w, h, w, h, esc(title))
	fmt.Fprintf(b, "<title>%s</title><desc>%s</desc>", esc(title), esc(desc))
	b.WriteString(chartStyle())
	fmt.Fprintf(b, `<rect class="bg" x="0" y="0" width="%d" height="%d" rx="6"/>`, w, h)
	fmt.Fprintf(b, `<text class="ttl" x="16" y="24">%s</text>`, esc(title))
}

// niceTicks returns rounded axis ticks covering [lo, hi].
func niceTicks(lo, hi float64, n int) []float64 {
	if hi <= lo {
		hi = lo + 1
	}
	raw := (hi - lo) / float64(n)
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	step := mag
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*mag >= raw {
			step = m * mag
			break
		}
	}
	start := math.Floor(lo/step) * step
	var out []float64
	for v := start; ; v += step {
		out = append(out, round(v, 9))
		if v >= hi-step*1e-9 || len(out) > 40 {
			break
		}
	}
	return out
}

func tickLabel(v float64) string {
	if math.Abs(v) >= 100 || v == math.Trunc(v) {
		return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.0f", v), ".0"), "")
	}
	return trimF(v)
}

// GroupedBars draws categories on x with one bar per series (legend on top).
func GroupedBars(title, yLabel, unit string, cats, series []string, vals [][]float64) string {
	var b strings.Builder
	w := 720
	h := 340
	left, right, top, bottom := 64, 16, 64, 56
	pw, ph := w-left-right, h-top-bottom
	maxV := 0.0
	for _, row := range vals {
		for _, v := range row {
			maxV = math.Max(maxV, v)
		}
	}
	ticks := niceTicks(0, maxV*1.1, 5)
	top0 := ticks[len(ticks)-1]
	svgOpen(&b, w, h, title, sprintf("分组柱状图：%d 个类别 × %d 个系列，单位 %s", len(cats), len(series), unit))
	// Legend.
	lx := 16
	for i, s := range series {
		fmt.Fprintf(&b, `<rect class="s%d" x="%d" y="36" width="12" height="12" rx="2"/><text class="sm" x="%d" y="46">%s</text>`, i%8+1, lx, lx+16, esc(s))
		lx += 28 + textW(s)
	}
	y := func(v float64) float64 { return float64(top) + float64(ph)*(1-v/top0) }
	for _, t := range ticks {
		fmt.Fprintf(&b, `<line class="grid" x1="%d" x2="%d" y1="%.1f" y2="%.1f"/><text class="m sm" x="%d" y="%.1f" text-anchor="end">%s</text>`, left, w-right, y(t), y(t), left-6, y(t)+4, tickLabel(t))
	}
	fmt.Fprintf(&b, `<text class="m sm" transform="translate(14 %d) rotate(-90)" text-anchor="middle">%s</text>`, top+ph/2, esc(yLabel+" ("+unit+")"))
	fmt.Fprintf(&b, `<line class="axis" x1="%d" x2="%d" y1="%.1f" y2="%.1f"/>`, left, w-right, y(0), y(0))
	if len(cats) > 0 && len(series) > 0 {
		gw := float64(pw) / float64(len(cats))
		bw := math.Min(28, (gw-16)/float64(len(series))-2)
		for ci, cat := range cats {
			gx := float64(left) + gw*float64(ci) + (gw-(bw+2)*float64(len(series)))/2
			for si := range series {
				v := 0.0
				if ci < len(vals) && si < len(vals[ci]) {
					v = vals[ci][si]
				}
				x := gx + float64(si)*(bw+2)
				hgt := y(0) - y(v)
				fmt.Fprintf(&b, `<rect class="s%d" x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="2"><title>%s · %s: %s %s</title></rect>`, si%8+1, x, y(v), bw, math.Max(hgt, 0.5), esc(cat), esc(series[si]), trimF(v), esc(unit))
			}
			fmt.Fprintf(&b, `<text class="sm" x="%.1f" y="%d" text-anchor="middle">%s</text>`, float64(left)+gw*(float64(ci)+0.5), h-bottom+18, esc(cat))
		}
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// HBar is one horizontal bar.
type HBar struct {
	Label string
	Value float64
	Class string // s1…s8 | ok | warn | bad | unk
	Tip   string
	Text  string // value label (default: value + unit)
}

// Threshold is a vertical reference line.
type Threshold struct {
	Value float64
	Label string
	Class string // thr | thr2
}

// HBars draws labelled horizontal bars on one value axis.
func HBars(title, xLabel, unit string, rows []HBar, lo, hi float64, thr []Threshold, legend [][2]string) string {
	var b strings.Builder
	w := 720
	labelW := 210
	rowH := 22
	top := 48
	if len(legend) > 0 {
		top = 64
	}
	h := top + rowH*len(rows) + 48
	if len(rows) == 0 {
		h = top + 60
	}
	right := 70
	pw := w - labelW - right
	for _, r := range rows {
		lo, hi = math.Min(lo, r.Value), math.Max(hi, r.Value)
	}
	for _, t := range thr {
		lo, hi = math.Min(lo, t.Value), math.Max(hi, t.Value)
	}
	ticks := niceTicks(lo, hi, 6)
	lo, hi = math.Min(lo, ticks[0]), ticks[len(ticks)-1]
	x := func(v float64) float64 { return float64(labelW) + float64(pw)*(v-lo)/(hi-lo) }
	svgOpen(&b, w, h, title, sprintf("横向柱状图：%d 行，单位 %s", len(rows), unit))
	lx := 16
	for _, l := range legend {
		fmt.Fprintf(&b, `<rect class="%s" x="%d" y="36" width="12" height="12" rx="2"/><text class="sm" x="%d" y="46">%s</text>`, l[0], lx, lx+16, esc(l[1]))
		lx += 28 + textW(l[1])
	}
	bottomY := top + rowH*len(rows)
	for _, t := range ticks {
		fmt.Fprintf(&b, `<line class="grid" x1="%.1f" x2="%.1f" y1="%d" y2="%d"/><text class="m sm" x="%.1f" y="%d" text-anchor="middle">%s</text>`, x(t), x(t), top-4, bottomY, x(t), bottomY+16, tickLabel(t))
	}
	fmt.Fprintf(&b, `<text class="m sm" x="%d" y="%d" text-anchor="middle">%s</text>`, labelW+pw/2, bottomY+34, esc(xLabel+" ("+unit+")"))
	zero := x(math.Max(lo, math.Min(0, hi)))
	fmt.Fprintf(&b, `<line class="axis" x1="%.1f" x2="%.1f" y1="%d" y2="%d"/>`, zero, zero, top-4, bottomY)
	for i, r := range rows {
		yy := top + i*rowH
		x0, x1 := zero, x(r.Value)
		if x1 < x0 {
			x0, x1 = x1, x0
		}
		cls := r.Class
		if cls == "" {
			cls = "s1"
		}
		tip := r.Tip
		if tip == "" {
			tip = r.Label + ": " + trimF(r.Value) + " " + unit
		}
		fmt.Fprintf(&b, `<text class="sm" x="%d" y="%d" text-anchor="end">%s</text>`, labelW-8, yy+15, esc(truncRunes(r.Label, 30)))
		fmt.Fprintf(&b, `<rect class="%s" x="%.1f" y="%d" width="%.1f" height="%d" rx="2"><title>%s</title></rect>`, cls, x0, yy+4, math.Max(x1-x0, 1), rowH-8, esc(tip))
		txt := r.Text
		if txt == "" {
			txt = trimF(r.Value) + " " + unit
		}
		tx := math.Max(x(r.Value), zero) + 4
		fmt.Fprintf(&b, `<text class="m sm" x="%.1f" y="%d">%s</text>`, tx, yy+15, esc(txt))
	}
	for _, t := range thr {
		cls := t.Class
		if cls == "" {
			cls = "thr"
		}
		fmt.Fprintf(&b, `<line class="%s" x1="%.1f" x2="%.1f" y1="%d" y2="%d"><title>%s</title></line><text class="m sm" x="%.1f" y="%d" text-anchor="middle">%s</text>`, cls, x(t.Value), x(t.Value), top-6, bottomY, esc(t.Label), x(t.Value), top-8, esc(t.Label))
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// textW estimates the rendered width of 11–12 px text (CJK ≈ 12 px).
func textW(s string) int {
	w := 0
	for _, r := range s {
		if r > 0x2E80 {
			w += 12
		} else {
			w += 7
		}
	}
	return w
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// PowerTreeSVG draws sources → OR → regulators → loads.
func PowerTreeSVG(t PowerTree) string {
	var b strings.Builder
	cols := map[int][]TreeNode{}
	maxCol := 0
	for _, n := range t.Nodes {
		cols[n.Col] = append(cols[n.Col], n)
		if n.Col > maxCol {
			maxCol = n.Col
		}
	}
	boxW, boxH, colW, gapY, top := 200, 66, 290, 34, 56
	maxN := 1
	for _, ns := range cols {
		if len(ns) > maxN {
			maxN = len(ns)
		}
	}
	w := 20 + colW*(maxCol+1)
	if w < 480 {
		w = 480
	}
	h := top + maxN*(boxH+gapY) + 10
	svgOpen(&b, w, h, "电源树 Power tree", sprintf("%d 个器件，%d 条电源连接", len(t.Nodes), len(t.Edges)))
	pos := map[string][2]float64{}
	var colIDs []int
	for c := range cols {
		colIDs = append(colIDs, c)
	}
	sort.Ints(colIDs)
	kindCls := map[string]string{"source": "s1", "or-diode": "s4", "regulator": "s3", "load": "s2"}
	kindName := map[string]string{"source": "输入源", "or-diode": "OR 二极管", "regulator": "稳压器", "load": "负载"}
	for _, c := range colIDs {
		ns := cols[c]
		off := float64((maxN-len(ns))*(boxH+gapY)) / 2
		for i, n := range ns {
			x := 16 + float64(c*colW)
			y := float64(top) + off + float64(i*(boxH+gapY))
			pos[n.Ref] = [2]float64{x, y}
		}
	}
	for _, e := range t.Edges {
		a, okA := pos[e.From]
		z, okZ := pos[e.To]
		if !okA || !okZ {
			continue
		}
		x1, y1 := a[0]+float64(boxW), a[1]+float64(boxH)/2
		x2, y2 := z[0], z[1]+float64(boxH)/2
		mx := (x1 + x2) / 2
		fmt.Fprintf(&b, `<path class="edge" d="M%.1f %.1f C%.1f %.1f %.1f %.1f %.1f %.1f"><title>%s</title></path>`, x1, y1, mx, y1, mx, y2, x2-6, y2, esc(e.Label))
		fmt.Fprintf(&b, `<path class="edge" d="M%.1f %.1f L%.1f %.1f L%.1f %.1f"/>`, x2-7, y2-4, x2-1, y2, x2-7, y2+4)
		fmt.Fprintf(&b, `<text class="m sm lbl" x="%.1f" y="%.1f" text-anchor="middle">%s</text>`, mx, (y1+y2)/2-6, esc(e.Net))
	}
	for _, c := range colIDs {
		for _, n := range cols[c] {
			p := pos[n.Ref]
			fmt.Fprintf(&b, `<rect class="box" x="%.1f" y="%.1f" width="%d" height="%d" rx="6"><title>%s — %s</title></rect>`, p[0], p[1], boxW, boxH, esc(n.Label), esc(n.Detail))
			fmt.Fprintf(&b, `<rect class="%s" x="%.1f" y="%.1f" width="5" height="%d" rx="2"/>`, kindCls[n.Kind], p[0], p[1], boxH)
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-weight="600">%s</text>`, p[0]+12, p[1]+17, esc(truncRunes(n.Label, 26)))
			parts := strings.Split(n.Detail, " · ")
			line1, line2 := strings.Join(parts[:min(2, len(parts))], " · "), ""
			if len(parts) > 2 {
				line2 = strings.Join(parts[2:], " · ")
			}
			fmt.Fprintf(&b, `<text class="m sm" x="%.1f" y="%.1f">%s</text>`, p[0]+12, p[1]+35, esc(truncRunes(line1, 32)))
			if line2 != "" {
				fmt.Fprintf(&b, `<text class="m sm" x="%.1f" y="%.1f">%s</text>`, p[0]+12, p[1]+51, esc(truncRunes(line2, 32)))
			} else {
				fmt.Fprintf(&b, `<text class="m sm" x="%.1f" y="%.1f">%s</text>`, p[0]+12, p[1]+51, esc(kindName[n.Kind]))
			}
		}
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// Charts renders every chart of a report, keyed by file stem.
func Charts(r *Report) map[string]string {
	out := map[string]string{}
	if p := r.Power; p != nil && len(p.Rails) > 0 {
		var cats []string
		var vals [][]float64
		for _, rr := range p.Rails {
			cats = append(cats, rr.Net)
			vals = append(vals, rr.Currents)
		}
		out["rail-current"] = GroupedBars("各场景电源轨电流 Rail current per scenario", "电流", "A", cats, p.Scenarios, vals)
		var rows []HBar
		for _, pp := range p.PartPower {
			rows = append(rows, HBar{Label: pp.Ref + " " + pp.Device, Value: pp.PowerW, Class: "s2", Tip: sprintf("%s %s（%s）: %s @%s", pp.Ref, pp.Device, pp.Kind, fW(pp.PowerW), pp.Scenario), Text: fW(pp.PowerW)})
		}
		out["part-power"] = HBars("器件功耗（最坏场景）Power per part", "功耗", "W", rows, 0, 0, nil, nil)
		rows = nil
		for _, rp := range p.RailPower {
			rows = append(rows, HBar{Label: rp.Net, Value: rp.PowerW, Class: "s1", Tip: sprintf("%s: %s × %s = %s", rp.Net, fV(rp.V), fA(rp.IA), fW(rp.PowerW)), Text: fW(rp.PowerW)})
		}
		out["rail-power"] = HBars("电源轨功率（V × Imax）Power per rail", "功率", "W", rows, 0, 0, nil, nil)
		if len(p.Tree.Nodes) > 0 {
			out["power-tree"] = PowerTreeSVG(p.Tree)
		}
	}
	for k, v := range analogCharts(r) {
		out[k] = v
	}
	if f := r.Feasibility; f != nil {
		var rows []HBar
		var srt []FeasRow
		for _, row := range f.Rows {
			if row.MarginPct != nil {
				srt = append(srt, row)
			}
		}
		sort.SliceStable(srt, func(i, j int) bool { return *srt[i].MarginPct < *srt[j].MarginPct })
		for _, row := range srt {
			cls := map[string]string{FeasOK: "ok", FeasMarginal: "warn", FeasOver: "bad"}[row.Status]
			v := math.Max(-50, math.Min(100, *row.MarginPct))
			rows = append(rows, HBar{Label: row.Ref + " " + row.Check, Value: v, Class: cls,
				Tip:  sprintf("%s %s: 应力 %s %s / 额定 %s %s → 余量 %s（%s，准则 ≥ %s %%）", row.Ref, row.Check, trimF(row.Stress), row.Unit, trimF(row.Rating), row.Unit, fPct(*row.MarginPct), statusZh(row.Status), f1(row.GuidePct)),
				Text: fPct(*row.MarginPct) + " " + statusZh(row.Status)})
		}
		out["margins"] = HBars("器件余量（升序）Component margin", "余量", "%", rows, 0, 100,
			[]Threshold{{Value: 20, Label: "20 %", Class: "thr2"}, {Value: 0, Label: "0", Class: "thr"}},
			[][2]string{{"ok", "满足准则"}, {"warn", "临界"}, {"bad", "超限"}})
	}
	if l := r.Layout; l != nil && len(l.IRPads) > 0 {
		var rows []HBar
		for _, p := range l.IRPads {
			cls := "ok"
			if p.RatioPct >= 100 {
				cls = "bad"
			} else if p.RatioPct >= 80 {
				cls = "warn"
			}
			rows = append(rows, HBar{Label: p.Net + " @ " + p.Pad, Value: p.RatioPct, Class: cls,
				Tip:  sprintf("%s @ %s: %s mV / 预算 %s mV（%s A）", p.Net, p.Pad, f3(p.DropMV), f1(p.BudgetMV), trimF(p.CurrentA)),
				Text: sprintf("%s mV（%s %%）", f2(p.DropMV), f1(p.RatioPct))})
		}
		out["ir-drop"] = HBars("负载焊盘直流压降 / 预算 IR drop per load pad", "占预算", "%", rows, 0, 100,
			[]Threshold{{Value: 100, Label: "预算 100 %", Class: "thr"}}, [][2]string{{"ok", "< 80 %"}, {"warn", "80–100 %"}, {"bad", "超预算"}})
	}
	return out
}

func statusZh(s string) string {
	switch s {
	case FeasOK:
		return "满足"
	case FeasMarginal:
		return "临界"
	case FeasOver:
		return "超限"
	case FeasUnknown:
		return "需数据手册"
	}
	return s
}
