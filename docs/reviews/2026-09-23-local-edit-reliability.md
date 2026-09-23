# 局部改版与 daemon 恢复修复记录

来源：2026-09-23 Windows / EasyEDA Pro 3.2.186 工具问题反馈；源码基线为
`dev@0bf26d4`。外部反馈不是现场复现证据，本轮没有操作客户工程。

本轮完成三个有明确代码依据的修复：

- **T2 已知不兼容的 net_label**：daemon 在派发前拒绝 V3 或未知产品版本，覆盖直接 typed
  create 和 connect_pin；autoconnect 在读取画布/执行批次前检查全部连接，dry-run 同样拒绝。
  不自动换成 netport，不声称 V4 的接口调用必定成功，也未改变 FIFO 超时语义。
- **T4 全页历史问题阻止局部新增导线**：完整检查本次路径；写后按对象、几何和数量比较新问题，
  保留旧问题诊断。旧零长度段在本次接触拓扑比较中不制造结点，新增零长度段仍失败；
  完整绘图比较仍严格拒绝零长度段。缺测、路径未落地、异常结点和上下文漂移仍失败。
- **T6 生命周期**：新增 `daemon stop/restart`，restart 复用 start 参数并以前台方式运行。
  health 上报 PID；旧版 daemon 回退到端口所属 PID，Windows 用 netstat 和本机进程接口。
  不依赖可写 PID 文件；拒绝未知、冲突、自身或远程 PID，不再输出“正在替换 pid 0”。

验证边界：

- `make test`、`make skill-check` 通过；新增 daemon/schguard/app 定向回归的 `-race` 检查通过。
- Windows amd64 CLI 构建与 app 测试二进制交叉编译通过；没有 Windows 实机执行证据。
- 新增回归覆盖旧零长度线/旧引脚方向保留、新增缺陷、同 ID 几何变化、缺测、未落地、
  V3/未知版本拒绝、整批写前拒绝及 dry-run、真实子进程停止和第三方进程保护。
- 本机 `daemon health` 返回 `windows: []`，固定 `esp32MiniRequire.md` 全流程与旧工程局部
  改版现场回归均未运行。这里的结论为 **offline-verified**，现场验收待补。

剩余：T1 多 renderer 的根因、T3 SDK 折线失败、T5 宿主队列与重连恢复、T7 统一 pin/回退
未在本轮解决。原报告缺少原始审计日志和最小工程，不能据此假定三项代码修复解决全部故障。

用户操作说明维护在公开 Skill 的 [原理图参考](../../.agents/skills/pcbpilot/references/schematic.md)
和 [环境恢复参考](../../.agents/skills/pcbpilot/references/environment-setup.md)。
