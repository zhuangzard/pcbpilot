# 设计意图 / design intent

Generator `pcbpilot intent derive`, schemaVersion 1. Machine contract: intent.json (this page is its reading).

- schematic: ../../../testdata/analog/frontend/sch-frontend.json, ../../../testdata/analog/frontend/values.json
- sim: sim.json (in-process pcbpilot sim power)
- standard: IPC-2221B, functional insulation, PD2, MG IIIa, 2000 m, OVC II, coated=false (defaulted: name, insulation, pollutionDegree, materialGroup, altitudeM, overvoltageCategory)
- copper: 4 layers, 1 oz outer / 0.5 oz inner, ΔT 10 °C, JLC04161H-7628 (4-layer 1.6 mm, L1→L2 7628 prepreg 0.2104 mm) (h=8.3 mil, εr=4.40); fab 6.0/6.0 mil, via 12/24 mil
- simulation: pcbpilot sim power, scenarios typical, peak, terminal-only, terminal-j2-only, worst, converged=true
- findings: 1 error, 1 warn, 2 info

## 电路功能 / blocks

| id | function | core | parts | summary |
|---|---|---|---|---|
| POWER_IN | power-input | J2 | J2 R7 | J2 (+5V, terminal); 1.69 mA worst (peak) |
| CONN_J1 | connector | J1 | J1 R1 | J1 (KF301-5.0-2P): SENS_IN |
| CONN_J3 | connector | J3 | J3 | J3 (HDR-1x4): ADC_CLK, ADC_CS, ADC_DOUT |
| IC_U1 | other | U1 | U1 C1 C2 C3 C6 C7 R2 R3 R4 R5 R6 | U1 (MCP6002T-I/SN) |
| IC_U2 | other | U2 | U2 C4 C5 | U2 (MCP3201-CI/SN) |
| IC_U3 | other | U3 | U3 | U3 (LM4040DIM3-4.1/NOPB) |

## 电压域 / domains

| id | kind | reference | Vrms | Vpeak | nets |
|---|---|---|---:|---:|---|
| SELV_5V | SELV | GND | 5 | 5 | +5V AAF ADC_CLK ADC_CS ADC_DOUT ADC_IN BUF_OUT DIV FILT_OUT GND SENS_IN SK_P SK_X VREF |

## 网络电气规划 / nets

| net | role | class | block | V nom | V peak | I (A) | source | width o/i/min mil | vias | clr mil | Z Ω | pair |
|---|---|---|---|---:|---:|---:|---|---|---:|---:|---:|---|
| +5V | power | POWER | POWER_IN | 5 | 5 | 0.0017 | simulated | 10/10/10 | 1 | 6 |  |  |
| GND | ground | GND |  | 0 | 0 | 0.0017 | simulated | 10/10/10 | 1 | 6 |  |  |
| ADC_CLK | clock | SIGNAL | IC_U2 | 0 | 5 | 0 | simulated | 6/6/6 | 1 | 6 |  |  |
| ADC_CS | analog | SIGNAL | IC_U2 | 0 | 5 | 0 | simulated | 6/6/6 | 1 | 6 |  |  |
| ADC_DOUT | analog | SIGNAL | IC_U2 | 0 | 5 | 0 | simulated | 6/6/6 | 1 | 6 |  |  |
| ADC_IN | analog | SIGNAL | IC_U2 | 0 | 0 | 0 | simulated | 6/6/6 | 1 | 6 |  |  |
| AAF | signal | SIGNAL | IC_U1 | 0 | 0 | 0 | simulated | 6/6/6 | 1 | 6 |  |  |
| BUF_OUT | signal | SIGNAL | IC_U1 | 0 | 0 | 0 | simulated | 6/6/6 | 1 | 6 |  |  |
| DIV | signal | SIGNAL | IC_U1 | 0 | 0 | 0 | simulated | 6/6/6 | 1 | 6 |  |  |
| FILT_OUT | signal | SIGNAL | IC_U1 | 0 | 0 | 0 | simulated | 6/6/6 | 1 | 6 |  |  |
| SENS_IN | signal | SIGNAL | CONN_J1 | 0 | 0 | 0 | simulated | 6/6/6 | 1 | 6 |  |  |
| SK_P | signal | SIGNAL | IC_U1 | 0 | 0 | 0 | simulated | 6/6/6 | 1 | 6 |  |  |
| SK_X | signal | SIGNAL | IC_U1 | 0 | 0 | 0 | simulated | 6/6/6 | 1 | 6 |  |  |
| VREF | signal | SIGNAL | POWER_IN | 4.15 | 4.15 | 0.0008 | simulated | 6/6/6 | 1 | 6 |  |  |

### why (power, switch, high-speed, HV)

- **+5V**: sim: nom 5 V (typical), envelope 5…5 V over 3 scenario(s); 1 scenario(s) with the rail unpowered (< 10 % of its maximum) left out of min/max; simulated worst/peak: 0.002A; IPC-2221B 5 V peak needs ≤ 3.9 mil: fab clearance 6.0 mil governs
- **GND**: sim: nom 0 V (typical), envelope 0…0 V over 4 scenario(s); simulated worst/peak: 0.002A
- **ADC_CLK**: no DC path in the simulation (floating signal): bounded by the supply rails of the connected parts, ≤ 5 V; simulation: no DC path (floating); simulated worst/typical: 0.000A; IPC-2221B 5 V peak needs ≤ 3.9 mil: fab clearance 6.0 mil governs
- **ADC_CS**: no DC path in the simulation (floating signal): bounded by the supply rails of the connected parts, ≤ 5 V; simulation: no DC path (floating); simulated worst/typical: 0.000A; IPC-2221B 5 V peak needs ≤ 3.9 mil: fab clearance 6.0 mil governs
- **ADC_DOUT**: no DC path in the simulation (floating signal): bounded by the supply rails of the connected parts, ≤ 5 V; simulation: no DC path (floating); simulated worst/typical: 0.000A; IPC-2221B 5 V peak needs ≤ 3.9 mil: fab clearance 6.0 mil governs
- **ADC_IN**: sim: nom 0 V (typical), envelope 0…0 V over 4 scenario(s); simulated worst/typical: 0.000A

## 网络类 / net classes

| class | track mil | inner mil | min mil | clearance mil | via mil | Z Ω | nets |
|---|---:|---:|---:|---:|---|---:|---|
| GND | 10 | 10 | 5 | 6 | 12/24 |  | GND |
| POWER | 10 | 10 | 5 | 6 | 12/24 |  | +5V |
| SIGNAL | 6 | 6 | 5 | 6 | 12/24 |  | AAF ADC_CLK ADC_CS ADC_DOUT ADC_IN BUF_OUT DIV FILT_OUT SENS_IN SK_P SK_X VREF |

## 设计提示 / findings

- **ERROR** `analog-target-miss` A3 U1:B: 品质因数 Q 0.5 outside the target 0.707 ± 5 % (spec) → adjust the component values (see the optimisation plan) or revise the spec
- **WARN** `analog-adc-source-impedance` A3 U1:B: ADC 采样建立误差 1.51 LSB outside the target ≤ 0.5 LSB (inferred) → lower the source impedance (buffer the node), add a charge-reservoir capacitor at the ADC pin (≥ 2^(N+1)·Csh) with its R·C settled before each sample, or lengthen the ADC sampling time
- **INFO** `analog-tolerance-after` A3 U1:B: with the proposed values 89 % of 100 Monte-Carlo runs meet every target → tighten the tolerance of the frequency-setting parts (C0G/NP0 ±5 % capacitors, 1 % or 0.5 % resistors) or widen the target window
- **INFO** `assumed-model` U3 model lm4040 is marked assumed → replace with datasheet numbers
