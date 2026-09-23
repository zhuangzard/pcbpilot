# Vision

`pcbpilot` follows an AI-native system shape:

```text
user goal
  -> Skill workflow
  -> source design data / constraints
  -> pure computation / data validation
  -> Go CLI/daemon typed actions
  -> EasyEDA connector plugin
  -> official eda API
```

The goal is not to replace EasyEDA. The goal is to make EasyEDA controllable through stable, inspectable, auditable operations that an AI agent can call reliably.

## Principles

1. Keep the EasyEDA extension thin.
   The connector should handle WebSocket connection, action dispatch, official `eda.*` calls, result normalization, and artifact upload. It should not own high-level workflow.

2. Make typed actions the default.
   Raw JavaScript execution is a debug escape hatch, not the main interface.

3. Treat state as product surface.
   Every meaningful response should carry active window, project, document, selected objects, warnings, and suggested verification.

4. Treat artifacts as first-class.
   Screenshots, BOM, netlist, and later Gerber/STEP files must be transferred as files with IDs, paths, MIME types, hashes, and sizes.

5. Put expert judgment in Skills.
   Skill instructions should constrain workflow, confirmation points, verification, and repair strategy.

6. Prefer data-driven closed loops.
   Preserve raw observations, revise source design data, compute and validate, then apply and read back.
   Screenshots aid review but never replace missing machine evidence. Follow the
   [schematic architecture baseline](../.agents/skills/pcbpilot/references/schematic-data.md#数据驱动架构基准).

## Non-goals

- Reimplement EasyEDA's editor.
- Scrape or automate DOM interactions as the primary path.
- Mirror all official API methods immediately.
- Let AI generate arbitrary EasyEDA JavaScript for normal workflows.
