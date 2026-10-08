# KiCad → 嘉立创（JLCPCB）：LCSC 器件、库与生产文件

本页覆盖 KiCad 10 设计流里与嘉立创相关的三件事：按 LCSC C 号选件并写入设计、
为 C 号拿到 KiCad 符号/封装、导出 JLCPCB 要求的 Gerber/钻孔/BOM/CPL。
器件必须是嘉立创/立创商城（LCSC）可贴的料（“用嘉立创来搜索元器件”）。

调研与实测日期：2026-10-09，KiCad 10.0.6 / 10.0.7（macOS）。来源均为官方页面或
仓库元数据，见文末“来源”；未能核实的内容明确标为“未核实”。

## 结论（推荐路径）

```
选件   pcbpilot kicad lcsc --search "10k 0402"          # JLC SMT 目录：C 号/basic/库存/封装
建库   pcbpilot kicad lcsc --import C25744 --lib-dir lib # 嘉立创 EasyEDA 库记录 → KiCad 自带导入器
写号   pcbpilot kicad lcsc --pcb b.kicad_pcb --sch b.kicad_sch --set R1=C25744
检查   pcbpilot kicad lcsc --pcb b.kicad_pcb --sch b.kicad_sch --check
出图   pcbpilot kicad fab  --pcb b.kicad_pcb --sch b.kicad_sch --out fab/
```

推荐的符号/封装来源是 **嘉立创自己的 EasyEDA 器件库**（与 C 号一一对应，和 JLC
贴片库同源），转换**只用 KiCad 10 自带的 EasyEDA 导入器**（KiCad 官方代码，作为独立
进程/模块调用，pcbpilot 不包含任何转换实现）。理由：

- 数据源就是 JLC 贴片时比对的那份器件记录，封装/焊盘编号与 JLC 一致的概率最高；
- 转换器是 KiCad 官方的，不引入 GPL/AGPL 第三方代码，pcbpilot 保持 MIT；
- 已在 KiCad 10.0.7 上实测（见下文“原型实测”）。

## 1. 选件：`kicad lcsc --search`

复用 Skill 里唯一的 JLC 目录客户端 [`scripts/parts-select.py`](../scripts/parts-select.py)
（见 [part-selection.md](part-selection.md)），不新增 HTTP 客户端：

```
pcbpilot kicad lcsc --search "AMS1117-3.3"           # 在线（JLC SMT 目录），显式触发
pcbpilot kicad lcsc --search "100nF 0402" --offline   # 只查 standard-parts.json，不联网
pcbpilot kicad lcsc --search "10k 0402" --json --qty 500
```

输出列：LCSC 号、BASIC/ext、库存、封装（`componentSpecificationEn`）、MPN、描述。
排序沿用 parts-select：规格匹配 → 库存 ≥ 数量 → basic → preferred → 单价。
CLI 优先用已安装 Skill 里的脚本；Skill 未同步时“封装”列为空（旧脚本无该字段），
`pcbpilot skill sync` 后恢复。参数核验规则（单位、阻值门禁、手册优先）与
part-selection.md 相同，搜索排名不等于电气核验。

## 2. 为 C 号拿到 KiCad 符号/封装：可选途径对比

| 途径 | 性质 / 许可 | 本次核实 | 结论 |
|---|---|---|---|
| **KiCad 10 自带 EasyEDA 导入器** | KiCad 官方（GPL，作为外部程序调用）。pcbnew `PCB_IO_MGR` 有 `EASYEDA`（“EasyEDA / JLCEDA Std”）和 `EASYEDAPRO`（“EasyEDA / JLCEDA Pro”）两个 IO 插件，均实现 `FootprintEnumerate/FootprintLoad`；`kicad-cli sym upgrade` 能把 EasyEDA Std 符号 JSON 转成 `.kicad_sym` | 10.0.7 实测 Std 封装与符号转换成功；Pro（`.elibz`）未实测 | **推荐的转换器** |
| 嘉立创 EasyEDA 库记录（`lceda.cn/api/products/<C>/components`） | 嘉立创自家服务器，EasyEDA Std 编辑器使用的接口；**未公开文档**，可能变更 | 实测可用；`easyeda.com` 同路径本网络返回 403，`lceda.cn` 200；不存在的 C 号返回 `success:false, code 404` | 当前 `--import` 的数据源（显式触发） |
| EasyEDA Pro 官方 API 导出器件库 | `eda.sys_FileManager.getDeviceFileByDeviceUuid(uuid, lib, 'elibz'|'elibz2')`（pro-api-types 有文档）；C 号 → uuid 用 `pcbpilot lib by-lcsc` | 未实测：需要新的连接器 action 并重新导入连接器 | 完全官方的备选数据源（见“待办”） |
| LCSC 商品页 “EDA Models” | 立创商城 | C6186 页面只提供 “EasyEDA” 一个入口（同一份 EasyEDA 库），未见 KiCad 直接下载 | 不是独立来源；其他器件页是否有第三方格式**未核实** |
| JLCPCB 官方 GitHub | `JLCPCB/KiJLC`（0BSD，KiCad BOM/CPL 导出，2022 后无更新）、`JLCPCB/kicad_tools`（MIT）、`JLCPCB/JLCPCB-SMT-Assembly-Components-orientation-fix`（MIT，贴片方向修正指南） | 仓库元数据已查 | **JLC 没有官方 KiCad 符号/封装库** |
| JLC 帮助中心 KiCad 10 BOM/CPL 文章 | 官方 | 已读 | 推荐 Bennymeg 的 Fabrication Toolkit 插件（Apache-2.0）出 BOM/CPL；不涉及建库 |
| `easyeda2kicad`（uPesy） | **AGPL-3.0** | 许可已查 | 只能作为用户自行安装的独立进程使用；**不得 vendor、import 或复制代码进 pcbpilot** |
| `JLC2KiCad_lib`（TousstNicolas） | MIT | 许可已查 | 可作为外部工具；pcbpilot 不依赖 |
| `kicad-jlcpcb-tools`（Bouni） | MIT，KiCad 插件：在 KiCad 里搜 JLC 料、写 LCSC 号、出 BOM/CPL | 许可已查 | 适合 GUI 用户；与本流程互不冲突 |
| `CDFER/JLCPCB-Kicad-Library` | MIT，JLC basic 件预制库 | 许可已查 | 可选的现成库，覆盖面有限 |

第三方工具大多同样读取上面那个未公开的 EasyEDA 接口；它们的价值在转换器，而 KiCad
10 已自带官方转换器，所以 pcbpilot 不再需要第三方转换代码。

### `--import` 做了什么（原型实测）

```
pcbpilot kicad lcsc --import C6186 --import C25744 --lib-dir lib [--lib-name lcsc]
```

1. Go 侧从 `lceda.cn`（失败再试 `easyeda.com`）取该 C 号的 EasyEDA 库记录，并校验
   记录里的 `lcsc.number` 等于请求的 C 号。TLS 走系统证书（KiCad 自带 python 的证书库
   在有代理的网络里会校验失败，所以下载不放在 python 里）。
2. 封装：KiCad 的 `PCB_IO_MGR.EASYEDA` 插件读取 → `KICAD_SEXP` 插件写入
   `lib/lcsc.pretty/<封装名>.kicad_mod`。
3. 符号：`kicad-cli sym upgrade <记录>.json` 生成单符号库，改名为 `<MPN>_<C号>`、
   把 `Footprint` 字段指向 `lcsc:<封装名>`、加隐藏字段 `LCSC Part #`，合并进
   `lib/lcsc.kicad_sym`（同名符号就地替换，重复导入不会出副本）。
4. 一次性把 `lib/lcsc.kicad_sym` 与 `lib/lcsc.pretty` 加进工程的符号/封装库表。

实测（KiCad 10.0.7）：C6186 → `lcsc:AMS1117-3.3_C6186`，SOT-223 封装 4 个焊盘
（1/2/3 + 散热片 4），符号 4 引脚 GND/VOUT/VIN/VOUT；C25744 → `R0402` 封装 2 焊盘。
两个库都能被 `kicad-cli sym export svg` / `fp export svg` 正常加载。

限制：不带 3D 模型；导入件同时带 EasyEDA 的 `Supplier Part`（= C 号）字段，但 fab
只认下面列出的 LCSC 字段名，所以 `--import` 额外写入 `LCSC Part #`；用 KiCad GUI
自己导入的 EasyEDA 符号要再 `--set` 一次。**导入件仍须对照数据手册核对引脚与焊盘**，
转换正确不等于器件选对。

## 3. LCSC 号写在哪里：字段约定

`kicad fab` / `kicad lcsc --check` 按优先级接受这些字段（大小写不敏感）：
`LCSC Part #`（JLC KiCad 10 指南让用户新建的字段名）、`LCSC`、`LCSC Part`、
`JLCPCB Part #`。值必须是 `C` + 数字。

- 板上封装（`.kicad_pcb` 的 footprint property）与原理图符号都会读；给了 `--sch`
  时原理图优先（板子由原理图更新），两边不一致会告警并提示 “Update PCB from Schematic”。
- 原理图经 `kicad-cli sch export bom` 读取，层次图由 KiCad 解析；**kicad-cli 会跳过
  没有 `lib_symbols` 定义的符号**（测试夹具因此带了最小符号定义）。
- 板上有、原理图 BOM 里没有的封装（板上独有件，或只在原理图里勾了“排除 BOM”）
  不会被悄悄丢掉：按板上字段继续门禁并告警，应先同步原理图与 PCB。
- `--set REF=Cxxxx`：板子经 pcbnew 加载并由 KiCad 自己保存（新字段隐藏、放 Fab 层）；
  原理图（含子图）做最小文本修改——已有 LCSC 类字段只替换值字符串，没有则在最后一个
  property 后插入一个隐藏的 `LCSC Part #`，其余字节不变。改之前先在 KiCad 里关闭文件。
  所有文件里都找不到的位号以非零退出。

## 4. 生产文件：`kicad fab`（JLC 官方设置）

设置来自 JLC 帮助中心《How to generate Gerber and Drill files in KiCad 9》（2026-09-24 更新，
带 JLC 要求表）与《KiCad 8》（2026-09-09），BOM/CPL 来自《How To Export BOM and CPL
Files From KiCad 10》（2026-09-09，KiCad 10.0.3 验证）。

| 项 | JLC 要求 | pcbpilot 的 kicad-cli 参数 |
|---|---|---|
| Gerber 格式 | RS-274X，Protel 扩展名 | kicad-cli 默认（不加 `--no-protel-ext`） |
| 坐标 | 4.6，mm | `--precision 6` |
| X2 / 网表属性 | 勾选（KiCad 9 文章，利于 DFM 查短路开路） | 默认开启（不加 `--no-x2/--no-netlist`） |
| 丝印减阻焊 | 勾选（KiCad 8 文章） | `--subtract-soldermask` |
| 铺铜 | 出图前检查/重灌 | `--check-zones`（只在内存里重灌，不改源文件） |
| 层 | F.Cu、全部内层**按叠层顺序**、B.Cu、F/B.Mask、F/B.Silkscreen、F/B.Paste、Edge.Cuts | 从 `(layers …)` 表按名字排序（不按层 id：B.Cu 在 KiCad 9 起是 2，以前是 31） |
| 钻孔 | Excellon，mm，十进制，绝对原点，椭圆孔“alternate”模式 | `--format excellon --excellon-units mm --excellon-zeros-format decimal --drill-origin absolute --excellon-oval-format alternate` |
| PTH/NPTH | **不合并**（KiCad 9 文章 FAQ：分开的 -PTH.drl / -NPTH.drl 直接接受） | `--excellon-separate-th` |
| 钻孔图 | 可选，便于人工检查 | `--generate-map --map-format gerberx2`，留在 `gerber/` 但不进 zip |
| 上传 | 一个 zip/rar，英文文件名 | `OUT/<板名>-gerber.zip`，只含 Gerber 与 .drl |
| BOM | 至少 Comment、Designator、Footprint、JLCPCB/LCSC Part #；csv/xls/xlsx | `OUT/<板名>-bom.csv`：`Comment,Designator,Footprint,LCSC Part #`，按 (值, 封装, C 号) 分组 |
| CPL | Designator、Mid X、Mid Y、Rotation、Layer；mm | `kicad-cli pcb export pos --format csv --units mm --side both --exclude-dnp` 改列名 → `OUT/<板名>-cpl.csv` |

门禁：每个**贴装件**（非 DNP、未排除 BOM、未排除坐标文件）都必须有合法 C 号，否则
列出全部缺号/非法件、非零退出，且**不写任何文件**（失败的运行不会看起来像完成）。
只做光板时用 `--no-assembly`（只出 Gerber/钻孔 zip，不检查 C 号）。真实板 PicoRick
revA 的板文件没有任何 LCSC 字段，`kicad fab` 正确地列出 145 个缺号件；
`--no-assembly` 产出 4 层 13 个文件的 zip（GTL/G1/G2/GBL/GTS/GBS/GTO/GBO/GTP/GBP/GM1 +
PTH/NPTH）。安装孔这类封装若没勾“排除 BOM/坐标文件”也会被要求 C 号——去 KiCad 里勾上，
不要随手填号。

### CPL 旋转角：不发明偏移表

CPL 的坐标与角度就是 KiCad 的封装位置/角度（底层件的角度也原样输出，例如板上 270°、
pos 报 -90°，写成 270）。JLC 贴片库对每种封装有自己的“零度方向”，与 KiCad 封装库
的零度可能不同（常见：SOT-23、有极性件、连接器、底层件），所以部分器件在 JLC 的贴片
预览里会显示转了 90°/180° 或偏移。JLC 官方仓库
`JLCPCB-SMT-Assembly-Components-orientation-fix` 的做法也是：在预览里看，改 CPL 对应
行的角度/坐标后重新上传；不改时 JLC 工程师也会人工处理并邮件确认。

pcbpilot **不内置**任何按封装的角度修正表（无法从官方来源确认，猜的表比不修更危险）。
修正走覆盖文件钩子，位号条目优先于封装条目：

```json
{"refs": {"U3": {"rotate": 180}},
 "footprints": {"Package_TO_SOT_SMD:SOT-23": {"rotate": 180},
                "SOT-223-3_TabPin2": {"rotate": -90, "dx": 0, "dy": 0}}}
```

```
pcbpilot kicad fab --pcb b.kicad_pcb --out fab/ --cpl-overrides jlc-rot.json
```

`rotate` 加到 KiCad 角度上（结果归一到 0–360），`dx/dy` 以 mm 加到 Mid X/Y；拼写错误的
键会被拒绝。实际用到的覆盖写进 `fab-report.json` 并打印。覆盖值必须来自本板在 JLC
预览里的观察，记录在工程里，不能从别的板照抄。

## 5. 输出与核对

- `OUT/fab-report.json`：层清单、zip 内文件、BOM 行数、CPL 件数、告警、已用覆盖。
- 上传前用 Gerber 查看器（或 JLC 上传后的在线预览）核对：板框闭合、内层顺序、孔与层
  对齐、过孔盖油与设计一致、丝印正常（JLC KiCad 8/9 文章的检查清单）。
- 上传 BOM/CPL 后在 JLC 的贴片预览逐个看方向，再决定是否写覆盖文件。

## 测试与环境

- 单元测试（`go test ./internal/kicad/ ./internal/app/ -run KiCad`）只用仓库内自写的 MIT
  夹具 `internal/kicad/testdata/fab/`（4 层板 + 两级原理图），不需要 KiCad。
- `PCBPILOT_KICAD_LIVE=1` 打开 KiCad 实机测试（出图、pcbnew 写字段、层次原理图 BOM、
  `--import` 联网转换）。
- 可执行文件：`PCBPILOT_KICAD_CLI`、`PCBPILOT_KICAD_PYTHON` 覆盖；默认找
  `/Applications/KiCad/KiCad.app/.../kicad-cli` 与
  `.../Python.framework/Versions/*/bin/python3`（版本目录随 KiCad 升级变化，按通配查找）。

## 待办 / 未核实

- EasyEDA Pro 官方 API 路线（`lib by-lcsc` → `getDeviceFileByDeviceUuid(…,'elibz')` →
  KiCad `EASYEDAPRO` 导入）需要新增只读连接器 action，未实现、未实测。
- `lceda.cn` 组件接口无公开文档，若失效 `--import` 会明确报错；届时改走上一条。
- LCSC 其他商品页是否提供第三方格式（如 KiCad）下载：未核实。
- JLC 帮助中心关于 CPL 旋转的专门文章（`why-the-component-rotation-in-the-cpl-file-is-incorrect`）
  页面为前端渲染，本次未能取得正文，结论依据 JLC 官方 GitHub 指南。

## 来源

- JLCPCB Help：How to generate Gerber and Drill files in KiCad 9 / KiCad 8 / KiCad 7
  （jlcpcb.com/help/article/how-to-generate-gerber-and-drill-files-in-kicad-9 等）
- JLCPCB Help：How To Export BOM and CPL Files From KiCad 10
  （jlcpcb.com/help/article/how-to-generate-the-bom-and-centroid-file-from-kicad）
- github.com/JLCPCB/JLCPCB-SMT-Assembly-Components-orientation-fix（README）
- GitHub API 许可元数据：JLCPCB/KiJLC、JLCPCB/kicad_tools、uPesy/easyeda2kicad.py、
  TousstNicolas/JLC2KiCad_lib、Bouni/kicad-jlcpcb-tools、bennymeg/Fabrication-Toolkit、
  CDFER/JLCPCB-Kicad-Library
- KiCad 10.0.7：`kicad-cli pcb export gerbers|drill|pos --help`、`sch export bom --help`，
  pcbnew `PCB_IO_MGR`（`ShowType(EASYEDA)` = “EasyEDA / JLCEDA Std”，`EASYEDAPRO` = “EasyEDA / JLCEDA Pro”）
- `@jlceda/pro-api-types`：`SYS_FileManager.getDeviceFileByDeviceUuid`
