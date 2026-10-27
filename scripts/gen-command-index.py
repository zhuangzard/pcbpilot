#!/usr/bin/env python3
"""Generate the Skill's command index from the CLI's own cobra help tree.

The CLI has no machine-readable command dump, so this walks
`pcbpilot <path> --help` breadth-first and reads each "...Commands:" block.
The "documented in" column is a plain-text scan of the public Skill's
Markdown for the command path, so the index also shows which commands no
reference mentions yet.

  python scripts/gen-command-index.py            # rewrite the index
  python scripts/gen-command-index.py --check    # non-zero when it is stale

The binary is $PCBPILOT_BIN, else --bin, else a fresh `go build` into a temp dir
(the index must describe this checkout, not whatever pcbpilot is on PATH).
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

REPO = Path(__file__).resolve().parent.parent
SKILL = REPO / ".agents/skills/pcbpilot"
INDEX = SKILL / "references/command-index.md"

# Shell plumbing, not design commands.
SKIP = {("help",), ("completion",)}
# A line in a help block: two-space indent, name padded to the longest name,
# then one space and the summary (so the longest name has a single space).
CMD_LINE = re.compile(r"^  ([A-Za-z0-9][\w-]*)(?: +(.*\S))?\s*$")
MAX_DOCS = 4


def parse_subcommands(help_text: str) -> list[tuple[str, str]]:
    """(name, summary) for every entry of every "...Commands:" block."""
    out, inside = [], False
    for line in help_text.splitlines():
        if line.rstrip().endswith("Commands:") and not line.startswith(" "):
            inside = True
            continue
        if inside:
            m = CMD_LINE.match(line)
            if m:
                out.append((m.group(1), m.group(2) or ""))
            elif not line.strip() or not line.startswith(" "):
                inside = False
    return out


def walk(run_help) -> list[tuple[tuple[str, ...], str]]:
    """Breadth-first over the tree; run_help(path) returns that command's --help."""
    # One level at a time, in parallel: each --help is a separate process.
    rows, level = [], [()]
    with ThreadPoolExecutor(max_workers=min(16, (os.cpu_count() or 2) * 2)) as pool:
        while level:
            nxt = []
            for path, text in zip(level, pool.map(run_help, level)):
                for name, summary in parse_subcommands(text):
                    child = path + (name,)
                    if child in SKIP:
                        continue
                    rows.append((child, summary))
                    nxt.append(child)
            level = nxt
    return sorted(rows)


def cli_help(binary: str):
    env = {**os.environ, "NO_COLOR": "1"}

    def run(path: tuple[str, ...]) -> str:
        res = subprocess.run([binary, *path, "--help"], capture_output=True, env=env,
                             timeout=60, encoding="utf-8", errors="replace")
        if res.returncode != 0:
            raise RuntimeError(f"pcbpilot {' '.join(path)} --help exited {res.returncode}: {res.stderr.strip()}")
        return res.stdout

    return run


def doc_pattern(path: tuple[str, ...]) -> re.Pattern:
    words = r"\s+".join(re.escape(w) for w in path)
    if len(path) == 1:
        # One bare word ("apply", "call", "sch") is ordinary prose; require the CLI name.
        return re.compile(r"pcbpilot\s+" + words + r"(?![\w-])")
    # kicad-cli has its own `pcb drc` / `sch export ...`; those are not ours.
    return re.compile(r"(?<![\w-])(?<!kicad-cli )" + words + r"(?![\w-])")


def doc_texts() -> dict[str, str]:
    texts = {}
    for md in sorted(SKILL.rglob("*.md")):
        if md == INDEX:
            continue
        rel = md.relative_to(INDEX.parent) if INDEX.parent in md.parents else Path("..") / md.relative_to(SKILL)
        texts[rel.as_posix()] = md.read_text(encoding="utf-8")
    return texts


def render(rows, texts) -> str:
    groups: dict[str, list] = {}
    for path, summary in rows:
        groups.setdefault(path[0], []).append((path, summary))
    undocumented = 0
    lines = [
        "# 命令索引（生成文件，勿手改）",
        "",
        "> 用途：按命令路径查一行说明和讲它的参考文档；参数以 `pcbpilot <路径> --help` 为准。",
        "> 由 `python scripts/gen-command-index.py` 从 CLI 的 `--help` 树生成（仓库根目录运行），",
        "> `--check` 在索引过期时非零退出（`scripts/tests/test_command_index.py`）。“文档”列是对",
        "> Skill Markdown 的纯文本匹配，只说明哪里提到该命令，不说明讲得是否完整；`—` 表示还没有参考提到它。",
        "",
        "## 目录",
        "",
    ]
    for top in groups:
        lines.append(f"- [{top}](#{top})")
    for top, items in groups.items():
        lines += ["", f"## {top}", "", "| 命令 | 说明 | 文档 |", "|---|---|---|"]
        for path, summary in items:
            pat = doc_pattern(path)
            hits = [name for name, text in texts.items() if pat.search(text)]
            if not hits:
                undocumented += 1
            shown = ", ".join(f"[{h.removeprefix('../')}]({h})" for h in hits[:MAX_DOCS])
            if len(hits) > MAX_DOCS:
                shown += f" +{len(hits) - MAX_DOCS}"
            summary = summary.replace("|", "\\|")
            lines.append(f"| `{' '.join(path)}` | {summary} | {shown or '—'} |")
    lines += ["", f"共 {len(rows)} 条命令，其中 {undocumented} 条尚无参考提到。", ""]
    return "\n".join(lines)


def build_binary(tmp: str) -> str:
    exe = os.path.join(tmp, "pcbpilot.exe" if os.name == "nt" else "pcbpilot")
    subprocess.run(["go", "build", "-o", exe, "./cmd/pcbpilot"], cwd=REPO, check=True)
    return exe


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--check", action="store_true", help="exit 1 when the committed index differs")
    ap.add_argument("--bin", help="pcbpilot binary built from this checkout (default $PCBPILOT_BIN, else go build)")
    args = ap.parse_args()
    with tempfile.TemporaryDirectory() as tmp:
        binary = args.bin or os.environ.get("PCBPILOT_BIN") or build_binary(tmp)
        text = render(walk(cli_help(binary)), doc_texts())
    if args.check:
        current = INDEX.read_text(encoding="utf-8") if INDEX.exists() else ""
        if current != text:
            print(f"{INDEX.relative_to(REPO)} is stale: run python scripts/gen-command-index.py", file=sys.stderr)
            return 1
        return 0
    INDEX.write_text(text, encoding="utf-8", newline="\n")
    print(f"wrote {INDEX.relative_to(REPO)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
