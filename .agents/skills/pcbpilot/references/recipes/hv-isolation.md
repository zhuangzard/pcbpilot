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
  `mop`（`MOOP`/`MOPP`）、`mopCount`（1/2）、`transient`（`mains`/`secondary`/`none`，缺省按两侧 kind 推断：
  有 `mains` → mains；两侧都是 SELV/isolated-secondary/floating/patient → secondary；否则 ES1 电压以下
  none，其余按 mains 保守处理）。
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
   内层 ≥ 间隙/2；自身焊盘附近的缩颈区放行。独立 DRC 对两域的铜按该对**间隙**判（所有层）。
5. **开槽**：跨域器件两侧焊盘最近距离 < 爬电且 ≥ 间隙时，在两排焊盘之间规划铣槽：宽 = 该对槽宽下限
   （IEC 60664-1 X 值：PD1 0.25 mm、PD2 1.0 mm、PD3 1.5 mm），长度 = 焊盘排跨度 + 2e，
   e 使绕槽端的沿面路径 `2·√(e² + ((g−w)/2)²) + w ≥ 爬电`。槽写进剧本为 `pcb.fill.create`（layer = MULTI
   = 板框挖槽，与 `pcb slot` / 安装孔同一 typed 原语，`MECH_FILL_*` 入 journal，`--replace` 可精确删除），
   同时作为路由障碍。间距 < 间隙（空气路径，开槽无效）或槽放不进两排焊盘之间时不开槽，报告要求换宽体器件。
6. **禁铺铜**：有布局时隔离带各段写成 `pcb.region.create`（no-wires + no-pours）；只布线时按领地边界描出
   宽 = 爬电的带，写成 no-pours 区域（`iso-moat-*`），防止 EasyEDA 覆铜按板级间距贴近另一域。
7. **布线后核查**：两域的焊盘/走线/过孔两两计算：直线距离 < 间隙 → `iso-clearance`；同一外层上沿面最短路径
   （绕过宽度 ≥ 槽宽下限的铣槽/挖槽）< 爬电 → `iso-creepage`。结果在 `report.md`「安规隔离」与
   `plan.json result.isolation`。

## 5. 核对

```bash
pcbpilot pcb auto run --board board.json --intent intent.json --place --out-dir out/
pcbpilot apply out/playbook.json --project <工程> --dry-run      # 看 iso-slot-* / iso-band-* / iso-moat-* 步骤
pcbpilot pcb check --intent intent.json --strict                   # 现场：两域铜皮的间隙 / 爬电
pcbpilot pcb check --intent intent.json --board board.json --json  # 离线：只跑隔离规则
```

- 报告「安规隔离」：每对的标准、间隙、爬电、槽宽下限与来源；开槽尺寸；核查 0 违规。
- `preview.svg`：隔离带与槽位置；没有走线穿过隔离带。
- apply 后 `pcb save` → `doc reload` → `pcb drc` 与 `pcb check --intent` 回读。

## 能力边界

- 覆铜不参与核查（由禁铺铜区域保证）；内电层是否遵守区域规则以 EasyEDA 重铺后的原生 DRC 为准。
- 贯穿绝缘距离（内层到外层、薄层绝缘）、灌封、固体绝缘、Y 电容额定值、变压器内部绝缘不在本工具范围。
- 爬电只计外层沿面；槽的绕行按凸多边形槽计算；圆弧导线按弦处理。
- 表值是工程参考：以你的产品标准版本和认证实验室的判定为准。

## 常见错误

- 两侧共用同一个 `GND` 网名 → 看不出两个域（原理图错误）；intent 里把同一个网写进两个域时以 `nets.<net>.domain` 为准。
- 光耦型号写成 “光耦” 或留空 → 旧推断认不出桥；有 intent 时按焊盘网络所属域判断，不依赖型号。
- SOP 光耦用在加强绝缘：两排焊盘面距常 < 4.6 mm（230 V 加强爬电）→ 引擎开槽；< 3.0 mm（加强间隙）→ 必须换宽体。
