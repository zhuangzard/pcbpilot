# 配方：机械要求 → `mech.json`

目的：把外壳/结构图上的约束（板框、安装孔、接口位置、禁布区、分区）写成引擎能执行的数据。
`mech.json` 里写错的字段会被**拒绝**（未知字段报错），而不是悄悄忽略。

## 1. 从机械图读出这些量（全部带单位与来源）

| 量 | 从哪里读 | 注意 |
|---|---|---|
| 板框尺寸 / 圆角 / 异形轮廓 | 外壳图或结构工程师的 DXF | 异形用 `outline` 多边形（顺时针或逆时针均可） |
| 安装孔位置 / 规格 | 外壳螺柱位置 | 用**孔中心**坐标；禁铜圆直径取螺帽/垫圈外径 + 装配公差 |
| 接口位置 | 外壳开孔中心 | 以沿板边的中心位置 `at` 表达；外伸量取面板厚度需要 |
| 天线净空、散热器、螺柱下方 | 模块数据手册 “PCB layout” 章节、散热器图 | 天线净空尺寸以模块手册为准，不用经验值 |
| 高压区 / 低压区 | 电气安全要求 | 通常不必手写，引擎按电压域自动分区（见 [hv-isolation.md](hv-isolation.md)） |

## 2. 写 `mech.json`（默认 mm，原点 = 板框左下角，y 向上）

```json
{
  "units": "mm",
  "board": {"width": 50, "height": 40, "cornerRadius": 2, "thickness": 1.6},
  "cornerHoles": {"size": "M3", "inset": 3.5},
  "holes": [{"name": "MH5", "x": 25, "y": 5, "dia": 3.2, "keepout": 6.5}],
  "fixed": [{"ref": "SW1", "x": 44, "y": 20, "rot": 90}],
  "edge": [
    {"ref": "USB1", "edge": "left",   "at": 20, "overhang": 0.8},
    {"ref": "J2",   "edge": "bottom", "at": -1}
  ],
  "keepouts": [
    {"name": "antenna", "rect": [35, 30, 50, 40], "noCopper": true, "noParts": true}
  ],
  "zones": [{"domain": "MAINS", "rect": [0, 0, 20, 40]}]
}
```

| 字段 | 单位 | 生效状态 | 语义 |
|---|---|---|---|
| `board.width/height` | mm | 生效 | 生成矩形板框，`cornerRadius` 为圆角 |
| `board.outline` | mm | 生效 | 多边形板框，优先于宽高 |
| `board.autoSize` + `margin` | mm | 生效 | 不给尺寸，按布局结果收紧板框再加边距 |
| `cornerHoles.size` | M2/M2.5/M3/M4 | 生效 | 钻孔 2.2/2.7/3.2/4.3 mm，禁铜圆 4.5/5.5/6.5/8.5 mm；**要求 `units:"mm"`** |
| `cornerHoles.inset` | mm | 生效 | 孔中心到两条板边的距离；省略 = 禁铜半径 + 0.5 |
| `holes[]` | mm | 生效 | 任意位置的孔；`keepout` 为禁铜圆**直径** |
| `fixed[]` | mm / 度 | 生效 | 把器件**本体中心**放到 (x, y)，旋转 `rot`；该器件不再被布局器移动。`side:"bottom"` 当前不支持翻面，只报告 |
| `edge[]` | mm | 生效 | 器件贴 `left/right/top/bottom` 边；`at` 为沿边位置（从该边低端量起，<0 = 居中）；`overhang` 为伸出板边长度 |
| `keepouts[]` | mm | 生效 | `rect: [x0,y0,x1,y1]` 或 `poly`；`noCopper`（同时禁过孔）/ `noParts`；`layers` 为层号数组，省略 = 全部铜层 |
| `zones[].domain` | mm | 生效 | 把某个电压域限定在矩形内（覆盖自动分区） |
| `zones[].block` | mm | **planned** | 按功能块分区：已解析，布局器尚未使用 |
| `heightZones[]` | mm | **planned** | 限高区：快照没有器件高度，当前不产生约束 |

## 3. 板边接口的朝向怎么定

引擎把接口开口方向判定为“焊盘质心 → 本体中心”（贴片 USB/Type-C 焊盘在后、插口在前），
在 0/90/180/270 中选开口最朝外的角度；对称的排针/端子则让长边沿着板边。**执行后必须看
`preview.svg` 或回读后的 3D 确认开口朝外**；焊盘在两侧的特殊封装可能判断错，此时改用
`fixed` 明确写 `rot`。

## 4. 执行与核对

```bash
pcbpilot pcb auto run --board board.json --mech mech.json --place --no-route --out-dir out/
```

核对 `out/report.md` 第 4 节：`出板`、`进入禁布区`、`越出电压域分区` 均为 0；`out/preview.svg`
里接口贴边且开口朝外、孔在角上、禁布区（红色虚线）内没有器件。然后再去掉 `--no-route` 布线。

## 常见错误

- 坐标写成板绝对 mil 却没写 `"units":"mil"` → 位置放大 39.37 倍。
- 孔的禁铜写成半径 → 实际只有一半大（字段是**直径**）。
- `fixed` 的 x/y 当成锚点 → 这里是**本体中心**；只有 `pcb.component.modify` 用锚点。
- 一个接口同时写在 `fixed` 和 `edge` → 以 `edge` 为准，`fixed` 的旋转被覆盖。
