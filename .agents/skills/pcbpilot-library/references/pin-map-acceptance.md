# 引脚映射验收记录（pin-map acceptance）

> 用途：任何新建、导入、复制或换绑的器件，在被原理图/PCB 使用前都要有一份这样的记录。
> 它回答“符号的第 N 脚、封装的第 N 个焊盘、数据手册的第 N 根引线是不是同一根”。
> 结论词汇按 [evidence-and-verdicts.md](../../pcbpilot/references/evidence-and-verdicts.md)。

## 目录

1. [什么时候必须做](#1-什么时候必须做)
2. [记录模板](#2-记录模板)
3. [封装图视角与镜像纪律](#3-封装图视角与镜像纪律)
4. [逐引线记录的填法](#4-逐引线记录的填法)
5. [重复、机械与裸露焊盘](#5-重复机械与裸露焊盘)
6. [建库后回读](#6-建库后回读)
7. [放一个一次性实例检查](#7-放一个一次性实例检查)
8. [拒绝条件](#8-拒绝条件)

## 1. 什么时候必须做

- `lib device build` 之前（EasyEDA 自建）；`kicad lcsc --import` 之后、首次使用之前（KiCad 导入）。
- 换绑：`sch rebind-symbol` / `sch rebind-footprint` / `sch replace` / `kicad sch-edit replace_part` 的新旧件编号不同或封装变体不同。
- 复用别人的库件（个人库、工程库、网上下载）时，只要这份记录不存在就当作未验收。
- 系统库/官方库里**同一完整 MPN、同一封装后缀**的件可以只做第 6、7 节（回读 + 实例），但仍写出处。

工具的通过信号都不等于本记录：`lib device validate` 的 `pinMappingVerified:true` 只说明 symbol pin 编号集合与
footprint pad 编号集合相等（实测：把 1 脚改名、改电气类型后仍然通过）；`kicad lcsc --import` 成功只说明转换器跑完。

## 2. 记录模板

每个器件一份，放在工程旁（例如 `parts/<MPN>.pinmap.md` 或 Device JSON 的 `evidence` 旁），交付时引用：

```text
器件       厂商 + 完整 MPN（含封装/包装/温度后缀）  LCSC/供应商号  数据手册（URL 或文件 + 版本/日期 + SHA-256 可选）
封装       厂商封装代号 + 尺寸（例 SOT-223-3, 6.5×3.5 mm）  图页码：pin table p.__ / package drawing p.__ / land pattern p.__
视角       封装图是 TOP VIEW / BOTTOM VIEW（原文标注抄下来）；Pin-1 标识形态（圆点/倒角/缺口/条纹）
库件       符号 <库:名> 、封装 <库:名>（或 EasyEDA libraryUuid/uuid）
逐引线     见第 4 节表格
例外       重复编号焊盘、机械焊盘、EP、NC/DNU 的处理与理由
回读       命令 + 结果摘要（第 6 节）
实例       一次性实例的位置与检查结果（第 7 节）
结论       PASS / INCOMPLETE / FAIL + 验证状态（source-only / offline-verified / live-verified）
```

## 3. 封装图视角与镜像纪律

- 先找图上的视角字样。多数 IC 的 pin 图是 **TOP VIEW**（从元件面往下看，即 PCB 顶层的视角）；QFN/BGA/LGA
  的焊盘图、部分模块和连接器的 footprint 图常是 **BOTTOM VIEW**；推荐焊盘图（land pattern）通常已是顶视。
- PCB 封装一律按**顶视**画。底视图要先左右镜像再读编号：底视里逆时针排的编号在顶视里是顺时针。
  只镜像一次，并在记录里写“已由 BOTTOM VIEW 镜像”；从不同页取数据时逐页写清各自视角。
- 编号方向：DIP/SOIC/QFP/QFN 顶视 Pin-1 起**逆时针**编号；BGA 用行字母（跳过 I、O、Q、S、X、Z 等，以手册为准）+ 列数字，
  A1 在顶视左上角。和这个常识不符时以手册为准，并在记录中注明。
- 连接器分清“板端座子”与“线端插头”：同一个系列的两半编号互为镜像；记录写明是哪一半、从哪一面看（mating face / PCB side）。
- 底层贴装件仍按顶视建库，由 EDA 在放到底层时翻转；不要为底层件另建一个镜像封装。
- 分立件也有引线顺序，不靠惯例：SOT-23 三极管/MOSFET 的 1/2/3 对应 B/E/C 或 G/S/D 因型号而异；二极管阴极、
  LED 阴极、钽/电解电容的正负极、SOT-223 的散热片归属都按手册逐脚确认。

## 4. 逐引线记录的填法

每一根物理引线一行，按封装编号顺序，不跳号：

| 手册引线号 | 手册引脚名 | 焊盘号（封装） | 符号脚号 | 符号脚名 | 电气类型 | 备注 |
|---|---|---|---|---|---|---|
| 1 | GND/ADJ | 1 | 1 | GND | power_in / Power | |
| 2 | VOUT | 2 | 2 | VOUT | power_out / Power | |
| 3 | VIN | 3 | 3 | VIN | power_in / Power | |
| Tab | VOUT | 4 | 4 | VOUT | power_out / Power | 散热片与 2 脚同网 |

- “焊盘号”必须等于“符号脚号”——EDA 只按编号连接，名字只给人看。
- 电气类型按 [library-conventions.md](library-conventions.md#3-引脚电气类型) 选，不照抄转换器给的类型。
- 同名多脚（多个 GND、VDD）逐个编号写，不合并；EasyEDA 符号同一编号只能出现一次。

## 5. 重复、机械与裸露焊盘

- **散热片 / EP 有电气连接**（接 GND、VOUT 等）：给它一个符号脚，让它进网表。EasyEDA 要求 pin/pad 编号唯一，
  用独立编号（例如 `4`、`EP`）并在符号上同名；KiCad 库可以用重复焊盘号（KiCad 自带 `SOT-223-3_TabPin2` 的散热片就编为 2），
  两种做法都要写进记录。只放进 `pinMapping.footprintOnly` 而没有符号脚的 EP 是**无网**的，铺铜不会连上它。
- **纯机械焊盘**（固定脚、屏蔽壳焊脚不接地时、定位柱）：无编号或编号留空/`MP`，写进 `footprintOnly` 并注明“机械、无网”。
  屏蔽壳要接地时它就不是纯机械焊盘。
- **EP 上的散热过孔**：按手册推荐数量与孔径；EasyEDA 封装内嵌的无网过孔放到板上后用 `pcb via-bond` 赋网。
- **NC / DNU**：NC 照建符号脚（类型 no_connect / Undefined），DNU（Do Not Use）不接任何网，记录写清“不得接地”还是“可接地”。
  不把手册没说是 NC 的脚当 NC。

## 6. 建库后回读

回读的对象是**写入后的库文件或库资产**，不是自己的 spec。逐行对照第 4 节表格。

EasyEDA（需已连接的编辑器）：

```bash
pcbpilot lib device get --uuid <device> --library <lib>      # 绑定的 symbol / footprint
pcbpilot lib symbol get --uuid <symbol> --library <lib>      # pins：number / name / pinType
pcbpilot lib footprint get --uuid <fp> --library <lib>       # pads：number / layer / x / y / shape
```

KiCad（只读；不改库文件）：

```bash
mkdir -p render                                                              # kicad-cli 不建输出目录（"Error creating svg file"）
kicad-cli sym export svg -o render/ -s <符号名> lib/lcsc.kicad_sym           # 看脚名、脚号、1 脚位置
kicad-cli fp export svg -o render/ --fp <封装名> --sp -l F.Cu,F.Silkscreen,F.Fab,F.Courtyard lib/lcsc.pretty
```

焊盘的数值回读用 KiCad 自带 python（`python -I`，脚本放在工程外的临时目录）：

```python
import sys, pcbnew
fp = pcbnew.FootprintLoad(sys.argv[1], sys.argv[2])            # <lib>.pretty 目录, 封装名
print("type", {0: "unspecified", 1: "THT", 2: "SMD"}.get(fp.GetAttributes() & 3))
for p in fp.Pads():
    q, s = p.GetPosition(), p.GetSize()
    print(p.GetNumber(), round(pcbnew.ToMM(q.x), 3), round(pcbnew.ToMM(q.y), 3), round(pcbnew.ToMM(s.x), 3), round(pcbnew.ToMM(s.y), 3))
```

实测（KiCad 10.0.6，`kicad lcsc --import C6186`）：转换出的 SOT-223 封装散热片编号 4、1 脚在右下（与 KiCad 自带
`SOT-223-3_TabPin2` 的 1 脚左上、散热片编号 2 方向相反）、封装类型 unspecified、符号四脚类型全是 unspecified。
这些都不是“错”，但会影响 ERC、CPL 旋转和贴片预览，必须写进记录。

## 7. 放一个一次性实例检查

库级回读看不到“放到图上以后能不能接线、网表里落到哪个焊盘”。在**工程外的临时目录**放一个实例：

- KiCad：写一个只含该器件的最小 spec（每个功能脚一个网，按脚名引用，例如 `U1:VOUT` 会同时取 2、4 脚），
  `pcbpilot kicad sch-build --spec t.json --out <tmp>/build` → `pcbpilot kicad sch-read --project <tmp>/build`
  核对每个网落到的 `REF:脚号` → `kicad-cli sch export svg -o <tmp>/render <tmp>/build/<name>.kicad_sch` 看图。
- EasyEDA：在用户同意的草稿页 `pcbpilot sch place --lib <l> --uuid <device> --x … --y …`，接线后 `sch check` /
  `sch connectivity` 读回脚号与网，再 `pcb import-changes` 到草稿 PCB 看焊盘编号与 1 脚位置；检查后删除草稿实例并回读确认。
  没有草稿页时不在用户的正式页面上试放，该项记 `INCOMPLETE`。

## 8. 拒绝条件

下面任一项成立就停止建库/导入，结论 `INCOMPLETE`，列出候选与页码请用户确认，不猜：

- 完整 MPN 对应多个封装，或订购后缀无法确定封装；
- pin table 与 package drawing 冲突，或图上没有视角标注且无法从 Pin-1 标识推断；
- 推荐焊盘图缺失且没有可写明依据的计算方法；
- EP / 散热片 / 屏蔽壳的电气归属手册没写或前后矛盾；
- 回读结果与记录不一致（编号、名字、类型、焊盘数、1 脚位置任一）；
- 数据手册不可读（扫描件看不清、链接失效）——换原厂 PDF，不凭图形轮廓估尺寸。
