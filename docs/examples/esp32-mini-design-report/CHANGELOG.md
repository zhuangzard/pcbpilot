# 设计报告变更记录 — ESP32-S3 mini

客户：Demo

由 `pcbpilot report design` 生成；每个版本目录是一个交付包：report.html（自包含）、report.md、report.json、manifest.json、assets/（图片、图表、热图）、data/（全部输入与证据），并打包为 zip。

## v3 — PASS with warnings

- 生成时间：2026-09-28T14:31:22Z
- 报告：[v3/report.html](v3/report.html) · [report.md](v3/report.md) · [report.json](v3/report.json)
- 输入摘要：`ff144d3dcdaa`
- 交付包：`pcbpilot-report-ESP32-S3-mini-v3.zip`（清单 [v3/manifest.json](v3/manifest.json)）

相对 v2：新增 analog:模拟仿真 analog.json；变更 board:板级回读 board dump；变更 check:pcb check；变更 drc:原生 DRC；变更 feedback:pcb auto feedback.json；移除 image:BOTTOM 温度（68.88–86.28 °C）；新增 image:BOTTOM 热图；移除 image:BOTTOM 电流密度（0.1–96.68 A/mm²）；移除 image:IN1 温度（68.88–86.28 °C）；新增 image:IN1 热图；移除 image:IN1 电流密度（0.1–96.68 A/mm²）；移除 image:IN2 温度（68.88–86.28 °C）；新增 image:IN2 热图；移除 image:IN2 电流密度（0.1–96.68 A/mm²）；变更 image:PCB 布局（编辑器快照）；移除 image:TOP 温度（68.88–86.28 °C）；新增 image:TOP 热图；移除 image:TOP 电流密度（0.1–96.68 A/mm²）；变更 image:pcb auto 离线布线预览（preview.svg）；变更 intent:设计意图 intent.json；变更 models:功率模型/额定 power-models.json；变更 plan:pcb auto plan.json；变更 post:设计后仿真 post.json；变更 reload-board:保存重载后回读；变更 rules-check:规则同步 rules check。

| 指标 | 之前 | 之后 | 变化 |
|---|---|---|---|
| IR 压降 +3V3 | 3.828 mV | 3.846 mV | +0.018 mV |
| IR 压降 +5V_TERM | 1.98 mV | 1.992 mV | +0.012 mV |
| IR 压降 VSYS_5V | 15.617 mV | 15.574 mV | -0.043 mV |
| 设计后压降 +5V_TERM | 0.993 mV | 1.007 mV | +0.014 mV |
| 意图电流 +3V3 | 1 A | 0.521 A | -0.479 A |
| 所需线宽 +3V3 | 11.83 mil | 4.82 mil | -7.01 mil |
| 总体结论 | FAIL | PASS with warnings |  |
| 板边安全距离（copper-to-edge） | — | PASS |  |

- 新增问题：info: [C7,R5,U3] A4 U3.EN: EN rises to VIH 13.37ms after the rail is stable (≥ 50µs required)
- 新增问题：warn: [C7,Q2,R5,R9] A2 Q2: 集电极峰值电流 228.4mA outside the target ≤ 200mA (inferred)
- 新增问题：info: impedance-controlled nets: USB_DM 90 Ω, USB_DP 90 Ω — order the board with the stackup the widths were solved for (JLC04161H-7628 (4-layer 1.6 mm, L1→L2 7628 prepreg 0.2104 mm))
- 已解决：FAIL: +3V3 过孔 1 < 需要 2 @ 1 A
- 已解决：FAIL: +3V3 线宽 10/10 mil（外/内）< 需要 11.83/23.65 mil @ 1 A
- 已解决：info: impedance-controlled nets: USB_DM 90 Ω, USB_DP 90 Ω — order the board with the stackup the widths were solved for (JLC04161H-7628 (4-layer 1.6 mm, L1→L2 prepreg 0.2104 mm))

## v2 — FAIL

- 生成时间：2026-09-21T14:13:20Z
- 报告：[v2/report.html](v2/report.html) · [report.md](v2/report.md) · [report.json](v2/report.json)
- 输入摘要：`e1310e2aa47f`
- 交付包：`pcbpilot-report-ESP32-S3-mini-v2.zip`（清单 [v2/manifest.json](v2/manifest.json)）

相对 v1：变更 intent:设计意图 intent.json。

| 指标 | 之前 | 之后 | 变化 |
|---|---|---|---|
| 意图电流 +3V3 | 0.521 A | 1 A | +0.479 A |
| 所需线宽 +3V3 | 4.82 mil | 11.83 mil | +7.01 mil |
| 总体结论 | PASS with warnings | FAIL |  |

- 新增问题：FAIL: +3V3 过孔 1 < 需要 2 @ 1 A
- 新增问题：FAIL: +3V3 线宽 10/10 mil（外/内）< 需要 11.83/23.65 mil @ 1 A

## v1 — PASS with warnings

- 生成时间：2026-09-21T14:13:20Z
- 报告：[v1/report.html](v1/report.html) · [report.md](v1/report.md) · [report.json](v1/report.json)
- 输入摘要：`6985c223aca7`
- 交付包：`pcbpilot-report-ESP32-S3-mini-v1.zip`（清单 [v1/manifest.json](v1/manifest.json)）

首个版本。

