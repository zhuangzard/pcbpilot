# 模块框与标题的数据转换

遵守 [数据驱动架构基准](../.agents/skills/pcbpilot/references/schematic-data.md#数据驱动架构基准)。
框/标题是源数据计算的产物，不在验收末尾靠手工补画；必检文字含位号、网络文字和自由文字，
型号/参数等非位号器件属性排除碰撞与框包络。旧框也须参与现场检查，但检查范围不等于删除授权。

1.4 将模块框和标题纳入本地布局 JSON。电气模型保持不变，绘图适配器负责把坐标、
单位与样式转换为官方 API 参数，再以回读结果验证转换是否正确。独立 Notes 功能
退出本版；模块标题保留。

## 数据契约

```json
{
  "schemaVersion": 1,
  "documentId": "<page-uuid>",
  "frames": [{
    "id": "POWER",
    "title": "POWER / AMS1117-3.3",
    "rect": {"minX": 100, "minY": 510, "maxX": 555, "maxY": 740},
    "titleX": 110,
    "titleY": 550,
    "fontSize": 20,
    "color": "#AA00AA",
    "lineType": 1,
    "titleLayout": {
      "width": 190,
      "height": 20,
      "clearance": 5,
      "obstacles": [{"minX": 130, "minY": 580, "maxX": 535, "maxY": 720}]
    }
  }]
}
```

- 默认 compose 的框内最小边距、模块/行间距、标题内缩为 **10 raw（0.1 inch / 2.54 mm）**；两层模式使用源 spacing，`compose --layout-page` 保留已选几何。标题净距为 5 raw。每个框保留自身高度，网格取整可增加少量留白。
- 坐标为 0.01 inch，y 向上；0.2 inch 标题对应 `fontSize=20`。
- `id` 是页面内稳定的模块标识。`rect` 是矩形路径范围；官方矩形起点转换为
  `(minX,maxY)`，宽高分别为 `maxX-minX` 与 `maxY-minY`。线宽固定为 1 raw，
  实际笔画在路径两侧各延伸 0.5 raw。
- 标题以左上角对齐，向右、向下展开。默认粉色 `#AA00AA`，框同色、无填充，
  `lineType=1` 是官方 `DASHED`。
- 可选 `titleLayout` 保存预测的文字宽高、与障碍物的净距及各障碍物 bbox；
  标题预测范围为 `[titleX,titleX+width] × [titleY-height,titleY]`。
  Apply/check 使用实际文字 bbox 核对预测包络、净距和碰撞，不以锚点正确代替完整验证。
- 规划器分别保留器件本体与标注、引脚、每段导线、电源标记及引线的占位，避免总 bbox
  抹掉局部空档。比较电路上方、下方的合法标题位置：先最小化框高度，再最小化面积，
  平局依次优先左对齐、顶部。能内嵌就不增加标题带；否则只扩出必要空间，最后向外取整到网格。
  字高保持 20，不靠缩小字体压缩高度。越出纸张时拒绝计划，不自动分页。
- 各模块先完成上述紧凑包络，再按功能顺序从左上角向右排，行满进入下一行。
  同行只对齐顶边，各框保留自己的内容高度；下一行按上一行最高框加固定行间距推进。
  不用全页最大高度扩框。计算出的位移同时作用于模块内所有器件、引脚、导线及标记。
  `power-layout` 默认使用左上起排；`--at` 明确指定核心坐标时保留该位置，
  `--frames-only` 使用现有位置。`sch compose` 接受任意已绘制的 Lib 模块数据并编译单页队列，见 [组合契约](schematic-page-composition.md)。

`sch compose` 的 `sheetBorder` 单独记录实际图纸内边框，与用于 Apply 核验的完整
`sheet` 纸张 bbox 区分。规划保留 10 raw 内框净距及 0.5 raw 虚线半宽后，向内
取整到 5 raw 网格，并输出 `usableBounds`。缺少内框时仅保留纸张 bbox 的兼容内缩，
明确输出 `placementBoundarySource:sheet-bbox-fallback`，不能据此宣称已验证红框净距。
单独的 `sch frame apply` 只转换输入坐标，不自行推导纸张或内框边界。

`power-layout` 的输入快照可在顶层提供
`titleMetrics: {"title":"POWER / AMS1117-3.3","fontSize":20,"width":189.022171,"height":20}`，
将同文字、同字号的官方 API 实测尺寸用于本地计算；文字或字号变化后应重新测量。
没有实测数据时使用保守字符宽度估算。`titleLayout` 是布局预测，实际渲染回读才是
转换验收证据；实测文字超出预测或碰撞时应修正数据、重算并 Apply。

## CLI 与 Apply

```bash
pcbpilot sch frame check --from plan.json --project <project>
pcbpilot sch frame apply --from plan.json --project <project>

pcbpilot sch power-layout --from geometry.json --out plan.json --playbook apply.json
pcbpilot sch apply apply.json --dry-run
pcbpilot sch apply apply.json --yes

# 电路位置和网表已正确，只补呈现层；不生成移件、删线或重连操作
pcbpilot sch power-layout --from geometry.json --out plan.json \
  --frames-only --playbook frames-apply.json
pcbpilot sch apply frames-apply.json --yes
```

`sch frame apply/check` 也接受 `--data` 内联 JSON，供 Apply 的 `run` 步骤使用。
框适配器适用于任意模块；当前 `power-layout` 的器件规划范围仍为固定 LDO 四器件。
首次转换前核对页面身份；队列在修改前后核对全部器件与 pin→net，图形回读通过后保存。
生成队列带 `requireFullExecution`，禁止 `--resume/--from/--to` 跳过校验及更换目标页面。
失败后回读、重新生成并完整执行，已有正确框会通过幂等比较保留。

每个页面、每个模块分别记录本工具的框/标题 ID。重复执行时核对完整几何和样式，
已有目标保持不变；只替换工具记录的旧图形，不根据颜色或文字模糊删除用户图形。
失败后先回读实际状态，不盲重试写操作；回读缺失或样式不符均不能报告验证通过。

## 验证边界

离线回归验证多组位置、尺寸、旋转输入下的包络和队列生成，并包含错网、缺字段、
出纸张、字号/颜色/线型不一致等负样例。紧凑标题的回归需覆盖上下空档、无空档时最小扩边、
长标题、分段折线、整体平移、同行不同高度及按当前行最高框换行；同时检查
显式内框的四边净距、向内网格取整、纸张与内框身份区分，以及缺失边框时的回退提示。
必检文字超出预测或碰撞时仍应拒绝验收；非位号器件属性的越框/重叠不属于此判据。
现场验证同一数据首次 Apply 和重复 Apply，
核对框与文字、引脚网表和严格电气门禁，最后用官方导图辅助检查阅读效果。
该流程验证软件转换机制，不代表整板制造或电源稳定性验证。

EasyEDA 3.2.186 的实测适配：矩形 `fillStyle` 即使传入 `None` 仍回读 null，
因此显式传 `fillColor="none"` 并核实无填充；文字请求 `LEFT_TOP=1` 后回读枚举 2，
但实际 bbox 左上角与目标一致。因此对齐以真实 bbox 的 `minX/maxY` 验证，
同时核对 fontSize、位置、旋转、颜色和内容。首次旧转换请求被校验拦下，修正后全队列通过。
