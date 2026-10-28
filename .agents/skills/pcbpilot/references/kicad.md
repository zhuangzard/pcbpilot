# KiCad 后端 — 快照、网表、fastroute 布线与全套门禁（`pcbpilot kicad`）

> 2026-10-09 用户决定：EasyEDA 不稳定，原理图 + PCB 改在 **KiCad 10**（已验证 10.0.6 / 10.0.7）上做；
> 器件仍用 LCSC C 号；整板布线仍是外部 fastroute（GPLv3，只作独立进程，pcbpilot 不打包、不下载、
> 不链接）。同日用户补充：**EasyEDA 流程的每一项计算和门禁都必须在 KiCad 流程里照样执行**，
> 设计意图 / 仿真 / 安全距离 / 设计评审 / 设计报告 / 说明书都是硬门禁。本文件只讲 KiCad 侧；
> fastroute 安装与参数见 [pcb-routing.md](pcb-routing.md#external-router-fastroute)，意图来源见
> [design-intent.md](design-intent.md)。

> 原理图：用 `kicad sch-build`（spec → 完整工程，一次调用，见 [kicad-sch-build.md](kicad-sch-build.md)），
> 不再逐步调用 `sch place / autoconnect --backend kicad`。
> 仍需逐件改单张图时（`--backend kicad --kicad-sch X`），多单元器件每个单元是一个实例：`sch place` 同一
> `--designator` 加 `--unit 2/3…` 逐个放（新单元继承已放单元的字段；已放单元之间字段不一致，或
> `--value/--footprint/--lcsc` 与之不同、已放单元没有，都拒绝——先 `sch modify --id U1` 给全部单元设上）；
> `sch modify --id U1` 的字段与改位号作用于全部单元，移动要写 `--id U1:2`（只写 `U1` 且有多个单元时拒绝）；
> `sch prim-delete --ids U1` 删全部单元，`U1:2` 只删该单元；`REF.PIN` 可指任何已放单元的脚（按实例的
> body style 取脚位）。改位号（`--designator` 或 Reference 字段）先校验：非空、无 `:` `.` 空白/控制字符，且不与
> 本图或工程其他图纸上的位号重名，否则拒绝且文件不变。**层次图**：一个件的单元分在几张图上时，改字段/改位号/
> 删全部单元一律拒绝（先把单元放到同一张图）；被多个图纸符号复用的子图上不放置、不改位号、不删除（一个符号就是各次复用的 U201/U301；
> 改字段时其每个复用位号都要通过上面的跨图检查）。
> 层次从 `.kicad_pro` 的根图沿图纸树遍历（含子目录里的子图，硬/软链接算同一张图）；找不到工程、多个工程
> （含嵌套工程：子目录工程的根图又是外层工程的子图）都含该图或有图纸读不了时，
> 放置/改位号/删全部单元拒绝（报出原因），普通字段修改照常。

## 工作方式

`pcbpilot kicad` 不经 daemon / 连接器，直接对 `.kicad_pcb` / `.kicad_sch` 文件工作：

```
pcbpilot ──▶ KiCad 自带 Python + pcbnew（internal/kicad/bridge.py，go:embed）
         │     snapshot / netclasses / rules / ripup / dsn / ses / pours / edit / fill
         │     sync-read / fp-info / sync-apply（kicad pcb-sync；只写进暂存目录）
         ├─▶ KiCad 自带 Python + pcbnew（internal/kicad/board.py）：kicad board 的 new / outline / stackup /
         │     holes / keepout / zone / text
         ├─▶ kicad-cli pcb drc / sch export netlist（`kicad drc` / `fab --preflight` 在副本上跑 DRC）
         ├─▶ fastroute（外部进程）
         └─▶ 共用的 EDA 中立计算：intentRequirements、planViaArrays、planWidenToIntent、
             planSilkTight、sim post-layout、snapshotIntentGates、tailGates、planWidenIR、
             report design、review-panel
```

- KiCad 定位：`$PCBPILOT_KICAD_APP`（macOS 默认 `/Applications/KiCad/KiCad.app`，Linux 默认前缀 `/usr`，
  Windows 默认最新的 `C:\Program Files\KiCad\<版本>`）；`$PCBPILOT_KICAD_PYTHON`、`$PCBPILOT_KICAD_CLI`
  可单独覆盖。macOS 的 Python 按 `Python.framework/Versions/*/bin/python3` 通配查找（升级后版本号会变）。
  Linux / Windows 路径是尽力而为，未现场验证。KiCad 安装/升级途中 `kicad-cli` 会被系统杀掉（退出码 137），等装完再跑。
- bridge 每个子命令在 stdout 只输出一行 `PCBPILOT_JSON:{...}`；pcbnew 的噪声（wx assert、swig 内存泄漏提示，
  偶尔与结果同一行）忽略，Go 侧取最后一个标记之后的 JSON。
- **输入板永不改写**：`kicad route` 把 `--pcb` 及同名 `.kicad_pro` / `.kicad_dru` 复制进 `<out-dir>/work/`，
  每一步写一块新的工作板（`01-rules`、`02-classed`、`03-imported`、`04-via-arrays`、`05-widen`、`06-pours`、`07-silk`…），
  最终板复制到 `<out-dir>/routed.kicad_pcb`。
- pcbnew 的 `SaveBoard` **不覆盖**已存在的 `.kicad_pro`（10.0.6 实测），bridge 写新板前先删目标的 `.kicad_pro/.kicad_prl`；
  swig 不暴露 netclass pattern 列表，用户 pattern 从 `.kicad_pro` 读回保留。

- **bridge 写操作全有或全无**：`edit`（加线/过孔、改线宽、丝印、`padZoneNone`）、`pours` 先校验全部网名、
  层号（必须是精确整数，`1.9`/`true` 拒绝；丝印文字层只能 3/4）、数值字段、对象 id 与位号；任一网不在板上（不会再静默落到 0 号无网）、id 不存在或位号对应多个封装，整批拒绝、
  **不保存**，错误列出每一项（大小写不同的网名给出 `did you mean`）。`padZoneNone` 用且只用一个选择器指定封装：
  `ref`（只匹配位号）/ `id`（只匹配快照 primitiveId）/ `uuid`（只匹配封装 uuid），命名空间互不混用。各操作字段必须是数组（`null`/缺省 = 空；对象、字符串、数字一律拒绝），网名与 id
  必须是字符串；`setText` / `hideTexts` 可寻址快照 `silk` 里的每条文字（字段、封装图形文字、板上文字），但图形文字
  KiCad 不保存隐藏状态，`hideTexts` 对它直接拒绝。`fp:<uuid>:<序号>` 只对未改动的文件稳定（保存会重排封装），
  每次写入后重新取快照再寻址。`netclasses` 的意图网不在板上仍是忽略并告警（`missingNets`），绝缘/板边域里缺的网
  另列 `missingDomainNets` 并告警。
- **位号重复 / 占位位号**（两个 `H1`、多个 `REF**`/`G***` logo、未标注的 `R?`）：快照里各自独立成条（各有
  primitiveId 与 uuid，标 `duplicateDesignator` / `placeholder`），顶层 `duplicateDesignators` 列出位号与全部 id
  并在 `partial` 里说明（按位号索引的读者会混淆它们），占位位号另列 `placeholderDesignators`（不算降级），不再互相覆盖。`kicad place` 把这些封装作为固定障碍原地保留、不出位姿，报告 `notes` 逐个
  列出；place.py 收到重复位号或不存在的键时整批拒绝、不写板，位姿文件是带类型的记录
  `{"pcbpilotPoses":2,"poses":[{"ref":…}|{"uuid":…}, …]}`（Go：`kicad.PlaceTargets`），单个封装用 `uuid` 记录指定；
  旧的 `{位号: 位姿}` 仍可读，但键一律按位号解释（位号可以就叫 `uuid:…`）。要让它们参与
  布局，先在原理图/PCB 里重新标注唯一位号。

## 板级设置（`kicad board new|outline|stackup|holes|keepout|zone|text`）

无头建板：mm、KiCad 坐标（y 向下）。参数和推荐顺序见聚焦 Skill pcbpilot-kicad-pcb 的 `references/board-setup.md`；这里只记实现事实。

- 分工：Go（`internal/kicad/board.go`）校验数值/单位/几何（多边形自交、圆角半径、挖空在板内且互不重叠、孔互叠、
  层叠厚度合计）并把形状转成 KiCad 图元（线段、三点圆弧、圆）；board.py 校验要看板子的部分（已启用层、网、现有板框、
  减层时内层上的对象、孔压焊盘），改板、保存、**重新加载核对**。错误码 `E_BOARD_*`，带 `fix`。
- 提交：输入连同 `.kicad_pro`/`.kicad_dru` 先复制到暂存目录再改，结果旁边放回同一份工程/规则后才做 stats/DRC 校验；
  全部检查通过才经 kfile 事务提交（`--out` 新建板 + 校验过的工程/规则字节、`--in-place` 锁检查 + 基准哈希 CAS），源工程/规则
  提交前再比一次哈希。`--report` 不得与任何板/工程/规则/锁文件同名（含大小写、硬链接）。`outline` / `stackup` / `holes` 另用 `kicad-cli pcb export stats` 回读板尺寸、
  板厚、NPTH/PTH 数；加/改铜或孔的 op（outline/stackup/holes/keepout/zone）默认对比改前改后的 error 级 DRC 并以它为准
  （`--no-drc` = 未验证，退出码 5，只有 `--force-unverified` 才写），`text` / `new` 用 `--drc` 开。
- 层叠：KiCad 10.0.6 的 Python 没有导出 stackup（`GetStackupDescriptor` 是不透明对象），所以 `SetCopperLayerCount`
  + `SetBoardThickness` 后把 `(stackup …)` 块写进保存后文件的 `(setup …)`，再由 pcbnew 加载、保存一遍规范化并核对
  `m_HasStackup`、板厚与铜层列表。板厚按 KiCad 的算法含阻焊层。
- 几何是精确判定（`board_geom.go`：线段/圆弧距离、有符号距离包含，容差 0.001 mm），board.py 用同一套公式对板上
  Edge.Cuts（KiCad 的折线近似）判孔/挖空；孔的预检只拦确定的物理重叠（钻孔与钻孔、钻孔穿铜、焊环压别的网的铜；焊盘用 `GetEffectivePolygon` 的真实形状），间距全归 DRC。写后回读逐项比对
  每个请求属性；灌铜先 UnFill 再 `ZONE_FILLER.Fill`，返回失败/异常/仍未灌 → `E_BOARD_VERIFY`。
- 10.0 的 `GetLayerID` 不再认 `F.SilkS` 这类旧短名（返回 -1），board.py 自带旧名映射。安装孔是锁定、board-only、
  不进 BOM/坐标文件的单焊盘封装。`outline` / `holes` / `keepout` 遇到已灌铜的板会全部重灌。
- kicad-cli 的 DRC/stats 用隔离的空全局库表（`SchCLIEnv`）：Windows 上带完整全局 fp-lib-table 时一次 DRC 要
  2 分钟以上（小板也会卡住），隔离后约 2 秒。

## 快照（`kicad snapshot board.kicad_pcb [--out board.json]`）

形状与 `pcb dump --include-copper` 一致（Go：`kicad.Snapshot(pcbPath) ([]byte, error)`），`boardSnapshot`、
`pcbauto.FromSnapshot`、`postsim.ParseBoard`、`pcb check` 的 edge/isolation/via-current、丝印门禁、说明书都直接读：

| 项 | 约定 |
|---|---|
| 单位/坐标 | mil，**y 向上**（y = −y_KiCad）；旋转仍是度、逆时针 |
| 铜层号 | pcbpilot 的 EasyEDA 编号：F.Cu=1、B.Cu=2、InN.Cu=14+N（In1=15）、通孔焊盘=12 |
| 器件 | primitiveId（封装 uuid；**板上 uuid 重复时改用 `ref:<位号>`**，位号也重复时 `fp:<uuid>:<序号>`——转换来的板曾见 203 个封装只有 39 个不同 uuid）/ uuid / designator / value / footprint / `lcsc`（字段 LCSC、LCSC Part、LCSC Part #、JLCPCB Part #）/ layer / x,y,rotation / bbox / pads |
| 焊盘 | padNumber / net / layer / x,y / width,height（轴对齐外包）/ rotation / shape `[RECT\|ELLIPSE\|OVAL\|POLYGON, w, h]` / 通孔 holeDiameter |
| NPTH | 进 `footprintHoles`（圆孔 dia；槽孔为包络多边形） |
| 板框 | Edge.Cuts 合成多边形取面积最大者；内部挖空写成 layer 12 的 `copper.fills`（`edgecut-N`；`kicad place` 按其精确多边形挡件） |
| 铜 | `copper.lines`、`arcs`（arcAngle 逆时针为正）、`vias`、`pours`、`poured`（每个外轮廓一组 `[外轮廓, 孔...]`）、`regions`（禁放=2、禁走线/过孔=5、禁铺=7） |
| 丝印 | `silk`：每个封装的位号/值字段与丝印层文字，pcbSilkText 形状（BBox、FontSize、Rotation、Hidden、CompID）；字段 id 为 `<器件id>#ref` / `#value` |
| 规则 | clearance / track / 最小线宽 / 过孔 / 铜到板边 / 孔到孔（mil），source=`kicad`；另有 `netclasses[]`、`netClassOf{net: 有效网络类}`、`kicadLayers` |

## 网表（`kicad netlist --sch X.kicad_sch --out conn.json [--values values.json]`）

`kicad-cli sch export netlist --format kicadxml`（含层次子图）→ pcbpilot 原理图连接 IR（schemaVersion 1.4，
与 `sch connectivity` 同形：components+pins、nets、connections），`intent derive --connectivity` / `sim power` /
pad-net diff 直接读。单独挂在 KiCad `unconnected-(…)` 网上的脚不产生连接：带 no-connect 标记的记 `noConnected`，
否则 `connectionState: "unconnected"`。`--values` 写 `{"parts":{ref:{value,mpn,lcsc}}}` 给 `intent derive --values`。
从 KiCad 起步的设计由此得到与 EasyEDA 相同的意图 / 安全推导。每个器件都有 `pins` 列表（无引脚的安装孔为 `[]`）。

**网络身份（唯一规则，`pkg/netname`）**：网的身份是 KiCad 自己的完整层次名，按 KiCad 转义写法——与 `.kicad_pcb`、
`kicad snapshot` 完全相同：子图局部网 `/pic_sockets/VCC_PIC`，根图局部标签 `VPP/MCLR` 为 `/VPP{slash}MCLR`，
全局/电源网 `GND`。`kicad netlist` 每个网写 `fullName`（身份，新增字段）和 `name`（仅显示名：去掉图纸路径、
`{slash}` 还原为 `/`，在全设计唯一时才用短名，否则等于 fullName）。**绝不在标签内部的 `/` 处截断**（旧版把
`/VPP/MCLR` 截成 `MCLR`）。`unconnected-(…)` 是"无网"，两侧比较都按无网处理。匹配方（pad-net-diff、
`signoff` 的 continuity/trace、`sim post-layout` 的脚↔焊盘检查与铜网查找、`kicad route` 的网络类、intent-widths）
一律先用 `fullName`；只有 `name` 的旧文档 / intent / sim 用显示名解析到板上**唯一**对应的网，同名多图纸时不猜：
sim 判 `sim-board-mismatch`、intent 报错、signoff-trace 判"ambiguous"失败（不再退回到板上去找）。两个名字解析到同一块板网
（如 `VCC_PIC` 与 `/pic_sockets/VCC_PIC` 同时出现）一律拒绝，绝不合并。kicad-cli XML 不转义 `/`，`/pic_sockets/VCC_PIC`
可能是子图标签 `VCC_PIC`，也可能是根图标签 `pic_sockets/VCC_PIC`：读法只按标签证据定（各图纸文件里的标签/层次标签/图纸引脚
恰好只有一种读法存在）；引脚在哪张图不算证据（网经层次端口跨图）。无证据、多种读法都有标签、或两个网读成同一身份时
`kicad netlist` 直接报 `ErrNetAmbiguous`（附修法：改标签名）。旧连接文档（无 fullName）按每个原理图网（文档 + 网 id）解析；
多页文档按网名合并（与 sim power / intent derive 同规则：不同页同名 = 同一网，但同一页上同名的多个网记录各自独立）。
两个不同原理图网（同页两条 `VCC` 记录，或 `VCC` 与 `/pwr/VCC`）落到同一块板网时 pad-net-diff 报 `netCollision`、
signoff-continuity 失败（板上短路），不合并。显示名 `name` 只在能唯一解析回本网时才缩短，绝不等于别的网的身份或显示名。
pad-net-diff 的大小写宽容只用于 EasyEDA 名（无 fullName）；KiCad 网名区分大小写。

## 预览与视觉回归（`kicad render`）

```bash
pcbpilot kicad render --sch root.kicad_sch --out review/r1                 # 每页一张 SVG（<base>.svg、<base>-<子图路径>.svg）
pcbpilot kicad render --pcb board.kicad_pcb [--side bottom] [--layers F.Cu,F.Silkscreen] [--png --dpi 150] --out review/b1
pcbpilot kicad render --sch root.kicad_sch --out review/r2 --compare review/r1   # 出图后与基线比
pcbpilot kicad render --out build/render --compare old/render              # 只比（例如两次 sch-build --render 的输出）
```

- 在工程**临时副本**上跑 kicad-cli（原目录不出现 `.kicad_prl`/锁文件），用隔离的空库表配置（默认配色，不受用户主题影响）。
  副本 = 工程目录的 KiCad 文件 + 依赖闭包：沿层次找到的每张子图、`sym-lib-table`/`fp-lib-table` 里相对或 `${KIPRJMOD}` 路径的库、
  `.kicad_pro` 指定的图框（`.kicad_wks`），按相对位置镜像复制（不改写任何文件，`../shared/x.kicad_sch` 照样能找到）；绝对路径或
  KiCad 变量指向的库原地只读（`render.json` 的 `readInPlace`）。绝对路径子图、未定义变量、找不到的文件 →
  `RENDER_DEPENDENCY_UNRESOLVED`（cannot-check，逐项列出），不出半张图。
- **出图校验**：层次里每一页都必须产出能解析、且真正画了东西的 SVG（`<defs>`/`<clipPath>` 等之外有几何：带坐标的 path、有尺寸的
  rect/circle、有字的 text、指向有内容目标的 use；缺页 `RENDER_INCOMPLETE`，空图/坏图 `RENDER_BAD_SVG`）。两页或两层压平后文件名相同
  （子图 `a/b` 与 `a-b`、层 `F.Cu` 与 `F_Cu`、层名 `top` 与总图）出图前就判 `RENDER_INCOMPLETE`；
  kicad-cli 的输出保留进 `warnings`，任何带失败字样的行（failed / error / cannot / unable / permission denied / not found /
  missing…，在进度行过滤之前判）即使退出码 0 也判 `RENDER_KICAD_DIAGNOSTIC`；只有明确列出的无害行（Fontconfig、
  `Plotted to '…'`、`Done.`）放过。以上都是 cannot-check（退出 5），**上一版渲染原样保留**：新图先在暂存目录完成；发布时旧文件
  （含 `render.json`）先移进 `--out/.render-backup/`，新文件拷完、最后写 `render.json` 才删备份，中途失败就把旧渲染搬回。
  备份目录还在（发布中断且没能恢复）时新渲染判 `RENDER_OUT_PARTIAL`、`--compare` 判 cannot-check。工程目录本身是链接/联接时
  按其目标列目录、按逻辑路径复制。
- **确定性**：kicad-cli 每次出图只有 `<title>` 里的出图时间（以及偶尔回显的临时路径）会变；pcbpilot 只在生成的头部（`<title>` 与
  其后的 `<desc>`）去掉时间与临时路径，图中文字里出现的临时副本路径换回用户自己的目录，`-0` 只在数值属性/路径数据里改写，
  `<text>` 文字不动。同一输入两次出图逐字节相同（`TestRenderLive*` 在 pic_programmer 原理图+板、complex_hierarchy 上实测）。
  绘制顺序保留不排序。
- PCB：`<base>-pcb-<side>.svg`（全部 `--layers` 叠在板框范围内）+ 每层一张 `<base>-pcb-<层>.svg`（带 Edge.Cuts 定位）；
  默认层 `F.Cu,F.Mask,F.Silkscreen,Edge.Cuts`，`--side bottom` 用 B.* 并镜像。
- PNG：只有板有（`kicad-cli pcb render` 3D 光追，按板框尺寸 × `--dpi` 定像素）。光追**不可逐字节复现**（10.0.6 实测同一板两次
  像素不同），所以 `--compare` 只列出不比较。原理图没有 PNG：kicad-cli 不出原理图位图，pcbpilot 也不带 SVG 光栅化库。
  这一步的诊断同样阻断，只放过“3D 模型加载不了”这一类消息（可选模型，只影响预览）；PNG 必须能解码，尺寸略小于请求值
  （KiCad 10 实测 640x400 → 616x368，每边允许小 48 像素以内），`render.json` 记实际尺寸。
- `--out` 写 SVG + `render.json`（文件、sha256、绘制元素数）。再次渲染到同一目录只替换 `render.json` 列出的文件；目录里有其他
  文件/子目录/链接（`RENDER_OUT_NOT_EMPTY` / `RENDER_OUT_UNSAFE`），或 `render.json` 列了渲染器不会写的名字（只允许纯文件名
  `.svg`/`.png`，`RENDER_OUT_BAD_MANIFEST`），一律拒绝、什么都不删。`--compare` 与 `--out` 是同一目录（含链接/联接）也拒绝；
  `--report` 只收纯文件名（如 `report.json`，不带目录，不能叫 `render.json`）：随渲染一起在暂存目录生成、一起发布到
  `--out`，`render.json` 里记为 `kind: report`，`--compare` 忽略它；没给 `--out`、只做比较或渲染失败时报告只打到 stdout。
  链接形式的 `.kicad_pro`/库表按解析后的内容复制，解析不到判 cannot-check。kicad-cli 消息按整行判定（"Loading…" 开头或超长路径
  也不会漏判），只截短显示。路径数据只有 move 没有画线段的 path、被 `display="none"`/`visibility="hidden"`/不透明度 0 隐藏，或
  没有可见颜料（fill 与 stroke 都是 none，或只有宽度 0 的 stroke；属性或 style，含继承与 `<use>`）的图形都不算画了东西；fill 只在
  能围出面积的图形上算数（曲线/圆弧、三个不共线点、有尺寸的 rect/circle/ellipse），直线 path、两点 polygon 必须有可见 stroke。
  SVG 必须恰好一个 `<svg>` 根元素。**这个“画了东西”的检查只是防空白输出的启发式判断，不是完整的 SVG 渲染器**（不算裁剪、
  遮挡、变换到画布外等），通过它不等于画面正确。`--compare` 不跟随链接：链接目录或非普通文件的 `.svg` 记 unreadable（cannot-check）。
- `--compare` 逐文件：`identical`（规范化后字节相同）/ `changed`（结构化 SVG 比较：每个绘制元素连同排序后的属性、文字和所在
  `<g>` 的样式作多重集合比较，给 `elementsAdded/elementsRemoved/changedElements` 和几条样例；只换了绘制顺序记 `reordered`）/
  `added` / `removed` / `unreadable`（解析不了、没画东西——两份相同的空图也不算通过——或 `render.json` 列了却不存在）。
  `verdict` 三态：全部 identical → `pass`（退出 0），有差异 → `fail`（6），一侧没有 SVG 或有 unreadable → `cannot-check`（5）；
  出图失败同样 cannot-check（5）并给 `error.code/fix`，参数/`--out` 用法错误退出 1。两边渲染选项不同写进 `notes`。
- SVG 变化只说明“画面动了”，不说明设计对错：连接是否变化以网表（`kicad netlist` / `sch-read`）为证据。
- `kicad sch-build --render` / `sch-edit --render` 用同一个出图函数，`<out>/render/` 也是规范化的 SVG，可直接作 `--compare` 的两侧。

## 原理图 → PCB 同步（`kicad pcb-sync`）

KiCad 的 Update PCB from Schematic（F8）不再由用户在 GUI 里点：原理图改完（`kicad sch-build` / `sch-edit` / 用户在
KiCad 里改并保存）后一律用它把板子拉齐，然后 `kicad place`（移动暂存的新件）→ `kicad route --sch` → `kicad fab`。

```bash
pcbpilot kicad pcb-sync --sch board.kicad_sch --pcb board.kicad_pcb --check            # 只读：退出 3 = 不同步
pcbpilot kicad pcb-sync --sch board.kicad_sch --pcb board.kicad_pcb --dry-run          # 只读：看计划
pcbpilot kicad pcb-sync --sch board.kicad_sch --pcb board.kicad_pcb --out work/sync/board.kicad_pcb
pcbpilot kicad pcb-sync --sch board.kicad_sch --pcb board.kicad_pcb --in-place [--break-lock stale]
pcbpilot kicad pcb-sync ... --plan-out plan.json      # 给用户审，记下 revision
pcbpilot kicad pcb-sync ... --in-place --apply-plan plan.json --revision <rev>
```

- **输入**：默认现导 `kicad-cli sch export netlist --format kicadxml`（隔离配置，同 sch 命令）；`--netlist F
  [--netlist-format kicadxml|kicadsexpr]` 用现成导出。网名一律按 `pkg/netname` 身份（XML 的未转义名按标签证据解析）。
  封装库 = 工程 `fp-lib-table` 覆盖用户全局表（同昵称工程优先；禁用行跳过；`${KIPRJMOD}`、`${KICAD10_FOOTPRINT_DIR}`、
  `kicad_common.json` 变量展开；只支持 `.pretty`）。用的是用户**真实**库表，不是隔离配置。
- **匹配**（`--match path-then-ref` 默认 / `path` / `ref`）：封装 `(path …)` = 符号的图纸路径 tstamps + 单元 UUID；多单元
  符号是一个封装，带任一单元的 key 都算（新件用第一个单元的）；同一子图复用多次 = 各自独立的 key。路径匹配不上时，只有
  位号在**整块原始板上**（不只是剩下没匹配的封装里）和原理图里都唯一，才按位号匹配并 `setPath` 重新挂钩（报告 `matchedBy:"ref"`）。
- **拒绝（退出 1，什么都不写）**：未标注位号 `E_UNANNOTATED`、重复位号 `E_DUP_REF`、重复 key、板上两个封装同一路径
  `E_DUP_PATH`（含一个多单元符号的不同单元 key 落在两个封装上）、上板符号 Footprint 字段为空 `E_FP_MISSING_FIELD`、库解析不了 `E_FP_LIB_UNRESOLVED`（报出变量名/昵称）
  / `E_FP_LIB_TYPE`、库里没有该封装 `E_FP_NOT_FOUND`。修原理图或库表后重跑，不要绕。计划里的发现（如
  `E_PIN_PAD_MISMATCH`：网表引脚在封装上没有焊盘）让 `--check` / `--dry-run` / `--plan-out` 退出 1（`verdict: plan-errors`，
  不写计划），不会报 in-sync；应用时 pad-net 门禁失败。
- **操作**（固定顺序）：`renameNet`（板网的焊盘**恰好**全部去往一个新名字、该名字板上还没有、没有孤儿/板专用件的焊盘在其上
  → 改名，走线/过孔/铺铜/带网的板上铜图形跟着走；拆分、合并、目标名已占用，或网上有没有原理图引脚对应的焊盘（孤儿/板专用件的焊盘、无编号
  焊盘、孔）都不改名，逐焊盘改网；保守：`.kicad_pro` 里任何涉及新旧任一名字的 netclass
  指派或 pattern（按通配和正则两种读法）、`.kicad_dru` 里出现任一名字或任何不能按字面判断的网/网络类条件（`matches`、通配、
  差分对/成员函数），都让改名**整批拒绝** `E_RENAME_REFERENCED`，列出原因，提示先在 Board Setup 改或 `--rename-nets=false`）→ `addFootprint`（库里加载，按图纸路径
  再按位号排序，从板框右侧 10 mm 起成行暂存，`--staging X,Y` / `--staging-gap`）→ `swapFootprint`（保留 uuid、所在 group、位置/角度/面/锁定/
  字段文字布局/路径、封装级 clearance / 阻焊 / 锡膏 / 铺铜连接覆盖与 allow-missing-courtyard / solder-mask-bridge，
  以及每个焊盘的本地覆盖（含与该形状默认值不同的热焊盘辐条角度；带到同编号的新焊盘；同编号多个焊盘覆盖不同时按顺序 + 封装内相对位置一一对应，对不上或带覆盖的焊盘在
  新封装里没有 → 整批拒绝并列出）；新封装缺的焊盘 → `orphanCopper`，
  同编号焊盘换了网且旧网有铜 → `danglingCopper`）→ `setPath` / `setRef` / `setValue` / `setField` / `removeField`
  （库封装自带的多余用户字段也去掉）/ `setSheet`（sheetname = 原理图图纸路径名，如 `/`、`/CH1/`）/ `setFlags`
  （dnp、exclude_from_bom）→ `setPadNet` → `orphan`。`--update values,footprints,fields,refs,dnp` 选类别（焊盘网总是同步），
  `--fields-ignore` 排除字段，`--rename-nets=false` 关改名。
- **孤儿**：没有符号认领的封装（含 exclude_from_board 符号的封装）默认 `--orphans flag` 只报告；`remove` 删除，**锁定的
  永不删**（降级为 flag），在 group 里的先移出 group 再删；板专用（board-only）封装永远不算孤儿、不被改动。走线和铺铜永远不移动、不删除：焊盘离开了有铜的网
  → `danglingCopper`，网上一个焊盘都不剩 → `removeNet`（仅报告）。
- **写入**：新增/换入的库封装文件在规划时记 sha256（`fpSha`，计入 revision），应用时只装这份字节（复制进暂存目录再加载），
  规划后库文件被改 → 退出 4。源板、`.kicad_pro`、`.kicad_dru` 读入时记下字节与哈希；bridge 加载的是复制进暂存目录的这份字节，应用前和提交前
  都再核对源文件（被改 → `E_FILE_CHANGED` 退出 4，什么都不写）。bridge `sync-apply` 先校验全部操作（封装 id、焊盘、网、库封装能加载），全有或全无，只写进目标旁的
  `.pcbpilot-stage-*/`（`PCBPILOT_STAGE_DIR` 之外一律拒绝）；三道门禁在暂存板上跑完后由 kfile 事务提交（锁文件、读后被改
  `E_FILE_CHANGED`、计划 revision）。`--in-place` 只有 pad-net、idempotent、schematic-parity **都 pass** 才提交（parity 不能检查 → 不提交；`--no-parity` 是 skip，
  只有再加 `--force-unverified` 才提交，仍退出 5），否则板子一字节不动；
  `--out` 只要暂存板能读回就保留输出。`--in-place` 不改 `.kicad_pro`（缺失时才写）；`--out` 发布的 `.kicad_pro` 是源工程的原字节（不是
  SaveBoard 重写的，非默认设置都保留；parity 也用它）；源没有 `.kicad_pro` 时目标已有的那份保持不动，只有目标也没有才写
  SaveBoard 生成的默认工程。`.kicad_dru` 同理：源有就发布源的（覆盖目标已有的，基哈希校验），源没有就保留目标的。改名检查、parity
  与提交都用**实际发布**的这份工程和规则，保留的目标文件提交前也再核对（被改 → 退出 4）。`--create`：`--pcb` 不存在时从空板起（全部新件暂存在
  原点起的行里）；没有源板可查版本，就核对 bridge 应用时的 pcbnew 版本，不是 10.x → cannot-check 退出 5。
- **门禁**（`internal/gate`，没验证就不是 pass）：`pad-net`（暂存板上与符号挂钩的封装焊盘网 vs 网表引脚网，
  `ComparePinNets` / `padNetDiffGate`；NPTH 与无编号焊盘不算；同编号的焊盘必须同网，没有网的也算）、`schematic-parity`（暂存板 + 工程 + 全部 `.kicad_sch`
  复制到临时目录、板改名为根图名，`kicad-cli pcb drc --schematic-parity --severity-all`；任一 parity 项失败，只有被 flag 的
  孤儿的 `extra_footprint` 记为 info；kicad-cli 缺失/崩溃/报告读不出 = cannot-check）、`idempotent`（暂存板重新规划 0 个操作）。
  `--no-parity` 是用户跳过，结论是 skip、退出 5，永远不是 pass。
- **退出码**：0 通过（`--check` 无漂移）· 1 错误/拒绝 · 2 用法 · 3 `--check` 有漂移 · 4 锁定/读后被改/revision 不符（未写）·
  5 有门禁没能验证（含 `--no-parity`）· 6 门禁失败（`--out` 输出保留，`--in-place` 未提交）。
- 原理图按真实依赖图收集：根图 + 图纸符号的 Sheetfile 递归（含 `../` 外部子图；绝对路径的图纸 → `E_SHEET_PATH` 拒绝），
  连同根图旁的 `.kicad_pro` 读一次、记哈希、按原目录结构复制到临时目录；网表导出与 parity 都用这份副本。这些文件和
  `--netlist` 在应用前、提交前（不提交时在返回成功前；`--check` / `--dry-run` / `--plan-out` 在给出结论或写计划前，库封装文件
  也一并核对）与板一起再核对（被改 → 退出 4）。`--out` 的目标板（已有的或原本不存在的）与 `--create` 时原本不存在的板也在核对之列。封装库配置（工程 / 全局 / 嵌套
  fp-lib-table、kicad_common.json，含当时不存在的）
  同样记快照、计入 revision、一并核对。parity 拿到的工程缺失或读不出 → cannot-check，不算通过。`--in-place` 无事可做时也验证读入字节的
  暂存副本，不读活文件。revision 还包含实际发布的工程与规则、将被覆盖的目标板 / 工程 / 规则的哈希或不存在状态，以及每个库封装的
  哈希。
- **报告** `pcbpilot.kicad.pcb-sync/1`，总是打印到 stdout；`--report` / `--plan-out` 必须以 `.json` 结尾，已存在的文件必须是
  pcb-sync 自己的报告或计划（`schema` 字段），否则退出 2；开始时的状态作为写入基准，运行中被创建或改动 → 不覆盖，退出 4；`--check` / `--dry-run` / `--plan-out` 只有给了 `--report` 才写文件
  （不会因为 KiCad 开着工程而失败），写入板子的运行默认写 `<目标>.pcb-sync.json`。`--report` / `--plan-out`
  不能是任何输入或输出文件（板/工程/规则/prl、任一 `.kicad_sch`、网表、`--apply-plan`、输出板及其工程；按规范化路径、
  Windows/macOS 不分大小写、硬链接/链接同一文件判断），不能带 `.kicad_*` 扩展名，两者也不能相同 → 用法错误退出 2；写入经
  kfile **带 KiCad 锁检查**，所在目录工程被 KiCad（或正在跑的 kicad-cli）打开时写不进去：退出 4、stdout 的 `ok:false`，
  `--report` 指到别处即可。字段：`verdict`、`mode`、
  `revision`、`kicadVersion`、`netlist`、`libs`、`match`、`ops[]`、`counts`、`staged[]`（`ref,xMm,yMm`，交给 `kicad place`）、
  `orphanCopper[]`、`danglingCopper[]`、`locks[]`（`broke` 标出被 `--break-lock` 删掉的）、`files[]`（base/new sha256）、
  `gates[]`、`errors[]`、`warnings[]`（`sourceOpenInKiCad`：`--out` 时源板在 KiCad 里开着，未保存的编辑不在文件里）。
- **锁**：KiCad 打开工程会写 `~<文件>.lck` / `~<工程>.kicad_pro.lck`（内容 `{"hostname","username"}`，无 pid；
  **kicad-cli pcb drc 运行期间也会建工程锁**，10.0.6 实测）。默认遇锁退出 4；`--break-lock stale` 只删本机本用户且没有
  KiCad 进程（kicad / pcbnew / eeschema / kicad-cli）在跑的锁，`any` 全删（用户明确决定）。Agent 不自己选 `any`。
- 实测（KiCad 10.0.6 演示副本）：complex_hierarchy 基线 = 68 个 `swapFootprint`（板上用工程库昵称、符号写官方库昵称，
  KiCad parity 正好报这 68 条 `footprint_symbol_mismatch`），同步后三门禁全过、`--check` 无漂移；ecc83 基线 = 6 swap +
  15 `setSheet`（老板的 sheetname 是 “Racine”）；multichannel 的符号写的是普通安装里没有的库（“Capacitors THD” 等，
  板上用工程库），默认同步按 `E_FP_LIB_UNRESOLVED` 拒绝、不写，`--update values,fields,refs,dnp` 时只有 1 个 `setValue`，
  之后幂等。parity 一次约 30–50 s，是同步的主要耗时。`--apply-plan` 重新计算计划，revision 必须与计划文件和 `--revision`
  三者一致（板或工程文件、选项、或由原理图算出的操作变了都会变）。固定回归：`PCBPILOT_KICAD_LIVE=1 go test ./internal/app -run TestPcbSyncLive`。

## 布线（`kicad route`）——与 `pcb auto route` + `pcb gate` 同序同算

```bash
pcbpilot kicad route --pcb board-b-v5a.kicad_pcb --intent intent.json --sim sim.json \
    --requirements 03_Requirement/REQUIREMENTS.md --requirements 00_Project_Scope/SCOPE.md \
    [--sch board.kicad_sch] [--rip-up] [--clearance-mil 5.98 --track-mil 10 --via-dia-mil 24 --via-drill-mil 12 --edge-mil 30] \
    [--project-config pcbpilot.project.json] --out-dir route/
```

0. **设计评审（design 阶段）**：`review-panel`（Codex、Kimi、Claude Code）以 `--requirements` + intent、sim、原理图连接
   为输入；`<out-dir>/review-design/review.json` 若 stage 相同、`inputSha256` 与当前输入一致且已通过则复用，否则重跑。
   **不通过即停止**（不布线）；只有签名 waiver 能放行，`--no-review` 也必须有 `{"gate":"design-review","match":"--no-review"}` waiver。
1. **项目与规则**：板旁没有 `.kicad_pro` 时自动建一个最小项目并写板级规则：间距/最小线宽/过孔取意图 `copper` 块，
   铜到板边取 `pcbauto.EdgeFromIntent` 的 max(外层, 内层)（意图无 edge 时用默认 20/30 mil）；
   `--clearance-mil` 等显式参数优先。已有项目时，意图的板边距离仍是下限（只升不降）。随后跑一次**输入板 DRC** 作基线。
   已有项目时，显式 `--via-dia-mil` / `--via-drill-mil` / `--track-mil` 低于该项目自己的最小过孔直径 / 最小孔径 / 最小环宽 /
   最小线宽即**在启动阶段拒绝**（只给一个过孔参数时与 Default 网络类的另一半配对检查环宽）（`RULE_BELOW_BOARD_MINIMUM`，评审与输入 DRC 之前）；不给这些参数时用板自己的网络类尺寸。
2. **布线前门禁：意图 → KiCad 网络类 + 自定义规则**（必须在导出 DSN 之前）：
   - 每网 `{outer, inner, min, clearance}` 按取值分组为 `PP<n>_W<线宽>` 网络类（精确网名 pattern，优先级最高；
     用户类更宽的值保留，记 `netclasses.kept`）。
     **过孔**取 max(意图 `netClasses` 的过孔、该网原网络类过孔、板最小孔径 / 最小过孔直径 / 孔径 + 2×最小环宽)，线宽、间距也不低于
     板最小值；KiCad 新建网络类自带 0.6/0.3 mm 过孔，旧版因此在 interf_u（最小孔 0.5、最小过孔 0.9 mm）上布出 16 个
     drill_out_of_range + 16 个 via_diameter。Default 的线宽/间距/过孔低于板最小值时在工作副本上抬高；其他每个网（含只在绝缘域里的网）按**有效**线宽、间距、过孔（多类合成、部分类由 Default 补齐）判，低于最小值的加 `PPF_<线宽>_<间距>_<径>_<孔>`（全 nm）类；都记 `netclasses.viaFloored`。生成的 PP / PPF / PPD 类优先级高于工程里所有类；写完后逐网复查有效线宽/间距/过孔，不符即 `mismatched` 停止。全程整数 nm 比较（0.2 mm 不会被 7.87 mil 的舍入削掉），内层线宽同样不低于板最小线宽。KiCad 9 以下没有无值网络类，`netclasses` 直接拒绝。
     `PPD_<域>` 类只有名字（不带尺寸），不覆盖网自身的线宽/间距/过孔。
   - **内层线宽**：KiCad 网络类只有一个线宽。取最接近的官方机制：DSN 里加 `(layer_rule In1.Cu … (rule (width W)))`
     让 fastroute 内层按内层宽走；`.kicad_dru` 写 `(layer inner)` 的 `track_width (min <widthMil.min>) (opt <inner>)`；
     有 `widthMil.min` 的类再加全层 `track_width (min …)`。规则写在 `# pcbpilot:begin … # pcbpilot:end` 块里。
     KiCad 10 的 DRC **不**按网络类线宽报错，线宽只能靠这些规则 + `intent-widths` 门禁。
   - **安全（ISO / 医疗）**：意图的绝缘对（`pairs` + `domains`）→ 每个域一个 `PPD_<域>` 网络类，`.kicad_dru` 写
     `(constraint clearance (min …))` 与 `(constraint creepage (min …))`（KiCad 10 支持 creepage，已实测会报 creepage 违规）；
     绝缘对两侧每个网的 DSN 间距抬到该对的电气间隙（fastroute 每类只有一个间距）。危险域的板边距离
     （`EdgeFromIntent.ByDomain`）写成 `(constraint edge_clearance (min …))`。
   - DSN：原生 `ExportSpecctraDSN`（um），再：复合类名的逗号改 `+`（`--no-neckdown-classes` 用逗号分隔）、加内层 layer_rule、
     所有间距 +0.2 mil（KiCad DRC 在 Board-A 上量出 0.1484 < 0.1501 mm）、**板边禁布带**（边框内侧与每个 Edge.Cuts 挖空外侧，
     外层/内层各自距离，且不低于板规则；安装孔禁布圆按外层距离放大）——KiCad 导出器不带铜到板边规则（Board-B V5 A 曾有内层线
     离边 13 mil）。**板边连接器开窗**：板边安装件（封装框角点在板框外或距板框 < 1 mil，与 `copper-to-edge` 门禁同一判据）
     的近边焊盘外扩（间距 + 2 mil）后，在禁布带上开全深度窗口（焊盘 + 向内直出的逃线），否则 USB/端子焊盘布不通；
     `summary.edgeExemptPads` 列出，其余铜照常禁布，门禁照常判（这些焊盘不低于工厂下限时为 WARN）。逐网复查类线宽/内层线宽/间距，不足即停。**不做** EasyEDA 的 dsn-fix / ses-repair / reconcile。
   路由器可执行文件：`--fastroute-bin` / `$FASTROUTE_BIN` / `~/.pcbpilot/fastroute/current` / PATH（tracemaker 同理）。`current`
   或显式路径是目录时（Windows 解压布局 `0.1.14/fastroute-0.1.14-windows-x64/fastroute.exe`）解析到其中、`bin/` 或下一层
   子目录里唯一的可执行文件；找不到或有多个即在启动阶段报错。
3a. **`--router fastroute|tracemaker|both`**（默认 fastroute）：TraceMaker 是第二个后端（`~/.pcbpilot/tracemaker/current`、
   `$TRACEMAKER_BIN` 或 `--tracemaker-bin`；`--tracemaker-time` 默认 300 s——它同时决定格点间距，Board-A 上 ≥300 s 为 0.05 mm
   全通 359/359，90–180 s 为 0.1 mm、2 条未布通）。直接读 classed 板（含 `.kicad_pro/.kicad_dru`）写出布好的板，不走 DSN/SES；
   约束由意图生成：有 `widthMil.min` 且小于类宽的网只允许在焊盘处缩到该最小（`min_width_mm`），其余宽于 DSN 下限的网禁止缩细
   （`no_neckdown`），显式差分对带间距/偏斜。`both`：两个后端同时跑，**先「完整 + 无新增 DRC 错误」的胜出并取消另一个**，胜出者仍要过
   本次运行的全部门禁；任一门禁失败就换下一个（被取消的会重跑），全都不过则按最少未布通/新增 DRC 的那个报告失败。
   `summary.routerCandidates` 记录每个后端的未布通数、用时、快检结果、是否过全量门禁。任何门禁、签核都不因后端而减少。
3. **fastroute**：一条 `runFastroute`（续跑、崩溃单线程重试）。`--multi-start`（默认 4）是 pcbpilot 设置：fastroute 自己
   并行重跑 N−1 个不同种子的第 1 轮网序、按（未布通、冲突、−分数）取最好，pcbpilot 不再叠第二层并行。同一 DSN + 同一参数
   结果逐字节相同（Board-A：3 次 `--multi-start=1`、2 次 `--multi-start=4` 的 SES 完全一致）。优化器默认关（`--optimizer`
   打开，`--optimizer-threshold`）：它的预算等于整个布线阶段（含 multi-start，至少 60 s）——Board-A `--multi-start=4`
   开 299 s / 关 134 s，同为 4 未布通 30 冲突，线长只差 0.1 %、过孔差 1；`--multi-start=1` 为 60 / 55 s、8 未布通。
   `--diagnose` 把未布通分 blocked / congestion：全部 blocked 时不续跑（重布无用），`route-complete` 逐条写
   「移动器件或加逃线」，`summary.blockedConnections` 列出。`--threads` 缺省 min(核数−1, 8)（Board-A 8 线程连跑 3 次
   SES 逐字节相同：14 未布通 / 3469 mm / 156 孔；单线程 13 / 3646 mm / 164 孔；机器高负载时墙钟 204–330 s 对 184 s，
   空闲时首轮约 50 对 112 s）；`--threads 1` 用于跨 fastroute 版本可复现。多线程崩溃自动单线程重跑。
   门禁与签核只读路由器无关的 `routeResult`（router、version、patchSha、args、session|board、unrouted/blocked 连接、
   violations、fixable；`summary.routeResult`），fastroute 是目前唯一实现。
   `--no-neckdown-classes` 列出线宽高于全局下限的**所有**意图网类（fastroute 的 fanout 微颈缩会把任意拥挤段收到类宽
   3/4、3/5、1/2，`min_trace_width_um` 只是下限）；最小线宽 = max(板最小线宽, min(最窄 `widthMil.min`, 最窄类线宽))、
   意图差分对/等长组（skew）文件。
   报告坐标是 DSN 单位 / 1000（KiCad um → mm，EasyEDA mil → inch），按 `specctra.ReportMilPerUnit` 换成 mil。
4. **导入后铜处理**（顺序同 `pcb auto route`）：SES 导入 + 重铺 → **过孔阵列**（`planViaArrays`，意图每次换层的过孔数）→
   **加宽到意图**（`planWidenToIntent`，KiCad DRC 报间距的加宽线先退一半再退回原宽）→ **铺铜**（GND 于 TOP/IN1/BOTTOM、
   主电源轨于 IN2，`defaultPourLayers`/`mainPowerRail`；`--pours auto` 只给没有铺铜的板铺；SMD 焊盘实连、通孔焊盘花焊盘，
   避免单辐条 starved_thermal）→ `--widen-net` → **丝印摆放**（`planSilkTight` + 组标签，最多 3 轮；降号的位号/组标签
   写入 `--silk-min-font` 字号与 0.15 mm 线宽）。
5. **门禁**（在 `<out-dir>/board-final.json` 上离线计算，`summary.json` 的 `gates[]` 与 `pcb auto route` 同契约）：

| gate | 判据 |
|---|---|
| `design-review` | 见第 0 步（design / layout 两条） |
| `kicad-drc` | `kicad-cli pcb drc --severity-all`（含被排除项 `excluded: true`）：未排除的 error 级 violations + unconnected + parity 合计 0；逐条列出；`info` 标出输入板原本就有的错误（摆放/封装问题，非布线所致）。警告（丝印重叠等）只记在 `summary.drc.warnings`，不进门禁。**工程自己的静默**也进门禁（`.kicad_pro` 的 `drc_exclusions` 排除、`rule_severities` 设为 `ignore`、或把 KiCad 默认为 error 的规则降为 `warning`）。**电气与制造规则**——短路（shorting_items、tracks_crossing）、间距族（clearance、hole_clearance、copper_edge_clearance、creepage、hole_to_hole、edge_clearance）、unconnected_items、原理图一致性（missing/extra/duplicate_footprints、net_conflict、footprint_symbol_mismatch）、尺寸（track_width、via_diameter、drill_out_of_range、annular_width、connection_width、microvia_drill_out_of_range）、starved_thermal、zones_intersect、solder_mask_bridge、自定义禁布（items_not_allowed）、板框/层/孔制造检查（invalid_outline、item_on_disabled_layer、track_on_post_machined_layer、through_hole_pad_without_hole），以及 pcbpilot 不认识的规则——被静默即列为 `EXCLUDED <key>` / `IGNORED <rule>` / `DOWNGRADED <rule>` 并**判失败**，只有逐条签名 waiver 能放行；其余 KiCad 默认 error 级规则（courtyards_overlap、pth/npth_inside_courtyard、overlapping_pads、长度/偏斜调谐、封装/库检查等）被静默时**不判失败**，但写进 detail 的 `WARNING` 与 `summary.drcPolicy.silenced`（规则、方式、位置）；丝印/文字等 warning 级规则只进 `info` |
| `pad-net-diff` | 有 `--sch` 时：板上每个焊盘的网 vs 原理图网表 |
| `intent-rules` | 每个意图网在布好的板上仍解析到它的 PP 网络类，类宽/间距不低于意图 |
| `intent-widths` | `checkIntentWidths`；有 `--sim` 时按每段仿真电流（segment，`segmentWidthNeed`），否则按网 |
| `intent-lengths` | 意图有等长组/差分 skew 时 |
| `copper-to-edge` / `isolation` / `via-current` | `pcb check` 的 edge、绝缘（电气间隙 + 爬电，开槽计入）、过孔电流规则（EasyEDA 合成一个 `pcb-check-intent`，KiCad 流程拆成三个门禁）；任何 ERROR 失败 |
| `post-layout-sim` | `sim post-layout`（IR 压降、开路、过孔电流、热）；没有 `--sim` 即失败 |
| `route-complete` | fastroute 会话 0 未布通、0 可修冲突；**唯一放宽**：剩余未布通连接全部属于已铺铜网、且 KiCad DRC 报 0 个 unconnected 时通过（铺铜已接上，逐条写在 info） |
| `silkscreen` | `silkGate`（位号贴自身封装、不压焊盘/孔/板边/其它丝印、字号不低于 min(项目字号, `--silk-min-font` 0.8 mm)，降号的列在 info） |
| `board-manual` | `runManualGate` 生成 `<out-dir>/manual/<板名>_使用说明.html`（`--project-config` 给 notes/pinMap） |
| `design-report` | `report design` 发布 `<out-dir>/report/vN/`（意图、仿真、board-final、DRC、post、说明书；有 `--sch` 再加网表对账与器件值）；缺 sim/post/说明书即失败 |

6. **IR 收敛**：只剩 `post-layout-sim`（± `intent-widths` / `silkscreen` / `board-manual`）失败时，`planWidenIR` 按压降/预算加宽、
   重跑第 5 步，最多 2 轮。仿真报「no copper path」但 KiCad DRC 对该网没有 unconnected 的条目标为 `SIM/KICAD MISMATCH`
   （`summary.simKicadMismatch`；门禁仍失败，因为这些焊盘的压降没有被证明），不再挡住收敛；同网的超预算压降照常列出并加宽。
7. **设计报告**（门禁 `design-report`）后，8. **发布评审**（review-panel `layout` 阶段，证据加上报告 JSON、`summary.json`、
   `board-final.json`）。任何门禁失败退出码非零。

## DRC 门禁（`kicad drc`）

任何 KiCad 板（不必经过 `kicad route`）的独立 DRC 判定，与 route 的 `kicad-drc` 门禁共用同一套严格解析与工程静默策略：

```bash
pcbpilot kicad drc --pcb board.kicad_pcb [--schematic-parity] [--out drc/]
pcbpilot kicad drc --pcb board.kicad_pcb --baseline-pcb ../revA/board.kicad_pcb     # 只报新增
pcbpilot kicad drc --pcb board.kicad_pcb --baseline drc/drc-report.json --waivers w.json
```

- **只读、在副本上跑**：板及同名 `.kicad_pro` / `.kicad_dru`（`--schematic-parity` 再加同名根原理图 `<板>.kicad_sch` 与整个层次）复制到临时目录，
  用 pcbpilot 的隔离 KiCad 配置（空的全局库表，kicadenv.go）运行 `kicad-cli pcb drc --format json --severity-all`；工程自己的
  `fp-lib-table` / `sym-lib-table` 随副本带上，`${KIPRJMOD}` 指回原目录，所以工程库照常解析，只在全局库里的封装出 `lib_footprint_*`
  环境类警告。工程目录不新增 `.kicad_prl`、锁文件或任何改动（演示板副本前后哈希一致）。子图在工程目录外 → cannot-check。
- **归属**：每个违例按条目 uuid 在 `.kicad_pcb` S 表达式里找到所属顶层对象（焊盘、丝印、courtyard 都归它的封装；线/弧/过孔/铺铜带网名），
  给出 `owner.class`：`environment`（库表类规则）> `schematic-parity` > `board-outline`（涉及 Edge.Cuts）> `routing`（涉及线、过孔、铺铜）>
  `footprint`（只涉及一个封装自己的对象）> `placement`（两个以上封装）> `graphic` > `unresolved`；位置另给 KiCad 坐标 `xMm/yMm`（mm，y 向下）。
- **检查（各自 pass / fail / cannot-check，总判定取最差，退出码 0 / 6 / 5）**：
  `kicad-drc`（violations + unconnected 的 error 级条目，以及**电气/制造规则的 warning 级条目**——hard 类规则如 hole_to_hole、
  connection_width、copper_sliver、holes_co_located、isolated_copper、padstack、被降级的 solder_mask_bridge；其余警告只列出）、`kicad-drc-policy`（同 route 门禁：电气/制造规则被排除、ignore 或从
  error 降为 warning 即失败；会在 warning 级门禁的规则（copper_sliver、holes_co_located、isolated_copper、padstack、footprint_symbol_field_mismatch 等）被排除或 ignore 同样失败；
  `.kicad_dru` 里 `(severity ignore)` 的自定义规则按其 constraint 对应的 DRC 规则归类（disallow → items_not_allowed、edge_clearance、hole_to_hole …），
  电气/制造类或认不出的 → `IGNORED custom rule …` 硬项；courtyard 等放置类只记 `SILENCED`）、`schematic-parity`（带 `--schematic-parity` 时，任何严重度的一致性条目）。
  `--waivers` 用 route 同一格式 `{"gate":"kicad-drc","match":…,"reason":…,"by":…}`，逐条接受（`kicad-drc` 的 waiver 也覆盖另两项）。
  kicad-cli 失败、报告不合 schema、基线读不了 → cannot-check，waiver 不能放行。
- **基线**：`--baseline-pcb` 对旧板副本也跑一次 DRC；`--baseline` 接受本命令（或 `kicad fab --preflight`）的报告，也接受原始 kicad-cli JSON
  （用当前板解析归属，没有工程策略，所以策略项全量判定）。匹配 = 同规则 + 同归属 key + **同一组违例对象**（条目 uuid 的多重集合相同）且每个对象与基线里同 uuid 的对象相距
  ≤ `--tolerance-mm`（默认 0.5；一致性与环境类不看位置）；有 uuid 的对象照样按 uuid 配对，只有缺 uuid 的对象按顺序比位置（两边都有 uuid 却不同 = 不同违例）；基线里是 warning、
  现在是 error 的算新增（升级）；一对一。所以焊盘不动、换了一条同网的线去碰它也是新增。`baseline.new` 计入门禁，`baseline.resolved` 是消失的旧违例。
  实测（interf_u 副本，pcbnew 把 R1 挪到 C1 上）：新增条目都涉及 R1，只多一条 copper_edge_clearance——KiCad 把同一块铺铜对板框的冲突
  报在了另一段 Edge.Cuts 上（对象不同 = 新增，同时留下一条 resolved 孪生项）。
- `--out DIR` 另存 `drc-report.json`、`drc.json`（原始报告）、`baseline-drc.json`。

实测（2026-10-27，Windows 11，KiCad 10.0.6，演示板副本，`--schematic-parity`）：pic_programmer PASS（只有 41 条环境类警告），
interf_u FAIL（copper_edge_clearance=board-outline、2 条 starved_thermal=routing、`connection_width` 被 ignore、22 条 footprint_symbol_mismatch），
cm5_minima FAIL（5 条排除项：courtyards_overlap 记 SILENCED，solder_mask_bridge 降级为 hard 项）、video FAIL（0 错误，161 条一致性警告）；
单板 8–40 s。

## 发布签核（`pcbpilot signoff`，EasyEDA 与 KiCad 同一条）

`kicad route` 与 EasyEDA `pcb auto route` 最后一步都自动运行（同一 `signoffGate`；`pcb auto route` 先发布设计报告
`<out-dir>/report`，design/layout 两次设计评审与 `kicad route` 相同），之后 `gate-set` 核对强制门禁集合；
`pcb gate` 的 `--out-dir` 可手动运行：

```bash
pcbpilot signoff --run-dir route/ --intent intent.json --sim sim.json --connectivity sch.json [--values values.json]
```

只读文件（board-final.json、意图、仿真、原理图连接、review.json、说明书、设计报告），任何一项不过即非零退出：

| 门禁 | 覆盖的核心卖点 |
|---|---|
| `signoff-review` | 设计阶段 Codex + Kimi + Claude 评审通过（review.json，stage=design） |
| `signoff-parts` | 每个 BOM 器件都有合法 LCSC C 号（原理图 supplierId / `--values` / 板上 LCSC 字段；安装孔、Mark、测试点、Logo 跳过） |
| `signoff-safety` | 绝缘对电气间隙 / 爬电（开槽计入）+ 铜到板边（pkg/safety：IEC 60664-1、62368-1、60601-1 MOOP/MOPP、61010-1） |
| `signoff-copper` | 按每段仿真电流的线宽、不低于 `widthMil.min`（禁止颈缩处）、过孔组载流 |
| `signoff-ir` | 设计后仿真结论 pass/warn（IR 压降、开路、过孔电流、温升） |
| `signoff-continuity` | 原理图 pin→net 划分 == 板上焊盘网络（`ComparePinNets`） |
| `signoff-trace` | 每条意图规则可追溯到原理图网络、器件和板上焊盘；表格写在 `signoff.md` |
| `signoff-artifacts` | post.json、带设计后仿真章节的说明书 HTML、设计报告 report.json 均存在 |

## 计时与提速（`summary.timings`）

`kicad route` 记录每段墙钟：设计评审、规则、输入 DRC、网络类、DSN 导出/准备、fastroute、SES 导入、过孔阵列、加宽、铺铜、丝印、
每个门禁（DRC、快照、仿真、意图/安全、尾部门禁）、IR 收敛、报告、发布评审、签核。Board-A（145 件，4 层，rip-up）实测
303 s → 213 s：multi-start=4 改为与主布线**并行的推测运行**（≥2 核，`--parallel-multi-start`，255.7 → 164.7 s）；
每轮门禁只跑一次 `kicad-cli` DRC（错误进门禁、警告只统计）；丝印轮次与上一轮相同即停止。丝印规划（共用 planSilkTight）
改为网格索引 + 每标签静态合法位缓存：Board-A 规划 67 s → 0.9 s、Board-B V5 A 88 s → 1.7 s（同一快照离线测，机器负载高），
未解决位号 23 → 8、58 → 23。剩余大头是 fastroute 本身。

kicad/pcb-fixes 前后（Board-A 草稿副本，`--rip-up --no-review`，同一命令；机器负载 15–80，墙钟只作参考）：

| | 前（de11806d） | 后 |
|---|---|---|
| 总计 / fastroute / 丝印 | 307 s / 231 s / 61 s | 821 s / 696 s / 23 s |
| fastroute 未布通（blocked） | 2 | 11（全部 blocked，不再续跑） |
| intent-widths | 23 段低于意图线宽（fastroute 微颈缩） | 0 |
| silkscreen 问题 | 14 | 11 |
| kicad-drc 错误 | 31 | 22 |

未布通变多来自「所有意图网类不准颈缩」：同一 DSN、`--multi-start=1` 单独测，旧规则 5 未布通（1 blocked）、新规则 15（8 blocked），
`--router.neck_width_um` 不改善——宽线进不了细间距焊盘。需要的是焊盘逃线（固定短桩），不是放开颈缩；
kicad/escape-stubs 已加意图网焊盘逃线（外出 / 同网桥 / 内收过孔，见 `pcb-routing.md` 与 `docs/kicad/PARITY.md`）：
同一命令 11 → 5 未布通（剩余全是 R303/R304/C314 摆放挡道，route-complete 给出挪件提示），KiCad DRC 22 → 15，
fastroute 696 → 91 s。`kicad route` 没有 `--candidates` 候选摆放试布（那是 `pcb auto route` 的功能）。

## 已知限制与坑

- 原生 DSN 把 KiCad「power」类型且整层铺铜的内层导成 plane，fastroute 不在其上走线。
- `intent-widths` 只检查直线段（与 EasyEDA 一致）；fastroute 不产生圆弧。
- 网名含 `*`/`?` 时 KiCad pattern 会按通配匹配；`netclasses.mismatched` 非空时布线前门禁直接失败。
- KiCad 的花焊盘铺铜在 postsim 栅格（0.5 mm）里可能丢掉细辐条连接；焊盘/过孔/走线之间的精确相接已在栅格化前并网，
  剩下的仿真开路若 KiCad 判连通会标 `SIM/KICAD MISMATCH`。
- 丝印：焊盘/孔挡满 30 mil 范围的位号（密板 0402/0603 阵列）在最小字号下仍可能无位，组标签放不下时只报告。

## 验证记录（2026-10-09，KiCad 10.0.7，fastroute 0.1.13）

- **Board-B V5 A**（转换来的 KiCad 板，203 封装/199 有 LCSC，4 层，无 .kicad_pro，草稿副本）：设计评审**未通过**
  （三家一致：J2/J8 与 CONNECTOR_PINOUT.md 不符；Codex 另有 R-12 BAT54S 钳位、ADC VD>VA 等），按规则停止布线；
  为验证布线管线另用标注为测试用途的 waiver 跑通：0 → 2 条 fastroute 未布通（1 条由 GND 铺铜接上，1 条 +5V_SENS 真开路，
  KiCad unconnected 1）；KiCad DRC 14 = 13 条输入板原有的 courtyards_overlap（摆放问题）+ 1 unconnected；copper-to-edge、
  isolation、via-current、intent-rules、intent-widths（segment）通过；post-layout-sim 失败（SV1/SV3/PV1_DRV IR 超预算，
  另有 1 条 +3V3「no copper path」为仿真栅格对过孔贴焊盘边的误判，KiCad 判定连通）；丝印 68 处（密板，规划器未解决的标签）；
  说明书、设计报告生成。单次约 18 min（评审复用时）。`pcbpilot signoff`（以 EasyEDA 原理图连接 pre-connectivity.json 对照）：
  review / parts（199/199 LCSC）/ safety / copper / trace / artifacts 通过，ir 与 continuity 失败
  （Q1–Q5 第 1 脚在板上有网、原理图连接里没有：栅极网络脚号与 JLC 封装不一致，需原理图侧核对）。
- **Board-A One revA**（草稿副本，145 件 4 层，rip-up，无 sim/评审）：2 条未布通，见上文计时。

固定回归：固定回归：`PCBPILOT_KICAD_LIVE=1 PCBPILOT_FASTROUTE_LIVE=<fastroute> go test ./internal/app -run TestKicadLive`
（自带 MIT 小板 `internal/kicad/testdata/tiny.kicad_pcb`，由同目录 `gen_tiny.py` 生成）。
