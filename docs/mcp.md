# pcbpilot 的 MCP：它是什么、怎么接、和 Skill 的关系

## 一句话

pcbpilot 的 MCP 是一个**很薄的本地 stdio 适配层**：Node.js 进程把 MCP 工具调用翻译成一次
`pcbpilot` 命令行调用；它自己不连 EasyEDA、不存状态、不做设计决策。

```
AI 客户端（Claude Code / Codex / DSH）
   │  MCP 协议（stdio，JSON-RPC）
   ▼
pcbpilot-mcp（node mcp/src/server.mjs）
   │  execFile("$PCBPILOT_BIN", [...args])     ← 每次工具调用启动一次 CLI
   ▼
pcbpilot CLI ──HTTP──▶ pcbpilot daemon（127.0.0.1:61832）
                           │  WebSocket
                           ▼
                 PCB Pilot Connector（EasyEDA 扩展）──▶ 官方 eda.* API
```

所以 MCP 调用与直接敲命令走的是**同一条链路**：同样的 typed action 校验、审计日志、自动保存、
版本诊断和 `--project/--doc` 目标锁定。MCP 不会绕过任何护栏。

## 暴露哪些工具（11 个）

| 工具 | 作用 |
|---|---|
| `pcbpilot_health` | daemon 与已连接窗口、版本（先调它） |
| `pcbpilot_actions` | typed action 目录与参数说明（调领域工具前查它） |
| `pcbpilot_artifact` / `_board` / `_document` / `_pcb` / `_project` / `_schematic` / `_system` | 7 个安全领域，每个执行一条该领域的 typed action |
| `pcbpilot_blocks` | 电路块库查询 |
| `pcbpilot_workflow` | 工作流状态机（结构化参数，不接受任意 CLI 选项） |

**故意不暴露** `debug` 领域（任意 JavaScript 执行）。修改类 action（`project.create` 除外）必须同时给
`project` 与 `doc`。

**不在 MCP 里的**：CLI 的组合命令（`pcb auto`、`pcb dump`、`apply`、`sch compose` 等）不是单条 typed action，
MCP 不直接提供。需要它们时让 Agent 在终端运行 `pcbpilot ...`（Skill 的配方正是这样写的）。

## 本机当前状态（2026-09-22）

原版与 pcbpilot 的 MCP **并存**，互不影响：

| 客户端 | 服务名 | 命令 | 调用的 CLI |
|---|---|---|---|
| Claude Code（`~/.claude.json` user 级） | `easyeda-agent` | `node ~/Tools/easyeda-agent/mcp/src/server.mjs` | `~/.local/bin/easyeda`（原版） |
| Claude Code（`~/.claude.json` user 级） | `pcbpilot` | `node <仓库>/mcp/src/server.mjs` | `~/.local/bin/pcbpilot` |
| Codex（`~/.codex/config.toml`） | `easyeda-agent` | `node ~/.local/share/easyeda-agent/mcp/src/server.mjs` | `~/.local/bin/easyeda`（原版） |
| Codex（`~/.codex/config.toml`） | `pcbpilot` | `node <仓库>/mcp/src/server.mjs` | `~/.local/bin/pcbpilot` |

工具名前缀不同（`mcp__easyeda-agent__easyeda_*` 与 `mcp__pcbpilot__pcbpilot_*`），端口段不同，所以同一会话里
两套都能用；**要操作 pcbpilot 连接的窗口就用 `pcbpilot_*` 工具**。

## 在其他机器上接入

```bash
# 1. 安装 CLI + Skill（发布后）
curl -fsSL https://raw.githubusercontent.com/zhuangzard/pcbpilot/main/install.sh | bash
# 2. 取 MCP 源码并安装依赖（MCP 目前不随安装脚本分发，planned）
git clone https://github.com/zhuangzard/pcbpilot.git ~/Tools/pcbpilot
cd ~/Tools/pcbpilot/mcp && npm ci --ignore-scripts
# 3. 注册
claude mcp add pcbpilot -s user -e PCBPILOT_BIN=$HOME/.local/bin/pcbpilot -- node ~/Tools/pcbpilot/mcp/src/server.mjs
codex mcp add pcbpilot --env PCBPILOT_BIN=$HOME/.local/bin/pcbpilot -- node ~/Tools/pcbpilot/mcp/src/server.mjs
```

注册后重启客户端，先调 `pcbpilot_health`。自检：`cd mcp && PCBPILOT_BIN=... npm test`（8 项）。

## MCP 与 Skill 怎么配合

- **Skill** 是知识与流程：什么时候做什么、参数怎么定、结果怎么判（`pcbpilot` 包，含
  [collaboration-workflow](../.agents/skills/pcbpilot/references/collaboration-workflow.md) 与
  [recipes](../.agents/skills/pcbpilot/references/recipes/index.md)）。
- **MCP** 是执行通道之一：单条 typed action 的结构化调用。
- **CLI** 是另一条执行通道，并且提供 MCP 没有的组合命令。

Agent 按 Skill 决策，单条读写可以走 MCP 或 CLI，整板引擎等组合能力走 CLI。两条通道最终都落到同一个
daemon 和连接器。
