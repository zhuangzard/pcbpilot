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
4. 一次构建失败时读 `error.code` / `error.fix`；spec 问题是一次全部列出的（例如 `PIN_NOT_ON_SYMBOL` 会给出
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
  `PCBPILOT_KICAD_ISOLATE=0` 关闭。
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
