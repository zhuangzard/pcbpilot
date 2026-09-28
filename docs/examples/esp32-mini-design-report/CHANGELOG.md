# 设计报告变更记录 — ESP32-S3 mini

客户：Demo

由 `pcbpilot report design` 生成；每个版本目录含 report.html（自包含）、report.md、report.json。

## v2 — FAIL

- 生成时间：2026-09-28T04:01:02Z
- 报告：[v2/report.html](v2/report.html) · [report.md](v2/report.md) · [report.json](v2/report.json)
- 输入摘要：`467962198b3e`

相对 v1：变更 intent:设计意图 intent.json。

| 指标 | 之前 | 之后 | 变化 |
|---|---|---|---|
| 意图电流 +3V3 | 0.521 A | 1 A | +0.479 A |
| 所需线宽 +3V3 | 4.82 mil | 11.83 mil | +7.01 mil |
| 总体结论 | PASS with warnings | FAIL |  |

- 新增问题：FAIL: +3V3 过孔 1 < 需要 2 @ 1 A
- 新增问题：FAIL: +3V3 线宽 10/10 mil（外/内）< 需要 11.83/23.65 mil @ 1 A

## v1 — PASS with warnings

- 生成时间：2026-09-28T04:01:02Z
- 报告：[v1/report.html](v1/report.html) · [report.md](v1/report.md) · [report.json](v1/report.json)
- 输入摘要：`3942cb484053`

首个版本。

