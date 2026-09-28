# 样例：ESP32-S3 mini 设计后仿真（`sim post-layout`）

对 2026-09-27 ESP32-S3 mini 现场板（EasyEDA Pro 桌面版 V3 3.2.149，connector 0.4.1，4 层
JLC04161H-7628，保存重载后的 `pcb dump --include-copper` 回读）做的设计后仿真。状态 `offline-verified`。

| 文件 | 内容 |
|---|---|
| [post.json](post.json) | 完整结果（schemaVersion 1） |
| [post.md](post.md) | 人读报告（含热图链接、与 pcb auto 对比、模型与假设） |
| [heatmaps/](heatmaps/) | 每层温度 `temp-<层>.svg`（统一色标，最热场景）与电流密度 `current-<层>.svg`（对数色标） |
| [feedback.json](feedback.json) | pcb auto 的 feedback.json 合并设计后建议（本板 0 条） |
| [elmer/](elmer/) | Elmer FEM 输入包：`case.sif`、`probes.json`、`mesh/mesh.header`；3.1 MB 的 `mesh.nodes/elements/boundary` 不入库，重跑命令即生成 |

## 结果

| 网络 | 最坏场景 | 设计后压降 | pcb auto 估算 | 最大 J | 最大过孔电流 |
|---|---|---|---|---|---|
| +3V3（L1 → U3.2） | peak | 2.70 mV / 预算 66.4 | 3.83 mV | 56.4 A/mm² | 0.501 A（34 % 载流量） |
| VSYS_5V（D1 → U1.4） | terminal-only | 13.61 mV / 92.5 | 15.62 mV | 48.0 A/mm² | 0.430 A |
| USB_VBUS（J2 → D2.2） | usb-only | 17.07 mV / 99.1 | 17.87 mV | 96.7 A/mm²（5 mil、21 mil 长颈部） | — |
| +5V_TERM（J1 → D1.2） | terminal-only | 0.99 mV / 99.8 | 1.98 mV | 48.0 A/mm² | 0.427 A |
| GND（回流，仅报告） | usb-only | 0.34 mV @ U3.41 | 1.12 mV | 23.4 A/mm² | 0.167 A |
| LX（开关节点，仅报告） | peak | 0.27 mV | 0.61 mV | 29.3 A/mm² | — |

热（静止空气 h = 10 W/m²K 双面，25 °C，无辐射）：板最高 **86.3 °C**，terminal-only 场景 ESP32 模组 U3 下方
（2.14 W：器件 2.13 W + 铜损 7.9 mW，能量平衡误差 < 1e-4 %）；U1 83.3 °C、D1 82.4 °C、L1 82.2 °C、D2 81.8 °C
（器件下板温；θJB/θJC 未入 `power-models.json` → Tj 标“需数据手册”）。铜损单独作用的铜自热 0.40 °C。
typical 场景板最高 37.6 °C；`--emissivity 0.9`（阻焊辐射）时最热场景降到 65.8 °C。
86 °C 是把 ESP32 的 peak 电流（Wi-Fi 发射突发）当**持续**功耗、且只算裸板自然对流的保守上限，
低于 105 °C（FR-4 Tg 余量）警告线。结论 **PASS**，0 条铜皮修改建议。

与 pcb auto 布线期 IR 估算的差别（设计后都更小，原因可逐条对应）：
焊盘内与过孔环内的走线段被更宽的铜短接（pcb auto 从焊盘中心量走线长度）；面铜用宿主真实灌铜
（含净距挖空、热焊盘辐条）的 0.5 mm 栅格，pcb auto 用自身规划多边形上 ≥ 25 mil 粗网格；过孔长度按
真实叠层 z（外层半固化片 0.21 mm、芯板 1.07 mm），pcb auto 取板厚/(层数−1)；逐场景（各自满足 KCL）
求解，pcb auto 对合并 worst 逐供电源求包络。

假设（均写入 `post.json.assumptions`）：IN1 在 dump 中没有任何铜对象 → 按 GND 负片平面建模
（板框内缩 10 mil、反焊盘 = 他网铜 + 6 mil）；J1 两个 THT 焊盘 dump 无孔径 → 按短边一半；83 条热焊盘
辐条按描边宽度当走线。Elmer：本机未装 ElmerSolver → `skipped`（输入包 32 872 节点、23 910 六面体、
244 体；探针 = 板最高点与 D1/D2/L1/U1/U2/U3 下最热单元）。

## 重新生成

在能同时看到 `artifacts/`（现场产物，仓库外）与本仓库 `.agents/`、`docs/examples/`（经符号链接命名为
`examples/`）的目录中：

```bash
A=artifacts/v05-live; E=examples/esp32-mini-post-layout
cp $A/final/feedback.json $E/feedback.json
pcbpilot sim post-layout --board $A/final.reload.json --sim $A/sim.json --intent $A/intent.json \
  --plan $A/final/plan.json --models-lib .agents/skills/pcbpilot/references/power-models.json \
  --out $E/post.json --report $E/post.md --svg-dir $E/heatmaps --feedback $E/feedback.json \
  --elmer-dir $E/elmer --elmer-check
```

仓库内离线回归用裁剪后的同一块板：`pkg/postsim/testdata/esp32mini/`（`TestESP32MiniPostLayout`）。
