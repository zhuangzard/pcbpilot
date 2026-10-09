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
     离边 13 mil）。**板边连接器开窗**：板边安装件（封装框角点在板框外或距板框 < 1 mil，与 `copper-to-edge` 门禁同一判据）
     的近边焊盘外扩（间距 + 2 mil）后，在禁布带上开全深度窗口（焊盘 + 向内直出的逃线），否则 USB/端子焊盘布不通；
     `summary.edgeExemptPads` 列出，其余铜照常禁布，门禁照常判（这些焊盘不低于工厂下限时为 WARN）。逐网复查类线宽/内层线宽/间距，不足即停。**不做** EasyEDA 的 dsn-fix / ses-repair / reconcile。
3. **fastroute**：一条 `runFastroute`（续跑、崩溃单线程重试）。`--multi-start`（默认 4）是 pcbpilot 设置：fastroute 自己
   并行重跑 N−1 个不同种子的第 1 轮网序、按（未布通、冲突、−分数）取最好，pcbpilot 不再叠第二层并行。同一 DSN + 同一参数
   结果逐字节相同（PicoRick：3 次 `--multi-start=1`、2 次 `--multi-start=4` 的 SES 完全一致）。优化器默认关（`--optimizer`
   打开，`--optimizer-threshold`）：它的预算等于整个布线阶段（含 multi-start，至少 60 s）——PicoRick `--multi-start=4`
   开 299 s / 关 134 s，同为 4 未布通 30 冲突，线长只差 0.1 %、过孔差 1；`--multi-start=1` 为 60 / 55 s、8 未布通。
   `--diagnose` 把未布通分 blocked / congestion：全部 blocked 时不续跑（重布无用），`route-complete` 逐条写
   「移动器件或加逃线」，`summary.blockedConnections` 列出。`--threads` 缺省 min(核数−1, 8)（PicoRick 8 线程连跑 3 次
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
| `kicad-drc` | `kicad-cli pcb drc --severity-error`：violations + unconnected + parity 合计 0；逐条列出；`info` 标出输入板原本就有的错误（摆放/封装问题，非布线所致）。警告（丝印重叠等）只记在 `summary.drc.warnings`，不进门禁 |
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
每个门禁（DRC、快照、仿真、意图/安全、尾部门禁）、IR 收敛、报告、发布评审、签核。PicoRick（145 件，4 层，rip-up）实测
303 s → 213 s：multi-start=4 改为与主布线**并行的推测运行**（≥2 核，`--parallel-multi-start`，255.7 → 164.7 s）；
每轮门禁只跑一次 `kicad-cli` DRC（错误进门禁、警告只统计）；丝印轮次与上一轮相同即停止。丝印规划（共用 planSilkTight）
改为网格索引 + 每标签静态合法位缓存：PicoRick 规划 67 s → 0.9 s、Gas V5 A 88 s → 1.7 s（同一快照离线测，机器负载高），
未解决位号 23 → 8、58 → 23。剩余大头是 fastroute 本身。

kicad/pcb-fixes 前后（PicoRick 草稿副本，`--rip-up --no-review`，同一命令；机器负载 15–80，墙钟只作参考）：

| | 前（de11806d） | 后 |
|---|---|---|
| 总计 / fastroute / 丝印 | 307 s / 231 s / 61 s | 821 s / 696 s / 23 s |
| fastroute 未布通（blocked） | 2 | 11（全部 blocked，不再续跑） |
| intent-widths | 23 段低于意图线宽（fastroute 微颈缩） | 0 |
| silkscreen 问题 | 14 | 11 |
| kicad-drc 错误 | 31 | 22 |

未布通变多来自「所有意图网类不准颈缩」：同一 DSN、`--multi-start=1` 单独测，旧规则 5 未布通（1 blocked）、新规则 15（8 blocked），
`--router.neck_width_um` 不改善——宽线进不了细间距焊盘。需要的是焊盘逃线（固定短桩，EasyEDA 流程已有 GND 预逃线，
KiCad 流程尚无），不是放开颈缩。`kicad route` 没有 `--candidates` 候选摆放试布（那是 `pcb auto route` 的功能）。

## 已知限制与坑

- 原生 DSN 把 KiCad「power」类型且整层铺铜的内层导成 plane，fastroute 不在其上走线。
- `intent-widths` 只检查直线段（与 EasyEDA 一致）；fastroute 不产生圆弧。
- 网名含 `*`/`?` 时 KiCad pattern 会按通配匹配；`netclasses.mismatched` 非空时布线前门禁直接失败。
- KiCad 的花焊盘铺铜在 postsim 栅格（0.5 mm）里可能丢掉细辐条连接；焊盘/过孔/走线之间的精确相接已在栅格化前并网，
  剩下的仿真开路若 KiCad 判连通会标 `SIM/KICAD MISMATCH`。
- 丝印：焊盘/孔挡满 30 mil 范围的位号（密板 0402/0603 阵列）在最小字号下仍可能无位，组标签放不下时只报告。

## 验证记录（2026-10-09，KiCad 10.0.7，fastroute 0.1.13）

- **Gas Module V5 A**（转换来的 KiCad 板，203 封装/199 有 LCSC，4 层，无 .kicad_pro，草稿副本）：设计评审**未通过**
  （三家一致：J2/J8 与 CONNECTOR_PINOUT.md 不符；Codex 另有 R-12 BAT54S 钳位、ADC VD>VA 等），按规则停止布线；
  为验证布线管线另用标注为测试用途的 waiver 跑通：0 → 2 条 fastroute 未布通（1 条由 GND 铺铜接上，1 条 +5V_SENS 真开路，
  KiCad unconnected 1）；KiCad DRC 14 = 13 条输入板原有的 courtyards_overlap（摆放问题）+ 1 unconnected；copper-to-edge、
  isolation、via-current、intent-rules、intent-widths（segment）通过；post-layout-sim 失败（SV1/SV3/PV1_DRV IR 超预算，
  另有 1 条 +3V3「no copper path」为仿真栅格对过孔贴焊盘边的误判，KiCad 判定连通）；丝印 68 处（密板，规划器未解决的标签）；
  说明书、设计报告生成。单次约 18 min（评审复用时）。`pcbpilot signoff`（以 EasyEDA 原理图连接 pre-connectivity.json 对照）：
  review / parts（199/199 LCSC）/ safety / copper / trace / artifacts 通过，ir 与 continuity 失败
  （Q1–Q5 第 1 脚在板上有网、原理图连接里没有：栅极网络脚号与 JLC 封装不一致，需原理图侧核对）。
- **PicoRick One revA**（草稿副本，145 件 4 层，rip-up，无 sim/评审）：2 条未布通，见上文计时。

固定回归：固定回归：`PCBPILOT_KICAD_LIVE=1 PCBPILOT_FASTROUTE_LIVE=<fastroute> go test ./internal/app -run TestKicadLive`
（自带 MIT 小板 `internal/kicad/testdata/tiny.kicad_pcb`，由同目录 `gen_tiny.py` 生成）。
