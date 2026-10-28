# 库件约定：字段、引脚类型、符号与封装、库表

> 用途：判断一个库件（自建、导入或现成）“合格”所需的约定。按通用 EDA/IPC 常识写成，具体数值永远以该器件的数据手册为准；
> 与手册冲突时听手册，并在 [引脚映射验收记录](pin-map-acceptance.md) 里写出差异。

## 目录

1. [必填字段](#1-必填字段)
2. [命名](#2-命名)
3. [引脚电气类型](#3-引脚电气类型)
4. [符号外形](#4-符号外形)
5. [封装：焊盘、阻焊、钢网](#5-封装焊盘阻焊钢网)
6. [封装：courtyard、fab、丝印、Pin-1](#6-封装courtyardfab丝印pin-1)
7. [KiCad 库表注册](#7-kicad-库表注册)

## 1. 必填字段

| 字段 | EasyEDA（Device 属性） | KiCad（符号字段） | 缺了会怎样 |
|---|---|---|---|
| 厂商 + 完整 MPN | `Manufacturer` / `Manufacturer Part` | `Manufacturer`、`MPN`（自定义字段） | BOM 无法采购；同系列后缀混淆 |
| 供应商号 | `Supplier Part`（LCSC C 号） | `LCSC Part #`（`kicad fab` / `lcsc --check` 也接受 `LCSC`、`LCSC Part`、`JLCPCB Part #`） | `kicad fab` 拒绝出 BOM/CPL；`signoff` 的 LCSC 子门禁失败 |
| 数据手册 | `Datasheet`（URL） | `Datasheet` | 无法复核引脚与封装 |
| 封装绑定 | Device 绑定的 footprint | `Footprint` = `库昵称:封装名` | 原理图 → PCB 时缺封装 |
| 位号前缀 | `designator`（U/R/C/L/D/Q/J/Y/F…） | `Reference` | 位号规则检查报错 |

- C 号是**订购身份**，不是电气证明：同一 C 号的封装后缀要和库件封装一致（`sch resolve-lcsc` 就是按“封装必须相同”判定的）。
- 无供应商号的件（机械件、手焊件）在 KiCad 里勾“排除 BOM / 排除坐标文件”，不填假号。

## 2. 命名

- 符号名用完整 MPN（可带供应商号，例如 `kicad lcsc --import` 产出的 `AMS1117-3.3_C6186`）；一族通用符号才用族名（`Device:R`）。
- 封装名按 IPC-7351 风格写出封装与关键尺寸，单位 mm，让人只看名字就能排除错配。常见写法：
  - 片式：`R_0402_1005Metric`、`C_0805_2012Metric`（英制代号 + 公制尺寸）；
  - 引线 IC：`SOIC-8_3.9x4.9mm_P1.27mm`、`TSSOP-20_4.4x6.5mm_P0.65mm`、`SOT-223-3_TabPin2`；
  - 无引线：`QFN-32-1EP_5x5mm_P0.5mm_EP3.45x3.45mm`；
  - 连接器：`系列_型号_1x04_P2.54mm_Vertical`。
  IPC-7351 的密度等级（Most/Nominal/Least，常写作 M/N/L 后缀）只在确实按该等级计算时写。
- 不同焊盘尺寸（例如手焊加长版）用不同名字，不覆盖同名封装。
- EasyEDA 上由 pcbpilot 新建/复制的资产名会被规范化并加 `EA_AGENT__` 前缀（connector 的命名规则），按名字搜索时带上它。

## 3. 引脚电气类型

类型决定 ERC 能查什么，按功能选，不按名字猜。EasyEDA 取值来自官方 `ESCH_PrimitivePinType`；
`lib symbol build` 把 `pinType` 原样交给 EasyEDA，且 `lib device validate` 不检查它，拼错不会在离线阶段报错。

| 功能 | KiCad `type` | EasyEDA `pinType` |
|---|---|---|
| 电源输入（VDD、VIN、GND 脚） | `power_in` | `Power`（地脚可用 `Ground`） |
| 电源输出（稳压器 VOUT、电源模块输出） | `power_out` | `Power` |
| 数字/模拟输入 | `input` | `IN` |
| 推挽输出 | `output` | `OUT` |
| 双向 GPIO、数据线 | `bidirectional` | `BI` |
| 三态输出 | `tri_state` | `HIZ` |
| 开漏 / 开集电极 | `open_collector` | `Open Collector` |
| 开射极 | `open_emitter` | `Open Emitter` |
| R、C、L、晶振、二极管、开关、连接器 | `passive` | `Passive` |
| NC | `no_connect` | `Undefined`（并在原理图打 NC 标记） |
| 不确定 | `unspecified` | `Undefined` |

- 被动件一律 `passive`；连接器的脚通常也是 `passive`，除非它就是电源入口（这时板上要有 PWR_FLAG 或驱动源）。
- 转换器给的类型不可信：实测 `kicad lcsc --import C6186` 的四脚全是 `unspecified`、`C25744`（电阻）两脚是 `input`。
  `kicad sch-build` 会把被动类导入件改成 `passive`，有源件不改——稳压器的 VIN/VOUT 仍是 `unspecified`，ERC 因此查不出“电源输入未驱动”。
  需要 ERC 覆盖时用 `kicad sch-build` 的 `pins[]`（带 `type`）生成符号，或请用户在符号编辑器改类型。
- `power_in` 的隐藏电源脚会按名字自动连网（KiCad 旧库常见），自建符号不用隐藏电源脚。

## 4. 符号外形

- 栅格：KiCad 引脚端点在 100 mil（2.54 mm）栅格、脚长 100 mil 或 150 mil；EasyEDA 坐标以资产自身单位为准，引脚端点落在其编辑器栅格上。
- 布局：输入/电源在左或上，输出在右，地在下；同功能分组（电源、时钟、总线、GPIO）。多单元器件（运放、门）把电源单独成一个单元。
- 1 脚：引脚号显示；必要时在 1 脚侧加独立的小圆标记，不把它画成反相圆圈。低有效用名字上划线（KiCad `~{RST}`），不靠引脚形状猜。
- 外框闭合、引脚线落在外框上、名字在框内；`Reference` 在上方、`Value` 在下方；`Footprint`、`Datasheet`、供应商号字段隐藏。

## 5. 封装：焊盘、阻焊、钢网

- 焊盘尺寸优先取手册的推荐焊盘图；没有时按 IPC-7351 由引线尺寸与公差计算，并写明密度等级与假设。器件本体/引线尺寸不是焊盘尺寸。
- 单位与原点：KiCad 原点放在器件中心（贴片坐标就是这个点）；EasyEDA footprint spec 用 mil（`1 mm = 39.37007874 mil`）。
- 阻焊开窗与钢网默认跟随焊盘（由板级规则统一扩展），只有手册要求时才在封装里单独设。大 EP 的钢网按手册分块（常见 50–80% 覆盖），
  避免浮高或桥连。
- 封装类型：KiCad 封装属性设 SMD / 通孔（`kicad fab` 的 CPL 走 `kicad-cli pcb export pos`）；实测 EasyEDA 转来的封装类型是 unspecified。
- 零度方向：KiCad 自带库遵循 IPC-7351 零度方向（例如 SOT-223 的 1 脚在左上）；转换来的封装可能不同，影响 CPL 旋转——
  在贴片预览里核对，修正走 `kicad fab --cpl-overrides`，见 [kicad-jlc.md](../../pcbpilot/references/kicad-jlc.md)。

## 6. 封装：courtyard、fab、丝印、Pin-1

| 层 | 内容 | 约定 |
|---|---|---|
| Courtyard（F.Courtyard / 禁布外框） | 器件本体 + 焊盘的外包络 + 间隙 | 常用间隙 0.25 mm（密集板 0.1 mm、BGA 1.0 mm 等按 IPC 等级）；闭合；DRC 用它查器件间距 |
| Fab（F.Fab） | 器件本体实际外形、Pin-1 倒角、`${REFERENCE}` | 按手册本体尺寸画，线宽约 0.1 mm；装配图用 |
| 丝印（F.Silkscreen） | 外形提示 + Pin-1 标记 + 位号 | 不压焊盘（离焊盘 ≥ 0.15–0.2 mm）；线宽约 0.12–0.15 mm；不画在器件下方被盖住还以为可读 |
| Pin-1 / 极性 | 丝印圆点或缺角、二极管阴极条、电容正极 `+` | 放在本体外、贴片后仍可见的位置；与 Fab 层倒角同侧 |

- 丝印与 Fab 层不能矛盾（例如丝印 Pin-1 在左上而 Fab 倒角在右下）。
- 器件下方有禁布（天线、晶振下方禁走线）时写成封装内的 keepout/rule area；EasyEDA 用 `lib footprint region`（仅可写的非系统库）。

## 7. KiCad 库表注册

- 两张表：`sym-lib-table`（符号库）与 `fp-lib-table`（封装库）。**全局**表在用户的 KiCad 配置目录，指向 KiCad 自带库
  （`${KICAD10_SYMBOL_DIR}`、`${KICAD10_FOOTPRINT_DIR}`，KiCad 10 实测）；**工程**表在工程目录，只对该工程生效。
- 工程自带的库一律写进工程表，路径用 `${KIPRJMOD}` 相对路径，工程目录整体拷走仍可打开：

  ```text
  (sym_lib_table
    (version 7)
    (lib (name "lcsc") (type "KiCad") (uri "${KIPRJMOD}/lib/lcsc.kicad_sym") (options "") (descr "project parts"))
  )
  (fp_lib_table
    (version 7)
    (lib (name "lcsc") (type "KiCad") (uri "${KIPRJMOD}/lib/lcsc.pretty") (options "") (descr "project parts"))
  )
  ```

- 库昵称（`name`）必须与符号 `Footprint` 字段里的前缀一致（`lcsc:R0402` → 昵称 `lcsc`），否则 ERC 报 `footprint_link_issues`、
  PCB 更新时找不到封装。不用绝对路径、不用用户名目录，不把工程库写进全局表。
- 谁来写：`kicad sch-build` 为它生成的工程写好两张表（已实测，`${KIPRJMOD}/lcsc.kicad_sym`、`${KIPRJMOD}/lcsc.pretty`）；
  `kicad lcsc --import` **不写**库表（实测只产出 `.kicad_sym` 与 `.pretty`）。已有工程要加表项时，把上面的条目交给用户在
  KiCad「偏好设置 → 管理符号库 / 管理封装库 → 工程专用库」里添加，Agent 不改全局表，也不手写工程表文件。
