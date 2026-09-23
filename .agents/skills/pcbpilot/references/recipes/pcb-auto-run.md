# 配方：整板自动设计的执行、判读、迭代与落地

只有用户**明确**要求整板自动布局/布线时使用（见 [collaboration-workflow.md](../collaboration-workflow.md)）。
引擎说明见 [pcb-auto.md](../pcb-auto.md)。

## 前置条件

1. 逐焊盘对账通过（[schematic-to-pcb.md](schematic-to-pcb.md)，退出码 0，或 2 且差异已向用户说明）。
2. `mech.json`（[mech-spec.md](mech-spec.md)）与 `power.json`（[power-spec.md](power-spec.md)）已写好并记录来源。
3. 板上若已有走线/铺铜：`pcbpilot pcb clear --only routing,copper --dry-run` 报告数量，**经用户确认**后再清；
   引擎规划默认从空铜开始。

## 1. 生成计划（不写编辑器）

```bash
pcbpilot pcb dump --include-copper --project <P> --doc <PCB> --out board.json
pcbpilot pcb auto run --board board.json --mech mech.json --power power.json \
  [--place] [--layers N] --out-dir out/
```

| 参数 | 何时用 |
|---|---|
| `--place` | 器件未布局或用户要求重排；不加则沿用现有布局只布线 |
| `--place --refine` | 已有较好布局，只做局部优化 |
| `--layers N` | 需求或成本已定层数（例如需求写明 4 层）；不给则引擎决策 |
| `--max-layers N` | 成本上限（默认 6） |
| `--seed N` | 布局随机种子；同输入同种子结果逐字相同，可换种子比较方案 |
| `--no-route` | 只看布局与层数，先和用户确认再布线 |
| `--timeout D` | 单次布线时间预算（默认 4m）；大板可加长 |
| `--grid G` | 布线栅格 mil（默认按线宽+间距推导，一般不改） |

## 2. 判读（按顺序，任何一项不满意就回去改输入，不要直接 apply）

1. **层数与理由**（报告第 1 节）：理由是否成立；“尝试过的叠层方案”里是否升级了混合层。
2. **电流来源**（第 2 节）：不能有 `heuristic` 残留（除非用户接受估算）。
3. **电路理解**（第 3 节）：功能块归属是否合理；电压域与隔离是否符合实际（参考 [hv-isolation.md](hv-isolation.md)）。
4. **布局**（第 4 节）：重叠/出板/越区/禁布 = 0；去耦平均距离通常 < 100 mil。
5. **布线**（第 5 节）：布通率、未布通原因；精确 DRC 必须 = 0（≠0 属已知缺陷 R-0，**不得 apply**，先报告）。
6. **高速**（第 6 节）：skew / vias / split-crossing 逐条处理（[high-speed.md](high-speed.md)）。
7. **预览** `out/preview.svg`：接口朝外、隔离带无穿越、黄色虚线为未布通。

未布通时的调整顺序：先改布局（`--place`、换 `--seed`、放宽板框或 `autoSize`）→ 再加层（`--layers`）→
最后才考虑降低线宽/间距要求（需用户同意）。**不用 GUI 手工补线**。

## 3. 落地

```bash
pcbpilot apply out/playbook.json --project <P> --doc <PCB> --dry-run
pcbpilot apply out/playbook.json --project <P> --doc <PCB>
pcbpilot pcb save --project <P> --doc <PCB>
pcbpilot doc reload --project <P> --doc <PCB>
pcbpilot pcb drc --project <P> --doc <PCB>
pcbpilot pcb check --project <P> --doc <PCB>
pcbpilot pcb dump --project <P> --doc <PCB> --out after.json     # 再做一次逐焊盘对账
```

剧本顺序：设层数 → 板框/孔/禁布区 → 器件位姿 → 走线与过孔 → 铺铜 → 把实心平面翻成内电层 →
重铺 → 保存 → 原生 DRC。中途失败时 `pcbpilot apply ... --resume` 从断点继续；超时的写入步骤先
回读确认是否已落地，不盲目重放。

## 4. 验收与状态

- 原生 DRC、`pcb check` 与引擎 DRC 分别报告，不互相替代。原生 DRC 的同网 GND Connection
  Error 先 `pcbpilot pcb pour-rebuild` 再判（见 [pcb-routing.md](../pcb-routing.md)）。
- 保存、重载、回读都通过后才可称 `live-verified`，并在 [pcb-auto.md](../pcb-auto.md) 回填实测记录。
- 已知限制：BGA 逃逸未实现（大 BGA 板布通率低）；超时路径下最终闸门漏检扇出过孔（R-0）。
