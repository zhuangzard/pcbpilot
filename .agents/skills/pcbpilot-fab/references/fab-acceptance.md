# 制造输出验收：订单契约、逐文件核对与验收记录

> 用途：`kicad fab` 成功退出之后、把文件交给用户上传之前，逐项核对产物并写验收记录。
> 结论词汇与汇总规则见 [evidence-and-verdicts.md](../../pcbpilot/references/evidence-and-verdicts.md)，
> 输出目录与清单行为见 [kicad-file-safety.md](../../pcbpilot/references/kicad-file-safety.md#6-制造输出目录与清单)，
> 投板前全量清单见 [prefab-checklist.md](../../pcbpilot/references/prefab-checklist.md)。

## 目录

1. [订单契约](#1-订单契约)
2. [预检的范围](#2-预检的范围)（[2.1 有效规则集 vs 契约](#21-有效规则集-vs-契约)）
3. [逐文件验收](#3-逐文件验收)
4. [BOM 与 CPL 审核](#4-bom-与-cpl-审核)
5. [贴片方向修正与预览](#5-贴片方向修正与预览)
6. [验收记录模板](#6-验收记录模板)
7. [实测记录](#7-实测记录)

## 1. 订单契约

出图前和用户确认一份契约，写进工程目录（例如 `fab/order-contract.json`，不放进 `--out`），每个数值带来源：

```json
{
  "vendor": "JLCPCB", "service": "PCB + PCBA（经济型 / 标准型）",
  "layers": 4, "thicknessMm": 1.6, "copperOz": {"outer": 1, "inner": 0.5},
  "finish": "ENIG", "mask": "green", "silk": "white",
  "impedance": {"controlled": true, "stackupId": "<厂商叠层编号>", "nets": ["USB_D±: 90 Ω diff"]},
  "assembly": {"sides": "top", "qty": 5},
  "viaProcess": "tented",
  "limits": {"minTrackMm": 0.127, "minSpaceMm": 0.127, "minDrillMm": 0.3, "minAnnularMm": 0.13},
  "sources": [{"item": "layers", "url": "<报价单 URL>", "retrieved": "YYYY-MM-DD"},
               {"item": "limits", "url": "<厂商能力页 URL>", "retrieved": "YYYY-MM-DD"},
               {"item": "finish", "url": "<厂商工艺页或报价单 URL>", "retrieved": "YYYY-MM-DD"},
               {"item": "assembly", "url": "<报价单 URL>", "retrieved": "YYYY-MM-DD"}]
}
```

- 这就是 `kicad fab --preflight --contract` 读的格式：未知键直接拒绝（`CONTRACT_INVALID`，拼错的 `limits` 不会被当成“没有限制”）；
  必填 `vendor`、`layers`（铜层数）和 `sources`（http(s) `url` + `YYYY-MM-DD` 的 `retrieved`）；每个会被检查的值（`layers`、每项 `limits`、
  `finish`、`assembly.sides`）都要有一条 `item` 指向它或父项（`limits`、`assembly`）的来源，`item` 拼错（不是契约字段）直接拒绝；
  `limits` 缺哪项，对应的 `min-*` 检查就是 cannot-check；`finish` 缺省则不比，要写明确工艺（“HASL lead-free”，不是 “HASL”）；
  `assembly.sides` 缺省则 `assembly-sides` 为 cannot-check。
- 数值只来自用户、厂商当期页面或报价单；每条 `sources` 写 URL 和获取日期。没有日期的旧数字（包括 Skill 参考里的）
  只当线索，下单当天要重新取。
- 用户没定的项（例如是否阻抗控制、贴片面）就问，不替用户选；缺项时契约相关的结论记 `INCOMPLETE`。
- `assembly.sides` 只取 `top` / `bottom` / `both`，出图后与 CPL `Layer` 列的面集合比对（第 4 节）；`both` 只在两面都有贴装件时成立。
- 盲埋孔、微孔、盘中孔、半孔、拼板：板上有就必须在契约里写明工艺，否则 `FAIL`（厂商默认服务通常不做）。

## 2. 预检的范围

每条预检写清它覆盖什么、不覆盖什么。`kicad fab` 出图本身**不跑 DRC/ERC**；`kicad fab --preflight` 只读地把能自动判定的项一次跑完：

| 检查 | 命令 | 覆盖 | 不覆盖 |
|---|---|---|---|
| ERC + 原理图质量 | `pcbpilot kicad sch-check --sch <root>.kicad_sch` | 整个层次的 ERC、重叠/离网格/标签压件 | 板子 |
| DRC、排除项策略、原理图一致性、板框闭合、层数、最小线宽/间距/孔径/环宽、表面处理、贴片面、C 号覆盖、BOM/CPL 位号 | `pcbpilot kicad fab --preflight --pcb … --sch … --contract fab/order-contract.json` | 每项 pass/fail/cannot-check，见 [kicad-jlc.md 预检](../../pcbpilot/references/kicad-jlc.md#预检kicad-fab---preflight只读)；板上规则由 KiCad DRC 查，规则本身对照契约（[2.1](#21-有效规则集-vs-契约)） | 契约四项以外的工艺极限（孔到孔、板边距离、微孔、阻抗）；C 号是否就是对的料；Gerber 文件本身（第 3 节） |
| 只看修改引入的 DRC | `pcbpilot kicad drc --pcb <新板> --baseline <上次 preflight.json 或 drc-report.json>`（或 `--baseline-pcb <旧板>`） | 新增违例（规则 + 归属 + 位置）与消失的违例 | 基线本身的问题（基线里已有的照样要修或签 waiver） |
| 发布签核 | 有运行目录：`signoff --run-dir <dir> …`（先做输入哈希绑定）；手工布线：显式输入 `signoff --board … --intent … --sim … --connectivity …` | 8 个 `signoff-*` 子门禁 | 运行目录读的不是这次出图的文件（哈希不符）→ 受影响门禁作废重跑；两种都没跑 → `INCOMPLETE` |

DRC 在大板上要几分钟（实测 8 层小板 6.5 min），让它跑完。preflight 的 `cannot-check`（DRC 没跑成、缺契约、规则读不懂）记 `INCOMPLETE`，
`fail` 记 `FAIL`；DRC 违例归属与修法见 [drc-triage.md](../../pcbpilot/references/drc-triage.md)，报告里每条带 `owner.class` 与位号/网名。

### 2.1 有效规则集 vs 契约

KiCad DRC JSON 只列**违例**，不列生效的规则；干净的 DRC 只说明板子没违反它自己的规则。`--preflight` 的 `min-track` / `min-space` /
`min-drill` / `min-annular` 读出有效规则集（只读），对每项取“板上任何地方允许的最小值”，与契约 `limits` 比：

| 来源 | 位置 | 计入 |
|---|---|---|
| Board Setup 约束 | `<board>.kicad_pro` → `board.design_settings.rules` | `min_track_width`、`min_clearance`、`min_through_hole_diameter`（`allow_microvias` 时加 `min_microvia_drill`）、`min_via_annular_width`；值为 0 记为“未设”；`allow_microvias` 等开关不是布尔值 → 孔径 cannot-check |
| 网络类 | `.kicad_pro` → `net_settings.classes[].clearance` | 每个类的间距（只有 `Default` 算全局下限；其他类为 0 → cannot-check，缺省则继承 Default）；网络类线宽/过孔尺寸只是默认值，DRC 不按它报错，不计入 |
| 自定义规则 | 同名 `<board>.kicad_dru`：`(constraint <track_width / clearance / hole_size / annular_width> (min …))` | 带条件/层的规则只拉低最小值，无条件的 error 规则才是全局下限；`(severity ignore)` / `(severity warning)`（不是强制下限）、只有 opt/max、数值读不懂 → 该项 cannot-check |
| 局部覆盖 | `.kicad_pcb` 封装、焊盘的 `(clearance …)`，铺铜的 `(connect_pads (clearance …))` | 计入间距（不算全局下限）；值读不懂 → cannot-check |

判定：任一来源更小 → `FAIL`，`items` 列出出处（改板子规则后重跑 DRC，不改契约凑数）；有读不懂/证明不了的规则，或没有全局下限（只有“未设”、
局部覆盖或带条件的规则）→ cannot-check（请用户在 Board Setup 填契约值）；否则最小值 ≥ 契约 → PASS。`info` 另列板上实测最细线宽、最小过孔/焊盘孔，方便和规则对照。契约四项以外的极限（孔到孔、铜到板边、
微孔、盲埋孔、阻抗线宽）仍按上表来源人工对照，结论写进验收记录；看不全 → `INCOMPLETE`。

## 3. 逐文件验收

| 对象 | 怎么核 | 停止条件 |
|---|---|---|
| 新鲜度 | `fab-report.json` 与清单 `.pcbpilot-fab-manifest.json` 里的每个文件，修改时间都是本次运行 | 任一文件比本次运行旧 |
| 非空 | 铜层、阻焊、丝印、`Edge_Cuts` 都有图形，不只是文件头 | 铜层或板框只有头（约 0.5 KB）；锡膏层只有头仅在该面没有 SMD 焊盘时合理 |
| 层集合 | `fab-report.json` 的 `layers` 中 `*.Cu` 个数 = 契约 `layers`；`zipFiles` = 每层一个 Gerber + `PTH.drl` + `NPTH.drl` | 少层、多层、缺钻孔 |
| 叠层与工艺 | 读 `gerber/<板>-job.gbrjob`（Gerber X2 作业文件，不进 zip）的 `GeneralSpecs`：`LayerNumber`、`BoardThickness`、`Finish`；`FilesAttributes` 每个铜层的 `Copper,Ln,Top/Inr/Bot` 顺序 | 与契约不一致：改板子 Board Setup 的叠层或改契约，不在 zip 里改文件 |
| 钻孔 | `grep '^T' gerber/<板>-PTH.drl` 看刀具表；PTH 与 NPTH 分开 | 出现 `C0.000` 之类不可能的孔径；板上有盲埋孔/微孔却只有一个贯通钻孔文件 |
| 残留文件 | 终端与 `warnings` 里 “holds Gerber/drill file(s) this run did not produce” | 有残留：确认后由用户删除，或改用新目录 |
| Gerber 查看器 | 用户在 KiCad GerbView 或厂商上传后的在线预览逐层看：板框闭合、孔与焊盘对齐、内层顺序、丝印/阻焊开窗、过孔盖油 | kicad-cli 没有查看器命令，这一项是 **manual**；没有用户的观察记录就是 `INCOMPLETE` |

读作业文件（任意 Python 3）：

```bash
python -c "import json,sys;d=json.load(open(sys.argv[1],encoding='utf-8'));print(d['GeneralSpecs']);[print(f['Path'],f['FileFunction']) for f in d['FilesAttributes']]" fab/gerber/<板>-job.gbrjob
```

只上传 `<板>-gerber.zip`、`<板>-bom.csv`、`<板>-cpl.csv`。`<板>-kicad-pos.csv` 是 kicad-cli 的原始坐标文件（角度未归一、
列名不同），留作对照，不上传；`gerber/` 里的钻孔图 `*-drl_map.gbr` 与 `.gbrjob` 也不进 zip。

## 4. BOM 与 CPL 审核

- BOM 行按（值、封装、C 号）合并；逐行核对 C 号对应的 MPN 与封装和原理图一致。`--search` 的排名第一不等于正确的料：
  实测按 MPN 搜索时首条可能是带后缀的工程样品或兼容替代型号，必须核对 MPN 完全一致或由用户确认替代。
- 下单当天对每个 C 号再查一次库存与 basic/extended（`pcbpilot kicad lcsc --search <C号> --qty <需求量>`），把日期写进记录。
  需求量按 C 号算，不是按板数：**板数 × 每板贴装位数**（BOM 里该 C 号的位号个数）**+ 损耗/备料**（厂商报价里的损耗或最小
  备料数，按当期报价取，不自己编比例）；拼板时板数取实际贴装的板数（拼板数 × 每拼板的单板数，可能比订购数多）。库存低于
  需求量 → 该行 `FAIL`（换料要用户确认）；没算损耗或拼板 → `INCOMPLETE`。
- `fab` 输出的 `cpl: left out N part(s) not assembled (…)` 与 `fab-report.json` 的 `cplOmitted` 列出不贴的件；逐个确认
  它们本来就不该贴（DNP、安装孔、测试点、Logo），而不是漏了属性。
- CPL 件数 = BOM 位号总数（工具已强制，位号不一致直接失败）。CPL `Layer` 列出现的面集合（只有 `Top` → top；只有 `Bottom` →
  bottom；两者都有 → both）必须等于契约 `assembly.sides`，否则 `FAIL`（改契约或改板，不改 CPL）：
  `python -c "import csv,sys;print(sorted({r['Layer'] for r in csv.DictReader(open(sys.argv[1],encoding='utf-8-sig'))}))" <板>-cpl.csv`

## 5. 贴片方向修正与预览

1. 先出一版不带覆盖的 CPL，上传到厂商贴片预览，逐个看极性件、IC 1 脚、连接器、SOT 类与底面件。
2. 把观察到的偏差写成本板的覆盖文件（放工程目录、进版本控制），位号条目优先于封装条目；封装键用 BOM `Footprint` 列的名字，
   带库前缀的 `Lib:Name` 也接受。
3. 重新运行 `kicad fab … --cpl-overrides <文件>`，核对终端每行 `cpl override: <位号> rotate+… dx+… dy+…`
   与 `fab-report.json` 的 `cplOverridesApplied`：**写了但没出现在这里的条目没有生效**（位号或封装名拼错时工具静默跳过，
   只有 JSON 键名拼错会被拒绝），逐条对账。
4. 再上传，在预览里确认修正后的方向；记录“已在厂商预览中确认”的位号清单。

本地预览只用于自查，不替代厂商预览：

```bash
kicad-cli pcb export svg --layers F.Fab,F.Courtyard,Edge.Cuts --mode-single --page-size-mode 2 \
  --exclude-drawing-sheet --sp --cdnp -o preview/top-fab.svg <board>.kicad_pcb
# 底面：--layers B.Fab,B.Courtyard,Edge.Cuts --mirror
```

`--sp` 画出焊盘轮廓与编号（看 1 脚），`--cdnp` 给 DNP 件打叉。

## 6. 验收记录模板

```markdown
# 制造输出验收：<板名> rev<X>（<日期>）
- 输入：<board>.kicad_pcb sha256=<…>，<root>.kicad_sch sha256=<…>；文件已在 KiCad 中关闭（无 .lck）
- 订单契约：fab/order-contract.json（来源获取日期 <…>）
- 证据绑定：板 / 原理图 / 有效规则 / intent / sim / connectivity 的 sha256，与签核运行（目录、时间）读到的一致：是 / 否（重跑了哪些门禁）
- 命令：pcbpilot kicad fab --pcb … --sch … --out <目录> [--cpl-overrides …]；pcbpilot 版本 <…>，KiCad <…>
| 项 | 结论 | 验证状态 | 证据 |
| ERC (`kicad sch-check`) | PASS/WARN/FAIL | live-verified | 报告路径、条数 |
| 预检（`kicad fab --preflight`） | 总判定 | live-verified | preflight.json：每项 status；fail/cannot-check 项逐条列出 |
| DRC + parity + 排除项 | … | … | preflight 的 `kicad-drc` / `kicad-drc-policy` / `schematic-parity`：错误 N（按 owner.class）、警告 N、waiver |
| 规则 vs 契约 | … | live-verified | preflight 的 `min-*` 项（含 `info` 出处）；契约四项以外的人工对照表 |
| LCSC / BOM | … | … | `lcsc --check` 输出、MPN 核对、逐 C 号需求量（板数 × 位数 + 损耗）与库存、查询日期 |
| Gerber/钻孔集合与叠层 | … | … | fab-report 层清单、gbrjob GeneralSpecs |
| Gerber 查看器 | … | manual | 用户观察记录 |
| CPL 方向 | … | manual | 预览确认的位号、覆盖文件与 cplOverridesApplied |
- waiver：<无 / 列表（用户签名）>
- 整体结论：可以下单 / 不可下单 / 未完成检查，不能确认可下单（缺：…）
```

整体结论按 evidence-and-verdicts 第 5 节取最差；`--no-assembly` 运行的 BOM/CPL 项写 `UNSUPPORTED（未出，光板订单）`。

## 7. 实测记录

2026-10-26，Windows 11，KiCad 10.0.6，pcbpilot v0.10.2 候选构建；输入均为 KiCad 自带 demo 的临时副本。

| 输入 | 运行 | 观察 |
|---|---|---|
| `pic_programmer`（2 层，全通孔） | `lcsc --check` | 63 封装、56 贴装件、56 缺号，退出 1 |
| 同上 | `fab --sch … --out` | 列出 56 个缺号件并给出 `--set` / DNP / `--no-assembly` 修法，退出 1，`--out` 为空 |
| 同上 | `fab --no-assembly` | 11 个文件的 zip（2 铜层 + 2 阻焊 + 2 丝印 + 2 锡膏 + 板框 + PTH/NPTH）；铜层文件名随板上自定义层名（`top_layer.gtl`）；两层锡膏只有文件头 |
| 同上 | 已有清单的目录再跑 | 只替换清单内文件，用户的 `notes.txt` 保留 |
| 同上 | `gerber/` 预置同名 `.gtl`（无清单） | 拒绝运行，提示移走或换目录，目录不变 |
| 同上 | `gerber/` 预置无关 `stale-In1_Cu.g2` | 运行成功、告警列出该文件、不进 zip |
| `openair-max`（作者已填大部分 C 号） | `lcsc --check` → `--set` 3 件 | 剩 1 件只在板上有的封装；补齐后 `fab` 报 6 条 `CPL_EMPTY_REF`（无位号的封装仍在坐标文件里），什么都不写；全程约 2.5 min |
| `royalblue54L_feather`（8 层，C 号齐全） | `kicad-cli pcb drc` | 6.5 min；283 条违例，其中 183 条 `drill_out_of_range`（盲孔孔径 0.00001 mm） |
| 同上 | `fab --sch … --out` | 19 s；17 个文件的 zip、BOM 29 行、CPL 48 件；`.gbrjob` 报 8 层 / 1.6 mm / ENIG；`PTH.drl` 刀具表出现 `T1C0.000`，即上面的盲孔——DRC 失败的板照样能出图，所以 DRC 必须单独跑 |
| 同上 | `--cpl-overrides`（位号 + 封装条目） | 每条生效的覆盖逐行打印并写入 `cplOverridesApplied`；不存在的位号/封装名不报错 |
| `interf_u`（2 层） | `fab --preflight --sch … --contract …`（契约 2 层、0.127/0.127/0.3/0.13 mm） | 12 s；FAIL：DRC 3 错（板框 1、布线 2）、`connection_width` 被 ignore、22 条一致性警告、环宽 0.05 < 0.13、24 件缺号；轮廓/层数/线宽/间距/孔径/贴片面 PASS；`--out` 与工程目录不变（2026-10-27，pcbpilot v0.11 开发构建） |
| `cm5_minima`（6 层，带排除项） | 同上（契约 4 层） | FAIL：层数 6≠4、`min_track_width` = 0 → min-track cannot-check、U702 局部间距 0.125 < 0.127、ENIG≠HASL、两面贴装≠top；5 条排除项中 courtyards_overlap 记 SILENCED |
| `vme-wren`（自带 `.kicad_dru`） | 只读 `.kicad_pro` 与 `.kicad_dru`（§2.1 的两条命令） | 网络类 `DDR4_CMD` 等线宽 0.08 mm、间距 0.1 mm；自定义规则 `clearance_under_fpga` 在 FPGA/DDR 下方区域另设孔径 0.2 mm、过孔 0.4 mm——只看 Board Setup 会漏掉这些更小的允许值 |
