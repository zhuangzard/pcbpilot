# 原理图美观度 Phase B：前后证据（2026-10，balanced）

由 `TestSchAesPhaseBArtifacts` 重新生成（确定性，两次运行逐字相同）：

```bash
SCHAES_PHASEB_OUT=$PWD/docs/reviews/2026-10-schematic-aesthetics/phaseB \
  go test ./internal/app -run TestSchAesPhaseBArtifacts -count=1 -timeout 60m
# 整页预览（esp32 两页）：
pcbpilot sch layout-plan --zones [--aesthetics balanced] --from internal/app/testdata/esp32-v05/zones-mcu.json --out p.json
pcbpilot sch layout-render --from p.json --out esp32-mcu-page-after.svg
```

每个 fixture 有 `*-before|after.layout.json`（可直接 `pcbpilot sch aesthetics --snapshot`）、
`.svg`（仓库离线渲染器 `sch layout-render` 的简化符号，**不是**官方 EasyEDA 图形）和 `.png`（rsvg-convert，32 色）。
所有前后预览先过 `validateCompleteLayoutPreview`（完整命名、无诊断态）。汇总 JSON：`summary-balanced.json`。

Fixture：`ams1117-lib-layout`（`sch lib-layout`，宏恩实测 POWER 模块）；`standalone`、`buck-zone`、`mcu-zone`
（`sch_layout_*_test.go` 的离线区 fixture）；`esp32-mcu-*` / `esp32-pwr-*`（v0.5/v0.6 两页 canonical 实测几何经
`internal/app/testdata/esp32-v05/derive-zones.py` 得到的 `layout-plan --zones` 输入，逐区）；`synthetic-long-wire`
（B2：两外围间 840 units 的 module_port 线）、`synthetic-bus-lane`（B3：D0..D3 端口错落）。

## 前后表

单元格 `前→后`，相等只写一个值；`–` = 该项 skipped。defects = W2 交叉 + W3 四通/歧义 X + W4 共线重叠 +
W7 穿越段 + L6 文字重叠（各类单独不得增加）。check/lint = 离线 sch check / layout-lint 计数差异（`=` 全部不变）。

| fixture | score | defects | W1 | W2 | W3 | W4 | W5 | W6 | W7 | W8 | L1 | L2 | L3 | L4 | L6 | L7 | N1 | N2 | N3 | connectivity | check/lint | status | evals |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| ams1117-lib-layout | 81.8→96.3 | 1→0 | 85→95 | 60→100 | 100 | 100 | 0→100 | 100 | 100 | 82→98 | 100 | 100 | 0→67 | – | 100 | 77→79 | 100 | – | – | pin→net ✓, islands ✓ | wire-crossing 1→0 | improved | 415 |
| standalone | 78.9→99.6 | 1→0 | 100 | 100 | 100 | 100 | 0→100 | 100 | 100 | 75→94 | 67→100 | 100 | – | – | 0→100 | – | 100 | – | – | pin→net ✓, islands ✓ | = | improved | 155 |
| buck-zone | 79.2→85.0 | 7→4 | 88→90 | 30→65 | 100 | 100 | 67→87 | 98→93 | 76→88 | 82→84 | 33→67 | 77 | 75 | 53 | 44 | 100 | 100 | – | – | pin→net ✓, islands ✓ | wire-crossing 4→2 | improved | 696 |
| mcu-zone | 63.8→66.6 | 21→14 | 68→72 | 0 | 100 | 100 | 56→69 | 100→86 | 74→91 | 82→83 | 20→40 | 86→95 | 45 | 100 | 100 | 100 | 0 | – | 50 | pin→net ✓, islands ✓ | wire-crossing 18→13 | improved | 947 |
| esp32-mcu-mcu | 50.0→71.6 | 5→4 | 86→80 | 16→37 | 100 | 100 | 12→75 | 100→96 | 100 | 81 | 0→67 | 75→81 | 0 | – | 9 | 88 | 0→100 | – | 25→50 | pin→net ✓, islands ✓ | wire-crossing 4→3 | improved | 857 |
| esp32-mcu-led | 80.8→98.9 | 0→0 | 100 | 100 | 100 | 100 | 0→100 | 100 | 100 | 85→83 | 0→100 | 80→100 | – | – | 100 | – | 100 | – | – | pin→net ✓, islands ✓ | = | improved | 174 |
| esp32-mcu-autodl | 93.4→97.1 | 0→0 | 100→95 | 100 | 100 | 100 | 100 | 100 | 100 | 80→93 | – | 58→100 | 100 | – | 100 | 71 | 100 | – | – | pin→net ✓, islands ✓ | = | improved | 738 |
| esp32-mcu-key_boot | 90.0→100.0 | 0→0 | 100 | 100 | 100 | 100 | 100 | 100 | 100 | 100 | 0→100 | 100 | – | – | 100 | – | 100 | – | – | pin→net ✓, islands ✓ | = | improved | 52 |
| esp32-mcu-key_rst | 90.0→100.0 | 0→0 | 100 | 100 | 100 | 100 | 100 | 100 | 100 | 100 | 0→100 | 100 | – | – | 100 | – | 100 | – | – | pin→net ✓, islands ✓ | = | improved | 52 |
| esp32-pwr-pwr_in | 89.2→94.8 | 0→0 | 75 | 100 | 100 | 100 | 50→100 | 100 | 100 | 83→82 | 100 | 67→100 | 67 | – | 100 | – | 100 | – | – | pin→net ✓, islands ✓ | = | improved | 356 |
| esp32-pwr-buck | 75.4→86.7 | 3→1 | 96→75 | 76 | 75→100 | 100 | 60→86 | 100 | 100 | 75 | 14→71 | 100 | 29 | – | 0→100 | 69 | 100 | – | – | pin→net ✓, islands ✓ | = | improved | 1313 |
| esp32-pwr-usb_conn | 43.8→50.6 | 17→14 | 61→67 | 0 | 100 | 100 | 20→40 | 0→21 | 36→47 | 83→84 | 33 | 85→92 | 0 | – | 100 | – | 0 | – | 25→50 | pin→net ✓, islands ✓ | wire-crossing 12→10 | improved | 418 |
| esp32-pwr-uart | 67.1→72.5 | 5→5 | 93→96 | 53 | 100 | 100 | 60→80 | 100 | 43→39 | 80→81 | 20 | 84→90 | 0 | – | 100 | – | 100 | – | 25→62 | pin→net ✓, islands ✓ | = | improved | 631 |
| synthetic-long-wire | 72.5→91.9 | 0→0 | 100→96 | 100 | 100 | 100 | 100 | 100 | 100 | 100→93 | 100 | 100 | 0 | – | 100 | – | 0→100 | –→100 | – | pin→net ✓, islands split by label (B2) | = | improved | 145 |
| synthetic-bus-lane | 95.0→100.0 | 0→0 | 100 | 100 | 100 | 100 | 100 | 100 | 100 | 100 | 100 | 100 | – | – | 100 | – | 100 | – | 50→100 | pin→net ✓, islands ✓ | = | improved | 100 |

## 读数

- **验收**：AMS1117 异网交叉 1→0（W2 60→100、离线 wire-crossing 1→0），T 结点 W5 0→100；pin→net、NC、物理线岛不变，
  compose 校验通过，check/lint 无新增。
- **没有一行变差的 check/lint 计数**；所有行 pin→net 不变；只有 `synthetic-long-wire` 按设计拆了物理线岛（B2，
  module_port 且不跨核心/外围）。
- 密集区（buck、mcu-zone、esp32 USB_CONN）只部分改善：评估预算（`generate.maxEvaluations`）先耗在线束重布上，
  对齐多数因无空闲位置被拒（`reroute` / `check`），属预期——美观永不以可读性或连接为代价。
- 原生总线（2026-10-03 起默认画，balanced/precision）：只有 `synthetic-bus-lane` 形成完整泳道，after 预览多一条
  `D[0:3]` 原生总线（粗线 + 名字；主干在端口列外侧，四条梳齿各停在端口前 5 units，不碰任何图元），N3 由泳道
  分 100 变为原生总线记分 100，其余指标、check/lint、连接不变。esp32 与 mcu-zone 的唯一协议组 `U0_UART`
  只有 2 个成员（balanced 门槛 3），precision（门槛 2）下其标签仍不同列/不同向，均在 `busLanes[].native`
  记 `skipped` + 原因，几何与上一版逐字节一致（只多了这条报告字段）。真实板上尚无 ≥3 成员的完整泳道样例，
  这是已知覆盖缺口：需要一块带 D0..D7 / SPI 组的源数据来做正例。
