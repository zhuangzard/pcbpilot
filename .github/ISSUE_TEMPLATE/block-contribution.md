---
name: 块贡献投稿 (block contribution)
about: 上报一个自己验证过的好设计块 —— 不会提 PR 也能投,维护者代落库,署名归你
title: "[block-contribution] block.<拟定id>: <一句话说明>"
labels: block-contribution
---

<!--
  两条路都欢迎:
  ① 会提 PR:直接按 .agents/skills/pcbpilot/references/standard-blocks-contributing.md 走,不用开这个 issue;
  ② 不方便提 PR:填这份 issue,维护者(或 AI)代为落库 —— author 署名仍然是你,永不删除。
  AI agent 也可以在用户做出一块好电路后,起草这份 issue、经用户确认后投稿。
-->

## 块是什么

<!-- 一句话:什么外设电路,解决什么问题。例:SP3485 半双工 RS-485 现场接口,带失效安全偏置和可跳线终端 -->

## 拓扑来源(硬要求 —— 不凭记忆手写)

- [ ] 官方参考设计:`official-ref:<vendor + 文档名/章节>`
- [ ] 器件手册应用电路:`datasheet:<mpn> + 图号/章节`
- [ ] 验证过的开源板:`oshwhub:<url>`

## 验证状态(如实填 —— draft 也收)

- [ ] **已验证**:在真实工程跑过 `place → wire → sch check → DRC=0`,netlist 逐网核实
  <!-- 粘验证记录:工程名 + 日期 + 结果摘要 -->
- [ ] **draft**:拓扑来自上述一手源,但引脚名尚未 `sch read` 核实 / 未整板验证

## 块 JSON(草稿即可)

<!-- 按 internal/blocks/data/_schema.json 的形状;字段不全没关系,核心是 parts / internal_nets / ports。
     用到的新器件请给真实 LCSC C 号(维护者负责补 deviceUuid)。 -->

```json
{
  "id": "block.<拟定id>",
  "desc": "...",
  "parts": {},
  "internal_nets": [],
  "ports": {}
}
```

## 署名

- GitHub @handle:<!-- 落库时写进 author,永不删除 -->
