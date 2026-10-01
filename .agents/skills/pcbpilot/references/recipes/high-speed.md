# 配方：高速信号（差分、阻抗、等长、参考平面）

目的：让引擎认出高速网络、选对层数与阻抗线宽、差分对贴线布、对内等长，并在布线后给出可执行的修正项。

## 1. 命名要让引擎认得出

差分识别需要**接口关键字 + P/N 后缀**同时满足：

| 关键字（任一） | 后缀对 | 例 |
|---|---|---|
| USB、MIPI、DSI、CSI、LVDS、ETH、RGMII、SGMII、HDMI、TMDS、PCIE、SATA、CAN、RS485/485、DP、DM | `_P/_N`、`P/N`、`DP/DM`、`DP/DN` | `USB_DP`/`USB_DM`、`MIPI_DSI_0P`/`MIPI_DSI_0N`、`ETH_TXP`/`ETH_TXN` |
| （仅 USB 数据脚） | `D+`/`D-`，且前面是开头或分隔符 | `D+`、`USB_D-` |

`LED+`、`BAT-`、`VOUT+` 是**极性**，不会被当成差分。名字不规范又不能改原理图时，在
`power.json` 的 `diffPairs` 里显式列出 `[["P网名","N网名"]]`，或在 intent 的 `spec.hsInterfaces` 里声明。

走 `intent derive` 时另外按网名认出（无需声明）：DDR 的 DQS/CK 对（`DDR_A_DQS0P/N`、`DDR_CLKA_P/N`、
`_t/_c`）、DDR 字节通道与地址命令组、HDMI/MIPI/LVDS 同端口的多对（等长组 `<前缀>_LANES`）。接口类别
按从具体到一般的顺序判定：`SSTX/SSRX/_SS`→USB3（USB3 口上的 `…_DP/_DM` 是 USB2）、PCIE、SATA、
HDMI/TMDS、MIPI/DSI/CSI、LVDS、DDR/DQS、ETH/MDI/TRD/TXP…、USB/D+/DP/DM、CAN/485——PCIe、SATA 的
`TXP` 不会再被当成以太网。

## 2. 接口规则（引擎内置默认值）

| 类别 | 差分阻抗 | 对内长度差上限 | 等长组容差（默认） | 过孔上限 | 最长 | 需要参考平面 |
|---|---|---|---|---|---|---|
| USB3 / PCIe / SATA | 90 / 85 / 100 Ω | 5 mil | — | 2 | 6–8 in | 是（≥1 Gb/s，2 层 = error） |
| HDMI（TMDS） | 100 Ω | 5 mil | 100 mil（对间） | 2 | 6 in | 是 |
| MIPI D-PHY / LVDS | 100 Ω | 10 mil | 50 mil（通道间） | 2 | 6 in | 是 |
| DDR（DQS/CK） | 100 Ω | 5 mil | 字节通道 25 mil，地址命令 100 mil | 2 | 3 in | 是 |
| Ethernet（MDI） | 100 Ω | 50 mil | — | 2 | 4 in | 是 |
| USB2 | 90 Ω | 100 mil | — | 2 | 8 in | 是（全速可 2 层） |
| CAN / RS-485 | 120 Ω | 500 mil | — | 4 | — | 否 |
| 时钟（XTAL/CLK） | 50 Ω 单端 | — | 2 | 2 in | 是 |
| RF（ANT/RF_） | 50 Ω 单端 | — | 0 | 1 in | 是 |

产品芯片手册的布线指南更严时，以手册为准：在 `spec.hsInterfaces[]` 写 `maxSkewMil` / `lengthTolMil` /
`maxVias`，intent 把它们写进每网，布线器、SI 检查和规则推送都按声明值执行。

## 3. 层数与阻抗

- 出现 USB3/PCIe/HDMI/MIPI/Ethernet/LVDS 或 RF → 引擎至少选 **4 层**（外层紧邻 GND 平面）；
  只有 USB2 全速 / CAN / RS-485 时，2 层可以，按紧耦合差分布线，但阻抗不受控。
- 4 层按嘉立创 JLC04161H-7628（外层到 GND 8.28 mil，εr 4.4）求解：差分线距取工艺最小间距，
  解出线宽（90 Ω：6 mil 间距 → 10.8 mil，4 mil 间距 → 9.0 mil；100 Ω/4 mil → 7.1 mil；85 Ω/4 mil → 10.2 mil）；
  6 层 JLC06161H-2116（4.4 mil，εr 4.2）100 Ω/4 mil → 5.0 mil。intent 与引擎用同一叠层常数
  （`pcbauto.StackupReference`）。2 层板参考面在 1.6 mm 外，解出的宽度不可制造，引擎改用标准线宽并在
  报告写“阻抗不受控”。
- 需要严格阻抗时，下单前在嘉立创阻抗计算器用**实际叠层**复算，并在订单备注阻抗要求。

## 4. 布线时发生了什么

- **差分对按一个单元布线**（2026-09-30，`pkg/pcbauto/pairroute.go`；仅限 intent 声明了接口的对——只靠网名猜出的对
  沿用旧的软偏置，和 re-couple 一样，因为间距、类限值都来自 intent）：先布**领线**（首轮取跨度短的一根；
  领线每步检查“再加一个 pitch 的铜是否合法”，贴着焊盘/管脚、给伙伴留不出位置的格子 ×1.6），再布**跟随线**：
  - 距领线中心线一个 pitch（线宽/2 + `pairGapMil` + 线宽/2，来自阻抗解/intent）的同层格子便宜（×0.4），
    取“离得最近且两线占位不重叠”的一圈——旧的软偏置环向内伸半格，两根线的占位互相重叠，协商每轮把这对当
    冲突，历史代价堆在耦合路径上，跟随线反而躲开它（ESP32 USB：85 % → 0 %）；
  - 离开这条路径（出线区以外）×2；走领线没用的层 ×3；过孔只在领线过孔旁一个孔距的环上正常计价，其他位置 ×4
    （过孔成对、同一换层序列）；出线区 = 该线自身焊盘周围 max(类出线预算/2, 2 pitch)，引脚把两根线分开的地方不罚；
  - A* 启发式按 ×0.4 缩放，保证能找到绕一点路的耦合路径（否则搜索会直奔直线方向）。
  首轮若这一单元不“好”（有未通、过孔不对称或耦合份额 < 类下限），再试另一根领线、再试两根自由布（旧软偏置），
  择优：连通 > 已耦合 > 过孔对称 > 耦合份额 > 更短。**没有任何约束单元耦合到类下限的一半**时，说明几何把两根
  线分开了（器件挡在走廊里、ESD 行列方向与走向垂直），约束只会让线绕远、离开保护器件焊盘，这对就按自由方式布
  且之后协商也逐根重布（ESP32 mini 当前布局即此情形）。协商中一对里任一根冲突，两根一起拆一起重布（沿用首轮
  选定的领线/模式）；约束随协商轮次每轮减半，最终退到旧的 0.55 软偏置，挤在密脚连接器处的几对才能互相让路。
- **单元布线只在不吃亏时保留**：带声明差分对的板，流水线对同一叠层**两种方式各布一遍**（成对单元 / 逐根旧方式），
  依次比：布通率 → DRC → 联合评分的电气组（ESD 支线、去耦/热回路、IR、长度差/跨缝/过孔）→ 高速 + 差分对发现数；
  单元方式只有在不少布通、不多违规、不伤其他电气项时才保留（notes `differential pairs: routed as units …` /
  `leg-by-leg routing kept …`）；细栅格重试两种方式都试。完成度排在耦合前面：一条没布通的连接本身就是电气失败。
  这条保护在差分对计入联合评分后仍保留：单元方式只有在电气组（现在含 `diff-pair` 项）不变差时才保留。
- **布局给差分对留走廊**（2026-09-30，`pkg/pcbauto/placer_corridor.go`；仅 intent 声明了接口的对）：沿该对的链
  （连接器 → ESD/串联件 → 芯片）两根线骨架的中线、宽 = 两线间距 + 一个 pair pitch + 间距，不属于这对的器件
  （CC 电阻、上拉、LED…）侵入走廊按每 mil ×4 计价——权重低于去耦（6–7）与端口保护（5），高于上拉（2）/信号（1.5），
  所以先让开的是无关小件。链上的直通/串联件（USBLC6、串阻）另按**朝向**计价：入口/出口那一排两脚的连线应与
  来向垂直、并从朝向来向的一侧接入（行沿来向摆放 = 远端那根要绕过近端引脚 → 换层），每端最多 150 mil；
  扭绞按**接入路径**（焊盘经所在器件边外 10 mil 的接入点）计数（`PairTwistAccess`，每次交叉按 400 mil）。
  ESP32 mini（seed 3，确定性 `PCBPILOT_BENCH_WORK=3e6`）：R4（CC）不再挡在 USBLC6 → CH340 之间，
  USB 对 0/0 过孔、单层、端部未耦合 438/500 mil，4 项对检查全过（dev：2/0 过孔、307/250 mil，3 项不过），
  ESD 支线 68 → 0 mil，联合 92.6 → 92.7。其他种子见 CHANGELOG（仍可能有 1–3 项不过，例如 USBLC6 落成行沿来向）。
- **走廊不能以布通为代价**（2026-10-01，`placer_corridor.go` + `placeab.go`）。反例：HS 压测 pcie-m2（`--place --loops 0`，
  6 个种子，确定性 `PCBPILOT_BENCH_WORK=3e6`）走廊上线后 seed 5/6 各丢一根腿（`no-legal-path`，84.6 / 92.3 %）。
  根因不是走廊太宽，而是**只有带链（中间有 AC 电容/ESD/串阻）的对才有走廊**：TX 经 C1/C2 有链，RX、REFCLK 直连
  J1 → U1 没有链，也就没有走廊。TX 的走廊/朝向/接入扭绞三项合起来把 U1 横移约 27 mil 去对齐 C1/C2，并把 CLKREQ# 上拉
  R1 挤进 RX/REFCLK 的出线区——单独去掉三项中任何一项都恢复 100 %，说明是“一对独占全部松弛”而不是某一项本身错。修法：
  1. **直连对也建走廊**：两根腿都只有“同一连接器的脚 → 同一 IC 的脚”时合成一条直连链，让它的出线区同样不许无关件进入；
  2. **多对器件按对数分摊力**：一个器件（多对 IC、密脚连接器）承载 n 个对的走廊时，每个对施加在它身上的走廊与朝向力 ×1/n
     （引脚/对密度越高，单个对能拿到的松弛越少），一个对不能再为自己把多对器件移开或转向；
  3. **布线器终裁**：有走廊的板同时布“走廊布局”与“无走廊布局”（即 eb78993a 的布局），按 安全门槛 → 布通率 → 平面开路 →
     DRC → 电气组（无走廊一方的差分对检查不得更差）→ 高速/差分 SI 发现数 择优，平手保留走廊。
  结果（6 种子，失败检查 / 布通率，基线 = 走廊前 eb78993a）：pcie-m2 均值 2.83 / 100 % → 1.33 / 100 %（每个种子 ≤ 基线；
  3ceaf3b1 为 3.50 / 96.1 %）；usb3-typec、gbe-rj45、ESP32 USB 对见 CHANGELOG v0.6.2。负例保留：不要用“把连接器和 IC
  排除出朝向代价”来修——它修好 pcie 却让 ESP32 seed 3 联合分掉到 89.1（CH340 单对，朝向正是它需要的）；按对数分摊
  对单对器件不起作用，所以 ESP32 不受影响。回归：`TestCorridorsCoverDirectPairs`、`TestCorridorABRanking`、
  `TestPlaceThenRouteNeverLosesCompletion`；种子扫描用 `STRESS_HS_SEED=<n> STRESS_HS_MODES=place make stress-hs`。
- **间距落到目标值**：栅格只能把跟随线放在“占位不与领线重叠”的最近格，比目标 pitch 多出 1–2 格
  （usb3-typec 2.4 mil 栅格：13 mil pitch 实际 16.8 mil，4 mil 间距变 7.8 mil）。布线收尾把跟随线上与领线平行、
  偏差在 3 格内的直线段平移到精确 pitch（相邻段沿自身方向滑动保持 45°；焊盘/过孔/T 接端点不动），精确 DRC
  出现新违规的对整对恢复（notes `pair gap: … snapped`）。
- 带 intent 的 ≥1 Gb/s 高速网（USB3/PCIe/SATA/HDMI/MIPI/LVDS/DDR）走“参考层”：紧邻单网平面（GND）的
  信号层正常计价，紧邻分割电源平面或没有相邻平面的层（混合 IN2-SIG+PWR 叠层下的 BOTTOM）每步 ×4。
  USB2 / 以太网 / CAN 不加价：整层 ×4 是粗手段，ESP32 mini（2026-09-27）上它让 USB_DM 为躲一段
  并未跨缝的 BOTTOM 短跳而绕过 USBLC6，ESD 支线 68 → 299 mil、联合分 92.6 → 80.6（回归测试
  `TestESP32MiniIntentKeepsESDOnPath`）。真跨缝仍由布线后的 `split-crossing` 检查报告。无 intent 的板不变。
- 带 intent 的差分对若两根走了不同路线（长度差 > 4×上限且 > 40 mil），把较长一根沿另一根严格重布
  （更强的贴线折扣）；只有全部连通、过孔不增、长度差下降时才保留，否则逐段恢复原铜。
- 布完若对内长度差超预算，在较短一根的直线段上加蛇形线（大差值）或 45° 梯形凸起（小差值），
  所有新线段都做间距检查；放不下时报告“only partly”，**不会**硬塞。小修正的最小步长是
  min(15 mil, 上限)（5 mil 的 USB3/PCIe 对差 12 mil 以前根本不试），凸起不再要求直线段两端留一个
  pitch（碰撞由逐段间距检查负责）。测长度包括扇出/BGA 逃逸线，和 SI 检查口径一致。
- 等长组（`lengthGroup` 有 ≥2 个单元，差分对算一个单元、取两根平均长度）：短的单元补到最长者
  减容差/4；差分对两根加同样长度，任何一根加不上就两根一起恢复，不制造新的对内差。
- 布完做信号完整性检查：每网长度、过孔数、对内长度差、是否跨越相邻电源分割区，以及每对的耦合与对称
  （`si.pairs[].coupling`，下节）。

## 5. 读 SI 结果并修正

`out/report.md` 第 6 节 / `plan.json` 的 `si.findings`：

| kind | 含义 | 修法 |
|---|---|---|
| `skew` | 对内长度差超预算 | 布局让两端引脚更对称，或给该对留出直线空间（移开旁边器件）后重跑 `pcb auto run`。按网名单独重布当前 CLI 未开放（`RouteOptions.Nets` 仅库内可用，planned） |
| `vias` | 过孔超上限 | 把两端器件放同一面；该对换层处旁边加 GND 回流过孔（路线图 R-30） |
| `length` | 超最大长度 | 布局把两端拉近（这是布局问题，不是布线问题） |
| `split-crossing` | 走在分割电源平面的缝上方 | 该段改走 GND 平面邻层；或在跨缝处加缝合电容 |
| `group-skew` | 等长组（HDMI 对间、DDR 字节通道/地址命令）最长−最短超容差（`si.groups[]` 给每组 min/max/spread） | 布局让组内各路两端距离接近；留出直线段给蛇形线；必要时放宽 `lengthTolMil` 须有芯片手册依据 |
| `no-reference` | 需要参考平面的高速线走在没有相邻平面的层上（2 层板；混合 IN2 叠层的 BOTTOM） | 换 4 层以上、让高速对只走紧邻 GND 的层；不得当作“通过” |
| `coupling` | 差分对“主体”（两端出线区以外）同层、间距在目标 ±max(40 %, 2 mil) 内的长度份额低于类下限（取两根里较低者） | `--place` 已为 intent 对留走廊、按来向摆正直通件；仍报时看 `report.md`「布局依据」里挡在两端之间的器件与其归属，修 mech/groups 约束后按参数重跑（不手工挪件） |
| `uncoupled` | 某一端出线区内（半径 = 类出线预算）一根线未耦合的长度超过该端预算（类预算 × 该端器件数：连接器 + ESD 算两处出线） | 两根线出引脚后尽快并拢；该端器件朝向让 P/N 顺序与走向一致 |
| `via-asymmetry` | 两根过孔数不同 | 过孔成对放在同一换层处，或两根都留在一层（典型根因：一根要从另一根的焊盘行/穿通线下钻过） |
| `layer-asymmetry` | 两根用的层集合不同（≥10 mil 才算一层） | 同上：同一换层序列 |

耦合判据（`pkg/pcbauto/si.go` 类表，出处写在代码注释；无统一数值标准，属工程初值，待真板校准）：

| 类 | 主体耦合下限 | 每端出线预算 |
|---|---|---|
| USB3 / PCIe / SATA / HDMI / MIPI / LVDS / DDR（≥1 Gb/s，上升沿 ≈50 ps） | 80 % | 150 mil |
| USB2 / Ethernet / 通用差分 | 60 % | 250 mil |
| CAN / RS-485 | 不判 | 不判 |

依据：各接口布线指南（TI SPRAAR7 高速接口布线、USB 2.0 / PCIe / HDMI 板级设计指南）一致要求两根以恒定间距边耦合走完全程、
过孔成对对称、同一换层序列，只在封装/连接器出线处允许分开；指南不给统一的百分比，数值为初值。对内长度差仍用类表的
`maxSkewMil`（`skew`）。这些发现进 SI 报告、`report.md` 第 6 节和 feedback 困难度理由；intent 声明的对还计入
**联合评分电气组的 `diff-pair` 项**（权重 0.2，得分 = 4 项检查（耦合份额、端部未耦合、过孔对称、层对称）通过的
比例；2026-09-30 起布局会给这些对留走廊，仍耦合不起来就是这块布局的缺陷）。只靠网名猜出的对仍只报告不计分，
`high-speed` 项与细栅格重试仍只看长度差/跨缝/过孔。主体 < 50 mil（端点都在出线区内，如 ESP32 mini 的 USB 链）
时耦合份额不判，只判端部未耦合与对称。回归：`TestPairSIFlagsUncoupledESP32USB`（v0.5 现场板：USB_DM 2 孔 /
USB_DP 0 孔、整对 0 mil 耦合，过去报“干净”，现在必报 `uncoupled`、`via-asymmetry`、`layer-asymmetry`）、
`TestPairCouplingSynthetic`、`TestESP32MiniIntentKeepsESDOnPath`（seed 3：ESD 支线 ≥ 89、联合 ≥ 92、USB 对 4 项全过）。

apply 后把约束写进 EasyEDA，让原生 DRC 和报告也按差分对/等长组检查：

```bash
pcbpilot pcb diff-pair create --name USB --positive USB_DP --negative USB_DM --project <P> --doc <PCB>
pcbpilot pcb eq-group create --name DDR_ADDR --nets A0,A1,A2 --project <P> --doc <PCB>
pcbpilot pcb report --project <P> --doc <PCB>        # 回读 skew / spread
```

## 6. 压力测试样例（`make stress-hs`）

`internal/app/testdata/stress/hs/<case>/`（`gen.py` 生成；真实封装几何 + 人工首版布局 + 手算
`expect.json`：线宽用独立实现的 Hammerstad-Jensen + 边耦合公式在引擎叠层上解，并用 IPC-2141 交叉核对；
限值按接口设计指南）。每例离线全链路、真实 CLI 进程内执行，**不连 daemon**：

`intent derive` → 规则计划（`planIntentRules` + 采集的宿主默认规则快照：差分对分组、每接口容差、等长组、
0 冲突）→ `pcb auto run --intent`（给定布局只布线 + `--place --loops 0`）→ SI 对账：阻抗线宽/间距 ±5%、
布线主体线宽（颈缩只许在焊盘处，每焊盘 ≤60 mil）、对内长度差、等长组差、过孔预算、分割跨越 0、
`no-reference` 0、布通率、平面连接、DRC 0、隔离（域间对、桥、禁铺区）、交流耦合电容成对摆放。

| 用例 | 内容 | 关键期望 |
|---|---|---|
| `usb3-typec` | USB3 控制器（内置翻转 mux）→ 100 nF TX 交流耦合 → 流通式 ESD → 24P 骑板 Type-C；USB2 + USBLC6；CC 5.1 k；VBUS 分压检测 | 90 Ω（4/4 mil 工艺 9.0/4.0 mil），≤5 mil，≤2 孔；USB2 ≤100 mil；`ac-coupling`，无 `usb-esd` |
| `hdmi-tx` | HDMI 发送器 → 2× 流通 ESD → A 型座；DDC/HPD/+5V | 100 Ω 7.1/4.0 mil，≤5 mil；`HDMI_LANES` 4 对 ±100 mil（按网名自动成组） |
| `gbe-rj45` | 千兆 PHY → 4 对 MDI → 分立网络变压器 → RJ45；Bob-Smith（4×75 Ω → 1 nF/2 kV → 机壳）；机壳↔GND 1 nF/2 kV；`isolationVrms` 1500 | 100 Ω，≤50 mil；GND↔CHASSIS basic、要求耐压 2121 V → 间隙/爬电 1.5 mm（IEC 60664-1 F.2，PD2），桥 T1、C21；变压器下禁铺 |
| `pcie-m2` | PCIe x1 根端口 → M.2 M-key；TX 100 nF；REFCLK | 85 Ω 10.2/4.0 mil，≤5 mil，≤2 孔 |
| `ddr3-x16` | 控制器 BGA-196 → 2× x8 DDR3 FBGA-78；字节通道点对点 + 差分 DQS；地址命令/CK fly-by → VTT 端接（6 层） | DQS/CK 100 Ω 5.0 mil；DQ 50 Ω 7.8 mil；字节通道 ±25 mil（≤2 孔），地址命令 ±100 mil（≤4 孔） |
| `usb3-2layer-negative` | 同 `usb3-typec` 网表，2 层板 | 必须报 `reference-plane-missing`（error）+ `impedance-uncontrolled`，SI 必须报 `no-reference` |

另外 `TestStressHSRealBoards` 用 `intent derive --board` 从 RK3568（4 层）和 K230（6 层）的 PCB 焊盘
重建意图、`pcb auto run --intent` 布线，报告高速发现（仅报告，不判通过）。

运行：`make stress-hs`（约 30–60 分钟）；单例 `STRESS_HS_CASE=hdmi-tx STRESS_HS_MODES=route make stress-hs`；
`STRESS_HS_REAL=0` 跳过真板；结果在 `$STRESS_HS_OUT`（默认 `$TMPDIR/pcbpilot-stress-hs`）：每例
`intent.json`、`rules-plan.json`、`<mode>/plan.json|report.md|preview.svg` 与总表 `summary.md`。
失败项就是引擎的待办——不得为通过而放宽判据；修引擎后补回归测试。

### 当前基线（2026-10-01，差分对走廊 + `diff-pair` 计分；确定性 `PCBPILOT_BENCH_WORK=3e6`，路由预算 2 min）

eb78993a（dev）与本分支同条件对比（route + place，seed 1，失败检查数）：

| 用例 | 检查 | dev 失败 | 本分支失败 | 说明 |
|---|---|---|---|---|
| `ddr3-x16` | 74 | 16 | 16 | 相同（仅 DQ 过孔超限的网不同） |
| `gbe-rj45` | 125 | 6 | 6 | 相同 |
| `hdmi-tx` | 73 | 0 | 0 | 首版本分支在 route 模式掉到 2（单元方式保留了一个 GND 焊盘断开平面的结果）：单元/逐根择优现在先比平面/地连接开路数，已修复 |
| `pcie-m2` | 67 | 6 | 5 | route 模式平面开路消失；place 模式 TX/TXC 长度差变大、`no-reference` 消失 |
| `usb3-2layer-negative` | 60 | 2 | 2 | 相同 |
| `usb3-typec` | 111 | 5 | 8 | place 模式 seed 1：USB_DP 一根未通（97.5 %） |

布局是混沌的（一个种子说明不了趋势），place 模式另跑 6 个种子（seed 1–6）的均值：

| 用例 | dev 失败 / 布通 | 本分支失败 / 布通 | 备注 |
|---|---|---|---|
| `usb3-typec` | 8.50 / 98.8 %（3 个种子未布通） | 5.33 / 99.2 %（2 个） | 变好 |
| `pcie-m2` | 2.83 / 100 % | 3.50 / 96.1 %（seed 5、6 的 RX_N/REFCLK_N `no-legal-path`） | 变差，待办：只用 `--loops 0` 时无闭环补救 |
| `gbe-rj45` | 4.83 / 94.8 % | 4.83 / 93.2 % | 仅 seed 1 不同（96.9 → 90.6 %） |

试过把连接器/芯片排除在朝向计价之外：pcie 6 个种子全布通，但 ESP32 mini seed 3 的联合分掉到 89.1
（回归门槛 92），未采用。

### 上一基线（2026-09-30，确定性模式 `PCBPILOT_BENCH_WORK=3e6`，路由预算 2 min，route + place）

dev（89382624，只加了虚拟时钟补丁）与本分支同条件对比，失败检查数相同；变化在布通率和差分对：

| 用例 | 检查 | dev 失败 | 本分支失败 | 说明 |
|---|---|---|---|---|
| `ddr3-x16` | 74 | 16 | 16 | 完全相同（DQS/CK 对两种方式都耦合不起来，保留逐根） |
| `gbe-rj45` | 125 | 6 | 6 | 相同 |
| `hdmi-tx` | 73 | 0 | 0 | 相同 |
| `pcie-m2` | 67 | 6 | 6 | 相同 |
| `usb3-2layer-negative` | 60 | 2 | 2 | route 布通 80.4 → 87.5 %（成对单元胜出），对上耦合长度 862 → 2027 mil，`coupling` 发现 4 → 2 |
| `usb3-typec` | 111 | 5 | 5 | route 布通 95 → 97.5 %；SSRX2 由“未通”变为“0/4 孔不对称” |

同一确定性条件下 5 块真板 fixture bench 两边逐项相同（mipi 100 / bbclaw 100 / szpi 90.1 / rk3568 57.2 / k230 71.1 %）。
耦合判据报出的问题主要由布局决定（走廊里的器件、ESD 穿通方向与走向垂直、连接器引脚交错）：布线器能做的是在两种方式中取
电气更好的一个并如实报告；走廊留空已由布局器承担（见上一节）。

### 历史基线（2026-09-27，stress/hs，无并行负载，路由预算 2 min）

| 用例 | 检查 | 失败 | 结论 | 剩余失败（引擎待办） |
|---|---|---|---|---|
| `hdmi-tx` | 73 | 0 | PASS | — |
| `usb3-2layer-negative` | 60 | 0 | PASS | 正确报 `reference-plane-missing` + SI `no-reference` |
| `usb3-typec` | 111 | 6 | FAIL | 给定布局 95%（RX2 一根在骑板座密脚处无路）；混合 IN2 叠层使 B 排焊盘出线在 BOTTOM 无参考（6 处 `no-reference`，40–100 mil）；布局模式 USB2 对 4–5 孔 |
| `pcie-m2` | 67 | 6 | FAIL | 5 mil 对内差剩 6–8 mil（短网全在颈缩/阶梯段，凸起放不下）；REFCLK 143 mil；布局后 TX 交流耦合电容相距 66 mil（布局器的配对项权重不足） |
| `gbe-rj45` | 125 | 6 | FAIL | RJ45 的 3/6 线对跨 4/5 线对（T568 固有）→ TRD1 差 252 mil；平面连接 2–3 个开路；布局模式 1 条未通 |
| `ddr3-x16` | 74 | 18 | FAIL | 6 层双 BGA：布通 49–75%，字节通道差 137–786 mil（容差 25），fly-by 地址差 >2000 mil，过孔超预算——BGA 区内没有蛇形空间，fly-by 仍按树布线 |

真板（`--board`，仅报告）：RK3568 4 层 `--intent` 布通 48.4%（同负载下无 intent 48.0%，引擎改动 A/B 与 dev 一致），
认出 HDMI 8、MIPI 10、PCIe 26、USB3 6、USB 6、DDR 70 网，HDMI_LANES 组差 1777 mil、5 对超对内上限、9 处 `no-reference`
（混合叠层）；K230 6 层 67.1%，认出 MIPI 28、DDR 64、USB 4，DDR 字节通道 A0/B0 差 6 mil（满足 25），
地址组 200–223 mil（超 100），MIPI 各 lane 组未布通。
