# ADC front-end (analog fixture) 设计报告 v1

> **总体结论：FAIL** · 客户 — · 生成 2026-09-29T00:00:00Z · pcbpilot dev
>
> 输入摘要 `f9d1cb89da7aa6820cc245a56290e5c8a4b71a57e3018889095d3450d6a7ee2b`

**不通过原因**

- intent 有 1 条 error 级发现

**警告**

- intent 有 1 条 warn 级发现
- 4 个器件功率模型为假定值（assumed）
- 7 项器件额定需数据手册确认
- 5 项验证缺少证据（N/A）
- 2 个章节/子项因输入缺失未生成

## 0 封面与输入来源

| 类型 | 说明 | 状态 | 路径 | sha256 |
|---|---|---|---|---|
| intent | 设计意图 intent.json | 已提供 | `intent.json` | `57336c182e9d` |
| sim | 电源仿真 sim.json | 已提供 | `sim.json` | `7361b6398636` |
| plan | pcb auto plan.json | 未提供 | `` | `` |
| feedback | pcb auto feedback.json | 未提供 | `` | `` |
| board | 板级回读 board dump | 未提供 | `` | `` |
| reload-board | 保存重载后回读 | 未提供 | `` | `` |
| drc | 原生 DRC | 未提供 | `` | `` |
| check | pcb check | 未提供 | `` | `` |
| rules-check | 规则同步 rules check | 未提供 | `` | `` |
| net-diff | 焊盘网络对账 | 未提供 | `` | `` |
| analog | 模拟仿真 analog.json | 已提供 | `analog.json` | `5b2b8a71b4a4` |
| values | 器件值/型号 | 已提供 | `../../../testdata/analog/frontend/values.json` | `8739d241ebc9` |
| models | 功率模型/额定 power-models.json | 已提供 | `../../../.agents/skills/pcbpilot/references/power-models.json` | `93ed6785b02b` |

## 1 执行摘要

| 指标 | 数值 | 说明 |
|---|---|---|
| 电源轨 +5V | 5 V，最大 1.69 mA | 最坏场景 peak |
| 电源轨 VREF | 4.15 V，最大 0.85 mA | 最坏场景 typical |
| 输入功率（typical） | 6.75 mW | 负载 6.03 mW，损耗 0.72 mW |
| 输入功率（peak） | 8.45 mW | 负载 7.73 mW，损耗 0.72 mW |
| 最紧器件余量（相对准则） | 68.8 %（准则 ≥ 33.3 %） | C6 直流电压 V vs 额定 |
| 器件检查 | 14 ok / 0 临界 / 0 超限 / 7 需数据手册 |  |
| 原生 DRC（EasyEDA） | N/A | 未提供 pcb drc 结果（--drc） |
| pcb check（DFM 重建审计） | N/A | 未提供 pcb check 输出（--check） |
| 设计发现（intent） | 1 error / 1 warn / 2 info |  |

### 主要风险

| 级别 | 来源 | 内容 | 建议 |
|---|---|---|---|
| error | intent:analog-target-miss | [C2,C3,C4,R4,R5,R6,U1] A3 U1:B: 品质因数 Q 0.5 outside the target 0.707 ± 5 % (spec) | adjust the component values (see the optimisation plan) or revise the spec |
| warn | intent:analog-adc-source-impedance | [C2,C3,C4,R4,R5,R6,U1] A3 U1:B: ADC 采样建立误差 1.51 LSB outside the target ≤ 0.5 LSB (inferred) | lower the source impedance (buffer the node), add a charge-reservoir capacitor at the ADC pin (≥ 2^(N+1)·Csh) with its R·C settled before each sample, or lengthen the ADC sampling time |
| info | feasibility | 7 项器件额定未知（需数据手册），未计入余量判定 | 在 power-models.json 的 ratings 中补充并注明来源 |

## 2 需求与设计意图

| ID | 功能 | 核心 | 器件 | 说明 |
|---|---|---|---|---|
| POWER_IN | power-input | J2 | J2, R7 | J2 (+5V, terminal); 1.69 mA worst (peak) |
| CONN_J1 | connector | J1 | J1, R1 | J1 (KF301-5.0-2P): SENS_IN |
| CONN_J3 | connector | J3 | J3 | J3 (HDR-1x4): ADC_CLK, ADC_CS, ADC_DOUT |
| IC_U1 | other | U1 | U1, C1, C2, C3, C6, C7, R2, R3, R4, R5, R6 | U1 (MCP6002T-I/SN) |
| IC_U2 | other | U2 | U2, C4, C5 | U2 (MCP3201-CI/SN) |
| IC_U3 | other | U3 | U3 | U3 (LM4040DIM3-4.1/NOPB) |

| 电压域 | 类型 | 参考 | Vrms / Vpk | 网络 | 器件 |
|---|---|---|---|---|---|
| SELV_5V | SELV | GND | 5 / 5 | 14 | 20 |

| 安全设定 | 值 | 说明 |
|---|---|---|
| 安全标准 Standard | IPC-2221B | 工程默认值（规格书未声明） |
| 绝缘等级 Insulation | functional | 工程默认值（规格书未声明） |
| 污染等级 Pollution degree | PD2 | 工程默认值（规格书未声明） |
| 材料组 Material group | IIIa | 工程默认值（规格书未声明） |
| 海拔 Altitude | 2000 m | 工程默认值（规格书未声明） |
| 过电压类别 OVC | II | 工程默认值（规格书未声明） |
| 三防涂覆 Coated | 否 | 规格书声明 |

**设计发现**

| 级别 | 类型 | 内容 | 建议 |
|---|---|---|---|
| error | intent:analog-target-miss | [C2,C3,C4,R4,R5,R6,U1] A3 U1:B: 品质因数 Q 0.5 outside the target 0.707 ± 5 % (spec) | adjust the component values (see the optimisation plan) or revise the spec |
| warn | intent:analog-adc-source-impedance | [C2,C3,C4,R4,R5,R6,U1] A3 U1:B: ADC 采样建立误差 1.51 LSB outside the target ≤ 0.5 LSB (inferred) | lower the source impedance (buffer the node), add a charge-reservoir capacitor at the ADC pin (≥ 2^(N+1)·Csh) with its R·C settled before each sample, or lengthen the ADC sampling time |
| info | intent:analog-tolerance-after | [C2,C3,C4,R4,R5,R6,U1] A3 U1:B: with the proposed values 89 % of 100 Monte-Carlo runs meet every target | tighten the tolerance of the frequency-setting parts (C0G/NP0 ±5 % capacitors, 1 % or 0.5 % resistors) or widen the target window |
| info | intent:assumed-model | [U3] U3 model lm4040 is marked assumed | replace with datasheet numbers |

## 3 电源仿真

场景：typical、peak、terminal-only、terminal-j2-only；收敛：是。

| 网络 | 标称 V | 范围 V | typical A | peak A | terminal-only A | terminal-j2-only A | 最大 A |
|---|---|---|---|---|---|---|---|
| +5V | 5 | 0–5 | 0.0014 | 0.0017 | 0 | 0.0017 | 0.0017 (peak) |
| VREF | 4.15 | 0–4.15 | 0.00085 | 0.00085 | 0 | 0.00085 | 0.00085 (typical) |

![各场景电源轨电流](charts/rail-current.svg)

| 场景 | 输入功率 | 负载功率 | 损耗 | 整体效率 |
|---|---|---|---|---|
| typical | 6.75 mW | 6.03 mW | 0.72 mW | 89.3 % |
| peak | 8.45 mW | 7.73 mW | 0.72 mW | 91.5 % |
| terminal-only | 0 W | 0 W | 0 W | — |
| terminal-j2-only | 8.45 mW | 7.73 mW | 0.72 mW | 91.5 % |

![器件功耗](charts/part-power.svg)

| 器件 | 型号 | 模型 | 功耗 | 场景 |
|---|---|---|---|---|
| U2 | MCP3201-CI/SN | load | 3.12 mW | peak |
| U3 | LM4040DIM3-4.1/NOPB | load | 2.91 mW | typical |
| U1 | MCP6002T-I/SN | load | 1.7 mW | peak |
| R7 | 0402WGF1001TCE | resistor | 0.72 mW | typical |

![电源轨功率](charts/rail-power.svg)

![电源树](charts/power-tree.svg)

| 从 | 到 | 电源轨 |
|---|---|---|
| J2 | U1 | +5V 5 V / 1.69 mA |
| J2 | U2 | +5V 5 V / 1.69 mA |

**模型可信度**：approx 2　assumed 4　value 14　

| 器件 | 型号 | 模型 | 可信度 | 来源 |
|---|---|---|---|---|
| C1 | CL05B104KO5NNNC | generic-capacitor | value |  |
| C2 | CL05B103KB5NNNC | generic-capacitor | value |  |
| C3 | CL05B103KB5NNNC | generic-capacitor | value |  |
| C4 | CL05B102KB5NNNC | generic-capacitor | value |  |
| C5 | CL05A105KA5NQNC | generic-capacitor | value |  |
| C6 | CL05B104KO5NNNC | generic-capacitor | value |  |
| C7 | CL05B104KO5NNNC | generic-capacitor | value |  |
| J1 | KF301-5.0-2P | kf301-2p | **assumed** | 2-pin screw terminal used as a supply input: voltage from the net name (e.g. 5V_TERM → 5 V), else the default; 20 mΩ contact + short lead (assumed). |
| J2 | KF301-5.0-2P | kf301-2p | **assumed** | 2-pin screw terminal used as a supply input: voltage from the net name (e.g. 5V_TERM → 5 V), else the default; 20 mΩ contact + short lead (assumed). |
| J3 | HDR-1x4 | generic-connector | **assumed** |  |
| R1 | 0402WGF1003TCE | generic-resistor | value |  |
| R2 | 0402WGF2002TCE | generic-resistor | value |  |
| R3 | 0402WGF1001TCE | generic-resistor | value |  |
| R4 | 0402WGF1002TCE | generic-resistor | value |  |
| R5 | 0402WGF1002TCE | generic-resistor | value |  |
| R6 | 0402WGF1000TCE | generic-resistor | value |  |
| R7 | 0402WGF1001TCE | generic-resistor | value |  |
| U1 | MCP6002T-I/SN | mcp6002 | approx | Microchip MCP6001/2/4 datasheet (DS21733): IQ 100 µA typ / 170 µA max per amplifier (dual = 2×); output load current is not included. |
| U2 | MCP3201-CI/SN | mcp3201 | approx | Microchip MCP3201 datasheet (DS21290): IDD ≈ 300 µA typ / 500 µA max while converting at 5 V; IREF ≈ 150 µA during conversion. |
| U3 | LM4040DIM3-4.1/NOPB | lm4040 | **assumed** | LM4040 shunt reference: the DC power sim has no zener element, so the cathode is a constant sink at a typical bias point (0.7 mA, between the 60–75 µA minimum and 15 mA maximum of TI SNOS633). The reference VOLTAGE and the real cathode current come from pcbpilot sim analog (spice-models/analog-models.json). |

**假设**

- DC operating point (MNA + Newton-Raphson): capacitors open, inductors = DCR, switching regulators averaged by power balance; not a transient simulation
- IC signal pins are high-impedance (no DC drive) unless a GPIO drives an LED network
- loads are constant-current above a knee voltage and resistive below it (unpowered rails draw nothing)
- J2: input source +5V = 5.00 V (net name) with 0.02 Ω series resistance
- J2 (terminal-j2) disconnected in terminal-only
- J1 (terminal) disconnected in terminal-j2-only

## 3A 模拟电路仿真 Analog SPICE

ngspice：ngspice-47（16 次运行）；结论 **FAIL**。数值来自 ngspice（方法列），“解析”列为闭式公式交叉核对。

- 模拟块：3 个（仿真 3）
- 目标：9 项：满足 7，未满足 2
- 建议修改：3 个元件值（原理图修改，须用户确认）

| 块 | 类别 | 电路 | 拓扑 | 模型 | 状态 |
|---|---|---|---|---|---|
| A1 | 电压基准 | 电压基准 U3（VREF） | shunt reference lm4040-4.1 (4.096 V) biased by R7 from +5V | lm4040-4.1（approx） | PASS |
| A2 | 电压跟随器 | 电压跟随器 U1:A（SENS_IN → BUF_OUT） | voltage follower (buffer) | mcp6002（approx） | PASS |
| A3 | Sallen-Key 低通 | Sallen-Key 低通 U1:B（BUF_OUT → FILT_OUT） | unity-gain Sallen-Key low-pass | mcp6002（approx） | FAIL |

**目标 vs 仿真**

| 块 | 指标 | 仿真/计算 | 解析 | 方法 | 目标 | 状态 |
|---|---|---|---|---|---|---|
| A1 | 基准阴极电流 | 753.6µA | 754µA | ngspice-op | 73µA … 15mA (datasheet) | PASS |
| A1 | 输出电压 | 4.096V | 4.096V | ngspice-op |  |  |
| A2 | 增益 | 0.1667 V/V | 0.1667 V/V | ngspice-ac | 0.1667 V/V ± 2 % (inferred) | PASS |
| A2 | -3 dB 频率 | 90.08Hz | 90.09Hz | ngspice-ac |  |  |
| A2 | 谐振峰 | 0 dB |  | ngspice-ac |  |  |
| A2 | 相位裕度 | 72.4 ° |  | ngspice-loop | ≥ 45 ° (inferred) | PASS |
| A2 | 阶跃过冲 | 0 % |  | ngspice-tran |  |  |
| A2 | 输出上摆极限 | 4.975V |  | ngspice-dc |  |  |
| A3 | 增益 | 1 V/V | 1 V/V | ngspice-ac | 1 V/V ± 2 % (inferred) | PASS |
| A3 | -3 dB 频率 | 1.024kHz | 1.024kHz | ngspice-ac | 1kHz ± 5 % (spec) | PASS |
| A3 | 自然频率 f0 | 1.59kHz | 1.592kHz | ngspice-ac |  |  |
| A3 | 品质因数 Q | 0.5 | 0.5 | ngspice-ac | 0.707 ± 5 % (spec) | FAIL |
| A3 | 谐振峰 | 0 dB |  | ngspice-ac |  |  |
| A3 | 相位裕度 | 57.9 ° |  | ngspice-loop | ≥ 45 ° (inferred) | PASS |
| A3 | 阶跃过冲 | 0 % |  | ngspice-tran |  |  |
| A3 | 输出上摆极限 | 4.975V |  | ngspice-dc | ≥ 4.014V (inferred) | PASS |
| A3 | ADC 采样建立误差 | 1.51 LSB |  | ngspice-tran | ≤ 0.5 LSB (inferred) | WARN |

![A2 频率响应（Bode）](charts/analog-a2-bode.svg)

![A2 环路增益 T(f)（输出端电压注入）](charts/analog-a2-loop.svg)

![A2 小信号阶跃响应](charts/analog-a2-step.svg)

![A2 直流传输特性（输入扫描）](charts/analog-a2-transfer.svg)

![A3 频率响应（Bode）](charts/analog-a3-bode.svg)

![A3 环路增益 T(f)（输出端电压注入）](charts/analog-a3-loop.svg)

![A3 小信号阶跃响应](charts/analog-a3-step.svg)

![A3 直流传输特性（输入扫描）](charts/analog-a3-transfer.svg)

![A3 ADC 采样保持（mcp3201，每 10µs 一次，Csh 预放电到 0 V）](charts/analog-a3-sample.svg)

**容差分布（Monte-Carlo）**

| 块 | 方案 | 指标 | 标称 | 最小 | 最大 | σ | 达标比例 | 块良率 | 方法 |
|---|---|---|---|---|---|---|---|---|---|
| A1 | 当前 | 基准阴极电流 | 753.6µA | 747.5µA | 761.2µA | 2.955µA | 100 % | 100 % | ngspice-op-mc ×100 |
| A2 | 当前 | -3 dB 频率 | 90.08Hz | 82.1Hz | 97.51Hz | 3.01Hz | — | 100 % | ngspice-ac-mc ×100 |
| A2 | 当前 | 增益 | 0.1667 V/V | 0.1649 V/V | 0.1685 V/V | 0.0006872 V/V | 100 % | 100 % | ngspice-ac-mc ×100 |
| A3 | 当前 | 自然频率 f0 | 1.59kHz | 1.49kHz | 1.665kHz | 34.23Hz | — | 0 % | ngspice-ac-mc ×100 |
| A3 | 当前 | -3 dB 频率 | 1.024kHz | 939.4Hz | 1.111kHz | 35.28Hz | 75 % | 0 % | ngspice-ac-mc ×100 |
| A3 | 当前 | 增益 | 1 V/V | 1 V/V | 1 V/V | 3.747e-08 V/V | 100 % | 0 % | ngspice-ac-mc ×100 |
| A3 | 当前 | 品质因数 Q | 0.5 | 0.48 | 0.522 | 0.0105 | 0 % | 0 % | ngspice-ac-mc ×100 |
| A3 | 修改后 | 自然频率 f0 | 1.001kHz | 938.1Hz | 1.048kHz | 21.57Hz | — | 89 % | ngspice-ac-mc ×100 |
| A3 | 修改后 | -3 dB 频率 | 993.5Hz | 924.4Hz | 1.058kHz | 28.08Hz | 89 % | 89 % | ngspice-ac-mc ×100 |
| A3 | 修改后 | 增益 | 1 V/V | 1 V/V | 1 V/V | 4.212e-08 V/V | 100 % | 89 % | ngspice-ac-mc ×100 |
| A3 | 修改后 | 品质因数 Q | 0.702 | 0.673 | 0.732 | 0.0148 | 100 % | 89 % | ngspice-ac-mc ×100 |

**建议的元件值修改（原理图修改，须用户确认）**

| 块 | 位号 | 原值 | 新值 | 动作 | 器件 | 修改前 → 修改后 |
|---|---|---|---|---|---|---|
| A3 | C2 | 10nF | **33nF** | replace-lcsc | C1585 33nF | -3 dB 频率 1.024kHz → 993.5Hz；品质因数 Q 0.5 → 0.702 |
| A3 | C3 | 10nF | **15nF** | set-value | 需选型：0402 15nF 10% capacitor | -3 dB 频率 1.024kHz → 993.5Hz；品质因数 Q 0.5 → 0.702 |
| A3 | R5 | 10kΩ | **5.1kΩ** | replace-lcsc | C25905 5.1kΩ | -3 dB 频率 1.024kHz → 993.5Hz；品质因数 Q 0.5 → 0.702 |

确认后：`pcbpilot sim analog compile-plan` → `pcbpilot apply --dry-run` → apply；随后重跑 sim analog / intent derive / report design。

**发现**

| 级别 | 来源 | 说明 | 建议 |
|---|---|---|---|
| error | analog A3 | A3 U1:B: 品质因数 Q 0.5 outside the target 0.707 ± 5 % (spec) | adjust the component values (see the optimisation plan) or revise the spec |
| warn | analog A3 | A3 U1:B: ADC 采样建立误差 1.51 LSB outside the target ≤ 0.5 LSB (inferred) | lower the source impedance (buffer the node), add a charge-reservoir capacitor at the ADC pin (≥ 2^(N+1)·Csh) with its R·C settled before each sample, or lengthen the ADC sampling time |
| info | analog A3 | A3 U1:B: with the proposed values 89 % of 100 Monte-Carlo runs meet every target | tighten the tolerance of the frequency-setting parts (C0G/NP0 ±5 % capacitors, 1 % or 0.5 % resistors) or widen the target window |

> U2.VREF VREF input 150µ A (mcp3201)

数据包：`data/analog/analog.json` `data/analog/ngspice/pcbpilot-generic.lib` `data/analog/ngspice/A1-op.cir` `data/analog/ngspice/A1-op.log` `data/analog/ngspice/A1-mc.cir` `data/analog/ngspice/A2-dc.cir` `data/analog/ngspice/A2-dc.log` `data/analog/ngspice/A2-dc-dc.txt` `data/analog/ngspice/A2-main.cir` `data/analog/ngspice/A2-main.log` `data/analog/ngspice/A2-main-ac.txt` `data/analog/ngspice/A2-main-tran.txt` `data/analog/ngspice/A2-loop.cir` `data/analog/ngspice/A2-loop.log` `data/analog/ngspice/A2-loop-loop.txt` `data/analog/ngspice/A2-mc.cir` `data/analog/ngspice/A3-dc.cir` `data/analog/ngspice/A3-dc.log` `data/analog/ngspice/A3-dc-dc.txt` `data/analog/ngspice/A3-main.cir` `data/analog/ngspice/A3-main.log` `data/analog/ngspice/A3-main-ac.txt` `data/analog/ngspice/A3-main-tran.txt` `data/analog/ngspice/A3-loop.cir` `data/analog/ngspice/A3-loop.log` `data/analog/ngspice/A3-loop-loop.txt` `data/analog/ngspice/A3-sample.cir` `data/analog/ngspice/A3-sample.log` `data/analog/ngspice/A3-sample-sample.txt` `data/analog/ngspice/A3-mc.cir` `data/analog/ngspice/A3-dc-after.cir` `data/analog/ngspice/A3-dc-after.log` `data/analog/ngspice/A3-dc-dc-after.txt` `data/analog/ngspice/A3-main-after.cir` `data/analog/ngspice/A3-main-after.log` `data/analog/ngspice/A3-main-ac-after.txt` `data/analog/ngspice/A3-main-tran-after.txt` `data/analog/ngspice/A3-loop-after.cir` `data/analog/ngspice/A3-loop-after.log` `data/analog/ngspice/A3-loop-loop-after.txt` `data/analog/ngspice/A3-sample-after.cir` `data/analog/ngspice/A3-sample-after.log` `data/analog/ngspice/A3-sample-sample-after.txt` `data/analog/ngspice/A3-mc-after.cir` 

## 4 器件可行性

统计：满足 14 · 临界 0 · 超限 0 · 需数据手册 7

| 器件 | 型号 | 检查项 | 应力 | 额定 | 余量 | 准则 | 结论 | 额定来源 / 备注 |
|---|---|---|---|---|---|---|---|---|
| C1 | CL05B104KO5NNNC | 直流电压 V vs 额定 (100 nF X7R 0402) | 0 V | 16 V | 100 % | ≥ 33.3 % | 满足 | Samsung CL part number |
| C2 | CL05B103KB5NNNC | 直流电压 V vs 额定 (10 nF X7R 0402) | 0 V | 50 V | 100 % | ≥ 33.3 % | 满足 | Samsung CL part number |
| C3 | CL05B103KB5NNNC | 直流电压 V vs 额定 (10 nF X7R 0402) | 0 V | 50 V | 100 % | ≥ 33.3 % | 满足 | Samsung CL part number |
| C4 | CL05B102KB5NNNC | 直流电压 V vs 额定 (1 nF X7R 0402) | 0 V | 50 V | 100 % | ≥ 33.3 % | 满足 | Samsung CL part number |
| C5 | CL05A105KA5NQNC | 直流电压 V vs 额定 (1 µF X5R 0402) | 4.15 V | 25 V | 83.4 % | ≥ 33.3 % | 满足 | Samsung CL part number |
| C6 | CL05B104KO5NNNC | 直流电压 V vs 额定 (100 nF X7R 0402) | 5 V | 16 V | 68.8 % | ≥ 33.3 % | 满足 | Samsung CL part number |
| C7 | CL05B104KO5NNNC | 直流电压 V vs 额定 (100 nF X7R 0402) | 5 V | 16 V | 68.8 % | ≥ 33.3 % | 满足 | Samsung CL part number |
| J1 | KF301-5.0-2P | 接触电流 vs 额定 | 0 A | — | — | ≥ 20 % | 需数据手册 | 需数据手册（额定值未知，不做猜测） |
| J2 | KF301-5.0-2P | 接触电流 vs 额定 | 0.0017 A | — | — | ≥ 20 % | 需数据手册 | 需数据手册（额定值未知，不做猜测） |
| J3 | HDR-1x4 | 接触电流 vs 额定 | 0 A | — | — | ≥ 20 % | 需数据手册 | 需数据手册（额定值未知，不做猜测） |
| R1 | 0402WGF1003TCE | 功率 P vs 封装额定 (100 kΩ @typical) | 0 W | 0.0625 W | 100 % | ≥ 50 % | 满足 | 0402 通用厚膜额定（70 °C）；UNI-ROYAL part number；P = I²R（仿真未给功率） |
| R2 | 0402WGF2002TCE | 功率 P vs 封装额定 (20 kΩ @typical) | 0 W | 0.0625 W | 100 % | ≥ 50 % | 满足 | 0402 通用厚膜额定（70 °C）；UNI-ROYAL part number；P = I²R（仿真未给功率） |
| R3 | 0402WGF1001TCE | 功率 P vs 封装额定 (1 kΩ @typical) | 0 W | 0.0625 W | 100 % | ≥ 50 % | 满足 | 0402 通用厚膜额定（70 °C）；UNI-ROYAL part number；P = I²R（仿真未给功率） |
| R4 | 0402WGF1002TCE | 功率 P vs 封装额定 (10 kΩ @typical) | 0 W | 0.0625 W | 100 % | ≥ 50 % | 满足 | 0402 通用厚膜额定（70 °C）；UNI-ROYAL part number；P = I²R（仿真未给功率） |
| R5 | 0402WGF1002TCE | 功率 P vs 封装额定 (10 kΩ @typical) | 0 W | 0.0625 W | 100 % | ≥ 50 % | 满足 | 0402 通用厚膜额定（70 °C）；UNI-ROYAL part number；P = I²R（仿真未给功率） |
| R6 | 0402WGF1000TCE | 功率 P vs 封装额定 (100 Ω @typical) | 0 W | 0.0625 W | 100 % | ≥ 50 % | 满足 | 0402 通用厚膜额定（70 °C）；UNI-ROYAL part number；P = I²R（仿真未给功率） |
| R7 | 0402WGF1001TCE | 功率 P vs 封装额定 (1 kΩ @typical) | 0.000723 W | 0.0625 W | 98.8 % | ≥ 50 % | 满足 | 0402 通用厚膜额定（70 °C）；UNI-ROYAL part number |
| U1 | MCP6002T-I/SN | 供电 +5V 电压范围 | 5 V | — | — | ≥ 3 % | 需数据手册 | 需数据手册（推荐工作电压范围） |
| U2 | MCP3201-CI/SN | 供电 +5V 电压范围 | 5 V | — | — | ≥ 3 % | 需数据手册 | 需数据手册（推荐工作电压范围） |
| U2 | MCP3201-CI/SN | 供电 VREF 电压范围 | 4.15 V | — | — | ≥ 3 % | 需数据手册 | 需数据手册（推荐工作电压范围） |
| U3 | LM4040DIM3-4.1/NOPB | 供电 VREF 电压范围 | 4.15 V | — | — | ≥ 3 % | 需数据手册 | 需数据手册（推荐工作电压范围） |

![器件余量](charts/margins.svg)

| 对象 | 准则 | 说明 |
|---|---|---|
| 电流（电感/稳压器/二极管/LED/BJT/连接器） | 余量 ≥ 20 % | margin = (额定 − 应力)/额定 |
| 电感 Ipk | Isat ≥ 1.3 × Ipk 更稳妥 | 库中只有单一 Current Rating 时同时比较 Ipk 与 Irms |
| 电阻功率 | P ≤ 50 % 额定（70 °C 额定值） | 0402 1/16 W、0603 1/10 W、0805 1/8 W、1206 1/4 W（通用厚膜） |
| MLCC 直流电压 | 额定 ≥ 1.5 × 工作电压（余量 ≥ 33 %） | X5R/X7R < 2× 时须查 DC 偏压容量曲线 |
| ESD/TVS | VRWM ≥ 线上最高工作电压 | 余量 ≥ 0 即通过 |
| 供电范围 | 工作电压距推荐范围边界 ≥ 3 % | 下限 margin = (V − Vmin)/V |
| 结温 | Tj ≤ 80 % Tj,max | Tj = Ta + P·θJA，θJA 未知 → 需数据手册 |
| 未知额定 | 标记“需数据手册” | 绝不猜测额定值 |

## 5 工程计算

| 基础 | 值 |
|---|---|
| 外层/内层铜厚 | 1 oz (1.378 mil) / 0.5 oz (0.689 mil) |
| 允许温升 ΔT | 10 °C |
| 叠层 | JLC04161H-7628 (4-layer 1.6 mm, L1→L2 7628 prepreg 0.2104 mm) |
| 外层→参考平面 h | 8.28 mil |
| 介电常数 εr | 4.4 |
| 工艺最小线宽/间距 | 6 / 6 mil |

| 项目 | 公式 | 依据 |
|---|---|---|
| IPC-2221/2152 载流 | `I = 0.048 · ΔT^0.44 · A^0.725，A = w · t（mil²）；内层按 IPC-2152 用同一曲线、内层铜厚` | IPC-2221B §6.2 外层曲线；IPC-2152 内外层同截面载流相近（与 intent/pcbauto 一致） |
| 单过孔载流 | `I = 0.024 · ΔT^0.44 · (π·(d+t)·t)^0.725，镀铜 t = 0.7 mil` | 孔壁按内层导体计 |
| 电压间距 | `IPC-2221B 表 6-1（B1 内层 / B2 外层未涂覆 / B4 涂覆），与工艺最小间距取大` |  |
| 微带线 Z0 | `Hammerstad–Jensen（含铜厚修正）` | pkg/pcbauto MicrostripZ0 |
| 差分 Zdiff | `Zdiff ≈ 2·Z0·(1 − 0.48·e^(−0.96·s/h))` | 边耦合微带近似 |
| 爬电/电气间隙 | `IEC 62368-1 / 60601-1 / 61010-1 表格或 IPC-2221B（按 intent.standard）` | pkg/safety；工程参考，最终以认证机构为准 |

**载流线宽**

| 网络 | 类 | 电流 A | 来源 | 外层需/计划 mil | 内层需/计划 mil | 外层载流 A | 结论 |
|---|---|---|---|---|---|---|---|
| +5V | POWER | 0.0017 | simulated | 0 / 10 | 0 / 10 | 0.886 | PASS |
| GND | GND | 0.0017 | simulated | 0 / 10 | 0 / 10 | 0.886 | PASS |

**过孔**

| 网络 | 电流 A | 钻孔 mil | 单孔 A | 需要 | 计划 | 结论 |
|---|---|---|---|---|---|---|
| +5V | 0.0017 | 12 | 0.739 | 1 | 1 | PASS |
| GND | 0.0017 | 12 | 0.739 | 1 | 1 | PASS |

**电压间距**

| 网络 | Vpk | IPC mil | 工艺 mil | 计划 mil | 决定因素 | 结论 |
|---|---|---|---|---|---|---|
| +5V | 5 | 3.94 | 6 | 6 | 工艺最小值 | PASS |
| ADC_CLK | 5 | 3.94 | 6 | 6 | 工艺最小值 | PASS |
| ADC_CS | 5 | 3.94 | 6 | 6 | 工艺最小值 | PASS |
| ADC_DOUT | 5 | 3.94 | 6 | 6 | 工艺最小值 | PASS |
| ADC_IN | 0 | 3.94 | 6 | 6 | 工艺最小值 | PASS |
| GND | 0 | 3.94 | 6 | 6 | 工艺最小值 | PASS |

无绝缘对（单一电压域）。

## 6 布局与布线

> 本节不可用：无 plan.json、板级 dump 或图片

## 7 验证状态

| 检查 | 结论 | 说明 | 证据 |
|---|---|---|---|
| 原生 DRC（EasyEDA） | **N/A** | 未提供 pcb drc 结果（--drc） | `` |
| pcb check（DFM 重建审计） | **N/A** | 未提供 pcb check 输出（--check） | `` |
| 规则同步（pcb rules check） | **N/A** | 未提供（--rules-check） | `` |
| 焊盘网络对账（pad-net diff） | **N/A** | 未提供（--net-diff） | `` |
| 保存/重载一致性 | **N/A** | 未提供板级回读（--board / --reload-board） | `` |

## 8 测试点计划

| ID | 类别 | 测什么 | 在哪里 | 期望 | 限值/设置 | 方法 |
|---|---|---|---|---|---|---|
| T01 | 上电限流 | 输入电流（限流上电） | 任一路输入源：J2(+5V) | 典型 1.35 mA（typical），峰值场景 1.69 mA | 首次上电限流 50 mA（1.5× 典型）；功能/射频测试前升到 ≥ 2.54 mA（1.5× 峰值） | 可调限流电源 + 电流表；先 0.5× 电压缓升，再升至标称 |
| T02 | 电源轨 | +5V 直流电压 | 负载端 R7.1 | 5 V ± 外部电源精度（未声明） | 满载 1.69 mA（peak 场景） | 万用表 DC V（≥4½ 位），黑表笔就近 GND 焊盘 |
| T03 | 电源轨 | VREF 直流电压 | 负载端 U3.1 | 4.15 V（仿真范围 0 V–4.15 V） | 满载 0.85 mA（typical 场景） | 万用表 DC V（≥4½ 位），黑表笔就近 GND 焊盘 |

## 9 制造与装配

| 制板参数 | 值 | 说明 |
|---|---|---|
| 层数 | 4 |  |
| 叠层 | JLC04161H-7628 (4-layer 1.6 mm, L1→L2 7628 prepreg 0.2104 mm) | 下单时选择同名叠层 |
| 板厚 | 1.6 mm |  |
| 铜厚 外/内 | 1 oz / 0.5 oz |  |
| 阻抗控制 | 不需要 |  |
| 表面处理（建议） | 无铅喷锡（HASL-LF）即可 |  |
| 阻焊/丝印（建议） | 绿油白字（标准）；细间距器件间保留阻焊桥 | 建议项，非计算结果 |

| 器件 | 型号 | 关注点 | 说明 |
|---|---|---|---|
| J1 | KF301-5.0-2P | 机械件 | 插拔受力件：检查定位柱/固定脚焊接强度与板边对齐 |
| J2 | KF301-5.0-2P | 机械件 | 插拔受力件：检查定位柱/固定脚焊接强度与板边对齐 |
| J3 | HDR-1x4 | 机械件 | 插拔受力件：检查定位柱/固定脚焊接强度与板边对齐 |
| U1 | MCP6002T-I/SN | 1 脚方向 / 湿敏 | 按 1 脚标记贴装；湿敏等级（MSL）以数据手册为准，超时需烘烤 |
| U2 | MCP3201-CI/SN | 1 脚方向 / 湿敏 | 按 1 脚标记贴装；湿敏等级（MSL）以数据手册为准，超时需烘烤 |
| U3 | LM4040DIM3-4.1/NOPB | 1 脚方向 / 湿敏 | 按 1 脚标记贴装；湿敏等级（MSL）以数据手册为准，超时需烘烤 |

- 无绝缘对（单一 SELV 域）：无铣槽/爬电特别要求

- 静电防护：全程佩戴腕带、防静电台面与包装（IC/模块/ESD 器件均为静电敏感）
- 回流：无铅（SAC305）曲线峰值约 240–250 °C；模块类器件以其手册的回流次数/温度为准
- 清洗：免洗助焊剂残留在高阻/射频区域需清洗

## 10 调试上电流程与注意事项

| # | 步骤 | 操作 | 期望 | 异常处理 |
|---|---|---|---|---|
| 1 | 目检 | 放大镜/AOI 检查极性与方向件：U1, U2, U3；细间距与 EP 器件检查连锡、立碑、少锡 | 极性/1 脚全部与丝印一致，无连锡 | 返修后重新目检，禁止带缺陷上电 |
| 2 | 断电短路检查 | 万用表欧姆档，红表笔接各电源轨、黑表笔接 GND（见下表） | 每条轨 > 100 Ω，数值与下表已知路径同量级 | < 10 Ω：按电源树从源头逐段断开（拆 0 Ω/电感/二极管）定位短路 |
| 3 | 限流上电 | 可调电源接其中一路输入（J2(+5V)，逐路验证），限流 50 mA（1.5× 典型），电压从 0 缓升到标称 | 空载/典型电流 ≈ 1.35 mA（仿真 typical），无器件发热 | 电流顶到限流：立即断电，回到第 2 步；局部发热用热像/手触定位 |
| 4 | 测量各电源轨 | 按 §8 测试点计划逐条测量，从上游到下游：+5V → VREF | 均在 §8 期望范围内 | 见下方故障特征表 |
| 5 | 功能与接口 | 按 §8 测接口枚举、复位/启动键、指示灯 | 全部通过 | 接口失败先查差分对焊接与 CC/上下拉电阻值 |
| 6 | 带载 / 峰值 | 运行峰值负载固件（如射频发射），记录输入电流与各轨最低电压 | 输入电流 ≈ 1.69 mA（仿真峰值场景），各轨不跌出容差 | 跌落：检查 IR 压降路径（§6）与输入源能力 |

| 电源轨 | 已知电阻路径 | 期望 | 判据 |
|---|---|---|---|
| +5V | 无已知纯电阻路径 | > 100 Ω（通常 kΩ 级；IC 体二极管/ESD 使读数低于纯电阻路径） | < 10 Ω 判短路；10–100 Ω 需排查 |
| VREF | 无已知纯电阻路径 | > 100 Ω（通常 kΩ 级；IC 体二极管/ESD 使读数低于纯电阻路径） | < 10 Ω 判短路；10–100 Ω 需排查 |

| 现象 | 可能原因 | 检查 |
|---|---|---|
| 上电即顶限流 | 电源轨短路 / 极性件反贴 / IC 方向错 | 断电测各轨对地电阻；目检极性件 |

## 11 附录

| 网络 | 角色 | 类 | V 标称/最大 | 电流 A | 线宽 外/内 | 间距 | 阻抗 |
|---|---|---|---|---|---|---|---|
| +5V | power | POWER | 5/5 | 0.0017 | 10/10 | 6 |  |
| AAF | signal | SIGNAL | 0/0 | 0 | 6/6 | 6 |  |
| ADC_CLK | clock | SIGNAL | 0/5 | 0 | 6/6 | 6 |  |
| ADC_CS | analog | SIGNAL | 0/5 | 0 | 6/6 | 6 |  |
| ADC_DOUT | analog | SIGNAL | 0/5 | 0 | 6/6 | 6 |  |
| ADC_IN | analog | SIGNAL | 0/0 | 0 | 6/6 | 6 |  |
| BUF_OUT | signal | SIGNAL | 0/0 | 0 | 6/6 | 6 |  |
| DIV | signal | SIGNAL | 0/0 | 0 | 6/6 | 6 |  |
| FILT_OUT | signal | SIGNAL | 0/0 | 0 | 6/6 | 6 |  |
| GND | ground | GND | 0/0 | 0.0017 | 10/10 | 6 |  |
| SENS_IN | signal | SIGNAL | 0/0 | 0 | 6/6 | 6 |  |
| SK_P | signal | SIGNAL | 0/0 | 0 | 6/6 | 6 |  |
| SK_X | signal | SIGNAL | 0/0 | 0 | 6/6 | 6 |  |
| VREF | signal | SIGNAL | 4.15/4.15 | 0.00085 | 6/6 | 6 |  |

| 位号 | 型号 | 类型 | 模型 | 可信度 | 解码 |
|---|---|---|---|---|---|
| C1 | CL05B104KO5NNNC | capacitor | generic-capacitor | value | 100 nF X7R 0402 16 V |
| C2 | CL05B103KB5NNNC | capacitor | generic-capacitor | value | 10 nF X7R 0402 50 V |
| C3 | CL05B103KB5NNNC | capacitor | generic-capacitor | value | 10 nF X7R 0402 50 V |
| C4 | CL05B102KB5NNNC | capacitor | generic-capacitor | value | 1 nF X7R 0402 50 V |
| C5 | CL05A105KA5NQNC | capacitor | generic-capacitor | value | 1 µF X5R 0402 25 V |
| C6 | CL05B104KO5NNNC | capacitor | generic-capacitor | value | 100 nF X7R 0402 16 V |
| C7 | CL05B104KO5NNNC | capacitor | generic-capacitor | value | 100 nF X7R 0402 16 V |
| J1 | KF301-5.0-2P | connector-source | kf301-2p | assumed |  |
| J2 | KF301-5.0-2P | connector-source | kf301-2p | assumed |  |
| J3 | HDR-1x4 | connector | generic-connector | assumed |  |
| R1 | 0402WGF1003TCE | resistor | generic-resistor | value | 100 kΩ ±1% 0402 |
| R2 | 0402WGF2002TCE | resistor | generic-resistor | value | 20 kΩ ±1% 0402 |
| R3 | 0402WGF1001TCE | resistor | generic-resistor | value | 1 kΩ ±1% 0402 |
| R4 | 0402WGF1002TCE | resistor | generic-resistor | value | 10 kΩ ±1% 0402 |
| R5 | 0402WGF1002TCE | resistor | generic-resistor | value | 10 kΩ ±1% 0402 |
| R6 | 0402WGF1000TCE | resistor | generic-resistor | value | 100 Ω ±1% 0402 |
| R7 | 0402WGF1001TCE | resistor | generic-resistor | value | 1 kΩ ±1% 0402 |
| U1 | MCP6002T-I/SN | load | mcp6002 | approx |  |
| U2 | MCP3201-CI/SN | load | mcp3201 | approx |  |
| U3 | LM4040DIM3-4.1/NOPB | load | lm4040 | assumed |  |

**未生成内容**

- §6 布局与布线：无 plan.json、板级 dump 或图片
- §9.2 DFM 警告：未提供 pcb check 输出（--check）

| 术语 | 含义 |
|---|---|
| 余量 Margin | (额定 − 应力)/额定 × 100 %；下限类检查为 (工作值 − 下限)/工作值 |
| IR 压降 | 直流电流在走线/过孔/平面电阻上的电压降 |
| Ipk / Irms | 开关电源电感电流峰值 / 有效值 |
| Isat | 电感饱和电流：电感量下降到规定比例时的电流 |
| VRWM | TVS/ESD 反向工作电压（不导通的最高电压） |
| VRRM | 二极管最大重复反向电压 |
| θJA | 结到环境热阻（°C/W） |
| DC 偏压 | II 类 MLCC 在直流电压下有效容量下降的现象 |
| 爬电距离 / 电气间隙 | 沿绝缘表面 / 空气中两导体的最短距离 |
| TDR | 时域反射计：测量传输线阻抗 |
| SELV | 安全特低电压 |
| DFM | 可制造性设计 |
| EP | 器件底部裸露散热/接地焊盘 |

---
本报告由 pcbpilot report design 自动生成；数值可由 report.json 复核。工程参考值（安全距离、降额准则）最终以器件数据手册与认证机构为准。
