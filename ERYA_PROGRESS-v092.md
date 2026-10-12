# v0.9.2 阶段二进度（预桥接）

- 2026-10-12 11:01 起步。基线配方在 /tmp/pico3（fastroute, --rip-up --no-review，5 未布通，430s，exit=1）。
  板子 1267 条已有线 → 预桥接必须在 rip-up 之后做；bridge.py edit 已支持 addTracks，补 locked 标志让 rip-up 保留桥。
  U303 pitch 15.75mil，焊盘 7.87×34.4mil，同网相邻对：+3V3 43-44、48-49（5 条未布通里并不直接含它们）。
  方案：Go 规划器（宽度 = min(意图宽, 对异网焊盘 5.9mil 间距允许的最大宽) 且 >= 意图 min）+ `kicad prebridge` 子命令。
- 2026-10-12 11:05 prebridge 子命令+规划器+3 个单测过；U303 dry-run 规划出 43-44、48-49 两座 +3V3 桥（11.82mil，受 5.9mil 间距缩颈，意图 15.75/min 10）。下一步：写桥板 → 同配方 fastroute 对比。
- 11:05 已启动桥板 fastroute 运行：/tmp/pico4/run.sh（等 times.txt ALLDONE）
- 11:06 更正：上条「已启动」不实（macOS 无 setsid，运行未起）；现已用 nohup 真正启动 /tmp/pico4/run.sh（pid 54614）。
- 11:13 桥板 fastroute 完成：未布通 5→3，但 DRC 错 9→15（clearance 4→12），桥处无违规。结果带随机性，已起噪声对照：未桥基线重跑 + tracemaker 桥/未桥（run2.sh A/B，2 并发）。
- 11:30 全部完成：fastroute 重跑与首跑逐条一致（确定性）；tracemaker 无桥 2 / 有桥 5 未布通。报告 REPORT-v0.9.2-prebridge.md，结论：不建议产品化。

# v0.9.2 阶段三进度（单引脚逃线预置 escape-stubs）

- 2026-10-12 起步。实现 `pcbpilot kicad escape-stubs`（internal/app/kicad_escapestubs.go、cmd_kicad_escapestubs.go；bridge.py addVias 新增 locked）。
  规划：channelOf 度量选 tight 焊盘（viaDirs<=2 或 run<阈值）→ 8 方向找最短可行「桩+过孔」→ 以净空余量/远离器件体评分；最受限者先放（每轮重算可行方向数）。
  过孔：板规则过孔，环 clearance+0.25mil 安全余量，同网 SMD 焊盘回避，孔到孔≥max(过孔径, 钻孔+9.85mil)。
- 首次 dry-run：viaDirs<=2 在裸板上选中 72 个焊盘（含 IC101/102），U303.22 被相邻桩挤掉 → 改为动态最受限优先；性能待查（二次方重算）。
- 11:50 已启动 /tmp/pico5/run.sh A（fastroute 全桩→U303 桩）与 B（tracemaker 全桩→无桩基线），2 并发
- 12:11 全部完成：fastroute 全桩 5/5 未布通（DRC 9→5），U303 桩 5（DRC 6），tracemaker 2→19。结论无效，报告 REPORT-v0.9.2-escape-stubs.md
