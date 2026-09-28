# PCB 电气感知自动设计（`pcb auto`）

`pcbpilot pcb auto` 是离线引擎 `pkg/pcbauto` 的入口：读懂电路 → 按电压/电流定线宽与间距 →
决定层数与叠层 → 按机械约束和电压域布局 → 先平面扇出再拥塞协商布线 → 等长 → 独立 DRC 与
信号完整性检查。**它从不直接写编辑器**，只输出 `pcbpilot apply` 剧本，所有工程写入仍走
typed action，符合“禁止手工操作 EDA 工程”准则。

验证状态：`offline-verified`（5 块真实开源板 fixture 与合成隔离板离线回归；剧本通过
`pcbpilot apply --dry-run` 预检）。**尚未在用户的 EasyEDA 现场执行剧本并回读**，首次使用
按下文“执行”一节逐步验证，结果回填本页。

## 什么时候用

| 场景 | 命令 |
|---|---|
| 想知道每个网该多宽、多大间距、几层板、哪里是高压区 | `pcb auto analyze` |
| 已有人工布局，只要自动布线 | `pcb auto run`（不加 `--place`） |
| 从刚导入的散乱器件开始，按机械要求自动布局再布线 | `pcb auto run --place --mech mech.json` |
| 已有布局只想微调 | `pcb auto run --place --refine` |
| 要求固定层数（如需求写明 4 层） | 加 `--layers 4` |
| 确认版布局只改个别器件（如把 RC 电容挪近引脚、ESD 转向） | `pcb auto run --place --refine --only C7,D3 --no-route` |
| 原理图画了模块框，要 PCB 按框归属 | `sch groups --pages <页> --out groups.json` → `--groups groups.json` |

`pcb route-short`、`route-critical`、`layout-plan` 等既有命令仍适用于逐模块、逐网的精细控制；
`pcb auto` 负责整板级决策。二者可组合：先 `pcb auto analyze` 取线宽/层数/域，再用既有命令
做局部。

## 输入

1. **板子**：`--board board.json`（`pcbpilot pcb dump --include-copper --out board.json`），
   省略则直接读已连接编辑器的当前 PCB。器件、焊盘、网络必须已从原理图导入（`pcb import-changes`）。
2. **机械规格** `--mech mech.json`（默认 mm，原点为板框左下角，y 向上）：

```json
{
  "units": "mm",
  "board": {"width": 50, "height": 40, "cornerRadius": 2, "thickness": 1.6},
  "cornerHoles": {"size": "M3", "inset": 3.5},
  "holes": [{"x": 25, "y": 5, "dia": 3.2, "keepout": 6.5}],
  "fixed": [{"ref": "U1", "x": 25, "y": 20, "rot": 0}],
  "edge":  [{"ref": "USB1", "edge": "left", "at": 20, "overhang": 0.8},
            {"ref": "J1", "edge": "bottom", "at": -1}],
  "keepouts": [{"name": "antenna", "rect": [38, 28, 50, 40], "noCopper": true, "noParts": true}],
  "heightZones": [{"rect": [0, 0, 12, 40], "maxHeight": 3}],
  "zones": [{"domain": "MAINS", "rect": [0, 0, 20, 40]}]
}
```

- `edge`：接口贴哪条边、沿边位置（`at<0` 居中）、外伸量。引擎按焊盘质心→本体中心判断开口
  朝向并自动旋转，使开口朝外；对称排针取长边沿板边。
- `board.autoSize: true`：不给尺寸，按布局结果收紧板框（加 `margin`）。
- `zones`：手动指定电压域分区（`domain`）；不给时多电压域自动左右分区并留隔离带。按功能块分区
  （`block`）与限高区（`heightZones`）当前只解析不生效（planned），见 [recipes/mech-spec.md](recipes/mech-spec.md)。
- 未知字段会被拒绝（防止拼错字段被静默忽略）。

3. **电源预算** `--power power.json`（强烈建议提供；不给时电流按网名估算并在报告中标“需要确认”）：

```json
{
  "tempRiseC": 10,
  "rails": [
    {"net": "VBUS", "voltage": 5, "currentA": 2.0},
    {"net": "+3V3", "voltage": 3.3, "currentA": 0.8},
    {"net": "VIN_24V", "voltage": 24, "currentA": 3, "plane": true}
  ],
  "diffPairs": [["ETH_TXP", "ETH_TXN"]],
  "diffOhm": 90, "singleEndedOhm": 50, "coated": false
}
```

4. **仿真逐脚电流** `--sim sim.json`（可选；`pcbpilot sim power` 的 schemaVersion 1 输出）：

```json
{"schemaVersion":1,"generator":"pcbpilot sim power","scenarios":["typical","worst"],
 "results":[{"scenario":"worst",
   "nets":{"+3V3":{"voltage":3.3,"currentA":0.55,"role":"power",
     "pins":[{"ref":"L1","pin":"2","currentA":0.55,"dir":"pass"},
             {"ref":"U1","pin":"2","currentA":0.53,"dir":"sink"}]}},
   "ripple":{"SW":{"iPeakA":0.75,"iRmsA":0.56}}}]}
```

   - 取 `worst` 结果；没有则取各场景逐脚最大值（报告写 `envelope(...)`）。`dir`：`source` 供给该网、
     `sink` 从该网取电、`pass` 无直流电流（<1 nA）。合并的 worst 是逐脚最大值（每脚带 `scenario`），
     **不满足 KCL**：例如 +5V 的两只 OR 二极管都以满电流出现。此时每个供电器件单独供电各解一次，
     每段电流、每焊盘压降取最大值；主干段电流取 max(下游 Σsink, 供电脚自身电流)。单场景文件满足
     KCL 时按实际分担求解（最大源为参考，其余源注入自身电流）。地网以回流入口（`kind:
     connector-source`）为参考。电容纹波 RMS（`ripple["C1"]`）用于该电容焊盘的扇出线宽/过孔；
     开关节点纹波可按网名（`ripple["SW"]`）或电感位号（`ripple["L1"]`）给出。
   - 仿真列出的网：电流来源为 `simulated`（报告引用场景）；开关节点按纹波 RMS 定宽、按峰值定过孔。
     `power.json` 已声明的轨仍以声明为准，声明 < 仿真时报告告警。仿真里没有的网沿用原逻辑。
   - **分段线宽**：布线后每段铜按其下游焊盘电流之和求 IPC 宽，收窄到 [类别最小值, 布线宽度]；
     主干保持全宽，上拉等小电流分支降到类别最小值；平面/铺铜承载的地不收窄走线。扇出过孔数与扇出
     短线宽按**该焊盘**电流算。
   - **IR drop**：电源网的实际铜皮（走线、过孔筒、平面/铺铜方阻网格）求解直流压降，报告每个汇点
     焊盘的压降、最坏路径逐段压降。预算 `--ir-budget`（默认 `2%,30mV` 取大；>5 V 轨只用百分比）。
     超预算：沿最坏路径逐级回宽已收窄段 → 仍超则以 1.5× 线宽（过孔占压降 >25% 时每焊盘 +1 孔）
     重布，至多 2 轮，仅在布通率与 DRC 不变差且压降改善时采用；否则 `report.md` 标 ✗ 超预算。
   - 平面模型忽略反焊盘和异网铜（偏乐观）；地网只报告地电位抬升不设预算。没有 `--sim` 时布线与评分
     与原来逐字节一致。

5. **设计意图** `--intent intent.json`（可选；`pcbpilot intent derive` 的输出或手写，可与 `--power`/`--sim` 同用，
   **intent 声明的值优先**）：
   - `nets.<NET>`：`widthMil.outer/inner`、`currentA`、`viasPerTransition`、`via{drillMil,diaMil,countPerTransition}`、
     `clearanceMil`、`diffPair`、`role` 覆盖推断，报告来源 `intent`；`netClasses[]` 的 `trackMil`/`clearanceMil`
     作为该类网的下限，类 `viaDrillMil/viaDiaMil` 是成员过孔的首选尺寸。电源网的布线过孔、扇出过孔用
     该网过孔尺寸（不低于板规则），每个换层点放 `countPerTransition` 颗（阵列，见
     [pcb-routing.md 过孔载流](pcb-routing.md)）；声明的过孔载流不足进 `warnings`（`via-current:`）。
   - `domains[]` / `pairs[]` / `standard`：电压域与两域之间的绝缘要求。距离由 `pkg/safety` 按
     IPC-2221B / IEC 62368-1 / IEC 60601-1（MOOP·MOPP）/ IEC 61010-1 查表（pair 显式给出 `clearanceMm`/
     `creepageMm` 时用显式值，低于标准计算值时告警）。引擎据此：按域分区、隔离带宽 = 爬电距离、
     路由器逐对（不是逐网）隔离两域铜皮、DRC 按该对间隙判、跨域器件焊盘排距 < 爬电时在剧本里铣槽
     （`pcb.fill.create` layer MULTI = 板框挖槽）、写禁铺铜区域、布线后核查沿面爬电（计入开槽）。
     全部细节与表值见 [recipes/hv-isolation.md](recipes/hv-isolation.md)。
   - 没有 `pairs` 的 intent（例如全 SELV 的 ESP32 mini）不产生隔离带、槽或禁铺铜区域；未声明的网与无
     `--intent` 时的网络计划与分区决策不变。

## 输出（`--out-dir DIR`）

| 文件 | 内容 |
|---|---|
| `report.md` | 中文报告：层数及理由、每个电源/地/高速网的电压电流线宽间距过孔、功能块与块间信号、电压域与隔离要求、布局与布线指标、DRC、高速检查；有 `--sim` 时加「2.1 仿真电流」与「5.1 分段电流、分支线宽与 IR drop」；有 `--intent` 且有绝缘对时加「安规隔离」（每对标准/间隙/爬电/槽宽、开槽、核查结果） |
| `plan.json` | 全部决策与几何（分析、叠层、电路模型、布局、走线、过孔、铺铜区、SI）；`--sim` 时 `analysis.nets[].padCurrents`、`route.power.nets[]`（每焊盘压降、每段电流/布线宽/分段宽、最坏路径） |
| `playbook.json` | `pcbpilot apply` 剧本：叠层 → 板框/孔/禁布区 → 隔离槽（`iso-slot-*`）与隔离带/禁铺铜区（`iso-band-*` / `iso-moat-*`）→ 器件位姿 → 走线/过孔 → 铺铜 → 翻转内电层 → 重铺 → 保存 → DRC |
| `preview.svg` | 目视复核图：器件按电压域着色，各层走线、过孔、平面分区、隔离带、未布通飞线（黄色虚线） |
| `feedback.json` | 回推原理图的建议（见下节「闭环：布线难点回推原理图」）；`report.md` 同名一节。`--no-feedback` 关闭 |
| `board.routed.json` | `--board` 输入的 `pcb dump --include-copper` 形态：布局后的器件/焊盘、引擎走线与过孔、隔离槽（MULTI fill）、禁铺铜区。`pcb check --intent intent.json --board board.routed.json` 离线判剧本将写出的铜皮 |

## 闭环：布线难点回推原理图

布不通、飞线大量交叉、绕远、高速网过孔多、IR drop 超预算、焊盘窄于电流所需线宽——这些常常是
**原理图**的问题（GPIO 分在模块另一侧、排针针序与对端相反、去耦被两颗 IC 共用、稳压器在错误一端），
只在 Layout 里挪件治标不治本。`pcb auto run` 布线后自动写 `feedback.json` 和报告「6b. 回推原理图的建议」；
已有 `plan.json` 时可单独重算：

```bash
pcbpilot pcb auto run --board board.json --power power.json --out-dir out/            # 默认验证前 3 个换脚候选
pcbpilot pcb auto run --board board.json --power power.json --out-dir out/ --feedback-loop 3
pcbpilot pcb feedback --plan out/plan.json --board board.json --power power.json --out out/feedback.json [--verify N] [--loop N]
pcbpilot sch pin-swap --plan out/feedback.json --item FB01 --sch <该页 sch connectivity.json> --dry-run
pcbpilot sch pin-swap --plan out/feedback.json --item FB01 --sch <该页 sch connectivity.json> --out swap.playbook.json
```

每条建议：`kind / severity / evidence（指标、网、位号、坐标）/ proposal（精确改法）/ expectedGain（方法 +
前后数值）/ confidence / applyable / status=live-unverified`。

| kind | 何时出现 | 证据与收益怎么算 | applyable |
|---|---|---|---|
| `mcu-pin-swap` | 器件在 [pin-capabilities.json](pin-capabilities.json) 里（ESP32-S3-WROOM-1 起步，`--pin-caps` 追加 STM32/AT32） | 网→引脚的指派问题：匈牙利解（长度 + 与固定网的交叉）起步，再做换位/迁移局部搜索（含换脚网之间的交叉），最后撤回不值 25 mil 的改动，使原理图改动最少。候选先按飞线估算，再在**板副本**上把焊盘网络对调、整条流水线重布，报布通率/过孔/线长/飞线交叉/DRC/联合评分前后值；重布无收益的进 `rejected` | 是 |
| `connector-pin-swap` | 通用排针/排母（J/P/CN/H 位号 + 表内型号关键字；USB、Type-C、SWD/JTAG、端子等协议口排除），电源/地脚固定 | 同上 | 是（对外接口，需用户同意线束同步改） |
| `decap-ownership` | ≥8 脚 IC 的某电源网 300 mil 内没有“最近归属于它”的 电源↔地 电容（没有或与别的 IC 共用） | 最近电容距离；按 0402 就近 60 mil 估回路长度 | 否（加件） |
| `rail-ir-drop` | `--sim` 且电源轨超预算/开路 | 最坏路径按 track/via/plane 分摊；给出加宽倍数、稳压器到最远负载需缩短到的长度、拆轨后主干电流（线性 R 模型） | 否 |
| `package-change` | 大电流焊盘（`--sim` 的逐脚电流；无仿真时只看 L/D/F/FB/J/P 串联件）窄于该电流 IPC 线宽且入焊盘段颈缩 | 宽度比 | 否（仅建议） |

**引脚能力表规则**：只有带 `gpio` 且无 `fixed` 的脚是可换位；`netNeeds` 按网名推导需求（ADC/触摸/USB/DAC）；
`gpio-matrix`（ESP32 系）数字功能任意映射，`af-table`（STM32/AT32）外设网要求目标脚具备当前脚的全部复用功能，
只有 LED/KEY/CS/RST/INT 等纯 GPIO 网自由换。ESP32-S3-WROOM-1 固定：IO0/IO3/IO45/IO46（strapping）、
IO19/IO20（USB）、TXD0/RXD0（ROM 下载串口，CH340 自动下载依赖它）、IO35–37（R8 版八线 PSRAM）；IO15/16、
IO39–42、IO47/48 可换但带 `caution`（32k 晶振 / JTAG / 1.8 V），置信度下调。表是 Skill 规范源，
`pkg/pcbauto/data/` 是嵌入镜像，`TestPinCapsMirrorInSync` 保证一致——**改表两处同改**。

**`--feedback-loop N`**：在内存副本上应用最优换脚 → 重布 → 联合评分提高（且布通率不降、DRC 不增）才保留，
最多 N 轮、每轮试 3 个候选；接受的换脚按“原脚→终脚”合成一条建议。`plan.json`/`playbook.json` 始终是**未改动
原理图**的那块板（与不加 flag 时逐字节相同，只多 `feedback` 字段），因为现场焊盘网络来自原理图。

**应用换脚（`sch pin-swap`）**：只生成 playbook，不执行。步骤：`check-before`（带 `--sch` 时）→ 旧脚
`schematic.pin.disconnect` → 新脚清 NC → `sch autoconnect --kind net_label --strict` → 旧脚置 NC → `schematic.save`
→ `check-after`（期望连通快照，逐脚 pin→net 与 NC 比对）。`--sch` 会校验旧脚确实在该网、新脚为空，否则报
`stale feedback`。旧脚若是直连导线（非桩线+网标），disconnect 不处理，playbook 在该步停止，需重新规划。

**护栏**：
- 换脚改的是原理图 → 与 Layout 变更同一确认规则：先把 diff 与重布前后数值给用户，得到明确同意再 `sch apply`。
- 执行后：`sch gate` / `sch connectivity` 回读 → 原理图更新到 PCB（`pcb import-changes`）→ `pcb pad-net-diff`
  对账 → 重新 `pcb auto run`；这一串完成前不得称“已改善”。固件引脚定义同步修改。
- `status` 一律 `live-unverified`：目前只有离线证据（2026-09-27）。
- 加件/封装/拆轨是设计取舍，给数值不自动执行；回到 S2（目标连接数据）改源数据再走 S3–S6。

**实测（离线，2026-09-27，ESP32 mini v4-base live dump）**：确认版布局上唯一未被功能锁定的可换网是 LED_CTRL
（ESP_TXD/RXD 在 ROM UART0、按键在 IO0/EN、USB 在 IO19/20，均正确排除）。飞线估算提出 IO2→IO1（交叉 −1、
飞线 +10 mil），整板重布 **无收益**（联合 92.28→92.28，过孔 22→22，线长 7.55→7.59 in）→ 进 `rejected`，
`--feedback-loop 3` 0 接受：该板**无有益 GPIO 换脚**。负对照：把 LED_CTRL 人为移到 IO5（模块左列）后，
搜索把它移回右列 IO1，重布验证 过孔 24→22、线长 8.26→7.59 in、联合 86.4→86.8（`TestFeedbackESP32MiniMisassignedLED`）。
`--sim --ir-budget 0.2%,3mV` 压力下给出 +3V3 超预算 1.36× 的三条带数值措施。

## 引擎怎么决策（读报告时对照）

- **线宽**：IPC-2221 载流公式（外层），内层按 IPC-2152 结论同截面折算铜厚；电源/地最低 10 mil，
  开关节点最低 20 mil；地作为平面，走线只按最大单轨回流定宽。电流大时换层过孔数按单孔载流算。
  有 `--sim` 时每段按自身电流收窄、扇出过孔按焊盘电流计数，并做 IR drop 校核（见输入第 4 条）。
- **间距**：IPC-2221B 电压表与工艺最小值取大；高压网络的占位自动放大（大半径占位的静态可行图改用精确
  距离变换计算，结果与逐格扫描一致，只是快）。有 `--intent` 绝缘对时另加**域间**要求：外层半爬电、
  内层半间隙的领地围栏，DRC 按该对间隙判，布线后按沿面路径核查爬电。
- **高压器件自身焊盘（HV footprint relief）**：高压网的 IPC 间距常大于其器件自身焊盘间距（MB10S 交/直流
  脚 1.5 mm、1206 分压电阻 1.8 mm、DPAK 漏极）。在自身焊盘的缩颈范围内，铜皮离**同一器件**的其他焊盘
  可以与自身焊盘一样近（不能更近）；路由器占位与 DRC 同一口径；域间 pair 要求不放宽；布局按高压网间距给器件加
  keep-clear（halo）。
- **同域 ΔV 间距**：有 intent 电压包络时，同一绝缘域内两网按两者**同时**可能出现的最大电压差查 IPC-2221B
  （分压链相邻节点 141 V → 0.6 mm，不再按 848 V 绝对值 4.2 mm）；开关节点/交流网按整个摆幅；上限仍是
  两网各自的绝对要求。仅用于精确焊盘判定与 DRC；走线之间的占位仍按“各自份额相加”（两条高压线之间偏保守）。
- **不可行的跨域器件**：焊盘距 < 间隙（开槽不增加空气路径）或放不下最小槽宽的跨域器件列入
  `result.isolation.infeasible`，stderr 打印 `isolation: INFEASIBLE`，`report.md` 醒目列出，
  `pcb check --intent` 报 `iso-infeasible` ERROR（`--strict` 非零退出）——必须换器件，不会静默通过。
- **细间距出线**：粗线在自己焊盘附近放不下时缩到工艺最小线宽（缩颈），离开焊盘区恢复全宽。
- **层数**：按布线需求面积 / 单层可用面积估算信号层数；高速差分（MIPI/HDMI/USB3/PCIe/以太网/LVDS）、
  RF、深 BGA、细间距高密度 → 需要参考平面 → 4 层；需求 > 2.2 层或 BGA ≥4 圈 → 6 层。
  USB2 全速、CAN、RS-485 不单独构成加层理由。`--max-layers` 为成本上限，`--layers` 强制。
- **平面**：4 层 = TOP / GND 平面 / 电源分割平面 / BOTTOM；电源分割按各电源轨过孔种子做加权
  Voronoi 并留分割缝，输出每轨铺铜多边形。外层布不通时自动把电源层升为“信号+电源铺铜”混合层
  重布并择优（报告里“尝试过的叠层方案”）。
- **布线**：先给平面/铺铜网的每个贴片焊盘打扇出过孔，再所有信号网按优先级做拥塞协商
  （PathFinder），最后严格合法化只拆冲突段、不拆整网；仍无解的标未布通，**绝不输出短路**。
  差分对背靠背布线并贴线；超出对内长度差预算时自动加蛇形线或 45° 凸起。
- **铺铜连通仿真**：模拟平面/铺铜被异网过孔打穿后的真实连通，孤岛自动补线。
- **独立 DRC**：输出后用精确几何（旋转焊盘、线段距离）复核；违规网拆除、放大余量重布，
  三轮后仍违规则丢弃并报告。
- **电路理解**：按位号/型号/引脚数分类器件；以芯片/接口为核心吸附专属外围，去耦电容按电源脚数
  分配到芯片；按地网划分参考域（0Ω/磁珠星形连接合并），光耦/隔离器/隔离电源/变压器/继电器
  视为跨域桥；市电网名或 >60 V 为危险域；危险域↔SELV 要求加强绝缘，按工作电压给出爬电距离
  与电气间隙，桥接器件焊盘排距不够时建议开槽。
- **布局（先核心、后辅助）**：多电压域左右分区 + 隔离禁铜带，隔离器件骑跨。每个被动件都有归属
  （核心 + 角色 + 目标引脚）：去耦 / 晶振 / 负载电容 / 功率级 / 端口保护 / 上下拉 / 信号串联 /
  链 / 测试点，拉力依次减弱；去耦按容值分级（≤220 nF 必须贴脚）。核心先做块级力导向（开关电源与
  模拟/射频互斥 400 mil）并选朝向，再按角色排队贴辅助件；背面去耦直接放到 BGA 焊盘正下方。
  退火含整块刚性移动。开关电源单独建模（`report.md`「开关电源」表）：拓扑（Buck/Boost，
  `certain`/`likely`）、热回路电容（Buck 取输入侧、Boost 取输出侧）、自举、反馈分压；热回路按
  多边形周长/面积计价，反馈件远离电感和开关节点。`likely` 的拓扑要对照数据手册核对。
  「接口信号链」表按原理图顺序列出 连接器/天线 → ESD → 串联件 → 芯片：ESD 必须最先遇到且不挂支线，
  差分两侧串联件并排；天线链计全长，链上的网自动标为 RF。连接器受保护引脚外侧是端口预留区，其他块
  器件不要进。关键器件（热回路、自举、高频去耦、晶振、ESD、反馈）在退火后还会做确定性精修。
  链的顺序错了（例如 ESD 画在串阻之后）先改原理图；链识别错了（支路、电源轨）修 `chains.go` 判据。
- **第 0 阶段先于一切**：`report.md` 第 0 节是物理可行性（器件占地、布线需求 vs 各层容量、BGA 逃逸与过孔工艺）
  和叠层决策（2 / 4 / 4 混合 / 6 / 8 层的利用率与结论）。出现「需要与机械 / 客户协商」时，先把放大尺寸、加层、
  BGA 区域规则或盘中孔等条目交给用户决定，不要硬布——那只会耗时间然后布不通。
- **验收看综合分，不看布局分**：`report.md`「综合评分」= 门槛 × 布通率² × 0.97^DRC × 质量分（电气在布线后
  的铜皮上实测、布线效率、布局装配三组几何平均）。`--place` 默认跑 3 轮布局↔布线闭环（`--loops`），在布不通和
  DRC 违规附近加宽器件后重排，取最好的一轮。判定为“不可交付”时，先看门槛和布通系数，再看是哪一组质量分拖后腿；
  不要用手工挪器件兜底，按参数（`--loops`、`--seed`、mech/power 约束）重算。**先读 `report.md`「布局依据」表核对归属**：归属错（例如测试点被当成串阻、
  开关电源被当成 logic）就修网名/判据再重跑，不要手工挪器件。

## 原理图模块框 → PCB 归属（`--groups`）

原理图上每个功能模块的框（核心 + 去耦/晶振/稳压外围）就是 PCB 布局需要的**归属**：共享电源轨上
“buck 输出电容”与“MCU 去耦”同网，只靠网表分不清；框能分清。

```bash
pcbpilot sch groups --project <工程> --pages <页1>,<页2> --out groups.json   # 只读，需连接器 ≥ 0.2.9
pcbpilot pcb auto run --board board.json --groups groups.json --place --out-dir out/
```

- 器件归入包含其 bbox 中心的**最小**框（嵌套框内层优先）；框名取框左上角最近的文字。
- 少于 `--min-members`（默认 2）件的框、以及装下约整页的框（图框/标题栏）忽略；`unframed` 列出未入框器件，
  它们仍按网表规则归属。
- pcbpilot 自己 compose 的页面也可以直接用 composition JSON 作为 `--groups`；`sch groups` 让任意人工原理图同样可用。
- 验证状态 `live-verified`（2026-09-25，桌面 V3 3.2.149，连接器 0.2.9）：按 compose 剧本重建 ESP32 两页原理图后读取，
  7 个多件模块与 composition 逐件一致，框坐标与 `sch frame apply` 源数据一致；单件模块（BOOT/RST 按键）按
  `--min-members 2` 列入 `unframed`。生成文件直接用于 `pcb auto run --groups`。新 action 需要**连接器和 daemon 都是新版**：
  旧 daemon 会报 `unknown action`，`pcbpilot daemon restart` 后恢复。
- 归属进入引擎后按角色决定拉力：去耦 / 功率链（buck 输入、电感、输出电容）/ `pin-filter`（IC 信号脚到地/电源的
  电容，如 EN 复位 RC）/ 晶振与负载电容 / 端口保护（保护件留在连接器旁，不跟核心走）。

### 两阶段宏布局（`--macro`，实验，默认关闭）

思路：先把每个核心和它的关键外围作为刚体“宏”摆好，再让宏和其余器件一起退火，最后解冻精修。
2026-09-25 在 8 块板（含 5 块真实开源板）的 A/B 上，它在 7 块上**不如**默认的单阶段退火：走线更长，
去耦更远，szpi 晶振 3.8 → 9.9 mm，rk3568 分数 66.9 → 61.3。原因：在全局排布出来之前先冻结局部几何，
固定的是“在空处摆得好”的形状，而不是“在整板里摆得好”的形状；刚体宏也让退火步长变粗。默认流程里
关键角色的拉力、退火后的确定性精修（`polish`）已经让同一模块在 PCB 上靠拢，所以不开宏。保留开关供对照：
`pcb auto bench … --macro`。

### 局部调整（`--refine --only`）

用户已确认的布局只改几个器件时，用 `--only` 只让这些器件可动，其余位姿视为固定；配合 `--no-route` 只出放置剧本。
**只放置、且层数与现板相同时，剧本不写 `pcb.stackup.set`**：重写叠层会把现场的 GND 内电层打回信号层，
而没有布线步骤去重铺它（2026-09-25 修复，`TestPlaybookPlaceOnlyKeepsStackup`）。改动器件后仍要重新走
Layout 两轮自检并请用户确认，确认后再布线。

## 执行

```bash
pcbpilot pcb dump --include-copper --out board.json --project <工程>
pcbpilot pcb auto run --board board.json --mech mech.json --power power.json --place --out-dir out/
# 先读 out/report.md 与 out/preview.svg，确认层数、电流、隔离与未布通项
pcbpilot apply out/playbook.json --project <工程> --dry-run
pcbpilot apply out/playbook.json --project <工程>
pcbpilot pcb save --project <工程>
pcbpilot doc reload --project <工程>
pcbpilot pcb drc --project <工程>
pcbpilot pcb check --project <工程>
```

## 护栏

- 报告中“heuristic”电流与“engineering default”的爬电/间隙值必须由人确认；产品安规
  （IEC 62368-1、60335、医疗 60601 等）可能要求更大距离。
- 剧本会移动器件、改层数、写整板铜：只在用户确认报告后执行，先 `--dry-run`。
- 剧本从空铜开始写；已有走线的板先用 `pcb clear --only routing,copper --dry-run` 评估再决定。
- `pcb.stackup.set` 改层数会重排叠层，必须在写铜前执行（剧本已按此顺序）。
- 平面层先作为信号层铺铜再翻转为内电层（已验证的 power-planes 配方）；翻转后若 DRC 出现同网
  Connection Error 先 `pcb pour-rebuild`（见 [pcb-routing.md](pcb-routing.md)）。
- 未布通项在报告与预览中列出，不得用 GUI 手工补线；调整 mech/power/层数后重算。
- 引擎结果是离线几何证明，最终以 EasyEDA 原生 DRC 和保存重载后的回读为准。
- **封装内 NPTH / 槽孔已建模**（2026-09-25 E2E 发现）：USB-C 等封装的定位孔是封装文档里的
  MULTI 层（layerId 12）FILL，不是焊盘；旧 dump 看不到，引擎把 CC1 走线/过孔直接布进 J2 的孔，
  原生 DRC 报 “Slot Region to Track/Via ≥ 11.8mil”。现在 `pcb dump` 经 `pcb.footprint.sources`
  输出 `footprintHoles[]`（主器件位号 + 源图元 id，圆/多边形，按同封装焊盘验证的变换落到板坐标），
  引擎把它们当作**随主器件移动、无网络**的障碍，铜到孔边 ≥ `rules.slotClearanceMil`（板规则无
  Slot Region 项时默认 0.3 mm = 11.81 mil）；`pcb check` 同阈值报 `footprint-hole-clearance` ERROR。
  旧 dump（无 `footprintHoles`）必须重新 dump，否则仍看不到这些孔。
- **重排用 `--replace <上一版 journal>`**：剧本创建的孔（MULTI fill）和区域会把 `primitiveId` 捕获进
  apply journal（`MECH_FILL_*` / `MECH_REGION_*`）；下一版 `pcb auto run --replace out-prev/playbook.json.journal.jsonl`
  会在写新机械件前只删除这些 ID，不会叠出第二套孔和禁布区（2026-09-25 连续两次重排现场验证：始终 4 孔 7 区域）。
  没有 journal 的旧剧本只能按其 `pcb.fill.create` / `pcb.region.create` 的**精确几何**逐个比对后 `--ids` 删除，
  不要用 bbox 阈值去猜（同日用 `minY>1400` 过滤误删了一个 M3 孔禁布区，随后按剧本同一步参数补回）。
- 天线等**有主**禁布区（`owner`）写入时拆成：整块 `no-wires,no-pours` + 主器件两侧的
  `no-components` 条带（按主器件本体外扩 20 mil 丝印余量）。EasyEDA 区域不能豁免器件，整块
  `no-components` 会让模块自己报 “Device to Prohibited Region”。

## 实测记录

**2026-09-25 ESP32-S3 mini（esp32MiniRequire 第一节，桌面 V3 3.2.149，ceshi PCB1）** — `live-verified`（Layout 阶段）

- 开始状态：`pcb import-changes` 后 30 件散布、无板框；逐焊盘对账 0 差异。
- 输入：`mech.json`（autoSize、M3 角孔、U3 贴上边、J2 贴下边外伸 0.5 mm、J1 贴左边）、`power.json`
  （USB_VBUS 1.5 A、+5V_TERM 2 A、VSYS_5V 1 A、LX 1.5 A、+3V3 0.8 A、USB 差分 90 Ω）、
  `--groups` 两页原理图 composition、`--layers 4`。
- 结果：板框搜索 43.5 × 43 mm；0 重叠/出板/禁布；去耦平均 71 mil；J1 进线口朝外（块库开口声明）；
  U3 天线端贴边且两侧 3 mm 无器件；save→reload 后 30 件位姿、7 区域、4 孔与剧本逐项一致，
  `semanticSha256` 两轮相同；原生 DRC 只剩未布线 Connection Error；`pcb check` 仅丝印（P9）与铺铜（P8）WARN。
- 本轮修掉的引擎缺陷（均有 `pkg/pcbauto/autoframe_test.go` 等回归）：autoSize 以导入散布为框、孔落板外；
  共享电源轨上 buck 输出电容被分给 CH340/模块（→ `--groups`）；OR 二极管无归属；声明电流的 LX
  进了电源平面；对称端子进线口朝内；快照本体把 WROOM 天线端砍半（模块伸出板边 3.5 mm）；
  天线禁布区让模块自报 DRC；重载后 270° 读成 -90.00000000000001°（dump 归一化）；`pcb check`
  把 buck 输出电容当作远离 IC 的去耦。

**同板 P7–P10（用户确认 Layout 后）** — `live-verified`

- 布线基线：确认版 fresh dump（与确认时 semanticSha 相同）；`pcb auto run` 不带 `--place`。
- 结果：4 层 TOP / IN1-GND 平面 / IN2 分区（+3V3、VSYS_5V、USB_VBUS、+5V_TERM）/ BOTTOM+GND 铺铜，
  另补 TOP GND；30/30 布通，扇出 55、信号过孔 21，原生 DRC 通过，逐焊盘对账 0，`pcb check` 0 ERROR，
  save→reload 后 `contentSha256` 不变。
- 过程中实测暴露并修复：U0RXD 距 U3 NC 焊盘 5.92 < 5.98 mil（引擎容差 0.1 放过）→ 交付严格 DRC + 微修（顶点外移
  0.08 mil）；两个 VSYS_5V 扇出孔相距 7 mil（Hole to Hole）→ 板规则孔距 + 同网共享扇出孔；21 个焊盘内过孔 →
  只对 IC/模块散热焊盘开放；ESD 朝向使 USB P/N 扭绞 → 布局扭绞代价（确认版布局未动，建议项：D3 转 180°）。
- 5 块真实板回归（mipi/bbclaw/szpi/rk3568/k230）用于把关：菊花链与全局路由余量都会让布通率下降，已关闭/撤回。
- 原理图→PCB 邻近核对（确认版布局，边缘距离）：降压 L1/R1/R2/C1 距 U1 ≤ 0.6 mm，输出电容 C2/C3 距 L1 3V3 脚
  1.8–1.9 mm；U3 去耦 C6/C5 距 3V3 脚 1.5/2.5 mm；ESD D3 贴 USB-C。**缺陷**：EN 复位 RC 电容 C7 距 EN 脚 10.2 mm
  → 新增 `pin-filter` 角色（IC 信号脚到地/电源的电容），同板重排后 3.5 mm；确认版布局未改动。
- 晶振（szpi CH334F 的 X1）：负载下同一 seed 曾被放到 36 mm 外，另一次 0.4 mm —— 退火按墙钟冷却且辅助件归属
  依赖 map 顺序。改为按步数冷却 + 排序遍历后同一 seed 逐字节一致，X1 距时钟脚 3.8 mm（人工 4.1 mm）。

**同板布局调整（用户要求 C7 靠近 EN、D3 转向）** — `live-verified`（Layout 阶段，待用户确认）

- 输入：确认版 fresh dump `adj0.json` + 两页 composition groups；`pcb auto run --place --refine --only C7,D3 --no-route --layers 4`。
- 结果：只动 C7 (110,1310,90°) → (395,1385,90°)、D3 (910,410,0°) → (915,405,90°)；C7 EN 脚到 U3 EN 脚 3.29 mm
  （原 10.2 mm）；USB P/N 扭绞 1 → 0。剧本只有 place-C7 / place-D3 / save（不写叠层）。
- 现场：先 `pcb rip-up` + 按 dump 中 9 个铺铜的精确 ID `pour-delete`，再 apply；save → reload 后 30 件、7 区域、4 孔不变，
  C7/D3 位姿与剧本一致；`pcb silk-align --refs` 修 3 处丝印压焊盘和 D3 侧向位号；`pcb check` 0 ERROR（只剩布线前的
  “电源未铺铜”）；原生 DRC 只剩未布线 Connection Error。
- 用户确认后重布（`pcb auto run` 不带 `--place`，基线为确认版 fresh dump）：30/30 布通，综合分 78.2 → 92.2（调整前同板），
  信号过孔 19 + 扇出 55；原生 DRC 通过 0 条；`pcb check` 0 ERROR；逐焊盘对账 0（原理图连通性必须当场重读，旧文件里有已删位号）；
  save → reload 前后 `contentSha256` 相同。`pcb check` 曾报 USB_DP 过孔“单层”：TOP 桥线穿过过孔焊盘而不在中心结束，属误报，
  已改为按过孔半径判接触（`TestPcbCheck_ViaOnBridgeTrackOK`）。
- 收尾：`project export` 导出 `.epro2` 备份（916515 字节，ZIP 校验通过；曾因 CLI 响应 1 MiB 上限截断报
  “unexpected end of JSON input”，上限已改 32 MiB），随后 `pcb clear --no-preserve-outline` 与两页 `sch clear`，重载后均为空。


**同板 E2E 复盘 F6/F7（2026-09-25，离线重放现场 dump r2/r4）** — 引擎修复 `offline-verified`，现场待下一轮
- F6：默认栅格报“100% (30/30)”但 DRC 1、+5V 两条平面连接未接通。原因 ① 安装孔栅格禁区只封 `Dia/2+Keep`，
  少了另一半间距，D1.1 的 +5V 扇出孔落进 M3 保持环 2.6 mil，终检又把整条 +5V 桥接一起删掉；② USB 焊盘 pad-track
  差 0.06 mil 时微修沿“违规点→焊盘中心”方向移，细长焊盘上几乎平行于边，移不开。修法：禁区补 `Clearance/2`；
  终检中扇出孔/短线违规先在同焊盘周边换位（每个候选用精确 DRC 复核），换不成才报 `fanout-drc`，不再拆网；
  微修按违规双方精确最近点的法向移动（顶点 / 整段 / 锚定端外侧摆动），过孔同样可微移（同网线端跟随）。
  CLI 顶行改为 `signal x% (a/b), plane connections c/d`，report.md 分列“信号布通率 / 平面/地连接”。
- F7：`pcb check` 报 via–U1.36 5.96 < 5.98 mil 而引擎 DRC 0。原因不是距离算法，而是 `--mech` 的 edge 吸边在模型里把
  U1 移了 0.05 mil，剧本（无 `--place`）不搬器件，引擎按不存在的位置布线。修法：不带 `--place` 时 mech 固定/贴边件
  保持实测位姿（`ApplyMechInPlace`，偏差 >0.5 mil 只记 note）。
- 离线重放：r2 默认栅格 DRC 0、0 未接通；r4 用现场 route2 铜重放，严格 DRC 检出 pad-via 5.95 mil，微修移孔 0.06 mil 后 0。
  回归：`pkg/pcbauto/microfix_test.go`。

**同板 E2E 修复复测（2026-09-25，桌面 V3 3.2.149，连接器 0.2.10）** — `live-verified`

- 封装 NPTH：`pcb dump` 读出 J2 两个定位孔（e45/e46，Ø29.5 mil，`pad-verified`）与板规则槽孔间距 11.81 mil；
  `pcb check` 的 `footprint-hole-clearance` 与原生 DRC 逐条一致（4 条）。新引擎默认参数重布：信号 30/30、平面 54/54、
  综合分 92.3（高速重试自动选 2.88 mil 栅格），原生 DRC 通过；带 `--mech` 的仅布线运行不再重复写孔/区域（7 区域 / 4 孔）。
- 丝印：`pcb silk-align` 一轮收敛，`unresolvedPairs` 为空，`pcb check` silkOverlap=0（0.2.9 时 C2/C3 三轮不收敛）。
