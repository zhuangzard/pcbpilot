# 离线格式读取与上游 API 监测

两条离线命令，均不需要 daemon、connector 或编辑器，也都**不写工程**：

| 目的 | 命令 |
|---|---|
| 读 V4 本地 `.eprj3` 工程/文档：清单、计数、单位、版本标记、Schema 发现 | `pcbpilot project inspect-eprj3 <工程目录 \| X.eprj3 \| 文档文件> [--json]` |
| 比较我们锁定的 `eda.*` API 与更新的上游来源 | `pcbpilot api upstream-diff <来源> [--json]` 或 `--fetch` |

官方客户端 CLI 是否存在见 `pcbpilot health` 的 `officialCli`（[environment-setup.md](environment-setup.md#官方客户端-cli-探测healthofficialcli只读)）。

## `project inspect-eprj3`

输入为工程根目录（含唯一 `*.eprj3`）、`.eprj3` 索引文件，或单个
`.esch2` / `.epcb2` / `.epan2` / `.ecfg` / `.evar`。

报告内容：

- **清单**：索引 `profile` 的 boards / schematics / sheets / pcbs / panels，与磁盘文件互相对照
  （`missing-document`、`unlisted-document`、`uuid-mismatch`）。
- **计数**（每个主文档，按最终一致性取胜者、删除行不计）：`components`、`wires`、`nets`、
  `tracks`（铜层 LINE/ARC：layerId 1、2、15–46）、`vias`、`pours`。原理图 `components` 含图框
  与网络标记等 COMPONENT 行；原理图 `nets` = 非空 `NET` / `Global Net Name` 属性值去重；
  PCB `nets` = `NET` 行（id 为 `["NET",名]`）。`deletedRecords`、`supersededRecords` 说明日志里
  有多少行被删除或被更大 ticket 覆盖。
- **坐标约定**：原理图 10 mil、图纸左下原点、Y 向上，文件 `rotation = (360 − API 角) % 360`；
  PCB 单位 mil、`angle` = API 角；同时给出文件自身的 CANVAS 显示单位和 `yAxisDirection` 标记。
- **版本标记**：DOCHEAD `editVersion`（如 `4.1.36`）及 V3/V4 证据；两代标记同时出现报 `mixed-generation`。
- **Schema 发现**：按官方 `easyeda-format-skill` JSON Schema（MIT，内嵌子集）校验，按
  （docType、记录类型、路径、规则）聚合计数。它们是**参考性**的：官方 Schema 描述“生成器应写出的”
  形态，编辑器真实写出的文件（实测 4.1.36）会省略 `groupId`/`locked`/`strikeout`、把布尔写成 null。
  Schema 自身注明的偏差（未分组的 `groupId` 写成数字 0）单列为 `documentedDeviations`。
- **结构发现**：`malformed-line`（带文件:行号）、`empty-file`、`index-not-json` 等为 error，
  退出码非 0；`dangling-partId`、`dangling-parentId`、`dangling-pad-net`、`ticket-collision`、
  `crlf` 为 warning，不改变退出码。

边界：

- 只读工具，不是写入路径。生成或改写 `.eprj3` 再让编辑器重载，不是 pcbpilot 接受的工作流
  （见 SKILL.md“硬红线”：工程写入只来自 typed action / Cobra 子命令 / `pcbpilot apply`）；需要写入仍走 typed action。
- 计数是文件里的事实，不等于编辑器回读；现场验收仍以 typed readback、DRC 为准。
- 未覆盖的记录类型（LAYER、RULE、PRIMITIVE…）列在 `schema.unvalidatedRecordTypes`，不算“已通过”。

### 回归样例

| 样例 | 来源 / 许可 | 状态 |
|---|---|---|
| `internal/eprj3/testdata/upstream-example/` | `easyeda/kicad-to-easyeda-eprj3@a16545d` 的编辑器写出样例（Apache-2.0，原样保留，LICENSE 同目录） | 解析干净：P1/P2 各 6 组件 3 导线 2 网；PCB1 6 组件 4 网 18 铜线 1 铺铜；editVersion 4.1.36 |
| `internal/eprj3/testdata/synthetic-minimal/` | 按官方文档结构手写的最小工程（本仓库，MIT） | 含过孔、内层走线、丝印线（不计）、删除行与覆盖行；Schema 0 违规 |

## V3 → V4 格式差异（已知）

依据 `easyeda-format-skill` FORMATLOG.md（9e42727）与官方 CLI 文档（699e686），`inspect-eprj3 --json`
的 `conventions` 字段带同一张表与来源。

| 主题 | V4（eprj3 / 4.1.x） | V3 |
|---|---|---|
| 单位 | 原理图 10 mil（0.01 inch）；**PCB 改为 mil**，网格/栅格写盘 ×10；CANVAS `unit` 仅显示单位且只收小写 `mm`/`mil` | 全局 0.01 inch |
| docType | 20 种：`CONFIG`（原 `PROJECT_CONFIG`）、新增 `EDIT_HEAD`、`PANEL_LIB`、`FONT`、`VARIANT`、`COMPONENT_GROUP(_DATA)`、`SIMULATION`、`SIMULATION_SCH` | 11 种 |
| DOCHEAD | `docType/uuid/client`（16 位小写十六进制）+ `updateTime`、`version`；编辑器另写 `editVersion` | 仅 `docType/uuid/client` |
| 编码 | UTF-8 文本，一行一记录 `{外层}||{载荷}|` + LF（编辑器最后一行省略结尾 `|`）；中文 id/标题常见（如 PART `电阻.1`） | 同框架 |
| 布尔 / 颜色 | JSON `true/false`；原理图颜色可为 `null`（主题默认）；PCB 颜色还可 `rgb()`/`cmyk()`/`data:`/`blob:` | `1/0`；`#RRGGBB` 或 `""` |
| 平票 | ticket 大者胜，相同则 `client` 字典序**大者**胜 | 规范写“小者保留”（与实现相反） |
| 圆弧 | 单一 `ARC` + `arcType` DOT/CENT | `ARC` 与 `CARC` 两种 |
| Y 轴标记 | 本地文件可带 `yAxisDirection` up/down（缺省 ≡ down）；4.1.36 样例未带 | 无 |
| 工程形态 | 文件夹工程：`X.eprj3` + `sch/<原理图>/<页>.esch2` + `pcb/*.epcb2` + `panel/*.epan2`，库文档内嵌在主文档之前 | 单文件 `.eprj/.eprj2`（SQLite） |

## `api upstream-diff`

基线（pinned）是内嵌的 `internal/apidoc/api-index.json`，由 `gen.py` 从
`extension/package-lock.json` 锁定的 `@jlceda/pro-api-types` 生成（单测保证两者版本一致）。
较新来源任选其一：

- npm 包 `.tgz` / `.tar.gz`、`index.d.ts` 或解包目录；
- `gen.py` 生成的 api-index JSON，或保存的官方 `easyeda-pro doc api` 导出
  （`{"classes":[{callPath, methods:[{name,comment}]}]}`；只有方法名，不比较签名/稳定性）；
- `--fetch`：从 npm registry 下载 `@jlceda/pro-api-types@latest`（唯一联网路径，`--timeout` 默认 20 s）。

输出：新增/删除的类与方法、签名变化（`signature`；仅写法不同如 `{ [key: string]: T }` ↔
`Record<string, T>`、`Array<T>` ↔ `T[]` 归为 `signature-cosmetic`）、稳定性变化（alpha/beta/stable）。
每项标注 `connectorUses`（connector 源码直接引用，构建时静态扫描嵌入）、`namespaceInUse`、
`notable`（关注清单：`pcb_Document.autoRouting/autoLayout`、`sch_PrimitiveBus.create/getAll`、
`sch_PrimitiveAttribute.createNetLabel`、`sys_FileSystem.getProjectsPaths`）。
`breakingForConnector` = 我们用到且被删除或真实改签名的方法；`--fail-on-breaking` 时退出码 3。

2026-10-01 实测 `--fetch`：0.4.25 → 0.4.26 新增 4 个类（`pcb_ImageTool`、`pcb_Tool`、`sys_ExternalApi`、
`sys_Help`）、36 个方法；`pcb_Document.autoLayout` alpha → beta；connector 用到的 10 个方法真实改签名
（如多个 `delete` 返回值增加 `undefined`、`createProject` 增加 `fileFormat`）。

规则：类型声明只是线索。新方法出现或稳定性提升，不代表某个宿主可调用；先用
`pcbpilot api probe --path <ns.method>` 在目标宿主实测，再按 typed action → Cobra 流程接入。
宿主自带 `autoRouting/autoLayout` 只能作为对照基线，不能取代 `pcb auto` 的电气约束。
