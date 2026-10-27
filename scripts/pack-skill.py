#!/usr/bin/env python3
"""Check and package the tracked public skills without local scratch files.

`.agents/skills/public-skills.json` lists the shipped Skills. The archive has
one top-level directory per Skill; a canonical copy of the manifest rides in
the router as `pcbpilot/.public-skills.json`. Text files are packed with LF
line endings whatever the checkout's core.autocrlf, as the git index has them. A
focused Skill may link into the router `pcbpilot/`; every other local link must
stay inside its own Skill, so each package still works after extraction.
"""

import argparse
from collections import defaultdict
import gzip
import io
import os
from pathlib import Path
import re
import subprocess
import sys
import tarfile
from urllib.parse import unquote, urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parent))
import skill_manifest  # noqa: E402

SKILLS = Path(skill_manifest.SKILLS_ROOT)
SKILL = SKILLS / skill_manifest.ROUTER
# A paragraph this long appearing in two Skills is copied knowledge; keep one
# home (normally a pcbpilot/ reference) and link to it instead.
DUPLICATE_MIN_CHARS = 200


def tracked_files(repo: Path) -> list[Path]:
    raw = subprocess.check_output(["git", "-C", str(repo), "ls-files", "-z", "--", SKILLS.as_posix()])
    return sorted({Path(os.fsdecode(item)) for item in raw.split(b"\0") if item})


def tracked_skill_files(repo: Path, manifest: skill_manifest.Manifest) -> dict[str, list[Path]]:
    files = tracked_files(repo)
    by_skill = defaultdict(list)
    for file in files:
        parts = file.relative_to(SKILLS).parts
        if len(parts) > 1 and parts[0] in manifest.skills:
            by_skill[parts[0]].append(file)
    for name in manifest.skills:
        if SKILLS / name / "SKILL.md" not in files:
            raise ValueError(f"public {name}/SKILL.md is not tracked; stage reviewed skill files before packaging")
    # A tracked Skill that is neither shipped nor repository-only would silently
    # vanish from the release; a retired one would be deleted from every install.
    for file in files:
        parts = file.relative_to(SKILLS).parts
        if len(parts) == 2 and parts[1] == "SKILL.md":
            name = parts[0]
            if name in manifest.retired:
                raise ValueError(f"{name} is listed as retired but still tracked under {SKILLS.as_posix()}")
            if name not in manifest.skills and not name.startswith("pcbpilot-repo-"):
                raise ValueError(f"{name}/SKILL.md is tracked but not listed in {skill_manifest.MANIFEST_NAME}")
    return {name: sorted(by_skill[name]) for name in manifest.skills}


def tracked_executables(repo: Path) -> set[Path]:
    # Git's index mode is the source of truth: a Windows checkout has no
    # executable bits, so stat() alone would ship every helper as 0644.
    raw = subprocess.check_output(["git", "-C", str(repo), "ls-files", "-s", "-z", "--", SKILLS.as_posix()])
    out = set()
    for item in raw.split(b"\0"):
        meta, _, name = item.partition(b"\t")
        if name and meta.startswith(b"100755"):
            out.add(Path(os.fsdecode(name)))
    return out


def packed_bytes(path: Path) -> bytes:
    """File content as shipped: text (no NUL byte, git's own heuristic) gets LF
    line endings, so a Windows checkout with core.autocrlf packs what the git
    index holds and shebangs and version lines stay parseable."""
    data = path.read_bytes()
    return data if b"\0" in data else data.replace(b"\r\n", b"\n")


def inside(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def paragraphs(text: str):
    for block in re.split(r"\n\s*\n", text):
        normalized = " ".join(block.split())
        if len(normalized) >= DUPLICATE_MIN_CHARS:
            yield normalized


def duplicate_paragraphs(repo: Path, by_skill: dict[str, list[Path]]) -> list[str]:
    """Paragraphs of at least DUPLICATE_MIN_CHARS that occur in more than one Skill."""
    seen = {}
    problems = []
    for name, files in by_skill.items():
        for file in files:
            if file.suffix != ".md":
                continue
            for paragraph in paragraphs((repo / file).read_text(encoding="utf-8")):
                first = seen.setdefault(paragraph, (name, file))
                if first[0] != name:
                    problems.append(f"{file.as_posix()} repeats a paragraph of {first[1].as_posix()}: {paragraph[:60]}...")
    return problems


def check_skills(repo: Path) -> tuple[skill_manifest.Manifest, dict[str, list[Path]]]:
    repo = repo.resolve()
    manifest = skill_manifest.load(repo)
    manifest_path = (SKILLS / skill_manifest.MANIFEST_NAME).as_posix()
    if not subprocess.check_output(["git", "-C", str(repo), "ls-files", "--", manifest_path]).strip():
        raise ValueError(f"{manifest_path} is not tracked")
    by_skill = tracked_skill_files(repo, manifest)
    if SKILL / skill_manifest.INSTALLED_NAME in by_skill[manifest.router]:
        raise ValueError(f"{(SKILL / skill_manifest.INSTALLED_NAME).as_posix()} is generated by the packager; do not track it")
    roots = {name: (repo / SKILLS / name).resolve() for name in manifest.skills}
    tracked = {repo / file for files in by_skill.values() for file in files}
    for name, files in by_skill.items():
        root = roots[name]
        for file in files:
            path = repo / file
            if path.is_symlink():
                raise ValueError(f"{file}: bundled symlinks are unsupported; include a tracked regular file")
            if not inside(path.resolve(), root):
                raise ValueError(f"{file}: resolves outside the public skill")
            if not path.is_file():
                raise ValueError(f"{file}: tracked package file is missing or not a regular file")
            if path.suffix != ".md":
                continue
            # Local documentation links must work after extracting the archive.
            # Web links are intentionally left intact and do not trigger network I/O.
            for match in re.finditer(r"\[[^\]]*\]\(([^)]+)\)", path.read_text(encoding="utf-8")):
                link = match.group(1).strip().split(' "')[0].strip("<>")
                parsed = urlsplit(link)
                if parsed.scheme or parsed.netloc or not parsed.path:
                    continue
                target = (path.parent / unquote(parsed.path)).resolve()
                if inside(target, root):
                    pass
                elif name != manifest.router and inside(target, roots[manifest.router]):
                    pass  # focused Skill -> shared router file; both always ship together
                elif any(inside(target, other) for other in roots.values()):
                    raise ValueError(f"{file}: local link crosses into another skill: {link}; "
                                     f"only focused skills may link, and only into {manifest.router}/")
                else:
                    raise ValueError(f"{file}: local link escapes the installed skill: {link}")
                if target not in tracked and not (target.is_dir() and any(target in item.parents for item in tracked)):
                    raise ValueError(f"{file}: local link is not included in the tracked package: {link}")
    duplicates = duplicate_paragraphs(repo, by_skill)
    if duplicates:
        raise ValueError("duplicated content across skills (keep one home and link to it):\n  " + "\n  ".join(duplicates))
    return manifest, by_skill


def check_skill(repo: Path) -> list[Path]:
    """Every packaged Skill file (the manifest itself is not counted)."""
    _, by_skill = check_skills(repo)
    return [file for files in by_skill.values() for file in files]


def pack_skill(repo: Path, output: Path) -> int:
    repo = repo.resolve()
    manifest, by_skill = check_skills(repo)
    for name in manifest.skills:
        root = repo / SKILLS / name
        if output.resolve() == root or root in output.resolve().parents:
            raise ValueError("output archive must be outside the public skills")
    # Git's timestamp gives repeated builds of the same source the same archive.
    stamp = int(subprocess.check_output(["git", "-C", str(repo), "show", "-s", "--format=%ct", "HEAD"]).strip())
    executable = tracked_executables(repo)
    output.parent.mkdir(parents=True, exist_ok=True)
    count = 0
    with output.open("wb") as stream, gzip.GzipFile(filename="", mode="wb", fileobj=stream, mtime=stamp) as compressed:
        with tarfile.open(fileobj=compressed, mode="w") as archive:
            def add(name: str, content: bytes, mode: int) -> None:
                entry = tarfile.TarInfo(name)
                entry.size, entry.mtime, entry.mode = len(content), stamp, mode
                archive.addfile(entry, io.BytesIO(content))

            add(skill_manifest.ARCHIVE_ENTRY, manifest.canonical_bytes(), 0o644)
            for name in manifest.skills:
                for file in by_skill[name]:
                    # as_posix(): tar member names are always "/"-separated. str() on
                    # Windows yields "pcbpilot\references\guide.md", which every
                    # extractor then treats as one flat filename.
                    add((Path(name) / file.relative_to(SKILLS / name)).as_posix(), packed_bytes(repo / file),
                        0o755 if file in executable else 0o644)
                    count += 1
    return count


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
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
            manifest, by_skill = check_skills(args.repo)
            count = sum(len(files) for files in by_skill.values())
            print(f"Skill package check passed: {len(manifest.skills)} skill(s) ({', '.join(manifest.skills)}), "
                  f"{count} tracked files; local links stay inside the installed skills")
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"skill package check failed: {error}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
