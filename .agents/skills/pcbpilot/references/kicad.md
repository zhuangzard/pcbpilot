# KiCad 后端 — 快照、fastroute 布线与门禁（`pcbpilot kicad`）

> 2026-10-09 用户决定：EasyEDA 不稳定，原理图 + PCB 改在 **KiCad 10**（已验证 10.0.6）上做；
> 器件仍用 LCSC C 号；整板布线仍是外部 fastroute（GPLv3，只作独立进程，pcbpilot 不打包、不下载、
> 不链接）。本文件只讲 KiCad 侧；fastroute 安装与参数含义见
> [pcb-routing.md](pcb-routing.md#external-router-fastroute)，意图来源见 [design-intent.md](design-intent.md)。

## 工作方式

`pcbpilot kicad` 不经 daemon / 连接器，直接对 `.kicad_pcb` 文件工作：

```
pcbpilot ──▶ KiCad 自带 Python + pcbnew（internal/kicad/bridge.py，go:embed）  读板/网络类/DSN/SES/铺铜
         └─▶ kicad-cli pcb drc --format json --severity-error                 DRC
         └─▶ fastroute（外部进程）                                              布线
```

- KiCad 定位：`$PCBPILOT_KICAD_APP`（macOS 默认 `/Applications/KiCad/KiCad.app`，Linux 默认前缀 `/usr`，
  Windows 默认最新的 `C:\Program Files\KiCad\<版本>`）；`$PCBPILOT_KICAD_PYTHON`、`$PCBPILOT_KICAD_CLI`
  可单独覆盖。macOS 的 Python 按 `Python.framework/Versions/*/bin/python3` 通配查找（KiCad 升级后版本号会变）。
  Linux / Windows 路径是尽力而为（发行版把 pcbnew 装进系统 Python；Windows 安装包带 `bin\python.exe`），
  未现场验证。
- bridge 每个子命令在 stdout 只输出一行 `PCBPILOT_JSON:{...}`；pcbnew 的调试噪声（wx assert、swig
  内存泄漏提示，偶尔与结果同一行）一律忽略，Go 侧只取最后一个标记之后的 JSON。
- **输入板永不改写**：`kicad route` 先把 `--pcb` 及同名 `.kicad_pro`/`.kicad_dru` 复制进
  `<out-dir>/work/`，所有写入都在 `--out-dir`。`.kicad_pro` 必须与板同目录（DRC 规则和网络类在里面）。

## 快照（`pcbpilot kicad snapshot board.kicad_pcb [--out board.json]`）

形状与 `pcb dump --include-copper` 一致，`boardSnapshot`、`pcbauto.FromSnapshot`、`postsim.ParseBoard`
都能直接读（Go 接口 `kicad.Snapshot(pcbPath) ([]byte, error)`）：

| 项 | 约定 |
|---|---|
| 单位/坐标 | mil，**y 向上**（y = −y_KiCad）；旋转仍是度、逆时针（翻转 y 不改变视觉转向） |
| 铜层号 | 沿用 pcbpilot 的 EasyEDA 编号：F.Cu=1、B.Cu=2、InN.Cu=14+N（In1=15）、通孔焊盘=12，现有门禁原样可用 |
| 器件 | designator / value（也写进 device）/ footprint（库:名）/ `lcsc`（字段名 LCSC、LCSC Part、LCSC Part #、JLCPCB Part # 任一）/ layer 1 顶 2 底 / x,y,rotation / bbox / pads |
| 焊盘 | padNumber / net / layer / x,y / width,height（轴对齐外包）/ rotation / shape `[RECT\|ELLIPSE\|OVAL\|POLYGON, w, h]`（焊盘自身坐标系；圆角矩形按矩形算）/ 通孔 holeDiameter |
| NPTH | 不进 pads，进 `footprintHoles`（圆孔 dia；槽孔用包络多边形，`envelope:true`） |
| 板框 | Edge.Cuts 合成多边形取面积最大的一个（圆弧由 KiCad 折线化），内部挖空写成 layer 12 的 `copper.fills`；无闭合板框时退化为 bbox 并记 `partial` |
| 铜 | `copper.lines`（primitiveId=KiCad uuid）、`arcs`（带 midX/midY 与 arcAngle，逆时针为正）、`vias`（diameter/holeDiameter/fromLayer/toLayer）、`pours`（区域轮廓）、`poured`（已填充铜，每个外轮廓一组 `[外轮廓, 孔...]` 偶奇规则）、`regions`（规则区：禁放=2、禁走线/过孔=5、禁铺=7） |
| 规则 | clearanceMil = max(板最小间距, Default 类间距)；trackWidthMil = Default 类线宽；trackWidthMinMil = 板最小线宽；过孔、铜到板边、孔到孔；source=`kicad` |
| 额外 | `netclasses[]`、`netClassOf{net: 有效网络类}`、`kicadLayers{层号: {name, type}}` |

不含丝印（`silk` 为空）：丝印门禁仍是 EasyEDA 流程的事，KiCad 侧暂用 KiCad DRC 的丝印检查。

## 布线（`pcbpilot kicad route`）

```bash
pcbpilot kicad route --pcb board.kicad_pcb --intent intent.json --out-dir route/ [--sim sim.json] \
    [--rip-up] [--max-time 5m] [--waivers waivers.json]
```

1. **复制**输入到 `work/input.kicad_pcb`；`--rip-up` 删掉未锁定的走线与过孔（从零布）。
2. **布线前门禁：意图 → KiCad 网络类**（必须在导出 DSN 之前）。`intentRequirements(intent)` 的每网
   `{outer, inner, min, clearance}` 按相同取值分组，每组一个网络类 `PP<n>_W<线宽>`，以精确网名 pattern
   绑定，优先级最高（同一网若还匹配用户自己的类，KiCad 生成复合类 `PP1_W20,Power3V3`，取值以 PP 类为准）。
   用户类已经更宽/更大的线宽、间距保留（与 EasyEDA 的 `ApplyNetRequirements` 同口径：只升不降），记在
   `netclasses.kept`。重跑时先清掉旧 PP 类与其 pattern；用户 pattern 从 `.kicad_pro` 读回保留
   （swig 不暴露 pattern 列表）。
   - **内层线宽的取舍**：KiCad 网络类只有一个线宽，没有分层线宽。采用最接近的两个官方机制：
     (a) DSN 里给该类加 `(layer_rule In1.Cu … (rule (width W)))`，让 fastroute 在内层按内层宽布；
     (b) 在 `<板>.kicad_dru` 写 `(rule "pcbpilot PPn inner" (layer inner) (condition "A.hasNetclass('PPn')")
     (constraint track_width (min <widthMil.min>) (opt <inner>)))`——`opt` 是 KiCad 交互布线器采用的线宽，
     `min` 让 DRC 卡住颈缩下限。另外凡有 `widthMil.min` 的类都加一条全层 `track_width (min …)`。
     这些规则写在 `# pcbpilot:begin … # pcbpilot:end` 块里，用户原有规则保留。
     注意：KiCad 10 的 DRC **不**按网络类线宽报错（实测：GND 类 20 mil、实线更细只报 clearance），
     所以线宽下限只能靠上面的自定义规则 + `intent-widths` 门禁。
3. **DSN**：KiCad 原生 `ExportSpecctraDSN`（单位 um），再做三件事并复核：复合类名中的逗号改 `+`
   （fastroute 的 `--no-neckdown-classes` 用逗号分隔；SES 不带类名，改名不回到板上）；加内层
   layer_rule；逐网复查类线宽/内层线宽/间距，任何不足即停（`dsnRequirements.short`）。
   **不做** EasyEDA 的 dsn-fix / ses-repair / reconcile——KiCad 的 DSN/SES 不需要。
4. **fastroute**：复用 `runFastroute`（续跑、崩溃单线程重试、最后一次 multi-start=4 兜底）；
   `--no-neckdown-classes` = 每网 `widthMil.min = outer` 且高于全局下限的类；最小线宽 =
   max(意图里最窄的 `widthMil.min`, 板最小线宽)，向上取整到 0.1 µm（`--min-trace-um` 可覆盖）；
   意图里的差分对/等长组写成 `route-pairs.txt` / `route-tune.txt`。
5. **SES 导入**：`ImportSpecctraSES` → `ZONE_FILLER` 全部重铺 → `<out-dir>/routed.kicad_pcb`
   （`.kicad_pro`、`.kicad_dru` 同目录；pcbnew 的 SaveBoard 不覆盖已有的 `.kicad_pro`，bridge 先删再存）。
6. **门禁**（`summary.json` 的 `gates[]`，任一失败退出码非零；`--waivers` 同 `pcb auto route`）：

| gate | 判据 |
|---|---|
| `route-complete` | 导入的会话 0 未布通、0 可修复冲突（`routeCompleteGate`） |
| `kicad-drc` | `kicad-cli pcb drc --severity-error`：violations + unconnected_items + schematic_parity 合计 0；每条列出类型、对象种类、层、网、位置（mil, y 向上）、uuid 与 KiCad 原文 |
| `intent-widths` | 对 routed 板的快照跑 `checkIntentWidths`：外层/内层宽、仅焊盘 50 mil 内或本网铺铜内可颈缩、永不低于 `widthMil.min`。**依据**：给 `--sim` 且设计后仿真能跑时按每段仿真电流（segment，与 `pcb gate` 默认一致）；否则按整网电流（net），`summary.widthBasisNote` 写明原因 |
| `intent-lengths` | 意图有等长组/差分 skew 时才出现 |

输出：`work/`（输入副本、classed 板、reqs）、`route-kicad.dsn`（原生导出）、`route.dsn`（送 fastroute 的）、
`route.r<n>.ses` 与报告、`routed.kicad_pcb`、`drc.json`（kicad-cli 原文）、`board-routed.json`（快照）、
`post.*`（有 `--sim` 时）、`summary.json`。

## 已知限制与坑

- bridge 依赖 KiCad 自带 Python；KiCad 升级/安装中途 `kicad-cli` 会被系统杀掉（退出码 137），等安装完成再跑。
- 输入板的内层若是 KiCad「power」类型并带整层铺铜（如 GND 平面），原生 DSN 把它们导成 plane，
  fastroute 不在其上走线，相当于两层布线 + 过孔打平面。
- `intent-widths` 只检查直线段（与 EasyEDA 一致），圆弧走线不计；fastroute 不产生圆弧。
- 网名含 `*`/`?` 时 KiCad 的 pattern 会按通配匹配，可能把类挂到别的网；`netclasses.mismatched`
  非空时布线前门禁直接失败。
- 丝印门禁、板卡说明书门禁、逐焊盘原理图对账（pad-net-diff）尚未接入 KiCad 流程。

## 验证记录

见文末「现场验证」一节（由开发会话填写，含 PicoRick 实板数字）。
