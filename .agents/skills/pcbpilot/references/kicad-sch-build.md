# KiCad 原理图：写 spec → `kicad sch-build` → review-panel → 完成

> KiCad 路径下**不要**再用逐步的 `sch place / autoconnect / layout-plan / zone-arrange / titleblock …`（每步一个回合，
> 30 件板要几个小时）。你只写设计数据，`pcbpilot kicad sch-build` 一次调用（几秒）画出过门禁的完整工程。
> 完整字段、门禁与计时见仓库 `docs/kicad/sch-build.md`。

## 规则

1. **不写坐标。** spec 只有器件、网、电源轨、块、分页/分区、标题栏、意图标注；位置一律由确定性布局计算。
   唯一的位置提示是关系：`"near": "U2:VIN"`（放在该脚旁）。
2. 器件优先给 LCSC C 号（`kicad lcsc --search` 选件）；没有 C 号或离线时给 `pins`（生成方框符号），
   或 `symbol: "Device:R"` 这类 KiCad 自带符号。标准电路用块（`pcbpilot blocks search …`）。
3. 网的引脚写 `REF:脚名` 或 `REF:脚号`；电源网写 `rails`（电压、电流 → intent derive 的线宽/IR/安全）。
4. **多单元器件**（双运放、四与非门、带独立电源单元的器件）：spec 里只写一个件，`REF:脚号` 可指任何单元的脚
   （`U1:5` 是 B 单元，`U1:8`/`U1:4` 是电源单元）；sch-build 画出**全部**单元（含电源单元，ERC 不报缺单元）。
   KiCad 派生符号（`Amplifier_Operational:LM358`、`74xx:74HC00` 这类 `extends` 的）自动展平后使用。
   只放部分单元时在件上写 `"units": [1, 3]`（未列出的单元不画，其脚不能进网）；sch-read 只对**部分放置**的件
   输出 `units`，回读再构建不会多出单元。`set_value` / `set_field` / `remove_part` 作用于该位号的全部单元。
   公共（unit 0）电源脚的件不能只放部分单元：KiCad ERC 报 `missing_power_pin`（错误）。
5. 一次构建失败时读 `error.code` / `error.fix`；spec 问题是一次全部列出的（例如 `PIN_NOT_ON_SYMBOL` 会给出
   符号真实的脚号脚名），一轮改完再跑。门禁失败时 `--out` 不会被改动。

## 流程

```bash
pcbpilot kicad sch-build --spec design.json --out build/ --dry-run      # 先看计划（可选）
pcbpilot kicad sch-build --spec design.json --out build/ --requirements 需求.md
pcbpilot kicad sch-read --project build/                                # 紧凑 JSON（含每件 bbox），代替读 .kicad_sch
pcbpilot kicad sch-edit --project build/ --spec delta.json              # 之后的每个修改：一次调用一个增量
pcbpilot kicad sch-checkpoint list --project build/                     # 回退：sch-checkpoint restore N
```

门禁（全部硬性）：KiCad 网表 == spec、ERC 无错误、`kicad sch-check` 质量检查、工程级规则（电源朝上/地朝下、
去耦 ≤ 25.4 mm、无交叉、无四通结点、无悬空标签、无自动网名、层次一致、标题栏齐全）、连接 IR、intent derive；
给了 `--requirements` 还有 review-panel 设计评审（跳过要签名豁免）。产物：工程文件、`connectivity.json`、
`intent.json`、`erc.json`、`sch-review.json`、报告里每阶段计时。

### 设计评审门禁 `sch-review`（v0.12 起）

每次 sch-build / sch-edit 在**暂存的最终文件**上跑一遍 `kicad sch-check --review` 的同一套审查（用本次的网表、ERC
和刚推导的 `intent.json`），报告 `sch-review.json` 与工程一起原子提交，`gates[]` 里是 `sch-review`：

- `--sch-review advisory`（默认）：只报告，不阻断——未豁免的 error、cannot-check 或 likely warning 都记为 `warn`，
  增量修复照常可写；
- `--sch-review strict`：只有 proven error 和 cannot-check 让构建失败（`SCH_REVIEW_FAILED`，`--out` 不动，失败工程
  与 `sch-review.json` 在 `.pcbpilot/sch-build/failed/`）；likely warning、not-applicable 只报告（`warn`）；签核、
  `pcbpilot-review` 用 strict；
- `--sch-review off`：跳过（gate 为 `skip`），`sch-review.json` 换成一条 skipped 记录，旧报告不会留在新图旁边；
- 审查本身跑不了（intent 读不出、工具错误）不是发现：任何模式都整笔不写（`SCH_REVIEW_FAILED`，消息含
  `tool/input error`），`sch-review.json` 写不出同样阻断。

审查项、证据、豁免与退出码见 `pcbpilot kicad sch-check --help`；要点：只有 proven 的
发现是 error；internal pull-up、测试点、参考轨等**角色**写在 spec 的 `intent.review`（经 `intent derive --spec`
进 `intent.json`，字段见 [design-intent.md](design-intent.md#review)），或在符号字段 `ReviewRole` /
`InternalPullup` 上声明——低电流本身从不豁免。接受一条复核过的发现：`--waivers` 里
`{"gate":"sch-review","match":"<finding.fingerprint>","reason":…,"by":…}`（按 fingerprint 精确匹配，不按文字；
豁免不会把 cannot-check 变成 pass）。报告 `verdict`：`pass` / `pass-with-warnings`（有未豁免的 warning，覆盖不全，
不写“已验证”）/ `pass-with-waivers` / `fail` / `cannot-check`；每条降级的发现都单独列出 `missingEvidence`。
`sch-review.json` 的 `summary`：`errors`、`cannotCheck` 全列，`groups` 把 warning/info 按检查项 + 缺失证据分组（每组计数、
按器件分布、几个示例脚和 fingerprint）；`checks[].findings` 仍保留每一条，门禁消息与 sch-check 的 stderr 用分组形式。

### 大板与分页（v0.10.1 起）

- **自动分页**：某页的区放不进 A0 时自动拆成 `<页>-2`、`<页>-3`…（区不拆开，按阅读顺序装页，标题加 `(k/n)`；页名里的 `/` 在网名里按 KiCad 规则写成 `{slash}`，
  netlist / pcb-sync / parity 一致，v0.12.1 起不再因此 E_VERIFY_PARITY），
  跨页网自动变层次标签 + 根图图纸引脚；根图符号自动换行进 A0。保存的 spec 仍是你写的原始分页。
  报告 `warnings` 列出 `page … split into N pages`；单个区自己就超过 A0 会点名，要在 spec 里拆这个区。
- **密集区**：一个区画不干净（质量/EG-06 失败）时可试 `--auto-split`（按 IC 拆子区，只在问题变少时保留；默认关，
  会更慢、可能多走线交叉）。最稳的写法仍是在 spec 里每个 IC 簇一个区。
- **失败如实报告**：任何失败报告都是 `ok:false` + `error`。`SHEET_NOT_LOADED` = KiCad 加载不了某页（通常是
  MPN/value 里有 KiCad 不接受的字符），不是布局问题，不要用 `--no-planner` 去绕。
- **ERC 用隔离的 KiCad 配置**（空的全局库表，避免加载全部官方库卡死）；用户自设 `KICAD_CONFIG_HOME` 优先，
  `PCBPILOT_KICAD_ISOLATE=0` 关闭。因此 ERC 对官方库只报 warning；但**工程自带的库**（`lcsc`、项目库、
  `pcbpilot_*`）报 footprint/符号链接问题时 ERC 门禁失败（v0.10.1 的 sch-edit 会丢 `fp-lib-table` 和
  `lcsc.pretty`，已修复）。
- **可复现**：`--planner-timeout` 按墙钟决定哪些区用 planner；同一 spec 要逐字节可复现时用 `--no-planner --fresh`。
- **planner 预算**（v0.12.1）：`--planner-timeout` 是整个布局阶段的一个截止点（含分页重排），只阻止新的 planner 启动；
  已启动的跑完候选上限，所以大 spec 可能超时，`warnings` 会写明。大 spec（几百个区）质量/EG 门禁失败、`error.fix`
  提示 `--no-planner` 时，先用 `--no-planner` 重建一次（实测上千件的大板：planner 布局超时三倍且 EG-08 失败，网格几秒内通过）。
- **项目封装库**（v0.12.1）：`"footprint": "mylib:FOO"` 的 `mylib.pretty` 放在 spec `libDirs` 列的目录下（相对路径相对 spec 文件所在目录，不是当前目录；绝对路径原样；或直接列
  `…/mylib.pretty`），不论符号来自 LCSC / 自带库 / pins，都会随工程复制并登记进 `fp-lib-table`（`${KIPRJMOD}`）。
  找不到 → `FOOTPRINT_LIB_NOT_FOUND`（构建失败，不再静默）。不要手改 `fp-lib-table`：每次 sch-build / sch-edit 都会重写它。

### GUI 改动保护（v0.10.2 起）

- sch-edit 和对已有 `--out` 的 sch-build 会从存储的 spec 重画整个工程。上次构建后在 KiCad GUI 里改过的
  图纸/工程/库文件（改动、新增、删除；含子目录，以及会覆盖一个不是 pcbpilot 写的文件）会被拒绝：`EXTERNAL_EDIT`，
  列出文件，什么都不写。比对依据是构建时按暂存内容记录的 sha256 清单（`state.json` 的 `manifest`）；旧工程没有
  清单时比对 checkpoint，都没有也拒绝。构建期间工程被保存 = `CONCURRENT_CHANGE`；读不了文件 = `PROJECT_UNREADABLE`。
- 要保留 GUI 改动：`sch-read --project build/ --out edited.json` 导入成 spec，构建到新的 `--out`。确定要丢弃：
  加 `--discard-external-edits`（报告列出将覆盖的文件，并先把全部工程文件存一个 checkpoint，可 `sch-checkpoint restore N` 找回）。
- 任一 `~*.lck` 锁文件 = 工程还在 KiCad 里打开：`KICAD_OPEN`，先在 KiCad 保存并关闭（覆盖开关不跳过它）。
  `.kicad_prl`、`_autosave-*`、`*-bak`、`*-backups/`、`.history/` 等 KiCad 附属文件不算改动；kicad-cli 不改 `.kicad_pro`。
- 只有 `.kicad_pro` 变了：KiCad 保存工程（如保存 PCB）时会把全部默认设置写进去。没在 KiCad 改工程设置（网络类、
  文本变量、板设置）时 `--discard-external-edits` 是安全的。
- 报告文件（`erc.json`、`connectivity.json`、`values.json`、`intent-spec.json`、`intent.json/md`、`render/*.svg`）
  不是设计数据：被改过或旧工程没记录过时不拒绝，先存进 checkpoint，报告 `warnings` 里 `WARNING: report(s) …`
  点名并给出 checkpoint 目录，再重新生成。要找回就从那个目录手工拷回（不要整体 restore 那个 checkpoint）。

别的工具画的 KiCad 工程也能过同一套工程级门禁：`pcbpilot kicad sch-check --sch root.kicad_sch --engineer-grade --spec design.json`
（spec 提供网类型、区、页、标题栏；EG-06 按区找 IC 电源脚）。

delta 例子（只写意图，脚名即可）：

```json
{"ops":[{"op":"add_part","part":{"ref":"R10","value":"10k","symbol":"Device:R","zone":"mcu","near":"U3:IO8"}},
        {"op":"connect","net":"I2C_SDA","pins":["U3:IO8","R10:2"]},
        {"op":"rename_net","from":"LED_CTRL","to":"LED_EN"}]}
```

已有 EasyEDA 设计迁到 KiCad：`pcbpilot kicad sch-build --from-connectivity p1.json --from-connectivity p2.json --out dir/`
（连接 IR 1.4 来自 `pcbpilot sch connectivity`）。

例子：仓库 `examples/kicad-sch-build/esp32-mini.json`（esp32MiniRequire.md 的 31 件板，块 + LCSC）。
