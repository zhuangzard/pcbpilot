#!/usr/bin/env python3
"""Package the MCP server as the release asset mcp.tar.gz.

The archive root is mcp/ with src/, package.json, package-lock.json, VERSION
(= the release version, reported by the server's initialize handshake) and
PRODUCTION node_modules installed with `npm ci --omit=dev --ignore-scripts`.
All dependencies are pure JavaScript (no native addons), so one archive serves
every platform and installing it needs only Node.js >= 20.17 — no npm and no
registry access on the user's machine. node_modules/.bin (symlinks) is left
out. The tar is deterministic (sorted, mtime 0, uid/gid 0). Size: ~3.5 MB
compressed, ~24 MB extracted.

  pack-mcp.py --version v0.6.1 --out dist/mcp.tar.gz
  pack-mcp.py --version 0.6.1 --out x.tgz --node-modules /prepared/node_modules   # offline (tests)
"""

import argparse
import gzip
import io
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile

REPO = Path(__file__).resolve().parent.parent
VERSION_RE = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-dev\.[1-9][0-9]*)?")


def add_tree(tar, root: Path, arc_root: str):
    paths = sorted(p for p in root.rglob("*"))
    for p in paths:
        rel = p.relative_to(root).as_posix()
        if rel == "node_modules/.bin" or rel.startswith("node_modules/.bin/") or "/.bin/" in rel or rel.endswith("/.bin"):
            continue
        if p.is_symlink():
            continue  # never needed at runtime; the installer refuses to follow links anyway
        info = tarfile.TarInfo(f"{arc_root}/{rel}")
        info.mtime, info.uid, info.gid, info.uname, info.gname = 0, 0, 0, "", ""
        if p.is_dir():
            info.type, info.mode = tarfile.DIRTYPE, 0o755
            tar.addfile(info)
        elif p.is_file():
            data = p.read_bytes()
            info.size = len(data)
            info.mode = 0o755 if os.access(p, os.X_OK) or data.startswith(b"#!") else 0o644
            tar.addfile(info, io.BytesIO(data))


def pack(version: str, out: Path, repo: Path = REPO, node_modules=None) -> Path:
    version = version.removeprefix("v")
    if not VERSION_RE.fullmatch(version):
        raise SystemExit(f"--version must be X.Y.Z or X.Y.Z-dev.N, got {version!r}")
    src = repo / "mcp"
    with tempfile.TemporaryDirectory(prefix="pcbpilot-mcp-") as tmp:
        stage = Path(tmp) / "mcp"
        stage.mkdir()
        for name in ("package.json", "package-lock.json", "README.md"):
            if (src / name).is_file():
                shutil.copy2(src / name, stage / name)
        shutil.copytree(src / "src", stage / "src")
        (stage / "VERSION").write_text(version + "\n", encoding="utf-8")
        if node_modules:
            shutil.copytree(node_modules, stage / "node_modules", symlinks=True)
        else:
            subprocess.run(["npm", "ci", "--omit=dev", "--ignore-scripts", "--no-audit", "--no-fund"],
                           cwd=stage, check=True, stdout=subprocess.DEVNULL)
        sdk = stage / "node_modules/@modelcontextprotocol/sdk/package.json"
        if not sdk.is_file():
            raise SystemExit("MCP SDK missing from node_modules after install")
        natives = [p for p in (stage / "node_modules").rglob("*.node")]
        if natives:
            raise SystemExit(f"native addons would make mcp.tar.gz platform-specific: {natives[:3]}")
        raw = io.BytesIO()
        with tarfile.open(fileobj=raw, mode="w", format=tarfile.PAX_FORMAT) as tar:
            root = tarfile.TarInfo("mcp")
            root.type, root.mode, root.mtime = tarfile.DIRTYPE, 0o755, 0
            tar.addfile(root)
            add_tree(tar, stage, "mcp")
        out.parent.mkdir(parents=True, exist_ok=True)
        with open(out, "wb") as f, gzip.GzipFile(filename="", mode="wb", fileobj=f, mtime=0) as gz:
            gz.write(raw.getvalue())
    return out


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--version", required=True)
    ap.add_argument("--out", required=True, type=Path)
    ap.add_argument("--repo", type=Path, default=REPO)
    ap.add_argument("--node-modules", type=Path, help="copy this prepared node_modules instead of running npm ci")
    a = ap.parse_args()
    path = pack(a.version, a.out, a.repo, a.node_modules)
    print(f"{path} ({path.stat().st_size / 1e6:.1f} MB)")


if __name__ == "__main__":
    main()
