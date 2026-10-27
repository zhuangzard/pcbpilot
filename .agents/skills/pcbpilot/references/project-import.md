# 外部工程导入与迁移

本页覆盖工程级操作：KiCad ↔ EasyEDA 的一次性迁移（`project import`、`kicad sch-import`）、Altium Designer
工程迁移的能力边界（`unsupported`）、MCP 新建工程容器、跨工程打开与原生 `.epro2` 导出。

## KiCad 设计导入 EasyEDA（`project import`）

设计在 KiCad 后端完成后，用官方导入器一次性带入 EasyEDA，生成当前团队里的**新工程**（关联封装与 3D 模型），
从不修改已有工程：

```bash
pcbpilot project import --file kicad/Demo.kicad_pro --name Demo_kicad          # 也可给 KiCad 工程目录
pcbpilot project import --file design.zip --type KiCad --name Demo --team <teamUuid>
```

`--file` 为 KiCad 工程目录或 `.kicad_pro` 时，`.kicad_pro`、`.kicad_sch`、`.kicad_pcb`、`.kicad_dru` 与库表会被打包；
其他格式按 `--type` 交给导入器（KiCad、EasyEDA Pro、EasyEDA、JLCEDA Pro、JLCEDA、Allegro、OrCAD、EAGLE、PADS、
LTspice；默认 KiCad）。命令返回不等于迁移成功：导入后按下文 Altium 一节“目标工程已经由用户独立提供”的 1–4 步逐页回读连接、
Board 绑定、板框、层叠与 DRC，数量与关键网不一致时不报告成功。Altium 工程本身仍是 `unsupported`（见下文）。

## EasyEDA 工程转 KiCad 原理图（`kicad sch-import`）

```bash
pcbpilot kicad sch-import --epro Project.epro --board <板名> --out kicad/ --pcb kicad/<板名>.kicad_pcb
```

把 `.epro` 导出中一块板的原理图转成 `<out>/<name>.kicad_sch`（根页，每个 EasyEDA 页一张子页）、`<name>.kicad_pro`、
`easyeda.kicad_sym` 与 `sym-lib-table`。符号来自导出的 `.esym`；器件保留位号、值、LCSC 号、MPN 与厂商；有 `--pcb` 时
Footprint 字段取该板同位号的封装。电源标记转 KiCad 电源符号，网络端口转全局标签。默认用 kicad-cli 导出结果网表，
与 EasyEDA 数据（及 `--pcb` 焊盘网络）逐网逐脚比较，分区不一致即失败；`--no-check` 跳过比较时结果只能算 `source-only`。
工程含多块板时必须给 `--board`。转换后的修改走 `kicad sch-edit` / `sch-build`，不手改文本。

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

来源与完整探测矩阵见 [GitHub issue #203](https://github.com/zhoushoujianwork/easyeda-agent/issues/203)。


## MCP 新建工程（`project.create`）

从首页调用 MCP `project.create` 时，先通过 `pcbpilot_health` 选择真实窗口，把新名称放在
`payload.friendlyName`，可用 `payload.open` 请求打开。此动作只创建工程容器，必须提供
`window`，不要传 `project` 或 `doc`；拟建名称不是已有工程，首页标签不是原理图页面。
创建后检查 `created` / `opened` / `partial` 并读回工程身份，再处理文档创建。部分成功时
先用 `pcbpilot project find --window <id> --name <完整友好名称> --team <teamUuid>`
按友好名称和团队精确查找，不盲目重复创建。它只调用官方项目 UUID 枚举与逐项详情读取；
查找默认等待 90 秒，可显式加 `--timeout 120s`（5 秒到 10 分钟）。超时仍是未知，不能据此创建同名工程；
`found` 可用于核对已有工程，`unknown`（例如 UUID 清单为空、详情缺失或枚举报错）不能当作
不存在；只有 `enumeration.complete:true` 且 `presence:"absent"` 才能说明指定团队**根文件夹**内
没有匹配（SDK 不保证递归子文件夹）。本例未传 `folderUuid`，故先查目标团队根文件夹。
结果仍需核对 UUID 和团队，不因同名自动打开或重试创建。无 `--team` 的查找仅
用于发现匹配，不证明全局不存在。返回 `UNKNOWN_ACTION` 时检查连接器是否实现此动作；健康检查的
版本兼容不能证明 handler 存在。其他 MCP 写操作仍要求真实 `project` 和 `doc`；不得推广此例外。

## 工程级打开与原生导出

`project open --uuid` 是旧的文档打开别名，只能用于当前工程内页面，不是跨工程打开。
跨工程时先保存所有未保存文档，再显式调用：

```bash
pcbpilot project open --window <window-id> --project-uuid <project-uuid> --page-uuid <page-uuid> --allow-discard-unsaved
pcbpilot project export --window <window-id> --project-uuid <project-uuid> --out ./deliverable.epro2
```

打开命令使用官方 `dmt_Project.openProject`；该 API 可能丢弃未保存数据，因此标志是明确确认，
不是自动保存。命令核对打开后的真实工程身份；超时或失败先读回，不盲目重复。
导出使用官方 `sys_FileManager.getProjectFile`，要求目标工程已激活，前后检查 UUID，
拒绝覆盖文件，校验原生 ZIP 完整性并输出字节数/SHA-256。输出 `restoreVerified=false`：
ZIP 校验和导出成功不代表重新导入验证通过。请在导出前显式保存所有文档。
底层调用正式 typed action `project.open` / `project.export`，需要注册这两个 action 的新版 daemon 及包含 handler 的新版连接器。旧 daemon 或连接器拒绝 unknown action / UNKNOWN_ACTION 时停止并升级；禁止回退到 debug.exec_js。
直接调用 `project.open` 同样必须传 `allowDiscardUnsaved:true`；`project.export` 不接受页面路由。
限制：归档最大 16 MiB，解压验证上限 128 MiB，超限明确失败。

MCP 使用 `pcbpilot_project_transfer`，`operation` 为 `open` 或 `export`，均需 `window`、
`projectUuid`；打开另需 `allowDiscardUnsaved=true`，导出另需新 `out` 路径。不传文档路由。
此工具需要含上述 CLI 命令的匹配构建；不要仅替换 MCP 而仍使用旧 CLI。

工程身份可能先于文档树就绪。需直接进入原理图时，打开命令同时传 `--page-uuid`（MCP `pageUuid`），等待目标页面出现在树中后只打开一次，并核对工程和页面身份。省略此参数只保证工程身份，不保证页面已加载。
