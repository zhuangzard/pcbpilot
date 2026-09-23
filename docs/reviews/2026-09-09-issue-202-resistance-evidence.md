# Issue #202：电阻参数误读与选型误匹配

报告：[通过 skill 读取元器件 datasheet 理解错误](https://github.com/zhuangzard/pcbpilot/issues/202)。
`0805W8F330LT5E` / C52548 的正确阻值为 **330mΩ = 0.33Ω，±1%**；报告中的 33Ω
大了 100 倍。报告附带的模型回答承认从 MPN 的 `330` 猜三位电阻码。仓库没有实现这种
厂家料号解码，不能声称已证明该模型回答是由某段脚本产生。

## 来源核验（2026-09-09）

1. 使用现有 `parts-select.py` 的 JLCPCB SMT 目录查询函数，以 `C52548` 为关键词读取
   实时返回，器件身份与具名参数如下（省略价格和库存等时效数据）：

   ```json
   {
     "componentCode": "C52548",
     "componentModelEn": "0805W8F330LT5E",
     "componentBrandEn": "UNI-ROYAL(Uniroyal Elec)",
     "componentTypeEn": "Chip Resistor - Surface Mount",
     "componentSpecificationEn": "0805",
     "attributes": [
       {"attribute_name_en": "Resistance", "attribute_value_name": "330mΩ"},
       {"attribute_name_en": "Tolerance", "attribute_value_name": "±1%"}
     ]
   }
   ```

2. 目录给出的 [LCSC 手册页面](https://www.lcsc.com/datasheet/lcsc_datasheet_2206010000_UNI-ROYAL-Uniroyal-Elec-0805W8F330LT5E_C52548.pdf)
   实际返回 HTML，其中的 iframe 指向
   [厂家 PDF](https://datasheet.lcsc.com/datasheet/pdf/0a975aaa49b7c97f38a963127be4a823.pdf?productCode=C52548)。
   已读取文本并渲染核对第 2 页（印刷 Page 2/9）：
   《Thick Film Chip Resistors – Data Sheet》，Feb.12,2019 V.3，§2.3 指定 F=±1%；
   §2.4.2 指定 ≤2% 系列第 8–10 位为有效数字，第 11 位为倍率；§2.4.3 指定 L=10⁻³。
   因而本料号中的 `330L` 为 `330 × 10⁻³ Ω = 0.33Ω`，与目录相符。
   该规则属于此厂商的这份系列手册，不推广为通用 MPN 规则。
   核对文件 SHA-256：`11cd644d5d8a34a6d12775afb80bf58d8fc11f0c3b700dbd0f7a59942ceaa5ef`。

## 独立复现的脚本缺陷

`parts-select.py` 先将文本整体转成小写、删去 Ω，再做子串命中，并在另一匹配路径中
删去小数点。由此丢失前缀大小写、数量级和数值边界。修复前对仓库实际标准库运行：

- `local_select("33Ω")` 首项为 `res.330_0402` / C25104（330Ω）；还返回数字碰巧命中的 IC。
- `local_select("1mΩ")` 返回 `res.1m_0402` / C26083（1MΩ）。
- `local_select("0.33Ω")` 返回 `res.523k_0402` / C170333；命中了无关身份数字。
- C52548 描述 `330mΩ` 对 `33Ω` 与 `330MΩ` 的旧 relevance 都为 1，真正等价的
  `0.33Ω` 却为 0。在线路径在全部 relevance=0 时仍保留候选并推荐。

这些是独立确认的选型缺陷；报告没有附执行日志，因此不将其等同于原模型误读的已证实调用链。
标准库中没有 C52548 条目，本轮不写入未经库身份解析的新器件，也没有所谓已修好的既有错值条目。

## 修复与验收范围

- Skill 先锁定厂商/MPN/C号/封装，保留来源原文与换算；处理具名参数冲突，禁止从料号
  数字猜值；即使命中块库也不能省略需求与参数核对。将 C52548 作为有来源的回归事实。
- 选型 helper 对显式 Ω/ohm 查询先做电阻类别与数值匹配，再按文字、库存和价格排名；
  无精确候选不能降级成错误阻值推荐。原始参数及换算结果供核查，未核实其他规格仍需读手册。
- 本轮不修改 EDA 连接器或图面，不需要热加载插件；不宣称已经重放报告者的 Claude
  会话或完成整板端到端验收。脚本修复只能机械约束经过该 helper 的查询，不能保证模型
  在绕过来源核验时不会再次猜错。

## 已完成验证

- 新增 20 项电阻选择回归，覆盖原始 C52548 响应、毫欧/兆欧、小数点、无匹配、参数
  冲突、MPN-only、电感 DCR、普通非电阻查询，以及四种 CLI 空结果形态。
  最初 11 项用例在修复前出现 12 个断言失败和 1 个缺字段错误；独立审查再补上中文
  邻接单位、逗号/分数/幂表达式尾段误读和英文 ohm 不支持前缀的负例，保留 UNIOHM
  品牌搜索。最终 20 项全部通过。
- `make release-script-test`：60 项通过；新增测试由现有 CI 入口自动发现。
- `make skill-check`、`git diff --check` 通过。
- 直接运行修复后的 `parts-select.py C52548 --online --json`，重新查询实时目录，断言
  MPN 为 `0805W8F330LT5E`，结果为：

  ```json
  {"resistance":{"raw":"330mΩ","ohms":"0.33","source":"attributes.Resistance"}}
  ```

本轮只改 Skill、选型 helper、回归和记录。后续按用户要求纳入 v1.4.4，随发布关闭
#202，不等待报告者复验；新增规则仍不代表原模型会话已经重放通过。
