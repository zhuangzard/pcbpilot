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
| 原理图生成/布局/美观度/块复用（sch layout-plan/aesthetics/block-apply/autoconnect/group-move…） | 连接器写入 | 接到上面的写后端 | 待做 |
| 旧原理图迁移 EasyEDA → KiCad | — | `kicad sch-import --epro X --board B --out DIR [--pcb board]`（每页一张子图 + 层次根图；符号/单元/图形、位号、值、封装、LCSC、导线、电源、网络端口、非连接；导出后自动与 EasyEDA 连接性逐网逐脚比对） | 完成（分支 kicad/sch；Gas A/B：224/224 网络、686 脚一致，与板焊盘 129/129 网络同名一致；KiCad 自带 EasyEDA Pro 导入器只能在 GUI 中用，kicad-cli 不能读 .epro） |
| 原理图检查（sch check/gate/ERC） | 连接器 + 自检 | `kicad-cli sch erc` + 现有离线检查 | 待做（`kicad sch-import` 结果 ERC 可跑：仅 10 条 power_pin_not_driven——缺 PWR_FLAG——和封装库未登记的警告） |
| 意图推导（intent derive，含安全表） | 离线 | 离线（输入改为 KiCad connectivity） | 进行中 |
| 仿真 power/analog/post-layout | 离线 | 离线（board-final.json 来自 KiCad） | 进行中 |
| 布局（pcb auto run / place） | 连接器剧本 | `kicad place`（pcbnew） | 已实现，紧凑度/居中待调 |
| 布线（fastroute + 意图规则 + 宽度/过孔阵/IR 闭环） | 连接器 + dsn-fix/ses-repair | `kicad route`（原生 DSN/SES） | 进行中（kicad/core） |
| 铺铜/电源平面 | pcb.pour.* | pcbnew ZONE + ZONE_FILLER | 进行中 |
| 全部硬门禁（DRC、意图、安全/隔离、仿真、丝印、说明书、报告、设计评审） | runQualityGates | 同一门禁代码跑在 KiCad 快照上 | 进行中 |
| 丝印位号（silk-align --tight、组标签） | pcb.silk.* | pcbnew 字段位置/可见性 | 待做 |
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

- `--backend kicad` 还没接的命令：`sch connect/disconnect`（电源短桩）、`autoconnect`、`block-apply`、`layout-plan` 落地、
  `group-move`、`zone*`、`titleblock`、`page-new/rename/delete`（新增子图）、`prim-delete`（删除图元）、`rebind-*`、`replace`、
  `no-connect --clear`；`sch check/gate` 对 KiCad 用 ERC + 离线检查。
- 写后端：删除/修改已有导线与标签、拖动时导线跟随、`extends` 派生库符号、按 EasyEDA 单位（10 mil、y 向上）输入坐标。
- `kicad sch-import`：镜像元件的方向只按 KiCad 变换搜索（Gas 工程里没有镜像件，未实测）；不导入总线、图片、表格；
  字段对齐方式未迁移（位置照搬）；未自动加 PWR_FLAG。
- `kicad netlist` 与 kicad/core 分支的同名命令重叠，合并时保留一个（字段形状相同：connectivity 1.4）。
