# v0.9.2 第三阶段报告：单引脚逃线预置（escape-stubs）

分支 exp/place-route，仅本地提交，未 push / tag / release。所有硬门禁未放松；唯一豁免仍是第一阶段的 `--no-review`。

## 做了什么（Q1）
`pcbpilot kicad escape-stubs --pcb X --intent I --rip-up [--ref U303] [--max-via-dirs 2] [--min-run-mil N] --out Y [--plan p.json] [--dry-run]`
（`internal/app/kicad_escapestubs.go`、`cmd_kicad_escapestubs.go`，3 个单测；`bridge.py addVias` 新增 `locked`）。
- 选 tight 焊盘：细间距 IC 上、非 GND、≥2 焊盘的网的焊盘，`kicad channels` 的 viaDirs ≤ 2（或 best run < 阈值）。
- 每个焊盘试 8 方向，每向取最短可行「桩+过孔」，按（净空余量 + 远离器件体 − 长度）选向；最受限者先放（每轮重算剩余可行方向数，已放的桩算铜）。
- 桩宽 = 意图宽，受异网间距缩颈，不低于意图 min（+1V1/+3V3：15.75→11.4 mil）；过孔 = 板规则过孔，环净空 ≥ clearance+0.25 mil，避开同网 SMD 焊盘，孔到孔 ≥ max(过孔径, 钻孔+9.85 mil)；放不下则跳过并写原因。仅桩+过孔被锁定。
- 阻焊桥接只由铜间距间接保证，未单独建模。
- PicoRick 裸板：全 IC 73 桩 / 3 跳过（U303.22、U303.24、U303.55；22 被相邻 23 的桩占掉空间）；仅 U303：38 桩 / 3 跳过。规划约 50 s。

## 对比（Q2）：PicoRick_One_revA，`--rip-up --no-review --waivers w.json`，每项 1 次
| 运行 | 未布通 | KiCad DRC 错误 | DRC 警告新增项 | 墙钟* |
|---|---|---|---|---|
| fastroute 无桩（阶段二基线，已确定性） | 5 | 9（clearance 4 + unconnected 5） | — | 430s |
| fastroute 全 IC 桩（73） | **5** | **5（unconnected 5，clearance 0）** | via_dangling 17，hole_to_hole 2，track_dangling 1 | 494s |
| fastroute 仅 U303 桩（38） | 5 | 6（clearance 1 + unconnected 5） | via_dangling 10，hole_to_hole 2，track_dangling 1 | 419s |
| tracemaker 无桩（本阶段重跑） | 2 | 2 | — | 494s |
| tracemaker 全 IC 桩（73） | **19** | 19 | via_dangling 69 | 747s |

*二丫机器负载不同，只看量级。tracemaker 无桩重跑与阶段二首跑一致（同样 U303.23→C307.1 / 23→50 两条）。

被堵连接：
- fastroute 无桩：+3V3 22→C314.1、D6、+1V1 23→C307.1、USB_DM、USB_DP。
- fastroute 全桩：+3V3 43→42、22→C314.1、48→44、33→C312.1，SWCLK 24→J104.2。
- fastroute U303 桩：+3V3 42→44、C314.1→22、48→44、C312.1→33，SWCLK 24→J104.2。
- tracemaker 全桩：19 条（+3V3 22/48/10/49/33、+1V1 23、XIN/XOUT、QSPI_SD0/3、D3/D1/D5/D8、SCLK、SWCLK、GPIO20）。

门禁：四个新运行与基线一样，kicad-drc / post-layout-sim / route-complete / silkscreen / board-manual / design-report / signoff / gate-set 全部 FAIL，无一整板通过；tracemaker 两个运行另有 intent-widths FAIL，fastroute 的 intent-widths PASS。

## 结论（Q3）：未满足「减少未布通且不增加 DRC 错」，如实记为**无效**
1. fastroute 全桩：旧目标确实被解开——U303.23(+1V1)、U303.46/47(USB)、U303.9(D6) 全部布通，DRC 错误 9→5，clearance 4→0。但未布通总数仍是 5：失败点移到 +3V3 的 42/43/44/48/33，另有未用桩过孔的 via_dangling 警告 17 个。按判据不算改善（未布通未减少）。
2. 仅 U303 桩没有更好：clearance 1 个新错（在 IC102 附近，远离桩），未布通仍 5。
3. tracemaker 明显变坏（2→19）：锁定的 73 个过孔和短桩占掉全板通道，tracemaker 对固定铜的绕行能力弱。
4. 原因分析：
   - U303.22/24 本身就放不下桩（相邻 23 的桩把空间用掉，24 被 U303.32 挡住），所以这两条被堵连接没有被桩解决——22→C314.1 在所有 fastroute 运行里都未布通。
   - 桩解决了「单个引脚没通道」，但把 +3V3 的 42/43/44/48 周围的通道也锁死了；锁定铜是对整板求解的扰动，失败位置被挪动而不是消除。
   - 默认阈值 viaDirs≤2 在裸板上选中 73 个焊盘（含与失败无关的 IC101/102），规划器看不到「哪些焊盘真被堵」。
5. 局限：单板、每配置 1 次（fastroute 已知确定性，tracemaker 无桩重跑一致）；DRC 错误数下降（9→5）是真实但小的收益，且来自 clearance 变少，可能是搜索扰动而非桩的功劳，不足以支持产品化。

## 建议
不产品化为默认 pre-route 步骤；`kicad escape-stubs` 保留为实验命令。若继续：(a) 只对「上一轮已知被堵」的焊盘生成桩（闭环，而非按通道度量全选），(b) 桩不锁定、只作为布线器的起点提示，(c) 对 +3V3 这类同网多焊盘组让布线器识别整组，而不是逐脚预置。
