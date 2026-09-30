# esp32-v05 — ESP32-S3 mini, live board revision (v0.5/v0.6)

| File | What |
|---|---|
| `board.json` | pre-layout `pcb dump` of the live board (designators: U1 SY8089 buck, U2 CH340C, U3 ESP32-S3-WROOM-1, D3 USBLC6). **No board outline** (`partial: board outline unavailable`). |
| `mech.json` | 4 × M3 corner holes, `autoSize` frame — meant for `pcb auto run --place`. |
| `groups1.json`, `groups2.json` | placement groups. |
| `intent.json`, `sim.json` | intent / sim power of this revision. |
| `sch-*.json` + `values.json` | connectivity + values of **the same revision** (live `artifacts/v06-live/conn-*.json`; values per MPN). `sim power` on them reproduces `sim.json` and `docs/examples/esp32-mini-design-report/v3/data/sim.json`. |

Use these sim inputs with this board. `pkg/powersim/testdata/esp32mini` is an
**earlier** schematic revision of the same project (ESP32 = U1, buck = U4, nets
+5V/VBUS/5V_TERM); pairing it with this board put the 1.66 W ESP32 on the buck's
SOT-23 pads (192 °C) and dropped 17 current-carrying pins. `sim post-layout` now
fails such a pair with `sim-board-mismatch`.

## Route-only (no `--place`)

The dump has no outline, so a route-only run routes inside the part envelope.
Known results (2026-09-28):

- with `--mech`: refused — the autoSize corner holes land on the corner parts
  (MH3 over SW1.1/SW1.2). Before the guard this silently lost IO0 and GND
  (SW1 pads inside the hole keep-out) and wrote the holes into the playbook; the
  offline E2E's "95.9 %" was that plus CC1 congestion on 2 layers.
  J2 and R4 do **not** overlap (25.7 mil apart).
- without `--mech`, 4 layers, intent + sim: 100 % (30/30), DRC 0. Before the
  `nodeCong` bounds fix this panicked (claim disk one cell off the grid).
- Route-only also keeps J2's own locating holes at the mech-moved pose, not the
  measured one (`savePose/restore` restores pads, not part holes) — known gap.

Regression: `TestRouteOnlyMechHoleOnPartRefused`, `TestRouteOnlyNoOutlineNoPanic`,
`TestESP32MiniIntentKeepsESDOnPath` (with `--place`).
