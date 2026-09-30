# 设计后仿真：`pcbpilot sim post-layout`

状态：`offline-verified`（解析解验证 7 项 + ESP32-S3 mini 现场回读板回放；Elmer 交叉校验的输入包
已实现并单测，真实 ElmerSolver 运行本机未安装、未验证）。

**设计前仿真**（`sim power`、`intent derive`）从原理图算每焊盘电流和器件功耗，决定线宽/过孔；
**设计后仿真**在**布完线的真实铜皮**上复核：同一份电流在实际走线、灌铜、负片平面、过孔上产生多少
压降、电流密度和温升。输入是 EasyEDA 回读的铜（`pcb dump --include-copper`），所以手工布线的板
和 `pcb auto` 的板一样能验。边界是**板级**：裸板静止空气、上下表面自然对流，不含外壳、风扇、
气流（不是 CFD）；目的是板上安全——热和损耗在哪、哪段线必须加宽、哪个瓶颈/拐角挤电流。

## 1. 运行（现场板 → 离线仿真 → 报告）

```bash
# 1) 只读回读真实铜皮（显式 save、有界 reload 后再 dump，证明是落盘版本）
pcbpilot --project <工程> pcb dump --include-copper --out board.json
# 2) 设计后仿真（sim.json / intent.json 来自 S6.5 的 intent derive --sim-out）
pcbpilot sim post-layout --board board.json --sim sim.json --intent intent.json \
    --out post.json --report post.md --svg-dir heatmaps/ \
    [--plan out/plan.json] [--feedback out/feedback.json] [--elmer-check]
# 3) 设计报告第 6A 章 + 封面结论（热图与 Elmer 输入包自动进版本包）
pcbpilot report design --out-dir reports/<name> --post post.json --sim sim.json --intent intent.json \
    --board board.json --reload-board board.reload.json --drc drc.json …
```

`pcb auto run … --sim sim.json --post-sim` 在布线后对**自身结果**（`board.routed.json`，引擎输出，
不是现场回读）跑同一流程，写 `post.json / post.md / heatmaps/`，并把铜皮建议并入 `feedback.json`；
`--report-dir` 时自动带进报告。落地（apply）后仍须对现场板重新 dump 再跑一次，现场结果才算数。

常用参数：`--cell 0.5`（mm，大板自动变粗，保持每层 ≤ 60k 单元）；`--ambient 25`；
`--h-top/--h-bottom 10`（自然对流 8–12 W/m²K）；`--emissivity 0.9` 打开线性化辐射（默认关，偏保守）；
`--plating 0.7`（mil，JLC 18 µm）；`--via-dt 10`；`--ir-budget 2%,30mV`；`--temp-rise`（默认取
intent `copper.tempRiseC`）；`--margin 1.2`；`--j-max`（A/mm²，默认按 IPC 逐线宽推导）；
`--plane 15=GND` / `--plane 15=none`；`--scenario peak,usb-only`；`--outer-oz/--inner-oz/--board-thickness`
（默认取 intent 的 copper 与 stackup 字符串）。

## 2. 模型（与 `pcbpilot sim post-layout --help` 一致）

- **铜皮栅格**：每层按 `--cell` 栅格化，每单元 5×5 子采样：走线/圆弧为胶囊，灌铜为带 ARC 的复杂多边形
  （偶奇规则，`ARC` 角度 y 向上逆时针为正，已用灌铜避让拐角实测核对），热焊盘辐条（`stroked-thermal-spoke-path`）
  按描边宽度当走线，焊盘按形状（RECT/OVAL/ELLIPSE + 旋转），过孔环。MULTI 层填充与封装 NPTH 视为开孔。
- **负片平面（内电层）**：`pcb dump` 不列出负片层的铜。某内层**没有任何铜对象**时，按地网络
  （仿真 role=ground、引脚最多者）的整层平面建模：板框内缩 copper-to-edge，其他网络的铜（过孔、THT、
  走线、焊盘）+ 规则间距挖出反焊盘，同网过孔直连（忽略热焊盘辐条电阻）。这是**假设**，写入
  `assumptions[]`；`--plane 15=GND` 显式声明，`--plane 15=none` 关闭。只有铺铜轮廓、没有灌铜结果的
  铺铜（pcb auto 结果）按同样的“轮廓 − 他网铜 − 间距”处理。
- **直流**：每个 power/ground/switch 网、每个场景单独解（各场景满足 KCL，最坏取最大；只有合并 worst
  时退化为逐供电源包络）。走线 = 一维电阻 ρL/(w·t)，在 T 接点、过孔、焊盘中心及每个单元处切分；
  走线在焊盘或过孔环内的部分被更宽的铜短接；面铜单元链接 G = (t/ρ)·min(覆盖率调和平均, 公共边铜占比)；
  过孔按叠层真实 z 分段，R = ρh/(π(d+t)t)。参考 = 供电器件（地网：回流入口，connector-source 优先），
  其余焊盘按仿真电流取/注。IC(0) 预条件 CG。ρ 取 20 °C 值，不随温度修正。
- **热**：2.5-D 有限体积，每层每单元一个温度：面内 k_Cu·t·覆盖率 + FR-4（相邻介质各一半），
  层间 FR-4 厚度方向 + 过孔孔壁铜，上下表面对流（+ 可选辐射），板边绝热。热源 = `sim.json`
  各场景的**时间平均**功耗 `parts[].thermalW`（peak 场景：负载取平均电流的孪生工作点，I²R 类取
  √(P_avg·P_peak)，见 [power-sim.md](power-sim.md)）按焊盘面积分到器件所在面 + 焦耳热按
  (thermalCurrentA/currentA)² 缩放到 RMS 上界。每个场景都解，热图取最热场景。IR 压降、过孔电流、
  铜自热（线宽反馈）仍用峰值工作点电流。v0.6.1 之前的 sim.json 没有 `thermalBasis`：退回把各场景
  `powerW` 当持续功耗（峰值突发会把板温高估数倍），assumptions 写明，应重跑 `sim power`。
- **sim ↔ 板一致性（fail）**：带电流的 sim 焊盘在板上找不到（器件不在、无此焊盘、焊盘在别的网）、
  同一位号 sim 的 MPN 与板上器件不同、或 ≥10 mW 的发热件不在板上，都判
  `sim-board-mismatch` 失败——否则该电流会被静默丢出 IR/过孔/热求解，发热会落到别的封装下。
  MPN 不同但 sim 引脚都在板上该封装里（同封装改值，如 LED 限流电阻 1 k→330 Ω 的 what-if 还没同步到 PCB）
  只报 `sim-board-bom` warn：同步原理图到 PCB、重新 dump 后再签核。
- **结温**：Tj = 器件下板温最大值 + P·θJB（只有 θJC 时用 θJC）；θ 取 `power-models.json` 的
  `ratings.thetaJbCW / thetaJcCW / tjMaxC`（须带数据手册出处）。没有额定只报板温并标“需数据手册”，不猜。
- **过孔载流**：IPC-2221 外层曲线作用于孔壁截面 π(d+t)t（IPC-2152：内层≈外层），ΔT = `--via-dt`。

## 3. 读结果

`post.json`（schemaVersion 1，只加字段不改名）：`nets[]`（每网最坏场景、参考、电流、预算、最坏压降与
焊盘、铜损、最大电流密度 A/mm²、热点、压降最大的走线段、最大过孔电流、铜自热、各场景明细、每个
负载焊盘的电压/压降）、`vias[]`（按电流排序，含载流量与占用 %）、`thermal`（最热场景、板最高温度与
位置、各层最高/平均、每器件板温/Tj/状态、各场景能量平衡、仅铜损时的铜自热）、`feedback[]`、
`compare[]`（`--plan` 时与 pcb auto 布线期 IR 估算对比）、`elmer`、`maps[]`、`assumptions[]`、`model[]`、
`findings[]`、`verdict`。

结论：**fail** = sim 与板不是同一设计版本（`sim-board-mismatch`）、电源网超 IR 预算、负载开路、过孔超载流量、Tj 超 Tj,max、板温 > 130 °C；
**warn** = 压降 > 80 % 预算、过孔 > 80 %、Tj > 80 % Tj,max、板温 > 105 °C（FR-4 Tg 余量）、Elmer 不一致。
报告第 6A 章与“验证状态”一行都由它生成，进入封面总体结论。

热图（`--svg-dir`）：每层 `temp-<层>.svg`（°C，统一色标，器件框 + 位号 + 最热点）与
`current-<层>.svg`（A/mm²，对数色标，低于最大值 1/1000 的单元不着色）。

## 4. 铜皮修改建议（feedback）

写入 `post.json.feedback[]`，`--feedback` 合并进 `feedback.json`（与 pcb auto 同一 schema，旧的 `post-*`
项被替换）。每项有网络、层、坐标、实测指标和具体改法；全部 `live-unverified`，改板后必须重新
save → reload → dump → sim post-layout → DRC 才能称已改善。

| kind | 触发 | 建议 |
|---|---|---|
| `widen-segment` | 走线电流 > IPC 载流（high）；电流×余量需要更宽且线长 ≥ 50 mil（medium）；铜自热 > tempRiseC；J > `--j-max`；面铜颈部电流密度 > 一个单元宽的铜条在 tempRiseC 下的 IPC 允许值 | 最小线宽（IPC-2152，电流×余量），或并联铜（同网铺铜/另一层走线 + 两端过孔）；颈部：移开他网障碍、缩小净距 |
| `corner-crowding` | 两条同层走线首尾相接、夹角 ≤ 95°，拥挤后电流 I·(1+(180°−角)/180°) > 载流；或锐角 < 85° 且 ≥ 50 mA | 改两段 45° 或圆弧（半径 ≥ 3× 线宽），或两腿加宽 |
| `via-bottleneck` | 过孔电流 > 50 % 载流量（>80 % medium，>100 % high） | 用 ⌈I×余量/载流量⌉ 个过孔或更大孔径 |

## 5. Elmer FEM 交叉校验（可选，类似 `--spice-check`）

`--elmer-dir` 把**同一个热模型**写成 Elmer 输入包：`mesh/`（节点 = 每层每个板内单元中心，与有限体积的
未知量一一对应；8 节点六面体 = 相邻两层之间每 2×2 单元块）+ `case.sif`（HeatSolver，按
(k_xy, k_z, 热源密度) 量化分体、体内体积加权平均，总功率守恒；上下表面 Robin 边界，侧边绝热；
SaveScalars 在探针点——板最高点、每个发热器件下最热单元——输出温度到 `probes.dat`）+ `probes.json`。
`--elmer-check` 在找到 `ElmerSolver` 时运行并比较（容差 max(1 °C, 5 % 温升)），一致为 `agree`，
不一致为 `disagree` 并降级为 warn；找不到则 `skipped`，提示 `pcbpilot sim tools install --only elmer`，
输入包保留，之后可在该目录 `ElmerSolver case.sif`，再 `--elmer-result probes.dat` 比较。
安装与版本自检见 [environment-setup.md](environment-setup.md#仿真工具ngspice--elmer-fem)
（`pcbpilot sim tools check|install`，macOS brew tap `elmercsc/elmerfem`、Ubuntu PPA、Windows 官方安装包）。
报告里写实际 Elmer 版本；未运行时不得写“已交叉验证”。

## 6. 验证（解析解，`go test ./pkg/postsim`）

| 用例 | 解析解 | 模型 | 误差 |
|---|---|---|---|
| 10 mil / 1 oz 均匀走线 1 A、1000 mil | R = ρL/(wt)：48.65 mV；J = 112.49 A/mm² | 48.651 mV；112.49 A/mm² | < 0.01 % |
| 1000×500 mil 矩形铜片、两端满宽接触 | Rs·L/W：9.435 mV（10 A） | 9.384 mV | −0.55 % |
| 两层过孔链（上-孔-下-孔-上） | 各段走线 + 2×孔壁：78.766 mV | 78.766 mV | < 0.01 % |
| 一维肋片（端部加热、两面对流、端部绝热） | θ(x) = θ0·cosh(m(L−x))/cosh(mL) | x=10/500/1000/2000 mil | −2.9 / −1.0 / −0.4 / +0.8 % |
| 大平板点热源 | θ(r) = Q/(2πkt)·K0(mr) | r = 5/10/20/40 mm | −2.6 / −1.9 / −1.2 / +0.4 % |
| 能量守恒（器件 + 焦耳热） | Σ 对流散热 = Σ 热源；焦耳热 = I²R | 误差 < 1e-5 % | 通过 |
| IPC 量级：10 mil / 1 oz / 1 A、3×5 in 裸板 | IPC-2221 外层公式 ΔT ≈ 13.2 °C | 12.2 °C（0.5 mm 单元；0.254 mm 单元 11.2 °C） | 同量级（IPC-2152 图表同样约 10–20 °C），只作量级核对，不是图表拟合 |

## 7. 样例：ESP32-S3 mini（现场回读板）

输入：仓库 `pkg/postsim/testdata/esp32mini/`（`artifacts/v05-live/final.reload.json` 裁剪版 + sim + intent 铜参数），
输出与热图：仓库 `docs/examples/esp32-mini-post-layout/`（`post.json`、`post.md`、`heatmaps/*.svg`、`elmer/`）。
要点：IN1 没有铜对象 → 按 GND 负片平面建模（假设已记录）；各电源网均在预算内，最坏 USB_VBUS 17.07 mV
（J2→D2.2 的 10 mil 长走线，含 5 mil 颈部 96.7 A/mm²，长度 21 mil，铜自热 < 0.5 °C）；
最大过孔电流 0.50 A（+3V3，34 % 载流量）；U3/U1/D1/D2 无 θJB 额定 → 只报板温。
板温：用该板同版本原理图的新 sim（`internal/app/testdata/esp32-v05/` 的 connectivity + values）按时间平均
功耗解，最热场景 terminal-only 约 **39 °C**（0.49 W：ESP32 3.3 V × 0.1 A 平均 0.33 W、D1 OR 二极管
0.06 W、SY8089 buck 损耗 0.04 W…；本板是 buck 而非 LDO，若换 AMS1117 则 (5−0.35−3.3)×I_avg≈0.14 W
会成为第二热源）。量级核对：板内 8218 个 0.5 mm 单元 ≈ 20.5 cm²，平均温升 ΔT ≈ P/(2·h·A) = 0.49/(2·10·0.00205) ≈ 12 °C → 约 37 °C 平均、热点 39 °C。测试
`TestESP32MiniThermalAverage`。旧 sim.json（无 thermalBasis）仍给 86.3 °C（1.66 W TX 峰值按持续计，已废弃口径，
`TestESP32MiniPostLayout` 保留作兼容回归）。

### E2E FAIL 根因（2026-09-28 离线全链 E2E：ESP32 板温 192 °C）

1. **输入不是同一设计版本**：E2E 用 `pkg/powersim/testdata/esp32mini`（2026-09-25 早期原理图，ESP32=U1、
   buck=U4、网名 +5V/VBUS/5V_TERM）的 sim 配 `internal/app/testdata/esp32-v05` 的板（buck=U1、ESP32=U3、
   网名 VSYS_5V/USB_VBUS/+5V_TERM）。1.66 W 的 ESP32 功耗被按位号放到 SOT-23-5 的 buck 焊盘下 → 192 °C；
   17 个带电流焊盘对不上被静默丢出 IR 求解（+5V 等网“无铜”却通过）。修法：同版本 fixture
   （`esp32-v05/sch-*.json` + `values.json`），并新增 `sim-board-mismatch` fail（`TestSimBoardMismatch`）。
2. **峰值当持续**：peak/单源场景把 ESP32 TX 0.5 A 突发当持续热源（即使输入一致，现场 v3 板也报 86 °C）。
   修法：上面的时间平均热源；载流仍用峰值。

HV 反激（同一次 E2E，板温 8374 °C）：J2 输出端子的 `load` 模型（外部 12 V/2 A 负载，24 W）被当成 J2 焊盘上的
热源 → 现为 `offBoard`（`thermalW` 0，见 [power-sim.md](power-sim.md)）。修后仍约 380 °C：D3（SS310，2 A × ~0.8 V
= 1.6 W）只有焊盘和细线散热，是**真实的设计问题**（需要整流管铜皮散热区/更低 Vf 器件/多层），不是模型错误，
不得放宽 FR-4 判据。

与 pcb auto 布线期 IR 估算的差异（设计后都更小）：焊盘/过孔环内走线被短接（pcb auto 从焊盘中心量）；
真实灌铜替代 ≥ 25 mil 粗网格；过孔长度按真实叠层 z；逐场景而非合并包络。

## 8. 能力边界

- 直流稳态：不含开关纹波的交流电流分布、趋肤效应、瞬态热；开关节点按平均电流。
- 电阻率不随温度修正（60 °C 时铜电阻约 +16 %）；热焊盘辐条在负片平面上忽略。
- 单元 0.5 mm：窄于单元的走线在热模型里按覆盖率摊开，局部峰值温度偏低；需要时 `--cell 0.25`。
- 器件本体不单独对流（器件顶面与板面共用 h），元件引线/封装内部热阻只通过 θJB/θJC 进入。
- THT 焊盘 dump 不带孔径时按短边一半假设（写入 assumptions）。
- 负片平面是推断，dump 若给出负片层网络应改为读实际数据。
- Elmer 路径未在本机用真实 ElmerSolver 跑过：输入包格式、探针输出列顺序按 Elmer 文档编写并单测，
  首次真实运行后再标 `live-verified`。
