# pcbpilot console（v0.7 设计）：本地 Web 驾驶舱

状态：v0.7 为**开发中、未发布**（分支 `v07/console`）。本页区分“已实现（v0.7）”与“计划（v0.8/v0.9）”；
计划项在界面上同样标 `planned`，不冒充已完成。

console 是 daemon 在 `http://127.0.0.1:<端口>/ui/` 提供的本地单页应用。它回答三个问题：
系统现在在做什么（daemon、版本、窗口、每个项目、每条动作）、做到哪了（设计流程时间线、仿真轮次、
报告）、接下来需要我决定什么（决策卡）。它**只编排、不做 LLM**：Agent 仍是本机的 Claude Code / Codex。

## 边界（不可放宽）

- console 从不编辑 EDA 工程。所有 EDA 写入仍只经 daemon 的 typed action（Skill → CLI → `/action`），
  不绕过任何检查、门禁或回读要求；console 本身不调用 `/action` 的写动作。
- console 只写自己拥有的数据：`pcbpilot.project.json`、资料库（`resources/` + `.pcbpilot-kb/`）、
  决策卡答复、以及 `~/.pcbpilot/console/` 下的注册表。
- 决策卡记录用户取舍，不替代回读证据，也不能批准 Skill 禁止的操作（GUI 兜底、`debug.exec_js` 等）。
- 截图只是展示；验收证据仍是对象回读、DRC、check 与保存重载。

## 架构

```
浏览器 /ui (embed 静态页) ──fetch/SSE──▶ daemon :61832 ──┬─ /api/*   internal/console（只读聚合 + 自有状态写入）
                                                        ├─ /action  typed actions（不变）─▶ connector ─▶ eda.*
CLI (pcbpilot ask / kb / project-config / 长离线命令) ────┘   审计 sink：每条 action → 事件总线 → SSE
```

- `internal/console`：`go:embed ui/`（纯 HTML/CSS/JS，无构建步骤、无 CDN，离线可用，明暗主题）。
- daemon 只做**加法**：`Server.Handle`（挂载路由）、`Server.OnActivity`（审计 sink → 事件总线）、
  `Port/StartedAt/AutosaveDebounce`；审计行新增 `projectUuid/projectName/documentUuid/documentType/outputDir`
  （omitempty）。health 通过 daemon 自己的 `/health` 读取，所以其他分支新增的字段（如更新状态）自动透传。
- `daemon start --console=true`（默认）挂载；daemon 绑定非回环地址时 console 自动禁用。

## 页面

### 1. 监控（首页，最高优先级）

持续实时：SSE 推送，无需手动刷新；daemon 重启后浏览器自动重连，期间顶部横幅显示
“daemon 离线，正在重连…”，重连后按 pid/启动时间识别“daemon 已重启”并全量刷新。

```
┌ ● pcbpilot console [v0.7 dev] │ 监控 项目 活动流 Agent运行 决策(1) ─────────────── ◐ ┐
│ [在线] [窗口 1] [运行中项目 2] [项目总数 4] [活动运行/会话 3] [待决策 1]                │
├──────────────────────────────┬──────────────────────────────┬──────────────────────┤
│ Daemon                       │ 组件版本（对齐）              │ 待决策卡（前 3 条）   │
│ 状态 online  版本 0.7.0      │ daemon 0.7.0 aligned         │  [确认] [调整] 备注   │
│ PID 94255  端口 127.0.0.1:…  │ CLI 0.7.0 aligned            ├──────────────────────┤
│ 运行时长 1h 02m  自动保存 3s │ Skill(Claude) 0.6.0 stale    │ 活动运行              │
│ 登录服务 已安装/加载         │ MCP 0.7.0 / linked (source)  │  cli sim post-layout  │
│ 更新/离线重试状态（若有）    │ connector ab12 0.7.0 aligned │  会话 host:claude …   │
├──────────────────────────────┴──────────────────────────────┼──────────────────────┤
│ EasyEDA 窗口：窗口 | 宿主(V3/V4+版本) | connector | 工程 | 文档 | 连接于 | 心跳    │ 实时动作流            │
├─────────────────────────────────────────────────────────────┤ 12:40:33 ✓ pcb.save  │
│ 运行中的项目：项目 | 状态 | 阶段 | 最终状态(DRC/报告) | 动作 | 时长 | 最后活动 | 最后错误 │ 12:40:31 ✗ pcb.pour… │
├─────────────────────────────────────────────────────────────┤ …                     │
│ 全部项目（空闲 / 已结束，含重启前的历史）                     │                       │
├─────────────────────────────────────────────────────────────┤                       │
│ 本地工作目录：目录 | 状态(运行中/失败/已完成/进行中) | 最新报告 | 最新工件 | 模板 │                       │
└─────────────────────────────────────────────────────────────┴──────────────────────┘
```

| 组件 | 数据来源 |
|---|---|
| Daemon 卡 | `DaemonInfo`（pid、`Server.StartedAt`、端口、`AutosaveDebounce`）+ 登录服务状态（`readDaemonServiceStatusCtx`：后台单飞探测，10 s 截止并杀掉挂住的 launchctl/systemctl，30 s 缓存；`serviceProbe.state` = `checking / stale / timeout / error / ok`，页面显示“检查中…”或缓存时间。`/api/status` 与 SSE hello 从不等待探测，最多等 200 ms） |
| 组件版本 | daemon/CLI 同一二进制；Skill 与自更新共用 `selfupdate.Targets`（Claude Code / Codex / `~/.agents` / ZCode，只列已存在目录）：拷贝安装读 `.version`，软链源码安装读 `SKILL.md` 的 `metadata.version` 并标 `linked`；MCP 与 `pcbpilot update --check` 一致：各客户端（Claude Code / Codex / ZCode / `~/.agents`）的注册全部指向源码检出 `mcp/src/server.mjs` 时显示 `linked (source)`，否则显示发布戳 `~/.pcbpilot/mcp/current/VERSION`（v0.6.1 起 release 包带发布版本，不再读 `mcp/package.json`）；connector 读 health `windows[].connectorVersion` 与 `connectorVersionOk`。徽章：`aligned / drift（补丁不同）/ stale（主次版本不同）/ dev / missing / linked（源码检出，git pull 更新）` |
| 更新状态 | health 若含 `updates`/`update`/`offline` 字段则原样展示（v0.6.1 自更新分支提供）；缺失时写明“未报告” |
| 窗口 | health `windows[]`：宿主形态（V3/V4 + 精确版本）、工程、文档、连接与心跳时间 |
| 项目表 | 项目注册表（见下）+ 窗口在线数 + 旧 workflow 最高阶段（诊断）+ 关联工作目录的最新报告结论 + 运行中的 CLI 命令数 |
| 实时动作流 | 事件总线 `activity`；首屏用审计日志尾部（今天/昨天，各 ≤ 8 MB） |
| 本地工作目录 | `workdirs.json` + 工件扫描 + 运行登记：离线设计链（没有 EDA 窗口的项目）的最终状态与原因 |

**项目注册表**（`~/.pcbpilot/console/registry.json`，schemaVersion 1，派生数据、非权威）：

- 启动时后台回填最近 60 天审计日志，按文件字节游标增量读取（半行留到下次）；回填完成后每次持久化把
  今天/昨天文件游标移到 EOF（此后的行都已由实时流计入），重启不重复计数。
- 归属顺序：行内工程上下文（v0.7 起审计行自带）→ 窗口最近一次 `project.current` / `document.current`
  结果推导（`derived`）→ `unattributed`（旧日志无上下文）。daemon 本地行（无窗口无工程，如 `system.health`）不计入任何项目。
- 每个项目：首次/最后活动、活动时长（相邻动作间隔 < 10 分钟累加）、动作/失败/写入数、最后动作、最后错误、
  最后 DRC（passed/违规数）、最后保存、文档、客户端、窗口、CLI 工作目录（`outputDir`）。
- 状态：`running`（5 分钟内有动作）/ `idle`（24 小时内或窗口在线）/ `finished`。

### 2. 项目（工作目录）

工作目录 = Agent 运行 CLI 的本地文件夹。登记来源：`project-config init`、`console projects add`、console 页面。
列表显示流程模板、EDA 工程、最新工件、最新报告与**目录状态**：`running`（有运行中的命令）/ `failed`
（最新报告 FAIL 或最近一次命令失败）/ `finished`（有 PASS 或 PASS with warnings 报告）/ `in-progress`。

```
┌ ESP32-S3 mini  /path/to/work ───────────────────────────────────────────────┐
│ [时间线] [仿真轮次] [报告] [资料库] [流程配置]                               │
├─────────────────────────────────────────────────────────────────────────────┤
│ 原理图 S0–S6.5: [S0 完成(隐含)] … [S5.5 完成 analog.json] [S6.5 完成 intent]  │
│ PCB P0–P10.5:   [P7 完成 plan.json] [P9 跳过：理由] [P10 完成 check] …       │
│ 报告 P11:       [P11 完成 reports/…/v2/manifest.json]                        │
│ 工件表：类型 | 步骤 | 路径（只读打开）| 时间 | 大小                          │
└─────────────────────────────────────────────────────────────────────────────┘
```

| 标签页 | 数据来源 |
|---|---|
| 时间线 | 有界扫描工作目录（深度 ≤ 6、≤ 20 000 文件、跳过 `.git/node_modules/resources/.pcbpilot-kb`），按 JSON 头部 `generator` 识别：`intent derive`→S6.5、`sim power`→S6.5、`sim analog`→S5.5、`sim post-layout`→P10.5、`report design` manifest→P11；`pcb auto run` 的 feedback/plan→P7，`board.routed`→P8，`pcb check`/DRC/板级 dump→P10；再叠加旧 workflow 历史与 `pcbpilot.project.json`。同 sha256 的多份副本合并为别名。状态：`done`（有证据）、`implied`（自身无工件、后续步骤有证据）、`pending`（**无证据 ≠ 没做**）、`skipped`（配置跳过；若仍有证据会注明）；
报告包 `vN/data/` 里的副本与原件同 sha256 时以原件为主路径 |
| 仿真轮次 | 每个 sim-power/analog/post JSON 解析关键指标（电源轨 V/I、器件总功耗、收敛；目标满足/失败、finding 数；板最高温、最坏 IR 占预算、过孔最大利用率），按时间排序与同类上一轮求差（Δ） |
| 报告 | `vN/manifest.json`（版本、结论、生成时间、文件数、html/md/zip）；预览 iframe 由响应头 `Content-Security-Policy: sandbox` 隔离（iframe 本身不加 sandbox 属性：那会让请求变成不透明源发起，SameSite=Strict cookie 不随请求发送） |
| 资料库 | 见“资料库”一节 |
| 流程配置 | `pcbpilot.project.json` 的步骤/仿真/报告章节开关与约束，保存前服务端校验 |

文件读取 `GET /api/workdirs/{id}/file?path=`：路径清洗 + 解析符号链接后必须仍在工作目录内；响应带
`Content-Security-Policy: sandbox`（不透明源、无脚本），存储的 HTML 无法携带 console cookie 调 API。

### 3. 活动流

全部实时动作（最近 2000 条内存环 + 审计尾部），可按动作/工程/错误码/客户端过滤。

### 4. Agent 运行

- **登记的运行**（`POST /api/runs/events`）：v0.7 由 CLI 自动上报长离线命令（`sim power|analog|post-layout`、
  `report design`、`intent derive`、`pcb auto run`、`kb add|reindex`）的开始/结束（≤ 300 ms、best-effort、
  `PCBPILOT_CONSOLE_HOOK=0` 关闭）；结束的运行追加到 `~/.pcbpilot/console/runs.jsonl`，重启后仍可见。
- **推断的 CLI 会话**：同一主机（+`PCBPILOT_CLIENT_LABEL`）的动作间隔 < 5 分钟聚为一个会话。每条 CLI
  命令是一个进程，所以这只是“一个 Agent 在工作”的近似；页面明确写出这一点。
- **本地 Agent 桥接**：`planned (v0.8)`，页面顶部标注。

### 5. 决策卡

`pcbpilot ask` 提问 → 卡片出现在首页与决策页 → 用户点选项（可加备注）→ 命令返回。见下文 API 与 CLI。

## HTTP / SSE API

全部在 `/api/` 下，经统一守卫（见“安全”）。JSON；错误形如 `{"ok":false,"error":{"code","message"}}`。

| 方法 路径 | 用途 |
|---|---|
| `POST /api/session` | 用 `X-Pcbpilot-Token` 换 HttpOnly `SameSite=Strict` cookie（EventSource 不能设请求头） |
| `GET /api/events` | SSE：`hello`（daemon 身份）、`activity`、`ask`、`run`、`project`、`status`、15 s `heartbeat`；`retry: 2000` |
| `GET /api/status` | daemon、health 原文、窗口、组件版本、更新状态透传、console 状态（订阅数、待决策、回填）。health 经后台单飞探测，最多等 750 ms，超时返回上次结果并标 `healthProbe.state=stale` |
| `GET /api/activity?limit=&project=` | 最近动作（无 payload/result，只保留 passed/saved/violations 等小字段） |
| `GET /api/projects` | 监控项目表 + 工作目录列表（含目录状态） |
| `GET /api/templates` | 模板、步骤、仿真、报告章节、资料类型目录 |
| `POST /api/workdirs` / `DELETE /api/workdirs/{id}` | 登记 / 取消登记（不删文件；只接受存在的绝对路径目录） |
| `GET /api/workdirs/{id}` | 配置+校验、时间线、工件、仿真轮次、报告、资料库统计 |
| `GET /api/workdirs/{id}/file?path=&download=1` | 只读文件（沙箱 CSP） |
| `GET|PUT /api/workdirs/{id}/config`、`POST …/config/init` | 读 / 校验后写 / 按模板创建 `pcbpilot.project.json` |
| `GET …/kb`、`GET …/kb/search?q=&k=&kind=&tag=&doc=`、`GET …/kb/{doc}?chunks=1`、`POST …/kb/upload`、`POST …/kb/{doc}/tags` | 资料库 |
| `GET /api/runs`、`POST /api/runs/events` | 运行登记（v0.8 桥接写入同一接口）与推断会话 |
| `GET /api/ask`、`POST /api/ask`、`GET /api/ask/{id}?wait=≤60`、`POST /api/ask/{id}/answer`、`POST /api/ask/{id}/cancel` | 决策卡队列（长轮询） |

## 项目配置 `pcbpilot.project.json`（schemaVersion 1）

```json
{
  "schemaVersion": 1,
  "name": "ESP32-S3 mini",
  "template": "full",
  "eda": { "project": "1cdaf015a8ae4a68ba26327e6b6c31f0" },
  "steps": { "S5.5": {"enabled": true}, "P9": {"enabled": false, "reason": "客户自带丝印规范"} },
  "sims": { "power": {"enabled": true}, "analog": {"enabled": true}, "monteCarlo": {"enabled": false, "reason": "模板 full"},
            "postLayout": {"enabled": true}, "elmer": {"enabled": false, "reason": "模板 full"} },
  "reportSections": { "3A": {"enabled": true}, "6A": {"enabled": true} },
  "constraints": {
    "standards": ["IPC-2221B", "IEC 62368-1"],
    "requirements": ["resources/requirement/customer-requirement.md"],
    "mech": "mech.json",
    "fab": { "profile": "jlcpcb", "layers": 4 },
    "parts": { "preferred": ["C6186"], "banned": ["C123456"] },
    "notes": ""
  },
  "resourcesDir": "resources",
  "updatedAt": "2026-09-28T16:40:00Z",
  "updatedBy": "console"
}
```

- 读取严格：未知字段拒绝（防止 `"enable": false` 之类拼写让步骤静默保持开启）；新于本版本的
  schemaVersion 拒绝；旧文件缺少的目录项按默认补齐。
- 模板：`full`、`quick-proto`（跳 S5.5/P10.5、§3A/§6A）、`schematic-only`、`layout-from-existing`、`analog-heavy`。
- 守卫（error）：S4 开则 S5 必开；P7/P8 开则 P10 必开；P7 开则 P6 必开；步骤开而其仿真关；
  Elmer 需 postLayout；Monte-Carlo 需 analog；必需章节 0/1/7/11 不能关；同一器件既优选又禁用。
  提示（warn）：跳过无理由；§3A/§6A 开但对应仿真关；约束路径不是相对路径。
- CLI：`pcbpilot project-config init|show|set|validate`（`--dir`）。Skill 先读它，跳过的步骤在报告
  §11.5“按项目流程模板跳过”列出理由（`report design --project-config`，默认自动读 `./pcbpilot.project.json`），
  被配置跳过的章节不再计入“缺失”，§7 的 N/A 仍表示没有证据。

## 资料库（`pcbpilot kb`）

目标：单项目 ~500 份 PDF 也能用，Agent 永远不把整库读进上下文，而是 **索引 → 筛选 → 深读**。

```
<工作目录>/resources/<kind>/<清洗后文件名>      原件（kind: paper datasheet standard requirement reference-design mech other）
<工作目录>/.pcbpilot-kb/catalog.json           文档元数据（schemaVersion 1）：id(sha256 前 12)、sha256、路径、标题、类型、标签、
                                                页数、分片数、字符数、提取器、状态、说明、摘要槽 {text,keyPoints,by,at}、别名
<工作目录>/.pcbpilot-kb/text/<id>.jsonl        每行一个分片 {n,page,text}
<工作目录>/.pcbpilot-kb/terms/<id>.json        每文档倒排 {lens:[每分片词数], tf:{词:[[分片,次数]…]}}
```

- 提取：PDF 用纯 Go 读取器（`github.com/ledongthuc/pdf`，BSD，2026-09 仍在维护，保持单二进制；按行重排保留表格行），
  文本过少（< 40 字符/页，常见于无 ToUnicode 的 CID 字体）时若本机有 `pdftotext` 自动改用；两者都没有文字时标
  “可能是扫描件，未内置 OCR”。md/txt/csv/json 原文；html 去脚本/样式/标签；docx 读 `word/document.xml`。
  其他格式（图片、STEP、DXF、zip、xlsx）只存储（`stored-only`）。限制：≤ 3000 页、≤ 32 MB 文本；读取器 panic 被捕获。
- 分片：约 1200 字符、重叠 150，优先在段落/句末断开，**不跨页**，所以每个命中都能引用到页。
- 检索：Okapi BM25（k1 1.2、b 0.75），分词：拉丁/数字连写为词（`ams1117`、`3.3` 保持完整），中日韩字符取二元组；
  每文档最多 3 个命中防止长手册霸屏；`--docs` 给出按文档汇总的筛选表。引用键 `kb:<id>#p<页>`（无页格式 `#c<分片>`）。
- 去重：同 sha256 不重复存储，新来源记为别名、合并标签。并发：跨进程锁文件（2 分钟过期）+ 原子写。
- 规模：500 份 × 平均 60 页 ≈ 3 万分片；倒排按文档分文件、console 进程内按 mtime 缓存，单次检索只读命中文档的正文。
  v0.9 若实测变慢，改为单段合并索引（格式版本升级，`kb reindex` 重建）。
- CLI：`pcbpilot kb add <文件|目录> --kind --tag`、`list`、`search <q> [--docs] [--doc id] [--kind] [--tag]`、
  `show <id> [--pages 3-5|--chunk n]`、`set-summary <id> --text|--file --point`、`tag`、`summarize-status`、`reindex`。
- console：拖放/选择上传（multipart 流式到 `.pcbpilot-kb/upload-tmp`，单文件 ≤ 200 MB、单请求 ≤ 1 GB、扩展名白名单、
  文件名清洗后写入 `resources/<kind>/`），列表、检索、打标签。

## 决策卡（`pcbpilot ask`）

```
pcbpilot ask --question "Layout 回读版本可以进入 P7 吗？" --option ok="确认" --option adjust="还要调整::说明模块" \
  --default adjust --timeout 2h --step P6 --context-file review.md
→ stdout {"id","status":"answered","choice":"ok","note","by":"console|cli|default","at"}
```

- 队列在 daemon 内存；结束的卡追加 `~/.pcbpilot/console/decisions.jsonl`（决策日志）。卡片有选项（≤ 12）或允许自由回答，
  超时 ≤ 24 h；到期有 `--default` 则返回默认（`by:"default"`）。退出码：0 已答/默认、2 无人可问、3 到期无默认、4 撤销。
- 回退：daemon/console 不可达且 stdin 是终端时在终端提问（`--fallback none` 关闭）；daemon 重启导致卡片丢失时报错，要求重新提问。
- Skill 规定何时用：P6 Layout 确认、`sim analog` 改值计划、`feedback.json` 换脚、跳过步骤、候选方案二选一。

## v0.8：本地 Agent 桥接（设计，未实现）

目标：在 console 里和本机 Agent 对话；console 负责启动、观察、转发，不自带模型。

| 部件 | 设计 |
|---|---|
| 启动 | 每个会话一个子进程，工作目录 = 项目工作目录，环境带 `PCBPILOT_CLIENT_LABEL=<run id>`（审计行与会话一一对应）。Claude Code：优先 Claude Agent SDK（TypeScript/Python，通过一个小的本地 sidecar），或 `claude -p --output-format stream-json --verbose --input-format stream-json`；Codex：`codex exec --json`。只启动用户已安装、已登录的 CLI；console 不保存任何模型凭据 |
| 事件映射 | 流式 JSON 的 `system/init`、`assistant`（文本、`tool_use`）、`user`（`tool_result`）、`result`（用量/花费/时长）→ `POST /api/runs/events`（`start/progress/tool/end`）；`Task`/子 Agent 调用 → 以 `parentRunId` 建子运行，形成运行树；Codex `item.*` 事件同理 |
| 用户输入 | console 输入框 → 会话 stdin（stream-json 输入）；中断 = 发送 interrupt / SIGINT |
| 权限提示 | Claude Code 以 `--permission-prompt-tool` 指向 pcbpilot 提供的 MCP 工具（或 SDK `canUseTool` 回调），把“是否允许执行 X”转成决策卡（选项：允许一次 / 拒绝 / 总是允许此规则），答复回写给 Agent；Codex 用其审批模式的等价事件。console 从不自动批准 |
| 决策卡 | Agent 在 Skill 指引下调用 `pcbpilot ask`（v0.7 已实现），卡片带 `runId` 显示在对应会话旁 |
| 安全 | 仍只绑定回环；启动 Agent 需要页面令牌 + 显式点击；每个会话的命令行与工作目录在启动前展示；Agent 写 EDA 仍只能走 Skill → CLI → typed action |

## 安全

- 仅绑定 `127.0.0.1`（daemon host 非回环时不挂载 console）。
- Host 头必须是 `127.0.0.1/localhost/[::1]`（防 DNS 重绑定）；有 `Origin` 时必须是同端口的回环 http 源，`null` 拒绝。
- 每次安装一个令牌 `~/.pcbpilot/console.token`（0600，32 字节随机）。`console open|url` 把令牌放在 URL
  `#fragment`（不发给服务器），页面用请求头换取 HttpOnly `SameSite=Strict` cookie 并立即从地址栏去掉令牌；
  cookie 认证的非 GET 请求还要 `X-Pcbpilot-Csrf: 1`（跨站表单无法设置自定义头）。
- 不发任何 CORS 头；静态页 CSP `default-src 'self'`、`frame-ancestors 'none'`、无内联脚本/样式；文件响应 `sandbox`。
- 上传只写进工作目录的 `resources/`，扩展名白名单、文件名清洗、大小限制；文件读取禁止路径与符号链接逃逸。

## 分期

| 版本 | 内容 | 状态 |
|---|---|---|
| v0.7 | 本页的监控首页、项目/时间线/仿真轮次/报告、活动流、运行登记与会话推断、项目配置 + Skill 读取、资料库（CLI + 上传/检索）、`pcbpilot ask` 队列与终端回退、CLI 长命令上报、安全模型、离线端到端验收脚本 `scripts/console-e2e.sh` | 开发中（已实现，离线验证） |
| v0.8 | 本地 Agent 桥接（Claude Code / Codex 会话、子 Agent 与工具调用树、权限提示转决策卡、console 内对话）；资料库摘要由 Agent 批量回填的队列 | 计划 |
| v0.9 | Tauri 托盘应用安装器：打包 pcbpilot 二进制、daemon 登录服务、connector `.eext`、ngspice/Elmer、Skill 与 MCP 安装；托盘图标显示 daemon 状态与待决策数，点开即 console；资料库大规模索引格式与 OCR 可选组件 | 计划 |

## 验收

- `go test -short ./internal/console/ ./pkg/kb/ ./pkg/projectconfig/ ./internal/daemon/ ./internal/app/`：
  令牌/Origin/Host 拒绝、cookie+CSRF、SSE（hello/activity/heartbeat）、决策卡长轮询/冲突/到期、注册表回填归属与增量游标、
  文件路径与符号链接逃逸、配置校验与写入、上传清洗与检索、运行登记与会话推断、PDF/DOCX/HTML/MD 提取与中英文检索。
- `scripts/console-e2e.sh <out>`：ESP32（两轮，第二轮 R9 1 kΩ→330 Ω）、HV 反激（市电→SELV）与隔离违规板（必须 FAIL）
  的离线全链路 `intent derive → sim power → sim analog → pcb auto run → pcb check → sim post-layout → report design`，
  console 显示时间线、仿真 Δ、报告与目录状态。截图见 [assets/console/](assets/console/)。

### 2026-09-28 离线实测记录（临时 HOME、daemon 端口 61850，无 connector，不接触任何 EDA 编辑器）

| 工作目录 | 链路结果 | 报告结论（console 目录状态） |
|---|---|---|
| ESP32-S3 mini，第 1 轮 | 7/7 步完成；`pcb auto --place --layers 4` 布通 33/33 | v1 FAIL：设计后仿真板温 192 °C（usb-only，把峰值电流当持续功耗、自动布局收紧到 46×45 mm 的保守上限）→ `失败` |
| ESP32-S3 mini，第 2 轮（R9 1 kΩ→330 Ω） | 7/7 步完成 | v2 FAIL；CHANGELOG 列出 LED1 余量 94.4 %→84.8 % 等差值；仿真轮次页显示 I(+3V3) +2.4 mA、板最高温 +0.07 °C 等 Δ |
| HV 反激（市电→SELV） | 7/7 步完成；布通 37/53 | v1 FAIL：模拟 error 1、`pcb check` copper-to-edge 8 条（市电域加强绝缘要求 ≥ 259.9 mil）、布线未完成、IR/设计后仿真未通过 → `失败` |
| 隔离违规板 + mains/SELV intent | check + report | v1 FAIL（按预期）：2 项工程计算不满足、copper-to-edge → `失败` |
| 仓库样例 ESP32 v3 报告包（demo-work） | — | v3 PASS with warnings → `已完成`；运行登记的命令进行中时显示 `运行中` |

第一次运行暴露并修复了 `pcb auto` 路由器越界 panic（`nodeCong` 在板边对小半径过孔圆盘逐格取值时越过网格；
回归测试 `TestNodeCongAtGridEdge`）。ESP32 不带 `--place` 时手工摆放布通 95.9 %，脚本因此按客户需求用 4 层并运行布局器。
报告预览 iframe 在浏览器中加载为 HTTP 200；headless 截图不合成该跨源沙箱帧，所以报告内容另以
`hv-iso-fail-report-html.png` 直接截取。

| 截图 | 内容 |
|---|---|
| [monitor.png](assets/console/monitor.png) / [monitor-dark.png](assets/console/monitor-dark.png) | 监控首页（亮/暗）：daemon、组件版本（Skill 0.6.0 对 0.7.0 标 stale）、窗口、运行中/历史项目、本地工作目录状态、待决策卡、活动运行、实时动作流 |
| [monitor-service-checking.png](assets/console/monitor-service-checking.png) | daemon 启动 3 s、登录服务探测（假 launchctl 挂住）尚未返回时页面已完整渲染，服务显示“检查中…” |
| [components-source-install.png](assets/console/components-source-install.png) | 源码安装：Skill（Claude Code / ZCode）读 `metadata.version` 标 `linked`，MCP 显示 `linked (source)`；登录服务探测返回后显示“已安装并加载” |
| [projects.png](assets/console/projects.png) | 工作目录列表：已完成 / 失败 / 运行中 |
| [esp32-timeline.png](assets/console/esp32-timeline.png)、[hv-flyback-timeline.png](assets/console/hv-flyback-timeline.png)、[demo-finished-timeline.png](assets/console/demo-finished-timeline.png) | 设计流程时间线与工件表（P9 按配置跳过） |
| [esp32-sims.png](assets/console/esp32-sims.png)、[hv-flyback-sims.png](assets/console/hv-flyback-sims.png) | 仿真轮次与 Δ |
| [hv-iso-fail-report-html.png](assets/console/hv-iso-fail-report-html.png) | FAIL 报告封面与不通过原因 |
| [library.png](assets/console/library.png)、[process.png](assets/console/process.png) | 资料库与流程配置 |
| [agents.png](assets/console/agents.png)、[activity.png](assets/console/activity.png)、[decisions.png](assets/console/decisions.png) | 运行登记/会话推断（桥接标 planned）、活动流、决策卡 |
