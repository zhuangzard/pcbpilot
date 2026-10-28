# 实测运行记录（仿真 → 报告全链）

> 用途：照着跑一遍，或对照自己的结果判断“是工具/输入问题还是设计问题”。两条链都在 2026-10-26 用 pcbpilot 0.10.x
> 开发构建、ngspice-47、KiCad 10.0.6（Windows）实际执行；未安装 ElmerSolver。验证状态 `offline-verified`。
> 输入路径是源码仓库里的 fixture 与 KiCad 自带 demo（复制到临时目录再跑）。

## 目录

1. [链 A：ESP32-S3 mini（同版本原理图 + 布线后板）](#链-aesp32-s3-mini同版本原理图--布线后板)
2. [链 B：KiCad 自带 demo（pic_programmer）](#链-bkicad-自带-demopic_programmer)
3. [从这两次运行学到的判读要点](#从这两次运行学到的判读要点)

## 链 A：ESP32-S3 mini（同版本原理图 + 布线后板）

输入：仓库 `internal/app/testdata/esp32-v05/`（两页 connectivity + `values.json`）与同版本布线后板
`pkg/postsim/testdata/esp32mini/board.json`（4 层，IN1 无铜对象）。

```bash
pcbpilot sim tools check                                    # ngspice ok v47；elmer missing（optional）→ 退出 0
pcbpilot sim power --connectivity sch-905bb85957eaf435.json --connectivity sch-950ae6609e91d753.json \
    --values values.json --out sim.json --report sim.md --spice sim.cir --spice-check
pcbpilot intent derive --connectivity … --values values.json --sim sim.json \
    --out intent.json --report intent.md --analog-out analog.json
pcbpilot sim post-layout --board board.json --sim sim.json --intent intent.json \
    --out post.json --report post.md --svg-dir heatmaps --elmer-check
pcbpilot report design --project-name "ESP32-S3 mini" --customer Demo --intent intent.json --sim sim.json \
    --analog analog.json --post post.json --board board.json --values values.json --spice sim.cir --out-dir reports/esp32 --no-zip
pcbpilot report manual --board board.json --intent intent.json --sim sim.json --post post.json --out manual.html
pcbpilot signoff --board board.json --intent intent.json --sim sim.json --connectivity … --values values.json \
    --report reports/esp32/v1/report.json --manual manual.html --out-dir signoff
```

| 步骤 | 实际结果 |
|---|---|
| `sim power` | 6 个场景（typical / peak / buttons-pressed / terminal-only / usb-only / worst），0 warning；+3V3 3.318 V，峰值 0.521 A；`spiceCheck` 16 个节点最大差 0.59 µV，pass |
| `intent derive` | 9 块、21 网、1 域；0 error / 3 warn（Q2 集电极脉冲 230 mA、L1 峰值余量 11 %、USB 预算 86 %）；顺带 `sim analog` 4 块、7 次 ngspice 运行，WARN |
| `sim post-layout` | PASS，约 19 s；最坏压降 USB_VBUS 16.87 mV / 预算 99 mV；最大过孔 0.50 A；板最高 39.0 °C；Elmer `skipped`，输入包写在 `elmer/` |
| `report design` | v1 **FAIL**：copper-to-edge 7 处（这块旧板的铺铜内缩 14 mil < 20 mil）；另有 warnings；§7 的 DRC / check / 规则同步 / 对账 / 重载一致性为 N/A（故意没给） |
| `report manual --out` | 单文件 HTML，3 个接口、4 个电源测量点、0 处对账不一致 |
| `signoff` | 非零退出：`signoff-review`（无 review.json）、`signoff-safety`（同一 copper-to-edge）、`signoff-copper`（28 段走线窄于 intent 线宽，USB 差分与电源网的 5 mil 出线）失败；`signoff-ir`、`-parts`、`-continuity`、`-trace`、`-artifacts` 通过 |

读法：仿真本身全部通过，但报告和签核仍判 FAIL —— 板边距离与线宽是**板**的问题，要交还 router 修板后重跑全链；
不能因为“仿真 PASS”就说板可以下单。

## 链 B：KiCad 自带 demo（pic_programmer）

```bash
pcbpilot kicad netlist --sch pic_programmer/pic_programmer.kicad_sch --out conn.json    # 63 器件 / 34 网
pcbpilot kicad snapshot pic_programmer/pic_programmer.kicad_pcb --out board.json
pcbpilot sim power --connectivity conn.json --out sim.json --report sim.md
```

第一次：所有轨 0 V，10 条 warning —— 直流输入连接器（CONN_2）与 7805 都没有模型，6 颗 IC 按 50 mA 假设。
这个结果没有意义。补一个项目模型文件后重跑：

```json
{"schemaVersion":1,"models":[
 {"id":"demo-dc-in","kind":"connector-source","match":{"nameRegex":"^CONN_2$"},"supplyPins":["2"],"returnPins":["1"],
  "voltageV":15,"rsOhm":0.05,"sourceName":"dc-in","source":"demo assumption: 15 V bench supply","confidence":"assumed"},
 {"id":"demo-7805","kind":"ldo","match":{"nameRegex":"^7805$"},"pins":{"in":["VI"],"out":["VO"],"gnd":["GND"]},
  "vout":5.0,"dropoutV":2.0,"iqA":0.005,"maxA":1.0,"source":"generic 78xx typical","confidence":"approx"}]}
```

| 步骤 | 实际结果 |
|---|---|
| `sim power --models models.json` | VCC 5.000 V / 0.111 A；整流后 14.2 V；VCC_PIC 0.02 V（由 LT1373 升压 + 三极管开关供电，未建模） |
| `intent derive --models … --sim sim.json` | 20 块、34 网、2 域；0 error / 8 warn；`sim analog` 4 块 WARN |
| `sim post-layout` | **FAIL**：`sim-board-mismatch` —— 板快照里子页网名是 `/pic_sockets/VCC_PIC`，`kicad netlist` 输出 `VCC_PIC`；另有板温 107.6 °C（U3 7805 下方，1.09 W，来自假设的 IC 负载 × 9 V 压差） |

读法：①模型缺失时先补模型，不读数字；②层次图子页网名目前在 post-layout 对账中不归一：工具给 FAIL，只有逐网证明
`/pic_sockets/VCC_PIC` ↔ `VCC_PIC` 这类差异一一对应（焊盘集合相同）后才降为 INCOMPLETE 并报告为工具缺口，未证明的仍是
FAIL（[判定规则 §6.1](../../pcbpilot/references/evidence-and-verdicts.md#61-网络身份与对账差异)）；③板温高的原因是 `assumed`
负载，涉及的轨与器件热结论都是 INCOMPLETE（§6.2），换成数据手册电流后再判断是否是真实散热问题。

## 从这两次运行学到的判读要点

- `sim tools check` 退出 0 只说明 ngspice 可用；Elmer 缺失永远不影响退出码。
- 仿真 PASS ≠ 报告 PASS ≠ 签核 PASS：三者覆盖的事实不同，分别报告。
- `report design` 在缺证据时照样出版本，只把章节标 N/A；交付版要把 N/A 补齐。
- `signoff` 用 `--board` + `--sim` 自己重算设计后仿真（结果写进 `--out-dir/post.json`），不读你之前的 post.json。
