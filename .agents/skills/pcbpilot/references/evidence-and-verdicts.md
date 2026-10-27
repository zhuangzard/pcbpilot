# 证据与结论词汇（整个 Skill 共用）

> 用途：任何检查、评审、签核或“能不能投板”的回答，都用本页的结论词汇和证据强弱排序；
> 每个 pcbpilot 门禁/报告的输出如何换算成这些词汇见第 4 节。命令参数以 `--help` 为准。

## 目录

1. [两个维度：结论 vs 验证状态](#1-两个维度结论-vs-验证状态)
2. [结论词汇](#2-结论词汇)
3. [证据强弱排序与冲突规则](#3-证据强弱排序与冲突规则)
4. [pcbpilot 输出 → 结论](#4-pcbpilot-输出--结论)
5. [汇总与措辞](#5-汇总与措辞)
6. [跨任务共用的判定规则](#6-跨任务共用的判定规则)

## 1. 两个维度：结论 vs 验证状态

| 维度 | 回答的问题 | 取值 |
|---|---|---|
| **结论**（verdict） | 这一项检查的结果是什么 | `PASS` / `WARN` / `FAIL` / `INCOMPLETE` / `UNSUPPORTED` / `BLOCKED` |
| **验证状态**（status） | 这个结论/样例/能力建立在什么上 | `source-only` / `offline-verified` / `live-verified` |

两者独立：同一个 `PASS` 可以只是 `offline-verified`（例如对旧快照算的），必须两者都写。

验证状态沿用 [Skill 设计](../SKILL.md#样例与能力状态) 的定义，落到检查结果上是：

| 状态 | EasyEDA | KiCad |
|---|---|---|
| `source-only` | 只有需求、数据手册、样例或计划，未对工程执行 | 同左 |
| `offline-verified` | 对导出的 JSON（`pcb dump --include-copper`、`sch connectivity`）离线计算；数据可能已过时 | 对 `kicad snapshot` / `kicad netlist` 产物离线计算 |
| `live-verified` | 在用户已连接的编辑器里写入后 save → 有界 `doc reload` → 新鲜回读，并在这份回读上检查 | KiCad 没有在线编辑器：**已保存且已在 KiCad 中关闭**的工程文件，由 `kicad-cli` / pcbnew 重新加载后检查（`kicad sch-check`、`kicad route` 的 `kicad-drc`、`kicad fab`），等同 live |

文档修改、源码里存在某检查、离线单元测试都不能把结论升级为 `live-verified`。

## 2. 结论词汇

| 结论 | 含义 | 能否说“通过” |
|---|---|---|
| `PASS` | 检查在**新鲜、完整、范围明确**的输入上运行完毕，0 阻塞项、0 未处理告警 | 能（写明范围） |
| `WARN` | 运行完毕，无阻塞项，但仍有 WARN 级发现（含已知误报） | **不能**；写“未通过（N 条告警）”并逐条给证据 |
| `FAIL` | 运行完毕，有阻塞项（ERROR/FATAL、短路、开路、门禁失败） | 不能 |
| `INCOMPLETE` | 也叫 cannot-check：跑了但证据不足——输入缺失、过时、只读到部分页/类别、超时后无回读、读回失败、未保存、两份证据互相矛盾 | 不能；写缺什么、怎么补 |
| `UNSUPPORTED` | 该能力不存在（无 typed action / 子命令，或宿主不支持），含 `planned` | 不能；写“未检查（不支持）”，不得改走 GUI 补 |
| `BLOCKED` | 前置条件不满足，检查根本没跑：connector 未连、页面未打开、版本门禁拒绝、窗口在后台 DRC 不返回、文件被 KiCad 占用 | 不能；先解决前置条件再跑 |

硬规则：

- **缺证据或证据矛盾 → `INCOMPLETE`，永远不是 `PASS`。** “没报错”只有在检查确实覆盖了该对象时才算数；
  空列表要先确认是 known-empty（完整读取后真的为空）而不是 unknown（没读到）。
- 退出码 0 不等于 `PASS`：`sch check`、`pcb check` 默认有发现也退出 0（见第 4 节），要读发现列表或加 `--strict`。
- 签名 waiver（`{gate, match, reason, by}`）放行的项写 `PASS（waived）`，在报告里保留原始失败项、理由和签名人；
  waiver 只来自用户，Agent 不自己写。
- `UNSUPPORTED` 与 `BLOCKED` 的区别：前者换环境也做不到，后者修好环境就能跑。

## 3. 证据强弱排序与冲突规则

从强到弱。结论只能建立在**覆盖该对象**的最强可得证据上；弱证据只能用来发现问题，不能推翻强证据给出的失败。

1. **权威宿主的新鲜回读**：EasyEDA save → `doc reload` → typed 读取；KiCad 关闭后由 kicad-cli/pcbnew 重新加载的已保存文件。
2. **官方工具的逐条结果**（同一份新鲜数据）：`kicad-cli` ERC/DRC JSON、`pcb drc --json` 的逐条行。
3. **pcbpilot 重建检查**（同一份新鲜数据）：`sch check`、`sch bridge-check`、`sch nets`、`sch reconcile`、
   `pcb check`、`pcb layout-lint`、`kicad sch-check` 质量项、`pad-net-diff`、`signoff-continuity`。
4. **离线计算**（快照/JSON）：只在快照晚于最后一次写入时有效；写入后快照即过时。
5. **只有汇总/布尔值的结果**：例如 `sch drc` 在部分 EasyEDA 构建上只返回布尔/汇总——“0 条”不能证明逐条干净。
6. **独立评审意见**：`review-panel` 的 met / not_met / cannot_judge；补充机械检查，不能替代。
7. **图像**：`sch export-image`、`pcb stage-snapshot`、kicad-cli SVG。只用于发现遗漏，永不单独给 `PASS`。
8. **来源资料**：需求、数据手册、样例。定义“应该是什么”，但不证明板子“是什么”（只给 `source-only`）。
9. 记忆、猜测、别的板的数值：不是证据。

冲突规则：

- 两份证据矛盾时该项记 `INCOMPLETE`，两份都写进报告，不取更乐观的一份。典型：`SIM/KICAD MISMATCH`
  （仿真报开路、KiCad DRC 判连通）、`sch drc` 无错但 `sch check` 有发现、`pad-net-diff` 与原理图网表不一致。
- 覆盖范围不同不算矛盾：`pcb drc` 通过不说明 `pcb check` 的 DFM 项，二者各报各的。
- 同一检查在写入前后结果不同，以写入后的新鲜结果为准，旧结果作废。

## 4. pcbpilot 输出 → 结论

| 来源 | 输出 | 结论换算 |
|---|---|---|
| `sch gate` | `pass` / `fail` / `blocked` + advisory | `pass` 且无 advisory → PASS；有 advisory → WARN；`fail` → FAIL；`blocked` → BLOCKED |
| `sch check` | findings；默认退出 0 | 读 `result.findings`：error/fatal → FAIL，其余 → WARN；`--all-pages` 的非活动页数据浅 → 该页 INCOMPLETE |
| `sch bridge-check` | BRIDGE / ORPHAN | BRIDGE（真短路）→ FAIL；只有 ORPHAN → WARN |
| `sch drc` | fatal 计数；可能只有汇总 | fatal > 0 → FAIL；仅告警 → WARN；只有布尔/汇总 → 不单独给 PASS，与 `sch check` 合看 |
| `sch nets` | 网名变体 / 单引脚网 | 变体（非零退出）→ FAIL；单引脚网 → WARN（`--strict` 才阻塞） |
| `sch reconcile` | split / missing / unresolved | 任一 → FAIL；手工组“对不了账” → 该组 INCOMPLETE |
| `pcb drc` | violations；`--timeout` | violations > 0 → FAIL；超时 → BLOCKED（把窗口切到前台**跑一次**，不循环重试） |
| `pcb check` | ERROR / WARN / INFO；默认退出 0 | ERROR → FAIL；WARN → WARN；INFO 单列 |
| `pcb layout-lint` | overlap / off-board / tight / crossings + 分数 | overlap、off-board → FAIL；tight、crossings → WARN；分数不是结论 |
| `pcb gate` / `pcb auto route` | `gates[]`：`{gate, pass, items, waived, info}` | 逐门禁 pass → PASS，否则 FAIL；`waived` 非空 → PASS（waived） |
| `pcb module-check` | `status`: pass / fail / incomplete | 直接对应 |
| `pcb poured-list` | 完整 inventory | 完整读取的 `[]` → known-empty；任一字段缺测 → INCOMPLETE |
| `kicad sch-check` | `ok`、ERC、质量发现 | `ok:false` → FAIL；ERC 只有警告 → WARN |
| `kicad sch-build` / `sch-edit` | `ok:false` + `error.code` | FAIL（未写任何文件）；`SHEET_NOT_LOADED` 是加载问题，修 spec 字符，不绕过 |
| `kicad route` | `summary.json` 的 `gates[]` | 同 `pcb gate`；`summary.simKicadMismatch` 里的网 → INCOMPLETE（门禁本身仍失败）。v0.10.2 起读不了的 kicad-cli DRC 报告记为 “cannot check”，`kicad-drc` 门禁失败而不是当作干净 |
| `kicad fab` | 缺 C 号即非零退出且不写文件 | FAIL；`--no-assembly` 时 BOM/CPL 项为 UNSUPPORTED（未出），不是 PASS |
| `sim post-layout` | `verdict.status`: pass / warn / fail | 直接对应，但 PASS 还要满足第 6.2 节的覆盖条件；`sim-board-mismatch` 按第 6.1 节判 |
| `review-panel` | 门禁 pass/fail；每条需求 met / not_met / cannot_judge | not_met 或 blocking → FAIL；评审者缺失/超时 → FAIL（工具即如此）；**门禁通过时 cannot_judge 的需求仍逐条记 INCOMPLETE** |
| `signoff` | 8 个 `signoff-*` 子门禁 | 逐项对应；任一失败整体非零 |
| `sch save` / `pcb save` / `doc reload` | `saved`、回读 | `saved:false` 或 reload 后读不到 → 涉及持久化的结论全部 INCOMPLETE |
| 没有对应命令的项 | — | `manual`：人工检查给出证据（文件、页码、测量值）才可写 PASS，否则 INCOMPLETE |

## 5. 汇总与措辞

- 汇总取最差：`FAIL` > `BLOCKED` > `INCOMPLETE` > `UNSUPPORTED`（必需项）> `WARN` > `PASS`。整体只有在
  **每个必需项**都是 `PASS`（含 waived）时才能写“通过”，并写明范围（哪些页、哪块板、哪个文件版本）。
- 每条结论配：命令（完整参数）、输入文件及其时间/哈希、验证状态、发现数，FAIL/WARN/INCOMPLETE 附对象 ID 与修法。
- 允许的说法：“`pcb drc` PASS（live-verified，save→reload 后）；`pcb check` WARN（2 条 acute-angle）；
  制造可行性 INCOMPLETE（未出 Gerber）”。禁止的说法：“基本通过”“应该没问题”“检查都过了”（不列范围）。
- 评审流程见 [review-workflow.md](review-workflow.md)，DRC/ERC 分类与修法见 [drc-triage.md](drc-triage.md)，
  投板前逐项清单见 [prefab-checklist.md](prefab-checklist.md)。

## 6. 跨任务共用的判定规则

评审、仿真报告、制造验收、布线签核都按这里判；其他页面只引用本节，不另写一套。

### 6.1 网络身份与对账差异

- 网的身份是**实例限定**的完整名（层次路径 + 网名）：`/A/BIAS` 与 `/B/BIAS` 是两个网，`BIAS` 不能代表其中任何一个。
  不得用去掉 `/Sheet/` 前缀、合并 `unconnected-(…)` 写法、大小写归一等办法把差异“消掉”。
- 只有逐网证明**一一对应**（一边的每个网恰好对应另一边的一个网，且两边的 `REF:PAD` 集合完全相同）时，差异才算命名差异；
  证明过程（映射表）写进报告。证明不了的差异不能当命名问题处理。
- 判定优先级（`sim-board-mismatch`、`pad-net-diff` netMismatch、`signoff-trace` “not on the board” 同样适用）：
  已证明只是工具路径归一造成的差异 → `INCOMPLETE`（写明是工具缺口，不是 PASS）；真实的或未查清的连接差异 → `FAIL`。
- 现状：`kicad netlist` 去掉子页路径而 `kicad snapshot` 保留 KiCad 原名；工具统一网络身份的修复 v0.10.2 起提供。
  在此之前，KiCad 原理图↔板一致性以 `kicad-cli pcb drc --schematic-parity` 输出的 `schematic_parity` 条目为证据。
- `SIM/KICAD MISMATCH`（仿真判开路、KiCad DRC 判连通）是两份证据矛盾，按第 3 节记 `INCOMPLETE`，不属于本条。

### 6.2 仿真覆盖与模型假设

- `sim post-layout` / `signoff-ir` 的 PASS 只对**已建模**的部分成立：每个预期的电源轨、源和负载都在模型里，负载电流有数据手册
  或用户给的证据。用默认每脚 50 mA（`assumed`）的负载、IR 表为空或缺某条轨 → 受影响的轨记 `INCOMPLETE`。
- 板温 PASS 不等于器件热安全。没有逐器件功耗、热阻（θJA/θJB/θJC）与 Tj,max 时，器件热结论记 `INCOMPLETE`；
  Tj 裕量与板温分开报告。
- 假设沿链传递：基于 `assumed` / `approx` 模型的结论，在下游（intent 线宽、`signoff-ir`、设计报告、评审、下单结论）都要
  点名该假设；下游门禁通过不会把假设变成证据。

### 6.3 只读任务的快照

- 评审、仿真、报告这类只读任务不 `sch save` / `pcb save`、不 `doc reload`、不跑 `pcb gate`（写入表见
  [review-workflow.md §1](review-workflow.md#1-开工前后端范围只读边界)）。
- EasyEDA 用当前编辑器状态的只读快照（`pcb dump --include-copper`、`sch connectivity`）。它可能含未保存的修改，也不证明
  已持久化，所以结论最高 `offline-verified`，报告写明“基于未经 save/reload 的编辑器状态”。KiCad 用已保存且已在 KiCad 中
  关闭的文件（复制到工程外再跑）。
- 结论必须基于已保存状态（`live-verified`、持久化、下单前的最终证据）时，停下交回 `pcbpilot` 路由：经用户明确授权后
  save → 有界 reload，再取新快照继续。
