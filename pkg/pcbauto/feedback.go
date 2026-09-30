package pcbauto

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Closed loop PCB → schematic.
//
// When routing is hard the cure is often upstream: a GPIO on the far side of
// the MCU, a header pin order that crosses every line, a decoupling cap two
// ICs share, a rail whose regulator sits at the wrong end. BuildFeedback
// turns routing evidence into concrete schematic proposals, each with the
// measured evidence, the exact change and the gain estimated by re-running
// the relevant offline check (the router itself for pin swaps). Nothing is
// written: pin swaps become a guarded `sch pin-swap` playbook, the rest are
// design proposals for the designer.

// Feedback kinds.
const (
	FBPinSwap       = "mcu-pin-swap"
	FBConnectorSwap = "connector-pin-swap"
	FBDecap         = "decap-ownership"
	FBIRDrop        = "rail-ir-drop"
	FBViaCurrent    = "via-current"
	FBPackage       = "package-change"
)

// Feedback is feedback.json.
type Feedback struct {
	SchemaVersion int              `json:"schemaVersion"`
	Source        string           `json:"source"`
	Difficulty    FeedbackHardness `json:"difficulty"`
	Items         []*FeedbackItem  `json:"items"`
	// Rejected are candidates the offline re-route did not confirm.
	Rejected []*FeedbackItem `json:"rejected,omitempty"`
	Loop     *FeedbackLoop   `json:"loop,omitempty"`
	Notes    []string        `json:"notes,omitempty"`
	// Rules the agent must follow before any item reaches the schematic.
	Rules []string `json:"rules"`
}

// FeedbackHardness summarises why (or whether) routing was hard.
type FeedbackHardness struct {
	Hard          bool     `json:"hard"`
	Completion    float64  `json:"completion"`
	Unrouted      int      `json:"unrouted"`
	UnroutedNets  []string `json:"unroutedNets,omitempty"`
	RatsCrossings int      `json:"ratsCrossings"`
	RatsLengthMil float64  `json:"ratsLengthMil"`
	WireLengthIn  float64  `json:"wireLengthIn"`
	Vias          int      `json:"vias"`
	HSViaFindings int      `json:"hsViaFindings"`
	IROver        int      `json:"irOverBudget"`
	NeckDowns     int      `json:"neckDowns"`
	Joint         float64  `json:"joint"`
	Reasons       []string `json:"reasons,omitempty"`
}

// FeedbackItem is one proposal.
type FeedbackItem struct {
	ID           string           `json:"id"`
	Kind         string           `json:"kind"`
	Severity     string           `json:"severity"` // high | medium | low
	Title        string           `json:"title"`
	Evidence     FeedbackEvidence `json:"evidence"`
	Proposal     FeedbackProposal `json:"proposal"`
	ExpectedGain *FeedbackGain    `json:"expectedGain,omitempty"`
	Confidence   float64          `json:"confidence"`
	// Applyable: `sch pin-swap --item` can compile it into a schematic
	// playbook. Additions (a cap, a regulator) and package changes are
	// design proposals only.
	Applyable bool     `json:"applyable"`
	Caveats   []string `json:"caveats,omitempty"`
	// Status is always live-unverified: nothing here has touched EasyEDA.
	Status string `json:"status"`
}

// FeedbackEvidence is what was measured.
type FeedbackEvidence struct {
	Metrics map[string]float64 `json:"metrics,omitempty"`
	Nets    []string           `json:"nets,omitempty"`
	Refs    []string           `json:"refs,omitempty"`
	Points  []FeedbackPoint    `json:"points,omitempty"`
	Detail  []string           `json:"detail,omitempty"`
}

// FeedbackPoint is a labelled board coordinate (mil, y-up).
type FeedbackPoint struct {
	Label string  `json:"label"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
}

// FeedbackProposal is the exact schematic change.
type FeedbackProposal struct {
	Summary string    `json:"summary"`
	Swaps   []PinSwap `json:"swaps,omitempty"`
	Actions []string  `json:"actions,omitempty"`
}

// FeedbackGain is the estimated effect, with how it was estimated.
type FeedbackGain struct {
	// Method: reroute (the pipeline re-run on a board copy with the change),
	// ratsnest (MST length / crossings), model (a stated linear model).
	Method  string             `json:"method"`
	Before  map[string]float64 `json:"before,omitempty"`
	After   map[string]float64 `json:"after,omitempty"`
	Summary string             `json:"summary"`
}

// FeedbackLoop records `--feedback-loop N`.
type FeedbackLoop struct {
	Passes   []FeedbackPass     `json:"passes"`
	Accepted []PinSwap          `json:"accepted"`
	Before   map[string]float64 `json:"before"`
	After    map[string]float64 `json:"after"`
}

// FeedbackPass is one loop pass.
type FeedbackPass struct {
	Pass     int                `json:"pass"`
	Tried    string             `json:"tried"`
	Kept     bool               `json:"kept"`
	Metrics  map[string]float64 `json:"metrics"`
	Decision string             `json:"decision"`
}

// FeedbackOptions configure BuildFeedback.
type FeedbackOptions struct {
	Caps    *PinCapTable
	Circuit *Circuit
	// Route are the pipeline options of the base run; re-routes reuse them
	// with the base stackup forced.
	Route Options
	// Verify caps how many pin-swap candidates are re-routed (0 = ratsnest
	// estimate only).
	Verify int
	// Loop > 0 runs the accept-if-better loop for that many passes.
	Loop int
	// Source labels the producer ("pcb auto run", "pcb feedback").
	Source string
	Log    func(format string, args ...any)
}

func (o *FeedbackOptions) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log(format, args...)
	}
}

// routeMetrics are the verified numbers of one routed board.
func routeMetrics(b *Board, an *Analysis, res *Result, js *JointScore) map[string]float64 {
	m := map[string]float64{}
	if res == nil || res.Route == nil {
		return m
	}
	s := res.Route.Stats
	m["completion"] = s.Completion
	m["unrouted"] = float64(len(res.Route.Unrouted))
	m["vias"] = float64(s.Vias)
	m["fanoutVias"] = float64(s.FanoutVias)
	m["wireLengthIn"] = round2(s.WireLengthIn)
	if res.DRC != nil {
		m["drc"] = float64(len(res.DRC.Violations))
	}
	if js != nil {
		m["joint"] = round2(js.Overall)
	}
	r := Ratsnest(b, an)
	m["ratsCrossings"] = float64(r.Crossings)
	m["ratsLengthMil"] = math.Round(r.LengthMil)
	return m
}

// BuildFeedback derives schematic proposals from a routed board. b must be
// the board as routed (placement applied); res its pipeline result.
func BuildFeedback(ctx context.Context, b *Board, res *Result, js *JointScore, opt FeedbackOptions) (*Feedback, error) {
	if opt.Caps == nil {
		opt.Caps = DefaultPinCaps()
	}
	fb := &Feedback{SchemaVersion: 1, Source: opt.Source, Items: []*FeedbackItem{}, Rules: []string{
		"本文件只是离线建议：没有任何一条写入过 EasyEDA（status 一律 live-unverified）。",
		"引脚交换改的是原理图：执行 `pcbpilot sch pin-swap --plan feedback.json --item <id>` 生成的 playbook 前，先把 diff 给用户确认，与布局变更同一确认规则。",
		"执行后必须：sch gate / 连通性回读 → 重新导入 PCB（原理图→PCB 更新）→ pcb pad-net-diff 对账 → 重新 pcb auto run；未完成前不得称已改善。",
		"加件（去耦、稳压器）、封装更换属于设计变更，只给方案与数值，由用户决定后走原理图模块流程。",
	}}
	if res == nil || res.Route == nil || res.Analysis == nil {
		fb.Notes = append(fb.Notes, "没有布线结果：只能给出与布线无关的建议")
		return fb, nil
	}
	an := res.Analysis
	base := routeMetrics(b, an, res, js)
	fb.Difficulty = hardness(b, an, res, js)

	// a/b. pin swaps (MCU GPIO matrix, generic headers)
	cands := SearchPinSwaps(b, an, opt.Caps)
	verify := opt.Verify
	if res.Route.Stats.Millis > 60000 && verify > 0 {
		fb.Notes = append(fb.Notes, fmt.Sprintf("基线布线耗时 %.0fs：引脚交换只做飞线估算，未逐条重布验证（用 `pcb feedback --verify N` 单独验证）", float64(res.Route.Stats.Millis)/1000))
		verify = 0
	}
	unroutedNet := map[string]bool{}
	for _, u := range res.Route.Unrouted {
		unroutedNet[u.Net] = true
	}
	for i, c := range cands {
		it := swapItem(c, unroutedNet)
		if i < verify {
			after, err := rerouteSwaps(ctx, b, res, c.swaps, opt)
			if err != nil {
				return nil, err
			}
			it.ExpectedGain = &FeedbackGain{Method: "reroute", Before: base, After: after.metrics}
			if ok, why := swapAccepted(base, after.metrics, false); ok {
				it.ExpectedGain.Summary = gainSummary(base, after.metrics)
				it.Confidence = 0.8
				if it.Severity == "low" && (after.metrics["vias"] < base["vias"] || after.metrics["completion"] > base["completion"]) {
					it.Severity = "medium"
				}
			} else {
				it.ExpectedGain.Summary = "离线重布未确认收益：" + why
				it.Confidence, it.Applyable = 0.2, false
				fb.Rejected = append(fb.Rejected, it)
				opt.logf("feedback: %s rejected by re-route (%s)", c.key(), why)
				continue
			}
			for _, s := range c.swaps {
				if s.Caution != "" {
					it.Confidence -= 0.15
				}
			}
			it.Confidence = round2(math.Max(it.Confidence, 0.3))
		}
		fb.Items = append(fb.Items, it)
	}

	// c. decoupling ownership
	fb.Items = append(fb.Items, decapFeedback(b, an)...)
	// d. IR drop
	fb.Items = append(fb.Items, irFeedback(b, an, res)...)
	// d2. via arrays the router could not complete (viafix.go)
	fb.Items = append(fb.Items, viaShortFeedback(res)...)
	// e. package / pad pitch vs current
	pk := packageFeedback(b, an, res)
	fb.Difficulty.NeckDowns = len(pk)
	fb.Items = append(fb.Items, pk...)

	// Loop driver.
	if opt.Loop > 0 {
		lp, item, err := feedbackLoop(ctx, b, res, js, opt)
		if err != nil {
			return nil, err
		}
		fb.Loop = lp
		if item != nil {
			fb.Items = append([]*FeedbackItem{item}, fb.Items...)
		}
	}
	sev := map[string]int{"high": 0, "medium": 1, "low": 2}
	sort.SliceStable(fb.Items, func(i, j int) bool { return sev[fb.Items[i].Severity] < sev[fb.Items[j].Severity] })
	for i, it := range fb.Items {
		it.ID = fmt.Sprintf("FB%02d", i+1)
		it.Status = "live-unverified"
	}
	for i, it := range fb.Rejected {
		it.ID = fmt.Sprintf("RJ%02d", i+1)
		it.Status = "live-unverified"
	}
	if len(cands) == 0 {
		fb.Notes = append(fb.Notes, "引脚交换：在可重映射器件（pin-capabilities.json 收录的 MCU、通用排针）上没有找到能缩短飞线或减少交叉的排列")
	}
	return fb, nil
}

func hardness(b *Board, an *Analysis, res *Result, js *JointScore) FeedbackHardness {
	h := FeedbackHardness{Completion: res.Route.Stats.Completion, Unrouted: len(res.Route.Unrouted),
		WireLengthIn: round2(res.Route.Stats.WireLengthIn), Vias: res.Route.Stats.Vias}
	seen := map[string]bool{}
	for _, u := range res.Route.Unrouted {
		if !seen[u.Net] {
			seen[u.Net] = true
			h.UnroutedNets = append(h.UnroutedNets, u.Net)
		}
	}
	sort.Strings(h.UnroutedNets)
	r := Ratsnest(b, an)
	h.RatsCrossings, h.RatsLengthMil = r.Crossings, math.Round(r.LengthMil)
	if js != nil {
		h.Joint = round2(js.Overall)
	}
	si := CheckSI(b, an, res.Stackup, res.Route)
	pairBad := map[string]bool{}
	for _, f := range si.Findings {
		if f.Kind == "vias" {
			h.HSViaFindings++
		}
		if pairDefect(f.Kind) {
			pairBad[f.Net] = true
		}
	}
	if len(pairBad) > 0 {
		var ps []string
		for p := range pairBad {
			ps = append(ps, p)
		}
		sort.Strings(ps)
		h.Reasons = append(h.Reasons, fmt.Sprintf("%d 对差分对未按耦合/对称走线（%s；SI coupling/uncoupled/via-asymmetry）", len(ps), strings.Join(ps, ", ")))
	}
	h.IROver = res.Route.Power.Violations()
	if h.Unrouted > 0 {
		h.Reasons = append(h.Reasons, fmt.Sprintf("%d 条连接未布通（%s）", h.Unrouted, strings.Join(h.UnroutedNets, ", ")))
	}
	if conns := res.Route.Stats.Connections; conns > 0 && float64(h.RatsCrossings) > 0.5*float64(conns) {
		h.Reasons = append(h.Reasons, fmt.Sprintf("飞线交叉 %d 处（信号连接 %d 条）", h.RatsCrossings, conns))
	}
	if h.HSViaFindings > 0 {
		h.Reasons = append(h.Reasons, fmt.Sprintf("高速网络过孔超限 %d 处", h.HSViaFindings))
	}
	if n := len(res.Route.ViaShortfalls); n > 0 {
		h.Reasons = append(h.Reasons, fmt.Sprintf("%d 条载流网络的换层过孔阵列不足（via-current）", n))
	}
	if h.IROver > 0 {
		h.Reasons = append(h.Reasons, fmt.Sprintf("%d 条电源轨 IR 压降超预算", h.IROver))
	}
	if r.LengthMil > 0 && res.Route.Stats.WireLengthIn*1000 > 1.6*r.LengthMil {
		h.Reasons = append(h.Reasons, fmt.Sprintf("布线总长 %.1f in 是信号飞线 %.1f in 的 %.1f 倍（含电源走线）", res.Route.Stats.WireLengthIn, r.LengthMil/1000, res.Route.Stats.WireLengthIn*1000/r.LengthMil))
	}
	h.Hard = len(h.Reasons) > 0
	return h
}

func swapItem(c *swapCandidate, unrouted map[string]bool) *FeedbackItem {
	nets := make([]string, 0, len(c.swaps))
	var moves []string
	sev := "low"
	for _, s := range c.swaps {
		nets = append(nets, s.Net)
		from, to := s.FromPin, s.ToPin
		if s.FromName != "" {
			from += "(" + s.FromName + ")"
		}
		if s.ToName != "" {
			to += "(" + s.ToName + ")"
		}
		moves = append(moves, fmt.Sprintf("%s: %s.%s → %s.%s", s.Net, s.Ref, from, s.Ref, to))
		if unrouted[s.Net] {
			sev = "high"
		}
	}
	if sev == "low" && c.after.Crossings < c.before.Crossings {
		sev = "medium"
	}
	it := &FeedbackItem{Kind: c.kind, Severity: sev,
		Title: fmt.Sprintf("%s 引脚重分配：%d 个网络换脚，飞线 %.0f→%.0f mil、交叉 %d→%d", c.ref, len(c.swaps), c.before.LengthMil, c.after.LengthMil, c.before.Crossings, c.after.Crossings),
		Evidence: FeedbackEvidence{Nets: nets, Refs: []string{c.ref}, Metrics: map[string]float64{
			"netRatsLengthMil": math.Round(c.before.LengthMil), "netRatsCrossings": float64(c.before.Crossings),
			"boardRatsLengthMil": math.Round(c.board.LengthMil), "boardRatsCrossings": float64(c.board.Crossings)}},
		Proposal:   FeedbackProposal{Summary: "在原理图中把下列网络改接到同一器件的等价引脚：" + strings.Join(moves, "；"), Swaps: c.swaps},
		Confidence: 0.4, Applyable: true,
		ExpectedGain: &FeedbackGain{Method: "ratsnest",
			Before:  map[string]float64{"ratsLengthMil": math.Round(c.board.LengthMil), "ratsCrossings": float64(c.board.Crossings)},
			After:   map[string]float64{"ratsLengthMil": math.Round(c.boardTo.LengthMil), "ratsCrossings": float64(c.boardTo.Crossings)},
			Summary: fmt.Sprintf("整板信号飞线 %.0f→%.0f mil，交叉 %d→%d（未重布验证）", c.board.LengthMil, c.boardTo.LengthMil, c.board.Crossings, c.boardTo.Crossings)},
	}
	if c.kind == FBConnectorSwap {
		it.Caveats = append(it.Caveats, "连接器针序是对外接口：线束/对插板/外壳丝印必须同步修改，需用户明确同意")
		it.Confidence = 0.3
	}
	if c.caps != nil && c.caps.Source != "" {
		it.Evidence.Detail = append(it.Evidence.Detail, "引脚能力来源："+c.caps.Source)
	}
	for _, s := range c.swaps {
		if s.Caution != "" {
			it.Caveats = append(it.Caveats, fmt.Sprintf("%s.%s(%s)：%s", s.Ref, s.ToPin, s.ToName, s.Caution))
		}
	}
	it.Caveats = append(it.Caveats, "固件的引脚定义（GPIO 号）必须同步修改")
	return it
}

type rerouted struct {
	board   *Board
	res     *Result
	js      *JointScore
	metrics map[string]float64
}

// rerouteSwaps re-runs the pipeline on a board copy with the swaps applied.
func rerouteSwaps(ctx context.Context, b *Board, res *Result, swaps []PinSwap, opt FeedbackOptions) (*rerouted, error) {
	nb := ApplyPinSwaps(b, swaps)
	ro := opt.Route
	ro.Stack.Force = res.Stackup.Layers
	r2, err := Run(ctx, nb, ro)
	if err != nil {
		return nil, err
	}
	js := Joint(nb, r2.Analysis, opt.Circuit, r2.Stackup, r2.Route, r2.DRC, JointOptions{PlacementScore: -1})
	return &rerouted{board: nb, res: r2, js: js, metrics: routeMetrics(nb, r2.Analysis, r2, js)}, nil
}

// swapAccepted: no worse on completion and DRC, and better on the joint
// score or clearly better on vias / wire length. strict (the loop) demands
// a joint-score gain.
func swapAccepted(before, after map[string]float64, strict bool) (bool, string) {
	if after["completion"] < before["completion"]-1e-6 {
		return false, fmt.Sprintf("布通率 %.1f%%→%.1f%%", before["completion"], after["completion"])
	}
	if after["drc"] > before["drc"] {
		return false, fmt.Sprintf("DRC %0.f→%0.f", before["drc"], after["drc"])
	}
	dj := after["joint"] - before["joint"]
	if dj < -0.05 {
		return false, fmt.Sprintf("联合评分 %.1f→%.1f", before["joint"], after["joint"])
	}
	if strict {
		if dj > 0.05 || after["completion"] > before["completion"] {
			return true, ""
		}
		return false, fmt.Sprintf("联合评分无提升（%.2f→%.2f）", before["joint"], after["joint"])
	}
	if dj > 0.05 || after["completion"] > before["completion"] || after["vias"] < before["vias"] ||
		after["wireLengthIn"] < 0.98*before["wireLengthIn"] {
		return true, ""
	}
	return false, fmt.Sprintf("重布后无可测收益（评分 %.2f→%.2f，过孔 %.0f→%.0f，线长 %.2f→%.2f in）",
		before["joint"], after["joint"], before["vias"], after["vias"], before["wireLengthIn"], after["wireLengthIn"])
}

func gainSummary(a, b map[string]float64) string {
	return fmt.Sprintf("离线重布：布通 %.1f%%→%.1f%%，过孔 %.0f→%.0f（扇出 %.0f→%.0f），线长 %.2f→%.2f in，飞线交叉 %.0f→%.0f，DRC %.0f→%.0f，联合评分 %.1f→%.1f",
		a["completion"], b["completion"], a["vias"], b["vias"], a["fanoutVias"], b["fanoutVias"], a["wireLengthIn"], b["wireLengthIn"],
		a["ratsCrossings"], b["ratsCrossings"], a["drc"], b["drc"], a["joint"], b["joint"])
}

// feedbackLoop applies the best swap candidate to an in-memory copy,
// re-routes, keeps it when the joint score improves, and repeats. The live
// board and the base plan are never touched: the accepted swaps are the
// recommendation.
func feedbackLoop(ctx context.Context, b *Board, res *Result, js *JointScore, opt FeedbackOptions) (*FeedbackLoop, *FeedbackItem, error) {
	cur, curRes, curJS := b, res, js
	base := routeMetrics(b, res.Analysis, res, js)
	curM := base
	lp := &FeedbackLoop{Before: base, After: base}
	tried := map[string]bool{}
	for pass := 1; pass <= opt.Loop; pass++ {
		if ctx.Err() != nil {
			break
		}
		cands := SearchPinSwaps(cur, curRes.Analysis, opt.Caps)
		kept := false
		n := 0
		for _, c := range cands {
			if tried[c.key()] {
				continue
			}
			tried[c.key()] = true
			if n++; n > 3 {
				break
			}
			rr, err := rerouteSwaps(ctx, cur, curRes, c.swaps, opt)
			if err != nil {
				return nil, nil, err
			}
			ok, why := swapAccepted(curM, rr.metrics, true)
			fp := FeedbackPass{Pass: pass, Tried: c.key(), Kept: ok, Metrics: rr.metrics}
			if ok {
				fp.Decision = gainSummary(curM, rr.metrics)
				cur, curRes, curJS, curM = rr.board, rr.res, rr.js, rr.metrics
				kept = true
			} else {
				fp.Decision = "未保留：" + why
			}
			opt.logf("feedback loop pass %d: %s → %s", pass, c.key(), fp.Decision)
			lp.Passes = append(lp.Passes, fp)
			if kept {
				break
			}
		}
		if !kept {
			break
		}
	}
	_ = curJS
	lp.After = curM
	// Compose: original pin → final pin of every net that moved.
	lp.Accepted = composeSwaps(b, cur)
	if len(lp.Accepted) == 0 {
		return lp, nil, nil
	}
	refs := map[string]bool{}
	var nets []string
	for _, s := range lp.Accepted {
		refs[s.Ref] = true
		nets = append(nets, s.Net)
	}
	var rl []string
	for r := range refs {
		rl = append(rl, r)
	}
	sort.Strings(rl)
	caps := opt.Caps
	for i := range lp.Accepted {
		s := &lp.Accepted[i]
		if pc := caps.For(b.Part(s.Ref).Device); pc != nil {
			if p := pc.Pin(s.FromPin); p != nil {
				s.FromName = p.Name
			}
			if p := pc.Pin(s.ToPin); p != nil {
				s.ToName, s.Caution = p.Name, p.Caution
			}
		}
	}
	kind := FBPinSwap
	if caps.For(b.Part(rl[0]).Device) == nil {
		kind = FBConnectorSwap
	}
	c := &swapCandidate{ref: strings.Join(rl, "+"), kind: kind, swaps: lp.Accepted,
		board: Ratsnest(b, res.Analysis), boardTo: Ratsnest(cur, curRes.Analysis)}
	c.before, c.after = c.board, c.boardTo
	it := swapItem(c, map[string]bool{})
	it.Title = fmt.Sprintf("闭环接受的引脚交换（%d 轮）：%s", len(lp.Passes), strings.Join(nets, ", "))
	it.ExpectedGain = &FeedbackGain{Method: "reroute", Before: base, After: curM, Summary: gainSummary(base, curM)}
	it.Confidence, it.Severity = 0.8, "medium"
	if base["completion"] < curM["completion"] {
		it.Severity = "high"
	}
	return lp, it, nil
}

// composeSwaps diffs pad nets between the original and final boards.
func composeSwaps(orig, final *Board) []PinSwap {
	var out []PinSwap
	for _, p := range orig.Parts {
		fp := final.Part(p.Ref)
		if fp == nil {
			continue
		}
		from := map[string]string{} // net → original pin
		to := map[string]string{}
		now := map[string]string{} // pin → final net
		was := map[string]string{}
		for _, pd := range p.Pads {
			was[pd.Number] = pd.Net
		}
		for _, pd := range fp.Pads {
			now[pd.Number] = pd.Net
		}
		for pin, n := range was {
			if n != "" && now[pin] != n {
				from[n] = pin
			}
		}
		for pin, n := range now {
			if n != "" && was[pin] != n {
				to[n] = pin
			}
		}
		for n, f := range from {
			t, ok := to[n]
			if !ok {
				continue
			}
			out = append(out, PinSwap{Ref: p.Ref, Net: n, FromPin: f, ToPin: t, ToWasFree: was[t] == "", FromBecomesFree: now[f] == ""})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ref != out[j].Ref {
			return out[i].Ref < out[j].Ref
		}
		return out[i].Net < out[j].Net
	})
	return out
}

// ---- c. decoupling ownership ------------------------------------------------

// decapFeedback finds ICs whose supply pins have no decoupling cap of their
// own nearby — either none at all or one they share with another IC.
func decapFeedback(b *Board, an *Analysis) []*FeedbackItem {
	const serveR = 300.0 // mil: a decap farther than this does not decouple the pin
	type decap struct {
		p   *Part
		pwr *Pad
		net string
	}
	var caps []decap
	for _, p := range b.Parts {
		if !strings.HasPrefix(upper(p.Ref), "C") || len(p.Pads) != 2 {
			continue
		}
		a, c := p.Pads[0], p.Pads[1]
		ra, rc := an.Plan(a.Net, b.Rules).Role, an.Plan(c.Net, b.Rules).Role
		switch {
		case ra == RolePower && rc == RoleGround:
			caps = append(caps, decap{p, a, a.Net})
		case rc == RolePower && ra == RoleGround:
			caps = append(caps, decap{p, c, c.Net})
		}
	}
	type icNet struct {
		ic  *Part
		net string
	}
	pins := map[icNet][]*Pad{}
	var order []icNet
	for _, p := range b.Parts {
		if !strings.HasPrefix(upper(p.Ref), "U") || len(p.Pads) < 8 {
			continue
		}
		for _, pd := range p.Pads {
			if pd.Net == "" || an.Plan(pd.Net, b.Rules).Role != RolePower {
				continue
			}
			k := icNet{p, pd.Net}
			if _, ok := pins[k]; !ok {
				order = append(order, k)
			}
			pins[k] = append(pins[k], pd)
		}
	}
	dist := func(k icNet, d decap) (float64, *Pad) {
		best, at := math.Inf(1), (*Pad)(nil)
		for _, pd := range pins[k] {
			if v := pd.Box.C.Dist(d.pwr.Box.C); v < best {
				best, at = v, pd
			}
		}
		return best, at
	}
	// owner of each decap: the nearest IC on its net
	owner := map[*Part]icNet{}
	for _, d := range caps {
		best := math.Inf(1)
		for _, k := range order {
			if k.net != d.net {
				continue
			}
			if v, _ := dist(k, d); v < best {
				best, owner[d.p] = v, k
			}
		}
	}
	var out []*FeedbackItem
	for _, k := range order {
		own := 0
		nearest, nearAt, nearCap := math.Inf(1), (*Pad)(nil), (*Part)(nil)
		for _, d := range caps {
			if d.net != k.net {
				continue
			}
			v, at := dist(k, d)
			if owner[d.p] == k && v <= serveR {
				own++
			}
			if v < nearest {
				nearest, nearAt, nearCap = v, at, d.p
			}
		}
		if own > 0 {
			continue
		}
		pinList := []string{}
		for _, pd := range pins[k] {
			pinList = append(pinList, pd.Number)
		}
		it := &FeedbackItem{Kind: FBDecap, Severity: "medium", Confidence: 0.5, Applyable: false,
			Evidence: FeedbackEvidence{Nets: []string{k.net}, Refs: []string{k.ic.Ref}, Metrics: map[string]float64{"supplyPins": float64(len(pins[k]))}}}
		pt := pins[k][0].Box.C
		it.Evidence.Points = append(it.Evidence.Points, FeedbackPoint{Label: k.ic.Ref + "." + pins[k][0].Number, X: math.Round(pt.X), Y: math.Round(pt.Y)})
		if nearCap == nil {
			it.Title = fmt.Sprintf("%s 的 %s 没有任何去耦电容", k.ic.Ref, k.net)
			it.Evidence.Detail = append(it.Evidence.Detail, fmt.Sprintf("%s 引脚 %s 接 %s，板上该网络没有 电源↔地 的两脚电容", k.ic.Ref, strings.Join(pinList, "/"), k.net))
		} else {
			o := owner[nearCap]
			it.Evidence.Refs = append(it.Evidence.Refs, nearCap.Ref)
			it.Evidence.Metrics["nearestDecapMil"] = math.Round(nearest)
			if o.ic != nil && o.ic != k.ic {
				it.Title = fmt.Sprintf("%s 的 %s 与 %s 共用去耦 %s（距 %.0f mil）", k.ic.Ref, k.net, o.ic.Ref, nearCap.Ref, nearest)
				it.Evidence.Detail = append(it.Evidence.Detail, fmt.Sprintf("%s 离 %s 更近，%s.%s 到它 %.0f mil（>%.0f mil 视为不去耦）", nearCap.Ref, o.ic.Ref, k.ic.Ref, nearAt.Number, nearest, serveR))
			} else {
				it.Title = fmt.Sprintf("%s 的 %s 最近去耦 %s 距 %.0f mil", k.ic.Ref, k.net, nearCap.Ref, nearest)
				it.Evidence.Detail = append(it.Evidence.Detail, fmt.Sprintf("%s.%s 到 %s %.0f mil（>%.0f mil 视为不去耦）", k.ic.Ref, nearAt.Number, nearCap.Ref, nearest, serveR))
			}
		}
		it.Proposal = FeedbackProposal{
			Summary: fmt.Sprintf("在原理图 %s 所在模块为 %s 引脚 %s 增加一颗 100nF 0402 去耦电容（%s↔GND），归属 %s", k.ic.Ref, k.net, strings.Join(pinList, "/"), k.net, k.ic.Ref),
			Actions: []string{"sch：在该 IC 模块内加 C（100nF/0402，LCSC 查 standard-parts.json），连接 " + k.net + " 与 GND", "groups：把新电容写进该 IC 的模块成员，布局时跟随核心", "PCB：导入后放在引脚旁 ≤ 60 mil，过孔就近接地"},
		}
		after := 60.0
		it.ExpectedGain = &FeedbackGain{Method: "model", Before: map[string]float64{"nearestDecapMil": math.Round(nearest)}, After: map[string]float64{"nearestDecapMil": after},
			Summary: fmt.Sprintf("去耦回路长度按 0402 就近放置估算 %s→%.0f mil（回路电感约与长度成正比）", fmtMil(nearest), after)}
		out = append(out, it)
	}
	return out
}

func fmtMil(v float64) string {
	if math.IsInf(v, 1) {
		return "∞"
	}
	return fmt.Sprintf("%.0f", v)
}

// ---- d. IR drop ---------------------------------------------------------------

func irFeedback(b *Board, an *Analysis, res *Result) []*FeedbackItem {
	pw := res.Route.Power
	if pw == nil {
		return nil
	}
	var out []*FeedbackItem
	for _, n := range pw.Nets {
		if n.Role != RolePower || (n.Status != "over-budget" && n.Status != "open") {
			continue
		}
		it := &FeedbackItem{Kind: FBIRDrop, Severity: "high", Confidence: 0.6, Applyable: false,
			Evidence: FeedbackEvidence{Nets: []string{n.Net}, Metrics: map[string]float64{
				"worstMV": round2(n.WorstMV), "budgetMV": round2(n.BudgetMV), "currentA": round2(n.CurrentA), "voltageV": n.VoltageV}}}
		ref := n.WorstRef
		if ref == "" {
			ref = n.Reference
		}
		if ref != "" {
			it.Evidence.Refs = append(it.Evidence.Refs, strings.Split(ref, "|")...)
		}
		if n.Status == "open" {
			it.Title = fmt.Sprintf("%s 有焊盘与供电端之间没有铜连接", n.Net)
			it.Proposal = FeedbackProposal{Summary: "先修连通：检查该轨在原理图中的供电器件与网络名（是否拆成了两个同名/异名网络），以及叠层是否给它分了平面"}
			out = append(out, it)
			continue
		}
		share := map[string]float64{}
		for _, s := range n.WorstPath {
			share[s.Kind] += s.DropMV
		}
		for k, v := range share {
			it.Evidence.Metrics["path_"+k+"MV"] = round2(v)
		}
		excess := n.WorstMV - n.BudgetMV
		f := n.WorstMV / n.BudgetMV
		it.Title = fmt.Sprintf("%s 压降 %.1f mV 超预算 %.1f mV（%.2f×）", n.Net, n.WorstMV, n.BudgetMV, f)
		var worstPt, refPt *Point
		if pd := padByKeyBoard(b, n.WorstPad); pd != nil {
			worstPt = &pd.Box.C
			it.Evidence.Points = append(it.Evidence.Points, FeedbackPoint{Label: "worst " + n.WorstPad, X: math.Round(pd.Box.C.X), Y: math.Round(pd.Box.C.Y)})
		}
		for _, p := range n.Pads {
			if p.Dir == "source" {
				if pd := padByKeyBoard(b, p.Pad); pd != nil {
					refPt = &pd.Box.C
					it.Evidence.Points = append(it.Evidence.Points, FeedbackPoint{Label: "source " + p.Pad, X: math.Round(pd.Box.C.X), Y: math.Round(pd.Box.C.Y)})
					break
				}
			}
		}
		var acts []string
		if t := share["track"]; t > excess {
			k := t / (t - excess)
			if onPlaneOrPour(res.Stackup, n.Net) {
				acts = append(acts, fmt.Sprintf("走线段贡献 %.1f mV（该轨已有平面/铺铜，主要是入平面前的走线）：这些段加宽到 %.1f×，或在负载焊盘旁就近打孔入平面（R ∝ L/w）", t, k))
			} else {
				acts = append(acts, fmt.Sprintf("走线段贡献 %.1f mV：把该轨改为内层平面/铺铜，或走线加宽到 %.1f× 即可满足预算（R ∝ 1/w）", t, k))
			}
		}
		if v := share["via"]; v > 0.3*n.WorstMV {
			acts = append(acts, fmt.Sprintf("过孔贡献 %.1f mV：换层处过孔数量需增加到 %.1f×", v, v/math.Max(v-excess, 1e-3)))
		}
		if worstPt != nil && refPt != nil {
			L := worstPt.Dist(*refPt)
			it.Evidence.Metrics["sourceToWorstMil"] = math.Round(L)
			acts = append(acts, fmt.Sprintf("原理图/布局：稳压器到最远负载直线 %.0f mil；把稳压器移到负载侧，使路径缩短到 ≤ %.0f mil（R ∝ L）", L, L/f))
		}
		var worstA float64
		for _, p := range n.Pads {
			if p.Pad == n.WorstPad {
				worstA = p.CurrentA
			}
		}
		if worstA > 0 && n.CurrentA > worstA {
			rem := n.CurrentA - worstA
			acts = append(acts, fmt.Sprintf("拆轨：%s（%.2f A）单独一路 LDO/磁珠支路，主干电流 %.2f→%.2f A，共用段压降按比例降到 %.0f%%", n.WorstPad, worstA, n.CurrentA, rem, 100*rem/n.CurrentA))
		}
		acts = append(acts, "局部大电容只改善瞬态，不降低直流压降")
		it.Proposal = FeedbackProposal{Summary: fmt.Sprintf("%s 需要把最坏路径电阻降到当前的 %.0f%%", n.Net, 100/f), Actions: acts}
		it.ExpectedGain = &FeedbackGain{Method: "model", Before: map[string]float64{"worstMV": round2(n.WorstMV)}, After: map[string]float64{"worstMV": round2(n.BudgetMV)},
			Summary: "线性电阻模型：压降 ∝ 路径电阻；每条措施给出满足预算所需的倍数，采纳后须 --sim 重跑确认"}
		out = append(out, it)
	}
	return out
}

func padByKeyBoard(b *Board, key string) *Pad {
	i := strings.LastIndex(key, ".")
	if i < 0 {
		return nil
	}
	p := b.Part(key[:i])
	if p == nil {
		return nil
	}
	for _, pd := range p.Pads {
		if pd.Number == key[i+1:] {
			return pd
		}
	}
	return nil
}

// ---- e. package / pad pitch --------------------------------------------------

// packageFeedback flags high-current pads narrower than the width their
// current needs, where the router had to neck the track down.
func packageFeedback(b *Board, an *Analysis, res *Result) []*FeedbackItem {
	type hit struct {
		pd         *Pad
		np         *NetPlan
		amps, need float64
		minW       float64
	}
	hits := map[*Pad]*hit{}
	for _, t := range res.Route.Tracks {
		np := an.Plan(t.Net, b.Rules)
		if np.CurrentA < 0.5 || (np.Role != RolePower && np.Role != RoleSwitch) {
			continue
		}
		for _, end := range []Point{t.A, t.B} {
			for _, p := range b.Parts {
				for _, pd := range p.Pads {
					if pd.Net != t.Net || pd.Box.Dist(end) > 1 {
						continue
					}
					amps, ok := padAmps(b, an, np, p, pd)
					if !ok || amps < 0.5 {
						continue
					}
					need := TraceWidthForCurrent(amps, an.TempRiseC, b.Rules.CopperOz, false)
					if t.Width >= 0.8*need || math.Min(pd.Box.W, pd.Box.H) >= need {
						continue
					}
					h := hits[pd]
					if h == nil {
						h = &hit{pd: pd, np: np, amps: amps, need: need, minW: t.Width}
						hits[pd] = h
					}
					h.minW = math.Min(h.minW, t.Width)
				}
			}
		}
	}
	byPart := map[string][]*hit{}
	for _, h := range hits {
		byPart[h.pd.Part] = append(byPart[h.pd.Part], h)
	}
	refs := make([]string, 0, len(byPart))
	for r := range byPart {
		refs = append(refs, r)
	}
	sort.Strings(refs)
	var out []*FeedbackItem
	for _, r := range refs {
		hs := byPart[r]
		sort.Slice(hs, func(i, j int) bool { return hs[i].pd.Number < hs[j].pd.Number })
		p := b.Part(r)
		it := &FeedbackItem{Kind: FBPackage, Severity: "low", Confidence: 0.3, Applyable: false,
			Evidence: FeedbackEvidence{Refs: []string{r}, Metrics: map[string]float64{}}}
		var det, nets []string
		worst := 0.0
		for _, h := range hs {
			pw := math.Min(h.pd.Box.W, h.pd.Box.H)
			det = append(det, fmt.Sprintf("%s.%s（%s，该脚 %.2f A）：焊盘宽 %.1f mil，电流需要 %.1f mil，入焊盘段颈缩到 %.1f mil", r, h.pd.Number, h.pd.Net, h.amps, pw, h.need, h.minW))
			nets = append(nets, h.pd.Net)
			worst = math.Max(worst, h.need/pw)
		}
		it.Evidence.Detail, it.Evidence.Nets = det, dedupStrings(nets)
		it.Evidence.Metrics["widthRatio"] = round2(worst)
		it.Title = fmt.Sprintf("%s（%s）大电流焊盘比所需线宽窄 %.1f×", r, p.Device, worst)
		it.Proposal = FeedbackProposal{Summary: fmt.Sprintf("可选：%s 换用引脚/焊盘更宽的封装（或同功能多引脚并联的型号），使焊盘宽不小于所需线宽（当前差 %.1f×）", r, worst),
			Actions: []string{"仅建议：短颈缩（< 焊盘长度）通常可接受；电流/温升紧张时再换封装", "换封装是 BOM 与原理图器件变更，需用户确认后走选型流程"}}
		it.ExpectedGain = &FeedbackGain{Method: "model", Summary: "颈缩段电流密度按宽度比降低；未量化温升"}
		out = append(out, it)
	}
	return out
}

// padAmps is the current through one pad: the simulated per-pad current
// when --sim supplied it, else the net current only for series power-path
// parts (inductor, diode, fuse, ferrite, connector) — a decap or a load
// pin does not carry the whole rail.
func padAmps(b *Board, an *Analysis, np *NetPlan, p *Part, pd *Pad) (float64, bool) {
	if np.hasPadCurrents() {
		if pc, ok := np.padCurrent(pd); ok {
			return pc.sizing(), true
		}
		for _, pc := range np.PadCurrents {
			if pc.Pad == pd.Key() {
				return pc.sizing(), true
			}
		}
		return 0, false
	}
	ref := upper(p.Ref)
	for _, pre := range []string{"L", "D", "F", "FB", "J", "CN", "P"} {
		if strings.HasPrefix(ref, pre) && len(ref) > len(pre) && ref[len(pre)] >= '0' && ref[len(pre)] <= '9' {
			return np.CurrentA, true
		}
	}
	return 0, false
}

func dedupStrings(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// feedbackTimeout bounds the extra routing a feedback pass may spend.
func feedbackTimeout(base time.Duration, verify, loop int) time.Duration {
	n := verify + 3*loop
	if n < 1 {
		n = 1
	}
	return base * time.Duration(n)
}

var fbKindCN = map[string]string{FBPinSwap: "MCU 引脚交换", FBConnectorSwap: "连接器针序", FBDecap: "去耦归属", FBIRDrop: "电源轨压降", FBPackage: "封装/焊盘"}

// WriteFeedback renders the "回推原理图的建议" section.
func WriteFeedback(p func(string, ...any), fb *Feedback) {
	if fb == nil {
		return
	}
	p("## 6b. 回推原理图的建议\n\n")
	h := fb.Difficulty
	if h.Hard {
		p("布线难点：%s。\n\n", strings.Join(h.Reasons, "；"))
	} else {
		p("布线无明显难点（布通 %.1f%%，飞线交叉 %d，联合评分 %.1f）；以下只列有数据支撑的改进。\n\n", h.Completion, h.RatsCrossings, h.Joint)
	}
	if len(fb.Items) == 0 {
		p("没有值得回推原理图的修改。\n\n")
	} else {
		p("| ID | 类型 | 级别 | 建议 | 预期收益 | 置信 | 可自动生成 |\n|---|---|---|---|---|---|---|\n")
		for _, it := range fb.Items {
			g := ""
			if it.ExpectedGain != nil {
				g = it.ExpectedGain.Summary
			}
			ap := "否"
			if it.Applyable {
				ap = "`sch pin-swap --item " + it.ID + "`"
			}
			p("| %s | %s | %s | %s | %s | %.2f | %s |\n", it.ID, fbKindCN[it.Kind], it.Severity, mdCell(it.Title+"："+it.Proposal.Summary), mdCell(g), it.Confidence, ap)
		}
		p("\n")
		for _, it := range fb.Items {
			if len(it.Evidence.Detail)+len(it.Proposal.Actions)+len(it.Caveats) == 0 {
				continue
			}
			p("**%s** %s\n\n", it.ID, it.Title)
			for _, d := range it.Evidence.Detail {
				p("- 证据：%s\n", d)
			}
			for _, a := range it.Proposal.Actions {
				p("- 方案：%s\n", a)
			}
			for _, c := range it.Caveats {
				p("- 注意：%s\n", c)
			}
			p("\n")
		}
	}
	if len(fb.Rejected) > 0 {
		p("离线重布未确认、已剔除的候选：\n\n")
		for _, it := range fb.Rejected {
			p("- %s：%s\n", it.Title, it.ExpectedGain.Summary)
		}
		p("\n")
	}
	if lp := fb.Loop; lp != nil {
		p("闭环（--feedback-loop）：%d 次尝试，接受 %d 个换脚。\n\n", len(lp.Passes), len(lp.Accepted))
		for _, ps := range lp.Passes {
			p("- 第 %d 轮 `%s`：%s\n", ps.Pass, ps.Tried, ps.Decision)
		}
		p("\n")
	}
	for _, n := range fb.Notes {
		p("- %s\n", n)
	}
	p("\n规则：\n\n")
	for _, r := range fb.Rules {
		p("- %s\n", r)
	}
	p("\n")
}

func mdCell(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ") }
