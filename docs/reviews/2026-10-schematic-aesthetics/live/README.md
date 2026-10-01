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
