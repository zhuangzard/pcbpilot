package schaes

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Metric groups.
const (
	GroupWiring = "wiring"
	GroupLayout = "layout"
	GroupLabels = "labels"
)

// SchemaVersion of the JSON report.
const SchemaVersion = 1

// metricNames is the metric catalog (ID → group, name). IDs are a public
// contract (JSON keys, style metricWeights).
var metricNames = map[string][2]string{
	"W1": {GroupWiring, "转折/连接 bends per connection"},
	"W2": {GroupWiring, "异网交叉 wire crossings"},
	"W3": {GroupWiring, "四通结点/同网歧义交叉 4-way & ambiguous X"},
	"W4": {GroupWiring, "共线重叠 collinear overlap"},
	"W5": {GroupWiring, "T 结点质量 T-junction quality"},
	"W6": {GroupWiring, "绕行比 detour vs Manhattan"},
	"W7": {GroupWiring, "穿本体/标签/文字 wire through bodies & text"},
	"W8": {GroupWiring, "落格 grid landing"},
	"L1": {GroupLayout, "信号流向 signal flow"},
	"L2": {GroupLayout, "标记朝向一致 marker orientation"},
	"L3": {GroupLayout, "行列对齐 row/column alignment"},
	"L4": {GroupLayout, "间距均匀 spacing uniformity"},
	"L5": {GroupLayout, "模块框整洁 frame tidiness"},
	"L6": {GroupLayout, "文字重叠 text overlaps"},
	"L7": {GroupLayout, "版面均衡 sheet balance"},
	"N1": {GroupLabels, "长线宜改标签 long wire → label"},
	"N2": {GroupLabels, "短程滥用标签 label overuse"},
	"N3": {GroupLabels, "总线/虚拟总线 bus lanes"},
}

var metricOrder = []string{"W1", "W2", "W3", "W4", "W5", "W6", "W7", "W8", "L1", "L2", "L3", "L4", "L5", "L6", "L7", "N1", "N2", "N3"}

// Offender is one worst object of a metric.
type Offender struct {
	Ref   string  `json:"ref,omitempty"`
	Net   string  `json:"net,omitempty"`
	At    *Pt     `json:"at,omitempty"`
	Value float64 `json:"value"`
	Note  string  `json:"note,omitempty"`
}

// Metric is one measured (or skipped) aesthetic.
type Metric struct {
	ID         string             `json:"id"`
	Group      string             `json:"group"`
	Name       string             `json:"name"`
	Value      float64            `json:"value"`
	Unit       string             `json:"unit"`
	Score      float64            `json:"score"` // 0–100, meaningless when Skipped
	Weight     float64            `json:"weight"`
	Skipped    bool               `json:"skipped,omitempty"`
	Reason     string             `json:"reason,omitempty"`
	Detail     string             `json:"detail,omitempty"`
	Thresholds string             `json:"thresholds"`
	Source     string             `json:"source"`
	Extra      map[string]float64 `json:"extra,omitempty"`
	Worst      []Offender         `json:"worst,omitempty"`
}

// LaneRow reports the label lane of one bus candidate (N3).
type LaneRow struct {
	Candidate BusCandidate `json:"candidate"`
	Markers   int          `json:"markers"`
	NativeBus string       `json:"nativeBus,omitempty"`
	Aligned   float64      `json:"aligned"`
	PitchCV   float64      `json:"pitchCV"`
	SameDir   float64      `json:"sameDir"`
	Score     float64      `json:"score"`
	Scored    bool         `json:"scored"`
	Note      string       `json:"note"`
}

// Report is the schematic aesthetics report.
type Report struct {
	SchemaVersion int                `json:"schemaVersion"`
	Source        string             `json:"source"`
	Score         float64            `json:"score"`
	Verdict       string             `json:"verdict"` // excellent | good | fair | poor | incomplete | unscored
	Groups        map[string]float64 `json:"groups"`
	// WiredShare is the share of netted, non-NC pins reached by a wire; the
	// wiring group is multiplied by it (unwired pins never look tidy).
	WiredShare    float64        `json:"wiredShare"`
	Weight        float64        `json:"weight"` // applied weight: 0 (report-only)
	Tier          string         `json:"tier"`
	Profile       Profile        `json:"profile"`
	Metrics       []Metric       `json:"metrics"`
	Lanes         []LaneRow      `json:"lanes,omitempty"`
	BusCandidates []BusCandidate `json:"busCandidates"`
	Counts        map[string]int `json:"counts"`
	Measured      int            `json:"measured"`
	Skipped       int            `json:"skipped"`
	Priority      []string       `json:"priority"`
	Boundary      string         `json:"boundary"`
	Notes         []string       `json:"notes,omitempty"`
}

// Metric returns a metric by id.
func (r *Report) Metric(id string) *Metric {
	for i := range r.Metrics {
		if r.Metrics[i].ID == id {
			return &r.Metrics[i]
		}
	}
	return nil
}

// Priority is the fixed constraint order. Aesthetics is the last tier.
var Priority = []string{
	"1 连接正确性 connectivity: every flag/port on a real wire, no false connection (X ≠ contact, T = contact), NC preserved, core + exclusive peripherals owned together (hard: sch check / layout-lint / bridge-check / DRC)",
	"2 可读性 readability: no overlaps, designators visible, markers point outward, frames enclose their content (hard/soft: layout-lint, sch check, layout-score)",
	"3 美观 aesthetics: this report — soft, weight 0, never a gate, never a reason to move a wire that is already correct",
}

const boundary = "只报告：权重 0，不进 layout-score / layout-lint / sch check / gate，不改任何几何；连接正确性 > 可读性 > 美观，任何美化不得改变连接、NC 或核心/外围归属"

// Analyze scores one snapshot. prof nil = balanced; Name "auto" = AutoProfile.
func Analyze(s *Snapshot, prof *Profile) *Report {
	var p Profile
	switch {
	case prof == nil:
		p, _ = ProfileByName("")
	case prof.Name == "auto":
		p = AutoProfile(s)
	default:
		p = prof.clone()
		p.Auto = prof.Auto
	}
	if p.GroupWeights == nil {
		p = p.clone()
	}
	rep := &Report{SchemaVersion: SchemaVersion, Source: s.Source, Profile: p, Weight: 0,
		Tier: "lowest (after connectivity, readability)", Priority: Priority, Boundary: boundary,
		Groups: map[string]float64{}, Notes: append([]string(nil), s.Notes...)}
	t := buildTopo(s)
	cr := t.crossings()
	rep.Counts = map[string]int{"parts": len(s.Parts), "wires": len(s.Wires), "segments": len(t.segs), "islands": len(t.islands),
		"markers": len(s.Markers), "texts": len(s.Texts), "frames": len(s.Frames), "buses": len(s.Buses), "connections": t.connections(), "crossings": len(cr)}
	a := &analyzer{s: s, t: t, p: &p, cr: cr}
	for _, id := range metricOrder {
		m := a.metric(id)
		m.ID, m.Group, m.Name = id, metricNames[id][0], metricNames[id][1]
		m.Weight = p.metricWeight(id)
		if m.Skipped {
			m.Score, m.Value = 0, 0
		} else {
			m.Score, m.Value = round1(m.Score), round3(m.Value)
		}
		if len(m.Worst) > 8 {
			m.Worst = m.Worst[:8]
		}
		rep.Metrics = append(rep.Metrics, m)
	}
	rep.Lanes = a.lanes
	rep.BusCandidates = a.candidates()
	// aggregate: group = weighted mean of measured metrics; overall = group-weighted mean
	gs, gw := map[string]float64{}, map[string]float64{}
	for _, m := range rep.Metrics {
		if m.Skipped {
			rep.Skipped++
			continue
		}
		rep.Measured++
		gs[m.Group] += m.Score * m.Weight
		gw[m.Group] += m.Weight
	}
	// Anti-gaming (same rule as pcb aesthetics' routed share): a netted pin
	// that no wire reaches cannot make the wiring look better. The wiring
	// group is scaled by the wired share of netted, non-NC pins.
	rep.WiredShare = 1
	if s.HasWires && s.HasPins {
		netted, wired := 0, 0
		for pi, p := range s.Parts {
			for qi, q := range p.Pins {
				if q.Net == "" || q.NC {
					continue
				}
				netted++
				if _, ok := t.pinIsl[[2]int{pi, qi}]; ok {
					wired++
				}
			}
		}
		if netted > 0 {
			rep.WiredShare = round3(float64(wired) / float64(netted))
		}
		rep.Counts["nettedPins"], rep.Counts["wiredPins"] = netted, wired
	}
	tot, totW := 0.0, 0.0
	for _, g := range []string{GroupWiring, GroupLayout, GroupLabels} {
		if gw[g] <= 0 {
			continue
		}
		v := gs[g] / gw[g]
		if g == GroupWiring {
			v *= rep.WiredShare
		}
		rep.Groups[g] = round1(v)
		w := p.GroupWeights[g]
		tot += v * w
		totW += w
	}
	if totW > 0 {
		rep.Score = round1(tot / totW)
	}
	switch {
	case rep.Measured == 0:
		rep.Verdict = "unscored"
	case rep.Skipped > 0:
		rep.Verdict = "incomplete"
	case rep.Score >= 90:
		rep.Verdict = "excellent"
	case rep.Score >= 75:
		rep.Verdict = "good"
	case rep.Score >= 55:
		rep.Verdict = "fair"
	default:
		rep.Verdict = "poor"
	}
	return rep
}

type analyzer struct {
	s     *Snapshot
	t     *topo
	p     *Profile
	cr    []crossing
	lanes []LaneRow
	cands []BusCandidate
}

func (a *analyzer) candidates() []BusCandidate {
	if a.cands == nil {
		a.cands = DetectBusCandidates(SnapshotNets(a.s))
		if a.cands == nil {
			a.cands = []BusCandidate{}
		}
	}
	return a.cands
}

func (a *analyzer) nettedPins() int {
	n := 0
	for _, p := range a.s.Parts {
		for _, q := range p.Pins {
			if q.Net != "" && !q.NC {
				n++
			}
		}
	}
	return n
}

func skip(reason string) Metric { return Metric{Skipped: true, Reason: reason} }

func ptr(p Pt) *Pt { q := Pt{round1(p.X), round1(p.Y)}; return &q }

const judgment = "数值为工程判断，未经人审校准（Phase E 以 A/B 偏好拟合）"

func (a *analyzer) metric(id string) Metric {
	needWires := func() (Metric, bool) {
		if !a.s.HasWires {
			return skip("source carries no wires (canonical / components.list without --include-wires)"), false
		}
		if len(a.t.segs) == 0 {
			if n := a.nettedPins(); n > 0 {
				// nothing drawn is not "perfectly tidy": scored 0 (anti-gaming)
				return Metric{Score: 0, Unit: "—", Detail: fmt.Sprintf("no wire segments while %d netted pins exist", n),
					Thresholds: "no wires with netted pins → 0", Source: "防刷分：未连接不能显得整齐（与 pcb aesthetics 未布通按 0 同一原则）"}, false
			}
			return skip("page has no wire segments"), false
		}
		return Metric{}, true
	}
	switch id {
	case "W1":
		if m, ok := needWires(); !ok {
			return m
		}
		return a.w1()
	case "W2":
		if m, ok := needWires(); !ok {
			return m
		}
		return a.w2()
	case "W3":
		if m, ok := needWires(); !ok {
			return m
		}
		return a.w3()
	case "W4":
		if m, ok := needWires(); !ok {
			return m
		}
		return a.w4()
	case "W5":
		if m, ok := needWires(); !ok {
			return m
		}
		return a.w5()
	case "W6":
		if m, ok := needWires(); !ok {
			return m
		}
		return a.w6()
	case "W7":
		if m, ok := needWires(); !ok {
			return m
		}
		return a.w7()
	case "W8":
		return a.w8()
	case "L1":
		return a.l1()
	case "L2":
		return a.l2()
	case "L3":
		return a.l3()
	case "L4":
		return a.l4()
	case "L5":
		return a.l5()
	case "L6":
		return a.l6()
	case "L7":
		return a.l7()
	case "N1":
		if m, ok := needWires(); !ok {
			return m
		}
		return a.n1()
	case "N2":
		return a.n2()
	case "N3":
		return a.n3()
	}
	return skip("unknown metric")
}

// ---------------------------------------------------------------------------
// wiring

func (a *analyzer) w1() Metric {
	perIsl := map[int]int{}
	var bends []Offender
	for _, n := range a.t.sortedNodes() {
		if bendAt(n) {
			is := a.t.nodeIsl[qkey(n.at)]
			perIsl[is]++
			bends = append(bends, Offender{Net: a.t.islands[is].net(), At: ptr(n.at), Value: 1})
		}
	}
	total := len(bends)
	conn := a.t.connections()
	v := float64(total) / math.Max(1, float64(conn))
	var worst []Offender
	for _, is := range a.t.islands {
		if c := perIsl[is.id]; c > 0 {
			worst = append(worst, Offender{Net: is.net(), At: ptr(a.t.segs[is.segs[0]].a), Value: float64(c),
				Note: fmt.Sprintf("%d bends over %d terminals", c, len(is.terms))})
		}
	}
	sortWorst(worst)
	return Metric{Value: v, Unit: "bends/connection", Score: ramp(v, 0.5, 2.5), Worst: worst,
		Detail:     fmt.Sprintf("%d bends / %d pin-to-pin connections", total, conn),
		Thresholds: "≤0.5 → 100, ≥2.5 → 0",
		Source:     "IEC 61082-1:2014 连接线规则（直线、少转折）；" + judgment}
}

func (a *analyzer) w2() Metric {
	n := 0
	var worst []Offender
	for _, c := range a.cr {
		if c.netA == c.netB && c.netA != "" {
			continue // same-net X is W3
		}
		n++
		worst = append(worst, Offender{Net: c.netA + "×" + c.netB, At: ptr(c.at), Value: 1, Note: "strict X (no contact)"})
	}
	conn := a.t.connections()
	v := float64(n) / math.Max(1, float64(conn))
	return Metric{Value: v, Unit: "crossings/connection", Score: ramp(v, 0, 0.25), Worst: worst,
		Extra:      map[string]float64{"crossings": float64(n)},
		Detail:     fmt.Sprintf("%d different-net crossings / %d connections", n, conn),
		Thresholds: "0 → 100, ≥0.25/connection → 0",
		Source:     "IEC 61082-1:2014 尽量避免连接线交叉；严格内部 X 不导通（schematic-data.md，3.2.186 实测）；" + judgment}
}

func (a *analyzer) w3() Metric {
	four, ambiguous := 0, 0
	var worst []Offender
	for _, n := range a.t.sortedNodes() {
		if len(n.arms) >= 4 {
			four++
			worst = append(worst, Offender{Net: a.t.islands[a.t.nodeIsl[qkey(n.at)]].net(), At: ptr(n.at), Value: float64(len(n.arms)),
				Note: fmt.Sprintf("%d-way junction: use two staggered T", len(n.arms))})
		}
	}
	for _, c := range a.cr {
		if c.netA == c.netB && c.netA != "" {
			ambiguous++
			worst = append(worst, Offender{Net: c.netA, At: ptr(c.at), Value: 1, Note: "same-net X without contact: reads as a junction but is two islands"})
		}
	}
	v := float64(four + ambiguous)
	return Metric{Value: v, Unit: "count", Score: math.Max(0, 100-25*v), Worst: worst,
		Extra:      map[string]float64{"fourWay": float64(four), "ambiguousX": float64(ambiguous)},
		Detail:     fmt.Sprintf("%d four-way junctions, %d same-net X crossings", four, ambiguous),
		Thresholds: "each −25 (0 → 100, ≥4 → 0)",
		Source:     "IEEE Std 315-1975 / IEC 60617：连接点用错开的 T，不用十字四通；同网 X 在 EasyEDA 不导通（schematic-data.md）"}
}

func (a *analyzer) w4() Metric {
	n, length := 0, 0.0
	var worst []Offender
	for i := 0; i < len(a.t.segs); i++ {
		for j := i + 1; j < len(a.t.segs); j++ {
			s1, s2 := a.t.segs[i], a.t.segs[j]
			if ov := collinearOverlap(s1, s2); ov > 0.5 {
				n++
				length += ov
				note := "redundant same-net overlap"
				if s1.net != s2.net && s1.net != "" && s2.net != "" {
					note = "different-net overlap (electrical: see sch check multi-net-wire)"
				}
				worst = append(worst, Offender{Net: s1.net, At: ptr(s1.a), Value: round1(ov), Note: note})
			}
		}
	}
	sortWorst(worst)
	return Metric{Value: float64(n), Unit: "pairs", Score: ramp(float64(n), 0, 4), Worst: worst,
		Extra:      map[string]float64{"overlapLength": round1(length)},
		Detail:     fmt.Sprintf("%d collinear overlapping segment pairs, %.0f units", n, length),
		Thresholds: "0 → 100, ≥4 pairs → 0",
		Source:     "导线不得重叠走线（schematic.md 电气规则：导线不能经过异网线）；" + judgment}
}

func (a *analyzer) w5() Metric {
	var junctions []*node
	var others []*node
	for _, n := range a.t.sortedNodes() {
		if len(n.arms) == 3 {
			junctions = append(junctions, n)
		}
		if len(n.arms) >= 2 {
			others = append(others, n)
		}
	}
	if len(junctions) == 0 {
		return Metric{Value: 1, Unit: "clean share", Score: 100, Detail: "no T-junctions (stub + label style)",
			Thresholds: "clean share × 100", Source: "IEEE Std 315-1975 T 结点；拥挤距 2 格（10 units，见 W8 落格）"}
	}
	const crowd = 10.0
	clean := 0
	var worst []Offender
	for _, j := range junctions {
		bad := ""
		for _, d := range j.arms {
			if math.Abs(d.X) > 1e-6 && math.Abs(d.Y) > 1e-6 {
				bad = "diagonal arm"
			}
		}
		if bad == "" && j.pin {
			bad = "junction on a pin"
		}
		if bad == "" {
			is := a.t.nodeIsl[qkey(j.at)]
			for _, o := range others {
				if o == j || a.t.nodeIsl[qkey(o.at)] != is {
					continue
				}
				if d := manhattan(o.at, j.at); d > eps && d < crowd {
					bad = fmt.Sprintf("crowded: another node %.0f units away", d)
					break
				}
			}
		}
		if bad == "" {
			clean++
			continue
		}
		worst = append(worst, Offender{Net: a.t.islands[a.t.nodeIsl[qkey(j.at)]].net(), At: ptr(j.at), Value: 1, Note: bad})
	}
	v := float64(clean) / float64(len(junctions))
	return Metric{Value: v, Unit: "clean share", Score: 100 * v, Worst: worst,
		Detail:     fmt.Sprintf("%d/%d T-junctions clean (orthogonal, not on a pin, ≥10 units from other nodes)", clean, len(junctions)),
		Thresholds: "clean share × 100",
		Source:     "IEEE Std 315-1975 T 结点；拥挤距 2 格（10 units = 2 × 5-unit 连接格，layout-lint off-grid 同一格）；" + judgment}
}

func (a *analyzer) w6() Metric {
	sumL, sumR := 0.0, 0.0
	var worst []Offender
	for _, is := range a.t.islands {
		if len(is.terms) < 2 {
			continue
		}
		var pts []Pt
		for _, tm := range is.terms {
			pts = append(pts, tm.at)
		}
		r := rmstManhattan(pts)
		if r < eps {
			continue
		}
		sumL += is.length
		sumR += r
		if ratio := is.length / r; ratio > 1.05 {
			worst = append(worst, Offender{Net: is.net(), At: ptr(is.terms[0].at), Value: round3(ratio),
				Note: fmt.Sprintf("%.0f units of wire for a %.0f-unit Manhattan tree", is.length, r)})
		}
	}
	if sumR == 0 {
		return skip("no multi-terminal wire trees")
	}
	sortWorst(worst)
	v := sumL / sumR
	return Metric{Value: v, Unit: "wire/RMST", Score: ramp(v, 1.1, 2.0), Worst: worst,
		Detail:     fmt.Sprintf("Σwire %.0f / Σ rectilinear MST %.0f", sumL, sumR),
		Thresholds: "≤1.1 → 100, ≥2.0 → 0 (RSMT ≥ ⅔·RMST, Hwang 1976)",
		Source:     "曼哈顿最小生成树下界（Hwang 1976）；" + judgment}
}

func (a *analyzer) w7() Metric {
	n := 0
	var worst []Offender
	for _, sg := range a.t.segs {
		hit := ""
		for _, p := range a.s.Parts {
			if !p.HasBox {
				continue
			}
			if segClipLen(sg, p.Box.Grow(-0.5)) > 1 {
				hit = "through body of " + p.Ref
				break
			}
		}
		if hit == "" {
			for mi, m := range a.s.Markers {
				if sg.has(m.Anchor) || onSegment(sg.a, sg.b, m.Anchor) || a.t.markerIsl[mi] == a.t.segIsl[sg.i] && m.Kind == KindNetLabel {
					continue // own stub / wire under its own label
				}
				if segClipLen(sg, m.Box.Grow(-0.5)) > 1 {
					hit = "through marker " + m.Net
					break
				}
			}
		}
		if hit == "" {
			for _, tx := range a.s.Texts {
				if segClipLen(sg, tx.Box.Grow(-0.5)) > 1 {
					hit = fmt.Sprintf("through %s text %q", tx.Kind, tx.Content)
					break
				}
			}
		}
		if hit != "" {
			n++
			worst = append(worst, Offender{Net: sg.net, At: ptr(sg.a), Value: 1, Note: hit})
		}
	}
	v := float64(n) / math.Max(1, float64(len(a.t.segs)))
	return Metric{Value: v, Unit: "share of segments", Score: ramp(v, 0, 0.15), Worst: worst,
		Extra:      map[string]float64{"segments": float64(n)},
		Detail:     fmt.Sprintf("%d of %d segments cross a body, marker or text", n, len(a.t.segs)),
		Thresholds: "0 → 100, ≥15% of segments → 0",
		Source:     "wire-through-body 是 sch check 的 ERROR（这里只按软项重复计数）；标记/位号遮挡见 schematic-data.md 页面碰撞统一范围"}
}

func (a *analyzer) w8() Metric {
	// audit set: every wire vertex and pin must sit on the 5-unit connection grid
	var pts []Pt
	pinAt := map[[2]int64]bool{}
	for _, p := range a.s.Parts {
		for _, q := range p.Pins {
			pts = append(pts, Pt{q.X, q.Y})
			pinAt[qkey(Pt{q.X, q.Y})] = true
		}
	}
	// target set: part anchors and free wire vertices (bends / ends not on a
	// pin). Pin positions follow the symbol's own pin pitch, so they are not
	// held to the coarser target grid.
	var tpts []Pt
	for _, p := range a.s.Parts {
		tpts = append(tpts, Pt{p.X, p.Y})
	}
	for _, w := range a.s.Wires {
		if w.Implicit {
			continue
		}
		pts = append(pts, w.Pts...)
		for _, q := range w.Pts {
			if !pinAt[qkey(q)] {
				tpts = append(tpts, q)
			}
		}
	}
	if len(pts) == 0 {
		return skip("no wire vertices or pins")
	}
	g, tg := 0, 0
	var worst []Offender
	for _, q := range pts {
		if onGrid(q.X, a.p.GridUnits) && onGrid(q.Y, a.p.GridUnits) {
			g++
		} else {
			worst = append(worst, Offender{At: ptr(q), Value: 1, Note: fmt.Sprintf("off the %g-unit grid", a.p.GridUnits)})
		}
	}
	for _, q := range tpts {
		if onGrid(q.X, a.p.TargetGrid) && onGrid(q.Y, a.p.TargetGrid) {
			tg++
		}
	}
	f, ft := float64(g)/float64(len(pts)), 1.0
	if len(tpts) > 0 {
		ft = float64(tg) / float64(len(tpts))
	}
	return Metric{Value: f, Unit: "share on grid", Score: 100 * ((1-a.p.GridBlend)*f + a.p.GridBlend*ft), Worst: worst,
		Extra:      map[string]float64{"targetGridShare": round3(ft)},
		Detail:     fmt.Sprintf("%d/%d pins+vertices on %g; %d/%d anchors+free vertices on %g", g, len(pts), a.p.GridUnits, tg, len(tpts), a.p.TargetGrid),
		Thresholds: fmt.Sprintf("score = 100 × ((1−%.1f)·share@%g + %.1f·share@%g)", a.p.GridBlend, a.p.GridUnits, a.p.GridBlend, a.p.TargetGrid),
		Source:     "5-unit 连接格 = layout-lint off-grid 判据（internal/app/cmd_sch_layout.go detectOffGridAnchors）；10-unit 为 0.1 in 制图惯例（只要求器件锚点与非引脚顶点）"}
}

// ---------------------------------------------------------------------------
// layout

func isConnectorRef(ref string) bool {
	u := strings.ToUpper(ref)
	for _, p := range []string{"USB", "CN", "J", "P", "X", "H"} {
		if strings.HasPrefix(u, p) && len(u) > len(p) && u[len(p)] >= '0' && u[len(p)] <= '9' {
			return true
		}
	}
	return false
}

func (a *analyzer) contentBox() Box {
	var b Box
	for _, p := range a.s.Parts {
		if p.HasBox {
			b = b.Union(p.Box)
		}
	}
	return b
}

func (a *analyzer) l1() Metric {
	ok, tot := 0, 0
	var worst []Offender
	for _, m := range a.s.Markers {
		want := ""
		switch {
		case m.Kind == KindPower:
			want = "up"
		case m.Kind == KindGround:
			want = "down"
		case m.Kind == KindNetPort && m.PortIO == "IN":
			want = "left"
		case m.Kind == KindNetPort && m.PortIO == "OUT":
			want = "right"
		}
		if want == "" || m.Dir == "" {
			continue
		}
		tot++
		if m.Dir == want {
			ok++
		} else {
			worst = append(worst, Offender{Net: m.Net, At: ptr(m.Anchor), Value: 1, Note: fmt.Sprintf("%s points %s, flow wants %s", m.Kind, m.Dir, want)})
		}
	}
	cb := a.contentBox()
	conns := 0
	if cb.Valid() && len(a.s.Parts) >= 4 {
		for _, p := range a.s.Parts {
			if !p.HasBox || !isConnectorRef(p.Ref) {
				continue
			}
			conns++
			tot++
			x := (p.Box.C().X - cb.MinX) / math.Max(1, cb.W())
			if x <= 0.3 || x >= 0.7 {
				ok++
			} else {
				worst = append(worst, Offender{Ref: p.Ref, At: ptr(p.Box.C()), Value: round3(x), Note: "interface connector in the middle of the page (inputs left / outputs right)"})
			}
		}
	}
	if tot == 0 {
		return skip("no power/ground markers, directional ports or connectors to judge")
	}
	v := float64(ok) / float64(tot)
	return Metric{Value: v, Unit: "share", Score: 100 * v, Worst: worst,
		Extra:      map[string]float64{"items": float64(tot), "connectors": float64(conns)},
		Detail:     fmt.Sprintf("%d/%d follow the flow (power up, GND down, IN left, OUT right, connectors at the left/right 30%%)", ok, tot),
		Thresholds: "share × 100; connector edge band = outer 30% of content width",
		Source:     "IEC 61082-1:2014 信号流向左→右、上→下；电源上/地下 = orientation.json 真值表朝向；30% 边带为" + judgment}
}

func (a *analyzer) l2() Metric {
	ok, tot := 0, 0
	var worst []Offender
	for mi, m := range a.s.Markers {
		if m.Dir == "" {
			continue
		}
		if m.Kind == KindNetPort {
			tot++
			if m.Dir == "left" || m.Dir == "right" {
				ok++
			} else {
				worst = append(worst, Offender{Net: m.Net, At: ptr(m.Anchor), Value: 1, Note: "vertical net port: text reads sideways (folded label)"})
			}
		}
		// body must continue the stub direction (point away from the wire)
		is := a.t.markerIsl[mi]
		if is < 0 {
			continue
		}
		for _, si := range a.t.islands[is].segs {
			sg := a.t.segs[si]
			var from Pt
			switch {
			case near(sg.b, m.Anchor):
				from = sg.a
			case near(sg.a, m.Anchor):
				from = sg.b
			default:
				continue
			}
			stub := dirFromVec(Pt{m.Anchor.X - from.X, m.Anchor.Y - from.Y})
			if stub == "" {
				break
			}
			tot++
			if stub == m.Dir {
				ok++
			} else {
				worst = append(worst, Offender{Net: m.Net, At: ptr(m.Anchor), Value: 1, Note: fmt.Sprintf("%s body points %s but its wire arrives heading %s (reversed/folded)", m.Kind, m.Dir, stub)})
			}
			break
		}
	}
	if tot == 0 {
		return skip("no markers with a known direction")
	}
	v := float64(ok) / float64(tot)
	return Metric{Value: v, Unit: "share", Score: 100 * v, Worst: worst,
		Detail:     fmt.Sprintf("%d/%d marker checks consistent (ports horizontal, body continues its stub)", ok, tot),
		Thresholds: "share × 100",
		Source:     "orientation.json「旗体顺着导线朝外」；竖排端口 = layout-score folded-labels 判据（这里只做软计数）"}
}

func dirFromVec(v Pt) string {
	switch {
	case math.Abs(v.X) < eps && v.Y > eps:
		return "up"
	case math.Abs(v.X) < eps && v.Y < -eps:
		return "down"
	case math.Abs(v.Y) < eps && v.X > eps:
		return "right"
	case math.Abs(v.Y) < eps && v.X < -eps:
		return "left"
	}
	return ""
}

func (a *analyzer) boxedParts() []Part {
	var out []Part
	for _, p := range a.s.Parts {
		if p.HasBox {
			out = append(out, p)
		}
	}
	return out
}

// sizeClass separates small 2/3-pin parts (passives, LEDs, transistors) from
// ICs/connectors: alignment is judged within a class only, because a capacitor
// deliberately offset to meet an IC pin is pin alignment, not a near-miss.
func sizeClass(p Part) string {
	if len(p.Pins) > 0 && len(p.Pins) <= 3 || len(p.Pins) == 0 && p.Box.Area() < 2500 {
		return "small"
	}
	return "large"
}

func (a *analyzer) l3() Metric {
	ps := a.boxedParts()
	if len(ps) < 3 {
		return skip("fewer than 3 parts with a bbox")
	}
	const reach = 300.0 // only parts within this orthogonal distance are visual neighbours
	sum, aligned, judged := 0.0, 0, 0
	var worst []Offender
	for i, p := range ps {
		c := p.Box.C()
		ax, ay, nx, ny, peers := false, false, false, false, 0
		for j, q := range ps {
			if i == j || sizeClass(q) != sizeClass(p) {
				continue
			}
			d := q.Box.C()
			if math.Abs(d.Y-c.Y) > reach && math.Abs(d.X-c.X) > reach {
				continue
			}
			peers++
			if math.Abs(d.Y-c.Y) <= reach {
				if dx := math.Abs(d.X - c.X); dx <= a.p.AlignTol {
					ax = true
				} else if dx <= a.p.NearMissTol {
					nx = true
				}
			}
			if math.Abs(d.X-c.X) <= reach {
				if dy := math.Abs(d.Y - c.Y); dy <= a.p.AlignTol {
					ay = true
				} else if dy <= a.p.NearMissTol {
					ny = true
				}
			}
		}
		if peers == 0 {
			continue // no same-class neighbour: nothing to align with
		}
		judged++
		sc := 0.0
		if ax || ay {
			sc = 1
			aligned++
		}
		if (nx && !ax) || (ny && !ay) {
			sc -= 0.25
			worst = append(worst, Offender{Ref: p.Ref, At: ptr(c), Value: 1, Note: "near-miss: almost aligned with a same-class neighbour"})
		} else if !(ax || ay) {
			worst = append(worst, Offender{Ref: p.Ref, At: ptr(c), Value: 1, Note: "on no row or column with a same-class neighbour"})
		}
		sum += math.Max(0, sc)
	}
	if judged == 0 {
		return skip("no part has a same-class neighbour within 300 units")
	}
	v := float64(aligned) / float64(judged)
	return Metric{Value: v, Unit: "aligned share", Score: 100 * sum / float64(judged), Worst: worst,
		Extra:      map[string]float64{"judged": float64(judged)},
		Detail:     fmt.Sprintf("%d/%d parts share a row/column centre with a same-class neighbour (tol %.1f, near-miss ≤%.0f units, within %.0f)", aligned, judged, a.p.AlignTol, a.p.NearMissTol, reach),
		Thresholds: "per part: aligned 1, near-miss −0.25; classes: ≤3-pin small vs IC/connector",
		Source:     "PCB P1 行列共线的原理图对应（docs/reviews/2026-09-routing-aesthetics §4）；容差按风格档；" + judgment}
}

func (a *analyzer) l4() Metric {
	ps := a.boxedParts()
	type line struct {
		key   float64
		items []Part
	}
	group := func(horizontal bool) []float64 {
		var lines []*line
		for _, p := range ps {
			c := p.Box.C()
			k := c.Y
			if !horizontal {
				k = c.X
			}
			var hit *line
			for _, l := range lines {
				if math.Abs(l.key-k) <= math.Max(a.p.AlignTol, 0.5) {
					hit = l
					break
				}
			}
			if hit == nil {
				hit = &line{key: k}
				lines = append(lines, hit)
			}
			hit.items = append(hit.items, p)
		}
		var cvs []float64
		for _, l := range lines {
			if len(l.items) < 3 {
				continue
			}
			sort.Slice(l.items, func(i, j int) bool {
				if horizontal {
					return l.items[i].Box.MinX < l.items[j].Box.MinX
				}
				return l.items[i].Box.MinY < l.items[j].Box.MinY
			})
			var gaps []float64
			flush := func() {
				if len(gaps) >= 2 {
					m, sd := meanStd(gaps)
					if m > 0 {
						cvs = append(cvs, sd/m)
					}
				}
				gaps = nil
			}
			for k := 1; k < len(l.items); k++ {
				g := l.items[k].Box.MinX - l.items[k-1].Box.MaxX
				if !horizontal {
					g = l.items[k].Box.MinY - l.items[k-1].Box.MaxY
				}
				if g > 300 || g < 0 { // unrelated or overlapping (layout-lint's job)
					flush()
					continue
				}
				gaps = append(gaps, g)
			}
			flush()
		}
		return cvs
	}
	cvs := append(group(true), group(false)...)
	if len(cvs) == 0 {
		return skip("no row or column with ≥3 adjacent parts")
	}
	m, _ := meanStd(cvs)
	return Metric{Value: m, Unit: "mean gap CV", Score: ramp(m, 0.15, 0.8),
		Extra:      map[string]float64{"lines": float64(len(cvs))},
		Detail:     fmt.Sprintf("%d rows/columns of ≥3 parts, mean gap CV %.2f", len(cvs), m),
		Thresholds: "CV ≤0.15 → 100, ≥0.8 → 0",
		Source:     "PCB P2 阵列等距的原理图对应；" + judgment}
}

func (a *analyzer) l5() Metric {
	if !a.s.HasFrames || len(a.s.Frames) == 0 {
		return skip("no module frames in the snapshot (add rectangles / frames, or a lib-layout module frame)")
	}
	var scores []float64
	var worst []Offender
	outside := 0
	for _, p := range a.boxedParts() {
		in := false
		for _, f := range a.s.Frames {
			if f.Box.Contains(p.Box.C()) {
				in = true
				break
			}
		}
		if !in {
			outside++
			worst = append(worst, Offender{Ref: p.Ref, At: ptr(p.Box.C()), Value: 1, Note: "outside every module frame"})
		}
	}
	for i, f := range a.s.Frames {
		var content Box
		for _, p := range a.boxedParts() {
			if f.Box.Contains(p.Box.C()) {
				content = content.Union(p.Box)
			}
		}
		for _, m := range a.s.Markers {
			if f.Box.Contains(m.Anchor) {
				content = content.Union(m.Box)
			}
		}
		for j := i + 1; j < len(a.s.Frames); j++ {
			if overlapArea(f.Box, a.s.Frames[j].Box) > 1 {
				worst = append(worst, Offender{Ref: f.Title, At: ptr(f.Box.C()), Value: 1, Note: "frames overlap (sch check partition-overlap)"})
				scores = append(scores, 0)
			}
		}
		if !content.Valid() {
			continue
		}
		margins := []float64{content.MinX - f.Box.MinX, f.Box.MaxX - content.MaxX, content.MinY - f.Box.MinY, f.Box.MaxY - content.MaxY}
		mm, sd := meanStd(margins)
		cv := 0.0
		if mm > 0 {
			cv = sd / mm
		}
		sc := ramp(cv, 0.25, 1.2)
		minM := math.Min(math.Min(margins[0], margins[1]), math.Min(margins[2], margins[3]))
		if minM < 5 {
			sc *= 0.5
			worst = append(worst, Offender{Ref: f.Title, At: ptr(f.Box.C()), Value: round1(minM), Note: "content within 5 units of the frame edge"})
		}
		scores = append(scores, sc)
	}
	if len(scores) == 0 {
		return skip("frames enclose no parts")
	}
	m, _ := meanStd(scores)
	share := 1 - float64(outside)/math.Max(1, float64(len(a.boxedParts())))
	return Metric{Value: share, Unit: "parts framed share", Score: m * share, Worst: worst,
		Detail:     fmt.Sprintf("%d frames, mean margin score %.0f, %d parts outside frames", len(a.s.Frames), m, outside),
		Thresholds: "margin CV ≤0.25 → 100, ≥1.2 → 0; min margin <5 halves; × framed share",
		Source:     "schematic.md 验证与交付：器件本体与位号须完整落在所属框内；均匀留白为" + judgment}
}

func (a *analyzer) l6() Metric {
	type item struct {
		b     Box
		label string
		owner string
	}
	var items []item
	for _, m := range a.s.Markers {
		items = append(items, item{m.Box, m.Kind + " " + m.Net, ""})
	}
	for _, t := range a.s.Texts {
		items = append(items, item{t.Box, t.Kind + " " + t.Content, t.Owner})
	}
	if len(items) == 0 {
		return skip("no markers or texts")
	}
	n := 0
	var worst []Offender
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if ov := overlapArea(items[i].b, items[j].b); ov > 1 {
				n++
				worst = append(worst, Offender{At: ptr(items[i].b.C()), Value: round1(ov), Note: items[i].label + " overlaps " + items[j].label})
			}
		}
		for _, p := range a.boxedParts() {
			if p.Ref == items[i].owner {
				continue
			}
			if ov := overlapArea(items[i].b, p.Box); ov > 1 {
				n++
				worst = append(worst, Offender{Ref: p.Ref, At: ptr(items[i].b.C()), Value: round1(ov), Note: items[i].label + " overlaps body " + p.Ref})
			}
		}
	}
	sortWorst(worst)
	v := float64(n) / float64(len(items))
	est := 0
	for _, m := range a.s.Markers {
		if m.Estimated {
			est++
		}
	}
	return Metric{Value: v, Unit: "overlaps/label", Score: ramp(v, 0, 0.1), Worst: worst,
		Extra:      map[string]float64{"overlaps": float64(n), "estimatedMarkerBoxes": float64(est)},
		Detail:     fmt.Sprintf("%d overlaps among %d markers/texts and bodies (%d marker boxes predicted)", n, len(items), est),
		Thresholds: "0 → 100, ≥0.1 overlaps per label → 0",
		Source:     "marker-overlap / text-overlap 是 sch check finding；预测框尺寸 = connect_pin/sch check 同一把尺（schematic-wiring.md）"}
}

func (a *analyzer) l7() Metric {
	ps := a.boxedParts()
	if len(ps) < 4 {
		return skip("fewer than 4 parts with a bbox")
	}
	cb := a.contentBox()
	const n = 4
	cw, ch := cb.W()/n, cb.H()/n
	var occ []float64
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			cell := Box{cb.MinX + float64(i)*cw, cb.MinY + float64(j)*ch, cb.MinX + float64(i+1)*cw, cb.MinY + float64(j+1)*ch}
			s := 0.0
			for _, p := range ps {
				s += overlapArea(cell, p.Box)
			}
			occ = append(occ, s/math.Max(1, cell.Area()))
		}
	}
	m, sd := meanStd(occ)
	cv := 0.0
	if m > 0 {
		cv = sd / m
	}
	sc := ramp(cv, 1.0, 2.5)
	detail := fmt.Sprintf("4×4 occupancy CV %.2f", cv)
	extra := map[string]float64{"occupancyCV": round3(cv)}
	if a.s.Sheet != nil && a.s.Sheet.Valid() {
		sh := *a.s.Sheet
		off := (math.Abs(cb.C().X-sh.C().X)/sh.W() + math.Abs(cb.C().Y-sh.C().Y)/sh.H()) / 2
		sc = 0.5*sc + 0.5*ramp(off, 0.1, 0.4)
		extra["centreOffset"] = round3(off)
		detail += fmt.Sprintf(", content centre offset %.2f of the sheet", off)
	}
	return Metric{Value: cv, Unit: "occupancy CV", Score: sc, Extra: extra, Detail: detail,
		Thresholds: "CV ≤1.0 → 100, ≥2.5 → 0; with a sheet: ½ × centring (offset ≤0.1 → 100, ≥0.4 → 0)",
		Source:     "PCB P7 留白均匀的原理图对应；" + judgment}
}

// ---------------------------------------------------------------------------
// labels / buses

func (a *analyzer) n1() Metric {
	cross := map[int]int{}
	for _, c := range a.cr {
		cross[c.a]++
		cross[c.b]++
	}
	n, multi := 0, 0
	var worst []Offender
	for _, is := range a.t.islands {
		if len(is.terms) < 2 {
			continue
		}
		multi++
		if is.length > a.p.LongWire || cross[is.id] > a.p.LongCrossings {
			n++
			worst = append(worst, Offender{Net: is.net(), At: ptr(is.terms[0].at), Value: round1(is.length),
				Note: fmt.Sprintf("%.0f units, %d crossings: use local net labels / ports", is.length, cross[is.id])})
		}
	}
	if multi == 0 {
		return skip("no multi-terminal wire trees")
	}
	sortWorst(worst)
	v := float64(n) / float64(multi)
	return Metric{Value: v, Unit: "share of trees", Score: ramp(v, 0, 0.15), Worst: worst,
		Extra:      map[string]float64{"candidates": float64(n)},
		Detail:     fmt.Sprintf("%d/%d wire trees longer than %.0f units or crossing more than %d wires", n, multi, a.p.LongWire, a.p.LongCrossings),
		Thresholds: fmt.Sprintf("long > %.0f units (≈ half an A4 drawing width 1170) or > %d crossings; share 0 → 100, ≥0.15 → 0", a.p.LongWire, a.p.LongCrossings),
		Source:     "schematic.md：GND/VCC 就近重复符号、跨模块信号用 netport；A4 图纸宽 1170 units（lib-layout sheet）；" + judgment}
}

func (a *analyzer) n2() Metric {
	if !a.s.HasMarkers {
		return skip("source carries no markers")
	}
	byNet := map[string][]int{}
	for i, m := range a.s.Markers {
		if (m.Kind == KindNetPort || m.Kind == KindNetLabel) && !IsPowerNet(m.Net) && !IsGroundNet(m.Net) {
			byNet[m.Net] = append(byNet[m.Net], i)
		}
	}
	if len(byNet) == 0 {
		return skip("no signal net labels / ports on the page")
	}
	frameOf := func(p Pt) int {
		for i, f := range a.s.Frames {
			if f.Box.Contains(p) {
				return i
			}
		}
		return -1
	}
	n, considered := 0, 0
	var worst []Offender
	for _, net := range sortedKeys(byNet) {
		idx := byNet[net]
		if len(idx) < 2 {
			continue
		}
		considered++
		var b Box
		frames := map[int]bool{}
		for _, i := range idx {
			at := a.s.Markers[i].Anchor
			b = b.Union(Box{at.X, at.Y, at.X + 1e-6, at.Y + 1e-6})
			frames[frameOf(at)] = true
			if is := a.t.markerIsl[i]; is >= 0 {
				for _, tm := range a.t.islands[is].terms {
					b = b.Union(Box{tm.at.X, tm.at.Y, tm.at.X + 1e-6, tm.at.Y + 1e-6})
				}
			}
		}
		span := b.W() + b.H()
		if len(frames) > 1 {
			continue // cross-module port: labels are the intended drawing
		}
		if span < a.p.ShortLabel {
			n++
			worst = append(worst, Offender{Net: net, At: ptr(b.C()), Value: round1(span), Note: fmt.Sprintf("%d labels for a net spanning %.0f units: a short wire reads better (if no ownership rule asks for labels)", len(idx), span)})
		}
	}
	if considered == 0 {
		return skip("no signal net uses ≥2 labels on this page")
	}
	v := float64(n) / float64(considered)
	return Metric{Value: v, Unit: "share of labelled nets", Score: ramp(v, 0, 0.25), Worst: worst,
		Detail:     fmt.Sprintf("%d/%d labelled signal nets span < %.0f units within one frame", n, considered, a.p.ShortLabel),
		Thresholds: fmt.Sprintf("span < %.0f units (≈ schScoreRowMinGap 117: two facing labels need that much room); share 0 → 100, ≥0.25 → 0", a.p.ShortLabel),
		Source:     "schematic.md：同模块相邻外围优先真实正交线、不拆成满页同名标签；117 = layout-score 实测同排标签最小间距"}
}

func (a *analyzer) n3() Metric {
	cands := a.candidates()
	if !a.s.HasMarkers && !a.s.HasBuses {
		for _, c := range cands {
			a.lanes = append(a.lanes, LaneRow{Candidate: c, Note: "source carries no markers: lane not measurable"})
		}
		return skip(fmt.Sprintf("source carries no markers or buses (%d bus candidate(s) listed, lanes not measurable)", len(cands)))
	}
	if len(cands) == 0 {
		return skip("no bus candidates (indexed nets, SPI/I2C/UART/SDIO groups, MIPI/USB pairs) on this page")
	}
	var scored []float64
	var worst []Offender
	for _, c := range cands {
		row := LaneRow{Candidate: c}
		mem := map[string]bool{}
		for _, n := range c.Members {
			mem[n] = true
		}
		for _, b := range a.s.Buses {
			if c.Key != "" && strings.HasPrefix(strings.ToUpper(b.Name), strings.ToUpper(strings.TrimRight(c.Key, "_"))) {
				row.NativeBus = b.Name
			}
		}
		var ms []Marker
		for _, m := range a.s.Markers {
			if mem[m.Net] && (m.Kind == KindNetPort || m.Kind == KindNetLabel) {
				ms = append(ms, m)
			}
		}
		row.Markers = len(ms)
		switch {
		case row.NativeBus != "":
			row.Score, row.Scored, row.Aligned, row.SameDir = 100, true, 1, 1
			row.Note = "native bus " + row.NativeBus
		case len(ms) < 2:
			row.Note = "members wired directly or off-page: no lane to judge"
		default:
			row.Scored = true
			// largest aligned set: same x (column) or same y (row)
			bestAxis, best := "", 0
			for _, axis := range []string{"x", "y"} {
				cnt := map[int64]int{}
				for _, m := range ms {
					k := m.Anchor.X
					if axis == "y" {
						k = m.Anchor.Y
					}
					cnt[int64(math.Round(k/math.Max(a.p.AlignTol, 0.5)))]++
				}
				for _, v := range cnt {
					if v > best {
						best, bestAxis = v, axis
					}
				}
			}
			row.Aligned = float64(best) / float64(len(ms))
			var pos []float64
			for _, m := range ms {
				if bestAxis == "x" {
					pos = append(pos, m.Anchor.Y)
				} else {
					pos = append(pos, m.Anchor.X)
				}
			}
			sort.Float64s(pos)
			var gaps []float64
			for k := 1; k < len(pos); k++ {
				gaps = append(gaps, pos[k]-pos[k-1])
			}
			if gm, gsd := meanStd(gaps); gm > 0 {
				row.PitchCV = round3(gsd / gm)
			}
			dirs := map[string]int{}
			mx := 0
			for _, m := range ms {
				dirs[m.Dir]++
				if dirs[m.Dir] > mx {
					mx = dirs[m.Dir]
				}
			}
			row.SameDir = float64(mx) / float64(len(ms))
			row.Score = round1(100 * row.Aligned * (1 - math.Min(1, row.PitchCV)) * row.SameDir)
			row.Note = fmt.Sprintf("virtual lane: %d labels, aligned %.0f%%, pitch CV %.2f, same direction %.0f%%", len(ms), 100*row.Aligned, row.PitchCV, 100*row.SameDir)
			if row.Score < 80 {
				worst = append(worst, Offender{Net: c.Suggested, Value: row.Score, Note: row.Note})
			}
		}
		row.Aligned, row.SameDir = round3(row.Aligned), round3(row.SameDir)
		if row.Scored {
			scored = append(scored, row.Score)
		}
		a.lanes = append(a.lanes, row)
	}
	if len(scored) == 0 {
		return skip(fmt.Sprintf("%d bus candidate(s) but none drawn with ≥2 labels/ports or a native bus on this page", len(cands)))
	}
	m, _ := meanStd(scored)
	return Metric{Value: float64(len(scored)), Unit: "lanes scored", Score: m, Worst: worst,
		Extra:      map[string]float64{"candidates": float64(len(cands))},
		Detail:     fmt.Sprintf("%d of %d bus candidates drawn as a lane, mean lane score %.0f", len(scored), len(cands), m),
		Thresholds: "lane = 100 × aligned share × (1 − min(1, pitch CV)) × same-direction share; native bus = 100",
		Source:     "虚拟总线：同组索引名标签同列等距同向（本评审 §3 N3）；" + judgment}
}

func sortWorst(w []Offender) {
	sort.SliceStable(w, func(i, j int) bool { return w[i].Value > w[j].Value })
}
