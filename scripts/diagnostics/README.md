# PCB 空读诊断（#200）

先在 UI 中选中受影响的 PCB，保持该页活动。探针只读，不导入、不保存、不切页。

在 macOS/zsh 的仓库根目录运行（将工程名换成实际值）：

```sh
pcbpilot --project '工程名' debug exec --timeout 60 --code "$(cat scripts/diagnostics/pcb-empty-read.js)" > pcb-read-evidence.json
```

也可在已授权的 `debug.exec_js` 中执行该文件全文。`getAllPrimitiveId` 是独立的官方
枚举入口，不把 `import-changes` 的 componentsBefore/After 当独立计数——后者同样调用 getAll。

判读：

- `sameDocument:false`：前后身份不一致或缺失，不能比较计数。
- noArgs 与 undefined 不同：参数序列化路径差异，需要记录 SDK/宿主版本。
- ids 非零而 getAll 为零：ID 与实例读取通道不一致。
- top/bottom 非零而无层筛选为零：无筛选路径异常。
- 同一调用的首尾 noArgs 不同：数据或读取状态正在变化。
- 全部为零：仍不能判断真空板还是读取故障；需与原始工程及 UI 数据核对。

每个读取最多等 2 秒，超时不会取消宿主 Promise。输出仅用于定位，不提供“检查通过”结论。
