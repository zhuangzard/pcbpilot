# pcbpilot 使用手册

本手册覆盖从一台新机器安装到交付一块 PCB 的完整流程。命令的精确参数以
`pcbpilot <domain> <command> --help` 为准；Agent 的工作规则以
[`.agents/skills/pcbpilot/SKILL.md`](../.agents/skills/pcbpilot/SKILL.md) 为准。

目录

1. [系统组成](#1-系统组成)
2. [系统要求](#2-系统要求)
3. [安装](#3-安装)
4. [连接 EasyEDA 并验证](#4-连接-easyeda-并验证)
5. [第一次使用：把需求交给 Agent](#5-第一次使用把需求交给-agent)
6. [完整设计流程：原理图 S0–S6](#6-完整设计流程原理图-s0s6)
7. [完整设计流程：PCB P0–P10](#7-完整设计流程pcb-p0p10)
8. [常用命令速查](#8-常用命令速查)
9. [MCP 接入](#9-mcp-接入)
10. [升级与版本](#10-升级与版本)
11. [故障排查](#11-故障排查)
12. [能力边界](#12-能力边界)
13. [开发者](#13-开发者)

---

## 1. 系统组成

```
AI 客户端（Claude Code / Codex）
   │  Skill：工作流、规则、参考数据；或 MCP 工具
   ▼
pcbpilot CLI + 本地 daemon（端口 61832–61841）
   │  typed action、写前守卫、审计日志、回读对账
   ▼
PCB Pilot Connector（.eext，运行在 EasyEDA Pro 内）
   │  官方 eda.* API
   ▼
EasyEDA Pro 工程（嘉立创 EDA 专业版，桌面或 Web，V3 / V4）
```

| 部件 | 作用 | 安装位置 |
|---|---|---|
| `pcbpilot` CLI / daemon | 所有设计动作的唯一入口；参数化数据 → typed action → 回读 | `~/.local/bin`（源码安装）或 `/usr/local/bin` |
| PCB Pilot Connector | EasyEDA 内的薄桥接层，把 typed action 转成官方 API 调用 | EasyEDA 扩展管理器（侧载 `.eext`，**不在插件市场**） |
| Skill `pcbpilot` | 给 Agent 的工作流、红线、样例和参考数据 | `~/.claude/skills`、`~/.codex/skills`、`~/.agents/skills` |
| MCP 服务（可选） | 同一套 typed action 的 MCP 工具入口 | `mcp/src/server.mjs`，注册到 Claude Code / Codex |

pcbpilot 与上游 easyeda-agent 使用不同的命令名、端口段（上游 60832–60841）、连接器 uuid
和更新渠道，可同时安装、互不干扰。

## 2. 系统要求

| 项 | 要求 |
|---|---|
| 操作系统 | macOS、Linux；Windows 用 `install.ps1`（源码构建脚本仅 bash） |
| EasyEDA Pro | **V3（3.2.x）或 V4（推荐 ≥ 4.1.60）**，桌面版或 Web 版均可；国际版 pro.easyeda.com 与国内版 lceda.cn 均可 |
| 源码构建 | Go ≥ 1.26、Node.js ≥ 20.17、npm、make |
| 仅用发布版 | 无需 Go/Node（MCP 需要 Node） |

宿主版本差异由 `pcbpilot health` 的 `hostCompatibility` 报告（`line: v3|v4`、特性表）。
V3 与 V4 的已知差异（网络标签写法、未设置样式读回 `undefined`、修改器件会重置 LCSC 编号等）
由连接器按版本自动处理，见 [V4/V3 开发台账](v4-development.md)。

## 3. 安装

### 3.1 新机器：交给 Claude 一句话

在新机器上打开 Claude Code（或 Codex），直接说：

```text
克隆 https://github.com/zhuangzard/pcbpilot 并按仓库 AGENTS.md 的「新机器安装」完成 pcbpilot 安装，
最后告诉我需要我手动做的连接器导入步骤。
```

Agent 会执行 `scripts/setup-agent.sh`，自动完成：

1. 处理上游 easyeda-agent 残留（见下表）；
2. 源码编译 CLI → `~/.local/bin/pcbpilot`（无 sudo；无 Go 时改用发布版安装脚本）；
3. 把 Skill 软链到 Claude Code / Codex / ZCode / `~/.agents`（`git pull` 即更新）；
4. 安装 MCP 依赖并注册到检测到的每个客户端；
5. 以仓库版本构建连接器 `.eext` 并打印路径；
6. 以登录服务启动 daemon，并自动验证全部客户端。

脚本支持的 AI 客户端：**Claude Code**（`claude mcp` / `~/.claude.json`）、**Codex / Codex Desktop**
（`~/.codex/config.toml`）、**ZCode**（`~/.zcode/cli/config.json` 与 `~/.zcode/skills`），以及共享的
`~/.agents/skills` / `~/.agents/mcp.json`。每个检测到的客户端都会写入 pcbpilot，最后自动验证：
每个客户端 MCP 指向存在的文件、无上游残留、Skill 链接有效、真实启动 MCP 并完成 initialize + tools/list 握手。

**已装过上游 easyeda-agent 的机器**（第 0 步，默认执行）：

| 上游残留 | 处理 | 恢复 |
|---|---|---|
| MCP `easyeda-agent` / `easyeda`（Claude Code、Codex、ZCode、`~/.agents`） | 移除，并由 pcbpilot MCP 取代 | 备份目录中的 `*.orig` 配置与 `RESTORE.txt` |
| Skill `easyeda-agent`、`easyeda-design-workflow` 及名称以 `easyeda` 开头的 Skill | **移动**到 `~/.pcbpilot/upstream-backup/<时间>/`，不删除 | 按 `RESTORE.txt` 移回 |
| 上游 CLI `easyeda`、daemon（60832–60841）、EasyEDA 内的 “EDA Agent Connector” | 保留（端口与名称不同，不冲突） | — |

`--purge-upstream` 另外停止上游 daemon 并把其 CLI 与数据目录移入备份；`--keep-upstream` 跳过第 0 步。

daemon 以**登录服务**运行（macOS `~/Library/LaunchAgents/com.pcbpilot.daemon.plist`，Linux
`systemd --user`），日志 `~/.pcbpilot/daemon.log`；已有健康 daemon（如开发时的 `make dev`）时不改动。
`--no-service` 不装服务（`pcbpilot daemon start` 会前台阻塞）。

**唯一需要人做的一步**是在 EasyEDA 里导入连接器（扩展管理器没有 API，本项目也禁止用
GUI 自动化操作 EDA），见 [4. 连接 EasyEDA](#4-连接-easyeda-并验证)。

### 3.2 手动：从源码

```bash
git clone https://github.com/zhuangzard/pcbpilot.git
cd pcbpilot
scripts/setup-agent.sh            # 或 --dry-run 先看会做什么
```

### 3.3 手动：只装发布版（无需克隆）

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/zhuangzard/pcbpilot/main/install.sh | bash
# Windows PowerShell
irm https://raw.githubusercontent.com/zhuangzard/pcbpilot/main/install.ps1 | iex
```

发布版脚本校验 `checksums.txt` 后安装 CLI 与 Skill，并打印连接器下载地址。可用环境变量：

| 变量 | 作用 |
|---|---|
| `PCBPILOT_VERSION=vX.Y.Z` | 锁定版本（也可绕开 GitHub API 匿名限额） |
| `PCBPILOT_INSTALL_DIR` | 安装目录 |
| `PCBPILOT_INSTALL_SKILLS=codex,claude,agents` / `none` | Skill 安装目标 |
| `PCBPILOT_SKILL_PRESERVE=1` | 保留本地修改过的 Skill |
| `PCBPILOT_GITHUB_PROXY` | 下载代理前缀 |

注意：发布版只包含已发布的版本；`dev` 分支上的新能力需要源码安装。

## 4. 连接 EasyEDA 并验证

1. **卸载旧连接器**：EasyEDA Pro → 扩展管理器 → 已安装 → 卸载旧的 “PCB Pilot Connector”。
   EasyEDA 按 uuid 去重，同 uuid 的新包不卸载旧包会**静默导入失败**。
2. **导入** `extension/build/dist/pcbpilot-connector_vX.Y.Z.eext`（源码）或 Release 中的
   `pcbpilot-connector.eext`。
3. **高级 → 扩展管理器 → 已安装 → 选中 PCB Pilot Connector**（状态须为 `Enabled`）→ **Config** 页签 → 勾选 **允许外部交互**。不开的话连接器永远连不到 daemon（daemon 侧看不到任何连接尝试）。
4. **重新加载编辑器**：Web 版刷新页面，桌面版重开工程/窗口。导入后已打开的页面仍可能在跑旧连接器。
5. 验证：

```bash
pcbpilot daemon start                   # 若未在运行
pcbpilot health                         # windows[] 出现目标工程和文档，connectorVersion 与仓库一致
pcbpilot update --check --exit-code     # 可选：CLI / Skill / 连接器版本对账
```

“页面已打开”不等于连接器已连接；以 `health.windows` 精确列出目标工程和文档为准。

已知宿主问题：

- **桌面版 3.2.149（国际版）重启后连接器不自启**：侧载连接器只在导入当次运行有效，重启 EasyEDA 后需重新
  导入同一个 `.eext`（覆盖导入保留外部交互设置，但状态可能变回 `Disabled`，需点回 `Enabled`）。
- **Online 与 Half Offline 模式的扩展存储不共用**：切换运行模式后要在新模式下重新导入并授权。
- 可与上游 easyeda-agent 的 “EDA Agent Connector” 同时安装（端口段不同），不要误删对方。

## 5. 第一次使用：把需求交给 Agent

安装后无需先学 CLI。把需求和文件交给 Agent，并说明使用 pcbpilot：

```text
请使用 pcbpilot，在项目 ceshi 中完成一块 ESP32-S3-WROOM-1 最小系统板：
5V 端子输入，降压到 3V3，CH340 USB 下载，BOOT/RESET 按键，一颗 GPIO 控制的 LED，
四角 M3 固定孔。4 层板，GND 内电层，天线区域禁铜。自行选型，从原理图一直做到 DRC 和保存。
```

Agent 会按设计流程执行，并在需要你决策时提问（例如降压方案、叠层、接口在哪条边）。
**PCB 布局完成后一定会停下来给你看预览并等待确认**，确认后才整板布线。

其他常用委托：见 [README 的“直接这样告诉 Agent”](../README.md#直接这样告诉-agent)。

## 6. 完整设计流程：原理图 S0–S6

流程脊柱见 [design-flow.md](../.agents/skills/pcbpilot/references/design-flow.md)。要点：

| 阶段 | 做什么 | 关键命令 |
|---|---|---|
| S0 需求规格 | 把需求写成 `s0` 规格（模块、信号流、叠层、接口、决策） | `pcbpilot spec` |
| S1 选型 | 标准器件库优先，核对数据手册与 LCSC C 号；国际版需重解析 uuid | `scripts/parts-select.py`、`scripts/parts-relocalize.py`、`pcbpilot lib by-lcsc` |
| S2 连接数据 | 源数据副本写清连接、核心/外围归属、网络策略 | `pcbpilot sch zones-derive` |
| S3 区内布局 | 每个模块求解核心+外围的几何 | `pcbpilot sch layout-plan --zones` |
| S4 纸张布局 | 模块排到图纸（放不下就加大图纸或分页） | `pcbpilot sch layout-sheet-plan`、`sch layout-composition` |
| S5 Apply | 固定转换写入 EasyEDA | `pcbpilot sch compose --layout-page` / `pcbpilot apply` |
| S6 验证 | 回读对账、连接检查、DRC、保存重开 | `sch connectivity`、`sch layout-lint`、`sch check`、`sch drc`、`doc reload` |

网络连接约定：模块内部直连，电源/地用符号；**同一页模块之间用网络标签，跨页用端口**。

## 7. 完整设计流程：PCB P0–P10

| 阶段 | 做什么 | 关键命令 |
|---|---|---|
| P0–P1 导入 | 从原理图导入，逐焊盘对账 | `pcb import-changes`、`pcb dump`、`scripts/pad-net-diff.py` |
| P2–P5 布局 | 机械约束（板框、孔、贴边接口、天线净空）→ 离线求解 → 写入 | `pcb auto run --place --mech mech.json --power power.json --groups <原理图 composition>` → `pcbpilot apply` |
| P6 布局确认 | 整板预览 + 两轮自检（第 2 轮 save → reload → fresh dump → render），**交用户确认** | `pcb stage-snapshot --fit-mode board`、`pcb check`、`pcb drc` |
| P7–P8 布线与铜 | 关键网、电源主干、平面/分区铺铜、GND 铺铜 | `pcb auto run`（不带 `--place`）→ `apply`、`pcb pour-fit`、`pcb pour-rebuild` |
| P9 丝印 | 位号转正、避让焊盘/器件 | `pcb silk-align`、`pcb check` |
| P10 终检 | 原生 DRC、逐焊盘对账、DFM 检查、保存重开后 `contentSha256` 不变 | `pcb drc`、`pcb check`、`pcb dump` |

`pcb auto run` 的输入：

- `mech.json`：板框（固定尺寸，或 `autoSize` 自动搜索最小可行板框）、四角安装孔、贴边接口、
  禁布区、天线净空宽度；见 [mech-spec](../.agents/skills/pcbpilot/references/recipes/mech-spec.md)。
- `power.json`：电源轨电压/电流、差分对与阻抗目标；见 [power-spec](../.agents/skills/pcbpilot/references/recipes/power-spec.md)。
- `--groups`：原理图模块归属（让降压的输出电容跟着降压芯片，而不是被同一电源轨的别的芯片抢走）。
- `--replace <上一版 journal>`：重排已落地的板时，精确删除上一版创建的孔和禁布区，避免叠加。

输出 `plan.json`、`playbook.json`、`preview.svg`、`report.md`；**先读报告，再 `apply --dry-run`，再 `apply`**。
详细判读见 [pcb-auto-run](../.agents/skills/pcbpilot/references/recipes/pcb-auto-run.md)。

实测：ESP32-S3 最小系统板（30 件、4 层）在 EasyEDA V3 桌面版上从导入到 P10 全流程完成，
100% 布通、原生 DRC 通过，见 [showcase](showcase-esp32-mini.md)。

## 8. 常用命令速查

```bash
pcbpilot health                              # 连接、窗口、宿主版本
pcbpilot project info | find | open          # 工程；pcbpilot sch pages / pcb docs 列文档
pcbpilot doc switch <doc> --project <p>      # 切换文档
pcbpilot doc reload <doc> --project <p>      # 保存→关闭→重开（真实持久化验证）
pcbpilot sch connectivity --doc <page>       # 原理图连接数据
pcbpilot sch check | drc | layout-lint       # 原理图检查
pcbpilot pcb dump --include-copper --out b.json   # PCB 完整快照（含 semanticSha256 / contentSha256）
pcbpilot pcb auto run --board b.json ... --out-dir out/   # 离线规划（不写 EDA）
pcbpilot apply out/playbook.json --dry-run   # 预检
pcbpilot apply out/playbook.json --yes       # 执行（带 journal，可 --resume）
pcbpilot pcb drc | check                     # 原生 DRC / DFM 检查
pcbpilot pcb stage-snapshot --stage "P6"     # 整板预览
pcbpilot audit cost --day <YYYY-MM-DD> --record   # 记录一场 E2E 的成本
pcbpilot actions                             # typed action 目录
pcbpilot api search <关键词>                 # 官方 eda.* API 索引
```

完整命令索引：[原理图 CLI](cli/schematic.md)、[PCB CLI](cli/pcb.md)、[CLI 总览](cli/README.md)。

## 9. MCP 接入

`scripts/setup-agent.sh` 会自动注册。手动方式：

```bash
npm --prefix mcp ci --ignore-scripts
claude mcp add pcbpilot --scope user --env PCBPILOT_BIN="$(command -v pcbpilot)" -- node "$(pwd)/mcp/src/server.mjs"
codex  mcp add pcbpilot --env PCBPILOT_BIN="$(command -v pcbpilot)" -- node "$(pwd)/mcp/src/server.mjs"
```

MCP 是同一套 typed action 的另一个入口，不暴露任意 JavaScript（`debug.exec_js`）。详见 [mcp.md](mcp.md)。

## 10. 升级与版本

| 情况 | 做法 |
|---|---|
| 源码安装 | `git pull && scripts/setup-agent.sh`；连接器版本变了就重导 `.eext`（先卸载旧的） |
| 发布版 | `pcbpilot update`（CLI + Skill），`pcbpilot update --check` 看三方版本 |
| 只改了 CLI/daemon | 无需重导连接器；重启 daemon |
| 连接器 handler / manifest 变了 | 卸载旧连接器 → 导入新 `.eext` → 重新加载编辑器 → `health` 确认 `connectorVersion` |

版本只以签名 tag `vX.Y.Z`（`make release`）为准；推送到 `dev`/`main` 不是发布。
发布流程见 [release-workflow.md](release-workflow.md)。

## 11. 故障排查

| 症状 | 原因 | 处理 |
|---|---|---|
| 所有动作超时 / `health.windows` 为空 | 没开“允许外部交互”，或编辑器未重载；3.2.149 桌面版重启后连接器未自启 | 扩展管理器 → Config 页签勾选；刷新 Web 页面 / 重开桌面工程；3.2.149 重新导入 `.eext` |
| 导入新 `.eext` 后版本没变 | 未卸载旧的同 uuid 连接器；或旧页面仍在运行 | 先卸载再导入；重新加载编辑器 |
| `connector did not respond` 于放置器件 | 器件 uuid 属于另一站点（国际版 vs 国内版） | `scripts/parts-relocalize.py --out <副本>` 按 LCSC 号重解析 |
| lceda.cn 在海外很慢 | 服务器在国内 | 用国际版 pro.easyeda.com（V3），同样支持 |
| 重开后读回的器件角度是 -90 | 宿主浮点表示 | `pcb dump` 已归一化；对比时用 dump 数据 |
| 重开后 `semanticSha256` 变了但没改动 | 铺铜重新材料化（新 ID/顺序） | 用 `contentSha256` 做持久化证明 |
| 丝印位号压焊盘（连接器 < 0.2.8） | silk-align 按转正前尺寸规划 | 升级到 0.2.8；旧版连跑两次 |
| `pcb clear --only copper` 删了安装孔（连接器 < 0.2.8） | 旧版把 MULTI 层孔 fill 算作铜 | 升级到 0.2.8；或用 `pcb rip-up` + `pcb pour-delete --ids` |
| `pcbpilot: command not found` | 安装目录不在 PATH | `export PATH="$HOME/.local/bin:$PATH"` |

更多：[连接器运行时恢复](connector-runtime-recovery.md)、[开发环境](dev-environment.md)。

## 12. 能力边界

- EasyEDA 扩展 API 没有编程式 undo：通过源数据、写前守卫、回读和精确回滚降低风险。
- 连接器导入与“允许外部交互”必须由人完成；Agent 不操作 GUI。
- 泪滴、部分视图显隐等宿主未开放的接口标为 `unsupported`，不以 GUI 兜底。
- 受控阻抗所需的介质参数无法从 API 完整读取，报告只给目标线宽，不声称阻抗合格。
- 大型 BGA 板（RK3568、K230 级）自动布通率仍有限（离线回归 55–62%），适合作为起点；小中型板可全流程。
- 新器件建库以原厂数据手册为准；证据不足时会停下来问。

## 13. 开发者

- 仓库协作规则：[AGENTS.md](../AGENTS.md)（`CLAUDE.md` 为其软链接）
- 文档导航：[docs/README.md](README.md)；概念词汇：[concepts.md](concepts.md)
- 开发环境：[dev-environment.md](dev-environment.md)（`make dev` 热重载 daemon）
- 测试：`go test -short ./...`、`make lint-test`、`npm --prefix extension test`、`npm --prefix mcp test`、`make skill-check`
- 布局/布线引擎：[pcbauto.md](pcbauto.md)；固定端到端用例：[esp32MiniRequire.md](../esp32MiniRequire.md)
