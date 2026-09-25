#!/usr/bin/env bash
# setup-agent.sh — one-shot, idempotent setup of pcbpilot on a new machine,
# run from a clone of https://github.com/zhuangzard/pcbpilot.
#
#   git clone https://github.com/zhuangzard/pcbpilot.git && cd pcbpilot
#   scripts/setup-agent.sh            # source build (Go + Node available)
#   scripts/setup-agent.sh --release  # released CLI/Skill via install.sh instead
#   scripts/setup-agent.sh --dry-run  # print what would happen
#
# It installs everything an AI client (Claude Code / Codex) needs:
#   1. pcbpilot CLI + daemon  → ~/.local/bin/pcbpilot (no sudo)
#   2. Skills (pcbpilot + repo lookup/maintain) → symlinked into ~/.claude/skills,
#      ~/.codex/skills and ~/.agents/skills, so `git pull` updates them
#   3. MCP stdio server → npm deps + `claude mcp add` / `codex mcp add` (user scope)
#   4. Connector .eext  → built at the repo version; importing it into EasyEDA is a
#      HUMAN step (the extension manager has no API, and GUI automation is not
#      allowed): uninstall the old "PCB Pilot Connector", import the printed file,
#      enable Advanced → Extension manager → Installed → PCB Pilot Connector
#      (Enabled) → Config → Allow external interaction, reload the editor.
#   5. daemon as a login service (macOS launchd / Linux systemd --user; skipped
#      when a healthy daemon already runs, e.g. `make dev`) + `pcbpilot health`
#
# Step 0 keeps an existing upstream easyeda-agent install from interfering:
#   default          remove upstream MCP registrations ("easyeda-agent", "easyeda")
#                    from Claude Code and Codex, and MOVE upstream skill dirs
#                    (easyeda-agent, easyeda-design-workflow, any skill named easyeda*)
#                    out of the client skill roots into ~/.pcbpilot/upstream-backup/<ts>/
#                    (nothing is deleted; RESTORE.txt in that dir says how to undo).
#                    The upstream CLI `easyeda`, its daemon (ports 60832-60841) and its
#                    "EDA Agent Connector" do not collide with pcbpilot and are left alone.
#   --purge-upstream also stop the upstream daemon and move its CLI + data dir to the backup
#   --keep-upstream  skip step 0
#   --no-service     do not install the daemon login service (start it yourself:
#                    `pcbpilot daemon start`, which blocks)
#
# Environment: PCBPILOT_BIN_DIR (default ~/.local/bin), CLAUDE_CONFIG_DIR, CODEX_HOME.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="${PCBPILOT_BIN_DIR:-$HOME/.local/bin}"
MODE=source
DRY=0
UPSTREAM=clean
SERVICE=1
for a in "$@"; do
  case "$a" in
    --release) MODE=release ;;
    --dry-run) DRY=1 ;;
    --keep-upstream) UPSTREAM=keep ;;
    --purge-upstream) UPSTREAM=purge ;;
    --no-service) SERVICE=0 ;;
    -h|--help) sed -n '2,37p' "$0"; exit 0 ;;
    *) echo "unknown option: $a" >&2; exit 2 ;;
  esac
done

say()  { printf '\033[1m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m!!\033[0m %s\n' "$*" >&2; }
run()  { if [ "$DRY" = 1 ]; then printf '   $ %s\n' "$*"; else "$@"; fi; }
have() { command -v "$1" >/dev/null 2>&1; }

[ -f "$REPO/AGENTS.md" ] && [ -d "$REPO/.agents/skills/pcbpilot" ] || { echo "run from a pcbpilot clone" >&2; exit 1; }

# 0. Upstream easyeda-agent -------------------------------------------------
if [ "$UPSTREAM" != keep ]; then
  BK="$HOME/.pcbpilot/upstream-backup/$(date +%Y%m%d-%H%M%S)"
  found=0
  note() { found=1; run mkdir -p "$BK"; if [ "$DRY" = 0 ]; then printf '%s\n' "$*" >> "$BK/RESTORE.txt"; fi; }
  say "Retiring upstream easyeda-agent MCP registrations (Claude Code / Codex / ZCode / ~/.agents)"
  if [ "$DRY" = 1 ]; then
    python3 "$REPO/scripts/agent_clients.py" clean-upstream --backup "$BK" --dry-run
  else
    out="$(python3 "$REPO/scripts/agent_clients.py" clean-upstream --backup "$BK")"; printf '%s\n' "$out"
    [ -n "$out" ] && found=1
  fi
  for root in "${CLAUDE_CONFIG_DIR:-$HOME/.claude}/skills" "${CODEX_HOME:-$HOME/.codex}/skills" "$HOME/.agents/skills" "$HOME/.zcode/skills"; do
    [ -d "$root" ] || continue
    for dst in "$root"/*; do
      [ -e "$dst" ] || [ -L "$dst" ] || continue
      base="$(basename "$dst")"
      case "$base" in pcbpilot*) continue ;; esac
      # A dangling link (its target moved earlier in this loop) has no SKILL.md.
      sname="$( { sed -n 's/^name:[[:space:]]*//p' "$dst/SKILL.md" 2>/dev/null || true; } | head -1 | tr -d '"'"'"' ')"
      case "$base|$sname" in
        easyeda-agent\|*|easyeda-design-workflow\|*|*\|easyeda*) ;;
        *) continue ;;
      esac
      tag="$(basename "$(dirname "$root")")"
      say "Moving upstream skill $dst → $BK/skills-$tag/"
      note "mv \"$BK/skills-$tag/$base\" \"$dst\""
      run mkdir -p "$BK/skills-$tag"
      run mv "$dst" "$BK/skills-$tag/$base"
    done
  done
  if [ "$UPSTREAM" = purge ]; then
    if have easyeda; then
      UP="$(command -v easyeda)"
      say "Stopping the upstream daemon and moving $UP to the backup"
      run "$UP" daemon stop >/dev/null 2>&1 || run pkill -f "$UP daemon" || true
      note "mv \"$BK/bin/easyeda\" \"$UP\""
      run mkdir -p "$BK/bin"
      run mv "$UP" "$BK/bin/easyeda"
    fi
    for d in "$HOME/.local/share/easyeda-agent"; do
      if [ -d "$d" ]; then
        note "mv \"$BK/share/easyeda-agent\" \"$d\""
        run mkdir -p "$BK/share"
        run mv "$d" "$BK/share/easyeda-agent"
      fi
    done
  fi
  if [ "$found" = 1 ]; then
    say "Upstream leftovers handled; undo steps in $BK/RESTORE.txt"
  else
    say "No upstream easyeda-agent MCP/skills found"
  fi
  if have easyeda && [ "$UPSTREAM" != purge ]; then
    warn "upstream CLI $(command -v easyeda) kept (ports 60832-60841, no conflict); --purge-upstream moves it away"
  fi
fi

# 1. CLI --------------------------------------------------------------------
if [ "$MODE" = source ] && ! have go; then
  warn "Go not found — falling back to the released CLI/Skill (install.sh)"
  MODE=release
fi
if [ "$MODE" = source ]; then
  say "Building the CLI from source → $BIN_DIR/pcbpilot"
  run mkdir -p "$BIN_DIR"
  run make -C "$REPO" build
  run install -m 0755 "$REPO/bin/pcbpilot" "$BIN_DIR/pcbpilot"
else
  say "Installing the released CLI + Skill (install.sh)"
  run env PCBPILOT_INSTALL_DIR="$BIN_DIR" PCBPILOT_INSTALL_SKILLS=none bash "$REPO/install.sh"
fi
PCB="$BIN_DIR/pcbpilot"
case ":$PATH:" in *":$BIN_DIR:"*) ;; *) warn "$BIN_DIR is not on PATH — add: export PATH=\"$BIN_DIR:\$PATH\"" ;; esac

# 2. Skills (symlinks into the clone) --------------------------------------
say "Linking Skills into the AI clients"
roots=("${CLAUDE_CONFIG_DIR:-$HOME/.claude}/skills" "${CODEX_HOME:-$HOME/.codex}/skills" "$HOME/.agents/skills")
[ -d "$HOME/.zcode" ] && roots+=("$HOME/.zcode/skills")   # ZCode reads ~/.zcode/skills too
for root in "${roots[@]}"; do
  run mkdir -p "$root"
  for s in pcbpilot pcbpilot-repo-lookup pcbpilot-repo-maintain; do
    src="$REPO/.agents/skills/$s"
    [ -d "$src" ] || continue
    dst="$root/$s"
    if [ -L "$dst" ] || [ ! -e "$dst" ]; then
      run ln -sfn "$src" "$dst"
    else
      warn "$dst exists and is not a symlink — left alone (move it away to link the clone)"
    fi
  done
done

# 3. MCP --------------------------------------------------------------------
if have node && have npm; then
  say "Installing MCP dependencies"
  run npm --prefix "$REPO/mcp" ci --ignore-scripts --no-audit --no-fund
  NODE="$(command -v node)"
  SERVER="$REPO/mcp/src/server.mjs"
  say "Registering the pcbpilot MCP server with every AI client found (Claude Code / Codex / ZCode / ~/.agents)"
  DRYFLAG=""; [ "$DRY" = 1 ] && DRYFLAG="--dry-run"
  python3 "$REPO/scripts/agent_clients.py" register --bin "$PCB" --node "$NODE" --server "$SERVER" $DRYFLAG
else
  warn "Node.js (>= 20.17) not found — MCP skipped (the CLI + Skill work without it)"
fi

# 4. Connector ----------------------------------------------------------------
EEXT=""
if have node && have npm && [ "$MODE" = source ]; then
  say "Building the connector .eext at the repo version"
  if [ ! -d "$REPO/extension/node_modules" ]; then
    run npm --prefix "$REPO/extension" ci --no-audit --no-fund
  fi
  if run make -C "$REPO" connector; then
    EEXT="$(ls -t "$REPO"/extension/build/dist/pcbpilot-connector_v*.eext 2>/dev/null | head -1 || true)"
  else
    warn "connector build failed — use the release .eext instead"
    EEXT="https://github.com/zhuangzard/pcbpilot/releases/latest (pcbpilot-connector.eext)"
  fi
else
  EEXT="https://github.com/zhuangzard/pcbpilot/releases/latest (pcbpilot-connector.eext)"
fi

CONN_VER="$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' "$REPO/extension/extension.json" | head -1)"

# 5. Daemon (login service) + health --------------------------------------------
if [ "$DRY" = 0 ] && "$PCB" daemon health >/dev/null 2>&1; then
  say "A pcbpilot daemon is already running — left as is"
elif [ "$SERVICE" = 1 ] && [ "$(uname)" = Darwin ]; then
  PL="$HOME/Library/LaunchAgents/com.pcbpilot.daemon.plist"
  say "Installing the daemon login service ($PL)"
  if [ "$DRY" = 1 ]; then
    printf '   $ write %s; launchctl bootstrap gui/%s %s\n' "$PL" "$(id -u)" "$PL"
  else
    mkdir -p "$HOME/Library/LaunchAgents" "$HOME/.pcbpilot"
    cat > "$PL" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.pcbpilot.daemon</string>
  <key>ProgramArguments</key><array><string>$PCB</string><string>daemon</string><string>start</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$HOME/.pcbpilot/daemon.log</string>
  <key>StandardErrorPath</key><string>$HOME/.pcbpilot/daemon.log</string>
</dict></plist>
PLIST
    launchctl bootout "gui/$(id -u)/com.pcbpilot.daemon" >/dev/null 2>&1 || true
    launchctl bootstrap "gui/$(id -u)" "$PL" || warn "launchctl bootstrap failed — start manually: $PCB daemon start"
  fi
elif [ "$SERVICE" = 1 ] && have systemctl; then
  UNIT="$HOME/.config/systemd/user/pcbpilot-daemon.service"
  say "Installing the daemon login service ($UNIT)"
  if [ "$DRY" = 1 ]; then
    printf '   $ write %s; systemctl --user enable --now pcbpilot-daemon\n' "$UNIT"
  else
    mkdir -p "$(dirname "$UNIT")"
    printf '[Unit]\nDescription=pcbpilot daemon\n\n[Service]\nExecStart=%s daemon start\nRestart=on-failure\n\n[Install]\nWantedBy=default.target\n' "$PCB" > "$UNIT"
    systemctl --user daemon-reload && systemctl --user enable --now pcbpilot-daemon || warn "systemd --user failed — start manually: $PCB daemon start"
  fi
else
  say "Starting the daemon in the background (log: ~/.pcbpilot/daemon.log)"
  if [ "$DRY" = 1 ]; then printf '   $ nohup %s daemon start &\n' "$PCB"; else
    mkdir -p "$HOME/.pcbpilot"; nohup "$PCB" daemon start >> "$HOME/.pcbpilot/daemon.log" 2>&1 &
  fi
fi
if [ "$DRY" = 0 ]; then
  for _ in 1 2 3 4 5 6 7 8 9 10; do "$PCB" daemon health >/dev/null 2>&1 && break; sleep 1; done
  "$PCB" daemon health >/dev/null 2>&1 && say "daemon healthy" || warn "daemon not answering yet — see ~/.pcbpilot/daemon.log"
fi

VERIFY_RC=0
if [ "$DRY" = 0 ] && have node; then
  say "Verifying: every client has the pcbpilot MCP, no upstream entries, skills resolve, MCP handshake"
  python3 "$REPO/scripts/agent_clients.py" verify --bin "$PCB" --server "$REPO/mcp/src/server.mjs" --repo "$REPO" || VERIFY_RC=$?
fi

cat <<EOF

────────────────────────────────────────────────────────────────────────────
 pcbpilot is installed. ONE step needs a human (no API / no GUI automation):

   1. EasyEDA Pro → 高级/Advanced → 扩展管理器/Extension manager → 已安装/Installed:
      uninstall any older "PCB Pilot Connector" (same uuid imports silently fail;
      leave upstream "EDA Agent Connector" alone — it uses other ports)
   2. Import: ${EEXT}
   3. Select "PCB Pilot Connector" → status Enabled → Config tab →
      tick 允许外部交互 / Allow interactive with external
   4. Reload the editor (Web: refresh the page; desktop: restart EasyEDA).
      Desktop 3.2.149 may need the import again after every EasyEDA restart.

 Then check:   $PCB health
                windows[] must list your project + document, connectorVersion = ${CONN_VER}
 Restart Claude Code / Codex so they pick up the Skill and the MCP server.
────────────────────────────────────────────────────────────────────────────
EOF
exit "$VERIFY_RC"
