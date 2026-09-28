# 设计报告变更记录 — ESP32-S3 mini

客户：Demo

由 `pcbpilot report design` 生成；每个版本目录是一个交付包：report.html（自包含）、report.md、report.json、manifest.json、assets/（图片、图表、热图）、data/（全部输入与证据），并打包为 zip。

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

