package designreport

import (
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// ShortFailOhm / ShortExpectOhm are the unpowered rail-to-GND guideline
// thresholds (DMM ohms range, red lead on the rail).
const (
	ShortFailOhm   = 10.0
	ShortExpectOhm = 100.0
)

// knownPath finds resistive paths rail → GND: one resistor, or two in series
// through a net that only touches resistors and high-impedance IC pins.
func (c *ctx) knownPath(rail string) (float64, string) {
	var paths []float64
	var desc []string
	for _, ref := range c.refs {
		q := c.parts[ref]
		if q.Kind != powersim.KindResistor || q.Res == nil || q.Res.Ohm <= 0 {
			continue
		}
		ns := q.nets()
		if len(ns) != 2 || (ns[0] != rail && ns[1] != rail) {
			continue
		}
		mid := ns[0]
		if mid == rail {
			mid = ns[1]
		}
		if c.isGround(mid) {
			paths = append(paths, q.Res.Ohm)
			desc = append(desc, ref+" "+fmtSI(q.Res.Ohm, "Ω"))
			continue
		}
		for _, ref2 := range c.refs {
			q2 := c.parts[ref2]
			if ref2 == ref || q2.Kind != powersim.KindResistor || q2.Res == nil || q2.Res.Ohm <= 0 {
				continue
			}
			n2 := q2.nets()
			if len(n2) == 2 && ((n2[0] == mid && c.isGround(n2[1])) || (n2[1] == mid && c.isGround(n2[0]))) {
				paths = append(paths, q.Res.Ohm+q2.Res.Ohm)
				desc = append(desc, sprintf("%s+%s = %s（经 %s）", ref, ref2, fmtSI(q.Res.Ohm+q2.Res.Ohm, "Ω"), mid))
			}
		}
	}
	if len(paths) == 0 {
		return 0, ""
	}
	g := 0.0
	for _, r := range paths {
		g += 1 / r
	}
	return 1 / g, strings.Join(desc, "；")
}

func (c *ctx) buildBringUp() {
	if c.src == "" {
		c.missing("10 调试上电流程", "无电气数据（需要 sim.json 或 intent.json）")
		return
	}
	bu := &BringUpSection{}
	var rails []string
	for _, net := range sortedKeys(c.elec) {
		if en := c.elec[net]; en.Role == "power" && !en.Floating && !c.isGround(net) {
			rails = append(rails, net)
		}
	}
	rails = c.railOrder(rails)
	for _, r := range rails {
		ohm, desc := c.knownPath(r)
		exp := sprintf("> %g Ω（通常 kΩ 级；IC 体二极管/ESD 使读数低于纯电阻路径）", ShortExpectOhm)
		if ohm > 0 {
			exp = sprintf("> %g Ω，且 ≤ 已知电阻路径 %s", ShortExpectOhm, fmtSI(ohm, "Ω"))
		} else {
			desc = "无已知纯电阻路径"
		}
		bu.Shorts = append(bu.Shorts, ShortRow{Net: r, KnownPath: desc, Expected: exp, FailBelow: sprintf("< %g Ω 判短路；%g–%g Ω 需排查", ShortFailOhm, ShortFailOhm, ShortExpectOhm)})
	}
	var polar []string
	if ms := c.rep.Manufacturing; ms != nil {
		for _, a := range ms.Assembly {
			if strings.Contains(a.Concern, "极性") || strings.Contains(a.Concern, "方向") {
				polar = append(polar, a.Ref)
			}
		}
	}
	polar = uniqSorted(polar)
	typA, peakA, srcNames := c.typInputA()
	step := func(title, action, expect, iff string) {
		bu.Steps = append(bu.Steps, Step{N: len(bu.Steps) + 1, Title: title, Action: action, Expect: expect, IfFail: iff})
	}
	step("目检", "放大镜/AOI 检查极性与方向件："+orNone(strings.Join(polar, ", "))+"；细间距与 EP 器件检查连锡、立碑、少锡",
		"极性/1 脚全部与丝印一致，无连锡", "返修后重新目检，禁止带缺陷上电")
	step("断电短路检查", "万用表欧姆档，红表笔接各电源轨、黑表笔接 GND（见下表）", "每条轨 > 100 Ω，数值与下表已知路径同量级",
		"< 10 Ω：按电源树从源头逐段断开（拆 0 Ω/电感/二极管）定位短路")
	if typA > 0 {
		step("限流上电", sprintf("可调电源接其中一路输入（%s，逐路验证），限流 %s（%g× 典型），电压从 0 缓升到标称", srcNames, fA(math.Max(typA*BenchLimitFactor, 0.05)), BenchLimitFactor),
			sprintf("空载/典型电流 ≈ %s（仿真 typical），无器件发热", fA(typA)), "电流顶到限流：立即断电，回到第 2 步；局部发热用热像/手触定位")
	}
	step("测量各电源轨", "按 §8 测试点计划逐条测量，从上游到下游："+strings.Join(rails, " → "), "均在 §8 期望范围内", "见下方故障特征表")
	if c.rep.Power != nil && len(c.rep.Power.Ripple) > 0 {
		step("开关电源波形", "示波器接地弹簧测开关节点与输出纹波（§8）", "占空比与仿真一致，纹波在估算量级", "占空比异常/间歇工作：检查反馈分压、电感焊接、输入电容")
	}
	step("功能与接口", "按 §8 测接口枚举、复位/启动键、指示灯", "全部通过", "接口失败先查差分对焊接与 CC/上下拉电阻值")
	if peakA > 0 {
		step("带载 / 峰值", "运行峰值负载固件（如射频发射），记录输入电流与各轨最低电压", sprintf("输入电流 ≈ %s（仿真峰值场景），各轨不跌出容差", fA(peakA)), "跌落：检查 IR 压降路径（§6）与输入源能力")
	}
	// Failure signatures, only for what exists.
	hasBuck, hasLDO, hasOR, hasUSB, hasLED := false, false, false, false, false
	for _, ref := range c.refs {
		switch c.parts[ref].Kind {
		case powersim.KindBuck:
			hasBuck = true
		case powersim.KindLDO:
			hasLDO = true
		case powersim.KindLED:
			hasLED = true
		}
		if c.tree.kind[ref] == "or-diode" {
			hasOR = true
		}
	}
	if it := c.in.Intent; it != nil {
		for _, np := range it.Nets {
			if strings.HasPrefix(np.Interface, "USB") {
				hasUSB = true
			}
		}
	}
	sig := func(s, cause, check string) {
		bu.Signatures = append(bu.Signatures, Signature{Symptom: s, Cause: cause, Check: check})
	}
	sig("上电即顶限流", "电源轨短路 / 极性件反贴 / IC 方向错", "断电测各轨对地电阻；目检极性件")
	if hasBuck {
		sig("降压输出 0 V，输入正常", "EN 未拉高 / 电感虚焊 / 芯片未起振", "测 EN 电平与开关节点波形")
		sig("降压输出 ≈ 输入电压", "反馈分压开路或阻值错（FB 悬空）", "测 FB 电压应 ≈ Vref；核对分压电阻")
		sig("输出纹波大 / 啸叫", "输出电容虚焊或容值不足（DC 偏压）/ 电感饱和", "测纹波与电感电流；核对 §4 电感余量")
	}
	if hasLDO {
		sig("LDO 输出偏低", "输入电压不足（压差不够）或过流/过热保护", "测 Vin − Vout 与芯片温度")
	}
	if hasOR {
		sig("系统电压比输入低 > 0.5 V", "OR 二极管 Vf 高于模型 / 走线压降", "测二极管两端压差，对照 §3 Vf")
	}
	if hasUSB {
		sig("USB 不枚举", "D+/D− 焊接或交换 / Type-C CC 下拉缺失 / 桥接芯片未供电", "测 VBUS、CC 电阻值、桥接芯片供电与晶振")
	}
	if hasLED {
		sig("指示灯不亮", "LED 反贴 / 驱动脚未输出 / 限流电阻错", "二极管档测 LED 方向；测驱动脚电平")
	}
	c.rep.BringUp = bu
}

func orNone(s string) string {
	if s == "" {
		return "（无）"
	}
	return s
}

func uniqSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return natLess(out[i], out[j]) })
	return out
}
