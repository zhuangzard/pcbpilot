package designreport

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/postsim"
)

// §6A 设计后仿真验证 Post-layout verification: the real copper of the finished
// board (post.json of `pcbpilot sim post-layout`): IR drop per load pad, via
// currents, current density, thermal maps and part temperatures, copper
// feedback, the optional Elmer cross-check, and pass / warn / fail.

// PostSection is §6A.
type PostSection struct {
	Status      string        `json:"status"` // PASS | WARN | FAIL
	Source      string        `json:"source,omitempty"`
	Board       string        `json:"board,omitempty"`
	Scenarios   string        `json:"scenarios"`
	Boundary    string        `json:"boundary"`
	Settings    []KV          `json:"settings"`
	Nets        []PostNetRow  `json:"nets"`
	Vias        []PostViaRow  `json:"vias"`
	Thermal     []KV          `json:"thermal"`
	Layers      []PostLayer   `json:"layers"`
	Parts       []PostPartRow `json:"parts"`
	Feedback    []PostFbRow   `json:"feedback"`
	Compare     []PostCmpRow  `json:"compare,omitempty"`
	Elmer       []KV          `json:"elmer,omitempty"`
	Maps        []Image       `json:"maps"`
	Reasons     []string      `json:"reasons,omitempty"`
	Assumptions []string      `json:"assumptions,omitempty"`
	Data        string        `json:"data,omitempty"` // package path of post.json
}

// PostNetRow is one net's DC result on the real copper.
type PostNetRow struct {
	Net       string  `json:"net"`
	Role      string  `json:"role"`
	Scenario  string  `json:"scenario"`
	Reference string  `json:"reference"`
	CurrentA  float64 `json:"currentA"`
	BudgetMV  float64 `json:"budgetMV,omitempty"`
	WorstMV   float64 `json:"worstMV"`
	WorstPad  string  `json:"worstPad"`
	LossMW    float64 `json:"lossMW"`
	MaxJ      float64 `json:"maxJAmm2"`
	MaxViaA   float64 `json:"maxViaA"`
	RiseC     float64 `json:"copperRiseC"`
	Status    string  `json:"status"`
}

// PostViaRow is one via's worst current.
type PostViaRow struct {
	Net       string  `json:"net"`
	At        string  `json:"at"`
	CurrentA  float64 `json:"currentA"`
	AmpacityA float64 `json:"ampacityA"`
	UsePct    float64 `json:"usePct"`
	Scenario  string  `json:"scenario"`
}

// PostLayer is one layer's temperature.
type PostLayer struct {
	Layer string  `json:"layer"`
	MaxC  float64 `json:"maxC"`
	MeanC float64 `json:"meanC"`
	At    string  `json:"at"`
}

// PostPartRow is one part's board temperature / junction estimate.
type PostPartRow struct {
	Ref      string  `json:"ref"`
	Side     string  `json:"side"`
	PowerW   float64 `json:"powerW"`
	Scenario string  `json:"scenario"`
	BoardC   float64 `json:"boardMaxC"`
	Theta    string  `json:"theta,omitempty"`
	TjC      float64 `json:"tjC,omitempty"`
	TjMaxC   float64 `json:"tjMaxC,omitempty"`
	Status   string  `json:"status"`
}

// PostFbRow is one copper feedback item.
type PostFbRow struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Where    string `json:"where"`
	Summary  string `json:"summary"`
}

// PostCmpRow compares with pcb auto's routing-time IR estimate.
type PostCmpRow struct {
	Net     string  `json:"net"`
	AutoMV  float64 `json:"autoMV"`
	PostMV  float64 `json:"postMV"`
	DeltaMV float64 `json:"deltaMV"`
	Pad     string  `json:"pad"`
}

// ParsePost reads a post.json.
func ParsePost(raw []byte) (*postsim.Result, error) {
	var r postsim.Result
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if r.Generator == "" || r.SchemaVersion == 0 {
		return nil, fmt.Errorf("not a pcbpilot sim post-layout document")
	}
	return &r, nil
}

func postStatus(s string) string {
	switch s {
	case "pass":
		return StatusPass
	case "warn":
		return StatusWarn
	case "fail":
		return StatusFail
	}
	return StatusNA
}

func (c *ctx) buildPost() {
	p := c.in.Post
	var maps []Image
	for _, img := range c.in.Images {
		if img.Kind == "heat" {
			maps = append(maps, img)
		}
	}
	if p == nil {
		c.missing("6A 设计后仿真", "未提供 post.json（pcbpilot sim post-layout --out post.json）")
		c.rep.Verification = append(c.rep.Verification, Check{Name: "设计后仿真（post-layout）", Status: StatusNA,
			Detail: "未提供（--post）：真实铜皮的压降、过孔电流与温升未验证"})
		return
	}
	s := &PostSection{Status: postStatus(p.Verdict.Status), Source: p.Inputs.Source, Board: p.Inputs.BoardSemantic,
		Scenarios: strings.Join(p.Scenarios, ", ") + "（" + p.Mode + "）", Maps: maps, Reasons: p.Verdict.Reasons,
		Assumptions: append(append([]string(nil), p.Model...), p.Assumptions...), Data: c.dataPath("post")}
	s.Boundary = sprintf("板级：裸板静止空气，上下表面自然对流 h 顶 %s / 底 %s W/m²K，环境 %s °C；不含外壳、风扇或气流（非 CFD），板边绝热",
		f1(p.Settings.HTop), f1(p.Settings.HBottom), f1(p.Settings.AmbientC))
	if p.Settings.Emissivity > 0 {
		s.Boundary += sprintf("；线性化辐射 ε %s", trimF4(p.Settings.Emissivity))
	}
	s.Settings = []KV{
		{"网格", sprintf("%s mm 单元 %d×%d，板内 %d 单元/层", trimF4(p.Grid.CellMm), p.Grid.NX, p.Grid.NY, p.Grid.Cells), ""},
		{"IR 预算", p.Settings.IRBudget, ""},
		{"铜温升限值", sprintf("%s °C，余量 ×%s", f1(p.Limits.TempRiseC), trimF4(p.Limits.Margin)), "线宽反馈"},
		{"过孔", sprintf("镀层 %s mil，载流 ΔT %s °C", trimF4(p.Settings.PlatingMil), f1(p.Settings.ViaDeltaTC)), "IPC-2221 外层曲线作用于孔壁截面"},
		{"FR-4", sprintf("面内 %s / 厚度方向 %s W/mK", trimF4(p.Settings.KFR4XY), trimF4(p.Settings.KFR4Z)), ""},
	}
	for l, n := range p.Settings.Planes {
		s.Settings = append(s.Settings, KV{"负片平面 " + l, n, "dump 不列出负片铜：按整层平面扣反焊盘建模"})
	}
	for _, n := range p.Nets {
		s.Nets = append(s.Nets, PostNetRow{Net: n.Net, Role: n.Role, Scenario: n.Scenario, Reference: n.Reference, CurrentA: n.CurrentA,
			BudgetMV: n.BudgetMV, WorstMV: n.WorstMV, WorstPad: n.WorstPad, LossMW: n.LossMW, MaxJ: n.MaxJAmm2, MaxViaA: n.MaxViaA,
			RiseC: n.MaxTraceRiseC, Status: n.Status})
	}
	for i, v := range p.Vias {
		if i >= 8 || v.CurrentA <= 0 {
			break
		}
		s.Vias = append(s.Vias, PostViaRow{Net: v.Net, At: sprintf("(%s, %s) mil", f1(v.X), f1(v.Y)), CurrentA: v.CurrentA,
			AmpacityA: v.AmpacityA, UsePct: v.UsePct, Scenario: v.Scenario})
	}
	if t := p.Thermal; t != nil {
		at := t.MaxAt.Layer + sprintf(" (%s, %s) mil", f1(t.MaxAt.X), f1(t.MaxAt.Y))
		if t.MaxAt.What != "" {
			at += "，" + t.MaxAt.What + " 下方"
		}
		s.Thermal = []KV{
			{"板最高温度", sprintf("%s °C", f1(t.MaxBoardC)), "场景 " + t.Scenario + "；" + at},
			{"热源", sprintf("%s（器件 %s + 铜损 %s）", fW(t.TotalW), fW(t.PartsW), fW(t.JouleW)), ""},
			{"能量平衡", sprintf("散出 %s，误差 %.4f %%", fW(t.LossW), math.Abs(t.BalanceErrPct)), "Σ 对流散热 = Σ 热源"},
		}
		if t.JouleScenario != "" {
			s.Thermal = append(s.Thermal, KV{"铜自热（仅铜损）", sprintf("%s °C", f2(t.MaxCopperRiseC)), "场景 " + t.JouleScenario})
		}
		for _, l := range t.Layers {
			s.Layers = append(s.Layers, PostLayer{Layer: l.Layer, MaxC: l.MaxC, MeanC: l.MeanC, At: sprintf("(%s, %s)", f1(l.X), f1(l.Y))})
		}
		for _, pt := range t.Parts {
			if pt.PowerW <= 0 {
				continue // unpowered parts only follow the board; the maps show them
			}
			row := PostPartRow{Ref: pt.Ref, Side: pt.Side, PowerW: pt.PowerW, Scenario: pt.Scenario, BoardC: pt.BoardMaxC, TjC: pt.TjC, TjMaxC: pt.TjMaxC, Status: pt.Status}
			if pt.ThetaCW > 0 {
				row.Theta = pt.ThetaKind + " " + trimF4(pt.ThetaCW) + " °C/W"
			}
			s.Parts = append(s.Parts, row)
		}
	}
	for _, it := range p.Feedback {
		var at []string
		for _, q := range it.Evidence.Points {
			at = append(at, sprintf("%s (%s, %s)", q.Label, f1(q.X), f1(q.Y)))
		}
		s.Feedback = append(s.Feedback, PostFbRow{ID: it.ID, Kind: it.Kind, Severity: it.Severity, Where: strings.Join(at, " "), Summary: it.Proposal.Summary})
	}
	for _, cr := range p.Compare {
		s.Compare = append(s.Compare, PostCmpRow{Net: cr.Net, AutoMV: cr.AutoMV, PostMV: cr.PostMV, DeltaMV: cr.DeltaMV, Pad: cr.PostPad})
	}
	if e := p.Elmer; e != nil {
		s.Elmer = []KV{{"状态", e.Status, e.Note}, {"输入包", sprintf("%d 节点 / %d 六面体 / %d 体", e.Nodes, e.Elements, e.Bodies), e.Deck}}
		if e.MaxDiffC > 0 {
			s.Elmer = append(s.Elmer, KV{"最大差", sprintf("%s °C（容差 %s °C）", f2(e.MaxDiffC), f2(e.TolC)), ""})
		}
	}
	c.rep.Post = s
	// Verification row: drives §0 verdict and the top risks.
	det := sprintf("post-layout %s", strings.ToUpper(p.Verdict.Status))
	worst := ""
	for _, n := range p.Nets {
		if n.Role == "power" && n.BudgetMV > 0 {
			worst += sprintf("；%s %s/%s mV", n.Net, f2(n.WorstMV), f1(n.BudgetMV))
		}
	}
	det += worst
	if t := p.Thermal; t != nil {
		det += sprintf("；板最高 %s °C", f1(t.MaxBoardC))
	}
	if len(p.Feedback) > 0 {
		det += sprintf("；%d 条铜皮修改建议", len(p.Feedback))
	}
	if len(p.Verdict.Reasons) > 0 {
		det += "：" + strings.Join(p.Verdict.Reasons, "；")
	}
	var path, sha string
	for _, r := range c.in.Refs {
		if r.Kind == "post" && r.Present {
			path, sha = r.Path, r.SHA256
		}
	}
	c.rep.Verification = append(c.rep.Verification, Check{Name: "设计后仿真（post-layout）", Status: s.Status, Detail: det, Evidence: path, SHA256: sha, Data: c.dataPath("post")})
}

// postKeyNumbers adds §1 key numbers.
func (c *ctx) postKeyNumbers(add func(l, v, n string)) {
	s := c.rep.Post
	if s == nil {
		return
	}
	var worst *PostNetRow
	for i := range s.Nets {
		n := &s.Nets[i]
		if n.BudgetMV > 0 && (worst == nil || n.WorstMV/n.BudgetMV > worst.WorstMV/worst.BudgetMV) {
			worst = n
		}
	}
	if worst != nil {
		add("设计后最坏压降（真实铜皮）", sprintf("%s mV / 预算 %s mV", f2(worst.WorstMV), f1(worst.BudgetMV)), worst.Net+" @ "+worst.WorstPad+"（"+worst.Scenario+"）")
	}
	if len(s.Thermal) > 0 {
		add("设计后板最高温度", s.Thermal[0].Value, s.Thermal[0].Note)
	}
}

// dataPath is the package path of an input kind ("" when not packaged).
func (c *ctx) dataPath(kind string) string {
	if c.in.Data == nil {
		return ""
	}
	return c.in.Data[kind]
}
