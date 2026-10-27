---
name: pcbpilot-fab
description: "用 pcbpilot 为 KiCad 工程出制造与贴片文件并验收：出 Gerber、钻孔、BOM、CPL/坐标文件，嘉立创（JLC）下单、PCBA 贴片、光板打样，LCSC C 号检查与补齐、贴片方向修正、订单契约（层数/板厚/表面处理/阻抗/贴片面）与最终验收记录。EasyEDA 工程没有 pcbpilot 制造输出路径，本 Skill 会说明并改道。Fabrication and assembly output for KiCad boards via pcbpilot: export Gerber + drill zip, BOM and CPL (pick-and-place / centroid) for a JLCPCB PCB or PCBA order, check and fill LCSC part numbers, correct CPL rotations, record the order contract (stackup, finish, impedance, assembly sides) and write the final go / no-go acceptance record. Use for “出 Gerber”, “下单”, “JLC/嘉立创”, “PCBA”, “贴片”, “BOM”, “CPL/坐标文件”, “drill”, “export gerbers”, “ready to order”."
license: MIT
metadata:
  author: "zhuangzard (pcbpilot); original easyeda-agent by zhoushoujianwork"
  version: "0.10.2"
  homepage: "https://github.com/zhuangzard/pcbpilot"
---

# pcbpilot-fab

把一块**已完成**的 KiCad 板变成可上传的制造文件，并给出有范围、有证据的下单结论。命令一律先 `--help` 核对；
结论词汇（`PASS` / `WARN` / `FAIL` / `INCOMPLETE` / `UNSUPPORTED` / `BLOCKED`）与验证状态按
[evidence-and-verdicts.md](../pcbpilot/references/evidence-and-verdicts.md) 使用，不自造说法。

## 何时用，何时交回 router

- 用：出 Gerber/钻孔/BOM/CPL、补 LCSC C 号、改贴片方向、判断“文件能不能上传/能不能下单”。
- 交回 `pcbpilot`：要改原理图、布局、布线、铺铜或规则（制造检查发现的问题由 router 的设计流程修，修完再回来）；
  只做设计评审而不出文件（走 [review-workflow.md](../pcbpilot/references/review-workflow.md)）；选型与建库（含 `kicad lcsc --import`）。
- **EasyEDA 工程**：pcbpilot 没有 EasyEDA 制造输出路径（只有 `bom export`、`project export`、`pcb export-dsn`）。告诉用户：
  Gerber/钻孔/坐标在 EasyEDA 编辑器里自行导出；或在 KiCad 里有了 `.kicad_pcb` 之后走本 Skill（`kicad sch-import` 只转原理图，
  不产生板子）。制造文件项记 `UNSUPPORTED`，不用 GUI 代做。

## 硬规则

1. 不手工编辑 `.kicad_*`；`kicad lcsc --set` 是唯一由本 Skill 运行的原地写入，运行前确认文件已在 KiCad 中关闭、
   工程已提交 git 或已复制备份（[kicad-file-safety.md](../pcbpilot/references/kicad-file-safety.md)）。
2. 订单契约先于出图；契约里的每个数值带来源 URL 与获取日期。没有契约就不写“可以下单”。
3. `kicad fab` 出图不跑 DRC/ERC；出图前用只读的 `kicad fab --preflight`（DRC + 制造检查）与 `kicad sch-check`（ERC），各报各的范围。
4. 每次出图用新目录或只由 pcbpilot 清单管理的目录；不把旧文件、别的工具的输出混进上传包。
5. 不为通过门禁乱填 C 号；不该贴的件在 KiCad 里标 DNP / 排除 BOM / 排除坐标文件（用户操作），不是填号。
6. CPL 方向覆盖只来自本板在厂商预览里的观察；pcbpilot 不带、也不要编一张按封装的角度表。
7. 结论只建立在本次运行的新鲜文件上；Gerber 查看器与贴片预览是 manual 项，没有用户的观察就是 `INCOMPLETE`。

## 工作流

**0. 确认输入。** 板文件 `<board>.kicad_pcb`、根原理图 `<root>.kicad_sch`、厂商与服务（PCB / PCBA）、数量。
查锁文件：`ls -a <工程目录> | grep '\.lck$'`；有锁 → `BLOCKED`，请用户关闭 KiCad 中的工程。

**1. 订单契约。** 与用户逐项确认服务、层数、板厚、铜厚、表面处理、阻焊/丝印色、是否阻抗控制（叠层编号）、贴片面、
过孔工艺、工艺极限，写成 `fab/order-contract.json`（模板见 [fab-acceptance.md §1](references/fab-acceptance.md#1-订单契约)）。
用户未定的项直接问；参考资料里的厂商数字没有日期就当过期。

**2. 预检（只读，一条命令一个结论）。**

```bash
pcbpilot kicad sch-check --sch <root>.kicad_sch                                  # ERC，预检不含
pcbpilot kicad fab --preflight --pcb <board>.kicad_pcb --sch <root>.kicad_sch   --contract fab/order-contract.json [--waivers <签名 waiver>] [--no-assembly] > <评审目录>/preflight.json
```

`--preflight` 不写任何文件（板和原理图在临时副本上查），每项 `pass` / `fail` / `cannot-check`，总判定取最差，退出码 0 / 6 / 5：
`kicad-drc`（错误级违例，带归属：封装/布局/布线/板框/原理图一致性/环境）、`kicad-drc-policy`（排除项与被关掉的规则，触及电气/制造规则
且无签名 waiver → fail）、`schematic-parity`、`board-outline`、`contract`、`layer-count`、`min-track`/`min-space`/`min-drill`/`min-annular`
（有效规则集含网络类、`.kicad_dru`、局部覆盖 vs 契约）、`finish`、`assembly-sides`、`lcsc`（覆盖率）、`bom-cpl`。映射：fail → `FAIL`，交回
router 修板（归属与修法见 [drc-triage.md](../pcbpilot/references/drc-triage.md)，改契约凑数不算修）；cannot-check → `INCOMPLETE`，按 detail 的
`fix` 补（缺契约、规则 0/读不懂、DRC 没跑成）。大板 DRC 要几分钟，等它完成。命令不覆盖的项（钻孔表、gbrjob、查看器、方向）见
[§2](references/fab-acceptance.md#2-预检的范围)。修了板以后可以 `pcbpilot kicad drc --pcb <新板> --baseline <评审目录>/preflight.json` 只看新增。

**3. 补 LCSC C 号（PCBA）。** 对每个缺号件：

```bash
pcbpilot kicad lcsc --search "<MPN 或 值+封装>" --qty <需求量>      # --offline 只查内置常用料
pcbpilot kicad lcsc --pcb <board>.kicad_pcb --sch <root>.kicad_sch --set R1=C25744 --set C1=C1525,C2=C1525
pcbpilot kicad lcsc --pcb <board>.kicad_pcb --sch <root>.kicad_sch --check     # 直到 0 missing / 0 invalid
```

`--qty` 是**这个 C 号**的需求量 = 板数 × 每板贴装位数（BOM 里该 C 号的位号数）+ 厂商损耗/备料，拼板时板数按拼板数取整后的
实际贴装板数（算法见 [§4](references/fab-acceptance.md#4-bom-与-cpl-审核)）。候选的 MPN 与原理图完全一致才能直接写；只是兼容/替代型号时先给用户确认。安装孔、Mark、测试点、Logo、PCB 保险丝这类
不贴的封装，请用户在 KiCad 里勾排除属性。符号/封装要从 C 号建库属于库任务，交回 router。

**4. 出图。**

```bash
pcbpilot kicad fab --pcb <board>.kicad_pcb --sch <root>.kicad_sch --out <新目录>                # PCBA
pcbpilot kicad fab --pcb <board>.kicad_pcb --out <新目录> --no-assembly                         # 光板
```

v0.10.2 起的行为（细节见 [kicad-file-safety.md §6](../pcbpilot/references/kicad-file-safety.md#6-制造输出目录与清单)）：
先在暂存目录生成全部文件，成功后事务式发布并写 `.pcbpilot-fab-manifest.json`；目标文件名已存在而不在清单里 → 拒绝、什么都不改；
缺 C 号或 BOM/CPL 位号不一致（`CPL_NOT_IN_BOM`、`BOM_NOT_IN_CPL`、`CPL_DUPLICATE_REF`、`BOM_DUPLICATE_REF`、`CPL_EMPTY_REF`）
→ 非零退出、什么都不写，每条带 `fix`；DNP / 不贴的件从 CPL 去掉并列在 `cplOmitted`。失败时 `--out` 为空或保持原样，不要当成部分完成。

**5. 贴片方向（PCBA）。** 首版 CPL 上传厂商预览 → 记录偏转的位号 → 写本板的覆盖文件 →
`kicad fab … --cpl-overrides <file>` → 逐条对账 `cplOverridesApplied`（拼错的位号/封装名会被静默跳过）→ 再看预览。
本地自查可用 fab 层 SVG（[§5](references/fab-acceptance.md#5-贴片方向修正与预览)），它不替代厂商预览。

**6. 逐文件验收。** 按 [§3](references/fab-acceptance.md#3-逐文件验收)：文件新鲜且非空、`fab-report.json` 层清单与契约层数一致、
`*-job.gbrjob` 的层数/板厚/表面处理与契约一致、PTH/NPTH 分开且刀具表合理、无残留告警；BOM 逐行核 MPN/封装/库存，
`cplOmitted` 逐个确认（[§4](references/fab-acceptance.md#4-bom-与-cpl-审核)）。请用户在 Gerber 查看器或厂商在线预览逐层看并回报。

**7. 验收记录。** 按 [§6 模板](references/fab-acceptance.md#6-验收记录模板) 写一份记录（建议 `fab/acceptance-<日期>.md`，放在 `--out` 之外），
并与 [prefab-checklist.md](../pcbpilot/references/prefab-checklist.md) 第 0、8、9 节对齐。

## 决策点

| 情况 | 做法 |
|---|---|
| 用户只要打样光板 / 自己焊 | `--no-assembly`；BOM/CPL 项记 `UNSUPPORTED（光板订单）`，不跳过 DRC 与契约 |
| 缺号件很多（demo、未选型的板） | 先问是 PCBA 还是光板；PCBA 时按件补号，不批量猜 |
| 件不该贴却被要求 C 号 | 请用户在 KiCad 勾 DNP 或排除 BOM + 坐标文件，再 `--check` |
| 同目录再出一版 | 可以复用清单管理的目录；目录里有不是 pcbpilot 写的同名文件 → 换新目录 |
| 板上有盲埋孔、微孔、盘中孔、半孔 | 契约必须写明工艺；否则 `FAIL`，交回 router 或请用户选服务 |
| 阻抗控制网 | 叠层取自契约的厂商叠层编号，线宽/间距核对属 router 的布线任务；未核对 → `INCOMPLETE` |
| 贴片面 | 契约 `assembly.sides` 只写 `top` / `bottom` / `both`；与 CPL `Layer` 列实际出现的面集合（`Top`、`Bottom`）比对，必须相等。只有两面都有贴装件时才写 `both`；不一致 → `FAIL`。底面件方向在预览里逐个看 |
| 有 `kicad route` 运行目录 | 先按 [§6.5](../pcbpilot/references/evidence-and-verdicts.md#65-运行证据绑定到确切输入) 比对哈希：板、原理图、有效规则、intent、sim 与这次出图的输入一致，才 `signoff --run-dir <dir> …`；不一致的门禁作废重跑，绑定表写进验收记录 |
| 没有运行目录（手工布线） | 显式输入签核：`kicad snapshot` 出图用的板 → `signoff --board … --intent … --sim … --connectivity …`，新鲜度与缺输入写法见 [review-workflow.md pre-fab](../pcbpilot/references/review-workflow.md#pre-fab能否投板)；不跑就是 `INCOMPLETE` |

## 报告

每项一行：检查、命令（完整参数）、输入文件与时间/哈希、结论、验证状态、发现数与对象。整体只用三种说法：
**可以下单**（每个必需项 PASS，含签名 waiver）/ **不可下单**（任一 FAIL）/ **未完成检查，不能确认可下单**（有 INCOMPLETE、
BLOCKED 或 manual 项无证据，列出缺什么）。上传清单只列 `<板>-gerber.zip`、`<板>-bom.csv`、`<板>-cpl.csv`。

## 常见失败 → 修法

| 现象 | 原因 | 修法 |
|---|---|---|
| `N placed part(s) without an LCSC part number`，`--out` 为空 | 贴装件缺号 | 第 3 步补号，或排除不贴件，或 `--no-assembly` |
| `CPL_EMPTY_REF` | 没有位号的封装（Logo、焊盘图形）仍在坐标文件里 | 用户在 KiCad 给它勾“排除坐标文件”，或标注位号后从原理图更新 PCB |
| `CPL_NOT_IN_BOM`：excluded from the BOM but not from position files | 只排除了 BOM | 同时勾排除坐标文件，或取消排除 BOM |
| `CPL_NOT_IN_BOM`：DNP in the schematic but not on the board | 原理图与板不同步 | 用户在 KiCad 里 Update PCB from Schematic，再出图 |
| `*_DUPLICATE_REF` | 位号重复 | 原理图重新标注 → 更新 PCB |
| `would overwrite files pcbpilot does not own` | 目标目录有别的来源的同名文件 | 换新 `--out`，或由用户移走那些文件 |
| 告警 `holds Gerber/drill file(s) this run did not produce` | 旧版本或别的工具的残留 | 不进 zip，但让用户删掉或换目录，免得误传 |
| `unknown field "…"`（覆盖文件） | 覆盖 JSON 键名拼错 | 只允许 `refs` / `footprints` 与 `rotate` / `dx` / `dy` |
| 覆盖写了却不在 `cplOverridesApplied` | 位号或封装名不匹配（静默跳过） | 用 CPL 的位号、BOM 的 `Footprint` 名或 `Lib:Name` 重写 |
| `--search` 首条 MPN 带后缀或换了前缀 | 目录里没有原型号，排名给了替代件 | 不直接写；换查询或请用户确认替代 |
| `--preflight` 的 `min-track` 等为 cannot-check：`… = 0 (no constraint)` | Board Setup 没设该最小值，也没有别的规则约束它 | 请用户在 Board Setup 填契约值（或 `.kicad_dru`），重跑；不当作 PASS |
| `kicad-drc-policy` fail：`IGNORED connection_width` / `DOWNGRADED solder_mask_bridge` | 工程把电气/制造检查关掉或降级 | 用户恢复成 error 后重跑，或逐条签名 waiver（`{"gate":"kicad-drc",…}`） |
| `contract` fail：`CONTRACT_INVALID` | 键名拼错、缺来源 URL/日期、`assembly.sides` 不是 top/bottom/both | 按 [§1](references/fab-acceptance.md#1-订单契约) 补齐；数值重新从厂商页面取 |
| DRC 报 `drill_out_of_range`，钻孔刀具表有 `C0.000` | 板上过孔/孔径定义错误 | `FAIL`，交回 router 修板；`kicad fab` 照样能出图，不能当证据 |
| 锡膏层文件只有文件头 | 该面没有 SMD 焊盘 | 正常；铜层或板框只有头才是错误 |
| `kicad-cli` 退出码 137 或长时间无输出 | KiCad 正在安装/升级，或板很大 | 等安装完成；大板 DRC 等几分钟，不当作成功 |
| 有 `~<file>.lck` | KiCad 打开着该文件 | `BLOCKED`；请用户关闭，不自己删锁 |
