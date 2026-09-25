# 快速开始

完整说明见 [使用手册](manual.md)。本页只列最短路径。

## 组成

| 部件 | 是什么 | 装在哪 |
|---|---|---|
| **CLI / daemon**（`pcbpilot`） | typed action 协议、写前守卫、审计、回读 | `~/.local/bin`（源码安装）或 `/usr/local/bin`（发布版） |
| **PCB Pilot Connector**（`.eext`） | 运行在 EasyEDA 内的薄桥接层，调用官方 `eda.*` API | EasyEDA 扩展管理器（侧载，**不在插件市场**） |
| **Skill**（`pcbpilot`） | Agent 的工作流、规则、样例、参考数据 | `~/.claude/skills`、`~/.codex/skills`、`~/.agents/skills` |
| **MCP**（可选） | 同一套 typed action 的 MCP 工具入口 | `mcp/src/server.mjs`，注册到 Claude Code / Codex |
| **EasyEDA Pro（宿主）** | V3（3.2.x）或 V4，桌面或 Web（pro.easyeda.com / lceda.cn） | 用户自己打开 |

## 1. 安装

**新机器，交给 Claude：**

```text
克隆 https://github.com/zhuangzard/pcbpilot 并按仓库 AGENTS.md 的「新机器安装」完成 pcbpilot 安装，
最后告诉我需要我手动做的连接器导入步骤。
```

**自己运行（源码，推荐）：**

```bash
git clone https://github.com/zhuangzard/pcbpilot.git && cd pcbpilot
scripts/setup-agent.sh          # --dry-run 先预览
```

它编译 CLI、链接 Skill、安装并注册 MCP（Claude Code / Codex / ZCode）、构建连接器 `.eext`，并把 daemon
装成**登录服务（必需）**：`pcbpilot daemon service install`，开机登录自动启动。
需要 Go ≥ 1.26、Node.js ≥ 20.17；没有 Go 时自动改用发布版。

**只装发布版：**

```bash
curl -fsSL https://raw.githubusercontent.com/zhuangzard/pcbpilot/main/install.sh | bash      # macOS / Linux
irm https://raw.githubusercontent.com/zhuangzard/pcbpilot/main/install.ps1 | iex             # Windows
```

发布版脚本会查询 GitHub latest release。遇到 `403`（匿名额度）时 `export GITHUB_TOKEN=…`，
或用 `PCBPILOT_VERSION=vX.Y.Z` 锁版本。其他变量：`PCBPILOT_INSTALL_DIR`、
`PCBPILOT_INSTALL_SKILLS=codex,claude,agents|none`、`PCBPILOT_SKILL_PRESERVE=1`、`PCBPILOT_GITHUB_PROXY`。

## 2. 导入连接器（人工）

1. EasyEDA Pro → **高级 → 扩展管理器 → 已安装**：先卸载旧的 “PCB Pilot Connector”
   （EasyEDA 按 uuid 去重，不卸载则新包静默导入失败）。上游的 “EDA Agent Connector” 可以保留。
2. 导入 `extension/build/dist/pcbpilot-connector_vX.Y.Z.eext`（源码）或
   [Release](https://github.com/zhuangzard/pcbpilot/releases/latest) 中的 `pcbpilot-connector.eext`。
3. 选中 “PCB Pilot Connector” → 状态为 `Enabled` → **Config** 页签 → 勾选 **允许外部交互**。
4. 重新加载编辑器：Web 刷新页面；桌面版重启 EasyEDA。

## 3. 验证

```bash
pcbpilot daemon service status # 登录服务已安装（必需）；缺失时 pcbpilot daemon service install
pcbpilot health                # windows[] 出现目标工程/文档；connectorVersion 与仓库一致
pcbpilot update --check        # 可选：CLI / Skill / 连接器三方版本
```

## 4. 开始使用

在 Claude Code / Codex 里描述需求并说明“使用 pcbpilot”，例如
[README 的示例](../README.md#直接这样告诉-agent)。PCB 布局完成后 Agent 会停下来请你确认，确认后才布线。

## 升级

| 情况 | 做法 |
|---|---|
| 源码安装 | `git pull && scripts/setup-agent.sh`；连接器版本变了就按第 2 步重导 |
| 发布版 | `pcbpilot update`（CLI + Skill）；升级后重启 daemon |
| 只改了 CLI / daemon | 无需重导连接器 |

`daemon start` 默认会把已存在的发布版 Skill 目录同步到当前 CLI 版本（开发构建不写入；
`PCBPILOT_SKILL_PRESERVE=1` 保留本地改动）。连接器是侧载的，daemon 只能检测版本落后并提示，重导需要人来做。

## 常见卡点

| 症状 | 原因 | 处理 |
|---|---|---|
| 动作全部超时、`health.windows` 为空 | 未勾选“允许外部交互” / 编辑器未重载 | 按第 2 步第 3、4 项处理 |
| 重导 `.eext` 后版本没变 | 未卸载旧的同 uuid 连接器；旧页面仍在跑旧代码 | 先卸载再导入；刷新 / 重启 |
| 桌面 3.2.149 重启后连不上 | 已知问题：侧载连接器重启后不自启 | 每次启动后重新导入同一个 `.eext`，状态点回 `Enabled` |
| 切换 Online / Half Offline 后连不上 | 两种模式的扩展存储不共用 | 在新模式下重新导入并授权 |
| 放置器件时 `connector did not respond` | 器件 uuid 属于另一个站点（国际版 / 国内版） | `scripts/parts-relocalize.py` 按 LCSC 号重解析 |
| `pcbpilot: command not found` | 安装目录不在 PATH | `export PATH="$HOME/.local/bin:$PATH"` |

延伸阅读：[使用手册](manual.md) · [功能清单](FEATURES.md) · [架构](architecture.md) · [开发环境](dev-environment.md)
