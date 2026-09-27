# 配方：电源预算 → `power.json` → 线宽 / 间距 / 过孔

目的：让 `pcb auto` 按**真实电流与电压**定线宽、间距和换层过孔数，而不是按网名猜。
不给 `power.json` 时，引擎按网名估电流并在报告里标“需要确认”——那只是占位，不是设计。

**先算，再手填**：`pcbpilot sim power` 从原理图算出每个网的电压和每个焊盘的电流（含 buck
纹波与多输入源场景），输出 `sim.json`；第 2 节的公式它都已按真实分压电阻/器件模型执行，
手填前先看它的 `worst` 结果与 warnings/assumptions，见 [power-sim.md](../power-sim.md)。

## 1. 列出电源轨

```bash
pcbpilot pcb auto analyze --board board.json --json > analysis.json
```

在 `result.analysis.nets` 里取 `role` 为 `power`、`ground`、`switch` 的网。核对：每个真实电源轨
都在（名字不像电源的轨会被当成 signal——例如 `MOTOR_SUPPLY` 应在 `power.json` 里声明）；
被误判为电源的网（例如 `VREF` 基准，几乎没有电流）也在 `power.json` 里写小电流覆盖。

## 2. 计算每个轨的电流（写进 `currentA`）

| 情况 | 公式 / 取值 | 记录 |
|---|---|---|
| 负载轨（3V3、1V8…） | Σ 负载最大工作电流（数据手册 “max”/峰值，不用典型值） | 每个负载的料号、页码 |
| LDO 输入 | I_in = I_out + I_q | LDO 料号与 I_q |
| DC-DC 输入 | I_in = V_out·I_out / (V_in,min·η)，η 取手册在该负载点的效率 | 效率曲线页码 |
| 开关节点 `SW/LX` | 电感峰值电流 I_L,pk = I_out + ΔI_L/2 | 电感纹波计算 |
| 电机 / 加热 / LED 灯带 | 堵转或满载电流 | 规格书 |
| 电池 `VBAT` | 最大放电电流（含充放电同时） | 充电 IC 设定 |
| USB `VBUS` | 输入协商上限（默认 0.5 A、BC1.2 1.5 A、Type-C 1.5/3 A）或下游 Σ | 连接器与协议 |

再乘 **1.25–1.5 裕量**（写明取值）。地网不用声明：引擎把地当平面，地的**走线**按最大单轨回流定宽。

## 3. 写 `power.json`

```json
{
  "tempRiseC": 10,
  "rails": [
    {"net": "VBUS",  "voltage": 5.0, "currentA": 2.0},
    {"net": "+3V3",  "voltage": 3.3, "currentA": 0.8},
    {"net": "VM",    "voltage": 12,  "currentA": 3.0, "plane": true},
    {"net": "VREF",  "voltage": 2.5, "currentA": 0.01}
  ],
  "diffPairs": [["ETH_TXP", "ETH_TXN"]],
  "diffOhm": 90,
  "singleEndedOhm": 50,
  "coated": false
}
```

| 字段 | 单位 | 默认 | 含义 |
|---|---|---|---|
| `tempRiseC` | °C | 10 | 允许导体温升。消费类 10，紧凑/高温环境 5，放宽到 20 会显著变窄 |
| `rails[].net` | — | — | 与原理图网名**完全一致**。开关节点（`SW`/`LX`）可声明电流用于定宽，但仍保持 `switch` 角色，不进电源平面 |
| `rails[].voltage` | V | 网名推断 | 决定间距（IPC-2221B） |
| `rails[].currentA` | A | 网名启发式 | 决定线宽与过孔数 |
| `rails[].plane` | bool | 自动 | 记录“该轨应走平面/铺铜”的意图。**当前版本只记录不决策**（`planned`）：4 层以上所有电源轨都进电源分割平面，2 层板电源轨走计算线宽的走线 |
| `diffPairs` | — | 自动识别 | 名字识别不到的差分对 |
| `diffOhm` / `singleEndedOhm` | Ω | 90 / 50 | 阻抗目标 |
| `coated` | bool | false | 三防漆：外层间距用 IPC-2221B B4 列 |

## 4. 引擎怎么换算（读报告时对照）

- **外层线宽**：IPC-2221 `I = 0.048·ΔT^0.44·A^0.725`（A 为截面积 mil²），按外层铜厚（默认 1 oz = 1.378 mil）
  求宽；**内层**按 IPC-2152 结论用同一系数、按内层铜厚（默认 0.5 oz）求宽。
- 下限：电源/地 ≥ 10 mil，开关节点 ≥ 20 mil，信号 = 板规则线宽；电流推出的宽度向上取 0.05 mm。
- **间距**：`max(板规则, IPC-2221B(电压))`；>30 V 时报告写出依据。
- **过孔数/换层**：`ceil(I / I_via)`，I_via 按钻孔直径、0.7 mil（18 µm）镀层求。
- **缩颈**：粗线进细间距焊盘时，在焊盘附近自动降到工艺最小线宽，离开后恢复。

参考量级（ΔT=10 °C，1 oz 外层）：0.5 A≈10 mil（下限），1 A≈11.8 mil，2 A≈31 mil，3 A≈55 mil。

## 5. 核对证据

```bash
pcbpilot pcb auto analyze --board board.json --power power.json
```

报告第 2 节每个声明的轨 `来源` 应为 `declared`；没有 `heuristic` 残留；线宽与上表量级一致。
若某轨线宽大到放不进连接器焊盘，说明要么电流过大、要么需要多焊盘并联或铺铜区（`"plane": true`）。

## 6. 声明电流 vs 仿真电流（`--sim`）

有 `pcbpilot sim power` 的逐脚电流时，一起传入：

```bash
pcbpilot pcb auto run --board board.json --power power.json --sim sim.json --out-dir out/
```

| 情况 | 采用 | 报告 |
|---|---|---|
| 轨在 `power.json` 里声明 | 声明值（设计意图）定整网线宽；逐脚电流仍用于分段收窄、每焊盘过孔和 IR drop | 声明 ≥ 仿真：`why` 写明；声明 < 仿真：**告警**，复核预算 |
| 轨只在仿真里 | 仿真电流，来源 `simulated`（写场景） | 2.1 节列出仿真与采用值 |
| 两处都没有 | 原逻辑（网名 / 启发式） | 同第 5 节 |

- 仿真的 `worst` 场景优先；没有时取各场景逐脚最大值。开关节点按纹波 RMS 定宽、峰值定过孔。
- 仿真是**逐焊盘**的，声明是**整轨**的：声明给上限（主干宽度），仿真决定分支怎么收窄。
  若 IR drop 超预算，先回宽已收窄段，再带更宽线宽重布；仍不够就要改拓扑（加铺铜、换层、挪器件）。
- 预算 `--ir-budget 2%,30mV`（默认，取大；>5 V 轨只用百分比），也可在 `power.json` 写
  `"irBudget": {"pct": 2, "mv": 30}`。

## 常见错误

- 用典型电流 → 线宽偏窄；用输出电流当 DC-DC 输入电流 → 高压侧偏窄或偏宽。
- 网名大小写不同（`+3v3`）→ 声明不生效，报告仍是 `heuristic`。
- 把 `VREF`、`VDDA` 这类小电流轨当大电流 → 浪费面积；给它们写小电流即可。
