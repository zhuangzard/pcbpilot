# 板卡使用说明书（`report manual` 与 `board-manual` 门禁）

给软件、调试和测试工程师看的板卡使用说明书：**一个**自包含 HTML（内联 CSS 与 SVG，无外部请求，A4 可打印）。
从文件离线生成，不访问 daemon 或编辑器。模板规范源
[templates/board-manual/manual.html.tmpl](../templates/board-manual/manual.html.tmpl)；Go 包 `pkg/boardmanual`
用 `go:embed` 嵌入副本，`TestTemplateMatchesSkill` 要求两份逐字节相同（改模板后
`cp .agents/skills/pcbpilot/templates/board-manual/manual.html.tmpl pkg/boardmanual/templates/`）。
状态：`offline-verified`（Gas Module V5 Board A：gate-A-final 板数据，`board-manual` PASS）。

## 硬要求：每次设计更新自动重生成

- `pcb auto route` 与 `pcb gate` 在质量门禁的最后**总是**生成说明书，并把 `board-manual` 写进 `summary.json`
  / `gate.json` 的 `gates[]`；它失败，整次运行失败。
- 输入：门禁刚取的 `board-final.json`、`--intent`、`--sim`；`pcbpilot.project.json` 的 `manual` 块给 notes、
  引脚表、名称和额外副本（路径相对工作目录）：

  ```json
  {"schemaVersion": 1, "name": "GasModule_V5_A",
   "manual": {"notes": "pcbpilot.manual-notes.json", "pinMap": "../fpga/board_pins.tcl",
              "analog": "../sim/analog.json", "name": "GasModule_V5_A", "out": "../05_Output/GasModule_V5_A_使用说明.html",
              "doc": "1eb633d9222155fb", "lang": "zh"}}
  ```

  `notes` 缺省为 `<工作目录>/pcbpilot.manual-notes.json`；`--project-config` 指定文件/目录，`none` 不读。
- 输出：`<out-dir>/manual/{<Board>_使用说明.html（当前版）, vN/<Board>_使用说明.html, index.json}`，配置了
  `manual.out` 时再复制一份——**只在** `manual.doc` 等于本次板子的 PCB 文档 uuid（`--doc`）时写；`doc` 为空、
  未给 `--doc` 或不一致时不写并记为门禁失败项（2026-10-06：B 板跑时读了 A 的配置，覆盖了 A 的已发布说明书）。
  同一工作目录的第二块板用 `--project-config pcbpilot.project.B.json`：给文件就读该文件本身。
- `--no-manual` 只在 `--waivers` 里有签名条目 `{"gate":"board-manual","match":"--no-manual","reason":…,"by":…}`
  时才允许；否则命令直接拒绝。
- 离线复跑（不连 EDA）：

  ```
  pcbpilot report manual --board gate/board-final.json --intent intent.json --sim sim.json --post gate/post.json \
      --out-dir gate/ [--project-config <工作目录>] [--project-name P --doc-name PCB1] --gate
  ```

  `--gate` 时门禁不通过返回非零。只要一份临时 HTML 时用 `--out file.html`（不版本化、不判门禁）。

## 版本

与 `report design` 一致：首页写 vN、板数据 sha256（dump 的 `semanticSha256`，没有则文件 sha256）、pcbpilot
版本、日期、工程/文档。旧版本保留在 `vN/`。**板数据 sha256 不变时不升版本**，原地重建当前 vN（变更记录仍相对
上一版）。最后一章“变更记录”自动比较上一版：接口增删、逐脚网络变化、名称、LED（名称/阳极网络）、电源输入与
各电源轨数值。`index.json` 每版记录快照、门禁结果与变更列表。`report design --manual <html>` 把当前说明书放进
设计报告包 `vN/manual/` 并在封面链接。

## `board-manual` 门禁（全部满足才 PASS）

| 检查 | 失败条件 |
|---|---|
| notes | 说明文件不存在 |
| 接口用途 | 任一板上接口缺 `name`、是什么 `purpose`、接什么 `connectsTo`、作用 `systemRole`、什么时候用/怎么用 `usage[]`、注意 `cautions[]` |
| 逐脚说明 | 任一接口焊盘没有 `pinNotes`（器件与作用） |
| LED | 任一 LED 没有含义（`hardware`，或 `firmwares[]` 里每个固件都有 `modes`） |
| 电源输入 | 缺 `power.input.connector` / `polarity` / `recommendedSupply`，或接口不在板上 |
| 仿真 | 缺 post.json（`sim post-layout`），或 post.json 的板数据 sha256（`inputs.boardSemanticSha256`）≠ 说明书所用板数据：说明书必须显示同一份铜皮的仿真 |
| 机械 | 板框缺失、尺寸图缺失、没有安装孔、板厚未知（`mechanical.thicknessMm` 或 intent 叠层）、没有带来源的器件高度 |
| TODO | 计算后的说明书任何字段仍含 “TODO”（只有 `openItems[]` 可以写未决项） |
| 对账 | notes 里的接口/脚/网络/LED 阳极/测量点/引脚表与板数据不一致（`docPins` 文档网络 ≠ 板上网络、`expectedPins` ≠ 焊盘数……） |
| 交叉探查（KiCad） | 给了 `--kicad-sch/--kicad-pcb`（`kicad route --sch` 自动传入）时：任一 BOM 器件只在一侧（PCB 上有封装无原理图符号，或原理图 `in_bom`+`on_board` 符号无封装），或某原理图页没有出图。`exclude_from_bom` / `board_only` 封装与没有铜焊盘的封装（仅 NPTH 的安装孔）不需要符号 |
| 生成 | 读入、生成或写文件失败 |

对账失败时**改错的一方**：文档写错就改文档（例 J_AUX 写 AGND，本板只有 GND），板子错就回到设计；不能删
`docPins` 或把文档网络改成板上网络来“通过”。

## 章节

1 概览（使用顺序）· 2 机械尺寸与安装孔 · 3 仿真结论 / 工作条件 · 4 接头位置图（KiCad 工程另含原理图 ↔ PCB 对照）· 4 接口详细说明 · 5 电源要求 · 6 输入输出 · 7 LED ·
8 注意事项与跳线 · 9 仪器设备 · 10 测量点 · 11 上电步骤 · 12 软件接口 · 13 故障排查 · 14 文档与板数据对账 ·
15 未决项 · 16 数据来源 · 17 变更记录。

- **机械尺寸与安装孔**（全部来自 dump）：尺寸图按真实 mm 绘制（SVG `width/height` 为 mm，A4 放得下即 1:1，
  否则写出比例），原点 = 板左下角、X 向右、Y 向上；板尺寸、板角圆角/倒角、每个安装孔距原点 X/Y、孔距、
  每个接头本体到最近板边的距离或伸出量；10 mm 比例尺。孔表（位号、功能、X/Y、成品孔径、焊盘/环、金属化、
  网络、螺丝头/铜柱禁布 Ø（来自 no-components 禁布区）、建议螺丝 M2/M2.5/M3/M4/M5）、过孔类型统计、最细走线
  与最小间距、接头位置表（1 脚与本体中心 X/Y、旋转、最近板边、距边、伸出、插拔方向）、器件高度（`mechanical.heights[]`，
  必须写来源）。尺寸图 SVG 与孔表/接头表 CSV 可直接下载。安装孔识别：无主 footprint 孔、`H*`/`MH*` 器件、
  或无网络的多层（layer 12）圆形填充 Ø1.5–8 mm。
- **仿真结论 / 工作条件**（`--post`，门禁自动传入；`--analog` 或 `manual.analog` 可选）：每项一行结论（温度/压降/
  过孔/功率/模拟前端）；工作条件明说（环境 25 °C、裸板静止空气、无外壳）；各场景最高板温与位置/器件、各层最高/平均、
  板温最高 10 个器件、内嵌 temp-TOP/BOTTOM 热图；允许最高环境温度 = 环境 + (限值 − 最高温)（板温上限或已知结温上限中
  较小者，假设损耗不随温度变化），缺 θJB/θJC 或 Tj,max 的器件逐个列出；每条电源轨压降 vs 预算（含 4 个阀门漏极网络）；
  利用率最高 5 个过孔；输入功率、板外负载（阀门）、板上损耗与推荐电源；滤波通道 fc/Q/增益 vs 目标。
- **接头位置图**：方形画布；接头按角色着色并标“位号 角色 名称”+ 副标题，引线到最近板边外，1 脚白点，`partLabels`
  标注主要器件，底边 LED 行在板下方标注；角色无 notes 时按网络推断（TCK/TMS/TDI/TDO → JTAG，CANH/CANL → CAN，
  TXD/RXD → UART，只有 VIN/+nV 与 GND → POWER，2 脚 → JUMPER）。
- **原理图 ↔ PCB 对照（交叉探查，第 4 章 `#s4x`，仅 KiCad 工程）**：`report manual --kicad-sch root.kicad_sch
  --kicad-pcb board.kicad_pcb`（`--board` 必须是这块 .kicad_pcb 的 `kicad snapshot`）；`kicad route --sch` 在门禁里
  自动传入原理图与布好的板。kicad-cli 按页面坐标出图（1 单位 = 1 mm，原点 = 页面左上角，不画图框）：原理图每个
  层次页一张，PCB 每层一张（B.Cu、F.Cu、F.Silkscreen、Edge.Cuts，黑白出图后在页面里按层着色），以 base64
  data URI 嵌入，仍是单个离线 HTML（约 1–8 MB）。位号表（JSON）`ref → {schPage, schBoxes[], schRot, pcbBox,
  pcbRot, rotation, nets[], findings[], lcsc, footprint, side}`：符号框 = 库符号本体+引脚经实例旋转/镜像（多单元
  器件每单元一框），封装框 = 快照 bbox（mil、y 上 → mm、y 下）；`rotation = pcbRot − schRot`，让放大镜里的封装
  与符号同向。每个网络带意图线宽（outer/min）、电流、post.json 的 IR 压降/预算/状态；`findings` 是 intent
  （安全/爬电/额定值……）与 post-layout 中点名该器件、或不点名器件但点名其网络的发现。
  交互：鼠标移到原理图器件 → 放大镜（默认圆形）显示该器件 PCB 区域（铜 + 丝印），虚线贝塞尔连到符号，两侧同时
  高亮；移到 PCB 器件 → 反向显示原理图符号（必要时自动翻页）。单击固定，拖动放大镜移位，滚轮缩放（Shift+滚轮
  15° 旋转），R / Shift+R 旋转 90°，O 圆形/矩形，+/- 缩放，Esc 关闭；两个视图可滚轮缩放、拖动平移，PCB 层可开关。
  打印时隐藏放大镜。表格“已对照器件”列出全部位号（无法对照的标红）。
- **接口页**：焊盘按真实位置与形状绘制（电源红、地黑、信号蓝、空脚灰，橙圈 = 1 脚），脚表含网络/类型/电压/最大电流/
  方向/文档网络/说明；`highlight` 显示为高亮说明框。

## 写作标准（以 Gas Module V5 Board A 为样例）

样例：`GasControl_PCB/04_Code/hardware/pcb/pcbpilot.manual-notes.json`，J6 引脚表文字来自
`03_Requirement/CONNECTOR_PINOUT.md`。

1. **每个接口六问**：是什么（`purpose`）/ 接什么设备（`connectsTo`）/ 在系统中的作用（`systemRole`）/
   什么时候用、怎么用（`usage[]` 按步骤）/ 注意（`cautions[]`）。例：J1“整板唯一的电源入口 / 12 V 适配器 5.5×2.1
   中心正 / 12 V 直驱阀门并产生 5 V、3.3 V、1.8 V、15 V / 首次上电限流 0.3 A，D3 亮 = 12 V 进板”。
2. **长得像的接口写“不是…”**：J3 是 J4 的 120 Ω 终端跳线，**不是第二个 CAN 口、不接线**；用 `highlight` 写清：
   总线两端各一个 120 Ω、中间节点不插，断电量 J4.1–J4.2：只插本板 ≈ 120 Ω，两端都插 ≈ 60 Ω。J8 是扩展口，
   3、4 脚只在 01_bringup 时兼作阀门测试跳线；相邻的 +3V3/GND 脚不要插帽。
3. **逐脚“器件 + 作用”**：J6 每脚写接哪个阀/传感器、它干什么（“SV1 进气阀低边，接 Q2 漏极，PURGE 和 RUN 时开”），
   电气要求（每阀 0.34 A、四阀 1.37 A、10.8–13.2 V），以及插拔、取电、标定占位等注意。`docPins` 抄设计文档原文，
   供逐脚对账。
4. **上电步骤每项有合格判据**：`bringup[].checks[]` 写 检查项 / 测量位置 / 期望值 / 应看到（LED、终端）/ 合格 /
   不合格时怎么办；顺序为空板电阻 → 限流上电 → 电源轨 → JTAG 与 01_bringup → 假负载阀门测试 → 正式固件 →
   最后才接真实负载。
5. **软件接口**：串口参数、命令表（字符/作用/允许状态/应答码）、遥测行逐字段、CAN/I2C 等未实现的接口明确写“固件未实现”。
6. **LED 表**：每个 LED 写颜色、阳极网络（会与板数据对账）、每个固件下的含义；硬件灯写 `hardware`。
7. **故障排查**：现象 → 可能原因 → 量什么（例“D3 不亮 → 无 12 V / F1 断 / 反接被 Q1 阻断 → 量 J1.1、F1.2、C1.1”）。
8. **只写真实值**：数据来自设计文档、仿真、BOM、固件源码或数据手册并注明来源；不知道的写进 `openItems[]`，
   不写 TODO、不猜。

## notes.json 字段

未知字段报错。所有字段可选（门禁要求的除外）。

```jsonc
{
  "title": "…", "subtitle": "…", "revision": "…", "overview": "…",
  "sequence": ["使用顺序第 1 步", "…"], "sources": ["写 notes 时依据的文档"],
  "partLabels": {"U8": "CPLD"},
  "mechanical": {"thicknessMm": 1.6, "heights": [{"ref": "C1", "side": "top", "heightMm": 10.2, "source": "LCSC C3340"}],
                 "mountingHole": {"function": "安装孔", "plated": "…", "screw": "M3"},
                 "mating": {"J6": "线束从右边水平插入"}, "notes": ["…"]},
  "power": {
    "input": {"connector": "J1", "voltageV": 12, "voltageRange": "…", "currentA_typ": 1.4, "currentA_max": 1.5,
              "idleCurrent": "…", "recommendedSupply": "…", "plug": "…", "polarity": "…", "protection": "…"},
    "margin": 1.5, "notes": ["…"],
    "rails": {"+5V": {"probe": "C9.1", "tolerance": "4.98 V ± 2 %", "expected": "…", "note": "…"}}
  },
  "connectors": {"J6": {
    "name": "气路接头", "role": "GAS", "subtitle": "位置图副标题",
    "purpose": "…", "connectsTo": "…", "systemRole": "…", "usage": ["…"], "highlight": "高亮说明框（\n 换行）",
    "mating": "…", "part": "…", "expectedPins": 20, "source": "…",
    "pinNotes": {"1": "…"}, "cautions": ["…"],
    "docPins": {"1": {"name": "SV3-S", "net": "SV3_DRV"}}
  }},
  "cautions": ["…"],
  "io": [{"signal": "…", "connector": "J5", "pin": "1", "direction": "…", "level": "…", "maxCurrent": "…", "notes": "…"}],
  "jumpers": [{"ref": "J3", "name": "…", "default": "…", "settings": ["…"], "note": "…"}],
  "firmwares": ["01_bringup", "02_gas_control"],
  "leds": [{"ref": "D16", "name": "LED0 PWR", "color": "green", "net": "LED0_A", "hardware": "", "modes": {"01_bringup": "…"}}],
  "firmware": {"fpgaRef": "U8", "images": [{"name": "…", "file": "…", "tool": "…", "purpose": "…"}], "notes": ["…"]},
  "measurements": {"signals": [{"signal": "…", "probe": "R42.2", "net": "CLK_CPLD", "expected": "…", "instrument": "…", "note": "…"}], "notes": ["…"]},
  "equipment": [{"item": "…", "spec": "…", "purpose": "…"}],
  "bringup": [{"title": "…", "firmware": "…", "setup": ["…"], "note": "…",
               "checks": [{"item": "…", "probe": "…", "expect": "…", "see": "应看到…", "pass": "…", "fail": "…"}]}],
  "software": {"interfaces": [{"name": "…", "connector": "…", "settings": "…", "status": "…", "notes": "…"}],
               "commands": [{"key": "R", "name": "…", "acceptedIn": "…", "effect": "…", "ack": "…"}], "ack": ["…"],
               "telemetry": {"period": "…", "example": "…", "fields": [{"field": "F1", "format": "…", "meaning": "…"}], "notes": "…"},
               "notes": ["…"]},
  "troubleshooting": [{"symptom": "…", "cause": "…", "check": "…"}],
  "openItems": ["已跟踪的未决项（唯一允许写未知点的地方）"]
}
```

`probe` 写 `位号.焊盘`（`C9.1`、`J6.4`）或 `位号`；电源轨未给 probe 时自动选：接头脚 → 另一端接地的两脚电容 → 任意焊盘。
`--pin-map`（或 `manual.pinMap`）读 Quartus `.tcl/.qsf` 的 `set_location_assignment` 或 Vivado `.xdc` 的 `PACKAGE_PIN`。
