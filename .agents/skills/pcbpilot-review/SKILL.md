---
name: pcbpilot-review
description: "只读设计评审与“能不能投板”判断：对嘉立创EDA专业版（EasyEDA Pro）或 KiCad 工程按 quick / full / pre-fab 三档跑 ERC/DRC、连通性、原理图↔PCB 对账、DFM、电源与签核检查，按证据强弱给出 PASS/WARN/FAIL/INCOMPLETE 结论和固定格式报告，从不修改设计。触发：评审、检查一下我的设计、审一下原理图/PCB、ERC/DRC 报错是什么意思、哪里有问题、能不能投板/下单、投板前检查。Read-only design review for EasyEDA Pro and KiCad projects: quick / full / pre-fab tiers driving ERC/DRC, connectivity, schematic-vs-PCB parity, DFM, power and sign-off checks, with evidence-ranked verdicts and a fixed report; never edits the design. Use for: review my design, audit the schematic/PCB, check my board, what's wrong with it, explain these ERC/DRC errors, is it ready for fab / ready to order, pre-fab check."
license: MIT
metadata:
  author: "zhuangzard (pcbpilot); original easyeda-agent by zhoushoujianwork"
  version: "0.10.1"
  homepage: "https://github.com/zhuangzard/pcbpilot"
---

# pcbpilot-review：只读评审与投板判断

用 pcbpilot 的只读检查给一份 EasyEDA 或 KiCad 设计出结论。评审回答“它现在是什么样、证据有多强”，不回答“我帮你改好”。
修复建议写成具体命令交给用户；用户确认要改时，交回 [pcbpilot](../pcbpilot/SKILL.md) 路由按定点修改模式执行。

## 什么时候用 / 什么时候交回

- 用：用户要“看看/评审/审一下/查错/解释 ERC 或 DRC/能不能投板”，且没有要求你修改。
- 交回 router：用户要求修复、布线、铺铜、补 LCSC 号、出制造文件之外的写入；或评审中途改为设计任务。
  只有 `kicad fab --out <工程外新目录>` 这类只写输出目录的命令可以在 pre-fab 档里跑（见下）。
- 两个后端都有（既有 `.kicad_*` 又开着 EasyEDA 工程）时先问以哪份为准，不假设一致。

## 硬规则

1. **只读。** 不跑任何会改设计的命令。完整“会写入、评审中不跑”的命令表见
   [review-workflow.md §1](../pcbpilot/references/review-workflow.md#1-开工前后端范围只读边界)；KiCad 每条命令写什么见
   [kicad-file-safety.md §2](../pcbpilot/references/kicad-file-safety.md#2-命令写入表)。最容易误跑的：`pcb gate`（会 save + reload）、
   `kicad sch-check --fix-pwr-flag`、`kicad sch-fit`、`kicad lcsc --set`、`kicad route` / `kicad place`、任何 `sch`/`pcb` 修改子命令。
   EasyEDA 只取当前编辑器状态的只读快照，不 save / reload；它的限制与“需要已保存状态时怎么办”见
   [evidence-and-verdicts.md §6.3](../pcbpilot/references/evidence-and-verdicts.md#63-只读任务的快照)。
2. **KiCad 在副本上评审。** 实测 `kicad-cli` 跑 ERC 时会在工程目录新建 `<工程>.kicad_prl`，被中断的 `kicad-cli` 会留下
   `~<工程>.kicad_pro.lck`。把工程复制到工程外的临时目录再跑；所有输出（JSON、报告）写到另一个评审目录。
3. **结论词汇与证据排序只用** [evidence-and-verdicts.md](../pcbpilot/references/evidence-and-verdicts.md)：缺证据、证据矛盾、
   检查实际没覆盖对象 → `INCOMPLETE`，永远不写“基本通过”。退出码 0 不等于 PASS，读发现列表。
4. **空结果先证明覆盖。** 一个 PASS 只有在检查真的处理了对象时才算：看计数（处理了几个网/焊盘/热源）而不只看 `pass`。
5. 发现问题不顺手修。EasyEDA 现场命令串行；`--page` 会临时切换页面（默认切回），报告里注明。

## 工作流

### 0. 定档与范围

- 档位：用户没说 → quick；说“全面/仔细/审计” → full；说“投板、下单、生产、Gerber、JLC” → pre-fab。每档包含前一档。
- 记录输入：文件路径 + 修改时间或 git 提交；EasyEDA 记 `health` 里的宿主版本、工程名、文档。
- 命令清单与两后端对照以 [review-workflow.md §2](../pcbpilot/references/review-workflow.md#2-三档与命令) 为准；下面只写顺序、
  判断点和实测坑。用到的每条命令先 `pcbpilot <cmd> --help` 核对。

### 1. 预检（结论可能直接是 BLOCKED）

- EasyEDA：`pcbpilot health`；`windows[]` 里没有目标工程/文档 → BLOCKED，等用户打开，不自己启动宿主。
  多板工程先 `pcbpilot board list`、`pcbpilot sch pages` 确定与该 PCB 绑定的页。
- KiCad：查 `~*.lck`。锁文件内容是 `{"hostname","username"}`；确认 KiCad 真开着该工程才判 BLOCKED（请用户关闭），
  否则是中断运行留下的残锁——在**副本**里删掉它即可，不碰用户原目录。
- 直接调用 `kicad-cli`（例如 PCB DRC）时设 `KICAD_CONFIG_HOME=~/.pcbpilot/cache/kicad-config/v1`（任一 pcbpilot KiCad
  原理图命令如 `kicad sch-check` 会建好这份空库表配置）。实测 Windows + KiCad 10 上不设它，`kicad-cli pcb drc --schematic-parity`
  10 分钟无输出；设了 26 秒完成。代价是库类违规必然出现（见第 5 步“环境类”）。

### 2. quick

- KiCad（副本里，`<root>` 为根图）：
  ```bash
  pcbpilot kicad sch-check --sch <root>.kicad_sch > out/sch-check.json      # ERC + 质量检查，非零=有 ERC error 或质量发现
  pcbpilot kicad sch-read  --sch <root>.kicad_sch --out out/spec.json      # 位号/网表概览
  pcbpilot kicad netlist   --sch <root>.kicad_sch --out out/conn.json      # 后续 intent / signoff 的输入
  KICAD_CONFIG_HOME=~/.pcbpilot/cache/kicad-config/v1 \
    kicad-cli pcb drc --format json --severity-all --schematic-parity -o out/drc.json <board>.kicad_pcb
  ```
  `drc.json` 干净不够：带 `"excluded": true` 的条目与 `.kicad_pro` 里设成 `ignore` 的规则按
  [§6.4](../pcbpilot/references/evidence-and-verdicts.md#64-drc-排除项与被关掉的规则) 列出，触及短路/间距/未连接即 FAIL。
- EasyEDA（窗口在前台，串行）：`pcbpilot sch gate --only layout-lint,check,bridge-check --json`、`pcbpilot sch nets`、
  `pcbpilot sch connectivity --all-pages`（存 `conn.json`）、`pcbpilot pcb drc --json`、`pcbpilot pcb layout-lint --json`。
  `sch check --page <名>` 会切页再切回；`sch bridge-check` 没有 `--page`，只对当前页准确——非当前页只能记 `--all-pages`
  的浅数据结果 = 该页 INCOMPLETE（[review-workflow.md §3](../pcbpilot/references/review-workflow.md#3-层次与多页覆盖)）。

### 3. full

```bash
pcbpilot kicad snapshot <board>.kicad_pcb --out out/board.json          # EasyEDA：pcb dump --include-copper --out out/board.json
pcbpilot intent derive --connectivity out/conn.json --out out/intent.json --report out/intent.md --sim-out out/sim.json
pcbpilot pcb check --board out/board.json --intent out/intent.json --json   # EasyEDA 现场去掉 --board 跑全部规则
pcbpilot sim post-layout --board out/board.json --sim out/sim.json --intent out/intent.json --out out/post.json --report out/post.md
python <pcbpilot Skill 根>/scripts/pad-net-diff.py --sch out/conn.json --pcb out/board.json
```

- KiCad 的 `kicad netlist` 已把元件值放进 `device.name`，`intent derive` 不必再给 `--values`；`kicad sch-read` 的输出**不是**
  `--values` 格式（实测报 `cannot unmarshal array`）。
- 人工审计（去耦、电源轨、接口电平、复位/启动脚、连接器脚序）引用 `intent.md` 的 findings 与 `sim.json`；逐条写出处。
- `review-panel --stage design --requirements <需求.md> --evidence …` 只在用户给了需求文档、且同意把设计数据发给外部评审
  CLI（默认 codex、kimi、claude）时跑；否则记 “未运行（无需求文档/未授权）” = INCOMPLETE。

### 4. pre-fab

- 逐条走 [prefab-checklist.md](../pcbpilot/references/prefab-checklist.md)，第 0 节订单契约先于一切。
- `pcbpilot signoff`：有 `kicad route` / `pcb gate` / `pcb auto route` 的 run 目录就 `--run-dir <run>`，并先按
  [§6.5](../pcbpilot/references/evidence-and-verdicts.md#65-运行证据绑定到确切输入) 核对它读的就是当前文件；没有时用显式输入
  `--board out/board.json …`，新鲜度与“缺输入”的写法见 [review-workflow.md pre-fab](../pcbpilot/references/review-workflow.md#pre-fab能否投板)。
- KiCad：`pcbpilot kicad lcsc --pcb <board> --sch <root> --check`；`pcbpilot kicad fab --pcb <board> --sch <root> --out <新目录>`
  （缺 C 号时什么都不写并非零退出）。EasyEDA 没有 pcbpilot 制造输出路径 → Gerber/CPL 记 UNSUPPORTED，只能 `bom export`。

### 5. 分诊、分级、汇总

- 每条违规按 [drc-triage.md](../pcbpilot/references/drc-triage.md) 判归属（封装自身 / 布局 / 布线 / 板级几何 / 原理图同步 / 环境），
  同一根因合并成一个发现。
- 环境类（`footprint_link_issues`、`lib_symbol_issues`、`lib_footprint_issues`）在隔离配置下必然出现：不算设计缺陷，
  但“与库一致”这一项记 INCOMPLETE 并说明原因。
- 严重度 S0–S3 与汇总规则见 [review-workflow.md §4](../pcbpilot/references/review-workflow.md#4-严重度分级) 和
  [evidence-and-verdicts.md §5](../pcbpilot/references/evidence-and-verdicts.md#5-汇总与措辞)。

### 6. 报告

固定用 [review-workflow.md §5](../pcbpilot/references/review-workflow.md#5-报告模板) 的模板，节不省略；每条结论带完整命令、
输出文件、验证状态（KiCad 已保存文件经 kicad-cli 检查 = `live-verified`；对 `board.json` / `conn.json` 的计算 = `offline-verified`）。
一份实测样例（KiCad demo 副本的 quick + full，含真实输出摘要）见 [references/kicad-demo-review.md](references/kicad-demo-review.md)。

## 实测的“假 PASS / 假 FAIL”与处理

| 现象 | 原因 | 怎么记、怎么补 |
|---|---|---|
| `sim post-layout` PASS，但 IR 表为空、热源 0.000 W 或负载是默认 50 mA | 电源来自连接器或器件无功耗模型（`intent.md` 里 `unknown-model`、`floating-domain`） | 受影响的轨 INCOMPLETE（[§6.2](../pcbpilot/references/evidence-and-verdicts.md#62-仿真覆盖与模型假设)）；补电源模型或 `intent derive --spec`（声明 rails）后重跑 |
| `pcb check --board` `passed:true`，`trackCount`/`padCount` 为 0 | 离线只跑 copper-to-edge、isolation、via-current，其余计数未运行 | 只对这三项记 PASS（看 `limitations[]`）；其余 DFM 项 EasyEDA 现场才有，KiCad 记 UNSUPPORTED |
| `pad-net-diff.py` 抛 `TypeError: 'NoneType' object is not iterable` | `kicad netlist` 对无网连接的单脚件（安装孔 CONN_1）写 `"pins": null` | 连通性该项 INCOMPLETE；以 kicad-cli DRC 的 `schematic_parity` / `signoff-continuity` 为证据 |
| `pad-net-diff` 上百条 netMismatch，`signoff-trace` 报 “not on the board” | 层次工程：网表 IR 去掉了 `/Sheet/` 前缀并把 `unconnected-(…)` 记为 NC，板快照保留 KiCad 原名 | 不靠去前缀消差异（`/A/BIAS` ≠ `/B/BIAS`）：按 [§6.1](../pcbpilot/references/evidence-and-verdicts.md#61-网络身份与对账差异) 逐网证明一一对应才记 INCOMPLETE，否则 FAIL；v0.10.2 前以 DRC `schematic_parity` 为证据 |
| `kicad-cli` 长时间无输出 | 全局库表加载慢 | 中断后清副本里的残锁，设 `KICAD_CONFIG_HOME` 重跑一次，不循环重试 |
| `pcb drc` 超时 | EasyEDA 窗口在后台 | BLOCKED；请用户把窗口切到前台后跑一次 |
| `sch drc` 0 条但 `sch check` 有发现 | `sch drc` 可能只返回汇总 | 以 `sch check` 逐条为准，`sch drc` 不单独给 PASS |
| `signoff-parts` 全部缺 LCSC | 设计未填 C 号 | 只有用户要贴片时才是 S1；裸板订单记 UNSUPPORTED 并写明 |
