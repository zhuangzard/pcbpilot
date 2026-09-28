#!/usr/bin/env bash
# Regenerates docs/examples/analog-frontend/ (offline; needs ngspice on PATH).
# Run from the repository root:  bash docs/examples/analog-frontend/run.sh
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
BIN=${PCBPILOT:-pcbpilot}
export PCBPILOT_SKILLS_DIR="$ROOT/.agents/skills"   # the repo's Skill data, not an installed copy
export SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-1790640000}
cd "$ROOT/docs/examples/analog-frontend"
rm -rf analog.json analog.md plan.json plots analog-ngspice intent.json intent.md sim.json reports after esp32-mini
F=../../../testdata/analog/frontend
R=../../../.agents/skills/pcbpilot/references   # relative paths keep the recorded provenance portable
LIBS="--analog-models $R/spice-models/analog-models.json --models-lib $R/power-models.json --parts $R/standard-parts.json"

# 1. analog SPICE: detect → ngspice → targets → Monte-Carlo → optimise → plan
"$BIN" sim analog $LIBS --connectivity $F/sch-frontend.json --values $F/values.json --spec $F/spec.json \
    --out analog.json --report analog.md --plots-dir plots --apply-plan plan.json

# 2. design intent (analog findings included) + design report v1 with §3A
"$BIN" intent derive --models-lib $R/power-models.json --connectivity $F/sch-frontend.json --values $F/values.json --analog analog.json \
    --out intent.json --report intent.md --sim-out sim.json \
    --report-dir reports/analog-frontend --report-name "ADC front-end (analog fixture)"

# 3. what-if: the plan's values applied in memory (the schematic is not touched)
"$BIN" sim analog $LIBS --connectivity $F/sch-frontend.json --values $F/values.json --spec $F/spec.json \
    --what-if plan.json --no-optimise --work-dir none \
    --out after/analog.json --report after/analog.md --plots-dir after/plots

# 4. the ESP32 mini regression board (little analog: EN RC, auto-reset BJTs, buck FB)
E=../../../pkg/powersim/testdata/esp32mini
mkdir -p esp32-mini
"$BIN" sim analog $LIBS --connectivity $E/sch-905bb85957eaf435.json --connectivity $E/sch-950ae6609e91d753.json \
    --values $E/values.json --out esp32-mini/analog.json --report esp32-mini/analog.md --plots-dir esp32-mini/plots
