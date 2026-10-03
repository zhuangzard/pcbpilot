package designreport

import (
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// AestheticsSection is §6B "布局布线美观度" (report.json key "aesthetics"):
// the report-only placement + routing aesthetics of pcb aesthetics / the
// pcb auto joint. It is an ADDITIONAL section: it never changes the verdict
// or any other section, and its constraint tier is the lowest (7 of 7).
type AestheticsSection struct {
	Source      string      `json:"source"` // reload-board | board | plan
	Score       float64     `json:"score"`
	Placement   float64     `json:"placement"`
	Routing     float64     `json:"routing"`
	RoutedShare float64     `json:"routedShare"`
	Weight      float64     `json:"weight"`
	Tier        string      `json:"tier"`
	Profile     string      `json:"profile"`
	ProfileNote string      `json:"profileNote"`
	Boundary    string      `json:"boundary"`
	Metrics     []AesRow    `json:"metrics"`
	Symmetry    []AesSymRow `json:"symmetry,omitempty"`
	Exemptions  []KV        `json:"exemptions,omitempty"`
	Priority    []KV        `json:"priority"`
	// Trades are the electrical sub-scores the aesthetics stages of the
	// pcb auto run lowered within their tolerance (placement routed guard,
	// post-route beautify): one row per stage that ran, "无" when it traded
	// nothing — never silent.
	Trades []KV `json:"electricalTrades,omitempty"`
}

// AesRow is one metric.
type AesRow struct {
	ID, Name, Value, Score, Detail string
}

// AesSymRow is one detected isomorphic group.
type AesSymRow struct {
	Kind, Signature, Instances, Axis, Error, Score string
}

func (c *ctx) buildAesthetics() {
	var rep *pcbauto.AestheticsReport
	src := ""
	// Score the board with the profile the pcb auto run used (plan.json),
	// default balanced; the style never changes any other section.
	var prof *pcbauto.AesProfile
	if pl := c.in.Plan; pl != nil && pl.Joint != nil && pl.Joint.Aesthetics != nil {
		p := pl.Joint.Aesthetics.Profile
		if p.Name != "" {
			prof = &p
		}
	}
	for _, cand := range []struct {
		b   *Board
		src string
	}{{c.in.ReloadBoard, "reload-board"}, {c.in.Board, "board"}} {
		if cand.b == nil || len(cand.b.raw) == 0 {
			continue
		}
		in, err := pcbauto.AesInputFromSnapshot(cand.b.raw)
		if err != nil {
			continue
		}
		in.Profile = prof
		rep, src = pcbauto.Aesthetics(in), cand.src
		break
	}
	if rep == nil {
		if pl := c.in.Plan; pl != nil && pl.Joint != nil && pl.Joint.Aesthetics != nil {
			rep, src = pl.Joint.Aesthetics, "plan"
		}
	}
	if rep == nil {
		// Deliberately NOT recorded in Missing: an additional, report-only
		// section must not change the verdict of an existing report.
		return
	}
	s := &AestheticsSection{Source: src, Score: rep.Score, Placement: rep.Placement, Routing: rep.Routing, RoutedShare: rep.RoutedShare,
		Weight: rep.Weight, Tier: sprintf("%d / %d（最低）", rep.Tier, len(pcbauto.ConstraintPriority)),
		Profile: rep.Profile.Name,
		ProfileNote: sprintf("计划权重 %s，对齐容差 %s mil，目标格 %s mil，检出对称必须对称 %v，可用松弛 +%s%% 线长 / +%s%% 面积 / +%d 过孔（Phase B/C 生成器才会使用）",
			f2(rep.Profile.Weight), f1(rep.Profile.AlignTolMil), f1(rep.Profile.PlacementGridMil), rep.Profile.SymmetryRequired,
			f1(rep.Profile.Slack.WirelengthPct), f1(rep.Profile.Slack.AreaPct), rep.Profile.Slack.ExtraVias),
		Boundary: "只报告：权重 " + f2(rep.Weight) + "，不进综合分、不影响结论与其它章节；安全、电气、制造、布通、效率、布局约束全部优先，差分/RF/等长/电流过孔/隔离带与开槽/高压间距在度量前豁免"}
	if a := rep.Profile.Auto; a != nil {
		s.ProfileNote += "；auto 选档：" + a.Reason
	}
	for _, m := range rep.Metrics {
		r := AesRow{ID: m.ID, Name: m.Name, Detail: m.Detail}
		if m.Skipped {
			r.Value, r.Score, r.Detail = "—", "—", "未测："+m.Reason
		} else {
			r.Value, r.Score = f3(m.Value), f1(m.Score)
		}
		s.Metrics = append(s.Metrics, r)
	}
	for _, g := range rep.Symmetry {
		var inst []string
		for _, x := range g.Instances {
			inst = append(inst, strings.Join(x, "+"))
		}
		s.Symmetry = append(s.Symmetry, AesSymRow{Kind: g.Kind, Signature: g.Signature, Instances: strings.Join(inst, " | "),
			Axis: g.Axis.Type, Error: f3(g.Error), Score: f1(g.Score)})
	}
	for _, e := range rep.Exemptions {
		s.Exemptions = append(s.Exemptions, KV{e.Kind + "（" + e.Metrics + "）", sprintf("%d 项", len(e.Items)), e.Why})
	}
	if pl := c.in.Plan; pl != nil {
		zero := "安全（隔离/爬电/间隙、高压板边带、过孔电流、可交付结论）、布通、平面开路、DRC、SI/差分/隔离发现数、过孔数（不增）、超预算的原始压降：零容差"
		row := func(stage string, tol float64, ts []pcbauto.AesTrade) {
			v := "无"
			if len(ts) > 0 {
				v = pcbauto.AesTradesText(ts)
			}
			s.Trades = append(s.Trades, KV{sprintf("%s（每项容差 %s 分）", stage, f2(tol)), v, zero})
		}
		if pl.Placement != nil && pl.Placement.Aesthetics != nil && pl.Placement.Aesthetics.Guard != "" {
			a := pl.Placement.Aesthetics
			row("布局美观阶段", a.ElectricalTol, a.Trades)
		}
		if pl.Result != nil && pl.Result.Route != nil && pl.Result.Route.Beautify != nil && pl.Result.Route.Beautify.Kept {
			bs := pl.Result.Route.Beautify
			row("布线美化", bs.ElectricalTol, bs.Trades)
		}
	}
	for _, t := range pcbauto.ConstraintPriority {
		s.Priority = append(s.Priority, KV{sprintf("%d %s", t.Rank, t.Name), t.Kind, t.Covers})
	}
	c.rep.Aesthetics = s
}
