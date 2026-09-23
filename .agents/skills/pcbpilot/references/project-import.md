# 外部工程导入与迁移

本页描述 Altium Designer 工程迁移的当前能力边界。它不是 `pcbpilot` 的导入命令。

## Altium Designer 工程：当前 `unsupported`

当前没有经过验证的 typed action 或 CLI 能把 `.SchDoc` / `.PcbDoc` 程序化导入
EasyEDA Pro。Agent 不得用交互界面、CUA、`debug.exec_js`、分块 base64 上传或私有消息总线
兜底。需要导入时停止现场写入，将能力标为 `unsupported`，先实现并验证 typed import。

如果目标工程已经由用户独立提供，pcbpilot 只从现有工程开始读取与核验：

1. `pcbpilot health --project <project>` 与 `pcbpilot doc ls --project <project>`：确认连接器、
   目标工程，以及导入所得的原理图/PCB 文档。
2. 对每张原理图读取 `pcbpilot sch connectivity --page <name-or-uuid> --project <project>`，
   核对器件身份、全部物理引脚、网络、NC、分层页面与位号；再逐页运行适用的
   `layout-lint`、`sch check`、`bridge-check` 和 SDK DRC，分别保存结果。
3. 对 PCB 核对 Board 绑定、器件与焊盘网络、板框、层叠、机械层/禁布区、关键网和
   丝印；运行 `pcbpilot pcb layout-lint`、`pcbpilot pcb check --strict` 与
   `pcbpilot pcb drc --strict`。AD 机械层到 EasyEDA
   层的映射必须在原生画布和数据回读中确认，不能只看工程已出现在目录里。
4. 记录导入前后的数量与关键不变量。缺页、空读、板框/机械层变化、网络不一致或检查
   未运行时，均不能报告迁移成功；修复完成后显式保存。

## 为什么不能调用名义上的导入 API

官方 beta 方法 `eda.sys_FileManager.importProjectByProjectFile` 的签名列出了
`'Altium Designer'` / `'Protel'` 类型，但在 issue #203 的 EasyEDA Pro 桌面端
3.2.149（Windows x64，本地工作区）实测中，对 `.epro2`、`.eprj2` 和真实 `.SchDoc`
都会快速 resolve 为 `undefined`，不抛错，也没有创建文档或改变工程。

因此：

- 方法存在、Promise resolve 或返回 `undefined` 均不算成功；不得继续后续自动化。
- `getAllProjectsUuid()` 在本地工作区可能返回 `[]`，不能单独作为导入副作用判据。
- `eda.sys_FormatConversion.convertAltiumDesignerLibrariesToEasyEDA*` 只转换
  `.SchLib` / `.PcbLib` 库文件，不转换 `.SchDoc` / `.PcbDoc` 工程文档。
- 当前不封装 `import.ad` typed action。将来只有在受支持宿主上证明真实副作用，并能在
  返回值缺失、超时和部分导入时明确失败，才可进入 CLI/action 实现。

未来接口封装至少要在写入前记录目标工程指纹，并在返回后核对工程/文档身份、文档清单、
原理图连接数据及 PCB 板框/机械层；不得把 `undefined`、无变化或单一 UUID 清单当成功。

来源与完整探测矩阵见 [GitHub issue #203](https://github.com/zhuangzard/pcbpilot/issues/203)。
