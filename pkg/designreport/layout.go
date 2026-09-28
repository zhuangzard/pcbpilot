package designreport

import (
	"math"
	"sort"
	"strings"
)

// MaxSegments is how many IR segments the HTML/Markdown tables show (all are
// counted; report.json keeps the same top list).
const MaxSegments = 20

func (c *ctx) buildLayout() {
	pl := c.in.Plan
	if pl == nil && len(c.in.Images) == 0 && c.in.Board == nil {
		c.missing("6 布局与布线", "无 plan.json、板级 dump 或图片")
		return
	}
	ls := &LayoutSection{Images: c.in.Images}
	if pl == nil {
		c.missing("6 布局与布线 — 布线统计/IR/SI", "pcb auto 输出目录（plan.json）未提供")
	}
	if pl != nil && pl.Result != nil {
		res := pl.Result
		if st := res.Stackup; st != nil {
			ls.StackupName = st.JLCStackup
			ls.StackNotes = st.Reasons
			for _, l := range st.Stack {
				ls.Stackup = append(ls.Stackup, StackRow{ID: l.ID, Name: l.Name, Kind: string(l.Kind), Nets: strings.Join(append(append([]string{}, l.Nets...), l.PourNets...), ", "), Dir: l.Dir})
			}
		}
		if rr := res.Route; rr != nil {
			s := rr.Stats
			ls.Routing = []KV{
				{"信号连接完成率", sprintf("%s %% (%d/%d)", f1(s.Completion), s.Routed, s.Connections), ""},
				{"未布通", sprintf("%d", s.Unrouted), ""},
				{"信号过孔 / 扇出过孔", sprintf("%d / %d", s.Vias, s.FanoutVias), ""},
				{"布线总长", sprintf("%s in", f2(s.WireLengthIn)), ""},
				{"迭代 / 剩余冲突", sprintf("%d / %d", s.Iterations, s.Conflicts), ""},
			}
			if res.DRC != nil {
				ls.Routing = append(ls.Routing, KV{"引擎内 DRC", sprintf("%d 违规 / %d 项检查", len(res.DRC.Violations), res.DRC.Checked), "离线独立 DRC；以原生 DRC 为准（§7）"})
			}
			if pw := rr.Power; pw != nil {
				ls.IRBudget = sprintf("%d %% 或 %g mV 取大（场景 %s）", int(pw.Budget.Pct), pw.Budget.MV, pw.Scenario)
				ls.IRModel = pw.Model
				var segs []SegRow
				for _, n := range pw.Nets {
					row := IRNetRow{Net: n.Net, Role: string(n.Role), VoltageV: n.VoltageV, CurrentA: n.CurrentA, Reference: n.Reference, BudgetMV: round(n.BudgetMV, 2),
						WorstMV: round(n.WorstMV, 3), WorstPad: n.WorstPad, Status: n.Status}
					if n.BudgetMV > 0 {
						row.RatioPct = round(n.WorstMV/n.BudgetMV*100, 1)
					}
					ls.IRNets = append(ls.IRNets, row)
					if n.BudgetMV > 0 {
						for _, p := range n.Pads {
							if p.Dir == "sink" && p.CurrentA > 0 {
								ls.IRPads = append(ls.IRPads, IRPadRow{Net: n.Net, Pad: p.Pad, CurrentA: p.CurrentA, DropMV: round(p.DropMV, 3), BudgetMV: round(n.BudgetMV, 2), RatioPct: round(p.DropMV/n.BudgetMV*100, 2)})
							}
						}
					}
					if len(n.WorstPath) > 0 {
						pr := PathRow{Net: n.Net, Pad: n.WorstPad}
						for _, st := range n.WorstPath {
							pr.Steps = append(pr.Steps, PathStep{Kind: st.Kind, What: st.What, DropMV: round(st.DropMV, 3)})
							pr.TotalMV += st.DropMV
						}
						pr.TotalMV = round(pr.TotalMV, 3)
						ls.WorstPaths = append(ls.WorstPaths, pr)
					}
					for _, sg := range n.Segments {
						segs = append(segs, SegRow{Net: n.Net, Layer: sg.Layer, Kind: sg.Kind, LengthMil: round(sg.LengthMil, 1), CurrentA: round(sg.CurrentA, 4),
							RoutedMil: sg.RoutedMil, WidthMil: sg.WidthMil, NeedMil: round(sg.NeedMil, 2), DropMV: round(sg.DropMV, 4)})
					}
				}
				ls.SegmentsAll = len(segs)
				sort.SliceStable(segs, func(i, j int) bool {
					if segs[i].DropMV != segs[j].DropMV {
						return segs[i].DropMV > segs[j].DropMV
					}
					return segs[i].Net < segs[j].Net
				})
				if len(segs) > MaxSegments {
					segs = segs[:MaxSegments]
				}
				ls.Segments = segs
				sort.SliceStable(ls.IRPads, func(i, j int) bool { return ls.IRPads[i].RatioPct > ls.IRPads[j].RatioPct })
			} else {
				c.missing("6.4 IR 压降", "plan.json 无 route.power（pcb auto run 未带 --sim/--intent 仿真电流）")
			}
		}
		if iso := res.Isolation; iso != nil {
			ls.Isolation = append(ls.Isolation, sprintf("标准 %s：%d 对绝缘要求，%d 个铣槽，%d 个禁铺区，%d 条间距/爬电问题", iso.Standard, len(iso.Pairs), len(iso.Slots), len(iso.Moats), len(iso.Findings)))
			for _, f := range iso.Findings {
				ls.Isolation = append(ls.Isolation, sprintf("%s %s: %s", f.Severity, f.Kind, f.Message))
			}
			for _, s := range iso.Slots {
				ls.Isolation = append(ls.Isolation, sprintf("铣槽 %s (%s↔%s) 宽 %s mil × 长 %s mil", s.Ref, s.A, s.B, f1(s.WidthMil), f1(s.LengthMil)))
			}
		}
	}
	if pl != nil {
		if si := pl.SI; si != nil {
			for _, n := range si.Nets {
				var ly []string
				for _, l := range n.Layers {
					ly = append(ly, sprintf("%d", l))
				}
				ls.SINets = append(ls.SINets, SINetRow{Net: n.Net, Class: n.Class, LengthMil: n.LengthMil, Vias: n.Vias, Layers: strings.Join(ly, ",")})
			}
			for _, p := range si.Pairs {
				st := StatusPass
				if p.LimitMil > 0 && p.SkewMil > p.LimitMil {
					st = StatusFail
				}
				ls.SIPairs = append(ls.SIPairs, SIPairRow{P: p.P, N: p.N, SkewMil: p.SkewMil, LimitMil: p.LimitMil, Status: st})
			}
			for _, f := range si.Findings {
				ls.SIFindings = append(ls.SIFindings, sprintf("%s %s: %g (限值 %g) — %s", f.Net, f.Kind, f.Value, f.Limit, f.Fix))
			}
		}
		if j := pl.Joint; j != nil {
			ls.Joint = append(ls.Joint, KV{"联合评分 Joint", f1(j.Overall), sprintf("可交付 %v；平面连接 %d/%d", j.Deliverable, j.PlanePads-j.PlaneOpen, j.PlanePads)})
			for _, it := range j.Items {
				ls.Joint = append(ls.Joint, KV{it.Group + "/" + it.ID, f1(it.Score), it.Detail})
			}
		}
		fb := c.in.Feedback
		if fb == nil {
			fb = pl.Feedback
		}
		if fb != nil {
			d := fb.Difficulty
			ls.Difficulty = []KV{
				{"布线困难", map[bool]string{true: "是", false: "否"}[d.Hard], ""},
				{"飞线交叉 / 飞线总长", sprintf("%d / %s mil", d.RatsCrossings, f1(d.RatsLengthMil)), ""},
				{"IR 超预算网络", sprintf("%d", d.IROver), ""},
				{"缩颈段", sprintf("%d", d.NeckDowns), ""},
			}
			for _, it := range fb.Items {
				ls.Feedback = append(ls.Feedback, sprintf("[%s/%s] %s — %s（状态 %s）", it.Severity, it.Kind, it.Title, it.Proposal.Summary, it.Status))
			}
			ls.Feedback = append(ls.Feedback, fb.Notes...)
		}
	}
	c.rep.Layout = ls
}

// buildVerification is §7: each check PASS / WARN / FAIL / N/A with evidence.
func (c *ctx) buildVerification() {
	ev := func(kind string) (string, string) {
		for _, r := range c.in.Refs {
			if r.Kind == kind && r.Present {
				return r.Path, r.SHA256
			}
		}
		return "", ""
	}
	var out []Check
	add := func(name, kind, st, detail string) {
		p, h := ev(kind)
		out = append(out, Check{Name: name, Status: st, Detail: detail, Evidence: p, SHA256: h})
	}
	if d := c.in.DRC; d != nil {
		st, det := StatusPass, "原生 DRC 通过，0 违规"
		if !d.Passed {
			st, det = StatusFail, sprintf("原生 DRC 未通过：%d 违规（%s）", d.Violations, strings.Join(d.Kinds, "; "))
		}
		if d.CreatedAt != "" {
			det += "；" + d.CreatedAt
		}
		add("原生 DRC（EasyEDA）", "drc", st, det)
	} else {
		add("原生 DRC（EasyEDA）", "drc", StatusNA, "未提供 pcb drc 结果（--drc）")
	}
	if k := c.in.Check; k != nil {
		st := StatusPass
		switch {
		case k.Errors > 0:
			st = StatusFail
		case k.Warns > 0:
			st = StatusWarn
		}
		add("pcb check（DFM 重建审计）", "check", st, sprintf("ERROR %d / WARN %d / INFO %d", k.Errors, k.Warns, k.Infos))
	} else {
		add("pcb check（DFM 重建审计）", "check", StatusNA, "未提供 pcb check 输出（--check）")
	}
	// Board-edge safety distance (copper-to-edge): measured on the save/
	// reload dump (else the board dump, else the plan).
	if eg := c.edge(); eg.chk != nil {
		st := StatusPass
		det := eg.requirement() + "；实测 " + eg.measured()
		if n := eg.errors(); n > 0 {
			st = StatusFail
			det = sprintf("%d 处低于板边安全距离；", n) + det
		}
		kind := eg.src
		if kind == "plan" {
			det += "（计划几何，未经宿主回读）"
		}
		add("板边安全距离（copper-to-edge）", kind, st, det)
	} else {
		add("板边安全距离（copper-to-edge）", "board", StatusNA, "未提供板级回读或 plan.json；要求 "+eg.requirement())
	}
	if r := c.in.RulesCheck; r != nil {
		st, det := StatusPass, "intent 规则与 EasyEDA 一致（"+r.Status+"）"
		if r.Status != "in-sync" || len(r.Plan.Conflicts) > 0 || r.Plan.PendingWrites > 0 {
			st, det = StatusFail, sprintf("规则未同步：status %s，冲突 %d，待写 %d", r.Status, len(r.Plan.Conflicts), r.Plan.PendingWrites)
		}
		add("规则同步（pcb rules check）", "rules-check", st, det)
	} else {
		add("规则同步（pcb rules check）", "rules-check", StatusNA, "未提供（--rules-check）")
	}
	if n := c.in.NetDiff; n != nil {
		st, det := StatusPass, "原理图网表与 PCB 焊盘网络一致"
		if !n.Passed {
			st, det = StatusFail, "焊盘网络差异："+n.Summary
		}
		add("焊盘网络对账（pad-net diff）", "net-diff", st, det)
	} else {
		add("焊盘网络对账（pad-net diff）", "net-diff", StatusNA, "未提供（--net-diff）")
	}
	switch b, r := c.in.Board, c.in.ReloadBoard; {
	case b != nil && r != nil:
		st, det := StatusPass, "保存重载前后 semanticSha256 一致："+short(b.SemanticSHA256)
		if b.SemanticSHA256 == "" || b.SemanticSHA256 != r.SemanticSHA256 {
			st, det = StatusFail, sprintf("semanticSha256 不一致：%s → %s", short(b.SemanticSHA256), short(r.SemanticSHA256))
		}
		add("保存/重载一致性", "reload-board", st, det)
	case b != nil:
		add("保存/重载一致性", "board", StatusNA, sprintf("只有一份板级回读（%s，semantic %s）；需 --reload-board 才能比对", b.CapturedAt, short(b.SemanticSHA256)))
	default:
		add("保存/重载一致性", "board", StatusNA, "未提供板级回读（--board / --reload-board）")
	}
	if pl := c.in.Plan; pl != nil && pl.Result != nil && pl.Result.Route != nil {
		s := pl.Result.Route.Stats
		st := StatusPass
		if s.Unrouted > 0 || s.Completion < 100 {
			st = StatusFail
		}
		det := sprintf("信号 %s %%（%d/%d）", f1(s.Completion), s.Routed, s.Connections)
		if j := pl.Joint; j != nil && j.PlanePads > 0 {
			det += sprintf("，平面连接 %d/%d", j.PlanePads-j.PlaneOpen, j.PlanePads)
			if j.PlaneOpen > 0 {
				st = StatusFail
			}
		}
		add("布线完成度（pcb auto）", "plan", st, det)
		if pw := pl.Result.Route.Power; pw != nil {
			over := pw.Violations()
			st := StatusPass
			if over > 0 {
				st = StatusFail
			}
			add("直流压降（IR drop）", "plan", st, sprintf("%d 个网络超预算，最坏 %s %% 预算", over, f1(pw.WorstRatio()*100)))
		}
	}
	c.rep.Verification = out
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// boardXYmm converts board mil coordinates to mm relative to the outline's
// lower-left corner.
func (c *ctx) boardXYmm(x, y float64) (float64, float64) {
	b := c.in.Board
	if b == nil {
		return 0, 0
	}
	minX, minY := math.Inf(1), math.Inf(1)
	for _, p := range b.Outline.Points {
		if len(p) >= 2 {
			minX, minY = math.Min(minX, p[0]), math.Min(minY, p[1])
		}
	}
	if math.IsInf(minX, 1) {
		minX, minY = b.Outline.BBox.MinX, b.Outline.BBox.MinY
	}
	return round((x-minX)*0.0254, 1), round((y-minY)*0.0254, 1)
}
