# 模拟电路仿真 Analog SPICE（pcbpilot sim analog）

- ngspice：ngspice-47（/opt/homebrew/bin/ngspice，11 次运行）
- 模拟块：3 个，已仿真 3；目标 9 项，满足 8，未满足 1；结论 **警告**
- 类别：电压跟随器 ×1、Sallen-Key 低通 ×1、电压基准 ×1
- Monte-Carlo：100 次/块，seed 1；优化：false

## 发现

| 级别 | 块 | 类型 | 说明 | 建议 |
|---|---|---|---|---|
| warn | A3 | analog-adc-source-impedance | A3 U1:B: ADC 采样建立误差 1.53 LSB outside the target ≤ 0.5 LSB (inferred) | lower the source impedance (buffer the node), add a charge-reservoir capacitor at the ADC pin (≥ 2^(N+1)·Csh) with its R·C settled before each sample, or lengthen the ADC sampling time |

## A1 · 电压基准 U3（VREF） — 通过

- 类别：电压基准（voltage-reference）；拓扑：shunt reference lm4040-4.1 (4.096 V) biased by R7 from +5V
- 器件：C5, R7, U3
- 电源轨：+5V = 5 V（power-sim），GND = 0 V（ground）
- 模型：reference `lm4040-4.1`（approx）— TI LM4040 datasheet (SNOS633): 4.096 V, min operating current 73 µA max

| 位号 | 作用 | 值 | 容差 | 容差来源 |
|---|---|---|---|---|
| C5 | C | 1uF | ±10 % | description |
| R7 | Rbias | 1kΩ | ±1 % | description |

| 指标 | 仿真/计算 | 解析 | 方法 | 目标 | 状态 |
|---|---|---|---|---|---|
| 基准阴极电流 | 753.6µA | 754µA | ngspice-op | 73µA … 15mA（datasheet） | PASS |
| 基准负载电流 | 150µA | — | analytic | — | — |
| 基准功耗 | 3.087mW | 3.088mW | ngspice-op | — | — |
| 输出电压 | 4.096V | 4.096V | ngspice-op | — | — |

容差 Monte-Carlo（ngspice-op-mc，100 次，normal, ±tol = 3σ (truncated)，良率 100.0 %；变动 C5, R7）

| 指标 | 标称 | 最小 | 最大 | 均值 | σ | 达标比例 |
|---|---|---|---|---|---|---|
| 基准阴极电流 | 753.6µA | 747.5µA | 761.2µA | 753.7µA | 2.955µA | 100.0 % |

> U2.VREF VREF input 150µ A (mcp3201)

## A2 · 电压跟随器 U1:A（SENS_IN → BUF_OUT） — 通过

- 类别：电压跟随器（opamp-follower）；拓扑：voltage follower (buffer)
- 器件：C1, R1, R2, R3, U1
- 电源轨：+5V = 5 V（power-sim），GND = 0 V（ground）
- 模型：opamp `mcp6002`（approx）— Microchip MCP6001/2/4 datasheet (DS21733): GBWP 1 MHz, SR 0.6 V/µs, VOS ±4.5 mV max (no typ given → max used), AOL 112 dB, VCMR V−−0.3 … V++0.3 V, output within 25 mV of the rails, ISC ≈ ±23 mA at 5.5 V

| 位号 | 作用 | 值 | 容差 | 容差来源 |
|---|---|---|---|---|
| C1 | C | 100nF | ±10 % | description |
| R1 | R | 100kΩ | ±1 % | description |
| R2 | R | 20kΩ | ±1 % | description |
| R3 | R | 1kΩ | ±1 % | description |

| 指标 | 仿真/计算 | 解析 | 方法 | 目标 | 状态 |
|---|---|---|---|---|---|
| 增益 | 0.1667 V/V | 0.1667 V/V | ngspice-ac | 0.1667 V/V ± 2 %（inferred） | PASS |
| 增益 | -15.6 dB | — | ngspice-ac | — | — |
| -3 dB 频率 | 90.08Hz | 90.09Hz | ngspice-ac | — | — |
| 谐振峰 | 0 dB | — | ngspice-ac | — | — |
| 相位裕度 | 72.4 ° | — | ngspice-loop | ≥ 45 °（inferred） | PASS |
| 环路穿越频率 | 953.1kHz | — | ngspice-loop | — | — |
| 阶跃过冲 | 0 % | — | ngspice-tran | — | — |
| 上升时间 10–90% | 3.882ms | — | ngspice-tran | — | — |
| 建立时间 2% | 6.925ms | — | ngspice-tran | — | — |
| 直流偏置输入 | 15.03V | — | ngspice-dc | — | — |
| 输出峰值电流 | 5.5pA | — | ngspice-tran | — | — |
| 直流环路增益 | 111 dB | — | ngspice-loop | — | — |
| 输出直流 | 2.5V | — | ngspice-op | — | — |
| 输出上摆极限 | 4.975V | — | ngspice-dc | — | — |
| 输出下摆极限 | 24.99mV | — | ngspice-dc | — | — |
| 输入共模 | 2.505V | — | ngspice-dc | — | — |

容差 Monte-Carlo（ngspice-ac-mc，100 次，normal, ±tol = 3σ (truncated)，良率 100.0 %；变动 C1, R1, R2, R3）

| 指标 | 标称 | 最小 | 最大 | 均值 | σ | 达标比例 |
|---|---|---|---|---|---|---|
| -3 dB 频率 | 90.08Hz | 82.1Hz | 97.51Hz | 90.06Hz | 3.01Hz | — |
| 增益 | 0.1667 V/V | 0.1649 V/V | 0.1685 V/V | 0.1667 V/V | 0.0006872 V/V | 100.0 % |

![A2 bode](plots/analog-a2-bode.svg)

![A2 loop](plots/analog-a2-loop.svg)

![A2 step](plots/analog-a2-step.svg)

![A2 transfer](plots/analog-a2-transfer.svg)

## A3 · Sallen-Key 低通 U1:B（BUF_OUT → FILT_OUT） — 警告

- 类别：Sallen-Key 低通（sallen-key-lowpass）；拓扑：unity-gain Sallen-Key low-pass
- 器件：C2, C3, C4, R4, R5, R6, U1
- 电源轨：+5V = 5 V（power-sim），GND = 0 V（ground）
- 模型：opamp `mcp6002`（approx）— Microchip MCP6001/2/4 datasheet (DS21733): GBWP 1 MHz, SR 0.6 V/µs, VOS ±4.5 mV max (no typ given → max used), AOL 112 dB, VCMR V−−0.3 … V++0.3 V, output within 25 mV of the rails, ISC ≈ ±23 mA at 5.5 V

| 位号 | 作用 | 值 | 容差 | 容差来源 |
|---|---|---|---|---|
| C2 | C1 | 33nF | ±10 % | description |
| C3 | C2 | 15nF | ±10 % | description |
| C4 | C | 1nF | ±10 % | description |
| R4 | R1 | 10kΩ | ±1 % | description |
| R5 | R2 | 5.1kΩ | ±1 % | description |
| R6 | R | 100Ω | ±1 % | description |

| 指标 | 仿真/计算 | 解析 | 方法 | 目标 | 状态 |
|---|---|---|---|---|---|
| 增益 | 1 V/V | 1 V/V | ngspice-ac | 1 V/V ± 2 %（inferred） | PASS |
| 增益 | -2.23e-05 dB | — | ngspice-ac | — | — |
| -3 dB 频率 | 993.5Hz | 993.7Hz | ngspice-ac | 1kHz ± 5 %（spec） | PASS |
| 自然频率 f0 | 1.001kHz | 1.002kHz | ngspice-ac | — | — |
| 品质因数 Q | 0.702 | 0.701 | ngspice-ac | 0.707 ± 5 %（spec） | PASS |
| 谐振峰 | 0 dB | — | ngspice-ac | — | — |
| 相位裕度 | 58.2 ° | — | ngspice-loop | ≥ 45 °（inferred） | PASS |
| 环路穿越频率 | 758.6kHz | — | ngspice-loop | — | — |
| 阶跃过冲 | 4.13 % | — | ngspice-tran | — | — |
| 上升时间 10–90% | 344.1µs | — | ngspice-tran | — | — |
| 建立时间 2% | 947µs | — | ngspice-tran | — | — |
| 直流偏置输入 | 2.053V | — | ngspice-dc | — | — |
| 首次采样误差 | 1.53 LSB | — | ngspice-tran | — | — |
| 输出峰值电流 | 28.15µA | — | ngspice-tran | — | — |
| 直流环路增益 | 111 dB | — | ngspice-loop | — | — |
| 输出直流 | 2.048V | — | ngspice-op | — | — |
| 输出上摆极限 | 4.975V | — | ngspice-dc | ≥ 4.014V（inferred） | PASS |
| 输出下摆极限 | 24.99mV | — | ngspice-dc | — | — |
| ADC 采样建立误差 | 1.53 LSB | — | ngspice-tran | ≤ 0.5 LSB（inferred） | WARN |
| 输入共模 | 2.053V | — | ngspice-dc | — | — |

容差 Monte-Carlo（ngspice-ac-mc，100 次，normal, ±tol = 3σ (truncated)，良率 89.0 %；变动 C2, C3, C4, R4, R5, R6）

| 指标 | 标称 | 最小 | 最大 | 均值 | σ | 达标比例 |
|---|---|---|---|---|---|---|
| 自然频率 f0 | 1.001kHz | 938.1Hz | 1.048kHz | 1kHz | 21.57Hz | — |
| -3 dB 频率 | 993.5Hz | 924.4Hz | 1.058kHz | 991.2Hz | 28.08Hz | 89.0 % |
| 增益 | 1 V/V | 1 V/V | 1 V/V | 1 V/V | 4.212e-08 V/V | 100.0 % |
| 品质因数 Q | 0.702 | 0.673 | 0.732 | 0.701 | 0.0148 | 100.0 % |

![A3 bode](plots/analog-a3-bode.svg)

![A3 loop](plots/analog-a3-loop.svg)

![A3 step](plots/analog-a3-step.svg)

![A3 transfer](plots/analog-a3-transfer.svg)

![A3 sample](plots/analog-a3-sample.svg)
