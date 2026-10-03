# 原生总线现场验证（2026-10-01）

- 宿主：EasyEDA Pro 桌面版 V3 3.2.149.88089769，connector 0.7.1-dev.2，daemon dev（v0.7.0-…）。
- 工程：ceshi，原理图页 P1（905bb85957eaf435），空白区 x 600–800 / y 200–320（避开器件 bbox、导线、文字、模块框 ≥40）。
- `api probe`：`sch_PrimitiveBus.create/getAll/delete` 与 `sch_PrimitiveAttribute.createNetLabel` 在该 V3 上均为 function。
- 首次 `sch bus list` 报 EDA_API_UNAVAILABLE：connector 用 `(globalThis as any).eda` 取 API，编辑器沙箱的 `eda` 不在 globalThis 上 → 修为沙箱全局 `eda`（0.7.1-dev.2）。

| 步骤 | 结果 | 证据 |
|---|---|---|
| list | 0 条 | list1.json |
| create `TBUS[0:7]` 两分支 [[620,260,780,260],[620,260,620,320]] | 已建 3c739e2eacd69563；宿主回读为一条往返折线 620,320→620,260→780,260→620,260，旧核对按嵌套数组逐位比较误报 unverified → 改为无向线段集合比较 | create.json |
| list | 1 条，名称/几何一致 | list2.json |
| save → doc reload → list | 持久化，同 ID 同几何 | list3.json |
| delete --ids 3c739e2eacd69563 | verified，survivors 0 | del.json |
| save → reload → list | 0 条；页面 51 器件、126 导线段与测试前逐 ID/坐标一致 | list4.json |

状态：V3 3.2.149 live-verified；V4 未验证。总线本身不提供电气连接证据，成员网连通仍以逐 pin 网表与 `sch check` 为准。

---

# 布局原生总线 Apply 现场验证流程（2026-10-03 起草，待主 Agent 执行）

用户决定（2026-10-03）：布局生成默认画原生总线。本节验证 `sch bus apply` 的日志/回读/替换/回滚链路。
**只读为主**：唯一的写入是在 ceshi P1 空白区（x 600–800 / y 200–320，同上表）建/删一条总线，最后回滚并核对页面
与开始时逐 ID 一致。不动器件、导线、标签；不用 GUI，不用 `debug.exec_js`。任何一步与预期不符即停，保留输出。

前提：`apply` 先做成员检查——总线名对应的成员网必须已在页上的引脚上、且各有自己的标签/端口（总线不是连接证据），
所以测试名取自 P1 **已有**的组（第 3 步挑）。

```bash
P=ceshi; D=905bb85957eaf435; W=live-bus; mkdir -p $W
# 1 宿主与 API（只读）：windows[] 须精确含 ceshi/P1；记录宿主形态与版本
pcbpilot health
pcbpilot api probe --project $P --path sch_PrimitiveBus.create --path sch_PrimitiveBus.getAll \
  --path sch_PrimitiveBus.get --path sch_PrimitiveBus.delete
# 2 基线（只读）
pcbpilot sch bus list --project $P --doc $D > $W/b0.json
pcbpilot sch list --project $P --page $D --stay --include-pins --include-bbox --include-wires > $W/page0.json
# 3 挑一个成员已带标签/端口的组（只读，离线）；取其 NativeBusName：索引组 NAME[a:b]，协议组用组名
#   （前缀已含协议名用前缀，如 SPI1/UART0；否则 PREFIX_KIND，如 U0_UART；无前缀用 KIND）
pcbpilot sch bus candidates --snapshot $W/page0.json --json
#   没有任何组（或成员无标签）→ 只做第 4 步离线 dry-run，记 apply「live-unverified：P1 无可用成员组」，结束。
# 4 计划（空白区；竖直主干 x=700，两条 10 长梳齿，5-unit 格，不碰任何图元）与离线 dry-run
cat > $W/bus-plan.json <<'JSON'
[{"busName":"<组名>","members":["<成员1>","<成员2>"],
  "line":[[700,220,700,300],[700,220,690,220],[700,300,690,300]]}]
JSON
pcbpilot sch bus apply --plan $W/bus-plan.json --dry-run --bus-list $W/b0.json   # creates[0].line 为嵌套多段线
# 5 首次 apply：期望 status=applied、created 1、deleted 0、verification 含 live-verified（V4 为 host-unverified）
pcbpilot sch bus apply --plan $W/bus-plan.json --journal $W/journal.json --project $P --doc $D > $W/a1.json
pcbpilot sch bus list --project $P --doc $D > $W/b1.json        # 比 b0 多 1 条；宿主可能回读成一条往返折线
pcbpilot sch bus check --project $P --doc $D                    # exit 0；该总线 ok，无 member-without-label
# 6 重跑 = 替换（按日志 ID + 名字 + 几何）：期望 deleted=[a1 的 ID]、created 1 个新 ID、总数不变（不重复）
pcbpilot sch bus apply --plan $W/bus-plan.json --journal $W/journal.json --project $P --doc $D > $W/a2.json
pcbpilot sch bus list --project $P --doc $D > $W/b2.json        # 条数 = b1
# 7 持久化：保存 → 重载 → 回读同 ID 同几何；美观度报告 N3 记该组为 native bus、busChecks ok
pcbpilot sch save --project $P --doc $D
pcbpilot doc reload $D --project $P
pcbpilot sch bus list --project $P --doc $D > $W/b3.json        # = b2（ID、名字、线段集合）
pcbpilot sch aesthetics --project $P --doc $D --json > $W/aes.json
# 8 回滚（按日志精确 ID）：期望 status=rolled-back、deleted=[a2 的 ID]；用户已有总线（b0 中的）原样保留
pcbpilot sch bus apply --rollback --journal $W/journal.json --project $P --doc $D > $W/r.json
pcbpilot sch bus list --project $P --doc $D > $W/b4.json        # = b0
# 9 收尾：保存 → 重载 → 回读，与基线逐 ID 对账
pcbpilot sch save --project $P --doc $D
pcbpilot doc reload $D --project $P
pcbpilot sch bus list --project $P --doc $D > $W/b5.json        # = b0
pcbpilot sch list --project $P --page $D --stay --include-pins --include-bbox --include-wires > $W/page1.json
#   page1 与 page0：器件/导线/标记的 ID 与坐标一致（只多过又删掉一条总线）
```

负例（可选，不写页面）：把 `bus-plan.json` 的成员换成页上无标签的网 → 第 5 步应在**任何写入前**以
`member-without-label` / `member-without-pin` 失败、`b` 不变；把日志里的 ID 对应总线在别处改过几何（不要在本页做）
→ 应以 “changed on the page … left untouched” 拒绝。

结果记录：宿主形态与精确版本、connector 版本、每步 JSON（`$W/*.json`）、是否全部符合；V3 与 V4 分别计。
