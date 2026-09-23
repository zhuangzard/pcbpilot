# 配方：高速信号（差分、阻抗、等长、参考平面）

目的：让引擎认出高速网络、选对层数与阻抗线宽、差分对贴线布、对内等长，并在布线后给出可执行的修正项。

## 1. 命名要让引擎认得出

差分识别需要**接口关键字 + P/N 后缀**同时满足：

| 关键字（任一） | 后缀对 | 例 |
|---|---|---|
| USB、MIPI、DSI、CSI、LVDS、ETH、RGMII、SGMII、HDMI、TMDS、PCIE、SATA、CAN、RS485/485、DP、DM | `_P/_N`、`P/N`、`DP/DM`、`DP/DN` | `USB_DP`/`USB_DM`、`MIPI_DSI_0P`/`MIPI_DSI_0N`、`ETH_TXP`/`ETH_TXN` |
| （仅 USB 数据脚） | `D+`/`D-`，且前面是开头或分隔符 | `D+`、`USB_D-` |

`LED+`、`BAT-`、`VOUT+` 是**极性**，不会被当成差分。名字不规范又不能改原理图时，在
`power.json` 的 `diffPairs` 里显式列出 `[["P网名","N网名"]]`。

## 2. 接口规则（引擎内置默认值）

| 类别 | 差分阻抗 | 对内长度差上限 | 过孔上限 | 最长 | 需要参考平面 |
|---|---|---|---|---|---|
| USB3 / PCIe / HDMI | 90 / 85 / 100 Ω | 5 mil | 2 | 6–8 in | 是 |
| MIPI D-PHY | 100 Ω | 10 mil | 2 | 6 in | 是 |
| Ethernet（MDI） | 100 Ω | 50 mil | 2 | 4 in | 是 |
| LVDS | 100 Ω | 10 mil | 2 | — | 是 |
| USB2 | 90 Ω | 100 mil | 2 | 8 in | 是（全速可 2 层） |
| CAN / RS-485 | 120 Ω | 500 mil | 4 | — | 否 |
| 时钟（XTAL/CLK） | 50 Ω 单端 | — | 2 | 2 in | 是 |
| RF（ANT/RF_） | 50 Ω 单端 | — | 0 | 1 in | 是 |

产品芯片手册的布线指南更严时，以手册为准并在报告里注明。

## 3. 层数与阻抗

- 出现 USB3/PCIe/HDMI/MIPI/Ethernet/LVDS 或 RF → 引擎至少选 **4 层**（外层紧邻 GND 平面）；
  只有 USB2 全速 / CAN / RS-485 时，2 层可以，按紧耦合差分布线，但阻抗不受控。
- 4 层按嘉立创 JLC04161H-7628（外层到 GND 约 8.3 mil，εr≈4.4）求解：差分线距取工艺最小间距，
  解出线宽（90 Ω 约 10/6 mil）。2 层板参考面在 1.6 mm 外，解出的宽度不可制造，引擎改用标准线宽并在
  报告写“阻抗不受控”。
- 需要严格阻抗时，下单前在嘉立创阻抗计算器用**实际叠层**复算，并在订单备注阻抗要求。

## 4. 布线时发生了什么

- 差分两根背靠背布线；后布的一根在距前一根“线宽+线距”处代价打折，自然贴着走。
- 布完若对内长度差超预算，在较短一根的直线段上加蛇形线（大差值）或 45° 梯形凸起（小差值），
  所有新线段都做间距检查；放不下时报告“only partly”，**不会**硬塞。
- 布完做信号完整性检查：每网长度、过孔数、对内长度差、是否跨越相邻电源分割区。

## 5. 读 SI 结果并修正

`out/report.md` 第 6 节 / `plan.json` 的 `si.findings`：

| kind | 含义 | 修法 |
|---|---|---|
| `skew` | 对内长度差超预算 | 布局让两端引脚更对称，或给该对留出直线空间（移开旁边器件）后重跑 `pcb auto run`。按网名单独重布当前 CLI 未开放（`RouteOptions.Nets` 仅库内可用，planned） |
| `vias` | 过孔超上限 | 把两端器件放同一面；该对换层处旁边加 GND 回流过孔（路线图 R-30） |
| `length` | 超最大长度 | 布局把两端拉近（这是布局问题，不是布线问题） |
| `split-crossing` | 走在分割电源平面的缝上方 | 该段改走 GND 平面邻层；或在跨缝处加缝合电容 |

apply 后把约束写进 EasyEDA，让原生 DRC 和报告也按差分对/等长组检查：

```bash
pcbpilot pcb diff-pair create --name USB --positive USB_DP --negative USB_DM --project <P> --doc <PCB>
pcbpilot pcb eq-group create --name DDR_ADDR --nets A0,A1,A2 --project <P> --doc <PCB>
pcbpilot pcb report --project <P> --doc <PCB>        # 回读 skew / spread
```
