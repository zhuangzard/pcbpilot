# 只读设计评审：quick / full / pre-fab 三档

> 用途：用户说“检查一下”“帮我 review”“能不能投板”时，按本页选档、跑命令、分级、出固定格式报告。
> 评审**只读**：不移动器件、不布线、不铺铜、不改规则、不写 LCSC 号；结论词汇见
> [evidence-and-verdicts.md](evidence-and-verdicts.md)。

## 目录

1. [开工前：后端、范围、只读边界](#1-开工前后端范围只读边界)
2. [三档与命令](#2-三档与命令)
3. [层次与多页覆盖](#3-层次与多页覆盖)
4. [严重度分级](#4-严重度分级)
5. [报告模板](#5-报告模板)

## 1. 开工前：后端、范围、只读边界

1. **识别后端**：EasyEDA（`pcbpilot health` 的 `windows[]` 里有目标工程/文档）还是 KiCad（目录里有
   `.kicad_pro` / `.kicad_sch` / `.kicad_pcb`）。两者都有时问用户以哪份为准，不自行假设一致。
2. **定范围**：哪些原理图页 / 哪块板 / 哪个文件版本（记录文件修改时间或 git 提交）。用户没指定档位时默认 quick；
   提到“投板、下单、生产、Gerber”用 pre-fab。
3. **只读边界**——以下命令会写入，评审中**不跑**，除非用户明确同意：

| 命令 | 写了什么 |
|---|---|
| `pcb gate` | 重灌铺铜 → `pcb save` → `doc reload`（改动并保存板子） |
| `pcb auto route`、`kicad route`、`kicad place` | 布局/布线（KiCad 写到 `--out`/`--out-dir`，但这是设计运行，不是评审） |
| `kicad sch-check --fix-pwr-flag`、`kicad sch-fit`（无 `--dry-run`） | 原地改 `.kicad_sch` |
| `kicad lcsc --set` | 原地改 `.kicad_pcb` 和 `.kicad_sch` |
| `sch no-connect`、`sch apply`、任何 `pcb`/`sch` 修改子命令 | 改 EasyEDA 文档 |

   只写输出文件的命令（`--out` JSON、`kicad fab --out <新目录>`、`review-panel --out-dir`、`signoff --out-dir`）可以跑，
   输出放在工程目录**之外**的评审目录。文件安全见 [kicad-file-safety.md](kicad-file-safety.md)。
4. 用户在评审中途改了设计：丢弃旧结果，重新取快照再跑（[collaboration-workflow.md](collaboration-workflow.md) 的按轮次规则）。

## 2. 三档与命令

每档包含前一档全部内容。命令后面的参数是最少必需的；页面、工程名用 `--project` / `--page` 固定。

### quick（几分钟：结构性错误）

查孤立/悬空、短路、单引脚网、明显 ERC/DRC 错误。

| | EasyEDA | KiCad |
|---|---|---|
| 环境 | `pcbpilot health`（版本门禁、窗口） | 文件在 KiCad 中已关闭（无 `~*.lck`） |
| 原理图 | `pcbpilot sch gate --only layout-lint,check,bridge-check --json`；`pcbpilot sch nets` | `pcbpilot kicad sch-check --sch <root>.kicad_sch` |
| 原理图读数 | `pcbpilot sch connectivity --all-pages`（存 JSON 供后续） | `pcbpilot kicad sch-read --sch <root>.kicad_sch --out spec.json`；`pcbpilot kicad netlist --sch <root>.kicad_sch --out conn.json` |
| PCB | `pcbpilot pcb drc --json`（窗口在前台）；`pcbpilot pcb layout-lint` | `kicad-cli pcb drc --format json --severity-all --schematic-parity -o drc.json <board>.kicad_pcb` |

### full（设计是否正确：审计）

| | EasyEDA | KiCad |
|---|---|---|
| 原理图门禁 | `pcbpilot sch gate --json`（含官方 DRC，四关固定顺序） | quick 已含 ERC + 质量检查 |
| 模块对账 | `pcbpilot sch reconcile`（block-apply 落地的模块） | 由 sch-build 的“KiCad 网表 == spec”门禁保证；非 sch-build 工程记 `manual` |
| 意图 / 电源 | `pcbpilot intent derive --connectivity conn.json --values values.json --out intent.json --report intent.md --sim-out sim.json` | 同左（`conn.json` 来自 `kicad netlist`） |
| 原理图↔PCB | `pcbpilot pcb dump --include-copper --out board.json` 后 `python scripts/pad-net-diff.py --sch conn.json --pcb board.json` | `pcbpilot kicad snapshot <board>.kicad_pcb --out board.json` 后同一脚本；kicad-cli DRC 的 `schematic_parity` |
| DFM / 安全 | `pcbpilot pcb check --intent intent.json --json` | `pcbpilot pcb check --board board.json --intent intent.json --json`（离线：板边、绝缘、过孔电流） |
| 铜上仿真（已布线） | `pcbpilot sim post-layout --board board.json --sim sim.json --intent intent.json --out post.json --report post.md` | 同左 |
| 独立评审 | `pcbpilot review-panel --stage design --requirements <需求.md> --evidence intent.json --evidence conn.json --evidence sim.json --out-dir review/design` | 同左 |

full 档另做的审计（人工，引用上面命令的输出）：去耦（每个电源脚的电容、距离）、电源轨（电压/电流/压降与 `sim.json` 对照）、
接口电平与上拉、复位/启动脚状态、连接器脚序 vs 需求文档。

### pre-fab（能否投板）

full 档全部 + 下列项，并逐条走 [prefab-checklist.md](prefab-checklist.md)：

| | EasyEDA | KiCad |
|---|---|---|
| 统一签核 | `pcbpilot signoff --run-dir <run> --intent intent.json --sim sim.json --connectivity conn.json`（`<run>` 是已有的 `pcb gate` / `pcb auto route` 输出目录） | 同左（`<run>` 是 `kicad route --out-dir`） |
| 发布评审 | `pcbpilot review-panel --stage release …`（证据加上 `signoff.json`、设计报告） | 同左 |
| 制造文件 | EasyEDA 没有 pcbpilot 制造输出路径：只有 `pcbpilot bom export --type csv`；Gerber/坐标由用户在 EasyEDA 里导出 → 记 `UNSUPPORTED`，人工核对 | `pcbpilot kicad fab --pcb <board>.kicad_pcb --sch <root>.kicad_sch --out <新目录>`；查看 `fab-report.json`，Gerber 查看器人工核对 |
| LCSC | `signoff-parts` | `pcbpilot kicad lcsc --pcb … --sch … --check` |

没有 `signoff` 所需的 run 目录（例如手工布线、从未跑过 `pcb gate`）时，`signoff` 记 `INCOMPLETE`，
在报告里写“需要用户同意运行 `pcb gate`（会保存并重载板子）”。

## 3. 层次与多页覆盖

- **EasyEDA**：`sch check` / `sch bridge-check` 的 `--all-pages` 对非活动页只拿到浅数据（引脚可能为空）。
  逐页 `--page <名>` 跑才算该页 `PASS`；只跑了 `--all-pages` 的页记 `INCOMPLETE`。`sch nets` 读全工程网表，
  是唯一能发现跨页网名变体（`+3V3` vs `3V3`）的检查。一个工程有多块板时只读与该 PCB 绑定的原理图页
  （`pcbpilot board list`、`pcbpilot sch pages`）。
- **KiCad**：`kicad sch-check`、`kicad netlist`、`kicad sch-read` 都从根图走完整层次。`kicad netlist` / `kicad sch-read`
  的 `--sch` 必须给根图；`kicad sch-check --sch` 给任一图纸也会找到同目录的根图。
  报告列出每张图纸文件和它的检查结果；重复使用的子图（同一文件多个实例）按实例核对位号。
- 报告的“覆盖”一节必须列出：检查了哪些页/图纸、哪些没检查及原因。没列出的页不能被总结为通过。

## 4. 严重度分级

| 级别 | 定义 | 例子 | 对结论 |
|---|---|---|---|
| **S1 阻断** | 板子做出来功能错误、损坏或不安全 | 短路、开路、脚序错、电源接反、绝缘距离不足、缺 C 号的贴装件 | FAIL；必须修 |
| **S2 重要** | 可能导致功能/良率/可靠性问题，需要设计者判断 | 去耦缺失或太远、线宽低于意图、过孔载流余量不足、单引脚网、ERC 警告未解释 | WARN 或 FAIL（按门禁）；修或写明接受理由 |
| **S3 改进** | 不影响功能 | 丝印位置、美观、45° 折线、命名风格 | 单列，不影响结论 |
| **S0 未知** | 证据不足以判断 | 无法读取的页、`cannot_judge`、缺仿真 | INCOMPLETE；写补证据的方法 |

已知误报也按原级别列出并附证据，不删除（[collaboration-workflow.md](collaboration-workflow.md) 的 WARN 规则）。

## 5. 报告模板

固定按下列顺序，空节写“无”，不省略节：

```markdown
# 设计评审：<工程> / <板>（<quick|full|pre-fab>）

- 后端与版本：EasyEDA <宿主版本> / KiCad <版本>；pcbpilot <版本>
- 输入：<文件或文档 + 修改时间/提交>；评审时间 <日期>
- 总结论：<PASS | WARN | FAIL | INCOMPLETE>（只有每个必需项都 PASS 才写 PASS）

## 覆盖
| 页/图纸/板 | 检查 | 结论 | 验证状态 |

## 检查结果
| 检查（完整命令） | 结论 | 发现数 | 输出文件 |

## 发现
| # | 级别 | 对象（位号:脚 / 网 / 坐标 mil） | 问题 | 证据（命令输出行、页码） | 建议修法（命令） |

## 未检查 / 不支持
| 项 | 原因（INCOMPLETE / UNSUPPORTED / BLOCKED） | 怎么补 |

## 需要用户决定
- <要写入的修复、需要同意的 pcb gate、waiver 等>
```

修复建议写成具体命令（例如 `pcbpilot sch no-connect …`、`pcbpilot kicad sch-edit --project … --spec delta.json`），
但评审本身不执行；用户确认后转入定点修改模式。
