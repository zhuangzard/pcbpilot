# 文档导航与信息归属

按当前问题选择入口，不需要先读全部文档。操作电路使用公开设计 Skill；查询或维护源码可以
使用[仓库协作 Skill](agent-collaboration.md)。规范、实现与历史报告发生差异时，应分别报告
差异和版本，不能用旧验收记录覆盖当前契约。

## 按任务阅读

| 当前任务 | 先读 | 需要更多信息时 |
|---|---|---|
| 完整使用说明（安装 → 设计流程 → 排障） | [manual.md](manual.md) | [quick-start.md](quick-start.md) |
| 新机器安装（含 MCP）、连接和升级 | [quick-start.md](quick-start.md)、`scripts/setup-agent.sh` | [dev-environment.md](dev-environment.md) |
| 让 Agent 设计或修复电路 | [SKILL.md](../.agents/skills/pcbpilot/SKILL.md) | 该入口的任务路由与参考 |
| 找到可改参数的相近案例 | [样例索引](../.agents/skills/pcbpilot/references/examples/index.md) | 具体样例的来源、参数及回读 |
| 确认能做什么、不能做什么 | [FEATURES.md](FEATURES.md) | [cli/README.md](cli/README.md) |
| 理解概念与职责 | [concepts.md](concepts.md)、[architecture.md](architecture.md) | [schematic-connectivity-model.md](schematic-connectivity-model.md) |
| 整板自动设计引擎的算法、证据与路线图 | [pcbauto.md](pcbauto.md) | Skill 操作页 [pcb-auto.md](../.agents/skills/pcbpilot/references/pcb-auto.md) |
| MCP 是什么、怎么接、与 Skill/CLI 的关系 | [mcp.md](mcp.md) | [mcp/README.md](../mcp/README.md) |
| 与原版 easyeda-agent 的关系、并装约定 | [ADR 0002](adr/0002-fork-as-pcbpilot.md) | [NOTICE](../NOTICE) |
| 新增或修复工具能力 | [cli-design.md](cli-design.md)、[protocol.md](protocol.md) | [connector-contract.md](connector-contract.md)、[ecosystem-survey.md](ecosystem-survey.md) |
| 维护 Skill 和知识 | [skill-design.md](skill-design.md)、[编写约定](../.agents/skills/README.md) | [Agent 协作设计](agent-collaboration.md) |
| 准备已获批准的版本发布 | [release-workflow.md](release-workflow.md) | [仓库发布授权规则](../AGENTS.md) |
| 验证从需求到成品 | [e2e-automation-acceptance.md](e2e-automation-acceptance.md) | [原始回归需求](../esp32MiniRequire.md)第一节、[仓库规则](../AGENTS.md) |
| 查历史检查结论 | [历史证据索引](reviews/README.md) 与对应报告 | 回到该记录的版本、输入、原始证据和未覆盖项 |

## 信息只有一个维护位置

| 信息 | 维护位置 | 其他入口怎么引用 |
|---|---|---|
| 仓库协作、分支和发布约束 | 根目录 [AGENTS.md](../AGENTS.md) | `CLAUDE.md` 软链接，不复制正文 |
| 仓库查询和维护流程 | `.agents/skills/pcbpilot-repo-*/` | `.claude` 和用户级安装链接共享源目录 |
| 设计工作流、命令、操作知识 | `.agents/skills/pcbpilot/` | README / 文档索引仅链接；发布包内引用自包含 |
| 新概念、架构理由与能力边界 | 本目录对应主题页 | 在 Skill 的相关决策点引用已纳入包内的操作知识 |
| 可重用设计输入、参数和步骤 | 公开 Skill 的 `references/examples/` | 通过样例索引发现，保留状态与证据 |
| 某次检查、复现、失败与局限 | `docs/reviews/` 或对应历史报告 | 当前能力页引用结论，不复制整份日志 |
| 个人运行状态和原始私有材料 | 仓库外或本地忽略目录 | 只回填脱敏且可迁移的结论 |

新增文档先判断它属于操作知识、设计理由还是一次性证据；更新已有主题通常比再写一份综合
指南更容易保持一致。增加入口时补本索引；公开 Skill 内链接仍须通过 `make skill-check`，
不能依赖只在源码仓库存在的文件。

## 历史资料与清理

`reviews/` 保存带日期或版本的实测、回归与失败证据，`releases/` 保存版本说明。
这些材料说明当时发生过什么；当前命令与架构分别以 CLI 索引、概念表、架构和公开 Skill 为准。
旧 Phase 1/2 规划、2026-08 路线图、三层布局蓝图、命令收敛提案及未立项外壳占位已移除；
仍有价值的审计数据、gate 现场结果和移动安全经验已归入对应证据页或现行命令说明。
需要追溯被删的旧想法时使用 Git 历史，不在当前导航中保留失效操作指南。
