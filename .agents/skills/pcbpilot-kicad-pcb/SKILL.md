---
name: pcbpilot-kicad-pcb
description: "用 pcbpilot 离线完成 KiCad 板子的板级设置、布局、布线与签核：kicad board 建板框/挖空/层叠/安装孔/禁布区/铺铜/文字（mm），kicad snapshot 读板，kicad place 多种子自动布局（锁定件、共用/占位位号保持不动），kicad route 按设计意图生成网络类、绝缘域与铺铜后用 fastroute/TraceMaker 布线并跑 DRC 等全部门禁，最后 signoff。适用于“画板框/定层叠/加安装孔”“KiCad 布局”“摆件”“KiCad 自动布线”“铺铜/重灌”“网络类/线宽规则”“DRC 报错怎么修”“布完能不能签核”。KiCad board setup, layout and routing with pcbpilot, offline on .kicad_pcb files: board outline/cutouts/stackup/mounting holes/keepouts/zones/text (kicad board), snapshot the board, auto-place footprints (best of several seeds; locked, shared and placeholder designators stay fixed), route with intent-derived netclasses, insulation domains and pours through fastroute or TraceMaker, gate on KiCad DRC and every intent/safety/sim check, then sign off. Use for \"draw the board outline / set a 4-layer stackup\", \"place my KiCad board\", \"autoroute this .kicad_pcb\", \"pour / refill ground\", \"netclasses from intent\", \"fix KiCad DRC errors\", \"is the routed board ready for sign-off\"."
license: MIT
metadata:
  author: "zhuangzard (pcbpilot); original easyeda-agent by zhoushoujianwork"
  version: "0.12.2"
  homepage: "https://github.com/zhuangzard/pcbpilot"
---

# pcbpilot-kicad-pcb

KiCad 板级工作（`.kicad_pcb`）：从已同步的板子到布好、过门禁、可签核的板子。本 Skill 只管**工作顺序和决策**；
每个子命令的参数、门禁表、输出文件与已知坑以 [kicad.md](../pcbpilot/references/kicad.md) 为准，动手前先 `--help` 核对。

## 什么时候用 / 什么时候交回 `pcbpilot`

- 用：给了 `.kicad_pcb`（或 KiCad 工程目录）并要布局、布线、铺铜、网络类、DRC 修复或布线后签核。
- 交回 router：KiCad 原理图绘制/修改（`kicad sch-build` / `sch-edit`）、选件与 C 号、`kicad fab` 制造文件、
  EasyEDA 板子、只读评审/“能不能下单”（评审不跑 `kicad place` / `kicad route`，见
  [review-workflow.md](../pcbpilot/references/review-workflow.md)）、电源/模拟仿真本身。

## 硬规则

1. 不文本编辑 `.kicad_*`，不做 GUI 自动化。读用 `kicad snapshot`，写只经 `kicad board` / `kicad pcb-sync` / `kicad rules` / `kicad place` / `kicad route`
   （默认写到新文件，输入板不动；`kicad board --in-place` 走锁检查 + 哈希比对，pcb-sync `--in-place` 走事务、门禁不过不写）。没有 typed 命令的修改交给用户在 KiCad 里做，记 `UNSUPPORTED`。细则见
   [kicad-file-safety.md](../pcbpilot/references/kicad-file-safety.md)。
2. 写之前请用户在 KiCad 中**关闭**该板；目录里有 `~<文件>.lck` 锁文件 → `BLOCKED`，不自己删锁。用户在 KiCad 里改过 →
   旧快照作废，重新 `kicad snapshot`。
3. 输出放工程目录之外的新目录；把选中的结果复制回工程是用户的决定（复制前同样关闭 KiCad）。
4. 不为过门禁放宽规则、压低 `--edge-mil`、加 KiCad exclusion；waiver 只来自用户签名，Agent 不替用户写。
5. 结论用 [evidence-and-verdicts.md](../pcbpilot/references/evidence-and-verdicts.md) 的词汇：DRC 报告读不出来是
   `INCOMPLETE`（cannot check），永远不是 `PASS`。

## 目前没有自动化、要用户在 KiCad 里做的事

| 步骤 | 现状 | 用户在 KiCad 里做 |
|---|---|---|
| 封装从库刷新 | 无 typed 命令（`UNSUPPORTED`） | Tools → Update Footprints from Library；先确认库表能找到库 |
| 锁定器件 | `kicad place` 尊重 KiCad 的 locked 标记 | 选中器件 → Lock；或在 `mech.json` 的 `fixed[]` 写死坐标 |

## 工作顺序

板框 → 原理图同步（`kicad pcb-sync`，不点 GUI 的 Update PCB）→ 封装刷新 → **设计规则（`kicad rules --apply`）** → 布局 →
**规则复查（`kicad rules --check --stage placed`，绝缘布局代理）** → 布线（`kicad route` 复用并核对这些规则）→ 铺铜 → DRC/门禁 →
post-layout 仿真 → 签核 → 保存（复制回工程）。板框、层叠、安装孔、禁布区、
铺铜、板上文字用 `kicad board`（[board-setup.md](references/board-setup.md)，单位 mm）；封装刷新由用户在 KiCad 完成；
任何一步回退（例如布局时发现缺件），从那一步重新往后走，旧的布线结果作废。

1. **同步、读板、定起点**：先 `pcbpilot kicad pcb-sync --sch board.kicad_sch --pcb board.kicad_pcb --out work/sync/board.kicad_pcb`（`--check` 只查，退出 3 = 不同步；拒绝/退出码/门禁见 [kicad.md](../pcbpilot/references/kicad.md#原理图--pcb-同步kicad-pcb-sync)），pad-net / schematic-parity / idempotent 全 pass 才算同步，新件暂存在板框右侧交给第 3 步；之后的步骤都用同步后的板：
   ```bash
   pcbpilot kicad snapshot board.kicad_pcb --out work/snap.json
   ```
   核对：`copperLayers`、`outline` 存在且是预期尺寸、器件数与原理图一致、`rules`（来自 `.kicad_pro`）、`netclasses` /
   `netClassOf`（现有网络类）、`duplicateDesignators` / `placeholderDesignators`（共用位号如两个 `H1`、占位位号如
   空、`REF**`、`G***`、`R?`）、每个器件的 `locked`。有原理图时同时出网表：
   `pcbpilot kicad netlist --sch board.kicad_sch --out work/conn.json`。
2. **设计意图与仿真**（布线必需）：`pcbpilot intent derive --connectivity work/conn.json --out work/intent.json
   --sim-out work/sim.json --report work/intent.md`。先看 `intent.md` 的 findings，电源轨电流/电压不对就用 `--spec`
   声明，不要带着错的意图去布线（[design-intent.md](../pcbpilot/references/design-intent.md)）。
2a. **设计规则**（布局前写入，GUI 里摆件/手动走线的人也看到正确线宽与绝缘规则）：
   ```bash
   pcbpilot kicad rules --pcb work/sync/board.kicad_pcb --intent work/intent.json --sim work/sim.json --plan --plan-out work/rules.plan.json
   pcbpilot kicad rules --pcb work/sync/board.kicad_pcb --intent work/intent.json --sim work/sim.json --apply-plan work/rules.plan.json --revision <rev>
   ```
   先看计划：每网 `basis`（电流 × IPC 还是意图下限）、`floors`（被板最小值/原有类抬高）、`conflicts`（用户类/规则冲突，
   只按名 `--replace <名>` 放行，Agent 不替用户决定）。`plan-coverage` 为 cannot-check（缺 sim/声明电流）时 `--apply` 拒绝，
   补证据再来；`isolation-coverage` 为 cannot-check（某绝缘域在板上一个网都没有）同样拒绝——修意图的网名，不要绕过；
   `footprint-pad-clearance` warn = 某封装焊盘间隙小于类间距（DRC 必报、布局改不掉），把列出的封装/类告诉用户，按用户决定
   设封装局部间距或降类间距，Agent 不替用户降；要下单时加 `--contract fab/order-contract.json`（与 `kicad fab --preflight`
   同一契约）把板最小值（含最小环宽）抬到契约，`fab-contract` 列出仍低于契约的用户规则。
   `staged-readback` / `readback` 是 KiCad 自己回读 + DRC 探针，失败即回滚。细则见
   [kicad.md 设计规则一节](../pcbpilot/references/kicad.md#设计规则kicad-rules意图--仿真--kicad-规则可验证)。
3. **布局**（可选；已有满意布局就跳过）：
   ```bash
   pcbpilot kicad place --pcb board.kicad_pcb --out work/place/board.kicad_pcb \
       --intent work/intent.json [--mech mech.json] [--groups groups.json] [--place-seeds 6] [--candidates 3] [--time-budget 8m]
   ```
   输出最佳种子 + `board.seed-N.kicad_pcb` 备选 + `board.report.json`；**输出板的走线/过孔被清除、铺铜未灌**，后面必须
   重新布线。`--time-budget` 到点交出当前最优并在报告标 `budgetHit`（进度在 stderr）。最优种子有非法摆放则退出码 6、`verdict: fail`：看报告 `illegal` 与 `zoneFill`（域装不下就扩域/移件），不要直接布线。`mech.json` 的写法见 [mech-spec.md](../pcbpilot/references/recipes/mech-spec.md)。
3a. **规则复查**：`pcbpilot kicad rules … --check --stage placed`：`effective-rules` 必须 pass；`isolation-placement` 是
   advisory 的布局代理（courtyard 边到边 < 绝缘间隙、未声明的跨域器件），不是铜间距，按提示回到布局。
4. **布线 + 门禁**（一条命令）：
   ```bash
   pcbpilot kicad route --pcb work/place/board.kicad_pcb --intent work/intent.json --sim work/sim.json \
       --sch board.kicad_sch --requirements REQUIREMENTS.md --out-dir work/route
   ```
   它做设计评审 → 板级规则 → 意图网络类/绝缘域 → 布线 → 过孔阵列/加宽/铺铜/丝印 → 全部门禁 → IR 收敛 → 报告 → 发布评审，
   顺序和每个门禁的判据见 [kicad.md 布线一节](../pcbpilot/references/kicad.md#布线kicad-route与-pcb-auto-route--pcb-gate-同序同算)。
5. **签核**：`pcbpilot signoff --run-dir work/route --intent work/intent.json --sim work/sim.json --connectivity work/conn.json`
   （`kicad route` 最后也会自动跑一次）。
6. **交付与目标工程签核**：签核读的是 `routed.kicad_pcb` 和同目录的 `routed.kicad_pro` / `routed.kicad_dru`（意图网络类、
   绝缘域规则在里面），三件连同依赖库清单（`${KIPRJMOD}` 下的库、3D 路径）一起交付。合并由用户做，先备份或 git commit：
   `.kicad_pro` 的 `board.design_settings` 与 `net_settings` 两节、整份 `.kicad_dru` 都要并入，板子改名为工程的板文件名。
   合并后关闭 KiCad，核对目标的有效规则与签核运行一致（不一致逐项给用户确认，否则停）：
   ```bash
   python -c "import json,sys;L=lambda p:(lambda d:(d['board']['design_settings'],d['net_settings']))(json.load(open(p,encoding='utf-8')));print('pro same:',L(sys.argv[1])==L(sys.argv[2]),'| dru same:',open(sys.argv[3],'rb').read()==open(sys.argv[4],'rb').read())" <目标>.kicad_pro work/route/routed.kicad_pro <目标>.kicad_dru work/route/routed.kicad_dru
   ```
   然后在**目标工程**重跑完整签核，不只 DRC：`kicad-cli pcb drc --format json --severity-all --schematic-parity`（含排除项与
   `ignore` 规则检查，[§6.4](../pcbpilot/references/evidence-and-verdicts.md#64-drc-排除项与被关掉的规则)）→ `kicad snapshot <目标>.kicad_pcb` → `signoff --board … --intent …
   --sim … --connectivity …`，按 [§6.5](../pcbpilot/references/evidence-and-verdicts.md#65-运行证据绑定到确切输入) 记哈希绑定。任一步没跑或不一致 → `INCOMPLETE`。

## 布局策略与有界布局循环

`kicad place` 是模拟退火布局器，只懂它拿到的约束；Agent 的工作是把设计判断变成约束，而不是一遍遍重跑碰运气。

- **先定死的**：板边连接器、开关、指示灯、安装孔、按外壳定位的器件 → KiCad 里 Lock，或写进 `mech.json` 的
  `fixed[]` / `edge[]`（连接器贴边、开口朝外）。天线净空、散热器下方 → `keepouts[]`。
- **功能分组**：给 `--groups`（原理图模块归属，`{"groups":[{id,core,members}]}`），外围跟着自己的核心走，端口保护件
  留在连接器旁。
- **去耦靠脚**：去耦电容必须在其 IC 电源脚旁；看 `report.json` 每个种子的 `decapToPinMeanMil`，偏大说明分组或意图缺失。
- **热与噪声隔离**：功率件、电感、开关节点与模拟/时钟/RF 分开；高压域由意图的 `domains` / `pairs` 自动分区。
- **共用 / 占位位号**（v0.10.2 起；更早版本直接报 duplicate designator 失败）：两个 `H1`、logo 的 `REF**` 等不会被移动，作为固定障碍列在 `report.json` 的 `fixed` 与 `notes`；
  要让它们参与布局，请用户在原理图里重新标注成唯一位号再同步。

有界循环（最多 3 轮，每轮写明理由）：

1. 跑 `kicad place`，读 `report.json`：每个种子 `illegal`（必须 0）、`labelBlocked`、`metrics.overlaps` / `outOfBoard` /
   `keepoutHits`、`weightedWirelengthIn` 对比 `startWirelengthIn`（原始布局的线长）。
2. 最佳种子比原布局线长更长、或关键关系（去耦、晶振、接口保护）变差 → 保留原布局，不为“自动”而换。
3. 请用户在 KiCad 里看最佳板和备选板，确认可接受的部分；把确认的器件 Lock（或加进 `fixed[]`），这就是**锁定集**，
   只增不减。
4. 只对剩余器件再跑一轮。3 轮后仍不满意 → 停下，列出冲突的约束交给用户决定，不继续扩大种子数硬试。
   `notes` 里出现 “annealing ran past 80 % of its time budget” 表示结果依赖机器负载、不可逐步复现，报告里注明。

## 布线前要确认的决策

- **后端** `--router fastroute|tracemaker|both`：fastroute（默认，外部进程，需自行安装到 `~/.pcbpilot/fastroute/current`
  或 `$FASTROUTE_BIN`）；TraceMaker 需 `--tracemaker-bin` / `$TRACEMAKER_BIN`；`both` 并行取先过全部门禁者。两个都没有 →
  布线步骤 `BLOCKED`，只交付布局。`current` 若链接到解压目录（Windows 包里是 `fastroute-<版本>-windows-x64/fastroute.exe`），
  显式 `--fastroute-bin <exe>`，否则要等输入板 DRC 跑完（可达数分钟）才报 “executable file not found”。
- **重布还是续布**：已有部分走线且要保留 → 不加 `--rip-up`；推倒重来 → `--rip-up`（锁定的走线保留）。
- **网络类来源**（报告里要写清每个类从哪来）：用户在 `.kicad_pro` 定义的类（比意图更宽时保留，记在
  `netclasses.kept`）、意图生成的 `PP<n>_W<线宽>`、绝缘域 `PPD_<域>`（`.kicad_dru` 写 clearance + creepage）。快照的
  `netClassOf` 是布线后每个网的有效类，`intent-rules` 门禁核对它。
- **铺铜** `--pours auto|on|off`：`auto` 只给**没有任何铺铜区**的板加 GND/电源铺铜；已有铺铜区的板只重灌、不新建。
  地/电源层用 `--gnd-net`、`--gnd-layers`、`--power-net`、`--power-layer` 改；层叠用 `kicad board stackup` 先定好。
- **设计评审**：`--requirements` 必需；评审不通过就停止，不布线。`--no-review` / `--no-manual` 需要用户签名的 waiver。

## 读结果与报告

- 每个门禁逐条报：`gate`、PASS/FAIL、`detail`、`info`；退出码非零 = 至少一个门禁失败。`kicad-drc` 只计 error 级，
  警告在 `summary.drc.warnings`，评审仍要列出；`info` 标出**输入板原本就有**的违规（布局/封装问题，不是布线造成的）。
  它看不到 KiCad 的排除项和设成 `ignore` 的规则：按 [§6.4](../pcbpilot/references/evidence-and-verdicts.md#64-drc-排除项与被关掉的规则) 另查，触及短路/间距/未连接即 FAIL。
- `kicad-drc` 的 detail 以 `cannot check:` 开头（kicad-cli 没写报告、报告截断或格式不对），或 `summary.inputDrc.error`
  非空 → `INCOMPLETE`，waiver 也不能放行；修好环境（KiCad 安装、库表、文件权限）后重跑。
- 违规归属与修法按 [drc-triage.md](../pcbpilot/references/drc-triage.md) 分组：先上游（原理图/封装/布局）后下游；
  `courtyards_overlap` 回到布局步骤，封装自身的冲突回到库，不靠挪件消掉。
- 验证状态：在已保存、已在 KiCad 关闭的文件上由 kicad-cli/pcbnew 跑出的结果算 `live-verified`；只对快照 JSON 离线算的
  只到 `offline-verified`。报告写明实际跑了哪些步骤、哪些是用户在 KiCad 里做的、哪些没跑（及原因）。

## 常见失败 → 处理

| 现象 | 原因 | 处理 |
|---|---|---|
| `kicad place` 报某位号 “is on N footprints: kept in place” | 共用位号 | 预期行为；要布局就请用户重新标注后同步 |
| 布局后 DRC 全是 `unconnected_items` | `kicad place` 清掉了铜 | 正常，接着 `kicad route` |
| 布局比原来更差 | 约束不足（缺 `--groups` / `--intent` / `mech.json`） | 补约束再跑，或保留原布局 |
| `route-complete` 失败，`summary.blockedConnections` 非空 | 器件挡道，重布无用 | 回到布局：按提示挪件或锁定后重跑 `kicad place` |
| `intent-widths` 失败 | 网太细或颈缩 | 检查意图线宽；`--pad-neck-floor-mil` 只允许在焊盘处颈缩 |
| `post-layout-sim` 失败，标 `SIM/KICAD MISMATCH` | 仿真栅格与 KiCad 连通判断不一致 | 记 `INCOMPLETE`，逐网核对，不当通过；网名/层次路径对不上按 [§6.1](../pcbpilot/references/evidence-and-verdicts.md#61-网络身份与对账差异) |
| `pad-net-diff` / `signoff-continuity` 失败 | 板与原理图没同步 | `kicad pcb-sync --check` 确认，`kicad pcb-sync` 同步后从第 1 步重走 |
| `lib_footprint_issues` | 当前配置找不到封装库（环境类） | 修库表后重跑；不算设计缺陷，也不算 PASS |
| 写入失败或结果被覆盖 | 文件仍在 KiCad 中打开 | 请用户关闭后重跑；记 `BLOCKED` |
| 布线后 `drill_out_of_range` / `via_diameter`（过孔小于 Board Setup 的最小孔/最小过孔） | 已有 `.kicad_pro` 时过孔尺寸没跟板约束对齐（KiCad 演示板实测） | 对照板约束与快照 `rules.viaDrillMil` / `viaDiameterMil`，显式给 `--via-dia-mil` / `--via-drill-mil` 重跑；不降低板约束 |
| `netclasses.kept` 里很多 “width kept at …” | 用户类（含 Default）比意图更宽，保留用户值 | 预期；报告写明这些网的线宽来自用户类而非意图 |
| `kicad route` 报 `RULES_STALE` | 板上的 `kicad rules` 规则与 route 的意图/sim/requirements 推导不一致 | 用同样的输入重跑 `kicad rules --apply`，再布线；不要删 `.kicad_dru` 块绕过 |
| `kicad rules --apply` 退出 6、`E_USER_RULE_CONFLICT` | 用户类/规则会被覆盖或削弱 | 把 `conflicts[]` 原样给用户；用户同意后按名 `--replace <名>`，或改意图 |
| `kicad rules` 退出 5、`plan-coverage` cannot-check | 电源网缺电流证据 | 给 `--sim`（或在意图 / requirements JSON 声明 `peakA` / `rmsA`）后重跑；不猜 |
