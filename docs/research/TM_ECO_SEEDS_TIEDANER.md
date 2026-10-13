# TraceMaker eco / routable 多种子复测(铁蛋儿)

数据全部为实测。记录目录:`/Users/taisen/pcbpilot-work/coadapt/records/tm-eco-b/<run-id>/`(meta.txt、place/route 原始日志、place.json、route-report.json、out.kicad_pcb、drc.json、drc-refill.json、moved_parts.json、unrouted.json),汇总见同目录 INDEX.csv。

## 环境与构建
- 机器:铁蛋儿(M3 Max, macOS 15.6.1, 14 核),KiCad 10.0.6(kicad-cli 直接从 agent shell 调用,HOME 指向工作区内 khome,未用 /tmp/pcbpilot-dev)。
- 源码:从二丫复制的 src(无 .git;commit 沿用二丫 meta:b99f1a236aaa612e9519ab12ca793d46415c0eb6)。板子 sha256 = 1572701b...6de5,与二丫一致。
- 构建:`cmake --preset macos-cpu -DTM_BUILD_TESTS=OFF -DTM_BUILD_PYTHON=OFF -DCMAKE_PREFIX_PATH=<工作区 deps>`,ninja -j10,18 s 完成,rc=0。**偏离**:铁蛋儿没有 CLI11 / nlohmann_json(未用 brew 安装任何东西);从本机(Air)的 Homebrew Cellar 复制了头文件版 CLI11 2.7.2 与 nlohmann-json 3.12.0 到工作区 deps/(二丫用 nlohmann 3.12.0,CLI11 版本未知)。tests/python 绑定关闭(二丫的构建同样没有 Catch2/pybind11 产物)。未复制任何二进制。
- 命令行与二丫 runrec.sh 相同:place `--threads 4 --route-threads 4 --route-check 3000000`(eco:`--eco-rounds 4 --eco-candidates 5`;routable:`--rounds 3 --loop-time 1500`),route `--priority-order-only --soft-zones --time 300 --no-kb --threads 4`,`nice -n 10`。2 条 lane 并行(按种子 2、3 各一条)。DRC:先 `--severity-all`(INDEX 的 drc_errors,与二丫口径一致,zone 未刷新),再 `--refill-zones --severity-error`(drc_refill)。
- 负载:两条 lane 的 router 重叠时 load 最高达 29(base 阶段结束时),eco/routable 阶段 load 约 8-10。各 run 起止负载见 meta.txt。

## 全部 run
| run | 模式 | 种子 | 未连接 | DRC(severity-all) | 其中 clearance | DRC refill(error) | 移动件数 | 总秒数 |
|---|---|---|---|---|---|---|---|---|
| base-s1-a(二丫) | base | 1 | 3 | 1009 | - | - | 0 | 303 |
| base-s1-b(二丫) | base | 1 | 2 | 993 | - | - | 0 | 302 |
| eco-s1(二丫) | eco | 1 | 2 | 961 | - | - | 0 | 3032 |
| routable-s1(二丫) | routable | 1 | 4 | 1037 | - | - | 136 | 2629 |
| base-s2 | base | 2 | 2 | 1145 | 499 | 46(+3 unconnected) | 0 | 301 |
| base-s3 | base | 3 | 2 | 1145 | 499 | 46(+3) | 0 | 301 |
| eco-s2 | eco | 2 | 2 | 1126 | 480 | 46(+3) | 0 | 966 |
| eco-s3 | eco | 3 | 2 | 1145 | 499 | 46(+3) | 0 | 963 |
| routable-s2 | routable | 2 | 4 | 1158 | 401 | 37(+4) | 136 | 1154 |
| routable-s3 | routable | 3 | 5 | 1041 | 362 | 41(+5) | 136 | 1177 |

二丫 4 行取自其 INDEX.csv,未复核类别。place 耗时:eco 665/663 s,routable 853/877 s(二丫 eco-s1 2730 s、routable-s1 2325 s,铁蛋儿快 2.7-4 倍)。全部 route_exit=3(时限到,tracemaker 未布完),place_exit=0,drc_exit=0,无超时失败。

### DRC 类别(铁蛋儿,severity-all)
| run | clearance | hole_clearance | track_width | silk_edge | lib_footprint | silk_overlap | silk_over_copper |
|---|---|---|---|---|---|---|---|
| base-s2/s3 | 499 | 199 | 46 | 1 | 145 | 108 | 147 |
| eco-s2 | 480 | 199 | 46 | 1 | 145 | 108 | 147 |
| eco-s3 | 499 | 199 | 46 | 1 | 145 | 108 | 147 |
| routable-s2 | 401 | 199 | 37 | 1 | 145 | 176 | 199 |
| routable-s3 | 362 | 199 | 41 | 1 | 145 | 111 | 182 |

refill 之后(zone 刷新,仅 error)所有 run 只剩 track_width 与 unconnected:base/eco 46+3,routable-s2 37+4,routable-s3 41+5。clearance/hole_clearance 在 refill 后为 0,即 severity-all 口径下的大量 clearance 是 zone 未刷新与非 error 项。

### 未连接身份
| run | 未连接 |
|---|---|
| base-s2/s3, eco-s2/s3 | +1V1 U303.23-C307.1;+1V1 U303.23-U303.50 |
| routable-s2 | +1V1 U303.50-U303.45;+1V1 U303.23-C307.1;+1V1 U303.23-U303.50;+1V1 U303.45-C308.1 |
| routable-s3 | +1V1 C308.1-U303.45;+1V1 U303.50-C308.1;+3V3 U303.22-C314.1;+3V3 U303.33-U303.22;+1V1 U303.23-U303.50 |

对照 U303 的 4 条目标网:+1V1 U303.23-C307.1 在全部 6 个 run 里都没布通(base/eco 的两条未连接都在 +1V1 的 U303.23 上);+1V1 U303.50-45 仅 routable-s2 同样未通;+3V3 U303.44-48 与 XOUT R305.2-U303.21 在 6 个 run 中均已连通(本机未出现)。routable 额外引入 +3V3 与 C308 相关的新断点。

### 移动件
- eco:所有 run 0 件移动(place 阶段 0 parts moved,eco 实际等价于 base 的原布局)。
- routable:s2 与 s3 均 136 件移动(与二丫 s1 的 136 相同);s2 最大位移 101.40 mm、90 件带旋转变化,s3 最大 97.05 mm、86 件带旋转变化;无换面。完整 ref,dx,dy,rot 见各 run 的 moved_parts.json。

## 结论
1. **eco 不优于 base**:eco 一件都没移动,未连接数与 base 相同(2),DRC 与 base 持平(s3 完全相同 1145,s2 少 19 个 clearance,是 route 阶段随机性,不是布局改进)。二丫 eco-s1(2/961)对 base-s1-b(2/993)的 -32 也在同一量级的噪声里。耗时代价:place 约 11 min(铁蛋儿)或 45 min(二丫)。
2. **routable 劣于 base**:未连接 4/5 对 2(+2、+3),并引入 +3V3 新断点;clearance 少 100-137,但 silk_overlap/silk_over_copper 增加(s2 +68/+52),总 DRC s2 +13、s3 -104,方向不一致;refill 后 error 37/41 对 46 稍低,但 unconnected 4/5 对 3。二丫 s1 同样是 4 未连接。三个种子(s1/s2/s3)未连接始终 ≥4,没有一个种子好过 base。
3. **种子间离散 vs 效应量**:
   - 未连接数:base 在 2-3 之间波动(同种子同输入 3 与 2:route 的时限/线程不确定);eco 2(s1/s2/s3);routable 4/4/5。base→routable 的 +2~+3 大于 base 内部 1 的波动,**稳定变差**;eco-base = 0,**无效应**。
   - 总 DRC(severity-all):铁蛋儿 base 1145(s2=s3,完全相同),二丫 base 993-1009(同种子两次相差 16),跨机器差约 140;eco 1126-1145(s 间差 19);routable 1041-1158(s 间差 117)。eco 相对 base 的 -0~-19 在噪声内;routable 的 s 间差(117)与其相对 base 的差(+13/-104)同量级,因此总 DRC 上看不出可靠优劣,只有未连接数这一项有可靠的负向差异。
   - 种子对 base 无意义(base 不用 seed,s2、s3 逐位相同);对 eco 影响仅体现在 route 随机性。
4. 总 DRC 的跨机器差(二丫 ~1000 vs 铁蛋儿 1145)说明 severity-all 口径依赖运行环境(未刷新 zone),应以 refill 口径或 unrouted 为准。二丫 INDEX 没有 refill 列,无法直接对比。

## 局限
- 只跑了种子 2、3 各一条(tracemaker route 阶段仍是 300 s 墙钟,受并行负载影响),未重复同种子多次来量化 route 噪声;eco/routable 的 s1 数据来自二丫,线程负载与 CPU 不同。
- 源码无 .git,commit 为二丫 meta 记录;CLI11 版本与二丫不一定一致。
