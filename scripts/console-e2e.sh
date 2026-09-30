#!/usr/bin/env bash
# Offline end-to-end design chain for the console acceptance test (v0.7).
#
# Runs, per board, in a fresh work dir (never touches an EDA editor — every
# command gets offline inputs, and --ports points at a port with no connector):
#   intent derive → sim power → sim analog → pcb auto run --place --intent --sim
#   → pcb check --board --intent → sim post-layout → report design
# Boards, low voltage → high voltage:
#   esp32      ESP32-S3 mini (5 V / 3V3), two rounds: round 2 changes the LED
#              resistor (what-if) so the console shows sim deltas and report v2
#   hv-flyback mains → SELV flyback stress board (testdata/stress/hv/flyback)
#   hv-iso-fail the iso-violation board checked against the mains/SELV intent
#              (pkg/pcbauto/testdata) — must end FAIL
# Each work dir gets pcbpilot.project.json and is registered with the console.
#
# Usage: scripts/console-e2e.sh <out-dir>
#   PCBPILOT=<binary>        (default: pcbpilot on PATH)
#   PCBPILOT_PORTS=61850-61850  daemon port for run events (default: a dead port)
#   ROUTE_TIMEOUT=90s        pcb auto routing budget per board
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=${1:?usage: scripts/console-e2e.sh <out-dir>}
BIN=${PCBPILOT:-pcbpilot}
PORTS=${PCBPILOT_PORTS:-61899-61899}
TO=${ROUTE_TIMEOUT:-90s}
export PCBPILOT_SKILLS_DIR="$ROOT/.agents/skills"
R="$ROOT/.agents/skills/pcbpilot/references"
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
P() { "$BIN" "$@" --ports "$PORTS"; }
log() { printf '\n== %s\n' "$*"; }
# step <name> <cmd…>: run, record exit code (a failing check is a result, not a script error)
step() { local name=$1; shift; if "$@" >"$W/logs/$name.out" 2>"$W/logs/$name.err"; then echo "ok   $name"; else echo "FAIL $name (exit $?)"; fi; }

# ── ESP32-S3 mini (low voltage) ────────────────────────────────────────────
E="$ROOT/pkg/powersim/testdata/esp32mini"
B="$ROOT/internal/app/testdata/esp32-v05"
W="$OUT/esp32"; mkdir -p "$W/logs"; cd "$W"
P project-config init --dir . --template full --name "ESP32-S3 mini (offline e2e)" >/dev/null
CONN="--connectivity $E/sch-905bb85957eaf435.json --connectivity $E/sch-950ae6609e91d753.json"
esp32_round() { # $1 = round dir, $2 = values file, $3 = report version label
  local d=$1 v=$2
  mkdir -p "$d"
  log "esp32 $d"
  step "$d-intent"  P intent derive $CONN --values "$v" --models-lib "$R/power-models.json" --no-analog --out "$d/intent.json" --report "$d/intent.md"
  step "$d-sim"     P sim power $CONN --values "$v" --models-lib "$R/power-models.json" --out "$d/sim.json" --report "$d/sim.md"
  step "$d-analog"  P sim analog $CONN --values "$v" --models-lib "$R/power-models.json" --analog-models "$R/spice-models/analog-models.json" --out "$d/analog.json" --report "$d/analog.md" --work-dir "$d/analog-ngspice"
  # 4 layers per the customer requirement (esp32MiniRequire.md §一); the engine places (P5) then routes.
  step "$d-auto"    P pcb auto run --board "$B/board.json" --mech "$B/mech.json" --groups "$B/groups1.json" --intent "$d/intent.json" --sim "$d/sim.json" --out-dir "$d/auto" --timeout "$TO" --feedback-verify 0 --layers 4 --place --seed 1
  step "$d-check"   P pcb check --board "$d/auto/board.routed.json" --intent "$d/intent.json" --json
  cp "$W/logs/$d-check.out" "$d/check.json" 2>/dev/null || true
  step "$d-post"    P sim post-layout --board "$d/auto/board.routed.json" --sim "$d/sim.json" --intent "$d/intent.json" --plan "$d/auto/plan.json" --models-lib "$R/power-models.json" --out "$d/post.json" --report "$d/post.md" --svg-dir "$d/heatmaps"
  step "$d-report"  P report design --out-dir reports/esp32-mini --project-name "ESP32-S3 mini" --intent "$d/intent.json" --sim "$d/sim.json" --analog "$d/analog.json" --plan-dir "$d/auto" --board "$d/auto/board.routed.json" --check "$d/check.json" --post "$d/post.json" --models "$R/power-models.json" --no-zip
}
esp32_round round1 "$E/values.json"
# Round 2: what-if — LED1 series resistor R9 1kΩ → 330Ω (more LED current),
# the kind of value change an agent proposes and the user approves.
python3 - "$E/values.json" "$W/values-round2.json" <<'PY'
import json, sys
v = json.load(open(sys.argv[1]))
parts = v.get("parts", v)
parts["R9"]["value"] = "330Ω"
parts["R9"]["mpn"] = "0402WGF3300TCE"
json.dump(v, open(sys.argv[2], "w"), ensure_ascii=False, indent=1)
PY
esp32_round round2 "$W/values-round2.json"
P console projects add . --name "ESP32-S3 mini (offline e2e)" >/dev/null || true

# ── HV flyback (mains → SELV) ───────────────────────────────────────────────
H="$ROOT/testdata/stress/hv/flyback"
W="$OUT/hv-flyback"; mkdir -p "$W/logs"; cd "$W"
P project-config init --dir . --template full --name "Flyback mains→SELV (HV stress)" >/dev/null
log "hv-flyback"
step intent  P intent derive --connectivity "$H/connectivity.json" --values "$H/values.json" --spec "$H/spec.json" --models "$H/models.json" --models-lib "$R/power-models.json" --no-analog --out intent.json --report intent.md
step sim     P sim power --connectivity "$H/connectivity.json" --values "$H/values.json" --models "$H/models.json" --models-lib "$R/power-models.json" --out sim.json --report sim.md
step analog  P sim analog --connectivity "$H/connectivity.json" --values "$H/values.json" --models "$H/models.json" --models-lib "$R/power-models.json" --analog-models "$R/spice-models/analog-models.json" --out analog.json --report analog.md --work-dir analog-ngspice
step auto    P pcb auto run --board "$H/board.json" --intent intent.json --sim sim.json --out-dir auto --timeout "$TO" --feedback-verify 0 --place --seed 1
step check   P pcb check --board auto/board.routed.json --intent intent.json --json
cp logs/check.out check.json 2>/dev/null || true
step post    P sim post-layout --board auto/board.routed.json --sim sim.json --intent intent.json --plan auto/plan.json --models-lib "$R/power-models.json" --out post.json --report post.md --svg-dir heatmaps
step report  P report design --out-dir reports/flyback --project-name "Flyback mains→SELV" --intent intent.json --sim sim.json --analog analog.json --plan-dir auto --board auto/board.routed.json --check check.json --post post.json --models "$R/power-models.json" --no-zip
P console projects add . --name "Flyback mains→SELV (HV stress)" >/dev/null || true

# ── HV isolation violation (must FAIL) ──────────────────────────────────────
A="$ROOT/pkg/pcbauto/testdata"
W="$OUT/hv-iso-fail"; mkdir -p "$W/logs"; cd "$W"
P project-config init --dir . --template quick-proto --name "Mains/SELV isolation violation" >/dev/null
cp "$A/iso-mains-selv.intent.json" intent.json
log "hv-iso-fail"
step check   P pcb check --board "$A/iso-violation.dump.json" --intent intent.json --json
cp logs/check.out check.json 2>/dev/null || true
step report  P report design --out-dir reports/iso-fail --project-name "Mains/SELV isolation violation" --intent intent.json --board "$A/iso-violation.dump.json" --check check.json --no-zip
P console projects add . --name "Mains/SELV isolation violation" >/dev/null || true

log "done — work dirs under $OUT"
for d in esp32 hv-flyback hv-iso-fail; do
  printf '%-12s ' "$d"; ls "$OUT/$d"/reports/*/v*/manifest.json 2>/dev/null | wc -l | tr -d ' '; done
