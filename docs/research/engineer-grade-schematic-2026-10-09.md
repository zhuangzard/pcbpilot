# 工程师级原理图：开源工具调研与 pcbpilot `kicad sch-build` 改进路线（2026-10-09）

> 范围：只看“如何生成可读、工程师级的原理图”（分页/层次、符号摆放、连线 vs 标签、KiCad 导出、质量度量），不涉及 PCB 布线。
> 源码均为浅克隆，放在 `~/.pcbpilot/study/`。只学原理，不复制代码，实现用我们自己的 Go。
>
> | 仓库 | commit | 语言 | 与本题的关系 |
> |---|---|---|---|
> | atopile/atopile | 619eda7 | Python/Zig | **不生成原理图**（见 §1） |
> | circuit-synth/circuit-synth | 3aaff18 | Python | 生成 .kicad_sch：层次图纸，只用标签不画导线 |
> | tscircuit/core + matchpack + schematic-match-adapt | 6a195d6 / HEAD | TS | 符号摆放（matchpack 是默认引擎） |
> | tscircuit/schematic-trace-solver | 62bb9e6 | TS | 连线与网络标签求解（MST + 标签） |
> | absmach/synth | 8787102 | Rust | 完整的自动原理图流水线 + lint 规则，最值得参考 |
>
> 单位约定：tscircuit 的原理图单位里，0.2 等于一个引脚间距，下文按 **1 单位 ≈ 12.7 mm** 换算（推断值，用于对照阈值）；pcbpilot 规划器的单位是 0.01 in = 0.254 mm（连接网格 5 单位 = 1.27 mm）。

---

## 0. 结论速览

1. **在“自动生成 KiCad 原理图”这件事上，没有一家比 pcbpilot 的区内（zone 内）几何质量更严格。** pcbpilot 已经有迷宫布线、命名岛、18 项美学指标（W1–W8/L1–L7/N1–N3）、`schquality` 几何门槛和 KiCad 网表比对。差距不在区内连线，而在**三处结构性问题**：
   - **分页/层次是“平坦 + 全局标签”**：根图只放没有引脚的方框，跨页网络用 global label。
   - **区与区之间永远用标签**，从不画导线。
   - **区在页面上的排布按 spec 顺序逐行排**，没有信号流。
2. **synth 是最好的参考对象**：
   - 先识别电路母题（motif）再摆放；
   - 按功率流分列（连接器/电源 → 稳压器 → MCU/IC → 无源）；
   - 用 barycenter 排序；
   - 规则是“组内画导线、组间用标签”，导线拥堵（交叉 >2）就退回标签；
   - 只在页面放不下时才分页，分页后用层次标签 + 图纸引脚；
   - 有 15 条可量化的原理图 lint（E-SYNTH-SCHEM-001..015）。
3. **tscircuit 的贡献是两条可直接移植的规则**：
   - **“距离决定画线还是贴标签”**：在 MST 中禁用长于阈值的边，剩下的森林里每个连通分量放一个标签。
   - **母题化摆放**：去耦电容排成一行，正极对齐同一条轨线；两脚件强制旋转成电源脚朝上、地脚朝下；晶振和负载电容成组；接地负载竖直堆叠。
4. **circuit-synth 是反面教材**：
   - 全标签、无导线，单页内部网络也用 hierarchical label；
   - 按面积排序的行式“文本流”摆放，没有信号流，去耦电容可能远离 IC；
   - 图纸引脚只放在右边且与标签坐标错开 1.27 mm。
   - 仓库里找不到移除 spiral 的提交，原因见 §2.2。
5. **atopile 根本不生成原理图**：它主张用 `.ato` 文本、电源树 Mermaid 图和 3D 力导向图代替原理图。

---

## 1. atopile

- **没有 .kicad_sch 写出器。**
  - `src/faebryk/exporters/` 下只有 bom、documentation、parameters、pcb、power_tree。
  - `src/atopile/build_steps.py:633-1200` 的构建步骤也不含原理图。
- `src/faebryk/core/zig/src/sexp/kicad/schematic.zig`（426 行）只是 S 表达式模型，用于符号库转换和文件读写。它覆盖 Sheet/SheetPin/GlobalLabel/Label/Wire/Junction/Bus/TitleBlock，但没有 hierarchical_label。
- `ato view`（`src/atopile/cli/view.py:51`）把实例图导出成 JSON，交给 three.js 的 ForceAtlas2 力导向图显示（`src/atopile/visualizer/web/src/lib/layoutEngine.ts`，“hierarchical”模式只是 y = 深度×100）。没有引脚、导线、符号。
- 另有电源树（`exporters/power_tree/power_tree.py`，Mermaid）和 I²C 树。
- **可借鉴的只有理念**：模块/接口层次以图的形式保存；“电源树”是一个很好的**顶层图补充视图**（见 §8 第 9 项）。

---

## 2. circuit-synth

路径相对 `src/circuit_synth/kicad/`。

### 2.1 层次 / 分页 / 端口

- **子电路对应子图纸**：每个 Python `@circuit` 子电路生成一个 `<sub>.kicad_sch`；顶层电路直接画在根图上（`sch_gen/main_generator.py:662-955`）。
- **接口就是传入的 Net 参数**，没有显式端口对象。判定哪些网络成为图纸引脚（`sch_gen/schematic_writer.py:1533-1690`），按以下顺序：
  1. 按对象身份；
  2. 按名字；
  3. 按兄弟子电路同名网络；
  4. 最后用硬编码列表 `VCC/GND/VIN/VOUT/...` 猜。
- **图纸符号与引脚**（`schematic_writer.py:1693-1747`）：
  - 所有引脚放在**右边**，按名字排序，间距 2.54，形状默认双向；
  - 父图在每个引脚处放一个 hierarchical label，但与引脚坐标差 1.27 mm（L1705 对 L1713，是 bug）；
  - 子图 `page_number` 写死为 "2"。
- **图纸尺寸**：高 = max(20.32, 网络数×2.54+10.16)，宽由最长名字估算（`schematic_writer.py:895-925`）。这里按子电路的**全部**网络计算，而不只是接口网络。
- **同一子电路多次实例化**：会被 `sheet_symbol_map[sub_name]` 覆盖（L1859），实际不支持。

### 2.2 摆放

- **最终生效的是文本流算法**：`SchematicWriter._place_components`（`schematic_writer.py:790-1038`）调用 `schematic/text_flow_placement.py`。
  - 按 (面积, 宽度) 从大到小排序；
  - 从 x = 24.7、y = 12.7 起从左往右排，超出右边界就换行，`spacing = 15`；
  - 先试 A4 横版，放不下试 A3，再放不下就抛错（L49-115）。但选出的纸张尺寸只写进日志，不写进文件。
- **前一阶段的 connection_aware 结果会被丢弃**：`main_generator._collision_place_all_circuits` 的 connection_aware 做法是
  - 按连接度排序，
  - 理想位置取已放邻居的加权质心，
  - 从理想位置向外做环形搜索（半径 10…100 mm，每圈 8 个角度）（`connection_aware_collision_manager.py:41-190`）。

  这是仓库里唯一的“spiral”，但它的结果被文本流覆盖（L833 “ignore existing positions”）。而且公共入口 `core/circuit.py` 约 L762 写死了 `sequential`。
- **关于“spiral 被移除”**：浅克隆里没有任何 schematic spiral 删除记录。
  - PCB 端仍保留 `{"hierarchical","spiral"}`（`pcb_gen/pcb_generator.py:200`）。
  - 唯一的书面理由在 `docs/prd/migration-executive-summary.md:72-76`：force-directed、connection-centric、connectivity-driven 三种算法因**结果不稳定、使用率不到 5%** 被删，统一用 hierarchical。
  - 结论：**螺旋/力导向摆放不确定，结果难以复现，评审时也解释不清。** 这一点与我们“确定性、可复现”的原则一致。
- **其他缺失**：没有电源在上/地在下、去耦就近、引脚侧感知、旋转选择这些规则（rotation 恒为 0）。

### 2.3 连线

- **不画任何导线**：参考输出中 `(wire` 计数为 0。
- **信号网络**：每个引脚端点贴一个 hierarchical label（`schematic_writer.py:1291-1530`）。
  - 标签角度 = 引脚方向 + 180 + 元件旋转；
  - 0°/90° 左对齐，180°/270° 右对齐（`schematic/label_utils.py:312`）。
- **`_is_net_hierarchical()` 恒返回 True**（L1053），单页内部网络也用层次标签，ERC 有噪声。
- **电源网络**：名字命中 `core/power_net_registry.py` 时改用电源符号。旋转 = 引脚角 − 90，GND/VSS 再加 180（L1398-1404）。不吸附网格。

### 2.4 导出与度量

- **导出**：
  - title_block 只写 title；
  - UUID 用 uuid4（不确定）；
  - 实例路径正确；
  - lib_symbols 完整嵌入。
- **度量**：只有调试日志里的连线长度和密度（`connection_aware_collision_manager.py:240-285`），没有美学评分，没有 ERC 门槛。

### 2.5 例子质量

- **好**：层次 UUID 和实例路径正确；元件不重叠；电源符号方向正确。
- **差**：
  - 全是“标签汤”，没有信号流，去耦电容可能离 IC 很远；
  - 图纸引脚单侧排列且错位；
  - 最大只到 A3；
  - 自动网络名 `N$1` 会出现在图纸引脚上。

**可借鉴**：引脚端点变换和“标签角 = 引脚方向 + 180”的对齐规则（我们已经有）；“包围盒外扩 + 碰撞”的简单检测。**需避免**：一切。

---

## 3. tscircuit：摆放（core + matchpack）

路径前缀：`core/lib/components/primitive-components/Group/`（记为 G/）和 `matchpack/lib/`。

### 3.1 模式与单元

- **默认 layout 模式**：没有手工坐标时默认是 “match-adapt”（`G/Group.ts:2478-2554`），**实际调用的是 matchpack**（`_doInitialSchematicLayoutMatchpack`）。schematic-match-adapt（模板匹配）在 core 里只剩一个辅助函数，已经是死路径。
- **嵌套 group**：先在内部布局，再作为一个刚性“复合芯片”参与父级打包（`applySchematicMatchPackLayoutToTree.ts:249-317, 517-605`）。
- **边框**：只有设置了 `border` 才画（`Group.ts:2695-2760`）。
- **折叠显示**：`showAsSchematicBox` 把整个 group 画成一个带引脚的方框符号。这正是“顶层图 = 模块方框 + 引脚”的思路。
- **多 sheet**：按连通分量分配；跨 sheet 的连接一律用 net label。

### 3.2 摆放流水线

`matchpack/lib/solvers/LayoutPipelineSolver/LayoutPipelineSolver.ts:89-226`，确定性，没有全局搜索：

1. **识别晶振电路**：晶振加每脚一个接地负载电容。
2. **识别去耦电容**（`IdentifyDecouplingCapsSolver.ts`）：
   - 条件：两脚电容，一脚接地、一脚接正电源；
   - 主芯片按强连接数确定；
   - `mainChipSide` 取主芯片上电源脚最多的一侧。
3. **分区**（`ChipPartitionsSolver.ts`）：
   - 晶振组、去耦组各成一区；
   - 其余元件按**强连接**（显式导线）的连通分量分区；
   - **只靠网络名相连的元件不合并。**
4. **区内摆放**：
   - **DecouplingCapRow**：≥2 个电容排成一行，按主芯片引脚顺序排列，**正极脚落在同一条水平轨线上**；
   - **ParallelAlignedPassive**：同一侧 ≥3 个两脚件，在芯片边外均匀排成一行；
   - **ParallelSeriesBranch**；
   - 兜底：`PackSolver2`，大件先放，目标是同网络焊盘距离的平方和最小。
5. **分区打包**：每个分区作为刚体，同网络焊盘相互吸引（这一步用弱连接）。去耦行就是在这里被拉向 IC 电源脚。
6. **后处理**，每一步碰撞即回滚：
   - AlignPowerGroundRows：同信号网络的两脚件排成一行；
   - PlaceNetOnlyDecouplingRows：只靠网络名相连的去耦行贴到主芯片的 `mainChipSide`；
   - GroundedLoadPair：电源 → 两脚件 → 两脚件 → GND 的链竖直堆叠，源脚在上；
   - AlignRegulatorCapacitorRow：稳压器电容成行，电源级放在下游左边；
   - AlignTestPoints。

**旋转规则**（`applySchematicMatchPackLayoutToTree.ts:75-110, 444-475`）：
- **IC 永不旋转。**
- 只有一个脚在电源/地上的两脚件，**强制**旋转成电源脚朝上（y+）或地脚朝下。
- 其余元件允许 0/90/180/270。

**常量**：chipGap 0.6、decouplingCapsGap 0.4、partitionGap 1.2、TRACE_CLEARANCE 0.2（约 7.6 / 5.1 / 15.2 / 2.54 mm）。

**没有显式的左→右信号流规则**，只有“稳压级放在下游左边”。

### 3.3 摆放如何交给连线求解器

见 `G/Group_doInitialSchematicTraceRender/createSchematicTraceSolverInputProblem.ts`：

- **directConnections**：显式 trace 拆成相邻两脚对。
- **netConnections**：每个 source_net。
- **`availableNetLabelOrientations`**：ground → `y-`，power → `y+`，信号 → `x-/x+`（L994-1017）。
- **最大 MST 边长** `schMaxTraceDistance`：默认 2.4（≈30 mm）（L37）。

---

## 4. tscircuit：连线与标签（schematic-trace-solver）

路径相对 `schematic-trace-solver/lib/solvers/`。

### 4.1 流水线

`SchematicTracePipelineSolver.ts:118-690`，共 27 级，GuidelinesSolver 已注释掉不运行。主干：

1. **MspConnectionPair**：MST 选出要画线的引脚对。
2. **TraceLines**：逐对布线。
3. **LongDistancePair**：未覆盖的引脚尝试连到最近的 3 个同网引脚。
4. **UnroutedTraceRecovery**：失败的对用通道路由重试。
5. **TraceOverlapShift**：拉开不同网络的共线重叠。
6. **NetLabelPlacement**。
7. **TraceLabelOverlapAvoidance**：导线绕开异网标签，再合并相邻标签。
8. **TraceCleanup**：减少拐弯、居中 Z 形。
9. **NetLabelPlacement**（第二次）。
10. **AvailableNetOrientation**：强制电源/地标签方向，必要时补一段 L 形连接线。
11. **RailNetLabelCornerPlacement**：电源标签吸附到拐角。
12. 各类碰撞求解。
13. **SameNetJunctionAlignment**：同网负载合成一条共享轨线加短分支。
14. **InlineNetLabel**。
15. **NetLabelToTrace**：把成对标签换回导线。

### 4.2 什么时候画导线、什么时候贴标签

关键：`MspConnectionPairSolver.ts:122-212`。

- **只看距离，不看连接类型。** 直接连接和网络连接合并成一张连通图，每个网络处理一次。（README 说“net connection 不布线”，这是错的。）
- **2 脚网络**：
  - 距离（曼哈顿；显式直连用欧氏）大于 `maxMspPairDistance` 时不配对，两端改为 port-only 标签；
  - 例外一：并联去耦“轨对”；
  - 例外二：两脚相对、`|dy| < 0.2`，此时仍画线，但不显示标签。
- **3 脚及以上**：Prim 曼哈顿 MST（`getMspConnectionPairsFromPins.ts`），**长于阈值的边代价设为无穷大**，结果是一片森林，**每个连通分量各放一个标签**。
- **边否决**：
  - 不同 section 之间不连；
  - HV、VH 两种 L 形都会横穿某芯片的“受限中心线”时不连；
  - 地网的规则：已显式成岛的地不再用线连接；同一行 ≥3 个芯片之间不跨行连地（`getGroundConnectionPolicy.ts`、`shouldSeparateGroundNetRows.ts`）。
- **交叉约束**：
  - 长距离补线必须不碰任何已有导线（`LongDistancePairSolver.ts:340`）；
  - NetLabelToTrace 把成对标签换成导线时，**简单路径的垂直交叉 >1 就拒绝**（`NetLabelToTraceSolver.ts:562-575`）。

### 4.3 单条导线的路由

`SchematicTraceSingleLineSolver2.ts`，不是网格 A*：

1. **初始路径**：从 `calculateElbow`（overshoot 0.2）生成的折线开始，引脚沿朝向出线，再用 L/Z/U 形连接。
2. **广度优先搜索**：碰到障碍时，把碰撞段做正交平移。候选坐标是“引脚与障碍边的中点”，或“障碍边外 0.2”。首尾段（引脚 stub）不动，改为平移相邻段。
3. **子路径排序**：按 (L1 长度, 有内部段落在两引脚包围盒内时罚 10)。
4. **障碍**：芯片原始包围盒，零余量；文字框按标签尺寸外扩。
5. **异网重叠**（`TraceOverlapIssueSolver.ts`）：第 i 组平移 ±(⌊i/2⌋+1)×0.1，正负号按 (异网交点数, 位移总量) 择优；检测两状态循环以防振荡。
6. **清理**：minimizeTurns（消阶梯、合并共线段）、balanceZShapes（Z 形中段居中）、Untangle。

### 4.4 标签放置

- **尺寸**：默认 0.45×0.2（约 5.7×2.5 mm）。
- **数量**：每个“导线连通分量”只放一个标签，宿主是“引脚最多的芯片所连的最长导线”（`host.ts`）。
- **候选位置**：每段导线的起点、中点、终点；**标签方向必须垂直于所在线段**；碰到芯片、文字或其他导线就拒绝。
- **电源/地**：只允许一个方向，选全组件中**最靠外**的位置（y+ 取最大 y，y- 取最小 y）。放不下就在 0.05 网格上做外推 + 横向搜索，范围 min(3×最大芯片宽, 2×标签长)，并补一段 L 形连接线（`AvailableNetOrientationSolver`）。
- **标签之间碰撞**：沿宿主线段每隔 0.1 移动，按离原位距离排序。

### 4.5 质量判据

- 没有全局代价函数，各级只做局部排序（见上）。
- 测试中的“好”不变式（`tests/fixtures/traceLabelCollisions.ts`）：**任何异网导线段都不得与标签矩形相交。**
- 回归测试是 52 个 SVG 快照用例。

---

## 5. synth（absmach/synth）

主要文档：`docs/schematic-procedures.md`（590 行，坦白写出了自身短板）、`docs/schematic-visual-loop.md`、`docs/modules.md`。代码：`crates/synth-layout/src/lib.rs`（7060 行）、`route/mod.rs`、`synth-kicad/src/schematic.rs`、`multisheet.rs`、`schem_erc.rs`。

### 5.1 层次 / 分页

- **模块先展开再布局**：`module "X"(vdd: power, i2c: I2C)` 有类型化端口和 interface 绑定；`use "X" as CH1` 展开成带前缀的元件，并设置 `component.sheet = "CH1"`。
- **刻意不用 complex hierarchy**（同一个 sheet 文件多次实例化）。理由：每个实例都要改位号、KiCad 实例路径坑多、电气上没有收益。**这与我们的选择一致。**
- **分页只在放不下时发生**：
  - 先把纸张从 A4 逐级放大到 A0；
  - 内容超过 A2 **且**至少两个 sheet 边界内有元件时才拆分（`synth-layout/src/sheets.rs:104,154`）；
  - 拆分时复用全局布局（`split_layout`，sheets.rs:186）。
- **跨页网络**：
  - 信号网络在子图每个端点放一个 `hierarchical_label`（sheets.rs:319）；
  - 电源网络不放标签，电源符号本身按值全局相连。
- **根图**（`synth-kicad/src/multisheet.rs:48-57`）：
  - 图纸方框在根图内容下方排成一行：边距 20、间距 25、高 60、最小宽 80 mm；
  - **图纸引脚放在方框底边，间距 12 mm**；
  - 每个引脚接一根 5.08 mm stub，再接同名本地 label。曾经尝试在根图上画导线通道，但会丢网络，已经放弃（约 multisheet.rs:340）。

### 5.2 摆放

见 `lib.rs:885-918`：

1. **母题聚类**（`build_clusters` lib.rs:1994；识别在 `synth-ir/src/clusters.rs:191`）。成员相对锚点的位置：

   | 成员 | 位置 |
   |---|---|
   | 去耦/轨/负载电容 | 锚点下方 |
   | 上拉、限流电阻 | 锚点上方 |
   | ESD | 锚点左侧 |
   | 复位网络 | 锚点复位脚所在一侧 |

   识别的母题包括 LED+R、USB+ESD、LDO+电容、晶振+2C、I²C 上拉、分压器。
2. **分列（信号流）**（`layer_for` lib.rs:4375）：

   | 列 | 元件 |
   |---|---|
   | 0 | 连接器，以及只有 power_out 的源 |
   | 1 | 稳压器（power_out + power_in） |
   | 2 | MCU / IC / 传感器 |
   | 3 | 无源及其他 |

3. **减少交叉**：barycenter 最多 4 轮，**功能网络权重 16，电源/地权重 1**（lib.rs:2211-2213, 2308-2398）。
4. **定 y**：Brandes–Köpf（lib.rs:2437-2680）。
5. **间距与网格**：簇间距 dx 60（组内 40）、dy 70，成员间隙 17.78，页边 20，目标宽高比 1.4（lib.rs:377-492）。元件吸附 2.54 mm，引脚端点 1.27 mm（lib.rs:657）。
6. **分区**：每个 group 区域先在局部布局，再做 shelf 打包；区域有框、标题和 notes。
7. **压缩**：MaxRects（`compact.rs`），簇作为刚体移动，宽高比漂移不超过 0.6。
8. **旋转**：两脚电源件 VCC 朝上、GND 朝下（lib.rs:1851-1905）；LED 链竖直；连接器锁 0°，使 1→N 自上而下（lib.rs:1672）。
9. **文字防重叠**（lib.rs:1309-1336）：按 1.27 mm 步长最多推 16 次，然后缩小字号，最后丢弃。
10. **自报的局限**：
    - 摆放搜索常常退化为“三个逐字节相同的方案”；
    - barycenter 通常不起作用；
    - `placement_cost` 写好了但没接入。

### 5.3 连线

- **电源**：只用电源符号，每个端点一个，从不画线（`classify_power_net` lib.rs:3877）。
- **信号**（`classify_net_labels` lib.rs:4054）：
  - 有分组时：**组内画导线，组间用标签**；
  - 无分组时：≥3 个端点或跨度 >80 mm 改用标签（lib.rs:4030-4037）。
- **剩余网络的布线**：
  1. Hashimoto–Stevens 通道路由；
  2. 1.27 mm 网格 A*，**拐弯代价 4、交叉代价 40**（`route/grid.rs:7-22`）；
  3. L 形兜底。
- **拥堵回退**：**与 >2 个异网段相交的网络整体改为标签**（`CROSSING_TRUNCATION_THRESHOLD = 2`，route/mod.rs:1867；文档写 5，代码是 2）。
- **标签名**：取引脚功能（`i2c_scl → SCL`），否则取引脚名，否则 `NET_n`（`pick_net_label` lib.rs:4127）；全局去重，避免两个不同网络在 KiCad 里被合并。
- **标签类型**：
  - 单页：只有本地 label，带 5.08 mm stub（schematic.rs:1043-1110）；
  - 多页：子图用 hierarchical_label，根图用本地 label；
  - global_label 基本不用；
  - 总线导出为 `bus_alias`。

### 5.4 KiCad 导出

- **UUID**：UUIDv5(项目, 类型, 名称)，重复导出逐字节相同。
- **title**：`board — sheet`；日期刻意留空（为了确定性）；comment 写 “N components / M nets”。
- **Ref/Value 字段**：放在引脚较少的那条轴上，Ref 在前，间距 2.54（没有引脚的一侧）或 3.81（有引脚的一侧）（schematic.rs:1734-1840）。
- **其他**：PWR_FLAG、no_connect、分组矩形 + 粗体标题 + notes 文本、Okabe–Ito 网络类配色、引脚 alternate 显示功能名。

### 5.5 度量

- **原理图 lint `E-SYNTH-SCHEM-001..015`**（`schem_erc.rs:83-116`）：

  | 编号 | 规则 |
  |---|---|
  | 001 | 电源符号倒置 |
  | 002 | 交叉 >5 |
  | 003 | 去耦电容离 IC 本体 >25 mm |
  | 004 | 显式导线网络 >100 mm |
  | 005 | 结点度 >3 |
  | 006 | 结点落在异网导线上 |
  | 007 | 内容出页 |
  | 008 | 网名非大写 |
  | 009 | 网名 >16 字符 |
  | 010 | 歧义的 VCC/VDD |
  | 011 | 文字重叠 |
  | 012 | 页面填充率 <0.45 |
  | 013 | 分组区域重叠，或元件在区域外 |
  | 014 | 自动网名 `net_N` 显示在图上 |
  | 015 | 分组没有 notes |

- **`LayoutScore`**：交叉数、总线长、标签 stub 数，CI 有基线（`fixtures/layout/score_baselines.json`）。所有夹具都是 0 交叉，但这是靠“拥堵就转标签”换来的。
- **视觉闭环**：kicad-cli 导出 SVG → resvg 转 PNG → agent 看图后执行布局操作 → 与像素基线比较。
- **自承缺口**：规则读的是内部 Layout，而**不是导出后的文件**；不建模标签位置，因此测不到标签碰撞；**没有元件本体重叠检查**。

### 5.6 例子输出

| 夹具 | 符号 | 电源符号 | 导线 | 标签 |
|---|---|---|---|---|
| named_nets | 18 | 13 | 11 | 0 |
| docs_blocks | 4 | — | 16 | 4（另有 5 段文字、1 个框） |

- 小设计以“电源符号 + 标签”为主，导线只出现在簇内短连接。
- 风格整洁但偏重标签，接近手画的“电源处处打符号、总线全用标签”。

---

## 6. pcbpilot `kicad sch-build` 现状（只读审阅）

worktree：`~/Github_Working/pcbpilot-wt/kicad-sch-build`，HEAD f6f7b4f2，另有未提交改动。

| 方面 | 现状 | 文件 |
|---|---|---|
| 输入 | spec：pages / zones（每区一个 core）/ parts / blocks / nets / rails；`Near` 是唯一的摆放提示 | `cmd_kicad_sch_build.go` sbSpec |
| 区内摆放 | 离线规划器 `PlanSchematicLayout`：以 core 为中心，外设按“宿主引脚前方槽位”摆放；anneal + repair 搜索、迷宫布线、岛命名。外设 ≤3 脚时允许任意旋转；**多单元器件不进规划器**，退回 grid | `kicad_sch_build_layout.go` planZone；`sch_layout_anneal.go`；`sch_layout_maze.go`；`sch_layout_engine_*.go` |
| 网络策略 | 地 → local_ground；电源 → local_power；**只在本区且本区 ≥2 脚 → direct（导线）；否则 module_port（标签）** | `zoneInputs()` |
| 区间连接 | **永远用标签**，同页相邻区也不画线 | 同上 |
| 区在页上排布 | `kicadPackZones`：按阅读顺序逐行排，选能容纳的最小纸张；**不考虑信号流或区间连接** | `sbPackFresh` / `packZones` |
| 多页 | 根图放 4 列网格的 `sheet` 方框（50.8×15.24），**没有图纸引脚**；跨页网络用 **global label**（`net_port_bi`）；子图标题 = 总标题 — 页名 | `writeSbProject`、`renderSbPage` |
| 电源 | 电源/地符号；未被驱动的轨在区下方集中放一排 PWR_FLAG | `renderSbPage` |
| 美学 | 已有 `pkg/schaes`（W1–W8/L1–L7/N1–N3）、bus lane、align、label split；**sch-build 没有打开**（`SchematicLayoutInput.Aesthetics/Optimization` 都没设置） | `sch_layout_aesthetics.go`、`sch_layout_bus_lane.go` |
| 几何门槛 | `kicad.CheckSchematic`：symbol-overlap、wire-through-body、diagonal、off-grid、label/text overlap、pin-on-wire、wire-end-on-wire、wire-overlap、title-block、off-page | `internal/kicad/schquality.go` |
| 重布线 | `schroute.go`：拖动符号后按岛重布 1.27 mm 网格 Manhattan 树，避开本体、字段和标签；没有干净路径时回退为标签 | `internal/kicad/schroute.go` |
| 硬门槛 | KiCad 网表 == spec、ERC、按区的 sch-check | `kicad_sch_build_render.go` |

**与外部工具的对比**：
- **区内连线质量领先**：maze + 命名岛 + 18 项指标，比 tscircuit 的局部排序和 synth 的“拥堵即转标签”都更严。
- **落后之处**：
  - 顶层图和层次都是“平坦 + 全局标签”；
  - 区间只有标签；
  - 页面级排布没有信号流；
  - 没有母题（去耦行、晶振、LED+R、分压器、上拉）的模板化摆放；
  - 两脚电源件旋转没有强制；
  - 质量规则只在区内几何上评估（`sbZoneFindings`），缺少整页和工程语义层面的规则（去耦距离、信号流方向、电源朝向、自动网名等）。

---

## 7. 工程师级规则集（建议 pcbpilot 强制执行的可量化检查）

**原则**：
- 全部规则都在**最终写出的 `.kicad_sch` 几何**上计算，弥补 synth 自承的缺口。实现复用 `kicad.SchScene` / `schquality.go` 的解析。
- 已有的 `schquality` finding 记作“[已有]”，新规则编号为 **EG-xx**。
- 阈值分 **hard**（门槛，不过即失败）和 **soft**（报告 + 计分）。
- 单位 mm；“引脚端点”指 KiCad pin 的连接点。
- 网络语义（电源/地/信号、驱动方向）从 spec 的 `Kind` 和引脚 `Type` 取得。

| ID | 规则 | 定义 | 如何从 .kicad_sch 计算 | 阈值 |
|---|---|---|---|---|
| EG-01 | 层次一致性 | 子图每个 hierarchical_label 都在父图对应 sheet 符号上有同名 `(pin …)`，反之亦然；信号网络不得使用 global_label | 解析父图 `(sheet (pin "N" …))` 和子图 `(hierarchical_label "N")`，求集合差；统计 `global_label` 中 `Kind≠power/ground` 的数量 | hard：差集 = 0，信号 global_label = 0 |
| EG-02 | 顶层图可读性 | 根图上每个 sheet 符号都有引脚；输入在左、输出在右、电源在上、地在下；引脚名 = 网络名；排序稳定 | 对每个 sheet pin，从 `(at x y angle)` 判断落在哪条边，与 pin 的 shape（input/output/bidirectional/passive）和网络 Kind 比对 | hard：没有引脚的 sheet = 0；soft：边位正确率 ≥ 90% |
| EG-03 | 分页规模 | 每页非电源符号数和页面填充率都合理 | 统计 `(symbol (lib_id …))` 中排除 `power:` 和 `#PWR` 的数量；填充率 = 内容包围盒面积 / 图框内可用面积 | soft：每页 ≤ 40 个符号；填充率 0.30–0.75（synth 下限 0.45 偏严，因为我们有区框） |
| EG-04 | 信号流左→右 | 有明确驱动方向的信号网络（output/power_out → input/power_in），驱动脚 x 不大于接收脚 x；连接器在页面左右外侧 | 从 spec 引脚类型找驱动和接收引脚，取各自所在符号中心 x 比较；连接器中心 x 落在页面宽度外侧 25% 带内 | soft：符合率 ≥ 80%；连接器在边带 ≥ 80%（与 schaes L1 对齐） |
| EG-05 | 电源向上/地向下 | 电源符号的引脚 stub 向下接（符号在上方）；地符号在下方；只有一脚接电源/地的两脚件，电源脚 y 小于另一脚（KiCad y 向下），地脚 y 大于另一脚 | 电源符号：符号 `at` 相对其连接点的方向（y 偏移符号）；两脚件：比较两个引脚端点的 y | hard：倒置电源/地符号 = 0（synth 001）；soft：两脚件朝向正确率 ≥ 95% |
| EG-06 | 去耦就近 | 两脚电容一脚在电源 P、一脚在地，且某 IC 有引脚在 P 上：电容本体到该 IC 最近 P 引脚端点的距离 | 包围盒到点的距离，取所有候选 IC 中的最小值 | hard：≤ 25 mm（synth 003）；soft 目标 ≤ 12.7 mm，且在该引脚所在侧（见 EG-07） |
| EG-07 | 引脚侧感知 | 与 IC 引脚 q 直连（同一导线岛）的外设，位于 q 外法线方向的半平面内 | q 的朝向由 pin angle 加符号旋转得到 n；外设中心 c 满足 (c−q)·n > 0 | soft：≥ 95% |
| EG-08 | 异网交叉 | 不同网络的导线严格相交（不在端点），不计结点 | 线段两两求交，排除同网 | hard：每个网络 ≤ 2（synth 截断阈值）；每页 ≤ 5（synth 002）；目标 0 |
| EG-09 | 四通/歧义结点 | 结点的连接度 ≥ 4；结点落在异网导线上 | 统计每个 junction 处的段端点 + 内部穿过数 | hard：= 0（synth 005/006；schaes W3） |
| EG-10 | T 结点与悬挂 | 每个 T 形连接都有 junction；不存在度为 2 的多余 junction；不存在悬空线端 | [已有] pin-on-wire / wire-end-on-wire 的扩展：线端点度 = 1 且不在引脚、标签或电源符号上即为悬空 | hard：缺 junction = 0，悬空 = 0，多余 junction = 0 |
| EG-11 | 画线还是贴标签 | ① 同页、同区或相邻区、2–3 脚的信号网络，最近对距离 ≤ 30 mm 且简单 L 路径异网交叉 ≤ 1，应画导线；② 导线树长 > 100 mm 或交叉 > 2 的信号网络应改为标签 | ①：对每个标签网络，取其所有标签端引脚的成对曼哈顿距离（tscircuit maxMsp 2.4 ≈ 30 mm）；②：按岛求导线总长和交叉数 | soft：①违例 ≤ 10%（schaes N2）；②违例 = 0（synth 004、schaes N1） |
| EG-12 | 每岛一标签 | 一个导线岛上同一网名的标签只出现一次；同页同名的多个岛各自有标签 | 按岛统计 label 数 | hard：岛上重复标签 = 0；无名岛（信号网络）= 0 |
| EG-13 | 标签附着与朝向 | 标签落在线端或 stub 端（不悬空、不在线中间造成歧义）；信号标签文字水平（angle 0/180）；标签方向垂直于所在 stub | label `at` 与线端重合；angle ∈ {0, 180} 的比例 | hard：悬空标签 = 0；soft：水平比例 ≥ 90%（schaes L2） |
| EG-14 | 标签与文字碰撞 | 标签矩形与异网导线、本体、其他文字相交 | [已有] label-overlap / text-over-symbol；补“异网导线穿过标签矩形”（tscircuit 不变式） | hard：= 0 |
| EG-15 | 拐弯与绕行 | 每条两端连接的拐弯数；绕行比 = 导线长 / 曼哈顿距离 | 按岛拆成端点对 | soft：平均拐弯 ≤ 1.5，单条 ≤ 3；绕行比 ≤ 1.6（schaes W1/W6） |
| EG-16 | 网格 | 引脚端点和线端在 1.27 mm 上；符号原点在 1.27 mm 上（KiCad 标准件的引脚在 2.54 mm 网格，原点同样在 2.54 mm） | [已有] off-grid | hard：= 0 |
| EG-17 | 字段文字 | Ref/Value 不与任何东西重叠，旋转 0° 或 90°，字号 1.27 | [已有] text-overlap；补字号和旋转统计 | hard：重叠 = 0 |
| EG-18 | 区框 | 区矩形两两不重叠；每个元件包围盒落在自己区的矩形内；每个区有标题 | 读 `rectangle` 和 `text`，与元件的 `pcbpilot_zone` 字段对照 | hard：= 0（synth 013） |
| EG-19 | 区说明 | 每个区有一段 notes（功能、关键参数、设计依据） | 区框内除标题外的 `text` 数量 ≥ 1 | soft：覆盖率 100%（synth 015） |
| EG-20 | 网名卫生 | 图上不出现自动网名（`Net-(`、`N$`、`NET_\d+`）；名字大写，长度 ≤ 16；不使用歧义名 VCC/VDD（应写成 +3V3 之类） | 扫描 label、global/hier label 和电源符号的 value | hard：自动名 = 0；soft：其余（synth 008/009/010/014） |
| EG-21 | 总线成列 | 索引组或协议组的标签同向、同列、等距 | 已有 schaes N3 / bus lane | soft：完整 lane 比例 ≥ 80% |
| EG-22 | 标题栏与页码 | title、date、rev、company 非空；多页时每页的 page 编号唯一且连续；子图标题包含页名 | 解析 `title_block` 和根图 `(instances … (page "n"))` | hard：缺项 = 0，页码重复 = 0 |
| EG-23 | 页面边界 | 内容距图框 ≥ 10 mm，不进入标题栏 | [已有] off-page / title-block | hard：= 0 |
| EG-24 | 本体重叠 | 符号本体两两不相交 | [已有] symbol-overlap | hard：= 0 |

**计分建议**：
- hard 规则全部为 0 才通过。
- soft 规则按 schaes 的 profile 权重汇总成 0–100 分，写进 `sch-build` 报告的 `engineerGrade` 门槛；默认低于 70 时 warn，不阻断，等收集到基线后再收紧。

---

## 8. sch-build 的算法改进（按收益/成本排序）

> 只给出方向、文件和工作量，不改代码。工作量：S ≤ 1 天，M 2–4 天，L ≥ 1 周。

1. **打开已有的美学与优化通道**（S，收益高，成本最低）
   - **做法**：在 `kicad_sch_build_layout.go` 的 `planZone` 中设置 `in.Aesthetics`（profile balanced）和 `in.Optimization`。
   - **约束**：受 `--planner-timeout` 约束，超时就保留基线结果。
   - **效果**：bus lane、align、label split、marker pass 都已实现，并且只在 `admit()` 不变差时才接受。
   - **验证**：esp32-mini 的 schaes 分数上升，ERC 和网表门槛不变。

2. **两脚电源/地件强制旋转 + 连接器锁 0°**（S）
   - **问题**：`planZone` 目前对 ≤3 脚外设允许 {0,90,180,270}。
   - **做法**：只有一脚在 power/ground 的两脚件，只允许“电源脚朝上 / 地脚朝下”的那个角度（tscircuit `getPowerGroundForcedRotation`，synth lib.rs:1851）；连接器固定 0°。
   - **验证**：EG-05。

3. **真正的层次图（替换 global label）**（M，收益最大）
   - **修改位置**：`kicad_sch_build_render.go` 的 `writeSbProject` 和 `renderSbPage`。
   - **子图**：跨页信号网络从 `net_port_bi`（global）改为 `LabelHier`。`internal/kicad/schwrite.go:593` 已支持；方向按驱动关系设为 input/output/bidirectional。
   - **根图 sheet 符号**：
     - 写入 `(pin "NET" input|output|bidirectional (at …))`；
     - 引脚位置：输入在左边、输出在右边、电源在上边、地在下边，各边按网络名或驱动顺序排序，间距 2.54；
     - 高度 = max(左, 右) 引脚数 × 2.54 + 5.08。
   - **根图连接**：两个 sheet 之间共享的网络：
     - 在根图上画导线：根图对象少，可直接复用 `internal/kicad/schroute.go` 的 Manhattan 树路由；
     - 或者按 synth 的做法：pin + 5.08 stub + 本地 label。
     - 建议：先做 label 版本，再尝试 wire 版本，交叉 > 2 时回退 label。
   - **根图方框排布**：按 §8-5 的信号流分列，不再用 4 列网格。
   - **电源网络**：继续用电源符号全局相连，不放图纸引脚（与 synth 一致）。
   - **验证**：EG-01/02，KiCad 网表 == spec。

4. **同页区间画导线（label → wire 回收）**（M）
   - **做法**：在 `packZones` 之后、渲染之前，对同页 module_port 网络：
     - 若端点只有 2–3 个，最近对距离 ≤ 30 mm，且用 schroute 的 Manhattan 树（避开区框、本体、字段）能找到异网交叉 ≤ 1 的路径，就把两端标签换成导线；否则保留标签。
     - 思路来自 tscircuit `NetLabelToTraceSolver` 和 synth 的“交叉 >2 回退”。
   - **前提**：区框要允许导线穿越（或者在框边开口），需要调整 `schquality` 对区框的处理。
   - **文件**：新增 `kicad_sch_build_interzone.go`；`renderSbPage` 需要接受额外的 page 级导线。
   - **验证**：EG-11①违例下降，EG-08/14 保持为 0。

5. **页面级区排布：信号流分列 + barycenter**（S–M）
   - **问题**：`sbPackFresh` / `packZones` 目前按阅读顺序逐行排。
   - **列**：给每个区打 layer（synth `layer_for` 思路）：
     - 0：连接器或输入区，含只有 power_out 的器件；
     - 1：稳压区；
     - 2：MCU/IC 区；
     - 3：外设区。
     - 也可以由 spec 可选字段 `zone.flow` 覆盖。
   - **列内顺序**：用区间网络做加权 barycenter（信号 16、电源 1）排序，然后做 shelf 打包。
   - **验证**：EG-04 区级版本（区中心 x 随 layer 递增），以及区间标签对的平均距离下降。

6. **母题识别 → 自动生成摆放提示**（M）
   - **做法**：新增 `sch_layout_motifs.go`，在 `planZone` 之前识别：
     - 去耦组：按 IC 电源脚分组，**排成一行，正极脚落在同一水平轨线上，顺序跟随引脚顺序**（tscircuit DecouplingCapRow）；
     - 晶振 + 2 个负载电容；
     - LED + 限流电阻（竖直链）；
     - 分压器（竖直，上端在电源）；
     - I²C 和复位上拉（宿主引脚上方）；
     - ESD（连接器侧）；
     - 稳压器输入/输出电容（左入右出）。
   - **输出**：规划器现有的 `SchematicLayoutPeripheral` hint 或 `Near` 关系，不直接给坐标，保留规划器的合法性门槛。
   - **验证**：EG-06（目标 ≤ 12.7 mm）、EG-07，schaes L3 对齐分上升。

7. **整页工程规则门槛 `engineerGrade`**（M）
   - **做法**：在 `internal/kicad` 新增 `scheng.go`，实现 §7 中尚未有的规则（EG-01/02/04/05/06/07/11/12/13/18/19/20/22），复用 `SchScene`。
   - **接入**：`sbQualityCheck` 之后作为新 gate，并加入 `kicad sch-check --engineer`。
   - **原因**：现有检查只覆盖区内几何（`sbZoneFindings`）和通用几何（`schquality`）。

8. **多单元器件进入规划器**（M–L）
   - **问题**：`planZone` 遇到 `p.Multi` 就报错，导致整区退回 grid（运放、逻辑门、MCU 分单元时）。
   - **做法**：每个单元作为独立 component，电源单元单独放（或放在区角）。
   - **文件**：`kicad_sch_build_layout.go` planZone；`sbMeasure.get` 已支持 unit。

9. **顶层补充视图：电源树**（S）
   - **做法**：根图（或封面页）用 text/rect 画出电源树：输入 → 保护 → 稳压 → 各轨电压/电流 → 负载区。数据来自 spec 的 rails 和 nets。
   - **说明**：思路来自 atopile 的 power_tree exporter。工程师评审时第一眼就看这张图。

10. **自动纸张升级与超量分页**（S–M）
    - **做法**：单页内容超过 A3 时提示（或在 `--auto-pages` 下）按 zone 自动拆页；拆页只是重新分配 `zone.page`，然后走 §8-3 的层次图流程。
    - **说明**：这一步借鉴 synth 的“先放大、后拆分”。拆页是重排，不是缩放，因此不会让字变小。

11. **区说明与网名卫生**（S）
    - **做法**：
      - spec 中 `zone.notes` 渲染为区框内的文本（EG-19）；
      - 标签名优先取引脚功能名（synth `pick_net_label`）；
      - 对 spec 中的自动名或歧义名在加载时报错（EG-20）。

**推荐顺序**：1 → 2 → 7（先能量化）→ 3 → 5 → 4 → 6 → 8 → 9/10/11。

前三项合计约 3 天，就能让现有 esp32-mini 的输出在“电源朝向、整页度量”两方面可验收；3–5 项解决“看起来像一张工程图”的结构性问题（顶层图、区间连线、信号流）。

---

## 9. 附：各工具对照表

| 维度 | circuit-synth | tscircuit | synth | pcbpilot sch-build |
|---|---|---|---|---|
| 层次 | 子电路对应子图；引脚单侧且错位 | group 刚体；showAsSchematicBox | 放不下才分页；hier label + 根图 label | 多页 = 无引脚方框 + global label |
| 摆放 | 面积排序文本流 | 母题 + 分区打包 + 回滚式对齐 | 母题 + 功率流分列 + barycenter + BK | 以 core 为中心的槽位 + anneal/repair |
| 电源/地 | 电源符号，旋转随引脚 | 强制旋转 + y± 标签 | 电源符号 + 两脚件旋转 | 电源/地符号（旋转未强制） |
| 去耦 | 无规则 | 去耦行，正极对齐 | 锚点下方 | 规划器就近（无行模板） |
| 连线 | 无（全标签） | MST + 距离阈值；BFS 修折线 | 组内导线、组间标签；A*（拐弯 4、交叉 40）；交叉 >2 转标签 | 区内迷宫布线；区间全标签 |
| 标签 | 全 hierarchical | 每连通分量一个，垂直于导线段，电源取最外侧 | 本地/层次，按引脚功能命名 | 命名岛 + bus lane（美学关闭） |
| 度量 | 无 | 局部排序 + 标签碰撞不变式 | 15 条 lint + LayoutScore + 视觉闭环 | schquality 14 类 + schaes 18 项（区级） |
| 确定性 | uuid4（否） | 是 | UUIDv5（是） | 状态文件中 UUID 稳定（是） |
