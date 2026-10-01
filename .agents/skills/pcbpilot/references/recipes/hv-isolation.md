# 配方：高压 / 低压分区与安规隔离

目的：按**真实工作电压**和产品安规标准，给每一对电压域算出电气间隙（空气）与爬电距离（沿面），
让布局、布线、开槽和检查都按这组数执行。**安规距离由人最终确认**：引擎给出的是带出处的工程参考值
（每条 `why` 都写 “engineering reference — confirm against the product standard/certification lab”）。

## 1. 两种输入方式

| 方式 | 何时用 | 距离从哪来 |
|---|---|---|
| **intent.json**（推荐）：`pcb auto run --intent intent.json` | 已跑 `pcbpilot intent derive`，或手写了电压域与安规标准 | `pkg/safety` 按标准查表（或 intent 里显式给出的值，取显式值并在低于标准值时告警） |
| 旧的推断（无 `--intent`） | 快速看一眼 | 按地网划域、按网名认市电，IEC 60664-1 PD2 / MG III 的工程默认值（`InsulationDistances`） |

有 `--intent` 时电压域、跨域器件和屏障**全部以 intent 为准**，不再按地网推断。

## 2. intent.json 里怎么写（`standard` / `domains` / `pairs`）

```json
{
  "standard": {"name": "IEC62368-1", "insulation": "reinforced", "mop": "", "mopCount": 0,
               "pollutionDegree": 2, "materialGroup": "IIIa", "altitudeM": 2000,
               "overvoltageCategory": "II", "coated": false},
  "domains": [
    {"id": "MAINS", "kind": "mains", "nets": ["AC_L", "AC_N", "L_F"], "reference": "AC_N",
     "workingVrms": 230, "workingVpeak": 325},
    {"id": "SELV", "kind": "SELV", "nets": ["GND", "+3V3", "ZC"], "reference": "GND",
     "workingVrms": 3.3, "workingVpeak": 3.3}
  ],
  "pairs": [
    {"a": "domain:MAINS", "b": "domain:SELV", "workingVrms": 230, "workingVpeak": 325,
     "insulation": "reinforced"}
  ]
}
```

- `standard.name`：`IPC-2221B` | `IEC62368-1` | `IEC60601-1` | `IEC61010-1`（`IEC 60950-1` 按 62368-1 算）。
- `insulation`：`functional` | `basic` | `supplementary` | `double` | `reinforced`；pair 上的值优先于 standard。
- `pollutionDegree` 缺省 2；`materialGroup` 缺省 IIIa（普通 FR-4，CTI ≥ 175；高 CTI 板材写 `I`/`II`）；
  `altitudeM` 缺省 ≤ 2000 m；`overvoltageCategory` 缺省 II；`coated` 只影响 IPC 功能间距。
- `domains[].kind`：`SELV` | `hazardous` | `mains` | `patient` | `floating` | `isolated-secondary`。
  `mains`/`hazardous` 或峰值 > 60 V 为危险域。
- pair 可选字段：`clearanceMm`/`creepageMm`（给了就用，低于标准计算值时报告告警）、`slotWidthMm`、
  `mop`（`MOOP`/`MOPP`）、`mopCount`（1/2）、`transient`（`mains`/`secondary`/`none`）+ `mainsVrms`
  （Table F.1 取值电压）。`intent derive` 会写出这两项：spec.domains 声明优先；mains 类域按其**市电**电压
  （不是整流后的母线电压）；有市电的设计里危险侧 = mains、两侧可触及 = secondary；都没有时留空，由标准规则
  推断（ES1 以下 none，其余按 230 V mains 保守处理并告警）。
- 没有隔离件跨接的危险域↔可触及域也成对（`bridges` 为空或只有 Y 电容等）；离线电源的一次侧地经整流桥
  与市电线是**同一个 mains 域**，不会再和市电之间出一对“绝缘”。
- 在 S0 `spec.json` 里写产品标准与使用环境，由 `intent derive` 转成这里的 `standard`；手写 intent 时直接按上表写。

## 3. 标准与查表（`pkg/safety`，`safety.Distances(pair, standard)`）

| 标准 | 电气间隙 | 爬电距离 | 其他 |
|---|---|---|---|
| IPC-2221B（及任何标准下的 functional） | Table 6-1：内层 B1、外层未涂覆 B2（>3050 m 用 B3）、涂覆 A5；按峰值电压分档，>500 V 每伏递增 | 同间隙 | — |
| IEC 62368-1:2018 | 取大：程序 1 按峰值工作电压（IEC 60664-1 F.2，加强 ×1.6）；程序 2 按市电瞬态（Table 12 = 60664-1 F.1，由市电电压与 OVC 定，secondary 降一档）→ F.2；加强/双重取高一档冲击电压 | Table 17（= 60664-1 F.4）按 Vrms、PD、MG 线性插值后向上取 0.1 mm；加强/双重 = 2 × 基本；不小于间隙 | 海拔 > 2000 m 间隙乘 Table 16 系数；报告 ES1/ES2/ES3（Table 4：30 Vrms/42.4 Vpk/60 V DC；50 Vrms/70.7 Vpk/120 V DC） |
| IEC 60601-1（MOPP） | Table 12：1 MOPP / 2 MOPP，取覆盖该工作电压的上一档 | 同表 | 耐压 1 MOPP 1500 Vrms、2 MOPP 4000 Vrms（峰值 ≤ 354 V，Table 6）；海拔系数同上 |
| IEC 60601-1（MOOP） | 按 IEC 62368-1 的表（1 MOOP = basic，2 MOOP = reinforced） | 同左 | 耐压 1 MOOP 1500 / 2 MOOP 3000 Vrms（峰值 ≤ 354 V） |
| IEC 61010-1:2010 | 基本 = max(峰值 F.2, OVC 冲击 F.2)；加强 = max(2 × 基本, 高一档冲击) | F.4 通用列（不用 PWB 列，偏保守）；加强 2 × | — |

已钉住的表值（单元测试 `pkg/safety/distances_test.go`）：62368-1 基本 250 Vrms PD2 MG IIIa 爬电 2.5 mm；
230 V 市电 OVC II 基本 1.5 / 2.3 mm、加强 3.0 / 4.6 mm；60601 MOPP 250 Vrms 1× 4 / 2.5 mm、2× 8 / 5 mm；
IPC B2 100/300/500 V = 0.6/1.25/2.5 mm；61010 CAT II 300 V 基本 1.5 / 3.0、加强 3.0 / 6.0 mm。

医疗板：`standard.name = IEC60601-1`，病人侧隔离写 `mop: "MOPP", mopCount: 2`；只防操作者的写 `MOOP`。
不写 `mop` 时按 MOPP（更严）并在 `why` 里说明。

## 4. 引擎做了什么（`pcb auto run --intent`）

1. **每网意图**：`intent.nets` 的线宽（outer/inner）、电流、换层过孔数、间距、差分对以 `source: "intent"` 覆盖
   `--power` / `--sim` / 推断；`netClasses` 的线宽/间距作为下限。
2. **电压域 → 分区**：非跨域器件只在自己域的分区里；跨域器件（光耦、变压器、隔离器，以及任何焊盘同时落在
   两个受绝缘约束的域上的器件，如 Y 电容）骑跨隔离带。
3. **隔离带宽度 = 该对的爬电距离**（板面上沿面距离起控制作用，爬电 ≥ 间隙）。带内禁铜禁过孔；跨域器件所在
   位置的带段打开（否则引脚排距小于带宽的器件无法接线），那一段由第 4、5、6 条保证。布局代价还要求不同器件上
   两域焊盘相距 ≥ 爬电距离。
4. **逐对间距（per-pair）**：路由器按“最近焊盘所属域”把板面分成领地，每个域的铜在外层离对方领地 ≥ 爬电/2、
   内层 ≥ 间隙/2。领地是直线、对半分的近似，只对“深入自己领地 ≥ 半个要求”的焊盘成立；跨域器件的焊盘排和两域
   贴得近的器件（**夹挤焊盘**）不满足，所以路由器对这些焊盘另做**精确判定**：外层按绕槽的沿面路径判爬电、按直线
   判间隙，内层按直线判间隙，不足就不放铜。自身焊盘附近的缩颈区里领地放行，但对**每一个**对方焊盘仍精确判定：
   铜离对方焊盘不得近于该对要求，或者（跨域器件自己的焊盘排，由槽承担）不得近于本网自身最近焊盘已有的距离。
   2026-09-30 修正的两例（都是负载下布局退火被截短后出现的几何）：光耦 GND 焊盘的缩颈放行让 GND 扇出过孔离
   变压器 AC_N 脚 155.9 mil；AC_N 过孔在光耦 AC 排旁、缩颈区外，绕槽端到光耦 ZC 脚 180.2 mil——均 < 181.1 mil
   加强爬电。独立 DRC 对两域的铜按该对**间隙**判（所有层）。
5. **开槽**：跨域器件两侧焊盘最近距离 < 爬电且 ≥ 间隙时，在两排焊盘之间规划铣槽：宽 = 该对槽宽下限
   （IEC 60664-1 X 值：PD1 0.25 mm、PD2 1.0 mm、PD3 1.5 mm），长度 = 焊盘排跨度 + 2e，
   e 使绕槽端的沿面路径 `2·√(e² + ((g−w)/2)²) + w ≥ 爬电`。槽写进剧本为 `pcb.fill.create`（layer = MULTI
   = 板框挖槽，与 `pcb slot` / 安装孔同一 typed 原语，`MECH_FILL_*` 入 journal，`--replace` 可精确删除），
   同时作为路由障碍。间距 < 间隙（空气路径，开槽无效）或槽放不进两排焊盘之间时不开槽，报告要求换宽体器件。
6. **禁铺铜**：有布局时隔离带各段写成 `pcb.region.create`（no-wires + no-pours）；只布线时按领地边界描出
   宽 = 爬电的带，写成 no-pours 区域（`iso-moat-*`），防止 EasyEDA 覆铜按板级间距贴近另一域。
7. **高压器件自身焊盘与同域 ΔV**：高压网的 IPC 间距大于器件自身焊盘距（MB10S、1206 分压电阻、DPAK）时，
   在自身焊盘缩颈范围内铜皮离同一器件的其他焊盘最近可到“自身焊盘距”（不更近）；同域两网按同时可能出现的最大
   电压差查 IPC-2221B（分压链相邻节点）。两者只放宽**同域**网间要求，不放宽域间 pair；布局按高压网间距加 halo。
8. **不可行**：跨域器件焊盘距 < 间隙、或槽放不进两排焊盘之间 → `result.isolation.infeasible` + stderr
   `isolation: INFEASIBLE` + 报告醒目列出 + `pcb check --intent` 的 `iso-infeasible` ERROR。
9. **布线后核查**：两域的焊盘/走线/过孔两两计算：直线距离 < 间隙 → `iso-clearance`；同一外层上沿面最短路径
   （绕过宽度 ≥ 槽宽下限的铣槽/挖槽）< 爬电 → `iso-creepage`。结果在 `report.md`「安规隔离」与
   `plan.json result.isolation`。
10. **安全不取决于时间预算**（2026-09-30）：隔离开槽尺寸、领地+精确焊盘围栏、板边距离带都是布线的**硬约束**，
   不是有时间才做的修补——预算不够时宁可少布通，也不放违规铜；载流换层过孔阵列的补救在自己的工作量预算上跑完
   （不再因“routing budget 用完”跳过）。仍有任何安全/电气发现（隔离 creepage/clearance、不可行桥、绝缘域到可触及
   板边的 ERROR、过孔阵列不足、IR 超预算/电源网未接通、SELV 铜低于工艺板边规则）时结果带显式**不可交付**结论：`plan.json result.blockers[]`、stderr
   `NOT DELIVERABLE: …`、`report.md` 综合评分节「不可交付」原因、`feedback.json notDeliverable[]`，联合评分
   `deliverable=false`（隔离与绝缘域板边按门槛封顶 40，其余为不封顶的 `blockers`；dev 曾把 16 处市电到板边 ERROR
   的 `medical/2xMOPP` 判为可交付 76.1）。回归：`TestStarvedBudgetMainsSelvStaysSafe`（饥饿退火 +
   3e6/3e4/3e3 工作量预算）、`TestStarvedBudgetEdgeBands`、`TestStarvedBudgetViaArrays`。
11. **板边与金属安装孔是可触及面**：危险/市电/病人域的铜到板框中心线、到金属安装孔螺钉头（禁铜环外沿）
   ≥ `max(间隙, 爬电)`（`intent.edge.byDomain`，由 `safety.Distances(域 ↔ edge:accessible)` 算出，默认
   **加强绝缘** —— 板边可能被手指、接地机壳、金属支柱碰到；外壳提供另一重保护或安装件保护接地时写
   `spec.edge.insulation: "basic"`）。路由器对这些网另加到板边/螺钉头的距离场，铺铜/平面按域内缩，
   引擎 DRC 与 `pcb check copper-to-edge` / `copper-to-hole` 复核；`pcb rules apply --intent` 把该域所在
   网络类 `PP_<类>` 的 Board Outline 格写成域距离。SELV 域用默认 20 / 30 mil。原生 Creepage Distance
   规则只在计划里给建议值，不自动启用（全板、不分网，会误报所有 SELV 对）。例：230 Vac 加强绝缘
   （IEC 62368-1，PD2，MG IIIa，OVC II）→ 间隙 3.0 / 爬电 4.6 mm → 板边 ≥ 181.2 mil。

## 5. 核对

```bash
pcbpilot pcb auto run --board board.json --intent intent.json --place --out-dir out/
pcbpilot apply out/playbook.json --project <工程> --dry-run      # 看 iso-slot-* / iso-band-* / iso-moat-* 步骤
pcbpilot pcb check --intent intent.json --strict                   # 现场：两域铜皮的间隙 / 爬电
pcbpilot pcb check --intent intent.json --board board.json --json  # 离线：隔离规则 + 板边安全距离
pcbpilot pcb check --intent intent.json --board out/board.routed.json --strict  # 离线：判引擎布好的板（含槽、板边距离）
```

- 报告「安规隔离」：每对的标准、间隙、爬电、槽宽下限与来源；开槽尺寸；核查 0 违规。
- `preview.svg`：隔离带与槽位置；没有走线穿过隔离带。
- apply 后 `pcb save` → `doc reload` → `pcb drc` 与 `pcb check --intent` 回读。

## 能力边界

- 引擎生成的平面/覆铜按所属绝缘域裁剪（离对方领地 ≥ 半个要求、离对方铜皮 ≥ 整个要求，重建为行合并矩形），
  隔离核查也量平面/覆铜轮廓（`pour#…`）；EasyEDA 重铺后的实际填充仍以原生 DRC 为准。4 层板的 SELV
  地平面不再铺到高压区下方（否则只隔 0.2 mm 半固化片——贯穿绝缘距离本工具不计算）。
- 需要 > 250 mil 线宽的电流（如 30 A）不走格点布线，报 `needs-pour`：按覆铜/母排手工处理并核对温升。
- 贯穿绝缘距离（内层到外层、薄层绝缘）、灌封、固体绝缘、Y 电容额定值、变压器内部绝缘不在本工具范围。
- 爬电只计外层沿面；槽的绕行按凸多边形槽计算；圆弧导线按弦处理。
- 表值是工程参考：以你的产品标准版本和认证实验室的判定为准。
- IEC 61800-5-1（电驱）、IEC 60335、ISO 6469-3 没有独立表：按 IEC 62368-1 / 60664-1 的插值与
  `spec.domains[].transient/ratedVrms` 声明计算。IEC 60601-1 的 1 × MOOP 走 IEC 62368-1 协调（程序 2），
  与 IEC 60601-1 Table 13（源自 60950-1，230 V 市电 1 MOOP 2.0 mm）有差异，按认证路线确认。
- 两条**高压**走线之间的布线占位按“各自份额相加”（≈ 两者 IPC 间距之和），比 ΔV 要求保守至约 2 倍：
  2 层板上高压网密集时可能剩少量未布通（压力测试 flyback 实测 90–94 %），需要人工或放宽布局。
- 同域 ΔV 放宽按“同一时刻”比较（最大对最大、典型对典型）：同一域里由**不同**高压源独立供电且可能单独
  掉电的两网不在假设内，请把它们分到不同域或在 intent 里给出显式间距。

## 常见错误

- 两侧共用同一个 `GND` 网名 → 看不出两个域（原理图错误）；intent 里把同一个网写进两个域时以 `nets.<net>.domain` 为准。
- 光耦型号写成 “光耦” 或留空 → 旧推断认不出桥；有 intent 时按焊盘网络所属域判断，不依赖型号。
- SOP 光耦用在加强绝缘：两排焊盘面距常 < 4.6 mm（230 V 加强爬电）→ 引擎开槽；< 3.0 mm（加强间隙）→ 必须换宽体。
- 1206 高压 MLCC 当 Y 电容跨加强绝缘：焊盘距 1.8 mm < 3.0 mm 间隙 → INFEASIBLE，换 Y1 引线电容。
- 隔离器引脚映射/朝向画反（输出侧信号接到输入侧那一排）→ 两排都有两个域的网，焊盘距只有 0.65 mm，
  引擎报 INFEASIBLE；先核对数据手册的 side 1 / side 2。
- 高压分压链电阻数不够：每颗两端电压 > 工作电压（1206 ≈ 200 V）→ `resistor-voltage` error。

## 压力测试样例（`make stress-hv`，状态：source-only / offline-verified，未上现场）

来源：`testdata/stress/hv/<case>/`，手工设计的电路与真实封装焊盘（`gen.py` 生成 connectivity/values/board，
`spec.json`、`models.json`、`expect.json` 手写；`expect.json` 的数字按标准手算并在 `why` 写出推导）。
每个变体离线跑 `sim power → intent derive --spec → pcb auto run --intent --sim [--place]`（布局 + 仅布线两种）
→ `pcb check --intent --board out/board.routed.json`，逐项对比；`STRESS_HV_CASE=<case>` 只跑一个，
`STRESS_HV_OUT=<dir>` 保留全部中间文件。已知限制在 expect 的 `knownLimits` 里写明，报告为 KNOWN。
每个变体还核对 `pcb check` 的 **via-current**：ERROR 必须为 0，或对应 `plan.json route.viaShortfalls[]` 里一条
已报告的短缺（写明各替代方案为何失败）——没有解释的 ERROR 算失败（2026-09-30 起；此前 flyback 布局变体的
`VOUT_RAW` 换层 2/3 孔、裕量 −10.9 % 只在 notes 里提一句）。flyback 的 `2000m-e2e` 变体用离线 e2e 链的配置
（`--place --seed 1 --timeout 90s`、默认栅格、布局↔布线循环）复现该缺陷：`PCBPILOT_BENCH_WORK=3e6` 下 dev 在 L1 两焊盘之间
换层、阵列 2/3 孔（ERROR），本分支沿原路线把换层点滑到能放下 3 孔的位置（0 ERROR）。

| 样例 | 标准与条件 | 手算答案 | 关键检查 |
|---|---|---|---|
| flyback 2000 m / 5000 m | IEC 62368-1 加强，PD2，MG IIIa，OVC II；230 Vac → 12 V/2 A（UC3843、STD7N65M2、EE25、PC817+TL431、Y1） | 一次侧与市电同域，工作 324.6 V DC / 650 Vpk；间隙 3.0 mm（2500 V → 高一档 4000 V）/ 5000 m 4.44 mm；爬电 3.3×2 = 6.6 mm；PC817 行距 6.1 mm → 1 mm 槽约 251 mil；T1、Y 电容不开槽；L/N 1 A → 0.35 mm、2 过孔；VOUT 2 A → 0.80 mm、3 过孔 | 布线后与离线检查 0 隔离违规；槽与禁铺铜进剧本 |
| medical 2×MOPP / 1×MOOP | IEC 60601-1，病人侧 250 Vrms | 2×MOPP：8 mm / 5 mm（Table 12），ISO7741DW 行距 7.29 mm → 槽约 495 mil；1×MOOP：2.5 / 1.5 mm，无槽 | 布局与仅布线 0 违规、100 % |
| cat3 | IEC 61010-1 加强，CAT III 600 V | 瞬态 600 V OVC III 6000 V：基本 5.5 mm，加强 max(11, 8) = 11.0 mm；爬电 6.0×2 = 12.0 mm；ISO7741DWW、EP10 不开槽；6 × 1 MΩ 分压节点 848/707/566/425/283/142/0.7 V，每颗 141 V < 200 V | 分压链任意两网铜皮 ≥ IPC-2221B B2(ΔV)（独立实现的表） |
| inverter reinforced / functional | 400 VDC 母线，声明 `transient: mains, ratedVrms: 400`，OVC II | 高边驱动随开关节点浮动（465 V DC / 535 Vpk）；加强 5.5 / 9.4 mm，U1/U2/U3 开槽；功能性 IPC B2 535 V = 2.67 mm；30 A → 2 oz ΔT 20 °C 423 mil（10.75 mm）、30 过孔 → 报 `needs-pour` | Kelvin/栅极回路网必须布通（KNOWN：高压网份额相加） |
| negative | 同 flyback 条件，40 × 25 mm，1206 当 Y 电容 | C2 焊盘 1.8 mm < 3.0 mm 间隙 → INFEASIBLE；T1 仍开槽 | `pcb check --strict` 非零退出 |

从这些样例改参数时：换标准/海拔/污染等级只改 `spec.json`，重新 `intent derive`；换器件改 `gen.py`
里的封装与位置后重新生成，并先用 `expect.json` 写出新的手算答案，再跑 `make stress-hv`。
