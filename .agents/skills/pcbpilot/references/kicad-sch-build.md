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
`intent.json`、`erc.json`、报告里每阶段计时。

### 大板与分页（v0.10.1 起）

- **自动分页**：某页的区放不进 A0 时自动拆成 `<页>-2`、`<页>-3`…（区不拆开，按阅读顺序装页，标题加 `(k/n)`），
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

delta 例子（只写意图，脚名即可）：

```json
{"ops":[{"op":"add_part","part":{"ref":"R10","value":"10k","symbol":"Device:R","zone":"mcu","near":"U3:IO8"}},
        {"op":"connect","net":"I2C_SDA","pins":["U3:IO8","R10:2"]},
        {"op":"rename_net","from":"LED_CTRL","to":"LED_EN"}]}
```

已有 EasyEDA 设计迁到 KiCad：`pcbpilot kicad sch-build --from-connectivity p1.json --from-connectivity p2.json --out dir/`
（连接 IR 1.4 来自 `pcbpilot sch connectivity`）。

例子：仓库 `examples/kicad-sch-build/esp32-mini.json`（esp32MiniRequire.md 的 31 件板，块 + LCSC）。
