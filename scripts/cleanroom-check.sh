#!/usr/bin/env bash
# cleanroom-check.sh — provenance tripwire for the router rewrite
# (docs/router-cleanroom/CLEANROOM.md §6). Run by `make cleanroom-check` and by
# reviewers before merging a router-v2 branch.
#
#   scripts/cleanroom-check.sh            # commits in dev..HEAD
#   scripts/cleanroom-check.sh A..B       # any commit range
#
# Checks (failures exit 1; check 4 only warns):
#   1. every commit in the range that touches a guarded path carries the
#      Clean-room:, Clean-room-sources: and Clean-room-attest: trailers;
#   2. guarded files at HEAD have no GPL licence header, and guarded Go packages
#      import only the standard library, this module and allowlisted modules;
#   3. guarded Go files do not name a forbidden project (pkg/routerbench and
#      cmd/routerbench may say "fastroute"). A tripwire, not proof;
#   4. a commit adding more than 1000 lines to guarded paths is listed for
#      manual CRO review.
# The script reads only this repository (git objects and the working tree).
set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo"

range="${1:-dev..HEAD}"
guarded=(pkg/pcbroute pkg/routerbench cmd/routerbench)
bench='^(pkg|cmd)/routerbench/'
# Modules outside this one that guarded code may import (MIT/BSD/Apache-2.0
# only). Adding one is a CRO decision recorded in the commit.
allowed_modules=()
deny='fastroute|freerouting|kicad|easyeda-pcb-router'
bulk_limit=1000

fail=0
err() { echo "FAIL: $*"; fail=1; }

# 1 + 4: per-commit trailers and bulk drops.
commits=$(git rev-list --reverse "$range" -- "${guarded[@]}")
for c in $commits; do
	short=$(git rev-parse --short "$c")
	for key in Clean-room Clean-room-sources Clean-room-attest; do
		val=$(git log -1 --format="%(trailers:key=$key,valueonly,separator=%x2C)" "$c" | tr -d '[:space:]')
		[ -n "$val" ] || err "$short has no $key: trailer"
	done
	added=$(git show --numstat --format= "$c" -- "${guarded[@]}" | awk '$1 != "-" { n += $1 } END { print n + 0 }')
	if [ "$added" -gt "$bulk_limit" ]; then
		echo "REVIEW: $short adds $added lines to guarded paths (> $bulk_limit); needs manual CRO review"
	fi
done

# 2: licence headers and imports (HEAD state of the working tree).
files=$(git ls-files -- "${guarded[@]}")
if [ -n "$files" ]; then
	gpl=$(echo "$files" | xargs grep -lE 'GNU (Lesser |Affero )?General Public License|SPDX-License-Identifier:.*GPL' || true)
	for f in $gpl; do err "$f has a GPL licence header"; done

	pkgs=()
	for d in "${guarded[@]}"; do
		[ -d "$d" ] && pkgs+=("./$d/...")
	done
	main=$(go list -m)
	mods=$(go list -deps -test -f '{{if and (not .Standard) .Module}}{{.Module.Path}}{{end}}' "${pkgs[@]}" | sort -u)
	for m in $mods; do
		[ "$m" = "$main" ] && continue
		ok=0
		for a in "${allowed_modules[@]+"${allowed_modules[@]}"}"; do
			[ "$m" = "$a" ] && ok=1
		done
		[ "$ok" = 1 ] || err "guarded code imports module $m, which is not on the allowlist"
	done

	# 3: forbidden project names in Go code and comments.
	for f in $(echo "$files" | grep '\.go$' || true); do
		pat="$deny"
		[[ "$f" =~ $bench ]] && pat='freerouting|kicad|easyeda-pcb-router'
		hits=$(grep -niE "$pat" "$f" || true)
		[ -z "$hits" ] || err "$f names a forbidden project:"$'\n'"$hits"
	done
fi

n=$(echo "$commits" | grep -c . || true)
if [ "$fail" = 0 ]; then
	echo "cleanroom-check: OK ($n guarded commit(s) in $range)"
else
	echo "cleanroom-check: FAILED ($n guarded commit(s) in $range)"
fi
exit "$fail"
