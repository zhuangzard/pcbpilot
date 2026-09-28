# 模拟电路仿真：`pcbpilot sim analog`

状态：`offline-verified`（ngspice-47 实跑：解析对照单元测试 + ADC 前端样例全闭环 + ESP32 mini 回放；
现场读取路径复用 `sim power` 的只读逐页读取，未现场跑）。

目的：原理图里有**模拟电路**时，自动把每个模拟块抽成 SPICE 子电路（带电源轨与负载），用
**ngspice** 仿真，和目标比较，做容差 Monte-Carlo，并**用仿真数据改元件值**（输出值修改计划）。
它不改原理图：值修改是原理图修改，必须用户确认后才执行。直流电源树仍由
[`sim power`](power-sim.md) 负责；本命令读它的电源轨电压。

## 1. 运行

```bash
pcbpilot sim tools status                 # ngspice 是否可用；缺失：pcbpilot sim tools install [--run]
pcbpilot sim analog --connectivity sch-p1.json --values list-p1.json --spec analog-spec.json \
    --out analog.json --report analog.md --plots-dir plots --apply-plan plan.json
pcbpilot --project <工程> sim analog --pages P1,P2 --out analog.json --report analog.md   # 现场只读
```

- 输入与 `sim power` 相同（离线 connectivity + values，或现场 `--pages`）；`--sim sim.json` 复用电源仿真，
  否则进程内跑 typical 场景取电源轨。
- `--work-dir`（默认 `<out>-ngspice/`）保存每次运行的 `.cir` 网表、`.log` 与 `wrdata` 数据（>500 行的数据文件
  抽稀保存，指标在抽稀前计算）。`--what-if plan.json` 把计划里的新值**只在内存里**套上再仿真，预览修改效果。
- ngspice 缺失：只做解析计算，每块标 `skipped: "ngspice missing → run pcbpilot sim tools install"`，并给一条
  info 级 finding；不会假装仿真过。
- `intent derive` 默认在同一设计上自动跑本步骤（S5.5，见 [design-flow.md](design-flow.md)），finding 进入
  `intent.json`；`--analog` 复用已有 analog.json，`--no-analog` 跳过。

## 2. 识别的电路类别

| class | 识别依据 | 主要指标（方法） |
|---|---|---|
| `opamp-follower` / `opamp-noninverting` / `opamp-inverting` / `opamp-difference` / `opamp-integrator` | 运放通道（库匹配或 IN+/IN-/OUT/V+/V- 引脚名，双/四运放按通道 A–D）+ 反馈网络 | 增益、-3 dB 带宽（ac）、相位裕度/穿越频率（环路增益）、阶跃过冲/上升/建立（tran）、输出摆幅与输入共模（dc 扫描）、输出电流 |
| `sallen-key-lowpass/highpass`、`mfb-lowpass/highpass` | +in / −in 的 X 节点 RC 结构（单位增益或 1+Rf/Rg） | f0、Q（相位 ±90° 点）、fc、峰值、相位裕度 |
| `comparator` | 无负反馈；out→+in 电阻为回差；开漏比较器（LM393…）带上拉 | 上升/下降阈值与回差（三角波 tran），解析节点方程对照 |
| `current-sense` / `instrumentation-amp` | INA180/INA240 固定增益；INA128/AD620/INA333 由 RG 求增益 | 增益、带宽、满量程输出（分流器电流取自 sim power） |
| `voltage-reference` | LM4040/TL431 分流基准 + 偏置电阻 | 阴极电流 Ik（op）vs 数据手册窗口、功耗 |
| `adc-input` | ADC 引脚（MCP3201/MCP3008、STM32F1、ESP32-S3 模拟网）的源网络 | 采样保持建立误差（LSB，SAR 开关 + Csh 预放电到 0 V 的 tran）、源阻抗、允许最大源阻抗 |
| `regulator-feedback` | power-models.json 中有 `vref` 的 buck/LDO 的 FB 分压 | Vout = Vref·(1+Rtop/Rbot)（op，误差放大器强制 FB = Vref），容差分布 |
| `crystal-load` | Y/X 位号或描述含晶振，两脚各一只对地电容 | CL = C1C2/(C1+C2)+Cstray vs 规格 CL；BVD 模型串联谐振的频偏 ppm |
| `reset-rc` | MCU 复位/使能脚（EN/CHIP_PU/NRST…）上拉 R + 对地 C | 电源斜坡后到 VIH 的延时 vs 数据手册最小值 |
| `transistor-switch` / `level-shifter` | BJT 基极电阻驱动 / MOSFET 栅极（栅接电源轨 = 电平转换） | Vce(sat)、Ib、强制 β、饱和过驱倍数、导通/释放时间、集电极峰值电流 |
| `lc-filter` | 非开关节点的电感/磁珠 + 输出侧对地电容 | 谐振 f0、峰值 dB（负载由 sim power 电流折算） |
| `rc-lowpass` | 驱动脚 → 串联 R → 对地 C → IC 输入 | fc、阶跃 |

识别不到拓扑的运放标 `opamp-unclassified` 并照常做直流/交流；未知运放用通用模型并在 finding 标 `assumed`。

## 3. 模型（`references/spice-models/`）

- `analog-models.json`：运放（LM358/LM324/TL071/TL072/OPA333/OPA2333/MCP6001/MCP6002/LMV321/LMV358/TLV9061/TLV9062：
  GBW、压摆率、Aol、Vos、输出摆幅余量、输入共模范围、输出电流、电源范围、引脚表）、比较器、电流检测/仪表放大器、
  基准、ADC 输入模型、复位时序、MOSFET、BJT 开关参数；**每条带数据手册来源与 confidence**（多为 `approx` 典型值）。
  匹配顺序同 power-models：LCSC → MPN → nameRegex。
- `pcbpilot-generic.lib`：行为级子电路 `PCBPILOT_OPAMP`（两极点、限流压摆、输出箝位、理想输出节点供环路注入）、
  `PCBPILOT_COMP_OD`、`PCBPILOT_VAMP`、`PCBPILOT_SHUNTREF` 与通用 D/NPN/PNP/NMOS/PMOS。Go 包内嵌一份，测试保证一致。
- 厂商 SPICE 模型：放进 `spice-models/vendor/`，在 `vendorModels[]` 写引脚顺序映射，见
  [spice-models/README.md](spice-models/README.md)。厂商模型没有理想输出节点，相位裕度改由阶跃过冲估算并注明。
- 稳压器 Vref、BJT Gummel-Poon 参数来自 [power-models.json](power-models.json)。

## 4. 目标（`--spec`）与判定

```json
{"blocks":[{"core":"U1:B","targets":{"fcHz":1000,"q":0.707},"tolPct":{"fcHz":5,"q":5},"fixed":["R4"]},
           {"core":"U1:A","inputRange":[0,24]}],
 "adc":{"sampleRateHz":100000,"tsampleS":1e-6},"phaseMarginMinDeg":45,"mcRuns":100,"seed":1,
 "crystals":{"Y1":{"clPF":12}},"rails":{"VBAT":3.7}}
```

块按 `block`（A3）、`core`（U1:B）、`output` 网或任一位号匹配；`min/max` 设窗口，`fixed` 禁止优化器动的位号。
没有 spec 时目标自动推断：设计标称（增益 ±2 %、fc/Q ±10 %）、相位裕度 ≥ 45°、ADC 建立 ≤ ½ LSB、驱动 ADC 的运放
输出上限 ≥ 0.98·Vref、基准 Ik 在数据手册窗口、复位延时 ≥ 数据手册最小值、BJT 过驱 ≥ 2、LC 峰值 ≤ 6 dB、稳压 Vout =
网名电压 ±3 %。未达 spec/datasheet 目标 = FAIL（error），未达推断目标 = WARN。每个指标同时给出解析公式值作交叉核对。

## 5. 优化与修改计划（原理图修改 — 用户确认）

1. 只对未达标（或 spec 指定）且解析模型能计算的指标优化；不可计算的（相位裕度、ADC 建立）给 finding 与改法。
2. 解析初值（Sallen-Key 电容比设计、晶振 C = 2·(CL−Cs) 等）→ E96/E24 电阻、E12 电容与 standard-parts.json 库存值
   上的坐标搜索；代价 = 目标误差 + 每改一个件 + 非库存件 + 仅 E96 的惩罚（少改、用库存件）。
3. 用 ngspice 以新值重跑同一套分析，**仿真代价下降才接受**；同时给修改后的 Monte-Carlo 良率。
4. `--apply-plan plan.json`（`kind: pcbpilot.schematic-value-plan`，`requiresUserConfirmation: true`）：每项
   ref、原值 → 新值、系列、库存件（`replace-lcsc`）或 `needsPartSelection` + 搜索提示、理由、修改前后指标。
5. **执行规则**：把 plan 的修改与前后数值展示给用户，等待明确同意；然后
   `pcbpilot sch list --page <页> > sch-list.json` →
   `pcbpilot sim analog compile-plan --plan plan.json --components sch-list.json --out value-playbook.json` →
   `pcbpilot apply value-playbook.json --dry-run` → apply。库存件编译为 `schematic.component.replace`（保留位号/
   uniqueId/位置）；无库存件默认拒绝，`--allow-value-only` 才编译为只改 Value 属性（BOM 仍是旧料号，必须另行选型）。
   执行后 `sch save`、重新导出 connectivity/list，重跑 `sim analog` / `intent derive` / `report design`。

## 6. 输出（`analog.json`，schemaVersion 1，字段只增不改名）

`{schemaVersion, generator, inputs, ngspice{available, path, version, runs, note}, summary{blocks, byClass,
simulated, targets, met, failing, changes, status}, blocks[{id, class, title, core, channel, parts, nets, input,
output, rails[], topology, components[{ref, role, kind, value, text, tolPct, tolSource, package}], model{id, kind,
confidence, source, params}, analytic{}, metrics[{name, label, unit, value, analytic, method, target{value|min|max,
tolPct, source, why}, status}], tolerance{runs, seed, method, stats{metric:{nominal,min,max,mean,std,p1,p99,inSpecPct}},
yieldPct}, curves[{name bode|loop|step|transfer|sample|ramp|switch|rstep|xtal, x, series[]}], optimisation{status,
before, after, changes[], toleranceAfter, verified}, findings[], simulated, skipped, status, netlist}], findings[],
plan, assumptions[], warnings[], artifacts[{block, kind, path}], workDir}`。`method` 说明数值来源：
`ngspice-op/dc/ac/tran/loop` 或 `analytic`。

设计报告 §3A 读取它（`report design --analog analog.json`，或 `intent derive --report-dir` 自动带上），
图表为内联 SVG，analog.json 与网表/输出进报告包 `vN/data/analog/`，见 [design-report.md](design-report.md)。

## 7. 样例与回归数字

完整样例：仓库 `docs/examples/analog-frontend/`（`run.sh` 可复现；输入 `testdata/analog/frontend/`）。
0–24 V 传感器 → 100k/20k 分压 → 1k/100nF 抗混叠 → MCP6002 跟随 → 单位增益 Sallen-Key（故意 Q = 0.5）→
100 Ω/1 nF → MCP3201（LM4040 4.096 V 基准）。

| 块 | 结果（ngspice-47） |
|---|---|
| A1 LM4040 | Ik 753.6 µA（解析 754 µA；窗口 73 µA–15 mA）PASS |
| A2 跟随器 | 增益 0.1667、输入极点 90.08 Hz（解析 90.09）、PM 72.4°、MC 良率 100 % |
| A3 Sallen-Key | fc 1.024 kHz、Q 0.500（spec 0.707±5 % → FAIL）、PM 57.9°、ADC 首采样建立 1.51 LSB（WARN：MCP6002 1 MHz 驱动 SAR 反冲）；MC：Q 达标 0 % |
| 计划 | C2 10 nF → 33 nF（C1585）、C3 10 nF → 15 nF（需选型）、R5 10 kΩ → 5.1 kΩ（C25905）；ngspice 复核 fc 993.5 Hz、Q 0.702；修改后 MC 良率 89 %（X7R ±10 % 电容 → 提示换 C0G） |

ESP32 mini（`pkg/powersim/testdata/esp32mini`，模拟部分很少）：EN RC 10 kΩ/1 µF 在 1 ms 电源斜坡后 13.37 ms 到
VIH = 0.75·VDD（解析 13.86 ms，MC 12.8–15.3 ms；要求 ≥ 50 µs）PASS；自动下载 Q2 拉 EN：导通 13.4 µs 放掉 C6，
**集电极峰值 228 mA 超过 MMBT3904 连续 200 mA**（µs 级脉冲，WARN，建议串电阻或核对脉冲额定），释放后 13.77 ms
回到 VIH；Q1（IO0）过驱 171×、Vce(sat) 30 mV；SY8089 反馈 Vout 3.318 V（±1 % 电阻 MC 3.284–3.353 V，另加 Vref 精度）。

## 8. 能力边界

- 运放为两极点行为模型（非晶体管级）：不含噪声、失真、共模抑制随频率、温漂；输入共模越界靠 dc 点检查报告。
- 稳压器环路补偿、晶振负阻（起振裕量）没有模型，只报告分压精度与 CL/频偏，并在块 notes 注明。
- Monte-Carlo 只变动无源件；运放 Vos 以典型值固定。电容直流偏压降容未计入。
- `.noise` 未实现；比较器传播延时只在行为模型内粗略体现。
- 识别基于连接拓扑；奇异结构（多级反馈、开关电容）会落到 `opamp-unclassified`，需人工补 spec 或模型。
