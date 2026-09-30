# 美观度 Phase A 基线（2026-09-28）

Phase A = **只度量、只报告**（[README](README.md) §5A）。实现：`pkg/pcbauto/aesthetics.go`（布线 R1–R9、
入口与豁免）、`aesthetics_place.go`（布局 P1–P9）、`symmetry.go`（同构子电路对称检测）、
`aesthetics_snapshot.go`（dump / `board.routed.json` 读入）。出口：

- `pcbpilot pcb aesthetics --board <dump.json|board.routed.json> [--json] [--all] [--no-exemptions]`（离线）；
- `pcb auto run` 的 `plan.json` → `joint.aesthetics`，`report.md`「综合评分」下一行；**权重 0**，不进 `Groups`、
  `Quality`、`Overall`，也不是交付门槛；
- `layout-score` 的 tidy 维**本阶段不接**（权重、分数保持不变，Phase C 再把 P1–P9 并入）。

所有数字由 `pkg/pcbauto/aesthetics_golden_test.go` 钉住（`TestAesGoldenBaselines`、`TestAesReviewCrossCheck`）；
改判据/阈值必须同时改这里与 golden。阈值出处写在代码注释里，全部是**待 Phase E 校准的初值**。

## 1. 口径要点

- **美观是叠加的最低层软约束**（`pcbauto.ConstraintPriority` 第 7 层，见 `docs/concepts.md`「约束优先级」）：
  安全、电气、制造/DRC、布通、效率、布局全部优先，已沉淀的研究步骤一步不减（§8 流程契约）。
- **电气/安全豁免在度量前硬编码**：差分对（`RoleDiff` / `PairWith`）、RF（`RoleRF`）、等长组（`LengthGroup`）网络整网
  不计 R1–R9；地/电源网 ≥3 个间距 ≤2.5 倍过孔径的过孔视为过孔阵列（`power-via-array`），按电流加倍/IR 反馈加孔的网
  （`ViasPerTransition>1` / `ExtraVias`，`via-current`）不计 R7；逐对/高压间距高于板规则的网（`hv-clearance`）不计
  R6 平行等距与 R8 绕行；中段线宽与相邻段不同的“S 形”是 intent 颈缩过渡，不计 R3；隔离带（moat / isolation 区域）与
  长宽比 ≥1.5 的 MULTI 开槽 2×间距内的走线不计。板边铜距带不进任何美观项（P6 只量器件本体到板边）。报告
  `exemptions[]` 逐条列出。
- **防刷分**：布线组分数 × 布通份额（按铜皮连通性算，平面/铺铜网和地网除外）——少布线不可能更“好看”
  （`TestAesUnroutedNeverHelps`）。
- 组内算术平均、aesthetics = 布局组与布线组的算术平均（README §2.2）。
- `--no-exemptions` 只用于和评审脚本对账或诊断；它**不**遵守电气优先。

## 2. ESP32 样例板（`esp32-v05-fixed.routed.json` 计划铜皮 / `esp32-v05-live.reload.json` 保存重载后）

总分（balanced）：fixed **43.2**（布局 37.0 / 布线 49.5，布通份额 1.000）；reload **43.3**（37.0 / 49.5）。

| 项 | fixed 值 | 分 | reload 值 | 分 | 说明 |
|---|---|---|---|---|---|
| P1 行列共线 | 0.733 未对齐 | 0 | 同 | 0 | 15 件有同类近邻，11 件不在任一共线上；7 处“差一点”（C6/C7 差 2.8 mil，R8/R9 差 3.7 mil） |
| P2 阵列等距 | – | skip | – | skip | 无 ≥3 件同类共线行 |
| P3 朝向一致 | 0.625 | 0 | 同 | 0 | 对称无源件多数派 10/16；8 件停在 180/270（R1/R2/R3/R5/R7/C5/C6/C7，见 §5） |
| P4 对称 | – | skip | – | skip | 未检出同构子电路（BOOT/EN 两路按键外围不同构，LED 只有一路） |
| P5 模块矩形度 | 0.692 | 75.1 | 同 | 75.1 | 最差 B-U2（CH340）凸包/外框 0.58 |
| P6 边距一致 | – | skip | – | skip | 板边首排不足 2 件 |
| P7 留白均匀 | 0.875 | 72.5 | 同 | 72.5 | TOP 8×8 占用 CV 0.87 |
| P8 位号丝印 | – | skip | – | skip | 两份 dump 都没有 silk |
| P9 原点落格 | 0.533 | 37.3 | 同 | 37.3 | 5 mil 16/30，25 mil 0/30 |
| R1 非 8 向 | 0.182 | 0 | 0.182 | 0 | 48 段 / 1823 mil，全部在焊盘端；0.5–3° 微歪 17 段 / 681 mil |
| R2 焊盘入线 | 0.242 | 24.2 | 0.247 | 24.7 | 101 次入线：轴向 32 / 45° 21 / 斜入 48；偏心 46（reload 45），角部出线 28 |
| R3 S 形错位 | 0.698/in | 30.2 | 0.698/in | 30.2 | 7 处；reload 多出的一处中段线宽与两侧不同（宿主合并后的 intent 颈缩过渡），按颈缩豁免 |
| R4 转折密度 | 3.891/in | 100 | 同 | 100 | 39 个全 45°；冗余共线顶点 5（reload 宿主合并后 2） |
| R5 层方向 | 0.712 | 64.8 | 同 | 64.8 | TOP（器件层，权重 0.25）h 25.3%；BOTTOM（权重 1）v 93.9% |
| R6 平行等距 | – | skip | – | skip | 只有 1 对平行邻线（U0RXD/U0TXD），束内 CV 无定义 |
| R7 落格 | 0 | 0 | 0 | 0 | 过孔 0/12（豁免：4 个 GND 阵列过孔、差分 USB_DM 2 个、按电流定孔的 +3V3/+5V_TERM/GND/LX/VSYS_5V），自由顶点 0/94 |
| R8 单网最大绕行 | 1.853 | 76.5 | 1.851 | 76.6 | 最差 CC2 1.85×，中位 1.05× |
| R9 悬空 stub | 0 | 100 | 0 | 100 | |

豁免：差分 USB_DP / USB_DM（整网）；GND 过孔阵列 4 个（`auto-v37/38/41/42`，按 2.5 倍过孔径聚类）；按电流定孔的
5 个网（+3V3、+5V_TERM、GND、LX、VSYS_5V）不计 R7。

### 2.1 与评审基线对账（`--no-exemptions`，`TestAesReviewCrossCheck`）

| 指标 | 评审（raw/scripts/aesmetrics.py） | 本实现 | 差异原因 |
|---|---|---|---|
| 非 8 向长度占比 | 17.6%（103 段 / 2077 mil） | 17.7%（48 段 1823 mil + 焊盘内 55 段 254 mil = 2077 mil） | 一致。本实现把整段落在同一焊盘内的 55 段（254 mil，被焊盘铜皮盖住）单列、不计分（Fable R2），计分口径 = 1823/11763 = 15.5%（带豁免时 1823/10020 = 18.2%） |
| ≥20 mil 歪扇出线 | 48 段 / 1823 mil | 48 段 / 1823 mil | 一致 |
| 转折 / 密度 | 55 个，4.68/in，全 45° | 55 个，4.676/in，全 45° | 一致 |
| 冗余共线顶点 | 16（6 在线宽变化处） | 16（6） | 一致 |
| S 形错位 | 9 处，中位偏移 6.4 mil | 8 处，中位 6.4 mil | 少的一处是 USB_DM 在 D3.6 的 (996,423)：该拐点实际在 D3.6 焊盘铜皮内（焊盘 20.9×42.2 旋转 270°，x 向半宽 21.1 mil）。评审脚本把 dump 的 `width/height`（已是轴对齐外形）当成焊盘坐标系尺寸再旋转一次，90°/270° 焊盘被转了两次，把这个点判到焊盘外 |
| 焊盘入线 | 114 次：轴向 36% / 45° 22% / 斜入 42%，偏心 55，角部 26 | 115 次：41 / 26 / 48（36% / 23% / 42%），偏心 56，角部 28 | 同一原因：旋转焊盘的边界修正后多认出 1 次入线（45° 类），角部出线判定按真实焊盘尺寸多 2 处 |
| 层方向 | TOP h 30.8% / BOTTOM v 92.3% | 30.8% / 92.3% | 一致 |
| 落格 | 过孔 0/69、顶点 0/492 | 0/69、0/492 | 一致 |
| 平行等距 | 仅 1 对，CV 无定义 | 1 对，skip | 一致 |
| reload S 形错位 | 10 处 | 9 处（`--no-exemptions`；默认口径再减 1 处颈缩过渡 = 7） | 同上，一处拐点在旋转焊盘内 |

结论：评审基线数字可复现；差异全部来自评审脚本对旋转焊盘的尺寸处理，本实现用 `snapPadToPad` 的焊盘坐标系尺寸，是正确口径。

## 3. 五块真板（布局项；这些 dump 没有 copper，布线项 skip）

| 板 | 总分 | P1 未对齐 | P2 CV | P3 多数派 | P4 误差（组） | P5 | P6 CV | P7 CV | P8 | P9 5mil |
|---|---|---|---|---|---|---|---|---|---|---|
| mipi-3in1 | 46.6 | 0 / 100 | 0.067 / 64.1 | 1.00 / 88.8 | skip | 0.693 / 67.4 | 0.299 / 0.5 | 1.349 / 25.1 | 0.620 / 15.1 | 0.172 / 12.1 |
| bbclaw | 53.5 | 0.303 / 59.4 | 0 / 100 | 0.75 / 37.5 | 0.712 / 0（1） | 0.604 / 51.3 | 0.212 / 40.5 | 1.080 / 52.0 | 0.830 / 75.0 | 0.889 / 65.6 |
| szpi-esp32s3 | 42.5 | 0.171 / 85.7 | 0.163 / 0 | 0.562 / 0 | 0.120 / 77.8（7） | 0.695 / 69.4 | 0.525 / 0 | 0.673 / 92.7 | 0.785 / 56.9 | 0 / 0 |
| rk3568 | 39.0 | 0.352 / 49.6 | 0.153 / 0 | 0.551 / 0 | 0.228 / 59.3（19） | 0.776 / 83.2 | 0.280 / 19.6 | 0.706 / 89.4 | 0.694 / 50.0 | 0.003 / 0.2 |
| k230 | 53.3 | 0.089 / 100 | 0.329 / 0 | 0.685 / 6.7 | 0.054 / 92.2（13） | 0.751 / 77.5 | 0.403 / 6.2 | 0.528 / 100 | 0.727 / 46.0 | 0.721 / 51.3 |

（格式：值 / 分。P9 统计排除机械件与锁定件。）

### 3.1 检出的对称组（P4）

| 板 | 类型 | 签名 | 实例 | 最佳变换 | 误差 | 分 |
|---|---|---|---|---|---|---|
| bbclaw | decap-ring | U9 +3V3 | C15 / C19 / C20 | 竖轴 | 0.712 | 0 |
| szpi | channel | 电容+电容 | C2+C4 / C11+C9 / C1+C3 / C10+C8 | 竖轴 | 0.160 | 68.6 |
| szpi | channel | 电容+电容 | C62+C67 / C63+C68 / C64+C69 | 平移 | 0.175 | 64.3 |
| szpi | channel | 二极管+电阻（LED 通道） | D5+R7 / D6+R8 / D7+R9 / D8+R10 | 平移 | 0.261 | 39.7 |
| szpi | channel | 电容+电阻 | C70+R41 / C71+R39 | 横轴 | 0.000 | 100 |
| szpi | channel | 5C+3R 模拟前端 | C38+C47+C54+C73+C78+R29+R35+R43 / C36+C46+C53+C72+C77+R31+R34+R42 | 平移 | 0.083 | 90.6 |
| szpi | diff-pair | USB_D+/USB_D- | D13 / D11 | 横轴 | 0 | 100 |
| szpi | decap-ring | U1 3V3 | C13 / C6 | 竖轴 | 0.295 | 30.0 |
| rk3568 | **block** | **SY8113B 双路 Buck** | C274+C275+C281+L9+R42+R43+U16 / C397+C398+C404+L21+R213+R214+U32 | 180° 点对称 | 0.343 | 16.3 |
| rk3568 | channel | 电容+电容 | C254+C255 / C252+C253 | 点对称 | 0.010 | 100 |
| rk3568 | channel | 电容+电阻 | C359+R171 / C356+R168 | 点对称 | 0.495 | 0 |
| rk3568 | channel | 3R | R137+R183+R197 / R136+R182+R196 | 平移 | 0.026 | 100 |
| rk3568 | channel | 2R ×2 组 | R180+R194 / R181+R195；R184+R199 / R185+R198 | 横轴 | 0.003 | 100 |
| rk3568 | diff-pair ×6 | PCIE20_IREFCLK、PCIE20_ITX、PCIE30_ITX0、PCIE30_RX0、USB3_HOST1_SSTX_2、USB3_OTG0 | 串阻/隔直电容/ESD 成对 | 竖轴或点对称 | 0 | 100 |
| rk3568 | decap-ring ×7 | U4 VDD_LOGIC、U6 VCC_1V8 ×3 / VCC-DRAM / VCC_0V6、U11 VCC3V3_SYS | 2–8 颗 | 横/竖轴、点 | 0.196–1.82 | 0–58 |
| k230 | **block** | GH1.25 4P 接口 + 双 ESD ×3 | CN2+D5+D6 / CN3+D7+D8 / CN4+D18+D19 | 平移 | 0.103 | 84.9 |
| k230 | channel | 电容+电阻 | C25+R15 / C39+R21 / C32+R18 | 平移 | 0 | 100 |
| k230 | channel | 二极管+电阻 | D11..D13,D9,D10 + R82..R86（5 路） | 平移 | 0.036 | 100 |
| k230 | channel | 二极管+电阻 | D5..D8,D18,D19 + R70..R96（6 路） | 横轴 | 0.026 | 100 |
| k230 | channel | 二极管+2R | D17+R93+R94 / D16+R91+R92 | 横轴 | 0.002 | 100 |
| k230 | channel | ESD+电阻 | R59+U15 / R58+U16 | 横轴 | 0.009 | 100 |
| k230 | diff-pair | USB1_N/USB1_P | R93 / R91 | 横轴 | 0 | 100 |
| k230 | decap-ring ×6 | U22 VDD0V8_CORE（20 颗）、U23 VDD_1V1 ×3 / VDD_1V8、U13 VDD_3V3 | | 横/竖轴 | 0.033–0.304 | 27–100 |
| mipi / ESP32 | – | 无 | | | | |

读法：k230（官方参考板）的多路接口/LED 通道几乎都是 100 分，说明检测器与误差归一化口径合理；rk3568 的
双路 SY8113 被识别为同构块，但两路是 180° 点对称摆放、元件相对位置不一致（误差 0.34），是真实的“可更对称”。
单颗 decap-ring 误差大（rk3568 U6 C17/C387 1.82）多为去耦被归到远处电源脚，属于 Understand 归属口径，Phase C 前复核。

### 3.2 已知口径问题（Phase E 校准前不据此改生成）

- **P6 边距一致**在所有真板上都很低（0.5–40 分）：板边首排混有接口（结构决定、贴边）与普通器件。候选修法：
  接口件按 edge-io 单列，只对非接口件算 CV。
- **P2 阵列等距**在 szpi / rk3568 / k230 为 0：大板上共线同类件常被其它件隔开，步距天然不等；需要把“行”限制为
  中间无异类件的连续段。
- **P3 朝向**：真板 180/270 的对称无源件很多（szpi 61、rk3568 162、k230 139），30 分的折叠罚分可能过重。
- **P8**：rk3568 / k230 的 dump 丝印没有 BBox，压焊盘项无法判定（只算方位/方向/字号）；K230 的丝印旋转以
  0.1° 为单位（−900），已按“超过 ±360 且除以 10 为 90 的倍数”归一。mipi 有 11/19 个位号框压焊盘，需复核 dump 的 BBox。
- **P7** 阈值（CV 0.6→100、1.6→0）为初值；两块合成参考板本就稀疏（CV 1.55 / 2.34），不是反例。

## 4. 引擎布线的五块板（`make fixture-bench` 同一轮日志，人放布局 + 引擎布线）

`pkg/pcbauto/fixture_test.go` 在每块板布完后记一行只报告的 aesthetics（balanced；不影响断言）。2026-09-28 干净一轮
（`make fixture-bench`，无其它负载；布通率见 §9）：

| 板 | 总分 | 布局 | 布线 × 布通份额 | R1 非 8 向 | R2 入线 | R3 S 形 /in | R4 转折 /in | R5 层向 | R6 CV | R8 最大绕行 | R9 stub |
|---|---|---|---|---|---|---|---|---|---|---|---|
| mipi-3in1 | 51.6 | 51.1 | 52.0 × 1.000 | 5.3% | 0.323 | 0.347 | 2.66 | 0.651 | skip | 1.77× | 0 |
| bbclaw | 47.9 | 50.8 | 44.9 × 1.000 | 4.7% | 0.340 | 0.512 | 3.81 | 0.475 | 0.204 | 2.16× | 0 |
| szpi-esp32s3 | 29.2 | 40.7 | 17.7 × 0.870 | 6.7% | 0.406 | 1.56 | 7.84 | 0.336 | 0.207 | 3.45× | 3 |
| rk3568 | 25.7 | 37.7 | 13.7 × 0.473 | 23.6% | 0.243 | 1.70 | 6.88 | 0.523 | 0.117 | 3.38× | 1 |
| k230 | 32.7 | 54.2 | 11.1 × 0.610 | 7.3% | 0.349 | 1.98 | 9.51 | 0.497 | 0.226 | 28.2× | 2 |

读法：R7 过孔/顶点落格在五块板上全为 0（路由格与 5 mil 设计格不整除，与 ESP32 一致）；R1/R2 的焊盘端斜线与
S 形错位是所有板的共同短板，和评审结论一致；K230 有一根网绕行 28×（布线期，未布通附近），属 Phase B 线索。
这些都是**引擎布线**的数字，不是参考板原厂铜皮——仓库里仍没有已布线真板正样本（评审 §1 结论不变）。

## 5. `placer.tidy` 为什么在样例板上没生效（只调查，未修）

结论：**tidy 确实执行了，之后也没有任何阶段再挪件**；是 tidy 自己的接受判据 `try()` 拒绝了大部分动作，另有 3 件是固定件。

- 复现：按 `internal/app/esp32_esd_regression_test.go` 同参数（`--place --layers 4 --seed 3`，mech/intent/sim/两份 groups，
  `--no-feedback`）在副本中给 `tidy()` 加日志重跑。最终 `Place()` 输出与 `esp32-v05-fixed.routed.json` 30 件 x/y/旋转
  **完全一致**、两次运行相同。`Place()` 共跑 10 次：前 9 次是 AutoFrame 板尺寸试探（`autoframe.go:121,150`），第 10 次
  是唯一一轮布局↔布线闭环，100% 布通、DRC 0，闭环停止；`loop.go:163` 恢复的是同一轮位姿。`Run()`、feedback、
  power-stage、pinswap、`carveStrips`、`autosize` 都不移动器件；导出 `ExportPlacedSnapshot` 写回 `round2(Pos)`，锚点/中心口径无错。
- 统计：折叠 180/270→0/90 **0/8 接受**；组多数派对齐 **0/6 接受**；5 mil 吸栅 **16/27 接受**；J1/J2/U3 是 mech 固定的
  板边件（`mech.go:268` → `placer.go:219` 不进 `pl.movable`），tidy 不看。16 + 11 被拒 + 3 固定 = 实测 16/30 落格。
- 折叠被拒（硬代价 0，局部代价超 `before×1.02+1`）：2 脚件转 180° 会交换两个焊盘所在侧，`symmetricPassive`
  （`placer.go:1505`）当它“电气中性”，但 `localCost` 按真实网几何算——R1 +26%、R2 +60%、R3 +30%、R5 +2.2%、
  R7 +27%、C5 +11%、C6 +48%、C7 +2.7%。
- 对齐/吸栅被拒：legalise / spiral / polish 按本体中心把件贴着 `spacing/2` 间距框排紧，任何 ≤5 mil 的挪动都产生
  `8*pairOverlap` 硬代价（`placer.go:514`、`272`），`try()` 要求硬代价 ≤1e-6（`placer.go:1531`）。例如 C2 与 U3 重叠 27090、
  R1 与 U1 7745；少数无重叠角点又超 2% 局部代价（R1 +4.8%、R2 +8.3%、R4 +4.5%、C6 +3.3%、D1 +4.0%）。
- 结构性原因：吸栅只试最近点与所在格 4 角（`placer.go:1582-1595`，最近点常与某角重复）、从不挪邻件腾位、
  按 `b.Parts` 顺序逐件处理；slack 只有 2%（折叠/吸栅）和 5%（对齐）。
- Phase B 修法方向（未实施）：折叠时同时计入“交换焊盘侧”的线长并允许在 tether slack 内换回；吸栅/对齐按组整体
  平移或先在 legalise 里就把中心放在网格上；接受判据改为“硬代价不增加”而非“为 0”。

## 6. USB_DP/DM 差分疑点（只调查，未修）

结论：**差分对被识别了、配对逻辑也跑了，但它只是给第二根网打折扣的软偏置**；没有任何机制让两根网一起走，事后也没有检查耦合。不是一行能修的 bug。

- 识别正常：`electrical.go:505-514`（`pairPartner`/`looksDiffName`）按名识别，intent 也显式声明（`diffPair`、`interface: USB`、
  `lengthGroup: USB_D`，`intent.go:482-483, 508-511`）。计划：两网 role `diff` 互为 pair，间隙 6.0 mil，线宽 11.7 mil，90 Ω。
- 路由：`routeOrder`（`router.go:1614-1644`）让两网相邻先后布（DP 先，跨度 604 vs 723）；`buildPairField`（`router.go:1646-1685`）
  只在**对方已布铜的同一层**、距中心线约一个 pair pitch（17.7 mil）的一圈格点上把代价 ×0.55（`router.go:866-869`）。
  没有联合布线、没有同层约束、没有过孔对称（`daisyDiff = false`，`router.go:1990-1993`），每根网各自是一棵 Steiner 树；
  协商拆线（`router.go:1755-1766`）逐网重布，伙伴不跟随。`recouple`（`tune.go:334-392`）只在长度差 > max(4×限值, 40 mil)
  = 400 mil（USB2 限值 100 mil，`si.go:56`）时触发，本板长度差 14 mil，从不触发。
- 实测几何（fixed 与 reload 相差 ≤6 mil）：DP 863 mil 全 TOP、0 过孔；DM 877 mil（TOP 750 + BOTTOM 127）、2 过孔
  (918.9,423.5)/(874.1,314.8)；中心距 20 mil 内耦合仅 62–70 mil，且几乎都在 J2 的 USB-C 引脚区。主要分叉在 D3→U2：
  DP 从 J2.B6 出发沿 y≈177 在 J2 本体下北行，DM 从 D3.6 出发沿 y≈433 在 U2 本体下南行，两段相距约 256 mil，从相反两侧进入相邻引脚 U2.5/U2.6。
- 复现：同参数重跑结果一致。SI 报告：DM 2 过孔 / DP 0 过孔，长度差 14 < 100，**无 finding**，“high-speed”分 100；`PairTwist` = 0。
  偏置系数改 0.1 时首轮耦合明显更好且 DM 全在 TOP（说明全 TOP 解存在），但协商后耦合又掉回并把 DP 换到 BOTTOM；0.3/0.03 与默认基本相同。
- 根因（多因叠加）：① 只有软偏置；② 逐网协商拆线把耦合拆散；③ 没有检查耦合的判据——`CheckSI`（`si.go:263-281`）只看每网
  过孔数（≤2，DM 的 2 个过孔刚好通过）、长度、参考层与长度差，没有“耦合长度占比”和“两网过孔数差”，长度匹配但不耦合的
  对能通过；④ 布局把 D3 夹住：R3 本体与 D3 焊盘仅约 16 mil，西侧绕行需约 24 mil；R4 正好在 D3→U2 走廊（x 1014–1060、
  y 322–402）；`PairTwist`（`chains.go:453-482`，`placer.go:1864` 作布局罚分）用焊盘中心连直线判交叉，DM 那条线离 D3.3 中心约 4 mil
  擦过，判 0 交叉，但焊盘实际逼出过孔或绕行；⑤ ESD 回归测试（`esp32_esd_regression_test.go:13-18`）钉的是 esd-stub ≥ 89，
  当前布局正处于该钉值。
- 建议（未实施，属功能缺口）：先在 `CheckSI` 的差分对循环里加“同层耦合长度占比”（中心距 ≤ pitch+容差）与“两网过孔数差”
  finding，并用同一判据触发 `recouple`；回归测试：`esp32-v05-fixed.routed.json` → `FromSnapshot` → 带 intent 的 `Analyze` → `CheckSI`，
  期望出现 DM/DP 过孔不对称 finding 且耦合占比 < 0.15（今天两者都报干净）。更大的修法是差分对共享段联合布线（或一根被拆时
  伙伴一起重布）以及考虑焊盘尺寸与间距的 `PairTwist`。美观度分析对差分网整网豁免，所以这一问题不会在 R1–R9 里出现，必须由 SI 判据兜住。

### 6.1 后续（2026-09-30，v0.6.2 core 分支）

- **判据已补**：`CheckSI` 对每个差分对量 出线区外主体的同层耦合份额、每端出线区内未耦合长度（预算 = 类预算 × 该端器件数）、
  两根过孔数与层集合是否一致（`pkg/pcbauto/pairsi.go`；类限值在 `si.go` 类表，USB2 60 % / 250 mil）。本节的两份快照现在报
  `uncoupled`（307 mil > 250，U2 端）、`via-asymmetry`（2 / 0）、`layer-asymmetry`，回归 `TestPairSIFlagsUncoupledESP32USB`。
  主体为 0：这块板整对都在 J2+D3 与 U2 两端的出线区内。
- **布线已改为成对单元**（`pairroute.go`：领线留位、跟随线沿 pitch、过孔成对、协商成对拆布、收尾对齐到精确间距），并由流水线与
  逐根方式比较后择优（布通 → DRC → 电气组 → 高速/差分发现）。本板在当前布局下两种方式都耦合不起来——R4 坐在 D3→U2 走廊里、
  D3 的 DM 行要从 DP 穿通线下钻过——所以保留逐根结果（ESD 支线 68 mil、联合 92.6，与 dev 相同）。把 R3/R4 挪开的试验布局上，
  成对单元给出 0/0 过孔、D3→U2 全段贴线，余下的是 D3 行列方向造成的端部未耦合：**根因在布局**（走廊留空、ESD 穿通方向），
  属于布局器待办，判据会一直报出来。

## 7. 风格档（style profiles）与 auto 选档

风格只改**软目标**：美观组计划权重、组内逐项权重、对齐容差/目标格、是否要求对称、生成器可花的松弛预算。唯一真源
`pcbauto.AesProfiles`（`aesthetics_profile.go`）；`ParseAesStyle` 拒绝任何碰硬约束的键（clearance、width、neck、
current、via 尺寸、creepage、edge、drc、completion…，报错写明层级），并校验范围（计划权重 ≤ 0.30 等）。
Phase A 实际施加的 joint 权重仍为 0，档位权重只是计划值（Phase C 生效）。

| 档 | 计划权重 | 对齐容差 / 近失 | 目标格（占 P9 比例） | 对称 | 松弛 线长/面积/过孔 | 组内权重 |
|---|---|---|---|---|---|---|
| functional | 0.05 | 4 / 20 mil | 5 mil（0） | 平均 | 0 / 0 / 0 | P2 P4 P9 R5 R6 R7 ×0.5 |
| balanced（默认） | 0.10 | 2 / 15 mil | 25 mil（0.3） | 平均 | 2% / 0 / 0 | 全 1（= §2–§3 基线） |
| precision | 0.20 | 1 / 10 mil | 25 mil（1.0） | 最差组决定 | 5% / 3% / 4 | P1 P3 P9 R1 R2 R3 ×1.5，P4 ×2 |
| custom | 文件给定 | 文件给定 | 文件给定 | 文件给定 | ≤10% / ≤10% / ≤20 | P1–R9 各 0–3 |

同一块 K230：functional 55.8 / balanced 53.3 / precision 39.0（P9 72.1 / 51.3 / 2.6，P4 92.2 / 92.2 / 27.4）；
`TestAesProfilesOnlyTouchSoftObjectives` 断言三档下 joint 的 electrical / efficiency 组、Quality、Overall 完全相同。

**auto**：复杂度指数 = 0.2·器件数/300 + 0.15·网络数/250 + 0.2·引脚密度/(12 个/cm²) + 0.15·(层数−2)/6 + 0.1·高速
+ 0.05·高压 + 0.15·RUDY 最大利用率/1.5（各项封顶 1）；≥ 0.45 → functional，≤ 0.20 → precision，其余 balanced（初值）。

| 板 | 指数 | 选档 | 主要因素 |
|---|---|---|---|
| RK3568 | 0.85 | functional | 362 件、26 pin/cm²、拥塞 1.0 |
| K230 | 0.82 | functional | 405 件、53.8 pin/cm²、6 层 |
| SZPI ESP32-S3 | 0.64 | functional | 180 件、27 pin/cm²、4 层 |
| bbclaw | 0.37 | balanced | 69 件、8.7 pin/cm² |
| ESP32 样例 | 0.34 | balanced | 30 件、6.9 pin/cm²、4 层、USB |
| mipi-3in1 | 0.25 | balanced | 34 件、4.5 pin/cm²、MIPI 高速 |
| HV flyback（无 intent 读入） | 0.08 | precision | 36 件、1.3 pin/cm²、2 层；风格不能碰隔离/爬电，精修只花软预算 |

`pcb aesthetics` / `pcb auto run` 在 stderr 打印选档与理由，JSON `profile.auto` 与报告 6B 章同样记录。

## 8. 流程契约与“不改变既有输出”

- `TestFlowContract`（`internal/app/flow_contract_test.go`，在 `make test` 里，约 40 s）对 ESP32 与 HV flyback 跑完整离线链：
  sim power → intent derive → sim analog → pcb auto run --intent --sim → pcb check --intent → pcb rules（假宿主出计划）
  → sim post-layout → pcb aesthetics → report design，逐项断言：intent 网的 currentA/widthMil、净类与 HV 绝缘对；仿真轨；
  模拟块；plan 里 intent 来源的网、按电流定尺寸的过孔、intent 线宽落到铜皮、布线期 IR、板边铜距带、HV 隔离对/铣槽/隔离带
  （含 board.routed.json 的 slot/moat 图元）；check 摘要与 edge；rules 计划；设计后 IR 与热；报告 0–11 章（含 3A、6A、6B）
  齐全且顺序不变；且同一结果算 joint 时带与不带 aesthetics 完全相同。AGENTS.md 已写明：不得为让改动通过而削弱它。
- 与改动前（`857466b`）二进制同输入对跑（`scratchpad/flow/flow.sh`，脚本不入库）：sim.json、intent.json、analog.json、
  board.routed.json、playbook.json、preview.svg、check.json 逐字节语义相同；plan.json 只多出 `joint.aesthetics`，另 HV 路由
  触到 120 s 预算，协商迭代 32 vs 36（铜皮相同，时间相关）；post.json 仅一个 1e-5 W 的浮点求和尾数差；report.md 只多 6B 一章，
  输入摘要/哈希随 plan.json 字节变化而变。
- 顺带发现（未修，已另开任务）：`pcb auto run` 不带 `--place` 直接布 `esp32-v05/board.json` 在改动前后都会 panic
  （`router.go:835 nodeCong` 圆盘偏移未做网格越界检查）；契约测试因此对 ESP32 用已布局的 `esp32-v05-fixed.routed.json` 做 route-only。

## 9. 质量矩阵（2026-09-28，本机 Apple Silicon，顺序执行）

| 层级 | 命令 | 结果 |
|---|---|---|
| 单测 + 离线全链 | `make test`（含 `TestAes*` 单调性/对称/golden/对账/风格档、`TestFlowContract`、designreport 6B） | 通过（20 个包 ok）；另见下方 flaky 说明 |
| HV / HS（pkg/pcbauto） | `go test ./pkg/pcbauto -run 'HV|HS|SI|Iso…'`（非 short） | 16/17 通过；`TestHVFootprintRelief` 失败——在改动前 `857466b` 同样失败（20 s 墙钟预算、时序相关，`make test` 中曾通过一次），与本阶段无关，已另开任务 |
| 版图评分校准 | `make layout-calibrate` | 通过（九维与权重未变） |
| 协作入口 | `make agent-check`、`make skill-check` | 通过 |
| 5 板 fixture bench | `make fixture-bench`，分支 vs 改动前二进制同机背靠背 | 见下表 |

| 板 | 参考值 | 本分支 | 改动前 `857466b`（同机紧接着跑） | 迭代（分支 / 改动前） |
|---|---|---|---|---|
| mipi | 100 | 100.0 | 100.0 | 8 / 8 |
| bbclaw | 100 | 100.0 | 100.0 | 8 / 8 |
| szpi | 90.1 | 89.8（首轮 90.1） | 90.1 | 13 / 29 |
| rk3568 | 56.0 | 47.8（首轮 47.5） | 48.0 | 2 / 2 |
| k230 | 71.1 | 63.6（首轮 62.9） | 63.1 | 4 / 3 |

结论：本阶段不改任何生成路径（`Run()` 不调用美观度；joint 只在显式 `Aesthetics: true` 时附带报告块，权重 0），
分支与改动前在同一台机器上结果一致，差异只来自墙钟预算内的迭代次数。RK3568 / K230 的 56.0 / 71.1 参考值在今天这台
机器上**改动前也跑不出来**（4 分钟预算内只跑 2–4 轮协商），需在参考机器上重测或改为按迭代数限时，才能作为硬门槛。
`make stress-hv` / `make stress-hs`（internal/app，build tag，1–2 h）本阶段未跑：其 HV 链路由 `TestFlowContract` 的 HV
flyback 用例覆盖，且新代码只在 `pcb auto` 结束后附加只报告的 JSON 块。

## 10. 复现

```bash
go test ./pkg/pcbauto -run 'TestAes' -v            # 单调性 + 对称 + golden + 评审对账
pcbpilot pcb aesthetics --board pkg/pcbauto/testdata/esp32-v05-fixed.routed.json
pcbpilot pcb aesthetics --board pkg/pcbauto/testdata/esp32-v05-fixed.routed.json --no-exemptions --json
pcbpilot pcb aesthetics --board internal/app/testdata/boards/lckfb-k230-canmv.json --all
```
