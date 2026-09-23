# Skills 的规范源与维护约定

本目录是全部 Skill 的唯一源码位置。`.claude` 通过相对链接共享 `.agents`，根目录不再保留
`skills/` 副本或兼容链接。

| Skill | 任务 | 分发方式 |
|---|---|---|
| [pcbpilot](pcbpilot/SKILL.md) | 参数化电路设计、typed 执行与回读 | 唯一公开设计包；Release、ClawHub、SkillHub |
| [pcbpilot-repo-lookup](pcbpilot-repo-lookup/SKILL.md) | 跨项目只读查询实现、样例、证据 | 链接源码 checkout |
| [pcbpilot-repo-maintain](pcbpilot-repo-maintain/SKILL.md) | 修改工具、知识、验证与 Git 贡献 | 链接源码 checkout |

## 安装与打包

发布版仍通过根目录 `install.sh` 或 `pcbpilot update` 安装 CLI 与公开设计 Skill。
源码链接使用 `python3 scripts/install-agent-skills.py`，默认只安装仓库协作入口；
`--scope design` 选择公开设计 Skill，`--scope all` 选择全部，`--dry-run` 预览。
目标选择、旧链接迁移和冲突保护见 [Agent 协作设计](../../docs/agent-collaboration.md)。

`pack-skill.py` 只打包本目录 `pcbpilot/` 内已跟踪或已暂存的文件，压缩包根仍是
`pcbpilot/`。本 README、协作 Skill、memory 和本机状态不进入公开包；公开包中的
相对链接必须在独立解包后成立。发布授权与版本准备见 [发布流程](../../docs/release-workflow.md)。

## 编写与维护

修改前按 [Skill 设计](../../docs/skill-design.md) 确定工作流与知识归属。

- 保留实测硬知识；精简只做去重、下沉或结构化。确认同一事实仍有明确维护位置后才能移除旧文。
- 信息只有一个家：入口保留任务路由与关键约束，正文放到按需读取的 reference；不要重复规则表。
- 改 CLI、daemon、typed action、connector 或块库时，同步公开 Skill 的命令签名、例子和限制。
- 公共入口保持完整设计工作流，不重新拆成原理图、PCB、规则等多个互相漂移的公开 Skill。
- `SKILL.md` 保留合法 frontmatter 与已有 `metadata.version`，版本行维持两空格缩进。
  name 使用稳定英文 slug；description 清晰区分触发任务，正文语言保持自洽。
- references 给出明确加载场景；长文提供目录。安装/开发历史不塞进设计入口。
- 核心电路块先查 `pcbpilot blocks ls/show/search`；它们来自 CLI 内嵌数据。
  `library/modules/` 是另一类可复用模块资产，区别见 [概念表](../../docs/concepts.md)。
- 不提交 `__pycache__`、编译产物、凭据、原始私有日志或机器绝对路径。

## 验证

- 协作 Skill、安装与目录迁移：`make agent-check`。
- 公开包与样例：`make skill-check`；frontmatter 使用 `skills-ref validate`。
- lint 规则、模块与块库：按变更运行 `make lint-test`、`make modules-audit`、`go test ./internal/blocks/`。
- 打包、安装和脚本运行：`make release-script-test`。
- 现场回归按 [AGENTS.md](../../AGENTS.md) 的触发条件，只以客户原始需求为输入。
  包结构、离线测试与现场验证分别报告，不互相替代。
