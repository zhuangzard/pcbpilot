# KiCad 板级设置：`pcbpilot kicad board`

无 GUI 地给 `.kicad_pcb` 建板框、层叠、安装孔、禁布区、铺铜和板上文字。每个子命令先校验全部输入
（数值、几何、层名对照板上已启用层、网名对照板上已有网），有任何问题就**一个字节都不写**并列出全部问题；
通过后由 pcbnew 写入、保存、重新加载核对，再提交。动手前用 `--help` 核对参数。

## 共同约定

- **单位一律 mm**，KiCad 坐标系（x 向右、y 向下，与 PCB 编辑器显示的坐标一致）。spec 文件可写
  `"units": "mm"`；写别的单位（mil、in）直接 `E_BOARD_UNITS` 拒绝，不换算。
- **写入方式二选一**：`--out NEW.kicad_pcb`（默认用法；输入板不动，`.kicad_pro` / `.kicad_dru` 一起带过去，
  目标已存在 → `E_BOARD_OUT_EXISTS`）或 `--in-place`（替换 `--pcb` 本身：先查 KiCad 锁文件，提交前再比对文件哈希，
  期间文件被保存过 → `E_FILE_CHANGED`，原文件不动）。两者都经 kfile 原子写入；工程目录会出现 `.pcbpilot/`（写锁、事务日志）。
- 写之前请用户在 KiCad 里关闭该板；有 `~<文件>.lck` → `E_KICAD_LOCKED`（退出码 4），**不要自己删锁**。
- `--spec FILE.json` 与形状参数互斥；JSON 的字段写法见各子命令 `--help`，多余字段直接拒绝。
- `--drc`：在输入和结果上各跑一次 `kicad-cli pcb drc`（error 级，隔离的空全局库表，库检查不参与），结果多出任何
  error 即失败、不写；`drc.new[].ours` 标出是否指向本次新建的对象。逐步搭板时每步都加 `--drc`。
- 报告是一段 JSON（stdout，`--report` 另存）：`ok`、`verdict`、`checks[]`（`pcbnew-reload`、
  `kicad-cli-stats`、`kicad-drc-no-new-errors`）、`issues[]`（`code`/`field`/`detail`/`fix`）、`created[]`（uuid）、
  `removed[]`、`summary`、`warnings`、`written`。
- 退出码：0 已写；2 输入被拒；4 锁定或文件已变；5 检查无法运行；6 检查失败（2/4/5/6 都没写）；1 其他错误。

## 子命令

| 命令 | 作用 | 要点 |
|---|---|---|
| `new --out X.kicad_pcb [--layers 2\|4\|6]` | 空板 + 工程文件 | 空板没有网：铺铜/带网孔要等原理图同步（或已有网的板） |
| `outline --rect W,H [--origin X,Y] [--corner-radius R]` / `--polygon "x,y …"` / `--circle X,Y,D` | Edge.Cuts 板框（线段 + 真圆弧） | 已有板框 → `E_BOARD_OUTLINE_EXISTS`，只有 `--replace` 才删掉板级 Edge.Cuts 重画；自交/重复点/零面积 → `E_BOARD_GEOMETRY` |
| `outline … --slot X1,Y1,X2,Y2,W` / `--cutout-circle` / `--cutout-rect` / `--cutout-polygon` | 槽孔、挖空 | 必须整个在板内且互不重叠；不给外形时加到现有板框上（没有闭合板框 → `E_BOARD_NO_OUTLINE`）；板边缺口画进外形多边形 |
| `stackup --layers 2\|4\|6 [--thickness 1.6] [--outer-copper 0.035] [--inner-copper 0.0175] [--dielectric t1,t2,…] [--finish ENIG]` | 层数 + 物理层叠 | 介质 core/prepreg 交替、默认 FR4 εr 4.5；板厚 = 全部层之和（含阻焊）；减层时被删的内层上还有走线/铺铜/焊盘/图形 → `E_BOARD_LAYER_IN_USE` |
| `holes --at X,Y … --drill D [--type pth --pad P --net GND] [--ref H5]` | 安装孔 | 每孔一个锁定、board-only 的 `pcbpilot:MountingHole_*` 封装；NPTH 不能带网/焊环；PTH 必须给 `--pad`；出板/压焊盘/压挖空/孔互叠 → 拒绝 |
| `keepout --rect X,Y,W,H\|--polygon\|--circle [--layers F.Cu,B.Cu] [--forbid tracks,vias,pads,pours,footprints] [--name]` | 规则区 | 默认全部铜层、五项全禁 |
| `zone --rect …\|--polygon\|--circle\|--board-outline --net GND --layers In1.Cu [--clearance --min-width --thermal-gap --spoke-width --connection thermal\|solid\|none\|tht-thermal --priority N] [--no-fill]` | 铺铜 + 灌铜 | 灌铜按 KiCad「全部重灌」做（报告写明重灌了几个区）；`summary.filledAreaMm2` 为 0 有警告 |
| `text --text "rev A" --at X,Y [--layer F.Silkscreen\|B.Silkscreen\|F.Fab\|B.Fab] [--size 1 --thickness 0.15 --rotation --justify]` | 板上文字 | 背面层自动镜像；低于板子最小丝印字高/线宽给警告 |

`outline`、`holes`、`keepout` 改变铺铜能覆盖的范围：板上已有灌好的铜时会自动全部重灌（警告里写明）。
层名可写 KiCad 10 名称（`F.Silkscreen`）或旧短名（`F.SilkS`），`*.Cu` = 全部已启用铜层。

## 推荐顺序（从空板或已同步的板）

```bash
pcbpilot kicad board new --out work/board.kicad_pcb --layers 2          # 或从已同步的板开始
pcbpilot kicad board outline --pcb work/board.kicad_pcb --in-place --drc --rect 80,50 --corner-radius 3 --slot 20,30,40,30,2
pcbpilot kicad board stackup --pcb work/board.kicad_pcb --in-place --drc --layers 4 --thickness 1.6 --finish ENIG
pcbpilot kicad board zone    --pcb work/board.kicad_pcb --in-place --drc --board-outline --net GND --layers In1.Cu
pcbpilot kicad board holes   --pcb work/board.kicad_pcb --in-place --drc --drill 3.2 --at 4,4 --at 76,4 --at 4,46 --at 76,46
pcbpilot kicad board keepout --pcb work/board.kicad_pcb --in-place --drc --rect 55,5,12,8 --name antenna
pcbpilot kicad board text    --pcb work/board.kicad_pcb --in-place --drc --text "rev A" --at 40,44
```

先铺铜再加带网 PTH 孔：孔会随自动重灌接到铺铜上；反过来，孔在灌铜前是孤立的，`--drc` 会报
`unconnected_items` 并拒写。完成后 `kicad snapshot` 复核 `outline`、`copperLayers`，必要时
`kicad-cli pcb export svg` 出图给用户看。

## 与 `kicad place` 的关系

`kicad place` 的板框来自快照：Edge.Cuts 合成后面积最大的闭合多边形；板内挖空（`edgecut-N`）按**精确多边形**
挡住器件（以前按短边直径的圆处理，长槽两端会放进器件）；`kicad board holes` 的孔是锁定的 MountingHole
封装，布局时固定不动；禁放封装的规则区（`--forbid` 含 `footprints`）是禁放区。Edge.Cuts 不闭合时快照退回外包框并在
`partial` 里说明——先修板框再布局。

## 失败 → 处理

| code | 处理 |
|---|---|
| `E_BOARD_LAYER` | 换成 detail 里列出的已启用层；需要内层先 `stackup --layers 4` |
| `E_BOARD_NET` | 网名区分大小写（detail 给 did-you-mean）；空板没有网，先同步原理图 |
| `E_BOARD_OUTLINE_EXISTS` | 确认要换板框再加 `--replace`；只加挖空就别给外形 |
| `E_BOARD_LAYER_IN_USE` | 先在 KiCad 里移走内层上的对象，或保留层数 |
| `E_BOARD_GEOMETRY` / `E_BOARD_OVERLAP` | 按 detail 的坐标挪开；不要缩小别人的焊盘来让位 |
| `E_BOARD_VERIFY`（退出码 6） | 写后回读不一致，没有提交；保留报告上报，不要手改文件 |
| `E_KICAD_LOCKED` / `E_FILE_CHANGED` | 请用户关闭 KiCad / 重新从当前文件运行 |
