---
name: pcbpilot-library
description: "用 pcbpilot 选型找料、建库与维护器件库（EasyEDA Pro 与 KiCad）：按 MPN/C 号/参数搜索现有器件，数据手册驱动新建器件、符号、封装、3D 模型，逐引线核对引脚编号与焊盘映射，导入 LCSC 器件到 KiCad 并注册工程库表，把已放置器件刷新/换绑到库里的新版本。触发：选型、找料、查 C 号、新建器件/封装/符号、建库、自定义器件、引脚编号/引脚定义对不上、封装 1 脚方向、数据手册、库表。Find parts and create or maintain symbols and footprints with pcbpilot for EasyEDA Pro and KiCad: search by MPN, LCSC number or value+package, datasheet-driven custom part / footprint / symbol creation, pin numbering and pin-to-pad acceptance, LCSC import into KiCad with portable library tables, and refreshing placed parts from the library. Use for: part selection, find an LCSC part, custom part, new footprint, new symbol, pin numbering mismatch, pin 1 orientation, datasheet pinout, library table."
license: MIT
metadata:
  author: "zhuangzard (pcbpilot); original easyeda-agent by zhoushoujianwork"
  version: "0.12.1"
  homepage: "https://github.com/zhuangzard/pcbpilot"
---

# pcbpilot-library：选型与建库

只管“器件身份”这一层：选哪颗料、它的符号/封装/3D 从哪来、引脚映射对不对、已放置的件怎么换到新库件。
画原理图、布局布线、评审和出制造文件交回路由 Skill [pcbpilot](../pcbpilot/SKILL.md)。

## 何时用 / 何时交回

- 用：选型找料、查 C 号、复用或新建器件（EasyEDA Device、KiCad 符号/封装）、核对引脚定义、导入 LCSC 件到 KiCad、
  库表注册、把已放置的件换绑到新符号/封装/器件。
- 交回 router：会话开始的 `pcbpilot health` 与连接器问题、整图绘制（`kicad sch-build` 全流程、EasyEDA `sch` 数据驱动布局）、
  原理图 → PCB 同步（`pcb import-changes`）、DRC/评审/签核、制造输出。器件选定后，放到哪里、怎么连线也交回 router。

## 硬规则

1. **先搜后建。** 顺序：`blocks search`（标准电路）→ [standard-parts.json](../pcbpilot/references/standard-parts.json)
   → `lib search` / `lib by-lcsc`（EasyEDA）或 `kicad lcsc --search`（JLC 目录）→ KiCad 自带库 → 最后才自建。
   完整 MPN 与封装都匹配才复用；搜到“差不多”的件不改名冒充。
2. **身份锁到后缀。** 厂商 + 完整 MPN（封装、包装、温度、电压档后缀）+ 供应商号 + 数据手册版本，缺一不建、不放。
   实测 `kicad lcsc --search "AMS1117-3.3"` 前三条是 `AMS1117-3.3`（SOT-223）、`-3.3RG`、`-3.3S`（SOT-89）：只差后缀，封装不同。
3. **每个新件、导入件、换绑件都要一份引脚映射验收记录**，模板与填法见 [pin-map-acceptance.md](references/pin-map-acceptance.md)：
   手册视角（TOP/BOTTOM VIEW，镜像只做一次）、逐引线 手册脚 → 焊盘号 → 符号脚号/脚名 → 电气类型、
   重复/机械/裸露焊盘的归属、建库后回读、一次性实例检查。手册有歧义就**拒绝**，结论 `INCOMPLETE`，列候选问用户。
4. **工具的成功不是验收。** `lib device validate` 的 `pinMappingVerified:true` 只比较编号集合（改脚名、改类型照样通过）；
   `lib device build` 的 `verified:true` 只证明资产已创建；`kicad lcsc --import` 成功只说明 KiCad 转换器跑完。
5. **KiCad 库文件不手改。** pcbpilot **不能**按几何参数生成 KiCad 封装（`.kicad_mod`），也不编辑 `.kicad_sym`。可用的来源只有：
   KiCad 自带库、`kicad lcsc --import`、`kicad sch-build` 用 `pins[]` 生成的方框符号（只有符号）。其余的新封装/符号修改记
   `UNSUPPORTED`，把验收记录交给用户在 KiCad 符号/封装编辑器里做，做完再按记录回读。文件规则见
   [kicad-file-safety.md](../pcbpilot/references/kicad-file-safety.md)。
6. **库表用工程级 + `${KIPRJMOD}`。** 不改全局库表，不写绝对路径；细节见 [library-conventions.md](references/library-conventions.md#7-kicad-库表注册)。
7. **参数核验保留原始单位**，不从料号数字猜值；规则在 [part-selection.md](../pcbpilot/references/part-selection.md)。

## 工作流

### 1. 选型（两个后端通用）

```bash
pcbpilot blocks search "<功能关键词>"                       # 标准电路先查块
pcbpilot kicad lcsc --search "100nF 0402" --offline         # 只查 standard-parts.json，不联网
pcbpilot kicad lcsc --search "AMS1117-3.3" --qty 500 --json  # JLC SMT 目录（联网）：C 号 / basic / 库存 / 封装 / MPN
```

排名只给候选（规格匹配 → 库存 → basic → preferred → 单价）。选定后读该 C 号的手册确认参数、封装与引脚，
写进验收记录的“器件”行。EasyEDA 侧也可用 Skill 根目录的 `python scripts/parts-select.py "<query>" --online`（同一份目录客户端）。

### 2. EasyEDA：复用或自建 Device

```bash
pcbpilot lib search --query "<完整 MPN>"          # 也可 --query C6186（C 号默认精确匹配）
pcbpilot lib by-lcsc --lcsc C6186,C25744          # C 号 → libraryUuid + deviceUuid（按当前连接的站点解析）
pcbpilot lib device get --uuid <device> --library <lib>
```

- 有完全匹配：读 Device 绑定的 symbol/footprint（`lib symbol get` / `lib footprint get`），按验收记录第 6 节核对后使用。
- 只缺封装或 3D：`lib footprint copy --source-library <l> --uuid <fp> --name <新名> --scope project` 复制后改绑；
  3D 用 `lib model3d search` / `create` + `lib device model3d --expected-name <Device 名>`。
- 必须自建：先写验收记录，再写 Device JSON（骨架 [library-device-example.json](../pcbpilot/references/library-device-example.json)，
  规格契约见 [library-authoring.md](../pcbpilot/references/library-authoring.md)），然后：

```bash
pcbpilot lib libraries                                     # 取可写的 personal / project 库 UUID
pcbpilot lib device validate --spec device.json            # 离线：证据页码、编号集合、几何合法性
pcbpilot lib device build --spec device.json               # 写入前重跑同一校验；失败尝试回滚
```

  `pinType` 用 EasyEDA 的真实取值（`IN`、`OUT`、`BI`、`Passive`、`Power`、`Ground`、`Open Collector`、`Undefined` …，
  对照表在 [library-conventions.md](references/library-conventions.md#3-引脚电气类型)）；validate 不检查它。
  有电气连接的 EP/散热片要有符号脚，只列进 `pinMapping.footprintOnly` 的焊盘是无网的。
- build 后回读三件资产、放一次性实例（验收记录第 6、7 节），再交回 router 使用。

### 3. KiCad：导入、复用、注册

```bash
pcbpilot kicad lcsc --import C6186 --import C25744 --lib-dir ./lib   # 联网；写 lib/lcsc.kicad_sym + lib/lcsc.pretty/
```

- 实测（KiCad 10.0.6，约 30 s）：符号 `lcsc:AMS1117-3.3_C6186`（4 脚：GND/VOUT/VIN/VOUT）、封装
  `lcsc:SOT-223-3_L6.5-W3.4-P2.30-LS7.0-BR`（散热片焊盘 4）。无 3D 模型；引脚类型与封装类型需按验收记录修正或记录。
- `--import` **不写库表**：已有工程把两条 `${KIPRJMOD}` 条目交给用户添加；用 `kicad sch-build` 新建的工程直接在 spec 里写
  `"lcsc": "C6186"`，sch-build 自己导入（缓存于 `~/.pcbpilot/cache/kicad-lcsc/`）并写好 `sym-lib-table` / `fp-lib-table`。
- KiCad 自带库的件在 spec 里写 `"symbol": "Device:C", "footprint": "Capacitor_SMD:C_0805_2012Metric"`；
  工程内自有库写 `"symbol": "path/lib.kicad_sym:Name"` 或在 spec `libDirs` 里列目录。自有**封装**库同理：
  `"footprint": "mylib:FOO"` 且 `mylib.pretty` 在某个 `libDirs` 目录下（或直接列该 `.pretty`），sch-build 复制并登记进工程
  `fp-lib-table`，与符号来源无关；找不到就是 `FOOTPRINT_LIB_NOT_FOUND`。这就是给 sch-build 工程加库的 typed 方式——
  不要手改 `fp-lib-table`（下次构建会重写）。
- 没有现成符号时用 `pins[]`（`number` / `name` / `type`，KiCad 类型名）生成方框符号；封装仍须来自现有库。
- 回读：`kicad-cli sym export svg` / `fp export svg --sp`、KiCad python 读焊盘（验收记录第 6 节），再在临时目录做一次性
  `kicad sch-build` → `kicad sch-read` 核对每个网落到的 `REF:脚号`（第 7 节）。

### 4. 把已放置的件刷新到库

这是“实例 ↔ 库”的刷新，和原理图 → PCB 同步是两件事；刷新后再交回 router 做 `pcb import-changes` / PCB 更新。
KiCad 能否用 `sch-edit` 取决于工程来源，规则见 [kicad-file-safety.md §1 第 4 条](../pcbpilot/references/kicad-file-safety.md#1-四条规则)。

| 场景 | 命令 | 写后必须 |
|---|---|---|
| EasyEDA：同一器件换符号 | `sch rebind-symbol --id <primitiveId> --symbol <名>`（或 `--symbol-uuid` + `--symbol-lib`） | 新 primitiveId：`sch check` / `sch drc` 查接线 |
| EasyEDA：同一器件换封装 | `sch rebind-footprint --id <primitiveId> --footprint <名>`（或 `--footprint-uuid` + `--footprint-lib`） | 同上；超时先新鲜读取对账，不重试、不 import-changes |
| EasyEDA：换成另一个器件 | `sch replace --id <primitiveId> --lcsc <C号>`（或 `--device-uuid` + `--device-lib`） | 读 `pinDiff`，非空就重接线后 `sch check` |
| EasyEDA：补真实 C 号 | `sch resolve-lcsc`（dry-run）→ `sch resolve-lcsc --apply`，多页逐页 | unresolved 逐个人工判定 |
| KiCad 原理图：换件（仅 sch-build 生成的工程） | `kicad sch-edit --project D --spec delta.json`，op `replace_part` | 门禁自动重跑；`kicad sch-read` 回读 |
| KiCad 原理图：手画或导入的工程 | pcbpilot 不改（`UNSUPPORTED`）；交用户在 KiCad 里换符号/封装 | 用户改完后 `kicad sch-read` 回读，对照验收记录 |
| KiCad PCB：封装从库更新 | pcbpilot **不支持**（`UNSUPPORTED`） | 用户在 PCB 编辑器“从库更新封装”/“从原理图更新 PCB”后，重新 `kicad snapshot` |

## 决策点

- 库里有同 MPN 但封装后缀不同 → 不复用，按正确后缀找或自建。
- 只有同系列其他后缀的手册 → 停，找到本后缀的手册或订购信息页再继续。
- 导入件与手册不一致（脚名、焊盘数、1 脚位置）→ EasyEDA：复制后自建正确版本；KiCad：记 `UNSUPPORTED`，交用户在编辑器修。
- 用户只要“选型建议”→ 只做第 1 步，交付候选表 + 每颗的手册证据，不建库、不写图。

## 报告

用 [evidence-and-verdicts.md](../pcbpilot/references/evidence-and-verdicts.md) 的结论词汇与验证状态。每颗器件报：
身份（厂商 + 完整 MPN + C 号 + 手册版本）→ 来源（复用/导入/自建）→ 验收记录位置 → 回读与实例结果 → 结论。
例：“U1 AMS1117-3.3 / C6186：KiCad 导入件，引脚映射 PASS（offline-verified，sch-read 网表 + SVG）；引脚类型 WARN（4 脚 unspecified）；3D UNSUPPORTED（导入无模型）”。

## 常见失败 → 修法

| 现象 | 原因 | 修法 |
|---|---|---|
| `lib search` / `lib by-lcsc` 报 `connector did not respond`、连接器 DEGRADED | 编辑器未响应 | `BLOCKED`；交回 router 查 `health`，不循环重试 |
| `lib device validate`：`pin mapping mismatch: footprint-only pads are [EP]` | 焊盘多于符号脚 | 有电气连接 → 给符号加脚；纯机械 → 写进 `pinMapping.footprintOnly` |
| `duplicate pad number "2"` | EasyEDA 不允许重复编号（KiCad 允许） | 散热片用独立编号 + 同名符号脚 |
| 块 `block-apply` 首个 place 超时 | `deviceUuid` 属于另一站点 | `scripts/parts-relocalize.py` 生成本机副本（[part-selection.md](../pcbpilot/references/part-selection.md#站点差异deviceuuid-必须按当前版本重解析)） |
| ERC `footprint_link_issues` / 找不到库 | 库昵称与 `Footprint` 前缀不符，或库未注册 | 按 [library-conventions.md §7](references/library-conventions.md#7-kicad-库表注册) 补工程表项；隔离配置下的同类告警属环境类（[drc-triage.md](../pcbpilot/references/drc-triage.md)） |
| `kicad fab` 拒绝：缺 C 号 | 符号无 `LCSC Part #` | `kicad lcsc --set REF=C…`（先关 KiCad、先备份）；机械件勾排除 BOM |
| `kicad-cli … export svg`：`Error creating svg file` | 输出目录不存在 | 先 `mkdir -p` |
| 贴片预览器件转了 90°/180° | 封装零度方向与 JLC 不同 | `kicad fab --cpl-overrides`，值来自本板预览 |
