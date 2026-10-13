# TraceMaker `--mode eco` / `--mode routable` 在 PicoRick 上的实测

日期 2026-10-13，主机 二丫（macOS x86_64，共享机，load 3.8–8.7，另有 agent 同时在跑，我最多 4 线程）。
完整记录（命令、meta、stdout/stderr、report JSON、输出 .kicad_pcb、DRC、未布通清单、移动清单）：
`/Users/taisen/pcbpilot-work/coadapt/records/tm-eco/<run-id>/`，索引 `records/tm-eco/INDEX.csv`（追加式）。
二丫原目录 `~/fpga-remote/pcbpilot-handoff/v093-eco/`。

## 设置（真实情况）
- 二进制：从 `~/Github_Working/tracemaker-pcbpilot` 克隆到 `v093-eco/src`，源码 commit b99f1a2（用户分支 dynamic-routing-eco 比它只多 research/docs，未改 C++），本地 CMake preset macos-cpu 重编。sha256 见各 run 的 meta.txt。（事后我删除了 `v093-eco/src/build`，只是自己的编译产物，可重编。）
- 输入：`in.kicad_pcb` = pcbpilot 对 PicoRick 拆线后的 `out-tracemaker/tracemaker/in.kicad_pcb`（含 in.kicad_dru、constraints.json）。`PicoRick_One_revA.kicad_pcb` 是人工布好的原板。
- 流程：base = 直接 `tracemaker route`；eco/routable = `tracemaker-place --mode X ... --route-check 3000000 --route-threads 4 --threads 4 --seed 1` 得 placed.kicad_pcb，再用与 base 完全相同的命令做最终布线：
  `tracemaker route <pcb> --constraints constraints.json --priority-order-only --soft-zones --time 300 --no-kb --threads 4`。
- 重要限制：`tracemaker-place` 没有 --constraints/--first-nets/--priority-order-only/neck-down 选项，它内部的 route-check 用默认 RouterOptions（无意图线宽、无优先级）。所以放置阶段的评估器与最终布线器不是同一个东西：评估器给输入板 93 未布通（266/359），而真实意图布线只有 2–3 未布通。
- DRC：`kicad-cli pcb drc --refill-zones --severity-error`（直接在 agent shell 运行，未经 pcbpilot-dev）。第一次 DRC 没刷新铜皮，出现 ~1000 条假的 zone clearance（INDEX 里原行保留，+drc-refill 行为更正值）。未做 pcbpilot 的 widen 步骤，所以 track_width 数字比 pcbpilot 流程里的 7 条高，只能用于彼此对比。
- 未做：`eco-s2`（seed 2）启动后被我终止（耗时；eco 放置过程是确定性的，seed 1 两次都原样保留输入）。部分目录在 records/tm-eco/eco-s2，标记 KILLED_BY_AGENT。

## 结果表
| run-id | 模式 | 放置耗时/总耗时 | 最终未布通 | DRC error（refill, track_width）/ 未连接项 | 移动器件 | HPWL (评估器) | 备注 |
|---|---|---|---|---|---|---|---|
| base-s1-a | 人工放置，直接布 | 0 / 303 s | 3 | 46 / 5 | 0 | 2866.3 mm | U303.23-C307.1、U303.23-U303.50（+1V1），GPIO27 U303.39-J106.8 |
| base-s1-b | 同上重复（噪声） | 0 / 302 s | 2 | 41 / 2 | 0 | 2866.3 | 仅 +1V1 U303.23 两条 |
| eco-s1 | eco 4轮x5候选 | 2730 / 3032 s | 2 | 25 / 2 | 0 | 2866.3（不变） | 评估器最终验证 12M：输入 55，eco 58 -> 保留输入；板与 base 完全相同，差异纯属布线噪声 |
| eco-repair-s1 | eco + --repair-input | 2418 / 2720 s | 3 | 46 / 5 | 0 | 2866.3 | 同样保留输入（无重叠需修复） |
| routable-s1 | routable 3轮, --loop-time 1500 | 2325 / 2629 s | 4 | 44 / 4 | 136（全板） | 1596.4 mm（-44%） | 胜出的是 "full" 从零放置：输入 55 -> 42(12M)；中位位移 29.9 mm，最大 100.2 mm，90 个转角度，0 翻面 |
| human 原板（参考） | 人工布线 | - | 0 | 193（189 track_width + 4 copper_edge，按 pcbpilot dru）/ 0 | - | - | 说明 dru 对人工板更严，DRC 数字只作相对比较 |

U303 四条目标网络的结果（人工放置）：+3V3 U303.44-48、XOUT R305.2-U303.21 在所有 base/eco 运行中均布通；+1V1 U303.50-45 布通；+1V1 U303.23-C307.1（及 U303.23-U303.50）在每个 base/eco 运行中都未布通，是唯一稳定失败。
routable 的新放置未布通：D6 U303.9-IC101.19、+3V3 U303.1-U303.49、+1V1 U303.23-C307.1、+1V1 U303.23-U303.50（比人工放置多出 +3V3 与 D6，U303 周围问题没有消除）。

eco 被评估器挑选的移动只有 J106 旋转180、R303/C313/U302 平移 0.5–2 mm 等，评估器内的改善（93->85）在 12M 预算验证时消失（58 vs 55），故一个器件也没写出。

## 结论（8 行）
1. 在 PicoRick 上 eco 与 routable 都没有胜过人工放置：最终未布通 eco 2/3、routable 4，人工放置重复两次为 3 和 2。
2. 噪声：同一块板同一命令重复两次是 3 与 2；eco 输出板与输入完全相同却得 2 和 3，所以 1 条差距属于噪声（布线器是时间限制、不确定的）。
3. eco 没移动任何器件：评估器里候选的小改善（93->85）在更大预算下反转，验证关卡把输入保留下来；花了 ~45 分钟换来零改动。
4. routable 重放置了全部 136 个器件（中位 30 mm，最大 100 mm），HPWL 降 44%，但这是一个完全不同的板子，丢掉了人工的机械/类别布局，U303 区域未布通反而更多（+3V3、D6）。对"保持人工放置、只微调"的目标不可用。
5. 根因：放置评估器用默认布线选项，与 pcbpilot 的意图线宽/neck-down/优先级不一致，93 未布通的评估与实际 2–3 的最终布线几乎无相关，它选出的移动不可信。上游"64->35"的收益也很可能包含这种预算扰动。
6. 稳定失败点始终是 U303.23(+1V1)，不是靠平移 0.5–2 mm 能解开的；与 RESULTS.md 的"net order 才是最强旋钮、微调帮助小且噪声大"一致。
7. 采用建议：不采用 --mode routable 作为默认；eco 只有在评估器能吃 constraints.json / neck-down / priority 参数后才值得（需要上游给 tracemaker-place 加这些选项）。
8. 评估指标必须做多次重复并报告方差；我们自己的闭环应使用与最终布线器相同的配置，且只对失败网络局部增量重布（秒级），而不是整板 12M 预算的 ~10 分钟一次评估。

## 补充：负载、未布通身份、DRC 分类、移动清单

| run | 1分钟load 起->止 | 未布通连接 | DRC(refill) 分类 | 初版DRC(陈旧铜皮) 分类 |
|---|---|---|---|---|
| base-s1-a | 4.73 -> 6.59 | ['+1V1 U303.23-C307.1', '+1V1 U303.23-U303.50', '/Controller/GPIO27 U303.39-J106.8'] | {'track_width': 46, '_unconnected_items': 5} | {'clearance': 470, 'hole_clearance': 199, 'track_width': 46, 'silk_edge_clearance': 1, 'lib_footprint_mismatch': 25, 'lib_footprint_issues': 13, 'silk_overlap': 108, 'silk_over_copper': 147, '_unconnected_items': 3} |
| base-s1-b | 5.69 -> 5.60 | ['+1V1 U303.23-C307.1', '+1V1 U303.23-U303.50'] | {'track_width': 41, '_unconnected_items': 2} | {'clearance': 459, 'hole_clearance': 199, 'track_width': 41, 'silk_edge_clearance': 1, 'lib_footprint_mismatch': 25, 'lib_footprint_issues': 13, 'silk_overlap': 108, 'silk_over_copper': 147, '_unconnected_items': 2} |
| eco-s1 | 6.59 -> 4.64 | ['+1V1 U303.23-C307.1', '+1V1 U303.23-U303.50'] | {'track_width': 25, '_unconnected_items': 2} | {'clearance': 443, 'hole_clearance': 199, 'track_width': 25, 'silk_edge_clearance': 1, 'lib_footprint_mismatch': 25, 'lib_footprint_issues': 13, 'silk_overlap': 108, 'silk_over_copper': 147, '_unconnected_items': 2} |
| eco-repair-s1 | 5.60 -> 4.65 | ['+1V1 U303.23-C307.1', '+1V1 U303.23-U303.50', '/Controller/GPIO27 U303.39-J106.8'] | {'track_width': 46, '_unconnected_items': 5} | {'clearance': 466, 'hole_clearance': 199, 'track_width': 46, 'silk_edge_clearance': 1, 'lib_footprint_mismatch': 25, 'lib_footprint_issues': 13, 'silk_overlap': 108, 'silk_over_copper': 147, '_unconnected_items': 3} |
| routable-s1 | 4.64 -> 5.69 | ['D6 U303.9-IC101.19', '+3V3 U303.1-U303.49', '+1V1 U303.23-C307.1', '+1V1 U303.23-U303.50'] | {'track_width': 44, '_unconnected_items': 4} | {'clearance': 443, 'hole_clearance': 199, 'track_width': 44, 'silk_edge_clearance': 1, 'hole_to_hole': 1, 'lib_footprint_mismatch': 25, 'lib_footprint_issues': 13, 'silk_overlap': 131, 'silk_over_copper': 180, '_unconnected_items': 4} |

未完成：eco-s2（seed 2）启动后被我终止，原因是耗时，eco 放置是确定性的；目录 records/tm-eco/eco-s2 只有部分文件，已标 KILLED_BY_AGENT。其余 5 个运行全部完成，退出码见各 meta.txt。

base-s1-a/b、eco-s1、eco-repair-s1 的移动清单为空（0 个器件，见各目录 moved_parts.json）。

rouable-s1 移动清单：136 个，完整清单见 records/tm-eco/routable-s1/moved_parts.json（ref, dx, dy, rot）。U303 附近及位移最大的 10 个：

| ref | dx mm | dy mm | rot 度 |
|---|---|---|---|
| R305 | 19.01 | -22.8 | -90.0 |
| C307 | 22.14 | -17.316 | 0.0 |
| U303 | 19.304 | -19.94 | 0.0 |
| R403 | -97.13 | 24.714 | 0.0 |
| J102 | -91.28 | 36.973 | -90.0 |
| Q401 | -89.22 | 26.265 | 0.0 |
| L401 | -69.06 | 60.89 | -90.0 |
| C402 | -55.571 | 70.82 | -180.0 |
| U401 | -45.361 | 69.37 | -180.0 |
| C403 | -53.569 | 49.75 | 90.0 |
| C404 | -56.11 | 44.67 | 90.0 |
| C317 | -45.1 | -50.28 | 90.0 |
| SW301 | 16.31 | -63.2 | -180.0 |
