# 实测样例：KiCad demo 副本的 quick + full 评审

> 用途：第一次在 KiCad 工程上做评审时对照本页，确认命令顺序、输出长什么样、哪些结果要降级。
> 验证状态：`offline-verified` + kicad-cli 对已保存副本的检查；2026-10-26，Windows 11，KiCad 10.0.6，pcbpilot dev 构建。
> 输入：KiCad 自带 demo `pic_programmer`（两张图纸：根图 + `pic_sockets`；63 个元件，34 个网，双层已布线），复制到临时目录。
> 未覆盖：EasyEDA 现场路径、`review-panel`（无需求文档）、`kicad fab`（demo 无 LCSC 号）。

## 目录

1. [准备](#1-准备)
2. [quick 输出摘要](#2-quick-输出摘要)
3. [full 输出摘要](#3-full-输出摘要)
4. [报告摘录](#4-报告摘录)

## 1. 准备

```bash
W=$(mktemp -d); mkdir -p "$W/proj" "$W/out"
cp -r "<KiCad>/share/kicad/demos/pic_programmer/." "$W/proj/"
(cd "$W/proj" && sha256sum *.kicad_* > ../out/before.sha)   # 评审结束后 sha256sum -c，证明设计文件未变
ls "$W/proj"/~*.lck 2>/dev/null                              # 无输出 = 无锁
```

结束时 `sha256sum -c` 全部 OK；但工程目录多出 `pic_programmer.kicad_prl`（`kicad sch-check` 内部的 ERC 生成）和一个被中断的
`kicad-cli` 留下的 `~pic_programmer.kicad_pro.lck`——这正是要在副本上评审的原因。

## 2. quick 输出摘要

| 命令 | 退出码 | 输出（摘要） | 结论 |
|---|---|---|---|
| `kicad sch-check --sch proj/pic_programmer.kicad_sch` | 1 | stderr `0 ERC error(s), 44 warning(s), 7 quality finding(s)`；ERC 44 条全是 `footprint_link_issues`；质量发现 6 条 `text-over-symbol`（C2/C3/L1/P1/R15/U3 的 Footprint 字段压在电源符号上）+ 1 条 `text-overlap`（R11 Reference × Footprint） | FAIL（质量门禁）；按严重度只是 S3 可读性问题，ERC 环境类 → 库一致性 INCOMPLETE |
| `kicad sch-read --sch … --out out/spec.json` | 0 | `{"nets":34,"parts":63,"ok":true,"path":"out/spec.json"}` | 概览，无结论 |
| `kicad netlist --sch … --out out/conn.json` | 0 | `63 components, 34 nets, 159 connections` | 后续输入 |
| `kicad-cli pcb drc --format json --severity-all --schematic-parity …`（设隔离 `KICAD_CONFIG_HOME`） | 0 | `Found 41 violations / 0 unconnected items / 0 schematic parity issues`；41 条全是 `lib_footprint_issues`（warning） | 铜几何与连通 PASS（live-verified）；原理图↔PCB parity PASS；库一致性 INCOMPLETE |

同一条 DRC 不设 `KICAD_CONFIG_HOME` 时 10 分钟无输出，被中断。

## 3. full 输出摘要

| 命令 | 输出（摘要） | 结论 |
|---|---|---|
| `kicad snapshot proj/pic_programmer.kicad_pcb --out out/board.json` | 63 个元件、轮廓、铜皮，约 1 s | 输入 |
| `intent derive --connectivity out/conn.json --out out/intent.json --report out/intent.md --sim-out out/sim.json` | `20 blocks, 34 nets, 2 domains, 0 pairs; classes GND(1) POWER(2) SIGNAL(31); findings 0 error / 9 warn`；模拟子步骤 `analog-lc-peaking`（L1 谐振峰 33.4 dB）；`floating-domain`（P101–P106 无地参考，实为安装孔）；7 条 `unknown-model`（U1–U6、JP1） | WARN；`unknown-model` 意味着后面的电流是假设值 |
| `pcb check --board out/board.json --intent out/intent.json --json` | `passed:true`，`errors 0`；`limitations[]` 写明 copper-to-edge 外层最小 151.18 mil（要求 20 mil）；`trackCount 0`、`padCount 0` | 仅 copper-to-edge / isolation / via-current 三项 PASS；其余 DFM 项 UNSUPPORTED（离线） |
| `sim post-layout --board … --sim … --intent … --out out/post.json --report out/post.md` | `post-layout PASS — board max 25.0 °C`；IR 表为空，热源 0.000 W | **INCOMPLETE**（空跑：没有任何电源网带电流） |
| `pad-net-diff.py --sch out/conn.json --pcb out/board.json` | `TypeError: 'NoneType' object is not iterable`（P101–P106 的 `"pins": null`） | INCOMPLETE（工具故障）；把 null 换成 `[]` 的临时副本跑出 117 条 netMismatch，全部是 `/pic_sockets/VCC_PIC` vs `VCC_PIC`、`unconnected-(…)` vs NC 这类写法差异 |
| `signoff --board … --intent … --sim … --connectivity out/conn.json --out-dir out/signoff` | `signoff failed: signoff-review, signoff-parts, signoff-trace, signoff-artifacts`；safety / copper / ir / continuity PASS（`34 net(s) identical`） | review、artifacts = 缺输入；parts = 63 个元件无 LCSC 号；trace = 层次网名误报；ir 的 PASS 继承上面的空跑 → INCOMPLETE |

## 4. 报告摘录

```markdown
# 设计评审：pic_programmer / pic_programmer.kicad_pcb（full）
- 后端与版本：KiCad 10.0.6；pcbpilot dev
- 总结论：INCOMPLETE（无 S1/S2 设计缺陷；电源/热仿真空跑，连通性逐焊盘对账工具故障）

## 发现
| # | 级别 | 对象 | 问题 | 证据 | 建议修法 |
| 1 | S2 | L1 / VCC 滤波 | LC 谐振峰 33.4 dB | intent.md findings `analog-lc-peaking` | 输出电容串 0.5–2 Ω 或换带 ESR 的电解（用户确认后 kicad sch-edit） |
| 2 | S3 | C2 C3 L1 P1 R15 U3 R11 | Footprint 字段文字压在电源符号/位号上 | sch-check quality `text-over-symbol` ×6、`text-overlap` ×1 | 在 KiCad 里隐藏或挪开 Footprint 字段 |

## 未检查 / 不支持
| 电源压降与温升 | INCOMPLETE | U1–U6、JP1 无功耗模型，电源来自连接器 → 补 power-models 或 intent --spec rails |
| 逐焊盘对账 | INCOMPLETE | pad-net-diff 读 `pins:null` 崩溃；kicad-cli schematic_parity 0 条作为替代证据 |
| 库一致性 | INCOMPLETE | 隔离配置下 85 条 lib/footprint_link 环境类告警 |
| 离线 DFM 其余项 | UNSUPPORTED | 只有 EasyEDA 现场 `pcb check` 覆盖 |
```
