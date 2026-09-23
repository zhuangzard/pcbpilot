# Agent 与文档协作设计

本仓库让设计使用者、跨项目知识查询者和工具维护者各有入口，并让不同 Agent 客户端读取
同一份规则。术语见 [concepts.md](concepts.md#agent-协作入口与文档归属)，文档归属见
[README.md](README.md)。

## 参考项目的巧妙之处

参考 [DIY Electronics with AI](https://github.com/zhoushoujianwork/diy-electronics-with-ai/tree/7c8c33305b9800eb7c00479b44199b35cb219bf8)
在该 commit 的 `AGENTS.md`、`docs/AI_COLLABORATION.md`、`docs/PROJECT_STRUCTURE.md`、
`docs/AGENT_SKILL.md`、`.agents/skills/` 与安装脚本；这里分析其结构，未执行其中的任务指令。

| 设计 | 为什么有效 | 本项目采用方式 |
|---|---|---|
| `.agents` / `AGENTS.md` 规范源，Claude 入口使用链接 | 客户端更换不会产生两份逐渐漂移的规则 | 采用相对链接；替换旧的重复 `CLAUDE.md` 与机器绝对路径 |
| 查询、目录研究、项目工程、Git 贡献分别触发 | Agent 只加载当前职责，查询不会自然演变为写库和发布 | 保留完整设计 Skill，新增只读查询与仓库维护两个入口 |
| README 导航、协作规则、目录契约、项目证据各有位置 | 使用者先找到入口，维护者知道新知识该放哪里 | `docs/README.md` 提供任务路由和唯一归属表 |
| 安装链接指向 checkout，Skill 自己解析仓库根目录 | 从其他项目复用知识，改进可回到同一可审查 Git 仓库 | 用户级链接安装与可迁移根目录解析，不依赖调用目录 |
| 事实、验证等级和私有数据边界明确 | 编译结果不会冒充实机支持，知识共享不夹带个人库存 | 沿用现有样例状态和 typed 回读契约，贡献时只回填公开或脱敏结论 |

这里的价值来自职责与证据可组合，不要求启动多个 Agent。参考项目的硬件目录分类和库存
功能适合实验室，本项目已有 typed CLI、库与样例，不引入第二套板卡目录或个人物料数据库。
设计工作流仍保持一个公开入口，避免拆分原理图、PCB 和规则后再次发生知识漂移。
合并旧规则时保留了布局工作流记忆，并把原 `CLAUDE.md` 中经 Makefile 核实的发布知识迁到
[release-workflow.md](release-workflow.md)，避免用软链接替换文件时丢失有效经验。

## 入口与源目录

```text
AGENTS.md                         仓库规则规范源
CLAUDE.md -> AGENTS.md             兼容入口
.agents/
  memory/                         已有共享经验，保持原记录
  skills/
    README.md                     Skill 维护与分发约定
    pcbpilot/                唯一公开设计包（真实目录）
    pcbpilot-repo-lookup/           查询实现、样例、证据
    pcbpilot-repo-maintain/         修改工具、知识、验证与 Git 贡献
.claude -> .agents                同一套仓库资源
docs/README.md                    文档导航与归属
```

公开设计包继续由 `install.sh`、`pcbpilot update` 和正式 release 管理；仓库协作 Skill 依赖
源码 checkout，只通过下面的开发安装脚本链接。两条分发路径不会相互覆盖。已有 `.claude`
目录迁移到 `.agents`，包括本地未提交状态；Git 只收录已审查的共享文件，worktree 和本机设置
保持忽略。

## 从其他项目调用

在要共享的 pcbpilot checkout 中运行（需要 Python 3 和文件系统软链接支持）：

```bash
python3 scripts/install-agent-skills.py --dry-run
python3 scripts/install-agent-skills.py
# 使用当前 checkout 的公开设计 Skill，或安装全部源码 Skill：
python3 scripts/install-agent-skills.py --scope design
python3 scripts/install-agent-skills.py --scope all --target all
# 客户端未扫描 ~/.agents/skills 时，才安装兼容入口：
python3 scripts/install-agent-skills.py --target codex
python3 scripts/install-agent-skills.py --target claude
python3 scripts/install-agent-skills.py --target all
# 自定义发现目录：
python3 scripts/install-agent-skills.py --skills-dir /path/to/skills
```

默认目标是 `~/.agents/skills`，遵守 `AGENTS_HOME`；Codex 和 Claude 目标分别遵守
`CODEX_HOME` / `CLAUDE_HOME`。默认 `--scope repo` 安装仓库协作 Skill；`--scope design`
选择公开设计 Skill，`--scope all` 选择全部真实 Skill 目录。所有目标先预检，同源链接可重复
安装。唯一自动修复的旧链接是指向同一 checkout 原 `skills/pcbpilot` 目录的链接，
即使旧源已随迁移消失也可识别；真实发布版目录、其他断链和其他 checkout 的链接均保留并报错。
`--dry-run` 不创建目录、不改链接。迁移中失败会恢复本次替换的旧链接。

源 checkout 需要持续存在。移动仓库后旧用户级链接会失效：确认旧目标属于自己后，手动移除
对应链接并从新位置重装；安装器不猜测或覆盖其他 checkout。Windows 需要启用软链接支持
（如开发者模式及 Git `core.symlinks=true`）；未启用时使用规范入口，不复制一份规则冒充同步。

重新加载客户端后，输入 `$pcbpilot-repo-lookup` 或 `$pcbpilot-repo-maintain` 选择入口，
也可由客户端按任务自动匹配；`@` 通常用于选择文件，不是显式调用 Skill。

例如，在另一个电子项目里请求：

- `使用 $pcbpilot-repo-lookup 查找 AMS1117 的参数化布局样例，列出实测证据和未验证项。`
- `使用 $pcbpilot-repo-maintain 把本次可复用的工具修复回填源码仓库，验证后按当前授权提交。`
- `使用 $pcbpilot 按当前项目需求修改原理图，并回读连接和保存结果。`

## 验证与维护

`make agent-check` 验证真实文件与兼容链接、根目录定位、安装预检、幂等性和路径迁移，
并核对新文档入口链接。CI 同样运行它。它证明仓库协作机制可用，不证明 EDA 现场设计通过。
公开 Skill 仍使用 `make skill-check`；源码移动后同步更新资源查找、打包与安装测试，不改变
EDA 设计动作。发布包只含 `pcbpilot/`，不把整个 `.agents` 目录打包。
新增协作 Skill 放入 `.agents/skills/pcbpilot-repo-<职责>/`，写清触发范围与相应归属，安装器
自动发现；不要在 `.claude` 再放副本，也不要把仓库依赖打入公开 Skill 包。
