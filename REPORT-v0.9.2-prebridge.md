# v0.9.2 第二阶段报告：同网相邻引脚预桥接（prebridge）

分支 exp/place-route，仅本地提交，未 push / tag / release。所有硬门禁未放松；唯一豁免仍是第一阶段的 `--no-review`。

## 做了什么（P1）
- `pcbpilot kicad prebridge --pcb X --intent I --rip-up [--ref U303] --out Y [--plan p.json] [--dry-run]`
  （`internal/app/kicad_prebridge.go`、`cmd_kicad_prebridge.go`，3 个单测）。
- 规划：同一器件上同网、同层、同尺寸、沿短轴相邻、圆心距 ≤ 19.7 mil（0.5mm）的焊盘对。
  桥 = 焊盘中心到中心的**锁定**短走线（bridge.py `addTracks` 新增 `locked`，rip-up 保留，DSN 里按固定铜处理）。
- 宽度 = 意图宽，只在“对异网焊盘/走线/过孔的间距”要求下才缩，且不低于意图 min；放不下就跳过并写原因。
  PicoRick U303 上 +3V3 的 43-44、48-49 两座桥：意图 15.75 mil，缩到 11.82 mil（0.4mm 间距下的几何上限，≥ 意图 min 10）。
- 整板扫描只出这 2 座桥（别的器件没有同网相邻的细间距焊盘）。
- 顺带：bridge 与 kicad-cli DRC 调用现在放进独立进程组，超时杀整组（`internal/kicad/proc_unix.go`）。

## 对比（P2）：PicoRick_One_revA，同一约束，`--rip-up --no-review --waivers w.json`
| 运行 | 未布通 | KiCad DRC 错误 | 墙钟* |
|---|---|---|---|
| fastroute 无桥（第 1 次） | 5 | 9（clearance 4 + unconnected 5） | 430s |
| fastroute 无桥（重跑，同二进制） | 5（相同的 5 条） | 9（相同） | 518s |
| **fastroute 有桥** | **3** | **15（clearance 12 + unconnected 3）** | 359s |
| tracemaker 无桥 | 2 | 2（unconnected 2） | 501s |
| **tracemaker 有桥** | **5** | 5（unconnected 5） | 549s |

*墙钟受二丫机器上其它负载影响，只能看量级，不作结论。

门禁：五个运行的 kicad-drc / post-layout-sim（未跑）/ route-complete / silkscreen / board-manual / design-report / signoff / gate-set 全部仍 FAIL，没有任何一个运行通过整板；tracemaker 两个运行另有 intent-widths FAIL。
DRC 警告类计数（lib_footprint、silk_*）无桥/有桥相同（fastroute：13/25/16/10，track_dangling 1→2）。
未布通明细：
- fastroute 无桥：+3V3 22→C314.1、D6、+1V1 23→C307.1、USB_DM、USB_DP。
- fastroute 有桥：+3V3 U303.42→43、+1V1 45→50、+1V1 23→C307.1。
- tracemaker 无桥：+1V1 U303.23→C307.1 / 23→50。
- tracemaker 有桥：+1V1 的 45/50/23 共 5 条。

## 结论与失败原因
1. **桥本身是干净的**：桥处无 DRC 违规、无阻焊桥接报错，宽度/间距按规划满足。
2. **不能减少失败的根因**：目标失败（+3V3 22、+1V1 23/45/50、USB 46/47）是**单个引脚的逃线空间/通道**问题，不是同网相邻焊盘的互连；2 座桥只合并了 43-44、48-49，没碰那些连接。
   +1V1 的 45/50（间隔 46-49，非相邻）仍要绕出，预桥接按规格不处理。
3. **结果不能说变好**：fastroute 未布通 5→3，但 DRC 错 9→15（clearance 4→12，多数在远离 U303 的位置）；tracemaker 反而 2→5。fastroute 无桥重跑完全一致（确定性），所以差异确实由桥引起，但属于对整板求解的扰动（锁定铜改变了路径搜索），不是对症改善；方向不一致。
4. 局限：只测了一块板、每个配置一次；tracemaker 未重复测噪声。

## 是否建议产品化
**不建议**作为默认 pre-route 步骤。收益未被证明（一好一坏，且 fastroute 的“好”伴随更多 clearance 错），而该步骤要求锁定铜、增加变量。
可保留为 `kicad prebridge` 实验命令。若要真正解决这批失败，应转向：单引脚逃线预置（扇出短桩+过孔，按通道度量选方向）或让布线器识别同网焊盘组；非相邻同网焊盘的外侧绕桥会堵相邻引脚的逃线，不推荐。
