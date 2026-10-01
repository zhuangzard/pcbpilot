# 原理图美观度 Phase A 基线（2026-10）

由 `pkg/schaes/golden_test.go` 锁定（总分 ±0.05、已连份额、已测/未测数）。改度量、阈值或风格档会有意移动这些数：
同时更新本表、黄金表并在提交里写原因。复现：

```bash
pcbpilot sch aesthetics --snapshot pkg/schaes/testdata/ams1117-lib-layout.json
pcbpilot sch aesthetics --snapshot internal/app/testdata/esp32-v05/sch-950ae6609e91d753.json --style precision
pcbpilot sch aesthetics --snapshot pkg/schaes/testdata/synthetic/esp32-mcu-point-to-point.json --json
```

## Fixture 来源

| fixture | 来源 | 内容 | 能测什么 |
|---|---|---|---|
| `ams1117-lib-layout` | `pcbpilot sch lib-layout --from internal/app/testdata/lib-layout/ams1117.json`（dev @ 066321ba）的输出，存为 `pkg/schaes/testdata/ams1117-lib-layout.json` | 仓库生成器真实输出：宏恩 POWER 实测 bbox/引脚 + 15 段线 + 6 个标记 | 全部布线项；无框、无信号标签 |
| `esp32-mcu-canonical` / `esp32-pwr-canonical` | `internal/app/testdata/esp32-v05/sch-950ae6609e91d753.json` / `sch-905bb85957eaf435.json`（v0.5/v0.6 现场 connectivity 快照） | 真实器件 bbox、引脚坐标、网络；**无导线/标记** | 只有 W8、L3、L7（及 PWR 的 L1/L4）；其余 `skipped`，判定 `incomplete` |
| `esp32-*-drafted` | 测试生成器 `drafted()`：在真实摆放上按“细心绘图员”规则画线——半周长 <120 的信号网直连、其余短桩 + netport、电源旗朝上/地朝下、偏移 20/30/40/50/10 依次试，**无干净位置就留空不画**（绝不伪造短路） | 合成；MCU 页 35% / PWR 页 41% 引脚因原摆放过密留空 | 防刷分：已连份额 0.649 / 0.586 直接压低布线组 |
| `esp32-*-stub-label` | `stubLabel()`：每个引脚 20 units 外向桩 + flag/port，不避让 | 合成；含共线重叠（异网）与标签互压 | 负对照：W4、W5、W7、L6、N2 应响 |
| `esp32-*-point-to-point` | `pointToPoint()`：信号网按引脚顺序 L 形串接、电源地用旗 | 合成；大量交叉、穿本体、绕行 | 负对照：W2、W3、W6、W7、N1 应响 |

`drafted` / `point-to-point` 也以归一化快照提交在 `pkg/schaes/testdata/synthetic/`（`TestSyntheticFixturesCommitted` 防漂移，
`SCHAES_WRITE_FIXTURES=1` 重生成），可直接喂 CLI。

## 总表（分 / 判定 / 布线·版面·标签 / 已连份额 / 已测·未测 / 逐项）

| fixture | 档 | 分 | 判定 | W/L/N | 已连 | 测/缺 | 逐项 |
|---|---|---|---|---|---|---|---|
| ams1117-lib-layout | functional | 85.1 | incomplete | 79.3/84.7/100.0 | 1.000 | 14/4 | W1=85 W2=60 W3=100 W4=100 W5=0 W6=100 W7=100 W8=100 L1=100 L2=100 L3=0 L4=– L5=– L6=100 L7=77 N1=100 N2=– N3=– |
| ams1117-lib-layout | balanced | 81.8 | incomplete | 78.3/75.5/100.0 | 1.000 | 14/4 | W1=85 W2=60 W3=100 W4=100 W5=0 W6=100 W7=100 W8=82 L1=100 L2=100 L3=0 L4=– L5=– L6=100 L7=77 N1=100 N2=– N3=– |
| ams1117-lib-layout | precision | 79.8 | incomplete | 76.9/71.2/100.0 | 1.000 | 14/4 | W1=85 W2=60 W3=100 W4=100 W5=0 W6=100 W7=100 W8=63 L1=100 L2=100 L3=0 L4=– L5=– L6=100 L7=77 N1=100 N2=– N3=– |
| esp32-mcu-canonical | functional | 82.0 | incomplete | 100.0/52.1/0.0 | 1.000 | 3/15 | W1=– W2=– W3=– W4=– W5=– W6=– W7=– W8=100 L1=– L2=– L3=62 L4=– L5=– L6=– L7=43 N1=– N2=– N3=– |
| esp32-mcu-canonical | balanced | 64.6 | incomplete | 72.1/52.1/0.0 | 1.000 | 3/15 | W1=– W2=– W3=– W4=– W5=– W6=– W7=– W8=72 L1=– L2=– L3=62 L4=– L5=– L6=– L7=43 N1=– N2=– N3=– |
| esp32-mcu-canonical | precision | 47.9 | incomplete | 44.3/54.0/0.0 | 1.000 | 3/15 | W1=– W2=– W3=– W4=– W5=– W6=– W7=– W8=44 L1=– L2=– L3=62 L4=– L5=– L6=– L7=43 N1=– N2=– N3=– |
| esp32-pwr-canonical | functional | 86.2 | incomplete | 100.0/63.2/0.0 | 1.000 | 5/13 | W1=– W2=– W3=– W4=– W5=– W6=– W7=– W8=100 L1=100 L2=– L3=62 L4=0 L5=– L6=– L7=54 N1=– N2=– N3=– |
| esp32-pwr-canonical | balanced | 67.1 | incomplete | 71.9/59.0/0.0 | 1.000 | 4/14 | W1=– W2=– W3=– W4=– W5=– W6=– W7=– W8=72 L1=100 L2=– L3=23 L4=– L5=– L6=– L7=54 N1=– N2=– N3=– |
| esp32-pwr-canonical | precision | 47.6 | incomplete | 43.8/53.9/0.0 | 1.000 | 4/14 | W1=– W2=– W3=– W4=– W5=– W6=– W7=– W8=44 L1=100 L2=– L3=23 L4=– L5=– L6=– L7=54 N1=– N2=– N3=– |
| esp32-mcu-drafted | functional | 77.9 | incomplete | 64.9/84.9/100.0 | 0.649 | 15/3 | W1=100 W2=100 W3=100 W4=100 W5=100 W6=100 W7=100 W8=100 L1=100 L2=88 L3=62 L4=– L5=– L6=100 L7=43 N1=100 N2=100 N3=– |
| esp32-mcu-drafted | balanced | 74.9 | incomplete | 62.9/78.3/100.0 | 0.649 | 15/3 | W1=100 W2=100 W3=100 W4=100 W5=100 W6=100 W7=100 W8=75 L1=100 L2=88 L3=62 L4=– L5=– L6=100 L7=43 N1=100 N2=100 N3=– |
| esp32-mcu-drafted | precision | 74.1 | incomplete | 61.5/77.7/100.0 | 0.649 | 15/3 | W1=100 W2=100 W3=100 W4=100 W5=100 W6=100 W7=100 W8=50 L1=100 L2=88 L3=62 L4=– L5=– L6=100 L7=43 N1=100 N2=100 N3=– |
| esp32-mcu-stub-label | functional | 57.6 | incomplete | 66.7/41.0/60.0 | 1.000 | 16/2 | W1=100 W2=100 W3=100 W4=50 W5=0 W6=100 W7=0 W8=100 L1=29 L2=83 L3=62 L4=– L5=– L6=0 L7=43 N1=100 N2=0 N3=100 |
| esp32-mcu-stub-label | balanced | 59.1 | incomplete | 65.5/43.2/66.7 | 1.000 | 16/2 | W1=100 W2=100 W3=100 W4=50 W5=0 W6=100 W7=0 W8=74 L1=29 L2=83 L3=62 L4=– L5=– L6=0 L7=43 N1=100 N2=0 N3=100 |
| esp32-mcu-stub-label | precision | 62.8 | incomplete | 68.2/48.1/71.4 | 1.000 | 16/2 | W1=100 W2=100 W3=100 W4=50 W5=0 W6=100 W7=0 W8=48 L1=29 L2=83 L3=62 L4=– L5=– L6=0 L7=43 N1=100 N2=0 N3=100 |
| esp32-mcu-point-to-point | functional | 33.5 | incomplete | 33.3/45.2/16.7 | 1.000 | 14/4 | W1=100 W2=14 W3=50 W4=25 W5=11 W6=0 W7=0 W8=100 L1=29 L2=100 L3=62 L4=– L5=– L6=0 L7=43 N1=17 N2=– N3=– |
| esp32-mcu-point-to-point | balanced | 34.5 | incomplete | 34.3/46.6/16.7 | 1.000 | 14/4 | W1=100 W2=14 W3=50 W4=25 W5=11 W6=0 W7=0 W8=75 L1=29 L2=100 L3=62 L4=– L5=– L6=0 L7=43 N1=17 N2=– N3=– |
| esp32-mcu-point-to-point | precision | 36.4 | incomplete | 34.8/52.3/16.7 | 1.000 | 14/4 | W1=100 W2=14 W3=50 W4=25 W5=11 W6=0 W7=0 W8=50 L1=29 L2=100 L3=62 L4=– L5=– L6=0 L7=43 N1=17 N2=– N3=– |
| esp32-pwr-drafted | functional | 64.7 | incomplete | 54.6/71.2/80.0 | 0.586 | 17/1 | W1=100 W2=100 W3=100 W4=100 W5=100 W6=100 W7=49 W8=100 L1=65 L2=97 L3=62 L4=0 L5=– L6=100 L7=54 N1=100 N2=100 N3=0 |
| esp32-pwr-drafted | balanced | 60.1 | incomplete | 52.8/67.9/66.7 | 0.586 | 16/2 | W1=100 W2=100 W3=100 W4=100 W5=100 W6=100 W7=49 W8=72 L1=65 L2=97 L3=23 L4=– L5=– L6=100 L7=54 N1=100 N2=100 N3=0 |
| esp32-pwr-drafted | precision | 57.4 | incomplete | 52.0/66.6/57.1 | 0.586 | 16/2 | W1=100 W2=100 W3=100 W4=100 W5=100 W6=100 W7=49 W8=45 L1=65 L2=97 L3=23 L4=– L5=– L6=100 L7=54 N1=100 N2=100 N3=0 |
| esp32-pwr-stub-label | functional | 57.1 | incomplete | 69.5/41.1/50.0 | 1.000 | 17/1 | W1=100 W2=88 W3=100 W4=50 W5=33 W6=100 W7=0 W8=100 L1=30 L2=96 L3=62 L4=0 L5=– L6=0 L7=54 N1=100 N2=0 N3=50 |
| esp32-pwr-stub-label | balanced | 56.1 | incomplete | 67.8/40.8/50.0 | 1.000 | 16/2 | W1=100 W2=88 W3=100 W4=50 W5=33 W6=100 W7=0 W8=71 L1=30 L2=96 L3=23 L4=– L5=– L6=0 L7=54 N1=100 N2=0 N3=50 |
| esp32-pwr-stub-label | precision | 57.8 | incomplete | 69.2/44.0/50.0 | 1.000 | 16/2 | W1=100 W2=88 W3=100 W4=50 W5=33 W6=100 W7=0 W8=42 L1=30 L2=96 L3=23 L4=– L5=– L6=0 L7=54 N1=100 N2=0 N3=50 |
| esp32-pwr-point-to-point | functional | 44.3 | incomplete | 34.5/41.9/72.2 | 0.966 | 15/3 | W1=100 W2=100 W3=0 W4=0 W5=18 W6=0 W7=0 W8=100 L1=30 L2=100 L3=62 L4=0 L5=– L6=0 L7=54 N1=72 N2=– N3=– |
| esp32-pwr-point-to-point | balanced | 44.4 | incomplete | 35.1/41.5/72.2 | 0.966 | 14/4 | W1=100 W2=100 W3=0 W4=0 W5=18 W6=0 W7=0 W8=72 L1=30 L2=100 L3=23 L4=– L5=– L6=0 L7=54 N1=72 N2=– N3=– |
| esp32-pwr-point-to-point | precision | 46.3 | incomplete | 36.9/44.9/72.2 | 0.966 | 14/4 | W1=100 W2=100 W3=0 W4=0 W5=18 W6=0 W7=0 W8=44 L1=30 L2=100 L3=23 L4=– L5=– L6=0 L7=54 N1=72 N2=– N3=– |

（canonical 行的“标签 0.0”表示该组无已测项，不参与总分。）

## 读数

1. **排序正确**：同一真实摆放上 drafted > stub-label > point-to-point（MCU 74.9 / 59.1 / 34.5，balanced），
   由 `TestSyntheticStylesOrderIsSane` 锁定；PWR 页 drafted 60.1 > stub-label 56.1 > point-to-point 44.4。
2. **单调性**（`TestSyntheticDegradationsAreMonotone`，drafted MCU，k = 0…4，balanced）：
   jog W1 100→90.0、加交叉 W2 100→20.0、四通 W3 100→0、偏格 W8 75.0→68.5、翻旗 L2 87.5→70.8、标签互压 L6 100→0
   （对应总分 74.9 → 74.6 / 70.5 / 70.2 / 70.8 / 64.6 / 68.9）；
   目标指标逐步不升、满剂量严格下降；纯旋钮的总分同样不升（容差 0.5 只给 W8 这类份额项加顶点时的小幅波动），
   偏格旋钮会真实移动器件，只断言目标项。
3. **生成器真实输出（ams1117）**：电气正确，但 1 处 +5V×+3V3 严格 X、4/4 个 T 结点离拐点仅 5 units、同类器件不共线——
   这是 Phase B1/B4 的第一批靶子。
4. **真实摆放（esp32 canonical）**：同类器件共线 61.5%（MCU）/ 25%（PWR）、占用 CV 1.86 / 1.69；precision 档 10-unit
   锚点份额低（多数器件锚点在 5 而非 10 的倍数上）。canonical 缺导线，所以分数只覆盖 3–5 项，判定恒为 `incomplete`。
5. **防刷分**：`TestUnwiringNeverHelps`——删掉全部导线后布线组记 0、总分下降；删一半导线已连份额必降。
6. **风格档**：precision 对 W1/W2/W3/L2/L3/N3 加权，好项越多分可能越高——跨档分数不可比，只在同档内比较。
