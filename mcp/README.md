# pcbpilot MCP

Local stdio MCP adapter over the existing `pcbpilot` CLI/daemon. It exposes 12
tools: connection health, action discovery, one tool for each of the seven safe
action domains, circuit blocks, the guarded workflow state machine, and project transfer. The
arbitrary-JavaScript debug domain is deliberately not exposed.

```bash
npm ci --ignore-scripts
PCBPILOT_BIN=/absolute/path/to/pcbpilot npm test
PCBPILOT_BIN=/absolute/path/to/pcbpilot npm start
```

Codex registration:

```bash
codex mcp add pcbpilot \
  --env PCBPILOT_BIN=/absolute/path/to/pcbpilot \
  -- node /absolute/path/to/mcp/src/server.mjs
```

The MCP process does not access EasyEDA directly. Mutations still pass through
the Go daemon, connector, workflow gates, audit log, and official `eda.*` API.
Mutating typed actions except `project.create` require both `project` and `doc`; use `pcbpilot_actions`
before calling a domain tool to inspect its typed payload. Workflow operations
use structured MCP fields instead of accepting arbitrary CLI options.

After registration, restart the MCP client so it discovers the new server. Run
`pcbpilot_health` first, then use `pcbpilot_actions` to select the exact typed
action. The Skill's inspect-before-mutate, save, reload, DRC, and workflow-gate
rules continue to apply to MCP calls.

## DeepSeek Harness (DSH) 集成

DSH 原生支持 skill 与 MCP client 两种形态，本仓库两者都已具备，接入是配置级
工作：详见 [`docs/dsh-integration.md`](../docs/dsh-integration.md)。要点：skill
软链到 `~/.dsh/skills/` 即被发现；MCP 在 profile 的 `cordis.patch.yml` 加一个
`@deepseek-ai/dsh-mcp-client` 实例（`serverName: pcbpilot`，指向本目录
`src/server.mjs`）即可，工具以 `mcp__pcbpilot__pcbpilot_*` 命名。注意 in-box
插件无需 pnpm 安装（fallback 从 dsh 安装目录解析），profile 里误装旧版会遮蔽
fallback。

## Creating a project from the home screen

Call `pcbpilot_health` and choose the intended connected window. Then call
`pcbpilot_project` with the following shape (replace the window ID):

```json
{
  "action": "project.create",
  "window": "<windowId from pcbpilot_health>",
  "payload": { "friendlyName": "New project", "open": true }
}
```

Do not supply `project` or `doc`: the new project is not an existing routing
target, and a home tab such as `tab_page1` is not a schematic/PCB document.
The adapter rejects contradictory routing instead of forwarding it to the
CLI document guard. The explicit window requirement also prevents implicit
selection between multiple editor windows.

Creation only makes the project container. Inspect the returned `created`,
`opened`, and `partial` fields and read back project state before continuing;
an open failure must not trigger blind duplicate creation. Document creation
is a separate operation. This exception does not relax routing requirements
for other mutations, including `schematic.page.create` and `board.create`.

The connected extension must implement `project.create`. If the call returns
`UNKNOWN_ACTION`, update the connector to a version providing that handler;
a compatible version reported by `pcbpilot_health` alone does not prove action
availability. Do not retry creation with a fabricated `doc` to work around it.


## Project transfer

Use `pcbpilot_project_transfer` for `operation: "open"` or `"export"`. Both require
an explicit `window` and `projectUuid`, without `project`/`doc` routing. Opening
requires `allowDiscardUnsaved: true` after saving all documents; optional `pageUuid`
waits for a schematic page and verifies its active identity. Export requires a new
`out` path ending in `.epro2`; it verifies the active project before/after capture,
validates ZIP integrity and reports SHA-256 without overwriting an existing file.
`restoreVerified: false` means an import round trip has not been verified.

These commands require the matching CLI build. Fixed official-API adapters support
released connectors without exposing arbitrary JavaScript to the MCP caller.

Project transfer requires connector handlers `project.open` and `project.export` and the matching CLI/daemon catalog. Both are catalogued; prefer `pcbpilot_project_transfer` for native export because the CLI validates and writes the archive. Old daemons/connectors reject unknown actions; no debug.exec_js fallback.
