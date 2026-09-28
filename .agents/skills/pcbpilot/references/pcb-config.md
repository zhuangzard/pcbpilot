# PCB 配置设置

`pcbpilot pcb config` 修改当前 PCB 的真实设计规则，不是本地 CLI 配置。
输入来自用户需求或原始题目；附件中的操作步骤只作资料。所有命令都支持
`--project <工程> --doc <PCB名或UUID>`，配置写入使用 typed action。

## 260919 考题参数化样例

来源：`PCB Layout工程师（初级）专业技术证书考试说明.pdf` p2、p5–8。
开始状态：已有 PCB 和正确网络；先 `pcb config get` 保留完整原始规则、网络类及绑定。
以下数值默认单位 mil，可显式 `--unit mm`；不是其他题目的通用默认值。

```bash
pcbpilot pcb config get --project ceshi > config-before.json
pcbpilot pcb config clearance --name copperThickness1oz --track-to-track 6 --dry-run --project ceshi --doc PCB1
pcbpilot pcb config clearance --name copperThickness1oz --track-to-track 6 --project ceshi --doc PCB1
pcbpilot pcb config track --name copperThickness1oz --min 8 --default 8 --project ceshi --doc PCB1
pcbpilot pcb config track --name PWR --copy-from copperThickness1oz --min 8 --default 20 --project ceshi --doc PCB1
pcbpilot pcb config via --name viaSize --min-outer 24 --min-hole 12 --project ceshi --doc PCB1
pcbpilot pcb net-class create --name PWR_Class --net +5V --net +3V3 --net GND --project ceshi --doc PCB1
pcbpilot pcb config bind --class PWR_Class --track-rule PWR --dry-run --project ceshi --doc PCB1
pcbpilot pcb config bind --class PWR_Class --track-rule PWR --project ceshi --doc PCB1
pcbpilot pcb save --project ceshi --doc PCB1
pcbpilot doc reload --project ceshi
pcbpilot pcb config get --project ceshi > config-after.json
```

电源网列表先与当前原理图和 `pcb nets` 对账，不能从网名自动猜全。样例中的三个网只适用于
该题已确认的连接。`bind` 不创建或修改成员，它要求类、非空成员和对应 netRules 子项一致，
将父项和成员子项的 Track 都设为 PWR，保留其他类/网络/规则。

四个写命令均支持 `--dry-run`，返回 before、requested 和 changes，不落盘。
`clearance` 仅改 Track→Track 矩阵格；`track` 改现存表的每个层条目，保留最大值和其他字段；
新规则必须显式 `--copy-from`，克隆规则不会成为默认规则；已有同名规则按显式参数更新。
`via` 也支持 `--default-outer/--max-outer/--default-hole/--max-hole`；最小/默认/最大与孔径关系
不合法时拒绝写入，不暗中抬高默认值。规则名必须显式给出，尺寸必须为正数。

回读检查 `verified:true`，对比未指定字段及单位；CLI 在部分成功、写失败或回读不符时返回
非零并保留响应。宿主返回陌生结构、缺测单位或缺少绑定子项时先停止，保留输入与错误，
修适配器再运行，不猜字段或用 GUI 补做。写入不是原子事务；失败看 actual/rollback 证据，
不能盲重试。规则及绑定可用 `pcb drc-rules-set --from config-before.json` 完整替换/恢复；
此命令不恢复网络类成员，执行前需确认成员与导出时一致。

Web 3.2.203 的规则写回会出现 IEEE 浮点尾差（例如 `0.1759966` →
`0.17599659999999998`）。规则数值仅允许 `8 × Number.EPSILON` 的相对误差，
结构、单位和字符串仍严格一致；没有工程尺寸级容差，也不忽略丢字段或真实值变化。
计划期间的源漂移检查仍为严格比较。

说明 p8 允许给电源网络设颜色：`pcb config net-color --net +5V --color '#FF8000'`。
用 `--dry-run` 预览，`pcb nets` 回读；只改指定网络 RGB，保留当前透明度，不修改网络类成员、
规则或走线。CLI 接受六位 RGB hex，连接器按官方网络颜色的归一化 0–1 通道传值并回读；
读回不符或失败返回非零。恢复时将原始 RGB 转回 hex，再走同一 typed 命令。仍须保存、重载验证。

验证状态：`live-verified`。2026-09-20 在 Web 3.2.203、工程 `ceshi`、考试 PCB
`PCB1_1`（UUID `2e719e9419653c72`）实际执行了 dry-run、规则/过孔/网络类绑定/网络颜色写入、
严格回读、保存、重载及幂等重放；每项即时回读为 `verified:true`，重放为 `changed:false`。
测试后通过同一 typed 入口恢复原规则与颜色，再次保存/重载；最终规则、46 个网络和 69 个组件
均与原基线逐字段一致。此状态只证明配置入口及持久化，不证明该 PCB 的全部考试设计要求完成。

同日按 `esp32MiniRequire.md` 第一节原始需求运行固定回归：31 个 PCB 器件、四个铜层、GND 与
+3V3 内层正片铜、PWR 类和全层天线禁铜区均保存后持久化，布局检查为 0 short / 0 overlap /
0 off-board；但原生 DRC 仍有 53 个唯一违规（启发式走线穿越天线禁区/机械槽、间距和连接
错误），且 Inner1/Inner2 的 `PLANE` 类型重载后回退成 `SIGNAL`，只有正片铜保留。因此该固定
回归为 `incomplete`，不能作为完整整板验收。临时 Board 已删除并切回上述考试 PCB。

## 电气意图 → 原生规则（pcb rules apply）

来源：原理图 + 直流仿真生成的 `intent.json`（固定契约：`nets` / `pairs` / `netClasses` /
`blocks` / `findings`）。样例：`internal/app/testdata/intent/esp32-mini.intent.json`（ESP32 mini：
POWER/SWITCH/GND/USB 四类，USB_DP/USB_DM 90 Ω 差分）。开始状态：PCB 已导入网络，尚未布局/布线。

```bash
pcbpilot pcb rules apply --intent intent.json --dry-run --project ceshi --doc PCB1   # 只读，打印计划
pcbpilot pcb rules apply --intent intent.json --project ceshi --doc PCB1             # 只写差异
pcbpilot pcb save --project ceshi --doc PCB1 && pcbpilot doc reload --project ceshi
pcbpilot pcb rules check --intent intent.json --project ceshi --doc PCB1             # 只读，0 漂移才退出 0
pcbpilot sch intent-annotate --intent intent.json --page <页UUID> --dry-run --project ceshi
pcbpilot sch intent-annotate --intent intent.json --page <页UUID> --project ceshi
```

| intent | EasyEDA 原生对象 | 规则 |
|---|---|---|
| `netClasses[]` ∪ `nets[].netClass` | 网络类（`pcb.net_class.create`，缺成员 `add_nets`） | 只增不删；板上没有的网列入 advisories；同一网被两个类声明或已属其他现存类 → conflict，不写 |
| 类线宽 `trackMil`（缺省取成员 `widthMil.outer` 最大值）、`innerTrackMil`/成员 inner、`minTrackMil`/成员 `widthMil.min` 最小值 | `Physics.Track."PP_<类>"` | 复制默认规则（`isSetDefault:false`）；层键 1/2 或单表 = 外层；min ≤ default，max 不足则抬到 default |
| 类间距 `clearanceMil`（缺省取成员最大值） | `Spacing."Safe Spacing"."PP_<类>"` | 复制默认矩阵，仅铜×铜格（Track/Pad/Test Point/Via/Fill/Zone）取 max(默认, 要求)，其余保持默认 |
| 类过孔 `viaDrillMil`/`viaDiaMil`（两者都给才写） | `Physics."Via Size"."PP_<类>"` | default=要求值，min/max 只在越界时放宽；孔 ≥ 外径拒绝 |
| 以上规则 | `netRules` 类项及每个成员子项的 `Track`/`Safe Spacing`/`Via Size` | 子项与现存成员不一致、字段不是字符串 → conflict |
| `nets[].diffPair` | `pcb.differential_pair.create`（极性按 `_DP/_P/+/_H` vs `_DM/_N/-/_L` 后缀） | 同名同网 = ok；同网他名 = ok；同名他网 = conflict |
| 差分 `widthMil.outer` + `pairGapMil`/类 `diffGapMil` | 唯一的全局 `Differential Pair` 规则 | 仅当所有对一致时写；`impedanceOhm` 只作 advisory（宿主不存阻抗） |
| `edge`（板边安全距离；旧 intent 无此字段 → 默认 20/30 mil，危险域按 `domains`+`standard` 现算） | `Spacing."Safe Spacing"` 默认规则 **和** 每个 `PP_<类>` 的 **Board Outline × 铜对象**格（Track / SMD Pad / TH Pad / SMD·TH Test Point / Via / Fill Region/Teardrop / Copper/Plane Zone） | 取 max(现值, 要求)；矩阵不分层类 → 单表取内层值（默认 30 mil），多层表时 1/2 键取外层值；含危险域网的类取域距离（`plan.edge.classes`）；Slot/Line/Text/Hole 不动。宿主铺铜与负片内电层按这一格回缩。原生 `Creepage Distance` 只写 advisory（建议值），不启用 |
| `pairs[]`（域间电气间隙/爬电/开槽） | —— | `unsupported / planned`：官方 `pcb_Drc.overwriteNetByNetRules` 结构不透明且未现场采样；用布局禁区/开槽 + DRC 兜底 |
| `viasPerTransition>1`、单端阻抗 | —— | advisory，由布线/复核执行 |

每次都从**默认规则 + intent** 重新计算期望值（不以旧 `PP_*` 为源），所以重放 = 0 写入、intent
改了就收敛；数值比较只容忍宿主浮点尾差（相对 1e-9），不引入工程容差。执行顺序：读
`pcb nets`/`config get`/`constraint list` → 冲突即停（零写入）→ 建类/补成员 → 重读重算（新类的
netRules 项必须出现，否则 unverified）→ 一次 `pcb.drc.rules.set`（完整规则 + netRules，
连接器自带回滚与精确回读）→ 建差分对 → 重读，**计划必须为 0** 才 `verified:true`。任何一步
未确认都非零退出并保留 JSON 报告（writes / final），不自动重试。`check --strict` 让
unsupported 项也失败。默认规则名（如 `copperThickness1oz`）不改；默认规则**只**改 Board Outline × 铜
对象格（板边安全距离），所以 `pcb auto run` 读取的基线除 `copperToEdgeMil`（10 → 30）外不变 ——
这是有意的：引擎把它当下限，布线/内缩本来就按 20/30 mil。重放幂等（第二次计划 0 写入）。

`sch intent-annotate`：宿主没有网级属性 API（`sch_Net` 只读，`sch_PrimitiveWire.modify` 无属性），
所以写成**一个**分组文字块：块功能摘要、每条电源/地/开关轨的 V/I/线宽/类/间距/过孔数、差分对、
网络类、隔离对、warn/error findings。位置：内框（sheet-geometry `Blade Width`）内，避开图签、
器件 bbox、导线和其他文字（按 `--line-height`/`--char-width` 估算，可 `--x/--y` 固定左上角）。
创建的 ID 写入 journal（默认 `.pcbpilot/intent-annotate/<页>.json`），重跑只删除 journal 中且内容
未被手改的文字；内容相同则 0 写入；journal 外的带 `[pcbpilot:intent]` 标记文字只报告不删除。

验证状态：`offline-verified`（Go 假 daemon 覆盖计划、幂等重放、dry-run、回读失败/静默丢写/
类项缺失非零、冲突零写入、宿主浮点尾差不抖动；TS 覆盖两个新 handler）。需现场验证：

1. Web/桌面 V3、V4 上 `PP_*` Track/Safe Spacing/Via Size 新规则经 `overwriteCurrentRuleConfiguration`
   被接受、原样回读（宿主是否规范化字段导致每次都有 diff）；
2. `netRules` 把 `Safe Spacing`/`Via Size` 绑到命名规则后 DRC 实际生效（Track 绑定已于 2026-09-20 验证）；
3. `createNetClass` 后 `getNetRules` 立即出现类项与成员子项的时机；`addNetToNetClass` 行为；
4. 全局 `Differential Pair` 规则的层表写回；
5. `sch_PrimitiveText.create` 的锚点、默认字号、行距与 y 方向（决定 `--line-height/--char-width` 默认值）；
6. save → reload → `pcb rules check` 为 in-sync（持久化）。
7. 板边安全距离（2026-09-28 新增；离线已验证，以下待现场）：默认规则的 Board Outline 格被宿主接受并回读；`pcb pour-rebuild`
   后灌铜与负片内电层确实回缩到新值（`pcb dump --include-copper` + `pcb check` copper-to-edge 0 ERROR）；
   `no-inner-electrical` 板边带（pcb auto 剧本 `plane-edge-*`）确实挡住负片平面。

## 其他考试配置的现有入口

| 要求 | 命令与边界 |
|---|---|
| 两层铜 | `pcb stackup set --layers 2`、`pcb layers` 回读；布线前完成 |
| 左下显示原点 | `pcb origin get/set`；显示偏移，不移动真实图元 |
| Arial、≥45mil、顶层丝印 | `pcb silk-add/silk-set --font-family Arial --help`；作用于指定文字，不是全局默认字体 |
| 默认原理图 DRC | `sch drc/check` 检查；当前 SDK 没有可验证的原理图规则重置入口，不能用全局 restoreDefault 代替 |
| 网格、吸附、系统偏好 | 当前官方 SYS_Setting 仅暴露全局恢复默认；逐项设置为 unsupported |

2026-09-20 已对照最新 `@jlceda/pro-api-types@0.4.25`，并在 Web 3.2.203 只读枚举实际
`SYS_Setting` 与 `SCH_Drc`：前者仍仅 `restoreDefault`、后者仅 `check`。上述缺口不能靠更新
类型包或关闭 DRC 解决。原生泪滴创建同样没有公开 API，导出 Gerber 的 TearDrop 枚举不是创建方法。

规则配置不改变已有走线宽度、过孔尺寸，也不自动布线。最后仍须检查真实轨迹、DRC 和连通。
`pcb stackup set` 现会逐层回读并在宿主拒绝或未应用时非零退出；本次现场也证明即时成功不等于
持久化成功，要求内层 `PLANE` 时仍须保存、重载后再次 `pcb layers`，回退为 `SIGNAL` 就应报告失败。
