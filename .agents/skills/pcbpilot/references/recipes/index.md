# 配方：把设计意图翻译成底层代码能执行的精确指令

配方回答一个问题：**用户说了一句话，Agent 应该交给 pcbpilot 的哪个命令、写出什么样的数据文件、
每个字段取什么值、执行后看什么证据。** 每个配方都给：适用场景、输入、字段表（单位与默认值）、
完整命令、执行后必须核对的证据、常见错误。

| 用户的话（例） | 配方 |
|---|---|
| “原理图画完了，转 PCB” | [schematic-to-pcb.md](schematic-to-pcb.md) |
| “5V 进、3A 给电机，3V3 给 MCU” / “线宽怎么定” | [power-spec.md](power-spec.md) |
| “板子 50×40，四角 M3，USB 在左边，天线下面不能有铜” | [mech-spec.md](mech-spec.md) |
| “220V 进，光耦隔离到单片机” / “高压区低压区分开” | [hv-isolation.md](hv-isolation.md) |
| “USB/MIPI/以太网要做阻抗和等长” | [high-speed.md](high-speed.md) |
| “帮我整板自动布局布线” | [pcb-auto-run.md](pcb-auto-run.md) |

## 精确指令契约（所有配方共同遵守）

写任何数据文件或命令参数前，先把下面每一项定死；有一项不确定就回到数据来源查，而不是猜。

| 项 | 规定 |
|---|---|
| 单位 | PCB 命令与快照：**mil**。`mech.json` 默认 **mm**（`"units":"mm"`），可改 `"mil"`。原理图：raw（5 raw 网格）。在文件里写明单位字段，不靠默认 |
| 坐标系 | y 向上。`mech.json` 原点 = 板框左下角；快照与 `pcb.component.modify` = 板绝对坐标 |
| 位置语义 | `pcb.component.modify` 的 x/y 是**封装锚点**，不是本体中心；旋转绕锚点。按中心规划时用引擎输出的 placement（已换算为锚点）或 `pcb modify --center` |
| 旋转 | 度，逆时针，0/90/180/270；引擎只在这四档中选 |
| 层号 | TOP=1、BOTTOM=2、多层（通孔）=12、内层从 15 起（15=Inner1…）；丝印 3/4 不是铜层。以 `pcbpilot pcb layers` 回读为准 |
| 网络名 | 大小写敏感，取自原理图/`pcbpilot pcb nets`。引擎按网名推断角色：`GND`/`AGND`/`AU_GND`→地；`3V3`/`+5V`/`VBUS`→电源；`USB_DP/DM`、`MIPI_*_P/N`→差分；`LED+`/`BAT-` 是**极性**不是差分 |
| 位号 | 保留原位号；配方里引用器件一律用位号（`J1`、`U3`），并先用快照确认存在 |
| 目标 | 写操作总带 `--project` 与 `--doc`（优先 UUID）；离线命令用 `--board <dump.json>` |
| 溯源 | 每个数据文件旁记录：来源（数据手册页码/机械图版本/用户原话）、日期、基线快照文件名 |
| 验证状态 | `source-only`（只写了数据）→ `offline-verified`（引擎/离线检查通过）→ `live-verified`（apply 后保存、重载、回读通过）。不越级声称 |

## 通用执行骨架

```bash
pcbpilot health                                                 # 目标窗口已连接
pcbpilot pcb dump --include-copper --out board.json --project <P> --doc <PCB>   # 新鲜基线
pcbpilot pcb auto analyze --board board.json [--mech mech.json] [--power power.json]
pcbpilot pcb auto run --board board.json [...] --out-dir out/   # 只生成计划，不写编辑器
pcbpilot apply out/playbook.json --project <P> --doc <PCB> --dry-run
pcbpilot apply out/playbook.json --project <P> --doc <PCB>
pcbpilot pcb save --project <P> --doc <PCB>
pcbpilot doc reload --project <P> --doc <PCB>
pcbpilot pcb drc --project <P> --doc <PCB>
pcbpilot pcb check --project <P> --doc <PCB>
```

任何一步的输出里有 `verified:false`、`partial`、`staleRisk`、WARN 或未知项，先回读查清，
不继续往下，也不把它写成“通过”。
