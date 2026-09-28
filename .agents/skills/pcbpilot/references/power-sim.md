# 电源仿真：`pcbpilot sim power`

状态：`offline-verified`（ESP32 mini 回放 + MNA 单元测试 + ngspice 交叉核对；现场读取路径已实现未现场跑）。

目的：从**原理图连接 + 器件模型**计算每个网的直流电压和**每个焊盘的电流**，
让线宽由电路算出，而不是按网名或手填 `power.json` 猜。它是直流工作点 + 平均化电源树，
**不是瞬态 SPICE**：电容开路、电感=DCR、开关电源按功率守恒平均，纹波用闭式公式估算。

## 1. 运行

离线（推荐先做，可复现）：

```bash
pcbpilot sch connectivity --page P1 > sch-p1.json      # 每页一份（或 --all-pages）
pcbpilot sch connectivity --page P2 > sch-p2.json
pcbpilot sch list --page P1 > list-p1.json           # 取 Value / MPN / LCSC（逐页，--all-pages 的非活动页可能是浅数据）
pcbpilot sch list --page P2 > list-p2.json
pcbpilot sim power --connectivity sch-p1.json --connectivity sch-p2.json \
    --values list-p1.json --values list-p2.json --out sim.json --report sim.md
```

现场（只读，不写工程；逐页切换读取后恢复原页）：

```bash
pcbpilot --project <工程> sim power --pages P1,P2 --out sim.json --report sim.md
```

常用参数：`--scenario peak,worst` 只算部分场景；`--switch SW3=closed` 强制开关状态；
`--models my-models.json` 追加/覆盖器件模型；`--spice sim.cir --spice-check` 导出线性化网表并在
`ngspice` 存在时比对节点电压（默认容差 1 mV，不存在则 `spiceCheck.skipped`；安装与状态见 `pcbpilot sim tools check`，[environment-setup](environment-setup.md#仿真工具ngspice--elmer-fem)）。完整说明见
`pcbpilot sim power --help`。

`--values` 接受 `sch list` 原样响应，或 `{"parts":{"R1":{"value":"10k","mpn":"…","lcsc":"C…"}}}`。
connectivity 里的 `device.name` 常是未解析模板 `={Value}`，不给 values 时电阻/电感取不到值，
会报 “value not parseable — left open” 警告。

## 2. 读结果（`sim.json`，schemaVersion 1，固定契约）

```json
{"schemaVersion":1,"generator":"pcbpilot sim power","scenarios":["typical","peak",…,"worst"],
 "results":[{"scenario":"peak",
   "nets":{"+3V3":{"voltage":3.318,"currentA":0.52,"role":"power",
       "pins":[{"ref":"U1","pin":"2","name":"3V3","currentA":0.50,"dir":"sink"}, …]}},
   "parts":{"U4":{"model":"buck","powerW":0.198,"notes":["Vout=0.6*(1+R3/R4)=3.318V …"]}},
   "ripple":{"SW":{"iPeakA":0.68,"iRmsA":0.53},"C1":{"iRmsA":0.21}},
   "warnings":[…],"assumptions":[…]}]}
```

- 焊盘 `currentA` 是经过该焊盘的电流大小（A）；`dir`：`source`=电流从器件流出进网（供电方），
  `sink`=从网流入器件，`pass`=无直流电流（<1 nA，如电容、FB、信号输入）。
- 网 `currentA` = Σ source 焊盘电流 = Σ sink 焊盘电流（KCL，逐网成立）；地网焊盘也列出（回流）。
- `role`：`ground`、`switch`（buck 的 LX 网）、`power`（源/稳压器/负载供电脚所在网或 ≥50 mA）、`signal`。
- `worst`：逐焊盘取所有场景最大值，`pins[].scenario` 标出出处；跨场景拼合后 KCL 不再成立，
  `voltageMin/voltageMax` 给电压范围。线宽取 `worst`，或取对应场景。
- `parts[].powerW` = Σ V·I（器件吸收功率；输入源为负并给 `suppliedW`）；稳压器另给 `mode`
  （regulating / dropout / off）、`vinV/voutV/inputA/outputA/efficiency`。
- **热用平均功率，载流用峰值（v0.6.1 起）**：`currentA`/`powerW` 是该场景工作点（peak 场景 = 峰值负载），
  线宽、过孔、IR 压降、额定电流都用它。稳态热要的是时间平均：每个 peak 场景另做一次**同源同开关、负载取
  typ（模型平均电流）**的孪生求解，写入 `thermalBasis:"average-bound"`、`parts[].thermalW`、
  `nets[].thermalCurrentA`；非 peak 场景 `thermalBasis:"average"`、`thermalW=powerW`。规则：负载/IC/LED/
  稳压器（功耗 ∝ 电流：LDO (Vin−Vout)·I、buck (1/η−1)·Pout）取平均工作点功耗；I²R 类（电阻、电感、磁珠、
  保险丝、二极管、三极管、ESD、整流桥）与铜取 √(P_avg·P_peak)、√(I_avg·I_peak)——电流在 [0, I_peak]
  且均值 I_avg 时 E[I²] ≤ I_avg·I_peak（开关型突发），是不依赖占空比的上界。`parts[].mpn` 供
  `sim post-layout` 核对位号是否与板上同一器件。**模型的 `typA` 必须是平均电流**（如 ESP32 的 Wi-Fi 连接平均
  0.1 A），`peakA` 是突发峰值（TX 0.5 A）。
- **板外负载不算板上热**：`load` 模型绑在连接器位号（J/P/CN/CON/X/USB/TB…）上时，表示产品输出端子后面的
  外部负载（如反激 12 V/2 A 输出端子），`parts[].offBoard:true`、`thermalW:0`——电流仍按它算（端子、走线、
  过孔载流不变）。负载真在板上时在模型写 `"offBoard": false`。
  E2E 根因（2026-09-28 HV 反激）：J2 输出端子的 24 W 外部负载被当成 J2 焊盘上的热源，板温 8374 °C。
- `ripple`：buck 的开关网与电感给 `iPeakA/iRmsA/iAvgA/deltaIA/duty`；输入电容给
  `Iout·√(D(1−D))`，输出网上每个电容给 `ΔI/(2√3)`（整值记给每颗，偏保守）。
- 顶层 `models[]` 列出每个位号绑定的模型、匹配依据和 `confidence`。

## 3. 场景

| 场景 | 含义 |
|---|---|
| `typical` | 负载取 typ，全部输入源接入，轻触开关松开 |
| `peak` | 负载取 peak，全部输入源接入 |
| `buttons-pressed` | 有轻触开关时生成：typ 负载 + 全部轻触开关按下（上拉电阻电流） |
| `<source>-only` | 有 ≥2 个输入源时逐个生成（如 `usb-only`、`terminal-only`），peak 负载、只接该源——OR 二极管各自承担全电流 |
| `worst` | 上述场景逐焊盘最大值 |

## 4. 模型从哪里来、什么时候是假设

库：[power-models.json](power-models.json)，按 LCSC C 号 → 精确 MPN → 名称正则匹配；
`--models` 文件优先。每条模型写 `source`（数据手册/LCSC 属性出处）和 `confidence`：

- `datasheet`：数字取自手册或 LCSC 参数；`approx`：典型值/曲线读数；`assumed`：占位，需核实；
  `value`：由阻值/感值解析（被动件）。
- 没匹配到的 IC：每个供电脚按 **50 mA 假设**并出 warning——绝不静默为 0。选型后把真实
  typ/peak 写回库（写 `source`），再跑一遍。
- 通用件：R 按值、电感 DCR 取模型或描述里的 `DC Resistance`，否则 0.05 Ω 假设；
  LED 颜色取模型 → 描述 → MPN 后缀，Vf 取颜色默认（红 1.9、黄 2.0、绿 2.1/3.0、蓝白 3.0 V @ 5 mA）。
- IC 信号脚视为高阻；只有**经 ≤1 个电阻驱动 LED 的 GPIO** 被视为推挽输出（高或低），并在
  assumptions 写明。其他信号驱动（UART、DTR/RTS→三极管）不建模，电流为 0。
- 稳压器：buck 以 V(FB)=Vref 约束真实分压电阻求 Vout，Iin=V(LX)·Iout/(η·Vin)+Iq；
  LDO 为 Vout 源 + Iin=Iout+Iq，输入不足进入 dropout，低于 `vinMinV` 或 EN 低则关断。

## 5. 样例（ESP32 mini，`offline-verified`）

来源：`ceshi` E2E（2026-09-25）两页 connectivity + S0 `sch list` 值，已裁剪入
`pkg/powersim/testdata/esp32mini/`（回放测试 `TestESP32MiniReplay`）。开始状态：原理图完成、PCB 未开始。

| 场景 | +3V3 | U4 输入 (+5V) | VBUS | 5V_TERM |
|---|---|---|---|---|
| typical | 3.318 V / 0.114 A | 4.733 V / 0.089 A | 0.043 A | 0.046 A |
| peak | 3.318 V / 0.521 A | 4.656 V / 0.424 A | 0.181 A | 0.244 A |
| usb-only | 3.318 V / 0.521 A | 4.593 V / 0.430 A | 0.430 A | 0 |
| terminal-only | 3.318 V / 0.521 A | 4.628 V / 0.427 A | 0 | 0.428 A |

Vout = 0.6·(1+45.3k/10k) = 3.318 V；LED1（黄）经 IO2→R9 1 kΩ 约 1.4 mA；SW 峰值 0.68 A、
RMS 0.53 A（L1 额定 0.77 A）；ngspice 比对 16 个节点最大差 0.4 µV。旧 `power.json` 的手填值
（+3V3 0.8 A、VBUS 1.0 A、SW 1.5 A）比计算结果大 1.5–3 倍：保守但不是算出来的。

## 6. 边界

- 不是瞬态：不算浪涌、上电冲击、电容充电、ESD 事件；TVS 只计漏电。
- MOSFET、运放、比较器、电池充电器等未建模器件按 open 或假设负载处理，warning 会列出。
- 多个地网被当成同一参考点（报告里可能合并）；多轨负载需在模型中写 `rails[]`。
- 结果质量取决于模型数据：`assumed` 的数字要回到数据手册核实。
