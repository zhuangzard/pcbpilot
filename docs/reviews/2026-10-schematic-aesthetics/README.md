# 原理图美观度：Phase A（度量 + 数据模型 + 总线能力）（2026-10）

状态：**Phase A 已实现（只报告）；Phase B 已实现（opt-in 生成，`--aesthetics STYLE`，离线验证，见
[phaseB/](phaseB/README.md) 与 [baseline.md §Phase B](baseline.md#phase-b-生成前后2026-10)）**。Phase A：`pcbpilot sch aesthetics`（`pkg/schaes`）、`sch bus list|create|delete|candidates`、
`report design` §6C。基线见 [baseline.md](baseline.md)。原生总线 action：V3 3.2.149 桌面版 **live-verified**
（[live/](live/README.md)），V4 未验证。**2026-10-03 用户决定布局画原生总线**：balanced/precision 默认为完整
标签泳道生成 `layout.buses`，`sch bus apply` 落地（日志/替换/回滚，live-unverified，现场流程见 live/README.md）。Phase C 起待用户确认。PCB 侧见
[2026-09-routing-aesthetics](../2026-09-routing-aesthetics/README.md)，两侧风格档同名。

优先级不变：**连接正确性 > 可读性 > 美观**。`sch layout-lint`、`sch check`、`bridge-check`、
`sch layout-score`（五维）、SDK DRC 全部保留且优先；本层是叠加的最低一层软约束，权重 0、不是门、
不改任何几何。

## 1. 现状（2026-10，dev @ v0.7.0）

| 能力 | 现状 | 美观相关的空白 |
|---|---|---|
| `sch layout-lint` | 放置硬门：overlap、pin-coincidence、tight（按 ownership 豁免）、off-grid 锚点（5 units）、out-of-sheet；`--strict` 缺测即失败 | 只看器件本体，不看导线/标签 |
| `sch check` | 电气 + 标记几何：floating-pin、geom/net-marker mismatch、multi-net-wire、**wire-crossing(WARN)**、wire-over-pin、zero-length、dangling、duplicate/redundant marker、marker/text/titleblock overlap、folded-net-label、reversed-net-flag、polarity-convention、pin-exit-direction、wire-through-body、partition-overlap | 交叉只报“有”，不量化；无转折、T 结点、四通、绕行、对齐 |
| `sch layout-score` | 五维诊断：folded-labels、reversed-labels、proximity、stub-tidiness（长链 >250、同排标签 <117）、frame-fit；每条归因带可执行 fix；缺测 `incomplete` | 无布线几何维度；无总线/标签策略维度 |
| 生成：`layout-plan --zones` / `lib-layout` / `compose` | 区内求解 + 纸张两层；路由为 5-raw 有向迷宫（`sch_layout_maze.go`，预算 200k 节点，扩张 40/80/160/320）；候选目标 `libCandidateScore` = [线长, 段数, 与已有件的轴向距离, 内容面积] 字典序 | 段数只是转折的粗代理；**没有交叉、T 结点间距、四通、标签泳道项** |
| `connect_pin` / `autoconnect` | pin → 短桩 → flag/port；四向 × offset 候选评分，预测 marker 框（netport 长 `6·len+8`、ground 10×21、power 6×11）、同批 stagger、三类硬拒（异网线、穿非目标 pin、图签） | 标签方向只求“不撞”，无同组对齐/等距 |
| netflag / netport / net label | `createNetFlag` / `createNetPort`（@beta，旋转取反由连接器自测补偿）；`createNetLabel` @beta “ADD since EDA v4”，V3 3.2.149/3.2.186 挂起或空回包，daemon 在 V3 返回 `HOST_API_UNSUPPORTED` | — |
| tidy / zones / groups | `sch zone tidy`、`sheet tidy`、`group tidy`、`align/distribute` 为旧三层体系；数据驱动基准已取代其主流程 | 对齐只在旧工具里，新生成链没有对齐后处理 |
| 总线 | 连接器只在 `connectivitySummary` 里**计数** `sch_PrimitiveBus.getAll()`，compose 预检要求 `buses==0` | 无创建/列表/删除 typed 能力 |
| 历史审计 | [2026-08 sch surface audit](../2026-08-sch-surface-audit.md)、[algorithm](../schematic-algorithm-validation.md) / [composition](../schematic-composition-validation.md) validation、[v1.6.0 acceptance](../2026-09-23-v1.6.0-schematic-acceptance.md) | 均围绕正确性与可读性，未涉及美观度量 |

离线实测（细节见 [baseline.md](baseline.md)）：

- 仓库自己的 `sch lib-layout` 输出（AMS1117 样例，真实测量几何）：1 处 **+5V × +3V3 严格 X 交叉**（电气正确、视觉歧义）；
  4/4 个 T 结点离拐点只有 **5 units**（拥挤）；同类器件 0/3 共线。正确性无问题，美观有明确可改点。
- esp32-v05 两页真实布局（canonical，只有器件/引脚）：同类器件行列共线 61.5% / 25%；整页占用 CV 1.86 / 1.69。
  用朴素网络策略把整页喂给 `layout-plan` 在 30 万候选预算内失败（SW2 / IO0 / +3V3 命名冲突）——这只说明
  “无归属约束的整页求解”不可行，不是生成器缺陷结论；正式流程仍须显式 zone/ownership。

## 2. 度量分类与精确定义（`pkg/schaes`）

坐标为 EasyEDA 原理图画布单位（0.01 in，y 向上）。每项 0–100；缺数据 `skipped`（不算满分，判定 `incomplete`）。
阈值列为 balanced 档；其他档见 §2.4。来源列标明是标准规则、仓库实测常量还是**工程判断（未校准，Phase E 人审拟合）**。

### 2.1 布线 W（组权 0.5；组分 × 已连引脚份额）

拓扑按 EasyEDA 实测接触语义（schematic-data.md）：端点重合 = 接触；端点落在他段内部 = T 接触；**严格内部 X 不导通**；共线重叠视为接触。

| ID | 指标 | 定义 | 打分 | 来源 |
|---|---|---|---|---|
| W1 | 转折/连接 | 两臂、方向改变的节点数 ÷ Σ(每线岛端子数−1) | ≤0.5→100，≥2.5→0 | IEC 61082-1 连接线直、少转折；数值判断 |
| W2 | 异网交叉 | 不同线岛、异网的严格内部 X 数 ÷ 连接数 | 0→100，≥0.25→0 | IEC 61082-1 少交叉；X 不导通（3.2.186 实测） |
| W3 | 四通/歧义 X | 臂数 ≥4 的结点 + 同网不同岛的 X（看似结点实为两岛） | 每处 −25 | IEEE 315 / IEC 60617：用错开的 T，不用十字结点 |
| W4 | 共线重叠 | 共线且重叠 >0.5 的段对（异网另注“电气，见 multi-net-wire”） | 0→100，≥4→0 | schematic.md：导线不得经过异网线 |
| W5 | T 结点质量 | 三臂结点中：全正交、不在引脚上、距同岛其他结点/拐点 ≥10 units 的比例；无 T 记 100 | 份额×100 | 10 = 2 × 5-unit 连接格；判断 |
| W6 | 绕行比 | Σ线岛长度 ÷ Σ端子直角最小生成树（RMST） | ≤1.1→100，≥2.0→0 | RSMT ≥ ⅔·RMST（Hwang 1976） |
| W7 | 穿本体/标记/文字 | 与器件 bbox（内缩 0.5）、他人 marker 框、位号/自由文字框相交 >1 unit 的段占比；自身桩线与自身标签豁免 | 0→100，≥15%→0 | wire-through-body 是 sch check ERROR；这里只做软计数 |
| W8 | 落格 | 引脚与顶点在 5-unit 格的份额；器件锚点与非引脚顶点在 10-unit 格的份额，按 gridBlend 混合 | `100×((1−b)·s5 + b·s10)` | 5 = layout-lint off-grid；10 = 0.1 in 惯例 |

防刷分：带网、非 NC 引脚中没有任何导线到达的比例记为 `wiredShare`，布线组分乘以它；有带网引脚却一根线都没有时布线各项记 0。

### 2.2 版面 L（组权 0.3）

| ID | 指标 | 定义 | 打分 | 来源 |
|---|---|---|---|---|
| L1 | 信号流向 | power 旗朝上、ground 朝下、IN 端口朝左、OUT 朝右；接口件（J/CN/USB/P/X/H+数字）中心在内容宽度左右 30% 边带 | 合规份额×100 | IEC 61082-1 左→右、上→下；orientation.json；30% 判断 |
| L2 | 标记朝向一致 | netport 水平（竖排 = 折叠标签）；旗体方向 = 桩线到达方向（顺线朝外） | 份额×100 | orientation.json；layout-score folded/reversed 同源判据 |
| L3 | 行列对齐 | 同类（≤3 脚小件 / IC·接口）且 300 units 内的邻居中，中心 x 或 y 在容差内共线；近失（容差 < d ≤ near-miss）扣 0.25 | 均值×100 | PCB P1 对应；容差按档 |
| L4 | 间距均匀 | 同行/列 ≥3 件、相邻净距 ≤300 的间距 CV 均值 | ≤0.15→100，≥0.8→0 | PCB P2 对应；判断 |
| L5 | 模块框整洁 | 每框内容到四边留白的 CV、最小留白 <5 减半、框重叠记 0，× 入框器件份额 | 见实现 | schematic.md：本体与位号完整入框 |
| L6 | 文字重叠 | marker 框（符号 ∪ 文字带）、位号、自由文字两两重叠 + 压非本件本体，个数 ÷ 标签数 | 0→100，≥0.1→0 | marker/text-overlap 是 sch check finding；预测框同一把尺 |
| L7 | 版面均衡 | 内容 bbox 4×4 占用率 CV；有图纸时再与居中偏移各占一半 | CV ≤1.0→100，≥2.5→0 | PCB P7 对应；判断 |

### 2.3 标签与总线 N（组权 0.2）

| ID | 指标 | 定义 | 打分 | 来源 |
|---|---|---|---|---|
| N1 | 长线宜改标签 | 多端子线岛中，总长 > longWire（600）或与他岛交叉 > longCrossings（2）的比例 | 0→100，≥0.15→0 | schematic.md：电源地就近重复符号、跨模块 netport；600 ≈ A4 图宽 1170 的一半 |
| N2 | 短程滥用标签 | 同一框内、≥2 个 netport/label 的信号网，全部端点半周长 < shortLabel（120）的比例；跨框（模块端口）豁免 | 0→100，≥0.25→0 | schematic.md：相邻外围用真实线；120 ≈ layout-score 同排标签最小间距 117 |
| N3 | 总线/虚拟总线 | 每个候选组：有前缀匹配的原生总线 = 100；否则其标签的同列/行份额 × (1 − min(1, 节距 CV)) × 同向份额 | 组均值 | 本评审定义；判断 |

候选组（`DetectBusCandidates`，只按名字、只做建议）：索引网 `PREFIX0…n`（≥3 个、编号近连续，散落 GPIO 不算）；
UART（TX/RX 或 ≥3 角色）、SPI（SCK + 数据 + ≥3 角色，≥5 成员记 QSPI）、I2C（SDA+SCL）、SDIO（CMD+CLK+D0…）；
USB（DP/DM、D+/D−）与 MIPI/LVDS（`_P/_N` 对）只作“按对并行”，不合并成字母总线。电源/地网永不入组。

### 2.4 风格档（与 `pcb aesthetics` 同名，计划权重相同；数值为原理图专用）

| 档 | 计划权重 | 对齐容差/近失 | 目标格/混合 | 长线/交叉 | 短程标签 | 额外权重 |
|---|---|---|---|---|---|---|
| functional | 0.05 | 5 / 20 | 5 / 0 | 800 / 3 | 80 | L3 L4 L7 W8 N3 ×0.5 |
| balanced（默认） | 0.10 | 2 / 15 | 10 / 0.3 | 600 / 2 | 120 | — |
| precision | 0.20 | 0.5 / 10 | 10 / 0.6 | 400 / 1 | 150 | W1 W2 W3 L2 L3 N3 ×1.5 |
| auto | 按器件数、引脚数、器件面积密度算复杂度：≥0.66 functional，≤0.33 precision，否则 balanced，打印理由 |
| custom | `--style-file`（裸对象或 `{"schematic":{…}}`）；连接、NC、归属、门禁类键直接拒绝 |

不同档的权重不同，**跨档分数不是同一标尺**，只在同档内比较。

## 3. 总线与网络端口能力调研

依据：`@jlceda/pro-api-types` 0.4.25（仓库连接器所用版本）、`pcbpilot api search bus|netport|netlabel`、
连接器现有调用与历史现场记录。**本任务未操作现场编辑器**。

| 能力 | 官方 API | V3 3.2.x | V4 | pcbpilot |
|---|---|---|---|---|
| 总线创建/修改/删除/读取 | `eda.sch_PrimitiveBus.create(busName, line, color?, lineWidth?, lineType?)`、`modify`、`delete`、`get`、`getAll`、`getAllPrimitiveId`，全部 **@beta**；`ESCH_PrimitiveType.BUS` | 类型包未标 “ADD since EDA v4”（该包对 V4 新增接口一律这样标注，共 10 处），推定 V3 存在；`getAll()` 已被 `connectivitySummary` 调用（compose 预检要求 `buses==0`），**create/delete 从未现场执行** | 同左，未现场执行 | `schematic.bus.list/create/delete` + `sch bus …`，离线单测 + `--dry-run`；**planned / live-unverified** |
| 总线路径规则 | `line` 为 `[x1,y1,…]` 或 `[[…],[…]]`；斜段非法、互不相连的多段线非法、单点多段线被忽略（文档 1.1–1.6） | — | — | 连接器与 CLI 写前同规则失败关闭；单点多段线也拒绝，以便回读精确比对 |
| 总线名语法 | 类型包未说明；`ISCH_PrimitiveBus` 注释提到可在导线/总线上放多个网络标签、`net` 读数可能滞后 | 未知 | 未知 | `NAME[a:b]` 以外只警告，不拒绝；待现场确认 |
| 总线分支（bus entry） | **不存在**：`ESCH_PrimitiveType` 只有 Bus/Wire 等，类型包中无 BusEntry | 不支持 | 不支持 | 成员接入 = 普通正交导线 + 成员名网络标签（现有 typed 路径）；写线守卫本就拒绝 45° 斜段 |
| 网络端口（跨页/跨模块） | `sch_PrimitiveComponent.createNetPort('IN'|'OUT'|'BI', net, x, y, rotation?, mirror?)` @beta；`setNetPortComponentUuid_IN/OUT/BI` | 已现场使用（connect_pin，旋转取反自动补偿） | 已现场使用（Web 4.1.60） | `schematic.netflag.create` / `power.connect_pin` / `autoconnect` 已有 |
| 离页连接器 | 无独立图元；跨页 = 同名 netport / 网络标签 | — | — | 同上 |
| 网络标签 | `sch_PrimitiveAttribute.createNetLabel(x, y, net)` @beta，“ADD since EDA v4” | 3.2.149/3.2.186 挂起或空回包；daemon 返回 `HOST_API_UNSUPPORTED` | 4.1.60 可能已创建却返回空值，连接器按新增属性唯一匹配验证 | `connect_pin --kind net_label` 已有 |

结论：**可编程创建总线在 API 层面存在（V3/V4 推定都有），但未经现场验证；没有 bus entry**。因此：

1. 已实现 typed action（连接器 `extension/src/schematic-bus.ts`、Go 目录 `internal/protocol/actions.go`、
   CLI `internal/app/cmd_sch_bus.go`），读写都失败关闭、写后回读，`partial` 非零退出；docs/FEATURES.md 标 planned / live-unverified。
2. 现场验证清单（交主 Agent，在 V3 与 V4 各跑一次，均用 `--project ceshi` 测试页）：
   `sch bus list`（空页应 `count:0`）→ `sch bus create --name 'D[0:3]' --points 400,600,400,300 --dry-run` → 去掉 `--dry-run` 真建 →
   `sch bus list` 与 `sch list --include-wires` 的 `connectivitySummary.buses` 对账 → `sch save` → `doc reload` → 再 list →
   在总线旁用 `connect_pin --kind netport` 接一个成员，`sch check` / 网表确认成员连通**只来自标签**、总线不改变任何 pin→net →
   `sch bus delete --ids …` → list 为空 → 保存。另测 `D0..D3`、`D[0..3]`、无下标名各一次，记录宿主接受的语法。
3. 宿主不支持或未验证时的**回退：虚拟总线**——同组成员的网络标签按索引名排在同一列/行、等节距、同朝向
   （N3 直接度量），电气上就是普通同名标签，不依赖任何新 API。

## 4. 优先级规则（固定，写进报告 `priority`）

1. 连接正确性：每个 flag/port 在真实导线上；无假连接（X ≠ 接触，T = 接触）；NC 保留；核心与专属外围整体归属、真实线树直连。
2. 可读性：无重叠、位号可见、标记顺线朝外、框包住内容（layout-lint / sch check / layout-score）。
3. 美观：本报告。软、权重 0、永不为门，永不成为移动一根已正确导线的理由；任何美化 pass（Phase B）若使 1/2 任一检查变差即回退。

## 5. 生成策略与 Phase B 计划（Phase B 已实施，见下方实现状态）

每项验收都包含：目标指标改善；`sch layout-lint` / `sch check` 零新增；连接（component/pin→net/NC）与归属完全不变；
现有 `sch_layout_*`、compose、frame、sheet-plan 测试全绿；`schaes` 合成单调性测试与基线表更新。

Phase B 实现状态（2026-10）：B1–B4 均以**生成后美化 pass** 落地（`internal/app/sch_layout_aesthetics.go`），
不改变求解器默认搜索（不加 `--aesthetics` 输出逐字节不变）。迷宫加了可选加权代价（`bendWeight`/`crossWeight`，
默认 0）；B1 的线束重布、T 落点、四通消除、标记重放在 `sch_layout_engine_routes.go` 辅助函数 + pass 内；目标向量
在 `sch_lib_objective.go`（`libAesObjective`，不进入 `libCandidateScore`）；B2 判据 `libLabelSplitAllowed`
（`sch_layout_netlabel.go`）；B3 `sch_layout_bus_lane.go`；B4 吸附目标 `schAlignSnapTargets`（`sch_zone_compact.go`）+
`alignPass`（`sch_layout_optimize.go`）。与原计划的差异：`sch_module_rows.go`（纸张层整区行排）未改——Phase B 只动区内；
naming frontier 文件未改，B2 复用 `libVisitMarkerAt` 与求解器同一套命名门禁。

| 阶段 | 内容 | 改动文件 | 验收测试 |
|---|---|---|---|
| B1 布线清理 | 迷宫代价加转折罚与交叉罚；T 结点与拐点/结点保持 ≥10 units；禁止四通（改错开 T）；后处理合并共线段、消 5-unit jog | `internal/app/sch_layout_maze.go`、`sch_layout_engine_routes.go`、`sch_layout_direct_route_frontier.go`、`sch_lib_objective.go`（目标向量加交叉/结点项） | ams1117 lib-layout：W2 交叉 1→0、W5 0→≥75；`TestLibAdapterMatchesStandaloneEngine` 等现有用例不退化；新增“同输入新旧线岛集合一致”对账测试 |
| B2 长线→标签 | 仅对 netPolicies 允许的网（`module_port`、`local_power`、`local_ground`）把超长/多交叉线岛换成两端局部标签；`direct` 网永不转换（归属不变量） | `sch_layout_netlabel.go`、`sch_layout_naming_frontier.go`、`sch_layout_route_naming_frontier.go` | N1 改善、N2 不变差；`direct` 线岛不变的负例（把 direct 网拉长也不得转标签）；compose 回放一致 |
| B3 总线泳道 | 对 N3 候选组，在 zone 内为成员标签分配同列等节距泳道（5-unit 格，节距 10/20，统一朝向）；原生总线只在 §3 现场验证通过后作为可选输出 | 新 `internal/app/sch_layout_bus_lane.go`，接入 `sch_layout_zones.go`；Apply 侧沿用 connect_pin，原生总线走 `schematic.bus.create` | 合成索引网样例 N3 ≥90；W7/L6 不变差；标签仍锚在各自 pin 的真实桩线上；无 bus API 时结果与虚拟总线一致 |
| B4 对齐后处理 | 区内同类外围吸附到共享行/列、等距化，只在 tether 余量与空闲区内移动，重验全部几何 | `sch_zone_compact.go`、`sch_module_rows.go`、`sch_layout_optimize.go`；纸张层 `sch_layout_sheet_z.go` 仅整区平移 | esp32-v05 两页 L3/L4 上升；layout-lint 0 overlap；layout-score 不降；sheet-plan 仍只选整区候选 |
| C 计分 | 风格档计划权重作为 `layout-plan` 候选的低位软项（字典序最后一位），绝不压过可行性与现有目标 | `sch_lib_objective.go`、`sch_layout_optimize.go` | 防刷分属性测试：删线、拆标签、断连不能升分；可行性回归不变 |
| E 校准 | A/B 偏好（导出图成对）+ 离线 VLM 初筛 → 拟合阈值与权重（Kendall τ ≥ 0.6）；补采真实已布线原理图正样本 | `pkg/schaes`、`docs/reviews/…/baseline.md` | 校准前后基线表与理由同时提交 |

## 6. 文件

- 度量：`pkg/schaes/`（model / parse / topology / bus / profile / aesthetics），测试含合成劣化单调性、防刷分、黄金基线、
  orientation.json 一致性；fixture `pkg/schaes/testdata/`。
- CLI：`internal/app/cmd_sch_aesthetics.go`、`internal/app/cmd_sch_bus.go`。
- 连接器：`extension/src/schematic-bus.ts`（+ `.test.ts`），注册于 `extension/src/actions.ts`。
- 目录：`internal/protocol/actions.go`（3 个 action）、`internal/app/dispatch.go`（create/delete 进 verifiedWriteActions）。
- 报告：`pkg/designreport/schaesthetics.go`、两份模板（及 Skill 副本）、`report design --sch-snapshot`。
- Skill：`schematic.md`、`schematic-wiring.md`、`auto-layout-sop.md`、`actions.md`、`SKILL.md` 路由表。
