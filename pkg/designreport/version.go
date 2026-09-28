package designreport

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Index is reports/<name>/index.json: every published version with the
// metrics the changelog compares.
type Index struct {
	SchemaVersion int          `json:"schemaVersion"`
	Generator     string       `json:"generator"`
	Project       string       `json:"project"`
	Customer      string       `json:"customer,omitempty"`
	Versions      []IndexEntry `json:"versions"`
}

// IndexEntry is one published version.
type IndexEntry struct {
	Version      int               `json:"version"`
	Label        string            `json:"label"`
	Dir          string            `json:"dir"`
	GeneratedAt  string            `json:"generatedAt"`
	Verdict      string            `json:"verdict"`
	InputsDigest string            `json:"inputsDigest"`
	Inputs       map[string]string `json:"inputs"` // kind:label → sha256
	Metrics      Metrics           `json:"metrics"`
	Findings     map[string]string `json:"findings"` // stable key → message
	Changes      *Changes          `json:"changes,omitempty"`
}

// Metrics are the numbers compared between versions.
type Metrics struct {
	RailCurrentA map[string]float64 `json:"railCurrentA,omitempty"`
	RailVoltageV map[string]float64 `json:"railVoltageV,omitempty"`
	IRDropMV     map[string]float64 `json:"irDropMV,omitempty"`
	MarginPct    map[string]float64 `json:"marginPct,omitempty"`
	Feasibility  map[string]int     `json:"feasibility,omitempty"`
	Completion   *float64           `json:"completionPct,omitempty"`
	Checks       map[string]string  `json:"checks,omitempty"`
	SuppliedW    map[string]float64 `json:"suppliedW,omitempty"`
	// IntentA is the planned (intent) current of power/ground/switch nets;
	// WidthNeedMil the IPC outer width it needs.
	IntentA      map[string]float64 `json:"intentCurrentA,omitempty"`
	WidthNeedMil map[string]float64 `json:"widthNeedMil,omitempty"`
}

// ParseIndex reads index.json (empty input = new index).
func ParseIndex(raw []byte) (*Index, error) {
	idx := &Index{SchemaVersion: SchemaVersion, Generator: Generator}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return idx, nil
	}
	if err := json.Unmarshal(raw, idx); err != nil {
		return nil, err
	}
	return idx, nil
}

// Latest is the highest version below v (v ≤ 0 = the highest overall).
func (idx *Index) Latest(below int) *IndexEntry {
	var best *IndexEntry
	for i := range idx.Versions {
		e := &idx.Versions[i]
		if (below <= 0 || e.Version < below) && (best == nil || e.Version > best.Version) {
			best = e
		}
	}
	return best
}

// Next is the next integer version.
func (idx *Index) Next() int {
	if l := idx.Latest(0); l != nil {
		return l.Version + 1
	}
	return 1
}

// Has reports whether a version exists.
func (idx *Index) Has(v int) bool {
	for _, e := range idx.Versions {
		if e.Version == v {
			return true
		}
	}
	return false
}

var reVersion = regexp.MustCompile(`^v?(\d+)$`)

// ResolveVersion turns "auto" / "vN" / "N" into a number.
func (idx *Index) ResolveVersion(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "auto") {
		return idx.Next(), nil
	}
	m := reVersion.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("--version %q: want auto, vN or N", s)
	}
	n, _ := strconv.Atoi(m[1])
	if n < 1 {
		return 0, fmt.Errorf("--version must be ≥ 1")
	}
	return n, nil
}

// Put inserts or replaces an entry, keeping versions sorted.
func (idx *Index) Put(e IndexEntry) {
	for i := range idx.Versions {
		if idx.Versions[i].Version == e.Version {
			idx.Versions[i] = e
			return
		}
	}
	idx.Versions = append(idx.Versions, e)
	sort.Slice(idx.Versions, func(i, j int) bool { return idx.Versions[i].Version < idx.Versions[j].Version })
}

// MetricsOf extracts the compared metrics of a report.
func MetricsOf(r *Report) Metrics {
	m := Metrics{Checks: map[string]string{}}
	if p := r.Power; p != nil {
		m.RailCurrentA, m.RailVoltageV, m.SuppliedW = map[string]float64{}, map[string]float64{}, map[string]float64{}
		for _, rr := range p.Rails {
			m.RailCurrentA[rr.Net], m.RailVoltageV[rr.Net] = round(rr.IMaxA, 6), round(rr.VNom, 4)
		}
		for _, sp := range p.ScenarioPwr {
			m.SuppliedW[sp.Scenario] = round(sp.SuppliedW, 5)
		}
	}
	if l := r.Layout; l != nil && len(l.IRNets) > 0 {
		m.IRDropMV = map[string]float64{}
		for _, n := range l.IRNets {
			m.IRDropMV[n.Net] = round(n.WorstMV, 3)
		}
		for _, kv := range l.Routing {
			if strings.HasPrefix(kv.Label, "信号连接完成率") {
				if f, err := strconv.ParseFloat(strings.Fields(kv.Value)[0], 64); err == nil {
					m.Completion = &f
				}
			}
		}
	}
	if f := r.Feasibility; f != nil {
		m.MarginPct, m.Feasibility = map[string]float64{}, map[string]int{}
		for _, row := range f.Rows {
			if row.MarginPct != nil {
				m.MarginPct[row.Ref+" "+row.Check] = *row.MarginPct
			}
		}
		for k, v := range f.Counts {
			m.Feasibility[k] = v
		}
	}
	if cs := r.Calcs; cs != nil {
		m.IntentA, m.WidthNeedMil = map[string]float64{}, map[string]float64{}
		for _, w := range cs.Widths {
			m.IntentA[w.Net], m.WidthNeedMil[w.Net] = w.CurrentA, w.OuterNeedMil
		}
	}
	for _, ch := range r.Verification {
		m.Checks[ch.Name] = ch.Status
	}
	m.Checks["总体结论"] = r.Verdict.Status
	return m
}

// FindingsOf returns the stable finding keys of a report.
func FindingsOf(r *Report) map[string]string {
	out := map[string]string{}
	if q := r.Requirements; q != nil {
		for _, f := range q.Findings {
			out[f.Source+"|"+f.Message] = f.Severity + ": " + f.Message
		}
	}
	if f := r.Feasibility; f != nil {
		for _, row := range f.Rows {
			if row.Status == FeasOver || row.Status == FeasMarginal {
				m := ""
				if row.MarginPct != nil {
					m = " 余量 " + fPct(*row.MarginPct)
				}
				out["feas|"+row.Ref+"|"+row.Check] = row.Status + ": " + row.Ref + " " + row.Check + m
			}
		}
	}
	for _, ch := range r.Verification {
		if ch.Status == StatusFail {
			out["check|"+ch.Name] = "FAIL: " + ch.Name
		}
	}
	if cs := r.Calcs; cs != nil {
		for _, w := range cs.Widths {
			if w.Status == StatusFail {
				out["calc|width|"+w.Net] = sprintf("FAIL: %s 线宽 %s/%s mil（外/内）< 需要 %s/%s mil @ %s A", w.Net, f2(w.OuterPlanMil), f2(w.InnerPlanMil), f2(w.OuterNeedMil), f2(w.InnerNeedMil), trimF(w.CurrentA))
			}
		}
		for _, v := range cs.Vias {
			if v.Status == StatusFail {
				out["calc|via|"+v.Net] = sprintf("FAIL: %s 过孔 %d < 需要 %d @ %s A", v.Net, v.Plan, v.Need, trimF(v.CurrentA))
			}
		}
	}
	return out
}

// EntryOf builds the index entry of a report.
func EntryOf(r *Report) IndexEntry {
	in := map[string]string{}
	for _, ref := range r.Inputs {
		if ref.Present {
			in[ref.Kind+":"+ref.Label] = ref.SHA256
		}
	}
	return IndexEntry{Version: r.Version, Label: r.VersionLabel, Dir: r.VersionLabel, GeneratedAt: r.GeneratedAt, Verdict: r.Verdict.Status,
		InputsDigest: r.InputsDigest, Inputs: in, Metrics: MetricsOf(r), Findings: FindingsOf(r), Changes: r.Changes}
}

// Compare computes the changes of cur against prev.
func Compare(prev *IndexEntry, cur IndexEntry) *Changes {
	if prev == nil {
		return nil
	}
	ch := &Changes{Previous: prev.Label, Identical: prev.InputsDigest == cur.InputsDigest}
	for _, k := range unionKeys(prev.Inputs, cur.Inputs) {
		a, b := prev.Inputs[k], cur.Inputs[k]
		switch {
		case a == "":
			ch.InputsChanged = append(ch.InputsChanged, "新增 "+k)
		case b == "":
			ch.InputsChanged = append(ch.InputsChanged, "移除 "+k)
		case a != b:
			ch.InputsChanged = append(ch.InputsChanged, "变更 "+k)
		}
	}
	pm, cm := prev.Metrics, cur.Metrics
	num := func(metric string, a, b map[string]float64, unit string, eps float64) {
		for _, k := range unionKeys(a, b) {
			x, okx := a[k]
			y, oky := b[k]
			switch {
			case okx && oky && math.Abs(x-y) > eps:
				ch.Rows = append(ch.Rows, ChangeRow{Metric: metric + " " + k, Before: trimF(x) + unit, After: trimF(y) + unit, Delta: signed(y-x) + unit})
			case okx && !oky:
				ch.Rows = append(ch.Rows, ChangeRow{Metric: metric + " " + k, Before: trimF(x) + unit, After: "—", Delta: "移除"})
			case !okx && oky:
				ch.Rows = append(ch.Rows, ChangeRow{Metric: metric + " " + k, Before: "—", After: trimF(y) + unit, Delta: "新增"})
			}
		}
	}
	num("电源轨电流", pm.RailCurrentA, cm.RailCurrentA, " A", 1e-4)
	num("电源轨电压", pm.RailVoltageV, cm.RailVoltageV, " V", 1e-3)
	num("输入功率", pm.SuppliedW, cm.SuppliedW, " W", 1e-4)
	num("IR 压降", pm.IRDropMV, cm.IRDropMV, " mV", 0.01)
	num("余量", pm.MarginPct, cm.MarginPct, " %", 0.05)
	num("意图电流", pm.IntentA, cm.IntentA, " A", 1e-4)
	num("所需线宽", pm.WidthNeedMil, cm.WidthNeedMil, " mil", 0.01)
	if (pm.Completion == nil) != (cm.Completion == nil) || (pm.Completion != nil && *pm.Completion != *cm.Completion) {
		ch.Rows = append(ch.Rows, ChangeRow{Metric: "布线完成率", Before: optPct(pm.Completion), After: optPct(cm.Completion), Delta: ""})
	}
	for _, k := range unionKeys(pm.Feasibility, cm.Feasibility) {
		if pm.Feasibility[k] != cm.Feasibility[k] {
			ch.Rows = append(ch.Rows, ChangeRow{Metric: "器件检查 " + k, Before: strconv.Itoa(pm.Feasibility[k]), After: strconv.Itoa(cm.Feasibility[k]), Delta: signed(float64(cm.Feasibility[k] - pm.Feasibility[k]))})
		}
	}
	for _, k := range unionKeys(pm.Checks, cm.Checks) {
		if pm.Checks[k] != cm.Checks[k] {
			ch.Rows = append(ch.Rows, ChangeRow{Metric: k, Before: orDash(pm.Checks[k]), After: orDash(cm.Checks[k])})
		}
	}
	for _, k := range unionKeys(prev.Findings, cur.Findings) {
		a, okA := prev.Findings[k]
		b, okB := cur.Findings[k]
		switch {
		case !okA && okB:
			ch.FindingsAdded = append(ch.FindingsAdded, b)
		case okA && !okB:
			ch.FindingsResolved = append(ch.FindingsResolved, a)
		}
	}
	return ch
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func optPct(p *float64) string {
	if p == nil {
		return "—"
	}
	return f1(*p) + " %"
}

func signed(v float64) string {
	if v > 0 {
		return "+" + trimF(v)
	}
	return trimF(v)
}

func unionKeys[V any](a, b map[string]V) []string {
	seen := map[string]bool{}
	var out []string
	for k := range a {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// RenderChangelog writes CHANGELOG.md from the index (newest first).
func RenderChangelog(idx *Index) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 设计报告变更记录 — %s\n\n", idx.Project)
	if idx.Customer != "" {
		fmt.Fprintf(&b, "客户：%s\n\n", idx.Customer)
	}
	b.WriteString("由 `pcbpilot report design` 生成；每个版本目录含 report.html（自包含）、report.md、report.json。\n\n")
	for i := len(idx.Versions) - 1; i >= 0; i-- {
		e := idx.Versions[i]
		fmt.Fprintf(&b, "## %s — %s\n\n", e.Label, e.Verdict)
		fmt.Fprintf(&b, "- 生成时间：%s\n- 报告：[%s/report.html](%s/report.html) · [report.md](%s/report.md) · [report.json](%s/report.json)\n- 输入摘要：`%s`\n\n",
			e.GeneratedAt, e.Dir, e.Dir, e.Dir, e.Dir, short(e.InputsDigest))
		ch := e.Changes
		if ch == nil {
			b.WriteString("首个版本。\n\n")
			continue
		}
		fmt.Fprintf(&b, "相对 %s：", ch.Previous)
		if ch.Identical {
			b.WriteString("输入完全相同（重新生成）。\n\n")
		} else {
			b.WriteString(strings.Join(ch.InputsChanged, "；") + "。\n\n")
		}
		if len(ch.Rows) > 0 {
			b.WriteString("| 指标 | 之前 | 之后 | 变化 |\n|---|---|---|---|\n")
			for _, r := range ch.Rows {
				fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", mdEsc(r.Metric), mdEsc(r.Before), mdEsc(r.After), mdEsc(r.Delta))
			}
			b.WriteString("\n")
		}
		for _, f := range ch.FindingsAdded {
			fmt.Fprintf(&b, "- 新增问题：%s\n", mdEsc(f))
		}
		for _, f := range ch.FindingsResolved {
			fmt.Fprintf(&b, "- 已解决：%s\n", mdEsc(f))
		}
		if len(ch.FindingsAdded)+len(ch.FindingsResolved) > 0 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func mdEsc(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s)
}
