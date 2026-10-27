"""The public-skill manifest shared by packaging, release checks and installers.

`.agents/skills/public-skills.json` names every Skill that ships in
skills.tar.gz (`skills`), the router among them (`router`, always `pcbpilot`),
and Skills an earlier release shipped that installers and the self-updater
remove again (`retired`). The archive carries a canonical copy inside the
router as `pcbpilot/.public-skills.json` (ARCHIVE_ENTRY): every installed router
then records its release's Skill set, and v0.10.x readers, which accept only
`pcbpilot/` entries, still read the archive. An archive without it is a
pre-0.11 single-Skill package (`LEGACY`).

The canonical serialization (sorted keys, two-space indent, one list item per
line) is a contract: install.sh parses it with awk, without a JSON parser.
"""

from dataclasses import dataclass
import json
from pathlib import Path, PurePosixPath
import re

MANIFEST_NAME = "public-skills.json"
SKILLS_ROOT = PurePosixPath(".agents/skills")
ROUTER = "pcbpilot"
INSTALLED_NAME = ".public-skills.json"
ARCHIVE_ENTRY = f"{ROUTER}/{INSTALLED_NAME}"
NAME = re.compile(r"[a-z0-9]+(?:-[a-z0-9]+)*")


@dataclass(frozen=True)
class Manifest:
    router: str
    skills: tuple
    retired: tuple

    def canonical_bytes(self) -> bytes:
        data = {"schemaVersion": 1, "router": self.router, "skills": list(self.skills), "retired": list(self.retired)}
        return (json.dumps(data, indent=2, sort_keys=True) + "\n").encode("utf-8")


LEGACY = Manifest(ROUTER, (ROUTER,), ())


def parse(raw) -> Manifest:
    """Validate a decoded manifest. Every name is the router or router-prefixed,
    so a retired entry can only ever name a directory pcbpilot itself owns."""
    if not isinstance(raw, dict) or raw.get("schemaVersion") != 1:
        raise ValueError(f"{MANIFEST_NAME}: schemaVersion must be 1")
    if set(raw) - {"schemaVersion", "router", "skills", "retired"}:
        raise ValueError(f"{MANIFEST_NAME}: unknown fields {sorted(set(raw) - {'schemaVersion', 'router', 'skills', 'retired'})}")
    router, skills, retired = raw.get("router"), raw.get("skills"), raw.get("retired", [])
    if router != ROUTER:
        raise ValueError(f"{MANIFEST_NAME}: router must be {ROUTER!r}")
    for field, names in (("skills", skills), ("retired", retired)):
        if not isinstance(names, list) or not all(isinstance(name, str) for name in names):
            raise ValueError(f"{MANIFEST_NAME}: {field} must be a list of names")
        if len(set(names)) != len(names):
            raise ValueError(f"{MANIFEST_NAME}: duplicate {field} entry")
        for name in names:
            if not NAME.fullmatch(name) or not (name == ROUTER or name.startswith(ROUTER + "-")):
                raise ValueError(f"{MANIFEST_NAME}: {field} entry {name!r} must be {ROUTER} or {ROUTER}-<slug>")
            if name.startswith(ROUTER + "-repo-"):
                raise ValueError(f"{MANIFEST_NAME}: {name} is a repository Skill and never ships publicly")
    if router not in skills:
        raise ValueError(f"{MANIFEST_NAME}: skills must include the router {router!r}")
    if router in retired or set(skills) & set(retired):
        raise ValueError(f"{MANIFEST_NAME}: a name cannot be both shipped and retired")
    return Manifest(router, tuple(skills), tuple(retired))


def loads(data: bytes) -> Manifest:
    try:
        raw = json.loads(data.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ValueError(f"{MANIFEST_NAME}: {error}") from None
    return parse(raw)


def load(repo: Path) -> Manifest:
    """The source manifest of a checkout (repo root)."""
    return loads((Path(repo) / SKILLS_ROOT / MANIFEST_NAME).read_bytes())


def from_archive(archive) -> Manifest:
    """The manifest of an opened skills.tar.gz; LEGACY when the archive predates it."""
    try:
        member = archive.getmember(ARCHIVE_ENTRY)
    except KeyError:
        return LEGACY
    if not member.isfile():
        raise ValueError(f"skills.tar.gz: {ARCHIVE_ENTRY} is not a regular file")
    return loads(archive.extractfile(member).read())
