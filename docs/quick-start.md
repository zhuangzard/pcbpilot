# 快速开始 & 使用注意事项

pcbpilot 有三个配套组成部分；EasyEDA Pro 是运行宿主：

| 部件 | 是什么 | 装在哪 |
|---|---|---|
| **CLI / daemon** (`pcbpilot`) | 掌管 typed action 协议、状态、审计、产物、校验 | 本机 `PATH`(默认 `/usr/local/bin`) |
| **连接器插件** (`.eext`) | 极薄桥接层,跑在 EasyEDA 内,把动作转成官方 `eda.*` 调用 | EasyEDA Pro「扩展管理」 |
| **Skill** (`pcbpilot`) | AI 客户端里的工作流、参考、脚本、规范 | `~/.claude/skills`、`~/.codex/skills` 和/或 Codex Desktop 使用的 `~/.agents/skills` |
| **EasyEDA Pro（宿主）** | 官方编辑器,需开启「允许外部交互」 | 桌面应用 |

> `pcbpilot update --check` 会列出 CLI、连接器和 Skill 的版本差异。差异是安装诊断；
> 某动作是否可用以当前 `--help`、action 目录和实际调用结果为准。

---

## 首次安装(5 步)

### 1. 装 CLI + Skill(一条命令)

```bash
curl -fsSL https://raw.githubusercontent.com/zhuangzard/pcbpilot/main/install.sh | bash
```

一键脚本会：
- 安装/更新 `pcbpilot` CLI/daemon 到 `PATH`;
- **自动检测已装的 AI 客户端**,把 `pcbpilot` skill 装到对应目录 —— Codex(`~/.codex/skills/pcbpilot`)、Codex Desktop 共享目录(`~/.agents/skills/pcbpilot`)、Claude Code(`~/.claude/skills/pcbpilot`);
- 打印连接器 `.eext` 的下载地址。

可用环境变量控制 skill 安装目标:

```bash
curl -fsSL https://raw.githubusercontent.com/zhuangzard/pcbpilot/main/install.sh -o install.sh
PCBPILOT_INSTALL_SKILLS=codex,agents,claude bash install.sh  # 指定目标
PCBPILOT_INSTALL_SKILLS=none bash install.sh         # 只装 CLI
PCBPILOT_SKILL_PRESERVE=1 bash install.sh            # 保留本地内容及旧版本标记
PCBPILOT_VERSION='<vX.Y.Z>' bash install.sh          # 锁定发布版，跳过 API 查询
```

> 装不上、报 `403`?脚本要调一次 `api.github.com` 查 latest release,匿名额度是每
> IP 每小时 60 次,公司出口 / NAT / CI 很容易撞满。要么 `export GITHUB_TOKEN=<token>`
> (或 `GH_TOKEN`;已 `gh auth login` 的话脚本会自动取 `gh auth token`),要么用
> `PCBPILOT_VERSION=<tag>` 直接锁版本绕开 API。

### 2. 启动 daemon

```bash
pcbpilot daemon start        # 前台阻塞运行,Ctrl-C 退出;建议单开一个终端常驻
```

daemon 默认固定监听 `61832`，连接器重试同一端口；不要额外启动多个 daemon。

### 3. 导入连接器 `.eext`

从 [GitHub Release](https://github.com/zhuangzard/pcbpilot/releases/latest) 下载
`pcbpilot-connector.eext`，或从[**立创官方插件市场**](https://github.com/zhuangzard/pcbpilot/releases/latest)一键安装(平台可原地自动更新,但版本可能滞后 CLI；缺少新 handler 时使用 GitHub Release 的 `.eext`),然后:

> EasyEDA Pro → **扩展管理 → 导入扩展** → 选中 `.eext` 文件

### 4. 开启「允许外部交互」

> EasyEDA Pro → **设置 → 允许外部交互 (Allow external interaction)**

不开这一项,连接器的 WebSocket 永远连不到本地 daemon。

### 5. 在 AI 客户端里用 Skill

```
/pcbpilot          # 原理图 + PCB 全流程
```

支持 MCP 的客户端还可以选择注册仓库内的 stdio 适配层。MCP 是**可选调用入口**,
不是替代 CLI/daemon 或 Skill 的第五套状态;它仍经过同一套 typed action、审计和
typed action、审计和事实检查。

```bash
git clone https://github.com/zhuangzard/pcbpilot.git
cd pcbpilot
npm --prefix mcp ci --ignore-scripts
codex mcp add pcbpilot \
  --env PCBPILOT_BIN="$(command -v pcbpilot)" \
  -- node "$(pwd)/mcp/src/server.mjs"
```

注册后重启 AI 客户端。可用工具包括连接健康、action 发现、7 个安全 action domain、
电路块和 guarded workflow;MCP 不暴露任意 JavaScript 的 `debug.exec_js`。

---

## 验证三要素是否对齐

```bash
pcbpilot daemon health
```

关注返回里的 `connectorVersionOk`:
- `true` —— 连接器与 daemon 的声明版本匹配;
- `false` —— 连接器版本有差异(常见于升级只升了 CLI 没重导 `.eext`,或旧窗口没重启)；按当前任务是否缺 handler 决定是否升级;
- 字段缺失/`null` —— dev 构建,无法硬比对(正常)。

---

## 升级注意事项

1. **`pcbpilot update`** —— 升级 CLI 二进制 + Skill 目录(装过一次之后的常规路径):
   ```bash
   pcbpilot update            # 下载本平台二进制 → sha256 校验(有 checksums.txt 时) → 原子替换 + 同步 skill
   pcbpilot update --check    # 只看不改:cli / skill / connector 三方版本一次列清
   sudo pcbpilot update       # 二进制装在 /usr/local/bin 等 root 目录时
   ```
   升完 **daemon 仍在跑旧二进制,要重启 daemon**;命令会提示。
   开发机上的 dev 构建(git-describe 版本号)默认不覆盖 —— 这是有意的,`--force` 才强升。
   一键脚本仍是**首次安装**(和重装连接器)的路径:
   ```bash
   curl -fsSL https://raw.githubusercontent.com/zhuangzard/pcbpilot/main/install.sh | bash
   ```
2. **重导连接器 `.eext`** —— EasyEDA 按 **uuid 去重**,光 bump 版本号不够:
   先在「已安装」里**卸载旧连接器**,再导入新 `.eext`(uuid 不变,原地更新)。
   *(这步只针对**侧载**的 GitHub Release `.eext`;若连接器是从[立创插件市场](https://github.com/zhuangzard/pcbpilot/releases/latest)装的,平台会原地自动更新 —— 但市场版本可能滞后 CLI,严格同版仍以 Release `.eext` 为准。)*
3. **完全退出并重启 EasyEDA** —— 重导**不会重载已开着的窗口**;旧窗口会继续跑旧代码、
   和新连接器抢 daemon socket。必须**彻底退出 EasyEDA 再打开**。
4. **`pcbpilot daemon health` 复核** —— 检查连接、窗口、版本差异和当前动作是否可用。

> 大多数改动其实不需要重导 `.eext`(daemon 侧的 typed action / CLI 更新无需碰连接器);
> 只有连接器 manifest / handler 变了才需要重新导入。是否需要,看 Release 说明。

### 自动帮你做的部分(省去手动)

- **Skill 目录自动同步**:`daemon start` 默认带 `--auto-update-skill`,启动时会**后台**
  把已存在的 Skill 目录拉齐到运行中的 CLI 发布版本，开发构建不自动写入，并把每一步打进
  daemon 日志。客户端目录遵循 `CODEX_HOME` / `CLAUDE_CONFIG_DIR`，默认仍为
  `~/.codex` / `~/.claude`。尊重
  `PCBPILOT_SKILL_PRESERVE=1`(保留本地改动);关掉用 `daemon start --auto-update-skill=false`。
  手动触发/查看:
  ```bash
  pcbpilot skill status      # 各 skill 目录版本 vs 最新 release
  pcbpilot skill sync        # 立即同步到最新(--version 锁版本,--preserve 保留本地改动)
  pcbpilot update --check    # 想连 CLI 二进制和连接器一起看时用这个
  ```
- **连接器落后自动提示**:连接器一注册,daemon 就比对版本;落后时打一条**可操作日志**
  (「stale connector: vX < daemon vY — 重导 .eext + 彻底重启 EasyEDA」)。
  **侧载**(GitHub Release)的连接器 `.eext` **无法**被 daemon 静默替换(sideload 无原地自动更新),
  所以这里只**检测+提示**,重导那步仍需你手动做(见上)。若连接器是从
  [**立创插件市场**](https://github.com/zhuangzard/pcbpilot/releases/latest)装的,
  平台**可原地自动更新** —— 但市场版本可能滞后 CLI；需要新 handler 时以 GitHub Release 的 `.eext` 为准。

---

## 常见卡点速查

| 症状 | 原因 | 处理 |
|---|---|---|
| 动作全部超时、连不上 | 没开「允许外部交互」 | 设置里打开 |
| 不确定谁落后了 | CLI / skill / 连接器版本不一致 | `pcbpilot update --check` 一次列清三方 |
| `connectorVersionOk:false` | `.eext` 落后 / 旧窗口没重启 | 重导 `.eext` + 彻底重启 EasyEDA |
| 重导 `.eext` 后没生效 | EasyEDA 按 uuid 去重,旧的没卸载 | 「已安装」里先卸载旧的再导入 |
| `pcbpilot: command not found` | `PATH` 没含安装目录 | 把 `/usr/local/bin` 加进 `~/.zshrc` |
| registry 安装的 Skill 版本不同 | registry 审核或同步有延迟 | 用一键脚本,或从同一 Release 下 `skills.tar.gz` 解压到 skills 目录 |

## 给 AI Agent 的推荐引导 Prompt

```text
请使用 pcbpilot 完成 EasyEDA Pro 任务。

先运行 pcbpilot health 核对目标工程、页面和连接器。安装版本需要对账时运行
pcbpilot update --check --exit-code；版本差异只作诊断，不作为动作许可。若当前命令或 connector
handler 缺失，升级对应组件；更新 Skill 后让客户端重新加载，更新连接器后完全退出并重开
EasyEDA。确认已开启“允许外部交互”。

先读 Skill 的 schematic-data.md「数据驱动架构基准」。保留官方原始快照，在源数据副本
明确 canonical 连接、核心/外围归属、参考引脚与约束；用 layout-plan --zones、
layout-sheet-plan、layout-render 计算并验证，已确认页用 compose --layout-page 固定转换。
问题由数据检查发现，修源数据/采集/算法后重算，不以现场逐件试摆或手改队列兜底。
Apply 后回读器件、pin→net/NC、真实直连、位号和框/标题，逐页运行检查并显式保存、重开回读。
位号参与遮挡/入框，型号/参数等非位号属性文字排除布局检查；截图只辅助发现规则遗漏。
不把同网/同框、高分、缺测或未完成溯源当通过，不把 GPIO 号当物理脚号。
```

延伸阅读:[功能清单与路线图](FEATURES.md) · [架构](architecture.md) · [开发环境与调试手册](dev-environment.md)
