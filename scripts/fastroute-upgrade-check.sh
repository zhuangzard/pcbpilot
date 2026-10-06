#!/usr/bin/env bash
# fastroute-upgrade-check.sh — check for a new fastroute release and adopt it
# only after it passes pcbpilot's compatibility checks. Run by a scheduled
# task every 3 days (user decision 2026-10-06); safe to run by hand.
#
#   scripts/fastroute-upgrade-check.sh            # check, download, verify, test, adopt
#   scripts/fastroute-upgrade-check.sh --dry-run  # only report the latest release
#
# Steps (any failure keeps the current version and exits non-zero):
#   1. latest release tag of github.com/parisxmas/fastroute vs the adopted one
#      (~/.pcbpilot/fastroute/current → the binary in use)
#   2. download + SHA256SUMS.txt verification (scripts/install-fastroute.sh)
#      into ~/.pcbpilot/fastroute/<version>/
#   3. interface check: every option pcbpilot passes is still in --help; the
#      full --help diff against the adopted version is saved for review
#   4. go test with PCBPILOT_FASTROUTE_LIVE=<new binary>: routes the EasyEDA
#      export fixture through dsn-fix + intent requirements and reads the
#      session and report the way pcbpilot does, plus the offline suites
#   5. adopt: ~/.pcbpilot/fastroute/current → new binary
# Report: ~/.pcbpilot/fastroute/check-<date>.log
#
# fastroute is GPLv3 and stays a separate process; nothing is vendored.
set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
state="$HOME/.pcbpilot/fastroute"
mkdir -p "$state"
log="$state/check-$(date +%Y%m%d-%H%M).log"
exec > >(tee "$log") 2>&1
dry=0
[ "${1:-}" = "--dry-run" ] && dry=1

current_bin=""
if [ -L "$state/current" ] || [ -e "$state/current" ]; then
	current_bin="$(readlink "$state/current" || true)"
fi
[ -z "$current_bin" ] && current_bin="${FASTROUTE_BIN:-$(command -v fastroute || true)}"
current_ver=""
[ -n "$current_bin" ] && [ -x "$current_bin" ] && current_ver="$("$current_bin" --version 2>/dev/null | awk '{print $NF}')"

latest="$(curl -fsSL https://api.github.com/repos/parisxmas/fastroute/releases/latest | python3 -c 'import sys,json; print(json.load(sys.stdin)["tag_name"].lstrip("v"))')"
echo "fastroute adopted: ${current_ver:-none} (${current_bin:-not found}); latest release: $latest"
if [ "$latest" = "$current_ver" ]; then
	echo "up to date"
	exit 0
fi
[ "$dry" = 1 ] && exit 0

dest="$state/$latest"
"$repo/scripts/install-fastroute.sh" --version "$latest" --dest "$dest" --yes
new="$dest/fastroute"

# Interface check: options pcbpilot passes (internal/app/cmd_pcb_fastroute.go fastrouteArgs).
help="$("$new" --help 2>&1 || true)"
missing=0
for opt in -de -do --report --diagnose --multi-start --initial-session --max-time --pairs --tune \
	--no-neckdown-classes router.min_trace_width_um router.autorouter.max_threads router.optimizer.max_threads; do
	if ! grep -q -- "$opt" <<<"$help"; then
		echo "INTERFACE CHANGE: option $opt not in fastroute $latest --help"
		missing=1
	fi
done
if [ -n "$current_bin" ] && [ -x "$current_bin" ]; then
	diff <("$current_bin" --help 2>&1 || true) <(echo "$help") > "$state/help-diff-$current_ver-$latest.txt" || true
	echo "--help diff saved: $state/help-diff-$current_ver-$latest.txt"
fi
if [ "$missing" = 1 ]; then
	echo "NOT ADOPTED: fastroute $latest changed an option pcbpilot uses; update fastrouteArgs first"
	exit 1
fi

# Behaviour check with the new binary.
cd "$repo"
if ! PCBPILOT_FASTROUTE_LIVE="$new" go test -short -count=1 ./internal/pcb/specctra ./internal/app \
	-run 'TestFastrouteLive|Fastroute|PrepareDSN|CrashRetry|Escapes|IntentWidths|Requirements|Reconcile|ImportRepair'; then
	echo "NOT ADOPTED: compatibility tests failed with fastroute $latest"
	exit 1
fi

ln -sfn "$new" "$state/current"
echo "ADOPTED fastroute $latest: $state/current -> $new (previous: ${current_ver:-none})"
