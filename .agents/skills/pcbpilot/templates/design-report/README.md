# 设计报告模板（design-report）

`pcbpilot report design` 的固定模板。每次运行（`intent derive --report-dir`、
`pcb auto run --report-dir` 或手动 `report design`）都按这里的章节生成一个**新版本**
`reports/<name>/vN/`，客户看到 v1、v2… 以及每版相对上一版的变化。用法与判读见
[design-report.md](../../references/design-report.md)。

## 文件与单一来源

| 文件 | 作用 |
|---|---|
| `report.html.tmpl` | Go `html/template`：自包含 HTML（内联 CSS、内联 SVG 图表、base64 图片；浅色/深色/打印） |
| `report.md.tmpl` | Go `text/template`：同内容的 Markdown；图表引用 `assets/charts/*.svg`，图片引用 `assets/`，数据链接 `data/`（版本包内相对路径） |
| `analog.html.tmpl` / `analog.md.tmpl` | §3A 模拟电路仿真的局部模板（`{{define "analog"}}`），由两个主模板 `{{template "analog" .}}` 引入 |

**本目录是规范源。** Go 包 `pkg/designreport/templates/` 用 `go:embed` 嵌入一份副本，
`TestTemplatesMatchSkill` 要求两份逐字节相同：改模板时先改这里，再
`cp .agents/skills/pcbpilot/templates/design-report/*.tmpl pkg/designreport/templates/`，
然后 `go test ./pkg/designreport/`。模板只负责排版；**所有数值在 Go 里计算**，模板里没有
算术，只有格式化函数（`f1/f2/f3/f4/num/opt/watt/pct100/eff/margin/short/join/statusZh/md/missing`；
HTML 另有 `chart`、`img`、`verdictClass`、`statusCls`；Markdown 另有 `imgpath`）。
数据对象是 `report.json`（`designreport.Report`），字段只增不改名。

## 章节与计算来源

缺输入的章节打印“本节不可用：原因”，原因同时写入 `report.json.missing[]`。从不补造数据。

| § | 章节 | 计算来源 |
|---|---|---|
| 0 | 封面 | `--project-name/--customer/--host/--connector`、pcbpilot 版本；输入来源表（路径 + sha256 + 字节，含未提供项）；总体结论 = FAIL（error 级 intent 发现、器件应力超额定、工程计算不满足、验证 FAIL、仿真不收敛）/ PASS with warnings（warn 发现、余量低于准则、需数据手册、assumed 模型、验证 WARN 或缺证据、章节缺失）/ PASS，并列出原因 |
| 1 | 执行摘要 | 板尺寸（外框点）、各电源轨 V/Imax、typical/peak 输入功率与损耗、最小器件余量、器件检查统计、最坏 IR 压降/预算、布线完成率、原生 DRC、pcb check、intent 发现数；主要风险 = intent error/warn + 余量超限/临界（升序）+ 需数据手册汇总 + 验证 FAIL（最多 12 条）；相对上一版的变化表 |
| 2 | 需求与设计意图 | `intent.json`：blocks（功能/核心/器件/摘要/功耗）、domains、standard（区分“规格书声明 / 工程默认”）、copper 假设、findings |
| 3 | 电源仿真 | `sim.json`：各场景电源轨电流（分组柱状图）与电压；场景功率平衡（输入 = Σ connector-source suppliedW，负载 = Σ load/ic-small/led，损耗 = 其余正功耗）；器件功耗与电源轨 V×Imax（横向柱状图）；电源树（源 → OR 二极管 → 稳压器 → 负载，由模型类型与引脚电流方向恢复）；稳压器各场景 Vin/Vout/Iout/Iin/D/η/损耗 + 仿真给出的计算过程；纹波 ΔI/Ipk/Irms；模型可信度表（assumed 高亮）与假设、警告 |
| 3A | 模拟电路仿真 | `--analog analog.json`（`pcbpilot sim analog`，或 `intent derive` 自动产出）：识别的模拟块（类别/拓扑/模型可信度/网表）、目标 vs ngspice 仿真（含解析交叉核对列）、Bode/环路增益/阶跃/传输/ADC 采样/开关/复位瞬态图、Monte-Carlo 容差（当前与修改后）、建议的元件值修改（原理图修改，须用户确认）与发现；analog.json 与 ngspice 网表/输出进报告包 `data/analog/`。无 analog.json 时只打印一行“未运行”，不计入缺失章节 |
| 4 | 器件可行性 | 每个器件的“应力 vs 额定 → 余量%”：额定来自 `power-models.json`（`maxA` 及可选 `ratings`）、MPN 解码（Samsung CL / Murata GRM / Yageo CC 电容的封装+介质+容值+耐压；UNI-ROYAL / Yageo RC 电阻的封装+阻值+公差）、器件描述中的 LCSC 属性（Current Rating / Voltage Rating / Power(Watts)），电阻功率默认表 0402 1/16 W、0603 1/10 W、0805 1/8 W、1206 1/4 W（70 °C 额定）。准则：电流 ≥ 20 %、电阻 P ≤ 50 %、MLCC 额定 ≥ 1.5×（< 2× 另提示 DC 偏压）、TVS VRWM ≥ 线电压、供电范围 ≥ 3 %、Tj ≤ 80 % Tj,max。额定未知一律“需数据手册”，不参与余量。余量图升序 |
| 5 | 工程计算 | `intent.json` 的 copper/nets/netClasses/pairs：IPC-2221/2152 线宽（外层 k=0.048，内层同曲线用内层铜厚，与 intent/pcbauto 一致）与计划线宽、计划线宽的载流；单孔载流与过孔数；IPC-2221B 电压间距 vs 工艺间距；按叠层 h/εr/t 重算微带 Z0 与差分 Zdiff 并与目标比较（>5 % WARN、>10 % FAIL）；每个绝缘对的间隙/爬电/铣槽 + `pkg/safety` 的耐压试验电压；plan 与 intent 叠层参数不一致时提示 |
| 6 | 布局与布线 | 图片（`--image`，>1 MB 自动减半缩小）+ plan 目录的 `preview.svg`；`plan.json`：叠层、布线统计、引擎 DRC、联合评分；`route.power` 的每网 IR 压降/预算、负载焊盘压降占预算（图）、最坏路径分解、压降最大的 20 段；SI 长度/过孔/skew；隔离报告；`feedback.json` 难度与反馈项 |
| 6A | 设计后仿真验证 | `--post post.json`（`sim post-layout`）：边界（板级自然对流，非 CFD）、设置、每网真实铜皮压降/预算/最坏焊盘/铜损/最大电流密度/最大过孔电流/铜自热、与 pcb auto IR 估算对比、过孔电流 vs IPC 载流量、板最高温度与能量平衡、各层温度、器件板温/θ/Tj/状态、热图与电流密度图（`heat` 图片，post.json `mapsDir` 自动收集）、铜皮修改建议（widen-segment / corner-crowding / via-bottleneck）、Elmer 交叉校验状态、模型与假设；结论作为 §7 一行进入总体结论 |
| 7 | 验证状态 | 设计后仿真（`--post`，PASS/WARN/FAIL = post.json verdict）、原生 DRC（`--drc`）、pcb check（`--check`，文本或 JSON）、规则同步（`--rules-check`）、焊盘网络对账（`--net-diff`）、保存/重载 semanticSha256（`--board` + `--reload-board`）、布线完成度与平面连接、IR 预算；每项 PASS/WARN/FAIL/N/A + 证据文件与 sha256 |
| 8 | 测试点计划 | 由电源树、仿真与板级焊盘生成：限流上电（1.5× typical 源电流，功能测试前 1.5× 峰值）；每条电源轨的期望值 ± 公差（稳压输出：Vref 精度（`ratings.vrefAccuracyPct`，缺省假定 2 % 并标注）+ 2×分压电阻公差×(1−Vref/Vout)；源：`ratings.sourceVMin/MaxV`；OR 后：仿真包络）、测量位置（负载焊盘 = IR 最坏焊盘，最近测试点/无源件焊盘，稳压器输出焊盘）；开关节点波形；输出纹波 ΔV ≈ ΔI/(8·fsw·ΣC)（理想电容估算）；复位/启动脚空闲与按下电平、上拉/对地电容与 τ=RC；LED 电流与阳极电压；接口枚举；受控阻抗 TDR；绝缘对耐压；晶振。没有 TP 的电源轨给出建议新增测试点（板坐标 mm，相对外框左下角） |
| 9 | 制造与装配 | 层数/叠层/板厚（叠层字符串）/铜厚、板级规则的线宽/间距/过孔/板边/孔距与实际最小线宽、阻抗控制目标；表面处理建议（最细焊盘中心距 ≤ 0.65 mm 或有 EP → ENIG）；pcb check 按类型分组；装配关注（极性、1 脚、EP、细间距、机械件、MSL）；高压/铣槽/涂覆；ESD 与回流通用注意 |
| 10 | 调试上电 | 步骤：目检（极性件列表）→ 断电对地电阻（已知电阻路径 = 直接对地或经高阻节点的两电阻串联，> 100 Ω 期望、< 10 Ω 判短路）→ 限流上电 → 各轨测量 → 开关波形 → 功能接口 → 峰值带载；故障特征只列本板存在的电路（降压、LDO、OR 二极管、USB、LED） |
| 11 | 附录 | 全部网络表（intent 或仿真）、器件表（模型、可信度、MPN 解码结果、功能块）、输入溯源、未生成内容、术语表 |

## 图表

纯内联 SVG（Go 生成，无脚本/CDN）：每个图有标题、坐标轴与单位、`<title>` 悬停提示，
并在同节提供数据表。颜色为 CSS 变量：浅色默认值 + `prefers-color-scheme: dark` + 打印覆盖；
系列色按固定顺序分配，状态色（满足/临界/超限）同时带文字标签，不只靠颜色。

## 版本与变更

`reports/<name>/index.json` 保存每一版的输入摘要、指标（电源轨电流/电压、输入功率、IR 压降、
器件余量、器件检查统计、意图电流与所需线宽、布线完成率、各验证状态、总体结论）和发现集合；
新版本与最高的旧版本比较，写入 `vN/report.json.changes`、摘要页和 `CHANGELOG.md`（新版在前）。
`--version auto` 取下一个整数；已存在的版本不覆盖（`--force` 除外）。相同输入除
`generatedAt` 外输出逐字节一致（`--date` / `SOURCE_DATE_EPOCH` 可固定时间）。
