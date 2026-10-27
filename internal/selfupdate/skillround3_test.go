package selfupdate

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// localBundleFixture reads a two-Skill dev bundle (retiring pcbpilot-old).
func localBundleFixture(t *testing.T) *LocalBundle {
	t.Helper()
	dir := localFixture(t, "1.4.9-dev.1", false)
	archive := multiTarball(t, "1.4.9-dev.1", twoSkills(), twoSkillManifest("pcbpilot-old"))
	writeTestFile(t, filepath.Join(dir, "skills.tar.gz"), string(archive))
	rewriteChecksum(t, dir, "skills.tar.gz", archive)
	b, err := ReadLocalBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLocalInstallFollowsSyncRulesForConflictsAndLinks(t *testing.T) {
	isolatedSkillHome(t)
	seedOwned(t, "claude", SkillName, "1.4.8")
	user := SkillDirFor("claude", demoSkill)
	writeTestFile(t, filepath.Join(user, "notes.md"), "mine")
	owned := seedOwned(t, "claude", "pcbpilot-old", "1.4.8")
	b := localBundleFixture(t)
	var log strings.Builder
	if err := b.installSkills(&log); err != nil {
		t.Fatal(err)
	}
	if got := treeSnapshot(t, user); len(got) != 1 || got["notes.md"] != "mine" {
		t.Fatalf("an unowned dir was replaced: %v", got)
	}
	if !strings.Contains(log.String(), "Skipped Skill "+user) || isDir(owned) {
		t.Fatalf("conflict not reported or retired Skill kept:\n%s", log.String())
	}
	if err := b.CheckSkill(SkillDirFor("claude", SkillName)); err != nil {
		t.Fatal(err)
	}
	b.Adopt = true
	if err := b.installSkills(io.Discard); err != nil || b.CheckSkillNamed(demoSkill, user) != nil {
		t.Fatalf("adopt: %v", err)
	}
}

func TestLocalInstallLeavesSymlinkedSkillTargetsAlone(t *testing.T) {
	isolatedSkillHome(t)
	seedOwned(t, "claude", SkillName, "1.4.8")
	checkout := filepath.Join(t.TempDir(), "checkout", demoSkill)
	writeTestFile(t, filepath.Join(checkout, "SKILL.md"), "checkout copy")
	if err := os.Symlink(checkout, SkillDirFor("claude", demoSkill)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	var log strings.Builder
	if err := localBundleFixture(t).installSkills(&log); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, filepath.Join(checkout, "SKILL.md")) != "checkout copy" || !strings.Contains(log.String(), "linked to") {
		t.Fatalf("a linked Skill's target was written:\n%s", log.String())
	}
}

func TestLocalInstallFailureAfterTheFirstSwitchRestoresTheClient(t *testing.T) {
	isolatedSkillHome(t)
	seedOwned(t, "claude", SkillName, "1.4.8")
	seedOwned(t, "claude", demoSkill, "1.4.8")
	home, _ := os.UserHomeDir()
	skills := filepath.Join(home, ".claude", "skills")
	b := localBundleFixture(t)
	before := treeSnapshot(t, skills)
	calls := 0
	skillRename = func(from, to string) error {
		calls++
		if calls == 3 { // router backup + switch succeed; the focused Skill's backup fails
			return errors.New("injected failure")
		}
		return os.Rename(from, to)
	}
	defer func() { skillRename = os.Rename }()
	if err := b.installSkills(io.Discard); err == nil {
		t.Fatal("failure hidden")
	}
	after := treeSnapshot(t, skills)
	for name, body := range before {
		if after[name] != body {
			t.Fatalf("client not restored: %s", name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok && !strings.Contains(name, ".pcbpilot-local-backup-") {
			t.Fatalf("new file left behind: %s", name)
		}
	}
}

func TestJournalFailureUndoesTheClientAndKeepsNothingHalfDone(t *testing.T) {
	for _, which := range []string{"copy", "write"} {
		t.Run(which, func(t *testing.T) {
			isolatedSkillHome(t)
			seedOwned(t, "claude", SkillName, "0.9.0")
			seedOwned(t, "claude", "pcbpilot-old", "0.9.0")
			home, _ := os.UserHomeDir()
			skills := filepath.Join(home, ".claude", "skills")
			before := treeSnapshot(t, skills)
			journal := filepath.Join(t.TempDir(), "skill-journal")
			serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest("pcbpilot-old")))
			if which == "copy" {
				journalCopy = func(string, string, bool, bool) error { return errors.New("injected copy failure") }
			} else {
				journalWrite = func(dir string, entries []JournalEntry) error {
					if len(entries) > 0 {
						return errors.New("injected write failure")
					}
					return appendJournal(dir, entries)
				}
			}
			defer func() { journalCopy, journalWrite = copyTree, appendJournal }()
			res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Clients: []string{"claude"}, Journal: journal}, nil)
			if err == nil || res.Changed != 0 || !strings.Contains(err.Error(), "restored") {
				t.Fatalf("journal failure hidden: %+v %v", res, err)
			}
			if !equalTrees(before, treeSnapshot(t, skills)) {
				t.Fatal("client not restored after a journal failure")
			}
			assertNoLeftovers(t, skills)
			var entries []JournalEntry
			if err := ReadJSON(journalFile(journal), &entries); err != nil || len(entries) != 0 {
				t.Fatalf("journal must not record an undone sync: %+v %v", entries, err)
			}
			if trees, _ := filepath.Glob(filepath.Join(journal, "tree-*")); len(trees) != 0 {
				t.Fatalf("partial journal copies left: %v", trees)
			}
		})
	}
}

func TestJournaledRollbackLeavesClientsTheUpdateDidNotTouch(t *testing.T) {
	isolatedSkillHome(t)
	claude := seedOwned(t, "claude", SkillName, "0.9.0")
	codex := seedOwned(t, "codex", SkillName, "0.9.0")
	snap, err := TakeSnapshot("v0.9.0", "")
	if err != nil {
		t.Fatal(err)
	}
	serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest()))
	if _, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Clients: []string{"claude"}, Journal: SkillJournalDir(snap.Dir)}, nil); err != nil {
		t.Fatal(err)
	}
	// The user edits the untouched client after the update.
	writeTestFile(t, filepath.Join(codex, "notes.md"), "edited later")
	if _, err := RestoreSnapshot(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	if readMarker(claude) != "0.9.0" || isDir(SkillDirFor("claude", demoSkill)) {
		t.Fatal("the updated client was not rolled back")
	}
	if readTestFile(t, filepath.Join(codex, "notes.md")) != "edited later" {
		t.Fatal("rollback overwrote a client the update never touched")
	}
}
