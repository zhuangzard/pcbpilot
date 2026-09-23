package selfupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolatedSkillHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv(PreserveEnv, "")
	return home
}

func seedSkill(t *testing.T, client string) string {
	t.Helper()
	dir := skillDir(client)
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"SKILL.md": "OLD", ".version": "1.4.1\n", "references/retired.md": "obsolete"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSkillTargetsHonorClientHomes(t *testing.T) {
	home := isolatedSkillHome(t)
	for _, entry := range []struct{ client, env string }{{"codex", "CODEX_HOME"}, {"claude", "CLAUDE_CONFIG_DIR"}} {
		custom := filepath.Join(home, entry.client+" custom")
		t.Setenv(entry.env, custom)
		want := filepath.Join(custom, "skills", SkillName)
		if got := skillDir(entry.client); got != want {
			t.Errorf("%s: got %s, want %s", entry.client, got, want)
		}
	}
	if got, want := skillDir("agents"), filepath.Join(home, ".agents", "skills", SkillName); got != want {
		t.Errorf("agents: got %s, want %s", got, want)
	}
}

func TestSkillSyncRemovesRetiredFiles(t *testing.T) {
	isolatedSkillHome(t)
	dir := seedSkill(t, "codex")
	serveRelease(t, "1.4.2", makeVersionedTarball(t, "1.4.2", map[string]string{"SKILL.md": "NEW"}, false))
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.4.2", Clients: []string{"codex"}}, nil)
	if err != nil || res.Changed != 1 {
		t.Fatalf("sync: %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "references/retired.md")); !os.IsNotExist(err) {
		t.Fatalf("retired file remained: %v", err)
	}
	if readMarker(dir) != "1.4.2" {
		t.Fatal("target marker not installed")
	}
	assertNoLeftovers(t, filepath.Dir(dir))
}

func TestSkillPreserveDoesNotClaimReleaseParity(t *testing.T) {
	isolatedSkillHome(t)
	dir := seedSkill(t, "codex")
	serveRelease(t, "1.4.2", makeVersionedTarball(t, "1.4.2", map[string]string{"SKILL.md": "NEW", "new.md": "ADDED"}, false))
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.4.2", Clients: []string{"codex"}, Preserve: true}, nil)
	if err != nil || res.Outcomes[0].Status != "preserved" {
		t.Fatalf("%+v %v", res, err)
	}
	if got := readMarker(dir); got != "1.4.1" {
		t.Fatalf("mixed content marked %s", got)
	}
	old, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if string(old) != "OLD" {
		t.Fatal("local file overwritten")
	}
	if _, err := os.Stat(filepath.Join(dir, "new.md")); err != nil {
		t.Fatal("new file missing", err)
	}
}

func TestSkillSyncFailedStageKeepsOldInstallationAndReturnsError(t *testing.T) {
	isolatedSkillHome(t)
	dir := seedSkill(t, "codex")
	// A local file conflicts with a release directory. Failure must happen in the
	// staging tree even when another file was copied earlier in lexical order.
	if err := os.WriteFile(filepath.Join(dir, "z-conflict"), []byte("LOCAL"), 0644); err != nil {
		t.Fatal(err)
	}
	serveRelease(t, "1.4.2", makeVersionedTarball(t, "1.4.2", map[string]string{"SKILL.md": "NEW", "a-new.md": "NEW", "z-conflict/item.md": "NEW"}, false))
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.4.2", Clients: []string{"codex"}, Preserve: true}, nil)
	if err == nil || res.Outcomes[0].Status != "error" {
		t.Fatalf("failure hidden: %+v %v", res, err)
	}
	if got := readMarker(dir); got != "1.4.1" {
		t.Fatalf("marker changed: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "a-new.md")); !os.IsNotExist(err) {
		t.Fatalf("partial copy changed old installation: %v", err)
	}
	assertNoLeftovers(t, filepath.Dir(dir))
}

func TestSkillSyncChecksumsAndPackageValidation(t *testing.T) {
	for _, tc := range []struct {
		name, manifest string
		status         int
		files          map[string]string
		ok             bool
	}{
		{name: "mismatch", manifest: strings.Repeat("0", 64) + "  skills.tar.gz\n", status: 200, files: map[string]string{"SKILL.md": "NEW"}},
		{name: "missing entry", manifest: strings.Repeat("0", 64) + "  other.bin\n", status: 200, files: map[string]string{"SKILL.md": "NEW"}},
		{name: "malformed hash", manifest: "BAD  skills.tar.gz\n", status: 200, files: map[string]string{"SKILL.md": "NEW"}},
		{name: "server failure", status: 503, files: map[string]string{"SKILL.md": "NEW"}},
		{name: "legacy absent checksum", status: 404, files: map[string]string{"SKILL.md": "NEW"}, ok: true},
		{name: "missing skill entry", status: 404, files: map[string]string{"README.md": "NEW"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedSkillHome(t)
			dir := seedSkill(t, "codex")
			serveRelease(t, "1.4.2", makeVersionedTarball(t, "1.4.2", tc.files, false))
			sums := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.manifest) }))
			defer sums.Close()
			checksumsURL = func(string) string { return sums.URL }
			res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.4.2", Clients: []string{"codex"}}, nil)
			if tc.ok {
				if err != nil || res.Changed != 1 {
					t.Fatalf("legacy compatibility failed: %+v %v", res, err)
				}
				return
			}
			if err == nil || res.Changed != 0 {
				t.Fatalf("invalid package accepted: %+v %v", res, err)
			}
			if readMarker(dir) != "1.4.1" {
				t.Fatal("old installation changed")
			}
			body, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
			if string(body) != "OLD" {
				t.Fatal("old content changed")
			}
		})
	}
}

func TestSkillSyncUnknownClientReturnsError(t *testing.T) {
	isolatedSkillHome(t)
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.4.2", Clients: []string{"typo"}}, nil)
	if err == nil || len(res.Outcomes) != 1 || res.Outcomes[0].Status != "error" {
		t.Fatalf("unknown client succeeded: %+v %v", res, err)
	}
}

func TestStartupSyncPinsSkillToRunningCLI(t *testing.T) {
	isolatedSkillHome(t)
	dir := seedSkill(t, "codex")
	serveRelease(t, "1.5.0", makeVersionedTarball(t, "1.4.2", map[string]string{"SKILL.md": "CLI-MATCHING"}, false))
	var requested string
	old := tarballURL
	tarballURL = func(v string) string { requested = v; return old(v) }
	StartupSync(context.Background(), "v1.4.2", nil)
	if requested != "1.4.2" || readMarker(dir) != "1.4.2" {
		t.Fatalf("startup drifted: downloaded %s, installed %s", requested, readMarker(dir))
	}
}

func TestStartupSyncSkipsDevelopmentBuilds(t *testing.T) {
	isolatedSkillHome(t)
	dir := seedSkill(t, "codex")
	old := latestAPIURL
	latestAPIURL = func() string { t.Fatal("development build queried release"); return "" }
	t.Cleanup(func() { latestAPIURL = old })
	StartupSync(context.Background(), "v1.4.2-1-g123-dirty", nil)
	if readMarker(dir) != "1.4.1" {
		t.Fatal("development startup changed installed Skill")
	}
}

func TestSkillPreserveNewInstallationHasReleaseMarker(t *testing.T) {
	isolatedSkillHome(t)
	serveRelease(t, "1.4.2", makeVersionedTarball(t, "1.4.2", map[string]string{"SKILL.md": "NEW"}, false))
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.4.2", Clients: []string{"codex"}, Preserve: true, CreateMissing: true}, nil)
	if err != nil || res.Outcomes[0].Status != "created" || readMarker(skillDir("codex")) != "1.4.2" {
		t.Fatalf("new installation: %+v %v", res, err)
	}
}

func TestSkillPreserveUnknownVersionStaysUnknown(t *testing.T) {
	isolatedSkillHome(t)
	dir := seedSkill(t, "codex")
	if err := os.Remove(filepath.Join(dir, versionMarker)); err != nil {
		t.Fatal(err)
	}
	serveRelease(t, "1.4.2", makeVersionedTarball(t, "1.4.2", map[string]string{"SKILL.md": "NEW", ".version": "1.4.2\n"}, false))
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.4.2", Clients: []string{"codex"}, Preserve: true}, nil)
	if err != nil || res.Outcomes[0].Status != "preserved" || readMarker(dir) != "" {
		t.Fatalf("unknown local contents claimed release: %+v %v", res, err)
	}
}

func TestSkillSyncFailureIsolatedAcrossClients(t *testing.T) {
	home := isolatedSkillHome(t)
	// Existing parent is a file, so creating the Claude destination fails while
	// Codex remains writable. The combined command must still report failure.
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "not-a-dir"))
	if err := os.WriteFile(filepath.Join(home, "not-a-dir"), []byte("BLOCKER"), 0644); err != nil {
		t.Fatal(err)
	}
	serveRelease(t, "1.4.2", makeVersionedTarball(t, "1.4.2", map[string]string{"SKILL.md": "NEW"}, false))
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.4.2", CreateMissing: true}, nil)
	if err == nil || len(res.Outcomes) != 3 || res.Changed != 2 {
		t.Fatalf("multi-client failure hidden: %+v %v", res, err)
	}
	if readMarker(skillDir("codex")) != "1.4.2" {
		t.Fatal("writable client did not install")
	}
}

func TestSkillSyncRejectsWrongMetadataEvenWithValidChecksum(t *testing.T) {
	for _, body := range []string{
		"No metadata",
		skillDocument("1.4.1", "OLD RELEASE"),
		"---\nname: pcbpilot\n---\nmetadata:\n  version: 1.4.2\n",
		"---\nmetadata:\n  version: 1.4.2\n  version: 1.4.1\n---\n",
	} {
		t.Run(body, func(t *testing.T) {
			isolatedSkillHome(t)
			dir := seedSkill(t, "codex")
			serveRelease(t, "1.4.2", makeTarball(t, map[string]string{"SKILL.md": body}, false))
			res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.4.2", Clients: []string{"codex"}}, nil)
			if err == nil || res.Changed != 0 || readMarker(dir) != "1.4.1" {
				t.Fatalf("accepted incompatible metadata: %+v %v", res, err)
			}
		})
	}
}

func TestSkillLegacyMissingMetadataCompatibilityIsBounded(t *testing.T) {
	for _, target := range []string{"0.9.0", "1.4.2"} {
		t.Run(target, func(t *testing.T) {
			isolatedSkillHome(t)
			srv := serveRelease(t, target, makeTarball(t, map[string]string{"SKILL.md": "Historical skill"}, false))
			checksumsURL = func(string) string { return srv.URL + "/not-found" }
			res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: target, Clients: []string{"codex"}, CreateMissing: true}, nil)
			if target == "0.9.0" {
				if err != nil || res.Changed != 1 {
					t.Fatalf("legacy fallback failed: %+v %v", res, err)
				}
			} else if err == nil || res.Changed != 0 {
				t.Fatalf("modern package bypassed metadata: %+v %v", res, err)
			}
		})
	}
}

func TestSkillClientHomesRejectRelativePathsBeforeDownload(t *testing.T) {
	isolatedSkillHome(t)
	for _, entry := range []struct{ client, env string }{{"codex", "CODEX_HOME"}, {"claude", "CLAUDE_CONFIG_DIR"}} {
		t.Run(entry.client, func(t *testing.T) {
			t.Setenv(entry.env, "relative/client")
			if err := ValidateClients([]string{entry.client}); err == nil {
				t.Fatal("relative home was accepted")
			}
			res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.4.2", Clients: []string{entry.client}, CreateMissing: true}, nil)
			if err == nil || len(res.Outcomes) != 1 || res.Outcomes[0].Status != "error" {
				t.Fatalf("relative home was used: %+v %v", res, err)
			}
		})
	}
}
