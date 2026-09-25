# PCB Pilot Connector

**让 AI Agent 替你画板子。** 这是 pcbpilot 系统在 EasyEDA(嘉立创EDA专业版)内的社区连接器:配合本地 `pcbpilot` CLI/daemon 与 Agent Skill,通过官方 API 在真实编辑器里完成选型、放置、连线、方框标注与校验。

```text
Skill / CLI -> Go daemon -> PCB Pilot Connector -> 官方 eda.* API
```

一行看懂:Skill 描述专家工作流,Go CLI/daemon 提供有类型、可观测的动作与校验,本连接器把这些 typed actions 桥接到官方 `eda.*` API——它是整个系统中**唯一直接调用 `eda.*` 的组件**,每一步操作最终都落在嘉立创自己开放的插件能力上。

- GitHub 仓库:https://github.com/zhuangzard/pcbpilot
- 最新 Release:https://github.com/zhuangzard/pcbpilot/releases/latest

## 效果演示

### 门禁控制板示例：本地数据 → SCH Apply → 真实原理图

器件、物理引脚、稳定网络 ID 和 NC 保存在本地连接图中。Agent 依据实测引脚几何计算
功能 Lib 内的器件位置、方向和连线，再组合页面，通过 `sch apply` 顺序写入 EasyEDA
并回读核对，保留原位号与器件身份。

**23 个器件 · 165 个物理引脚 · 28 个网络 · 2 页原理图**

![门禁控制板示例的电源与 RF 主控页：外围电路按引脚方向连接，粉色虚线框标识功能模块](images/access-control-power-rf.png)

电源、RF 主控和下载接口分别组成 Lib；外围电路按引脚方向连接，端口长短错落。

![门禁控制板示例的对讲与外设接口页：功能模块按 Z 字阅读顺序排列，各框随内容独立收紧](images/access-control-talk-interfaces.png)

对讲与外设按 Z 字阅读顺序排列，每框随内容独立收紧并保留内边距，标题利用上方或下方空档。

![门禁控制板示例的实际 Apply 阶段捕捉，加速播放](images/access-control-sch-apply.gif)

动图使用电源与 RF 主控页实际 Apply 捕捉的 12 张关键阶段导图，加速播放；静图源自 EasyEDA 官方导图，展示名称已匿名化。
录制方法见 [Apply 动图捕捉](https://github.com/zhuangzard/pcbpilot/blob/main/docs/schematic-showcase.md)。
两页布局与连接检查均为 0 错误、0 警告；官方 DRC 仍有 3 WARN，严格门禁未通过，部分文字避让仍待完善。
验证范围见 [1.4 发布与验证](https://github.com/zhuangzard/pcbpilot/blob/main/docs/releases/release-1.4.md)。

### 历史 PCB 案例：ESP32-S3 四层板

以下为独立的 ESP32 回归板案例：自动布局、板框贴合、铺铜、丝印，在真实画布上执行并回读校验。

![AI 在 EasyEDA 中完成 PCB 布局、板框和铺铜](images/demo-pcb-layout.gif)

由 agent 驱动 PCB 流程产出的 ESP32-S3 板：自动布局 → 板框贴合 → 规则感知布线 → 4 层电源平面 → 丝印碰撞避让。历史验证记录见 [完整案例](https://github.com/zhuangzard/pcbpilot/blob/main/docs/showcase-esp32-mini.md)。

![ESP32-S3 成品板:4 层电源平面 + 圆角板框 + 位号对齐](images/demo-esp32-board.png)

## 1.4 数据驱动原理图

- **数据 → Lib compose → SCH Apply**:在 canonical 数据中维护器件、引脚、网络与 NC,先设计 Lib 局部几何,再离线组合单页,通过顺序队列调用官方 API 并回读验证。
- **身份与显示分开**:稳定器件 ID 用于数据绑定,合法数字位号保持原样,功能名称存 Role。
- **紧凑布局与方框**:从左上向右按 Z 字排列，每框按内容独立收紧并保留最小内边距，同行顶齐，下一行按本行最大高度推进；粉色虚线框配 0.2 inch 标题，优先利用电路上方或下方空档。

版本与资产以 GitHub Release 为准。组合器使用已设计的模块几何,
不自动推导任意外围电路、分页或删除源页。构建步骤、已完成验证和未完成项见
[1.4 发布与验证](https://github.com/zhuangzard/pcbpilot/blob/main/docs/releases/release-1.4.md)。

## 已支持能力概览

**原理图**

- 器件与库:从立创/LCSC 库按 uuid 放真实器件、换型号、符号/封装重绑、C 号确定性解析;库优先,手绘符号只是兜底。
- 连线:`connect`/`autoconnect`(打分器自选方向,碰撞/穿件/图签全几何成本)、netflag/netport 自动补偿平台旋转存储的坑、成对删除。
- 布局与转换:`sch compose` 从已设计的 Lib 数据计算单页布局;`sch apply` 顺序执行规划队列;`sch frame apply/check` 生成并核对方框与标题。
- 校验与导出:Apply 后逐脚/网络/NC/几何回读,四阶段门禁(layout-lint → check → bridge-check → drc)、跨页网名审计、`sch read`、BOM 导出(自动补 LCSC C 号)、网表导出、页面导图 SVG/PNG/PDF。

**PCB**

- 布局:新建板并绑定原理图、模块感知自动布局(间距规则感知)、板框贴合/圆角、布局质量与可布性评分、丝印位置感知避让重排、自由丝印字串(板注/LED 极性标记)。
- 布线与铜:`pcb auto run` 内置电气感知整板引擎(布局、协商拥塞多层布线、平面/铺铜、独立 DRC;2026-09-25 在 ESP32-S3 mini 板 V3 3.2.149 桌面版现场 30/30 布通、原生 DRC 通过)、`route-short` 启发式短线布线(规则感知线宽、障碍感知)、铺铜/禁铺区/过孔缝合、4 层电源分配(GND 内电层 + 电源平面 + 焊盘过孔缝合)、挖槽。
- 叠层与制造:铜层数与内层类型设置、读取板子实时 DRC 规则并全链路遵循(缺失时回退 JLCPCB 工艺参考)、DRC/`pcb check`、Specctra DSN 导出/回导(可选对接外部 Freerouting,非必需)。

**基础设施**

- Typed action 协议:`--help` 自描述、动作目录可枚举,结构化输入输出,AI 每一步可观测、可验收、可回放。
- 连接器自愈重连看门狗(daemon 重启/窗口后台都能自动回来)、daemon 防抖自动保存、审计日志、窗口内非阻塞 toast 播报进度。
- `debug.exec_js` 用于任务范围内的临时调试。

完整能力清单与路线图见 [FEATURES](https://github.com/zhuangzard/pcbpilot/blob/main/docs/FEATURES.md)。

## 连接器本身做什么

这是一个真实可打包、可导入的 EasyEDA Pro 扩展,刻意保持很薄:

- 本地 WebSocket 传输:默认连接固定端口 `61832`、握手、注册、上下文同步、心跳、自愈重连;
- typed action 分发:把 daemon 下发的结构化动作映射到官方 `eda.*` 调用;
- 结果序列化:执行结果、警告、错误、上下文回传 daemon;
- 产物传输:截图、BOM、网表等二进制结果编码回传。

真正的工作流、校验、确认、产物处理和多步编排都在 Go CLI/daemon 与 Skill 层完成——所以**必须配套本地 `pcbpilot` CLI/daemon 一起用**,单装本插件没有任何效果。

## 安装与开始

**1. 装本连接器**:从 GitHub Release(https://github.com/zhuangzard/pcbpilot/releases/latest)侧载 `.eext`(或从源码构建 `extension/build/dist/pcbpilot-connector_vX.Y.Z.eext`)——与 CLI 严格同版。本连接器不在立创插件市场上架,无原地自动更新。

**2. 装 CLI/daemon + Skill**(一行脚本,自动检测 Claude Code / Codex 并装好 Skill):

```bash
curl -fsSL https://raw.githubusercontent.com/zhuangzard/pcbpilot/main/install.sh | bash
```

**3. 在 EasyEDA 中确认三件事**:

1. 已导入本连接器 `.eext`(扩展管理器中显示为 PCB Pilot Connector,顶部菜单为「PCB Pilot」);
2. 已开启外部交互:高级 → 扩展管理器 → 已安装 → 选中 PCB Pilot Connector → 状态须为 Enabled → Config 页签 → 勾选「允许外部交互 / Allow interactive with external」——否则连接器的 WebSocket 连不上本地 daemon;
3. 已启动本地 daemon(`pcbpilot daemon start`),`pcbpilot health` 能看到已连接窗口。

**之后升级不必再跑脚本**:`pcbpilot update` 一键升 CLI + Skill;`pcbpilot update --check` 只读三方(cli / skill / connector)版本对齐表。

### 版本配套约定

CLI/daemon、连接器与 Skill 遵循**同一版本号**。三者需配套安装,并运行开启外部交互的 EasyEDA Pro;EasyEDA 应用使用自身版本号。落后的连接器会被 `pcbpilot daemon health` 标成 stale。EasyEDA Pro V3(3.2.x)与 V4(推荐 ≥4.1.60)、桌面版与 Web 版均可使用。

连接器只通过 GitHub Release 侧载分发,与 CLI **严格同版**;无原地自动更新,升级需先卸载旧的「PCB Pilot Connector」(按 uuid 去重)再导入。导入后需重新加载编辑器(Web 版刷新页面;桌面版完全退出并重启 EasyEDA),已经打开的窗口可能仍运行旧连接器代码。桌面版 3.2.149 已知问题(#221):侧载连接器在每次重启 EasyEDA 后可能需要重新导入。

完整上手、版本对齐与升级注意事项见 [快速开始](https://github.com/zhuangzard/pcbpilot/blob/main/docs/quick-start.md)。

## 来源与并装说明

PCB Pilot Connector 是 pcbpilot 的连接器,由 [easyeda-agent](https://github.com/zhoushoujianwork/easyeda-agent)
的 “EDA Agent Connector” 分叉而来(MIT,感谢原作者)。两者 uuid 不同、端口段不同
(pcbpilot 61832–61841,原版 60832–60841),可在同一个 EasyEDA 中同时安装、同时启用,
各自只连接自己的 daemon。本插件仅通过 GitHub Release 分发,不在立创插件市场上架。

## 链接

- GitHub 仓库(架构、路线图、能力矩阵、实战案例):https://github.com/zhuangzard/pcbpilot
- Releases(严格同版 `.eext` + CLI 各平台二进制):https://github.com/zhuangzard/pcbpilot/releases
- [完整实战案例:一份需求文档 → ESP32-S3 四层板](https://github.com/zhuangzard/pcbpilot/blob/main/docs/showcase-esp32-mini.md)

MIT 许可,欢迎 star 与共建电路块库(一次贡献,署名可追,永久收益)。
