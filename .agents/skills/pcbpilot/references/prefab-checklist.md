# 投板前检查清单（pre-fab）

> 用途：review-workflow 的 pre-fab 档逐条走本清单。每项写明哪个 pcbpilot 检查或 `signoff` 子门禁能证明它；
> 没有命令能证明的标 **manual**——需要给出人工证据（文件、页码、测量值、截图位置）才能记 PASS，否则 INCOMPLETE。
> 结论词汇见 [evidence-and-verdicts.md](evidence-and-verdicts.md)。

## 目录

0. [订单契约（先于一切）](#0-订单契约先于一切)
1. [电源](#1-电源)
2. [信号完整性](#2-信号完整性)
3. [保护与 ESD](#3-保护与-esd)
4. [连通性](#4-连通性)
5. [机械](#5-机械)
6. [布线与铺铜](#6-布线与铺铜)
7. [DFM](#7-dfm)
8. [装配（贴片）](#8-装配贴片)
9. [文件与验收记录](#9-文件与验收记录)

列含义：**E** = EasyEDA 路径，**K** = KiCad 路径。`signoff-*` 来自 `pcbpilot signoff`；`pcb gate` / `kicad route` 门禁名见
[kicad.md](kicad.md)。“live” 规则（`pcb check` 的 DFM 项）只在 EasyEDA 在线编辑器上运行；`pcb check --board` 离线时只跑
copper-to-edge、isolation、via-current。

## 0. 订单契约（先于一切）

| # | 项 | 证明 |
|---|---|---|
| 0.1 | 制造商与服务、层数、板厚、铜厚、表面处理、阻焊/丝印颜色、是否阻抗控制、贴片面（单/双面）写成一份订单契约，每个数值带**来源 URL + 获取日期** | manual |
| 0.2 | 工艺极限（最小线宽/间距/孔/焊环/丝印）取自当期契约，不取参考文件里的旧数字；参考里的厂商数值没有日期时视为过期 | manual；数值写进 `intent.json` 的 fab 规则后由 `intent-rules` / `kicad-drc` 门禁检查 |
| 0.3 | 板上 DRC 规则与契约一致（不低于契约极限） | E：`pcb rules check --intent intent.json`；K：`kicad route` 的 `intent-rules`；manual 核对 `.kicad_dru` 与契约 |

## 1. 电源

| # | 项 | 证明 |
|---|---|---|
| 1.1 | 每条电源轨的电压、电流预算有来源（数据手册页码）且在 `sim.json` 场景里 | `sim power --report`；`intent derive --report`；数据来源 manual |
| 1.2 | 稳压器输入/输出电压、压差、功耗与温升在余量内 | `sim power`（稳压器表）；`sim post-layout` 器件温度；Tj 余量 manual 对照手册 |
| 1.3 | 每个电源脚有去耦，电容紧贴对应引脚 | E：`pcb check` `decap-too-far`（live，WARN）；K：manual（板图逐脚核对） |
| 1.4 | 走线/铺铜按每段电流的线宽，不低于 `widthMil.min` | `signoff-copper`；`intent-widths` 门禁 |
| 1.5 | 换层过孔组载流足够 | `signoff-copper`；`pcb check --intent` `via-current` |
| 1.6 | 负载焊盘 IR 压降在预算内、无铜上开路 | `signoff-ir`；`post-layout-sim` 门禁；`SIM/KICAD MISMATCH` 记 INCOMPLETE |
| 1.7 | 电源网已铺铜/平面，不靠细线 | E：`pcb check` `power-not-poured`；K：manual（`kicad route` 默认铺铜层见 kicad.md） |
| 1.8 | 上电时序、使能/复位默认状态、反接/过压保护符合设计 | manual（原理图 + 手册） |

## 2. 信号完整性

| # | 项 | 证明 |
|---|---|---|
| 2.1 | 差分对、等长组按意图约束 | `intent-lengths` 门禁（`kicad route`）；E：`pcb diff-pair list`、`pcb eq-group list`、`pcb report`（长度/skew） |
| 2.2 | 阻抗控制网的线宽/间距与契约叠层计算一致 | manual（叠层与阻抗来自 0.1 契约） |
| 2.3 | 高速/时钟线有连续参考平面，不跨分割 | manual（看平面层与走线）；E：`pcb check` `via-crosses-plane` 提示反焊盘风险 |
| 2.4 | 并行耦合（3W） | E：`pcb check` `parallel-coupling`（live）；K：manual |
| 2.5 | 晶振/敏感模拟走线短、远离开关节点 | manual；E 模块布局见 pcb-routing.md 的晶振模块验收 |

## 3. 保护与 ESD

| # | 项 | 证明 |
|---|---|---|
| 3.1 | 外部接口（USB、按键、外接线）有 ESD/TVS，器件靠近连接器、先经保护再进芯片 | `intent derive` 识别 ESD/TVS 块（`intent.md`）；位置与走线顺序 manual |
| 3.2 | 绝缘域之间电气间隙与爬电（开槽计入） | `signoff-safety`；`pcb check --intent` `iso-clearance` / `iso-creepage`；`isolation` 门禁 |
| 3.3 | 铜到板边、到金属安装孔 | `signoff-safety`；`copper-to-edge` 门禁；`pcb check` `copper-to-edge` / `copper-to-hole` / `plane-pullback` |
| 3.4 | 保险丝/限流、反接保护额定值匹配最坏场景电流 | `sim power`（`worst` 场景）+ manual 对照手册 |
| 3.5 | 安全标准与绝缘等级来自用户/认证要求，不由 Agent 假设 | manual（`intent derive --spec` 的输入要有来源） |

## 4. 连通性

| # | 项 | 证明 |
|---|---|---|
| 4.1 | 原理图 ERC 无错误，警告逐条解释 | E：`sch gate`；K：`kicad sch-check` |
| 4.2 | 无短路、无开路、无单引脚网、无网名变体 | E：`sch bridge-check`、`sch nets --strict`；PCB：`pcb drc` / `kicad-drc`、`route-complete` |
| 4.3 | 原理图 pin→net 与板上 pad→net 逐焊盘一致 | `signoff-continuity`；`pad-net-diff` 门禁；`scripts/pad-net-diff.py` |
| 4.4 | 每条意图规则能追溯到原理图网、器件、板上焊盘 | `signoff-trace` |
| 4.5 | 连接器脚序与需求/对接件一致（含镜像：本板座子 vs 对端插头） | `review-panel`（以 pinout 文档为 requirements）；manual 逐脚 |
| 4.6 | 有意悬空脚都有 NC 标记，没有“缺失连接自动当 NC” | E：`sch check` 悬空脚清单；K：`kicad netlist` 的 `noConnected` vs `connectionState:"unconnected"` |

## 5. 机械

| # | 项 | 证明 |
|---|---|---|
| 5.1 | 板框尺寸、圆角、开槽与机械图一致，板框闭合 | E：`pcb outline-get`；K：`kicad snapshot` 的 outline；与机械图对照 manual |
| 5.2 | 安装孔位置、孔径、禁铜/禁件范围（含垫圈/螺帽外径） | E：`pcb check` `copper-to-hole`；位置 manual |
| 5.3 | 板边连接器朝向、伸出量、插头插拔空间 | E：`pcb check --spec` `internal-on-edge`、`connector-plug-clearance`、`connector-mating-blocked`；K：manual |
| 5.4 | 高器件、散热器、外壳干涉 | manual（3D/机械图） |
| 5.5 | 天线净空（各层无铜） | E：`pcb check` `antenna-keepout`、`pcb antenna-keepout`；K：manual |

## 6. 布线与铺铜

| # | 项 | 证明 |
|---|---|---|
| 6.1 | 全部连接已布通（铺铜接上的需 DRC 判连通） | `route-complete`；`pcb drc` / `kicad-drc` 无 unconnected |
| 6.2 | 板上规则 = 意图网类（线宽/间距） | `intent-rules` 门禁 |
| 6.3 | 铺铜已按当前规则重灌；无死铜、无错层铺铜 | E：`pcb poured-list`（known-empty 规则）、`pcb check` `netless-pour` / `pour-layer-mismatch`；K：`kicad fab` 出图时 `--check-zones` 内存重灌 |
| 6.4 | 无悬空线头、孤岛线、重复线段、单层过孔 | E：`pcb check`（live）；K：kicad-cli DRC `track_dangling` |
| 6.5 | 锐角、非 45° 走线 | E：`pcb check` `acute-angle` / `non-orthogonal`；K：manual |
| 6.6 | 花焊盘辐条足够（手焊/波峰焊的通孔） | K：kicad-cli DRC `starved_thermal`；E：manual |

## 7. DFM

| # | 项 | 证明 |
|---|---|---|
| 7.1 | DRC 0 错误（按契约规则） | E：`pcb drc`；K：`kicad-drc` 门禁（警告另列） |
| 7.2 | 盘中孔（via-in-pad）有处理工艺（塞孔/盖油）或已移出 | E：`pcb check` `via-in-pad` / `netless-via-in-pad`；K：manual |
| 7.3 | 丝印不压焊盘、字号/线宽不低于契约，位号贴自身封装 | `silkscreen` 门禁；E：`pcb check` `silk-over-pad`；K：kicad-cli `silk_over_copper` / `silk_overlap` |
| 7.4 | 底层丝印从底面可读 | E：`pcb check` `silkscreen-flipped`（只判无歧义的情形，见 pcb-design-rules.md §11.2）；K：manual |
| 7.5 | 阻焊桥、细间距封装阻焊扩展 | K：kicad-cli `solder_mask_bridge`；E：manual |
| 7.6 | 过孔盖油/开窗与契约一致 | `pcb gate --silk-via-openings` 只影响丝印门禁；工艺选择 manual |
| 7.7 | 拼板、工艺边、V-cut / 邮票孔（若由设计方出拼板） | manual；V-cut 板边距离：`pcb check --edge-kind vcut` |

## 8. 装配（贴片）

| # | 项 | 证明 |
|---|---|---|
| 8.1 | 每个贴装件有合法 LCSC C 号；安装孔/Mark/测试点/Logo 已排除 BOM | `signoff-parts`；K：`kicad lcsc --check`，`kicad fab` 缺号即失败不写文件 |
| 8.2 | DNP 件：原理图标 DNP 或排除 BOM/坐标，不靠删 BOM 行 | K：`kicad fab` 按 DNP / 排除字段过滤（`--exclude-dnp`）；结果核对 manual；E：manual（`bom export`） |
| 8.3 | 贴片 Mark 点（全局 ≥3 不共线；细间距件按需局部 Mark） | E：`pcb check` `fiducial-missing`（INFO，仅在顶层焊盘较多的 SMT 板上报）；K：manual |
| 8.4 | 拼板/工艺边上的 Mark 与工具孔（若出拼板） | manual |
| 8.5 | CPL 坐标与角度：在厂商贴片预览里逐个看方向，偏差写进覆盖文件 | K：`kicad fab --cpl-overrides <本板的覆盖文件>`，用过的覆盖记录在 `fab-report.json`；预览核对 manual。pcbpilot 不内置按封装的角度表 |
| 8.6 | 极性件（二极管、电解、IC 1 脚）丝印标记清晰且与封装一致 | manual |
| 8.7 | 双面贴装时底面件的重量/回流顺序可接受 | manual |
| 8.8 | 器件库存/可贴性（basic/extended、库存 ≥ 数量） | `kicad lcsc --search … --qty N` 的结果（下单当天再查一次，记日期） |

## 9. 文件与验收记录

| # | 项 | 证明 |
|---|---|---|
| 9.1 | 输出到**新的空目录**（或 v0.10.2 起由清单管理的目录），文件是本次生成、非空 | K：`kicad fab --out <新目录>`、`fab-report.json`；见 [kicad-file-safety.md](kicad-file-safety.md)。E：UNSUPPORTED（pcbpilot 无 EasyEDA 制造输出） |
| 9.2 | Gerber 层集合与叠层一致（内层顺序）、PTH/NPTH 钻孔分开 | `fab-report.json` 的层清单 + manual 在 Gerber 查看器核对 |
| 9.3 | Gerber 查看器逐层看：板框闭合、孔与焊盘对齐、丝印、阻焊 | manual |
| 9.4 | 说明书、设计报告是最新版本 | `signoff-artifacts`；`board-manual` / `design-report` 门禁 |
| 9.5 | 发布评审通过 | `review-panel --stage release`；`signoff-review` 只看 design 阶段 |
| 9.6 | 最终验收记录：每节结论（PASS / FAIL / INCOMPLETE）、manual 项的证据、waiver 列表、订单契约日期 | 按 [review-workflow.md](review-workflow.md) 的报告模板 |

整体结论：0–9 节每个适用项 PASS（含签名 waiver）才写“可以投板”；任一 FAIL 写“不可投板”；
没有 FAIL 但有 INCOMPLETE / manual 未给证据时写“未完成检查，不能确认可投板”，列出缺的项。
