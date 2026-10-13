# v0.9.2 第一阶段：布局与布线联合 —— 实验报告

日期 2026-10-12。分支 `exp/place-route`（基于 dev 72d3754d / v0.9.1），**只提交本地，未 push、未打 tag、未发 release**。
输入板：PicoRick_One_revA（只读复制到 `v092-work/`），intent = `intent-min.json`，豁免 = `w.json`（仅 --no-review）。
KiCad 10.0.7；fastroute 0.1.13；TraceMaker pcbpilot-b99f1a2。所有 kicad-cli / 布线调用均带 `perl alarm` 硬超时 + 独立进程组；重型布线同时 ≤ 2。

**先说结论**

1. 「布局太紧」只解释了**一部分**。被堵连接几乎全落在 RP2040 QFN-56（0.4 mm，U303）上，其出线口确实最紧；但把 U303 周边摆松（placer 重摆 / halo）或用便宜代理挑「最松」的布局，**没有一个候选在 fastroute 全量布线 + 全门禁下优于人工基线**（基线 5 未布通；候选 6 / 9 / 11 / 15）。
2. 一类失败与摆放无关：U303 上同网相邻脚的互连（+3V3 44–42/48、+1V1 50–45 / 23–50）在**每一次**实验里都出现，包括「U303 网络先布、整板空」的两阶段实验。这是 QFN 脚间桥接 / 逃逸桩问题，应在布线器或预置桩层面解决，而不是靠摆放。
3. 代理对「好/坏」有弱单调信号（代理 top3 平均 8.7 未布通，最差对照 15），但**不足以替代全量布线**，也没有产出胜过人工摆放的布局。**不建议把 place-race 产品化为默认流程**；建议产品化的是 E1 的度量（诊断用）和 E4 的锁定接口（见 §6）。
4. 门禁：**无任何变化**（未改任何 gate 代码、阈值、豁免；所有对比都在全部 gate 下进行）。

## E0 基线复核（v0.9.1，同输入，两路并行各跑一次）

| | fastroute | tracemaker |
|---|--:|--:|
| 墙钟（并行，互相争抢 12 核；上轮串行 380 s / 508 s） | 589 s | 564 s |
| 未布通 | **5**：+3V3 U303.22–C314.1；D6 IC101.19–U303.9；+1V1 C307.1–U303.23；USB_DM R304.2–U303.46；USB_DP R303.2–U303.47 | **2**：+1V1 U303.23–C307.1；U303.23–U303.50 |
| KiCad DRC 错误 | 9（4 clearance[Analog，IC101 旁 0.16–0.19 < 0.2 mm] + 5 unconnected） | 2（unconnected） |
| intent-widths | 0 | 9 段低于意图宽 |
| 走线 段 / 长度 / 过孔 | 1364 / 3473.8 mm / 151 | 1485 / 2787.7 mm / 236 |
| 红门禁 | kicad-drc, route-complete, post-layout-sim\*, silkscreen, board-manual\*, design-report, signoff, gate-set\* | 另加 intent-widths |

\* 输入缺 sim / 网表 / 手册备注导致的既有红项，与上轮一致，不是本实验引入。结论：与 v0.9.1 报告**逐项一致，可复现**（fastroute 与上轮逐字节相同的走线统计）。

## E1 诊断「布局太紧」是否主因

工具：`pcbpilot kicad channels --pcb … --summary route/summary.json`（离线，约 1 分钟；`internal/app/kicad_channels.go`）。
对每个连接焊盘，从焊盘中心沿 8 个方向放一条净宽走线，量：自由行程、行程末端能否放下过孔的方向数（viaDirs）、到异网铜的净空、周围 300 mil 方形内其它器件 courtyard 覆盖率（density）。

被堵连接 × 通道指标（fastroute 的 5 条 + tracemaker 的 2 条；`ch` = 过孔方向数 / 自由方向数 / 最长自由行程 mil）：

| 连接 | 受困端 | viaDirs / free / run | 净空 mil | density | 另一端 | 另一端 viaDirs | 两端距离 mil |
|---|---|--:|--:|--:|---|--:|--:|
| +3V3 U303.22–C314.1 | U303.22 | 2 / 2 / 76 | 1.8 | 0.21 | C314.1 | 8 | 61 |
| D6 IC101.19–U303.9 | IC101.19 / U303.9 | 2 / 2 / 120 | 1.8 | 0.15 / 0.12 | — | 2 | 421 |
| +1V1 C307.1–U303.23 | **U303.23** | **1 / 1 / 44** | 1.8 | 0.21 | C307.1 | 8 | 134 |
| USB_DM R304.2–U303.46 | U303.46 | 2 / 2 / 108 | 1.8 | 0.30 | R304.2 | 8 | 85 |
| USB_DP R303.2–U303.47 | U303.47 | 2 / 2 / 104 | 1.8 | 0.28 | R303.2 | 8 | 84 |
| +1V1 U303.23–U303.50（tm） | U303.23 / .50 | 1 / 1 / 44；2 / 2 / 44 | 1.8 | 0.21 / 0.24 | — | — | 272 |

被堵端焊盘 vs 其余 486 个连接焊盘的 AUC（0.5 = 无信号）：-bestRun 0.76、-freeDirs 0.73、-viaDirs 0.66、density 0.66、-minGap 0.63（fastroute，10 个正例）；tracemaker（3 个正例）0.81 / 0.75 / 0.72 / 0.63 / 0.67。

读法与限制：
- **相关但不充分**。受困端全是 U303 / IC101 的细间距脚，viaDirs ≤ 2，U303.23 是全板最紧的一个（唯一 viaDirs = 1），与「出线口被去耦件 C314 / R303 / R304 围住」一致；C314 距 U303.22 仅 61 mil。去耦电容那一端都很松（8 个方向），所以问题在 **IC 侧**。
- 但全板有 9–10 个 tight 脚，只有一个子集被堵，AUC 0.66–0.81（正例仅 10 / 3 个，**小样本**，不能当统计结论）。
- TraceMaker 自带 `tracemaker escape` 把 U303 的 6 / 7 / 51 判为「死脚」，这三脚实际**都布通**；被堵的 22 / 23 / 9 / 46 / 47 反而被判「能出线」。D6（IC101.19–U303.9）是「沿途被围」。说明**单脚逃逸可行性不是充分判据**，拥塞与同网桥接也起作用。

## E2 布局-布线联合（staged racing）

工具：`pcbpilot kicad place-race`（`cmd_kicad_placerace.go`）：N 变体 × M 种子 → 每个候选毫秒级代理（trapped = 无过孔方向的细间距 IC 焊盘数，tight = 仅 1 个方向，meanViaDirs）→ 排序（placer 合法优先 → 代理 → placer 分）→ 写前 K 个 + 一个最差对照。变体：`scratch`（从零摆）、`halo:40`（细间距 IC 周围加 40 mil keep-clear 重摆）、`refine`（从人工摆放出发局部优化）、`refine-halo:40/80`。5 变体 × 8 种子 = 40 个候选，放置 + 代理共 622 s（负载 ~15；代理本身每候选 ≈ 数百 ms–4 s）。

阶段 1（代理）：基线输入有 9 个 tight 脚、0 trapped；40 个候选里 13 个把 tight/trapped 都降到 0（代理在这里饱和，后续靠 placer 分打平）。
阶段 2（对前 3 + 最差做 fastroute 全量布线，同 intent / 豁免 / 全部 gate；两路并行，墙钟含争抢）：

| 板 | 代理排名 | placer 线长 in | 未布通 | KiCad DRC 错 | 走线 mm / 过孔 | 墙钟 |
|---|--:|--:|--:|--:|--|--:|
| **基线（人工摆放）** | — | 111.1 | **5** | 9 | 3473.8 / 151 | 589 s |
| cand-1 refine-halo:40#3004 | 1 | 113.1 | 9 | 29（20 clearance + 9 unconnected） | 4077.4 / 203 | 1017 s |
| cand-2 refine#2006 | 2 | 113.5 | 6 | 6 | 4137.4 / 230 | 1095 s |
| cand-3 refine#2007 | 3 | 124.7 | 11 | 11 | 4446.0 / 228 | 860 s |
| worst halo:40#1006（对照） | 40 | 185.9 | 15 | 15 | 5559.1 / 283 | 976 s |

红门禁：每块都至少 kicad-drc / route-complete 红（同基线）；cand-1 另有大量 clearance 错。未做 tracemaker 的 top-K 对照（时间；见「没做成的」）。

读数：
- 代理序与结果**弱单调**（top3 平均 8.7 → 最差 15），说明「出线通道」确有意义；但**top 的候选都比基线差**，说明现有 placer 在整体布线友好度上不如人工摆放，而代理只量 IC 脚局部，看不到拥塞 / 线长（cand-3 线长 124.7、走线 4446 mm 就差得多）。
- 同一类失败反复出现（U303 的 +3V3 / +1V1 同网桥接），摆放变化并不能消除它。
- 墙钟：全量布线 ≈ 15–18 min / 候选（并行 + 负载）。「先便宜代理再全量」的节省只在代理**真能淘汰**候选时成立；本次代理没能挑出更好的，因此没有实际加速收益。

## E3 零移动对照（只改顺序 / 优先级）

fastroute 没有逐网优先级旋钮，我用「分阶段」实现最强的顺序改变：同一摆放、同一 DSN，先**只布 U303 的 45 个网络**（其余类 `ignore_net_classes`，空板），再把这些铜冻结为固定铜，布其余（`kicad stage-dsn` + `lock-dsn`）。

| 阶段 | 墙钟 | 未布通 |
|---|--:|--|
| 1：U303 的 45 网（除 GND）空板 | 131 s | **4**：+3V3 U303.44–48；XOUT R305.2–U303.21；+1V1 U303.50–45；+1V1 U303.23–C307.1 |
| 2：冻结 566 段 / 62 过孔后布其余 | 336 s | 总计 17（4 条 U303 老问题原样 + GND 7 条 + 其它 6），无新增固定铜违规 |

结论：**先 fanout 后其它救不回 U303**：U303 在整板空着、网络最少时仍有 4 条布不通；整体还更差（17 vs 5；两阶段实验在 fastroute 里直接跑、没有经过 pcbpilot 的后处理与铺铜，GND 的 7 条未布通是否会被后续铺铜覆盖**未验证**，所以总数不能与基线逐条比较；可确定的只有 U303 的 4 条仍在）。这把「摆放」与「顺序」分开了：顺序不是主因；同网桥接是布线器 / 逃逸桩问题。TraceMaker 侧未单独做顺序实验（其 `--first-nets` / `--escape-plan` 需 pcbpilot 透传参数，未实现）。

## E4 分层锁定可行性（调研 + 最小验证）

| | fastroute 0.1.13 | TraceMaker b99f1a2 |
|---|---|---|
| 机制 | DSN `(wire … (type fix))` / `(via … (type fix))` = 固定铜；`--initial-session` 从旧会话继续（可被 rip-up，**不是**锁定）；`--router.autorouter.ignore_net_classes` 只布选定类 | KiCad 板上已有铜 = 障碍；`locked` 的轨/孔 = 固定；未锁的「foreign unfixed copper」在 R2 协商 rip-up 中可被穿越重布；`--first-nets` + `--priority-order-only` 控制顺序；`route` 只布未连接；`--reroute` 才清除未锁铜 |
| 最小验证 | `lock-dsn`：从阶段 1 会话冻结 566 段 / 62 过孔写成 `(type fix)`；阶段 2 的 fastroute 报告里固定铜违规仍是原有 33 条（无新增），阶段 1 的 4 条 U303 失败原样存在 → 固定铜被当作不可动障碍 | `net-copper`：在 TM 已布板上删 4 网络（+1V1、USB_DM/DP、D6）、锁其余 1227 项，`route` 200 s：保留网络 **0 丢失 / 0 改动**，仅被删 4 网络新增 14 项；不锁对照结果相同（本场景不需 rip-up，**未能区分锁与不锁**） |
| 限制 | 固定铜不会写回 SES，必须由 pcbpilot 在导入后把阶段 1 的铜补回板（v0.9.1 逃逸桩已有 `addEscapes` 同款路径，但**尚未接到分层流程**）；类名需拆分（`stage-dsn` 已处理）；`--no-neckdown-classes` 要同步加 `__first` 类 | 锁定由 KiCad `locked` 标志表达；「先锁电源 / 差分 / 时钟再布其余」可用 `net-copper` + `route`；拆分布线需先把其余网络的铜删掉，TM 没有「只布这些网」开关（`--first-nets` 只是顺序）；它的 R6「割线容量证明 → 摆放 ECO」设计文档有，CLI 有 `--cut-report`，未接入 |
| 建议 | 接口已验证可行；作为「大块分层」手段值得做，但对 PicoRick 的 U303 问题无增益（E3） | 同左 |

## 遇到的问题 → 根因 → 解决 → 验证

1. **同时启动两个 `kicad route`，其中一个 1 秒失败**（`rename …bridge-….py.tmp: no such file`）。根因：`internal/kicad/kicad.go bridgeScript` 用固定 `.tmp` 文件名抽取内嵌 bridge.py，并发进程 rename 互相抢。解决：`os.CreateTemp` 唯一名。验证：新增 `TestBridgeScriptConcurrentExtraction`（16 并发；旧代码必红，新代码绿，`-race` 通过）；cand-1 重跑成功。这是 v0.9.1 「both」/并行测试都可能踩到的真实缺陷。
2. 代理在候选间饱和（13 / 40 个得 0 分）→ 只能靠 placer 分打平；改进方向见 §6，未在本阶段做。
3. 并行跑两路布线使 E0/E2 墙钟不可与上轮串行基线直接比（已在表中标注，未虚报加速）。

## 没做成的部分

- E2 的 tracemaker 全量对照（只跑了 fastroute；两路各 K+1 次布线按 ≥ 15 min 估算过长）。
- TraceMaker 的顺序实验（缺参数透传）；E3 只覆盖 fastroute。
- 把阶段 1 铜补回板、对「锁定后」的最终板跑全部 gate（只在路由器层面验证了锁定语义，没有生成可过 gate 的成品板）。
- 其它板的泛化：全部数据来自 PicoRick 一块板，E1 AUC 样本很小。
- 全量 `go test ./...`（不带 -short）未跑，见下。

## 是否建议产品化

| 项 | 建议 |
|---|---|
| `kicad channels` / escape 度量 | **建议保留为诊断**（只读、不影响门禁）；把被堵连接的「受困端 + 通道指标」写进 route 报告，替代/补充现有 placementHints |
| `kicad place-race` | **不建议默认产品化**：本实验未产生优于人工摆放的布局，代理会饱和。若继续：把代理扩展到拥塞 / 线长（RUDY、割线容量）并加「保持人工摆放，仅调 U303 周边 ≤ N 个器件」的 `Only+Refine` 变体，再评估 |
| 分层锁定（`lock-dsn` / `stage-dsn` / `net-copper` / `LockWiring`） | **接口建议保留**为实验命令；真正产品化需把阶段 1 铜补回板并跑全部 gate |
| 真正的下一步 | 针对 U303 类 QFN 的同网相邻脚桥接 / 预置逃逸桩（E3 证明与摆放、顺序无关）；这在布线器层面（fastroute 逃逸桩规划、TraceMaker `--escape-plan`）比再做摆放搜索更有可能奏效 |

## 门禁变化

**无。** 没有修改任何 gate、阈值、豁免规则；`route-complete` 仍是 v0.9.0 严格口径（未启用 `--pour-credit`）；所有候选与基线在同一组 gate 下比较，全部红项如实列出。

## 代码与测试

提交（本地 `exp/place-route`）：`kicad channels`、`kicad place-race`、`kicad lock-dsn` / `stage-dsn` / `net-copper`（均为实验命令）；`specctra.LockWiring`、`SplitClassesForStage`；bridge `removeNets` / `lockNets`；bridge 并发抽取修复。
新增单测：`kicad_channels_test.go`（开阔 / 被围 / 同网不阻挡 / 密脚判定 / AUC / 变体解析）、`specctra` 的 `TestLockWiring…` 与 `TestSplitClassesForStage…`、`TestBridgeScriptConcurrentExtraction`。
相关包 `go test -short`（internal/kicad、internal/pcb/…、internal/app/…、pkg/pcbauto/…）结果见下节「测试结果」。

原始数据：`~/fpga-remote/pcbpilot-handoff/v092-work/{e0,e1,e2,e3,e4}`（含各 summary.json、place-race.json、channels-*.json）。

## 测试结果

`go test -short -count=1 ./internal/kicad/... ./internal/pcb/... ./internal/app/... ./pkg/pcbauto/...`：**全部通过**（internal/kicad 17.9 s、internal/pcb/specctra 0.4 s、internal/pcb/svgimport 0.8 s、internal/app 288.8 s、pkg/pcbauto 187.6 s，0 失败）。
**未跑**：不带 `-short` 的完整 `go test ./...`（含 KiCad 在线测试、TestFixtureBench 等，约 55 min）、npm / ctest——本阶段只改了 Go 实验命令与 bridge，且需与布线实验错开，留给产品化前统一跑。
