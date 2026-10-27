package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// treeSnapshot maps every file under root to its content.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			b, _ := os.ReadFile(p)
			out[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	return out
}

func equalTrees(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestSyncFailedSwitchAtAnyPositionRestoresTheWholeClient(t *testing.T) {
	isolatedSkillHome(t)
	seedOwned(t, "claude", SkillName, "0.9.0")
	seedOwned(t, "claude", demoSkill, "0.9.0")
	seedOwned(t, "claude", "pcbpilot-old", "0.9.0")
	home, _ := os.UserHomeDir()
	skills := filepath.Join(home, ".claude", "skills")
	before := treeSnapshot(t, skills)
	serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest("pcbpilot-old")))
	// router (backup + switch), focused (backup + switch), retired (backup) = 5.
	for fail := 1; fail <= 5; fail++ {
		calls := 0
		skillRename = func(from, to string) error {
			calls++
			if calls == fail {
				return errors.New("injected failure")
			}
			return os.Rename(from, to)
		}
		res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Clients: []string{"claude"}}, nil)
		skillRename = os.Rename
		if err == nil || res.Changed != 0 {
			t.Fatalf("switch %d: failure hidden: %+v %v", fail, res, err)
		}
		if after := treeSnapshot(t, skills); !equalTrees(before, after) {
			t.Fatalf("switch %d: client not restored:\nbefore %v\nafter  %v", fail, before, after)
		}
		for _, o := range res.Outcomes {
			if o.Status != "error" && o.Status != "skipped" {
				t.Fatalf("switch %d: outcome claims success: %+v", fail, o)
			}
		}
	}
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Clients: []string{"claude"}}, nil)
	if err != nil || res.Changed != 3 {
		t.Fatalf("clean run: %+v %v", res, err)
	}
	assertNoLeftovers(t, skills)
}

func TestSyncStagingFailureChangesNothingForThatClient(t *testing.T) {
	isolatedSkillHome(t)
	router := seedOwned(t, "claude", SkillName, "0.9.0")
	seedOwned(t, "claude", demoSkill, "0.9.0")
	serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest()))
	home, _ := os.UserHomeDir()
	skills := filepath.Join(home, ".claude", "skills")
	before := treeSnapshot(t, skills)
	renames := 0
	skillRename = func(from, to string) error { renames++; return os.Rename(from, to) }
	// The router stages fine; the focused Skill (staged second) fails.
	stageCopy = func(src, dst string, preserve, skipMarker bool) error {
		if filepath.Base(src) == demoSkill {
			return errors.New("injected staging failure")
		}
		return copyTree(src, dst, preserve, skipMarker)
	}
	defer func() { skillRename, stageCopy = os.Rename, copyTree }()
	if _, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Clients: []string{"claude"}}, nil); err == nil {
		t.Fatal("staging failure hidden")
	}
	if renames != 0 || readMarker(router) != "0.9.0" || !equalTrees(before, treeSnapshot(t, skills)) {
		t.Fatalf("a staging failure switched something (renames=%d)", renames)
	}
	assertNoLeftovers(t, skills)
}

func TestFileUnderASkillNameIsAConflict(t *testing.T) {
	isolatedSkillHome(t)
	seedOwned(t, "claude", SkillName, "0.9.0")
	demo := SkillDirFor("claude", demoSkill)
	writeTestFile(t, demo, "a file, not a Skill")
	serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest()))
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Clients: []string{"claude"}}, nil)
	if err != nil || outcome(t, res, "claude", demoSkill).Status != "conflict" || readTestFile(t, demo) != "a file, not a Skill" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestFocusedSkillNeverReplacesAnUnownedDirWithoutAdopt(t *testing.T) {
	isolatedSkillHome(t)
	seedOwned(t, "claude", SkillName, "0.9.0")
	user := SkillDirFor("claude", demoSkill)
	writeTestFile(t, filepath.Join(user, "notes.md"), "mine")
	serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest()))
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Clients: []string{"claude"}, Force: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcome(t, res, "claude", demoSkill); o.Status != "conflict" || !strings.Contains(o.Err, "--adopt") {
		t.Fatalf("unowned dir: %+v", o)
	}
	if got := treeSnapshot(t, user); len(got) != 1 || got["notes.md"] != "mine" {
		t.Fatalf("user files changed: %v", got)
	}
	if problems := VerifyInstalledSkills("1.0.0"); len(problems) != 0 {
		t.Fatalf("a conflict is reported, not a verification failure: %v", problems)
	}
	res, err = SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Clients: []string{"claude"}, Adopt: true}, nil)
	if err != nil || outcome(t, res, "claude", demoSkill).Status != "updated" || readMarker(user) != "1.0.0" {
		t.Fatalf("--adopt: %+v %v", res, err)
	}
}

func TestRouterManifestCompletesSkillsTheCLIDoesNotKnow(t *testing.T) {
	isolatedSkillHome(t)
	// The router is already current (installed by a newer release) and names a
	// focused Skill this CLI has never heard of; the dir is missing.
	router := seedOwned(t, "claude", SkillName, "1.0.0")
	raw := `{"retired":[],"router":"pcbpilot","schemaVersion":1,"skills":["pcbpilot","` + demoSkill + `"]}`
	writeTestFile(t, filepath.Join(router, installedManifestName), raw)
	if p := VerifyInstalledSkills("1.0.0"); len(p) != 1 || !strings.Contains(p[0], "missing") {
		t.Fatalf("verify must report the missing focused Skill: %v", p)
	}
	if tg := Targets(false); !slices.ContainsFunc(tg, func(x SkillTarget) bool { return x.Client == "claude" && x.Skill == demoSkill }) {
		t.Fatal("status must list Skills named by the installed router's manifest")
	}
	serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest()))
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Clients: []string{"claude"}}, nil)
	if err != nil || outcome(t, res, "claude", demoSkill).Status != "created" {
		t.Fatalf("missing focused Skill not completed: %+v %v", res, err)
	}
	if p := VerifyInstalledSkills("1.0.0"); len(p) != 0 {
		t.Fatalf("after completion: %v", p)
	}
}

func TestJournaledRollbackUndoesExactlyWhatTheSyncChanged(t *testing.T) {
	isolatedSkillHome(t)
	router := seedOwned(t, "claude", SkillName, "0.9.0")
	retired := seedOwned(t, "claude", "pcbpilot-old", "0.9.0")
	writeTestFile(t, filepath.Join(retired, "keep.md"), "retired content")
	snap, err := TakeSnapshot("v0.9.0", "")
	if err != nil {
		t.Fatal(err)
	}
	// This CLI knows only the router; the release adds a focused Skill and
	// retires pcbpilot-old. Both must be undone from the journal alone.
	serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest("pcbpilot-old")))
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Journal: SkillJournalDir(snap.Dir)}, nil)
	if err != nil || res.Changed != 3 {
		t.Fatalf("sync: %+v %v", res, err)
	}
	added := SkillDirFor("claude", demoSkill)
	if !isDir(added) || isDir(retired) {
		t.Fatal("sync did not add/remove as expected")
	}
	restored, err := RestoreSnapshot(context.Background(), snap)
	if err != nil {
		t.Fatalf("restore: %v (%v)", err, restored)
	}
	if isDir(added) {
		t.Fatal("rollback kept the focused Skill the update created")
	}
	if readMarker(router) != "0.9.0" || readTestFile(t, filepath.Join(retired, "keep.md")) != "retired content" {
		t.Fatalf("rollback did not restore the router and the removed retired Skill (%v)", restored)
	}
}

func TestManifestArchiveRejectsLinksTraversalAndUnlistedRoots(t *testing.T) {
	manifest := twoSkillManifest()
	build := func(extra func(tw *tar.Writer)) []byte {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		base := multiTarball(t, "1.0.0", twoSkills(), manifest)
		r, _ := gzip.NewReader(bytes.NewReader(base))
		tr := tar.NewReader(r)
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			body := new(bytes.Buffer)
			body.ReadFrom(tr)
			tw.WriteHeader(h)
			tw.Write(body.Bytes())
		}
		extra(tw)
		tw.Close()
		gz.Close()
		return buf.Bytes()
	}
	for name, extra := range map[string]func(*tar.Writer){
		"symlink": func(tw *tar.Writer) {
			tw.WriteHeader(&tar.Header{Name: "pcbpilot/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside"})
		},
		"hardlink": func(tw *tar.Writer) {
			tw.WriteHeader(&tar.Header{Name: "pcbpilot/hard", Typeflag: tar.TypeLink, Linkname: "pcbpilot/SKILL.md"})
		},
		"traversal": func(tw *tar.Writer) {
			tw.WriteHeader(&tar.Header{Name: "pcbpilot/../../evil", Typeflag: tar.TypeReg, Mode: 0644})
		},
		"unlisted root": func(tw *tar.Writer) {
			tw.WriteHeader(&tar.Header{Name: "pcbpilot-other/SKILL.md", Typeflag: tar.TypeReg, Mode: 0644})
		},
	} {
		t.Run(name, func(t *testing.T) {
			if tree, err := extractSkillTree(build(extra), "1.0.0", false); err == nil {
				tree.cleanup()
				t.Fatal("accepted an unsafe manifest archive")
			}
		})
	}
}
