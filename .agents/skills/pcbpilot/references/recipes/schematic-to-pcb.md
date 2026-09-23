# 配方：原理图 → PCB 交接与逐焊盘对账

目的：让 PCB 上每个焊盘的网络与原理图每个物理引脚的网络**逐一相等**，再开始布局布线。
“导入成功”“器件都在”不能证明这一点。

## 前置条件（任一不满足就停）

- 原理图已通过 `sch check` / `sch bridge-check` / `sch drc`，已 `sch save` 且 `saved:true`。
- 所有物理引脚都有网络或明确 NC（缺失连接不能靠 PCB 端补）。
- Board 已关联原理图与 PCB（`pcbpilot board --help` 查看与核对）。

## 步骤

```bash
# 1. 导入（自动同步属性与位号修复；--help 查看开关）
pcbpilot pcb import-changes --project <P> --doc <PCB>

# 2. 保存并重载，拿权威状态
pcbpilot pcb save --project <P> --doc <PCB>
pcbpilot doc reload --project <P> --doc <PCB>

# 3. 找到这块 PCB 绑定的原理图，只读它的页面
pcbpilot board list --project <P>          # 记下 PCB 对应的 schematicUuid
pcbpilot sch pages --project <P>           # 取 parentSchematicUuid == 该值 的页面 UUID
pcbpilot sch connectivity --page <页UUID> --project <P> > sch-<页>.json   # 每页一次
pcbpilot pcb dump --project <P> --doc <PCB> --out board.json

# 4. 逐焊盘对账（Skill 自带脚本）
python3 scripts/pad-net-diff.py --sch sch-p1.json --sch sch-p2.json ... --pcb board.json \
  --ignore-refs <安装孔/Logo 位号>
```

工程里只有一个原理图时可以用 `sch connectivity --all-pages`；**有多个原理图（多个 Board）时
`--all-pages` 会把各原理图的同名页混在一起并报 `duplicate component`**，必须按上面逐页读取。

退出码：`0` 完全一致；`2` 只有网名大小写差异（EasyEDA 可能在 PCB 侧把网名转成大写），逐条报告，
不写成“通过”；`1` 有真实差异。

> 实测（2026-09-22，pic0rick PCB1，EasyEDA 3.2.149）：100 个器件、373 个焊盘全部对上，
> 仅 `Pdamp`/`PDAMP` 6 个焊盘大小写不同 → 退出码 2。`live-verified`（只读）。

## 读结果

| 项 | 含义 | 处理 |
|---|---|---|
| `missingOnPcb` | 原理图有、PCB 没有的器件 | 查是否没导入（确认弹窗未点）、封装缺失或被排除出 PCB；修后重导 |
| `extraOnPcb` | PCB 有、原理图没有 | 残留旧器件或机械件；机械件放进 `--ignore-refs`，残留件在原理图侧确认后再删 |
| `pinWithoutPad` | 引脚在封装里找不到对应焊盘号 | **符号脚号与封装焊盘号映射错**；查器件的 symbol→footprint 映射，不改网络掩盖 |
| `padWithoutPin` | 焊盘在符号里没有对应引脚 | 散热焊盘 / 外壳脚：确认是否应接 GND，在原理图补连接或明确 NC |
| `netMismatch` | 同一引脚两侧网络不同 | PCB 还是旧网表（重导）或原理图名有歧义（同名网被合并） |
| `netCaseOnly` | 只差大小写 | 检查原理图里是否有大小写不同的同名标签被合并；统一命名后重导 |
| note: 未连接引脚 | 原理图引脚既无网络也无 NC | 回原理图补齐，不能在 PCB 上“先布了再说” |

结果为 `OK` 才进入布局：[mech-spec.md](mech-spec.md)、[power-spec.md](power-spec.md) →
[pcb-auto-run.md](pcb-auto-run.md)，或按 [collaboration-workflow.md](../collaboration-workflow.md)
交给用户手工布局。

## 常见错误

- 导入命令返回成功但器件没出现：EasyEDA 弹出了确认对话框。确认后重跑步骤 2–4。
- 对账用的是导入前的 `board.json`：永远在 `doc reload` 之后重新 dump。
- 位号被改（`U?`）：`import-changes` 默认会做位号修复；仍异常时 `pcbpilot pcb sync-designators --help`。
