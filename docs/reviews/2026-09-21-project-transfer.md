# Project transfer and schematic identity investigation

Historical API exploration (before typed connector migration): validated on Windows, EasyEDA Pro 3.2.149.88089769, connector/daemon 1.5.2.
The CLI and MCP under test contain this patch; the installed connector was not changed.

- Old `project open --uuid` dispatches document.open, so it cannot be relied on to switch projects.
- Official openProject can resolve and expose project identity before the page tree is populated.
  The new project UUID path optionally waits for a specified schematic page and verifies both identities.
- Native getProjectFile(..., undefined, "epro2") returns a usable ZIP archive in the online host.
  Export succeeded through both CLI and real MCP stdio, with integrity and SHA-256 validation.
  Import/restore is not verified and is explicitly false in the result.
- Live round trip between two saved test projects succeeded. The normal three-net endpoint
  contract remained intact after returning to its schematic. No existing circuit edits were required.
- A separate one-resistor project reproduced supplierId reset when modifying only the designator.
  Including the observed supplierId in the same patch preserved it, and the resolved library UUID
  remained correct after save/reload. The Skill documents this compensation; no automatic
  connector-side property-preservation fix is claimed.
- Native schematic DRC in this host exposes only aggregate counts despite the verbose flag.
  Existing CLI help correctly warns about this. Reconstructed connectivity checks cannot identify
  those native warnings; they are not cleared or relabelled by this change.

Official contracts:
- https://prodocs.lceda.cn/cn/api/reference/pro-api.dmt_project.openproject.html
- https://prodocs.lceda.cn/cn/api/reference/pro-api.sys_filemanager.getprojectfile.html
- https://prodocs.lceda.cn/cn/api/reference/pro-api.sch_drc.check_1.html

Validation: targeted Go contract/ZIP/CLI tests and nine MCP tests passed, plus Skill package check.
The full Windows Go suite has seven pre-existing top-level failures, independently reproduced
on unmodified upstream e1fa8b0: TestSchDesignatorsOutputAliasesRejected, TestStripArtifactNesting,
TestResolveEnrichScriptPriority, TestResolveEnrichScriptNotFoundListsProbedPaths,
TestLayoutReportRefusesPathAliasesBeforeWriting, TestUpdateCLIReplacesBinaryAndVerifiesChecksum,
TestLocalBundleChecksContentsNotMarkers. They involve Windows symlink permissions, file modes,
path handling or installed-skill discovery. Excluding precisely these cases, all packages pass.
No claim of an unfiltered green suite, whole-board E2E, or manufacturing readiness.

## PR review: typed connector migration

The maintainer correctly rejected dispatching `debug.exec_js` from the CLI. The
historical live evidence above establishes API behavior only; it does not verify
the revised connector build. `project.open` and `project.export` now have dedicated
connector handlers and protocol catalog entries. Cobra and MCP dispatch those
actions, without a script fallback. Direct action calls enforce the same explicit
open acknowledgement, UUID checks and archive bounds. Skill action references
are synchronized at `.agents/skills/easyeda-agent/`.

Connector tests exercise `runAction` rather than generated JavaScript. CLI tests
assert action names/payloads and no fallback on UNKNOWN_ACTION. The archive tests
remain. Connector typecheck, all 419 tests and package build passed.
A matching rebuilt connector is required; installed 1.5.2 lacks these handlers.
New-build live verification and the prescribed whole-board ESP32 regression are
pending installation, not implied by old probes or offline tests. No release.

A read-only compatibility probe against the installed 1.5.2 daemon rejected `project.export` as unknown before connector dispatch; no file was written. Deployment requires the updated daemon catalog as well as CLI/MCP and connector, not a connector-only update.
