# KiCad 文件安全：谁写哪些文件、怎么回退

> 用途：在 KiCad 工程上运行任何 pcbpilot 命令之前，确认它会写哪些文件、文件是否被 KiCad 占用、
> 出错时如何回退。KiCad 命令总览见 [kicad.md](kicad.md)、[kicad-sch-build.md](kicad-sch-build.md)、[kicad-jlc.md](kicad-jlc.md)。

## 目录

1. [四条规则](#1-四条规则)
2. [命令写入表](#2-命令写入表)
3. [写前：关闭文件与锁文件](#3-写前关闭文件与锁文件)
4. [pcbpilot 自己做的文本修改](#4-pcbpilot-自己做的文本修改)
5. [检查点与回退](#5-检查点与回退)
6. [制造输出目录与清单](#6-制造输出目录与清单)

## 1. 四条规则

1. **不手工文本编辑 `.kicad_sch` / `.kicad_pcb` / `.kicad_pro` / `.kicad_sym` / `.kicad_mod`。** 读用
   `kicad sch-read`、`kicad netlist`、`kicad snapshot`；改原理图用 `kicad sch-build`（新建）/ `kicad sch-edit`（增量）；
   板子由 `kicad place` / `kicad route` 写到新文件。没有对应 typed 命令的修改交给用户在 KiCad 里做，并把能力记为
   `UNSUPPORTED`，不用 sed、正则或手写 S-expression 代替。
2. **写之前文件在 KiCad 中已关闭。** KiCad 不会察觉外部修改；打开状态下外部写入会在用户下次保存时被覆盖，或两份互相覆盖。
3. **输入不动，输出进新目录。** 能写到 `--out` / `--out-dir` 的命令就写到工程之外的新目录；原地写入的命令先有回退手段（第 5 节）。
4. **原理图写入看工程来源。** `kicad sch-edit`（含 `replace_part`、加件、改网）只用于 `kicad sch-build` 生成的工程——判据是
   `<工程>/.pcbpilot/sch-build/spec.json` 存在。用户手画的、`kicad sch-import` 或其他工具导入的工程没有这份 spec：换件、加件、
   改网都由用户在 KiCad 原理图编辑器里做，Agent 给出逐项修改清单，能力记 `UNSUPPORTED`，改完重新 `kicad sch-read` 回读。
   这类工程上 pcbpilot 只做三种原地修改：`kicad sch-fit`、`kicad sch-check --fix-pwr-flag`、`kicad lcsc --set`（只写 LCSC 字段）；
   都无检查点，先 git commit 或整目录备份。

## 2. 命令写入表

| 命令 | 读 | 写 | 原地修改输入？ |
|---|---|---|---|
| `kicad sch-read`、`kicad netlist`、`kicad snapshot` | 工程文件 | 仅 `--out` JSON（缺省 stdout） | 否 |
| `kicad sch-check` | 原理图层次 | 仅 stdout 报告 | 否；**加 `--fix-pwr-flag` 时原地改图纸**（逐页网表校验，校验不过的页不写） |
| `kicad sch-fit` | 根图及子图 | 每张图纸的页面尺寸与内容平移 | **是**（无检查点；先 `--dry-run`） |
| `kicad sch-build --out D` | spec、库 | `D/` 下工程、库表、`connectivity.json`、`intent.json`、`erc.json`、`D/.pcbpilot/` | 只写 `D`；门禁失败时 `D` 不变；`--dry-run` 不写 |
| `kicad sch-edit --project D` | `D/.pcbpilot/sch-build/spec.json` | 重建 `D` 中变化的区 | **是**，事务式（网表 ≠ 编辑后 spec 则不写）+ 检查点 |
| `kicad sch-checkpoint restore N` | `D/.pcbpilot/checkpoints/N` | 图纸、库、spec、状态；删除之后的构建新增的文件 | **是** |
| `kicad sch-import --out D` | `.epro`、可选 `--pcb` | `D/<name>.kicad_sch` + 子图、`easyeda.kicad_sym`、`sym-lib-table`、`.kicad_pro`（已有则保留） | 只写 `D` |
| `kicad lcsc --set` | 板、原理图 | 板由 pcbnew 重新保存；原理图（含子图）最小文本修改 | **是**（无检查点） |
| `kicad lcsc --import C… --lib-dir L` | 网络（JLC 的 EasyEDA 库记录） | `L/<lib-name>.kicad_sym`、`L/<lib-name>.pretty/` | 只写 `L`；**不改库表**，需要时把两个库加进工程库表（见下） |
| `kicad lcsc --check` / `--search` | 板、原理图 / 网络 | 无 | 否 |
| `kicad place --pcb B --out Y` | 板 | `Y`（+ `.kicad_pro`）、`Y.seed-N.kicad_pcb`、`Y.report.json`；输出板的走线/过孔被清除、铺铜未灌 | 否 |
| `kicad route --pcb B --out-dir R` | 板、同名 `.kicad_pro` / `.kicad_dru` | `R/work/` 逐步工作板、`R/routed.kicad_pcb`、`board-final.json`、`summary.json`、`report/`、`manual/`、`review-*/` | 否（复制进 `R/work/`） |
| `kicad fab --out O` | 板、原理图 | `O/gerber/`、`O/<板>-gerber.zip`、BOM、CPL、`fab-report.json` | 否；缺 C 号时什么都不写 |
| `kicad-cli … -o X`（直接调用） | 工程文件 | `X` | 否 |

库表：工程级 `sym-lib-table` / `fp-lib-table` 在工程目录，条目用 `${KIPRJMOD}` 相对路径保持可移植；全局库表属于用户的
KiCad 配置，不由 Agent 修改。添加库表条目属于修改工程文件——在 KiCad 的库管理器里由用户做，或交给 `kicad sch-build`
（它为生成的工程写库表）。

## 3. 写前：关闭文件与锁文件

- KiCad 打开一个文件时会在同目录建锁文件，名字是 `~` + 文件名 + `.lck`（例如 `~board.kicad_pcb.lck`）。写入前查一下：
  ```bash
  ls -a <工程目录> | grep '\.lck$'
  ```
  有锁文件 → 请用户在 KiCad 里关闭该工程（或至少该文件），确认后再写；结论记 `BLOCKED`，不要自己删锁文件——
  KiCad 崩溃留下的陈旧锁文件也由用户确认后处理。pcbpilot 目前**不**自动检查锁文件，这一步由 Agent 做。
- 用户在 KiCad 里改过之后，旧的 `sch-read` / `snapshot` 结果作废，重新读取再规划。
- `kicad-cli` 与 pcbnew 加载慢或卡住时（全局库表很大），sch-build 的 ERC 使用隔离的空 KiCad 配置；用户自设的
  `KICAD_CONFIG_HOME` 优先，`PCBPILOT_KICAD_ISOLATE=0` 关闭隔离。隔离配置下的 `lib_*` / `footprint_link_issues`
  是环境类结果（[drc-triage.md](drc-triage.md)）。
- KiCad 安装/升级途中 `kicad-cli` 会被系统杀掉（退出码 137），等安装完成再跑。

## 4. pcbpilot 自己做的文本修改

规则 1 约束的是 Agent；以下几处由 pcbpilot 在受控方式下改文件，用之前知道它们的边界：

| 命令 | 怎么改 | 保护 |
|---|---|---|
| `kicad lcsc --set` | 原理图：只替换已有 LCSC 类字段的值，或在最后一个 property 后插入隐藏的 `LCSC Part #`，其余字节不变；临时文件 + 原子替换。板子：pcbnew 加载后由 KiCad 自己保存 | 找不到的位号非零退出；**没有检查点**：先提交到 git 或复制工程 |
| `kicad sch-check --fix-pwr-flag` | 给 ERC 报“电源输入未驱动”的网接一个 PWR_FLAG | 每页写入前后网表必须一致，否则该页不写；之后重跑 ERC |
| `kicad sch-fit` | 改页面尺寸、把内容平移进图框 | 放不进 A0 的页报 “too big” 不改；无检查点，先 `--dry-run` |
| `kicad route` | 在**工作副本**的 `.kicad_dru` 里写 `# pcbpilot:begin … # pcbpilot:end` 规则块；无 `.kicad_pro` 时建最小工程 | 输入板永不改写 |

pcbnew 的 `SaveBoard` 不覆盖已存在的 `.kicad_pro`（10.0.6 实测），所以 bridge 写新板前先删除目标的 `.kicad_pro/.kicad_prl`——
只发生在 pcbpilot 自己的输出路径上。

## 5. 检查点与回退

- `kicad sch-build` / `kicad sch-edit` 每次成功都把图纸、库、spec、构建状态复制到 `<project>/.pcbpilot/checkpoints/N`：
  ```bash
  pcbpilot kicad sch-checkpoint list --project build/
  pcbpilot kicad sch-checkpoint restore 3 --project build/
  ```
  `restore` 会删除之后构建新增的文件；回退后用 `kicad sch-read` 确认内容，再继续 `sch-edit`。
- 没有检查点的原地写入（`kicad lcsc --set`、`kicad sch-fit`、`--fix-pwr-flag`）：写前 `git commit`（工程在 git 里时），
  否则把整个工程目录复制一份并在报告里写出备份位置。
- 板子：`kicad place` / `kicad route` 不改输入，回退就是丢弃输出目录；把选中的结果复制回工程是用户的决定，复制前关闭 KiCad。

## 6. 制造输出目录与清单

- **v0.10.2 之前**：每次 `kicad fab` 用一个新的空 `--out` 目录。旧目录里残留的 Gerber 可能和新文件混在一起被误传。
- **v0.10.2 起**：`kicad fab` 在 `--out` 里维护 `.pcbpilot-fab-manifest.json`，记录上一次成功运行写入的文件。后续运行：
  - 只替换或删除清单里列出的文件；目录里的其它文件属于用户，保持不动。
  - 本次要写的某个文件名已存在但**不在清单里**（不是 pcbpilot 写的）→ 拒绝运行，什么都不改；请用户换目录或自己移走该文件。
  - `gerber/` 里不在清单中的 Gerber/钻孔样文件保留原样，且**不进** zip。
  - `--out` 或 `--out/gerber` 是符号链接或目录联接时拒绝；同一目录另有 `kicad fab` 在运行时拒绝。
  - 发布是事务式的：中途失败会恢复上一套完整文件。
- 无论哪个版本，验收都看本次的 `fab-report.json`（层清单、zip 内文件、BOM 行数、CPL 件数、告警、用过的覆盖），
  并确认文件时间是本次运行；然后在 Gerber 查看器里人工核对（[prefab-checklist.md](prefab-checklist.md) 第 9 节）。
