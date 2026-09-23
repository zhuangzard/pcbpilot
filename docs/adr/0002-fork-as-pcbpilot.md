# ADR 0002：从 easyeda-agent 分叉为独立产品 pcbpilot

- 状态：已采纳（2026-09-22）
- 背景记录：[ADR 0001](0001-project-name-and-shape.md)（原项目命名，保留原文）

## 背景

本仓库起源于 [zhoushoujianwork/easyeda-agent](https://github.com/zhoushoujianwork/easyeda-agent)
（MIT）。在其上新增了整板自动设计引擎 `pkg/pcbauto` 之后，需要：

1. 多台机器长期使用自己的版本，自更新不能把它换回上游；
2. 与原版 easyeda-agent 在同一台机器上**同时安装、同时运行**，互不干扰；
3. 以后按自己的节奏逐步升级核心能力。

原版的自更新写死上游仓库，正式版 daemon 启动时会把 Skill 同步为上游同版本内容并提示升级；
插件、端口、命令名、Skill 名和配置目录也都与原版相同。只换更新地址无法满足第 2 条。

## 决策

改名为 **pcbpilot**，成为独立分发的产品，并完整保留原作者署名与 git 历史：

| 身份 | 原版 | pcbpilot |
|---|---|---|
| Go 模块 | github.com/zhoushoujianwork/easyeda-agent | github.com/zhuangzard/pcbpilot |
| CLI | `easyeda` | `pcbpilot` |
| Skill | easyeda-agent、easyeda-repo-* | pcbpilot、pcbpilot-repo-* |
| 状态目录 | ~/.easyeda-agent、./.easyeda | ~/.pcbpilot、./.pcbpilot |
| 环境变量前缀 | `EASYEDA_` | `PCBPILOT_` |
| daemon/连接器端口段 | 60832–60841（0xEDA0–0xEDA9） | 61832–61841（0xF188–0xF191） |
| daemon 服务身份 | easyeda-agent | pcbpilot |
| 连接器 | EDA Agent Connector，uuid e60502e7… | PCB Pilot Connector，新 uuid，仅 GitHub Release 侧载 |
| 发布渠道 | zhoushoujianwork/easyeda-agent | zhuangzard/pcbpilot（`RELEASE_REPO`，编译期注入） |
| 公共 Skill 市场 | ClawHub / SkillHub | 默认不发布（`PUBLISH_HUBS=1` 才发布） |
| 版本线 | 1.x | 从 0.1.0 开始 |

EasyEDA 自己的名字（EasyEDA、easyeda.com、`easyeda/pro-api-sdk`、`easyeda-pcb-router`、
`easyeda-api-skill`、`easyedaVersion` 字段）不改。历史记录（`docs/reviews`、`docs/releases`、
`docs/adr`、连接器 CHANGELOG）保留原文，只更新失效的路径与链接。

## 后果

- 两套可在同一台机器、同一个 EasyEDA 里并存：各自的连接器只扫描各自的端口段并校验服务身份。
  2026-09-22 本机实测：`easyeda` v1.5.1 daemon 在 60832、`pcbpilot` daemon 在 61832 同时运行，
  两个 CLI 的 `health` 各自只找到自己的 daemon。
- 上游的后续改进不再能直接 `git merge`（模块路径不同）；需要时从 `upstream` 远端按提交挑选并
  手工迁移导入路径。`upstream` 远端设为 `--no-tags`，避免上游 1.x 标签混入本仓库版本线。
- 连接器必须从 pcbpilot 的 Release 侧载；插件市场上的 “EDA Agent Connector” 属于原版。
- 发布仍需明确批准具体版本；`make release` 默认发往 `RELEASE_REPO`，不触及原项目的任何公开条目。
