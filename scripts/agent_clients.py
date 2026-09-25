#!/usr/bin/env python3
"""Register pcbpilot with every AI client on this machine, retire upstream
easyeda-agent entries, and verify the result.

Clients and where their user-level config lives:
  claude  ~/.claude.json            "mcpServers"   (the `claude mcp` CLI when present)
  codex   ~/.codex/config.toml      [mcp_servers.<name>] (+ .env table)
  zcode   ~/.zcode/cli/config.json  "mcp": {"servers": {...}}
  agents  ~/.agents/mcp.json        "mcpServers"   (only if the file already exists;
                                                     ZCode and others read it)
Skills are read from ~/.claude/skills, ~/.codex/skills, ~/.agents/skills and
~/.zcode/skills (setup-agent.sh links them; `verify` checks them).

  agent_clients.py clean-upstream --backup DIR [--dry-run]
  agent_clients.py register --bin PCBPILOT --node NODE --server SERVER.mjs [--dry-run]
  agent_clients.py verify --bin PCBPILOT --server SERVER.mjs --repo REPO

Every edited file is copied into the backup dir (clean-upstream) or next to
itself as <file>.bak-pcbpilot (register) before the first write.
"""
import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import time

HOME = os.path.expanduser("~")
UPSTREAM = ("easyeda-agent", "easyeda")
NAME = "pcbpilot"

CLAUDE_JSON = os.path.join(HOME, ".claude.json")
CODEX_TOML = os.path.join(os.environ.get("CODEX_HOME", os.path.join(HOME, ".codex")), "config.toml")
ZCODE_JSON = os.path.join(HOME, ".zcode", "cli", "config.json")
AGENTS_JSON = os.path.join(HOME, ".agents", "mcp.json")
SKILL_ROOTS = [
    os.path.join(os.environ.get("CLAUDE_CONFIG_DIR", os.path.join(HOME, ".claude")), "skills"),
    os.path.join(os.environ.get("CODEX_HOME", os.path.join(HOME, ".codex")), "skills"),
    os.path.join(HOME, ".agents", "skills"),
    os.path.join(HOME, ".zcode", "skills"),
]


def log(msg):
    print(f"   {msg}")


def have(cmd):
    return shutil.which(cmd) is not None


def backup(path, bdir):
    if bdir is None:
        dst = path + ".bak-pcbpilot"
        if not os.path.exists(dst):
            shutil.copy2(path, dst)
        return
    os.makedirs(bdir, exist_ok=True)
    shutil.copy2(path, os.path.join(bdir, os.path.basename(path).lstrip(".") + ".orig"))


def load_json(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def save_json(path, data):
    tmp = path + ".tmp-pcbpilot"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(data, f, indent=2, ensure_ascii=False)
        f.write("\n")
    os.replace(tmp, path)


# ── Codex TOML (line based: stdlib has no TOML writer) ─────────────────────
SECTION = re.compile(r"^\s*\[([^\]]+)\]\s*$")


def toml_sections(text):
    """Yield (header, start, end) line spans of every table."""
    lines = text.splitlines(keepends=True)
    heads = [(i, SECTION.match(l).group(1).strip()) for i, l in enumerate(lines) if SECTION.match(l)]
    out = []
    for k, (i, h) in enumerate(heads):
        end = heads[k + 1][0] if k + 1 < len(heads) else len(lines)
        out.append((h, i, end))
    return lines, out


def toml_has(text, name):
    _, secs = toml_sections(text)
    return any(h == f"mcp_servers.{name}" or h == f'mcp_servers."{name}"' for h, _, _ in secs)


def toml_drop(text, name):
    lines, secs = toml_sections(text)
    keep = [True] * len(lines)
    for h, s, e in secs:
        base = h.replace('"', "")
        if base == f"mcp_servers.{name}" or base.startswith(f"mcp_servers.{name}."):
            for i in range(s, e):
                keep[i] = False
    return "".join(l for l, k in zip(lines, keep) if k)


def toml_block(name, node, server, pbin):
    return (f'\n[mcp_servers.{name}]\ncommand = "{node}"\nargs = ["{server}"]\n\n'
            f'[mcp_servers.{name}.env]\nPCBPILOT_BIN = "{pbin}"\n')


# ── clean-upstream ─────────────────────────────────────────────────────────
def clean_upstream(bdir, dry):
    removed = []
    # claude
    if have("claude"):
        for n in UPSTREAM:
            if subprocess.run(["claude", "mcp", "get", n], capture_output=True).returncode == 0:
                removed.append(f"claude:{n}")
                if not dry:
                    os.makedirs(bdir, exist_ok=True)
                    with open(os.path.join(bdir, "RESTORE.txt"), "a") as f:
                        f.write(f"# Claude Code MCP {n} (was):\n")
                        f.write(subprocess.run(["claude", "mcp", "get", n], capture_output=True, text=True).stdout)
                    subprocess.run(["claude", "mcp", "remove", n], capture_output=True)
    elif os.path.exists(CLAUDE_JSON):
        d = load_json(CLAUDE_JSON)
        hit = [n for n in UPSTREAM if n in (d.get("mcpServers") or {})]
        if hit:
            removed += [f"claude:{n}" for n in hit]
            if not dry:
                backup(CLAUDE_JSON, bdir)
                for n in hit:
                    d["mcpServers"].pop(n)
                save_json(CLAUDE_JSON, d)
    # codex
    if os.path.exists(CODEX_TOML):
        text = open(CODEX_TOML, encoding="utf-8").read()
        hit = [n for n in UPSTREAM if toml_has(text, n)]
        if hit:
            removed += [f"codex:{n}" for n in hit]
            if not dry:
                backup(CODEX_TOML, bdir)
                for n in hit:
                    text = toml_drop(text, n)
                open(CODEX_TOML, "w", encoding="utf-8").write(text)
    # zcode + agents
    for label, path, getter in (("zcode", ZCODE_JSON, lambda d: (d.get("mcp") or {}).get("servers")),
                                ("agents", AGENTS_JSON, lambda d: d.get("mcpServers"))):
        if not os.path.exists(path):
            continue
        d = load_json(path)
        servers = getter(d) or {}
        hit = [n for n in UPSTREAM if n in servers]
        if hit:
            removed += [f"{label}:{n}" for n in hit]
            if not dry:
                backup(path, bdir)
                for n in hit:
                    servers.pop(n)
                save_json(path, d)
    for r in removed:
        log(("would remove " if dry else "removed ") + "upstream MCP " + r)
    if removed and not dry:
        with open(os.path.join(bdir, "RESTORE.txt"), "a") as f:
            f.write("# Edited config files were copied here as *.orig; copy them back to undo.\n")
    return removed


# ── register ───────────────────────────────────────────────────────────────
def register(pbin, node, server, dry):
    done = []
    entry = {"command": node, "args": [server], "env": {"PCBPILOT_BIN": pbin}}
    # claude
    if have("claude"):
        if not dry:
            subprocess.run(["claude", "mcp", "remove", NAME, "-s", "user"], capture_output=True)
            r = subprocess.run(["claude", "mcp", "add", NAME, "--scope", "user", "--env", f"PCBPILOT_BIN={pbin}",
                                "--", node, server], capture_output=True, text=True)
            if r.returncode != 0:
                print(r.stderr, file=sys.stderr)
                sys.exit("claude mcp add failed")
        done.append("claude (claude mcp add, user scope)")
    elif os.path.exists(CLAUDE_JSON):
        if not dry:
            backup(CLAUDE_JSON, None)
            d = load_json(CLAUDE_JSON)
            d.setdefault("mcpServers", {})[NAME] = dict(entry, type="stdio")
            save_json(CLAUDE_JSON, d)
        done.append(f"claude ({CLAUDE_JSON})")
    # codex
    if os.path.isdir(os.path.dirname(CODEX_TOML)):
        text = open(CODEX_TOML, encoding="utf-8").read() if os.path.exists(CODEX_TOML) else ""
        if not dry:
            if os.path.exists(CODEX_TOML):
                backup(CODEX_TOML, None)
            text = toml_drop(text, NAME).rstrip("\n") + "\n" + toml_block(NAME, node, server, pbin)
            open(CODEX_TOML, "w", encoding="utf-8").write(text)
        done.append(f"codex ({CODEX_TOML})")
    # zcode
    if os.path.isdir(os.path.dirname(ZCODE_JSON)):
        d = load_json(ZCODE_JSON) if os.path.exists(ZCODE_JSON) else {}
        if not dry:
            if os.path.exists(ZCODE_JSON):
                backup(ZCODE_JSON, None)
            d.setdefault("mcp", {}).setdefault("servers", {})[NAME] = dict(entry, type="stdio")
            save_json(ZCODE_JSON, d)
        done.append(f"zcode ({ZCODE_JSON})")
    # shared ~/.agents/mcp.json only when some client already uses it
    if os.path.exists(AGENTS_JSON):
        if not dry:
            backup(AGENTS_JSON, None)
            d = load_json(AGENTS_JSON)
            d.setdefault("mcpServers", {})[NAME] = dict(entry)
            save_json(AGENTS_JSON, d)
        done.append(f"agents ({AGENTS_JSON})")
    for x in done:
        log(("would register " if dry else "registered ") + x)
    return done


# ── verify ─────────────────────────────────────────────────────────────────
def mcp_handshake(node, server, pbin, timeout=30):
    """Start the server, initialize, list tools. Returns tool names."""
    env = dict(os.environ, PCBPILOT_BIN=pbin)
    p = subprocess.Popen([node, server], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                         stderr=subprocess.PIPE, text=True, env=env)
    def send(obj):
        p.stdin.write(json.dumps(obj) + "\n")
        p.stdin.flush()
    def recv(want_id):
        deadline = time.time() + timeout
        while time.time() < deadline:
            line = p.stdout.readline()
            if not line:
                break
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            if msg.get("id") == want_id:
                return msg
        raise RuntimeError("no MCP response (id %d)" % want_id)
    try:
        send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
            "protocolVersion": "2024-11-05", "capabilities": {},
            "clientInfo": {"name": "pcbpilot-setup-verify", "version": "1"}}})
        init = recv(1)
        if "error" in init:
            raise RuntimeError(init["error"])
        send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        send({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
        tools = recv(2)["result"]["tools"]
        return [t["name"] for t in tools]
    finally:
        p.kill()


def verify(pbin, server, repo):
    bad = []
    ok = []
    def check(cond, good, fail):
        (ok if cond else bad).append(good if cond else fail)
    # binaries
    check(os.access(pbin, os.X_OK), f"pcbpilot binary {pbin}", f"pcbpilot binary missing: {pbin}")
    check(os.path.exists(server), f"MCP server {server}", f"MCP server missing: {server}")
    # per client: pcbpilot present + points at existing files, upstream absent
    def entry_ok(label, e):
        if not e:
            bad.append(f"{label}: pcbpilot MCP not registered")
            return
        args = e.get("args") or []
        cmd = e.get("command", "")
        good = args and os.path.exists(args[0]) and (os.path.exists(cmd) or have(cmd))
        check(good, f"{label}: pcbpilot MCP → {args[0] if args else '?'}", f"{label}: pcbpilot MCP points at missing files {cmd} {args}")
    if os.path.exists(CLAUDE_JSON):
        d = load_json(CLAUDE_JSON)
        s = d.get("mcpServers") or {}
        entry_ok("claude", s.get(NAME))
        for n in UPSTREAM:
            check(n not in s, f"claude: no upstream {n}", f"claude: upstream MCP {n} still registered")
    if os.path.exists(CODEX_TOML):
        text = open(CODEX_TOML, encoding="utf-8").read()
        check(toml_has(text, NAME), "codex: pcbpilot MCP registered", "codex: pcbpilot MCP not registered")
        m = re.search(r'\[mcp_servers\.pcbpilot\][^\[]*args\s*=\s*\["([^"]+)"\]', text)
        check(bool(m) and os.path.exists(m.group(1)), "codex: pcbpilot MCP server path exists", "codex: pcbpilot MCP server path missing")
        for n in UPSTREAM:
            check(not toml_has(text, n), f"codex: no upstream {n}", f"codex: upstream MCP {n} still registered")
    if os.path.exists(ZCODE_JSON):
        s = (load_json(ZCODE_JSON).get("mcp") or {}).get("servers") or {}
        entry_ok("zcode", s.get(NAME))
        for n in UPSTREAM:
            check(n not in s, f"zcode: no upstream {n}", f"zcode: upstream MCP {n} still registered")
    if os.path.exists(AGENTS_JSON):
        s = load_json(AGENTS_JSON).get("mcpServers") or {}
        entry_ok("agents", s.get(NAME))
        for n in UPSTREAM:
            check(n not in s, f"agents: no upstream {n}", f"agents: upstream MCP {n} still registered")
    # skills
    for root in SKILL_ROOTS:
        if not os.path.isdir(root):
            continue
        link = os.path.join(root, "pcbpilot")
        check(os.path.isfile(os.path.join(link, "SKILL.md")), f"skill {link}", f"skill missing or broken: {link}")
        for n in os.listdir(root):
            p = os.path.join(root, n)
            md = os.path.join(p, "SKILL.md")
            sname = ""
            if os.path.isfile(md):
                for line in open(md, encoding="utf-8", errors="ignore"):
                    if line.startswith("name:"):
                        sname = line.split(":", 1)[1].strip().strip('"\'')
                        break
            upstream = n in ("easyeda-agent", "easyeda-design-workflow") or (sname.startswith("easyeda") and not n.startswith("pcbpilot"))
            check(not upstream, None, f"upstream skill still active: {p}")
    ok[:] = [x for x in ok if x]
    # MCP handshake: the server must start and expose pcbpilot tools
    try:
        tools = mcp_handshake(shutil.which("node") or "node", server, pbin)
        check(any(t.startswith("pcbpilot_") for t in tools), f"MCP handshake: {len(tools)} tools ({', '.join(sorted(tools)[:4])}…)",
              f"MCP handshake returned no pcbpilot tools: {tools}")
    except Exception as e:  # noqa: BLE001
        bad.append(f"MCP handshake failed: {e}")
    # CLI answers
    try:
        v = subprocess.run([pbin, "--version"], capture_output=True, text=True, timeout=20).stdout.strip()
        check(bool(v), f"CLI {v}", "CLI did not report a version")
    except Exception as e:  # noqa: BLE001
        bad.append(f"CLI failed: {e}")
    for x in ok:
        print(f"   ok   {x}")
    for x in bad:
        print(f"   FAIL {x}")
    return not bad


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    c = sub.add_parser("clean-upstream")
    c.add_argument("--backup", required=True)
    c.add_argument("--dry-run", action="store_true")
    r = sub.add_parser("register")
    r.add_argument("--bin", required=True)
    r.add_argument("--node", required=True)
    r.add_argument("--server", required=True)
    r.add_argument("--dry-run", action="store_true")
    v = sub.add_parser("verify")
    v.add_argument("--bin", required=True)
    v.add_argument("--server", required=True)
    v.add_argument("--repo", required=True)
    a = ap.parse_args()
    if a.cmd == "clean-upstream":
        clean_upstream(a.backup, a.dry_run)
    elif a.cmd == "register":
        register(a.bin, a.node, a.server, a.dry_run)
    else:
        sys.exit(0 if verify(a.bin, a.server, a.repo) else 1)


if __name__ == "__main__":
    main()
