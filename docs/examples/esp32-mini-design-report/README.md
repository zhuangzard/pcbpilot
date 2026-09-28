# 样例：ESP32-S3 mini 设计报告（v1 / v2）

`pcbpilot report design` 的参考输出（由 v0.5.0 生成）。打开 [v1/report.html](v1/report.html)（单文件，自包含）或
[v1/report.md](v1/report.md)；版本变化见 [CHANGELOG.md](CHANGELOG.md)，机器可读数据见各版本的
`report.json` 与 [index.json](index.json)。

## 来源

2026-09-27 ESP32-S3 mini 现场运行（EasyEDA Pro 桌面版 V3 3.2.149，connector 0.4.1，4 层，
工程 `ceshi`）留下的真实产物（仓库外的 `artifacts/v05-live/`，报告封面与附录记录了每个文件的
路径与 sha256）：`intent.json`、`sim.json`、`final/`（pcb auto plan/feedback/preview）、
`final.after2.json`（保存后板级回读）、`final.drc3.json`（原生 DRC 通过）、`final.check2.txt`
（pcb check）、`rules-check.json`（规则 in-sync）、两页原理图导出图与 `stage-snapshot` 布局快照。

- **v1**：上述产物原样输入 → `PASS with warnings`（L1 峰值电流余量 11 %、USB 0.5 A 预算余量 14 %、
  USBLC6 VRWM 与 5 V 线电压贴边、14 项额定需数据手册、pcb check WARN）；保存重载一致、逐焊盘对账、规则同步与原生 DRC 证据齐全，0 个章节缺失。
- **v2**：只把 intent 中 `+3V3` 声明为 1 A（[inputs/intent-3v3-1A.json](inputs/intent-3v3-1A.json)，
  手工改写的演示输入，不是重新 derive 的 intent）→ 10 mil 线宽 / 1 个过孔不再满足 → `FAIL`，
  变更记录列出意图电流、所需线宽、结论变化和新增问题。

## 重新生成

在一个同时能看到 `artifacts/`（现场产物）与本仓库 `.agents/`、`docs/examples/`（此处经符号链接
命名为 `examples/`）的目录中：

```bash
A=artifacts/v05-live
common=(--project-name "ESP32-S3 mini" --customer Demo --sim $A/sim.json --plan-dir $A/final
  --board $A/final.after2.json --reload-board $A/final.reload.json --drc $A/final.drc4.json
  --check $A/final.check2.txt --rules-check $A/rules-check.json --net-diff $A/netdiff.json
  --image sch:P1=$A/sch-905bb85957eaf435.png --image sch:P2=$A/sch-950ae6609e91d753.png
  --image layout=$A/snap/v05-final/snapshot.png
  --models .agents/skills/pcbpilot/references/power-models.json
  --host "EasyEDA Pro desktop V3 3.2.149" --connector 0.4.1
  --out-dir examples/esp32-mini-design-report)
pcbpilot report design "${common[@]}" --intent $A/intent.json                                          # v1
pcbpilot report design "${common[@]}" --intent examples/esp32-mini-design-report/inputs/intent-3v3-1A.json  # v2
```

相同输入除 `generatedAt` 外逐字节一致。图片按内容哈希存放在 `assets/`，两个版本共用。
