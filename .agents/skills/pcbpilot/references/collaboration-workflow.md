# 协作工作流：原理图深入，PCB 由用户主导

> 来源：用户个人 Skill `easyeda-design-workflow`（2026-09-13）并入 pcbpilot 包，命令与版本门禁
> 已改为 pcbpilot。它规定**默认的协作边界**；本包其他参考负责命令、schema、几何与 API 细节。
> 下列精确版本、保留位号和 WARN 判定要求来自用户，优先于其他参考中更宽松的约定。

没有指定软件时默认 EasyEDA Pro；用户明确指定 KiCad 等其他软件时遵循用户。仅解释原理、讨论
概念、不涉及绘图或编辑时，不启动安装与绘图流程。

## 协作模式

| 模式 | 触发 | 允许做什么 |
|---|---|---|
| 仅调研 | 用户要方案、选型、计算 | 读数据手册/参考设计，输出可追溯方案、计算、引脚表、待确认项；不启动 EDA、不升级 |
| 原理图设计 | 用户要求绘图 | 在授权范围内调研、选库、建 canonical 数据、绘制、验收。先读下文“深度调研” |
| PCB 检查（**默认**） | 用户说“检查一下” | 读现场、跑检查、给具体建议；**不**移动器件、布线、铺铜、改规则或重导入 |
| 定点修改 | 用户明确说“把这些器件移开”“修这条线” | 只改指定范围，不延伸为全板优化 |
| 整板自动设计 | 用户**明确**要求完整自动 PCB 设计 | 按 [pcb-auto.md](pcb-auto.md) 与 [recipes/](recipes/index.md) 运行 `pcbpilot pcb auto`，先交报告与预览，经用户确认再 `apply` |

- **人工编辑后重新取基线**：不与用户同时写同一文档；人改过后丢弃旧队列，重新读取。
- **按轮次工作**：用户提交一轮，检查一轮；没有新修改、新证据、新请求，不自动巡视。
- **控制上下文**：维护简短项目状态、canonical 文件与一份问题清单；只加载当前模式需要的参考。

## 0. 效率、范围与停止条件

- **先划定交付范围**：原理图、PCB、制造输出是核心；供电、引脚、关键信号做必要核算。固件、
  整机认证、实物性能验证不自动加入；未做的验证不写成已通过。
- **规划成批，写入成批**：从一次新鲜快照在本地算好功能区、板框、孔、姿态、关键网与回流，
  生成受保护的变更队列，按功能区提交；不逐个坐标试摆后反复跑整板检查。
- **检查跟随变更影响**：只改丝印不重规划电气；只改采购属性不重布铜。最后一次设计变更后做
  全范围验收与制造文件核对。
- **CLI/API 优先，视觉分阶段**：尺寸、间距、网络、焊盘用结构化数据判断。PCB 视觉检查安排在
  布局收敛与最终布线/丝印两个阶段；只有接口缺失、接口与现场矛盾或必须操作原生对话框时才用
  Computer Use，且不得用它修改工程（见 SKILL.md 红线）。
- **排障上限**：同一阻塞最多两种有依据的独立修法，或连续 15 分钟无新证据就停止该路径，记录
  已试方案与真实状态，转做不依赖它的工作。
- **停止要收敛**：用户说“停止”后不再改图、检查或导出；自动续跑提示不算重新授权。

## 1. 会话第一条命令与精确版本门禁

EasyEDA 任务的第一条命令：

```bash
pcbpilot update --check --exit-code
```

只有 pcbpilot CLI、实际加载的 pcbpilot Skill、运行中的 daemon、**所有已连接窗口**的
PCB Pilot Connector 的完整版本号都**精确等于** `zhuangzard/pcbpilot` 的 latest Release
（仅忽略前缀 `v`）才开始设计。退出码 0 不是充分证据：逐窗口核对 `pcbpilot health` 里的
`connectorVersion`。

**开发机例外**：Skill 以软链接指向源码仓库、CLI 为 `git describe` 开发构建时，以
“CLI、daemon、connector 来自同一提交构建”为准（`pcbpilot health` 中 CLI 与 daemon 版本相同，
connector 为同提交 `make connector` 产物），并在报告中写明是开发构建。

任一不满足：暂停设计，只做升级与连接恢复——`pcbpilot update`，重启 daemon；connector
版本不同时从同一 Release 安装 `pcbpilot-connector.eext`（在扩展管理器“已安装”里先卸载旧的
PCB Pilot Connector，再导入，开启“允许外部交互”，完全重启 EasyEDA）。升级后**新开 Agent 会话**
从第一条命令重来。原版 easyeda-agent 的 EDA Agent Connector 与本流程无关，不要卸载。

## 2. 权限与目标

确认 PCB Pilot Connector 已开启“允许外部交互”。运行 `pcbpilot health`，核对目标工程、页面与
文档类型；用 `pcbpilot doc ls` / `doc switch` 定位。写操作固定 `--project` 与 `--doc`，优先 UUID。

## 3. 原理图：先连接数据，再几何，再 Apply

先读 [schematic-data.md](schematic-data.md)。

1. 建立或导出本地 canonical connectivity JSON：器件稳定 ID、库身份、原位号、**完整物理引脚**、
   稳定 net ID、逐物理脚 pin→net 或明确 NC，保存来源与基线。
2. 逐脚明确状态：GPIO 名/编号不是封装物理脚号；重复电源脚、隐藏脚、多单元、裸焊盘不得省略；
   未知脚不猜接，不把缺失连接自动填 NC。
3. 保留原位号；stable ID、net ID、uniqueId 不随排版重建。用户明确要求才改位号。
4. 本地计算 XY、朝向、导线、标记与功能区（`sch lib-layout` / `sch compose` 等），不在画布上试摆。
5. 写入前取新鲜基线，生成 diff 与受保护队列，先 dry-run 再在授权范围内 `sch apply`。
6. Apply 后逐脚回读对账；超时、部分成功、`verified:false` 先回读，不盲重试、不改期望值掩盖差异。

## 4. 检查、保存与图像验收

原理图 Apply 后运行 `sch layout-lint`、`sch check`、`sch bridge-check`、`sch drc`（或
`sch gate --strict` 并逐项核对清单），显式 `sch save` 确认 `saved:true`，需要时 `doc reload`
回读；`sch export-image` 导图并实际查看。**仍有任何 WARN、未知或未验证项时，不描述为通过**；
已知误报也记录为未通过并附证据。INFO 单列。

## 5. PCB 分支

先做版本、权限、目标步骤；按任务读 [pcb.md](pcb.md)、[pcb-layout.md](pcb-layout.md)、
[pcb-routing.md](pcb-routing.md)。原理图转 PCB 以已核对的 pin→net/NC 为基线，验证 Board 绑定、
导入器件数、原位号、料号、封装、物理焊盘号和逐焊盘网络（见
[recipes/schematic-to-pcb.md](recipes/schematic-to-pcb.md)）。PCB 变更后 `doc reload`、检查、
`pcb save`、导图查看。未布线、未完成制造检查或仍有 WARN 的板不能描述为可制造。

## 6. 深度调研与交接

1. 按功能块梳理电源、信号链、时钟、控制、连接器、保护；优先制造商数据手册、应用笔记、官方
   模块文档与源项目原理图，记录 URL、版本/日期、页码。分开标明来源事实、推导与未验证假设。
2. 核对完整料号、供电范围、绝对最大额定值、逻辑阈值、启动/关断状态、功耗、关键时序；按实际
   场景计算并记录输入与单位。典型值不能当保证极限。
3. 建立完整物理 pin→net/NC 表，比较数据手册、EasyEDA 实时库符号与封装映射。模块与裸芯片分别
   核对（用 Pico 模块时按模块物理引脚建模，不按 RP2040 芯片引脚）。
4. 评估电源预算、去耦、回流、接口电平、模拟/数字耦合与确实涉及的高压隔离；给出计算依据，不用
   无条件的统一间距。安装柱检查含柱体/垫圈外径、装配公差与禁铜要求。
5. 保存精简交接资料：设计依据、BOM、pin→net 权威数据、布局约束、未决问题；在原文件更新，
   不产生多套“最终版”。
6. 原理图完成后交给用户布局：指出功能分区、去耦紧贴的具体引脚、关键环路、敏感网络、接口方向、
   机械限制——这些正是 `pcb auto` 的 `mech.json` / `power.json` 输入，可直接按
   [recipes/](recipes/index.md) 写成文件交给用户或引擎。

## 7. 交付

报告目标工程/页面、实际改动、本地权威 JSON 与 Apply 记录、逐脚对账、各项检查、保存状态、
导出图片与未解决问题。需要新会话时写明原因与继续入口。
