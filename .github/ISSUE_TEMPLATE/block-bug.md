---
name: 块缺陷上报 (block bug)
about: 某个电路块用出了问题 —— 引脚名不匹配 / 拓扑错 / 器件停产 / 约束错
title: "[block-bug] block.<id>: <一句话现象>"
labels: block-bug
---

<!--
  这份 issue 通常由 AI agent 在 block-apply / 验证过程中起草、经用户确认后提交。
  带证据的上报才能被处理 —— 空口说"不好用"没法修。
-->

## 哪个块、哪一版

- 块:`block.<id>`
- 版本:<!-- 块 JSON 的 updated 字段,或 pcbpilot version 输出;block-apply manifest 里的 revision 更好 -->

## 现象(选一类,删掉其余)

- [ ] **引脚名不匹配** —— 块里写的功能名与真实符号不符(`sch read` 实测)
- [ ] **拓扑错误** —— 连出来 netlist / DRC 不对
- [ ] **选型问题** —— 器件停产 / C 号失效 / 该料不适用此场景
- [ ] **约束错误** —— pcb_layout / placement / signals / silk 某条规则错或缺
- [ ] 其他

## 证据(必填)

<!-- 至少一样:
  - sch read 的引脚名摘录(块里写的 vs 实测的)
  - block-apply 的 manifest / 失败输出
  - sch check / DRC 的相关条目
  - 停产/换料:LCSC 页面链接 + 替代料 C 号
-->

```
(粘证据)
```

## 建议修复(可选)

<!-- 知道怎么改就写;愿意自己提 PR 就说一声,按 contributing 规则你会进 contributors 署名 -->
