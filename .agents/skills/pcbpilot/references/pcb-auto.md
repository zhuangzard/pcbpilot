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
  多边形周长/面积计价，反馈件远离电感和开关节点。`likely` 的拓扑要对照数据手册核对。**先读 `report.md`「布局依据」表核对归属**：归属错（例如测试点被当成串阻、
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
