---
name: pcbpilot-sim-report
description: "pcbpilot 仿真与设计文档专项：电源直流仿真（每网电压、每焊盘电流、压降 IR drop、电流能力、线宽/过孔依据）、ngspice 模拟电路仿真与改值计划、布线后真实铜皮压降/过孔电流/温升热图（可选 Elmer 交叉校验）、设计意图 intent.json、signoff-copper/signoff-safety 证据、版本化设计报告与板卡说明书；EasyEDA 与 KiCad 工程都适用，只读不改工程。触发词：仿真、电源仿真、压降、电流能力、温升、热图、ngspice、SPICE、Elmer、设计意图、设计报告、说明书、使用说明。Simulation and design documentation with pcbpilot: DC power simulation (rail voltages, per-pin currents, IR drop, current capacity for trace/via sizing), ngspice analog simulation with value-change plans, post-layout IR drop / via current / thermal maps on the real copper (optional Elmer FEM cross-check), design intent, sign-off copper and safety evidence, versioned design report and board user manual. Use for: simulate the power tree, IR drop, current capacity, temperature rise, ngspice, Elmer, design report, board manual."
license: MIT
metadata:
  author: "zhuangzard (pcbpilot); original easyeda-agent by zhoushoujianwork"
  version: "0.12.0"
  homepage: "https://github.com/zhuangzard/pcbpilot"
---

# pcbpilot-sim-report

回答“这块板电够不够、线够不够粗、会不会烫、模拟电路是否达标”，并把答案写成可追溯的设计报告与板卡说明书。
本 Skill 只编排 `sim` / `intent` / `report` / `signoff` 这些**离线、只读**命令；数据来自 EasyEDA 回读或 KiCad 文件。
命令参数以当前 `pcbpilot <cmd> --help` 为准；各命令的模型与字段契约在 router 的参考里，这里只给选择、顺序和判读。

## 什么时候用 / 什么时候交还 router

- 用：要电源树电压电流、线宽过孔的电流依据、布线后压降与温升、模拟块指标、intent.json、设计报告、说明书、
  `signoff-copper` / `signoff-safety` / `signoff-ir` 的证据。
- 交还 [pcbpilot](../pcbpilot/SKILL.md)：任何工程写入（`sim analog` 改值计划的执行、按 feedback 加宽走线/加过孔、
  补测试点）、连接器或宿主问题、Gerber/BOM 制造输出、只读评审“能不能下单”的总判断（仿真结论只是其中几项证据，
  流程见 [review-workflow.md](../pcbpilot/references/review-workflow.md)）。

## 硬规则

1. **只读**。仿真与报告命令都不写工程；`--apply-plan` 的改值计划和 `post.json` 的 `feedback[]` 是设计修改建议，
   展示给用户、得到明确同意后交还 router 执行，执行后整条链重跑。
2. **同一设计版本**。`sim.json`、`intent.json`、板快照必须来自同一版原理图与板；不同版本拼在一起会把功耗放到
   别的封装下。`sim-board-mismatch` 按 [网络身份规则 §6.1](../pcbpilot/references/evidence-and-verdicts.md#61-网络身份与对账差异) 判（已证明只是工具路径归一 →
   INCOMPLETE，真实或未查清 → FAIL）；修输入，不换旧 fixture、不放宽判据。
3. **只读快照**。不 save、不 reload：EasyEDA 用当前编辑器状态的 `pcb dump --include-copper`，结论最高 `offline-verified`
   并注明未经 save/reload；KiCad 用已保存、已在 KiCad 关闭的 `.kicad_pcb` 做 `kicad snapshot`。必须基于已保存状态时交还
   router 走经授权的 save → reload（[§6.3](../pcbpilot/references/evidence-and-verdicts.md#63-只读任务的快照)）。写入之后旧快照作废。
4. **模型和额定要有出处**。未建模 IC 默认每供电脚 50 mA（`assumed`），这样的轨只能 INCOMPLETE（[覆盖规则 §6.2](../pcbpilot/references/evidence-and-verdicts.md#62-仿真覆盖与模型假设)）；
   补模型写进 `--models` 文件并带 `source`，额定（θJB、Tj,max、Isat…）只从数据手册来，禁止凭印象填（[power-models.json](../pcbpilot/references/power-models.json)）。
5. **安装外部仿真器要用户同意**。`sim tools install` 会下载并运行安装包（Windows 需管理员批准）：先 `--dry-run`
   把计划告诉用户，同意后才 `--yes`。ngspice 必需，Elmer 可选。
6. **报告不手改、不覆盖**。`report design` 每次出新 `vN`；改输入再出下一版，`--force` 只修同版笔误。
   没跑 ElmerSolver 不写“已交叉验证”；图片只是展示证据。

## 先选仿真：问题 → 命令

| 问题 | 命令 | 需要的输入 | 能证明 | 不能证明 |
|---|---|---|---|---|
| 各电源轨电压、每焊盘电流、稳压器工作点、线宽过孔该按多大电流 | `sim power` | 每页 connectivity + values（或 KiCad `kicad netlist`）、输入源/稳压器模型 | 直流工作点与平均化电源树，逐场景 KCL | 纹波（`ripple` 只是闭式估算）、上电/启动时序、浪涌、负载阶跃等瞬态、ESD；未建模器件的真实电流——全部列为未覆盖，见下文 |
| 每网线宽/过孔/间距/阻抗/安规距离的计划 | `intent derive` | 同上 + `--sim sim.json` + 可选 `--spec`（层数、铜厚、温升、标准、声明轨电流） | 有理由的电气计划与 findings | 板上实际是否做到（那是 gate/signoff） |
| 运放/滤波/ADC 前端/基准/复位 RC/晶振负载是否达标 | `sim analog`（`intent derive` 默认顺带跑） | 同上 + ngspice + 可选 `--spec` 目标 | 行为级模型下的指标与容差良率 | 噪声、失真、环路补偿、起振裕量 |
| 布完线后真实铜皮的压降、过孔电流、电流密度、板温 | `sim post-layout` | 新鲜板快照 + 同版 `sim.json` + `intent.json`（铜厚/叠层/温升） | 裸板静止空气下、已建模负载的直流稳态 | 未建模的轨/负载、器件结温（缺功耗/θ/Tj,max 时）、外壳/风扇、交流电流分布、瞬态热 |
| 客户交付的设计说明、给软件/测试的使用说明 | `report design` / `report manual` | 上面全部产物 + DRC/check/重载回读 + notes | 汇总并按证据给结论 | 不新增任何检查；缺证据的章节是 N/A |

**直流仿真不覆盖的项**（报告里逐条列为未覆盖，pcbpilot 没有电源级瞬态仿真 → `UNSUPPORTED`，要证据只能另做）：输出纹波 →
厂商模型的瞬态 SPICE 或样板上示波器实测（满载、短地线探头）；上电/启动与浪涌 → 瞬态仿真或电流探头抓上电波形；负载阶跃 →
瞬态仿真或电子负载阶跃实测；ESD/浪涌抗扰 → 按标准的实验室测试。`sim analog` 的 tran 只覆盖模拟块，不覆盖电源级。

模型细节与边界：[power-sim.md](../pcbpilot/references/power-sim.md)、[analog-sim.md](../pcbpilot/references/analog-sim.md)、
[post-layout-sim.md](../pcbpilot/references/post-layout-sim.md)、[design-intent.md](../pcbpilot/references/design-intent.md)。

## 工作流

0. **工具**：`pcbpilot sim tools check`（`--json` 给脚本）；退出 0 = ngspice 可用。Elmer 缺失不影响退出码，只是
   `--elmer-check` 会 `skipped`（[安装细节](../pcbpilot/references/environment-setup.md#仿真工具ngspice--elmer-fem)）。
1. **取原理图输入**（每页一份，存到工作目录，不放进工程目录）：
   - EasyEDA：`pcbpilot sch connectivity --page <页> > sch-<页>.json`；`pcbpilot sch list --page <页> > list-<页>.json`。
     现场只读也可直接 `pcbpilot --project <工程> sim power --pages P1,P2 …`。
   - KiCad：`pcbpilot kicad netlist --sch <root>.kicad_sch --out conn.json`（器件值在 `device.name`，LCSC 字段进
     `supplierId`；BOM 型号不全时另给 `--values`）。
2. **电源仿真**：`pcbpilot sim power --connectivity sch-P1.json --connectivity sch-P2.json --values list-P1.json
   --values list-P2.json --out sim.json --report sim.md --spice sim.cir --spice-check`。
   - 先看 `sim.md` 的 Warnings/Assumptions：所有轨 0 V 或 “no power model” = 输入源或稳压器没匹配上，结果作废，
     写 `--models project-models.json`（`connector-source` / `ldo` / `buck` / `load`，字段见库文件 `_doc`）后重跑。
   - `spiceCheck.pass` 只证明线性化网表与 ngspice 一致，不证明模型对。
3. **设计意图**：`pcbpilot intent derive --connectivity … --values … --sim sim.json [--spec spec.json] --out intent.json
   --report intent.md --analog-out analog.json`。`--strict` 让 error 级 finding 非零退出；只有板子没有原理图时用
   `--board board.json`（电流是启发值，不能据此给 PASS）。
4. **模拟仿真（有模拟块或用户给目标时）**：`pcbpilot sim analog --connectivity … --values … --sim sim.json
   [--spec analog-spec.json] --out analog.json --report analog.md --plots-dir plots --apply-plan plan.json`；
   想看改值效果用 `--what-if plan.json`（只在内存）。plan 执行走 router + 用户确认（[§5](../pcbpilot/references/analog-sim.md)）。
5. **布线后仿真**：取板 —— EasyEDA `pcbpilot --project <工程> pcb dump --include-copper --out board.json`，KiCad
   `pcbpilot kicad snapshot <board>.kicad_pcb --out board.json`；然后
   `pcbpilot sim post-layout --board board.json --sim sim.json --intent intent.json --out post.json --report post.md
   --svg-dir heatmaps [--elmer-check] [--feedback out/feedback.json]`。负片内层没有铜对象时会按地平面推断并写进
   `assumptions[]`，不对就用 `--plane <层>=<网>` / `=none` 明示。
6. **设计报告**：`pcbpilot report design --out-dir reports/<name> --project-name "<名>" --intent intent.json --sim sim.json
   --analog analog.json --post post.json --board board.json --reload-board board.reload.json --drc drc.json --check check.txt
   --values … --host "<宿主与精确版本>"`。预布局版可直接 `intent derive … --report-dir reports/<name>`。
   各标志缺失时哪章变 N/A 见 [design-report.md](../pcbpilot/references/design-report.md#输入与证据)。
7. **板卡说明书**：临时预览 `pcbpilot report manual --board board.json --intent intent.json --sim sim.json --post post.json
   --out manual.html`；交付用 `--out-dir <dir> --gate`（读 `pcbpilot.project.json` 的 `manual` 块与 notes，门禁见
   [board-manual.md](../pcbpilot/references/board-manual.md)）。
8. **签核证据**：`pcbpilot signoff --board board.json --intent intent.json --sim sim.json --connectivity … --values …
   --report reports/<name>/vN/report.json --manual <html> [--review review.json] --out-dir signoff`（或 `--run-dir`
   指向 `pcb gate` / `kicad route` 的输出目录）。它自己重跑设计后仿真，任一子门禁失败即非零退出。

每一步之后把结论落盘、记录命令与输入哈希；上游输入变了，下游全部重跑（sim → intent → analog → post → report → manual → signoff）。

## 判读与报告

结论词汇与证据强弱只用 [evidence-and-verdicts.md](../pcbpilot/references/evidence-and-verdicts.md)，换算如下：

| 输出 | 换算 |
|---|---|
| `sim power` | 本身无 verdict：warnings 为空且模型无 `assumed` 才能作为后续 PASS 的依据；有 `assumed` 的轨点名器件，下游按 §6.2 处理 |
| `intent derive` / `sim analog` | finding error → FAIL，warn → WARN；`skipped`（ngspice 缺失）的块 → INCOMPLETE |
| `sim post-layout` | `verdict.status` 为上限：PASS 还须逐轨核对覆盖（`post.md` IR 表每条预期轨都在、负载电流有出处），缺的轨 INCOMPLETE；板温 PASS 不代表器件热安全；`sim-board-mismatch` 按 §6.1；Elmer `skipped` 不降级但不得写交叉验证 |
| `report design` | 封面 PASS / PASS with warnings / FAIL → PASS / WARN / FAIL；§7 的 N/A → 该项 INCOMPLETE，不是通过 |
| `report manual --gate` / `signoff` | 逐门禁对应；`signoff-ir` 跟随 post verdict 并继承同样的覆盖缺口，`signoff-copper` 看逐段线宽与过孔组，`signoff-safety` 看隔离与板边 |

报告格式：每条结论写命令（完整参数）、输入文件与 sha256（报告封面已有）、验证状态（离线快照 = `offline-verified`，
经授权 save → reload 后的新鲜回读才算 `live-verified`）、关键数字（最坏压降/预算、最大过孔占用、板最高温与位置）、
**逐器件 Tj 裕量单列**（缺功耗/θ/Tj,max → 热结论 INCOMPLETE）、覆盖表（每条轨的源、负载与电流出处）、假设清单（下游结论
照抄）、未覆盖项。示例：“`sim post-layout` PASS（offline-verified，同版 sim）：+3V3 最坏 2.7 mV / 预算 66 mV，板最高
39 °C；U2 负载为 `assumed` 50 mA → +1V8 INCOMPLETE；U3 无 θJB → 器件热 INCOMPLETE；Elmer 未运行”。

## 常见失败 → 修法

| 现象 | 原因 | 修法 |
|---|---|---|
| 所有轨 0 V、`no power model` 一串 | 输入连接器/稳压器未匹配模型 | 写项目 `--models`（源电压、稳压器 vout/dropout，带 `source` 与 `confidence`），重跑；不靠默认值出结论 |
| `value not parseable — left open` | 没给 `--values`，被动件取不到值 | 补 `sch list` 或 values 文件 |
| `sim-board-mismatch` | sim 与板不是同一版本，或位号/焊盘/网名不一致 | 用同版本 connectivity 重跑 `sim power`；查不清 → FAIL |
| KiCad 层次图子页的网报 mismatch（板上 `/Sheet/NET`，sim 里 `NET`） | `kicad netlist` 去掉了子页路径而 `kicad snapshot` 保留（v0.10.2 起工具统一网络身份） | 逐网证明一一对应后记 INCOMPLETE（工具缺口），否则 FAIL（§6.1）；不改快照 JSON 凑数 |
| 板温几百 °C | 输入版本混用；板外负载没标 `offBoard`；旧 sim.json 无 `thermalBasis` | 依次排除；排除后仍高就是真实散热问题，交还 router 改铜皮/器件，不放宽 105/130 °C 判据 |
| `spiceCheck.skipped` / analog `skipped` | ngspice 不可用 | `sim tools check` → 征得同意后 `sim tools install --yes --only ngspice` |
| Elmer `skipped` | 未装 ElmerSolver | 可选；输入包保留在 `elmer/`，之后 `--elmer-result elmer/probes.dat` 比较 |
| `report design` FAIL：copper-to-edge | 板上铜到板边距离不足（板的问题，不是报告的问题） | 交还 router 按 [pcb-design-rules.md §5.4](../pcbpilot/references/pcb-design-rules.md) 修铺铜内缩 |
| 报告 §7 多项 N/A | 没给 `--drc` / `--check` / `--reload-board` / `--net-diff` | 补证据再出下一版；交付版必须齐 |
| `signoff-review` 缺 review.json | 未跑设计评审 | `review-panel --stage design`（router 的评审流程），不伪造 |
| `signoff-copper` intent-widths 失败 | 走线窄于 intent 线宽或 `widthMil.min` | 交还 router 加宽/并联铜；改完重 dump → post-layout → signoff |
| `signoff-copper` cannot check: the board has no copper | 板子还没布线（0 走线/弧/过孔/已灌铺铜） | 先布线（`kicad route` / `pcb auto route`），再重 dump → post-layout → signoff |
| `board-manual` 门禁失败 | notes 缺接口用途、逐脚说明、LED、电源输入，或 post.json 不是同一板 | 按 board-manual.md 补 `pcbpilot.manual-notes.json`；先跑同板 post-layout |

实测样例与数字见 [references/verified-runs.md](references/verified-runs.md)。
