#!/usr/bin/env python3
"""Link checkout Skills, preserving release installs and unrelated checkouts."""

import argparse
import os
from pathlib import Path


REPO = Path(__file__).resolve().parent.parent


def skill_sources(repo: Path, scope: str = "repo") -> list[Path]:
    sources = [
        path.resolve()
        for path in sorted((repo / ".agents/skills").glob("*"))
        if not path.is_symlink() and path.is_dir() and (path / "SKILL.md").is_file()
        and (scope == "all" or (scope == "design" and path.name == "pcbpilot")
             or (scope == "repo" and path.name.startswith("pcbpilot-repo-")))
    ]
    if not sources:
        raise ValueError(f"no Skills found for scope {scope}")
    return sources


def target_dirs(target: str) -> list[Path]:
    clients = ("agents", "codex", "claude") if target == "all" else (target,)
    return [
        Path(os.environ.get(f"{client.upper()}_HOME") or str(Path.home() / f".{client}")).expanduser() / "skills"
        for client in clients
    ]


def legacy_link(source: Path, destination: Path) -> bool:
    """Only migrate the old public-Skill link belonging to this exact checkout."""
    if source.name != "pcbpilot" or not destination.is_symlink():
        return False
    old_source = source.parents[2] / "skills" / "pcbpilot"
    return destination.resolve() == old_source.resolve()


def link_plan(sources: list[Path], directories: list[Path]) -> list[tuple[Path, Path, bool]]:
    """Validate every destination before creating any directory or link."""
    plan = []
    seen = set()
    for directory in directories:
        directory = directory.expanduser().absolute()
        parent = directory
        while not parent.exists():
            if parent.is_symlink():
                raise ValueError(f"broken destination directory: {parent}")
            parent = parent.parent
        if not parent.is_dir():
            raise ValueError(f"destination parent is not a directory: {parent}")
        # Resolve parent aliases, not the destination Skill link itself.
        directory = directory.resolve()
        for source in sources:
            destination = directory / source.name
            if destination in seen:
                continue
            seen.add(destination)
            if legacy_link(source, destination):
                plan.append((source, destination, False))
                continue
            if destination.is_symlink():
                try:
                    existing = destination.resolve(strict=True)
                except (OSError, RuntimeError) as error:
                    raise ValueError(f"broken link preserved: {destination}") from error
                if existing != source:
                    raise ValueError(f"link to another checkout preserved: {destination}")
                plan.append((source, destination, True))
            elif destination.exists():
                raise ValueError(f"existing file or directory preserved: {destination}")
            else:
                plan.append((source, destination, False))
    return plan


def install(plan: list[tuple[Path, Path, bool]], dry_run: bool) -> None:
    created = []
    try:
        for source, destination, exists in plan:
            if exists:
                print(f"already installed: {destination}")
            elif dry_run:
                action = "migrate" if legacy_link(source, destination) else "link"
                print(f"would {action}: {destination} -> {source}")
            else:
                destination.parent.mkdir(parents=True, exist_ok=True)
                previous = None
                if legacy_link(source, destination):
                    previous = os.readlink(destination)
                    destination.unlink()
                    created.append((source, destination, previous))
                destination.symlink_to(source, target_is_directory=True)
                if previous is None:
                    created.append((source, destination, None))
                print(f"linked: {destination} -> {source}")
    except OSError:
        # Undo only links this invocation created and still owns.
        for source, destination, previous in reversed(created):
            if destination.is_symlink() and os.readlink(destination) == str(source):
                destination.unlink()
            if previous is not None and not destination.exists() and not destination.is_symlink():
                destination.symlink_to(previous, target_is_directory=True)
        raise


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    destination = parser.add_mutually_exclusive_group()
    destination.add_argument("--target", choices=("agents", "codex", "claude", "all"), default="agents")
    destination.add_argument("--skills-dir", type=Path, help="custom user-level Skill discovery directory")
    parser.add_argument("--dry-run", action="store_true", help="validate and print destinations without writing")
    parser.add_argument("--scope", choices=("repo", "design", "all"), default="repo",
                        help="repo collaboration Skills (default), public design Skill, or all checkout Skills")
    args = parser.parse_args()
    try:
        sources = skill_sources(REPO, args.scope)
        directories = [args.skills_dir] if args.skills_dir is not None else target_dirs(args.target)
        plan = link_plan(sources, directories)
        install(plan, args.dry_run)
    except (OSError, RuntimeError, ValueError) as error:
        parser.exit(1, f"error: {error}\n")
    print(f"{'Validated' if args.dry_run else 'Installed'} {len(sources)} checkout Skills ({args.scope}). "
          "Reload your Agent after installation.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
