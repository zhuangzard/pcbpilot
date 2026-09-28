package designreport

import (
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// DefaultVrefAccuracyPct is used for a regulator without ratings.vrefAccuracyPct
// (flagged "assumed" in the report).
const DefaultVrefAccuracyPct = 2.0

// BenchLimitFactor sets the bench-supply current limit relative to the
// simulated typical input current.
const BenchLimitFactor = 1.5

var reStrapNet = regexp.MustCompile(`(?i)^(EN|CHIP_PU|CHIP_EN|N?RST|N?RESET|RESETN|BOOT\d?|IO0|GPIO0|NRST)$`)

type padLoc struct {
	ref, pad string
	x, y     float64
}

// padsOn lists the board pads of a net.
func (c *ctx) padsOn(net string) []padLoc {
	var out []padLoc
	if b := c.in.Board; b != nil {
		for _, bp := range b.Components {
			for _, p := range bp.Pads {
				if p.Net == net {
					out = append(out, padLoc{ref: bp.Designator, pad: p.PadNumber, x: p.X, y: p.Y})
				}
			}
		}
	}
	return out
}

func (c *ctx) padAt(ref, pad string) (padLoc, bool) {
	if b := c.in.Board; b != nil {
		if bp := b.Part(ref); bp != nil {
			for _, p := range bp.Pads {
				if p.PadNumber == pad {
					return padLoc{ref: ref, pad: pad, x: p.X, y: p.Y}, true
				}
			}
		}
	}
	return padLoc{}, false
}

func isTP(ref string) bool { return strings.HasPrefix(strings.ToUpper(ref), "TP") }

// probePad picks the accessible pad of net nearest to target: a test point
// first, else a two-terminal passive's pad.
func (c *ctx) probePad(net string, target padLoc, haveTarget bool) (padLoc, bool, bool) {
	best, found, tp := padLoc{}, false, false
	bestD := math.Inf(1)
	for _, p := range c.padsOn(net) {
		pi := c.parts[p.ref]
		isT := isTP(p.ref)
		passive := pi != nil && len(pi.nets()) <= 2 && (pi.Kind == powersim.KindCapacitor || pi.Kind == powersim.KindResistor || pi.Kind == powersim.KindInductor)
		if !isT && !passive {
			continue
		}
		d := 0.0
		if haveTarget {
			d = math.Hypot(p.x-target.x, p.y-target.y)
		}
		if isT {
			d -= 1e6 // a real test point always wins
		}
		if d < bestD {
			best, bestD, found, tp = p, d, true, isT
		}
	}
	return best, found, tp
}

func (c *ctx) hasTP(net string) bool {
	for _, p := range c.padsOn(net) {
		if isTP(p.ref) {
			return true
		}
	}
	return false
}

// loadPad is the pad of a rail that draws the most current (or the IR worst pad).
func (c *ctx) loadPad(net string) (string, string) {
	if pl := c.in.Plan; pl != nil && pl.Result != nil && pl.Result.Route != nil && pl.Result.Route.Power != nil {
		for _, n := range pl.Result.Route.Power.Nets {
			if n.Net == net && n.WorstPad != "" {
				ref, pad, _ := strings.Cut(n.WorstPad, ".")
				return ref, pad
			}
		}
	}
	best, bi := elecPin{}, -1.0
	if en := c.elec[net]; en != nil {
		for _, p := range en.Pins {
			if p.Dir == "sink" && p.IA > bi {
				best, bi = p, p.IA
			}
		}
	}
	return best.Ref, best.Pin
}

func (c *ctx) typInputA() (float64, float64, string) {
	typ, peak := 0.0, 0.0
	var names []string
	for _, ref := range c.tree.order {
		if c.tree.kind[ref] != "source" {
			continue
		}
		net := c.tree.output[ref]
		names = append(names, ref+"("+net+")")
		if r := c.scenario("typical"); r != nil {
			if n := r.Nets[net]; n != nil {
				typ = math.Max(typ, n.CurrentA)
			}
		}
		if en := c.elec[net]; en != nil {
			peak = math.Max(peak, en.IA)
		}
	}
	if typ == 0 {
		typ = peak
	}
	return typ, peak, strings.Join(names, ", ")
}

func (c *ctx) buildTestPlan() {
	if c.src == "" {
		c.missing("8 测试点计划", "无电气数据（需要 sim.json 或 intent.json）")
		return
	}
	tp := &TestPlanSection{}
	n := 0
	add := func(it TestItem) {
		n++
		it.ID = sprintf("T%02d", n)
		tp.Items = append(tp.Items, it)
	}
	typA, peakA, srcNames := c.typInputA()
	if typA > 0 {
		limit := math.Max(typA*BenchLimitFactor, 0.05)
		add(TestItem{Category: "上电限流", What: "输入电流（限流上电）", Where: "任一路输入源：" + srcNames,
			Expected: sprintf("典型 %s（typical），峰值场景 %s", fA(typA), fA(peakA)), Limit: sprintf("首次上电限流 %s（%g× 典型）；功能/射频测试前升到 ≥ %s（%g× 峰值）", fA(limit), BenchLimitFactor, fA(peakA*BenchLimitFactor), BenchLimitFactor),
			Method: "可调限流电源 + 电流表；先 0.5× 电压缓升，再升至标称", Basis: "sim typical/peak 源电流"})
	}
	// Rails, upstream first.
	var railNets []string
	for _, net := range sortedKeys(c.elec) {
		if en := c.elec[net]; en.Role == "power" && !en.Floating && !c.isGround(net) {
			railNets = append(railNets, net)
		}
	}
	for _, net := range c.railOrder(railNets) {
		en := c.elec[net]
		prods := c.tree.producers[net]
		expected, basis := sprintf("%s（仿真范围 %s–%s）", fV(en.VNom), fV(en.VMin), fV(en.VMax)), "sim 各场景电压"
		for _, pr := range prods {
			pp := c.parts[pr]
			switch c.tree.kind[pr] {
			case "regulator":
				tol, why := c.regTolerance(pp, en.VNom)
				expected = sprintf("%s ± %s（%s … %s）", fV(en.VNom), fPct(tol), fV(en.VNom*(1-tol/100)), fV(en.VNom*(1+tol/100)))
				basis = why
			case "source":
				if rt := ratingsOf(pp); rt.SourceVMinV > 0 && rt.SourceVMaxV > 0 {
					expected = sprintf("%s–%s（源规格），仿真 %s", fV(rt.SourceVMinV), fV(rt.SourceVMaxV), fV(en.VNom))
					basis = "ratings.sourceVMin/MaxV — " + rt.Source
				} else {
					expected = sprintf("%s ± 外部电源精度（未声明）", fV(en.VNom))
					basis = "外部电源规格未在输入中声明"
				}
			case "or-diode":
				expected = sprintf("%s–%s（源电压 − Vf，随负载变化）", fV(en.VMin), fV(en.VMax))
				basis = "sim 各场景；再叠加源电压容差与 Vf 离散"
			}
		}
		lref, lpad := c.loadPad(net)
		where := ""
		if lref != "" {
			where = "负载端 " + lref + "." + lpad
		}
		target, have := c.padAt(lref, lpad)
		if pp, ok, isT := c.probePad(net, target, have); ok {
			d := ""
			if have {
				d = sprintf("，距负载 %s mm", f1(math.Hypot(pp.x-target.x, pp.y-target.y)*0.0254))
			}
			kind := "无源件焊盘"
			if isT {
				kind = "测试点"
			}
			where += sprintf("；探测 %s.%s（%s%s）", pp.ref, pp.pad, kind, d)
		}
		for _, pr := range prods {
			if out := c.tree.output[pr]; out == net && c.tree.kind[pr] == "regulator" {
				if l := c.outputPadOf(pr, net); l != "" {
					where += "；稳压器输出 " + l
				}
			}
		}
		add(TestItem{Category: "电源轨", Net: net, What: net + " 直流电压", Where: strings.TrimPrefix(where, "；"), Expected: expected,
			Limit: sprintf("满载 %s（%s 场景）", fA(en.IA), c.worstScenario(net)), Method: "万用表 DC V（≥4½ 位），黑表笔就近 GND 焊盘", Basis: basis})
		if !c.hasTP(net) && c.in.Board != nil {
			rec := TPRecommendation{Net: net, Reason: "电源轨无测试点"}
			if have {
				rec.Near = lref + "." + lpad
				rec.XMm, rec.YMm = c.boardXYmm(target.x, target.y)
			}
			tp.AddTP = append(tp.AddTP, rec)
		}
	}
	// Switch nodes / ripple.
	if w := c.worst; w != nil {
		for _, key := range sortedKeys(w.Ripple) {
			re := w.Ripple[key]
			if c.parts[key] != nil {
				continue
			}
			reg := c.parts[re.Regulator]
			fsw := 0.0
			if reg != nil && reg.Lib != nil {
				fsw = reg.Lib.FswHz
			}
			vin := 0.0
			if reg != nil {
				if e := c.elec[c.tree.input[reg.Ref]]; e != nil {
					vin = e.VMax
				}
			}
			out := c.tree.output[re.Regulator]
			cout, caps := c.capsOn(out)
			exp := sprintf("方波 0→%s，占空比 ≈ %s", fV(vin), f3(re.Duty))
			if fsw > 0 {
				exp += sprintf("，fsw %s", fmtSI(fsw, "Hz"))
			}
			add(TestItem{Category: "开关节点", Net: key, What: key + " 开关波形 / 过冲振铃", Where: re.Regulator + " 开关脚与 " + strings.Join(c.inductorOn(key), ",") + " 之间",
				Expected: exp, Limit: "振铃峰值不超过稳压器 SW 脚绝对最大值（需数据手册）", Method: "示波器 ≥ 200 MHz，10× 探头，接地弹簧（不用长地夹）", Basis: "sim ripple / power-models fsw"})
			if out != "" {
				e := "需输出电容值"
				if cout > 0 && fsw > 0 {
					dv := re.DeltaIA / (8 * fsw * cout)
					e = sprintf("≈ %s mVpp（ΔI %s /(8·fsw·ΣC %s)，理想电容，不含 ESR/ESL 与 DC 偏压衰减）", f2(dv*1000), fA(re.DeltaIA), fmtSI(cout, "F"))
				}
				add(TestItem{Category: "输出纹波", Net: out, What: out + " 输出纹波", Where: "输出电容 " + strings.Join(caps, ",") + " 两端", Expected: e,
					Limit: "20 MHz 带宽限制下测量；负载在 peak 场景", Method: "示波器 AC 耦合，接地弹簧直接压在输出电容 GND 端", Basis: "sim ΔI + 输出电容 MPN 解码"})
			}
		}
	}
	// Straps / reset / boot nets.
	press := c.scenario("buttons-pressed")
	for _, net := range sortedKeys(c.elec) {
		if !reStrapNet.MatchString(net) {
			continue
		}
		en := c.elec[net]
		idle := en.VNom
		exp := sprintf("空闲 %s", fV(idle))
		if press != nil {
			if pn := press.Nets[net]; pn != nil {
				exp += sprintf("；按键按下 %s", fV(pn.Voltage))
			}
		}
		if pu := c.pullOn(net); pu != "" {
			exp += "（" + pu + "）"
		}
		add(TestItem{Category: "复位/启动", Net: net, What: net + " 逻辑电平与上电时序", Where: c.pinsText(net), Expected: exp,
			Limit: "高电平 ≥ 0.75×VDD，低电平 ≤ 0.25×VDD（CMOS 通用判据；以芯片手册为准）", Method: "万用表测静态；示波器单次触发看上电上升沿（RC 延时）", Basis: "sim typical / buttons-pressed"})
	}
	// LEDs.
	for _, ref := range c.refs {
		p := c.parts[ref]
		if p.Kind != powersim.KindLED {
			continue
		}
		i := 0.0
		if r := c.scenario("typical"); r != nil {
			i = partCurrentIn(r, ref)
		} else {
			i = c.partCurrent(ref)
		}
		a := p.pinNet("A", "+", "ANODE")
		exp := sprintf("点亮，If ≈ %s", fA(i))
		if e := c.elec[a]; e != nil {
			exp += sprintf("，阳极 %s ≈ %s", a, fV(e.VNom))
		}
		add(TestItem{Category: "指示灯", Net: a, What: ref + " 驱动", Where: ref, Expected: exp, Method: "固件拉高驱动脚；万用表测阳极电压，串联电阻两端压降/阻值 = 电流", Basis: "sim typical"})
	}
	// USB / high-speed.
	if it := c.in.Intent; it != nil {
		seen := map[string]bool{}
		for _, net := range sortedKeys(it.Nets) {
			np := it.Nets[net]
			if np.Interface == "" || seen[np.Interface] {
				continue
			}
			seen[np.Interface] = true
			bridge := ""
			for _, b := range it.Blocks {
				if b.Function == "usb-uart" {
					bridge = b.Core + " " + c.device(b.Core)
				}
			}
			exp := "主机识别设备并完成枚举"
			if bridge != "" {
				exp = "主机枚举出 " + strings.TrimSpace(bridge) + " 串口设备；烧录下载成功"
			}
			add(TestItem{Category: "接口", Net: net, What: np.Interface + " 枚举 / 通信", Where: "接口连接器", Expected: exp,
				Method: "连接 PC；设备管理器 / lsusb 查看；执行一次固件下载", Basis: "intent 接口识别"})
		}
		imp := map[float64][]string{}
		for _, ncl := range it.NetClasses {
			if ncl.ImpedanceOhm > 0 {
				imp[ncl.ImpedanceOhm] = append(imp[ncl.ImpedanceOhm], ncl.Nets...)
			}
		}
		var zs []float64
		for z := range imp {
			zs = append(zs, z)
		}
		sort.Float64s(zs)
		for _, z := range zs {
			add(TestItem{Category: "阻抗", What: sprintf("%g Ω 受控阻抗", z), Where: "板边阻抗测试条（coupon），网络 " + strings.Join(imp[z], ", "),
				Expected: sprintf("%g Ω ± 10 %%（常见工厂阻抗控制公差）", z), Method: "TDR；下单时勾选阻抗控制并要求出具测试报告", Basis: "intent netClasses.impedanceOhm"})
		}
		for _, p := range it.Pairs {
			calc := c.pairTest(p.A, p.B)
			add(TestItem{Category: "绝缘/耐压", What: p.A + " ↔ " + p.B + " 耐压（hi-pot）与绝缘电阻", Where: "两域之间（短接各域全部网络）",
				Expected: calc, Limit: sprintf("爬电 %g mm / 间隙 %g mm（%s）", p.CreepageMm, p.ClearanceMm, p.StandardRef),
				Method: "耐压测试仪（生产 100 % 抽测按标准）；绝缘电阻 500 V DC", Basis: "pkg/safety 试验电压表"})
		}
	}
	// Crystals.
	for _, ref := range c.refs {
		if c.parts[ref].Kind == "crystal" {
			add(TestItem{Category: "时钟", What: ref + " 起振", Where: ref + " 输出侧", Expected: "稳定起振，频率偏差在晶振规格内（需数据手册）",
				Method: "低电容有源探头或频率计；普通 10× 探头会拉偏频率", Basis: "器件存在"})
		}
	}
	if len(tp.AddTP) > 0 {
		tp.Notes = append(tp.Notes, "建议新增测试点：直径 ≥ 1.0 mm 圆形裸铜焊盘（顶层），位置取负载端附近空白处；坐标为相对外框左下角的板坐标（mm），放置前请确认不与器件/走线冲突。")
	}
	c.rep.TestPlan = tp
}

func (c *ctx) worstScenario(net string) string {
	if c.worst != nil {
		if n := c.worst.Nets[net]; n != nil {
			return n.Scenario
		}
	}
	return "intent"
}

func (c *ctx) pairTest(a, b string) string {
	for _, ic := range c.rep.Calcs.safeInsulation() {
		if ic.A == a && ic.B == b {
			if ic.TestVrms > 0 {
				return sprintf("%g Vrms / 60 s 无击穿", ic.TestVrms)
			}
			if ic.TestRef != "" {
				return "试验电压见 " + ic.TestRef
			}
		}
	}
	return "试验电压需按标准确定"
}

func (cs *CalcSection) safeInsulation() []InsCalc {
	if cs == nil {
		return nil
	}
	return cs.Insulation
}

// regTolerance is the output tolerance of a regulated rail.
func (c *ctx) regTolerance(p *partInfo, vout float64) (float64, string) {
	acc, why := DefaultVrefAccuracyPct, sprintf("Vref ±%g %%（假定，数据手册未录入）", DefaultVrefAccuracyPct)
	if rt := ratingsOf(p); rt.VrefAccuracyPct > 0 {
		acc, why = rt.VrefAccuracyPct, sprintf("Vref ±%g %%（%s）", rt.VrefAccuracyPct, rt.Source)
	}
	vref := 0.0
	if p.Lib != nil {
		vref = p.Lib.Vref
	}
	fb := p.pinNet("FB", "ADJ")
	if fb == "" || vref <= 0 || vout <= 0 {
		return acc, why
	}
	var tols []string
	rtol := 0.0
	for _, ref := range c.refs {
		q := c.parts[ref]
		if q.Kind != powersim.KindResistor || q.Res == nil || q.Res.TolPct == 0 {
			continue
		}
		for _, n := range q.nets() {
			if n == fb {
				rtol = math.Max(rtol, q.Res.TolPct)
				tols = append(tols, sprintf("%s %g %%", ref, q.Res.TolPct))
			}
		}
	}
	if rtol == 0 {
		return acc, why + "；分压电阻公差未知"
	}
	div := 2 * rtol * (1 - vref/vout)
	return round(acc+div, 2), why + sprintf(" + 分压 2×%g %%×(1−Vref/Vout)=%s %%（%s）", rtol, f2(div), strings.Join(tols, ", "))
}

func (c *ctx) outputPadOf(reg, net string) string {
	for _, p := range c.elec[net].Pins {
		if p.Dir == "source" {
			return p.Ref + "." + p.Pin
		}
	}
	return ""
}

func (c *ctx) capsOn(net string) (float64, []string) {
	total := 0.0
	var refs []string
	if en := c.elec[net]; en != nil {
		for _, p := range en.Pins {
			q := c.parts[p.Ref]
			if q == nil || q.Kind != powersim.KindCapacitor {
				continue
			}
			refs = append(refs, p.Ref)
			if q.Cap != nil {
				total += q.Cap.Farad
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool { return natLess(refs[i], refs[j]) })
	return total, refs
}

func (c *ctx) inductorOn(net string) []string {
	var out []string
	if en := c.elec[net]; en != nil {
		for _, p := range en.Pins {
			if q := c.parts[p.Ref]; q != nil && q.Kind == powersim.KindInductor {
				out = append(out, p.Ref)
			}
		}
	}
	return out
}

func (c *ctx) pinsText(net string) string {
	var s []string
	if en := c.elec[net]; en != nil {
		for _, p := range en.Pins {
			s = append(s, p.Ref+"."+p.Pin)
		}
	}
	sort.Slice(s, func(i, j int) bool { return natLess(s[i], s[j]) })
	return strings.Join(s, ", ")
}

// pullOn describes resistors between net and a power rail, and caps to GND
// (with the RC time constant when both are known).
func (c *ctx) pullOn(net string) string {
	var out []string
	rPull, cGnd := 0.0, 0.0
	for _, ref := range c.refs {
		q := c.parts[ref]
		ns := q.nets()
		if len(ns) != 2 || (ns[0] != net && ns[1] != net) {
			continue
		}
		other := ns[0]
		if other == net {
			other = ns[1]
		}
		switch q.Kind {
		case powersim.KindResistor:
			if e := c.elec[other]; e != nil && e.Role == "power" {
				v := ""
				if q.Res != nil && q.Res.Ohm > 0 {
					v = " " + fmtSI(q.Res.Ohm, "Ω")
					rPull = q.Res.Ohm
				}
				out = append(out, sprintf("上拉 %s%s → %s", ref, v, other))
			}
		case powersim.KindCapacitor:
			if c.isGround(other) {
				v := ""
				if q.Cap != nil && q.Cap.Farad > 0 {
					v = " " + fmtSI(q.Cap.Farad, "F")
					cGnd += q.Cap.Farad
				}
				out = append(out, sprintf("对地电容 %s%s", ref, v))
			}
		}
	}
	if rPull > 0 && cGnd > 0 {
		out = append(out, sprintf("τ = R·C ≈ %s", fmtSI(rPull*cGnd, "s")))
	}
	return strings.Join(out, "，")
}
