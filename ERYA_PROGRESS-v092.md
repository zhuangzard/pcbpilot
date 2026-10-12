# v0.9.2 阶段二进度（预桥接）

- 2026-10-12 11:01 起步。基线配方在 /tmp/pico3（fastroute, --rip-up --no-review，5 未布通，430s，exit=1）。
  板子 1267 条已有线 → 预桥接必须在 rip-up 之后做；bridge.py edit 已支持 addTracks，补 locked 标志让 rip-up 保留桥。
  U303 pitch 15.75mil，焊盘 7.87×34.4mil，同网相邻对：+3V3 43-44、48-49（5 条未布通里并不直接含它们）。
  方案：Go 规划器（宽度 = min(意图宽, 对异网焊盘 5.9mil 间距允许的最大宽) 且 >= 意图 min）+ `kicad prebridge` 子命令。
- 2026-10-12 11:05 prebridge 子命令+规划器+3 个单测过；U303 dry-run 规划出 43-44、48-49 两座 +3V3 桥（11.82mil，受 5.9mil 间距缩颈，意图 15.75/min 10）。下一步：写桥板 → 同配方 fastroute 对比。
- 11:05 已启动桥板 fastroute 运行：/tmp/pico4/run.sh（等 times.txt ALLDONE）
