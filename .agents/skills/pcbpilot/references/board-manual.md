# 板卡使用说明书（`report manual`）

`pcbpilot report manual` 从**文件**生成一份给软件、调试和测试工程师看的板卡使用说明书：**一个**自包含 HTML
（内联 CSS 与 SVG，无外部请求，可直接打印 A4）。离线：不访问 daemon 或编辑器。模板规范源
[templates/board-manual/manual.html.tmpl](../templates/board-manual/manual.html.tmpl)，Go 包 `pkg/boardmanual`
用 `go:embed` 嵌入副本，`TestTemplateMatchesSkill` 要求两份逐字节相同（改模板后
`cp .agents/skills/pcbpilot/templates/board-manual/manual.html.tmpl pkg/boardmanual/templates/`）。
状态：`offline-verified`（Gas Module V5 Board A 现场板数据生成）。

```
pcbpilot report manual --board board-final.json [--intent intent.json] [--sim sim.json] \
    [--notes notes.json] [--pin-map board_pins.tcl] --out Board_使用说明.html [--lang zh|en] [--date RFC3339]
```

| 输入 | 来源 | 用于 |
|---|---|---|
| `--board`（必需） | `pcb dump --include-copper` | 外框、器件 bbox、焊盘（编号/网络/位置/尺寸/形状）、孔、多层圆形填充（安装孔） |
| `--intent` | `intent derive` | 网络类型（电源/地/信号）、电压域（非 SELV 自动警告） |
| `--sim` | `sim power` | 电源轨电压、逐焊盘电流（typical/peak/worst） |
| `--notes` | 人工编写 | 名称、用途、配套插头、注意事项、I/O 电平、LED、测试步骤、软件接口……；**只写真实值，未知写 TODO** |
| `--pin-map` | Quartus `.tcl/.qsf`（`set_location_assignment`）或 Vivado `.xdc`（`PACKAGE_PIN`） | 可编程器件引脚表，逐脚与板上网络对账 |

## 章节

1 概览（尺寸、层数、器件数、安装孔、使用顺序）· 2 接头位置图（方形画布；每个接头按角色着色 + “位号 角色 名称”
+ 副标题，引线到最近板边外，1 脚白点，器件标注，底边 LED 行标注，比例尺）· 3 每个接口：接到什么设备 /
在系统中的作用 / 怎么用 / 高亮说明框、焊盘真实位置图（电源红、地黑、信号蓝、空脚灰）、脚表（网络/类型/电压/最大电流/方向/文档网络/说明）·
4 电源要求（输入接口、典型/峰值/最坏电流、推荐 = 峰值 × 裕量（默认 1.5）、电源轨表）· 5 输入输出 · 6 LED（局部放大图 +
每个固件的含义）· 7 注意事项（人工 + 自动：电源脚最大电流、非 SELV 电压域、电源与信号混合接头、输入 > 2 A、对账不一致）与跳线 ·
8 仪器设备 · 9 测量点（编号探针图、电源轨期望值/公差/仿真范围/电流、关键信号）· 10 上电步骤（检查项/期望/应看到/合格/不合格）·
11 软件接口（接口、命令、应答、遥测字段、引脚表）· 12 故障排查 · 13 文档与板数据对账 · 14 TODO · 15 数据来源（sha256）。

接头识别：位号 J/CN/P/JP/CON（X 仅当封装像接头），或封装名像接头（XH、Micro-Fit、端子、2.54、DC-005…）。
角色（notes 无 `role` 时）：TCK/TMS/TDI/TDO → JTAG；CANH/CANL → CAN；TXD/RXD → UART；只有 VIN/+nV 与 GND → POWER；2 脚 → JUMPER；否则 CONN。

## notes.json 结构

未知字段报错（防拼写错误静默丢字）。所有字段可选。

```jsonc
{
  "title": "…", "subtitle": "…", "revision": "…", "overview": "…",
  "sequence": ["使用顺序第 1 步", "…"], "sources": ["写 notes 时依据的文档"],
  "partLabels": {"U8": "CPLD"},                       // 位置图上额外标注的器件
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
    "docPins": {"1": {"name": "SV3-S", "net": "SV3_DRV"}}   // 文档引脚表：逐脚与板上网络对账（"NC" = 空脚）
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
  "todo": ["…"]
}
```

`probe` 写 `位号.焊盘`（`C9.1`、`J6.4`）或 `位号`；电源轨未给 probe 时自动选：接头脚 → 另一端接地的两脚电容 → 任意焊盘。

## 交付前核对

- 第 13 章“文档与板数据对账”逐条处理：文档写错就改文档，板子错就回到设计；不能删 `docPins` 让表变空。
- 所有 J* 都在第 2 章且脚数 = 焊盘数；`expectedPins` 按 BOM 填写。
- 黄色单元格 = TODO：交付前要么补真实值，要么在第 14 章说明原因。
- 用浏览器打开并打印预览检查一次（SVG 内联，离线可读）。
