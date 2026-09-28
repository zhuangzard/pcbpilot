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

- 差分两根背靠背布线；后布的一根在距前一根“线宽+线距”处代价打折，自然贴着走。
- 带 intent 的高速网（有 `interface` 或等长组）走“参考层”：紧邻单网平面（GND）的信号层正常计价，
  紧邻分割电源平面或没有相邻平面的层（混合 IN2-SIG+PWR 叠层下的 BOTTOM）每步 ×4。无 intent 的板不变。
- 带 intent 的差分对若两根走了不同路线（长度差 > 4×上限且 > 40 mil），把较长一根沿另一根严格重布
  （更强的贴线折扣）；只有全部连通、过孔不增、长度差下降时才保留，否则逐段恢复原铜。
- 布完若对内长度差超预算，在较短一根的直线段上加蛇形线（大差值）或 45° 梯形凸起（小差值），
  所有新线段都做间距检查；放不下时报告“only partly”，**不会**硬塞。小修正的最小步长是
  min(15 mil, 上限)（5 mil 的 USB3/PCIe 对差 12 mil 以前根本不试），凸起不再要求直线段两端留一个
  pitch（碰撞由逐段间距检查负责）。测长度包括扇出/BGA 逃逸线，和 SI 检查口径一致。
- 等长组（`lengthGroup` 有 ≥2 个单元，差分对算一个单元、取两根平均长度）：短的单元补到最长者
  减容差/4；差分对两根加同样长度，任何一根加不上就两根一起恢复，不制造新的对内差。
- 布完做信号完整性检查：每网长度、过孔数、对内长度差、是否跨越相邻电源分割区。

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

### 当前基线（2026-09-27，stress/hs，无并行负载，路由预算 2 min）

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
