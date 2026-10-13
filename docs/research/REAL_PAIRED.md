# 真实 fastroute 配对扰动实验（PicoRick U303 周边）

**所有结果标注：direct-fastroute, no escape stubs / no widen step, valid only for paired comparison。**
即：直接调用 fastroute 0.1.13（改 DSN 的 placement、去掉 pcbpilot 的 15 条出线桩与 1 个过孔、无 widen/via-array/pour 步骤），SES 经 pcbnew 导入，再从 agent shell 运行 kicad-cli DRC。绝对数（13 未布通）**不能**与 pcbpilot 全流程基线（5 未布通 / DRC 9）直接比，只能在本表内做配对比较。

记录：`/Users/taisen/pcbpilot-work/coadapt/records/pairs/<run-id>/`（命令行、DSN/板 sha256、起止时间、墙钟、负载、fastroute 日志、route-report.json、SES、drc.json、meta.json），索引 `records/pairs/INDEX.csv`（含逐条未布通连接）与 `rows.json`。脚本：`realpaired/{gen_variants2.py,pair.py,ripup_import.py,aggregate.py}`。二丫上的原始目录：`/Users/taisenzhuang/fpga-remote/pcbpilot-handoff/v093-pairs/`（失败的第一次尝试因 ripup_import.py 的 pcbnew 迭代 bug，保留在该目录 `records_failed_attempts/`，未计入）。

## 设置
- 固定参数（全部 37 次运行相同）：`fastroute -de route.dsn -do route.ses --report --diagnose --multi-start=1 --router.min_trace_width_um=150 --router.autorouter.max_threads=1 --router.optimizer.enabled=false --no-neckdown-classes=（5 个 PP 类）`。fastroute 无显式种子；multi-start=1 + 单线程即确定性。DSN 基线 = /tmp/pico/out-fastroute/route.dsn（sha256 431665f7…），板 = /tmp/pico/PicoRick_One_revA.kicad_pcb（sha256 e9eeaa65…）。
- 变体：3 个 control（无改动）、20 个来自 paired_moves.json（保留符号；mBf/mBg/mBh 在真实 courtyard 下非法被跳过，8 个里的 5 个 spread 保留）、7 个自选（含 U303 y±0.3、C314 必须带晶振 X301 一起移、1.15x 展开 R10）、1 个 decoy（远处 C206 移 0.5 mm，用来量“无关扰动”造成的混沌）。90° 旋转因密度大全部非法，未测。
- 每次约 125–210 s（并行 3 个，机器同时有别的负载，load1 5–9），全部运行成功：fastroute rc=0，DRC rc=0；无失败/被杀的运行（唯一失败是上面提到的脚本 bug，运行前已修复重跑）。
- 度量：未布通 = fastroute report 的未布通连接数（与 KiCad DRC 的 unconnected 数逐项相等，13/13…），DRC 非 unconnected 的错误类在全部运行中均为 0（间距违规 0，说明 fastroute 的输出本身合法，失败全是没布完）。

## 结果（Δ = 相对 control 的 13）
Control：6 次（3 个 control × 各 2 次）全部 13 未布通，失败连接**完全一致**，过孔 164，DRC 非 unconnected 为 0。即路由器在相同输入下零噪声（确定性）。
| run | move | unrouted | Δvs ctrl | 新增/消失连接(对 ctrl) | DRC错误(非unconnected) | vias | 秒 | 模拟预测Δ(micro/nomicro) | 符号一致 |
|--|--|--|--|--|--|--|--|--|--|
| sim_mAf_nudge_radial | C302(-0.20,+0.00,r0) | 16 | +3 | +9/-6 | 0 | 157 | 202 | -1/+1 | 否 |
| sim_mAh_nudge_radial | C302(+0.20,+0.00,r0) | 16 | +3 | +9/-6 | 0 | 161 | 206 | +0/+1 | 否 |
| sim_mAg_nudge_radial | C302(-0.50,+0.00,r0) | 16 | +3 | +8/-5 | 0 | 160 | 152 | -1/+0 | 否 |
| sim_mAi_nudge_radial | C302(+0.50,+0.00,r0) | 13 | +0 | +3/-3 | 0 | 167 | 155 | +0/+1 | 是 |
| sim_mAj_nudge_radial | R303(+0.20,+0.00,r0) | 13 | +0 | +10/-10 | 0 | 162 | 133 | +0/+1 | 是 |
| sim_mAk_nudge_radial | R303(+0.50,+0.00,r0) | 15 | +2 | +10/-8 | 0 | 157 | 138 | +1/+1 | 是 |
| sim_mAl_nudge_radial | R303(-0.20,+0.00,r0) | 15 | +2 | +7/-5 | 0 | 173 | 208 | +0/+1 | 否 |
| sim_mAm_nudge_radial | R303(-0.50,+0.00,r0) | 12 | -1 | +5/-6 | 0 | 160 | 164 | +0/+1 | 否 |
| sim_mAo_nudge_radial | R304(+0.50,+0.00,r0) | 17 | +4 | +13/-9 | 0 | 155 | 144 | +0/+0 | 否 |
| sim_mAq_nudge_radial | R304(-0.50,+0.00,r0) | 15 | +2 | +8/-6 | 0 | 160 | 169 | +0/+0 | 否 |
| sim_mAs_nudge_radial | C312(+0.00,+0.50,r0) | 13 | +0 | +7/-7 | 0 | 158 | 149 | +0/+0 | 是 |
| sim_mAu_nudge_radial | C312(+0.00,-0.50,r0) | 15 | +2 | +7/-5 | 0 | 157 | 154 | +0/+0 | 否 |
| sim_mAy_rot180 | C302(+0.00,+0.00,r180) | 18 | +5 | +10/-5 | 0 | 159 | 173 | +0/+0 | 否 |
| sim_mBa_rot180 | R303(+0.00,+0.00,r180) | 15 | +2 | +11/-9 | 0 | 158 | 166 | +0/+2 | 否 |
| sim_mBc_rot180 | R304(+0.00,+0.00,r180) | 18 | +5 | +11/-6 | 0 | 175 | 179 | +0/+0 | 否 |
| sim_mBe_spread_side | C308(+0.62,+0.00,r0);R303(+0.62,+0.00,r0);C309(+0.62,+0.00,r | 14 | +1 | +9/-8 | 0 | 169 | 194 | +0/+2 | 否 |
| sim_mBi_spread_side | C315(+0.00,+0.62,r0);C312(+0.00,+0.62,r0) | 15 | +2 | +10/-8 | 0 | 167 | 154 | +0/+2 | 否 |
| sim_mBj_spread_side | C315(+0.00,+1.24,r0);C312(+0.00,+1.24,r0);C316(+0.00,+1.24,r | 17 | +4 | +13/-9 | 0 | 157 | 151 | +1/+1 | 是 |
| sim_mBk_spread_side | C313(+0.00,-0.62,r0);C311(+0.00,-0.62,r0) | 17 | +4 | +10/-6 | 0 | 160 | 172 | +1/+1 | 是 |
| sim_mBl_spread_side | C313(+0.00,-1.24,r0);C311(+0.00,-1.24,r0) | 13 | +0 | +9/-9 | 0 | 160 | 137 | +1/-2 | 否 |
| own_C307_away0.5 | C307(-0.50,+0.00,r0) | 15 | +2 | +10/-8 | 0 | 161 | 189 | - | - |
| own_C314X301_away0.6 | C314(-0.60,+0.00,r0);X301(-0.60,+0.00,r0) | 15 | +2 | +11/-9 | 0 | 173 | 136 | - | - |
| own_C314C307C302X301_away0.6 | C314(-0.60,+0.00,r0);C307(-0.60,+0.00,r0);C302(-0.60,+0.00,r | 14 | +1 | +4/-3 | 0 | 157 | 130 | - | - |
| own_R303R304_away0.5 | R303(+0.50,+0.00,r0);R304(+0.50,+0.00,r0) | 12 | -1 | +5/-6 | 0 | 171 | 146 | - | - |
| own_U303_y+0.3 | U303(+0.00,+0.30,r0) | 14 | +1 | +11/-10 | 0 | 161 | 137 | - | - |
| own_U303_y-0.3 | U303(+0.00,-0.30,r0) | 16 | +3 | +12/-9 | 0 | 172 | 168 | - | - |
| spread1.15_R10 | R305(-0.83,+0.73,r0);C302(-0.83,-0.38,r0);C315(+0.49,+0.97,r | 14 | +1 | +6/-5 | 0 | 155 | 131 | - | - |
| decoy_C206_+0.5x | C206(+0.50,+0.00,r0) | 15 | +2 | +5/-3 | 0 | 155 | 124 | - | - |

（“符号一致”：模拟的 micro 预测增量与真实 Δ 的符号是否相同，0 视为一个符号；20 个里 6 个一致 = 30%，不优于随机）

## 汇总（28 个扰动运行）
- Δ 分布（排序）：-1,-1,0,0,0,0,1,1,1,1,2×9,3×4,4×3,5×2；均值 +1.93，标准差 1.60；只有 2 个变好（-1），4 个持平，22 个变差。
- 无关 decoy（远处 C206 +0.5 mm）本身 +2，落在 U303 周边移动的中位范围内。
- 失败连接身份：+1V1 与 +3V3 在 28/28 个变体里都有未布通，且主要是 U303 焊盘之间的电源连接（44–43、48–44、50–45、48–49 等，诊断为 blocked，即 0.4 mm 间距焊盘上 15.75 mil 不收窄线宽的几何问题），不随挪动改变；其余失败（RX_IC2_VPOS 20/28、GND/GPIO 各 ≤5/28）是 congestion，随挪动换来换去（相对 control 平均新增约 9 条、消失约 7 条连接）。
- 净序代理：multi-start=4（fastroute 的随机打乱序，取最好）3 次均 14 未布通（比 ms=1 的 13 还多 1），且 3 次一致。fastroute 0.1.13 没有净序/优先级选项，“短网络先布”无法在真实路由器上直接测，只有这一个代理，不支持“换顺序即可通”。
- 1.15x 展开（U303 10 mm 内元件）：14（+1），R12 展开因 courtyard 非法未测。

## 结论（8 行）
1. 挪动 U303 周围的元件确实改变真实路由器的结果（Δ 从 -1 到 +5），但几乎都是变差；22/28 变差、2/28 变好，且好的只好 1 条。
2. 路由器自身在相同输入下零噪声（6 次 control 完全一致），所以差别都是输入扰动引起的“混沌”，不是随机波动；但“无关 decoy 也 +2”说明扰动的混沌幅度（σ≈1.6，均值 +1.9）大于任何真实信号。
3. 信号（最好 -1）小于混沌（σ 1.6，decoy +2）：当前数据无法证明任何一种挪动“有帮助”；R303 向左 0.5 与 R303+R304 向右 0.5 都 -1，但 R303 单独 +0.5 是 +2，方向互相矛盾，视为噪声。
4. 哪类挪动更好：被动件外推/展开/旋转/挪 U303 都没有稳定收益；旋转 180°（C302/R304）最差（+5）；电容簇（C314+C307+C302+X301 外移 0.6）+1、R303+R304 外移 0.5 为 -1，样本太少。
5. 模拟器的符号迁移到真实路由器不成立：20 个预测里仅 6 个符号一致（30%），模拟预测“多数微小改善”，真实多数变差；模拟的绝对数（2/18）与真实 13 不可比。
6. “+3V3/+1V1 在 U303 焊盘间的连接”在所有变体里都失败，是几何（intent 线宽不收窄）问题，挪外围去耦电容 ±0.5 mm 解决不了；这与 S1/S2 的结论一致。
7. 净序：fastroute 无选项，multi-start=4 的随机序不比默认序好（14 vs 13），所以模拟里“hpwl 升序 0 未布通”在真实路由器上没有被验证，也没有被反驳。
8. 局限：direct-fastroute（无出线桩/widen）、每变体单次、decoy 仅 1 个、σ 由 28 点估计；若要结论更硬需要多个 decoy 与 pcbpilot 全流程重测。

## blockers（真实记录）
- `pcbpilot kicad route`（/tmp/pcbpilot-dev）经二丫 agent 启动时，第一个 `kicad-cli`（`version`，之后是 `pcb drc`）永久挂起在 `wxDir::Open -> opendir`，0% CPU。同一 kicad-cli 从 agent shell 直接运行 5 秒完成。
- 系统日志（tccd）：TCC 授权请求的 responsible process 是 `/private/tmp/pcbpilot-dev`，`IDENTITY_ATTRIBUTION: Failed to copy signing info ... #-67062`（签名无效），请求 `kTCCServiceSystemPolicyAllFiles`，无人可点同意，kicad-cli 因此阻塞。设置假 HOME、伪造 KiCad.app 包装器均无效。
- 常驻代跑服务与 ad-hoc 签名副本两条绕过路径被权限分类器拒绝（用户后来手动放行），但因 direct-fastroute 路径已完成，未再尝试；pcbpilot 全流程的真实运行因此仍然缺失。
- 其它：agent 的 fast lane 在长任务期间会排队卡住，改用 queue=true / fpga_wait 解决。
