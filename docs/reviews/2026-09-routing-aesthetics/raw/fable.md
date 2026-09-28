# PCB 布局/布线美观度评审（Fable 独立评审）

依据：`pkg/pcbauto/{router,post,joint,placer}.go`、`internal/app/pcb_layoutscore.go`、`pcb_score_tidy.go`、`esp32-routed-preview.svg`（只读）。

## 0. 先看样例板暴露了什么

对 SVG 做了几何统计（250 段 track、69 via、30 器件）：

| 观察 | 数据 | 对应美观缺陷 |
|---|---|---|
| 非八向段占 33% | 角度直方图：0/45/90/135° 共 168 段，其余 82 段散在 5°–170° | `stringPull` 允许 pad 首末段任意角 → 每个 pad 入口一根"歪脖子"斜线，这是第一眼最刺眼的东西 |
| 41% 段 < 8px（≈ 40 mil） | 102/250 | chamfer 碎段 + 入口小折，视觉上"锯齿" |
| 69 via / ~40 连接 | ≈1.7 via/连接 | ViaCost=60 mil 太便宜；小板 2 层足以走完，via 应 ≤0.5/连接 |
| USB_DM/DP、+3V3 大绕行 | 左下 D2→J2 沿板底跨半板 | 无"同区就近入层"策略；也没有 detour 之外的视觉惩罚 |
| U3 引脚出线全部 ±30° | ESP32 两侧 pad 出线不沿 pad 轴 | 缺 pad-entry 规则 |
| 器件行列已基本齐（tidy 起作用） | C6/C7、R8/R9 等对齐 | 布局侧现有 `tidy()` 是对的方向，但只做了 grid/朝向，没做行列共线 |

结论：当前系统"电气正确、视觉业余"的主因 90% 在**布线后处理与 A* 代价**，10% 在布局对齐。美观度不是要新引擎，而是要补 **指标 + 3 个 post pass + 2 个代价项**。

## 1. 分类学：可计算的美观判据

### (a) 布局（placement）

| ID | 指标（几何定义） | 归一化 0–100 | 与电气冲突/优先级 |
|---|---|---|---|
| P1 行列共线 `align` | 同类/同组器件中心，按 x 或 y 聚类（DBSCAN eps=2 mil），"孤立点"=不在任何 ≥2 成员线上的比例 r | 100·(1−r)，r≥0.5→0 | 无冲突；去耦跟随 IC pad 时以 tether 为准，只在 tether 允许的 ±slack 内对齐 |
| P2 等距 `pitch` | 已有 `array-irregular`，改为 CV(步距) | ramp(CV, 0.02, 0.15) | 无 |
| P3 朝向一致 | 已有 rotation 子规则 | 已有 | 极性件豁免（已有 0.3 因子） |
| P4 镜像/对称 `symm` | 由网表识别同构子电路（同型号+同拓扑哈希，如双通道 LDO、两组按键）；对称轴为两组质心中垂线；分数 = 1 − mean(‖p_i − mirror(p'_i)‖)/module span | ramp(err, 0.05, 0.4) | 只在 spec 声明或自动识别成功时计分；否则 skipped |
| P5 模块矩形度 `blockRect` | 每个功能块凸包面积 / 成员 bbox 并集面积 | ramp(1/ratio, …) 或 ratio≥0.75→100，≤0.45→0 | 与 flowOrder 兼容 |
| P6 边距一致 `edgeMargin` | 沿板边器件到边距离的 CV | ramp(CV, 0.05, 0.3) | edge-IO 优先 |
| P7 丝印位号 | 已有 silk-side/silk-style；增加"位号不压 via/不出板" | 已有 | 无 |

### (b) 布线

| ID | 指标 | 归一化 | 冲突处理 |
|---|---|---|---|
| R1 折弯密度 `bends` | 每英寸方向变化数（不含 45° chamfer 对）；按网加权平均 | ramp(b, 4, 15 /in) | 差分/HS 蛇形延时段豁免（`tuneLengths` 输出标记 kind=tune） |
| R2 非八向段 `offOcti` | 非 0/45/90/135 段总长 / 总长 | ramp(f, 0, 0.08) | 允许 BGA 出线/斜 pad；≤ 1 grid 的 pad 内段不计 |
| R3 碎段 `fragments` | 长度 < 2·width 且非 chamfer 段数 / 总段数 | ramp(f, 0.03, 0.25) | 无 |
| R4 pad 入线 `padEntry` | 入线方向与 pad 长轴夹角 θ；θ>30° 或入线起点偏离 pad 中心 > 0.25·pad 短边 → 坏 | 100·(1−坏比例) | 密 pitch 连接器允许 45° |
| R5 层向纪律 `layerDir` | 每层 H 段长/(H+V)；偏离该层 pref 的比例 | 2 层板不计，≥4 层 ramp(wrong, 0.15, 0.5) | 短跳线(<100 mil)豁免 |
| R6 via 数/对齐 | via/连接（已有）+ via 落 25 mil 网格比例 + 同网相邻 via 共线 | ramp(vpc, 0.3, 1.5)；grid 比例直接 | 电流 via 阵列豁免 |
| R7 总线梳理 `busComb` | 同方向、间距 < 4·width 的平行段簇：簇内间距 CV + 拐角处 45° 同步（相邻线拐点差 = 1 pitch） | ramp(CV, 0.05, 0.3) | 差分对内间距由 SI 定，只评簇间 |
| R8 平行等距/通道均布 | 通道（两障碍间）内 track 的间距 CV；贴障碍 < 1 clearance 者比例 | ramp | 与 crosstalk 同向，无冲突 |
| R9 stub/T 头 | 段末端既非 pad/via 也非另一段中点 | 计数，>0 直接 −20/个 | 无 |
| R10 走线长度悬殊 `detourVis` | 已有 detour；追加"单网最大 detour"（一根绕全板的线即使总比好看也毁） | ramp(max, 1.5, 3) | 无 |
| R11 铜皮 | pour 岛面积 < 4·via 焊盘 → 孤岛；pour 边缘锯齿（顶点/周长）；铺铜后裸线比例 | ramp | 电气优先：孤岛应删 |
| R12 泪滴/颈缩一致 | neck 段宽度种类数/网、neck 出现位置是否只在 pad 前 | 报告级 | 不入分 |

优先级规则（写死）：电气 hard rule（clearance、爬电、电流宽、参考层完整、diff 对）→ DRC → 完成率 → 美观。美观项只在 **不改变网表、不降低任何电气子分** 的动作空间里优化（实现上：post pass 每步用 `segmentOK(strict)` + 重算 hot-loop/decap 距离不劣于原值才接受）。

## 2. 积分整合

- 新增第四组 `aesthetics`，权重建议 electrical 0.40 / efficiency 0.20 / placement 0.25 / aesthetics 0.15。保持**几何平均**（现有），但 aesthetics 组内用算术加权（子项彼此可补偿，单项 0 不该拖垮全组）。组分下限 clamp 到 30 再入几何平均，防止一个 R9 stub 把全板打成 0。
- **防作弊**：所有布线美观指标只在 `completion ≥ 100` 的网上计算，且分母用**总连接数**而非已布连接数——未布的网按最差值（0）计入 R1/R2/R4。这样"少布线换少折弯"必然降分。另加 gate：aesthetics 组不参与 `CompletionFactor`，永远乘在后面。
- **不能变成新 gate**：`Deliverable` 判定不看 aesthetics。
- **校准**：(1) 5 块 fixture 板用现有 `make layout-calibrate` 模式，取原厂人工布线作为 golden，要求每个 R 项 ≥ 75；(2) 负对照 = 对 golden 施加已知破坏（随机加 jog、把 pad 入线旋 30°、via cost=0 重布）后必须掉 ≥ 15 分；(3) 人工偏好：对同一板生成 4–6 个变体（不同 post pass 开关），做 pairwise 标注，用 Bradley-Terry 拟合子项权重，样本 ~200 对即可稳定；数据存 `internal/app/testdata/boards/<board>/prefs.jsonl`。

## 3. 生成——布局

现有 `placer.tidy()` 已做 180→0 折叠、组朝向多数派、5 mil 吸栅，建议在其后加 **`alignPass()`**（不动 anneal 内核）：

1. **对齐线聚类**：对每个 zone 内的 movable 件，按中心 x、y 各做 1D 聚类（eps = 15 mil），得到候选行/列线（取簇中位数）。
2. **合法化贪心**：每件按 tether slack 从小到大处理，尝试把中心吸到最近簇线（只在 tether/keep-apart/hardCost 不变差且 localCost 增幅 ≤ 3% 时接受）。这正是现有 `try()` 的形态，复用即可。
3. **等距**：同一行 ≥3 件，用 1D LP（Go 里直接闭式：固定首尾，中间均匀分布）；接受判据同上。
4. **对称**：`circuit.go` 已有 Blocks/Chains，加一个"同构块检测"（成员型号多重集 + 引脚网连通模式哈希）；识别到成对块后先摆一块，另一块用 mirror(pos, axis) 作为 seed 进 anneal，并在 `partCost` 加 `symmCost = 3·‖p − mirror(p')‖`。
5. 位号：`silk-align` 后统一 side（多数派）；现有只报不修的缺口在这里补。

不建议进 anneal 目标函数做 ILP：件数 ≤ 200 时贪心+局部 LP 足够，且可解释可回退。

## 4. 生成——布线

### A* 代价项（`router.search`）
- 90° 折弯 2g / 45° 0.4g 保留，但 **改成 45° 对代价 0.8g**（现在两次 45° = 0.8g < 一次 90° 2g，正确），另加 **"折弯间距惩罚"**：距上次折弯 < 3 格再折 +1.5g（消灭锯齿）。需要在状态里带 `sinceBend uint8`，只影响 dir 缓存。
- `ViaCostMil` 60 → **150** 起，随层数：2 层 200，4 层 120，6 层 90；样例板 69 via 直接减半。
- 层向：`WrongDirCost` 1.6 保留；对 2 层板 pref 为空 → 建议给 TOP=h/BOTTOM=v 的**软**偏好 1.15 而非 1.6。
- 邻线吸附：`pairField` 已有，把它泛化成 **busField**：同一 bus（spec 或名称前缀 D0..D7、IO*）先布第一根，后续根在 1 pitch 处 ×0.6，2 pitch ×0.8。
- pad 入口：`access()` 生成的 access 节点只保留 pad 长轴方向 ±1 格（有 45° 备选），密 pitch 才放开。

### post pass（`post.go`，在 stringPull 之后、DRC 前）
1. **padEntryFix**：首/末任意角段 → 拆成"沿 pad 轴直出 ≥ 1 grid + 45°/90° 接回"，`segmentOK(strict)` 验证；不通过保留原样并计入 R4。
2. **jogRemove**：模式 `A→B→C→D` 中 B、C 为一对反向 45°/90° 且 |BC| < 3·width → 试连 A→D（八向）或平移中段；是现有 `stringPull` 漏掉的"非最远可见段"情形。
3. **busComb**：对簇（R7 定义）按中线重排为等距，并把拐角对齐成同步 45°；只在所有成员 `segmentOK` 时整簇接受。
4. **viaSnap**：via 吸到 25 mil 网格、同网相邻 via 共线，仅当移动 ≤ 半格且 `viaCost` 非 Inf。
5. **spreadInChannel**：对通道内 track 做 1 次 "equal-gap" 平移（ripple），贴障碍者向中间移；这是"均布而非贴边"的实现。
6. **rip-up 顺序**：negotiate 收敛后，按 R1+R2+R10 单网得分最低的前 10% 网做 1 轮"美观重布"（严格模式，代价加折弯间距项），结果只在完成率不降、DRC 不增时接受。
7. 泪滴、圆弧角：**不建议**现在做（EasyEDA typed API 对 arc track 支持要先验证；泪滴是 pour 层面的事）。列为 unsupported/planned。

拓扑/橡皮筋路由器：**不建议**换。grid A* + 好的 post pass 在这类板上足够；rubber-band 是 ≥ 6 层 BGA 密板的收益，成本是重写。

## 5. VLM 视觉评审

可作**第二信号**，不入 joint 分数：
- 用途：(a) 校准阶段当"廉价人工"生成 pairwise 偏好；(b) 报告里给一段自然语言批评，指向具体 net/ref；(c) 回归里检测几何指标没覆盖的新缺陷类型（发现后立即转成几何指标）。
- 保持诚实：固定渲染样式（同一 `svg.go`，隐藏位号可选）；VLM 只输出结构化 JSON（issue 列表 + 位置 bbox），每条 issue 必须能在几何上定位到 ≥1 个对象，定位不到即丢弃；不允许输出总分；同一板渲染两种配色一致性 < 0.8 即视为不可用。
- 风险：样式偏好污染、对 via/层看不清、对"绕行"敏感但对"clearance"盲；因此 VLM 结果永远是 findings，不是 score。

## 6. 优先级 Top 10（收益/成本）

| # | 项 | 成本 | 验收 |
|---|---|---|---|
| 1 | R1/R2/R3/R4/R6/R10 六个布线指标 + `aesthetics` 组入 joint（防作弊分母） | 2 d | 5 fixture 板 golden ≥ 75；负对照掉 ≥ 15；ESP32 样例 R2 当前应 < 40 |
| 2 | ViaCost 按层数提高 + 折弯间距惩罚 | 0.5 d | ESP32 via 69→≤35，完成率不降；fixture-bench 完成率 ±1% 内 |
| 3 | padEntryFix post pass | 1 d | R2 ≥ 90、R4 ≥ 85；DRC 0 |
| 4 | jogRemove + fragments | 1 d | R3 碎段比例 < 5%；总长不增 |
| 5 | 布局 `alignPass`（行列聚类 + 等距） | 1.5 d | P1 ≥ 85；layout-calibrate 九维不掉；bbclaw 对齐率从 21/69 提升 |
| 6 | viaSnap + 同网 via 共线 | 0.5 d | via 落格 ≥ 90%；native DRC hole-to-hole 0 |
| 7 | busComb（bus 识别 + 等距 + 同步拐角） | 2 d | rk3568/k230 数据总线 R7 ≥ 80 |
| 8 | 美观 rip-up 轮（最差 10% 网重布） | 1 d | joint aesthetics 组 +10 且 electrical 不降 |
| 9 | 对称块检测 + 镜像 seed | 2 d | 双通道 fixture（需新建）P4 ≥ 80 |
| 10 | pairwise 偏好数据 + Bradley-Terry 权重拟合；VLM 作标注助手 | 2 d | 权重与人工 Kendall-τ ≥ 0.6 |

每项验收都以离线 fixture（`go test -short`）+ `make fixture-bench` 为准，现场板只作最终回归；改 `pcb_score_*` 判据前先跑 `make layout-calibrate`。
