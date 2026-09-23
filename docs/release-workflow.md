# 发布准备与分发契约

合并原 `CLAUDE.md` 中仍有效的发布知识，按当前 Makefile 核对。发布授权与版本选择遵守
[AGENTS.md](../AGENTS.md)；普通推送不会触发正式发版。

## Version and retention policy

- If the connector runtime (`extension/src/**`, its build/configuration, manifest capabilities,
  or action contract) does not change, increment patch: `1.4.4` → `1.4.5`.
- If users must update/re-import the connector to obtain the behavior, increment minor and reset
  patch: `1.4.x` → `1.5.0`. Breaking public contracts still require a major increment.
- CLI, daemon, connector asset and Skill keep one full release version even when the connector
  runtime is unchanged. Version differences are compatibility diagnostics; missing actions or
  incompatible protocols require the matching runtime, not a version-based design permission gate.
- Only the newest patch in each minor line is maintained and presented as current. Never delete
  published Git tags, GitHub Releases or assets merely because a newer patch exists: they are
  rollback, checksum and audit records, and fixed-version install links may still depend on them.
  GitHub's `Latest` pointer and hub `latest` tags move to the newest release; older patches are
  historical/superseded and receive no further fixes.

发布分为本地准备与外部发布。准备阶段先显式同步
`extension/extension.json`、`extension/package.json`、`extension/package-lock.json`
的版本（含 lock 的 `packages[""].version`），补齐 `extension/CHANGELOG.md` 对应条目，
再同步 Skill。下面的版本号须替换为本次完整 `vX.Y.Z` 版本：

```bash
python3 scripts/sync-skill-version.py X.Y.Z   # 准备时显式写 metadata.version
make skill-check                            # 离线检查公共 Skill 文件及安装后链接
make release-check VERSION=vX.Y.Z           # 校验版本、Changelog、打包输入；不修改源码
make release-build VERSION=vX.Y.Z           # 本地构建并核对全部资产；不提交、打 tag 或上传
```

`release-check` 要求 connector manifest、npm/lock 和 Skill 版本全部匹配，拒绝缺失的
Changelog。`release-build` 生成五平台 CLI、准确版本/UUID 的连接器、`skills.tar.gz`、
安装脚本和 `checksums.txt`，并验证资产与本机 CLI 的版本。它不会自动 bump 或提交源码。
`make eext` 仍是开发期升 patch 的快捷入口，不代替发布准备所需的完整版本同步。

Skill 包只包含 Git 已跟踪/已暂存文件的当前内容；新公共参考须先审阅并暂存，本地
草稿不入包。`make skill-check` 验证链接在仅安装 Skill 的目录中仍然成立。
GitHub、ClawHub 和 SkillHub 共用此受控打包器，不直接上传夹带草稿的工作目录。

完成验收并提交已审阅源码后，只有得到发布指令才运行：

```bash
make release VERSION=vX.Y.Z
```

`release` 要求已跟踪源码没有未提交改动、tag 不存在；它重新执行 `release-build`，
然后创建并推送 tag、发布 GitHub Release，最后 best-effort 发布到 ClawHub。
它不再修改版本或自动提交。不能覆盖已发布版本；只做准备的任务停在本地资产验收。

用户安装和升级：

```bash
curl -fsSL https://raw.githubusercontent.com/zhuangzard/pcbpilot/main/install.sh | bash
pcbpilot update            # CLI + 已安装 Skill → latest
pcbpilot update --check    # 只读 CLI / Skill / connector 版本表
```

安装变量要传给执行脚本的 `bash`，例如管道右侧 `PCBPILOT_INSTALL_SKILLS=codex,claude bash`，
不要只设置在 `curl` 一侧。连接器侧载包仍需卸载旧项后导入新包，保存并重开编辑器加载新运行时。

**版本与自更新契约**：CLI、connector、Skill 发布版本一致。
`scripts/sync-skill-version.py --check` 只核验不写入，`release-check` 负责检查准备结果。
`metadata.version` 保持两空格缩进的 `  version:` 格式；安装态 `.version` 是自更新器
写入的运行时标记，和包内声明不是同一个文件。`checksums.txt` 使用裸资产文件名；
改资产名时同步 `scripts/release-check.py`、Makefile 与 `internal/selfupdate.AssetName`。
自更新遇到没有校验和的旧 release，会通过执行下载二进制比对版本作兼容检查。

## 外部发布平台

- **ClawHub**：`release` 尾部 best-effort 发布；失败可在已有发布授权下用
  `make publish-skill VERSION=vX.Y.Z` 重试，需要 `clawhub login`。
  同版本不可覆盖。发布使用临时包的绝对路径，避免全局 workdir 导向另一份 Skill；
  `CLAWHUB_TAGS` 必须保留 `latest`，否则最新安装指针不会更新。
- **skillhub.cn**：GitHub `release: published` 触发 `.github/workflows/publish-skill.yml`，
  也可手动触发或用 `make publish-skill-hub VERSION=vX.Y.Z` 补发。
  `SKILLHUB_DRY_RUN=1` 只做打包和平台预检。版本来自 release tag/手动输入的 SemVer，
  同 slug 同版本不能覆盖，发布后还需平台审核。
- **SkillHub 身份与凭据**：只使用官方 CLI 安装器
  `curl -fsSL https://skillhub.cn/install/install.sh | bash -s -- --cli-only`。
  同名 CLI 可能属于其他服务，`make skillhub-check` 按实际 `publish` 参数校验身份，
  必要时用 `SKILLHUB_BIN` 指定。仓库 secret 为 `SKILLHUB_TOKEN`；CI 的 publish
  直接读取同名环境变量并完成鉴权，不运行 login/whoami，不回显 token 或写入凭据文件。
- **SkillHub 包格式**：其 `slug/displayName` 只注入临时 staging 副本；仓库 `SKILL.md`
  保持 Agent Skills 格式。不要为了平台字段破坏公共包的 frontmatter。
- **立创连接器市场 jlc-ext**：仍需人工通过网页提交，没有发布 CLI/API。
  市场可自动更新已安装连接器，但可能落后于 GitHub Release；不能把仓库发布成功
  当成市场已更新。更多候选验收范围见 [release-1.4.md](releases/release-1.4.md)。
