#!/usr/bin/env bash
# install-fastroute.sh — OPT-IN install of fastroute, the external autorouter
# behind `pcbpilot pcb autoroute --router fastroute`.
#
#   scripts/install-fastroute.sh                 # 0.1.7 → ~/.local/bin, asks first
#   scripts/install-fastroute.sh --version 0.1.7 --dest ~/bin --yes
#   scripts/install-fastroute.sh --dry-run
#
# fastroute (https://github.com/parisxmas/fastroute, a Rust port of
# Freerouting) is GPLv3. pcbpilot (MIT) only runs it as a separate process; it
# is never bundled, linked, or downloaded by pcbpilot itself, and setup-agent.sh
# does not call this script. Running it is your consent to download one release
# asset from GitHub. The asset is verified against the release SHA256SUMS.txt
# before anything is installed.
#
# macOS (arm64/x64) and Linux (x64/arm64). Windows: see the manual steps in
# .agents/skills/pcbpilot/references/pcb-routing.md (section fastroute).
set -euo pipefail

version=0.1.7
dest="$HOME/.local/bin"
yes=0
dry=0
while [ $# -gt 0 ]; do
	case "$1" in
	--version) version="$2"; shift 2 ;;
	--dest) dest="$2"; shift 2 ;;
	--yes) yes=1; shift ;;
	--dry-run) dry=1; shift ;;
	-h | --help) sed -n '2,17p' "$0"; exit 0 ;;
	*) echo "unknown option: $1" >&2; exit 2 ;;
	esac
done

case "$(uname -s)" in
Darwin) os=macos ;;
Linux) os=linux ;;
*) echo "unsupported OS $(uname -s); install manually (see pcb-routing.md)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
arm64 | aarch64) arch=arm64 ;;
x86_64 | amd64) arch=x64 ;;
*) echo "unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

asset="fastroute-${version}-${os}-${arch}.tar.gz"
base="https://github.com/parisxmas/fastroute/releases/download/v${version}"
echo "fastroute ${version} (GPLv3) for ${os}-${arch}"
echo "  download: ${base}/${asset}"
echo "  verify:   ${base}/SHA256SUMS.txt"
echo "  install:  ${dest}/fastroute"
[ "$dry" = 1 ] && exit 0
if [ "$yes" != 1 ]; then
	printf "Download and install? [y/N] "
	read -r answer
	case "$answer" in y | Y | yes) ;; *) echo "aborted"; exit 1 ;; esac
fi

if command -v sha256sum >/dev/null 2>&1; then
	sha() { sha256sum "$1" | cut -d' ' -f1; }
else
	sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$asset" "$base/$asset"
curl -fsSL -o "$tmp/SHA256SUMS.txt" "$base/SHA256SUMS.txt"
want="$(awk -v a="$asset" '$2 == a || $2 == "*"a {print $1}' "$tmp/SHA256SUMS.txt")"
got="$(sha "$tmp/$asset")"
if [ -z "$want" ] || [ "$want" != "$got" ]; then
	echo "checksum mismatch for $asset: SHA256SUMS.txt says '${want:-<missing>}', file is $got — not installing" >&2
	exit 1
fi
echo "checksum OK ($got)"

tar -xzf "$tmp/$asset" -C "$tmp"
mkdir -p "$dest"
install -m 0755 "$tmp/fastroute-${version}-${os}-${arch}/fastroute" "$dest/fastroute"
"$dest/fastroute" --version || true
case ":$PATH:" in *":$dest:"*) ;; *) echo "note: $dest is not on PATH; set FASTROUTE_BIN=$dest/fastroute or pass --fastroute-bin" ;; esac
