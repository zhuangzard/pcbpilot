---
name: pcbpilot-kicad-schematic
description: "用 pcbpilot 离线绘制、修改与检查 KiCad 原理图：写设计 spec 一次 `kicad sch-build` 生成过门禁的工程（分区、分页、层次图、电源符号、PWR_FLAG），之后用 `kicad sch-edit` 增量修改、`kicad sch-read` 回读、`kicad sch-check` 做 ERC 与质量检查；也用于 EasyEDA 原理图转 KiCad、多单元器件（双运放、四门）与标准电路块。触发：“画 KiCad 原理图”“改原理图”“加一个上拉/去耦/LED”“ERC 报错”“电源未驱动 / PWR_FLAG”“原理图分页 / 层次图”“EasyEDA 原理图转 KiCad”。KiCad schematic capture with the pcbpilot CLI: write a design spec and build a gate-checked project in one `kicad sch-build` call (zones, pages, hierarchical sheets, power symbols, PWR_FLAGs), then change it with `kicad sch-edit` deltas, read it back with `kicad sch-read`, check it with `kicad sch-check` (ERC + drawing quality). Use for: draw or edit a KiCad schematic, add a pull-up / decoupling cap / LED, fix ERC errors or 'power input not driven', split a schematic into sheets, multi-unit op-amps and gates, standard circuit blocks, convert an EasyEDA schematic to KiCad. Never text-edits .kicad_* files."
license: MIT
metadata:
  author: "zhuangzard (pcbpilot); original easyeda-agent by zhoushoujianwork"
  version: "0.11.0"
  homepage: "https://github.com/zhuangzard/pcbpilot"
---

# pcbpilot-kicad-schematic

KiCad 原理图的工作流与决策层：AI 写设计数据（器件、网、电源轨、块、分页分区），确定性工具画图、跑门禁、留检查点。
spec 字段、门禁细节与 delta 语法以 [kicad-sch-build.md](../pcbpilot/references/kicad-sch-build.md)（及仓库
`docs/kicad/sch-build.md`）为准；本 Skill 只讲怎么走、怎么选、怎么报告。命令参数先 `--help` 核对。

## 何时用，何时交回 `pcbpilot`

- **用**：从需求画 KiCad 原理图；改 sch-build 生成的工程；读/查已有 `.kicad_sch`（ERC、质量、网表）；`.epro` 或
  EasyEDA 连接 IR 转 KiCad 原理图；原理图层面的 ERC 修复。
- **交回 router `pcbpilot`**：EasyEDA 现场原理图（`sch …` 命令）；PCB 布局/布线/铺铜（`kicad place` / `kicad route`）；
  JLC 制造文件；只读评审或“能不能下单”；选型与建符号/封装；仿真。交回时附上 `out` 目录、最后检查点号和 `connectivity.json`。

## 硬规则

1. **不文本编辑任何 `.kicad_*`**（含 sed、正则、手写 S-expression）。读用 `sch-read` / `netlist`，写用 `sch-build` /
   `sch-edit`；谁写哪些文件、锁文件与回退见 [kicad-file-safety.md](../pcbpilot/references/kicad-file-safety.md)。
2. **spec 不写坐标**。唯一的位置提示是 `near: "REF:PIN"`；排版问题靠分区/分页解决，不靠坐标。
3. **写前确认文件已在 KiCad 中关闭**（同目录无 `~*.lck`）；有锁 → `BLOCKED`，请用户关闭，不删锁文件。
4. **不绕门禁**：不用 `--no-planner` 绕 `SHEET_NOT_LOADED`；`--no-review` 只能配用户签名的 waiver，Agent 不自写 waiver；
   门禁失败时 `--out` 不变，失败工程在 `<out>/.pcbpilot/sch-build/failed/` 供查看。
5. **先认工程来源和基线**：`sch-edit` 只用于 sch-build 生成的工程（有 `.pcbpilot/sch-build/spec.json`），且每次写前确认文件
   与最新检查点一致；不一致（用户在 GUI 改过）→ 停，先并入新基线。手画或导入的工程默认只读 + `sch-check`，修改由用户在
   KiCad GUI 里做。判据、哈希比对命令与仅有的原地修复见 [kicad-file-safety.md §1 第 4 条](../pcbpilot/references/kicad-file-safety.md#1-四条规则)。

## 工作流

```bash
# 0 环境：KiCad 在默认位置之外（如 Windows 用户级安装）时设 PCBPILOT_KICAD_APP 或 PCBPILOT_KICAD_CLI
# 1 标准电路先查块，器件查 C 号
pcbpilot blocks search usb        # → pcbpilot blocks show block.usbc_dual_orientation_data
pcbpilot kicad lcsc --search "10k 0402"
# 2 写 spec（schema pcbpilot.kicad.sch-build/1），先看计划
pcbpilot kicad sch-build --spec design.json --out build/ --dry-run
# 3 构建：--render 每页导出 SVG 到 build/render；有需求文档就给 --requirements（review-panel 门禁）
pcbpilot kicad sch-build --spec design.json --out build/ --render --report build-report.json [--requirements 需求.md]
# 4 回读（≤40 件打到 stdout，更多写到 build/.pcbpilot/sch-build/sch-read.json）
pcbpilot kicad sch-read --project build/
# 5 每个修改一个 delta：先 dry-run 看哪些区重排，再执行；报告的 undo 给出回退命令
pcbpilot kicad sch-edit --project build/ --spec delta.json --dry-run
pcbpilot kicad sch-edit --project build/ --spec delta.json --render --report edit-report.json
pcbpilot kicad sch-checkpoint list --project build/       # 回退：sch-checkpoint restore N
# 6 构建后独立复查与交接（--review：只读设计评审；0 通过（含 pass-with-warnings）/ 6 有 error / 5 cannot-check）
pcbpilot kicad sch-check --sch build/<name>.kicad_sch --review --intent build/intent.json [--waivers waivers.json]
pcbpilot kicad netlist --sch build/<name>.kicad_sch --out conn.json
```

- 设计评审（`sch-check --review`，sch-build/sch-edit 默认 advisory 内置为 `sch-review` 门禁，`--sch-review strict`
  阻断）：连接（I2C 上拉、开漏复位/中断上拉、上拉电压域、LED 限流、浮空输入、多输出冲突）、电源（IC 无电源/地、
  每个电源脚的去耦电容集合（共用 = warning）、稳压器输入/输出电容、≥ 1 µF 总容量）、网络完整性（ERC
  `multiple_net_names` 合并网、单脚网、层次标签↔图纸引脚）。每条发现带 rule、fingerprint、severity、certainty
  （proven/likely/heuristic，只有 proven 是 error）、位号/脚/网/页、修法和证据；缺可选声明的发现降为 likely warning
  并列出 `missingEvidence`，cannot-check 只留给必需证据缺失或拓扑未决（跳线、开关、分叉、二极管极性不明）。
  push-pull 输出驱动的复位、声明了内部上拉的脚（spec `intent.review.internalPullups` 或符号字段 `InternalPullup`）、
  连接器（板外边界，只给 info）、PWR_FLAG（虚拟，不算源也不算负载）不会误报；MCU GPIO 方向未知（不算驱动，写
  `pushPullOutputs` 才算）；开漏线/浮空输入只有在 `intent.review.internalPullsComplete` 或 `noInternalPulls` 给出“无内部
  上下拉”证据时才判 error，否则 likely warning；passive/unspecified 脚不算接收端（除非写进 `inputPins`）；二极管/LED
  按阳极→阴极方向导通。上拉到器件自己的电源即通过，不同/未知电源域又无电压 → likely warning。稳压器电容只有 intent 的 ldo/buck/boost 块才判 proven error（只有
  power_out 脚的负载开关、电源切换只给 likely），SW 脚按 intent 声明的拓扑追输出（buck：SW→电感→输出；boost：SW→整流管→输出；
  没声明或追到输入端 → cannot-check，不拿输入电容充当输出电容）；每个指定地脚（GND/VSS/EP）都必须接地（打了 no-connect 也不例外，确属设计时写豁免）；每个
  power_in 脚都要单独接到电源（VDD1 接了不能替 VDD2 免检）；只有供电端
  没有地脚的器件（双轨运放）不报缺地；去耦按每个电源脚判，同一器件的 VDD1/VDD2 共用一颗电容也报共用 warning；
  LED 按每颗管芯（双色 GA/GK/RA/RK 等）两侧都必须拓扑可判，一侧有电阻、另一侧经晶体管或极性不明 → cannot-check；
  只接 LED 公共端的节点（共阳/共阴）直达电源轨或一个驱动脚时算已判端点，兄弟 LED 是独立支路；每颗管芯要有自己独占
  的串联电阻，分支前共用的电阻不算各自限流；驱动脚的总电流不检查。报告 `summary`：error 与 cannot-check 全列，warning 按检查项/缺失证据分组并按器件计数。
  LED 限流按每颗 LED 自己的电流路径追踪（轨上别处的电阻、并联支路的电阻都不算），节点分叉 → cannot-check。修法：在 spec 里补件（上拉、去耦、
  限流电阻）再 sch-edit；确属设计意图的写进 `intent.review` 角色；人工复核接受的写签名豁免（按 fingerprint）。
  不覆盖：晶振负载电容、启动/strap 脚、反馈分压值、ESR/容量充分性、上电时序/倒灌、瞬态保护。
- 一轮改完所有 spec 问题：失败报告一次列出全部（`PIN_TWO_NETS`、`PIN_NOT_ON_SYMBOL` 带符号真实脚号脚名…），不要逐条试。
- 首次构建要联网导入 LCSC 符号（实测 31 件 ~28 s 解析 + ~39 s 布局）；缓存命中后可加 `--offline`。
- 复现同一图：`--no-planner --fresh`（`--planner-timeout` 按墙钟，机器忙时选中的区会变）。
- 已有设计迁入：`kicad sch-import --epro X.epro --board B --out D [--pcb B.kicad_pcb]`（逐网对账，不过则失败），或
  `kicad sch-build --from-connectivity p1.json --from-connectivity p2.json --out D`（EasyEDA 连接 IR，每页一个文件）。

## 决策点

**导线 vs 标签 vs 电源符号**：spec 只决定网的“种类”，画法由工具定——`rails` 或 `kind: power/ground` 的网画成全局电源符号
（电源朝上、地朝下）；区内信号用短导线，区间用网络标签，跨页用层次标签 + 根图 sheet pin；信号不用全局标签（EG-01）。
所以：网名要有意义（不出现 `Net-(`、`_N3`，EG-20）；本该是电源的网一定进 `rails`（带电压/电流，下游 intent 用）。

**分区与分页**：一个 IC 簇（核心 + 专属外围）一个区，`core` 指核心件；按功能分 `pages`。页放不进 A0 会自动拆成
`<页>-2…`（区不拆）；单个区自己超过 A0 时会点名，要在 spec 里拆这个区。密集区质量/EG-06 失败可试 `--auto-split`（只在
问题变少时保留，更慢、可能多交叉）；首选仍是手动把区拆细。

**重复使用的子图（repeated sheets）**：sch-build 每页只生成一个实例，不生成多实例子图。读/查用户的多实例设计时注意：
子图里的电源符号与全局标签会把**所有实例**连在一起（供电可以，逐通道信号不行，必须走层次标签/sheet pin）；同一子图
各实例的位号不同，`sch-read` / `netlist` 按实例给出，评审按实例核对；不要在被复用的子图上做按位号的修改。

**多单元与派生符号**：
- v0.10.1：`extends` 派生符号（`Amplifier_Operational:LM358`、`74xx:74HC00`…）报 `SYMBOL_NOT_FOUND … extends another symbol`；
  改用父符号、LCSC C 号或 `pins` 生成方框符号。多单元符号只走网格方案，每个单元都放。
- v0.10.2 起：派生符号自动展平；一个件写一次，`REF:脚号` 可指任何单元的脚（含电源单元）；只放部分单元写
  `"units": [1, 3]`（未列单元的脚不能进网），`sch-read` 只对部分放置的件回写 `units`。带公共（unit 0）电源脚的件不能部分放置。
  逐件改单张图的 `sch modify --backend kicad` / `sch prim-delete --backend kicad` 用 `REF:UNIT` 指单个单元（见
  [kicad.md](../pcbpilot/references/kicad.md)）。
- 多单元件配去耦电容时 EG-06 可能失败（电源单元与电容被网格摆远，实测 47 mm）；改用单单元符号（C 号或 `pins`）再建。

**常见电路形态**——有块用块（`blocks show` 看 ports 再写 `bind`），没有就写成 parts + nets：

| 形态 | 写法 |
|---|---|
| 去耦 | 无通用块。每个电源脚一颗电容，`near: "U1:VDD"`，两端接该轨与 GND；工具会竖放成排、电源朝上，EG-06 要求 ≤ 25.4 mm |
| 上拉 / 下拉 | 电阻 `near` 被拉的 IC 脚，一端信号网、一端电源轨/GND；I2C、EN、BOOT 等先看 MCU 块是否已含 |
| 分压 | 两颗电阻，中点网命名（`FB`、`VBAT_SENSE`），不留自动名；DCDC 块（如 `block.sy8089_buck_3v3`）已含反馈分压 |
| LED 指示 | `block.led_indicator_gpio`，`bind` 它的 CTRL 端口到 GPIO 网 |
| RC（复位/EN、滤波） | MCU 块（如 `block.esp32s3_wroom1_module`）含 EN 上电 RC；其它写 R + C，节点网命名 |
| 晶振 | 无通用块。晶体 + 两颗负载电容到 GND，网 `XTAL_IN` / `XTAL_OUT`，电容 `near` 振荡器脚；负载电容值按数据手册 CL 算 |
| USB-C 设备（sink） | `block.usbc_dual_orientation_data`（CC 5.1k 下拉、双取向 D+/D-、ESD）；只取电源用 `block.usbc_ufp_power_or` |

**渲染与基线对比**：`--render` 的 SVG（已规范化，同一 spec 两次构建逐字节相同）每次原地覆盖 `<out>/render/`；先把上一版
`render/` 和 `sch-read` 输出复制到工作区外，改完后 `pcbpilot kicad render --out <out>/render --compare <旧 render>` 逐页给
identical / changed（变化元素数）/ added / removed（[kicad.md](../pcbpilot/references/kicad.md) 的 `kicad render` 节）。连接是否变化以两份
`sch-read` JSON 的差异（parts/nets/noConnect）为证据，SVG 只用来发现排版回退（标签压线、区位置跳动），不单独给 PASS。

## 报告

结论词汇、证据强弱与汇总规则用 [evidence-and-verdicts.md](../pcbpilot/references/evidence-and-verdicts.md)（第 4 节有
`kicad sch-build` / `sch-check` 的换算），ERC/质量项分类看 [drc-triage.md](../pcbpilot/references/drc-triage.md)。每次报告写：

- 命令全参数、spec 路径、`--out`、检查点号 N、`totalMs`；报告 `gates[]` 逐项状态（netlist / erc / page-fit / quality /
  engineer-grade / connectivity / intent / sch-review / design-review）和 `warnings`；`sch-review` 为 warn 时列
  `sch-review.json` 的 error、cannot-check 与 likely warning 项（含 missingEvidence），`pass-with-warnings` 写 WARN，不写“通过”。
- ERC 只有警告 → `WARN`（列 `byType`），不写“通过”；`design-review` 为 skip（没给 `--requirements`）→ 需求覆盖 `INCOMPLETE`。
- 构建后状态：`fp-lib-table`、`lcsc.pretty/` 存在且非空、`render/` 文件时间是本次；没看渲染就不说“排版已确认”。

## 常见失败 → 修法

| 现象 | 原因 | 修法 |
|---|---|---|
| `ok:false`，`BUILD_FAILED`，消息 “N spec problem(s): PIN_TWO_NETS …” | spec 自相矛盾（一脚两网、脚名不存在、网无脚） | 按列表一次改完再建；`PIN_NOT_ON_SYMBOL` 用它给出的真实脚名 |
| `GATE_FAILED`：`engineer-grade … EG-06` | 去耦电容离所服务电源脚 > 25.4 mm | 给电容 `near` 电源脚；拆区；多单元件见上文；不要删电容过门禁 |
| `SHEET_NOT_LOADED` | value / MPN 等有 KiCad 不接受的字符 | 改 spec 字符；不是布局问题 |
| `page … split into N pages` / 某区 “too big” | 页或区超过 A0 | 前者是正常自动分页；后者在 spec 里拆区 |
| ERC `pin_to_pin`（Unspecified 引脚） | EasyEDA 转来的符号引脚类型不可信 | 记 WARN 并列出；不为消警告改引脚类型 |
| ERC `power_pin_not_driven`（非 sch-build 工程） | 电源来自连接器/被动脚器件 | 备份后 `kicad sch-check --sch … --fix-pwr-flag`；先确认确有电源来源 |
| ERC `lib_symbol_issues` / `footprint_link_issues` 只指向 `Device`、`Resistor_SMD` 等自带库 | ERC 用隔离的空全局库表 | 环境类，WARN 并注明；`PCBPILOT_KICAD_ISOLATE=0` 可用用户配置复核 |
| `sch-edit` 后 `fp-lib-table` 消失、`lcsc.pretty/` 为空，ERC 出现大量 `footprint_link_issues … 'lcsc'` | v0.10.1 缺陷：编辑时 LCSC 件的封装文件不再复制 | 判 FAIL；把修改并回原始 spec，先做第 5 条的基线比对，再 `kicad sch-build --spec <原始+修改> --out <同一目录>` 重建（区位置复用），再查这两项 |
| `extends another symbol` | v0.10.1 不支持派生符号 | 父符号 / C 号 / `pins`；v0.10.2 起自动展平 |
| `sch-fit` 报 “too big — split the sheet” | 单页放不下 A0 | 拆页；`sch-fit` 先 `--dry-run` |
| kicad-cli 退出码 137 或长时间卡住 | KiCad 安装/升级中被杀；全局库表过大 | 等安装完；保持默认隔离配置 |
