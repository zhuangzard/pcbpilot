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
| `sch layout-plan` 落地 | 连接器 Apply 队列（188 步级） | `--backend kicad --kicad-sch X [--fit]`：按位号从 KiCad 图重测器件后规划，核心不动、其余按计划偏移与旋转（以引脚落点反求 KiCad 旋转）一次写入；导线端点、标签、非连接、结点、电源符号随引脚移动，网表前后一致才落盘 | 完成（分支 kicad/sch-auto；单布局，`--zones` 未接；计划导线/标记不画，拖动后的导线可能变斜） |
| 原理图美观度/块复用（sch aesthetics/block-apply/group-move…） | 连接器写入 | 接到上面的写后端 | 待做 |
| 旧原理图迁移 EasyEDA → KiCad | — | `kicad sch-import --epro X --board B --out DIR [--pcb board]`（每页一张子图 + 层次根图；符号/单元/图形、位号、值、封装、LCSC、导线、电源、网络端口、非连接；导出后自动与 EasyEDA 连接性逐网逐脚比对） | 完成（分支 kicad/sch；Gas A/B：224/224 网络、686 脚一致，与板焊盘 129/129 网络同名一致；KiCad 自带 EasyEDA Pro 导入器只能在 GUI 中用，kicad-cli 不能读 .epro） |
| 原理图检查（sch check/gate/ERC） | 连接器 + 自检 | `kicad-cli sch erc` + 现有离线检查 | 待做（`kicad sch-import` 结果 ERC 可跑：仅 10 条 power_pin_not_driven——缺 PWR_FLAG——和封装库未登记的警告） |
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

## 原理图（分支 kicad/sch）尚未完成

- `--backend kicad` 还没接的命令：`sch disconnect`、`autoconnect --replace/--all-pages`、`block-apply`、
  `layout-plan --zones` 落地（及按计划重画导线/标记）、`group-move`、`zone*`、`titleblock`、`page-new/rename/delete`（新增子图）、`prim-delete`（删除图元）、`rebind-*`、`replace`、
  `no-connect --clear`；`sch check/gate` 对 KiCad 用 ERC + 离线检查。
- 写后端：删除/修改已有导线与标签、拖动时导线跟随、`extends` 派生库符号、按 EasyEDA 单位（10 mil、y 向上）输入坐标。
- `kicad sch-import`：镜像元件的方向只按 KiCad 变换搜索（Gas 工程里没有镜像件，未实测）；不导入总线、图片、表格；
  字段对齐方式未迁移（位置照搬）；未自动加 PWR_FLAG。
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
