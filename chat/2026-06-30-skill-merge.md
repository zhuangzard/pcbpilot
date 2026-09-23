# 2026-06-30 Skill 合并决策

## 背景

原先技能按职责拆成 4 个目录：

- `easyeda-design-flow`
- `easyeda-schematic`
- `easyeda-pcb`
- `easyeda-conventions`

这种拆法对仓库维护清晰，但对外发布和安装不友好：用户必须理解多个技能的依赖关系，Agent 触发时也可能只命中其中一块，导致整板任务缺少 flow/conventions 上下文。

## 决策

对外只保留一个技能：`pcbpilot`。

名字加 `-agent` 后缀，原因是：

- 避免看起来像官方 EasyEDA 技能或官方插件。
- 和仓库、CLI、daemon、connector 的品牌一致。
- 用户安装命令更简单，只有一个入口。

旧拆分目录已经合并进：

```text
.agents/skills/pcbpilot/
  SKILL.md
  agents/openai.yaml
  references/
  scripts/
```

## 合并后的结构

- `SKILL.md`：短入口，负责触发、工作流门禁、按任务路由 reference。
- `references/design-flow.md`：整板分阶段流程。
- `references/schematic.md`：原理图操作技能内容。
- `references/pcb.md`：PCB 操作技能内容。
- `references/*-conventions.md`：原理图/PCB 约定与 SOP。
- `references/orientation.json`：netflag/netport 朝向真值。
- `references/standard-parts.json`：标准器件库。
- `scripts/`：lint、BOM 补全、part 写回、选型、校准工具。

## 发布与安装

ClawHub 已发布：

```bash
clawhub install pcbpilot
```

国内 SkillHub 安装命令注明为：

```bash
skillhub install pcbpilot --registry https://skillhub.cn
```

国内发布仍需要先登录 `skillhub.cn`：

```bash
skillhub login --registry https://skillhub.cn
skillhub publish .agents/skills/pcbpilot --registry https://skillhub.cn --visibility public
```

## 后续约束

- release 只打包 `.agents/skills/pcbpilot`。
- 新文档、新脚本、新规则都落在 `pcbpilot` 下。
- 不再恢复旧拆分技能目录，除非将来有明确的插件化安装机制支持依赖技能。
