# 模拟仿真样例：ADC 前端（`pcbpilot sim analog` 全闭环）

输入 `testdata/analog/frontend/`（`gen.py` 生成的 1.4 连接 IR + 器件值 + 目标 spec）：0–24 V 传感器 →
100k/20k 分压 → 1k/100nF 抗混叠 → MCP6002 跟随（U1:A）→ 单位增益 Sallen-Key 低通（U1:B，故意 Q = 0.5）→
100 Ω/1 nF 反冲滤波 → MCP3201 12 位 SAR ADC（LM4040 4.096 V 基准）。spec：U1:B 目标 fc = 1 kHz、Q = 0.707（±5 %）。

复现（仓库根目录，需 ngspice）：`bash docs/examples/analog-frontend/run.sh`（`PCBPILOT=<二进制>` 可指定）。

| 步骤 | 命令 | 产物 |
|---|---|---|
| 1 模拟仿真 + 优化 | `sim analog --spec … --apply-plan plan.json` | [analog.md](analog.md)、[analog.json](analog.json)、[plan.json](plan.json)、[plots/](plots/)、`analog-ngspice/`（网表 `.cir`、日志、抽稀数据） |
| 2 设计意图 + 报告 | `intent derive --analog analog.json --report-dir …` | [intent.md](intent.md)（含 `analog-*` finding）、[报告 v1 §3A](reports/analog-frontend/v1/report.md)（`data/analog/` 携带 analog.json 与网表） |
| 3 修改预览 | `sim analog --what-if plan.json` | [after/analog.md](after/analog.md)：只在内存套用新值，原理图不变 |
| 4 ESP32 mini 回归 | `sim analog`（两页 + values） | [esp32-mini/analog.md](esp32-mini/analog.md) |

## 结果（ngspice-47）

| 块 | 指标 | 结论 |
|---|---|---|
| A1 LM4040 基准 | Ik 753.6 µA（解析 754 µA，窗口 73 µA–15 mA），MC 100 % | PASS |
| A2 U1:A 跟随 | 增益 0.1667（分压 20/120）、输入极点 90.08 Hz（解析 90.09）、PM 72.4° | PASS |
| A3 U1:B Sallen-Key | fc 1.024 kHz、f0 1.59 kHz、**Q 0.500 → spec 0.707 FAIL**；PM 57.9°；ADC 首采样建立 **1.51 LSB > ½ LSB（WARN）**；MC：Q 达标 0 %、fc 75 % | FAIL |

值修改计划（**原理图修改，须用户确认后执行**）：C2 10 nF → 33 nF（库存 C1585）、C3 10 nF → 15 nF（无库存件，
需选型）、R5 10 kΩ → 5.1 kΩ（库存 C25905）。ngspice 以新值复核：fc 993.5 Hz、Q 0.702（均在 ±5 % 内）；
修改后 Monte-Carlo 良率 89 %（X7R ±10 % 电容 → info finding 建议换 C0G/NP0 ±5 %）。what-if 复跑：8/9 目标达成，
剩余 ADC 建立 1.53 LSB（MCP6002 1 MHz 驱动 SAR 的反冲恢复，改值无法解决：换更快的运放、加大 C4 或延长采样）。

ESP32 mini：EN RC 在电源稳定后 13.37 ms 到 VIH（≥ 50 µs，PASS）；自动下载 Q2 拉低 EN 时 C6 放电峰值
228 mA > MMBT3904 连续 200 mA（µs 脉冲，WARN）；SY8089 反馈 Vout 3.318 V（MC 3.284–3.353 V）。
说明见 [analog-sim.md](../../../.agents/skills/pcbpilot/references/analog-sim.md)。
