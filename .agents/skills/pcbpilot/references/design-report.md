# 设计报告：每次运行自动更新、版本化交付客户（P11）

`pcbpilot report design` 把一次设计运行留下的产物汇成**客户可读、可追溯、带版本**的设计报告：
仿真结果与图表、逐器件可行性（应力 vs 额定与余量）、工程计算、原理图与布局图、测试点计划、
制造/装配/测试注意事项、调试上电流程。模板与每章计算来源见
[templates/design-report/README.md](../templates/design-report/README.md)。状态：`offline-verified`
（ESP32-S3 mini 现场产物生成样例；报告本身离线计算，不访问编辑器）。

## 什么时候生成

| 时机 | 命令 | 版本内容 |
|---|---|---|
| S6.5 原理图验收后 | `pcbpilot intent derive … --out intent.json --sim-out sim.json --report-dir reports/<name>` | 预布局版：需求、仿真、器件可行性、工程计算、测试计划、上电流程；布局/验证章节标“不可用” |
| P7 自动布线后 | `pcbpilot pcb auto run --board board.json --intent intent.json --sim sim.json … --out-dir out --report-dir reports/<name>` | 加入叠层、布线统计、IR 压降、SI、反馈 |
| P10 终检后（交付版） | `pcbpilot report design --out-dir reports/<name> --intent … --sim … --plan-dir out --board final.json --reload-board final.reloaded.json --drc drc.json --check check.txt --rules-check rules-check.json --image sch:P1=p1.png --image layout=snapshot.png --host "<宿主与精确版本>" --connector <版本>` | 完整报告：验证证据齐全，图片来自 typed 导出/快照 |

每次运行都会得到下一个 `vN`；**不要**覆盖旧版本（`--force` 只用于重生成同一版本的笔误修正）。
客户交付物是 `reports/<name>/vN/report.html`（单文件，可直接发送或打印），`CHANGELOG.md`
说明相对上一版改了什么。

## 输入与证据

| 标志 | 来自 | 缺失时 |
|---|---|---|
| `--intent` | `intent derive` | §2、§5 不可用；§4/§8 仍可用仿真 |
| `--sim` | `sim power` / `intent derive --sim-out` | §3 不可用；§4 用 intent 网络电流 |
| `--plan-dir` | `pcb auto run --out-dir`（plan.json、feedback.json、preview.svg） | §6 只有图片 |
| `--board` / `--reload-board` | `pcb dump --include-copper`（保存重载前/后） | 无 MPN 解码、无测试点坐标、无制板规则；无重载对比则 §7 该项 N/A |
| `--drc` `--check` `--rules-check` `--net-diff` | `pcb drc` / `pcb check`（文本或 `--json`）/ `pcb rules check` / 焊盘网络对账 JSON | §7 对应项 N/A，总体结论降为“PASS with warnings” |
| `--models` | 默认已安装 Skill 的 `power-models.json` | 只剩 MPN/描述解码的额定 |
| `--values` | `sch list` JSON 或 `{"parts":{ref:{value,mpn,description}}}` | MPN 取自板级 dump 的 device |
| `--image KIND[:LABEL]=PATH` | `sch export-image`、`pcb stage-snapshot` | §6 无图 |

所有输入以“路径 + sha256”记录在封面与附录；给了但读不出的文件直接报错（避免静默变薄）。
图片只作展示证据，数值结论全部来自对象回读与计算。

## 判读规则

- **总体结论**：FAIL > PASS with warnings > PASS，原因逐条列在封面。客户版交付前处理全部
  FAIL；warnings 逐条写明取舍（例如“USB 0.5 A 预算余量 14 %：产品说明要求 ≥ 1 A 端口”）。
- **需数据手册**：额定未知的检查不计余量、不影响 FAIL，但计入 warnings。补额定的正确做法是在
  `power-models.json` 该模型加 `ratings`（`vinMaxV`、`vccMinV/vccMaxV`、`vrrmV`、`vrwmV`、`isatA`、
  `iratedA`、`contactA`、`thetaJaCW`、`tjMaxC`、`sourceVMinV/sourceVMaxV`、`sourceBudgetA`、
  `vrefAccuracyPct`）并写 `source`；或在 values 的 description 里保留 LCSC 属性。**禁止凭印象填额定。**
- **余量准则**见模板 README §4；`marginal` 不是错误，是需要在报告里给出理由或换件的提示。
- **测试点计划**是 bring-up/生产测试的起点：公差里标“假定”的项（如 Vref 精度）应在拿到数据手册后
  补进 `ratings.vrefAccuracyPct` 再出下一版。建议新增测试点属于设计变更，按原理图/布局确认流程执行。
- §7 的 N/A 表示“没有证据”，不是“通过”。交付版必须带 `--drc`、`--check`、`--board` 与
  `--reload-board`（保存重载后 semanticSha256 一致）。

## 样例

仓库 `docs/examples/esp32-mini-design-report/`：v1 由 2026-09-27 ESP32-S3 mini 现场运行
（EasyEDA Pro 桌面版 V3 3.2.149，connector 0.4.1，4 层）的真实产物生成；v2 只把 intent 中
`+3V3` 声明为 1 A，用来演示变更记录（意图电流、所需线宽、线宽不足的 FAIL 与结论变化）。

## 负例

- 把截图当验证证据：图片只进 §6，§7 只认 DRC/check/回读文件。
- 在 `report.json` 里手工改数：报告必须可由输入重算；改输入再出新版本。
- 用 `--force` 覆盖已发给客户的版本：客户手里的 vN 与仓库不再一致，改为出 vN+1。
