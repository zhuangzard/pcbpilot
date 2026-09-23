# pcbrouting

可被其他 Go 项目直接引用的 PCB 局部寻路内核：

```go
import "github.com/zhuangzard/pcbpilot/pkg/pcbrouting"

request := pcbrouting.Request{
    From: pcbrouting.Point{0, 0}, To: pcbrouting.Point{100, 0},
    Step: 5, MaxDetour: 20, MaxStates: 100000,
}
result, err := pcbrouting.Solve(ctx, request, segmentClear)
// 仅当 err == nil && result.Status == pcbrouting.Found 时有可用路径。
// 复验不调用 Solve：
err = pcbrouting.Check(ctx, request, result.Points, segmentClear)
```

`Solve` 找到路径后会调用同包的 `Optimize45`，在逐段重新检查净距的前提下，先减少真实转折，
再缩短中心线。调用方已有合法路径时也可单独调用该函数；输入超过 4096 点时只做确定性压缩
和复验，避免可见性优化产生无界计算。

`segmentClear(from, to)` 由调用方实现：对**整段**应用真实障碍、所选铜层、线宽、净距和板边
规则；零长度查询表示端点能否容纳铜。先拒绝未知几何，不能只查中心线端点。调用期间使用
固定快照，回调必须确定且无编辑器副作用。独立 `Check` 重放候选几何，不信任求解器状态或
测量值；宿主级 DRC 仍是另一层验证。

包仅依赖 Go 标准库，不引用 `internal/app`、Cobra、daemon、Connector 或文件系统。
当前仓库直接把它嵌入 `pcbpilot pcb route solve/check`，晶振规划器也引用同一实现；不需要
另装 CLI。该包随仓库 Go module 分发，尚无独立 module 或稳定 v1 API 承诺。

目前支持单层零过孔、八方向网格搜索与最多 45° 转向，以及非网格终点的合法接入。
坐标、步长和绕行距离使用统一单位，数值比较容差为该单位的 `1e-6`。
`MaxDetour` 是到端点轴对齐包围矩形的欧氏距离上限，不在对角方向额外放宽。
搜索包含方向状态、确定性平局顺序、context 取消、状态数量预算和有界路径压缩。

`found` 证明一条可行路径，不承诺最优。其余可行性搜索结果为 `incomplete`，原因包括
`endpoint-blocked`、`no-path-within-bounds`、`state-budget`、`grid-limit`；它们都不证明
物理板全局无解。非法请求与 context 取消还返回 Go error。
多网协调、换层、差分、等长、阻抗及回流策略不属于本次提取范围。

验证：`go test ./pkg/pcbrouting`；性能采样：
`go test ./pkg/pcbrouting -run '^$' -bench BenchmarkSolveDetour -benchmem`。
