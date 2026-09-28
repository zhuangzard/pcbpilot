# 设计意图：`pcbpilot intent derive` → `intent.json`

状态：`offline-verified`（ESP32 mini 两页回放 + 合成市电/光耦板 + CLI 测试；现场只读路径已实现、
未现场跑）。

目的：原理图验收后，把“这块板每个电路是干什么的、每个网该怎么走”一次算清并写成可审计数据。
没有它，原理图 → PCB 只剩位置；有了它，EasyEDA 规则推送、`pcb auto`、安规检查和反馈回路都
读同一份带理由的电气计划，而不是各自按网名猜。它**只读原理图、不写工程**。

## 1. 运行

离线（可复现，推荐）：

```bash
pcbpilot sch connectivity --page P1 > sch-p1.json     # 每页一份
pcbpilot sch list --page P1 > list-p1.json            # Value / MPN / LCSC / 描述（额定值来源）
pcbpilot intent derive --connectivity sch-p1.json --connectivity sch-p2.json \
    --values list-p1.json --values list-p2.json \
    --spec spec.json --out intent.json --report intent.md --sim-out sim.json
```

- 不给 `--sim` 时在进程内跑 `sim power`（同一套 [power-models.json](power-models.json)，
  `--models` / `--models-lib` / `--scenario` / `--switch` 与 `sim power` 同义）；`--sim-out` 把这份
  仿真另存，供 `pcb auto run --sim` 复用，保证两边电流同源。
- 已有 `sim.json`：`--sim sim.json` 复用；只给 `--sim`（无连接）时按仿真的逐网焊盘表重建网表——
  没有器件值，分类靠模型 id，额定值和电容量无法核对（相应提示降为 info）。
- 只有 PCB、没有原理图（参考板、别人的工程）：`intent derive --board board.json`（`pcb dump` 格式）
  按焊盘重建网表，器件值未知 → 电流/额定按启发式（finding `netlist-from-board`），差分对、接口、
  阻抗线宽、等长组仍按网名和板子层数给出；层数默认取板子的 `copperLayers`。
- 现场只读：`pcbpilot --project <工程> intent derive --pages P1,P2 --out intent.json`
  （逐页读取后恢复原页）。
- `--strict`：有 `error` 级 finding 时非零退出，可作回归门槛。

`spec.json`（全部可选；未知字段报错，防拼写错误）：

```json
{"standard":{"name":"IEC62368-1","insulation":"reinforced","mop":"","pollutionDegree":2,
  "materialGroup":"IIIa","altitudeM":2000,"overvoltageCategory":"II","coated":false},
 "layers":4,"outerOz":1,"innerOz":0.5,"tempRiseC":10,
 "rails":[{"net":"+3V3","voltage":3.3,"currentA":0.8,"rippleMvpp":30,"peakV":0}],
 "hsInterfaces":[{"name":"ETH","pairs":[["TXP","TXN"]],"diffOhm":100,"lengthGroup":"ETH_TX"},
                 {"name":"DDR","pairs":[["DQS0_P","DQS0_N"]],"nets":["DQ0","DQ1"],"singleOhm":50,
                  "lengthGroup":"BYTE0","lengthTolMil":25,"maxSkewMil":5,"maxVias":2},
                 {"name":"SDIO","nets":["SD_CLK"],"singleOhm":50}],
 "mains":{"vrms":230,"nets":["L","N"]},
 "domains":[{"kind":"patient","nets":["ECG_IN"],"workingVrms":0},
            {"kind":"hazardous","nets":["HV_GND"],"transient":"mains","ratedVrms":400},
            {"kind":"isolated-secondary","nets":["CHASSIS_GND"],"isolationVrms":1500}],
 "usbBudgetA":0.5,
 "rules":{"clearanceMil":6,"trackMil":6,"viaDrillMil":12,"viaDiaMil":24}}
```

声明的轨电流**优先于仿真**（`currentSource: declared`）；声明低于仿真值会出 finding。只声明
`voltage`/`rippleMvpp`/`peakV`（不写 `currentA`）的轨保留仿真/启发电流。`domains[].transient`
（`mains`/`secondary`/`none`）+ `ratedVrms` 声明该域的瞬态来源与 IEC 60664-1 Table F.1 取值电压
（电池/直流母线、测量输入等不在网名上的情况；CAT III 600 V 输入把 COM 所在地域声明为
`{"kind":"mains","workingVrms":600}` 即按 600 V 行取值）。标准未声明时
按工程默认（无危险电压 = IPC-2221B/functional；有市电或 >60 V DC = IEC62368-1/reinforced），
`standard.defaulted` 列出被默认的字段，市电板未声明标准会出 `standard-defaulted` 警告。

## 2. 读结果（`intent.json` schemaVersion 1，固定契约：只加字段，不改名不删除）

| 字段 | 含义 |
|---|---|
| `blocks[]` | 电路功能：`function` ∈ power-input / buck / boost / ldo / charger / usb-uart / mcu / rf-module / led / esd / connector / isolation / mains / sensor / motor-driver / other；`subFunction` 细分（or-ing、keys、auto-download、optocoupler…）；`core`、`parts`、`nets`（该块**拥有**的网：电源网归输出它的块，信号网归核心在网上的块，地网全局不归块）、`summary`（带仿真数字的一句话）、`notes`（如 buck 分压求 Vout、纹波公式）。 |
| `nets{}` | 每网计划：`role`（power/ground/signal/switch/hs/diff/rf/analog/clock）、`domain`、`block`、`voltage{nom,min,max,peak}`（nom=typical；min/max=通电场景包络；开关节点 peak=Vin；市电 peak=√2·Vrms）、`currentA`+`currentSource`（simulated/declared/heuristic；开关节点按纹波 RMS，`peakA` 给峰值、`dcCurrentA` 给直流）、`pins[]`（ref/pin/currentA/dir，仿真 worst）、`widthMil{outer,inner,min}`、`viasPerTransition`、`via{drillMil,diaMil,countPerTransition,perViaA,ampacityA,currentA,marginPct,platingMil,lengthMil,resistanceMOhm,dropMV,source,why}`（电源/地/开关网按电流定的过孔尺寸与每次换层并联数，`countPerTransition`=`viasPerTransition`；`source` sized/class/declared）、`clearanceMil`、`impedanceOhm`/`diffPair`/`lengthGroup`/`pairGapMil`、`interface`（USB/USB3/PCIE/SATA/HDMI/MIPI/LVDS/DDR/ETH/CAN/RS485/DIFF）、`maxSkewMil`（对内长度差）、`lengthTolMil`（等长组容差）、`maxVias`（每网过孔上限）、`netClass`、`why[]`（每个数字的出处）。 |
| `domains[]` | 参考域（每个地一个，经 0 Ω/磁珠相连的地合并；市电；无参考=floating）。经整流桥/电阻等**非隔离件与市电线相连的地**（离线电源的 PGND）与市电线**同一个 mains 域**：其 `workingVrms`/`workingVpeak` 取市电与本域直流母线/漏极峰值中较大者，域名仍按市电电压（`MAINS_230VAC`）。`kind` ∈ SELV / hazardous（>60 V DC）/ mains / patient（spec 声明）/ floating / isolated-secondary（经隔离件才连到主 SELV 域的另一个低压域），`workingVrms`/`workingVpeak`。 |
| `pairs[]` | 隔离件（光耦、数字隔离器、隔离栅驱动 UCC215xx/Si823x/1EDI…、隔离放大器 AMC1xxx、隔离电源 MGJ/UCC12xxx…、变压器、继电器）跨接的两个域之间的绝缘要求；**没有隔离件跨接的危险域↔可触及域也成对**（铜皮在板上任何位置都要守距离）。字段：工作电压、`insulation`（危险↔可触及 = reinforced 或 spec 声明值；危险↔危险 = basic；病人 = spec 声明值；SELV↔SELV = functional）、`mop`/`mopCount`（取 spec 的产品级声明：2 × MOPP → `double`）、`transient`/`mainsVrms`（程序 2 的瞬态来源：spec.domains 声明优先；mains 类域按其市电电压；有市电的设计里危险侧 = mains、两侧可触及 = secondary；否则留空由标准规则推断）、`clearanceMm`/`creepageMm`/`slotRequired`/`slotWidthMm`/`standardRef`、`bridges`（含引脚同时落在两域的任何器件，如 Y 电容）。数字统一来自 `SafetyDistances(pair, standard)`。 `requiredWithstandV`（`spec.domains[].isolationVrms` 声明的耐压，如 IEEE 802.3 1500 Vrms → √2 倍，SELV↔SELV 也按 basic）。 |
| `netClasses[]` | GND、POWER、POWER_HI（>1 A）、SWITCH、HS_DIFF（多种阻抗时 HS_DIFF_<Ω>）、HS、RF、HV_<域>（**自身峰值 > 60 V 或市电线**的网）、SIGNAL；危险域里的低压网另成 `<类>_<域>`（如 `GND_MAINS_230VAC`、`SIGNAL_HAZ_450V`），不与可触及侧同类：`trackMil`（成员最宽外层线宽）、`innerTrackMil`、`minTrackMil`、`clearanceMil`、via、阻抗。可直接推成 EasyEDA 网络类。 |
| `findings[]` | 设计提示：电感 Ipk/Irms 对额定、稳压器余量/dropout/占空比/Vin 上限/输出电流、二极管压降损耗、引脚/连接器/器件额定电流、电阻功率、电阻工作电压（`resistor-voltage`：同一场景内两端电压差对 `Max working voltage`/`Limiting Element Voltage`，分压链逐颗核）、电容耐压（电解按 80 % 降额、MLCC 按直流偏压分开提示）、缺大容量电容、USB 500 mA 预算、USB 缺 ESD、阻抗不可控、绝缘开槽、未知功耗模型、市电电流未声明。每条带 `refs`/`nets`/`suggestion`。 |

附加：`copper`（层数/铜厚/温升/参考高度/εr/叠层名/工艺最小值）、`simulation`（场景、收敛、警告、假设）、
`definitions`（约定说明）。

数字的来源：宽度 = `pcbauto.TraceWidthForCurrent`（IPC-2221/2152，外层 1 oz、内层 0.5 oz、ΔT 10 °C，
电源/地 ≥10 mil、开关节点 ≥20 mil，按 0.05 mm 取整）；`min` = 最大单脚支路电流对应宽度（不低于类下限）；
过孔 = `pcbauto.SizeVias`（IPC-2221 内层曲线作用在孔壁 π(d+t)t 上，镀铜 0.7 mil、裕量 20 %；先用类尺寸加
并联数，换层处放不下才换 JLC 阶梯上更大的钻孔；网络类取成员最大过孔，`pcb rules apply` 写入该类 Via Size；
`spec.rails[].via{drillMil,diaMil,count}` 声明的过孔只评估不改，载流不足报 `via-undersized` error、裕量不足
`via-margin` warn；`spec.rules.viaPlatingMil`/`viaMarginPct` 可覆盖；公式与算例见 pcb-design-rules.md §2.4）；间距 = IPC-2221B B2（涂覆 B4）按本网峰值电压，不低于工艺间距；
差分 = `pcbauto.SolveDiff`，在**引擎自己建的叠层**上解（`pcbauto.StackupReference`：4 层
JLC04161H-7628 h=8.28 mil εr=4.4，6 层 JLC06161H-2116 h=4.4 mil εr=4.2；2026-09-27 前 intent 用
8.4/4.05 与 3.5/4.1，与引擎叠层不一致），间隙取工艺最小（紧耦合），
USB2/USB3 90 Ω、以太网/HDMI/MIPI/SATA/LVDS/DDR 100 Ω、PCIe 85 Ω、CAN/485 120 Ω；接口识别与
对内长度差/等长容差/过孔上限统一来自 `pcbauto.ClassifyHSName`（与布线器、SI 检查、规则推送同一张表）。
HDMI/MIPI/LVDS 同一端口的多对按网名自动成等长组（`<前缀>_LANES`）；DDR 的 DQS/CK 对、字节通道
（DQ/DM/DQS → `DDR_<通道>_BYTE<n>`，±25 mil）与地址命令组（`DDR_<通道>_ADDR`，±100 mil）按网名识别
（spec 声明优先）。2 层板无相邻参考面：USB2 等标“不可控”（warn）并按标准线宽紧耦合；≥1 Gb/s 的
接口（USB3/PCIe/SATA/HDMI/MIPI/LVDS/DDR）另出 `reference-plane-missing`（error）。USB3/PCIe/SATA 的 TX
对检查串联交流耦合电容（`ac-coupling` / `-missing` / `-value` / `-mismatch`）。名字像电源轨、但仿真中只经
电阻供电且 <1 mA 的网（VBUS_DET 分压）按信号处理，不进电源平面。`spec.domains[].isolationVrms`
（如 IEEE 802.3 MDI 1500 Vrms）把该绝缘对定为 basic 并以 √2·Vrms 作要求耐压（`requiredWithstandV`）；
`pairs[].bridges` 同时列出跨在两域上的电容/电阻（机壳 Y 电容）。电路理解调用 `pcbauto.Understand`（核心/外围、转换器、域与隔离桥）。

## 3. 正例：ESP32 mini（`pkg/powersim/testdata/esp32mini`，offline-verified）

- 9 个块：POWER_IN（J1 端子 + J2 VBUS 经 D1/D2 SS34 OR 到 +5V，TVS D3）、BUCK_3V3（SY8089A，
  `0.6·(1+45.3k/10k)=3.318 V`，0.521 A 峰值，η 90%）、RF_MODULE（ESP32-S3-WROOM-1，0.1/0.5 A）、
  USB_UART（CH340C）、CONN_J2（USB-C，CC 5.1 kΩ 下拉 = sink）、ESD（USBLC6）、LED（R9 1 kΩ，1.4 mA）、
  AUTO_DOWNLOAD（Q1/Q2 + R7/R8）、KEYS（SW1→IO0、SW2→EN、R5/R6 上拉、C6 EN RC）。
- `+3V3`：3.318 V、0.52 A simulated、POWER、10 mil；`SW`：SWITCH、20 mil、peak 4.73 V（=Vin）、
  Ipk 0.68 A；`USB_DP/USB_DM`：HS_DIFF、90 Ω、10.8 mil / 6 mil（JLC04161H-7628 8.28 mil/εr 4.4）、lengthGroup `USB_D`。
- 单一 SELV_5V 域，无 pairs。finding 0 error；预期 warn：L1 峰值 0.682 A 对额定 0.77 A 仅 11% 余量、
  USB-only 0.43 A = 500 mA 预算的 86%。

负例 / 边界（合成 `pkg/intent/testdata/mains-opto`）：市电端子 → 保险丝 → HLK-5M05 → AMS1117 → MCU，
继电器切市电负载，PC817 接 24 V 现场输入。得到 MAINS_230VAC / SELV_5V / ISO_24V 三域；
SELV↔MAINS reinforced（桥 PS1、K1），ISO↔SELV functional（桥 U3）；市电网 HV 类、98.5 mil
（IPC-2221B B2 @ 325 V），线宽按保险丝额定 1 A 并提示声明真实负载电流。污染等级 3 时爬电 ×1.6
超过隔离件焊盘排间距 → `slotRequired` + `insulation-slot` 警告。

## 4. 能力边界

- 直流仿真的边界照搬 [power-sim.md](power-sim.md)：没有瞬态，交流线电流不被仿真（市电网电流用保险丝额定
  或 spec 声明）；浮空信号网电压以相连器件的电源轨为上界。
- `SafetyDistances` 当前是占位实现：`pcbauto.InsulationDistances`（IEC 60664-1 PD2/MG III 工程默认，
  reinforced = 2× basic）+ 污染等级、海拔、IEC 60601-1 MOPP 下限；是否开槽按“爬电 > 5 mm（常见隔离件
  焊盘排间距）”估计，真实焊盘几何由 pcb 阶段复核。完整标准表由 `pkg/safety` 提供，接入只需
  `intent.SafetyProvider = safety.Distances`。所有 `standardRef` 都带 “confirm”，不是认证结论。
- 市电识别靠网名（L/N/AC_L/LINE/…）或 `spec.mains.nets`，再沿保险丝/电阻/电感/压敏电阻/继电器触点
  传播；pcbauto 的域划分只认标准网名，非常规命名请在 spec 中声明。
- 块分类是启发式：分不清的核心归 `other`（summary 仍给器件与网），无关系的零件归 `MISC`。

## 5. 流向

`intent.json` 是 S6.5 的产物（见 [design-flow.md](design-flow.md)），下游只读它：
- **规则推送**：`netClasses[]`（线宽/间距/过孔/阻抗）→ EasyEDA 网络类与 DRC 规则；`pairs[]` → 域间间距。
- **`pcb auto`**：`nets{}` 的宽度/过孔/差分/优先级、`blocks[]` 的分组与 `domains[]` 分区；`--sim-out`
  的 `sim.json` 给逐段线宽和 IR 压降。
- **安规检查**：`domains[]`/`pairs[]` + `standard`。
- **反馈**：布线或 DRC 回读与 `intent.json` 对账（线宽、间距、阻抗、开槽）；不符时改参数或 spec
  重新 derive，而不是改 `intent.json` 本身。
