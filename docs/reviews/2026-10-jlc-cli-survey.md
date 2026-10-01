# 嘉立创官方 EasyEDA Pro 客户端 CLI 调研（2026-10-01）

> 状态：`source-only`。本文只依据公开文档与仓库源码阅读，**未在本机运行**该 CLI。原因：本机安装的
> EasyEDA Pro 为 `3.2.149`，而 CLI 要求客户端 `V4.1.60+`；官方文档也以 Windows 客户端为基准。
> 凡未经实测的说法均标注“未验证”。访问日期均为 2026-10-01。

## 0. 结论速览

1. **找到了官方发布**：`easyeda/easyeda-client-cli`，2026-09-30 01:47 UTC 建仓（北京时间 09:47 首次提交）。
   它**不是独立安装的 CLI 程序**，而是**嘉立创 EDA 专业版桌面客户端 V4.1.60+ 内置的命令行入口**
   （国际版 `easyeda-pro`，国内版 `lceda-pro`）的**文档仓库**。仓库只有 4 个 Markdown 文件，没有源码。
2. **“不开编辑器生成工程文件”需要打折理解**：CLI 本身每条有效命令都要通过本地 bridge 连到一个
   **正在运行的客户端编辑器**（可用 `open --headless true` 隐藏窗口，但进程仍在）。文档中的“直接生成工程文件”
   是指脚本直接写 `.eprj3` folder 工程的 `.esch2` / `.epcb2` 文本，但官方推荐流程仍是：
   **先用 API 建骨架 → 从编辑器保存的文件里摘取库文档（SYMBOL/FOOTPRINT/DEVICE）→ 脚本写业务记录 →
   编辑器自动重载后再用 API 做计数、DRC、截图验收**。真正“完全不需要编辑器”的生成路线仍是
   我们 §12 已调研过的 `easyeda-eprj3-skill` + `easyeda-format-skill`。
3. **AI 相关能力**：内置 MCP 服务器（`--mcp stdio` / `--mcp http --port --token`）、统一 JSON 响应、
   `doc api` / `doc format` 两套可渐进查询的 API 与文件格式参考、一份专为 Agent 写的 `cli-for-ai.md`
   操作规程。**没有**内置 LLM、自然语言设计或自动选型；所有设计动作都是 Agent 通过 `invoke` 下发任意 JS。
4. **数据库**：CLI 没有独立的器件数据库接口。器件检索走编辑器内的 `eda.lib_Device.search()`，
   文档称“半离线模式下系统库完整可用（含 LCSC 编号、封装、3D）”。没有公开限流或数据许可条款（未验证）。
5. **对 pcbpilot 的意义**：最有价值的是 (a) 官方承认并文档化的**桌面客户端进程内 bridge + headless 会话**，
   可作为我们 WebSocket connector 之外的第二条传输通道（可省去手动导入 `.eext` / 打开“允许外部交互”，未验证）；
   (b) `doc format` 提供的**宿主自带格式 Schema**，可做我们 fixture/离线解析的权威对照；
   (c) `.eprj3` 双向同步语义，可让我们的离线引擎输出直接落盘再由宿主重载回读。
   我们在 typed action、写前守卫、离线布局布线引擎、仿真、报告、美观度、版本门与自更新方面全面领先，
   它不替代我们的主链。

## 1. 识别结果

### 1.1 官方项目

| 项 | 内容 |
|---|---|
| 名称 | `easyeda-client-cli`（“嘉立创EDA专业版客户端的CLI命令”） |
| URL | https://github.com/easyeda/easyeda-client-cli |
| 发布时间 | 仓库创建 2026-09-30T01:47:50Z；3 个提交均在 2026-09-30 09:47–10:14 (+0800)：Initial commit → README 双语索引 + CLI 文档 → 修正版本号描述（HEAD `699e686`） |
| 官方站点 | https://prodocs.easyeda.com/en/api/guide/cli.html（页面 Last updated `2026-09-30T02:13:26Z`）；AI 版纯文本 https://prodocs.easyeda.com/storage/texts/api/guide/cli-for-ai.md（与仓库 `cli-for-ai.md` 仅一处 `toB64` 写法差异） |
| 内容 | `README.md`、`easyeda-cli_en.md`、`easyeda-cli_zh.md`（各 1508 行）、`cli-for-ai.md`（1522 行），无源码、无 Release、无 tag |
| 语言 | GitHub 语言字段为 null（纯文档） |
| 许可 | README 末尾写 “Apache-2.0”，但仓库**没有 LICENSE 文件**，GitHub license 字段为 null。注意：这只可能覆盖文档；CLI 本体是闭源客户端的一部分，受 EasyEDA 客户端用户协议约束（协议原文未审阅，未验证） |
| 安装 | 无需单独安装：下载桌面客户端 V4.1.60+，安装器把目录加入 PATH（文档以 Windows `C:\Program Files\EasyEDA-Pro\easyeda-pro.exe` 为例；macOS/Linux 路径未写，未验证） |
| 版本 | 跟随客户端版本，`--help` 首行打印 `EasyEDA Pro 4.1.x.<hash>`；文档没有独立 CLI 版本号 |
| 宿主限制 | 仅桌面客户端；**浏览器 / 在线版不支持** session 与 bridge |

国内 `prodocs.lceda.cn` 对应页面本机访问超时，国内版文档未核对（未验证）。

### 1.2 同期相关的官方仓库（`github.com/easyeda`，2026-09 新建）

| 仓库 | 建仓 | 许可 | 与本题关系 |
|---|---|---|---|
| [easyeda-eprj3-skill](https://github.com/easyeda/easyeda-eprj3-skill) | 09-08 | MIT | 纯 Node 离线生成 `.eprj3`，**真正不需要编辑器**；v1.7.3，HEAD `40c94e1`（2026-09-18，与 §12 调研时相同，无新提交） |
| [easyeda-format-skill](https://github.com/easyeda/easyeda-format-skill) | 09-09 | MIT | 格式知识 + 297 份 JSON Schema + `validate.js`；**2026-09-28 由 `easyeda-pro-format-skill` 改名**（旧链接重定向），新增 `FORMATLOG.md` 记录 V3→V4 格式差异 |
| [kicad-to-easyeda-eprj3](https://github.com/easyeda/kicad-to-easyeda-eprj3) | 09-09 | Apache-2.0 | KiCad → `.eprj3` 转换器，附带真实 `.eprj3` 样例工程（含 `P1/P2.esch2`、`PCB1.epcb2`） |
| [easyeda-viewer](https://github.com/easyeda/easyeda-viewer) | 09-14 | Apache-2.0 | epro2 / eprj3 浏览器查看器（Vite + Go 桌面壳），含解析器与渲染 |
| [easyeda-api-skill](https://github.com/easyeda/easyeda-api-skill) | 03-16 | null | 已到 `1.1.42`（2026-10-01），2026-09-30 新增 `guide/cli.md`；自身仍是 Node WebSocket bridge（49620–49629） |
| [eext-batch-data-export](https://github.com/easyeda/eext-batch-data-export) | 09-30 | Apache-2.0 | 同日发布的批量导出工程数据扩展（未深读） |
| [eext-ai-reuse-block-placement](https://github.com/easyeda/eext-ai-reuse-block-placement) | 09-10 | Apache-2.0 | AI 复用模块放置扩展（未深读） |

### 1.3 易混淆的非官方候选

- `l3wi/jlc-cli`（npm `@jlcpcb/cli` / `@jlcpcb/core` / `@jlcpcb/mcp`，MIT）：README 明言 **Unofficial**，
  做 LCSC 搜索 + 转 KiCad 库，与嘉立创无关。
- npm `easyeda`（tscircuit/easyeda-converter）、`easyeda-mcp-pro`、`easyeda-copilot-mcp`：社区项目。
- `zhoushoujianwork/easyeda-agent` 及其大量 fork：我们的上游，非嘉立创官方。

检索记录：WebSearch（“嘉立创EDA CLI 命令行 工程文件 AI 2026”“EasyEDA Pro CLI github generate project file”）、
`gh search repos easyeda|lceda --sort updated`、`gh api orgs/easyeda/repos`、`npm search lceda` / `npm search easyeda cli`、
`npm view @jlceda/pro-api-types`（0.4.26，2026-09-28）。PyPI 未发现官方包（只做了常识性排查，未系统检索）。

## 2. 能力分析（依据文档，均未实测）

### 2.1 命令面

```
easyeda-pro activate | open [--path <.eprj/.eprj2/.eprj3/.elib>] [--headless <bool>] | stop | doctor
easyeda-pro session list | close [--destroy] | abort
easyeda-pro invoke --session <id> [--code <js>] [--fn <name>] [--args <json>] [--ext-uuid <id>] [--timeout <ms ≤1800000>]
easyeda-pro functions
easyeda-pro doc api [--class-name X [--method-name Y]] | doc external | doc format [--class-name T]
easyeda-pro --search <kw> | --mcp stdio | --mcp http --port 3030 --token <t>
```

- **会话**：一个编辑器窗口 = 一个 session；`open` 返回 `sessionId`；`--headless true` 隐藏渲染。
  `doctor` 返回 `endpoint`（如 `EasyEDAProf126dbc1`）、`connected`、`bridgeVersion`、`versionMatch`。
  “同一时间只有一个 bridge 活动”。
- **执行**：`invoke --code` 是 async 函数体，参数经 `__CLI__.args` 注入；`--ext-uuid eda` 以内置扩展身份运行，
  获得读写本地文件等完整权限（文档要求“始终加上”）。`--fn` + `--ext-uuid <扩展>` 可以调用**某扩展注册的函数**，
  `functions` 列出会话已知扩展——这意味着第三方扩展可以暴露 CLI 可调用的函数（注册 API 文档未给出，未验证）。
- **响应**：统一 `{ok, value, logs, durationMs}` / `{ok:false, error:{code,message}}`。
- **API 文档**：`doc api` 渐进披露（类 → 方法 → 完整签名与可运行示例 → 枚举），`callPath` 给出 `eda.*` 属性名。
- **格式文档**：`doc format --class-name TMSchComponent` 等返回字段、类型、必填，宿主自带。
- **MCP**：stdio 给本地 Agent；http + token 给远程/浏览器 Agent。MCP 暴露的工具清单文档未列（未验证）。

### 2.2 文档演示的端到端能力（全部经 `invoke` 下发 JS）

建工程（`createProject(..., EDMT_ProjectFileFormat.EPRJ3)`）→ 器件搜索（`lib_Device.search`）→
放置（create → 读属性快照 → 一次 `modify(designator + otherProperty)`，与我们 2026-09-21 发现的 supplierId 保留补偿一致）
→ 网络标志 → 正交导线 → `sch_Drc.check` → 网表（Protel2）/ BOM（csv/xlsx）/ PNG → PCB：
板框 polyline、`importChanges()`、**`autoLayout()`、`autoRouting()`**、`pcb_Drc.check`、截图、保存。
文档还点名 Gerber、坐标文件、IPC-2581、ODB++、iBOM、STEP 等导出类。

值得注意：

- 文档里 `autoLayout()` / `autoRouting()` 直接返回成功统计，而我们 §8.1 实测 `autoRouting()` 曾被 `@alpha`
  “开发版本”门挡住。V4.1.60 客户端内是否已放开，**未验证**；即使可用，也只是宿主自带的通用自动布局/布线，
  没有我们的意图线宽、隔离/爬电、过孔载流、板边铜距等约束。
- 经验规则（等待 5–10 s、`save()` 后等 8–10 s 再 DRC、每批 20–25 件、管脚坐标必须回读）与我们 Skill 的
  “回读优先”原则一致，但它们是 Agent 自觉遵守的文字规程，不是代码门禁。

### 2.3 文件格式（`.eprj3`）知识

文档公开的要点（与 `easyeda-format-skill` 一致，可交叉验证）：

- folder 工程：`X.eprj3` 索引 + `sch/<原理图>/<页>.esch2` + `<原理图>.ecfg` / `.evar` + `pcb/<PCB>.epcb2` + `panel/<拼板>.epan2`；文件名即文档名。
- 行格式：`{"type":..,"ticket":N,"id":"<16 位小写 hex>"}||{payload}|` + **LF**。
- 一个 `.esch2` 是多文档序列：`SYMBOL… → FOOTPRINT… → DEVICE… → BLOB → SCH_PAGE`；器件库内嵌，无独立工程库。
- 写入三规则：`partId` 指向 SYMBOL 文档里的 `PART` 行；`id` 文档内唯一；`ticket` 文档内唯一且元件 ticket 小于其属性。
- PCB：`NET`、`COMPONENT`、复合 id 的 `ATTR`（`<cid>e15`）、`PAD_NET`（id 为数组字面量 `["PAD_NET",cid,padNum,padLocalId]`）、
  `POLY`（`layerId 11` 板框）、`LINE`（走线）；板模板记录（`LAYER` / `RULE` / `PREFERENCE` …）必须照抄编辑器生成值。
- 坐标：原理图 10 mil、左下原点、Y 向上，A4 = 1170 × 825；PCB 单位 mil；**原理图文件 `rotation = (360 − API 角) % 360`，PCB `angle` = API 角**。
  这与我们记录的“`createNetFlag` 存储旋转取反”现象可能同源，值得对照（未验证）。
- 双向同步：编辑器保存即落盘；直接改盘，编辑器“稍后”重载；对象数不对就重写一次。`PCB_Drc.check()` 同时比对原理图与 PCB 网表。

### 2.4 “数据库”

- 无独立器件数据库 API、无离线 dump、无 LCSC 价格/库存接口；仅编辑器内 `lib_Device.search()`、
  `getSystemLibraryUuid()`（示例系统库 UUID `0819f05c4eef4c71ace90d822a990e87`，与我们 `standard-parts.json` 国内版一致）。
- 半离线模式下“系统库完整可用”——是否意味着客户端本地缓存了整个系统库、是否需要登录，文档未说明（未验证）。
- 限流、数据再分发许可：文档未提及（未验证）。我们不应把检索结果批量缓存再分发。

## 3. 与 pcbpilot 对比

### 3.1 他们有、我们没有（或较弱）

| 能力 | 官方 CLI | pcbpilot 现状 |
|---|---|---|
| 进程内 bridge，无需导入扩展 / 打开“允许外部交互” | 客户端内置（未验证是否仍需该开关） | 需侧载 `.eext` + 用户手动开开关 + 刷新 |
| 由 CLI **启动/打开/关闭**编辑器窗口、headless 会话 | `open --headless`、`session close --destroy` | Agent 不启动宿主；窗口须用户已打开 |
| 打开本地 `.eprj3` 路径并双向同步 | `open --path` | 只有在线工程；`project export` / `export-source` 只导出 epro2 归档 |
| 宿主自带的 API / 格式 Schema 查询 | `doc api` / `doc format` | 依赖 `@jlceda/pro-api-types` 与离线 runtime probe |
| 文件直写 + 宿主重载作为批量写入路径 | 文档化 | 只走 typed action 逐对象写入 |
| 宿主自带 autoLayout / autoRouting | 文档示例可用（未验证） | 自研 `pkg/pcbauto`（我们更强，但它是宿主原生） |
| 官方背书的 MCP 入口 | 有 | 我们也有 MCP，但非官方 |

### 3.2 我们有、他们没有

- **typed action 目录 + Cobra 子命令 + 写前守卫 / Apply journal / 严格回读**；他们只有任意 JS（我们禁止以 `debug.exec_js` 绕过）。
- **离线布局布线引擎** `pkg/pcbauto`：意图驱动线宽/颈缩、隔离带与开槽、按电流定过孔与阵列、板边铜距、fixture bench。
- **仿真**：DC/IR `sim power`、ngspice `sim analog`、`sim post-layout` 热/IR、Elmer 交叉校核。
- **数据驱动原理图架构**（canonical 源数据、核心/外围所有权、`sch layout-lint`）、块库与引脚审计。
- **设计报告** `report design`、**美观度**指标、**流程契约测试** `TestFlowContract`。
- **版本门**（CLI/daemon/Skill/MCP/connector 同版）、**自更新**、审计成本台账、web console。
- **Web 版宿主与 V3 (3.2.x) 宿主**均可用；官方 CLI 只支持桌面版 V4.1.60+。
- LCSC C 号 → 器件身份解析、国际版 UUID 重定位、BOM C 号补全。

### 3.3 建议吸收（按 价值/成本 排序）

| # | 项 | 价值 | 成本 | 集成方式 |
|---|---|---|---|---|
| 1 | **把官方格式 Schema 作为离线 fixture 的权威对照** | 高 | 低 | 固定 `easyeda-format-skill` commit，取其 `schemas/` 与 `kicad-to-easyeda-eprj3/example/` 的真实 `.eprj3` 样例进 `internal/app/testdata/`（注意 MIT/Apache 署名）；新增只读 `pcbpilot project inspect-eprj3 --dir`（解析行格式、多文档序列、引用完整性、正交性、id/ticket 唯一），先只读、保留未知字段。纯离线，CI 可跑 |
| 2 | **在 health / Skill 中识别官方 CLI 存在与版本** | 中 | 低 | `pcbpilot health` 只读探测 PATH 上的 `easyeda-pro` / `lceda-pro` 并执行 `doctor`（只读），报告 `connected`/`bridgeVersion`；Skill 写明“官方 CLI 属于任意 JS 通道，不是 pcbpilot 写入路径” |
| 3 | **`doc format` / `doc api` 作为上游差异监测源** | 中 | 低 | 在 `make` 目标或脚本里（有 4.1.60+ 时）dump `doc api` / `doc format` JSON，与 `@jlceda/pro-api-types` diff，发现新增/变更 API（如 `autoRouting` 是否解锁）；结果只作线索，能力仍需 runtime probe |
| 4 | **第二传输通道：daemon ↔ 官方 bridge** | 高 | 中高 | 新 transport：daemon 以子进程 `easyeda-pro invoke --session S --ext-uuid <pcbpilot-uuid> --fn <typedAction> --args <json>` 调用**我们 connector 注册的函数**，而非下发任意 JS——保持 typed action 契约，省去 WebSocket 端口与“允许外部交互”（未验证）。前提：确认扩展注册 CLI 函数的 API、`--ext-uuid` 对侧载扩展是否可用、权限模型、Windows/macOS 可用性。先做 probe 与 ADR，不替换现有 WS |
| 5 | **`.eprj3` 输出适配器（离线引擎 → 落盘 → 宿主重载回读）** | 高 | 高 | 规划 `pcbpilot project export-eprj3 --from <canonical/plan> --skeleton <宿主建好的工程目录>`：库文档从宿主保存的文件摘取（照官方建议），只生成业务记录；写盘后仍须 typed fresh readback + DRC + `module-check` 才算完成。可服务 CI 回归与批量 fixture。注意：不能作为 connector 失败的兜底（现有项目准则），需用户决定是否开放桌面版本地工程宿主 |
| 6 | **headless 会话用于无人值守回归** | 中 | 中 | 有 4.1.60+ 桌面客户端的 CI 机上，用 `open --headless --path <fixture.eprj3>` 打开样例工程跑只读检查（DRC/网表/截图）。这与“Agent 不自行启动宿主”的现行准则冲突，须用户明确批准后在专用测试机上做 |
| 7 | 文档写法借鉴 | 低 | 低 | `cli-for-ai.md` 的“When to Use / 硬约束表 / 需向用户确认的信息 / 校验检查点”结构值得参照，精简我们 SKILL.md 的入口说明 |

不建议吸收：以任意 `invoke --code` 作为设计写入路径；以宿主 `autoLayout` / `autoRouting` 取代 `pcbauto`
（可作为对照基线，但缺少我们的安全/电气约束）。

### 3.4 风险

- **许可**：文档仓库许可声明与 LICENSE 文件不一致；CLI 本体闭源，随客户端协议。我们只能“调用”而不能分发它。
  `easyeda-format-skill`（MIT）/ `kicad-to-easyeda-eprj3`（Apache-2.0）可复用，需保留署名与 NOTICE。
- **稳定性**：发布仅 1 天、无版本号与 changelog；README 快速开始用 `session open` 且 `invoke` 无 `--session`，
  与正文 `open` + `--session` 不一致——命令面可能仍在变化。格式方面 V3→V4 已有 docType、单位（PCB 改 mil）、
  布尔/颜色编码等破坏性变化（见 `FORMATLOG.md`），离线生成必须按宿主版本分支。
- **平台**：仅桌面客户端 V4.1.60+；文档基于 Windows，macOS/Linux 未说明；不支持 Web 版。我们的用户相当一部分用 Web 版或 V3。
- **云账号 / 联网**：文档未说明 CLI、系统库检索是否需要登录；半离线模式可用系统库，全离线模式下库可用性未写（未验证）。
- **ToS**：批量调用器件检索、缓存或再分发库数据的条款未查到（未验证），应限于用户本人设计用途。
- **版本耦合**：bridge 单实例、`versionMatch` 字段暗示 CLI 与编辑器版本需匹配；我们若接入第二通道，
  需把它纳入 `health` 与版本门。
- **安全**：`--ext-uuid eda` 授予本地文件读写全权限；`--mcp http` 暴露在网络端口（有 token）。我们不应默认启用或推荐 http 模式。
- **与项目准则的冲突**：直写文件、Agent 启动/关闭宿主、headless 均触及 CLAUDE.md 中“Agent 不自行启动或切换宿主”
  “不得以离线文件路线作兜底”的规定；任何吸收都须先由用户决定准则变化。

## 4. 下一步建议（不在本次范围内执行）

1. 用户若在一台机器上装有 V4.1.60+ 桌面客户端，先做只读 probe：`--help`、`doctor`、`functions`、`doc format`
   列表、`open --headless` 是否需要登录，结果写回本文并把状态从 `source-only` 提升。
2. 实施吸收项 1（只读 `.eprj3` 解析 + 官方 Schema fixture），纯离线、风险最低。
3. 为吸收项 4 写 ADR：评估“扩展注册函数 + 官方 bridge”能否承载 typed action 契约。

## 来源（访问日期 2026-10-01）

- 官方 CLI 文档仓库：https://github.com/easyeda/easyeda-client-cli （HEAD `699e686`）
- 官方文档页：https://prodocs.easyeda.com/en/api/guide/cli.html ；AI 版：https://prodocs.easyeda.com/storage/texts/api/guide/cli-for-ai.md
- 国内文档页 https://prodocs.lceda.cn/cn/api/guide/cli.html （本机访问超时，未核对）
- https://github.com/easyeda/easyeda-eprj3-skill （`40c94e1`）· https://github.com/easyeda/easyeda-format-skill · https://github.com/easyeda/kicad-to-easyeda-eprj3 · https://github.com/easyeda/easyeda-viewer · https://github.com/easyeda/easyeda-api-skill （1.1.42）· https://github.com/easyeda/eext-batch-data-export
- 非官方对照：https://github.com/l3wi/jlc-cli · npm `@jlcpcb/core` 0.5.0
- 本仓库：[ecosystem-survey.md §12](../ecosystem-survey.md#12-官方-eprj3-skill离线工程生成路线2026-09-21源码与离线实测)、[concepts.md eprj3 边界](../concepts.md#eprj3-文件生成与校验边界)、[2026-09-21-project-transfer.md](2026-09-21-project-transfer.md)
