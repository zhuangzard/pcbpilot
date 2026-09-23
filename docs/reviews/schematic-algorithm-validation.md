# 原理图通用算法验证 — v1.5.0-dev.14

> 本文是 2026-09-17 的历史验证记录，其中“新会话”“版本门禁”等词描述当时采用的流程，
> 不构成当前 Skill 的运行要求。当前流程以样例、实际版本证据和 save/reload/readback 为准。

2026-09-17。目标是通用算法，不是修好某一张原理图。dev.13 安装后的新会话完成 P1
只读复验，发现 DTR marker 与 C7 GND 可见导线真实相交；dev.14 将可见线宽、marker 自有引线
豁免及外来导线碰撞统一为共享数据规则，并在未修改的原始 P1/P2 输入上离线重算通过。
dev.14 安装后的下一全新会话又完成了 P1 的全新规划、Web Apply、原始回读与严格验收，
并对 P2 做了只读严格回归。该结果只覆盖本轮原理图通用算法与 P1/P2 现场验证，不等于
v1.5.0 发布验收。
规范唯一来源为 [Skill 数据驱动架构](../../.agents/skills/pcbpilot/references/schematic-data.md)。

## 已实现的通用契约

- 核心/外围显式归属；专属外围经真实线树连接，不能以同名标签或同框替代。
- 实测引脚外向方向贯穿刚体变换、候选筛选、执行守卫；器件、NC、位号及真实参数保全。
- 区分无接点内部 X 与端点/T/重叠接触；同网 X 仍不合并物理线岛。
  写后另核对物理分区，几何覆盖相同不能掩盖意外合并或断开。
- 几何失败记录两侧阻挡所有者，包括原导线的所属器件。归属完整、没有未来可用宿主时
  跳过无关 checkpoint，回退真正相关的宿主或阻挡器件；未知归属保守探索。
- 直线/折线失败后使用 5 raw 方向 A*；支持真实线岛到线树中段、X/T 分离、整网撤线重布、
  direct 局部前沿和最多 40 raw 的 attachment 刚体迁移。已连接线树可从真实中段安全命名，
  但标签不能替代 direct 连接。
- `layout-plan --report` 在成功和失败时都输出独立机器报告：源哈希、算法版本、阶段、
  搜索总预算/回退、放置冲突、路由冲突、各允许姿态的失败诊断。保留多重错误链；
  失败非零退出，不输出半成品，也不覆盖上一份合法布局来冒充成功。
- 旧 group-move/disconnect 对尚未安全支持的复杂拓扑写前拒绝，不用错误段解析继续写入。
- `sch layout-edit` 将一个 zone 的器件、引脚、位号、内部导线和标签统一表示为核心相对坐标；
  核心移动只应用一次区级平移。刚体目标碰撞时固定用户给定核心位置，仅重算本区外围，
  其他功能区作为固定障碍并保持不变。
- 标签显式区分 `pin` 与 `wire_tree` 锚定。单脚修复只沿实测引脚外向轴寻找合法长度；
  daemon 写前校验完整目标、旧线/标签、scene 指纹和标签本体旋转，串行替换后逐对象回读。
  范围外旧错误可保留但签名必须完全一致；部分写入不重试、不声称回滚。

## 回归证据

测试均使用纯计算或模拟宿主；真实反例仅用于校准语义，不按工程名、型号、位号或坐标特判。

| 回归族 | 检查内容 |
|---|---|
| `sch_layout_contract_test.go` | 不透明 ID/位号/网名重命名、源平移、四旋转与镜像、NC、确定性、输入不可变、标签替代实线负例 |
| `sch_layout_placement_conflict_test.go` | 封闭宿主失败、真实宿主回退成功、无关 checkpoint 跳过、未来同网宿主救活、预算与四旋转/平移 |
| `sch_layout_obstruction_owners_test.go` | 原导线所有者不会漏掉；移动该所有者可解除阻挡；未知归属不误称完整 |
| `sch_layout_maze_test.go` / `sch_layout_frontier_test.go` | 多折绕障、扩大边界、不同出脚方向、线树接入、X 穿越不造接点、局部前沿与节点预算终止 |
| `sch_layout_naming_retry_test.go` / `sch_layout_repair_test.go` | 拥挤端点改从真实线树中段命名、异网端点/T 拒绝、40 raw 有界刚体回退与输入不可变 |
| `sch_layout_report_test.go` / `sch_layout_feasibility_test.go` | 真实冲突与总预算报告、多重包装、早期失败姿态、报告 IO 失败、输入/输出别名保护 |
| `sch_wire_contact_test.go` / `internal/schguard` | ABAB 边界引脚、X/T、共线 waypoint、接触分区、方向、NC 和缺证据拒绝 |
| `schematic-wire-topology.test.ts` | 官方独立段解码、交叉证据、实际接触、级联保全及旧操作安全拒绝 |
| `sch_designator_geometry_test.go` | 官方 flat-segments 独立四元段、位号碰线正例和禁止虚构跨段斜线 |
| `sch_layout_edit_test.go` / `sch_layout_marker_anchor_test.go` | 核心与多级外围单次跟随、碰撞后固定核心局部重排、区外不变、跨区线树拒绝、重命名/输入不可变、预算终止、D1.3 外向支路及 pin/wire-tree 锚定 |
| `schematic_pin_repair_test.go` | 旧错误存在时的作用域替换、陈旧指纹、篡改标签朝向、部分删除、连接超时/partial、范围外对象变化全部失败关闭 |

宿主回退合成例在原 64000 候选上限内以 10282 次候选、2 次回退得到完整方案；
这是该合成用例的观测，不是所有电路的性能保证。

已通过：

- `go test ./...` 与 `make lint-test`；通用契约、回退、报告、方向真值表和规则 fixture 全绿。
- 保留的原始 P1 三 zone（源 SHA-256 `2128532c…`）全部完成：POWER_ENTRY/USB_SERIAL/
  BUCK_3V3 分别使用 6927/4469/20597 个候选；A* 展开 1682/0/38 个节点，无重布。
- 保留的原始 P2 五 zone（源 SHA-256 `c8b88e44…`）全部完成；仅 MCU 使用 A* 53 个节点，
  无重布。dev.7 的 184652 节点/4 次重布是另一个保留压力输入，不与本次原始输入混称。
- 连接器完整测试 331 项，0 失败、0 跳过；`npm run typecheck`。
- `make lint-test blocks-audit modules-audit`；1027 个块引脚引用无缺失/未知，35 条模块记录通过。
- `make skill-check release-script-test`；63 项脚本测试通过。
- `git diff --check`。
- `make local-build VERSION=v1.5.0-dev.9 DIST=dist/local-v1.5.0-dev.9`；全部资产
  checksum 通过。打包后的 Darwin arm64 CLI 重放 P1/P2，输出 SHA-256 分别为
  `0ee8864423c5c68d310db13e4fbef9b6b97c4cad27f54f1454121c27dcc41c24` 与
  `8e53d6e4e240e70a16bec8c539212a5c588d262856e20d68b0a5535e4db8ee9b`，
  和当前源码结果逐字节一致；输入哈希与报告中的 `sourceSha256` 一致。
- 这组离线证据形成时 dev.9 尚未替换运行时；后续现场记录见下一节。安装 CLI/Skill/
  daemon/Connector 后必须结束安装会话，由下一全新会话执行首条本地版本门禁，才能生成
  新鲜现场快照并 Apply。

这些 Go/连接器测试由已有 CI 的 `make test` 和 `npm test` 自动发现，不依赖 Agent
记住单独跑某个测试文件。以上是本地证据；在对应提交的远端 CI 实际完成前不声称 CI 已通过。

## dev.9 新会话现场验证（2026-09-17）

第一条命令 `easyeda update --local-dir dist/local-v1.5.0-dev.9 --check --exit-code`
退出 0，输出 READY；CLI、完整 Skill、本地资产、daemon 与 Connector 精确同版。
实际宿主 EasyEDA Pro 3.2.186，工程 ceshi。以下新增证据保存在本地忽略目录
`.easyeda/repair-20260914/live-dev9/`，不把旧快照登记为新现场结果。

- P1 严格门禁失败：10 个 pin-exit-direction、4 个 wire-through-body、1 个 orphan-stub。
  P2 严格门禁失败：11 个 pin-exit-direction。两页官方 DRC 均无阻塞，不能覆盖几何失败。
- 上文原始输入哈希对应 `generated/layout-zones-p*.json`，缺少位号 bbox 和官方引脚方向；
  不能把它们的求解成功等同于包含这些测量的完整布局成功。
- 本轮补有位号/方向的 P1 输入也完成三个区求解，源 SHA-256
  `a65f2f8d5f8bf3919bb2710647924d323b76445b6c4b0e4d1ff7bec481bf963a`；
  分别使用 160819/10986/20589 个候选，位号 bbox 与新鲜测量一致。
  这是区内离线成功，尚未进行本轮 P1 纸张转换或 Apply。
- P2 本轮使用 `live-dev4/generated-direction/layout-zones-p2.json`
  （SHA-256 `1ce722364aa9a32ef172f90c4770a3aa87f7ac60f8b4fe278e8cf11b76a01391`），
  dev.9 完成五区求解、一页 Z 型组合、固定渲染和保留实例转换。新鲜 14 个器件的完整字段
  与 dev.5 快照一致；新鲜逐器件位号测量也完全一致。纸张符号未变，沿用已保存的官方
  符号源内框/图签证据，未将其称为新鲜工程导出。
- 113 步 `--preserve-instances` 队列 dry-run 通过。真实执行第 1 步成功，第 2 步
  `verify-source-before-reset` 报 `source-drift: scene.wires`；第 3 步清理未执行。
  拒绝后的原始回读确认组件、导线、连接计数逐字段不变。没有修复后保存或完整 Apply 成功。
- 根因：`schDesignatorScene` 生成 `[]string`，队列 JSON 重载后 `sourceScene.wires`
  成为 `[]any`，`reflect.DeepEqual` 将同内容判成漂移。原测试只检查内存队列，漏掉真实
  文件往返。源码改为生成 JSON 原生数组，保持逐条导线严格比较；没有忽略导线或降低守卫。
  新测试覆盖有线/无线队列序列化后未变源通过、新增导线拒绝；修复前复现失败，修复后
  `go test ./...`、`make lint-test` 和 `git diff --check` 通过。
  该修复不改变 CLI/Skill 接口和既有保全契约。

当前安装运行时仍为 dev.9，源码修复尚未打包替换。后续需构建新的开发版本、完整安装后
结束安装会话，再由新会话首条版本门禁放行；重新采集、生成并完整执行队列，不续跑旧队列。
本轮没有重启/替换任何运行时，没有 PCB 操作、发布或清理原工程。

证据目录中的 `p2-before.json` 与 `p2-before-retry.json` 是失败命令遗留的零字节文件，
不作为前后对账证据；拒绝后的完整回读为 `p2-after-refusal.json`，执行阶段以 journal 为准。
`verification-summary.json` 保留零字节文件哈希是为了暴露采集失败，而不是把空文件算作快照。

## dev.10 新会话现场验证与 dev.11 修复（2026-09-17）

第一条命令 `easyeda update --local-dir dist/local-v1.5.0-dev.10 --check --exit-code`
退出 0，输出 READY；CLI、完整 Skill、本地资产、daemon 与 Connector 精确同版。
新鲜现场证据保存在本地忽略目录 `.easyeda/repair-20260914/live-dev10/`。

- P1/P2 均从新鲜组件、连接、纸张和位号读取重新生成源输入，没有复用 dev.9 队列。
  P1 源 SHA-256 为 `a65f2f8d5f8bf3919bb2710647924d323b76445b6c4b0e4d1ff7bec481bf963a`，
  三个 zone 全部求解；P2 源 SHA-256 为
  `1ce722364aa9a32ef172f90c4770a3aa87f7ac60f8b4fe278e8cf11b76a01391`，五个 zone 全部求解。
- 两页都完成实测纸张上的一页 Z 型组合、固定渲染和保留实例转换。P1 188 步、P2 113 步
  队列的 dry-run 均通过；相关 zones、report、page、composition、SVG、playbook 和 dry-run
  输出均留在该证据目录。
- 仅执行 P2。第 1、2 步成功，现场证明 dev.10 的 JSON 重载 source-wire 守卫修复有效；
  第 1–108 步设计动作全部成功。第 109 步 `electrical-check` 因 `$.passed=false` 停止，
  不是 timeout/partial，旧队列不可 resume。P1 未 Apply。
- P2 写后回读为 14 个 part、25 个物理线树、0 bridge、0 orphan；严格 layout-lint 的
  overlap、tight、pin coincidence、off-grid、out-of-sheet 均为 0，无必检缺测；官方 DRC
  fatal/error/warn/info 均为 0。但 `sch gate --strict` 仍因 5 个内部 X ERROR 和 1 个
  Designator-wire WARN 失败，不能称完整通过。页面已显式保存，官方导图已留档。
- 五个 X 的原始段都是不同 primitive 的严格内部交叉，交点没有端点、引脚或 marker，
  bridge-check 也证明物理线岛没有合并。根因是该宿主对实际联网导线仍可能返回空
  `wire.getState_Net()`；旧规则强制 raw wire net 非空，导致完整逐 pin 网表证据无法举证。
- Designator 报警的目标导线也没有穿过 R7 位号 bbox。根因是官方 rawLine 为彼此独立的
  四坐标段数组，Go 检查却把上一段尾点与下一段起点连成了不存在的对角线。
- dev.11 改为按每个物理岛汇总 raw wire、逐 pin 官方网表及 marker 的全部非空证据。
  raw wire net 可缺失，但至少一个逐 pin witness、每个 pin/marker 证据完整且全部唯一一致；
  缺失或冲突仍为 ERROR，X 仍不合并，端点/T/重叠规则未放宽。Go 同时保留官方
  `flat-segments`，位号只与真实独立段求交。正反单元回归均已加入。
- dev.11 离线门禁已通过：`go test ./...`、Connector 331/331、`npm run typecheck`、
  `make lint-test blocks-audit modules-audit skill-check release-script-test` 和
  `git diff --check`。`make local-build VERSION=v1.5.0-dev.11 DIST=dist/local-v1.5.0-dev.11`
  成功，五平台 CLI、Connector、Skill、安装脚本的 checksum
  全部通过；Darwin arm64 CLI 自报 `v1.5.0-dev.11`。这些仍是离线与打包证据。

dev.11 本地开发包已经形成；本会话不安装、不替换 daemon/Connector，也不继续 P1 Apply。
必须由下一全新会话执行 dev.11 本地版本门禁、重新读取现场
并重建队列后，才能判断这两个现场 finding 是否消失以及 P2 是否完整通过。

## dev.11 新会话复验与 dev.12 位号守卫修复（2026-09-17）

dev.11 已安装后的全新会话通过本地版本门禁；CLI、Skill、daemon 与 Connector 均为
`v1.5.0-dev.11`。现场证据保存在本地忽略目录
`.easyeda/repair-20260914/live-dev11/`。

- P2 只读复验通过：`sch check --strict` 为 `passed:true`；五处严格内部 X 均由不同物理
  线岛的完整逐 pin 网络证据证明为无接点交叉，仅保留 INFO。严格 `layout-lint` 对 14 个
  器件报告 0 overlap、0 tight、0 pin-coincidence、0 off-grid、0 out-of-sheet、0 缺测；
  官方 DRC fatal/error/warn/info 均为 0，`sch gate --strict` 最终 `verdict:pass`。
  与 dev.10 写后连接快照的 `connectivity-diff` 为空。该轮没有重新写 P2，属于 dev.11
  规则对已保存页面的只读现场复验，不把它登记为一次新的 Apply。
- P1 重新采集 17 个 part、61 条原始 wire 记录、17 个真实 Designator bbox 与新鲜纸张几何；
  新鲜数据按 `primitiveId` 与 dev.10 基线一致，证明此前失败没有修改页面。随后重新完成
  三区 `layout-plan --zones`、纸张规划、固定渲染、`compose --preserve-instances`，新生成的
  188 步队列 dry-run 通过。
- 第一次 P1 Apply 在第 1 步 `verify-project-unique-designators` 因队列携带的旧固定 window
  已断开而在 2 ms 内停止。没有进入第 3 步首次清理动作，也没有 mutation；该次队列没有
  resume，证据移至 `live-dev11/attempt1/`。随后重新生成不含固定 `--window`、只依赖队列
  project/document 动态路由的队列。
- 第二次仍在第 1 步停止：`schematic.components.list` 88,007 ms 后返回
  `connector did not respond`，期间 Connector 重新注册、window ID 改变。journal 只有该
  失败步骤；没有进入任何写步骤。失败后的新鲜回读与 Apply 前结果完全一致，旧队列未重试。
- 第 1 步实际请求为 `allPages:true,tagPages:true`，同时夹带 `includeDeviceIdentity:true`、
  `includeBBox:true`、`includePins:true`。该步骤本来只需检查跨页重复位号、已有实例句柄和
  待创建位号不存在，却在逐页遍历时重复执行慢速身份、几何和引脚 hydration。历史最小
  `allPages+tagPages` 读取约 0.35–3.5 秒；因此第二次失败不是浏览器缓存，也不是固定 window
  路由，而是 compose 生成的跨页前检职责过重。

dev.12 新增 `designatorsOnly` 期望模式。compose 的全工程前检现在只发送：

```json
{"allPages":true,"tagPages":true}
```

它只校验跨页重复位号、目标页已有 ref 对应的 `primitiveId`，以及待创建位号必须不存在；
禁止夹带 device identity、bbox、pins、实例属性、drawing 或 ownership 期望。紧随其后的
目标页守卫保持原有完整读取与验证，继续覆盖器件身份、实例属性、几何、完整引脚、网络/NC、
导线和连接摘要；精简跨页读取没有降低实例保全门禁。新增回归覆盖最小字段通过、跨页重复
拒绝、待建位号已占用拒绝、已有实例句柄改变拒绝，以及慢字段混入拒绝。

dev.12 离线门禁已通过：`go test ./...`、Connector 331/331、`npm run typecheck`、
`make lint-test blocks-audit modules-audit skill-check release-script-test`、Go 格式检查和
`git diff --check` 均无失败。`make local-build VERSION=v1.5.0-dev.12
DIST=dist/local-v1.5.0-dev.12` 已生成五平台 CLI、Connector、Skill 和安装脚本，全部资产
checksum 通过；Darwin arm64 CLI 自报 `v1.5.0-dev.12`。

dev.12 目前只完成源码、离线测试、Skill 契约、版本同步和本地开发包。它尚未安装，尚未从
新会话执行本地版本门禁，也尚未重建并现场 Apply P1；因此不能把该修复写成 P1 已通过，
更不能恢复或续跑任一 dev.11 队列。下一轮必须使用 dev.12 本地包，从新鲜快照重新生成
队列后再验证。

## dev.12 P1 现场 Apply 与 dev.13 检查器修复（2026-09-17）

dev.12 安装后的全新会话通过本地版本门禁；CLI、Skill、daemon 与 Web Connector 均为
`v1.5.0-dev.12`。本轮只使用现有 Web P1 标签，从新鲜页面数据重建原始三 zone 输入：
17 个器件、14 条 attachment，17/17 器件身份均由 dev.12 快照直接解析。现场证据保存在
本地忽略目录 `.easyeda/repair-20260914/live-dev12/`。

- `layout-plan --zones` 约 111 秒完成，随后生成单页 Z-flow、compose 计划和 188 步
  preserve-instances 队列；dry-run 通过。
- Apply 的第 1 步 `designatorsOnly` 在约 0.4 秒通过，第 2 步完整 P1 守卫在约 5.2 秒通过；
  第 3–185 步成功。第 186 步 `layout-lint --strict` 失败，第 187 步最终显式 save 和
  第 188 步最终实例对账未执行。旧队列未 resume，失败后已新鲜回读页面。
- 已落地并回读 17 个原实例、119 条 wire、32 个 marker、3 个 group 与 3 个 frame/title；
  pin/net/NC 末态守卫、electrical check、bridge check（32 trees、0 bridge、0 orphan）均通过，
  官方 DRC fatal/error/warn/info 均为 0。
- 严格布局门报 C5↔R6、C6↔L1、D1↔U3、D2↔D3 四组 tight-spacing；它们分别属于
  `BUCK_3V3` 或 `POWER_ENTRY` 的同一显式功能组。clusters 规则已对同功能组 tight 豁免，
  layout-lint 却没有复用 ownership，形成同一画布两套判据。
- clusters 另报 U2↔C7 overlap。U2 的 L 形线真实三段均未进入 C7 本体，但旧实现把整条
  折线包络当作碰撞成员，误把包络的空白右上角判成约 `8.5 × 0.5 raw` 相交。

dev.13 将这两个现场 finding 收敛为通用数据规则：layout-lint 与 clusters 共用本页显式
module/zone claim 和 persistent group ownership，仅豁免同 ownership 的 tight-spacing；
真实 overlap、跨 ownership tight 和归属缺失仍保持阻断，不按同网、距离或位号猜归属。
owned wire 的 cluster 总 `Box` 继续使用完整折线包络做占地与页面边界，但碰撞成员改按官方
flat-segments 的每条真实线段判断；真实线段或 marker 与其他器件相交仍报错。最小回归覆盖
同组 tight、跨组 tight、同组 overlap、L 形空角、真实线段相交、marker 相交与双源页面归属。

dev.13 离线门禁已通过：`go test ./... -count=1`、Connector 331/331、`npm run typecheck`、
`make lint-test blocks-audit modules-audit skill-check release-script-test`、Go 格式检查和 `git diff --check`
均无失败。版本元数据已统一为 `v1.5.0-dev.13`；本地开发包
`dist/local-v1.5.0-dev.13` 已生成五平台 CLI、Connector、Skill 和安装脚本，全部资产
checksum、同版 Connector/Skill 及本机 CLI 版本检查通过。这仍是离线与打包证据，
不将它写成现场通过。

第 187 步未执行，因此本轮不能登记为“最终显式保存完成”；daemon 防抖 autosave 即使可能
触发，也不能替代该证据。dev.13 在本节记载时尚未安装，也尚未重跑 P1，不能把检查器修复
写成现场门禁已经通过。

## dev.13 新会话只读复验与 dev.14 可见线宽修复（2026-09-17）

dev.13 安装后的全新会话通过本地版本门禁。本轮从现场 P1 新鲜回读 17 个器件、
60 条 canonical connection 和 3 个 persistent group；只读证据保存在
`.easyeda/repair-20260914/live-dev13/`，没有任何页面变更。

- `layout-lint --strict` 的所有几何计数为 0，证明 dev.13 的同归属 tight 和 L 形空角
  修复生效；`check` 通过，22 处合法内部 X 仅为 INFO；`bridge-check` 为 32 棵物理线树、
  0 bridge、0 orphan；官方 DRC fatal/error/warn/info 均为 0。
- `clusters` 仍精确拒绝 `U2 ↔ C7`。DTR marker 图元 `b5f2a402e19341e4`的锚点为
  `(820,1045)`，渲染 bbox 为 `(829.5,1039.5)..(860.5,1050.5)`；C7 GND 导线
  `3fead4de4cc179dc` 含 `(830,1040)→(830,1050)→(820,1050)→(820,1055)`。
  可见线宽真实相交，且网络分别是 DTR/GND；这不是 L 形包络空角误报。

dev.14 把这个反例固化成通用数据规则：线段可见半宽统一为 0.5 raw，cluster 与
组合器最终 marker 门共用同一线段 bbox 语义。marker 本体/文字与外来导线的正面积
交叠全部拒绝；只有“同一 marker 索引、恰好两点、恰好从其 `PinX/PinY` 到自身锚点”的
生成引线可在该锚点局部豁免。不按同网、同区或距离放宽，也不缩小 marker bbox。
四向自有引线正例、异物主导线沿 marker 边线走的负例和 P1 `C7-GND ↔ DTR` 最小反例
均已纳入 Go 回归。旧 dev.12 P1 输出在新规则下会先拒绝
`CC2 [-60,85]→[-65,85] crosses GND body/text`，证明必须整区重算。

重算又暴露两个有界调度问题，本轮未改原始输入、未提高每区 200000 候选，也未放宽碰撞：

- checkpoint 每次回退已消耗一个候选，旧成员数派生的 1024 分支上限却会在预算未用完时
  提前截断。现分支上限与该次共享候选额度一致，小预算仍保留 128 的次级递归守卫。
- 有 `allowedRotations` 时，旧调度对唯一有新鲜实测证据的源姿态只分配一半预算。
  诊断重放证明源姿态在原 200000 总额度内可用 125263 候选求解，并非几何无解。
  现大预算下源姿态先用 3/4（上限 150000），其余仍留给显式许可旋转；旧 20000
  默认窗口和未用配额归还语义保留。

最终使用未改的原始 P1 三 zone 输入（SHA-256 `a65f2f8d5f8bf3919bb2710647924d323b76445b6c4b0e4d1ff7bec481bf963a`）
离线重放通过，墙钟 88.51 秒。POWER_ENTRY 在源姿态用 123481/150000 候选找到完整解，
含变体评估该区共用 182541；USB_SERIAL/BUCK_3V3 分别为 18051/27211，三区合计 227803。
输出 `p1-zones-v4.json` SHA-256 为 `ca81241e275fc92d8fe3ec3322f47f9264cf8b0cd2561dd1049cc27b5d643823`，
固定渲染通过。USB_SERIAL 的 DTR 从真实线树 `(50,0)` 向下 45 raw 命名，C7 GND 从引脚向下
10 raw，现场反例对应的几何已分离。

未改的原始 P2 五 zone 输入（SHA-256 `1ce722364aa9a32ef172f90c4770a3aa87f7ac60f8b4fe278e8cf11b76a01391`）
也离线重放通过，墙钟 8.48 秒；输出 SHA-256 为
`67487deb3987df29e6cb3785709ee6c881f0fdbe8fa552c65ee2a4eceb0d568f`，固定渲染通过。

dev.14 最终离线门禁通过：`go test ./... -count=1`、Connector 331/331、
`npm run typecheck`、`make lint-test blocks-audit modules-audit skill-check release-script-test`、
Go 格式检查和 `git diff --check` 均无失败。`make local-build VERSION=v1.5.0-dev.14
DIST=dist/local-v1.5.0-dev.14` 已生成五平台 CLI、Connector、Skill 和安装脚本；全部资产
checksum 通过，Darwin arm64 CLI、包内 Connector 与 Skill 均自报 `v1.5.0-dev.14`。

上述是源码态 dev.14 的离线算法证据；它随后由下一节的独立新会话完成运行时与 Web 现场闭环。

## dev.14 新会话 P1 Web 闭环与 P2 回归（2026-09-17）

新会话的第一条 shell 命令为
`easyeda update --local-dir dist/local-v1.5.0-dev.14 --check --exit-code`，退出 0 并返回
`READY`。CLI、安装态完整 Skill、daemon 与唯一 Web Connector 均精确为
`v1.5.0-dev.14`；只使用工程 `475cc0f773ed4a6fb7a02336c8a6a67f` 的现有 Web 标签，
所有路由使用 project + document UUID，没有固定 window、创建新标签或启动桌面版。
完整现场证据保存在本地忽略目录 `.easyeda/repair-20260914/live-dev14/`。

写入前从 P1 `fb2fca2fba6d9d07` fresh 回读了完整 identity/pins/bbox/wires/connectivity、
17 个真实 Designator bbox、纸张、zones 和 groups。只读严格门禁精准得到唯一 blocker：

- `layout-lint --strict` 为 17 个器件、0 overlap、0 pin-coincidence、0 tight、0 off-grid、
  0 out-of-sheet、0 几何缺测；`check` 的 22 条 finding 全是已由物理线岛和逐 pin 网表
  证明合法的无接点内部 X，均为 INFO；32 棵物理线树为 0 bridge、0 orphan，官方 DRC
  fatal/error/warn/info 全 0。
- `clusters --strict` 唯一报 `U2 ↔ C7`，交叠为 `ovX=1, ovY=11 raw`。成员证据显示
  U2 的 DTR marker 占位与 C7 的外来 GND 导线真实接触，和 dev.13 反例一致；页面在此时
  没有 mutation。

从保留的原始三-zone源数据重新开始，而不是把 live 中间态反向当源。源 SHA-256 完整值为
`a65f2f8d5f8bf3919bb2710647924d323b76445b6c4b0e4d1ff7bec481bf963a`；全新
`layout-plan --zones` 在 85.39 秒完成，报告为 `status:planned, phase:complete`，算法版本
`v1.5.0-dev.14`，路由器实际计算 5644 ms。三个 zone 均使用原默认
`maxExpandedNodes:200000/maxReroutes:4`：POWER_ENTRY 展开 176639 节点、4 次重布、
共用 182541 个候选；USB_SERIAL 为 0/0/18051；BUCK_3V3 为 16/0/27211；没有失败线岛或
失败分类。新输出 SHA-256 为
`ca81241e275fc92d8fe3ec3322f47f9264cf8b0cd2561dd1049cc27b5d643823`，与固定
offline-dev14 v4 几何逐字节一致；固定渲染 SHA-256 为
`d602ab63df2a3d628740a616f047756588e125fa3ae54c4f82cc2f35ba58838f`，也逐字节一致。

纸张层生成一页 Z-flow，候选检查 15 次、`budgetLimited:false`；随后用 fresh P1 快照
生成 `--preserve-instances` 受保护队列。队列 meta 只固定 project/document UUID，共 196 步，
dry-run 为 `preflight passed`。完整 Apply 一次执行完成：`196 ok, 0 skipped`，墙钟
52.248 秒，journal 无 timeout、partial 或 unknown；其中实际变更步骤 185 个，包括保留实例
清图、17 个原实例移动、2 个 NC、127 条 wire、32 个 marker、3 个持久组、3 个 frame/title
和显式 save。没有续跑 dev.12/dev.13 队列，也没有手改生成队列或现场坐标。

Apply 后又独立 fresh 回读，不以队列内自报代替验收：

- 最终实例守卫与独立对账均为 17/17：全部 primitiveId、uniqueId、库身份、BOM/PCB 标志、
  供应商、自定义属性及非位号属性逐字段保持；目标位姿、bbox、60 个 pin→net/NC 全部命中。
  与 canonical 黄金表的 `connectivity-diff` 为空。14 条 attachment 和三个模块的
  core/peripheral ownership 由 compose 的真实线树硬门通过；现场持久组成员与三组源归属一致。
- 三个粉色虚线 frame/title 的独立 `frame check` 为 `verified:true, unchanged:3`；17/17
  Designator 均可见且有真实非空 bbox，器件本体和位号逐项全部在所属框内。型号、参数、描述、
  供应商等非位号属性只做实例保全，不进入碰撞或入框判据。
- 独立 `sch gate --strict` 为 `verdict:pass`：layout-lint 全零；17 个 clusters 为 0 overlap、
  0 out-of-sheet、0 tight；`check` 的 26 条 finding 全是 INFO 级合法内部 X，marker overlap、
  reversed flag、wire-over-pin、floating pin 均为 0；32 棵物理线树为 0 bridge、0 orphan-stub、
  0 orphan-flag、0 orphan-tree；官方 DRC fatal/error/warn/info 全 0。
- 原阻塞几何已经分离：最终 DTR marker 占位 y 为 908..966 raw，C7 GND marker/引线组从
  y=973.5 raw 起，clusters 不再报交叠。D1.3 回读为 `(175,960)`、官方外向角 180°，其首段
  到 `(170,960)`，沿左侧外向直出；所有 marker 自有引线方向检查为 0 reversed flag。
- 最终显式 `sch save` 返回 `saved:true`。官方整页 SVG 为
  `.easyeda/repair-20260914/live-dev14/p1-official.svg`，163715 bytes，SHA-256
  `ec77aa773b9e3992ef206623794a07621ddec7a9800e5f6cec85cf09267b7797`；官方 artifact 原件
  `.easyeda/artifacts/20260917-045425-schematic_export-52b7fd13.svg` 哈希相同。

最后对 P2 `d1e7188c3d1d23c3` 只读运行 dev.14 严格 gate，没有修改页面：14 个 clusters 为
0 overlap/0 out-of-sheet/0 tight，layout-lint 全零，5 个合法内部 X 仅为 INFO，25 棵物理
线树为 0 bridge/0 orphan，官方 DRC fatal/error/warn/info 全 0，最终 `verdict:pass`；随后
将现有 Web 标签切回 P1。至此 dev.14 的 P1/P2 原理图现场闭环通过，但 PCB、完整客户需求
S0–S6/P0–P10、发布资产与正式 tag/release 等 v1.5.0 发布验收仍未执行。

## 2026-09-17：Zone 归属提醒规则（本地源码验证）

新增只读 `sch zone-review --from zones.json --report review.json`，并接入
`layout-plan --zones` 求解前 stderr 提醒和成功/失败报告的 `zoneReview`。
规则给 AI 提供证据和建议，由 AI 决定是否修改源 JSON；没有自动拆区或改页面。
多个 >=4 引脚器件只是多核心弱线索，不按 U/D/J 等位号推断功能。
非电源/地子图按明确 netPolicies 计算，不宣称证明现场物理直连。

使用 `generated/layout-zones-p1.json` 和 `generated/layout-zones-p2.json` 保留输入，
本地构建 CLI 的结果保存在 `.easyeda/zone-review-validation-20260917/`：

- `p1-review.json`：7 条提醒。POWER_ENTRY 的 D1/USBC1 命中多引脚成员规则；
  D3/U3 命中与核心分离的非电源/地子图；D3→D2 命中仅共享 +5V 的 attachment。
  其余 4 条为电源/地关联的外围提醒，需语义复核，不直接认定错误或拆出电容。
- `p2-review.json`：2 条提醒，均为 C1/C2→U1 的电源/地 attachment；
  未触发多引脚成员或分离子图规则，不等于自动认定功能归属正确。

回归包含 P1 型最小输入、正反拓扑、稳定 ID/位号/网名重命名、输入顺序与坐标/姿态
不影响规则、原始输入不变、未知引脚/NC 冲突/attachment 环拒绝、隐式宿主不被改为
核心，以及求解成功/失败均保留提醒、提醒不改变退出码、源文件不可被报告覆盖。
`go test ./...` 通过。此批只验证规则与 CLI，不替代重新布局、Web Apply 或发布验收。

随后用两个独立 `mktemp` 源码副本验证“保留问题→AI 改源→重算”：副本排除 `.git`、
旧 `.easyeda`、`bin/dist/node_modules`，各自在副本内构建和运行 `go test ./...`。
第一轮保留 before 的 7 条提醒和源 SHA，拆成 USB_TYPE_C、POWER_ORING、EXT_5V_INPUT、
USB_SERIAL、BUCK_3V3 后，17 个组件对象、measurement、pin/net/NC、旋转授权和稳定 ID
逐字段不变；错误的 D3/U3 分离子图提醒消失，其他 6 条语义提醒保留，五区求解完成。

第一轮同时发现功能核心与布局锚点的授权冲突：USBC1 源 `allowedRotations:[0,180]` 包含
实测 180°，改成核心却被旧规则拒绝。现已将核心的**有效**许可收窄到实测角，不修改源列表。
第二个全新副本以 USBC1 为 USB_TYPE_C 核心复测：全量测试通过，五区/17 ID 唯一覆盖，
USBC1 输出 `(0,0)`、rotation 180、mirror false，源授权仍为 `[0,180]`，所有输出候选的
有效核心授权均为 `[180]`。布局完成 0.69 秒，报告路由 60 ms。留档位于
`.easyeda/zone-review-clean-validation-20260917/` 与
`.easyeda/zone-review-core-clean-validation-20260917/`；均未安装 CLI、连接 Web 或 Apply。

## dev.15 P1 五区现场修复与位号闭合障碍回归（2026-09-17）

本轮只使用 Web 版 EasyEDA Pro 3.2.186。CLI、daemon 与两个 Web Connector 均为
`v1.5.0-dev.15`，目标固定为工程 `475cc0f773ed4a6fb7a02336c8a6a67f` 的 P1
`fb2fca2fba6d9d07`；完整证据保存在
`.easyeda/p1-dev15-live-validation-20260917/`。

通用算法先收紧两项数据判据：实测 Designator bbox 改为闭合障碍，导线沿边或角点接触也
按 `wire-text` 拒绝；maze 失败缓存不再保存 `expanded-node-budget-exhausted`、
`relocation-budget-reserved` 和 `route-attempt-node-limit` 这三类阶段性预算结果，避免后续
合法分支复用过期失败。placement、A* edge 与 pin-exit 共用同一位号接触判据，最小回归
覆盖水平/垂直边界、角点、内部穿越、完全避开及三条缓存分类。当前工作树运行
`go test ./... -count=1` 与 `git diff --check` 均通过。

固定 P1 五区布局的首个现场候选在受保护队列第 186/188 步被严格门正确拦下：
`check` 唯一阻塞为 CC2 netport 与 R4 的 GND netflag 可见 bbox 重叠 `1×2 raw`；前 185 步
虽已落地，但没有续跑、跳步或声称回滚。fresh 回读后从数据层把 CC2 的线树锚点由
`(75,70)` 左移一个 5-raw 网格到 `(70,70)`，同步更新固定 frame 的 title occupancy，
重新完成五区纸张规划、固定渲染、Compose 和 188 步 dry-run，再生成全新队列完整执行。

第二个队列一次完成：`188 ok, 0 skipped`，journal SHA-256 为
`3ea43b3afb19c699f2b7e0bde867398328253a221cf9cf3d43cf5319a6e01c08`。队列内和独立
fresh 回读均得到 `sch gate --strict verdict:pass`：17 个器件有完整 bbox，layout-lint 与
17 个 cluster 的 overlap/tight/out-of-sheet 均为 0；marker overlap、floating pin、
wire-over-pin、reversed flag 均为 0；36 棵物理线树为 0 bridge、0 orphan；官方 DRC
fatal/error/warn/info 全为 0。`check` 保留 24 个已由物理线岛和逐 pin 网表证明合法的
无接点内部 X，级别均为 INFO，不阻塞严格门。

独立对账确认 60 条 `ref.pin→net` 电气映射与修改前哈希一致；17 个 primitiveId/位号及
device、uniqueId、BOM/PCB 标志、供应商和自定义属性的规范化哈希也完全一致。最终再次
显式 `sch save` 返回 `saved:true`。官方整页导图为
`.easyeda/p1-dev15-live-validation-20260917/p1-final-official.svg`（169692 bytes，SHA-256
`e3292ac53389e2354d5478e32a08342228a91ced2305feb3af3b7cedca9cff1d`）及同目录 PNG。

本轮证明固定布局数据能够保留问题、由严格数据门发现并修源后完整 Apply；仍不能把它写成
“五区默认 200000 节点预算的全量重新求解已普遍解决”。该全量重求解缺口、P2 新一轮现场
回归、S0–S6/P0–P10 整板验收和 `v1.5.0` 正式发布均不在本节的通过范围内。

## P1 Type-C/ESD 六区拆分与默认预算重算（2026-09-17）

用户明确要求把原 `USB_TYPE_C` 拆成两个独立功能区：`USB_TYPE_C` 仅含
USBC1/R3/R4，`USB_ESD_PROTECTION` 以 D1 为核心并仅含 D1/C8。两区声明软相邻，跨区
`USB_DP`、`USB_DM`、`USB5V` 保持 `module_port`；源数据 SHA-256 为
`0515afb38af6b1b6756253615ac91f851cbc1e59bde38c3d5dacdc99c6feb859`，证据保存在
`.easyeda/p1-six-zone-validation-20260917/`。

拆分暴露了通用缺口：USBC1 同侧重复的 DP/DM 引脚在 `module_port` 快速路径失败后会退化为
逐引脚标签，10 raw 间距下几何必然冲突。算法现在只在求解副本中把“同器件、同侧、重复的
信号 module_port”提升为 `direct`，先形成真实且互不短路的区内线树；源 zone 的跨区策略
保持 `module_port`。ABAB 交错最小回归新增了逐引脚物理线岛断言。现场首次 Apply 后严格门
还发现一个预期 1 raw 的字体擦边被 `1.000000...` 浮点尾差误判；共享 marker 判据加入
`1e-6` 数值容差，2 raw 真重叠仍报警并有回归覆盖。

六个 zone 在原默认预算 `maxCandidates:200000`、`routing.maxExpandedNodes:200000`、
`routing.maxReroutes:4` 下全部完成，未放宽 USBC1 的 `allowedRotations:[0,180]`。报告记录
区内路由 178 ms；USB_TYPE_C 展开 4323 个 A* 节点、完成 3 次指定线岛合并。纸张层生成
一张 A4，Type-C 与 ESD 相邻，其余四区保持 POWER_ORING、EXT_5V_INPUT、USB_SERIAL、
BUCK_3V3。

首个 188 步队列在第 186 步被上述浮点误判拦截，未使用 resume。修复判据并 fresh 回读后，
页面已匹配目标；重新 Compose 得到 16 步完整验证队列，dry-run 通过且 `16/16` 成功。
独立 `sch gate --strict` 最终 `verdict:pass`：17 个器件 0 overlap/tight/pin-coincidence/
off-grid/out-of-sheet，marker overlap 为 0，37 棵物理线树 0 bridge/orphan，官方 DRC
fatal/error/warn/info 全为 0。18 条 `wire-crossing` 全部是有逐 pin 和物理线岛证据的合法
无接点内部 X，级别为 INFO，不是警告或豁免错误。

最终分组回读明确得到 `USB_TYPE_C=USBC1,R3,R4`、`USB_ESD_PROTECTION=D1,C8`；修改前后
connectivity diff 为 `{}`，17 个器件的 primitiveId、device identity 与 properties diff 为
0 行。显式保存返回 `saved:true`。官方导图 `p1-six-zone-final.svg` 和 PNG 的 SHA-256 分别为
`5a5e695e44eaa7bc907816023c66afd0f731f35f94c2570b323453208afd71cc`、
`5d9445540e7ed74cb8ec2294b4aee428b8891abb037bdb45b645909b110d5a8e`。

本节证明 P1 六区通用算法、Web Apply 与回读闭环；不等同于 P2 新一轮现场回归、客户需求
S0–S6/P0–P10 整板验收或 `v1.5.0` 正式发布。

## 明确边界

有界窗口、姿态菜单与预算不是完备搜索；仍可能重复探索局部预算窗口。失败不证明全局无解，
不能靠扩大预算、放松硬约束或手改最终坐标宣称解决。外围语义归属仍需显式输入。
宿主接触语义现场证据限于已测 EasyEDA Pro 3.2.186；未知宿主行为须完整回读，不推定兼容。
离线实例保全/回读守卫测试不等于 dev.14 运行时现场验收；PCB 与正式发布均不在本轮执行范围。
