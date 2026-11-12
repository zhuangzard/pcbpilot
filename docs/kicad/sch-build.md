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
pcbpilot kicad sch-pdf --project build/ --out build/schematic.pdf   # 整套图一个 PDF：每页后面跟它的 BOM 页
```

常用开关：`--no-intent`（不跑 intent derive）、`--analog`（intent derive 内含模拟仿真）、`--no-planner`（全部区用
网格 + autoconnect，最快）、`--planner-budget`（每区布局器候选上限，默认 20000）、`--planner-timeout`（整个布局阶段
一个截止点，含自动分页/拆区的重排；过点后不再启动 planner、直接走网格；已启动的 planner 跑完自己的候选上限，
不能中途截断，所以阶段可能超时——超时会在 `warnings` 里写明实际耗时、启动 planner 的区数、最长一次和走网格的区数）、`--jobs`（并行 LCSC 导入 / 区布局）、`--fresh`（忽略已有的布局/uuid 状态）、
`--parts`（块用的 standard-parts.json）、`--layout-quality strict|advisory`（布局看门狗，默认 strict，见下文）、
`--zone-timeout`（每区布局墙钟上限，默认 60 s，0 = 不限）、`--no-package-text`（符号旁不画封装文字）、
`--no-bom-pages`（不生成每页后面的 BOM 页）；后两项见“封装文字与 BOM 页”一节。

输出为一个 JSON 报告（stdout，另存 `--report`）：`ok`、`gates[]`、`timingsMs[]`（每阶段毫秒）、`totalMs`、
`zones[]`（每区方法、绝对包围盒、是否复用/移动、该区的质量问题）、`partBoxes`（每件所在页与包围盒，供 AI 规划
下一块而不必再查询）、`symbols`（符号来源统计）、`bom`（BOM 页与缺项，见下文）、`outputs`、`checkpoint`、`undo`、
出错时 `error{code,message,fix}`。

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
| `parts[]` | `ref`、`value`、`lcsc`（C 号）、`mpn`、`symbol`（可选 `Lib:Name` 或 `path/lib.kicad_sym:Name`）、`footprint`、`zone`、`page`、`fields{}`（隐藏字段；`Description`、额定值、`Sourcing`、`Package` 进 BOM 页，见“封装文字与 BOM 页”）、`pins[]`（无符号/离线时按此生成方框符号）、`near`（关系提示 `REF:PIN`：放在该脚旁，同网时用短导线连上）。 |
| 封装库 | `footprint` 写 `Lib:Name` 时，工程目录或任一 `libDirs` 目录下的 `<Lib>.pretty`（`libDirs` 也可直接列 `<Lib>.pretty` 目录）随工程复制并登记进工程 `fp-lib-table`（`${KIPRJMOD}/<Lib>.pretty`），**不论符号来自哪里**（LCSC、KiCad 自带、`pins` 生成、本地库；v0.12.0 只在符号来自同目录 `.kicad_sym` 时才带上，其余静默丢掉，pcb-sync 再报 `E_FP_LIB_UNRESOLVED`）。都找不到、也不是 KiCad 自带库、也不在用户全局 `fp-lib-table` 里 → `FOOTPRINT_LIB_NOT_FOUND`；库找到了但没有该封装 → `FOOTPRINT_NOT_FOUND`。相对 `libDirs` 按 spec 文件所在目录解析（v0.12.1 及以前按当前目录）。 |
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
脚号脚名、`PIN_TWO_NETS`、`PIN_UNKNOWN_PART`、`NET_NO_PINS`、`RAIL_UNKNOWN_NET`、`LCSC_UNAVAILABLE`、
`FOOTPRINT_LIB_NOT_FOUND`、`FOOTPRINT_NOT_FOUND` …），AI 一轮改完。

`--from-connectivity`：EasyEDA 连接 IR 1.4（`pcbpilot sch connectivity`，每页一个文件）→ spec：器件（C 号 →
LCSC 符号，否则按 IR 的引脚表生成）、网、不连接、模块 → 区，每个文件一页。

## 封装文字与 BOM 页（v0.12.9，默认都开）

**封装文字。** 每个符号在值的旁边多一行可见的 `Package` 属性，读图的人不用点开属性就知道是 0603 还是 0805、
SOT-23 还是 SOIC-8。文字由封装名推出：`R_0603_1608Metric` → `0603`，`SOIC-8_3.9x4.9mm_P1.27mm` → `SOIC-8`，
`QFN-32-1EP_5x5mm_P0.5mm` → `QFN-32`，`R_Array_Convex_4x0603` → `4x0603`，认不出的取封装名的第一段（最长 16 字符）。
推得不合意时在件上写 `fields.Package` 覆盖。封装文字参与布局的文字占位计算（与值同一行放得下就同一行，否则另起一行），
不会压到导线或相邻器件。没有封装的件不显示文字，并在 `warnings` 和 `bom.noPackage` 里列出。测试点、安装孔、
基准点、焊接跳线、网络连接件、标志（位号前缀 `TP` `H` `MH` `FID` `JP` `SJ` `NT` `LOGO` `G`）默认不画封装文字
（写了 `fields.Package` 才画），BOM 里仍有它们的封装。`--no-package-text` 整体关闭。`Package` 是普通的符号字段：`kicad pcb-sync` 把它和其他字段一样同步到封装上（KiCad 的原理图一致性检查会比对它）。

**BOM 页。** 每个原理图页后面跟一张 BOM 图纸 `<页文件名>-bom.kicad_sch`（图纸名 `<页标题> BOM`），页码排成
“页、它的 BOM、下一页、它的 BOM …”，导出的 PDF 就是这个顺序。单页工程（根图就是图本身）的 BOM 图纸符号画在图的旁边。表格列：

| 列 | 来源 |
|---|---|
| Refs / Qty | 值、封装、MPN、C 号、规格、来源都相同的件合成一行，位号压缩（`R1-R4, R7`）；DNP 的件单独成行并标 `DNP` |
| Value / Package / MPN / LCSC | `value`、封装文字、`mpn`、`lcsc`。MPN 也认字段 `MPN` / `Mfr Part` / `Manufacturer Part (Number)` / `Part Number`，C 号也认字段 `LCSC Part #` / `LCSC` / `LCSC Part` / `JLCPCB Part #`（值必须是合法 C 号）——从别的工具读回来的原理图通常把它们放在字段里。设计自己没写 MPN 而 C 号有目录记录时，MPN 取目录的 |
| Spec / requirements | `fields.Description` 在前，然后 `Rating` `Power` `Voltage` `Current` `Tolerance` `Dielectric` `Note(s)` `Spec` `Requirements`，再是其余字段（`键: 值`）。变压器、电感这类件把 `Inductance`、`TurnsRatio`、`Isat`、`Isolation`、`Core` 等写成字段就会出现在这里 |
| Sourcing | `fields.Sourcing`（如 `custom / self-sourced`、`any`、指定厂家）；没写而有 C 号 → `LCSC`；值或说明里写着 custom / 定制 / 自制 → `custom / self-sourced`；都没有 → `—` |

**LCSC 目录补全（`--bom-catalog`）。** 设计只给了 C 号时，BOM 用该 C 号的目录记录补 MPN，并在规格格最前面加
`[LCSC catalogue] 厂家, 描述`（设计自己写了 `Description` 就不加：目录说的是“会买到什么”，不是“设计要求什么”）。
记录来自本机缓存（`~/.pcbpilot/cache/kicad-lcsc-catalog/`），由 `kicad lcsc --verify` 和 `sch-build --bom-catalog`
写入。默认构建只读缓存、不联网；`--bom-catalog` 才去取还没缓存的 C 号（每次 2 个并发、单个 25 s、总共 4 分钟；
`--offline` 时不取）。取不到只是警告，不让构建失败。报告 `bom.catalog`：`numbers`（设计里不同的 C 号数）、
`known`（有记录的）、`fetched`（本次联网取到的）、`failed[]`（目录没应答）、`unknown[]`（**目录里没有这个 C 号**——
多半是号写错了，用 `kicad lcsc --verify` 核对）。

表格放不下一张纸时依次用 A4 → A3 → A2 横向，再放不下就续页（`<页>-bom-2.kicad_sch` …）。一行不拆页；单元格内容超过半张纸高时截断并以“… (cut: see the spec)”结尾。没有任何 BOM 行的页（只有电源符号、测试点之类）不生成 BOM 图纸。BOM 图纸里没有符号、
导线和标签，不进网表；页面填充率、`page-fit`、布局看门狗等按页测量的门禁不计 BOM 图纸。原地重建时 BOM 图纸的
uuid 由页和行内容确定，内容不变文件就不变。`--no-bom-pages` 关闭。 `kicad sch-pdf --out` 必须是 `.pdf` 文件。

报告的 `bom`：`sheets` / `packageText`（两个开关的实际状态）、`pages[]`（`page`、`file`、`sheet`、`paper`、
`pageNumber`、`rows`）、`rows`、`parts`、`noPackage[]`、`noSpec[]`（不是普通阻容、规格格为空的件）和
`missing[]`（`ref`、`reason`、`fix`）。**`bom.missing` 是警告而不是门禁失败**：列出读者认不出来的件——没有封装，
或者既没有 MPN、也没有 C 号（含上面那些字段）、也没有 `Description`。值或说明里写着 custom / 定制 的件按内容判断（任一字段里有数字，或提到图纸 / 对插件，才算写了要求；只写“定制变压器”不算），单独一种原因
（“a custom part with no requirements”）：`fix` 要求写出它必须满足的参数（变压器 / 电感：匝比、电感量、饱和电流、
隔离电压、磁芯；连接器 / 结构件：图纸或对插件）。每条的 `fix` 是一句话的改法；交付前应为空。

spec 作者对每个**不是普通阻容**的件要写：`fields.Description`（这是什么）、关键额定值（功率、电压、电流、容差；
磁性件：电感量 / 匝比 / 饱和电流 / 隔离耐压 / 磁芯）和 `fields.Sourcing`（来源：LCSC、指定厂家、定制 / 自备）。例：

```json
{"ref": "T1", "value": "EE16 1:1.2", "zone": "iso", "footprint": "Transformer_SMD:Transformer_Coilcraft_CST1",
 "fields": {"Description": "Flyback transformer, custom wound", "Sourcing": "custom / self-sourced",
            "Inductance": "450 uH primary", "TurnsRatio": "1:1.2", "Isat": "0.8 A", "Isolation": "1.5 kV", "Core": "EE16 PC40"}}
```

**PDF。** `pcbpilot kicad sch-pdf --project build/ --out build/schematic.pdf`（或 `--sch root.kicad_sch`）用
kicad-cli 把根图及其下全部图纸按页码画成一个 PDF，不改原理图；输出 JSON 含 `pages`（PDF 页数）和 `sheets`（图纸顺序）。

## 流水线（全部在进程内、对文件操作）

1. **展开与解析**：块展开、符号/封装解析（LCSC 缓存 + 并行导入）、引脚名解析、全部诊断一次收集。
2. **分页分区**：区按功率/信号流排序；每页的区做**二维装箱**（`sbPack2D`，`kicad_sch_build_pack.go`）：
   每个区框放到已放区框的右侧 / 下方候选角点中、使所有区框外包盒最贴合纸张比例（或面积最小）的那个，
   按阅读顺序和按高 / 面积 / 宽排序共 4 种顺序 × 2 种目标各算一次，取最紧凑的；从 A4 起取第一张能装下的纸，
   每张纸上二维失败再试旧的成行打包（`kicadPackZones`），再试把各区换成备选形状（各区最窄 / 最矮 / 最小的
   干净画法）——内容只比某档纸大一点的页借此降一档（实测两页分别 A1 → A2、A0 → A1）。PWR_FLAG 行只放在**它自身宽度之上**的区框下面
   （不再压到全页最低的区框下）。`sch-edit` 保留旧位置时，若保留位置会让该页填充率低于看门狗下限，则整页重新装箱
   （报告里这些区 `moved:true`）。
3. **区内布局**（每区独立、并行；结果按输入哈希缓存在工程状态里）：
   - 先试离线布局器（`sch layout-plan` 引擎；小核心时打开 aesthetics + 有界旋转优化）；
   - 再试确定性网格方案（v0.12.9 起的**紧凑**网格，`sbGridVariant` 非 `Legacy`）：
     - 去耦电容先放（竖放、电源脚朝上、地脚朝下，成排放在所服务的电源脚旁，电源/地符号直接落在脚上）；
     - 其余器件对准它服务的引脚，用正交短导线连上（导线上贴标签或电源 T 接），**链式**挂接：芯片引脚 → 电阻 →
       电阻/电容……；同一信号网上第三个及以后的件（脚都已接线时）**T 型分支**：垂直接到该网已有导线的中点（KiCad 画
       结点，不再加标签；一个点只接一条分支，避免四通结点 EG-09；导线上的网名标签只挡住它的文字一侧）；每个引脚只按**它自己的网络名**预留短桩 + 标签空间（v0.12.8 按全区最长网络名 + 10 mm 给每个
       有脚的边预留，挂接在 ±15 mm 的搜索里几乎找不到位置）；已挂接器件只把本体 + 位号/值文字当障碍；
       电源/地网只挂到芯片（> 3 脚）的脚上（不画 C —GND— C 这种链）；
     - **节点画法**（`compact-node` 变体，布局引擎 v4）：不在任何芯片脚上、只能经一个已挂接的件到达的多成员信号
       节点（串阻另一端 + 滤波电容 + 钳位管 + 反馈电阻……）画成一簇导线：器件先沿**信号网**一路挂完，没有信号网可挂时
       才允许一个件挂到芯片的电源/地脚；一个信号网的第一条导线按“还要接上来的小件数 × 5.08 mm”画长，后面的
       成员逐个 T 接到这条线上；还要贴标签的引脚按自己的短桩 + 标签长度留空，带拐弯的导线不再压到相邻脚的标签上
       （引脚偏离 2.54 mm 网格半格的符号上，这种压线以前会让器件在重画时丢掉导线）。普通紧凑变体与节点变体
       都画，一起按 `sbZoneCost` 排名；有冲突时照旧回退为标签；
     - 核心够不到的子电路（按键的上拉 / 开关 / 串阻 / 滤波电容，经信号网连成的一组 ≥ 2 件）按组**单独画成
       有导线的小块**（组里与其他件共网最多的件当核心），再放进本区的空位：先找已画内容里的空洞，否则在所有
       已画物体右侧 / 下方的“天际线”角点里选使本区最接近横向图纸比例（宽 / √2 与高取大者最小）的那个；
     - 仍挂不上的件先填空洞（避开本体、待连引脚的短桩 + 标签空间、导线、符号、文字，留 1.27 mm），放不下才
       另起一列；
     - 其余引脚用 autoconnect 规划器批量落短桩 + 标签/电源符号；
   - 每个候选在草稿页上单独渲染，按 `kicad sch-check` 的质量检查 + 工程级硬规则打分（区内有 1 处交叉也算一条：
     EG-08 是每页 ≤ 1，多个各有 1 处交叉的区在同一页就过不了）；会造成短路的短桩一律拒绝；
   - 有问题的紧凑候选**有界重试**：把问题里点名的器件（及标签被点名的网上的器件）改为不挂接，重画，最多 4 轮；
   - 在所有**无问题**候选里按 `sbZoneCost` 选：区内信号连接中靠标签的比例 + 0.5 ×（未挂接小件比例）+
     0.5 ×（面积 / 最小候选面积 − 1），先选满足看门狗区级下限的；紧凑候选全都有问题时才画 v0.12.8 的
     旧网格（`legacy` 变体）兜底；报告 `zones[].variant` 记录用的是哪种。代价在最佳值 + 0.25 以内、也满足
     看门狗区级限值的其余干净画法作为该区的**备选形状**留给页面装箱（不存进状态文件）。页面装箱在某档纸上
     装得下、但内容外包盒低于看门狗的页面填充率时，先换成按行排，再试各区的备选形状（含最大的那个），纸张档位不变。
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
| `layout-quality` | 硬（`--layout-quality advisory` + 签名豁免时为警告） | 布局看门狗：区内全靠标签、阻容不挂接、诡异长线、整页大片空白、整板标签过多（见下节）。 |
| `connectivity` | 硬 | 网表 → 连接 IR 1.4 校验通过。 |
| `intent` | 硬（可 `--no-intent` 跳过） | 进程内 intent derive 成功。 |
| `design-review` | 硬（有 `--requirements` 时） | review-panel stage schematic。 |

## 布局看门狗（`layout-quality` 门禁，v0.12.9）

正确性门禁全过的图仍可能没法看：区内器件全靠网络标签、阻容离所服务的芯片引脚很远、整页大片空白。以前这种图
照样 `ok:true` 交出去。看门狗在布局后逐区、逐页、整板计算指标（与 `scripts/schmetrics` 从图纸上数的是同一口径）：

| 指标 | 范围 | 下限 / 上限 | 含义 |
|---|---|---|---|
| `signalLabelShare` | 区（区内信号连接 ≥ 8） | ≥ 0.90 判失败 | 区内器件引脚之间的信号连接（每网 引脚数 − 1）中靠标签而不是导线连上的比例 |
| `attachedShare` | 区（信号小件 ≥ 8） | < 0.20 判失败 | ≤ 3 脚、有信号脚的件中，用导线连到别的件引脚上的比例（去耦电容、两端都是电源的件不计） |
| `longestWireMm` | 区 | > 100 mm 判失败 | 单段导线最长长度（器件离所连引脚太远） |
| `fillRatio` | 页（A3 及以上、≥ 2 个区） | < 0.33 判失败 | 工程级门禁的填充率：内容外包盒 / 图框内面积；A4 是最小纸张、只有一个区的页、最大的区单独就需要这档纸的页不判（重排帮不上） |
| `signalLabelShare` | 整板（区内信号连接 ≥ 20） | > 0.55 判失败 | 全部区合计 |
| `attachedShare` | 整板（信号小件 ≥ 20） | < 0.60 判失败 | 全部区合计 |

阈值集中在 `internal/app/kicad_sch_build_watchdog.go` 的 `sbLayoutQualityLimits`，来源是 7 块实测
回归板（135–804 件）：v0.12.8 原样构建（`--no-planner`）时 4 块板上 8 个区全靠标签、5 块板上 13 个区阻容几乎不挂接、整板标签比例 0.41–0.78、
挂接比例 0.27–0.75、最低页面填充率 0.29（用户手里 v0.12.7 的结果 0.20–0.31）；第二轮改进后分别为 0、0、0.22–0.42、
0.76–0.97、0.35（页填充率阈值据此从 0.30 收紧到 0.33）。每个阈值
取在两列之间、偏向用户否掉的那一侧。7 块板里 6 块的 v0.12.8 结果会被拦下（只有 field board B 通过；用户手里 v0.12.7 生成的 7 份
全部会被拦下），改进后 7 块全过（`--no-planner` 与默认模式都过）。

**自动重试**（有界）：区级——选中的画法不满足区级限值时，把还没试过的画法（布局器之后的网格、紧凑之后的旧网格）
也画一遍，优先取满足限值的；页级——每页按 4 种顺序 × 2 种目标 × 5 种纸张装箱，`sch-edit` 保留位置导致空白时整页
重排。仍不满足就**不写** `--out`：

```json
{"ok": false,
 "error": {"code": "LAYOUT_QUALITY",
           "message": "layout-quality: 2 limit(s) missed: zone keys: signalLabelShare 1.00 > 0.90; board demo: attachedShare 0.27 < 0.60",
           "fix": "nothing was written; change the spec and rebuild: zone keys: add near hints in parts[]: R57 near U1:GPIO4, … ; or split zone keys into one zone per IC cluster … | board …: the zones with the most label joins / loose small parts: keys (49), … — the failed project is in <out>/.pcbpilot/sch-build/failed",
           "issues": [{"scope": "zone", "id": "keys", "page": "hmi", "metric": "signalLabelShare", "value": 1, "limit": 0.9, "fix": "add near hints in parts[]: …"}]}}
```

`gates[name=layout-quality].data` 另有每区的实测值（`zones[]`：variant、signalLabelShare、attachedShare、
longestWireMm、ms）、整板合计、阈值表和 `slowZones`。退出码非零，失败工程照惯例在 `.pcbpilot/sch-build/failed/`。

**时间看门狗**：`--zone-timeout`（默认 60 s）是每区网格画法 + 重试的墙钟上限，从网格阶段开始计（布局器仍受
`--planner-budget` 约束）；过点后、且已有一个干净画法时不再启动新的画法，保留目前最好的（还没有干净画法时剩下的
画法照跑完——列表有界——否则会交出门禁必挂的图），门禁明细和 `data.slowZones` 写明
“zone X took N s (limit 60 s): kept <variant>, K variant(s) not tried”（不再只是 warnings 里的一句）。

**Agent 收到 `LAYOUT_QUALITY` 时**：读 `error.issues` / `error.fix`，按建议改 spec——给点名的阻容加 `near`
（建议里已给出 `REF near IC:PIN`），把点名的区按 IC 簇拆成多个区，把空白页上的区挪到相邻页（`zones[].page`）——
然后重新构建。不要用 `--layout-quality advisory` 绕过：它只报告不拦截，必须在 `--waivers` 里带签名豁免
`{"gate":"layout-quality","match":"--layout-quality advisory","reason":…,"by":…}`，否则构建以
`LAYOUT_QUALITY_WAIVER_REQUIRED` 失败。

同一组门禁也可以对**任何工具画的** KiCad 工程单独运行（不重画）：`pcbpilot kicad sch-check --sch root.kicad_sch
--engineer-grade --spec design.json`。sheet 本身不含的信息（网的电源/地类型、区、页、标题栏）由 spec
（`pcbpilot.kicad.sch-build/1`，符号离线解析）提供；报告多一个 `engineerGrade` 门禁项，不通过时退出码非零。
EG-06 只在同一区内找 IC 电源脚，所以 spec 的 `zone` 要与图上的功能区一致。

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
- 芯片引脚排得很密（2.54 mm 一脚，编码器、连接器）时，相邻脚的件常因导线会穿过别的脚的短桩而挂不上，回退为标签；
  经“有界重试”改成不挂接的件也用标签。T 型分支接到导线中点时，若器件引脚与分支点不在一条线上会有一个小拐弯。
- 页面填充率（内容外包盒 / 图框面积）受纸张档位限制：内容只比某档纸大一点时仍会落到 0.35–0.45。7 块回归板第二轮
  后全部页中位数 0.60，总图纸面积 12.1 → 6.3 m²。链状块不会折行（很长的分压链仍是一长列）。
- 看门狗的阈值按 7 块实测板标定；更小的板（< 20 个信号连接）只判区级和页级指标。
- 多单元符号只走网格方案；KiCad `extends` 派生符号不支持。
- 封装文字让器件的文字占位变宽：7 块回归板 39 个原理图页里有 4 页（原来填充率 0.74–0.80）因此升了一档纸（A3 → A2），
  其余页填充率变化在几个点以内。值与封装同行的宽度上限（10.5 mm）是按这 7 块板调出来的经验值。
- BOM 页的图纸名（`<页标题> BOM`）很长时在标题栏里会超出格子；BOM 表的规格列只有 spec 写了 `fields` 才有内容。
- `kicad sch-pdf` 的页数是数 PDF 里的页对象得到的，没有用第二个 PDF 工具核对。
- KiCad 自带 `Device:LED` 的箭头超出引脚端，导线贴着画会被 `wire-through-body` 判中（用 LCSC 的 LED 或 pins）。
- 层次图的 sheet pin 只按页序分左右（不按信号方向）；电源网一律用全局电源符号，不走 sheet pin。
- 视觉评审（渲染 PNG 交给 review-panel）尚未接上：review-panel 目前只读文本证据；当前评审证据是 spec、
  连接 IR、intent、ERC。
- 网格方案的去耦是“竖放成排 + 电源/地符号”，不是导线并联到电源脚；对接导线只在有空间时画。
