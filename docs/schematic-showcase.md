# 原理图展示图片与 Apply 动图

README 展示使用真实原理图的官方导图，对外以“门禁控制板示例”匿名展示。静态图片源自
`pcbpilot sch export-image --format png --out <path>`；GIF 使用同一次 SCH Apply
在关键阶段导出的图片，按原执行顺序加速播放。播放间隔经过压缩，不代表实际执行耗时。

公开示例不展示真实工程名称。导出前保存目标页图签的显示状态，临时使用
`sch titleblock --hide` 隐藏图签，再通过官方接口导出静图和所有动画阶段。
导出结束后恢复原图签状态、显式保存并回读核对。`@Project Name` 是平台只读投影，
不要修改它或重命名实际工程；README、图片文件名和相关发布说明也使用通用示例名称。

## 从完整队列捕捉

先按 [SCH Apply](design-apply-playbook.md) 的流程保存完整快照，回读目标并生成带守卫的
完整队列。录制重建过程时，清页须完整备份并由新鲜基线守卫保护。可以由独立的受保护清页队列
核对基线、清除并验证零残余，再读取空页新快照生成受保护 Compose 队列；不能裸清页。

捕捉输入限于明确填写 `meta.project` 和 `meta.doc` 的单页受保护队列。脚本拒绝步骤及
`verify` 中的目标覆盖（包括 `flags`、`run`/`args` 参数与显式页面 payload）、页面导航和
管理动作，以及可隐藏跨页操作的脚本或嵌套 Apply。项目级只读 inventory 守卫仍可保留；
多页分别生成、捕捉各自的队列，导图目标始终由原队列继承。

```bash
python3 scripts/capture-sch-apply.py tmp/new-apply.json \
  --out-dir tmp/showcase-capture --frames 12
pcbpilot sch apply tmp/showcase-capture/capture-apply.json --dry-run
pcbpilot sch apply tmp/showcase-capture/capture-apply.json --yes
```

脚本只生成文件，不操作 EDA。它保留输入的全部元数据、原步骤、守卫、执行策略与顺序，
只插入 `run: "sch export-image"`，用 PNG 全页导图捕捉实际状态。从首件姿态完成后开始，
不导出清页后的空白页（实测空白页官方 PNG 接口可能超时）。每批器件在姿态修改后取样，
随后捕捉 NC、分批导线和网络标记、功能框完成状态；默认最多 12 帧，可设 6–20 帧，
阶段少时不会补造画面。所有导图路径均为绝对路径，工程/页面沿用原队列目标。
新增导图步骤使用 `retry:0`、`continueOnError:true`，只读录制失败记入 journal，
不会中断原始绘图；原有步骤策略不变，原有守卫失败仍停止。

输出包括 `capture-apply.json`、`capture-manifest.json` 和 `frames.ffconcat`。
manifest 记录原队列 SHA-256、每帧对应的原步骤 ID、步骤序号和阶段。
最后一帧通常在功能框检查后生成，后续仍须执行逐脚回读与严格门禁；
只有前置步骤通过，队列才会执行保存。
**已有最终画面不代表队列或验收成功。** 失败后保留 journal 和检查结果，回读后重新规划；
不要移除守卫、跳过失败步骤或通过 `--resume` 续跑受保护队列。

## 合成与发布

先查看实际生成的 PNG，并核对 Apply journal 与现有验证报告。脚本末尾会打印本地
`ffmpeg` 合成命令，也可以运行下面的入口先机械检查每张 PNG 都存在且画布尺寸一致，再合成：

```bash
python3 scripts/capture-sch-apply.py --gif tmp/showcase-capture/capture-manifest.json
```

该步骤只读本地图片并调用已安装的 `ffmpeg`，不操作 EDA。使用可变帧率保留每个阶段的停留时长，
避免重复编码静止画面；保留首尾画面，按阶段加速播放，生成 1440 像素宽的
`schematic-apply.gif`。命令只使用捕捉到的原图，不生成器件或连线；不覆盖已有 GIF。
每次录制使用新的输出目录，避免将旧图混入新演示。

项目文档应同时保留一张可点击查看细节的最终静态图。GIF 附注注明“真实 Apply 阶段导图，
加速展示”；已有 DRC WARN 或视觉限制仍按验证报告说明，不把展示动画描述为整板验收通过。
合成前必须确认 manifest 中所有 PNG 都存在；缺帧时先处理实际失败再重新录制，
不能拿缺帧序列冒充完整流程。

## 当前展示来源

2026-09-08 在已有门禁控制板工程采集：两张静图源自电源与 RF 主控页、对讲与外设接口页
的官方导图，GIF 记录电源与 RF 主控页从首件放置到功能框完成的 12 张关键阶段导图。
对外展示名称统一为“门禁控制板示例”，不公开真实项目名。动图宽 1440 像素，
约 8.8 秒、0.68 MiB；使用差分帧压缩，末帧停留后循环。

采集前保存完整快照及原生工程备份，重建后回读两页：23 个器件、165 个物理引脚的
位号、库身份、稳定 ID、网络/NC 与几何均保持一致，主页面已显式保存。
严格门禁因既有 3 个 DRC WARN 停止，最终画面不代表电气验收通过。
本次运行 CLI 为 v1.4.3，连接器为 1.4.2，不能据此声称三方同版验证完成；
其余覆盖范围见 [1.4 发布与验证](releases/release-1.4.md)。
