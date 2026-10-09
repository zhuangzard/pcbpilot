# KiCad 全功能对等（parity）计划

用户决定（2026-10-09）：设计全部在 KiCad 完成，EasyEDA 只作为提交工具；**pcbpilot 的所有功能都必须能在 KiCad 上运行**，
硬门禁（意图、仿真、ISO/医疗安全、丝印、说明书、报告、多代理设计评审）一个不少。本文件是对等矩阵与实施顺序，
一项功能只有在 KiCad 上实跑通过（Gas Module V5 或 PicoRick 实板）才标“完成”。

## 架构

pcbpilot 分两层：

1. **引擎层（与 EDA 无关）**：原理图布局规划/美观度/块复用、意图推导（含 pkg/safety 的 IPC-2221B、IEC 60664-1、62368-1、
   IEC 60601-1 MOOP/MOPP、61010-1）、仿真（power/analog/post-layout）、布局引擎 pkg/pcbauto、门禁计算、报告、说明书、
   设计评审（review-panel）。输入输出都是 pcbpilot 自己的 JSON（board snapshot、connectivity、intent、sim）。
2. **适配层**：读写 EDA。EasyEDA 走连接器动作协议（`pcb.*`、`schematic.*`、`document.*`…）。

KiCad 对等 = **为同一动作协议实现 KiCad 后端**（`--backend kicad`），而不是另写一套命令：

- PCB：pcbnew Python（文件级，稳定、无现场依赖）为主；需要现场 GUI 时用 KiCad 10 IPC API。
- 原理图：KiCad 10 的原理图没有可用的 Python/IPC 写接口 → 直接读写 `.kicad_sch` S-expression（已有 `internal/kicad` 解析器）；
  `kicad-cli sch erc/export netlist` 做回读和检查。
- DRC/ERC：`kicad-cli pcb drc` / `sch erc`（JSON）。
- 器件：LCSC 号搜索 + `kicad lcsc --import`（嘉立创 EasyEDA 器件 → KiCad 自带导入器）。
- 提交：`pcbpilot project import`（KiCad 工程 → EasyEDA 新工程）。

## 对等矩阵

| 能力 | EasyEDA 实现 | KiCad 实现 | 状态 |
|---|---|---|---|
| 原理图页面大小自适应 | sch_page_fit | `kicad sch-fit` | 完成（单测 + 模板实测） |
| 原理图读取/连接性 | schematic.* 动作 | `kicad netlist --sch X.kicad_sch --out conn.json`（kicad-cli 网表 → connectivity 1.4，intent derive / pad-net-diff 直接读） | 完成（分支 kicad/sch；Gas A：intent derive 与 V5 报告 intent.json 一致，仅差 pre-connectivity 人为删掉的 Q1–Q5 栅极） |
| 原理图写后端（`internal/kicad/schwrite.go`） | 连接器写入 | 放置库符号实例、导线、结点、标签（局部/全局/层次）、电源符号、非连接标志、文本/矩形、设字段、改位号、移动；拼接写入，其余文本逐字节保留，写后重解析 | 完成（单测 + kicad-cli 网表回读） |
| `sch place / wire / netflag / no-connect / modify` | 连接器写入 | 同一命令加 `--backend kicad --kicad-sch X`（坐标为 KiCad mm、y 向下；`--symbol Lib:Name`、`--through REF.PIN`、`--at REF.PIN`） | 完成（分支 kicad/sch） |
| `sch autoconnect / connect`（引脚短桩 + 电源/地/端口/标签） | 连接器逐脚 connect_pin（每脚约 4 s，常见“connector did not respond”） | 同一命令加 `--backend kicad --kicad-sch X [--fit]`：同一规划器读 `.kicad_sch` 几何（`internal/kicad/schscene.go`），全部短桩与标记一次写入；写前在项目副本上跑 kicad-cli 网表，每脚落在计划网络且其他网络不变才替换原文件，否则非零退出、原文件不动 | 完成（分支 kicad/sch-auto；单测 + kicad-cli 网表回读；3 脚批次约 0.4 s） |
| `sch layout-plan [--zones]` 落地 | 连接器 Apply 队列（188 步级；zones 85–111 s） | `--backend kicad --kicad-sch X [--fit]`：按位号（多单元用 `REF:UNIT`）从 KiCad 图重测后规划；整组锚点在核心处或按评分（与不动器件碰撞、图签禁区、页边）选最近的干净偏移；导线岛整体随动（桩、标签、电源符号刚体移动），其余碰到被移引脚的网在 1.27 mm 栅格上正交重布（A*，绕本体/字段/标签/文字/图签，不沿外网导线走、不碰外网引脚/线端，T 处加结点，引脚沿外向出线），线上局部标签放回新树，布不通的网改为引脚上的网络标签；`--zones` 把区框按行排进页面（`--fit` 选最小纸张）、画虚线框+区名（替换旧框）；写前严格门禁 + 网表一致 | 完成（分支 kicad/sch-quality；Gas A 实页 group-move 实测） |
| `sch group-move` / `sch destagger` / `sch titleblock` | 连接器（group-move 曾“报失败不回滚”F13、destagger 共线短路 F14） | `--backend kicad`：group-move `--refs R1,U1:2 --dx --dy`（mm）整体事务；destagger 只改单脚直桩的方向/长度，严格减少发现且不新增；titleblock `--title/--rev/--date/--company/--comment N=` 或 `--data` | 完成（分支 kicad/sch-quality） |
| **一次调用画原理图**（替代 S0–S6 约 80 步） | Agent 逐步 `sch place / autoconnect / layout-plan / zone-arrange / titleblock / check / gate / save` | `pcbpilot kicad sch-build --spec design.json --out DIR`（spec：器件/网/电源轨/块/分页分区/标题栏/意图，**无坐标**）：LCSC 符号缓存 + 并行导入、块展开（可读内部网名、焊盘别名）、区内布局（布局器 + 确定性网格：去耦竖排靠电源脚、对接短导线、引脚空间预留、autoconnect 批量落桩）、按 sch-check + 工程级规则选最干净的方案、电源朝上地朝下、层次图（层次标签 + 根图 sheet pin）、标题栏、PWR_FLAG、sch-fit、库表；门禁：网表 == spec、ERC、sch-check 质量、工程级（EG-01/05/06/08/09/13/20/22）全部硬性且事务化（失败不写）、connectivity.json + intent derive、可选 review-panel；`--from-connectivity` 迁 EasyEDA 设计；`kicad sch-edit`（增量，只重排受影响的区，保 uuid）、`kicad sch-read`（紧凑 JSON + bbox）、`kicad sch-checkpoint list/restore`；文档 [sch-build.md](sch-build.md) | 完成（分支 kicad/sch-build；ESP32-mini 31 件 / 22 网：全部门禁通过，冷启动约 8 s、缓存命中 1.3–7 s；单测：小 spec 往返、编辑/检查点、恶意 spec 路径、EasyEDA IR 迁移） |
| 原理图美观度/块复用（sch aesthetics/block-apply…） | 连接器写入 | 接到上面的写后端 | 待做 |
| 旧原理图迁移 EasyEDA → KiCad | — | `kicad sch-import --epro X --board B --out DIR [--pcb board]`（每页一张子图 + 层次根图；符号/单元/图形、位号、值、封装、LCSC、导线、电源、网络端口、非连接；导出后自动与 EasyEDA 连接性逐网逐脚比对） | 完成（分支 kicad/sch；Gas A/B：224/224 网络、686 脚一致，与板焊盘 129/129 网络同名一致；KiCad 自带 EasyEDA Pro 导入器只能在 GUI 中用，kicad-cli 不能读 .epro） |
| 原理图检查（sch check/gate/ERC） | 连接器 + 自检 | `pcbpilot kicad sch-check --sch root.kicad_sch [--fix-pwr-flag] [--ignore KIND]`：kicad-cli ERC（JSON）+ pcbpilot 质量检查（符号重叠、导线穿本体、斜线/离栅格/重叠导线、线端或引脚落在导线中段（KiCad 不连）、标签/字段文字压物、标签压线、引脚压标签、图签/出页），JSON 报告，ERC 错误或任一发现即非零；`--fix-pwr-flag` 每个未驱动电源网接一个 PWR_FLAG（逐页门禁 + 网表校验） | 完成（Gas A 导入：10 条 power_pin_not_driven → 0 ERC 错误；剩 199 条封装库未登记警告、92 条美观发现是导入页现状） |
| 意图推导（intent derive，含安全表） | 离线 | 离线（输入改为 KiCad connectivity） | 进行中 |
| 仿真 power/analog/post-layout | 离线 | 离线（board-final.json 来自 KiCad） | 进行中 |
| 布局（pcb auto run / place） | 连接器剧本 | `kicad place`（pcbnew） | 已实现，紧凑度/居中待调 |
| 布线（fastroute + 意图规则 + 宽度/过孔阵/IR 闭环） | 连接器 + dsn-fix/ses-repair | `kicad route`（原生 DSN/SES） | 进行中（kicad/core；kicad/pcb-fixes 修了实板缺陷，见下） |
| 铺铜/电源平面 | pcb.pour.* | pcbnew ZONE + ZONE_FILLER | 进行中 |
| 全部硬门禁（DRC、意图、安全/隔离、仿真、丝印、说明书、报告、设计评审） | runQualityGates | 同一门禁代码跑在 KiCad 快照上 | 进行中 |
| 发布签核 `pcbpilot signoff` | `pcb auto route` 末尾自动运行（设计报告 + 签核，kicad/pcb-fixes） | `kicad route` 末尾自动运行 | 完成（两端同一 `signoffGate`） |
| 丝印位号（silk-align --tight、组标签） | pcb.silk.* | `kicad route` 内同一规划器（planSilkTight + 组标签）经桥接写字段位置/字号/可见性 | 进行中（规划器改进见下；独立 `kicad silk` 命令未做） |
| 制造输出（Gerber/钻孔/BOM/CPL，LCSC） | EasyEDA 下单 | `kicad fab` | 完成（分支 kicad/fab） |
| 器件搜索与建库（LCSC） | lib by-lcsc | `kicad lcsc --search/--import` | 完成（分支 kicad/fab） |
| 设计评审（Codex/Kimi/Claude） | review-panel | 同（与 EDA 无关） | 完成 |
| 说明书 / 设计报告 | report manual/design | 同（输入来自 KiCad） | 进行中 |
| 提交到 EasyEDA | — | `project import` | 已实现，待新连接器实测 |
| 旧工程迁移 EasyEDA → KiCad | — | `easyeda2kicad_board.py`（焊盘拟合放置，LCSC 字段） | 实测通过（Gas A/B 199 件，≤0.062 mil） |

## 实施顺序

1. 合并 kicad/core、kicad/place、kicad/fab，`kicad route` 在 Gas A 上全部门禁实跑。
2. 原理图写后端（`.kicad_sch`）：先 Gas 原理图迁移（EasyEDA 原理图 → KiCad），再把 sch place/wire/layout-plan/aesthetics/
   block-apply 接到 KiCad 后端；`kicad-cli sch erc` 作为原理图门禁。
3. 丝印、说明书、报告在 KiCad 快照上全部跑通；`project import` 实测。
4. v0.9.0 发布：以本矩阵全部“完成”为前提。

## 原理图写入的事务与门禁（分支 kicad/sch-quality）

每个多对象 KiCad 写入（autoconnect、connect、layout-plan、zones、group-move、destagger、PWR_FLAG）都是一次事务：
先在内存中生成计划页 → **严格门禁**（`kicad.GateSchematic`：计划页的每条质量发现都必须是原页已有的，按键计数；
新增任何一条即拒写）→ 在项目副本上跑 kicad-cli 网表，与预期一致（移动类：分区不变、网名不变，仅布不通改标签时
允许 `Net-(…)` 自动名改名并上报）→ 原子替换文件。任何一步失败：非零退出，原文件一字节不动（F13/F14 的“报失败但
已写坏”在 KiCad 后端不存在）。KiCad 10 连接语义实测：引脚或线端落在导线中段**不连**（需结点），标签落在线中段连。

### 单项耗时（`go test ./internal/app -run TestKicadSchOpTimings -v`，rich fixture，M 系列 Mac，负载均值 ~27）

| 操作 | KiCad 后端 | 连接器旧数 |
|---|---|---|
| kicad-cli 网表导出 | 0.4–0.6 s | — |
| autoconnect 2 脚（一次校验写入） | 1.3 s | ~4 s/脚 |
| group-move 刚体 / 需重布 | 0.8–1.6 s | — |
| layout-plan --zones 落地（2 区） | 1.3 s | 85–111 s |
| titleblock / destagger 规划 | <10 ms | — |
| sch-check --fix-pwr-flag（fixture，2 个旗） | 3.7 s | — |
| sch-check --fix-pwr-flag（Gas A 5 页，10 个旗） | 24 s | — |

时间几乎全在 kicad-cli（每次写入 2 次网表导出；ERC 约 1.3 s/次）。

## sch-build（分支 kicad/sch-build）尚未完成

- 布局器对大核心常无解，这些区走确定性网格方案；区内“输入左、输出右”未强制（只按功率/信号流给区排序）。
- 视觉评审（PNG → review-panel）未接：review-panel 只读文本证据。
- 层次图 sheet pin 只按页序分左右；多单元符号只走网格；KiCad `extends` 派生符号不支持。

## 原理图（分支 kicad/sch）尚未完成

- `--backend kicad` 还没接的命令：`sch disconnect`、`autoconnect --replace/--all-pages`、`block-apply`、
  `zone*`（zone-arrange 的 KiCad 落地走 `layout-plan --zones`）、`page-new/rename/delete`（新增子图）、`prim-delete`（删除图元）、`rebind-*`、`replace`、
  `no-connect --clear`。
- 重布路由：线上非引脚处的电源符号/全局标签当作固定端点（会拉回原位置）；Prim 顺序 + 单网 A*，不做全局交叉最小化；
  `--zones` 不处理 `embedIn` 子区、多页；字段文字框为估算（0.8 em/字）。
- 写后端：`extends` 派生库符号、按 EasyEDA 单位（10 mil、y 向上）输入坐标。
- `kicad sch-import`：镜像元件的方向只按 KiCad 变换搜索（Gas 工程里没有镜像件，未实测）；不导入总线、图片、表格；
  字段对齐方式未迁移（位置照搬）；PWR_FLAG 由 `kicad sch-check --fix-pwr-flag` 补，sch-import 本身不加。
- `kicad netlist` 与 kicad/core 分支的同名命令重叠，合并时保留一个（字段形状相同：connectivity 1.4）。

## `kicad route` 实板缺陷修复（分支 kicad/pcb-fixes，2026-10-09）

在 Gas V5 A / PicoRick 草稿副本上跑 `kicad route` 发现的共用代码缺陷，均已修复并有单测：

| # | 缺陷 | 修复 | 位置 |
|---|---|---|---|
| 1 | 设计后仿真误报「no copper path」：过孔环只压焊盘边约 0.5 mil，0.5 mm 栅格看不到，KiCad 判连通 | 栅格化前按精确几何（矩形/跑道形焊盘、胶囊形走线、过孔圆）把相接的焊盘/过孔/走线并入同一节点；0.5 mil 缝隙仍判开路 | `pkg/postsim`（共用） |
| 2 | 仿真误开路挡住 IR 收敛 | KiCad DRC 对该网无 unconnected 的开路标 `SIM/KICAD MISMATCH`（`summary.simKicadMismatch`，门禁仍失败），IR 收敛照常；同网开路不再掩盖超预算压降 | `kicad route`、`irOverBudget`、`pkg/postsim` |
| 3 | fastroute 报告坐标在 KiCad DSN 下是 mm，却按 mil 标注 | 按 DSN 分辨率单位换算（`specctra.ReportMilPerUnit`：mil→inch、um→mm），fixableList 与自动逃线坐标同用 | 共用 |
| 4 | 密板丝印规划失败、每轮约 12 s | 八方位 × 横/竖 + 封装内居中；盖阻焊过孔作为第二档；无位时降到 JLC 最小 0.8 mm / 0.15 mm（`--silk-min-font`）；组标签也可降号；一次最多挪开 3 个挡位标签；网格索引 + 每标签静态合法位缓存。门禁仍硬：字号低于 min(项目字号, 最小字号) 失败 | `pcb_silk_tight.go`（共用） |
| 5 | EasyEDA `pcb auto route` 末尾没有签核 | 末尾发布设计报告并运行同一 `pcbpilot signoff`（`--review` 或 `<out-dir>/review-design/review.json`） | `pcb auto route` |
| 6 | DSN 板边禁布带挡住板边连接器焊盘 | 板边安装件（铜到板边门禁的同一判据：封装框到达板框）的近边焊盘在禁布带上开全深度窗口（焊盘 + 向内直出逃线，外扩间距 + 2 mil）；其余铜照常禁布，门禁不变 | `specctra.EdgeBandsExcept`（KiCad 与 EasyEDA 共用） |
| 7 | fastroute 多次运行结果不同 / 太慢 | 同 DSN + 同参数结果逐字节相同；fastroute 自己并行跑 multi-start 变体，不再叠第二层并行：`--multi-start`（默认 4）成为 pcbpilot 设置；优化器默认关（`--optimizer`），PicoRick 299 → 134 s、同为 4 未布通；`--diagnose` 全部 blocked 时不续跑，route-complete 写明需改摆放/逃线 | `kicad route`、`runFastroute` |
| 7a | 主运行单线程过慢 | `--threads` 缺省 min(核数−1, 8)（8 线程 3 次结果逐字节相同）；`--threads 1` 可复现 | `kicad route` |
| 7d | 门禁读 fastroute 专有字段 | 路由器无关 `routeResult`（`route_result.go`），route-complete（含铺铜放宽）只读它；fastroute 为唯一实现，留给第二后端 | 共用 |
| 7b | fastroute 微颈缩可收窄任意拥挤段 | 高于全局下限的所有意图网类进 `--no-neckdown-classes` | `forbidsNeckdown`（共用） |
| 7c | 非 fastroute 路径缺门禁 | `route-complete` 对外部 `--router`、`--router internal`、`pcb gate` 用板铜连通性判；`pcb auto route` 加 design/layout 评审；两条路径以 `gate-set` 收尾，强制门禁集合缺一即失败（`TestRouteGateSetContract`） | `route_gate_set.go`、`pcb auto route` |
| 8 | 计时 | `summary.timings` 保留；前后对比见 `.agents/skills/pcbpilot/references/kicad.md`「计时与提速」 | — |

## 意图网焊盘逃线（分支 kicad/escape-stubs，2026-10-09）

所有意图网类进 `--no-neckdown-classes` 后，满宽走线出不了细间距焊盘（PicoRick RP2040 U303 QFN-56 0.4 mm，
+3V3 / +1V1 15.75 mil：11 条连接 blocked）。不重开颈缩，改为**布线前预布固定逃线**（`planIntentEscapes`，
`kicad route` 与 EasyEDA `pcb auto route` / autoroute 共用；EasyEDA 原有 GND 预逃线照旧，已有逃线的焊盘跳过）：

- 候选：意图网的 SMD 焊盘，`widthMil.min < outer`，满宽从焊盘中心直出到封装 courtyard 外不满足间距。
- 宽度：在 DRC 同一几何（焊盘矩形、走线胶囊、过孔圆，间距 + 0.2 mil 布线余量，取两网 netclass / 意图间距较大者）下
  能放下的最大值，上限满宽，**不低于 widthMil.min**。
- 形状按序尝试：**外出**（从焊盘外端内缩 w/2 起，直出到满宽线端——按方形核，包住 fastroute 的八边形端帽——
  第一次放得下处再 +1 mil，否则到 courtyard 外）；**同网桥**（外出被占时，与同排相邻同网焊盘在内端相连：RP2040 43–44、48–49）；
  **内收 + 过孔**（两者都不行时向封装体内到过孔，过孔离开同网 SMD 焊盘一个间距——fastroute/Freerouting via-at-SMD 规则）。
- 有同网邻焊盘的候选先规划；后面的候选先占一条 widthMil.min 的外出预留，0.4 mm 间距上 +3V3/+1V1 相邻不会互相挤掉。
- 逃线以 `(type fix)` 写入 DSN（`specctra.AppendEscapes`：由 FixDSN 推广，mil→DSN 单位换算，带引号的过孔名）；
  fastroute 不把固定走线写回 SES，KiCad 在 SES 导入后由 `addEscapes` 把桩与过孔加到板上（`summary.intentEscapes` /
  `intentEscapesAdded`）。实测：fastroute 把落在焊盘内的固定走线端点按焊盘中心建模（比实板保守，报为
  pre-existing unfixable）；满宽线端在圆核刚放下处（离焊盘端 8.5 mil）插不进，到方核位置（约 15 mil）可布通。
- `intent-widths`：窄于全宽的段只有**整段**落在本网某焊盘铜 max(3×宽, 30 mil) 内（内部布线器颈缩区）且 ≥ widthMil.min
  才算焊盘逃线；从焊盘出发一直窄下去的段不再放过（原规则：一端在 50 mil 内即可）。
- 后仿真：逃线是板上真实铜，窄段进入 IR 模型（`TestEscapeStubNarrowsIRPath`）。
- 布不通反馈：`routeResult.placementHints` 与 `route-complete` 条目给出每条 blocked 连接该挪哪个件、往哪挪、至少多远
  （逃线走廊里挡着的件；走廊空则把另一端件拉向逃线出口；同件同网脚、远端连接不编造挪件）。

PicoRick 草稿副本，`kicad route --rip-up --no-review --threads 1`，同一命令、同一意图（墙钟受机器负载影响，只作参考）：

| | 前（dev 897c4041） | 后（r4，最终代码） | r3（无 via-at-SMD 规则） |
|---|---|---|---|
| 总计 / fastroute / 丝印 | 821 s / 696 s / 23 s | 106 s / 91 s / 3.7 s | 105 s / 90 s / 3.7 s |
| 逃线 | — | 15 焊盘（外出 11、同网桥 3、内收过孔 1），1 个焊盘放不下 min | 同左 |
| fastroute 未布通（blocked） | 11（11） | 5（5） | 4（4） |
| intent-widths | 0 | 0 | 0 |
| kicad-drc 错误 | 22（clearance 11 + unconnected 11） | 15（10 + 5） | 13（9 + 4） |

剩余 clearance 全是用户 `Analog` 网类（0.2 mm）与 fastroute 走线之间，与逃线无关（前后都有）。剩余 blocked 集中在
U303 右侧 USB 串阻 R303/R304 与左侧去耦 C314 的逃线走廊里，摆放提示：R303 右移 ≥ 16 mil、C314 左移 ≥ 37 mil、
R304 向左上靠近 U303.46 出口等——这些是摆放问题，不再是线宽规则问题。
