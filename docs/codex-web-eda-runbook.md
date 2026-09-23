# Codex 内置浏览器：本地版本测试执行步骤

供 Codex Agent 在用户已登录的 **Web EasyEDA Pro** 中复测本仓库的本地开发版。当前工程操作
遵守根目录 [AGENTS.md](../AGENTS.md) 和公开 [easyeda-agent Skill](../.agents/skills/pcbpilot/SKILL.md)：
工程读写只走 typed `easyeda` 命令或受保护 Apply；浏览器界面只用于打开工程、管理连接器
与只读观察。原理图的数据准备与转换细节见
[数据驱动架构基准](../.agents/skills/pcbpilot/references/schematic-data.md#数据驱动架构基准)。

## 1. 准备可识别的本地版本

1. 从源码选择新的 `vX.Y.Z-dev.N`，同步连接器、npm、Skill 元数据和 changelog；**每次源码改变
   递增 N**，不以同版不同内容充当独立验收候选。
2. 运行与改动相应的测试、`make skill-check` 和 `git diff --check`，审查并提交属于本轮的源码。
3. 构建可信本地包，安装包里的 CLI 和 Skill，再用安装后的 CLI 重启 daemon。命令里的版本和
   `darwin_arm64` 按当前候选及本机平台替换；daemon 命令在单独终端保持运行。

```bash
make local-build VERSION=vX.Y.Z-dev.N DIST="$PWD/dist/local-vX.Y.Z-dev.N"
dist/local-vX.Y.Z-dev.N/pcbpilot_darwin_arm64 update \
  --local-dir "$PWD/dist/local-vX.Y.Z-dev.N" \
  --binary "$(command -v easyeda)"
make local-daemon-restart LOCAL_EASYEDA="$(command -v easyeda)"
```

4. 在用户的内置浏览器 Web EDA 中，停用旧连接器；从上述 `dist` 导入
   `easyeda-agent-connector.eext` 并启用“允许外部交互”。同 UUID 的旧侧载项须先卸载；
   市场版与本地侧载版不要同时运行。权限弹窗按浏览器要求由用户单独确认。扩展安装项
   显示新版本，不证明当前网页已运行新代码。已有文档先按实际状态保留可用证据；更新插件后
   **请用户刷新当前 Web 页面**，Agent 不用 GUI 刷新来恢复工程或代替 typed 验证。

## 2. 核对实际运行时与测试工程

```bash
pcbpilot health
pcbpilot update --local-dir "$PWD/dist/local-vX.Y.Z-dev.N" --check --exit-code
```

从 `health.windows` 精确核对目标 `projectUuid`、`documentUuid`、`documentType` 和宿主版本；
本地测试的 CLI、daemon、运行中的 `connectorVersion` 须是同一 `dev.N`。`windowId` 重连会变，
不作为持久身份。`update --check` 还核对安装的 Skill 与本地包内容；**导入成功、扩展列表版本、
新 windowId 或浏览器标签已打开都不替代这一步**。若页面仍报旧 connector，停写并保留
`health` 输出；由用户完成 Web 页面刷新/重开后再核对，不反复 Apply、清站点数据或切桌面版。
页面切换也可能重新加载旧连接器：**首次 `health` 同版后，切到目标页再查一次**。若版本
回退或 typed `document.open` 超时，立即停写，保存超时与新旧 `health`；请用户检查扩展管理
只启用目标 `.eext`，并关闭当前测试标签、从工程链接新开 Web 标签。若出现未保存提示先核实
现场状态。新标签再次精确同版、目标页 fresh 回读稳定之前，不进行测量页写入或 Apply。

用户要新测试工程时，在已连接的真实窗口通过 typed
`pcbpilot project create --window <health-window-id> --name ... --open` 创建（不要给尚未存在的
工程传 `--project` 或 `--doc`），记录返回的工程 UUID；新建原理图页后记录页 UUID，
再次核对 `health.windows`。不要把
旧项目 `ceshi` 或其他现场工程当作临时画布。单个 Web 窗口的 typed 调用串行执行，所有写命令
显式带 `--project` 和 `--doc`。

## 3. 让无历史上下文的 Codex 执行与独立验收

给**新上下文**执行 subagent（`fork_turns: none`）只提供下列任务 prompt 和
[`esp32MiniRequire.md`「一、客户原始需求」](../esp32MiniRequire.md#一客户原始需求)。不提供
历史报告、加工后的 BOM/UUID/网表、预制布局或答案图；它自行选型和规划。

> 你是独立测试执行员。先读 AGENTS.md、公开 easyeda-agent Skill 和客户原始需求。
> 在已核对版本及工程/页 UUID 的专用 Web EDA 测试工程中，从原始快照构建参数化连接与
> 核心/外围归属，逐件测量唯一可见位号的官方 bbox，完成区内布局、整页布局、固定转换、
> 受保护 Apply。图签文本进入逐页源。对器件、物理引脚到网/NC、真实直连、位号、框和
> 导线逐对象回读；对“插上 USB 就能烧录”记录是否支持免手按键进入下载模式，
> 区分原理图可证明的控制路径与必须由实板证明的烧录行为。通过严格检查后显式保存、
> 真实重载、新鲜回读。随后只从保留源做一次
> 核心及专属外围的局部移动，并证明范围外对象不变；再测写前拒绝与重算幂等。保存所有
> 输入、哈希、计划、journal、错误和 readback。任何连接/加载/保存/回读失败立即停写，
> 不用 GUI 或任意 JS 补工程。逐项报告 pass、fail、blocked、not-run，不沿用旧结论。

主 Agent 只协调该窗口；其他 subagent 可并行做离线源审查。需要独立验收时另开**新上下文**
评审 subagent（同样 `fork_turns: none`），只给冻结的输入、journal 与 fresh 证据，
不给执行员的自评结论；在主 Agent
暂停访问窗口时才允许只读现场核查。具体场景 M1/F1/F2/E1/L1/L2/N1/R1 和判据见
[1.6.0 原理图测试报告](reviews/2026-09-23-v1.6.0-schematic-acceptance.md)。

目标页尚未放置的器件若需官方位号 bbox，可用**专用临时原理图页**测量：先冻结目标页
fresh 对象快照，typed 创建临时页并保存返回 UUID，只放与源一致的 device/变体/旋转，
用 `sch designator-geometry` 和 fresh `sch list --include-pins` 逐件对齐位号、parent 与页身份。
按原始创建 UUID typed 删除临时页，再读页列表与原目标页完整对象并比较；保留创建/删除
请求和回包。即时相等只证明内存状态，未经过 save→reload→fresh readback 时，清理的
持久化仍标 `incomplete`。运行期间其他 Agent 不访问同一窗口。

逐件只允许一条 attachment 表达所属外围的主依附关系；同一器件分别用两个真实引脚
再声明一次，哪怕端点同网，也应在 `sch zone-review` / `sch layout-plan --zones` 被拒绝。
新增正式页面后先重新读取**所有目标页**的 fresh `sch list`，再编 guarded playbook：
图框的派生 `@Page Count` 会随页面数变化，使建页前的快照过期。逐页对比对象时可单独
报告这个派生字段，不能因此忽略器件、引脚、导线或实例身份的实际差异。
使用 `--replace` 清理已有页面时，队列必须在 `sch clear` **之前**核对该页全部将被删除的
图元清单及内容，至少覆盖器件、导线、网络标记和图形/图框；仅检查器件数量或执行清页后的
`--expect-empty` 不足以保护现场。对相同器件但额外一条导线的快照做负例 dry-run，确认
写前拒绝；独立评审通过后才执行。重编队列须以最终版本连接器的新鲜页快照为输入。

## 4. 判定与收尾

- 完整原理图须核对客户需求、全部物理引脚、网络/NC、所有权、位号实测、direct 线树、
  图签与几何；逐页严格检查和 DRC 后显式保存，再 `doc reload` 并 fresh 回读。截图只辅助找漏。
- 局部修改须由保留源重算，记录改变范围及范围外不变量；合法目标现场 Apply、保存重载后
  复核，受阻目标须写前拒绝。旧线/旧标记修复只在真实缺陷可回读时执行。
- 失败的 Apply journal 不从失败步骤盲续跑。先记录 fresh 现场状态；无保存/重载证据就记
  `incomplete`。官方 DRC 只有聚合数时，不猜警告对象。
- 每轮冻结版本、输入 SHA-256、命令、计划、journal 和现场回读；用
  `pcbpilot audit cost --day ... --since ... --until ... --label ... --record` 记成本画像。
  测试结束检查 `git diff`、运行相应测试并提交仓库改动；不因本地测试创建正式发布标签。

这是原理图专项执行步骤（至 S6）。需要从客户需求跑到 PCB 成品时，按
[固定端到端验收](e2e-automation-acceptance.md)继续完整 S0–S6/P0–P10，仍只向执行 Agent
提供原始需求第一节。
