#!/bin/bash
# 挪乱→观察→playbook 回放恢复 演示(esp32-mini 板,工程默认 ceshi)
#
#   PROJECT=ceshi PAUSE=30 STEP_DELAY=1.2 bash examples/esp32-mini/demo-replay.sh
#   或:make demo-replay [PROJECT=ceshi]
#
# 器件 id 按位号实时查(不硬编码 primitiveId,重建过的板子也能跑)。
# 回放用 examples/esp32-mini/moves.playbook.json 的 s7-s24 移件区间(幂等)。
set -euo pipefail
cd "$(dirname "$0")/../.."

PROJECT="${PROJECT:-ceshi}"
PAUSE="${PAUSE:-30}"            # 挪乱后的观察秒数
STEP_DELAY="${STEP_DELAY:-1.2}" # 回放逐步间隔
P="--project $PROJECT"
PB=examples/esp32-mini/moves.playbook.json

N() { pcbpilot notify --message "$1" --type "$2" $P >/dev/null 2>&1 || true; }

# 按位号查 primitiveId
ids=$(pcbpilot pcb list $P 2>/dev/null | python3 -c '
import sys, json
want = {"LED1": "1900,1300", "SW1": "600,300", "C1": "2450,900", "R1": "1600,1200"}
d = json.load(sys.stdin)
for c in d["result"]["components"]:
    des = c.get("designator")
    if des in want:
        print(des, c["primitiveId"], want[des])
')
if [ -z "$ids" ]; then
  echo "找不到 LED1/SW1/C1/R1 —— 目标工程是 esp32-mini 板吗?(PROJECT=$PROJECT)" >&2
  exit 1
fi

N "🎬 回放演示:5 秒后挪乱 LED1 / SW1 / C1 / R1" info
sleep 5
while read -r des pid xy; do
  x="${xy%,*}"; y="${xy#*,}"
  pcbpilot pcb modify --id "$pid" --patch "{\"x\":$x,\"y\":$y}" $P >/dev/null 2>&1 \
    && echo "挪乱 $des -> ($x,$y)"
done <<< "$ids"
N "💥 已挪乱 —— ${PAUSE} 秒观察期,然后用录制的 playbook 逐步恢复" warning
sleep "$PAUSE"

N "▶️ 回放开始:18 步移件,每步间隔 ${STEP_DELAY}s" info
sleep 2
pcbpilot apply "$PB" --from 7 --to 24 --step-delay "$STEP_DELAY" $P

LINT=$(pcbpilot pcb layout-lint $P 2>&1 | head -1 | grep -o 'score [0-9]*/100' || true)
N "✅ 回放完成:${LINT:-lint 见终端},已存盘" success
pcbpilot pcb save $P >/dev/null 2>&1
echo "✓ 恢复完成 ${LINT:-}"
