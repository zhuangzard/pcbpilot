# 命令索引（生成文件，勿手改）

> 用途：按命令路径查一行说明和讲它的参考文档；参数以 `pcbpilot <路径> --help` 为准。
> 由 `python scripts/gen-command-index.py` 从 CLI 的 `--help` 树生成（仓库根目录运行），
> `--check` 在索引过期时非零退出（`scripts/tests/test_command_index.py`）。“文档”列是对
> Skill Markdown 的纯文本匹配，只说明哪里提到该命令，不说明讲得是否完整；`—` 表示还没有参考提到它。

## 目录

- [actions](#actions)
- [api](#api)
- [apply](#apply)
- [ask](#ask)
- [audit](#audit)
- [blocks](#blocks)
- [board](#board)
- [bom](#bom)
- [call](#call)
- [console](#console)
- [daemon](#daemon)
- [debug](#debug)
- [doc](#doc)
- [health](#health)
- [intent](#intent)
- [kb](#kb)
- [kicad](#kicad)
- [lib](#lib)
- [mcp](#mcp)
- [notify](#notify)
- [pcb](#pcb)
- [project](#project)
- [project-config](#project-config)
- [report](#report)
- [review-panel](#review-panel)
- [sch](#sch)
- [signoff](#signoff)
- [sim](#sim)
- [skill](#skill)
- [spec](#spec)
- [update](#update)
- [version](#version)
- [view](#view)
- [web](#web)
- [workflow](#workflow)

## actions

| 命令 | 说明 | 文档 |
|---|---|---|
| `actions` | Print the typed action catalog as JSON | [actions.md](actions.md), [pcb.md](pcb.md), [schematic.md](schematic.md), [SKILL.md](../SKILL.md) |

## api

| 命令 | 说明 | 文档 |
|---|---|---|
| `api` | Discover the official eda.* API surface (search / list, offline) | [actions.md](actions.md), [offline-format-api-watch.md](offline-format-api-watch.md), [schematic.md](schematic.md), [SKILL.md](../SKILL.md) |
| `api ls` | List eda.* namespaces (optionally filtered by substring) | — |
| `api probe` | Report typeof for eda.* members on the connected host (read-only, nothing is called) | [actions.md](actions.md), [offline-format-api-watch.md](offline-format-api-watch.md), [schematic-wiring.md](schematic-wiring.md) |
| `api search` | Rank eda.* methods matching all query terms (name/namespace/summary) | [actions.md](actions.md), [schematic.md](schematic.md), [SKILL.md](../SKILL.md) |
| `api show` | List all methods of one eda.* namespace | — |
| `api upstream-diff` | Compare the pinned eda.* API surface with a newer pro-api-types / official doc-api source | [actions.md](actions.md), [offline-format-api-watch.md](offline-format-api-watch.md), [SKILL.md](../SKILL.md) |

## apply

| 命令 | 说明 | 文档 |
|---|---|---|
| `apply` | Replay a declarative step file (playbook) against EasyEDA | [analog-sim.md](analog-sim.md), [design-flow.md](design-flow.md), [environment-setup.md](environment-setup.md), [examples/index.md](examples/index.md) +9 |

## ask

| 命令 | 说明 | 文档 |
|---|---|---|
| `ask` | Ask the user a decision question (console card, terminal fallback) and print the choice | [design-flow.md](design-flow.md), [SKILL.md](../SKILL.md) |

## audit

| 命令 | 说明 | 文档 |
|---|---|---|
| `audit` | Inspect the action audit log | — |
| `audit cost` | 一场设计跑了多久、多少次调用、多少是白花的(可 --record 落台账) | — |
| `audit export` | Export a recorded session from the audit log as a replayable playbook | [actions.md](actions.md) |
| `audit tail` | Show the most recent dispatches from the JSONL audit log | — |

## blocks

| 命令 | 说明 | 文档 |
|---|---|---|
| `blocks` | Query the embedded circuit-block library (电路块库) — offline, no daemon | [environment-setup.md](environment-setup.md), [kicad-sch-build.md](kicad-sch-build.md), [part-selection.md](part-selection.md), [pcb.md](pcb.md) +3 |
| `blocks ls` | List blocks (optionally filter by --category / --ready / --draft) | [environment-setup.md](environment-setup.md), [standard-blocks-contributing.md](standard-blocks-contributing.md) |
| `blocks search` | Find blocks by id/desc/category/port/part (case-insensitive) | [kicad-sch-build.md](kicad-sch-build.md), [part-selection.md](part-selection.md), [reusable-module-library.md](reusable-module-library.md), [standard-blocks-contributing.md](standard-blocks-contributing.md) |
| `blocks show` | Print a block's full JSON (parts, internal_nets, ports, notes) | [pcb.md](pcb.md), [schematic.md](schematic.md), [standard-blocks-contributing.md](standard-blocks-contributing.md) |

## board

| 命令 | 说明 | 文档 |
|---|---|---|
| `board` | Manage Boards (板子/组合) — the schematic↔PCB binding | [actions.md](actions.md), [pcb.md](pcb.md), [recipes/schematic-to-pcb.md](recipes/schematic-to-pcb.md), [review-workflow.md](review-workflow.md) |
| `board copy` | Copy a Board (its schematic + PCB) into a new Board | [pcb-auto.md](pcb-auto.md) |
| `board create` | Bind a schematic and/or PCB into a new Board (组合) | [pcb.md](pcb.md) |
| `board current` | Read the current Board (its bound schematic + PCB) | — |
| `board delete` | Delete a Board by name (no undo) | [pcb.md](pcb.md) |
| `board list` | List all Boards in the current project (name + schematic + pcb) | [pcb.md](pcb.md), [recipes/schematic-to-pcb.md](recipes/schematic-to-pcb.md), [review-workflow.md](review-workflow.md) |
| `board rebind` | Rebind a Board to the intended schematic + PCB (repair a stale binding) | [pcb.md](pcb.md) |
| `board rename` | Rename a Board by its current name | — |

## bom

| 命令 | 说明 | 文档 |
|---|---|---|
| `bom` | BOM export and enrichment | [review-workflow.md](review-workflow.md) |
| `bom enrich` | Enrich a BOM TSV with LCSC C-numbers via bom-enrich.py | — |
| `bom export` | Export schematic BOM as csv or xlsx artifact | [actions.md](actions.md), [prefab-checklist.md](prefab-checklist.md), [review-workflow.md](review-workflow.md), [schematic.md](schematic.md) |

## call

| 命令 | 说明 | 文档 |
|---|---|---|
| `call` | Generic escape hatch: call any typed action directly | — |

## console

| 命令 | 说明 | 文档 |
|---|---|---|
| `console` | Local web cockpit served by the daemon (/ui): monitor, projects, timeline, sims, reports, library, decisions | — |
| `console open` | Print the console URL and open it in the default browser | — |
| `console projects` | Manage the work dirs the console tracks (~/.pcbpilot/console/workdirs.json) | — |
| `console projects add` | Register a work dir (default: current directory) | — |
| `console projects list` | List registered work dirs | — |
| `console projects remove` | Forget a work dir (files are not touched) | — |
| `console url` | Print the console URL (with the token in the #fragment) | — |

## daemon

| 命令 | 说明 | 文档 |
|---|---|---|
| `daemon` | Manage the pcbpilot background daemon | [environment-setup.md](environment-setup.md), [pcb-auto.md](pcb-auto.md), [pcb.md](pcb.md) |
| `daemon health` | Check daemon health across the port range | [pcb.md](pcb.md) |
| `daemon restart` | Stop the local daemon, then start this CLI version in the foreground | [actions.md](actions.md), [environment-setup.md](environment-setup.md), [pcb-auto.md](pcb-auto.md) |
| `daemon service` | Install / check / remove the daemon login service (required by every install) | — |
| `daemon service install` | Write the login service for this binary and start it | — |
| `daemon service status` | Report whether the login service is installed and points at an existing binary (exit 1 if not) | — |
| `daemon service uninstall` | Stop and remove the login service | — |
| `daemon start` | Start the daemon (blocks until SIGINT/SIGTERM) | [environment-setup.md](environment-setup.md) |
| `daemon stop` | Stop the identified local daemon on --ports' first port | [environment-setup.md](environment-setup.md) |

## debug

| 命令 | 说明 | 文档 |
|---|---|---|
| `debug` | Debug escape hatches (confirmation-gated) | — |
| `debug exec` | Run raw eda.* JavaScript in the connector (escape hatch) | [actions.md](actions.md), [environment-setup.md](environment-setup.md), [pcb.md](pcb.md) |

## doc

| 命令 | 说明 | 文档 |
|---|---|---|
| `doc` | Discover and open/switch EasyEDA documents (schematic pages / PCBs) | [auto-layout-sop.md](auto-layout-sop.md), [collaboration-workflow.md](collaboration-workflow.md), [environment-setup.md](environment-setup.md), [examples/260919-at32f415/critical-routing.md](examples/260919-at32f415/critical-routing.md) +10 |
| `doc ls` | List all openable documents in the window (★ = active) | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [collaboration-workflow.md](collaboration-workflow.md), [environment-setup.md](environment-setup.md) +4 |
| `doc open` | Open a document (schematic page or PCB) by name or uuid | [actions.md](actions.md), [pcb.md](pcb.md) |
| `doc reload` | Save + close + reopen a document (default: the active one) — refreshes per-doc engine state | [actions.md](actions.md), [beginner-troubleshooting.md](beginner-troubleshooting.md), [collaboration-workflow.md](collaboration-workflow.md), [design-flow.md](design-flow.md) +20 |
| `doc switch` | Switch the foreground document by page name, PCB name, or uuid | [collaboration-workflow.md](collaboration-workflow.md), [environment-setup.md](environment-setup.md), [pcb.md](pcb.md), [schematic.md](schematic.md) |

## health

| 命令 | 说明 | 文档 |
|---|---|---|
| `health` | Check daemon health across the port range (alias of `daemon health`) | [collaboration-workflow.md](collaboration-workflow.md), [environment-setup.md](environment-setup.md), [offline-format-api-watch.md](offline-format-api-watch.md), [project-import.md](project-import.md) +4 |

## intent

| 命令 | 说明 | 文档 |
|---|---|---|
| `intent` | Electrical design intent derived from the finished schematic (intent.json) | [design-flow.md](design-flow.md), [design-intent.md](design-intent.md), [design-report.md](design-report.md), [pcb-auto.md](pcb-auto.md) +2 |
| `intent derive` | Derive intent.json (+ intent.md) from schematic connectivity, part values, power sim and spec | [actions.md](actions.md), [analog-sim.md](analog-sim.md), [design-flow.md](design-flow.md), [design-intent.md](design-intent.md) +13 |

## kb

| 命令 | 说明 | 文档 |
|---|---|---|
| `kb` | Per-project resource library: add papers/datasheets/standards/requirements/mech files, search (BM25), cite | [design-flow.md](design-flow.md) |
| `kb add` | Copy files into resources/<kind>/ and index them (dedupe by sha256) | — |
| `kb list` | List documents (id, kind, status, pages, summary state) | — |
| `kb reindex` | Re-extract and re-index every document (after upgrading pcbpilot or installing pdftotext) | — |
| `kb search` | BM25 search over page-bounded chunks; --docs screens documents first | [design-flow.md](design-flow.md), [SKILL.md](../SKILL.md) |
| `kb set-summary` | Fill a document's summary slot (after deep-reading it) | [design-flow.md](design-flow.md) |
| `kb show` | Show a document's metadata and summary; --pages / --chunk prints only that text (deep-read) | [design-flow.md](design-flow.md), [SKILL.md](../SKILL.md) |
| `kb summarize-status` | Which documents still need a summary (the agent's reading queue) | [design-flow.md](design-flow.md) |
| `kb tag` | Add or remove tags | — |

## kicad

| 命令 | 说明 | 文档 |
|---|---|---|
| `kicad` | Design in KiCad: placement, schematic sheets / import / netlist, snapshot, route + gates (fastroute), JLC fab output, LCSC parts | [design-flow.md](design-flow.md), [drc-triage.md](drc-triage.md), [kicad-file-safety.md](kicad-file-safety.md), [kicad-jlc.md](kicad-jlc.md) +3 |
| `kicad fab` | Export JLCPCB manufacturing files (Gerber+drill zip, BOM, CPL) with kicad-cli | [evidence-and-verdicts.md](evidence-and-verdicts.md), [kicad-file-safety.md](kicad-file-safety.md), [kicad-jlc.md](kicad-jlc.md), [prefab-checklist.md](prefab-checklist.md) +2 |
| `kicad lcsc` | Check, write or search LCSC (JLC) part numbers on a KiCad design | [kicad-file-safety.md](kicad-file-safety.md), [kicad-jlc.md](kicad-jlc.md), [kicad-sch-build.md](kicad-sch-build.md), [prefab-checklist.md](prefab-checklist.md) +2 |
| `kicad netlist` | Schematic connectivity JSON (the IR intent derive and pad-net-diff read) from a .kicad_sch | [evidence-and-verdicts.md](evidence-and-verdicts.md), [kicad-file-safety.md](kicad-file-safety.md), [kicad.md](kicad.md), [prefab-checklist.md](prefab-checklist.md) +2 |
| `kicad place` | Place a KiCad board's footprints with the pcbauto placer (best of several seeds) | [drc-triage.md](drc-triage.md), [kicad-file-safety.md](kicad-file-safety.md), [review-workflow.md](review-workflow.md) |
| `kicad route` | Route and gate a .kicad_pcb like pcb auto route: review → intent netclasses → fastroute → via arrays / widen / pours / silk → every gate → IR closure → report → release review | [board-manual.md](board-manual.md), [drc-triage.md](drc-triage.md), [evidence-and-verdicts.md](evidence-and-verdicts.md), [kicad-file-safety.md](kicad-file-safety.md) +6 |
| `kicad sch-build` | Build a complete, gate-checked KiCad schematic project from one design spec (one call, seconds) | [design-flow.md](design-flow.md), [evidence-and-verdicts.md](evidence-and-verdicts.md), [kicad-file-safety.md](kicad-file-safety.md), [kicad-sch-build.md](kicad-sch-build.md) +2 |
| `kicad sch-check` | Schematic hard gate on KiCad: kicad-cli ERC + pcbpilot quality checks (JSON, non-zero on fail) | [drc-triage.md](drc-triage.md), [evidence-and-verdicts.md](evidence-and-verdicts.md), [kicad-file-safety.md](kicad-file-safety.md), [kicad-sch-build.md](kicad-sch-build.md) +2 |
| `kicad sch-checkpoint` | List or restore the checkpoints kicad sch-build / sch-edit saved | [kicad-file-safety.md](kicad-file-safety.md), [kicad-sch-build.md](kicad-sch-build.md) |
| `kicad sch-edit` | Change a sch-build project in one call (add/remove/replace parts or blocks, connect by pin name, rename nets, move zones) | [design-flow.md](design-flow.md), [drc-triage.md](drc-triage.md), [kicad-file-safety.md](kicad-file-safety.md), [kicad-sch-build.md](kicad-sch-build.md) +2 |
| `kicad sch-fit` | Size every schematic sheet to its content (smallest A4…A0 that holds it clear of the title block) | [drc-triage.md](drc-triage.md), [kicad-file-safety.md](kicad-file-safety.md), [review-workflow.md](review-workflow.md) |
| `kicad sch-import` | Convert an EasyEDA Pro schematic (.epro export) into a hierarchical KiCad schematic and check its netlist | [kicad-file-safety.md](kicad-file-safety.md) |
| `kicad sch-read` | Compact parts/nets JSON of a KiCad schematic (a valid sch-build spec; read this, not the S-expression) | [kicad-file-safety.md](kicad-file-safety.md), [kicad-sch-build.md](kicad-sch-build.md), [review-workflow.md](review-workflow.md), [SKILL.md](../SKILL.md) |
| `kicad snapshot` | Board snapshot JSON (mil, y-up; the pcb dump --include-copper shape) of a .kicad_pcb | [board-manual.md](board-manual.md), [evidence-and-verdicts.md](evidence-and-verdicts.md), [kicad-file-safety.md](kicad-file-safety.md), [kicad.md](kicad.md) +3 |

## lib

| 命令 | 说明 | 文档 |
|---|---|---|
| `lib` | EasyEDA device library operations | [kicad-jlc.md](kicad-jlc.md), [library-authoring.md](library-authoring.md), [part-selection.md](part-selection.md), [schematic.md](schematic.md) |
| `lib by-lcsc` | Resolve LCSC C-numbers directly to device-library identity (libraryUuid + uuid) | [actions.md](actions.md), [kicad-jlc.md](kicad-jlc.md), [library-authoring.md](library-authoring.md), [part-selection.md](part-selection.md) +2 |
| `lib device` | Create and inspect Device records that bind symbols and footprints | [actions.md](actions.md), [library-authoring.md](library-authoring.md), [schematic-data.md](schematic-data.md) |
| `lib device build` | Create Symbol + Footprint + optional 3D model and bind one Device | [actions.md](actions.md), [library-authoring.md](library-authoring.md) |
| `lib device create` | Create a Device bound to an existing symbol and optional footprint | — |
| `lib device delete` | Delete one exact Device and verify absence | — |
| `lib device get` | Read a Device and its symbol/footprint association | [schematic-data.md](schematic-data.md) |
| `lib device model3d` | Bind, replace, or clear the 3D model on an existing Device | [actions.md](actions.md) |
| `lib device validate` | Validate a datasheet-backed Device build spec without opening EasyEDA | [actions.md](actions.md), [library-authoring.md](library-authoring.md) |
| `lib footprint` | Create and inspect footprint library assets | [actions.md](actions.md) |
| `lib footprint build` | Author pad/line geometry in an existing footprint from a JSON spec | — |
| `lib footprint copy` | Losslessly copy an existing footprint into a writable library | [actions.md](actions.md) |
| `lib footprint create` | Create an empty footprint asset and verify it by readback | — |
| `lib footprint delete` | Delete one exact footprint and verify absence | — |
| `lib footprint get` | Read a footprint asset | — |
| `lib footprint region` | Add a saved rule/keep-out region inside a writable non-system footprint | — |
| `lib libraries` | List EasyEDA libraries and writable personal/project library UUIDs | [actions.md](actions.md), [examples/260919-at32f415/layout-candidates.md](examples/260919-at32f415/layout-candidates.md) |
| `lib model3d` | Import and inspect 3D model library assets | [actions.md](actions.md) |
| `lib model3d copy` | Copy an existing 3D model into a writable library | — |
| `lib model3d create` | Import a STEP/3D model file and verify it by readback | — |
| `lib model3d delete` | Delete one exact 3D model and verify absence | — |
| `lib model3d get` | Read a 3D model asset | — |
| `lib model3d search` | Search existing 3D models | [actions.md](actions.md) |
| `lib search` | Search the EasyEDA device library by MPN, value+package, or name | [design-pre-analysis.md](design-pre-analysis.md), [library-authoring.md](library-authoring.md) |
| `lib symbol` | Create and inspect symbol library assets | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md) |
| `lib symbol build` | Author symbol outline and pins from a JSON spec | [actions.md](actions.md) |
| `lib symbol create` | Create an empty symbol asset and verify it by readback | — |
| `lib symbol delete` | Delete one exact symbol and verify absence | — |
| `lib symbol export-source` | Export a symbol's original .elibz2 file through the official read-only API | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md) |
| `lib symbol get` | Read a symbol asset | — |

## mcp

| 命令 | 说明 | 文档 |
|---|---|---|
| `mcp` | Install / register / check the pcbpilot MCP server (Node.js stdio adapter) | [environment-setup.md](environment-setup.md) |
| `mcp install` | Install mcp.tar.gz of a release (default: this CLI's version) and register it | — |
| `mcp register` | (Re)register ~/.pcbpilot/mcp/current with every present AI client | — |
| `mcp status` | Show the installed MCP version, Node.js and each client's registration | [environment-setup.md](environment-setup.md) |

## notify

| 命令 | 说明 | 文档 |
|---|---|---|
| `notify` | Show a toast inside the EasyEDA window (design-flow step notification) | — |

## pcb

| 命令 | 说明 | 文档 |
|---|---|---|
| `pcb` | PCB operations | [actions.md](actions.md), [collaboration-workflow.md](collaboration-workflow.md), [design-report.md](design-report.md), [drc-triage.md](drc-triage.md) +21 |
| `pcb add-component` | Place + connect ONE footprint on the PCB (the working alternative to import-changes) | [pcb-layout.md](pcb-layout.md) |
| `pcb aesthetics` | Measure placement + routing aesthetics offline (report-only, weight 0) | [pcb-auto.md](pcb-auto.md), [pcb-routing.md](pcb-routing.md), [schematic-wiring.md](schematic-wiring.md) |
| `pcb align` | Align components by edge/center (left\|right\|top\|bottom\|centerX\|centerY) | — |
| `pcb antenna-keepout` | Auto-generate the all-layer no-copper keep-out over each RF/antenna part's antenna end | [prefab-checklist.md](prefab-checklist.md) |
| `pcb arrange` | Coarse auto-layout SEED: cluster by shared nets, or pack a grid | — |
| `pcb auto` | Electrical-aware auto design: analyse, decide layers, place, route, check (offline engine → apply playbook) | [actions.md](actions.md), [board-manual.md](board-manual.md), [collaboration-workflow.md](collaboration-workflow.md), [design-flow.md](design-flow.md) +20 |
| `pcb auto analyze` | Understand the circuit and decide widths, clearances, domains and layer count (no placement/routing) | [pcb-auto.md](pcb-auto.md), [recipes/index.md](recipes/index.md), [recipes/power-spec.md](recipes/power-spec.md) |
| `pcb auto bench` | Benchmark engine placement against the human placement on real boards (layout-score, wirelength, routability) | [pcb-auto.md](pcb-auto.md) |
| `pcb auto feedback` | Recompute schematic feedback (pin swaps, decap ownership, IR drop, package) from an existing plan.json (offline) | — |
| `pcb auto route` | Finish a placed board with an external router: apply placement → fastroute → repair → pours → DRC → pad-net diff → post-layout sim | [board-manual.md](board-manual.md), [design-flow.md](design-flow.md), [evidence-and-verdicts.md](evidence-and-verdicts.md), [kicad.md](kicad.md) +4 |
| `pcb auto run` | Full pipeline: analyse → stackup → (place) → route → DRC/SI → plan.json + playbook.json + preview.svg + report.md | [actions.md](actions.md), [design-flow.md](design-flow.md), [design-intent.md](design-intent.md), [design-report.md](design-report.md) +12 |
| `pcb auto-place` | Module-aware auto placement: pull each satellite (cap/R/LED) to the chip pin it connects to | [pcb-layout.md](pcb-layout.md) |
| `pcb autoroute` | Auto-route the active PCB via an external router (DSN→fix→route→SES→import→repair→DRC) | [drc-triage.md](drc-triage.md), [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md) |
| `pcb beautify` | Round sharp track corners into arcs (走线美化) on the active PCB | [design-flow.md](design-flow.md), [pcb-design-rules.md](pcb-design-rules.md), [pcb-layout.md](pcb-layout.md) |
| `pcb board-info` | Read the current Board (schematic↔PCB linkage) + PCB info | [examples/260919-at32f415/fixed-mechanics.md](examples/260919-at32f415/fixed-mechanics.md) |
| `pcb check` | DFM audit: acute angles / dangling copper / bad vias / neck-down / 3W coupling (read-only) | [actions.md](actions.md), [beginner-troubleshooting.md](beginner-troubleshooting.md), [design-decisions.md](design-decisions.md), [design-intent.md](design-intent.md) +23 |
| `pcb clear` | One-shot PCB reset: components + routing + copper + regions + silk (keeps locked primitives & the board outline) | [pcb-auto.md](pcb-auto.md), [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md), [recipes/pcb-auto-run.md](recipes/pcb-auto-run.md) |
| `pcb clear-routing` | Native clearRouting (@alpha — may be unavailable; prefer rip-up) | — |
| `pcb config` | Read and configure real PCB design rules (配置设置) | [actions.md](actions.md), [pcb-config.md](pcb-config.md), [pcb.md](pcb.md) |
| `pcb config bind` | Assign a track rule to an existing net class and every member | [pcb-config.md](pcb-config.md) |
| `pcb config clearance` | Set only Track-to-Track clearance across existing layer tables | [pcb-config.md](pcb-config.md) |
| `pcb config get` | Export current rules, net classes and assignments | [actions.md](actions.md), [pcb-config.md](pcb-config.md), [pcb.md](pcb.md) |
| `pcb config net-color` | Set one network's RGB color, preserving transparency | [pcb-config.md](pcb-config.md) |
| `pcb config track` | Set track width limits, or copy an existing rule into a new named rule | [pcb-config.md](pcb-config.md) |
| `pcb config via` | Set via outer/hole diameter limits without changing existing vias | [pcb-config.md](pcb-config.md) |
| `pcb delete` | Delete PCB component primitives by id | [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md) |
| `pcb diff-pair` | Differential pair constraints (create/list/rename/delete) | [actions.md](actions.md), [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md), [prefab-checklist.md](prefab-checklist.md) +1 |
| `pcb diff-pair create` | Create a differential pair binding two nets | [actions.md](actions.md), [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md), [recipes/high-speed.md](recipes/high-speed.md) |
| `pcb diff-pair delete` | Delete a differential pair by name | [pcb-routing.md](pcb-routing.md) |
| `pcb diff-pair list` | List differential pairs on the active PCB | [actions.md](actions.md), [pcb-routing.md](pcb-routing.md), [prefab-checklist.md](prefab-checklist.md) |
| `pcb diff-pair rename` | Rename a differential pair | [pcb-routing.md](pcb-routing.md) |
| `pcb distribute` | Evenly space component centers along an axis (x\|y) | — |
| `pcb docs` | List PCB documents in the current project (uuid + name) | — |
| `pcb drc` | Run PCB DRC and return normalized violations | [design-report.md](design-report.md), [drc-triage.md](drc-triage.md), [evidence-and-verdicts.md](evidence-and-verdicts.md), [examples/260919-at32f415/critical-routing.md](examples/260919-at32f415/critical-routing.md) +12 |
| `pcb drc-rules` | Read the active PCB's DRC rule configuration without running a check | [pcb.md](pcb.md) |
| `pcb drc-rules-set` | Set full PCB rules from JSON, or raise only the pour clearance | [actions.md](actions.md), [design-flow.md](design-flow.md), [pcb-config.md](pcb-config.md), [pcb.md](pcb.md) |
| `pcb dsn-fix` | Repair an EasyEDA Specctra DSN export for an external router (offline) | [pcb-routing.md](pcb-routing.md) |
| `pcb dump` | Dump the board's read-only geometry to a self-contained JSON snapshot | [actions.md](actions.md), [design-flow.md](design-flow.md), [design-intent.md](design-intent.md), [design-report.md](design-report.md) +19 |
| `pcb eq-group` | Equal-length net group constraints (create/list/add/delete) | [actions.md](actions.md), [pcb-routing.md](pcb-routing.md), [prefab-checklist.md](prefab-checklist.md), [recipes/high-speed.md](recipes/high-speed.md) |
| `pcb eq-group add` | Add nets to an existing equal-length group | [pcb-routing.md](pcb-routing.md) |
| `pcb eq-group create` | Create an equal-length group from 2+ nets | [actions.md](actions.md), [pcb-routing.md](pcb-routing.md), [recipes/high-speed.md](recipes/high-speed.md) |
| `pcb eq-group delete` | Delete an equal-length group by name | [pcb-routing.md](pcb-routing.md) |
| `pcb eq-group list` | List equal-length groups on the active PCB | [prefab-checklist.md](prefab-checklist.md) |
| `pcb escape-plan` | Jointly reserve measured pad escape paths offline (no editor writes) | [pcb-routing.md](pcb-routing.md) |
| `pcb export-dsn` | Export the active PCB as a Specctra DSN (autorouter input) | [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md) |
| `pcb feedback` | Recompute schematic feedback (pin swaps, decap ownership, IR drop, package) from an existing plan.json (offline) | [pcb-auto.md](pcb-auto.md) |
| `pcb fill` | Net-bound filled region (填充区域 / 异形大块铜): create / list / delete | [beginner-troubleshooting.md](beginner-troubleshooting.md), [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md) |
| `pcb fill create` | Create a net-bound filled region (area via --points \| --rect \| --at/--size \| --ref) | [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md) |
| `pcb fill delete` | Delete net-bound filled regions by primitiveId | [pcb-routing.md](pcb-routing.md) |
| `pcb fill list` | List net-bound filled regions, optionally by layer/net | [pcb-routing.md](pcb-routing.md) |
| `pcb floorplan` | Plan ordered functional bands from the S0 signal flow (read-only) | [pcb-layout.md](pcb-layout.md) |
| `pcb gate` | Hard quality gate for a finished board: every schematic/simulation constraint of the intent, from code | [board-manual.md](board-manual.md), [evidence-and-verdicts.md](evidence-and-verdicts.md), [kicad.md](kicad.md), [pcb-auto.md](pcb-auto.md) +4 |
| `pcb grid-snap` | Snap component anchors to a grid (PCB data units, mil-scale) | [pcb-layout-conventions.md](pcb-layout-conventions.md) |
| `pcb import-autoroute` | Import a routed result (Specctra .ses / autoroute .json) into the active PCB | — |
| `pcb import-changes` | Sync the schematic netlist/components into the active PCB | [actions.md](actions.md), [design-flow.md](design-flow.md), [drc-triage.md](drc-triage.md), [pcb-auto.md](pcb-auto.md) +2 |
| `pcb layer-set` | Switch the active/edit PCB layer (id\|name\|top\|bottom\|inner1) | [actions.md](actions.md), [pcb-routing.md](pcb-routing.md) |
| `pcb layer-visibility` | Show/hide/focus PCB layers (preset or explicit show/hide) | [actions.md](actions.md), [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md) |
| `pcb layers` | List layers of the active PCB (+ current layer, copper count) | [actions.md](actions.md), [examples/260919-at32f415/fixed-mechanics.md](examples/260919-at32f415/fixed-mechanics.md), [examples/260919-at32f415/index.md](examples/260919-at32f415/index.md), [pcb-config.md](pcb-config.md) +3 |
| `pcb layout-lint` | Score PCB placement quality + predict routability (ratsnest crossings) | [drc-triage.md](drc-triage.md), [evidence-and-verdicts.md](evidence-and-verdicts.md), [examples/260919-at32f415/fixed-mechanics.md](examples/260919-at32f415/fixed-mechanics.md), [examples/260919-at32f415/ldo-placement.md](examples/260919-at32f415/ldo-placement.md) +5 |
| `pcb layout-plan` | Generate measured module placement candidates offline (no editor writes) | [actions.md](actions.md), [examples/260919-at32f415/index.md](examples/260919-at32f415/index.md), [examples/260919-at32f415/layout-candidates.md](examples/260919-at32f415/layout-candidates.md), [pcb-layout.md](pcb-layout.md) +3 |
| `pcb layout-score` | Score placement quality across 9 dimensions, with per-component attribution | [pcb-design-rules.md](pcb-design-rules.md), [pcb-layout-conventions.md](pcb-layout-conventions.md), [pcb-layout.md](pcb-layout.md) |
| `pcb list` | List placed components/footprints on the active PCB | [beginner-troubleshooting.md](beginner-troubleshooting.md), [examples/260919-at32f415/critical-routing.md](examples/260919-at32f415/critical-routing.md), [examples/260919-at32f415/fixed-mechanics.md](examples/260919-at32f415/fixed-mechanics.md), [examples/260919-at32f415/index.md](examples/260919-at32f415/index.md) +5 |
| `pcb lock` | Lock/unlock PCB components (batch, readback-verified) | [examples/260919-at32f415/fixed-mechanics.md](examples/260919-at32f415/fixed-mechanics.md) |
| `pcb modify` | Lay out a PCB component: move/rotate/flip layer/lock/designator | [drc-triage.md](drc-triage.md), [examples/260919-at32f415/critical-routing.md](examples/260919-at32f415/critical-routing.md), [examples/260919-at32f415/fixed-mechanics.md](examples/260919-at32f415/fixed-mechanics.md), [examples/260919-at32f415/ldo-placement.md](examples/260919-at32f415/ldo-placement.md) +4 |
| `pcb module-check` | Verify a routed module against fresh before/after copper snapshots and apply journal | [actions.md](actions.md), [evidence-and-verdicts.md](evidence-and-verdicts.md), [examples/260919-at32f415/crystal-guard-plan.md](examples/260919-at32f415/crystal-guard-plan.md), [pcb-routing.md](pcb-routing.md) +1 |
| `pcb module-owned` | Recover exact old module object ownership offline (no editor writes) | [module-owned-objects.md](module-owned-objects.md) |
| `pcb mount-holes` | Place M3 mounting holes at the board-outline corners (collision-checked cutouts) | [pcb-design-rules.md](pcb-design-rules.md), [pcb-routing.md](pcb-routing.md) |
| `pcb move` | Translate components by a relative (dx, dy) offset | [drc-triage.md](drc-triage.md) |
| `pcb net-class` | Inspect and create persisted EasyEDA PCB net classes | [design-flow.md](design-flow.md), [pcb-config.md](pcb-config.md), [pcb.md](pcb.md) |
| `pcb net-class create` | Create a real net class from explicit existing PCB nets | [design-flow.md](design-flow.md), [pcb-config.md](pcb-config.md) |
| `pcb net-class list` | List real net classes and net-rule assignments from the active PCB | [pcb.md](pcb.md) |
| `pcb net-classes` | Print the net-class → spec track-width ladder (signal/power-branch/power-trunk/high-current/gnd) | [design-decisions.md](design-decisions.md), [design-flow.md](design-flow.md), [pcb-design-rules.md](pcb-design-rules.md), [pcb-layout.md](pcb-layout.md) +2 |
| `pcb net-path` | Prove a pad-to-pad path through listed tracks/arcs/vias (read-only; pours excluded) | [actions.md](actions.md), [examples/260919-at32f415/critical-routing.md](examples/260919-at32f415/critical-routing.md), [examples/260919-at32f415/ldo-placement.md](examples/260919-at32f415/ldo-placement.md), [pcb-routing.md](pcb-routing.md) +1 |
| `pcb nets` | List nets on the active PCB (name, length, color) | [actions.md](actions.md), [examples/260919-at32f415/ldo-placement.md](examples/260919-at32f415/ldo-placement.md), [pcb-config.md](pcb-config.md), [pcb-routing.md](pcb-routing.md) +1 |
| `pcb new-board` | Create a NEW board (板) with a fresh empty PCB page, bound to an UNBOUND schematic | [pcb.md](pcb.md) |
| `pcb origin` | Read or set the PCB canvas/display origin (does not move geometry) | [design-flow.md](design-flow.md), [examples/260919-at32f415/fixed-mechanics.md](examples/260919-at32f415/fixed-mechanics.md), [pcb-config.md](pcb-config.md), [pcb.md](pcb.md) |
| `pcb origin get` | Read the canvas-origin X/Y offsets in mil | [design-flow.md](design-flow.md), [examples/260919-at32f415/fixed-mechanics.md](examples/260919-at32f415/fixed-mechanics.md), [pcb-config.md](pcb-config.md), [pcb.md](pcb.md) |
| `pcb origin set` | Set and read back canvas-origin X/Y offsets in mil | [examples/260919-at32f415/fixed-mechanics.md](examples/260919-at32f415/fixed-mechanics.md) |
| `pcb outline-clear` | Remove the current board outline (BOARD_OUTLINE layer) | — |
| `pcb outline-fit` | Resize the board outline to hug the placed parts + a margin (fix low utilization) | [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md) |
| `pcb outline-get` | Read the board outline, including true center-line dimensions and rendered bbox | [design-flow.md](design-flow.md), [examples/260919-at32f415/fixed-mechanics.md](examples/260919-at32f415/fixed-mechanics.md), [pcb-layout-conventions.md](pcb-layout-conventions.md), [pcb-layout.md](pcb-layout.md) +2 |
| `pcb outline-round` | Set a rounded-rectangle board outline (圆角板框) | [design-flow.md](design-flow.md), [examples/260919-at32f415/fixed-mechanics.md](examples/260919-at32f415/fixed-mechanics.md), [pcb-layout-conventions.md](pcb-layout-conventions.md), [pcb-layout.md](pcb-layout.md) |
| `pcb outline-set` | Set the board outline from a closed polygon of points (mil, y-up) | — |
| `pcb place-constrained` | Tiered placement: edge-must parts (connectors/module/IPEX) → board edge + locked, then legalize the rest | [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md) |
| `pcb pour` | Create a copper pour (铺铜) from a closed polygon, bound to a net (usually GND) | [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md), [pcb.md](pcb.md) |
| `pcb pour-clean` | Remove netless copper pours (net:"" dead copper — see `pcb check` netless-pour) | [drc-triage.md](drc-triage.md), [pcb-routing.md](pcb-routing.md), [pcb.md](pcb.md) |
| `pcb pour-delete` | Delete copper pour regions by primitiveId | [pcb-routing.md](pcb-routing.md) |
| `pcb pour-fit` | Auto-size a GND/power pour to the board outline, inset by the board-edge safety distance | [drc-triage.md](drc-triage.md), [pcb-design-rules.md](pcb-design-rules.md), [pcb-routing.md](pcb-routing.md), [pcb.md](pcb.md) |
| `pcb pour-list` | List copper pours (铺铜), optionally by net | [pcb-routing.md](pcb-routing.md) |
| `pcb pour-rebuild` | Re-pour (recompute) all pours after layout/routing changes | [design-decisions.md](design-decisions.md), [design-flow.md](design-flow.md), [drc-triage.md](drc-triage.md), [environment-setup.md](environment-setup.md) +7 |
| `pcb poured-list` | List materialized copper after pour rebuild (exact polygon sources) | [actions.md](actions.md), [evidence-and-verdicts.md](evidence-and-verdicts.md), [pcb-routing.md](pcb-routing.md), [prefab-checklist.md](prefab-checklist.md) +1 |
| `pcb power-planes` | 4-layer power distribution: GND 内电层 + power inner plane + via-stitch (fixes 2-layer pour conflict) | [drc-triage.md](drc-triage.md), [pcb-layout-conventions.md](pcb-layout-conventions.md), [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md) +1 |
| `pcb power-pour` | 2-layer power distribution: pour GND + each rail's local copper (电源走铺铜块, not thin tracks) | [design-decisions.md](design-decisions.md), [pcb-design-rules.md](pcb-design-rules.md), [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md) |
| `pcb refine` | Score-driven placement refinement with per-step rollback (dry-run by default) | [pcb-layout.md](pcb-layout.md) |
| `pcb region` | Keep-out / rule regions (禁止区域): create / list / delete | [examples/exam-differences.md](examples/exam-differences.md), [pcb-routing.md](pcb-routing.md) |
| `pcb region create` | Create a keep-out / rule region (area via --points \| --rect \| --ref) | [pcb-routing.md](pcb-routing.md) |
| `pcb region delete` | Delete keep-out / rule regions by primitiveId | [pcb-routing.md](pcb-routing.md) |
| `pcb region list` | List keep-out / rule regions, optionally by layer | [pcb-routing.md](pcb-routing.md) |
| `pcb report` | Read-only design report: per-net length, net-class totals, diff-pair skew, equal-length spread | [actions.md](actions.md), [pcb-routing.md](pcb-routing.md), [prefab-checklist.md](prefab-checklist.md), [recipes/high-speed.md](recipes/high-speed.md) |
| `pcb rip-up` | Rip up routing (delete tracks+vias); --net to scope, omit = all. Outline/locked are safe | [pcb-auto.md](pcb-auto.md), [pcb-routing.md](pcb-routing.md) |
| `pcb route` | Offline single-layer path solving and independent checking | [pcb-routing.md](pcb-routing.md) |
| `pcb route check` | Recheck a candidate against independent inputs without solving | [pcb-routing.md](pcb-routing.md) |
| `pcb route solve` | Find a bounded straight/45-degree path between measured pads | [pcb-routing.md](pcb-routing.md) |
| `pcb route-critical` | P7.0 critical nets first: power copper → diff pairs routed+measured → locked (issue #127) | [drc-triage.md](drc-triage.md), [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md) |
| `pcb route-short` | Self-route the short, clear hops: per-net MST, L-shaped tracks on the pads' layer | [pcb-auto.md](pcb-auto.md), [pcb-layout.md](pcb-layout.md) |
| `pcb rules` | Apply / check schematic electrical intent as native PCB rules (net classes, widths, clearances, vias, diff pairs) | [actions.md](actions.md), [design-flow.md](design-flow.md), [design-intent.md](design-intent.md), [design-report.md](design-report.md) +8 |
| `pcb rules apply` | Write the differences between intent and live rules (--dry-run: plan only) | [actions.md](actions.md), [design-flow.md](design-flow.md), [design-intent.md](design-intent.md), [drc-triage.md](drc-triage.md) +6 |
| `pcb rules check` | Read-only: diff intent vs live rules; non-zero when anything would be written | [design-flow.md](design-flow.md), [design-report.md](design-report.md), [drc-triage.md](drc-triage.md), [pcb-auto.md](pcb-auto.md) +2 |
| `pcb save` | Save the active PCB document to disk | [actions.md](actions.md), [collaboration-workflow.md](collaboration-workflow.md), [design-flow.md](design-flow.md), [evidence-and-verdicts.md](evidence-and-verdicts.md) +16 |
| `pcb ses-repair` | Repair the active PCB after 'pcb import-autoroute' of a Specctra session | [pcb-routing.md](pcb-routing.md) |
| `pcb silk-add` | Add a free silkscreen string (board marking / credit / note) with config | [pcb-config.md](pcb-config.md), [pcb-layout.md](pcb-layout.md) |
| `pcb silk-align` | Align component designators (位号) with collision avoidance, verified on the readback | [pcb-auto.md](pcb-auto.md), [pcb-layout-conventions.md](pcb-layout-conventions.md), [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md) +1 |
| `pcb silk-import-svg` | Import an SVG logo/artwork as a FILLED silkscreen graphic | [pcb-layout.md](pcb-layout.md) |
| `pcb silk-label-pads` | Label component pads with pin numbers / net names | — |
| `pcb silk-netnames` | Auto-generate network-name silkscreen labels in a zone | — |
| `pcb silk-set` | Batch-adjust existing silk: position / rotation / size / text, or align to a reference | [pcb-layout.md](pcb-layout.md), [pcb.md](pcb.md) |
| `pcb silk-zone-outline` | Auto-draw grouped orthogonal functional-zone outlines on silkscreen | — |
| `pcb slot` | Board cutout / slot (挖槽) — a MULTI-layer fill that mills a hole | [examples/exam-differences.md](examples/exam-differences.md), [pcb-routing.md](pcb-routing.md), [recipes/hv-isolation.md](recipes/hv-isolation.md) |
| `pcb snapshot` | Capture the active PCB canvas as a PNG artifact | [actions.md](actions.md), [pcb-layout-conventions.md](pcb-layout-conventions.md), [pcb-routing.md](pcb-routing.md), [pcb.md](pcb.md) |
| `pcb stackup` | Board stackup: copper layer count + inner-layer types (signal/plane) | [design-decisions.md](design-decisions.md), [pcb-config.md](pcb-config.md), [pcb-layout-conventions.md](pcb-layout-conventions.md), [pcb-layout.md](pcb-layout.md) |
| `pcb stackup set` | Set copper layer count and/or inner-layer types | [design-decisions.md](design-decisions.md), [pcb-config.md](pcb-config.md), [pcb-layout-conventions.md](pcb-layout-conventions.md), [pcb-layout.md](pcb-layout.md) |
| `pcb stackup show` | Show the current stackup (copper layer count + layers) | — |
| `pcb stage` | Deprecated PCB checklist records: status / confirm-layout / confirm-outline / reset | [pcb-layout.md](pcb-layout.md) |
| `pcb stage confirm-layout` | Record the placement in the deprecated checklist | — |
| `pcb stage confirm-outline` | Record the board outline in the deprecated checklist | — |
| `pcb stage confirm-tier` | Record one historical placement tier (does not lock parts or authorize actions) | — |
| `pcb stage reset` | Clear deprecated checklist entries (or --all) | — |
| `pcb stage set-assembly` | Persist assembly spacing metadata for compatibility diagnostics | [pcb-layout.md](pcb-layout.md) |
| `pcb stage status` | Show deprecated checklist records and their historical readiness calculation | — |
| `pcb stage-snapshot` | Capture a recording/demo STAGE: board-fitted viewport PNG + data bundle, gated on a non-blank/non-stale frame | [design-flow.md](design-flow.md), [design-report.md](design-report.md), [evidence-and-verdicts.md](evidence-and-verdicts.md), [examples/260919-at32f415/index.md](examples/260919-at32f415/index.md) +2 |
| `pcb sync-attrs` | Backfill PCB components' empty attribute values from the schematic (器件标准化字段) | [actions.md](actions.md) |
| `pcb sync-designators` | Repair placeholder PCB designators (U? / C?) from the schematic, matched by uniqueId | [actions.md](actions.md), [beginner-troubleshooting.md](beginner-troubleshooting.md), [pcb-layout.md](pcb-layout.md), [recipes/schematic-to-pcb.md](recipes/schematic-to-pcb.md) |
| `pcb track` | Create a copper track (导线) on a layer between two points (mil, y-up) | [examples/260919-at32f415/critical-routing.md](examples/260919-at32f415/critical-routing.md), [examples/260919-at32f415/ldo-placement.md](examples/260919-at32f415/ldo-placement.md) |
| `pcb track-delete` | Delete specific copper tracks by primitiveId (rip-up is net-scoped; this is surgical) | [drc-triage.md](drc-triage.md), [examples/260919-at32f415/ldo-placement.md](examples/260919-at32f415/ldo-placement.md), [pcb-routing.md](pcb-routing.md), [pcb.md](pcb.md) |
| `pcb track-list` | List copper tracks (导线), optionally by net/layer | [examples/260919-at32f415/critical-routing.md](examples/260919-at32f415/critical-routing.md), [examples/260919-at32f415/ldo-placement.md](examples/260919-at32f415/ldo-placement.md), [pcb-routing.md](pcb-routing.md) |
| `pcb track-lock` | Lock/unlock routed copper (tracks/vias/fills) so the native router + pour-rebuild leave it alone | [pcb-layout.md](pcb-layout.md) |
| `pcb via` | Place a via (过孔) at (x,y) with hole + outer diameter (mil, y-up) | — |
| `pcb via-bond` | Assign netless footprint-embedded vias (EPAD thermal vias) the net of the pad they sit in | [pcb-layout.md](pcb-layout.md), [pcb-routing.md](pcb-routing.md) |
| `pcb via-delete` | Delete specific vias by primitiveId (rip-up is net-scoped; this is surgical) | [drc-triage.md](drc-triage.md), [pcb-routing.md](pcb-routing.md), [pcb.md](pcb.md) |
| `pcb via-fence` | Place a perimeter-only via fence around a rectangle | [examples/260919-at32f415/critical-routing.md](examples/260919-at32f415/critical-routing.md), [pcb-routing.md](pcb-routing.md) |
| `pcb via-hop` | Route a layer hop: stub→via→hop-track→via→stub (optional bond fills) | [pcb-routing.md](pcb-routing.md) |
| `pcb via-list` | List vias (过孔), optionally by net | [examples/260919-at32f415/critical-routing.md](examples/260919-at32f415/critical-routing.md), [pcb-routing.md](pcb-routing.md) |
| `pcb via-stitch` | Place a grid of net vias in a rectangle (thermal vias / GND stitching) | [examples/260919-at32f415/critical-routing.md](examples/260919-at32f415/critical-routing.md), [pcb-routing.md](pcb-routing.md) |
| `pcb view-filter` | Read PCB canvas filter state (component-attribute write is unsupported) | [actions.md](actions.md) |
| `pcb view-side` | Switch the PCB view to the top or bottom side (for snapshots) | [actions.md](actions.md), [pcb-routing.md](pcb-routing.md) |
| `pcb widen` | Widen the tracks of chosen nets up to --max-mil where clearance allows | [drc-triage.md](drc-triage.md), [pcb-auto.md](pcb-auto.md), [pcb-routing.md](pcb-routing.md) |
| `pcb zones` | Functional zone claims (S0 modules[].zone → PCB, issue #126): set / status / clear | [pcb-layout.md](pcb-layout.md), [pcb.md](pcb.md) |
| `pcb zones clear` | Remove all zone claims | — |
| `pcb zones set` | Set zone claims from an S0 spec file (--spec) or manually (--module) | [pcb.md](pcb.md) |
| `pcb zones status` | Show the persisted zone claims (and live violations when a window is connected) | — |

## project

| 命令 | 说明 | 文档 |
|---|---|---|
| `project` | Read EasyEDA project and document context | [offline-format-api-watch.md](offline-format-api-watch.md), [project-import.md](project-import.md), [SKILL.md](../SKILL.md) |
| `project create` | Create an EasyEDA project container through the official API | [design-flow.md](design-flow.md), [examples/260919-at32f415/index.md](examples/260919-at32f415/index.md) |
| `project doc` | Read active editor document and schematic page context | — |
| `project export` | Export the active project as a native epro2 archive (no overwrite) | [actions.md](actions.md), [pcb-auto.md](pcb-auto.md), [project-import.md](project-import.md) |
| `project export-source` | Export the current project's original .epro2 file through the official read-only API | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md) |
| `project find` | Find projects by exact friendly name without opening them | [SKILL.md](../SKILL.md) |
| `project import` | Import a KiCad (or other) project into EasyEDA as a NEW project | — |
| `project info` | Read current EasyEDA project information | — |
| `project inspect-eprj3` | Read an EasyEDA Pro V4 local .eprj3 project offline (inventory, counts, units, version markers, schema findings) | [actions.md](actions.md), [offline-format-api-watch.md](offline-format-api-watch.md), [SKILL.md](../SKILL.md) |
| `project open` | Open a project by --project-uuid, or a legacy document by --uuid | [actions.md](actions.md), [design-flow.md](design-flow.md), [project-import.md](project-import.md) |

## project-config

| 命令 | 说明 | 文档 |
|---|---|---|
| `project-config` | Per-project process template: which steps/sims/report sections run, plus constraints (pcbpilot.project.json) | [design-flow.md](design-flow.md) |
| `project-config init` | Create pcbpilot.project.json from a template and register the dir with the console | [design-flow.md](design-flow.md) |
| `project-config set` | Change toggles and constraints (validated before writing) | — |
| `project-config show` | Print the config (JSON) with validation issues, skipped steps and sections | [design-flow.md](design-flow.md), [SKILL.md](../SKILL.md) |
| `project-config validate` | Validate the config; exit 1 on any error-level issue | — |

## report

| 命令 | 说明 | 文档 |
|---|---|---|
| `report` | Customer-facing, versioned design reports built from a run's artifacts (offline) | [board-manual.md](board-manual.md), [design-flow.md](design-flow.md), [design-report.md](design-report.md), [post-layout-sim.md](post-layout-sim.md) +1 |
| `report design` | Generate the next version of the customer design report (HTML + Markdown + JSON, changelog vs the previous version) | [analog-sim.md](analog-sim.md), [board-manual.md](board-manual.md), [design-flow.md](design-flow.md), [design-report.md](design-report.md) +5 |
| `report manual` | Generate the board USER MANUAL (one self-contained HTML: board picture, connector pinouts, power, I/O, LEDs, tests, bring-up, software) | [board-manual.md](board-manual.md), [SKILL.md](../SKILL.md) |

## review-panel

| 命令 | 说明 | 文档 |
|---|---|---|
| `review-panel` | Hard gate: Codex, Kimi and Claude Code review the design against the project requirements | [pcb-auto.md](pcb-auto.md), [review-workflow.md](review-workflow.md) |

## sch

| 命令 | 说明 | 文档 |
|---|---|---|
| `sch` | Schematic operations | [actions.md](actions.md), [analog-sim.md](analog-sim.md), [auto-layout-sop.md](auto-layout-sop.md), [design-intent.md](design-intent.md) +17 |
| `sch aesthetics` | Measure schematic aesthetics: wiring, layout, labels, bus lanes (report-only, weight 0) | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [schematic-wiring.md](schematic-wiring.md), [schematic.md](schematic.md) +1 |
| `sch align` | Align parts' rendered bboxes (left\|right\|top\|bottom\|centerx\|centery) to a reference part | [schematic-placement.md](schematic-placement.md) |
| `sch apply` | Replay a declarative step file (playbook) against EasyEDA | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [collaboration-workflow.md](collaboration-workflow.md), [design-flow.md](design-flow.md) +5 |
| `sch attribute-inspect` | Inspect one active-page attribute's visibility through three official read paths | [actions.md](actions.md) |
| `sch autoconnect` | Pin-aware planner: auto-pick direction/offset and connect a pin (or batch) to a flag/netport | [pcb-auto.md](pcb-auto.md), [schematic-data.md](schematic-data.md), [schematic-layout-conventions.md](schematic-layout-conventions.md), [schematic-wiring.md](schematic-wiring.md) +1 |
| `sch autolayout` | Module-aware planner: place parts by module zone with deterministic, lint-clean coordinates | [schematic-layout-conventions.md](schematic-layout-conventions.md), [schematic-placement.md](schematic-placement.md) |
| `sch autoplace-free` | Pack parts into the sheet's blank space (zone-less; keeps other parts fixed) | [schematic-placement.md](schematic-placement.md) |
| `sch block-apply` | Instantiate a circuit block: place its parts, wire internal_nets, bind ports | [drc-triage.md](drc-triage.md), [part-selection.md](part-selection.md), [schematic.md](schematic.md), [standard-blocks-contributing.md](standard-blocks-contributing.md) |
| `sch bridge-check` | Detect共线合并短路 (bridges) and孤儿桩 (orphans) at wire-tree granularity | [collaboration-workflow.md](collaboration-workflow.md), [drc-triage.md](drc-triage.md), [evidence-and-verdicts.md](evidence-and-verdicts.md), [prefab-checklist.md](prefab-checklist.md) +3 |
| `sch bus` | Native schematic buses (sch_PrimitiveBus, @beta; live-verified on V3 3.2.149, V4 unverified) and offline bus-candidate detection | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [schematic-wiring.md](schematic-wiring.md), [schematic.md](schematic.md) +1 |
| `sch bus apply` | Create a layout plan's native buses after their member labels exist (journalled, readback-verified, replace-safe) | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [schematic-wiring.md](schematic-wiring.md), [schematic.md](schematic.md) |
| `sch bus candidates` | Offline: find bus / virtual-bus candidate groups in a page snapshot and judge their label lanes | [actions.md](actions.md), [schematic-wiring.md](schematic-wiring.md) |
| `sch bus check` | Check every native bus: members exist on pins and carry their own label/port (non-zero exit on errors) | [actions.md](actions.md), [schematic-wiring.md](schematic-wiring.md) |
| `sch bus create` | Create one native bus from orthogonal polylines (verified by readback; --dry-run validates offline) | [actions.md](actions.md), [schematic-wiring.md](schematic-wiring.md) |
| `sch bus delete` | Delete native buses by primitive id (verified by readback) | — |
| `sch bus list` | List native buses on the active page (read-only) | [actions.md](actions.md) |
| `sch check` | Reconstructed per-item design check the SDK DRC can't itemize | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [beginner-troubleshooting.md](beginner-troubleshooting.md), [collaboration-workflow.md](collaboration-workflow.md) +14 |
| `sch clear` | Clear the active schematic page (delete all page primitives: components, flags, wires, buses, graphics) | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [pcb-auto.md](pcb-auto.md), [pcb-routing.md](pcb-routing.md) +3 |
| `sch clusters` | 列出 L1 虚拟组(器件 + 它自己的 marker/桩线)并判组间重叠 / 出图纸 | — |
| `sch compose` | Compose authored Lib circuits onto one sheet and compile a guarded SCH Apply | [library/modules/README.md](../library/modules/README.md), [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [collaboration-workflow.md](collaboration-workflow.md) +6 |
| `sch connect` | Stub a wire out of a pin and place a netflag/netport at its far end | [actions.md](actions.md), [drc-triage.md](drc-triage.md), [schematic-wiring.md](schematic-wiring.md), [schematic.md](schematic.md) |
| `sch connectivity` | Export layout-independent schematic connectivity IR | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [design-flow.md](design-flow.md), [design-intent.md](design-intent.md) +12 |
| `sch connectivity-diff` | Compare two connectivity snapshots offline | [actions.md](actions.md), [schematic-data.md](schematic-data.md) |
| `sch design-diff` | Compare local schematic identity, connectivity, geometry and complete compose plans | [actions.md](actions.md), [design-flow.md](design-flow.md), [schematic-data.md](schematic-data.md) |
| `sch designator-geometry` | Export measured current-page Designator text bboxes (read-only) | [auto-layout-sop.md](auto-layout-sop.md), [schematic-data.md](schematic-data.md) |
| `sch designators` | Allocate official reference numbers and repair placed component bindings | [actions.md](actions.md), [design-flow.md](design-flow.md), [schematic-data.md](schematic-data.md), [schematic-layout-conventions.md](schematic-layout-conventions.md) +2 |
| `sch designators allocate` | Allocate missing numeric references from measured library prefixes (offline) | [actions.md](actions.md), [design-flow.md](design-flow.md), [schematic-data.md](schematic-data.md), [schematic.md](schematic.md) |
| `sch designators plan` | Generate a guarded same-page renumbering sch apply queue (offline) | [schematic-data.md](schematic-data.md), [schematic-placement.md](schematic-placement.md) |
| `sch designators verify` | Verify a file-pinned designator repair against fresh EDA reads | — |
| `sch destagger` | 批量消 marker-overlap:换方向/桩长并带桩线一起挪(默认 dry-run;--apply 执行) | [schematic-placement.md](schematic-placement.md) |
| `sch disconnect` | Remove a pin's stub wire and its netflag/netport together (inverse of connect) | [actions.md](actions.md), [schematic.md](schematic.md) |
| `sch distribute` | Equalize edge-to-edge gaps between parts along one axis (x\|y) | — |
| `sch drc` | Run the official schematic DRC SDK gate (may be boolean/aggregate only) | [collaboration-workflow.md](collaboration-workflow.md), [evidence-and-verdicts.md](evidence-and-verdicts.md), [examples/260919-at32f415/index.md](examples/260919-at32f415/index.md), [examples/260919-at32f415/ldo-placement.md](examples/260919-at32f415/ldo-placement.md) +3 |
| `sch export-image` | Render the page — or only the given primitives — to SVG/PNG/PDF | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [collaboration-workflow.md](collaboration-workflow.md), [design-report.md](design-report.md) +5 |
| `sch extract-layout` | Reverse-derive a block's schematic_layout template from a placed instance (issue #140) | — |
| `sch frame` | Apply or verify computed module frames and titles from JSON | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [pcb-auto.md](pcb-auto.md), [schematic-data.md](schematic-data.md) +1 |
| `sch frame apply` | Apply owned dashed frames and titles; matching data performs zero graphics writes | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [pcb-auto.md](pcb-auto.md), [schematic-data.md](schematic-data.md) +1 |
| `sch frame check` | Read back owned frame geometry and styling; never modify the schematic | [auto-layout-sop.md](auto-layout-sop.md), [schematic-placement.md](schematic-placement.md) |
| `sch gate` | S5 校验门:按固定顺序跑 layout-lint → check → bridge-check → drc,出一张报告 | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [collaboration-workflow.md](collaboration-workflow.md), [design-flow.md](design-flow.md) +8 |
| `sch group` | Persistent virtual groups (create/list/add/remove/ungroup) — the platform has NO grouping API | [actions.md](actions.md), [schematic-placement.md](schematic-placement.md) |
| `sch group add` | Add members to an existing group | — |
| `sch group create` | Create a group from member designators (CSV); id is auto-assigned (g1, g2, …) | — |
| `sch group list` | List groups on the active page (--all-pages for every page); absent members are marked stale | — |
| `sch group remove` | Remove members from a group (an emptied group is deleted) | — |
| `sch group tidy` | 组内布局计算:双旗无源件竖放上电下地文字朝外,netport 水平化(默认 dry-run) | [schematic-placement.md](schematic-placement.md) |
| `sch group ungroup` | Dissolve a group (removes the relation only — no primitive is touched) | — |
| `sch group-arrange` | 第二层排布:把虚拟组当刚体,按**跨组信号网**关系铺进图纸可用区(ADR-0003) | — |
| `sch group-move` | Translate components+wires together as one rigid assembly (dx,dy) — by --ids or persistent --group/--groups | [schematic-placement.md](schematic-placement.md), [schematic.md](schematic.md) |
| `sch groups` | Read module frames from live schematic pages → PCB placement groups (read-only) | [actions.md](actions.md), [pcb-auto.md](pcb-auto.md) |
| `sch intent-annotate` | Write the electrical intent (block summaries, rail V/I/width/class) into the schematic as one replaceable text block | [actions.md](actions.md), [design-flow.md](design-flow.md), [pcb-config.md](pcb-config.md) |
| `sch layout-composition` | Convert a zones source plus one selected layout-sheet-plan page into compose input (offline) | [auto-layout-sop.md](auto-layout-sop.md) |
| `sch layout-edit` | Plan a core-relative move or protected pin-marker repair from retained data | [actions.md](actions.md), [schematic-data.md](schematic-data.md) |
| `sch layout-lint` | Check component placement for bbox overlaps and tight spacing | [collaboration-workflow.md](collaboration-workflow.md), [part-selection.md](part-selection.md), [schematic-layout-conventions.md](schematic-layout-conventions.md) |
| `sch layout-plan` | Plan a measured component set offline without Lib or project metadata | [auto-layout-sop.md](auto-layout-sop.md), [examples/260919-at32f415/ldo-placement.md](examples/260919-at32f415/ldo-placement.md), [schematic-data.md](schematic-data.md), [schematic-wiring.md](schematic-wiring.md) |
| `sch layout-render` | Compile layout JSON to SVG offline (no AI, EDA or diff panels) | [schematic-data.md](schematic-data.md) |
| `sch layout-score` | Score schematic layout readability (folded/reversed labels, proximity, chains) with executable fixes | — |
| `sch layout-sheet-plan` | Pack existing zone geometry into sheet previews offline (no Apply) | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [schematic-data.md](schematic-data.md) |
| `sch lib-layout` | Calculate Lib placement and wiring offline from canonical nets and measured pins | [library/modules/README.md](../library/modules/README.md), [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [collaboration-workflow.md](collaboration-workflow.md) +4 |
| `sch list` | List components on the active (or all) schematic page(s) | [actions.md](actions.md), [analog-sim.md](analog-sim.md), [auto-layout-sop.md](auto-layout-sop.md), [beginner-troubleshooting.md](beginner-troubleshooting.md) +8 |
| `sch materialize` | 将 1.4 原理图数据转换为器件放置 Apply 队列 | [actions.md](actions.md), [schematic-data.md](schematic-data.md) |
| `sch modify` | Modify component position, designator, BOM flags, or custom properties | [actions.md](actions.md), [examples/260919-at32f415/layout-candidates.md](examples/260919-at32f415/layout-candidates.md), [examples/260919-at32f415/ldo-placement.md](examples/260919-at32f415/ldo-placement.md), [part-selection.md](part-selection.md) +1 |
| `sch netflag` | Create a power/ground/net flag or port | — |
| `sch netlist` | Export schematic netlist as an artifact | [schematic.md](schematic.md) |
| `sch nets` | 跨页网名审计:同一条轨被写成多个名字 / 单引脚网(逐页命令看不见的那半) | [drc-triage.md](drc-triage.md), [evidence-and-verdicts.md](evidence-and-verdicts.md), [prefab-checklist.md](prefab-checklist.md), [review-workflow.md](review-workflow.md) |
| `sch no-connect` | Mark (or clear) a pin's no-connect flag (非连接标识) | [actions.md](actions.md), [drc-triage.md](drc-triage.md), [review-workflow.md](review-workflow.md), [schematic-layout-conventions.md](schematic-layout-conventions.md) +1 |
| `sch open` | Open or activate a schematic page by UUID | — |
| `sch page-delete` | Delete a schematic page (no undo) | [schematic-data.md](schematic-data.md) |
| `sch page-new` | Create a new schematic page under a schematic document | — |
| `sch page-rename` | Rename a schematic page | — |
| `sch pages` | List schematic documents and pages in the current project | [design-pre-analysis.md](design-pre-analysis.md), [recipes/schematic-to-pcb.md](recipes/schematic-to-pcb.md), [review-workflow.md](review-workflow.md) |
| `sch pin-swap` | Compile a pin-swap item of feedback.json into a guarded sch apply playbook (offline; live-unverified) | [design-flow.md](design-flow.md), [pcb-auto.md](pcb-auto.md), [SKILL.md](../SKILL.md) |
| `sch place` | Place a component from the device library at coordinates | [actions.md](actions.md), [examples/260919-at32f415/layout-candidates.md](examples/260919-at32f415/layout-candidates.md), [kicad-sch-build.md](kicad-sch-build.md), [kicad.md](kicad.md) +1 |
| `sch plan` | Generate a guarded sch apply playbook for explicit marker additions, including NC-to-connected pins (offline) | [actions.md](actions.md), [schematic-data.md](schematic-data.md) |
| `sch power-layout` | Plan a four-part fixed AMS1117 power circuit offline from measured geometry | [schematic-placement.md](schematic-placement.md) |
| `sch prim-delete` | Delete schematic primitives of ANY type by id (or the current selection if --ids omitted) | [actions.md](actions.md), [drc-triage.md](drc-triage.md), [schematic-layout-conventions.md](schematic-layout-conventions.md) |
| `sch read` | One-call semantic snapshot of the circuit (components + nets + check) | [actions.md](actions.md), [examples/260919-at32f415/index.md](examples/260919-at32f415/index.md), [examples/260919-at32f415/ldo-placement.md](examples/260919-at32f415/ldo-placement.md), [pcb-layout.md](pcb-layout.md) +3 |
| `sch rebind-footprint` | Swap a footprint transactionally (candidate create/readback before original delete) | — |
| `sch rebind-symbol` | Swap a symbol transactionally (candidate create/readback before original delete) | — |
| `sch reconcile` | 把「接对了没有」变成机械判据:从块库重推应有连接,与活体网表逐条对账 | [drc-triage.md](drc-triage.md), [evidence-and-verdicts.md](evidence-and-verdicts.md), [review-workflow.md](review-workflow.md) |
| `sch rename` | Rename a schematic document (the whole sheet, not a single page) | [actions.md](actions.md) |
| `sch replace` | Replace a placed component with a different library device (换型号 / 器件标准化) | [actions.md](actions.md), [part-selection.md](part-selection.md) |
| `sch resolve-lcsc` | Deterministically resolve placed parts to their device's REAL LCSC C-number (dry-run; --apply writes back) | [actions.md](actions.md), [schematic.md](schematic.md) |
| `sch save` | Save the active schematic document | [actions.md](actions.md), [analog-sim.md](analog-sim.md), [auto-layout-sop.md](auto-layout-sop.md), [collaboration-workflow.md](collaboration-workflow.md) +6 |
| `sch select` | Select schematic primitives by ID | — |
| `sch sheet` | Sheet 层操作(三层体系最外层)— tidy 全部功能区依据纸张排布 | [schematic-placement.md](schematic-placement.md) |
| `sch sheet tidy` | Sheet 层布局:全部功能区当刚体依据纸张排布(锚区+行排;默认 dry-run,--apply 逐区 zone move) | [schematic-placement.md](schematic-placement.md) |
| `sch sheet-geometry` | Report sheet bounds + title-block keep-out geometry (provenance-tagged) | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [design-flow.md](design-flow.md), [schematic-data.md](schematic-data.md) +4 |
| `sch status` | 原理图进度(S1–S6):逐条从活体推导,不落盘,判不了的明说 | — |
| `sch text-list` | List ALL text primitives on the active schematic page (id/content/x/y/…) | [schematic-layout-conventions.md](schematic-layout-conventions.md) |
| `sch titleblock` | Adjust the focused page's 明细表 (title block): visibility and/or fields | [actions.md](actions.md), [examples/260919-at32f415/index.md](examples/260919-at32f415/index.md), [schematic-data.md](schematic-data.md) |
| `sch titleblock-get` | Read a page's 明细表 (title block): show flag + field keys/values | [actions.md](actions.md), [auto-layout-sop.md](auto-layout-sop.md), [design-pre-analysis.md](design-pre-analysis.md), [environment-setup.md](environment-setup.md) +2 |
| `sch wire` | Create a schematic wire polyline — ESCAPE HATCH; 常规布线走 `sch autoconnect` / `sch block-apply` | [examples/260919-at32f415/ldo-placement.md](examples/260919-at32f415/ldo-placement.md) |
| `sch zone` | 功能区层操作(三层体系:Sheet→Zone→Group)— move 整区刚移 / tidy 组间叠加布局 | [schematic-placement.md](schematic-placement.md) |
| `sch zone move` | 功能区整体刚移:区内组+散件+桩线+旗+note 一起平移 (dx,dy),分区框自动重画 | — |
| `sch zone relayout` | 区级 placement-first 重排:锚定核心器件 → 外围纯计算落位 → 一遍性重连(不搬带线的东西) | — |
| `sch zone tidy` | 组间叠加布局:把功能区内的组与散件当刚体在区带内重排(默认 dry-run;--apply 执行) | [schematic-placement.md](schematic-placement.md) |
| `sch zone-arrange` | Two-phase deterministic zone layout plan: intra-zone compaction (R1-R5) + edge-affinity multi-shelf packing (A4-only, no mutation) | [schematic-placement.md](schematic-placement.md) |
| `sch zone-draw` | Draw the claimed functional zones as dashed frames + labels on the sheet (--clear removes them) | [schematic-layout-conventions.md](schematic-layout-conventions.md), [schematic-placement.md](schematic-placement.md) |
| `sch zone-plan` | Plan data-driven A4 functional partitions from the live sheet + module bboxes (no mutation) | [schematic-layout-conventions.md](schematic-layout-conventions.md), [schematic-placement.md](schematic-placement.md) |
| `sch zone-review` | 只读复核源 JSON 的 zone 归属，提示 AI 决定是否拆分 | [schematic-data.md](schematic-data.md) |
| `sch zones` | 原理图分区认领(S0 modules[].zone → 图纸格位):set / status / clear | [schematic-layout-conventions.md](schematic-layout-conventions.md) |
| `sch zones clear` | Remove all schematic zone claims | — |
| `sch zones set` | 认领模块的落位格位(给布局前的 autolayout 用;成员归属请用虚拟组) | [schematic-layout-conventions.md](schematic-layout-conventions.md) |
| `sch zones status` | 本页布局对象注册表(模块认领+块组+子组,--zone/--group 的统一命名空间) | — |
| `sch zones-derive` | Derive a layout-plan --zones source from design intent and measured symbols (offline) | [auto-layout-sop.md](auto-layout-sop.md) |

## signoff

| 命令 | 说明 | 文档 |
|---|---|---|
| `signoff` | Release sign-off (EasyEDA and KiCad alike): review, LCSC parts, safety, per-segment copper, IR, schematic↔PCB continuity and traceability, manual + report — non-zero unless all pass | [kicad.md](kicad.md), [pcb-auto.md](pcb-auto.md), [prefab-checklist.md](prefab-checklist.md), [review-workflow.md](review-workflow.md) +1 |

## sim

| 命令 | 说明 | 文档 |
|---|---|---|
| `sim` | Circuit simulations computed from the schematic (DC power tree, analog SPICE) | [analog-sim.md](analog-sim.md), [design-flow.md](design-flow.md), [environment-setup.md](environment-setup.md), [pcb-auto.md](pcb-auto.md) +6 |
| `sim analog` | SPICE (ngspice) simulation of the schematic's analog circuits + value optimisation plan | [analog-sim.md](analog-sim.md), [design-flow.md](design-flow.md), [design-intent.md](design-intent.md), [design-report.md](design-report.md) +3 |
| `sim analog compile-plan` | Compile an analog value-change plan into a pcbpilot apply playbook (after the user confirmed it) | [analog-sim.md](analog-sim.md), [design-flow.md](design-flow.md) |
| `sim post-layout` | Post-layout (设计后) verification on the real copper: IR drop, via currents, current density, thermal maps, part temperatures | [board-manual.md](board-manual.md), [design-flow.md](design-flow.md), [design-report.md](design-report.md), [environment-setup.md](environment-setup.md) +9 |
| `sim power` | DC power simulation: per-net voltage and per-pin current for every scenario | [actions.md](actions.md), [analog-sim.md](analog-sim.md), [design-intent.md](design-intent.md), [design-report.md](design-report.md) +9 |
| `sim tools` | Check / install the external cross-check simulators (ngspice, Elmer FEM) | [analog-sim.md](analog-sim.md), [design-flow.md](design-flow.md), [design-intent.md](design-intent.md), [environment-setup.md](environment-setup.md) +3 |
| `sim tools check` | Report path, version, minimum and status (ok/missing/outdated) of each simulator | [environment-setup.md](environment-setup.md), [post-layout-sim.md](post-layout-sim.md), [power-sim.md](power-sim.md), [SKILL.md](../SKILL.md) |
| `sim tools install` | Install missing/outdated simulators with this platform's package manager | [analog-sim.md](analog-sim.md), [design-flow.md](design-flow.md), [design-intent.md](design-intent.md), [environment-setup.md](environment-setup.md) +1 |

## skill

| 命令 | 说明 | 文档 |
|---|---|---|
| `skill` | Inspect and update the locally-installed pcbpilot skill dirs | [kicad-jlc.md](kicad-jlc.md) |
| `skill status` | Show installed skill dirs, their versions, and the latest release | — |
| `skill sync` | Update installed skill dirs to the latest release (or a pinned --version) | [kicad-jlc.md](kicad-jlc.md) |

## spec

| 命令 | 说明 | 文档 |
|---|---|---|
| `spec` | Validate and inspect the S0 design proposal (S0 spec) | [pcb-layout.md](pcb-layout.md) |
| `spec backfill` | 把 block-apply 落地后的真实位号回填进 spec 的 modules[].parts(离线) | — |
| `spec show` | Print the normalized S0 spec (both legacy spellings folded into one shape) | [pcb-layout.md](pcb-layout.md) |
| `spec validate` | Check an S0 spec for unknown enum values, contradictions and gaps | [pcb-layout.md](pcb-layout.md) |

## update

| 命令 | 说明 | 文档 |
|---|---|---|
| `update` | Upgrade everything: CLI, Skill, MCP, sim tools, daemon (+ connector download) | [auto-layout-sop.md](auto-layout-sop.md), [collaboration-workflow.md](collaboration-workflow.md), [environment-setup.md](environment-setup.md), [SKILL.md](../SKILL.md) |

## version

| 命令 | 说明 | 文档 |
|---|---|---|
| `version` | Print version information | — |

## view

| 命令 | 说明 | 文档 |
|---|---|---|
| `view` | Editor canvas view shortcuts (zoom / fit) for schematic & PCB | [pcb-routing.md](pcb-routing.md), [pcb.md](pcb.md) |
| `view fit` | Zoom to fit all primitives (适应全部, the `K` shortcut) | [pcb-routing.md](pcb-routing.md), [pcb.md](pcb.md) |
| `view fit-selection` | Zoom to fit the currently selected primitives (适应选中) | [pcb.md](pcb.md) |
| `view region` | Zoom to a rectangular region (canvas units: sch 0.01inch, PCB mil) | [pcb-routing.md](pcb-routing.md) |
| `view zoom` | Pan/zoom to a center coordinate and/or scale ratio (percent) | — |

## web

| 命令 | 说明 | 文档 |
|---|---|---|
| `web` | Control the connected EasyEDA Web editor page | [environment-setup.md](environment-setup.md) |
| `web reload` | Save the active document, refresh the whole Web page, and verify reconnect | [actions.md](actions.md), [environment-setup.md](environment-setup.md) |

## workflow

| 命令 | 说明 | 文档 |
|---|---|---|
| `workflow` | Deprecated project checklist: init / status / advance / confirm / reset / pages | — |
| `workflow advance` | Advance the deprecated checklist and print its historical next step | — |
| `workflow confirm` | Record a historical layout/outline checklist entry (no execution permission) | — |
| `workflow init` | Initialize (or restart) the deprecated project checklist | — |
| `workflow pages` | 逐页记账的工程归属表(--reap 用活体页表核销,--prune 删掉已证明外来的残留) | — |
| `workflow reset` | Clear deprecated checklist entries (or --all) | — |
| `workflow status` | Show deprecated checklist state; --reconcile compares it with the live document | [pcb-layout.md](pcb-layout.md) |

共 421 条命令，其中 102 条尚无参考提到。
