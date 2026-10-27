# ERC / DRC 分诊：分类、归属与修法

> 用途：拿到 ERC/DRC 或 pcbpilot 检查报告后，先判断每条违规**归谁**（板级几何 / 封装自身 / 原理图 /
> 环境配置），再按规则查修法和对应 pcbpilot 命令。结论词汇见 [evidence-and-verdicts.md](evidence-and-verdicts.md)；
> 评审时只报告修法，用户确认后才执行写入命令。

## 目录

1. [分诊流程](#1-分诊流程)
2. [归属判定](#2-归属判定)
3. [KiCad ERC](#3-kicad-erc)
4. [KiCad DRC](#4-kicad-drc)
5. [EasyEDA：官方 DRC 与 pcbpilot 重建检查](#5-easyeda官方-drc-与-pcbpilot-重建检查)
6. [不要这样修](#6-不要这样修)

## 1. 分诊流程

1. **拿逐条结果，不看汇总**：
   - KiCad：`kicad-cli sch erc --format json --severity-all -o erc.json <root>.kicad_sch`、
     `kicad-cli pcb drc --format json --severity-all --schematic-parity -o drc.json <board>.kicad_pcb`；
     或 `pcbpilot kicad sch-check --sch <root>.kicad_sch`（ERC + 质量检查一份 JSON）。
   - EasyEDA：`pcbpilot pcb drc --json`（每条一行 `{rule, objType, ruleName, net, x, y, layer, objs, message}`，坐标 mil）、
     `pcbpilot sch gate --json`、`pcbpilot pcb check --json`。
2. **先排环境类**（第 3/4 节标“环境”的类型）：它们说明检查环境不对，不是设计缺陷，但在修好前其它结果可能不全
   → 相关项 `INCOMPLETE`。
3. **按归属分组**（第 2 节），同一根因的多条合并成一个发现（例如一个器件挪错位置引出的 courtyard + clearance + silk）。
4. **先修上游**：原理图 → 封装/库 → 布局 → 布线 → 铺铜 → 丝印。下游违规常在上游修好后消失；每修一轮重跑完整检查。
5. **输入板原有 vs 本次引入**：`kicad route` 把输入板 DRC 作为基线，`kicad-drc` 门禁的 `info` 标出输入板原本就有的错误；
   EasyEDA 在写入前先存一份 `pcb drc --json` 作基线，写入后对比。

## 2. 归属判定

按违规里**涉及的对象**判断（KiCad JSON 的 `items[].description`，如 `PTH pad 1 [VCC] of C1`、`Footprint C2`、
`Track [VCC] on top_layer`；EasyEDA 的 `objs` 与 `objType`）：

| 涉及对象 | 归属 | 在哪里修 |
|---|---|---|
| 同一个封装内的焊盘/丝印/courtyard 互相冲突（`pth_inside_courtyard`、`silk_over_copper` 两端都属一个位号） | **封装自身几何** | 封装库（修封装或换封装），不是挪器件；同一封装的每个实例都会报 |
| 两个以上封装的 courtyard / 焊盘 / 孔 | **布局** | 移动器件 |
| 走线、过孔、铺铜（含与焊盘之间） | **布线 / 铺铜** | 改线、重灌、规则 |
| 走线/焊盘与 Edge.Cuts、安装孔 | **板级几何** | 板框、禁布带、铺铜内缩 |
| 原理图↔PCB 不一致（`schematic_parity` 各类、EasyEDA `Netlist Error`）、`unconnected_items` 在原理图本就不该连的地方 | **原理图 / 同步** | 先核对原理图，再同步到 PCB |
| 库找不到、配置缺失（`lib_footprint_issues`、`footprint_link_issues`、`lib_symbol_issues`） | **环境配置** | 库表 / KiCad 配置；不是设计错误 |

判定不了归属（对象列表为空、坐标落在无对象处）→ 该条 `INCOMPLETE`，附坐标请用户在 KiCad / EasyEDA 里看。

## 3. KiCad ERC

“实测”= 2026-10-26 在 KiCad 10.0.6 shipped demo 的副本上观察到（隔离的空 KiCad 配置）。其余为 KiCad ERC 的常见类型，
本次未复现，以 kicad-cli 输出的 `type` 为准。

| type | 含义 | 归属 | 常见修法（pcbpilot） |
|---|---|---|---|
| `power_pin_not_driven` | 电源输入脚所在网没有电源输出驱动（电源来自连接器或被动脚器件） | 原理图 | `pcbpilot kicad sch-check --sch … --fix-pwr-flag`（逐网加 PWR_FLAG，网表校验后才写）；确认真有电源来源，否则是漏接 |
| `pin_not_connected` | 引脚未连接 | 原理图 | 真该接 → `kicad sch-edit`（`connect` op）；有意悬空 → 加 no-connect（sch-build spec 的 NC） |
| `pin_not_driven` / `pin_to_pin` | 输入脚无驱动 / 两类引脚电气类型冲突（如两个输出相连） | 原理图或符号 | 核对符号引脚电气类型与数据手册；连接错误用 `kicad sch-edit` |
| `label_dangling` / `wire_dangling` | 标签或导线一端没连 | 原理图 | `kicad sch-edit` 重连；sch-build 工程不手改 |
| `multiple_net_names` / `similar_labels` | 一条网多个名字 / 仅大小写不同的标签 | 原理图 | `kicad sch-edit` 的 `rename_net` 统一 |
| `unannotated` / `duplicate_reference` | 位号未编号 / 重复 | 原理图 | 在 spec 里给唯一位号后 `kicad sch-edit` |
| `endpoint_off_grid` | 端点不在格点 | 原理图（美观） | 导入的图纸可接受；sch-build 生成的不应出现 |
| `footprint_link_issues`（实测） | 符号的 Footprint 字段指向配置里没有的库 | **环境** | 把库加进工程 `fp-lib-table`（[kicad-file-safety.md](kicad-file-safety.md)）；隔离配置下的 ERC 必然报，不代表设计错 |
| `lib_symbol_issues`（实测） | 符号库不在当前库表 | **环境** | 同上（`sym-lib-table`） |

pcbpilot 自己的质量检查（`kicad sch-check` 的 `quality[].findings[].kind`，任一存在即非零退出）：

| kind | 含义 | 修法 |
|---|---|---|
| `symbol-overlap`、`wire-through-body` | 符号重叠、导线穿过符号体 | sch-build 工程：`kicad sch-edit`（`move_zone`）或在 spec 拆区；其它工程：用户在 KiCad 里改 |
| `diagonal-wire`、`off-grid`、`wire-overlap` | 斜线、离格、导线重叠 | 同上；导入图纸的 off-grid 可 `--ignore off-grid` 并在报告注明 |
| `pin-on-wire`、`wire-end-on-wire` | 引脚或线端落在另一导线中段——**KiCad 不会连接** | 这是隐藏开路，按 S1 处理；修连接后核对网表 |
| `label-overlap`（实测）、`label-on-wire`、`pin-in-label`、`text-over-symbol`、`text-overlap` | 标签/字段文字互相或与符号重叠 | sch-build 工程重建或 `move_zone`；不影响网表但影响可读性 |
| `title-block`（实测）、`off-page` | 内容进入标题栏或出图纸 | `pcbpilot kicad sch-fit --sch <root> --dry-run` 看结果，再去掉 `--dry-run`（原地写） |

## 4. KiCad DRC

实测类型来自一份 shipped demo 副本上故意制造的错误（挪器件重叠、删线、改细线）；其余为 KiCad DRC 常见类型。

| type | 含义 | 归属 | 修法 |
|---|---|---|---|
| `shorting_items`（实测） | 不同网的铜相连 | 布线/布局 | 删除或改走短路的线；KiCad：`pcbpilot kicad route --rip-up …` 重布，或用户在 KiCad 改 |
| `clearance`（实测） | 铜间距小于规则（含铺铜间距） | 布线/布局 | 改线/挪件；铺铜类重灌（`kicad route` 第 4 步会重灌） |
| `unconnected_items`（实测） | 同网缺连接 | 布线（或原理图错连） | 先查原理图是否本应相连；再布线。`kicad route` 的 `route-complete` 门禁逐条给“移动器件或加逃线” |
| `track_width`（实测） | 线宽低于规则 | 布线 | 意图网类：`kicad route --intent intent.json`（PP 网络类 + `.kicad_dru`）；全局下限 `--min-track-mil` |
| `track_dangling`（实测） | 线端悬空 | 布线 | 删除残桩 |
| `courtyards_overlap`（实测） | 两个封装 courtyard 重叠 | **布局** | 挪件：`pcbpilot kicad place --pcb … --out …`（会清掉铜，需重布）或用户在 KiCad 挪 |
| `pth_inside_courtyard`（实测） | 通孔焊盘在别的 courtyard 内 | 布局（两个位号）或封装自身（一个位号） | 按第 2 节判归属 |
| `hole_to_hole`（实测） | 钻孔间距不足 | 布局或封装自身 | 同上；同一封装内的孔距问题回到封装库 |
| `silk_overlap`、`silk_over_copper`（实测） | 丝印互相重叠 / 被阻焊开窗裁掉 | 封装自身或丝印摆放 | 位号文字：`kicad route` 的丝印步骤（`planSilkTight`）；封装外形丝印：封装库 |
| `solder_mask_bridge`（实测） | 阻焊开窗把不同网连成一片 | 布线/封装 | 改线避开开窗；细间距封装检查阻焊扩展 |
| `lib_footprint_issues`（实测） | 板上封装的库在当前配置不存在 | **环境** | 库表；不影响铜几何检查但影响“与库一致”类检查 |
| `footprint_symbol_mismatch`（实测，parity） | 板上封装与原理图符号给的封装不同 | 原理图/同步 | 先定哪个对，改原理图（`kicad sch-edit` `replace_part`）或在 KiCad 里“从原理图更新 PCB” |
| `copper_edge_clearance` | 铜离板边太近 | 板级几何 | `kicad route --edge-mil`（意图 edge 为下限）；板边连接器焊盘按 `edgeExemptPads` 处理 |
| `hole_clearance`、`annular_width`、`drill_out_of_range`、`via_diameter` | 孔与铜、焊环、孔径、过孔尺寸超限 | 布线（过孔）或封装自身（焊盘） | 过孔参数 `kicad route --via-dia-mil/--via-drill-mil`；焊盘回封装库 |
| `starved_thermal` | 花焊盘辐条不足 | 铺铜 | `kicad route` 铺铜时通孔用花焊盘、SMD 实连（见 [kicad.md](kicad.md)） |
| `missing_footprint`、`extra_footprint`、`net_conflict`（parity） | 原理图与板的器件/网不一致 | 原理图/同步 | `pad-net-diff` 逐焊盘对账后同步 |

`kicad route` 的 `kicad-drc` 门禁只计 `--severity-error`；警告（丝印重叠等）只记在 `summary.drc.warnings`，评审仍要列出。

## 5. EasyEDA：官方 DRC 与 pcbpilot 重建检查

**`pcb drc --json`**：`rule` 是类别（pcbpilot 测试夹具中实际出现：`Clearance Error`（`ruleTypeName` `Safe Spacing`，
`objType` 如 `Track to Track`）、`Connection Error`、`Netlist Error`（`ruleName` `Import Changes`，原理图与 PCB 网表不一致））。

| 类别 | 归属 | 修法 |
|---|---|---|
| `Clearance Error` | 布线/布局；铺铜间距类先 `pcb pour-rebuild` 再测 | `pcbpilot pcb track-delete --ids <objs>` 后重布；挪件 `pcbpilot pcb modify --id … --patch '{"x":…,"y":…}'` |
| `Connection Error` | 布线 | 补线或 `pcb autoroute` / `pcb route-critical`；铺铜网先重灌再判 |
| `Netlist Error` | 原理图/同步 | 核对原理图后 `pcbpilot pcb import-changes`，再 `pad-net-diff` 对账 |

规则阈值本身不对（不是设计不对）：`pcbpilot pcb rules check --intent intent.json`（只读）→ `pcb rules apply --intent … --dry-run` → 用户确认后去掉 `--dry-run`。

**pcbpilot 重建检查**（官方 DRC 不报的）：

| 检查 / 规则 | 级别 | 归属 | 修法 |
|---|---|---|---|
| `sch bridge-check` BRIDGE | ERROR | 原理图（合并的导线树带两个网名 = 短路） | 删整棵树 `pcbpilot sch prim-delete --ids …`，再逐脚 `pcbpilot sch connect --pin U1:5 --kind … --net …` |
| `sch bridge-check` ORPHAN | WARN | 原理图 | 删残桩或接上 |
| `sch check` 悬空脚 | WARN | 原理图 | 真该接的接上；有意悬空 `pcbpilot sch no-connect --designator U1 --pin 23` |
| `sch nets` 网名变体 / 单引脚网 | ERROR / WARN | 原理图 | `sch block-apply … --bind <端口>=<统一网名>` |
| `sch reconcile` split / missing | ERROR | 原理图 | 按块库重连 |
| `pcb check` `copper-to-edge` / `plane-pullback` / `copper-to-hole` | ERROR | 板级几何 | `pcbpilot pcb pour-fit --net GND --layer 1 --intent intent.json --dry-run`（按安全距离内缩）；走线改线 |
| `pcb check` `iso-clearance` / `iso-creepage` | ERROR | 布局/布线 | 拉开两域或开槽；距离来自 `intent.json` |
| `pcb check` `via-current` | ERROR / WARN | 布线 | 加过孔或加粗 |
| `pcb check` `pour-layer-mismatch`、`netless-pour` | ERROR / WARN | 铺铜 | `pcbpilot pcb pour-clean --netless --dry-run`，再重建铺铜 |
| `pcb check` `power-not-poured`、`width-under-spec` | WARN | 布线/铺铜 | `pcb pour-fit` / `pcb power-planes`；`pcbpilot pcb widen --net … --dry-run` |
| `pcb check` `dangling-end`、`floating-track-island`、`duplicate-segment`、`overlapping-via`、`single-layer-via` | WARN | 布线 | `pcb track-delete --ids` / `pcb via-delete --ids` |
| `pcb check` `via-crosses-plane` | WARN | 布线/铺铜（平面后加的过孔无反焊盘） | 删过孔改外层，或 `doc reload` + `pcb pour-rebuild` |
| `pcb check` `acute-angle`、`parallel-coupling` | WARN | 布线 | 改线；3W 规则系数 `--coupling-w` |
| `pcb layout-lint` overlap / off-board | ERROR | 布局 | `pcb modify` / `pcb move` |

## 6. 不要这样修

- 不为让 DRC 变干净而放宽规则、加 KiCad exclusion 或把 `--inset`、`--edge-mil` 压到安全距离以下；接受一条违规
  只能用用户签名的 waiver，并保留在报告里。
- 不手工编辑 `.kicad_pcb` / `.kicad_sch` 文本去“删掉”违规（[kicad-file-safety.md](kicad-file-safety.md)）。
- 封装自身几何的违规不靠挪器件消掉；环境类违规不当设计缺陷报，也不当 PASS 忽略——修好环境后重跑。
- 修完不只重跑报错的那一项：重跑该档评审的全部检查（[review-workflow.md](review-workflow.md)）。
