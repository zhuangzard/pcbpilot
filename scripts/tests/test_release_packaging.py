"""Offline release preparation regression; all writes stay in temporary repos."""

import importlib.util
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
                "pcbpilot/SKILL.md", "pcbpilot/references/guide.md", "pcbpilot/references/new.md",
            })
            self.assertTrue(all(item.isfile() for item in archive.getmembers()))
        original = output.read_bytes()
        pack.pack_skill(self.repo, output)
        self.assertEqual(output.read_bytes(), original)

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
