# PR / Issue 排查（2026-09-08）

修复已纳入 v1.4.4。按用户后续要求，#190/#192/#201（以及后续 #202）随发版关闭，
不等待报告者复验；#191/#173/#43 保留开放。#200 已于 9 月 9 日另行关闭并建议改用
网页编辑器，本轮未进一步确认其原始根因。发行状态以
[v1.4.4 Release](https://github.com/zhuangzard/pcbpilot/releases/tag/v1.4.4)
为准；以下是发版前的排查与验证记录。

发版前远端同步状态（2026-09-09）：13 个修复及记录提交已推送到 main（至 d1dc9dd），
[对应 CI](https://github.com/zhuangzard/pcbpilot/actions/runs/34250243475)
4 个 job 全部通过。PR #199 已按“完整采纳到 main”关闭；已更新 #190、#192、#201、
#191、#200 的已有进度评论，Issue 仍保留开放。Windows PowerShell 5.1 原生测试已通过，
完整 Windows DSH 安装及其余宿主/整板验收缺口仍保留。尚未发布新版本。
以下开头表格与中间各节为历史排查快照，最新结论见文末“推送、CI 与 PR 收尾”。

首次排查时读取全部 1 个开放 PR、6 个开放 Issue 及评论；下表保留当时的判断。

| 项目 | 结论及本轮处理 | 后续验收 |
|---|---|---|
| [PR #199](https://github.com/zhuangzard/pcbpilot/pull/199) | 请求体上限从 1 MiB 放宽到 32 MiB，方向合理；补丁可应用到当前 main，独立临时副本中 `go test ./internal/daemon` 通过。未合并。 | 现有测试只覆盖 2 MiB 可通过；建议补超限拒绝测试，Skill 说明 base64 后整个 JSON 的大小限制。3D 模型真实导入未验证。 |
| [#192](https://github.com/zhuangzard/pcbpilot/issues/192) | 本地新增 `pcb modify` / `sch modify --patch-file`，兼容 UTF-8 BOM，与 `--patch` 互斥；同步 Skill。 | 命令级模拟 daemon 测试验证 payload 与无效输入不派发；尚未在 Windows PowerShell 5.1 真机执行。发布后可请报告者复验。 |
| [#191](https://github.com/zhuangzard/pcbpilot/issues/191) | 仓库 `docs/dev-environment.md` 已有 9 月 5 日的 3.2.186 超时实测；官方接口标注 EDA v4 / BETA，参数签名与当前调用一致。已把兼容性说明补进 Skill。 | 保持开放；受支持宿主上探测并核对连接。不能把换 netport/netflag 当原生 label 已修复。 |
| [#190](https://github.com/zhuangzard/pcbpilot/issues/190) | 报错发生在 `--doc PCB1` 解析阶段，未进入器件移动。已有维护者评论索要版本、成功/失败后的文档枚举，尚无新增答复。 | 等待同一会话的 health / doc ls 对照，不重复发送相同问题。 |
| [#200](https://github.com/zhuangzard/pcbpilot/issues/200) | 报告为 3.2.149 重启后 PCB getAll 空读；本机为 3.2.186，不能当同版复现。当前打开的是用户原理图工程，本轮未改动页面。 | 需在受影响宿主核对窗口、活动 PCB、文档 UUID 与 getAll / 引擎计数；读数异常时不能把 0 器件算通过或盲写。附带 cmdKey 症状在 `cmd_sch_block_layout_solve.go` 有网表导出影响命令上下文的历史线索，但未证明本票同源；升级 RPC、密集引脚短接需分别复现。 |
| [#173](https://github.com/zhuangzard/pcbpilot/issues/173) | 原生 UI 编组 API 未暴露的历史调查已有记录；virtual group 是独立功能开发，需要统一约束全部布局入口。 | 保持开放，不能用只实现 group CRUD 宣称完成。 |
| [#43](https://github.com/zhuangzard/pcbpilot/issues/43) | 芯片级 N8R8 实机验收；历史 R2 评论已纠正 `pcb check` 假绿，记录了 2 条短路，不能按曾有 0 ERROR 数字关闭。 | 专门运行真实编辑器整板回归，解决短路、RF / 高速约束后再验收；本轮未执行。 |

本轮仅修改 CLI 文件输入与 Skill 说明，未改连接器、布局判据或 autosave；离线测试不代表整板端到端验收。

## 补充验证与提交

修复提交：`6c70816`。后续补齐 Unreleased changelog、两个命令的文件输入示例、
文件缺失/显式零值覆盖/PCB center 冲突/空 inline 与文件互斥的命令级测试。

- `TestModifyPatchFile`：16 个用例通过。
- `TestModifyPatchFileBoundaries`：7 个边界用例通过；拒绝路径均确认没有派发动作。
- `TestBuildModifyPatch`：9 个已有合并语义用例通过。
- `go test ./...`：全量通过（Windows 专属测试在 macOS 跳过）。
- `make lint-test`、`make skill-check`：通过。
- Windows amd64 测试程序交叉编译：通过；不代表 Windows 运行通过。
- `TestModifyPatchWindowsPowerShell51`：本机 macOS 无 PowerShell 5.1，明确跳过。
  已接入 CI native-release-smoke；Windows runner 将通过 Set-Content 生成 BOM JSON，
  再实际调用 powershell.exe → easyeda.exe → 模拟 daemon 验证补丁。该 CI 尚未运行。

本次不升级版本、不发布；保留未完成的宿主复现及整板验收事项。

## 深入修复（后续进展）

- **#190 已有本地修复**：`discoverDocs` 在活动 PCB 被总表漏掉时，调用官方
  `pcb.board.info` 恢复名称与 UUID。项目、文档和类型必须与活动上下文一致。
  补读失败、跨项目、切页或名称不符均拒绝；不移除 `--doc`，不猜名称。
  7 个模拟宿主回归场景通过，相关路径 race 测试通过。尚待受影响宿主实测。
- **PR #199 的补丁已纳入本地实现**：保留原贡献者署名，增加 32 MiB 精确边界
  与多 1 字节拒绝测试，补充 Skill 的 base64 膨胀说明。三个请求体测试路径通过；
  实际 3D 模型导入未测，GitHub PR 状态尚未变更。
- **#200 的根因仍未证实**：源码确认 import-changes 的前后计数也来自 getAll，
  不能称为独立引擎计数。已准备 `scripts/diagnostics/pcb-empty-read.js`，一次只读调用
  对比 getAll 无参数/undefined/分层/ID 枚举与文档前后身份，支持定位参数或上下文差异。
  模拟 ID 非零、实例为空的探针验证通过。当前 autoconnect 的同列引脚硬拒绝与
  批次桩线互斥已有实现，对应回归测试通过；仍需问题工程才能解释报告中的短接。
- **实机阻塞**：本轮一度读取现有 PCB 返回 0，因无独立非空证据未判定为复现；
  已恢复原理图活动页。随后两个连接器均断开，health 确认 windows=[]，
  无法继续实机验证或 ceshi 整板回归。已请求恢复回归工程。

深入修复后的 `go test ./...`、文档恢复/补丁文件 `-race` 测试、`make lint-test`、
`make skill-check` 均通过。Windows PowerShell 5.1 仍待 Windows CI，未以交叉编译替代实测。

## ceshi 定点实测（17:14–17:30，UTC+8）

环境：Chrome 网页版 EasyEDA 3.2.186，ceshi 连接器 1.4.2，开发态 CLI/daemon。
这是针对故障的定点探测，不是 ESP32 客户需求到四层 PCB 的端到端验收。

1. 初始 `doc ls` 返回 null、Board 列表为空。**后来证据证明不能据此称工程为空**：
   `createPcb()` 返回 `d77b816f0ea2a04b`，可打开为 PCB3，但总表、当前 PCB 和
   getPcbInfo 都不可读。`board create --pcb` 返回 Board1，Board 总表仍为空。
2. 第一版“当前 PCB 元数据补读”在该状态下正确拒绝，但不能恢复。补充精确 UUID
   路径：document.current 的结果与响应 context 的 UUID、项目和类型一致才通过。
   实机使用 `--doc d77b816f0ea2a04b` 的读取探针成功执行，名称枚举不可用时仍能定位。
3. 同一探针中 `sameDocument:true`，getAll 无参数/undefined/Top/Bottom/ID 枚举
   全为 0，首尾无变化。没有非空图元证据，所以**尚未复现 #200 的“有器件却空读”**。
4. 测试电阻创建失败：`Cannot convert undefined or null to object`；
   测试丝印创建也失败：`无法创建文本图元`。未成功创建用于连续移动的图元，
   因而 #190/#192 的真实移动/文件补丁/保存重开验证均未完成。
5. UI 曾显示 PCB3，随后在重载/工程入口打开时停留开始页、“暂无数据”或白屏，
   连接器却继续上报旧活动 PCB。不能将连接器在线或活动 UUID 等同于文档数据加载就绪。
6. **清理未确认完成**：删除本轮创建的 PCB 时宿主抛
   `Cannot read properties of null (reading 'data')`；后续受项目身份前置检查保护的
   Board 清理也在该错误处停止。未反复重发删除。宿主恢复后只核查/清理上述 PCB3 UUID
   和本轮 create 返回的 Board1，不批量清空其他文档。

原始本地响应：`/tmp/easyeda-issue-live-20260908/`（create-pcb / add-component / silk-add /
probe / cleanup / cleanup-rest JSON）。当前宿主问题阻塞下，保留错误和清理待办，不宣称验收完成。

补充修复的全量 `go test ./...`、文档守卫 `-race`、Skill lint / package check 全部通过。
身份不一致测试覆盖文档 UUID、类型、项目；修复测试 fixture 的 health 路径后全量复跑通过。

## ceshi 恢复复核（23:04–23:08，UTC+8）

- 当前同名工程 UUID 为 `475cc0f773ed4a6fb7a02336c8a6a67f`，与前轮不同；
  PCB1 为 `7dc1c3e4a818108c`。Board1、PCB 总表及当前 PCB 元数据均恢复可读，
  `doc open` 可打开，UI 可读取 PCB1 编辑器结构。旧工程清理待办不能套用到本工程。
- `pcb add-component` 仍报 `Cannot convert undefined or null to object`；
  `silk-add` 仍报无法创建文本图元。执行 `doc reload`（保存、关闭、重开）并将
  Chrome ceshi 标签激活后，再按名称 `--doc PCB1` 放置，仍为同一错误。
- 只读探针身份前后一致，PCB 元数据正常，器件各枚举与线段枚举全部为 0。
  没有成功放置的已知图元，仍不构成 #200 的非空板空读复现，也不能验收连续移动。
- 本轮未成功创建测试器件，不盲目删除；回读器件仍为 0。连接器为 1.4.2，
  CLI/daemon 为开发构建；版本检查提示最新发行版 1.4.3，未将旧连接器视为同版验收。
- 原始响应保留于 `/tmp/easyeda-issue-live-20260908-recovered/`，包含 add、silk、
  probe、add-after-reload。本轮仅记录实机证据，未修改代码、未重跑已通过的离线测试。

## 内置浏览器根因定位（23:16 起，UTC+8）

同一新 ceshi，宿主 3.2.186、连接器 1.4.3。文档目录、PCB 激活、器件列表均返回；
`pcb.silk.add` 在 1ms 内失败。宿主文本创建先校验字体名，然后把失败统一包装为
“无法创建文本图元”。字体列表有 default/default2 等，不含空字符串；连接器此前
传空字体和非法对齐 0（SDK 定义 1–9）。三个丝印入口已改用 default + LEFT_TOP(1)，
匹配标签碰撞估算向右/向下的边界。

- 最小探针创建成功；由修复源码 esbuild 打包的完整 runAction 经 debug.exec_js
  执行丝印创建也成功，ID 为 `6cb3aa25fa01f364`。
- 实测 bbox：x=1000..1463.1、y=1160..1200，符合左上锚点及 40mil 字高。
- 保存、关闭、重开后，两条测试文字的 ID、内容、字体和对齐回读正确。
- 按已知 ID+文字删除，再保存重开，确认测试文字数为 0。
- 新插件包尚未导入当前连接器；上述实测是修复后的完整 handler，不是旧插件自动更新。

同时复现并修复重载死循环：typed add-component 成功后，旧数据门禁挡住文档目录
及当前板元数据，导致提示中的 doc reload 自己无法执行。现在仅放行这两个身份/绑定
元数据入口。新增测试确认元数据读取不会解除器件读取门禁，真正重载后才解除。

器件库查询正常；完整对象放置成功，保存重开后读到真实电阻。简写引用加显式
rotation=0 的 typed 放置也成功。省略 rotation 的对照调用被旧数据门禁提前拒绝，
所以早先器件创建报错根因尚未确定。

待清理的测试器件 ID：`8bf023120dfe490e`、`a3ad5ae7ac0921a2`。已明确保存。
Go 热重载后连接器未自动连回，内置浏览器控制工具连续超时，已请求刷新后继续移动
验证及清理，不能记为完成。

连接器 272 项测试、TypeScript typecheck、全量 go test、Skill lint/package check
均通过；make connector 已生成本地 1.4.3 dev 包。尚未执行完整 ESP32 端到端回归。

### 连接恢复后的移动验证（23:29–23:31）

连接恢复后，typed list 读到两只已保存的测试电阻；第二只的 designator/uniqueId
也正确持久化为 R_TEST2 / issue-probe-2。对第一只执行带 UTF-8 BOM 的 patch-file，
经名称 `--doc PCB1` 定位后成功移动到 (1100,1050)，返回 verified=true。

随后 doc reload 已通过修复后的文档定位、保存和关闭阶段，但宿主 document.open
超时。回读目录确认 PCB1 仍存在，活动页为 P1；只恢复打开一次仍超时，未重复移动。
现已停止自动重试，并请求手动双击 PCB1 作对照。两只测试器件尚待清理；不能声称
本次移动已经通过重开后持久化验收，也不能把所有页面载入问题归于已修复的丝印参数。

### 显式分屏重开与清理完成（23:35–23:41）

用户手动打开 PCB1 后，API 正常读到两只电阻。再次用无 BOM 的 patch-file，按精确
UUID 将第一只移动到 (1150,1120)，返回 verified=true。保存后分步测试官方分屏接口：
`getSplitScreenIdByTabId` 返回 editor-window-main；关闭后显式将该 ID 传给
openDocument，约 1.2s 成功。再保持原 reload 的 1s 关闭间隔对照，显式分屏重开约
1.7s 成功；读回 (1150,1120)，补全第二次移动的保存重开验证。

document.open handler 现先查询当前 tab 的官方分屏 ID，非空时显式传递；不可用则
保留原单参数路径，不猜分屏、不添加重试。打开后的身份回读不匹配时 ready=false，
不再乐观声明已就绪。新增 7 项回归覆盖分屏传递、查询异常/缺失/空值、无 tab、
旧页和身份读取错误。关闭后执行由完整修复源码编译的 handler，约 1.8s 重开成功；
当次即时 ready=false，随后 response context 与 typed list 均确认 PCB1，说明宿主
激活仍有异步阶段，不能用回调返回本身代替回读。

两只测试电阻已按已知 ID 删除、保存，通过修复 handler 重开后 typed list 确认
count=0。测试丝印此前已清理；本轮新 ceshi 清理完成。旧 UUID 工程的历史清理待办
仍与此工程分开，不据此宣布已清理旧工程。

279 项连接器测试、TypeScript typecheck、Skill package check 通过；make connector
成功，检查新 eext 的 dist/index.js 包含分屏解析逻辑。本轮未改 Go，无需重复前轮已通过
的全量 Go 测试。新插件包尚未导入正在运行的连接器，实机验证通过的是编译后的完整
handler；普通 CLI 要长期使用此修复，需导入新包并重新加载编辑器。

## 热加载部署与普通 CLI 回归完成（23:49–23:57）

使用仓库既有 hot-reload-server.mjs / hot-reload-inject.js，通过 debug.exec_js
把新 dist/index.js 写入当前内置浏览器的插件 IndexedDB 并重新加载。新窗口重新连接。
最终存储与本地 bundle 的 SHA-256 一致：
`ec7abb2ed4b235abd53800cd288c8d945293ca3306f4c1cfe97d5c14683d5905`，613396 字节。
本节替代上一节“插件尚未安装”的当前状态：修复代码已在当前浏览器运行。

- 普通 CLI silk-add 成功；doc reload 完整保存、关闭、重开成功，无临时 handler。
- 普通 CLI add-component（省略 rotation）成功，随后读取位号、uniqueId 正确。
- 按 `--doc PCB1` 连续执行 BOM patch-file 和无 BOM patch-file，分别改到
  (1100,1050) 和 (1150,1120)。每次标准 doc reload 后的 typed list 坐标均断言通过。
- 删除本轮测试电阻与丝印、保存重开后，PCB 器件数为 0。
- 新建隔离原理图页 `57424b52f4abaeb2`，一根真实短线连接测试坐标。仅调用一次
  createNetLabel(605,1185,"ISSUE191_PROBE")，7090ms 时原生 Promise 仍 pending；
  之后回读 Attribute=[]、导线 net=""。因此 #191 原生问题在当前 3.2.186 仍复现。
  测试页已删除，文档目录确认仅保留原 P1 与 PCB1。
- 修复 connect_pin(net_label) 不需要却仍执行的旋转校准探针；回归在旧代码下捕获
  多余 Power/__ROTPROBE__ 创建，修复后仅走 stub 与原生 label。该修复不等于宿主
  createNetLabel 已可用。最新代码已再次热加载，最终队列 abandoned=0。

#201 已修复两处 DSH URL.pathname：改用 Node fileURLToPath。Mac Node 22.22.0
及最低 20.17.0 各 7 项测试通过，包含真实 Node Windows 转换模式下的盘符、UNC、
中文、空格、#/%；真实 DSH loader 解析 YAML、启动 stdio MCP、枚举 11 工具、离线调用
和 Skill 读取通过。Windows 原生 runner 尚未执行，不把 Mac 的跨平台输入测试称为
Windows DSH 真机验收。CI 已接入，未推送触发。

连接器全量 280 项测试、TypeScript typecheck、Skill package check 和插件构建通过。
本轮没有改 Go 实现，沿用此前全量 Go 通过结果。#190 的当前环境普通 CLI 回归已完成，
#192 的跨平台文件功能已实测，PowerShell 5.1 原生命令解析仍待对应环境。
#200 旧版宿主复现、#173 编组、#43 完整整板验收及 #199 真实 3D 导入仍未完成。

## GitHub 进度同步（2026-09-09）

- [#190](https://github.com/zhuangzard/pcbpilot/issues/190#issuecomment-5588240718)
- [#192](https://github.com/zhuangzard/pcbpilot/issues/192#issuecomment-5588241084)
- [#201](https://github.com/zhuangzard/pcbpilot/issues/201#issuecomment-5588241501)
- [#191](https://github.com/zhuangzard/pcbpilot/issues/191#issuecomment-5588241883)
- [#200](https://github.com/zhuangzard/pcbpilot/issues/200#issuecomment-5588242274)
- [PR #199](https://github.com/zhuangzard/pcbpilot/pull/199#issuecomment-5588242636)

再次对照 PR #199：原提交 982eaf6 与采用提交 cf36d45 的 dispatch.go 内容完全一致；
原 2MiB 请求测试保留，本地新增 32MiB 精确边界与多 1 字节拒绝测试，署名保留。
针对请求体的回归再次通过。当前远端尚无采用提交，因此未关闭 PR；推送并完成 CI 后
可按“已在 main 采纳”关闭，无需重复合入相同实现。

## 推送、CI 与 PR 收尾（2026-09-09）

用户要求去掉重复 push 确认限制。本地 AGENTS.md 与仓库 CLAUDE.md 已同步约定：
要求提交修复、更新 GitHub 进度或处理 PR，即授权提交并推送相关已验证改动；采纳提交
进入远端 main 且相关 CI 通过后，可关联提交关闭完整采纳的 PR。未解决的 issue 继续开放。

13 个提交已从 ed91d03 推送至 d1dc9dd；
[CI 34250243475](https://github.com/zhuangzard/pcbpilot/actions/runs/34250243475)
对应的 headSha 为 d1dc9dd6ab8305184c3c41b89a53658c57651ed2，4 个 job 全部通过：
Ubuntu 的 CLI/connector/Skill 全套与 Ubuntu/macOS/Windows 原生 smoke。

- [Windows 日志](https://github.com/zhuangzard/pcbpilot/actions/runs/34250243475/job/102142525960)
  明确记录 TestModifyPatchWindowsPowerShell51/sch 与 /pcb PASS；真实 powershell.exe
  硬校验 5.1 后，生成带空格路径的 UTF-8 BOM 文件并调用新编译的 easyeda.exe，核对
  模拟 daemon payload。这补齐 #192 的 Windows 参数解析边界；Windows 编辑器整套
  实机链路仍未运行。
- Windows DSH 路径测试 7/7 通过，包含特殊字符实际路径的 Node 入口启动与 Skill
  读取。该 Windows 测试使用测试入口文件；完整 DSH loader 的集成测试仍只在 Mac
  完成，不能将此写为 Windows 完整安装验收通过。
- [PR #199](https://github.com/zhuangzard/pcbpilot/pull/199)
  已关闭为“已采纳”，关联远端采用提交
  [cf36d45](https://github.com/zhuangzard/pcbpilot/commit/cf36d45e4c554b88cfc0686d4925a536b3373d54)，
  保留原作者署名。入口与大小边界通过，实际 3D 模型导入未验收。
- 上节列出的 6 条 GitHub 评论已原位更新，保留各 issue 的实测范围与未完成项。
  #190/#192/#201 继续跟进发布与安装/报告者复验；#191/#200/#173/#43 未解决。

本次收尾未修改产品代码，不新增发布或整板验收声明。
