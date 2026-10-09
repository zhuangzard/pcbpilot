# `pcbpilot kicad sch-build` — 一次调用：设计数据 → 过门禁的 KiCad 原理图工程

> 2026-10-09。用户的头号抱怨是“原理图很慢”：Agent 用约 80 个细粒度 `sch` 子命令（S0–S6：分区、逐件放置、
> 逐脚 autoconnect、layout-plan、zone-arrange、titleblock、check、gate、save）一步一回合地画，30 件的 ESP32 板
> 要几个小时。业界经验是 **AI 做决定，确定性工具画图**。`sch-build` 就是这个工具：AI 只写设计数据（spec），
> 不写任何坐标；一次调用在几秒内生成完整的、过硬门禁的 KiCad 工程。

KiCad 路径因此变为：**写 spec → `kicad sch-build` → review-panel → 完成**；之后的修改用 `kicad sch-edit`
（一次调用一个增量），查看用 `kicad sch-read`（紧凑 JSON，代替读 S-expression）。

## 命令

```bash
pcbpilot kicad sch-build --spec design.json --out build/ [--name N] [--dry-run] [--offline]
                         [--requirements req.md … [--review codex,kimi,claude] [--no-review --waivers w.json]]
pcbpilot kicad sch-build --from-connectivity p1.json [--from-connectivity p2.json …] --out gas/   # EasyEDA → KiCad
pcbpilot kicad sch-edit --project build/ --spec delta.json [--dry-run]
pcbpilot kicad sch-read --project build/ [--out spec.json]      # 或 --sch root.kicad_sch
pcbpilot kicad sch-checkpoint list|restore N --project build/
```

常用开关：`--no-intent`（不跑 intent derive）、`--analog`（intent derive 内含模拟仿真）、`--no-planner`（全部区用
网格 + autoconnect，最快）、`--planner-budget`（每区布局器候选上限，默认 20000）、`--planner-timeout`（布局阶段
总墙钟，超时后未开始的区直接走网格）、`--jobs`（并行 LCSC 导入 / 区布局）、`--fresh`（忽略已有的布局/uuid 状态）、
`--parts`（块用的 standard-parts.json）。

输出为一个 JSON 报告（stdout，另存 `--report`）：`ok`、`gates[]`、`timingsMs[]`（每阶段毫秒）、`totalMs`、
`zones[]`（每区方法、绝对包围盒、是否复用/移动、该区的质量问题）、`partBoxes`（每件所在页与包围盒，供 AI 规划
下一块而不必再查询）、`symbols`（符号来源统计）、`outputs`、`checkpoint`、`undo`、出错时 `error{code,message,fix}`。

## spec（`pcbpilot.kicad.sch-build/1`）

AI 只写这一份数据。**没有坐标**：位置全部由确定性布局算出；唯一的位置提示是关系（`near`）。

```json
{
  "schema": "pcbpilot.kicad.sch-build/1",
  "name": "esp32-mini",
  "title": {"title": "ESP32-S3 mini", "rev": "A", "company": "pcbpilot", "date": "2026-10-09",
            "comments": ["5V terminal + USB-C OR, SY8089 3V3 buck, CH340C, ESP32-S3-WROOM-1"]},
  "pages": [{"id": "power", "title": "Power + USB"}, {"id": "mcu", "title": "MCU"}],
  "zones": [
    {"id": "usb", "title": "USB-C + CH340C", "page": "power"},
    {"id": "power_in", "title": "5V terminal + OR", "page": "power"},
    {"id": "buck", "title": "SY8089 5V to 3V3", "page": "power", "core": "U2"},
    {"id": "mcu", "title": "ESP32-S3-WROOM-1", "page": "mcu"}
  ],
  "parts": [
    {"ref": "J2", "value": "KF301-5.0-2P", "lcsc": "C474881", "zone": "power_in"},
    {"ref": "D1", "value": "SS34", "lcsc": "C8678", "zone": "power_in"},
    {"ref": "R10", "value": "10k", "symbol": "Device:R", "footprint": "Resistor_SMD:R_0402_1005Metric",
     "zone": "mcu", "near": "U3:IO8"},
    {"ref": "U9", "value": "MY-REG", "zone": "buck",
     "pins": [{"number": "1", "name": "GND", "type": "power_in"}, {"number": "2", "name": "VOUT", "type": "power_out"},
              {"number": "3", "name": "VIN", "type": "power_in"}]}
  ],
  "blocks": [
    {"block": "block.ch340c_usb_serial", "instance": "USB", "zone": "usb",
     "refs": {"U": "U1", "J_USB": "J1"}, "lcsc": {"J_USB": "C2765186"}, "bind": {"VBUS_5V": "USB_VBUS"}},
    {"block": "block.sy8089_buck_3v3", "instance": "BUCK", "zone": "buck",
     "bind": {"VIN": "VSYS_5V", "EN": "VSYS_5V", "3V3": "+3V3"}}
  ],
  "nets": [
    {"name": "VSYS_5V", "pins": ["D1:K", "D2:K"], "voltage": 4.6, "currentA": 0.6},
    {"name": "+5V_TERM", "pins": ["J2:1", "D2:A"]},
    {"name": "GND", "pins": ["J2:2"]},
    {"name": "LED_CTRL", "pins": ["U3:IO2"]},
    {"name": "I2C_SDA", "pins": ["U3:IO8", "R10:2"], "kind": "signal"}
  ],
  "rails": [{"net": "+3V3", "voltage": 3.3, "currentA": 0.5}],
  "noConnect": ["U3:IO46"],
  "unusedPins": "nc",
  "zoneOrder": "flow",
  "libDirs": ["./mylibs"],
  "intent": {"layers": 4, "standard": {"name": "IEC62368-1"}}
}
```

| 字段 | 含义 |
|---|---|
| `parts[]` | `ref`、`value`、`lcsc`（C 号）、`mpn`、`symbol`（可选 `Lib:Name` 或 `path/lib.kicad_sym:Name`）、`footprint`、`zone`、`page`、`fields{}`（隐藏字段）、`pins[]`（无符号/离线时按此生成方框符号）、`near`（关系提示 `REF:PIN`：放在该脚旁，同网时用短导线连上）。 |
| 符号来源顺序 | 显式 `symbol`（工程目录 → `libDirs` → KiCad 自带库）→ LCSC（`kicad lcsc --import` 同一转换，缓存于 `~/.pcbpilot/cache/kicad-lcsc/<C号>`，重复构建离线且瞬时，多件并行导入）→ `pins` 生成。KiCad `extends` 派生符号不支持（用父符号、C 号或 pins）。EasyEDA 转来的电阻/电容/电感/二极管/开关/连接器等被动类引脚统一改为 passive（EasyEDA 的引脚类型不可信，电阻常被标成 input）。 |
| `blocks[]` | 电路块库（`pcbpilot blocks ls`），用 block-apply 的同一规划器展开为器件 + 网（standard-parts.json 的器件、位号、internal_nets、端口绑定）。`refs`：role → 位号；`lcsc` / `values` / `symbols`：按 role 覆盖。块内部网自动取可读名（`USB_DP`、`BUCK_SW`、`BUCK_FB`、`Q1_BASE`、`LED1_A`），不会出现 `_N3`。块里的焊盘别名（`EP*`、`EPAD`、`EH`、`SHELL` …）映射到符号真实脚号；对不上就是硬错误。 |
| `nets[]` | `name`、`pins`（`REF:PIN`，PIN 为脚号或脚名；同名多脚全取；`NAME*` 前缀匹配）、`kind`（`power` / `ground` / `signal`，缺省按名字）、`voltage`、`currentA`（进入 intent derive 的 rails）。 |
| `rails[]` | 与 intent derive `--spec` 的 rails 相同（`net`、`voltage`、`currentA`）；并把该网定为电源。 |
| `pages[]` / `zones[]` | 多页 = 层次原理图（根图只画功能块 sheet 符号 + 接口 sheet pin）；区 = 带标题的虚线框，`core` 可指定核心件。 |
| `unusedPins` | `nc`（缺省：未用引脚都打不连接标志）或 `open`。 |
| `zoneOrder` | `flow`（缺省：连接器/输入 → 稳压 → 主控 → 外设）或 `spec`（按 zones 列表顺序）。 |
| `title` | 标题栏；缺项用默认值并在 warnings 里列出（EG-22 要求 title/date/rev/company 齐全）。 |
| `intent` | 原样并入 `intent derive --spec`（standard、layers、rules、domains …），rails 与网的电压/电流合并进去。 |

所有 spec 问题一次收集（`error.code = SPEC_INVALID`，每条带 `code` 与 `fix`：`PIN_NOT_ON_SYMBOL` 会列出符号真实
脚号脚名、`PIN_TWO_NETS`、`PIN_UNKNOWN_PART`、`NET_NO_PINS`、`RAIL_UNKNOWN_NET`、`LCSC_UNAVAILABLE` …），AI 一轮改完。

`--from-connectivity`：EasyEDA 连接 IR 1.4（`pcbpilot sch connectivity`，每页一个文件）→ spec：器件（C 号 →
LCSC 符号，否则按 IR 的引脚表生成）、网、不连接、模块 → 区，每个文件一页。

## 流水线（全部在进程内、对文件操作）

1. **展开与解析**：块展开、符号/封装解析（LCSC 缓存 + 并行导入）、引脚名解析、全部诊断一次收集。
2. **分页分区**：区按功率/信号流排序；每页的区用 `kicadPackZones`（sch-quality 的区打包器：阅读顺序成行、
   避开标题栏、取能装下的最小纸张）。
3. **区内布局**（每区独立、并行；结果按输入哈希缓存在工程状态里）：
   - 先试离线布局器（`sch layout-plan` 引擎；小核心时打开 aesthetics + 有界旋转优化）；
   - 再试确定性网格方案：去耦电容先放（竖放、电源脚朝上、地脚朝下，成排放在所服务的电源脚旁，电源/地符号直接
     落在脚上）；其余器件对准它服务的引脚，用正交短导线连上（导线上贴标签或电源 T 接）；每个引脚的短桩 +
     标签/电源符号空间预留，别的器件不压；两脚件若只有一个脚接电源/地则强制朝上/朝下；仍找不到伙伴的放成列；
     其余引脚用 autoconnect 规划器批量落短桩 + 标签/电源符号；
   - 每个候选在草稿页上单独渲染，按 `kicad sch-check` 的质量检查 + 工程级硬规则打分，取问题最少者；
     会造成短路的短桩一律拒绝（回退该件的对接后重画）。
4. **渲染**：电源符号永远朝上、地符号永远朝下（短桩反向时加一个 2.54 mm 拐弯）；结点按“≥3 个连接或线端落在
   线中间”补齐；区框 + 标题；每个无驱动的电源网一个 PWR_FLAG；标题栏（`SetTitleBlock`）；`FitSheet` 定纸张。
   多页时跨页信号用层次标签，根图每个 sheet 符号带接口 sheet pin（左：与前页共享，右：与后页共享），
   相邻块接口一致时直接画短导线，否则短桩 + 标签；电源仍是全局电源符号。
5. **写工程**（先写到临时目录）：`.kicad_pro`、根图 + 子图、`sym-lib-table` / `fp-lib-table`、
   `lcsc.kicad_sym` + `lcsc.pretty`、`pcbpilot_gen.kicad_sym`、`pcbpilot_power.kicad_sym`。所有文件名经
   `sbSafeJoin`：安全字符集、无 `..` / 分隔符 / 绝对路径，`filepath.Rel` 不越出 `--out`。
6. **门禁**（kicad-cli 只跑两次，并行：网表 + ERC）：见下表。任何硬门禁失败 → `--out` 不动，失败工程存到
   `<out>/.pcbpilot/sch-build/failed/` 供查看，退出码非零。
7. **提交**：比对开始时的文件 sha256（compare-and-swap，期间被别人改过就拒绝）、每工程一把写锁，然后原子替换。
8. **下游**：`connectivity.json`（连接 IR 1.4，`kicad netlist` 同一转换）、`values.json`、`intent-spec.json`、
   进程内 `intent derive` → `intent.json` / `intent.md`（线宽、IR、安全距离的输入），保证原理图 → PCB 连续。
9. **设计评审**：给了 `--requirements` 就跑 `review-panel`（stage `schematic`，默认 codex,kimi,claude，同输入复用
   上次通过的 review.json）；`--no-review` 需要签名豁免 `{"gate":"design-review","match":"--no-review",…}`。
10. **状态与检查点**：`<out>/.pcbpilot/sch-build/{spec.json,state.json}`（规范化 spec：引脚一律 `REF:脚号`；
    状态：uuid、每区布局 + 位置）；每次成功构建/编辑保存检查点 `<out>/.pcbpilot/checkpoints/N`。

## 门禁

| 门禁 | 级别 | 内容 |
|---|---|---|
| `netlist` | 硬，事务 | `kicad-cli sch export netlist` 与 spec **完全相等**：每个 spec 引脚在同名网上、网成员一致、未列出的引脚不连接、没有被并网。 |
| `erc` | 硬（错误） | `kicad-cli sch erc` 全严重度；错误即失败，警告列出（EasyEDA 符号的 `unspecified` 引脚会产生 pin_to_pin 警告）。 |
| `page-fit` | 硬 | 每页内容装得下 A4…A0（装不下 → 拆页）。 |
| `quality` | 硬 | `kicad sch-check` 的同一组检查（`kicad.CheckSchematic`）：重叠、线穿本体、斜线/离网格/重叠线、线端或引脚落在线中间、标签/字段压东西、标题栏、出页。 |
| `engineer-grade` | 硬（按页） | EG-01 层次一致（sheet pin ↔ 层次标签、信号不用全局标签）；EG-05 电源/地符号朝向 100%；EG-06 去耦电容本体到所服务 IC 电源脚 ≤ 25.4 mm；EG-08 异网交叉 ≤ 1/页；EG-09 四通结点 = 0；EG-13 悬空标签 = 0；EG-20 无自动网名（`Net-(`、`_N\d+`）；EG-22 标题栏齐全。另报告（软）：长线 > 50 mm、平均拐弯、填充率、信号流（有类型驱动脚的网中驱动在左的比例）。规则来源：`docs/research/engineer-grade-schematic-2026-10-09.md`（主仓库）。 |
| `connectivity` | 硬 | 网表 → 连接 IR 1.4 校验通过。 |
| `intent` | 硬（可 `--no-intent` 跳过） | 进程内 intent derive 成功。 |
| `design-review` | 硬（有 `--requirements` 时） | review-panel stage schematic。 |

## sch-edit（增量修改，一次调用）

delta 只描述意图：

```json
{"ops":[
  {"op":"add_part","part":{"ref":"R10","value":"10k","symbol":"Device:R","zone":"mcu","near":"U3:IO8"}},
  {"op":"add_block","block":{"block":"block.ams1117_ldo_3v3","zone":"ldo","page":"power","bind":{"VIN_5V":"VSYS_5V"}}},
  {"op":"remove_part","ref":"R9"},
  {"op":"replace_part","ref":"U2","with":{"lcsc":"C6186","value":"AMS1117-3.3"}},
  {"op":"connect","net":"I2C_SDA","pins":["U3:IO8","R10:2"]},
  {"op":"disconnect","pins":["U3:IO2"]},
  {"op":"rename_net","from":"LED_CTRL","to":"LED_EN"},
  {"op":"move_zone","zone":"led","page":"power"},
  {"op":"set_value","ref":"C1","value":"1uF"},
  {"op":"set_field","ref":"U1","name":"MPN","value":"CH340C"},
  {"op":"set_title","title":{"rev":"B"}}
]}
```

- 引脚按**脚名**写（`U3:IO8`、`U2:VIN`）；`replace_part` 按旧符号的脚名把连接迁到新符号。
- 只有输入变化的区重新布局；其它区原样保留布局、位置和符号 uuid（KiCad 原理图 ↔ PCB 的关联）。改名只换标签，不重排。
- 同样的门禁；网表门禁不过则什么都不写。`--dry-run` 输出完整计划（区复用/重排、坐标、连接、门禁），不写文件。
- 每次成功编辑一个检查点；报告里 `undo` 给出回到上一个检查点的命令。

## sch-read（紧凑读）

`kicad sch-read --project build/` 经 kicad-cli 网表读出：器件（ref、value、lcsc、symbol、footprint、zone、
`bboxMm`）、网（`REF:脚号`）、不连接脚、区的包围盒；sch-build 生成的工程还带回页、区、标题栏和 rails。
它本身就是合法 spec（`libDirs` 指向工程目录），`kicad sch-build --spec` 可原样重建（网表门禁保证一致）。
超过 40 个器件时写到 `<project>/.pcbpilot/sch-build/sch-read.json`，stdout 只回路径与计数。

## 计时（ESP32-mini，31 件 / 22 网 / 95 脚，KiCad 10.0.7，Apple Silicon）

| 情况 | 总墙钟 | 主要阶段 |
|---|---|---|
| 首次（31 件 LCSC 全部联网导入，并行 8） | ~8 s | 解析 3.2 s、布局 2.4 s、网表+ERC 2.5 s |
| 缓存命中，`--no-planner` | ~1.3–3 s | 网表+ERC ~1.2 s |
| 缓存命中，默认（布局器 + 网格候选） | ~4–7 s（机器空闲时） | 布局 ~2–5 s、网表+ERC ~1.2–2.5 s |

（机器负载很高时——同机其他代理在编译——同一构建测到 11–20 s。）kicad-cli 每次启动约 0.4 s 以上，所以
网表和 ERC 只在最后各跑一次、并行。旧的逐步流程（每步一个 LLM 回合 + 连接器往返）为数小时。

## 已知限制

- 布局器（`sch layout-plan` 引擎）对大核心（ESP32 模组、16 脚以上的 IC）常在预算内找不到解；这些区由确定性网格
  方案画（质量门禁与工程级门禁照样适用）。区内信号流“输入在左、输出在右”只在区的排序上体现，区内未强制。
- 多单元符号只走网格方案；KiCad `extends` 派生符号不支持。
- KiCad 自带 `Device:LED` 的箭头超出引脚端，导线贴着画会被 `wire-through-body` 判中（用 LCSC 的 LED 或 pins）。
- 层次图的 sheet pin 只按页序分左右（不按信号方向）；电源网一律用全局电源符号，不走 sheet pin。
- 视觉评审（渲染 PNG 交给 review-panel）尚未接上：review-panel 目前只读文本证据；当前评审证据是 spec、
  连接 IR、intent、ERC。
- 网格方案的去耦是“竖放成排 + 电源/地符号”，不是导线并联到电源脚；对接导线只在有空间时画。
