# 模拟电路仿真 Analog SPICE（pcbpilot sim analog）

- ngspice：ngspice-47（/opt/homebrew/bin/ngspice，7 次运行）
- 模拟块：4 个，已仿真 4；目标 6 项，满足 5，未满足 1；结论 **警告**
- 类别：稳压器反馈分压 ×1、复位 RC 延时 ×1、晶体管开关 ×2
- Monte-Carlo：100 次/块，seed 1；优化：true

## 发现

| 级别 | 块 | 类型 | 说明 | 建议 |
|---|---|---|---|---|
| warn | A2 | analog-transient-current | A2 Q2: 集电极峰值电流 228.4mA outside the target ≤ 200mA (inferred) | the peak is a short capacitor-discharge pulse; add a series resistor on the collector/capacitor path or confirm the pulse rating in the datasheet |
| info | A3 | analog-reset-ok | A3 U1.EN: EN rises to VIH 13.37ms after the rail is stable (≥ 50µs required) |  |

## A1 · 晶体管开关 Q1（RTS → IO0） — 通过

- 类别：晶体管开关（transistor-switch）；拓扑：BJT switch, drive RTS → Q1_B, load on IO0, return DTR
- 器件：Q1, R6, R7
- 电源轨：+3V3 = 3.318 V（power-sim）
- 模型：bjt `mmbt3904`（datasheet）— onsemi 2N3904/MMBT3904 SPICE model (IS=6.734f BF=416.4 BR=0.7371); IC max 200 mA.; onsemi MMBT3904 datasheet: hFE 100–300 at IC 10 mA, VCE(sat) 0.2 V max at 10 mA/1 mA, IC 200 mA
- 网表：`A1-op.cir`

| 位号 | 作用 | 值 | 容差 | 容差来源 |
|---|---|---|---|---|
| R6 | Rload | 10kΩ | ±1 % | description |
| R7 | Rdrive | 4.7kΩ | ±5 % | description |

| 指标 | 仿真/计算 | 解析 | 方法 | 目标 | 状态 |
|---|---|---|---|---|---|
| 拉低时间 | 28.79ns | — | ngspice-tran | — | — |
| 强制 β | 0.584 | 0.596 | ngspice-op | — | — |
| 基极电流 | 562.9µA | 557µA | ngspice-op | — | — |
| 集电极电流 | 328.8µA | 331.8µA | ngspice-op | — | — |
| 集电极峰值电流 | 330.6µA | — | ngspice-tran | ≤ 200mA（inferred） | PASS |
| 饱和过驱倍数 | 171 × | 168 × | ngspice-op | ≥ 2 ×（inferred） | PASS |
| 释放到 VIH | 131.7ns | — | ngspice-tran | — | — |
| 导通压降 | 29.82mV | — | ngspice-op | — | — |

**优化**：not-needed — every target is met

![A1 switch](plots/analog-a1-switch.svg)

> drive high level 3.32 V (U3 supply)

## A2 · 晶体管开关 Q2（DTR → EN） — 警告

- 类别：晶体管开关（transistor-switch）；拓扑：BJT switch, drive DTR → Q2_B, load on EN, return RTS
- 器件：C6, Q2, R5, R8
- 电源轨：+3V3 = 3.318 V（power-sim），GND = 0 V（ground）
- 模型：bjt `mmbt3904`（datasheet）— onsemi 2N3904/MMBT3904 SPICE model (IS=6.734f BF=416.4 BR=0.7371); IC max 200 mA.; onsemi MMBT3904 datasheet: hFE 100–300 at IC 10 mA, VCE(sat) 0.2 V max at 10 mA/1 mA, IC 200 mA
- 网表：`A2-op.cir`

| 位号 | 作用 | 值 | 容差 | 容差来源 |
|---|---|---|---|---|
| C6 | Cload | 1uF | ±10 % | description |
| R5 | Rload | 10kΩ | ±1 % | description |
| R8 | Rdrive | 4.7kΩ | ±5 % | description |

| 指标 | 仿真/计算 | 解析 | 方法 | 目标 | 状态 |
|---|---|---|---|---|---|
| 拉低时间 | 13.43µs | — | ngspice-tran | — | — |
| 强制 β | 0.584 | 0.596 | ngspice-op | — | — |
| 基极电流 | 562.9µA | 557µA | ngspice-op | — | — |
| 集电极电流 | 328.8µA | 331.8µA | ngspice-op | — | — |
| 集电极峰值电流 | 228.4mA | — | ngspice-tran | ≤ 200mA（inferred） | WARN |
| 饱和过驱倍数 | 171 × | 168 × | ngspice-op | ≥ 2 ×（inferred） | PASS |
| 释放到 VIH | 13.77ms | — | ngspice-tran | — | — |
| 导通压降 | 29.82mV | — | ngspice-op | — | — |

**优化**：unsupported — failing metric icPeakA is not a function of the component values in the analytic model — see the finding's suggestion

![A2 switch](plots/analog-a2-switch.svg)

> drive high level 3.32 V (U3 supply)

## A3 · 复位 RC 延时 U1.EN（EN） — 通过

- 类别：复位 RC 延时（reset-rc）；拓扑：RC pull-up R5 to +3V3 with C6 to GND on U1.EN
- 器件：C6, R5, U1
- 电源轨：+3V3 = 3.318 V（power-sim），GND = 0 V（ground）
- 模型：reset `esp32-s3-chip-pu`（approx）— Espressif ESP32-S3 datasheet, power-up and reset timing: CHIP_PU must rise ≥ 50 µs (t0) after the 3.3 V rail is stable and be held low ≥ 50 µs (t1) to reset; VIH = 0.75·VDD; hardware design guidelines recommend RC = 10 kΩ / 1 µF
- 网表：`A3-rstep.cir`

| 位号 | 作用 | 值 | 容差 | 容差来源 |
|---|---|---|---|---|
| C6 | C | 1uF | ±10 % | description |
| R5 | R | 10kΩ | ±1 % | description |

| 指标 | 仿真/计算 | 解析 | 方法 | 目标 | 状态 |
|---|---|---|---|---|---|
| 复位释放延时 | 13.37ms | 13.86ms | ngspice-tran | ≥ 50µs（datasheet） | PASS |
| RC 时间常数 | 10ms | — | analytic | — | — |

容差 Monte-Carlo（analytic-mc，100 次，normal, ±tol = 3σ (truncated)，良率 100.0 %；变动 C6, R5）

| 指标 | 标称 | 最小 | 最大 | 均值 | σ | 达标比例 |
|---|---|---|---|---|---|---|
| 复位释放延时 | 13.37ms | 12.81ms | 15.29ms | 13.84ms | 442.4µs | 100.0 % |

**优化**：not-needed — every target is met

![A3 rstep](plots/analog-a3-rstep.svg)

## A4 · 稳压器反馈分压 U4（+3V3） — 通过

- 类别：稳压器反馈分压（regulator-feedback）；拓扑：buck sy8089a feedback divider: Vout = 0.6 V·(1 + R3/R4)
- 器件：R3, R4, U4
- 电源轨：GND = 0 V（ground）
- 模型：buck `sy8089a`（datasheet）— Silergy SY8089A datasheet: VFB = 0.6 V, fsw = 1.5 MHz, IOUT 2 A, VIN 2.5–5.5 V, EN high ≥ 1.5 V; LCSC C479074 attributes: Quiescent Current 50 µA, Frequency 1.5 MHz. η ≈ 0.9 at 5 V→3.3 V / 0.3–1 A read from the efficiency curve (approx).
- 网表：`A4-op.cir`

| 位号 | 作用 | 值 | 容差 | 容差来源 |
|---|---|---|---|---|
| R3 | Rtop | 45.3kΩ | ±1 % | description |
| R4 | Rbot | 10kΩ | ±1 % | description |

| 指标 | 仿真/计算 | 解析 | 方法 | 目标 | 状态 |
|---|---|---|---|---|---|
| 分压器电流 | 60µA | — | analytic | — | — |
| 输出电压 | 3.318V | 3.318V | ngspice-op | 3.3V ± 3 %（inferred） | PASS |

容差 Monte-Carlo（ngspice-op-mc，100 次，normal, ±tol = 3σ (truncated)，良率 100.0 %；变动 R3, R4）

| 指标 | 标称 | 最小 | 最大 | 均值 | σ | 达标比例 |
|---|---|---|---|---|---|---|
| 输出电压 | 3.318V | 3.284V | 3.353V | 3.32V | 12.52mV | 100.0 % |

**优化**：not-needed — every target is met

> loop compensation not modelled (no regulator loop model in power-models.json)

> tolerance spread covers the divider resistors only; add the Vref accuracy of the datasheet (typically ±1–2 %) on top
