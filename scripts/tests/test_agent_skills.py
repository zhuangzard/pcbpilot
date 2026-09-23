"""Behavioral checks for repository discovery and non-destructive installation."""

import importlib.util
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
from urllib.parse import unquote, urlsplit


REPO = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("agent_install", REPO / "scripts/install-agent-skills.py")
installer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(installer)


class AgentSkillTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="pcbpilot agent skills ")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.checkout = self.root / "checkout with spaces"
        self.checkout.mkdir()
        shutil.copytree(REPO / ".agents/skills", self.checkout / ".agents/skills", symlinks=True)
        (self.checkout / "scripts").mkdir()
        for name in ("agent-repo-root.py", "install-agent-skills.py"):
            shutil.copy2(REPO / "scripts" / name, self.checkout / "scripts" / name)
        for name in ("AGENTS.md", "go.mod"):
            shutil.copy2(REPO / name, self.checkout / name)
        self.target = self.root / "user skills"

    def cli(self, *args, checkout=None):
        return subprocess.run(
            [sys.executable, str((checkout or self.checkout) / "scripts/install-agent-skills.py"),
             "--skills-dir", str(self.target), *args],
            cwd=self.root, text=True, capture_output=True,
        )

    def test_dry_run_does_not_create_destination(self):
        result = self.cli("--dry-run")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("would link", result.stdout)
        self.assertFalse(self.target.exists())

    def test_install_is_idempotent_and_resolves_from_another_project(self):
        self.assertEqual(self.cli().returncode, 0)
        for source in installer.skill_sources(self.checkout):
            destination = self.target / source.name
            self.assertEqual(destination.resolve(), source)
            result = subprocess.run(
                [sys.executable, str(destination / "scripts/repo-root.py")],
                cwd=self.root, text=True, capture_output=True,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(Path(result.stdout.strip()), self.checkout)
        result = self.cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("already installed", result.stdout)
        self.assertFalse((self.target / "pcbpilot").exists())
        self.assertFalse((self.target / "pcbpilot").is_symlink())

    def test_every_conflict_is_preflighted_without_partial_install(self):
        names = [source.name for source in installer.skill_sources(self.checkout)]
        for kind in ("directory", "file", "broken", "other-checkout"):
            with self.subTest(kind=kind):
                self.target.mkdir(exist_ok=True)
                conflict = self.target / names[-1]
                if kind == "directory":
                    conflict.mkdir()
                    (conflict / "user-data").write_text("keep")
                elif kind == "file":
                    conflict.write_text("keep")
                elif kind == "broken":
                    conflict.symlink_to(self.root / "missing")
                else:
                    conflict.symlink_to(self.root, target_is_directory=True)
                result = self.cli()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("preserved", result.stderr)
                self.assertFalse((self.target / names[0]).is_symlink())
                if kind == "directory":
                    self.assertEqual((conflict / "user-data").read_text(), "keep")
                    shutil.rmtree(conflict)
                elif kind == "file":
                    self.assertEqual(conflict.read_text(), "keep")
                    conflict.unlink()
                else:
                    self.assertTrue(conflict.is_symlink())
                    conflict.unlink()

    def test_moving_checkout_preserves_internal_discovery(self):
        moved = self.root / "moved checkout"
        self.checkout.rename(moved)
        result = self.cli(checkout=moved)
        self.assertEqual(result.returncode, 0, result.stderr)
        path = self.target / "pcbpilot-repo-lookup/scripts/repo-root.py"
        result = subprocess.run([sys.executable, str(path)], cwd=self.root, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(Path(result.stdout.strip()), moved)

    def test_wrong_repository_is_rejected(self):
        (self.checkout / "go.mod").write_text("module example.com/another-project\n")
        result = subprocess.run(
            [sys.executable, str(self.checkout / ".agents/skills/pcbpilot-repo-lookup/scripts/repo-root.py")],
            text=True, capture_output=True,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not pcbpilot", result.stderr)

    def test_all_scope_includes_real_public_skill_and_migrates_own_old_link(self):
        self.target.mkdir()
        destination = self.target / "pcbpilot"
        old = self.checkout / "skills/pcbpilot"
        destination.symlink_to(old, target_is_directory=True)
        result = self.cli("--scope", "all", "--dry-run")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(os.readlink(destination), str(old))
        self.assertIn("would migrate", result.stdout)
        result = self.cli("--scope", "all")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(destination.resolve(), self.checkout / ".agents/skills/pcbpilot")
        self.assertTrue((destination / "references/orientation.json").is_file())
        self.assertEqual(self.cli("--scope", "all").returncode, 0)

    def test_design_scope_preserves_release_directory_and_foreign_broken_link(self):
        self.target.mkdir()
        destination = self.target / "pcbpilot"
        destination.mkdir()
        (destination / "SKILL.md").write_text("user's release installation")
        self.assertNotEqual(self.cli("--scope", "design").returncode, 0)
        self.assertEqual((destination / "SKILL.md").read_text(), "user's release installation")
        shutil.rmtree(destination)
        old = self.root / "other-checkout/skills/pcbpilot"
        destination.symlink_to(old, target_is_directory=True)
        self.assertNotEqual(self.cli("--scope", "all").returncode, 0)
        self.assertEqual(os.readlink(destination), str(old))

    def test_failed_migration_restores_old_link(self):
        sources = installer.skill_sources(self.checkout, "all")
        self.target.mkdir()
        destination = self.target / "pcbpilot"
        old = self.checkout / "skills/pcbpilot"
        destination.symlink_to(old, target_is_directory=True)
        plan = installer.link_plan(sources, [self.target])
        original = Path.symlink_to

        def fail_later(path, target, **kwargs):
            if path.name == "pcbpilot-repo-maintain":
                raise OSError("simulated link failure")
            return original(path, target, **kwargs)

        with patch.object(Path, "symlink_to", fail_later):
            with self.assertRaises(OSError):
                installer.install(plan, False)
        self.assertEqual(os.readlink(destination), str(old))
        self.assertEqual(list(self.target.iterdir()), [destination])

    def test_multiple_targets_are_preflighted_and_aliases_are_deduplicated(self):
        sources = installer.skill_sources(self.checkout)
        other = self.root / "second client"
        other.mkdir()
        (other / sources[-1].name).mkdir()
        with self.assertRaises(ValueError):
            installer.link_plan(sources, [self.target, other])
        self.assertFalse(self.target.exists())
        alias = self.root / "client alias"
        alias.symlink_to(other, target_is_directory=True)
        (other / sources[-1].name).rmdir()
        plan = installer.link_plan(sources, [other, alias])
        self.assertEqual(len(plan), len(sources))

    def test_broken_destination_parent_is_preserved(self):
        missing = self.root / "missing parent"
        self.target.symlink_to(missing, target_is_directory=True)
        result = self.cli()
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(self.target.is_symlink())
        self.assertFalse(missing.exists())

    def test_link_creation_failure_rolls_back_only_new_links(self):
        sources = installer.skill_sources(self.checkout)
        plan = installer.link_plan(sources, [self.target])
        original = Path.symlink_to

        def fail_second(destination, target, **kwargs):
            if destination.name == sources[-1].name:
                raise OSError("simulated symlink failure")
            return original(destination, target, **kwargs)

        with patch.object(Path, "symlink_to", fail_second):
            with self.assertRaises(OSError):
                installer.install(plan, False)
        self.assertFalse(any(self.target.iterdir()))


class RepositoryContractTests(unittest.TestCase):
    def test_compatibility_entries_are_relative_links_to_canonical_sources(self):
        expected = {
            "CLAUDE.md": "AGENTS.md",
            ".claude": ".agents",
        }
        for name, target in expected.items():
            path = REPO / name
            self.assertTrue(path.is_symlink(), name)
            self.assertEqual(os.readlink(path), target)
            self.assertTrue(path.resolve().exists(), name)
        self.assertFalse((REPO / "skills").exists())
        self.assertFalse((REPO / ".agents/skills/pcbpilot").is_symlink())
        self.assertTrue((REPO / ".agents/skills/pcbpilot/SKILL.md").is_file())

    def test_collaboration_document_links_resolve(self):
        paths = [REPO / "docs" / name for name in
                 ("README.md", "agent-collaboration.md", "release-workflow.md")]
        paths += list((REPO / ".agents/skills").glob("pcbpilot-repo-*/SKILL.md"))
        for path in paths:
            for link in re.findall(r"\[[^\]]*\]\(([^)]+)\)", path.read_text()):
                parsed = urlsplit(link)
                if parsed.scheme or parsed.netloc or not parsed.path:
                    continue
                target = path.parent / unquote(parsed.path)
                self.assertTrue(target.exists(), f"{path}: broken link {link}")


if __name__ == "__main__":
    unittest.main()
