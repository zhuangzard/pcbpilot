"""Offline release preparation regression; all writes stay in temporary repos."""

import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
import zipfile


def load_script(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), Path(__file__).resolve().parents[1] / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


pack = load_script("pack-skill")
release = load_script("release-check")
pack_mcp = load_script("pack-mcp")
sync_version = load_script("sync-skill-version")
manifest_lib = load_script("skill_manifest")

# Test-only placeholder for a focused public Skill; real focused Skills are
# written elsewhere and are never created by these fixtures in the checkout.
DEMO = "pcbpilot-demo"


def write_manifest(repo, skills=("pcbpilot",), retired=()):
    path = Path(repo) / ".agents/skills/public-skills.json"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(manifest_lib.Manifest("pcbpilot", tuple(skills), tuple(retired)).canonical_bytes())


class TrackedSkillPackageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name)
        self.skill = self.repo / pack.SKILL
        (self.skill / "references").mkdir(parents=True)
        self.run_git("init", "-q")
        self.run_git("config", "user.name", "Release fixture")
        self.run_git("config", "user.email", "fixture@example.invalid")
        (self.skill / "SKILL.md").write_text('---\nmetadata:\n  version: "1.4.2"\n---\n[Guide](references/guide.md)\n')
        (self.skill / "references/guide.md").write_text("Release fixture\n")
        write_manifest(self.repo)
        self.run_git("add", ".agents/skills")
        self.run_git("commit", "-qm", "fixture")

    def run_git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.repo), *args], stderr=subprocess.STDOUT)

    def test_only_tracked_and_staged_files_are_packaged(self):
        (self.skill / "scratch.md").write_text("Do not ship\n")
        (self.skill / "references/new.md").write_text("Reviewed addition\n")
        self.run_git("add", ".agents/skills/pcbpilot/references/new.md")
        output = self.repo / "dist/skills.tar.gz"
        self.assertEqual(pack.pack_skill(self.repo, output), 3)
        with tarfile.open(output) as archive:
            self.assertEqual(set(archive.getnames()), {
                "public-skills.json", "pcbpilot/SKILL.md", "pcbpilot/references/guide.md", "pcbpilot/references/new.md",
            })
            self.assertTrue(all(item.isfile() for item in archive.getmembers()))
        original = output.read_bytes()
        pack.pack_skill(self.repo, output)
        self.assertEqual(output.read_bytes(), original)

    def test_executable_bit_comes_from_git_index(self):
        # A Windows checkout has no executable bits; the archive must still
        # mark helpers that git records as 100755.
        (self.skill / "tool.sh").write_text("#!/bin/sh\n")
        self.run_git("add", ".agents/skills/pcbpilot/tool.sh")
        self.run_git("update-index", "--chmod=+x", ".agents/skills/pcbpilot/tool.sh")
        output = self.repo / "dist/skills.tar.gz"
        pack.pack_skill(self.repo, output)
        with tarfile.open(output) as archive:
            self.assertEqual(archive.getmember("pcbpilot/tool.sh").mode, 0o755)
            self.assertEqual(archive.getmember("pcbpilot/SKILL.md").mode, 0o644)

    def test_local_link_to_untracked_file_fails(self):
        (self.skill / "scratch.md").write_text("Unreviewed\n")
        (self.skill / "references/guide.md").write_text("[Scratch](../scratch.md)\n")
        with self.assertRaisesRegex(ValueError, "not included in the tracked package"):
            pack.check_skill(self.repo)

    def test_source_tree_doc_link_does_not_escape_installed_skill(self):
        (self.repo / "docs").mkdir()
        (self.repo / "docs/design.md").write_text("Repository-only detail\n")
        (self.skill / "references/guide.md").write_text("[Design](../../../docs/design.md)\n")
        with self.assertRaisesRegex(ValueError, "escapes the installed skill"):
            pack.check_skill(self.repo)

    def test_symlink_is_not_silently_dropped_by_updater(self):
        path = self.skill / "references/guide.md"
        path.unlink()
        path.symlink_to(self.repo / "outside.md")
        (self.repo / "outside.md").write_text("outside\n")
        with self.assertRaisesRegex(ValueError, "symlinks are unsupported|escapes the installed skill"):
            pack.check_skill(self.repo)

    def test_missing_tracked_file_fails(self):
        (self.skill / "references/guide.md").unlink()
        with self.assertRaisesRegex(ValueError, "missing|not included"):
            pack.check_skill(self.repo)

    def test_web_links_and_anchors_need_no_network(self):
        (self.skill / "references/guide.md").write_text("[Web](https://example.invalid/docs) [Here](#part)\n")
        self.assertEqual(len(pack.check_skill(self.repo)), 2)


class MultiSkillPackageTests(unittest.TestCase):
    """Router pcbpilot/ plus a test-only focused Skill packaged side by side."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name)
        self.router = self.repo / ".agents/skills/pcbpilot"
        self.demo = self.repo / ".agents/skills" / DEMO
        (self.router / "references").mkdir(parents=True)
        (self.demo / "references").mkdir(parents=True)
        (self.router / "SKILL.md").write_text('---\nmetadata:\n  version: "1.4.2"\n---\n[Shared](references/shared.md)\n')
        (self.router / "references/shared.md").write_text("Shared router reference\n")
        (self.demo / "SKILL.md").write_text('---\nmetadata:\n  version: "1.4.2"\n---\n[Own](references/own.md)\n')
        (self.demo / "references/own.md").write_text("[Shared](../../pcbpilot/references/shared.md)\n")
        write_manifest(self.repo, ("pcbpilot", DEMO), ("pcbpilot-old",))
        self.run_git("init", "-q")
        self.run_git("config", "user.name", "Release fixture")
        self.run_git("config", "user.email", "fixture@example.invalid")
        self.run_git("config", "core.autocrlf", "false")
        self.stage_and_commit()

    def run_git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.repo), *args], stderr=subprocess.STDOUT)

    def stage_and_commit(self):
        self.run_git("add", "-A", ".agents/skills")
        self.run_git("commit", "-qm", "fixture", "--allow-empty")

    def test_archive_has_one_root_per_skill_and_canonical_manifest(self):
        output = self.repo / "dist/skills.tar.gz"
        self.assertEqual(pack.pack_skill(self.repo, output), 4)
        with tarfile.open(output) as archive:
            self.assertEqual(sorted(archive.getnames()), sorted([
                "public-skills.json", "pcbpilot/SKILL.md", "pcbpilot/references/shared.md",
                f"{DEMO}/SKILL.md", f"{DEMO}/references/own.md"]))
            self.assertEqual(archive.getnames()[0], "public-skills.json")
            manifest = manifest_lib.from_archive(archive)
            self.assertEqual(manifest.skills, ("pcbpilot", DEMO))
            self.assertEqual(manifest.retired, ("pcbpilot-old",))
            self.assertEqual(archive.extractfile("public-skills.json").read(), manifest.canonical_bytes())
        first = output.read_bytes()
        pack.pack_skill(self.repo, output)
        self.assertEqual(first, output.read_bytes(), "multi-skill archive must stay reproducible")

    def test_link_rules_across_roots(self):
        # Router -> focused Skill: the router must stand alone.
        (self.router / "references/shared.md").write_text(f"[Demo](../../{DEMO}/SKILL.md)\n")
        with self.assertRaisesRegex(ValueError, "crosses into another skill"):
            pack.check_skill(self.repo)
        (self.router / "references/shared.md").write_text("Shared router reference\n")
        # Focused -> another focused Skill.
        other = self.repo / ".agents/skills/pcbpilot-other"
        other.mkdir()
        (other / "SKILL.md").write_text('---\nmetadata:\n  version: "1.4.2"\n---\nOther\n')
        write_manifest(self.repo, ("pcbpilot", DEMO, "pcbpilot-other"))
        self.stage_and_commit()
        (self.demo / "references/own.md").write_text("[Other](../../pcbpilot-other/SKILL.md)\n")
        with self.assertRaisesRegex(ValueError, "crosses into another skill"):
            pack.check_skill(self.repo)
        # Focused -> repository docs: outside every installed Skill.
        (self.demo / "references/own.md").write_text("[Docs](../../../../docs/x.md)\n")
        with self.assertRaisesRegex(ValueError, "escapes the installed skill"):
            pack.check_skill(self.repo)
        # Focused -> untracked router file.
        (self.router / "references/draft.md").write_text("draft\n")
        (self.demo / "references/own.md").write_text("[Draft](../../pcbpilot/references/draft.md)\n")
        with self.assertRaisesRegex(ValueError, "not included in the tracked package"):
            pack.check_skill(self.repo)

    def test_unlisted_and_retired_tracked_skills_fail(self):
        stray = self.repo / ".agents/skills/pcbpilot-stray"
        stray.mkdir()
        (stray / "SKILL.md").write_text('---\nmetadata:\n  version: "1.4.2"\n---\n')
        self.stage_and_commit()
        with self.assertRaisesRegex(ValueError, "not listed in public-skills.json"):
            pack.check_skill(self.repo)
        self.run_git("mv", ".agents/skills/pcbpilot-stray", ".agents/skills/pcbpilot-old")
        self.stage_and_commit()
        with self.assertRaisesRegex(ValueError, "listed as retired but still tracked"):
            pack.check_skill(self.repo)

    def test_repository_skills_are_neither_listed_nor_packaged(self):
        repo_skill = self.repo / ".agents/skills/pcbpilot-repo-lookup"
        repo_skill.mkdir()
        (repo_skill / "SKILL.md").write_text("---\nname: lookup\n---\n")
        self.stage_and_commit()
        output = self.repo / "dist/skills.tar.gz"
        pack.pack_skill(self.repo, output)
        with tarfile.open(output) as archive:
            self.assertFalse(any(name.startswith("pcbpilot-repo-") for name in archive.getnames()))

    def test_large_paragraph_duplicated_across_skills_is_flagged(self):
        paragraph = ("Keep the decoupling capacitor next to the supply pin and return its ground through a "
                     "short via; measure the loop after every move and record the readback before routing. ") * 2
        (self.router / "references/shared.md").write_text("Intro\n\n" + paragraph + "\n")
        # Re-wrapped lines still count as the same paragraph.
        rewrapped = paragraph.replace("; ", ";\n")
        (self.demo / "references/own.md").write_text(rewrapped + "\n\n[Shared](../../pcbpilot/references/shared.md)\n")
        with self.assertRaisesRegex(ValueError, "duplicated content across skills"):
            pack.check_skill(self.repo)
        # Short shared phrases and repeats inside one Skill are fine.
        (self.demo / "references/own.md").write_text("Intro\n\n[Shared](../../pcbpilot/references/shared.md)\n")
        (self.router / "SKILL.md").write_text('---\nmetadata:\n  version: "1.4.2"\n---\n' + paragraph + "\n\n[Shared](references/shared.md)\n")
        self.assertEqual(len(pack.check_skill(self.repo)), 4)

    def test_sync_version_stamps_every_listed_skill(self):
        self.assertEqual(sync_version.sync(self.repo, "1.5.0", check=True), 1)
        self.assertEqual(sync_version.sync(self.repo, "1.5.0", check=False), 0)
        for root in (self.router, self.demo):
            self.assertIn('  version: "1.5.0"', (root / "SKILL.md").read_text())
        self.assertEqual(sync_version.sync(self.repo, "1.5.0", check=True), 0)

    def test_sync_version_writes_nothing_when_one_skill_lacks_a_version(self):
        (self.demo / "SKILL.md").write_text("---\nname: demo\n---\n")
        before = (self.router / "SKILL.md").read_bytes()
        self.assertEqual(sync_version.sync(self.repo, "1.5.0", check=False), 1)
        self.assertEqual((self.router / "SKILL.md").read_bytes(), before)

    def test_sync_version_keeps_crlf_line_endings(self):
        (self.demo / "SKILL.md").write_bytes(b'---\r\nmetadata:\r\n  version: "1.4.2"\r\n---\r\n')
        self.assertEqual(sync_version.sync(self.repo, "1.5.0", check=False), 0)
        self.assertEqual((self.demo / "SKILL.md").read_bytes(), b'---\r\nmetadata:\r\n  version: "1.5.0"\r\n---\r\n')

    def test_release_check_requires_every_skill_version_and_exact_archive(self):
        (self.repo / "extension").mkdir()
        for file in ["extension/extension.json", "extension/package.json"]:
            (self.repo / file).write_text(json.dumps({"version": "1.4.2"}))
        (self.repo / "extension/package-lock.json").write_text(json.dumps({"version": "1.4.2", "packages": {"": {"version": "1.4.2"}}}))
        (self.repo / "extension/CHANGELOG.md").write_text("## [1.4.2]\n")
        self.assertEqual(release.check_sources(self.repo, "v1.4.2"), "1.4.2")
        output = self.repo / "dist/skills.tar.gz"
        pack.pack_skill(self.repo, output)
        release.check_skills_archive(self.repo, output, "1.4.2")
        with self.assertRaisesRegex(ValueError, "wrong version"):
            release.check_skills_archive(self.repo, output, "1.4.3")
        # The archive must carry exactly the source list.
        write_manifest(self.repo, ("pcbpilot",))
        with self.assertRaisesRegex(ValueError, "differs from the source manifest"):
            release.check_skills_archive(self.repo, output, "1.4.2")
        write_manifest(self.repo, ("pcbpilot", DEMO), ("pcbpilot-old",))
        (self.demo / "SKILL.md").write_text('---\nmetadata:\n  version: "1.4.1"\n---\n')
        with self.assertRaisesRegex(ValueError, f"{DEMO}/SKILL.md version must be 1.4.2"):
            release.check_sources(self.repo, "v1.4.2")

    def test_release_check_rejects_legacy_or_padded_archive(self):
        legacy = self.repo / "legacy.tar.gz"
        with tarfile.open(legacy, "w:gz") as archive:
            data = (self.router / "SKILL.md").read_bytes()
            item = tarfile.TarInfo("pcbpilot/SKILL.md")
            item.size = len(data)
            archive.addfile(item, io.BytesIO(data))
        with self.assertRaisesRegex(ValueError, "has no public-skills.json"):
            release.check_skills_archive(self.repo, legacy, "1.4.2")
        output = self.repo / "dist/skills.tar.gz"
        pack.pack_skill(self.repo, output)
        padded = self.repo / "padded.tar.gz"
        with tarfile.open(output) as source, tarfile.open(padded, "w:gz") as archive:
            for member in source.getmembers():
                archive.addfile(member, source.extractfile(member))
            archive.addfile(tarfile.TarInfo("pcbpilot-unlisted/SKILL.md"), io.BytesIO(b""))
        with self.assertRaisesRegex(ValueError, "outside the listed skills"):
            release.check_skills_archive(self.repo, padded, "1.4.2")


class SkillManifestTests(unittest.TestCase):
    def test_checkout_manifest_is_valid_canonical_and_lists_the_router(self):
        repo = Path(__file__).resolve().parents[2]
        manifest = manifest_lib.load(repo)
        self.assertEqual(manifest.router, "pcbpilot")
        self.assertIn("pcbpilot", manifest.skills)
        raw = (repo / ".agents/skills/public-skills.json").read_bytes().replace(b"\r\n", b"\n")
        self.assertEqual(raw, manifest.canonical_bytes(), "keep public-skills.json canonical (sorted keys, indent 2)")

    def test_invalid_manifests_are_rejected(self):
        good = {"schemaVersion": 1, "router": "pcbpilot", "skills": ["pcbpilot", DEMO], "retired": []}
        self.assertEqual(manifest_lib.parse(good).skills, ("pcbpilot", DEMO))
        for change in [{"schemaVersion": 2}, {"router": "other"}, {"skills": [DEMO]},
                       {"skills": ["pcbpilot", "pcbpilot"]}, {"skills": ["pcbpilot", "user-skill"]},
                       {"skills": ["pcbpilot", "pcbpilot-repo-lookup"]}, {"skills": ["pcbpilot", "pcbpilot-../x"]},
                       {"retired": ["pcbpilot"]}, {"retired": [DEMO]}, {"retired": ["my-skill"]}, {"extra": True}]:
            with self.subTest(change=change):
                with self.assertRaises(ValueError):
                    manifest_lib.parse({**good, **change})

    def test_canonical_form_is_awk_parsable_one_item_per_line(self):
        text = manifest_lib.Manifest("pcbpilot", ("pcbpilot", DEMO), ()).canonical_bytes().decode()
        self.assertIn('  "skills": [\n    "pcbpilot",\n    "pcbpilot-demo"\n  ]', text)
        self.assertIn('  "retired": [],', text)


class ReleaseVersionAndAssetTests(unittest.TestCase):
    def test_local_prerelease_is_not_a_publishable_release(self):
        for file in ["extension/extension.json", "extension/package.json", "extension/package-lock.json", ".agents/skills/pcbpilot/SKILL.md", "extension/CHANGELOG.md"]:
            path = self.repo / file
            path.write_text(path.read_text().replace("1.4.2", "1.4.3-dev.1"))
        self.assertEqual(release.check_sources(self.repo, "v1.4.3-dev.1", local_dev=True), "1.4.3-dev.1")
        with self.assertRaises(ValueError):
            release.check_sources(self.repo, "v1.4.3-dev.1")
        with self.assertRaises(ValueError):
            release.check_sources(self.repo, "v1.4.3", local_dev=True)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name)
        (self.repo / "extension").mkdir()
        (self.repo / ".agents/skills/pcbpilot").mkdir(parents=True)
        self.uuid = "a" * 32
        self.write_json("extension/extension.json", {"version": "1.4.2", "uuid": self.uuid})
        self.write_json("extension/package.json", {"version": "1.4.2"})
        self.write_json("extension/package-lock.json", {"version": "1.4.2", "packages": {"": {"version": "1.4.2"}}})
        (self.repo / ".agents/skills/pcbpilot/SKILL.md").write_text('---\nmetadata:\n  version: "1.4.2"\n---\n')
        write_manifest(self.repo)
        (self.repo / "extension/CHANGELOG.md").write_text("# Changes\n\n## [1.4.2]\n\nNew release\n")

    def write_json(self, path, data):
        (self.repo / path).write_text(json.dumps(data))

    def test_sources_are_checked_without_mutation(self):
        before = {p: p.read_bytes() for p in self.repo.rglob("*") if p.is_file()}
        self.assertEqual(release.check_sources(self.repo, "v1.4.2"), "1.4.2")
        self.assertEqual(before, {p: p.read_bytes() for p in self.repo.rglob("*") if p.is_file()})
        with self.assertRaisesRegex(ValueError, "complete release tag"):
            release.check_sources(self.repo, "1.4.2")

    def test_drift_and_missing_changelog_fail(self):
        self.write_json("extension/package-lock.json", {"version": "1.4.2", "packages": {"": {"version": "0.8.17"}}})
        with self.assertRaisesRegex(ValueError, "packages"):
            release.check_sources(self.repo, "v1.4.2")
        self.write_json("extension/package-lock.json", {"version": "1.4.2", "packages": {"": {"version": "1.4.2"}}})
        (self.repo / "extension/CHANGELOG.md").write_text("## [1.4.1]\n")
        with self.assertRaisesRegex(ValueError, "no ##"):
            release.check_sources(self.repo, "v1.4.2")

    def test_connector_must_contain_exact_target_manifest(self):
        path = self.repo / "stale.eext"
        with zipfile.ZipFile(path, "w") as archive:
            archive.writestr("extension.json", json.dumps({"version": "1.4.1", "uuid": self.uuid}))
            archive.writestr("dist/index.js", "compiled")
        with self.assertRaisesRegex(ValueError, "version/UUID differs"):
            release.check_connector(path, "1.4.2", self.uuid)

    def test_checksums_have_exact_bare_asset_names(self):
        dist = self.repo / "dist"
        dist.mkdir()
        for name in release.ASSETS:
            (dist / name).write_bytes(name.encode())
        release.write_checksums(dist)
        rows = [line.split() for line in (dist / "checksums.txt").read_text().splitlines()]
        self.assertEqual([row[1] for row in rows], release.ASSETS)
        self.assertTrue(all(len(row[0]) == 64 for row in rows))
        (dist / release.ASSETS[0]).unlink()
        with self.assertRaisesRegex(ValueError, "missing/empty"):
            release.write_checksums(dist)


class MCPPackageAndManifestTests(unittest.TestCase):
    """mcp.tar.gz + manifest.json: the assets the self-updater needs (v0.6.1)."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        repo = self.root / "repo"
        (repo / "mcp/src").mkdir(parents=True)
        (repo / "mcp/package.json").write_text('{"name":"pcbpilot-mcp","version":"0.0.0"}')
        (repo / "mcp/package-lock.json").write_text("{}")
        (repo / "mcp/src/server.mjs").write_text("#!/usr/bin/env node\n")
        nm = self.root / "node_modules"
        (nm / "@modelcontextprotocol/sdk").mkdir(parents=True)
        (nm / "@modelcontextprotocol/sdk/package.json").write_text('{"version":"1.30.0"}')
        (nm / ".bin").mkdir()
        (nm / ".bin/tool").symlink_to("../@modelcontextprotocol/sdk/package.json")
        self.repo, self.nm = repo, nm

    def build(self, version="1.4.2"):
        out = self.root / "dist/mcp.tar.gz"
        pack_mcp.pack(version, out, self.repo, self.nm)
        return out

    def test_archive_is_versioned_deterministic_and_link_free(self):
        out = self.build("v1.4.2")
        first = out.read_bytes()
        release.check_mcp(out, "1.4.2")
        with tarfile.open(out) as archive:
            names = archive.getnames()
            self.assertTrue(all(n == "mcp" or n.startswith("mcp/") for n in names))
            self.assertFalse(any("/.bin" in n for n in names), names)
            self.assertEqual(archive.extractfile("mcp/VERSION").read().decode().strip(), "1.4.2")
        self.build("v1.4.2")
        self.assertEqual(first, out.read_bytes(), "mcp.tar.gz must be byte-reproducible")
        with self.assertRaisesRegex(ValueError, "VERSION differs"):
            release.check_mcp(out, "1.4.3")

    def test_manifest_lists_every_asset_with_sha256_and_min_connector(self):
        dist = self.root / "dist"
        dist.mkdir()
        for name in release.MANIFEST_ASSETS:
            (dist / name).write_bytes(name.encode())
        release.write_manifest(dist, "1.4.2")
        manifest = json.loads((dist / "manifest.json").read_text())
        self.assertEqual(manifest["minConnector"], "1.4.2")
        self.assertEqual(manifest["components"]["mcp"], "1.4.2")
        self.assertEqual(set(manifest["assets"]), set(release.MANIFEST_ASSETS))
        release.check_manifest(dist, "1.4.2")
        (dist / "mcp.tar.gz").write_bytes(b"tampered")
        with self.assertRaisesRegex(ValueError, "mismatch: mcp.tar.gz"):
            release.check_manifest(dist, "1.4.2")
        self.assertIn("mcp.tar.gz", release.ASSETS)
        self.assertIn("manifest.json", release.ASSETS)


if __name__ == "__main__":
    unittest.main()
