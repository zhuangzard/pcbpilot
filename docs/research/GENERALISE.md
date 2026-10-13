# 泛化实验：PicoRick 的发现能否推广到别的真实板（US-Zed2 + GasV5_A）

日期 2026-10-13。主机 铁蛋儿（TAISENs-MacBook-Pro-6，macOS 15.6.1，14 核，KiCad 10.0.6）。全部为 **direct-fastroute（未走 pcbpilot 门禁、无出线桩/widen/过孔阵列/铺铜步骤）**，只能在本文内部比较，不能与 pcbpilot 全流程数字直接比。
记录：`/Users/taisen/pcbpilot-work/coadapt/records/generalise/`（`INDEX.csv`：命令、flags、seed、sha256、负载、秒数、DRC 分类、未布通列表；每个 run 一个目录，含 fastroute.log、report.json、route.dsn/ses、routed.kicad_pcb、drc*.json、meta.json；`exitfeas/` 出口可行性 JSON；`scripts/` 全部脚本；`inputs/` Gas 的 DSN 与规则）。铁蛋儿上原始目录 `~/GitHub_Working/generalise-work/`。未改任何原件、未碰 v093-eco-b、未碰 8765 端口、无 push/tag。

## 1. 工具在铁蛋儿上是否可用（如实）

| 项 | 结果 |
|---|---|
| fastroute 0.1.13 | 铁蛋儿**没有安装**（`~/.pcbpilot/` 下无 fastroute 目录，PATH 无）。我把 小虾 的 `~/.pcbpilot/fastroute/0.1.13/fastroute`（sha256 c0b149b4…0005d，arm64）拷到 `generalise-work/bin/`，可运行（`fastroute 0.1.13`）。 |
| tracemaker | 铁蛋儿**没有安装**。把 小虾 的 `tracemaker`（pcbpilot-bec9569，sha256 4367eae7…）拷过去后**无法运行**：`dyld: Symbol not found __ZNSt3__127__from_chars_floating_point… (built for macOS 26.0 which is newer than running OS)`，铁蛋儿是 macOS 15.6.1。未绕过。 |
| tracemaker-place（同伴在 v093-eco-b 构建） | 我检查时 `v093-eco-b/build/macos-cpu/` 已不存在（目录被重建/清理中），无任何可执行文件；只读检查，未复用。故 **tracemaker 在两块板上都没有跑**（Zed2 的 tracemaker 结果只能引用 小虾 2026-10-12 的 E0tm：394/394，300 s）。 |
| pcbpilot 二进制 | 未使用（避免 TCC 挂起）。DSN 导出/SES 导入用 KiCad 自带 python（pcbnew），DRC 直接从 agent shell 跑 `kicad-cli pcb drc`，全程无挂起。 |
| shapely | 铁蛋儿/小虾都没有；出口可行性用纯 python 精确几何（线段-矩形/长圆/圆的距离，圆角矩形按圆角处理）重写，见 `scripts/exitfeas_real.py`。 |

## 2. 板子

| 板 | 来源 | 规模 | 最小焊盘间距的 IC | 说明 |
|---|---|---|---|---|
| US-Zed2（Board1_fixed） | `US-Zed2-redo/kicad/` | 4 层，139 件，110 网络，394 连接（去铺铜后） | **没有 ≤0.5 mm 的 IC**；最小 0.64–0.65 mm（U2 TSSOP-28、U3 QSOP-20、U4 MSOP-8、U15 SSOP-24）。所以"细间距 ≤0.5"集合为空，改用 ≤0.66 mm 的扩展集（80 个焊盘）。 | 对照板（简单板） |
| GasV5_A | `~/GitHub_Working/Gas_Control/GasControl_PCB/04_Code/hardware/kicad/GasV5_A.kicad_pcb` | 4 层，203 件，129 网络，462 连接，100x80 mm，仅有摆放（0 走线 0 铺铜），13 处 courtyard 重叠 | **U8 TQFP-144，0.5 mm 间距**（焊盘 0.28 x 1.8），U7 TSSOP-16 0.65 mm | 第二块"较密"的板。没有 .kicad_pro/网络类，**规则是我假设的**：默认线宽 0.200/间距 0.1524（与 Zed2 同属 EasyEDA 数值），GND 与 `+…` 电源网络 0.400，SW5/SW15 0.500；DRC 用 Zed2 同一份 .kicad_dru（0.1524/0.127）。 |

另外找到但没跑：GasV5_B（A 的变体）、`tracemaker-pcbpilot/tests/boards/plane_smd/plane_smd.kicad_pcb`（合成测试板，16 件，QFN-32 0.5 mm + 0.4 mm 器件，太小）、`pcbpilot*/internal/kicad/testdata/*.kicad_pcb`（tiny/fab，玩具板）。bench 目录里没有 .kicad_pcb。PicoRick 已被排除。

## 3. 布线实验（fastroute 0.1.13，每个配置跑 2 次）

共同 flags：`--diagnose --multi-start=4 --max-time=1200 --router.autorouter.max_threads=6 --router.copper_to_edge_clearance_um=500`；Z3/Gas 另加 `--router.min_trace_width_um=127 --no-neckdown-classes=PP_GND_POWER,PP_SWITCH`（Z4 用 200；Z1 与 小虾 run_fr.sh 一致，无最小线宽/无防收窄）。fastroute 无显式 seed（multi-start 内部乱序）。"去电源"变体 = `--router.autorouter.ignore_net_classes=PP_GND_POWER`（nps = 同时忽略 PP_SWITCH）。负载（1 分钟平均）3.4–12，同伴的作业在同机并行，所以秒数仅供参考。

DSN 输入：Z3/Z4/Z1 直接用 小虾 E0d3/E0d4/E0d 的 `ripped.dsn`/`ripped_rules.dsn`（同样本）；Z3 = 意图类宽（GND+电源 10 mil、BUCK_SW 20 mil、其余 6 mil，间距 6.2 mil，去铺铜）；Z4 = 项目自身 0.2/0.205 mm；Z1 = 保留铺铜为 plane（442 连接）。Gas 的 DSN 在铁蛋儿上由 pcbnew 导出再补类（`scripts/patchdsn.py`）。

| 配置 | 连接 | 被忽略 | **其余未布通** | 过孔 | 总长 mm | 秒 (r1/r2) | KiCad DRC 错误（按类） |
|---|---|---|---|---|---|---|---|
| Zed2 Z3 基线 | 394 | 0 | **0** | 185/185 | 3715.7/3716.6 | 9.6/9.9 | courtyards_overlap 16 |
| Zed2 Z3 忽略电源 | 394 | 219 | **0** | 129 | 2455.4 | 3.0/3.2 | courtyards_overlap 16 |
| Zed2 Z3 忽略电源+开关 | 394 | 221 | **0** | 126 | 2415.2 | 3.0/2.9 | courtyards_overlap 16 |
| Zed2 Z4 基线（0.2/0.205） | 394 | 0 | **0**（路由器报 1 处不可修间距：U4.4/C46.1，0.1785<0.2052） | 193 | 3714.7 | 14.1/14.2 | courtyards_overlap 16 |
| Zed2 Z4 忽略电源 | 394 | 219 | **0** | 141 | 2500.8 | 3.2/3.1 | courtyards_overlap 16 |
| Zed2 Z1 基线（铺铜当 plane） | 442 | 0 | **15**（12 blocked + 3 congestion） | 194 | 2949.8 | 12.7/13.0 | zones_intersect 60、courtyards 16、track_width 142（该配置无最小线宽）、starved_thermal 27；unconnected 21 |
| **Gas 基线** | 462 | 0 | **9** | 187/187 | 5094.3 | 327.6/322.5 | courtyards_overlap 13（EasyEDA 规则重跑）；首次用 KiCad 默认 0.2 规则的 `drc.json` 误报 clearance 499/track_width 102，是我没配 .dru 造成，已保留并在 `drc2_summary.json` 中更正 |
| **Gas 忽略电源** | 462 | 276 | **0** | 121 | 2860.1 | 4.5/4.6 | courtyards_overlap 13 |
| **Gas 忽略电源+开关** | 462 | 281 | **0** | 129 | 2817.4 | 1.9/1.9 | courtyards_overlap 13 |

确定性：18 个运行对中，未布通身份在 9/9 个配置里两次完全一致；SES sha256 在 8/9 个配置里逐字节相同，唯一例外是 Zed2 Z3 基线（同为 0 未布通、185 过孔，总长差 0.85 mm）。

未布通身份：
- **Gas 基线（9 条，全是电源/地，全部诊断为 congestion，即单独布得通）**：+3V3 C69.1–U8.64、+3V3 U8.116–U8.136、+3V3 U8.25–C71.1、+5V_SENS J6.8–J7.1、GND C66.2–U8.99、GND C70.2–U8.47、GND U8.11–U8.10、GND U8.135–U8.128、GND U8.83–U8.78。其中 8/9 连接的一端是 TQFP-144 U8。忽略电源网络后其余 186 条信号连接全部布通（0 失败）。
- **Zed2 Z1 的 15 条**：+12V×2、+3.3V×3、+5V、5VLNA×3、5VVGA×3、HV_NEG×2、HV_POS，端点是"铺铜碎片"（None.None），12 条 blocked，3 条的实焊盘端点是 C24.1、FB5.2、C32.2（电容/磁珠，不是 IC）。与 小虾 E1 的结论一致：铺铜碎片伪影，不是焊盘出口。
- Zed2 Z3/Z4 无失败。

## 4. 出口可行性（精确胶囊几何，同 router-sim/exitfeas.py 的定义）

定义：对每个 IC 型封装（≥5 个编号 SMD 焊盘，位号非 R/C/L/D/…）的间距 ≤ 阈值的焊盘，取该网络在 DSN 里的**意图线宽/间距**（Gas：电源 0.4、信号 0.2、间距 0.1524；Z3：电源 0.254、信号 0.1524、间距 0.1575；Z4：0.254/0.2、间距 0.2052），沿焊盘长轴向外伸出胶囊（焊盘端外 0.6 mm 短桩；另记 2.0 mm 直线版本 `axial2mm_ok`），要求到所有**异网**同层焊盘的距离 ≥ 间距。`maxw` = 二分得到的最大合法线宽。未建模：板边、孔、其他零件的走线（已全部拆除）。8 方向任一可出线（`any8`）在所有焊盘上都为真（0 个不可行），所以没有焊盘是"绝对封死"的。

| 板 / 阈值 | 焊盘数 | 0.6 mm 短桩轴向出线**不可行** | 其中电源类(PP_GND_POWER) | 其中被同一 IC 自身邻焊盘挡住 |
|---|---|---|---|---|
| Gas ≤0.5（仅 U8） | 144 | **51（35%）** | 7/26 | **0**（全部被前方异网元件焊盘 R/C/Q 挡住） |
| Gas ≤0.66（U8+U7） | 160 | 57 | 8/30 | 0 |
| Zed2 Z3 ≤0.5 | 0 | – | – | – |
| Zed2 Z3 ≤0.66 | 80 | 13（16%） | 3/20 | 0 |
| Zed2 Z4 ≤0.66 | 80 | 22（28%） | 4/20 | 0 |
| Zed2 Z1 ≤0.66 | 80 | 12 | 0 | 0 |

关键事实：在 Gas 上，U8 的**焊盘对焊盘**出线几何本身可行：自由焊盘的 `maxw`=0.4152 mm（焊盘宽 0.28、间距 0.5 → 理论 0.72−2×0.1524=0.415），≥0.4 mm 的电源意图线宽，只剩 15 µm 余量。51 个不可行焊盘无一例外是被焊盘正前方 0.6 mm 内的去耦电容/电阻/三极管焊盘挡住（C62、C63、C66–C72、R51、R52、R59、R60、R62–R64、Q4），即**摆放造成的出口堵塞**，不是焊盘间距造成的。与 PicoRick（0.4 mm 间距 + 0.4 mm 电源线，焊盘间根本放不下）机理不同。

不可行焊盘清单：Gas 51 个见 `records/generalise/exitfeas/gas_p0.5.json`（`axial_ok=false`，含 blocker 字段）；Zed2 Z3 的 13 个：U3.4,17,19,20、U4.3,4、U2.16,17,19,21,23,26,27（见 `z3_p0.66.json`）。

## 5. 失败是否与不可行焊盘重合？

| 板/运行 | 失败连接落在 IC 细间距焊盘上的端点 | 其中出口不可行 | 对照：该 IC 焊盘总体不可行率 | 判断 |
|---|---|---|---|---|
| Gas 基线（9 条） | U8 的 12 个端点：10、11、25、47、64、78、83、99、116、128、135、136 | **3/12（25%）**：U8.47、83、99，全是 GND，分别被 C70.1、C67.1、C66.1 挡住 | 51/144 = 35%（电源类 7/26 = 27%） | **没有富集**（观测 25% ≤ 基线 35%）；另 6/9 条连接两端都可出线，失败是拥塞（diagnose 均为 congestion） |
| Zed2 Z1（15 条） | 无 IC 焊盘端点（端点为铺铜碎片或 C24/FB5/C32） | 0 | 15% | 不适用：伪影 |
| Zed2 Z3 / Z4 | 无失败 | – | – | 检验空洞；唯一一处路由器间距违规（Z4 U4.4/C46.1）涉及 1 个不可行焊盘（U4.4，被 C6.1 挡住），样本 1，不作结论 |

"电源网络优先失败"这一半在 Gas 上**成立**：9/9 失败全是电源/地（276 条电源连接里 9 条未通 = 3.3%；186 条信号连接 0 条未通），忽略电源类后其余全部布通。在 Zed2 上因为基线 0 失败，无法检验（只能说明去掉电源后过孔 185→129、总长 3716→2455 mm，电源/地占了大半布线量）。

## 6. 局限
- Gas 的网络类/线宽/间距是我假设的，不是用户的；若真实意图线宽不同，Gas 的不可行集合和失败集合都会变。
- 0.6 mm 直出桩是保守定义：真实布线器可以在 0.2–0.3 mm 内转弯，所以"不可行"是上界；2.0 mm 版本与 any8 版本已一并记录（any8 全部可行）。
- 每个配置只有 2 次运行，fastroute 无 seed；Gas 基线 9 条失败的样本小，25% vs 35% 不是统计检验。
- 没有跑 tracemaker（二进制不兼容 macOS 15）、没有跑 pcbpilot 全流程、没有移动器件（本任务只测泛化的"失败 = 出口不可行"假说）。Z1 无法做"忽略电源"变体（DSN 只有一个 kicad_default 类）。
- DRC 的 courtyards_overlap（Zed2 16、Gas 13）是输入摆放本身就有的，不是布线造成。

## 7. 结论（8 行）
1. fastroute 0.1.13 可在铁蛋儿跑（需从 小虾 拷二进制）；tracemaker 二进制是为 macOS 26 构建的，在铁蛋儿（macOS 15.6.1）无法运行，同伴的 tracemaker-place 构建在我检查时尚不存在，所以本实验只有 fastroute 的数据。
2. 两块板：US-Zed2（简单对照，无 ≤0.5 mm IC，最小 0.64 mm）与 GasV5_A（U8 TQFP-144，0.5 mm，规则为我假设）；其余仓库里的 .kicad_pcb 都是玩具/合成板。
3. Zed2 基线在 6 个配置里 5 个 0 未布通（重复运行除 SES 字节外一致），说明"简单板放得下就布得通"，对假说无检验力；唯一有失败的配置（Z1）失败是铺铜碎片伪影，端点不是 IC 焊盘。
4. Gas 基线 9 条失败（约 327 s，两次完全一致），**全是电源/地**，且去掉电源/地类后其余 186 条信号连接 0 失败：PicoRick 的"电源网络先失败"在第二块板上**成立**。
5. 但"失败 = 焊盘出口几何不可能"在 Gas 上**不成立**：U8 自由焊盘的最大合法线宽 0.4152 mm ≥ 0.4 mm 电源意图线宽（几何可行，余量仅 15 µm）；9 条失败诊断均为 congestion（单独可布），失败端点的出口不可行率 3/12=25%，不高于 U8 总体 35%。
6. 机理不同：PicoRick 是 0.4 mm 间距而线宽 0.4 mm，焊盘间放不下；Gas 的出口障碍来自摆放，U8 有 51/144 个焊盘正前方 0.6 mm 内有去耦电阻/电容/三极管焊盘（0 个是被 U8 自身邻焊盘挡住），其中 GND 引脚 47/83/99 对应 3 条失败，但其余 6 条失败与出口无关。
7. 对"摆放⇄布线闭环"的含义：Gas 上的瓶颈是电源/地网络在 TQFP 周围的拥塞与去耦件紧贴引脚，可作为闭环挪件的候选目标（挪 C62/C66–C72/R62–R64/Q4 一类前方件），而不是调整焊盘出口线宽；该推论尚未用挪件实验验证。
8. 以上为 direct-fastroute、每配置 2 次、Gas 规则为假设；若要硬结论，需要用户给出 Gas 的真实网络类线宽，并增加 tracemaker（需 macOS 15 可用构建）与挪件配对实验。
