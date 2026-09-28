# PCB 设计规范手册（指针）

正本在 Skill 里（Skill 优先铁律 — 发布包必须自带规范）：

➡️ [`.agents/skills/pcbpilot/references/pcb-design-rules.md`](../.agents/skills/pcbpilot/references/pcb-design-rules.md)

内容：线宽/间距、过孔、布局、走线、铺铜、电源与地、差分对、Mark 点、
工艺边/拼板、丝印、叠层、DRC 三级清单（FATAL/ERROR/WARN）。
基于 JLC 工艺能力 + IPC-2221。

**消费方**：

- `pcb check` 新增 5 条规则的报错信息直接引用该手册章节号（`docs/pcb-design-rules.md §N`）：
  `silk-over-pad` §11.2 / `decap-too-far` §3.1 / `via-in-pad` §2.3 /
  `fiducial-missing` §9（`internal/app/pcb_check_dfm2.go`）；板边安全距离 §5.4 由
  `copper-to-edge` / `copper-to-hole` / `plane-pullback`（ERROR，`internal/app/pcb_check_edge.go` →
  `pkg/pcbauto/edge.go`）执行，取代旧的 `copper-near-edge`（只看走线/过孔、按板框 bbox、10mil，
  从未对 14mil 灌铜报过错）。
- 数值型工艺极限的机器可读版：`.agents/skills/pcbpilot/references/fab-rules-jlcpcb.json`
  （daemon 的 DRC fallback 基线，`internal/app/pcb_rules.go`）。
- net-class 线宽阶梯的代码正本：`internal/app/pcb_netclass.go`。

## 板边安全距离（摘要，正本 §5.4）

| 对象 | 默认 | 工艺下限 | 来源 |
|---|---|---|---|
| 外层铜（走线/焊盘/过孔/铺铜）→ 铣边 | 20 mil（0.5 mm） | 0.2 mm | JLC 能力（`fab-rules-jlcpcb.json` `copperToEdgeMil.routed`）+ 工厂 DFM 0.25–0.5 mm |
| 内层平面 / 内层铺铜 | 30 mil（0.76 mm，plane pull-back） | 0.2 mm | 业界平面回缩 0.5–1.0 mm |
| V-cut：外层 / 内层 | 0.5 mm / 0.8 mm | 0.4 mm | JLC V-cut 铜距 ≥0.4 mm（`copperToEdgeMil.vcut`） |
| 危险/市电/病人域 → 板边、金属安装孔 | max(间隙, 爬电)，默认加强绝缘 | — | `pkg/safety.Distances`（板边与安装件按可触及/接地面） |

配置：`spec.json` `edge` → `intent.json` `edge`（`pkg/intent/edge.go`；类型即
`pcbauto.IntentEdge`）。执行：`pcb auto`（按层禁布带、铺铜/平面内缩、负片 `no-inner-electrical`
板边带）、`pcb rules apply --intent`（Safe Spacing Board Outline × 铜对象格，默认规则 + 每个
`PP_*`）、`pcb pour-fit` / `power-pour` / `power-planes`（板框中心线多边形内缩）、`pcb check`
（量实际灌铜）、设计报告 §5/§7/§9。所有值都是工程默认，**不是**认证结论；危险域距离注明
“confirm with the certification lab”。
