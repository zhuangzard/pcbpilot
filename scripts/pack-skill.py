#!/usr/bin/env python3
"""Check and package the tracked public skill without local scratch files."""

import argparse
import gzip
import io
import os
from pathlib import Path
import re
import subprocess
import tarfile
from urllib.parse import unquote, urlsplit

SKILL = Path(".agents/skills/pcbpilot")


def tracked_skill_files(repo: Path) -> list[Path]:
    raw = subprocess.check_output(["git", "-C", str(repo), "ls-files", "-z", "--", str(SKILL)])
    files = [Path(os.fsdecode(item)) for item in raw.split(b"\0") if item]
    if SKILL / "SKILL.md" not in files:
        raise ValueError("public SKILL.md is not tracked; stage reviewed skill files before packaging")
    return sorted(set(files))


def check_skill(repo: Path) -> list[Path]:
    repo = repo.resolve()
    root = repo / SKILL
    files = tracked_skill_files(repo)
    tracked = {repo / file for file in files}
    for file in files:
        path = repo / file
        if path.is_symlink():
            raise ValueError(f"{file}: bundled symlinks are unsupported; include a tracked regular file")
        try:
            path.resolve().relative_to(root.resolve())
        except ValueError:
            raise ValueError(f"{file}: resolves outside the public skill") from None
        if not path.is_file():
            raise ValueError(f"{file}: tracked package file is missing or not a regular file")
        if path.suffix != ".md":
            continue
        # Local documentation links must work after extracting only this skill.
        # Web links are intentionally left intact and do not trigger network I/O.
        for match in re.finditer(r"\[[^\]]*\]\(([^)]+)\)", path.read_text(encoding="utf-8")):
            link = match.group(1).strip().split(' "')[0].strip("<>")
            parsed = urlsplit(link)
            if parsed.scheme or parsed.netloc or not parsed.path:
                continue
            target = (path.parent / unquote(parsed.path)).resolve()
            try:
                target.relative_to(root.resolve())
            except ValueError:
                raise ValueError(f"{file}: local link escapes the installed skill: {link}") from None
            if target not in tracked and not (target.is_dir() and any(target in item.parents for item in tracked)):
                raise ValueError(f"{file}: local link is not included in the tracked package: {link}")
    return files


def pack_skill(repo: Path, output: Path) -> int:
    repo = repo.resolve()
    files = check_skill(repo)
    if output.resolve() == repo / SKILL or repo / SKILL in output.resolve().parents:
        raise ValueError("output archive must be outside the public skill")
    # Git's timestamp gives repeated builds of the same source the same archive.
    stamp = int(subprocess.check_output(["git", "-C", str(repo), "show", "-s", "--format=%ct", "HEAD"]).strip())
    output.parent.mkdir(parents=True, exist_ok=True)
    with output.open("wb") as stream, gzip.GzipFile(filename="", mode="wb", fileobj=stream, mtime=stamp) as compressed:
        with tarfile.open(fileobj=compressed, mode="w") as archive:
            for file in files:
                path = repo / file
                content = path.read_bytes()
                # as_posix(): tar member names are always "/"-separated. str() on
                # Windows yields "pcbpilot\references\guide.md", which every
                # extractor then treats as one flat filename.
                entry = tarfile.TarInfo((Path("pcbpilot") / file.relative_to(SKILL)).as_posix())
                entry.size, entry.mtime = len(content), stamp
                entry.mode = 0o755 if path.stat().st_mode & 0o111 else 0o644
                archive.addfile(entry, io.BytesIO(content))
    return len(files)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parent.parent)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--out", type=Path)
    args = parser.parse_args()
    if not args.check and args.out is None:
        parser.error("pass --check or --out")
    try:
        if args.out is not None:
            count = pack_skill(args.repo, args.out)
            print(f"Packaged {count} tracked skill files -> {args.out}")
        else:
            count = len(check_skill(args.repo))
            print(f"Skill package check passed: {count} tracked files; local links stay inside the installed skill")
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"skill package check failed: {error}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
