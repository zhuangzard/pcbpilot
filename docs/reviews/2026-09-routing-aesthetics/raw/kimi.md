# PCB 布局布线美学：可评分 + 可生成的设计评审报告

调研基础：已通读 `pkg/pcbauto/joint.go`、`router.go`/`post.go`、`internal/app/pcb_layoutscore.go`、`pcb_score_tidy.go`、golden-board harness(`pcb_layoutscore_golden_test.go`）及 `pcb.beautify` typed action。以下结论均落在现有数据结构上（`Track{Layer,A,B,Width,Net}` 逐段线段 + `Via{Net,C,Dia}`，布局侧 bbox/rotation/anchor)，不需要新的数据模型。

## 1. 指标体系（计算口径 + 归一化 + 阈值）

### (b) 布线美学（当前完全空白，priority 最高）

| # | 准则 | 可计算度量（几何定义） | 归一化 / ramp(100→0) | 与电气冲突？ |
|---|---|---|---|---|
| R1 | 弯折密度 | 每 fully-routed 网 bends 数 / 布线总长（×1000 mil)；剔除 pad-entry 段与 tuning 蛇形（`tune.go` 已标记） | ≤0.8/1000mil→100,≥4→0 | 与 detour 冲突：为少弯绕远。用几何平均让两者互相制衡 |
| R2 | 拐角一致性 | 90° 角中已被 chamfer 的比例 + 同网倒角比一致；锐角（<45°）计数 | chamfer 率 ≥90%→100,≤30%→0；锐角 >0 直接扣至 ≤60 | 无（chamfer ≤3W 已是现状） |
| R3 | Pad entry 质量 | 末段进入角 vs pad 长轴夹角；入口点距 pad 中心偏移占 pad 宽比例；SMD 禁角部进入 | \|Δ角\|≤30° 且偏移≤25%→100；≥90° 或角部进入→0 | 与 RF/差分 entry 冲突：RF 馈线、差分对豁免 |
| R4 | 层方向纪律 | 每层信号长度中沿该层优选 H/V 方向的比例 | ≥85%→100,≤60%→0 | 有：强制转向可能切断回流路径。规则：换层优先发生在平面连续处，方向分让位于 `layerMul`/split-ref cost |
| R5 | 总线梳齿度 | 同 bus 组（net 前缀/intent 分组）内平行段（夹角≤15°，复用 `findParallelCoupling` 的几何）长度占比 + 平行间距 CV=σ/μ | 平行占比≥60%→100,≤20%→0;CV≤0.08→不扣，≥0.5→扣至 0 | 有：平行长走线耦合。平行加分仅在间距≥3W 时生效，否则按 3W 电气规则封顶 |
| R6 | 通道间距均匀 | 同层相邻平行异网段间隙序列的 CV（按通道分组） | CV≤0.1→100,≥0.6→0 | 无 |
| R7 | 锯齿/短 jog | 同向双弯且中间段 < max(3W, 10mil)；或对顶 jog | 每处扣分，0 处→100,≥5 处/板→0 | 无 |
| R8 | 过孔对齐 | 同 net/bus 的 via 落在公共行/列栅上的比例（沿用 tidy 的中位拟合线残差法，阈值 5mil) | ≥90%→100,≤40%→0 | 与 via 阵列/电流冲突：豁免 `viaarray.go` 生成的电源阵列 |
| R9 | 镜像对称 | 检测到镜像通道（同前缀 nets + 对称 pin 对，如 DDR byte lane / 两侧 decap):两半边走线长度比 + 拐点数比 | 长度比 1.0–1.15→100,>1.5→0 | 无（对称利于等长） |
| R10 | 铜面视觉平衡 | 铺铜碎片岛计数、细颈（<2×最小线宽）铜条计数 | 纯低权重项（组内 ≤0.1)，只报不狠扣 | 与 EMI 开槽冲突：EMI 开槽豁免 |

### (a) 布局美学（扩展现有 tidy 维，五子规则保留，加三子规则）

| # | 准则 | 度量 | ramp |
|---|---|---|---|
| P1 | 对齐行/列覆盖 | 全部件 anchor 对候选对齐线（同轴簇 + 中位拟合，推广 tidy array 算法）的残差 | 残差≤2mil 覆盖率 ≥80%→100,≤40%→0 |
| P2 | 镜像模块布局 | 从网表拓扑检测对称 decap/通道组，组内质心相对 IC 中面的镜像偏差 | ≤5mil→100,≥50mil→0 |
| P3 | 丝印阅读方向 | 同面位号旋转归类， majority 方向占比（IPC 阅读方向一致性，现状只有 silk-side 四方位） | 同 tidy 扣分机制 |
| — | 已有：grid-landing / 朝向一致 / 位号同侧 / 字号一致 / 阵列等距 | 不变 | 不变 |

**冲突解决总原则（写死为优先级规则）:** 电气（EMI/SI/PI/creepage/current)→ DRC → 效率（detour/vias)→ 美学。任何美学项不得以牺牲 hot-loop、3W、回流连续、爬电为代价；豁免清单（RF、差分、tuning 蛇形、电源/via 阵列、EMI 开槽）在度量里硬编码排除，而不是事后解释。

## 2. 融入 joint score

- **新增第 4 组 `aesthetics`**:`jointGroupWeight` 改为 electrical 0.45 / efficiency 0.20 / placement 0.25 / aesthetics 0.10。电气不动；placement 与 efficiency 各让 5%。继续用**加权几何平均**——它天然防"一组烂被另一组平均掉"，也防美学拿高分遮电气丑。
- **布局侧**对应把 tidy 权重 0.5 → 0.8、compact 0.8 → 0.7(P1/P2/P3 进 tidy)，保持 layout-score 内部加权算术平均不变。
- **防刷分：** ① 美学只统计 fully-routed 网，未布网不进分子分母，但 completion² 已在 overall 里重罚（现有机制，"少布线少弯折"不成立）;② R1/R7 全部按长度/每板归一，不数绝对值；③ 加单调性回归测试：注入 jog、打乱间距、去 chamfer 的合成板上分数必须严格下降；④ 各组 floor 沿用 `max(s,1)`，美学 0 分不秒杀整板（gate 仍只归短路/重叠）。
- **校准：** 沿用现有 harness 纪律——5 块 golden reference 板新组不得低于下限；新建**合成负对照**（在 reference dump 上注入 jog/乱间距/混倒角 → `maxDimension` 上限断言必须响），这比找人标数据便宜且可 CI。人工/VLM 偏好数据只做**权重拟合**（见 §5)，不进 CI 门禁。

## 3. 生成——布局

1. **snap-to-alignment-lines 后置 legalisation（首选，放 placer 之后）:** 对 anchor 坐标做行/列候选线抽取（排序后差分聚类），贪心 + 小规模 LP 把件吸到线上，目标 Σ位移最小，约束复用现有 overlap/净空检查。不动 placer 主算法，风险最小，直接喂给 `pcb align` 已有执行器。
2. **朝向 legalisation：** tidy rotation 的多数派即目标朝向，碰撞/DRC 检查通过则 `modify` 旋转；输出与 P1 度量同源，修完即涨分。
3. **对称放置：** 从网拓扑检测镜像组（IC 电源对脚 + 同值 decap)，作为 placer 的 soft symmetry constraint（惩罚质心镜像偏差），与现有 module/group 机制合流。
4. 阵列/栅格吸附已有 grid-snap，与 P1 合并为一条执行路径，避免两套"对齐"各有各的说法。

## 4. 生成——布线

1. **A* cost 增补（`search()` 步进 cost，局部改动）:** ① jog penalty——同向双弯且间距 < k·g 时第二弯加价（直接抑 R7);② bus 对齐场——同 bus 已布成员产生弱吸引势（沿其走廊中心线，强度 ≪ congestion)，引导后续成员梳齿化（R5);③ 通道居中势——在非拥挤通道内轻微拉向中线，防止 hugging(R6);④ 现有 bend/wrong-dir/via cost 不变。所有新项必须是 **soft multiplier**，严格模式下不可突破 DRC/claim。
2. **布线顺序：** bus 组整体排序优先（整组连布，而非逐 net)，再 fanout，再电源；rip-up-and-reroute 的受害者选择加入"美学损伤" tie-breaker（同等电气代价下拆丑的）。
3. **后置美化 pass（纯 Go，全在 `post.go`，每步复用 `segmentOK`+DRC 二分修复）:** ① jog 消除/之字拉直；② pad-entry 清理（末 ≤2 段沿 pad 轴、居中进入）;③ 同网 chamfer 比统一 + 把 `pcb.beautify` typed action（已存在，倒弧/泪滴/DRC 修复现成）接入 autoflow 收尾；④ bus 等间距横向重排（贪心平移 + DRC 验证）;⑤ via 行列吸附。橡胶带拉直（`stringPull`）本质已是 topological 后处理，够用；**不建议**现阶段换全 rubber-band router，投入产出不成比例。

## 5. VLM 渲染评审

**值得做，但只做两件事：** ① 用 `svg.go` 固定样式渲染 → VLM **成对比较**(A/B 相对判断远比绝对打分稳定），产出 pairwise preference 数据；② 用 Bradley-Terry 模型**离线拟合美学子权重**，校准 R1–R10 的内部权重。红线：VLM 信号永不进运行时打分、永不进门禁、永不替代几何度量；渲染样式锁定（防"靠渲染好看刷分")；只作为 golden-board 权重标定的旁证，与人审一致才采纳。

## 6. 优先级计划（影响/成本排序，均为本仓库 Go 可实现）

| # | 事项 | 影响/成本 | 验收 |
|---|---|---|---|
| 1 | 接入 `pcb.beautify` 到布线 pipeline 收尾（倒弧/泪滴现成） | 高/低 | 5 块 fixture bench 前后对比：R2 分上升，completion/DRC 不降；`make fixture-bench` 回归 |
| 2 | 新评分核 `pcb_score_routeaesth.go`:R1 bend 密度 + R3 pad-entry + R7 jog（纯几何离线） | 高/中 | 合成 polyline 单测全 ramp 断言；注入 jog 的 degraded fixture 必须响；golden 板不掉分 |
| 3 | A* 增补 cost(jog penalty + bus 对齐场 + 通道居中） | 高/中 | fixture bench:R1/R7 改善且 completion/HS findings 不回退（沿用 `betterHS` 式保留判据） |
| 4 | post-pass:jog 拉直 + pad-entry 清理 + chamfer 统一 | 高/中 | 同 #2 度量修后必涨；DRC=0;before/after SVG 双人审通过 |
| 5 | R4 层方向纪律 + R8 via 对齐度量 | 中/低 | 合成层方向 fixture；golden 板阈值断言 |
| 6 | bus 检测（前缀/intent)+ R5/R6 梳齿与间距均匀度量 | 高/中 | mipi(DDR/显示排线）板上 bus 检出率；间距 CV 改善 |
| 7 | bus 等间距 combing post-pass | 高/中高 | 同 #6 板 before/after;DRC 0；蛇形等长不被破坏（`tune.go` 标记豁免验证） |
| 8 | 布局 snap-to-alignment-lines legalisation + P1 度量 + 朝向修复执行器 | 高/中高 | tidy 对齐子分修后=100；负对照（打乱的件阵）必须响 |
| 9 | 对称检测与镜像放置/走线（P2/R9) | 中/中 | k230/rk3568 板镜像 decap 组检出；镜像偏差分改善 |
| 10 | VLM 成对比较 + Bradley-Terry 权重标定脚本（离线，`scripts/`) | 中/低 | 拟合后几何分与 VLM/人审一致率 ≥80%;CI 不依赖 VLM |

**总原则：** 先度量（#2/#5/#6）后生成（#3/#4/#7)，度量未合入并配好负对照之前，不动 router cost——否则生成端在优化一个没有校准的目标。全程沿用仓库既有纪律：golden 板不掉分、负对照必须响、合成满分不算数。
