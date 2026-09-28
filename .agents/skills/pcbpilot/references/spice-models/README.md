# SPICE 模型库（`pcbpilot sim analog`）

| 文件 | 内容 |
|---|---|
| `analog-models.json` | 运放/比较器/放大器/基准/ADC 输入/复位时序/MOSFET/BJT 的参数与数据手册来源（匹配：LCSC → MPN → nameRegex） |
| `pcbpilot-generic.lib` | 行为级子电路与通用器件模型；`pkg/analogsim/models/` 内嵌同一份（`TestGenericLibMatchesSkill`） |
| `vendor/` | 用户放入的厂商 SPICE 模型（`.lib` / `.cir` / `.sub`） |

用法与判定见 [analog-sim.md](../analog-sim.md)。

## 通用运放宏模型 `PCBPILOT_OPAMP`

端口 `inp inn vp vn out oa oi`，参数 `gbw aol sr vos p2 hp hn zout`（取自 `analog-models.json` 的 `gbwHz`、`aolDB`、
`slewVPerUs`、`vosV`、`p2Hz`/`p2Factor`、`headroomHighV/LowV`、`zoutOhm`）。第一级：限幅跨导（压摆率 = imax/C1）
驱动 R1∥C1（直流增益 aol、单位增益频率 gbw），并被箝位在输出摆幅内（削顶与恢复）；第二级：第二极点 p2（决定单位
增益相位裕度）。`oa` 是理想输出（零阻抗），`oi` 在 `zout` 之后；正常运行用 0 V 源连接，环路增益分析在此插入 AC 源，
T = −V(oa)/V(oi) 精确成立。

## 新增器件

在 `analog-models.json` 对应数组追加一条，必须写 `source`（数据手册编号与所用数值）和 `confidence`
（`datasheet` 为逐项核对过的最大/典型值，`approx` 为典型值转录，`assumed` 为占位）。引脚名不是
IN+/IN−/OUT/V+/V− 这类可解析名时，给 `pinout`（`{"1":"out","2":"vn","3":"inp","4":"inn","5":"vp"}`；
双/四运放可写 `"1":"out:A"`）。

## 厂商模型（vendor/）

1. 把厂商文件放进 `vendor/`，例如 `vendor/OPA2333.LIB`。
2. 在 `analog-models.json` 的 `vendorModels` 增加映射，`pinOrder` 按厂商 `.subckt` 行的端口顺序写**角色**：

```json
"vendorModels": [
  {"match": {"mpn": ["OPA2333AIDR"], "nameRegex": "(?i)^OPA2333"}, "kind": "opamp",
   "file": "vendor/OPA2333.LIB", "subckt": "OPA2333",
   "pinOrder": ["inp", "inn", "vp", "vn", "out"],
   "source": "TI OPA2333 PSpice model (SBOM…), downloaded 2026-…"}
]
```

   角色取值 `inp`（同相输入）、`inn`（反相输入）、`vp`（正电源）、`vn`（负电源/地）、`out`（输出）；厂商模型多出的
   端口写 `nc`（接一个悬空节点，由 1 GΩ 泄放电阻固定直流点）。例：TI 常见顺序 `.SUBCKT OPA2333 IN+ IN- V+ V- OUT`
   → `["inp","inn","vp","vn","out"]`；ADI 常见 `.SUBCKT AD8605 1 2 99 50 45`（1=+in, 2=−in, 99=V+, 50=V−, 45=out）
   → 同样写成 `["inp","inn","vp","vn","out"]`。**以厂商文件 `.subckt` 行和其注释为准。**
3. 厂商模型没有理想输出节点，环路增益注入不精确：相位裕度改由小信号阶跃过冲按二阶等效估算，并在块 notes 注明。
4. 模型须能在 ngspice 下运行（PSpice 语法需要 `ngspice` 的兼容模式时，在文件顶部加 `.options` 或转写后再放入）。
