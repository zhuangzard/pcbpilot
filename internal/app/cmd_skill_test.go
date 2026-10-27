package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
	"github.com/zhuangzard/pcbpilot/internal/version"
)

// demoSkill is a test-only focused public Skill next to the router.
const demoSkill = "pcbpilot-demo"

func multiSkillHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv(selfupdate.PreserveEnv, "")
	restore, err := selfupdate.OverridePublicSkillsForTest([]string{selfupdate.SkillName, demoSkill})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	orig := version.Version
	version.Version = "v1.0.0"
	t.Cleanup(func() { version.Version = orig })
	return home
}

func seedSkillDir(t *testing.T, client, skill, marker string) string {
	t.Helper()
	dir := selfupdate.SkillDirFor(client, skill)
	mustWrite(t, filepath.Join(dir, "SKILL.md"), []byte(skillDoc(marker)), 0o644)
	mustWrite(t, filepath.Join(dir, ".version"), []byte(marker+"\n"), 0o644)
	return dir
}

// serveSkillRelease answers /latest and /download/v<version>/{skills.tar.gz,checksums.txt}.
func serveSkillRelease(t *testing.T, version string, archive []byte) {
	t.Helper()
	sums := checksumsFor(map[string][]byte{"skills.tar.gz": archive})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"v%s"}`, version)
		case "/download/v" + version + "/checksums.txt":
			w.Write(sums)
		case "/download/v" + version + "/skills.tar.gz":
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(selfupdate.ReleaseBaseEnv, srv.URL)
}

func twoSkillArchive(t *testing.T, version string) []byte {
	t.Helper()
	manifest := `{"retired": [], "router": "pcbpilot", "schemaVersion": 1, "skills": ["pcbpilot", "` + demoSkill + `"]}` + "\n"
	return tarGzFiles(t, map[string]string{
		"public-skills.json":                manifest,
		"pcbpilot/SKILL.md":                 skillDoc(version),
		demoSkill + "/SKILL.md":             skillDoc(version),
		demoSkill + "/references/review.md": "[shared](../../pcbpilot/SKILL.md)",
	})
}

func runSkillCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := newSkillCmd(&out, &errOut)
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String() + errOut.String(), err
}

func TestSkillStatusReportsEverySkillAndAlignsOnlyWhenAllCurrent(t *testing.T) {
	multiSkillHome(t)
	serveSkillRelease(t, "1.0.0", twoSkillArchive(t, "1.0.0"))
	seedSkillDir(t, "claude", selfupdate.SkillName, "1.0.0")

	status := func() skillStatusReport {
		t.Helper()
		out, err := runSkillCmd(t, "status", "--json")
		if err != nil {
			t.Fatalf("skill status: %v\n%s", err, out)
		}
		var rep skillStatusReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("json: %v\n%s", err, out)
		}
		return rep
	}
	rep := status()
	if len(rep.Skills) != 2 || rep.Aligned {
		t.Fatalf("router current but focused Skill missing must not be aligned: %+v", rep)
	}
	seen := map[string]bool{}
	for _, tg := range rep.Targets {
		seen[tg.Client+"/"+tg.Skill] = true
	}
	if !seen["claude/pcbpilot"] || !seen["claude/"+demoSkill] || !seen["codex/"+demoSkill] {
		t.Fatalf("status must list every client × Skill: %v", seen)
	}
	out, _ := runSkillCmd(t, "status")
	if !strings.Contains(out, demoSkill) || !strings.Contains(out, "missing") || !strings.Contains(out, "Not aligned") {
		t.Fatalf("text status must name the missing focused Skill:\n%s", out)
	}

	demo := seedSkillDir(t, "claude", demoSkill, "0.9.0")
	if status().Aligned {
		t.Fatal("a focused Skill at another version must not be aligned")
	}
	mustWrite(t, filepath.Join(demo, ".version"), []byte("1.0.0\n"), 0o644)
	if !status().Aligned {
		t.Fatal("all Skills of every router client at the CLI version must be aligned")
	}
}

func TestSkillSyncAddsFocusedSkillsAndCheckSkillsReportsThem(t *testing.T) {
	multiSkillHome(t)
	serveSkillRelease(t, "1.0.0", twoSkillArchive(t, "1.0.0"))
	router := seedSkillDir(t, "claude", selfupdate.SkillName, "0.9.0")

	rows := checkSkills("1.0.0", nil)
	byLabel := map[string]updateSkillRow{}
	for _, r := range rows {
		byLabel[selfupdate.SkillLabel(r.Client, r.Skill)] = r
	}
	if byLabel["claude"].Status != "behind" || byLabel["claude/"+demoSkill].Status != "behind" {
		t.Fatalf("missing focused Skill next to a router must count as behind: %+v", byLabel)
	}
	if byLabel["codex/"+demoSkill].Status != "not-installed" {
		t.Fatalf("codex has no router: %+v", byLabel["codex/"+demoSkill])
	}
	notes := updateNotes(updateReport{Target: "1.0.0", Skills: rows})
	for _, n := range notes {
		if strings.Contains(n, demoSkill) && strings.Contains(n, "create-missing") {
			t.Fatalf("focused Skills follow the router; only one create-missing note per client: %v", notes)
		}
	}

	out, err := runSkillCmd(t, "sync", "--version", "1.0.0")
	if err != nil {
		t.Fatalf("skill sync: %v\n%s", err, out)
	}
	if !strings.Contains(out, demoSkill) || !strings.Contains(out, "created") {
		t.Fatalf("sync output must name the created focused Skill:\n%s", out)
	}
	for _, dir := range []string{router, selfupdate.SkillDirFor("claude", demoSkill)} {
		if b, _ := os.ReadFile(filepath.Join(dir, ".version")); strings.TrimSpace(string(b)) != "1.0.0" {
			t.Fatalf("%s marker %q", dir, b)
		}
	}
	for _, r := range checkSkills("1.0.0", []string{"claude"}) {
		if r.Status != "current" {
			t.Fatalf("after sync every claude Skill is current: %+v", r)
		}
	}
}
