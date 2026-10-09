# 外部工具原理调研：布线（TraceMaker / fastroute）与原理图生成（5 个 AI/MCP 工具）→ pcbpilot 改进清单（2026-10-09）

> 范围：用户已解除 clean-room 限制，允许阅读其他工具的源码。本文**只记录原理**，没有把任何代码搬进 pcbpilot，也没有改 pcbpilot 仓库（本文件除外）。
> 6 个工具都有源码，所以**没有用到 REA 逆向**。唯一的黑盒是 kicad-copilot 的布局引擎：它是闭源云服务（见 §5），本地没有二进制可逆，只能从它的 JSON schema 推断。按隐私原则，没有向该服务发送任何数据。
>
> | 工具 | 位置 | 版本 / commit |
> |---|---|---|
> | TraceMaker | `~/.pcbpilot/tracemaker/src-0.9.0/TraceMaker-0.9.0` | v0.9.0 tag 源码包 |
> | fastroute | `~/.pcbpilot/study/fastroute` | 浅克隆 HEAD（二进制是 0.1.13） |
> | kicad-copilot、KiCad-AI-Assistant（kcaa）、mcp-server-kicad、KiCAD-MCP-Server、schem | `~/.pcbpilot/study/<名>` | 浅克隆 HEAD（2026-10-09） |
> | pcbpilot | `~/Github_Working/pcbpilot`（dev）；`~/Github_Working/pcbpilot-wt/kicad-sch-build`（只读，含未提交改动） | — |
>
> 下文路径：外部工具相对各自仓库根目录；pcbpilot 相对仓库根目录；**WT** 指 kicad-sch-build 工作树。
> 基准数据在 `~/Github_Working/pcbpilot-wt/bench/tracemaker-vs-fastroute-2026-10-09*`，下文简称“基准”。
> 同日另一篇调研 `docs/research/engineer-grade-schematic-2026-10-09.md` 讲原理图美观（synth、tscircuit、circuit-synth）。本文只在 §10、§11 引用它的结论，不重复。

---

## 0. 结论速览

### 布线

1. **TraceMaker 快、布得通，主要不是靠 GPU。** 它的优势来自三点：
   - **八路变体组合赛跑**：8 个不同配置的变体在 8 个线程上同时跑，谁先全部布通就停。
   - **惰性、带缓存的精确障碍判定**：不预先栅格化障碍。
   - **持续累积 history 的“先提交者胜、受害者重排”式协商。**

   GPU 只用来算 cost-to-go 启发式场。PicoRick 上整趟只占约 3 s，而且加了 `.kicad_dru` 自定义规则后会被整个关掉（实测 `fields 0 GPU`），结果仍然是 60 s 全通。
2. **fastroute 慢，而且容易留下长尾。**
   - 它是 Freerouting 的忠实移植：无网格的“房间/门”搜索，每一步都要算多边形，每个连接都重建搜索库。
   - 它没有 present/history 拥塞代价；作者试过 PathFinder，效果变差。rip-up 代价反而逐趟**变贵**。
   - 多起点（multi-start）在第一次跑完之后才开始，而且每个变体重做扇出、串行跑各趟。

   PicoRick 日志里，141 s 的构成是：首跑 50 s（剩 6 个未通）+ 多起点约 90 s（3 个变体，最好剩 5 个）+ 优化器 0.9 s。5 个未通全在 U303 的 +1V1/+3V3/VREF 焊盘附近，最后 10 多趟每趟“routed 0”，是在空转。
3. **两家的 neck-down 都不符合 pcbpilot 的意图门禁。**
   - TraceMaker 的 neck-down 作用于**整条连接**，宽度降到板最小线宽或 0.15 mm（`src/route/router.cpp:179-183,3242-3265`）。没有按网络类的开关；`.kicad_dru` 里的 `track_width min` 在布线时根本不读。
   - fastroute 有 `--no-neckdown-classes`。但默认开着的“fanout micro neck-down”在任何插入失败处都会把线缩到类线宽的 3/4、3/5、1/2（`crates/fr-engine/src/autoroute/inserter.rs:181-246`）。`min_trace_width_um` 只是下限，不是禁令。
4. **pcbpilot 应该驱动 TraceMaker，但前提是给它打一个小补丁**（约 40–80 行，S 级），加上：
   - 按网络类禁止 neck-down；
   - neck 宽度下限取 `.kicad_dru` 的 `track_width min`；
   - 显式差分对列表。

   补丁先在本地构建脚本里维护，同时提交上游 PR。没有补丁时，TraceMaker 只能作为 `--router auto` 的后备，并且一定会被 `intent-widths` 门禁拦下（基准 T4：Power3V3 有 163 mm 线段是 0.15 mm）。

### 原理图

1. **pcbpilot 原理图慢的主因不是算法，是交互模型。**
   - 旧的 S0–S6 流程大约 80 个 `sch` 子命令，每个一轮 LLM。
   - 每次写入要跑 2 次 `kicad-cli` 网表导出（0.4–0.6 s 一次）。
   - `--fix-pwr-flag` 每个旗子一个事务（Gas A 上 24 s）。

   五个外部工具里**没有一个**真正自动摆放原理图符号：
   - mcp-server-kicad、KiCAD-MCP-Server、kcaa 都由模型或计划给坐标；
   - kicad-copilot 交给闭源云端的 ELK 分层布局；
   - schem 根本不出图。

   它们值得学的是**交互形态**：
   - 模型只写“引脚 → 网名 + 功能块”；
   - 每个网络一次批量调用，做到全成或全不成；
   - 写入前先在内存里做连通性校验；
   - 用快照做事务回滚；
   - 读回时给模型极简 JSON。
2. pcbpilot 的 `kicad sch-build` 分支（WT）已经走在这条路上（spec → 一次调用出完整工程），比所有被调研工具都完整。现在要做的是：
   - 把它收尾、合并，作为默认路径；
   - 把逐写入的 kicad-cli 校验换成进程内连通性模型；
   - 把区（zone）的页面排布从“按 spec 顺序成行”改成按信号流分层。

### 门禁

pcbpilot 只有 `kicad route` 跑全链门禁。现在：
- 外部 `--router` 命令会丢掉 `route-complete`；
- 内置 pcbauto 路由器只有自己的 verdict。

接入 TraceMaker 时应该先抽出一个 **RouterResult 接缝**，让所有布线器都走同一个 `qualityGates` → `signoff`。详见 §12。

---

## 1. 基准解读（PicoRick，4 层，359 个连接）

数据来自 `bench/tracemaker-vs-fastroute-2026-10-09-data/logs/`。

| 现象 | 证据 | 原因（见后文） |
|---|---|---|
| TM T4 用 57 s 布通全部 | `tm-soft-120-diffpairs.log`：`portfolio: 8 variants on 8 threads`；variant 5（“fast bends, cheap vias, 2x pitch”，pitch 0.1 mm）59.5 s 布通 359，其余变体 24–33 s 停在 299–343 | 组合赛跑 + 首个全通即停 |
| TM T2/T3 不开差分对，跑满 120/300 s 仍剩 1 个 | `tm-soft-120.log`：8 个变体都没全通；最好的是 variant 7（pitch 0.05），`SWCLK ripped up by +1V1 and not routed again` | 受害者 rip 达到 `rip_cap` 后不再重布；开差分对改变了布线顺序，碰巧让 2×pitch 的变体全通。**带运气成分** |
| TM T1（硬铺铜）52 个未通 | `tm-hardzones-300.log`：一串 `boxed in … would rip a connection already ripped too often` | 不加 `--soft-zones` 时，已铺好的 GND 平面算作障碍，信号过孔穿不下去 |
| TM 加 dru 后 GPU 场被关掉，时间几乎不变 | `tm-soft-120-diffpairs-dru.log`：`fields 0 GPU + 0 CPU`；60 s 对 57 s | 任何非 `disallow` 的自定义规则都会置 `needs_exact_`，同时关掉类缓存和场（`src/drc/rule_engine.cpp:343`、`router.cpp:226,1009`）。GPU 场本来就只占约 3 s |
| fastroute F2 用 141 s，剩 5 个 | `fr-minw150-300.log`：扇出 3.8 s → 自动布线 26 趟、46 s 剩 6 个（第 16–26 趟都是 routed 0）→ 多起点 3 个变体 64–83 s（最好剩 5）→ 优化器 0.9 s | 多起点在首跑之后串行开始；停滞判据要等 10 趟没有进步才停；尾部全是“blocked”项 |
| fastroute 未通全在 U303 电源脚 | `fr-minw150-300.json`：+1V1 ×3、+3V3 ×1、RX_IC1_VREF ×1，长度 1.5–3.2 mm | 密脚 QFN 出线，加上 0.4 mm 的 Power 类线宽，在 150 µm 下限时挤不出去。日志说这些项 `cannot be routed even on the board as loaded`：**这是布局问题，路由器怎么跑都没用** |
| pcbpilot 自己的 `kicad route` 路径 | `internal/app/cmd_kicad_route.go:638-641` 注释：PicoRick 主跑 112 s + multi-start 143 s，并行后取 max | `--threads` 默认 1（`:139`）是为了可复现，主跑因此是单线程；比基准里 fastroute 默认多线程还慢 |

---

## 2. TraceMaker 0.9.0：原理

### 2.1 搜索

**网格**
- 网格是八方向（octilinear）的晶格。
- pitch = 各网络类中最小的（线宽 + 间距）÷ 6，向下取整到 5 µm，再夹在 25–100 µm 之间（`src/route/router.cpp:247`）。
- 组合变体会把 pitch 改成 2× 或 0.5×（`router.cpp:250-251,3416-3420`）。
- 搜索状态是（层，格点，到达方向）。禁止 135° 和 180° 转弯（`router.cpp:881,1085`）。

**障碍不预先栅格化**
- 每个格点按需做一次精确的圆盘判定（`point_state`，`router.cpp:572`）。判定半径 = 半线宽 + 0.71·pitch，保证相邻格点之间的那一步也被覆盖。
- 判定失败时再用 margin = 0 判一次“紧”判定。只通过紧判定的点额外加 3·pitch 的代价。
- 判定结果按（网络类指针，线宽）缓存（`ClassCache`，`router.cpp:97-103,541`）。缓存值只有三种：`kFree`、`kBlocked`、某个网号（“只对该网合法”）。所以**同一类的所有网共享一份缓存**（`src/drc/obstacles.cpp:668`）。
- 已布铜放在单独的索引里。另有一张“附近有已布铜”计数栅格，用来跳过不必要的查询（`router.cpp:262-292`）。

**代价**（整数 nm，`router.cpp:1005-1153`）
- 直步 = pitch；斜步 = pitch·√2；每层有千分比系数（`--layer-cost`）。
- 45° 转弯 + 半步；90° 转弯 + 2 步（`:1099`）。
- 过孔默认 3 mm（`via_cost_mm`），清理阶段 ×10（`:2483`）。盘中孔算 4 个过孔（`:792`）。
- 协商态下，压到他网铜皮的代价 = `soft_cost + 4·hist`（`:627`）。
- history = 次数 × 2·pitch（`:532-536`）。

**算法**
- 单向 A*，用 `std::priority_queue`（`:1037`）。双向 A*、radix heap、4 叉堆都试过并回退（`docs/05-routing.md:828`）。
- 启发式：小窗口用 octile 距离；窗口 ≥ 60k 格时用 cost-to-go 场。场说“不可达”的地方回退到 octile，**从不用场来剪枝**（`:1009-1032`）。
- 预计会失败的严格搜索，先做一次精确洪泛可达性检查：每格只访问一次就能证明“不可达”，避免 A* 把整个窗口耗尽（`:944-1004`）。
- 失败分类：`Enclosed` 表示 open 表在没碰到窗口边界的情况下就耗尽了，此时扩大窗口也没用（`:1163`）。

**窗口与负载上限**
- 窗口 = 焊盘包围盒 + {2, 6, 20, ∞} mm + 长度/4（`:1515`）。严格搜索只用前两档，协商搜索用三档（`:1559`）。
- 每次搜索的扩展上限是 300 万次（`router.hpp:33`）。

**协商**
- 不是同时过载的 PathFinder，而是“提交者胜，冲突的受害者被 rip 并重新入队”。
- 冲突格的 history +1（`bump_history`，`:1410`），而且**一次运行内不衰减**；只在多样化重启时减半（`:3179-3182`）。
- 每条连接最多被 rip `rip_cap` = 8 次。`--first-nets` 指定的网不能被 rip。

**调度**
- 最多 12 趟：第 0 趟严格，之后各趟协商。失败的连接按失败次数从难到易重排。
- 之后是 6 次保留记忆的重启，再加最多 40 次“多样化”重启：清空 nogood、rip_cap +2、history 减半、打乱种子（`run`，`:3033,3159-3196,3306-3309`）。
- 全程保存最佳快照，最后返回它（`:3125-3145`）。

**顺序**
- 每个网先把已有铜簇连成 Prim MST，焊盘间距离用精确谓词二分得到（`plan`，`:348`；`pad_gap`，`:315`）。
- 排序可选：短的先、长的先、带抖动的短先；再叠加 KB 优先级和 `--first-nets`（`:426-458`）。

**失败记忆**
- 运行内有两种：
  - nogood：键 = 连接 + 模式 + 覆盖宽度 + 窗口内已布物哈希（`:1476-1510`）；
  - `learned_block`：精确判定撞上固定铜时，按（网，线宽）把那个格点永久封掉（`:1382`）。
- `--kb` 是跨运行的 SQLite（`src/learn/knowledge_base.hpp`）：
  - 记录每块板（按哈希）上失败过的连接，下次优先布；
  - 用 Thompson 采样选择跑哪几个变体（只在请求的变体少于 8 个时生效；`src/app/route_job.cpp:166-168,281,295-299`）。

### 2.2 neck-down（逐级放宽）

实际实现只有下面几级（`docs/05-routing.md:97-114` 里的 R0–R7 大部分还没做）：
1. 严格搜索；
2. 被围死或精确判定失败时，强制网格外的出线短线（`force_escapes`）；
3. **neck-down**：`width_override = neck_width(net)`，同时 `via_override = true`（`router.cpp:3242-3250`）；
4. 第 1 趟及以后：协商搜索；
5. 仍被围死：协商 + 出线 + neck 线宽 + neck 过孔，再失败就判死（`:3255-3279`）。

neck 宽度的计算（`router.cpp:179-183`）：
- `neck_width = max(板最小线宽, min(类线宽, 0.15 mm))`；
- 只有比类线宽窄时才生效；
- `track_width()` 对**这条连接的所有线段**都返回覆盖值（`:178`）。

所以缩线覆盖整条连接，不只是焊盘出线区。这和它自己的文档（`docs/05-routing.md:94-95`，说 neck 只在出线区）不一致，也解释了基准 T4 里 12 mm 长的 0.15 mm Power3V3 线段。出线规划器里有一份同样的公式（`src/route/escape.cpp:133-139`）。

**为什么 `.kicad_dru` 的 `track_width min` 不生效**
- 路由器取线宽只用 `class_width = max(类线宽, 板最小线宽)`（`router.cpp:177`），从不向 `RuleEngine` 要线宽。
- 自定义 `track_width` 只有两处读：DRC（`src/drc/drc.cpp:331`、`rule_engine.cpp:629-633`）和差分对规则（`src/route/diff_pair.cpp:35-39`）。
- 副作用：这类规则会置 `needs_exact_`，同时关掉类缓存和场，所以只会让布线变慢，对宽度没有作用。

**最小补丁位置**（下文 R2）
1. `neck_width()`：受保护的类返回 0，并让宽度下限 = `max(板最小线宽, rules.net_constraint(net,"track_width").min)`。
2. `:3244` 和 `:3260` 两处无条件的 `via_override = true` 加上同样的判断。
3. `escape.cpp:133` 的 `neck()` 做同样修改。
4. `RouterOptions` 加一个类名集合；`src/app/main.cpp` 和 `route_job.cpp` 加 `--no-neckdown-classes`。
5. 可选：`RouterOptions::pair_nets` 已经存在（`router.hpp:57-60`，目前只由 component-rules 填写），加一个 `--pairs FILE` 读进去，就能替代按网名后缀一刀切。

### 2.3 `--soft-zones`

- 铺铜填充在障碍判定里**直接不可见**：`copper_state`、`fixed_code`、`fixed_via_code` 和过孔孔壁检查都跳过 zone fill（`src/drc/obstacles.cpp:335,521,684,759`）。它不是“加代价”，而是“当作不存在”。因为这样，障碍缓存可以继续与网无关，速度不受影响。
- 规划时，面积 ≥ 1 mm² 的同网铺铜簇当作目标；搜索中，线宽圆盘离填充边缘留出 hw + pitch 的格点都算到达（`router.cpp:373-379,410,922-937`）。
- **它从不重铺**，连通性按旧的填充判断（`docs/05-routing.md:715-720`）。所以必须由外部重铺：KiCad 的 `--refill-zones` 或 pcbpilot 的 pour 步骤。

### 2.4 差分对

- 识别：网名尾部 P/N 或 +/− 互配（`rule_engine.cpp:360-373`；`diff_pair.cpp` 中的 `named_pairs`）。没有后缀的对只能从内部的 `pair_nets` 进来。
- 对规则（`pair_rule`，`diff_pair.cpp:24-77`）：
  - 线宽 = 类的差分线宽，被自定义 `track_width` 抬高；
  - 间距：`diff_pair_gap` → 类间距 → clearance；
  - 偏移 = ceil((w + gap)/2) + 1 µm。
- 耦合搜索（`route_pair`，`router.cpp:1868`）：
  - **一次 A* 搜中心线**，状态 =（层，格点，方向，哪一半在左）；
  - 动作有四种：直步、45° 后接 K 步直线（防止内侧斜角折叠）、成对过孔、换边（twist）；
  - 每一步在两条偏移线上都做精确检查，所以间距**由构造保证**。
- rip 一半时会连带另一半。清理阶段对被拆散的对重新耦合，失败就精确恢复（`:1444-1447,3216-3229,3326`）。
- skew：`tune_skew` 给短的一侧加蛇形线（`:2779,2679`）。

### 2.5 0.9.0 的 1.1–1.5× 加速

来自贡献者 PR #3（决策 D73，`docs/12-decisions.md:81`；`docs/05-routing.md:759-836`）。在 8 块 PCBench 板上测得 1.13–1.49×；网络类用通配模式的板约 2.2–2.4×。

| 改动 | 位置 | 效果 |
|---|---|---|
| **每个网只解析一次网络类**（原来每格都做通配/正则匹配） | `router.cpp:120-124,152-156`；`rule_engine.cpp:364` | 原来占约 80% 时间 |
| 过孔裸代价不可能改善其他层状态时，跳过过孔合法性检查（`via_useful`） | `router.cpp:1110-1118` | 原来占搜索约 35% |
| 记住上一次 `cache_for` 的结果；每线程一个探针对象池 | `router.cpp:538-556`；`obstacles.cpp:464-469` | — |
| 每格每次搜索只读一次 history；`fixed_via_code` 一遍扫完所有层 | `router.cpp:606`；`obstacles.cpp:737` | CPU −7% |
| 热路径不分配形状对象 | `src/geom/shape.cpp:158` | CPU −10% |
| 退化线段快速路径 | `shape.cpp:88` | 指令 −3.5% |

48 块测试板输出不变。但这是实测结论，不是保证：跳过过孔检查会改变扩展次数。

### 2.6 Metal / CUDA

- **GPU 只算 cost-to-go 场。** A* 留在 CPU 上（决策 D5）。
- 场的构造：只用已知被阻挡的缓存码，未知格当作可通，所以场始终是下界；它忽略转弯、history 和软代价（`router.cpp:811`）。
- 算法：GAMER 式 8 方向交替扫线 + 过孔松弛，迭代到不动点。不动点唯一，所以 CPU 和 GPU 结果按位相同（`src/gpu/field_cpu.cpp:9-80`）。
  - CUDA：每线一个线程（`src/gpu/field_cuda.cu:10-12`）。
  - Metal：32 lane SIMD-group 上做分段前缀最小值（`src/gpu/field_metal.mm:35-136`）。
- 计时模式下，场的耗时超过 30% 就自动关掉（`router.cpp:1015-1016`）。
- **CPU 并行只有组合赛跑**：8 个 `Router` 变体各自持有全部状态（`route_portfolio`，`router.cpp:3399-3568`）。单个变体内部是串行的。
- 可复现：`--work` 按扩展次数计预算；优胜者按全序决出（布通数 → 过孔少 → 线短 → 变体号，`:3549-3557`），所以结果与线程数无关。

### 2.7 字节保真的 `.kicad_pcb` 读写

- `sexpr::Document` 保留原文。每个节点只记字节区间和子节点索引（`src/sexpr/sexpr.hpp`）。
- 编辑记成区间替换或插入。写出时按偏移排序，拼接回未改动的原文；有重叠就抛错（`sexpr.cpp:239-262`）。
- 追加子节点时沿用父节点的缩进风格（`:205-237`）。
- 新加的线段和过孔挂在根节点下（`board_editor.cpp:40-66`）：mm 值用精确十进制，UUID 用确定性种子。

pcbpilot 的 `internal/kicad/schwrite.go` 已经是同样的“原文拼接”思路，两边一致。

### 2.8 DRC

- 每个形状是一个“圆角核”：点、折线或多边形，加上半径 r。距离比较用 `__int128` 精确平方（`src/geom/shape.hpp`、`shape.cpp:141`）。
- 空间索引是带代数戳的均匀哈希网格（`src/index/uniform_grid.hpp`）。铺铜另有边索引 `PolygonIndex`，把某块板的 DRC 从 730 s 降到 7 s（`drc.cpp:166-190`）。
- **路由器和 DRC 共用同一个 `RuleEngine`。** 所以下面这些在布线时就执行：clearance（含自定义）、孔间距、板边距离、禁布区、`disallow`、`physical_hole_clearance`。
- **只在布线后检查**的有：自定义 `track_width`、`via_diameter`、`annular_width`、`hole_size`，zone 自身间距，长度规则（只在清理阶段调长度时用）。

### 2.9 比 pcbpilot 现有做法好的地方

1. 用组合赛跑代替“一个配置 + 事后多起点”，首个全通即停，墙钟时间取最快的那个。
2. 惰性精确判定 + 按（类，线宽）共享缓存，取代 pcbauto 预先给每个网“占圆盘格”。pcbauto 的 `defaultGrid` 只能让主导类的间距精确（`pkg/pcbauto/router.go:425`），其他类靠近似。
3. 布线器和 DRC 用同一套规则引擎，布线时就执行 clearance、板边、禁布区。
4. 每次提交前都做精确几何复核，失败的点学成 `learned_block`，同样的坏步不会再走。
5. 用可达性洪泛证明“布不通”，早止损。pcbauto 有 `provablyUnreachable`（`router.go:2107`），两边思路一致。
6. 用 `--work` 按扩展次数计预算，做到可复现。pcbauto 有 `WorkRate` 虚拟时钟，同类。

---

## 3. fastroute：原理

背景：fastroute 是 Freerouting 的 Rust 忠实移植。`--parity` 模式下输出与 Java 版逐字节相同（`README.md:12-18`、`docs/PERFORMANCE.md`）。在此之上，它加了一层增强：并行趟、多起点、回滚、跳过 blocked 项、最小线宽。

### 3.1 搜索

- **无网格、基于形状**：在空闲凸区域（房间）和门之间做 A*。
  - 房间和门的类型见 `crates/fr-engine/src/autoroute/engine.rs:91,143,165,190`。
  - 房间按需补全，用障碍形状去减（`complete_expansion_room`，`engine.rs:900`）。
  - 过孔通过 drill page 扩展（`maze.rs:947,998`）。
- **每个连接都重建房间库**（`pipeline/autorouter.rs:188` 设置 `retain_autoroute_database: false`；`autoroute/router.rs:167-175`）。
- 队列是有序集合（`JavaTreeSet`），平局按活的门 id 打破。作者说换成二叉堆会改变结果（`maze.rs:85-109`）。
- 启发式是层感知的加权曼哈顿距离，加上 k × 过孔代价（`destination.rs:129-230`）。绕障碍时剪枝很弱。

**代价**
- 线长按层的水平/垂直系数加权，默认都是 1。
- 转弯代价默认 0。
- 过孔 50，平面网过孔 5；纯 SMD 网的过孔代价 ×0.1（`control.rs:320-331`、`fr-settings/defaults.rs:11-15,103`）。
- **rip-up 代价 = 100 × 趟号**（`router.rs:454`）：越往后越不愿意挪别人的线。victim 已经绕远的，rip 代价会除以它的绕行量，所以更便宜。扇出过孔 ×20000 保护（`maze.rs:26,1173-1270`）。

**协商与推挤**
- **没有 PathFinder 式的 present 或 history 代价。** 作者试过 history，16 块板上未通从 101 变成 105–121，而且每个变体都更慢（`docs/IMPROVEMENTS.md:225-232`）。
- **有推挤**：搜索中推开走线房间，插入时强制推挤（深度 20，`maze.rs:841-870`、`board/routing_board.rs:601`、`control.rs:186`）。

**顺序**
- 平面网先布（`autorouter.rs:241-286`）。
- 第 2 趟起，上一趟没布完的网排到前面；连续两趟停滞就做确定性打乱（`:294-339`）。
- 一条连接第二次失败时 rip 整个网（增强模式下只 rip 一次）。之后在原始板上单独试一次，还是失败就标为 blocked，以后跳过（`:443-460,833-873`）。

### 3.2 为什么会缩到网络类线宽以下

有四种机制（`automatic_neckdown` 默认开，`defaults.rs:53`）：
1. **目标焊盘处的门**：门宽按焊盘的 neck 宽度算（`maze.rs:331-339,872-883`）。只在焊盘处。
2. **`try_neck_down`**：失败的线段端点离起点或终点焊盘在约 2×(半焊盘 + 间距) 以内时，前段用全宽，末段用焊盘的 neck 宽度（`inserter.rs:276-345`）。只在焊盘附近。
3. **fanout micro neck-down**（`inserter.rs:181-246`）：**不限于焊盘附近。**
   - 只要一段两拐点的线强制插入没到位，并且 `with_neckdown` 为真（默认就是），就依次试焊盘 neck 宽度、类线宽的 3/4、3/5、1/2。
   - 没有离焊盘距离的判断。
4. **整条 neck 重试**（`router.rs:484-556`）：失败后把整条连接按 `neck_width_um` 重布。默认关；KiCad 插件把它设成 KiCad 最小线宽（`integrations/kicad/plugins/core.py:1560-1568`）。

`--router.min_trace_width_um` 换算成向上取整的半宽，然后把每个 neck 候选值抬到这个下限（`control.rs:113-119,348-360`）。所以它是**下限，不是禁令**：
- 例：250 µm 的类在 150 µm 下限下，候选依次是 187、150、125→150。所以板中间会出现 150 µm 的线段。
- 这就是基准 F2 中 Power3V3 有 23 mm neck 的原因。
- F1 没设下限，Digital 被缩到 0.09–0.112 mm，低于板最小线宽 0.15。

`--no-neckdown-classes` 把类标成 `no_neckdown`，四种机制全部关掉（`crates/fastroute/src/main.rs:415-425`、`control.rs:250-253`、`router.rs:524`）。pcbpilot 已经按意图传入这个参数：`kicad route` 中 `MinMil ≥ OuterMil` 的类禁止 neck（`internal/pcb/specctra/requirements.go:146`、`internal/kicad/dsn.go:215`）。

### 3.3 multi-start 与 initial-session

**`--multi-start=N`**（默认 4）
- 首跑开始前，先把**扇出前**的板克隆一份（`pipeline/mod.rs:193`）。
- 首跑之后如果还有未通，就用 rayon 并行跑 N−1 个变体（`mod.rs:248-307`）。每个变体：
  - 从扇出前的克隆开始，**重做扇出**；
  - 只换第 1 趟的信号顺序种子（`order_seed = 0x5eed_0000 + v`）；
  - **各趟串行**（`pass_threads = 1`）。
- 变体之间除了初始板，什么都不共享。
- 选优按（未通数，违规数，−router score）的字典序；平局保留先跑的那个。
- 如果估计时间（首跑耗时 × 3）超过 600 s，就跳过多起点（`mod.rs:161,197-207`）。
- `order_seed` **没有通过 CLI 暴露**。

**`--initial-session=FILE`**
- 读入 SES（`fr-io/post_load.rs:297`），然后把导入的线和过孔解除固定（`main.rs:437-450`）。之后它们和普通布线一样，可以被 rip、被优化。
- 用途是从检查点续跑。`-do` 的 SES 文件在运行中会被持续改写为当前最佳板（`autorouter.rs:960-965`）。
- pcbpilot 的 `--continue` 就是用它做续跑（`internal/app/cmd_pcb_fastroute.go:776`）。

### 3.4 时间花在哪里

1. **单步很贵**：房间补全走 45° completeShape 树（自身约 9%）、网格叶扫描约 6%、单纯形和八边形求交。每条连接布通后还要做一次 pull-tight（`router.rs:480-482`）。每次失败都把可达范围的队列耗尽，单次失败的搜索状态约 88 MB（`PERFORMANCE.md:190-191`）。
2. **阶段是串联的**：扇出 → 自动布线各趟 → 多起点（只在有未通时） → 优化器。优化器的预算 = 整个布线阶段耗时，至少 60 s（`mod.rs:229-232`）。不过在 F2 里，优化器 1 趟就因为改进不到 2.5% 停了，只用了 0.9 s。
3. **长尾**：
   - 停滞判据是第 8 趟起连续 10 趟进步不到 0.5 分才停（`autorouter.rs:31-39,1092-1135`），所以 F2 首跑有约 10 趟、每趟约 0.8 s 的空转。
   - blocked 项“第二次失败后跳过”，但 congestion 项每趟都会重试。
   - 并行趟里，尾部连接都挤在同一片区域，会触发 1.5 mm 足迹冲突，退回串行（`parallel_pass.rs:31`、`autorouter.rs:51`）。
4. **结构上不利于收敛**：rip 代价越往后越高，也没有 history；唯一的扰动是第 4 趟起的随机系数（`maze.rs:1232-1237`）。卡住的连接会用同样的方式反复失败。

### 3.5 其他

- **平面**：DSN 的 `plane` 变成非障碍的导电区域。没有建模反焊盘；电源层不能走线（`fr-io/structure.rs:336-344`、`control.rs:139-147`）。
- **差分对**（`--pairs`）：
  - 先在空板上布这一对，再把主网偏移 w + gap，切成 0.2 mm 小段做间距检查，空闲段之间用迷宫路由接起来（`fr-engine/src/diffpair.rs:452,502,603,737,793`）；
  - 布完后固定这一对，再布其余网；如果有未通，就释放这一对重新布（`main.rs:637-650`）。
- **报告**：`--diagnose` 把每个未通项单独布一次，分成 congestion 或 blocked（`crates/fastroute/src/report.rs:1-13,170-220`）。**pcbpilot 已经传了 `--diagnose`，但还没有用这个分类去驱动下一步**，比如 blocked 项应该回到布局去处理。

### 3.6 比 pcbpilot 现有做法好的地方

- 有推挤，有 pull-tight 和过孔位置优化器。pcbauto 只有 legalize 和 repair。
- 检查点 SES 加 `--initial-session` 续跑，是成熟的断点机制。
- blocked / congestion 自动分类。

pcbpilot 的 pcbroute v2 规划（`docs/router-cleanroom/PLAN.md`）选了 tile/房间路线（`pkg/pcbroute/tile` 已经合并）。fastroute 的数据说明：**这条路线每步代价高，而且作者自己测出 PathFinder 在它上面效果差。** 建议 v2 先做网格版（octile A* + 惰性缓存 + 组合赛跑，见 R6），tile 只用于平面和大面积空白区。

---

## 4. pcbpilot 布线现状（对照）

- **实际在用的内置路由器是 `pkg/pcbauto/router.go`**（2,465 LOC），不是 `pkg/pcbroute`。后者是 v2 骨架，`Route` 和 `RouteDSN` 都返回 not implemented（`pkg/pcbroute/types.go:76-84`）；`search`、`negotiate`、`rrr`、`shove` 等包只有 `doc.go`。
- **pcbauto 的做法**：
  - 均匀 3D 网格，pitch 2–5 mil。每个网占一个半径为 `w/2 + share` 的圆盘格；另有 8 格块摘要，用来快速跳过空区（`grid.go:49`、`router.go:425,880`）。
  - A*：八方向 + 过孔，带 decrease-key 的索引堆，窗口逐级放大（`router.go:1356`）。
  - 真正的 PathFinder 协商：present 系数从 0.6 起每轮 ×1.6，history += 0.5(u−1)，最多 40 轮（`negotiate`，`:1960`）。之后做严格合法化，仍冲突的网直接移除。
  - **neck 只在焊盘 max(3w, 30 mil) 以内或 BGA 区内**（`inNeck`，`:848`）。**这是三家里唯一满足“只在出线区缩线”的实现。**
  - **没有并行**，router.go 里没有 goroutine。
- **fastroute 包装**：
  - DSN 准备：`internal/kicad/dsn.go:215` 处理 PP 类、inner `layer_rule`、间距 +0.2 mil、no-neckdown 类，以及板边和安装孔禁布（`:108`）。
  - 调用：`internal/app/cmd_pcb_fastroute.go:667-776`，传 min trace、pairs、tune、`--diagnose`、`--continue` 续跑；崩溃时单线程重试一次。
  - `kicad route` 默认 `--threads 1`（`cmd_kicad_route.go:139`），并行跑一个 multi-start=4 的投机任务，取改进的那份（`:638-680`）。
- **`kicad route` 里 fastroute 是写死的**：`resolveFastroute`（`:309`）→ `runFastroute`（`doRoute`，`:568`）→ `r.route *fastrouteRun` 供 `route-complete` 使用。没有路由器抽象。

---

## 5. kicad-copilot（TS MCP）

**原理**
- **布局在云端，闭源。**
  - `extract_circuit` 和 `beautify_schematic` 把电路 POST 到 `https://circuit.tech.ru.net`（硬编码 Basic auth，`src/utils/server.ts:1`、`src/circuit/extract.ts:74`）。
  - 返回的是 ELK 分层图 JSON：部件 pos、rotate、mirror、`blocks_rect`、带拐点的 edge sections（`src/types/circuit.ts:76-131`）。
  - 输入里每个块有 `next_block_names[]`，构成功能流图（`:48-52`）。由此推断它用的是 **ELK layered（Sugiyama）从左到右的信号流布局 + 块分组**。
  - 本地没有二进制可逆。出于隐私，**pcbpilot 不应依赖或调用该服务。**
- **本地后处理**（`src/kicad/assembly.ts`）：
  - “服务器布局为准，只允许平移”，不旋转、不缩放（`:51`）。单位 10 mil，吸附 1.27 mm（`:39-40,76-78`）。
  - 整体平移量用环形搜索（`placementOffset`，`:376-458`）：目标点是被删部件的质心，按 1.27 mm 网格一圈圈往外找，评分按字典序 [重叠数, 出页, 曼哈顿距离]，留 5.08 mm 边距。
  - ELK 的 edge 转成正交导线，首末拐点对齐到引脚轴线（`:570-646`），自动算结点（`:751`）。
  - GND 用电源符号；其他具名网用改名的 VCC 符号当“短符号”，兜底用全局标签（`:158-200,555`）。
  - 电源符号的短桩：4 个方向 × 若干长度 × 若干垂直偏移，逐个试；不能穿引脚、不能碰他网，首个可行即用（`addNamedConnection`，`:1368-1460`）。
- **连接 API**：没有 connect 调用。每个引脚带 `signal_name`，同名即同网；`""` 表示不连，`"NC"` 表示加不连标记（`types/circuit.ts:5-9`）。
- **重排**：`beautify_schematic({blocks:{块:[refs]}})` 把选中的部件删掉，再按网名重新装配（`beautify.ts:46-111`）。导线重建，不是拖动。挂在被删部件上的悬空导线，按导线图遍历删除；碰到保留引脚的导线不删（`wire-removal.ts:134`）。
- **事务**：
  - 每次 `extract_circuit` 前自动建检查点；写入后回读网表，逐个断言 external_connect 引脚的网，不符就自动恢复（`extract.ts:35-64,73,107-112`）。
  - 所有写入走 `mutateSchematic`（`schematic-writer.ts:41-67`）：内存变换 → 在隐藏临时文件上跑 kicad-cli 网表校验 → 文件 sha256 比较并交换 → 原子 rename。写入经单并发队列串行执行（`utils/mutation-queue.ts`）。
  - 检查点是整文件字节快照，元数据原子写（`checkpoints/store.ts:51-63`）。
- **给模型的数据**：`{components:[{designator,value,block_name,pins:[{pin_number,name,signal_name}]}]}`，**没有坐标和导线**。超过 40 个部件就落盘、只返回路径（`tools/documents.ts:24-34`）。成功只回 `ok`，错误最多 36 条。

**比 pcbpilot 好**：块流图（`next_block_names`）驱动的分层排布；“装配只平移”加环形搜索；每次写入后自动断言、失败自动回滚；大读数落盘。

## 6. KiCad-AI-Assistant（kcaa，Python FastMCP + KiCad 插件）

**原理**
- **没有全局自动摆放。** 系统提示和 `kicad_plugin/skills/schematic_placement.md` 让模型：先 `extract_schematic_netlist`，优先用 `place_symbol_relative`（相对某部件放，比如“U1 右侧，间隔 2.54”），否则用 `find_free_area`。
- `find_free_area`（`kcaa/tools/placement_helpers.py:142-434`）：
  - 用库里的真实符号包围盒；
  - 枚举绘图区内（扣除标题栏，`:130`；边距 3.81 mm）所有 1.27 mm 格点；
  - 按到 prefer_near 的距离排序，取前 N 个不重叠的。暴力但简单。
- **防误短路**：`_find_safe_placement` 微调部件位置，保证引脚不会落在已有导线上（`symbol_edit_tools.py:795-914`）。
- **分组**：
  - `placement_group` 属性写进文件（`schematic_group_tools.py:426`）；
  - 锚点 = 引脚最多的部件（`:201`）；
  - 其余按引脚数交替放在锚点右侧一列、下方一行，间隔 2.54 mm；比高宽的部件转 90°（`:267-381`）；
  - 有分组评分（`:384-418`）。
- **连线**：`connect_pins_with_wire` 每次连一对（`wire_edit_tools.py:1925`），没有批量接口。
  - 路由是**候选枚举**，不是 A*：直连 → 两种 L → 8 条通道上的 Z（各 ±0.635/±1.27 偏移）→ W → 蛇形 → U 形绕行（`:271-460,1035`）。
  - 考虑出线方向；首个无冲突的方案即用；自动合并共线段、插结点（`:672,1615`）。
- **移动**：导线不跟随（`symbol_edit_tools.py` 约 `:3610`）。
- **检查点**：
  - 每次修改写 `.bak`；
  - 每个请求首次修改某路径时，自动做工程快照：tar.gz + sha256 清单，内容没变就跳过，有数量上限（`kicad_plugin/llm_client.py:2239-2275`、`kcaa/utils/version_manager.py:91-197`）。
- **省 token**：
  - 网表里引脚坐标只放在元件侧，网只引用（元件，引脚）（`kcaa/tools/netlist_tools.py:23-90`）；
  - 修改工具直接返回新的 body_bbox，免得再读一次；
  - 静态提示从约 2300 token 降到约 810 token，流程细节放进按需加载的 skill（`docs/skill-system-design.md`）。

**比 pcbpilot 好**：“相对放置”的语义（模型说关系、不说坐标）；引脚落在导线上的防短路检查；按需加载 skill 省 token。

## 7. mcp-server-kicad（Python）+ `schematic-plan` / `schematic-design` skills

注意：这两个 skill 在 **mcp-server-kicad**（`skills/`）里，不在 kcaa。

**引擎**
- 不自动摆放。`place_component` 要求给出 x、y、旋转、镜像，并检查是否在页内、吸附 1.27 mm、拒绝重复位号（`mcp_server_kicad/schematic.py:682,1116`、`_shared.py:827`）。
- 引脚端点完全按 KiCad 的方式算：**先旋转后镜像**，整数内部单位（`_connectivity.py:295,366`）。ADR 记录：顺序反了，12 种朝向里有 4 种会连错引脚（`docs/adr-routing-safety.md`）。
- **`wire_pins_to_net(pins, label_text)`**（`schematic.py:1971` → `_connectivity.plan_wire_pins:2253`）：
  - 一次一个网，**全成或全不成**。
  - 每个引脚向外画 2.54 mm 短桩，端点放本地标签；短桩会碰东西就把标签直接放在引脚端。
  - 不写结点，禁止交叉。
  - 每加一个对象，先放进内存模型，再检查下一个引脚，所以同一次调用里的引脚之间也不会互撞。
  - 碰撞规则 rule1/2/3/touch 用 0.05 mm 整数裕量（`:1920-2040`）。
  - 拒绝时给出带括号的错误码和对应修复：`[names]`、`[touch]`、`[dup_ref]`、`[netclass]`……
- 连通性模型：并查集 + 粗网格（`_UF`，`:656`；`Model`，`:1222`），有“可能连接”和“确定连接”两种视图。
- 文件 I/O：无损具体语法树（`_cst.py:168,207`，`serialize(parse(b)) == b`）+ 原子写（`_shared.py:699`）。
- **没有库缓存**：每次放置都重新解析整个 `.kicad_sym`（`schematic.py:1262`）。慢，pcbpilot 不要学这一点。
- `move_component` 不拖线；`connect_pins` 是不避障的 L 形（`:1440,2077`）。
- 多步工具部分失败时，返回精确的撤销调用列表（`:1862-1891`）。
- `hooks/hooks.json` 禁止 Read/Write/Edit 直接碰 KiCad 文件，强制一切经工具。

**`schematic-plan` skill**（`skills/schematic-plan/SKILL.md`）：只规划，不改文件。输入是已批准的 BOM，输出 `specs/schematic-plan.md`。
1. **页面大小用算式定**：每个件约 12.7 × 12.7 mm，级间 25.4–50.8 mm，边距 10 mm，标题栏 108 × 32 mm。可用面积：A4 179 × 168、A3 302 × 255、A2 476 × 378。选能放下的最小页，并写出算式（`:64-84`）。
2. 每个级（stage）一个包围盒表。
3. 每个位号一行精确坐标表（x，y，旋转）：
   - 有源件居中，输入无源件在左、输出无源件在右；
   - 去耦电容离 IC 25.4 mm；
   - 信号从左到右，电压从上到下（`:98-111`）。
4. 连线表 Order | Net | Tool | Pins：
   - 一个网的所有引脚放进一次 `wire_pins_to_net`；
   - 电源网先连；
   - 每个引脚只出现在一行；
   - 只有相邻的一对用 `connect_pins`。
5. 不连脚和 PWR_FLAG。
6. 层次图纸。

完成后派一个**不信任计划**的审阅子 agent（`agents/schematic-plan-reviewer.md`）：
- 重算页面边界；
- 对每个符号调 `get_symbol_info` 核对引脚名；
- 检查间距：竖直 ≥ 10.16、水平 ≥ 12.7、去耦 ≤ 25.4、级间 ≥ 25.4；
- 检查包围盒重叠和 BOM 覆盖；
- 输出 APPROVED 或 ISSUES_FOUND，最多 5 轮。

之后还需要用户批准。skill 里有一张“防合理化”表，例如“A4 够了”“看着间距挺好”这类说法都不接受，必须给出算式。

**`schematic-design` skill**（`skills/schematic-design/SKILL.md`）：
- 计划是唯一依据，**不许改坐标**。
- 顺序：页面 → 库 → 自定义符号 → 全部放置 → 按表连线 → 不连 → PWR_FLAG → 层次 → 标注 → `validate_hierarchy` → ERC = 0。
- 遇到错误就上报，不即兴发挥：缺符号、坐标出页时停下重新规划。
- 有错误码 → 动作对照表。**禁止**用 `add_label` / `add_wires` 绕过拒绝。
- 约定：
  - 网名大写，低有效加 `_N`；
  - 电源永远用标签，不串接；
  - 约 25 mm 以内才用 `connect_pins`；
  - PWR_FLAG 只加在有电源输入引脚、但没有驱动的网上。

**比 pcbpilot 好**：“规划 → 独立审阅 → 机械执行”三段门禁，并把数值版式规则写成可检查的条款；按网批量、全成或全不成，加机器可读的拒绝码。

## 8. KiCAD-MCP-Server（TS 前端 + Python 后端）

- **原理图同样不自动摆放。** `batch_add_components` 的 position 默认 (0,0)，可以给一个 origin 做块内相对定位（`python/commands/schematic_batch.py:90`）。
  - 返回每件的 `body_bbox` 和整块的 `placement_bbox`，方便模型排下一个块（`:67`）。
  - 字段按引脚轴线放：两脚竖直 → 字段放左右；两脚水平 → 放上下；多脚 → 放在最上和最下引脚的外侧（`:43`）。
  - 真正的摆放器只用于 PCB：力导向，引脚朝向选旋转，最小化 HPWL（`placement_optimizer.py:77`）；另有 HierPlace 包围盒打包（`_hierplace.py`）。**思路可以搬到原理图**：按块分组 → 打包；旋转从 4 个候选里按 HPWL 选。
- **连接**：
  - `batch_connect({ref:{pin:net}})` 把标签直接放在引脚端，沿引脚外向旋转（`schematic_batch.py:517`）。
  - **“对面标签”技巧**：14 mm 内如果有同网、方向相反的本地标签，就画一根导线把两者连起来，不再放第二个标签（`schematic_text_utils.py:185`）。
  - `batch_add_and_connect`：放置和连网一次完成，每件带 `nets:{pin:net}`（`:792`）。
  - `sync_junctions`：线端 + 引脚 ≥ 3 的点加结点，并删掉失效的结点（`wire_manager.py:676`）。
- **移动保连通**（`move_schematic_component`，`schematic_handlers.py:2022`，`preserveWires=true`）。这是被调研工具里**最强的部分**：
  1. 计算每个引脚的新旧位置；
  2. `drag_wires`：把线端、结点、标签、不连标记从旧点移到新点，删掉长度变 0 的线（`wire_dragger.py:360`）；
  3. `_straighten_bent_wires`：原本水平或竖直、被拖斜的线段，把自由端移到拖动端的轴线上（拐角跟着走），最多 8 遍；遇到结点、静止部件的引脚或分叉就不动，计入 LeftDiagonal（`:576`）；
  4. `synthesize_touching_pin_wires`：原来引脚直接相碰的，补一根短线（`:796`）；
  5. 同步结点，写一次文件。
- **快照**：`snapshot_project` 复制整个工程目录，用于续做，不是撤销（`kicad_interface.py:4809`）。批量工具不是原子的。
- **库缓存做得好**：
  - 库目录、库名 → 文件、抽出的符号块都有模块级缓存，按 mtime_ns 校验（`dynamic_symbol_loader.py:37-53`）；
  - `PinLocator` 按（mtime，size）缓存解析结果（`pin_locator.py:55`）。
  - 但 `batch_add_components` 每加一件就读写整个文件一次，O(n²)，不要学。
- **完成标准**（`docs/HEADLESS_AUTHORING.md` §3、§9）：
  - ERC 0 错误；
  - 渲染出 SVG 并看过；
  - 任何美化或重排都要做 **golden netlist diff**：前后各导一次网表，比较 net → {REF.PIN}；
  - ERC 从根图跑；
  - 符号原点放在 2.54 mm 网格上，间距约 12.7 mm（全局标签约 7.6 mm 长）。
- 工具路由：`list_tool_categories`、`search_tools` 按需暴露工具，减小工具列表（`src/tools/router.ts:38`）。

**比 pcbpilot 好**：拖线后再拉直的算法（pcbpilot 的 `DragSymbolsOpt` 对整体刚性移动的岛直接平移，否则拆掉重连，没有“拉直”这一档）；“对面标签变一根线”；放置后直接返回 bbox。

## 9. schem（Rust DSL）

- v0.1，约 600 行，**不出 `.kicad_sch`**，只出旧式 `.net`（`src/emitter/kicad_net.rs:1-71`），也没有布局。
- 为什么写得快：PEG 语法（`src/grammar.pest:1-66`）只有 `net NAME`、`component REF PART { 组 { pin: NET } }`、`constraint`、`#` 注释。连接直接写成引脚 → 网，**没有导线和坐标**；组名只起注释作用（`src/ast.rs:28-32`）。示例 111 行描述 5 个件、16 个网。
- 为什么查得快：
  - 单遍解析；`validate` 跑 5 个检查，**收集全部诊断后一次报出**（`src/validator.rs:18-153`）：未声明网（错误）、重复位号（错误）、重复网和未用网（警告）、约束单位错（错误）。
  - 网必须先声明再使用，所以笔误能当场发现。
  - **不检查引脚名是否存在于符号里**，这是它的短板。

**比 pcbpilot 好**：极简的文本格式，加上一次报出全部诊断。pcbpilot 的 sch-build spec 是 JSON，信息更全，但模型写起来更啰嗦；而且 JSON 解析错误一次只报一个。

---

## 10. pcbpilot 原理图现状（对照）

- **dev 分支**：
  - 写后端 `internal/kicad/schwrite.go`：原文拼接，字节保真。`LibSymbolFromFile`（`:283`）**每次调用都重新解析整个 `.kicad_sym`**。
  - 重连 `internal/kicad/schroute.go`：按岛拖动；不能刚性平移的岛拆掉，用 Prim + 单网 A* 重连；连不上就退回标签（`:253,378,788,925,1183`）。
  - 布局规划器 `internal/app/sch_layout_*.go`（约 14k LOC）：候选搜索 + 回溯 + 模拟退火 + 迷宫 A*，默认预算 2 万个候选。
  - 每次写入都是事务（`kicadSchCommitVerified`，`cmd_kicad_sch_auto.go:180`）：`GateSchematic` 的“不新增发现”比较 → 复制工程 → kicad-cli 网表比较 → 原子 rename。
- **时间花在哪里**（`docs/kicad/PARITY.md:69-81`）：
  - kicad-cli 网表 0.4–0.6 s，每次写入 2 次；ERC 1.3 s；
  - `--fix-pwr-flag` 在 Gas A 上 24 s（10 个旗子，10 个事务）；
  - 真正的大头是**旧流程约 80 个子命令，每个一轮 LLM**；
  - 其次是密区的规划器预算（WROOM 10 件区：20 万候选、66 s）。
- **kicad/sch-build 分支（WT）**：
  - 输入是 spec JSON `pcbpilot.kicad.sch-build/1`，一次调用出完整工程。
  - 流水线（`runSchBuild`）：
    1. resolve：LCSC 符号 8 路并行导入，有缓存（`internal/kicad/schbuild_lib.go:94`）；
    2. layout：各区并行，规划器有 8 s 超时，之后试网格变体，按 `CheckSchematic` 发现数选优；按 hash 复用已有布局（`kicad_sch_build_layout.go:1318-1400`）；
    3. pack：把各区排到页上；
    4. 网表和 ERC 并行，网表与 spec 不符则硬失败、不写入；
    5. page-fit 和 quality 门禁；
    6. intent derive；
    7. 检查点。
  - 未提交的改动：`cmd_kicad_sch_edit.go`（717 行，增量编辑、`sch-read`、`sch-checkpoint`）；quality 门禁已经改走 `rep.gate()`（`kicad_sch_build_render.go:512-513`），审计时看到的“quality 失败不影响退出码”已在工作树里修好；路径安全加固。
  - **区排布**：`packZones` / `sbPackFresh`（`kicad_sch_build_layout.go:1433-1520`）按 spec 里的 id 顺序做行式货架打包，**不看信号流**。这和同日另一篇调研的结论一致。
  - 规划器超时后不取消，还在占 CPU：`planZoneBounded` 只检查截止时间，规划器内部没有取消点（`:1402`）。

---

## 11. 排序后的 pcbpilot 改进清单

收益数字是基于上文证据的**估计**，标“实测”的除外。工作量：S ≤ 1 天，M 2–5 天，L > 1 周。

| # | 改动 | 预期收益 | 要改的文件 | 工作量 |
|---|---|---|---|---|
| **S1** | **sch-build / sch-edit / sch-read 收尾合并，作为默认原理图路径**。模型只写 spec：引脚 → 网 + 块 + 角色。修改走 `sch-edit` 的 op 列表，读回走 `sch-read`（kicad-copilot 式的“每引脚一个网名”视图，大于 40 件就落盘）。补上缺的 `docs/kicad/sch-build.md`。skill 改成“规划 → 审阅 → 一次 build”（mcp-server-kicad 的三段门禁，但坐标由 Go 计算，不让模型写） | 约 80 轮 LLM 降到 2–4 轮。墙钟从“十几分钟”级降到“一次 build 的秒级到十几秒”（WT 的分阶段计时可直接验证） | WT `internal/app/cmd_kicad_sch_build.go`、`cmd_kicad_sch_edit.go`、`kicad_sch_build_render.go`；`skills/pcbpilot`（KiCad 原理图章节）；`docs/kicad/sch-build.md` | M |
| **S2** | **进程内连通性模型，取代逐写入的 kicad-cli 校验**。实现：整数内部单位（KiCad 的 IU 和取整）、先旋转后镜像的引脚端点、线端/标签/电源/结点的并查集；KiCad 10 的规则是线端或引脚落在导线中段**不连**，标签落在中段**连**。增量命令（autoconnect、connect、`--fix-pwr-flag`、group-move、规划器候选评分）都用它做门禁；kicad-cli 网表 + ERC **只在事务末尾跑一次**。PWR_FLAG 等批量修复合并成一个事务 | 增量写入每次省 0.8–1.2 s（2 次导出）。`--fix-pwr-flag` 在 Gas A 上 24 s 降到约 2–3 s。sch-build 的候选评分可以用连通性 + 质量一起选优 | `internal/kicad/` 新增 `schconn.go`（可从 `schnet.go`、`schroute.go` 的岛模型复用）；`internal/app/cmd_kicad_sch_auto.go:180`；`cmd_kicad_schcheck.go`；WT `kicad_sch_build_layout.go:59-80` | M |
| **S3** | **区的页面排布按信号流分层**。spec 的块加 `next` 流向边（kicad-copilot 的 `next_block_names`），可以从角色推断默认值：连接器/电源 → 稳压 → MCU/IC → 外设/输出。用 Sugiyama 式分层：列 = 拓扑层，列内按 barycenter 排序，电压高的在上；每列做货架打包。区内每件的旋转从 4 个候选里按到同网伙伴的 HPWL 选（KiCAD-MCP-Server PCB 摆放器的思路）。跨区网一律用标签或电源符号；14 mm 内同网对面标签改成一根线 | 读图方向一致，跨区标签对齐；可度量：跨区网的平均距离、页面利用率、交叉数 | WT `kicad_sch_build_layout.go`（`packZones` / `sbPackFresh`，`:1433-1520`）；`cmd_kicad_sch_build.go`（`sbSpec` 加 `next`）；`internal/app/sch_layout_engine.go`（旋转选择） | M |
| S4 | **符号库缓存**：按（路径，符号名，mtime）缓存 `LibSymbolFromFile` 的结果；未命中不缓存 | 每个本地库件省一次整库解析（大库几十到几百 ms） | `internal/kicad/schwrite.go:283`；`schbuild_lib.go` | S |
| S5 | **规划器可取消，并和网格变体同时跑**。规划器加 `ctx` 取消点；`layoutZones` 里规划器和网格变体同时开跑，网格变体一旦 0 发现，就取消规划器（TraceMaker 组合赛跑思路）；`sbLayoutOpts.Timeout` 暴露成 CLI 参数 | 密区最坏情况从“8 s 超时 + 还在后台空转”变成“首个干净方案即停”。CPU 不再泄漏 | WT `kicad_sch_build_layout.go:1318-1412`；`internal/app/sch_layout_engine.go`（加取消点） | S |
| S6 | **移动后拉直**：在 `DragSymbolsOpt` 的“刚性平移 / 拆掉重连”之间加一档：拖动线端、标签、不连标记，再把被拖斜的线拉直（KiCAD-MCP-Server 的三步法），不行再拆掉重连。门禁用 golden netlist diff | 少量移动后的图更整齐，导线保留原样，重连更少 | `internal/kicad/schroute.go:378,925` | S–M |
| S7 | **把版式规则做成门禁，不靠提示词**：去耦电容离 IC ≤ 25.4 mm、件间距 ≥ 10.16 / 12.7 mm、标题栏禁区、页面按算式选择、网名规范（大写，低有效 `_N`） | 美观问题在 build 时直接失败，不用等人眼看 | `internal/kicad/schquality.go`（`CheckSchematic`）；WT `sbQualityCheck` | S |
| S8 | **spec 的诊断一次报全**（schem 思路）：未声明网、未用网、引脚名在符号里不存在（schem 没有、pcbpilot 能做）、单引脚网、重复位号，全部收集后返回，并附修复建议码（mcp-server-kicad 的 `[names]` / `[touch]` 风格） | 模型一轮改完，减少 build 往返 | WT `cmd_kicad_sch_build.go`（spec 校验） | S |
| **R1** | **抽出路由器接口，加 TraceMaker 后端**。`kicad route --router fastroute\|tracemaker\|auto`；TraceMaker 直接读写 `.kicad_pcb`（不走 DSN/SES）；把 `.kicad_pro` 和精简后的 `.kicad_dru` 同名复制到输出旁；之后必须重铺。默认参数：`--soft-zones`；平面层 `--no-tracks-on`；用 `--work N` 保证可复现；`--json`。`route-complete` 改读统一的 RouterResult | PicoRick 实测：141 s / 5 未通变成 57 s / 0 未通（单板，开差分对时；不开是 1 个未通）。同时省掉 DSN 后处理和 SES 导入 | `internal/app/cmd_kicad_route.go`（`doRoute`，`:568`；`r.route`；`kicadRouteCompleteGate`，`:1123`）；新增 `internal/app/kicad_route_tracemaker.go`；`internal/app/pcb_route_gates.go:892` | M |
| **R2** | **给 TraceMaker 打补丁**：`--no-neckdown-classes`；neck 下限取 `.kicad_dru` 的 `track_width min`；`--pairs FILE`（喂给已有的 `RouterOptions::pair_nets`）。用本地构建脚本 + SHA 记录维护补丁，同时向上游提 PR | 消除 T4/T5 那类 `intent-widths` 失败（92 条 track_width 错误 / 163 mm 窄线）。差分对只耦合意图中的那几对 | TraceMaker：`src/route/router.cpp:179-183,3242-3265`、`src/route/escape.cpp:133-139`、`src/route/router.hpp`、`src/app/main.cpp`、`route_job.cpp`。pcbpilot：安装脚本或说明（仿照 fastroute 的 `~/.pcbpilot/<tool>/current`）、`docs/kicad/` | S（补丁）+ S（构建） |
| **R3** | **跨路由器组合赛跑 + 早停**：`--router auto` 同时启动 fastroute（主跑）和 TraceMaker（打过补丁的），也可以加 pcbauto。各自的结果先过一组**便宜门禁**（route-complete + intent-widths + kicad-drc 未连接数），先通过者胜，其余取消；都没通过就按门禁结果择优。fastroute 侧：去掉“主跑完再看”的串行等待；`--diagnose` 判为 blocked 的项不再续跑，直接报给布局 | 墙钟 ≈ min(各路由器)，不再是 max 或累加。PicoRick 从约 143 s（pcbpilot 现路径）降到约 60 s。blocked 项早止损，省掉 10 趟以上空转 | `internal/app/cmd_kicad_route.go:567-690`；`cmd_pcb_fastroute.go:776`（续跑条件加 blocked 判断） | M |
| R4 | **把 blocked 项反馈给布局**：fastroute 的 blocked 分类，或 TraceMaker 的 `boxed in`，映射到元件和焊盘（PicoRick：U303 周围的 C314 等去耦件），生成 `place` 建议（移动或旋转去耦电容、放出线过孔），并加 `pcbpilot kicad route` 的 `--place-retry 1` | 处理长尾的根本办法：这些项任何路由器参数都解决不了 | `internal/app/cmd_kicad_route.go`；`pkg/pcbauto` 的摆放与评分 | M |
| R5 | **neck 策略按网络类三选一**：none / pad-local（长度 ≤ max(3w, 0.75 mm)，与 pcbauto `inNeck` 一致）/ anywhere。`intent-widths` 段宽判据同步认 pad-local 缩线。TraceMaker 要实现 pad-local，需要在 commit 时把 neck 宽度只用在离焊盘近的线段上，补丁变大（M） | 电源类可以安全地从密脚出线，又不会全程变细 | `internal/kicad/dsn.go`、`specctra/requirements.go`、`pcb_route_gates.go:96,370`；TraceMaker `router.cpp:1196`（commit） | M |
| R6 | **pcbroute v2 / pcbauto 采用 TraceMaker 的三件事**：(a) 惰性精确判定 + 按（类，线宽）共享缓存，取代占圆盘格；(b) N 个变体 goroutine 组合赛跑（pitch × {0.5,1,2}、顺序 × 3、过孔代价 × 2），首个全通即停，按扩展次数计预算，结果可复现；(c) 网络类每网只解析一次。v2 先做网格版，tile 只用于平面和大空白区 | 内置路由器从 0 并行变成多核满载；多类混合板的间距更精确；首个全通即停 | `pkg/pcbauto/router.go`（`setupNets`，`:440`；`search`，`:1356`；`negotiate`，`:1960`）；`pkg/pcbroute/search`、`negotiate`（实现）；`docs/router-cleanroom/PLAN.md` | L |
| R7 | **铺铜和规则文件卫生**：送给 TraceMaker 的 `.kicad_dru` 只保留 clearance、creepage、edge、disallow，去掉 `track_width`（布线时不生效，还会让它进入精确模式）；完整的 dru 留给 DRC 门禁。路由后一定重铺（`--refill-zones`），再跑 kicad-drc | 避免假失败；保住缓存和 GPU 场。PicoRick 上只差约 3 s，大板差得更多 | `internal/app/kicad_route_tracemaker.go`（R1 新文件）；`internal/kicad`（dru 生成） | S |
| R8 | **fastroute 可复现与线程**：`kicad route` 默认 `--threads 1` 是为了可复现，代价是单线程。可以改为 `--threads N` + 固定 multi-start，并在报告里记录（fastroute 并行趟的提交顺序是确定的，`parallel_pass.rs`）。需要先测 3 次结果是否一致 | 主跑可能快 2–3 倍（基准默认多线程首跑 50 s，pcbpilot 单线程 112 s） | `internal/app/cmd_kicad_route.go:139` | S（先测） |

### 11.1 原理图前三（速度 + 美观）

1. **S1：sch-build 作为默认路径，模型只写 spec。** 最大的时间在 LLM 轮次，不在算法。被调研的工具都在“模型给坐标、一个调用连一对”的模式里打转。pcbpilot 的 WT 已经超过它们，收尾合并即可。
2. **S2：进程内连通性 + 事务末尾只调一次 kicad-cli。** 把每次写入 0.8–1.2 s 和批量修复的 N 次导出压成一次；规划器的候选评分也能顺带按连通性把关。
3. **S3：按信号流分层的区排布 + 按 HPWL 选旋转 + 跨区一律用标签。** 美观的主要短板在区与区之间（同日另一篇调研也是这个结论）。区内几何 pcbpilot 已经比所有被调研工具都严格。

### 11.2 布线前三（速度 + 布通率）

1. **R1 + R2：TraceMaker 作为后端，加上 neck 补丁。** 结论：**pcbpilot 应该以“按网络类禁止 neck-down”的方式驱动 TraceMaker，这需要一个小补丁。**
   - 不打补丁的问题：
     - neck 作用于整条连接，降到 0.15 mm；
     - `track_width min` 不生效；
     - 只能按网名一刀切地识别差分对。
   - 这三点都会让 `intent-widths` 或 `intent-lengths` 失败，而且 pcbpilot 事后的“加宽到意图”在原位不一定加得宽（基准 §4 已提示）。
   - 补丁约 40–80 行，位置明确（§2.2）。上游合并前，按 fastroute 的方式让用户从带 tag 的源码 + 补丁本地构建，记录 SHA。TraceMaker 是 GPL，只作为外部进程调用，与 fastroute 情况相同。
   - 上游合并后撤掉本地补丁。
2. **R3：跨路由器组合赛跑 + blocked 早止损。** 把“fastroute 跑完 → 多起点 → 续跑”的串联，改成多个路由器并行、先过便宜门禁者胜。
3. **R4：长尾回到布局。** PicoRick 剩下的 5 个是布局造成的 blocked 项。只调路由器不能根治；需要在路由报告 → 摆放之间建立闭环。

  （R6 是长期最重要的，但工作量 L，不进前三。）

---

## 12. 每条路径都保住硬门禁

现状（详见 §4；门禁定义：`internal/app/pcb_route_gates.go`、`cmd_kicad.go:250,276`、`cmd_kicad_route.go:920-1123`、`cmd_signoff.go:194`）：
- 只有 `kicad route` 在流程内跑全链门禁：review → kicad-drc → pad-net-diff → intent-rules/widths/lengths → copper-to-edge → isolation → via-current → post-layout-sim → route-complete → silkscreen → board-manual → design-report → signoff（8 个子门禁）。
- EasyEDA 的外部 `--router` 命令**丢掉 `route-complete`**：只有 `summary["router"]=="fastroute"` 时才跑（`internal/app/cmd_pcb_auto_route.go:379`）。
- 内置 pcbauto 只有自己的 `verdict.go`，没有 silkscreen、manual、review、signoff。

做法：

1. **RouterResult 接缝**：`doRoute` 只产出 `{board 路径（.kicad_pcb 或 SES）, router 名/版本/补丁 SHA/参数, unrouted[], violations, blocked[], 用时}`。下游 `qualityGates` → IR 闭环 → design-report → 发布前 review → `signoff` **一行不改**，所有路由器（fastroute、TraceMaker、pcbauto、赛跑胜者）都必须经过它。`route-complete` 改读 RouterResult（规则仍然是：只有被铺铜覆盖、且 KiCad DRC 0 unconnected 才算通过）。EasyEDA 外部 `--router` 也走这个判据，去掉 `=="fastroute"` 的条件。
2. **赛跑的胜负只用“便宜门禁”预筛**，胜者再跑**全量**门禁。全量没过，就让次优者跑全量，不能只交便宜门禁的结果。
3. **TraceMaker 特有的风险都落到现有门禁上，不另设豁免**：
   - neck → `intent-widths`（有 `--sim` 时按段判）和 `signoff-copper`；
   - 没做 creepage → `isolation` 和 `signoff-safety`（送给它的 dru 仍保留 clearance/creepage，布线时就会执行）；
   - 铺铜旧 → 路由后强制重铺，`kicad-drc` 带 `--refill-zones`；
   - 差分对误耦合 → `intent-lengths`，加上 R2 的显式 pairs。
4. **可追溯**：board-manual 和 design-report 写明路由器、版本、补丁 SHA、参数和赛跑记录，作为 `signoff-artifacts` 的检查项。豁免仍然只认签名的 `{gate,match,reason,by}`。
5. **原理图路径**：
   - sch-build、sch-edit 的硬门禁保持：网表 = spec（事务式，不符不写）、ERC、page-fit、quality、intent derive。
   - S2 的进程内连通性只是**提前拦截**，事务末尾的 kicad-cli 网表比对和 ERC 不能省。
   - S7 的版式规则进入 quality 门禁。
   - 合并 WT 时确认 quality 走 `rep.gate()`：工作树里已经改了，要连同提交，并补一个“quality fail → 退出码非 0”的测试。
6. **内置 pcbauto（`pcb auto run --router internal`）也接 RouterResult**，补上 silkscreen、board-manual、review、signoff，或者在文档里明确它不能交付，只能当后备。

---

## 13. 未验证 / 待测

- R2 补丁之后的布通率：禁止 neck 会让 PicoRick 的 Power 类更难布。需要和 R4（布局反馈）一起，在至少 5 块真实板上测，包括 Gas V5 A。
- R8：fastroute 多线程下结果是否可复现，没有测。
- TraceMaker 0.9.0 在布线时执行 dru 的 `edge_clearance`，这一点是读代码得出的（RuleEngine 共用），没有实测。
- kicad-copilot 云端布局的具体算法，只能从 schema 推断为 ELK layered。
- 表中的收益数字，除标“实测”的以外，都是估计。
