# KiCad 后端 — 快照、网表、fastroute 布线与全套门禁（`pcbpilot kicad`）

> 2026-10-09 用户决定：EasyEDA 不稳定，原理图 + PCB 改在 **KiCad 10**（已验证 10.0.6 / 10.0.7）上做；
> 器件仍用 LCSC C 号；整板布线仍是外部 fastroute（GPLv3，只作独立进程，pcbpilot 不打包、不下载、
> 不链接）。同日用户补充：**EasyEDA 流程的每一项计算和门禁都必须在 KiCad 流程里照样执行**，
> 设计意图 / 仿真 / 安全距离 / 设计评审 / 设计报告 / 说明书都是硬门禁。本文件只讲 KiCad 侧；
> fastroute 安装与参数见 [pcb-routing.md](pcb-routing.md#external-router-fastroute)，意图来源见
> [design-intent.md](design-intent.md)。

## 工作方式

`pcbpilot kicad` 不经 daemon / 连接器，直接对 `.kicad_pcb` / `.kicad_sch` 文件工作：

```
pcbpilot ──▶ KiCad 自带 Python + pcbnew（internal/kicad/bridge.py，go:embed）
         │     snapshot / netclasses / rules / ripup / dsn / ses / pours / edit / fill
         ├─▶ kicad-cli pcb drc / sch export netlist
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

## 快照（`kicad snapshot board.kicad_pcb [--out board.json]`）

形状与 `pcb dump --include-copper` 一致（Go：`kicad.Snapshot(pcbPath) ([]byte, error)`），`boardSnapshot`、
`pcbauto.FromSnapshot`、`postsim.ParseBoard`、`pcb check` 的 edge/isolation/via-current、丝印门禁、说明书都直接读：

| 项 | 约定 |
|---|---|
| 单位/坐标 | mil，**y 向上**（y = −y_KiCad）；旋转仍是度、逆时针 |
| 铜层号 | pcbpilot 的 EasyEDA 编号：F.Cu=1、B.Cu=2、InN.Cu=14+N（In1=15）、通孔焊盘=12 |
| 器件 | primitiveId（封装 uuid；**板上 uuid 重复时改用 `ref:<位号>`**——转换来的 Gas V5 A 203 个封装只有 39 个不同 uuid）/ designator / value / footprint / `lcsc`（字段 LCSC、LCSC Part、LCSC Part #、JLCPCB Part #）/ layer / x,y,rotation / bbox / pads |
| 焊盘 | padNumber / net / layer / x,y / width,height（轴对齐外包）/ rotation / shape `[RECT\|ELLIPSE\|OVAL\|POLYGON, w, h]` / 通孔 holeDiameter |
| NPTH | 进 `footprintHoles`（圆孔 dia；槽孔为包络多边形） |
| 板框 | Edge.Cuts 合成多边形取面积最大者；内部挖空写成 layer 12 的 `copper.fills` |
| 铜 | `copper.lines`、`arcs`（arcAngle 逆时针为正）、`vias`、`pours`、`poured`（每个外轮廓一组 `[外轮廓, 孔...]`）、`regions`（禁放=2、禁走线/过孔=5、禁铺=7） |
| 丝印 | `silk`：每个封装的位号/值字段与丝印层文字，pcbSilkText 形状（BBox、FontSize、Rotation、Hidden、CompID）；字段 id 为 `<器件id>#ref` / `#value` |
| 规则 | clearance / track / 最小线宽 / 过孔 / 铜到板边 / 孔到孔（mil），source=`kicad`；另有 `netclasses[]`、`netClassOf{net: 有效网络类}`、`kicadLayers` |

## 网表（`kicad netlist --sch X.kicad_sch --out conn.json [--values values.json]`）

`kicad-cli sch export netlist --format kicadxml`（含层次子图）→ pcbpilot 原理图连接 IR（schemaVersion 1.4，
与 `sch connectivity` 同形：components+pins、nets、connections），`intent derive --connectivity` / `sim power` /
pad-net diff 直接读。单独挂在 KiCad `unconnected-(…)` 网上的脚不产生连接：带 no-connect 标记的记 `noConnected`，
否则 `connectionState: "unconnected"`。`--values` 写 `{"parts":{ref:{value,mpn,lcsc}}}` 给 `intent derive --values`。
从 KiCad 起步的设计由此得到与 EasyEDA 相同的意图 / 安全推导。

## 布线（`kicad route`）——与 `pcb auto route` + `pcb gate` 同序同算

```bash
pcbpilot kicad route --pcb GasV5_A.kicad_pcb --intent intent.json --sim sim.json \
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
2. **布线前门禁：意图 → KiCad 网络类 + 自定义规则**（必须在导出 DSN 之前）：
   - 每网 `{outer, inner, min, clearance}` 按取值分组为 `PP<n>_W<线宽>` 网络类（精确网名 pattern，优先级最高；
     用户类更宽的值保留，记 `netclasses.kept`）。
   - **内层线宽**：KiCad 网络类只有一个线宽。取最接近的官方机制：DSN 里加 `(layer_rule In1.Cu … (rule (width W)))`
     让 fastroute 内层按内层宽走；`.kicad_dru` 写 `(layer inner)` 的 `track_width (min <widthMil.min>) (opt <inner>)`；
     有 `widthMil.min` 的类再加全层 `track_width (min …)`。规则写在 `# pcbpilot:begin … # pcbpilot:end` 块里。
     KiCad 10 的 DRC **不**按网络类线宽报错，线宽只能靠这些规则 + `intent-widths` 门禁。
   - **安全（ISO / 医疗）**：意图的绝缘对（`pairs` + `domains`）→ 每个域一个 `PPD_<域>` 网络类，`.kicad_dru` 写
     `(constraint clearance (min …))` 与 `(constraint creepage (min …))`（KiCad 10 支持 creepage，已实测会报 creepage 违规）；
     绝缘对两侧每个网的 DSN 间距抬到该对的电气间隙（fastroute 每类只有一个间距）。危险域的板边距离
     （`EdgeFromIntent.ByDomain`）写成 `(constraint edge_clearance (min …))`。
   - DSN：原生 `ExportSpecctraDSN`（um），再：复合类名的逗号改 `+`（`--no-neckdown-classes` 用逗号分隔）、加内层 layer_rule、
     所有间距 +0.2 mil（KiCad DRC 在 PicoRick 上量出 0.1484 < 0.1501 mm）、**板边禁布带**（边框内侧与每个 Edge.Cuts 挖空外侧，
     外层/内层各自距离，且不低于板规则；安装孔禁布圆按外层距离放大）——KiCad 导出器不带铜到板边规则（Gas V5 A 曾有内层线
     离边 13 mil）。逐网复查类线宽/内层线宽/间距，不足即停。**不做** EasyEDA 的 dsn-fix / ses-repair / reconcile。
3. **fastroute**：`runFastroute`（续跑、崩溃单线程重试）+ 一次 multi-start=4；`--no-neckdown-classes`、最小线宽 =
   max(板最小线宽, min(最窄 `widthMil.min`, 最窄类线宽))、意图差分对/等长组（skew）文件。
4. **导入后铜处理**（顺序同 `pcb auto route`）：SES 导入 + 重铺 → **过孔阵列**（`planViaArrays`，意图每次换层的过孔数）→
   **加宽到意图**（`planWidenToIntent`，KiCad DRC 报间距的加宽线先退一半再退回原宽）→ **铺铜**（GND 于 TOP/IN1/BOTTOM、
   主电源轨于 IN2，`defaultPourLayers`/`mainPowerRail`；`--pours auto` 只给没有铺铜的板铺；SMD 焊盘实连、通孔焊盘花焊盘，
   避免单辐条 starved_thermal）→ `--widen-net` → **丝印摆放**（`planSilkTight` + 组标签，最多 3 轮）。
5. **门禁**（在 `<out-dir>/board-final.json` 上离线计算，`summary.json` 的 `gates[]` 与 `pcb auto route` 同契约）：

| gate | 判据 |
|---|---|
| `design-review` | 见第 0 步（design / layout 两条） |
| `kicad-drc` | `kicad-cli pcb drc --severity-error`：violations + unconnected + parity 合计 0；逐条列出；`info` 标出输入板原本就有的错误（摆放/封装问题，非布线所致）。警告（丝印重叠等）只记在 `summary.drc.warnings`，不进门禁 |
| `pad-net-diff` | 有 `--sch` 时：板上每个焊盘的网 vs 原理图网表 |
| `intent-rules` | 每个意图网在布好的板上仍解析到它的 PP 网络类，类宽/间距不低于意图 |
| `intent-widths` | `checkIntentWidths`；有 `--sim` 时按每段仿真电流（segment，`segmentWidthNeed`），否则按网 |
| `intent-lengths` | 意图有等长组/差分 skew 时 |
| `copper-to-edge` / `isolation` / `via-current` | `pcb check` 的 edge、绝缘（电气间隙 + 爬电，开槽计入）、过孔电流规则（EasyEDA 合成一个 `pcb-check-intent`，KiCad 流程拆成三个门禁）；任何 ERROR 失败 |
| `post-layout-sim` | `sim post-layout`（IR 压降、开路、过孔电流、热）；没有 `--sim` 即失败 |
| `route-complete` | fastroute 会话 0 未布通、0 可修冲突；**唯一放宽**：剩余未布通连接全部属于已铺铜网、且 KiCad DRC 报 0 个 unconnected 时通过（铺铜已接上，逐条写在 info） |
| `silkscreen` | `silkGate`（位号贴自身封装、不压焊盘/孔/板边/其它丝印、不小于项目字号） |
| `board-manual` | `runManualGate` 生成 `<out-dir>/manual/<板名>_使用说明.html`（`--project-config` 给 notes/pinMap） |
| `design-report` | `report design` 发布 `<out-dir>/report/vN/`（意图、仿真、board-final、DRC、post、说明书；有 `--sch` 再加网表对账与器件值）；缺 sim/post/说明书即失败 |

6. **IR 收敛**：只剩 `post-layout-sim`（± `intent-widths` / `silkscreen` / `board-manual`）失败时，`planWidenIR` 按压降/预算加宽、
   重跑第 5 步，最多 2 轮。
7. **设计报告**（门禁 `design-report`）后，8. **发布评审**（review-panel `layout` 阶段，证据加上报告 JSON、`summary.json`、
   `board-final.json`）。任何门禁失败退出码非零。

## 已知限制与坑

- 原生 DSN 把 KiCad「power」类型且整层铺铜的内层导成 plane，fastroute 不在其上走线。
- `intent-widths` 只检查直线段（与 EasyEDA 一致）；fastroute 不产生圆弧。
- 网名含 `*`/`?` 时 KiCad pattern 会按通配匹配；`netclasses.mismatched` 非空时布线前门禁直接失败。
- fastroute 报告里的坐标在 KiCad DSN（um）下是 mm，但 `route-complete` 的可修冲突条目沿用 EasyEDA 的「mil」标注（共用代码）。
- KiCad 的花焊盘铺铜在 postsim 栅格（0.5 mm）里可能丢掉细辐条连接；个别「no copper path」需对照 KiCad DRC 的 unconnected 判断。

## 验证记录（2026-10-09，KiCad 10.0.7，fastroute 0.1.13）

见仓库提交说明与开发会话报告；固定回归：`PCBPILOT_KICAD_LIVE=1 PCBPILOT_FASTROUTE_LIVE=<fastroute> go test ./internal/app -run TestKicadLive`
（自带 MIT 小板 `internal/kicad/testdata/tiny.kicad_pcb`，由同目录 `gen_tiny.py` 生成）。
