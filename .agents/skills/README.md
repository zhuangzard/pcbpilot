# Skills 的规范源与维护约定

本目录是全部 Skill 的唯一源码位置。`.claude` 通过相对链接共享 `.agents`，根目录不再保留
`skills/` 副本或兼容链接。

| Skill | 任务 | 分发方式 |
|---|---|---|
| [pcbpilot](pcbpilot/SKILL.md) | 参数化电路设计、typed 执行与回读；公开 Skill 的路由入口 | 公开（router）；Release、ClawHub、SkillHub |
| [pcbpilot-repo-lookup](pcbpilot-repo-lookup/SKILL.md) | 跨项目只读查询实现、样例、证据 | 链接源码 checkout |
| [pcbpilot-repo-maintain](pcbpilot-repo-maintain/SKILL.md) | 修改工具、知识、验证与 Git 贡献 | 链接源码 checkout |

## 公开 Skill 清单

[`public-skills.json`](public-skills.json) 是公开 Skill 的唯一清单：`router` 固定为 `pcbpilot`，
`skills` 列出随发布安装的全部公开 Skill（router 加若干聚焦 Skill，名称一律 `pcbpilot-<slug>`），
`retired` 列出以前发布过、现在要从安装目录移除的名字。新增、改名或下线公开 Skill 时同时改清单与
`internal/selfupdate/skills.go` 的 `publicSkills`（测试保证两者一致），并保持清单的规范格式
（键排序、两空格缩进、每个名字一行；`install.sh` 按行解析）。

- `pcbpilot` 是路由 Skill：保留完整设计工作流入口，按任务把 Agent 引到聚焦 Skill；共享参考、
  数据资产和脚本放在 `pcbpilot/`。
- 聚焦 Skill 只维护自己独有的 reference，共享事实链接到 `pcbpilot/` 内的文件，不复制。
  跨 Skill 的本地链接只允许“聚焦 Skill → `pcbpilot/`”，且两端都在清单中；`pcbpilot/` 不链接
  聚焦 Skill，聚焦 Skill 之间也不互链。
- 全部公开 Skill 共用一个版本：`scripts/sync-skill-version.py` 一次写入清单中每个 `SKILL.md`。
- 内容不重复：`pack-skill.py --check` 会拒绝在两个 Skill 中出现的长段落（≥200 字符，空白归一后比较）。
- 下线的 Skill 只从 pcbpilot 写入的目录移除（真实目录且带 `.version` 标记）；软链接和用户自建的
  同名目录保持不动，`PCBPILOT_SKILL_PRESERVE=1` 时全部保留。

## 安装与打包

发布版仍通过根目录 `install.sh` / `install.ps1` 或 `pcbpilot update` 安装 CLI 与清单中的全部公开
Skill：每个客户端 × 每个 Skill 各自先暂存再整体替换。已装 router 的客户端会自动补齐新增的聚焦
Skill；`PCBPILOT_INSTALL_SKILLS` 选择客户端，`PCBPILOT_SKILL_PRESERVE` 对每个 Skill 生效。
源码链接使用 `python3 scripts/install-agent-skills.py`，默认只安装仓库协作入口；
`--scope design` 选择清单中的公开 Skill，`--scope all` 选择全部，`--dry-run` 预览。
目标选择、旧链接迁移和冲突保护见 [Agent 协作设计](../../docs/agent-collaboration.md)。

`pack-skill.py` 只打包清单中各 Skill 内已跟踪或已暂存的文件：`skills.tar.gz` 根目录是每个
Skill 一个目录加一份规范格式的 `public-skills.json`。本 README、协作 Skill、memory 和本机状态不进入
公开包；公开包中的相对链接必须在解包后成立。已跟踪却不在清单（也不是 `pcbpilot-repo-*`）的 Skill、
以及仍被跟踪的已下线 Skill 都会让检查失败。发布授权与版本准备见 [发布流程](../../docs/release-workflow.md)。

## 编写与维护

修改前按 [Skill 设计](../../docs/skill-design.md) 确定工作流与知识归属。

- 保留实测硬知识；精简只做去重、下沉或结构化。确认同一事实仍有明确维护位置后才能移除旧文。
- 信息只有一个家：入口保留任务路由与关键约束，正文放到按需读取的 reference；不要重复规则表。
- 改 CLI、daemon、typed action、connector 或块库时，同步公开 Skill 的命令签名、例子和限制。
- `pcbpilot` 入口保持完整设计工作流与任务路由；聚焦 Skill 只承接一个职责（评审、制造输出、
  库、KiCad 原理图、KiCad PCB、仿真报告等），不另起一套工作流，也不复制 router 已有的规则。
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
