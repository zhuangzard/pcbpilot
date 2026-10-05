# EasyEDA Schematic — 连线(pin-aware autoconnect)

> 从 [`schematic.md`](schematic.md) 拆出(RFC #178)。入口、器件放置、Actions 目录、电气铁律仍在
`schematic.md` —— **先读它**,再按需读本文件。

---

## Pin-aware autoconnect — let the planner pick direction/offset

写线/接标记的 daemon 执行路径会校验引脚方向、零长/斜线和穿本体等非法输入，并做写后回读。
以下候选评分负责寻找合法形态，不能替代这些正确性检查。已有连通不证明布局合法；详见
schematic.md 的 typed 写入校验。

`connect_pin` (`sch connect`) keeps the connection **safe** (pin → short wire →
flag/netport, never a netflag on a bare pin), but it still makes YOU pick
`--direction` and `--offset`, so layout quality depends on judgment. **`sch
autoconnect` removes that judgment**: it pulls the real geometry (part bboxes,
pin coords, existing flag/port/label bboxes, title-block keep-out), scores every
`up/down/left/right × offset` candidate with a deterministic cost function
(flag-collision / through-part penalties, shortest-offset + outward-side +
kind-default bonuses), picks the lowest-cost one, and delegates the mutation to
`connect_pin`. Same schematic state + spec → same selection (deterministic).

`sch connect --kind net_label` 使用原生 `createNetLabel(x, y, net)`；方向和偏移只决定
桩线端点，该接口不接收旋转参数，因此不会创建 `__ROTPROBE__` 旋转校准旗。
电源、地和 netport 仍按原流程校准旋转。跳过无用探针不代表宿主已支持原生标签；
接口标注 EDA v4 起提供。Web 4.1.60 可能已创建标签却返回空值；连接器比较调用前后
新增的 Name 属性，并核对坐标、可见性、父导线及真实网名，仅唯一匹配才返回 `verified:true`
（移植自上游 easyeda-agent dbaf316）。读回不完整、调用仍挂起或匹配不唯一时返回
`partial:true, verified:false`，保留桩线与对象 ID，CLI 非零退出；先保存并回读，不能盲重试或
仅因返回空而删除桩线。调用已结束、无报错且页面属性清单**确无新增**时（V3 3.2.149 实测形态），
`connect_pin` 走已现场验证的“给桩线命名并移动其 Name 属性”路径。
兼容性与验证说明见 [schematic.md](schematic.md#原生-net_label-超时191)。

**批次内互斥 (issue #138):** 同一批(--spec / 多 --pin)里**已规划的短桩会当作
既存导线注册回 scene**,后续连接对它做同样的异网硬拒——同器件相邻异网引脚
(隔离 DC-DC 的 B0512S 类四域脚)不再出现短桩共线相触被 EasyEDA 合并成隐性
短路;规划器会自动换方向/offset 错开,四向全堵时按 #64 语义响亮报
"no safe candidate" 拒绝落笔。多域脚器件仍建议 power 上/gnd 下方向分治,给
规划器留出错开空间。**标签 stagger 用真实 marker bbox 预测(#148 Phase-2):**
预测框按 family + direction 使用活体 `getPrimitivesBBox` 标定值,并相对连接端点朝
body 所在一侧偏移,不再用旧的端点居中 24×11 框:ground 为 10×21/21×10、
power 为 6×11/11×6;**netport 的长度跟网名走**(`6*len(net)+8`,下限 31)——
写死 31 会让任何长于 3 字符的网名少算,评分器于是算出「刚好不撞」而渲染出来擦在
一起。**预测框 = 符号本体 ∪ 文字带**:`sch check` 的 marker-overlap 判的就是合并
后的框(power/ground 的网名画在符号旁,长 `6*len(net)`、高 12),判定与生成必须
同一把尺,否则评分器挑的「干净」位置在 check 眼里照样重叠。故
**10-unit pitch 平行脚上相邻 marker 会触发 stagger,自动挑不同 offset 错开**;
候选打分与同批后续连接注册回 scene 使用同一预测函数。

**密集区会拉长桩线超出 `--offset-max`。** 常规档位里一个「既可选(未被 #64 硬
拒绝)又不撞 marker」的候选都没有时,规划器把候选范围扩到 **3×offsetMax** 继续
找干净位置 —— 人工画法本就如此(同侧密集旗阶梯 offset 错列)。所以看到某根桩线
明显比同页其他的长,那是**让开标签**的结果,不是失控;扩展候选照样过全部判据,
#64 短路保护不会被绕过。真机 ceshi 单块回归:markerOverlaps 12 → 3。
残留不可避免的密集重叠由 `sch check` 的 marker-overlap 门捕获(见 Actions)。

**Hard rejects (issue #64):** two hazards are never soft penalties — they make a
candidate *unusable* no matter the offset, because EasyEDA would silently merge
nets and the post-hoc DRC can't see it: (1) a stub whose endpoint or path touches
an existing **foreign-net wire** (endpoint-on-wire = junction = net merge), and
(2) a stub **crossing a non-target pin** (EasyEDA trims+connects there, and the
wire-over-pin rule exempts pin endpoints). autoconnect now pulls existing wire
geometry into the scene automatically; a wire already on the target net is fine
(that's the connection point). **Title-block intrusion is now a THIRD hard reject
(#147)**: a label landing in the A4 图签 keep-out steers to a safe direction, or —
when every candidate enters it — fails rather than dropping a netport on the
明细表 (which layout-lint, part-only, and the geometry-blind electrical check both
miss). If EVERY direction/offset is a
hard reject, autoconnect refuses to place the stub and reports the connection as
failed — resolve the layout (move the part / clear the wire / free the title-block
corner) and retry. **Create-after backstop (#147 DoD2):** the plan hard-rejects a
title-block hit using a NOMINAL box, but a marker whose real rendered width (net-name
text) still spills into the 图签 is caught by a post-batch real-bbox re-read — the
intruding wire+marker is DELETED and that pin failed, so the command never returns
success with a marker on the title block. **Partial-run bookkeeping (#146):** a batch interrupted mid-way
returns `partial:true` + `succeeded[]`/`failed[]` pin lists — **retry ONLY the
failed pins, never replay the whole spec** (a blind re-run stacks duplicate markers
on the already-connected pins, which `NetKnown=false` after a connector drop can't
detect). **Always run `sch check` right after a batch autoconnect** — its new
`duplicate-net-marker` rule is the guard that catches those stacked markers.

**带痕候选不再静默入选。** 硬拒之外的碰撞惩罚是软性累加,此前选中候选哪怕
score 上千(真机:score=1737 的长桩扎进邻组标签区)也照连且报告只显示落选项。
现在**选中候选** score 超过软阈值或 reasons 里含碰撞类惩罚时,结果行会带
`⚠ WARN`(默认档照连);**`--strict`** 则把这类连接直接判失败、不落地。
看到 WARN 的处方是**挪件腾位后重连**,不是忽略它。

**平台会随机吞掉一个连接(stuck-at-99%),autoconnect 现在自己救一次。** 实测 2821 次
connect_pin 里 57 次失败,其中 23 次是 netflag 卡在「请求被丢掉但平台不报错」——
它是随机的,同一脚重发通常就成。所以失败后**重试一次,但只在连接器明确声明回滚
之后**(netflag 失败时它会先删掉已建的桩线);`connector did not respond` 这类
**状态未知**的失败绝不重试 —— 那可能只是我们没等到回应而对方已经建好了,重试会得到
第二条桩线和第二面旗。被救回来的连接在报告里标 `retried`,**别忽略这个字段**:
它是平台在变差还是变好的唯一现场证据。

另外 connect_pin 用的是 **35s 专用预算**而非默认 20s(**裸 `sch connect` 也
已对齐**,此前它还吃 20s 默认值,慢速成功被报成失败):连接器内部最坏路径
(wire 7s + 重试 0.25s + wire 重试 7s + netflag 7s = 21.25s)本来就超过 20s,
默认预算会让 daemon 先于连接器放弃 —— 报「connector did not respond」而对方其实
已经把线和旗建完了(实测 57 次失败里 17 次是这么来的)。**`sch connect` 失败必须
非零退出**；不能仅因回读 pin 已在目标网就报告 `slowLanded` 成功，因为修复前
就可能已经同网但几何错误。明确的几何门拒绝保留原始错误；超时等未知结果须回读
导线、标记及方向后核实，不盲重试。这项错误处理修正尚须随下一开发包部署。

```bash
# single pin by designator:pin (number OR name)
pcbpilot sch autoconnect --pin U1:41 --kind gnd --net GND
pcbpilot sch autoconnect --pin U1:3V3 --kind power --net +3V3

# 同名多脚:尾缀 * 一次连全部(连接器的冗余 VBUS/GND/屏蔽脚,#145)。
# 光写功能名会被判歧义并拒绝——autoconnect 不该替你挑一个;* 是你说"全接"。
# 对单脚是恒等,所以电源/地/屏蔽脚可放心加星。
pcbpilot sch autoconnect --pin J1:VBUS* --kind power --net 5V   # → J1:A4B9 + J1:B4A9

# explicit coordinates (compat with existing flows)
pcbpilot sch autoconnect --x 720 --y 670 --kind gnd --net GND

# preview the plan + rejected options WITHOUT mutating
pcbpilot sch autoconnect --pin U1:41 --kind gnd --net GND --dry-run --json

# batch spec — clustered pins auto-stagger so labels don't stack
pcbpilot sch autoconnect --spec p1-connect.json

# re-run the SAME spec safely — pins already on the target net are skipped
pcbpilot sch autoconnect --spec p1-connect.json          # idempotent, no growth

# re-route pins currently on the WRONG net (delete old flag+wire, reconnect)
pcbpilot sch autoconnect --spec p1-connect.json --replace
```

**Idempotent (issue #50):** before connecting, autoconnect reads each pin's
current net (via `sch list --include-pins`, which now carries `net`) and classifies
every connection into three states — `new` (floating → connect), `already-connected`
(already on the target net → **skip**, no duplicate flag+wire), and `conflict` (on a
different net). A conflict is an error by default; pass `--replace` to delete the old
flag+wire (deleted **together**, so no orphan stub — see issue #51) and reconnect.
Re-running the same spec is therefore safe and never stacks duplicates. `--dry-run`
reports the three states without mutating.

Spec JSON (`--spec`): `{"connections":[{"pin":"U1:41","kind":"gnd","net":"GND"},
{"pin":"U1:3V3","kind":"power","net":"+3V3"}], "rules":{"avoidTitleBlock":true,
"avoidPinFanout":true,"staggerLabels":true,"offsetRange":[18,80],"offsetStep":6,
"minLabelGap":12}}`. Each result reports the `selected` candidate (direction /
offset / endPoint / score), the `rejected` alternatives with reasons, and the
`wirePrimitiveId` / `flagPrimitiveId`. The title-block keep-out comes from the
shared `sch sheet-geometry` derivation (issue #26) — when the sheet bbox isn't
exposed it is reported as **provisional** and not geometrically enforced (so a
guessed box can't corrupt scoring). **Prefer `sch autoconnect`
over hand-picking `sch connect --direction/--offset`** for power/ground/netport
stubs; `sch connect` stays for when you deliberately override the geometry.

---

## 标签 vs 导线、总线与原理图美观度（软层：度量 + opt-in 生成）

优先级固定：**连接正确性 > 可读性 > 美观**。以下规则只决定“同样正确的画法里选哪种”，
不能为了好看移动已经正确的导线、改网名、拆核心/外围归属或放宽任何门禁。
`layout-lint`、`sch check`、`bridge-check`、`layout-score`、DRC 保持原样且优先。

**何时用导线、何时用标签**（与 `sch aesthetics` 的 N1/N2 同一把尺，阈值随风格档）：

- 同模块相邻外围、去耦、上下拉、反馈、串联支路：**真实正交导线**（数据驱动基准的直连不变量）。
  一根信号网在本页、同一框内的全部端点跨度 < `shortLabelSpanUnits`（balanced 120 units，
  ≈ layout-score 实测同排标签最小间距 117）却用了 ≥2 个标签 → N2 报“短程滥用标签”。
- 一棵线树总长 > `longWireUnits`（balanced 600 ≈ A4 图宽 1170 的一半）或与异网交叉 >
  `longWireCrossings`（balanced 2）→ N1 建议改为两端局部 net label / netport（电源地改为就近
  重复的 power/ground 符号）。**只是建议**：跨模块信号本来就该用 netport；改画法须走源数据 →
  重算 → Apply → 回读，不在现场逐根改。
- 跨模块 / 跨页信号：netport（不伪装成电源 flag）；电源/地：就近重复符号，旗体顺导线朝外，
  电源朝上、地朝下（L1/L2 与 `orientation.json` 同一真值表）。
- 交叉：严格内部 X 在 EasyEDA 不导通。异网 X 计入 W2；同网 X（看似结点、实为两岛）和
  四通结点计入 W3——改成错开的两个 T。T 结点不要落在引脚上、离其他拐点/结点 ≥ 10 units（W5）。

**总线**（`sch bus …`，官方 `sch_PrimitiveBus` @beta，**V3 3.2.149 现场已验证，V4 未验证**；多分支回读为一条往返折线，核对按线段集合）：
**宿主只接受 `NAME[a:b]` 总线名**（V3 3.2.149 实测：`U0_UART` 返回空结果，`U0_UART[0:1]` 成功）。所以原生总线只画
**带序号的组**（D0..D7 → `D[0:7]`）；SPI/I2C/UART/SDIO 等协议组保持虚拟标签泳道（报告写明原因），`sch bus create`
与 `sch bus apply` 在写入前拒绝其他名称。

- 总线只是绘图对象，**不建立也不证明**成员连通；成员连通仍以逐 pin 网表、`sch check` 为准。
- 扩展 API **没有总线分支（bus entry）图元**：成员用普通正交导线 + 成员名网络标签接入
  （`connect_pin` / `autoconnect`），不画 45° 斜线（写线守卫拒绝斜段）。
- `sch bus create --name 'D[0:7]' --points x1,y1,x2,y2,… [--points …] --dry-run` 先离线校验：
  每段水平/垂直、非零长、各多段线互相接触；`NAME[a:b]` 以外的名字只警告（宿主语法未验证）。
  不加 `--dry-run` 时写后回读名字与路径，不符即 `partial:true` 非零退出，保留 ID、禁止盲重试。
- 宿主不可用或未验证时用**虚拟总线**：同组成员（D0…D7、SPI/I2C/UART/SDIO、MIPI/USB 对）的
  标签放同一列/行、等节距、同朝向。`sch bus candidates --snapshot page.json` 离线列出候选组与
  泳道质量（N3）；USB/MIPI 是差分对，按对并行，不合并成字母总线。

**布局画原生总线（用户决定 2026-10-03）**：`sch layout-plan [--zones] / lib-layout --aesthetics
balanced|precision` 默认把**完整**的标签泳道画成原生总线（`layout.buses` / `modules[].buses`，官方编码
`busName` + `line` 嵌套多段线）；`functional` 默认不画，`--native-bus=false` 只留虚拟泳道。规则：

- 资格：组成员数 ≥ 风格档 `generate.nativeBusMinMembers`（balanced 3、precision 2、functional 0=关；
  `--native-bus` 显式开启时关档按 3）；每个成员在本区**恰好一个**标签/端口，全部同向、同列（aligned =
  sameDir = 100%）；索引组编号必须连续。USB/MIPI 差分对永不成总线。不合格的泳道在
  `busLanes[].native` 记 `skipped` + 原因，保持虚拟泳道。
- 命名：索引组 `NAME[a:b]`（如 `D[0:7]`）；协议组用组名——前缀已含协议名用前缀（`SPI1`、`UART0`、
  `I2C2`），否则 `PREFIX_KIND`（`U0_UART`、`ESP_UART`），无前缀用 `SPI`/`I2C`（`schaes.NativeBusName`）。
  非 `NAME[a:b]` 名字的宿主语法尚未现场确认，`sch bus create` 只警告。
- 几何：主干沿标签列外侧（距最远标签本体/文字 ≥ 15 units），每个成员一条梳齿、停在其标签前 5 units；
  只有正交段、5-unit 格、梳齿起点在主干上。总线**不碰任何东西**：与器件本体、位号、引脚、标记（本体 +
  文字带）、导线、标记引线、其他总线及其名字框都保持 ≥ 5 units（比“只在 tap 处接触”更严）；放不下就
  外移最多 20 units、再试无梳齿主干，仍不行则记原因留虚拟泳道。
- 连接：成员照旧是普通导线 + 同名标签/端口；总线永远不是连接证据。`validateSchNativeBuses`（compose）、
  `sch bus check`、`sch aesthetics` 的 `busChecks` 都要求每个成员网在引脚上且有自己的标签/端口；否则
  报 `member-without-label` / `member-without-pin`（error），N3 也不给该总线记分。
- 宿主：`--bus-host absent`（health/api probe 无 `sch_PrimitiveBus`）→ 不画，`fallback-virtual`；
  `--bus-host unverified`（V4 未现场验证）→ 照画，状态 `host-unverified`。
- Apply：`sch compose --playbook` 在 `wire-tree-check` 之后、保存之前追加 `sch bus apply`：api probe
  （缺 API → 回退虚拟总线、不写）→ 读页 → 日志分类（日志 ID 且名字 + 线段集合一致 = 本工具建的 → 按
  ID 替换；日志 ID 已不在 → 剪除；不在日志 = 用户的 → 永不删除，完全相同的不重复建）→ 成员检查与“不碰
  导线/引脚”检查（失败则不写）→ 删旧 → 逐条 create（连接器按无向线段集合回读）→ 每次写后落日志 →
  终态回读（自建的等于计划、用户的未变、总数 = 用户 + 自建）。重跑只替换、不重复；
  `sch bus apply --rollback` 按日志精确 ID 删除。`compose --replace` 若页上有日志证明不了的总线，编译时
  拒绝（`sch clear` 会删掉它），由用户自己处理；日志默认 `<项目根>/.pcbpilot/bus-journal/<project>_<doc>.json`
  （`--bus-journal` 覆盖）。现场验证流程见 `docs/reviews/2026-10-schematic-aesthetics/live/README.md`（待执行）。

**度量命令（只报告）**：

```bash
pcbpilot sch list --include-pins --include-bbox --include-wires --project P --doc <page> > page.json
pcbpilot sch aesthetics --snapshot page.json            # 或 layout-plan / lib-layout 输出、canonical 快照
pcbpilot sch aesthetics --snapshot page.json --style precision --json
pcbpilot sch aesthetics --project P --doc <page>        # 现场只读（live-unverified）
```

18 项：布线 W1–W8、版面 L1–L7、标签/总线 N1–N3，每项 0–100 并给阈值与来源；缺数据的项
`skipped`（不算满分），布线组乘“已连引脚份额”（未连的引脚不能显得整齐）。权重 0、永远
exit 0、不是门；分数只用来排 Phase B 生成器的改进，不用来签字。风格档与 `pcb aesthetics`
同名（functional / balanced / precision / auto / custom），只改软目标。

**生成（Phase B，opt-in）**：离线 `sch lib-layout --aesthetics STYLE` / `sch layout-plan [--zones]
--aesthetics STYLE` 把上面的规则变成生成动作，每步过全部门禁、不过即回滚（流程与门禁细节见
[auto-layout-sop.md](auto-layout-sop.md) §2「美化生成 Phase B」）：

| 规则 | 生成动作 | 阈值（风格档 `generate`） |
|---|---|---|
| 少转折、少交叉 | trunk 重布：候选按 [异网交叉, 长度 + 转折代价] 预排；direct 网另走加权迷宫（`bendCostUnits` / `crossCostUnits`） | functional 10/40、balanced 20/80、precision 30/150 |
| T 结点离拐点 ≥10、无四通 | 支路改落到岛上其他点（错开的 T）；被撞标记一并重放 | `junctionClearanceUnits` 10 |
| 不穿本体/标签、同网不假交叉 | 标记重放 + 门禁（穿越、文字重叠计缺陷，先于总分） | — |
| 落格 | 只在 5-unit 格生成；对齐位移本身取整到格 | — |
| 长线 → 标签 | 仅标签策略网的非核心↔外围 trunk；拆后两岛各自就近命名 | `longWireUnits`/`longWireCrossings`、`labelSplit` |
| 虚拟总线 | 同组标签统一方向 → 统一列 → ≥3 个时等节距（短外向 jog） | `busPitchUnits` 10 |
| 行列对齐 | 同类外围平移吸附共享行/列，核心不动，只在空闲处 | `alignMoveUnits` 0 / 20 / 40 |

自定义：`--style-file` 里的 `"generate":{…}` 覆盖以上键（范围校验；连接/归属/门禁类键仍拒绝）。
实测（离线 fixture，balanced）：AMS1117 lib-layout 交叉 1→0、W5 0→100、总分 81.8→96.3；
ESP32-v05 MCU 页 MCU 区缺陷 5→4、总分 50→71.6、PWR 页 BUCK 区缺陷 3→1。完整前后表与预览：
`docs/reviews/2026-10-schematic-aesthetics/phaseB/`。
