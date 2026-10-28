package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// demoSkill is a test-only placeholder for a focused public Skill. Real
// focused Skills live in .agents/skills and are listed in public-skills.json.
const demoSkill = "pcbpilot-demo"

// withPublicSkills stands in for a CLI built from a checkout whose manifest
// lists more Skills.
func withPublicSkills(t *testing.T, skills []string, retired ...string) {
	t.Helper()
	old := publicSkills
	publicSkills = SkillManifest{SchemaVersion: 1, Router: SkillName, Skills: skills, Retired: append([]string{}, retired...)}
	t.Cleanup(func() { publicSkills = old })
}

// multiTarball builds skills.tar.gz with one root per Skill. manifest nil
// writes no pcbpilot/.public-skills.json (a pre-0.11 archive). SKILL.md bodies are
// wrapped with frontmatter declaring version.
func multiTarball(t *testing.T, version string, skills map[string]map[string]string, manifest *SkillManifest) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name string, body []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if manifest != nil {
		raw, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		write(SkillManifestEntry, append(raw, '\n'))
	}
	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, skill := range names {
		files := skills[skill]
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		slices.Sort(paths)
		for _, p := range paths {
			body := files[p]
			if p == "SKILL.md" {
				body = "---\nname: " + skill + "\nmetadata:\n  version: \"" + version + "\"\n---\n" + body
			}
			write(skill+"/"+p, []byte(body))
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func twoSkillManifest(retired ...string) *SkillManifest {
	return &SkillManifest{SchemaVersion: 1, Router: SkillName, Skills: []string{SkillName, demoSkill}, Retired: append([]string{}, retired...)}
}

func twoSkills() map[string]map[string]string {
	return map[string]map[string]string{
		SkillName: {"SKILL.md": "router", "references/shared.md": "shared"},
		demoSkill: {"SKILL.md": "focused", "references/own.md": "[shared](../../pcbpilot/references/shared.md)"},
	}
}

func seedOwned(t *testing.T, client, skill, version string) string {
	t.Helper()
	dir := SkillDirFor(client, skill)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "SKILL.md"), "OLD "+skill)
	if version != "" {
		writeTestFile(t, filepath.Join(dir, versionMarker), version+"\n")
	}
	return dir
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func outcome(t *testing.T, res SyncResult, client, skill string) TargetOutcome {
	t.Helper()
	for _, o := range res.Outcomes {
		if o.Client == client && o.Skill == skill {
			return o
		}
	}
	t.Fatalf("no outcome for %s/%s in %+v", client, skill, res.Outcomes)
	return TargetOutcome{}
}

func TestPublicSkillsMatchCheckoutManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".agents", "skills", SkillManifestName))
	if err != nil {
		t.Fatal(err)
	}
	m, err := parseSkillManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.Skills, publicSkills.Skills) || !slices.Equal(m.Retired, publicSkills.Retired) || m.Router != publicSkills.Router {
		t.Fatalf("selfupdate.publicSkills %+v differs from .agents/skills/%s %+v — update skills.go together with the manifest",
			publicSkills, SkillManifestName, m)
	}
}

func TestSkillManifestValidation(t *testing.T) {
	good := `{"schemaVersion":1,"router":"pcbpilot","skills":["pcbpilot","pcbpilot-demo"],"retired":["pcbpilot-old"]}`
	if _, err := parseSkillManifest([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"schemaVersion":2,"router":"pcbpilot","skills":["pcbpilot"]}`,
		`{"schemaVersion":1,"router":"other","skills":["other"]}`,
		`{"schemaVersion":1,"router":"pcbpilot","skills":["pcbpilot-demo"]}`,
		`{"schemaVersion":1,"router":"pcbpilot","skills":["pcbpilot","user-skill"]}`,
		`{"schemaVersion":1,"router":"pcbpilot","skills":["pcbpilot","pcbpilot-repo-lookup"]}`,
		`{"schemaVersion":1,"router":"pcbpilot","skills":["pcbpilot","../escape"]}`,
		`{"schemaVersion":1,"router":"pcbpilot","skills":["pcbpilot"],"retired":["pcbpilot"]}`,
		`{"schemaVersion":1,"router":"pcbpilot","skills":["pcbpilot","pcbpilot-x"],"retired":["pcbpilot-x"]}`,
		`{"schemaVersion":1,"router":"pcbpilot","skills":["pcbpilot"],"retired":["notes"]}`,
		`{"schemaVersion":1,"router":"pcbpilot","skills":["pcbpilot"],"extra":1}`,
		`not json`,
	} {
		if _, err := parseSkillManifest([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestTargetsListEveryPublicSkillPerClient(t *testing.T) {
	isolatedSkillHome(t)
	withPublicSkills(t, []string{SkillName, demoSkill})
	seedOwned(t, "claude", SkillName, "1.0.0")
	all := Targets(false)
	if len(all) != len(clientOrder)*2 {
		t.Fatalf("want %d targets, got %+v", len(clientOrder)*2, all)
	}
	if all[0].Client != "claude" || all[0].Skill != SkillName || all[1].Skill != demoSkill {
		t.Fatalf("order must be client, then manifest order: %+v", all[:2])
	}
	present := Targets(true)
	if len(present) != 1 || present[0].Skill != SkillName || present[0].Installed != "1.0.0" {
		t.Fatalf("present targets: %+v", present)
	}
	if !ClientInstalled("claude") || ClientInstalled("codex") {
		t.Fatal("ClientInstalled must follow the router dir")
	}
}

func TestSyncInstallsEveryArchiveSkillNextToTheRouter(t *testing.T) {
	isolatedSkillHome(t)
	router := seedOwned(t, "claude", SkillName, "0.9.0")
	writeTestFile(t, filepath.Join(router, "references/retired.md"), "obsolete")
	serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest()))
	// This CLI still only knows the router: the fetched manifest decides.
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0"}, nil)
	if err != nil {
		t.Fatalf("sync: %v %+v", err, res)
	}
	if res.Changed != 2 {
		t.Fatalf("want router + focused Skill changed for claude only, got %+v", res)
	}
	if o := outcome(t, res, "claude", SkillName); o.Status != "updated" {
		t.Fatalf("router: %+v", o)
	}
	if o := outcome(t, res, "claude", demoSkill); o.Status != "created" {
		t.Fatalf("focused Skill must be created next to an installed router: %+v", o)
	}
	if o := outcome(t, res, "codex", demoSkill); o.Status != "skipped" {
		t.Fatalf("a client without the router stays untouched: %+v", o)
	}
	demo := SkillDirFor("claude", demoSkill)
	if readMarker(demo) != "1.0.0" || readMarker(router) != "1.0.0" {
		t.Fatalf("markers: router %q demo %q", readMarker(router), readMarker(demo))
	}
	if got := readTestFile(t, filepath.Join(demo, "references/own.md")); !strings.Contains(got, "../../pcbpilot/references/shared.md") {
		t.Fatalf("focused Skill content: %q", got)
	}
	if _, err := os.Stat(filepath.Join(router, "references/retired.md")); !os.IsNotExist(err) {
		t.Fatal("router dir must be clean-replaced")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(router), SkillManifestName)); !os.IsNotExist(err) {
		t.Fatal("the archive manifest must never be written into a client skills dir")
	}
	assertNoLeftovers(t, filepath.Dir(router))

	// Idempotent: a second sync downloads nothing and changes nothing.
	withPublicSkills(t, []string{SkillName, demoSkill})
	res, err = SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0"}, nil)
	if err != nil || res.Changed != 0 {
		t.Fatalf("second sync: %+v %v", res, err)
	}
	if o := outcome(t, res, "claude", demoSkill); o.Status != "up-to-date" {
		t.Fatalf("second sync focused: %+v", o)
	}
}

func TestSyncCreateMissingInstallsAllSkillsForANewClient(t *testing.T) {
	isolatedSkillHome(t)
	serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest()))
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Clients: []string{"codex"}, CreateMissing: true}, nil)
	if err != nil || res.Changed != 2 {
		t.Fatalf("sync: %+v %v", res, err)
	}
	for _, skill := range []string{SkillName, demoSkill} {
		if readMarker(SkillDirFor("codex", skill)) != "1.0.0" {
			t.Fatalf("%s not installed", skill)
		}
	}
}

func TestSyncOldSingleSkillArchiveInstallsRouterOnly(t *testing.T) {
	isolatedSkillHome(t)
	withPublicSkills(t, []string{SkillName, demoSkill})
	router := seedOwned(t, "claude", SkillName, "1.0.0")
	owned := seedOwned(t, "claude", demoSkill, "1.0.0")
	user := seedOwned(t, "agents", demoSkill, "") // same name, never installed by pcbpilot
	seedOwned(t, "agents", SkillName, "1.0.0")
	old := multiTarball(t, "0.10.1", map[string]map[string]string{SkillName: {"SKILL.md": "old router"}}, nil)
	serveRelease(t, "0.10.1", old)
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "0.10.1"}, nil)
	if err != nil {
		t.Fatalf("sync to a pre-manifest release: %v %+v", err, res)
	}
	if readMarker(router) != "0.10.1" || !strings.Contains(readTestFile(t, filepath.Join(router, "SKILL.md")), "old router") {
		t.Fatal("router not installed from the old archive")
	}
	// The old release does not ship the focused Skill: the pcbpilot-installed
	// copy goes (it would sit at another version), a same-named user dir stays.
	if o := outcome(t, res, "claude", demoSkill); o.Status != "removed" {
		t.Fatalf("owned focused Skill: %+v", o)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatal("owned focused Skill remained")
	}
	if o := outcome(t, res, "agents", demoSkill); o.Status != "kept" {
		t.Fatalf("user Skill: %+v", o)
	}
	if readTestFile(t, filepath.Join(user, "SKILL.md")) != "OLD "+demoSkill {
		t.Fatal("user Skill was modified")
	}
}

func TestSyncRemovesOnlyPcbpilotOwnedRetiredSkills(t *testing.T) {
	isolatedSkillHome(t)
	const retired = "pcbpilot-old"
	for _, c := range []string{"claude", "codex", "agents"} {
		seedOwned(t, c, SkillName, "0.9.0")
	}
	owned := seedOwned(t, "claude", retired, "0.9.0")
	userDir := seedOwned(t, "codex", retired, "")           // no marker: user content
	foreign := filepath.Join(skillsBase("agents"), "notes") // never a candidate: not pcbpilot-named
	writeTestFile(t, filepath.Join(foreign, versionMarker), "1\n")
	archive := multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest(retired))

	// Preserve keeps everything, retired Skills included.
	serveRelease(t, "1.0.0", archive)
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Preserve: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcome(t, res, "claude", retired); o.Status != "kept" || !isDir(owned) {
		t.Fatalf("preserve must keep retired Skills: %+v", o)
	}
	if readMarker(SkillDirFor("claude", SkillName)) != "0.9.0" {
		t.Fatal("preserve must keep the previous router marker")
	}

	res, err = SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Force: true}, nil)
	if err != nil {
		t.Fatalf("sync: %v %+v", err, res)
	}
	if o := outcome(t, res, "claude", retired); o.Status != "removed" {
		t.Fatalf("owned retired Skill: %+v", o)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatal("owned retired Skill remained")
	}
	if o := outcome(t, res, "codex", retired); o.Status != "kept" || !strings.Contains(o.Err, "not installed by pcbpilot") {
		t.Fatalf("unowned retired Skill: %+v", o)
	}
	if readTestFile(t, filepath.Join(userDir, "SKILL.md")) != "OLD "+retired {
		t.Fatal("user dir under a retired name was modified")
	}
	if !isDir(foreign) {
		t.Fatal("a non-pcbpilot dir was touched")
	}
	assertNoLeftovers(t, filepath.Dir(owned))
}

func TestSyncRetiredSymlinkIsLeftAlone(t *testing.T) {
	isolatedSkillHome(t)
	seedOwned(t, "claude", SkillName, "0.9.0")
	checkout := filepath.Join(t.TempDir(), "checkout", "pcbpilot-old")
	writeTestFile(t, filepath.Join(checkout, versionMarker), "0.9.0\n")
	link := SkillDirFor("claude", "pcbpilot-old")
	if err := os.Symlink(checkout, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest("pcbpilot-old")))
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcome(t, res, "claude", "pcbpilot-old"); o.Status != "kept" || o.Linked == "" {
		t.Fatalf("linked retired Skill: %+v", o)
	}
	if _, err := os.Stat(filepath.Join(checkout, versionMarker)); err != nil {
		t.Fatal("checkout behind a link was touched")
	}
}

// A Skill dir linked into a checkout the way installers link directories
// (linkDir: a symlink on Unix, a junction on Windows — Go reports a junction
// as irregular, not a symlink) is a link: reported Linked, never written.
func TestSyncLinkedDirIntoCheckoutIsLeftAlone(t *testing.T) {
	isolatedSkillHome(t)
	checkout := filepath.Join(t.TempDir(), "checkout", ".agents", "skills")
	router, focused := filepath.Join(checkout, SkillName), filepath.Join(checkout, demoSkill)
	for _, d := range []string{router, focused} {
		writeTestFile(t, filepath.Join(d, "SKILL.md"), "CHECKOUT "+filepath.Base(d))
		writeTestFile(t, filepath.Join(d, versionMarker), "0.9.0\n")
	}
	for skill, target := range map[string]string{SkillName: router, demoSkill: focused} {
		link := SkillDirFor("claude", skill)
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		// linkDir needs no privilege (a junction on Windows, a symlink
		// elsewhere): any failure is a test failure.
		if err := linkDir(target, link); err != nil {
			t.Fatalf("linkDir: %v", err)
		}
		if runtime.GOOS == "windows" {
			assertJunction(t, link)
		}
		if got := SkillLinkTarget(link); got == "" || !sameDirForTest(t, got, target) {
			t.Fatalf("SkillLinkTarget(%s) = %q, want %s", link, got, target)
		}
		if ownedSkillDir(link) {
			t.Fatalf("a linked dir must never count as an owned Skill: %s", link)
		}
	}
	serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest()))
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Force: true}, nil)
	if err != nil {
		t.Fatalf("sync: %v %+v", err, res)
	}
	if o := outcome(t, res, "claude", SkillName); o.Status != "linked" || o.Linked == "" {
		t.Fatalf("linked router not reported as linked: %+v", o)
	}
	for _, d := range []string{router, focused} {
		if got := readTestFile(t, filepath.Join(d, "SKILL.md")); got != "CHECKOUT "+filepath.Base(d) {
			t.Fatalf("checkout written through the link: %s = %q", d, got)
		}
		if got := readMarker(d); got != "0.9.0" {
			t.Fatalf("checkout marker rewritten through the link: %s = %q", d, got)
		}
	}
}

func sameDirForTest(t *testing.T, a, b string) bool {
	t.Helper()
	x, err1 := os.Stat(a)
	y, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(x, y)
}

func TestSyncRejectsIncompleteOrMisversionedArchiveBeforeWriting(t *testing.T) {
	isolatedSkillHome(t)
	router := seedOwned(t, "claude", SkillName, "0.9.0")
	cases := map[string][]byte{
		"listed Skill missing": multiTarball(t, "1.0.0", map[string]map[string]string{SkillName: {"SKILL.md": "r"}}, twoSkillManifest()),
		"invalid manifest": multiTarball(t, "1.0.0", twoSkills(),
			&SkillManifest{SchemaVersion: 1, Router: SkillName, Skills: []string{SkillName, "user-notes"}}),
	}
	cases["focused Skill at another version"] = func() []byte {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		add := func(name, body string) {
			tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(body)), Typeflag: tar.TypeReg})
			tw.Write([]byte(body))
		}
		raw, _ := json.Marshal(twoSkillManifest())
		add(SkillManifestEntry, string(raw))
		add("pcbpilot/SKILL.md", "---\nmetadata:\n  version: \"1.0.0\"\n---\n")
		add(demoSkill+"/SKILL.md", "---\nmetadata:\n  version: \"0.9.9\"\n---\n")
		tw.Close()
		gz.Close()
		return buf.Bytes()
	}()
	for name, archive := range cases {
		t.Run(name, func(t *testing.T) {
			serveRelease(t, "1.0.0", archive)
			if _, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0"}, nil); err == nil {
				t.Fatal("accepted a bad archive")
			}
			if readMarker(router) != "0.9.0" || readTestFile(t, filepath.Join(router, "SKILL.md")) != "OLD pcbpilot" {
				t.Fatal("a rejected archive changed the installation")
			}
			if isDir(SkillDirFor("claude", demoSkill)) {
				t.Fatal("a rejected archive created a Skill")
			}
		})
	}
}

func TestSyncPreservePerSkillKeepsLocalEdits(t *testing.T) {
	isolatedSkillHome(t)
	seedOwned(t, "claude", SkillName, "0.9.0")
	demo := seedOwned(t, "claude", demoSkill, "0.9.0")
	writeTestFile(t, filepath.Join(demo, "notes.md"), "local edit")
	serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest()))
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Preserve: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcome(t, res, "claude", demoSkill); o.Status != "preserved" {
		t.Fatalf("%+v", o)
	}
	if readTestFile(t, filepath.Join(demo, "SKILL.md")) != "OLD "+demoSkill || readTestFile(t, filepath.Join(demo, "notes.md")) != "local edit" {
		t.Fatal("preserve overwrote local content")
	}
	if !strings.Contains(readTestFile(t, filepath.Join(demo, "references/own.md")), "shared.md") || readMarker(demo) != "0.9.0" {
		t.Fatal("preserve must add new files and keep the old marker")
	}
}

func TestLocalAssetsReadVersionFromMultiSkillArchive(t *testing.T) {
	archive := multiTarball(t, "1.2.3", twoSkills(), twoSkillManifest())
	v, err := archiveSkillVersion(archive)
	if err != nil || v != "1.2.3" {
		t.Fatalf("archiveSkillVersion: %q %v", v, err)
	}
}

func TestLocalBundleCarriesEveryListedSkill(t *testing.T) {
	dir := localFixture(t, "1.4.9-dev.1", false)
	writeArchive := func(archive []byte) {
		path := filepath.Join(dir, "skills.tar.gz")
		if err := os.WriteFile(path, archive, 0644); err != nil {
			t.Fatal(err)
		}
		rewriteChecksum(t, dir, "skills.tar.gz", archive)
	}
	writeArchive(multiTarball(t, "1.4.9-dev.1", twoSkills(), twoSkillManifest()))
	b, err := ReadLocalBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(b.Manifest.Skills, []string{SkillName, demoSkill}) || b.Skills[demoSkill]["references/own.md"] == nil || b.Skill["SKILL.md"] == nil {
		t.Fatalf("bundle: %+v", b.Manifest)
	}
	skill := t.TempDir()
	for name, data := range b.Skills[demoSkill] {
		writeTestFile(t, filepath.Join(skill, name), string(data))
	}
	writeTestFile(t, filepath.Join(skill, versionMarker), b.Version)
	if err := b.CheckSkillNamed(demoSkill, skill); err != nil {
		t.Fatal(err)
	}
	if b.CheckSkill(skill) == nil {
		t.Fatal("router check accepted the focused Skill's files")
	}
	// An unlisted top-level directory, or a listed Skill that is missing, fails.
	extra := twoSkills()
	extra["pcbpilot-unlisted"] = map[string]string{"SKILL.md": "x"}
	writeArchive(multiTarball(t, "1.4.9-dev.1", extra, twoSkillManifest()))
	if _, err := ReadLocalBundle(dir); err == nil || !strings.Contains(err.Error(), "not listed") {
		t.Fatalf("unlisted Skill dir: %v", err)
	}
	writeArchive(multiTarball(t, "1.4.9-dev.1", map[string]map[string]string{SkillName: {"SKILL.md": "r"}}, twoSkillManifest()))
	if _, err := ReadLocalBundle(dir); err == nil {
		t.Fatal("accepted a bundle missing a listed Skill")
	}
	// A pre-manifest single-Skill bundle still reads as router-only.
	writeArchive(multiTarball(t, "1.4.9-dev.1", map[string]map[string]string{SkillName: {"SKILL.md": "r"}}, nil))
	if b, err = ReadLocalBundle(dir); err != nil || !slices.Equal(b.Manifest.Skills, []string{SkillName}) {
		t.Fatalf("legacy bundle: %+v %v", b, err)
	}
}

func rewriteChecksum(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	path := filepath.Join(dir, "checksums.txt")
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(readTestFile(t, path)), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == name {
			line = checksumHex(data) + "  " + name
		}
		out = append(out, line)
	}
	writeTestFile(t, path, strings.Join(out, "\n")+"\n")
}

func TestLocalInstallWritesEverySkillForRouterClients(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture executable is a POSIX shell script")
	}
	isolatedSkillHome(t)
	seedOwned(t, "agents", SkillName, "1.4.8")
	retired := seedOwned(t, "agents", "pcbpilot-old", "1.4.8")
	dir := localFixture(t, "1.4.9-dev.1", false)
	archive := multiTarball(t, "1.4.9-dev.1", twoSkills(), twoSkillManifest("pcbpilot-old"))
	writeTestFile(t, filepath.Join(dir, "skills.tar.gz"), string(archive))
	rewriteChecksum(t, dir, "skills.tar.gz", archive)
	b, err := ReadLocalBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "pcbpilot")
	writeTestFile(t, bin, "old")
	if err := b.Install(bin, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, skill := range []string{SkillName, demoSkill} {
		if err := b.CheckSkillNamed(skill, SkillDirFor("agents", skill)); err != nil {
			t.Fatal(err)
		}
	}
	if isDir(retired) || isDir(SkillDirFor("claude", demoSkill)) {
		t.Fatal("retired Skill kept, or a client without the router got Skills")
	}
}

func TestRollbackRemovesFocusedSkillAddedByTheUpdate(t *testing.T) {
	isolatedSkillHome(t)
	withPublicSkills(t, []string{SkillName, demoSkill})
	router := seedOwned(t, "claude", SkillName, "0.9.0")
	user := SkillDirFor("codex", demoSkill) // a user dir in a client the snapshot did not cover
	writeTestFile(t, filepath.Join(user, versionMarker), "x\n")
	snap, err := TakeSnapshot("v0.9.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Skills) != 2 || snap.Skills[0].Skill != SkillName {
		// claude router + the codex user dir (present, unlinked) are both saved.
		t.Fatalf("snapshot skills: %+v", snap.Skills)
	}
	serveRelease(t, "1.0.0", multiTarball(t, "1.0.0", twoSkills(), twoSkillManifest()))
	if _, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "1.0.0", Clients: []string{"claude"}}, nil); err != nil {
		t.Fatal(err)
	}
	added := SkillDirFor("claude", demoSkill)
	if !isDir(added) {
		t.Fatal("update did not add the focused Skill")
	}
	restored, err := RestoreSnapshot(context.Background(), snap)
	if err != nil {
		t.Fatal(err)
	}
	if isDir(added) || readMarker(router) != "0.9.0" {
		t.Fatalf("rollback: added Skill kept or router not restored (%v)", restored)
	}
	if !slices.Contains(restored, "skill:claude/"+demoSkill+" removed") {
		t.Fatalf("restored: %v", restored)
	}
	if !isDir(user) {
		t.Fatal("rollback removed a dir it had saved")
	}
}

// assertJunction: link is a directory junction as Go >= 1.23 reports it
// (irregular, not a symlink), so the junction branch of SkillLinkTarget is
// what a test using it exercises.
func assertJunction(t *testing.T, link string) {
	t.Helper()
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || fi.Mode()&os.ModeIrregular == 0 {
		t.Fatalf("%s is not a junction as Go reports one: mode %v", link, fi.Mode())
	}
}

// SkillLinkTarget per kind of entry: a plain directory and a missing path are
// not links; a junction (Windows, always created as one here) and a symlink
// (where the OS lets this process create one) resolve to their target.
func TestSkillLinkTargetKinds(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "checkout", "pcbpilot")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(root, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := SkillLinkTarget(plain); got != "" {
		t.Fatalf("plain directory classified as linked to %q", got)
	}
	if got := SkillLinkTarget(filepath.Join(root, "missing")); got != "" {
		t.Fatalf("missing path classified as linked to %q", got)
	}
	if runtime.GOOS == "windows" {
		junction := filepath.Join(root, "junction")
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
			t.Fatalf("mklink /J: %v: %s", err, out)
		}
		assertJunction(t, junction)
		if got := SkillLinkTarget(junction); got == "" || !sameDirForTest(t, got, target) {
			t.Fatalf("junction: SkillLinkTarget = %q, want %s", got, target)
		}
	}
	symlink := filepath.Join(root, "symlink")
	if err := os.Symlink(target, symlink); err != nil {
		if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314)) { // ERROR_PRIVILEGE_NOT_HELD
			t.Logf("symlink case not checked: %v", err)
			return
		}
		t.Fatal(err)
	}
	if got := SkillLinkTarget(symlink); got == "" || !sameDirForTest(t, got, target) {
		t.Fatalf("symlink: SkillLinkTarget = %q, want %s", got, target)
	}
}
