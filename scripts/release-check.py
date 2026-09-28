#!/usr/bin/env python3
"""Validate an already-versioned release; never modify sources or publish."""

import argparse
import hashlib
import json
import platform
from pathlib import Path
import re
import subprocess
import tarfile
import zipfile

ASSETS = [
    "pcbpilot_darwin_amd64", "pcbpilot_darwin_arm64",
    "pcbpilot_linux_amd64", "pcbpilot_linux_arm64", "pcbpilot_windows_amd64.exe",
    "pcbpilot-connector.eext", "skills.tar.gz", "mcp.tar.gz", "install.sh", "install.ps1",
    "manifest.json",
]
# manifest.json describes every other asset (version + sha256 + size) for the
# updater and humans; checksums.txt still covers all ASSETS including it.
MANIFEST_ASSETS = [name for name in ASSETS if name != "manifest.json"]
# Installer scripts are published verbatim; the packaged copy must match the source.
INSTALLERS = ["install.sh", "install.ps1"]


def skill_version(text: str) -> str:
    match = re.search(r'^  version:\s*"([^"]+)"$', text, re.MULTILINE)
    if not match:
        raise ValueError("SKILL.md metadata.version missing")
    return match.group(1)


def check_sources(repo: Path, tag: str, local_dev: bool = False) -> str:
    pattern = r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    if local_dev:
        pattern += r"-dev\.[1-9][0-9]*"
    if not re.fullmatch(pattern, tag):
        expected = "vX.Y.Z-dev.N (N >= 1)" if local_dev else "vX.Y.Z"
        raise ValueError(f"VERSION must be a complete {'local development version' if local_dev else 'release tag'}: {expected}")
    version = tag[1:]
    for name in ["extension/extension.json", "extension/package.json", "extension/package-lock.json"]:
        data = json.loads((repo / name).read_text(encoding="utf-8"))
        if data.get("version") != version:
            raise ValueError(f"{name}: version {data.get('version')!r}, expected {version}")
        if name.endswith("package-lock.json") and data.get("packages", {}).get("", {}).get("version") != version:
            raise ValueError(f"{name}: packages[''].version must also be {version}")
    if skill_version((repo / ".agents/skills/pcbpilot/SKILL.md").read_text(encoding="utf-8")) != version:
        raise ValueError(f"SKILL.md version must be {version}; run scripts/sync-skill-version.py {version}")
    changelog = (repo / "extension/CHANGELOG.md").read_text(encoding="utf-8")
    if not re.search(rf"^##\s*\[{re.escape(version)}\]", changelog, re.MULTILINE):
        raise ValueError(f"extension/CHANGELOG.md has no ## [{version}] entry")
    return version


def check_connector(path: Path, version: str, uuid: str) -> None:
    with zipfile.ZipFile(path) as archive:
        manifest = json.loads(archive.read("extension.json"))
        if manifest.get("version") != version or manifest.get("uuid") != uuid:
            raise ValueError(f"{path}: packaged connector version/UUID differs from the requested source")
        if "dist/index.js" not in archive.namelist():
            raise ValueError(f"{path}: compiled connector entry is missing")


def write_manifest(dist: Path, version: str, repo_slug: str = "zhuangzard/pcbpilot") -> None:
    """Versions and sha256 of every asset the updater needs. The connector must
    equal the release exactly (daemon gate, 2026-09-28): minConnector = version."""
    assets = {}
    for name in MANIFEST_ASSETS:
        path = dist / name
        if not path.is_file() or path.stat().st_size == 0:
            raise ValueError(f"missing/empty release asset: {path}")
        data = path.read_bytes()
        assets[name] = {"sha256": hashlib.sha256(data).hexdigest(), "size": len(data)}
    manifest = {
        "schemaVersion": 1,
        "name": "pcbpilot",
        "version": version,
        "tag": f"v{version}",
        "repo": repo_slug,
        "minConnector": version,
        "components": {"cli": version, "daemon": version, "skill": version, "mcp": version, "connector": version},
        "assets": assets,
    }
    with (dist / "manifest.json").open("w", encoding="utf-8", newline="\n") as stream:
        json.dump(manifest, stream, indent=2, sort_keys=True)
        stream.write("\n")


def check_mcp(path: Path, version: str) -> None:
    with tarfile.open(path, "r:gz") as archive:
        names = set()
        for member in archive.getmembers():
            name = member.name
            if not (name == "mcp" or name.startswith("mcp/")) or ".." in name.split("/"):
                raise ValueError(f"mcp.tar.gz: entry outside mcp/: {name}")
            if not (member.isfile() or member.isdir()):
                raise ValueError(f"mcp.tar.gz: links/devices are not allowed: {name}")
            names.add(name)
        for need in ("mcp/src/server.mjs", "mcp/package.json", "mcp/VERSION",
                     "mcp/node_modules/@modelcontextprotocol/sdk/package.json"):
            if need not in names:
                raise ValueError(f"mcp.tar.gz is missing {need}")
        if archive.extractfile("mcp/VERSION").read().decode().strip() != version:
            raise ValueError("mcp.tar.gz VERSION differs from the release")


def check_manifest(dist: Path, version: str) -> None:
    manifest = json.loads((dist / "manifest.json").read_text(encoding="utf-8"))
    if manifest.get("version") != version or manifest.get("minConnector") != version:
        raise ValueError("manifest.json version/minConnector differs from the release")
    if set(manifest.get("assets", {})) != set(MANIFEST_ASSETS):
        raise ValueError("manifest.json must list every release asset except itself")
    for name, entry in manifest["assets"].items():
        data = (dist / name).read_bytes()
        if entry.get("sha256") != hashlib.sha256(data).hexdigest() or entry.get("size") != len(data):
            raise ValueError(f"manifest.json sha256/size mismatch: {name}")


def write_checksums(dist: Path) -> None:
    records = []
    for name in ASSETS:
        path = dist / name
        if not path.is_file() or path.stat().st_size == 0:
            raise ValueError(f"missing/empty release asset: {path}")
        records.append(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {name}\n")
    # newline="\n": checksums.txt is consumed by install.sh / internal/selfupdate,
    # so it must stay LF even when the release is cut on Windows.
    with (dist / "checksums.txt").open("w", encoding="utf-8", newline="\n") as stream:
        stream.write("".join(records))


def check_artifacts(repo: Path, dist: Path, version: str) -> None:
    expected = {}
    for line in (dist / "checksums.txt").read_text(encoding="utf-8").splitlines():
        digest, name = line.split()
        if name in expected or name not in ASSETS or not re.fullmatch(r"[0-9a-f]{64}", digest):
            raise ValueError(f"invalid checksum asset entry: {line}")
        expected[name] = digest
    if set(expected) != set(ASSETS):
        raise ValueError(f"checksums.txt must name all {len(ASSETS)} release assets with bare filenames")
    for name in ASSETS:
        if hashlib.sha256((dist / name).read_bytes()).hexdigest() != expected[name]:
            raise ValueError(f"checksum mismatch: {name}")
    manifest = json.loads((repo / "extension/extension.json").read_text(encoding="utf-8"))
    check_connector(dist / "pcbpilot-connector.eext", version, manifest["uuid"])
    with tarfile.open(dist / "skills.tar.gz", "r:gz") as archive:
        item = archive.extractfile("pcbpilot/SKILL.md")
        if item is None or skill_version(item.read().decode()) != version:
            raise ValueError("packaged SKILL.md has the wrong version")
    check_mcp(dist / "mcp.tar.gz", version)
    check_manifest(dist, version)
    for name in INSTALLERS:
        if (dist / name).read_bytes() != (repo / name).read_bytes():
            raise ValueError(f"packaged {name} differs from source")
    # install.ps1 is executed by `irm | iex`: a BOM would break the first token and
    # Windows PowerShell 5.1 decodes a BOM-less script with the system ANSI codepage.
    installer_ps1 = (dist / "install.ps1").read_bytes()
    if not installer_ps1.isascii():
        raise ValueError("install.ps1 must be pure ASCII (no BOM, no literal non-ASCII text)")
    os_name = platform.system().lower()
    arch = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine().lower())
    if arch and os_name in {"darwin", "linux"}:
        binary = (dist / f"pcbpilot_{os_name}_{arch}").resolve()
        actual = subprocess.check_output([str(binary), "--version"], text=True).strip()
        if actual != f"pcbpilot v{version}":
            raise ValueError(f"native release CLI version mismatch: {actual}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version")
    parser.add_argument("--local-dev", action="store_true", help="require vX.Y.Z-dev.N; never publish")
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parent.parent)
    parser.add_argument("--connector", type=Path)
    parser.add_argument("--write-manifest", type=Path, help="write DIST/manifest.json (before --write-checksums)")
    parser.add_argument("--repo-slug", default="zhuangzard/pcbpilot")
    parser.add_argument("--write-checksums", type=Path)
    parser.add_argument("--artifacts", type=Path)
    args = parser.parse_args()
    try:
        version = check_sources(args.repo, args.version, args.local_dev)
        if args.connector:
            uuid = json.loads((args.repo / "extension/extension.json").read_text(encoding="utf-8"))["uuid"]
            check_connector(args.connector, version, uuid)
        if args.write_manifest:
            write_manifest(args.write_manifest, version, args.repo_slug)
        if args.write_checksums:
            write_checksums(args.write_checksums)
        if args.artifacts:
            check_artifacts(args.repo, args.artifacts, version)
        print(f"Release check passed: {args.version}" + (f"; artifacts in {args.artifacts}" if args.artifacts else ""))
    except (OSError, ValueError, KeyError, tarfile.TarError, zipfile.BadZipFile, subprocess.CalledProcessError) as error:
        parser.exit(1, f"release check failed: {error}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
