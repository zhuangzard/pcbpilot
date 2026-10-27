package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRollbackNeverDeletesWhatReplacedACreatedSkill(t *testing.T) {
	for name, replace := range map[string]func(t *testing.T, dir string){
		"unmarked dir": func(t *testing.T, dir string) {
			writeTestFile(t, filepath.Join(dir, "notes.md"), "user content")
		},
		"user file": func(t *testing.T, dir string) {
			writeTestFile(t, dir, "a user file")
		},
		"another install": func(t *testing.T, dir string) {
			writeTestFile(t, filepath.Join(dir, versionMarker), "2.0.0\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			isolatedSkillHome(t)
			seedOwned(t, "claude", SkillName, "0.9.0")
			snap, err := TakeSnapshot("v0.9.0", "")
			if err != nil {
				t.Fatal(err)
			}
			serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest()))
			if _, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Journal: SkillJournalDir(snap.Dir)}, nil); err != nil {
				t.Fatal(err)
			}
			created := SkillDirFor("claude", demoSkill)
			if err := os.RemoveAll(created); err != nil {
				t.Fatal(err)
			}
			replace(t, created)
			before := treeSnapshot(t, filepath.Dir(created))
			_, err = RestoreSnapshot(context.Background(), snap)
			if err == nil || !strings.Contains(err.Error(), "conflict") {
				t.Fatalf("rollback must report the conflict: %v", err)
			}
			after := treeSnapshot(t, filepath.Dir(created))
			for p, body := range before {
				if strings.HasPrefix(p, demoSkill) && after[p] != body {
					t.Fatalf("rollback touched %s", p)
				}
			}
			if readMarker(SkillDirFor("claude", SkillName)) != "0.9.0" {
				t.Fatal("the router was not rolled back")
			}
		})
	}
}

func TestVerifyFailureBeforeOrAfterTheSwitchRestoresTheClient(t *testing.T) {
	for _, when := range []string{"staged", "installed"} {
		t.Run(when, func(t *testing.T) {
			isolatedSkillHome(t)
			seedOwned(t, "claude", SkillName, "0.9.0")
			seedOwned(t, "claude", demoSkill, "0.9.0")
			home, _ := os.UserHomeDir()
			skills := filepath.Join(home, ".claude", "skills")
			before := treeSnapshot(t, skills)
			serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest()))
			verify := func(skill, dir string) error {
				staged := strings.Contains(filepath.Base(dir), ".easyeda-skill-stage-")
				if skill == demoSkill && staged == (when == "staged") {
					return errors.New("injected mismatch")
				}
				return nil
			}
			res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Clients: []string{"claude"}, verify: verify}, nil)
			if err == nil || res.Changed != 0 {
				t.Fatalf("verification failure hidden: %+v %v", res, err)
			}
			if !equalTrees(before, treeSnapshot(t, skills)) {
				t.Fatal("client not restored")
			}
			assertNoLeftovers(t, skills)
		})
	}
}

func caseCollisionArchive(t *testing.T, version string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name, body string) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	add(SkillManifestEntry, `{"retired":[],"router":"pcbpilot","schemaVersion":1,"skills":["pcbpilot"]}`)
	add("pcbpilot/SKILL.md", "---\nmetadata:\n  version: \""+version+"\"\n---\n")
	add("pcbpilot/skill.md", "shadow")
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestCaseInsensitiveCollisionsAreRejected(t *testing.T) {
	if tree, err := extractSkillTree(caseCollisionArchive(t, "1.0.0"), "1.0.0", false); err == nil {
		tree.cleanup()
		t.Fatal("extract accepted names that differ only in case")
	}
	dir := localFixture(t, "1.4.9-dev.1", false)
	archive := caseCollisionArchive(t, "1.4.9-dev.1")
	writeTestFile(t, filepath.Join(dir, "skills.tar.gz"), string(archive))
	rewriteChecksum(t, dir, "skills.tar.gz", archive)
	if _, err := ReadLocalBundle(dir); err == nil || !strings.Contains(err.Error(), "case-insensitive") {
		t.Fatalf("local bundle accepted a case collision: %v", err)
	}
}
