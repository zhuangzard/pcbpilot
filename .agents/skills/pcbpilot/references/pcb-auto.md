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

## 输出（`--out-dir DIR`）

| 文件 | 内容 |
|---|---|
| `report.md` | 中文报告：层数及理由、每个电源/地/高速网的电压电流线宽间距过孔、功能块与块间信号、电压域与隔离要求、布局与布线指标、DRC、高速检查 |
| `plan.json` | 全部决策与几何（分析、叠层、电路模型、布局、走线、过孔、铺铜区、SI） |
| `playbook.json` | `pcbpilot apply` 剧本：叠层 → 板框/孔/禁布区 → 器件位姿 → 走线/过孔 → 铺铜 → 翻转内电层 → 重铺 → 保存 → DRC |
| `preview.svg` | 目视复核图：器件按电压域着色，各层走线、过孔、平面分区、隔离带、未布通飞线（黄色虚线） |

## 引擎怎么决策（读报告时对照）

- **线宽**：IPC-2221 载流公式（外层），内层按 IPC-2152 结论同截面折算铜厚；电源/地最低 10 mil，
  开关节点最低 20 mil；地作为平面，走线只按最大单轨回流定宽。电流大时换层过孔数按单孔载流算。
- **间距**：IPC-2221B 电压表与工艺最小值取大；高压网络的占位自动放大。
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

