// Package selfupdate keeps the locally-installed pcbpilot skill directories
// (~/.claude, ~/.codex and the shared ~/.agents skill roots) in sync with a
// released version, so a user upgrading the CLI never has to hand-copy the skill.
//
// It deliberately does NOT touch the EasyEDA connector .eext: sideloaded
// extensions have no official in-place auto-update (that is a marketplace-only
// feature), so the daemon can only DETECT a stale connector and log an
// actionable re-import notice (see internal/daemon staleConnectorNotice).
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// RepoSlug is the GitHub owner/repo this build updates from (release assets,
// latest-version checks, connector download hints). It is a distribution
// setting, not the Go module path: a fork sets it at build time with
//
//	-X 'github.com/zhuangzard/pcbpilot/internal/selfupdate.RepoSlug=<owner>/<repo>'
//
// (the Makefile passes RELEASE_REPO), and PCBPILOT_RELEASE_REPO overrides it
// at run time. Use Repo() rather than reading the variable.
var RepoSlug = "zhuangzard/pcbpilot"

// RepoEnv overrides RepoSlug at run time (e.g. to test another channel).
const RepoEnv = "PCBPILOT_RELEASE_REPO"

// ReleaseBaseEnv points every release URL (latest-version lookup and asset
// downloads) at another server: {base}/latest answers {"tag_name":"vX.Y.Z"} and
// {base}/download/vX.Y.Z/<asset> serves the assets. For offline tests and
// private mirrors; the checksum-verified third-party mirror fallback is off
// while it is set.
const ReleaseBaseEnv = "PCBPILOT_RELEASE_BASE_URL"

func releaseBase() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv(ReleaseBaseEnv)), "/")
}

// ReleaseAssetURL is the download URL of one release asset.
func ReleaseAssetURL(version, asset string) string {
	if b := releaseBase(); b != "" {
		return fmt.Sprintf("%s/download/v%s/%s", b, strings.TrimPrefix(version, "v"), asset)
	}
	return fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s", Repo(), strings.TrimPrefix(version, "v"), asset)
}

// ReleasePageURL is the human release page of a version.
func ReleasePageURL(version string) string {
	return fmt.Sprintf("https://github.com/%s/releases/tag/v%s", Repo(), strings.TrimPrefix(version, "v"))
}

// Repo returns the effective release repository.
func Repo() string {
	if v := strings.TrimSpace(os.Getenv(RepoEnv)); strings.Count(v, "/") == 1 {
		return v
	}
	return RepoSlug
}

const (
	// SkillName is the router Skill's slug (dir name under each client's
	// skills/). Focused public Skills ship next to it; see skills.go.
	SkillName = "pcbpilot"
	// versionMarker records the installed skill version inside a skill dir.
	versionMarker = ".version"
	// PreserveEnv, when "1", makes a sync keep existing files (local edits win).
	PreserveEnv = "PCBPILOT_SKILL_PRESERVE"
)

// clientOrder is the deterministic client iteration order.
// zcode reads ~/.zcode/skills (setup-agent.sh links it when ~/.zcode exists).
var clientOrder = []string{"claude", "codex", "agents", "zcode"}

// Endpoint builders, overridable in tests to point at an httptest server.
var (
	tarballURL = func(version string) string {
		return ReleaseAssetURL(version, "skills.tar.gz")
	}
	latestAPIURL = func() string {
		if b := releaseBase(); b != "" {
			return b + "/latest"
		}
		return fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", Repo())
	}
	latestWebURL = func() string {
		if b := releaseBase(); b != "" {
			return b + "/latest"
		}
		return fmt.Sprintf("https://github.com/%s/releases/latest", Repo())
	}
)

// SkillTarget is one installed (or installable) skill location: one public
// Skill of one client.
type SkillTarget struct {
	Client    string `json:"client"`    // "claude" | "codex" | "agents" | "zcode"
	Skill     string `json:"skill"`     // public Skill name; SkillName is the router
	Dir       string `json:"dir"`       // absolute skill dir
	Present   bool   `json:"present"`   // dir exists on disk
	Installed string `json:"installed"` // version marker, "" if unknown/missing
	// Linked is the symlink target when the skill dir is a symlink (a source
	// install from a git checkout). Updaters never write through it: `git pull`
	// is what updates a linked skill.
	Linked string `json:"linked,omitempty"`
}

// SkillLinkTarget returns where dir points when it is a symlink or, on
// Windows, a directory junction (what `mklink /J` gives an unprivileged
// user), else "". Go >= 1.23 reports a junction as ModeIrregular, not
// ModeSymlink, and filepath.EvalSymlinks returns it unresolved: without this
// a junction into a checkout read as a copied Skill the updater may write.
func SkillLinkTarget(dir string) string {
	fi, err := os.Lstat(dir)
	if err != nil {
		return ""
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		if runtime.GOOS == "windows" && fi.Mode()&os.ModeIrregular != 0 {
			// os.Readlink reads mount-point reparse data; any other
			// reparse point fails it and is not a link.
			if t, err := os.Readlink(dir); err == nil && t != "" {
				return filepath.Clean(t)
			}
		}
		return ""
	}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		return r
	}
	t, _ := os.Readlink(dir)
	return t
}

// skillDir returns the router skill dir for a client under $HOME, or "" if unknown.
func skillDir(client string) string {
	return SkillDirFor(client, SkillName)
}

// ValidateClients rejects unsupported client names and relative client homes
// before any network or filesystem work. An empty selection means both clients.
func ValidateClients(clients []string) error {
	if len(clients) == 0 {
		clients = clientOrder
	}
	for _, client := range clients {
		if client != "codex" && client != "claude" && client != "agents" && client != "zcode" {
			return fmt.Errorf("unknown skill client %q (want codex, claude, agents, or zcode)", client)
		}
		if skillDir(client) == "" {
			return fmt.Errorf("invalid %s client home: CODEX_HOME/CLAUDE_CONFIG_DIR and user home must be absolute paths", client)
		}
	}
	return nil
}

// Targets returns one target per (client, public Skill) in deterministic order
// (clients, then this CLI's Skill order, then Skills only the installed
// router's manifest names). When onlyPresent is true, only
// dirs that already exist on disk are returned (the daemon's default — never
// create a skill dir for a client the user doesn't use).
func Targets(onlyPresent bool) []SkillTarget {
	var out []SkillTarget
	for _, c := range clientOrder {
		// This CLI's list plus anything the installed router's release shipped.
		for _, skill := range knownSkills([]string{c}).Skills {
			t, ok := skillTarget(c, skill)
			if !ok || (onlyPresent && !t.Present) {
				continue
			}
			out = append(out, t)
		}
	}
	return out
}

func skillTarget(client, skill string) (SkillTarget, bool) {
	dir := SkillDirFor(client, skill)
	if dir == "" {
		return SkillTarget{}, false
	}
	linked := SkillLinkTarget(dir)
	installed := readMarker(dir)
	if linked != "" {
		// A symlinked source checkout never gets a fresh .version marker
		// (a stale one from an old copy install may remain); its SKILL.md
		// frontmatter is the version actually in use.
		if v := linkedSkillVersion(dir); v != "" {
			installed = v
		}
	}
	return SkillTarget{Client: client, Skill: skill, Dir: dir, Present: isDir(dir), Installed: installed, Linked: linked}, true
}

// ClientInstalled reports whether a client has the router Skill installed —
// the signal that pcbpilot is in use there, so missing focused Skills belong
// next to it without --create-missing.
func ClientInstalled(client string) bool {
	dir := skillDir(client)
	return dir != "" && isDir(dir)
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// linkedSkillVersion reads metadata.version from a linked skill's SKILL.md.
func linkedSkillVersion(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return ""
	}
	v, err := skillMetadataVersion(string(b))
	if err != nil {
		return ""
	}
	return v
}

func readMarker(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, versionMarker))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// LatestReleaseVersion resolves the newest GitHub release tag. It authenticates
// the API request when GH_TOKEN/GITHUB_TOKEN is available, then falls back to
// GitHub's public releases/latest redirect when anonymous API quota is exhausted.
func LatestReleaseVersion(ctx context.Context) (string, error) {
	url := latestAPIURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token := githubToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var body struct {
				TagName string `json:"tag_name"`
			}
			if decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); decodeErr == nil {
				if core := SemverCore(body.TagName); core != "" {
					return core, nil
				}
			}
		}
	}
	apiErr := err
	if apiErr == nil {
		apiErr = fmt.Errorf("github releases/latest: %s", resp.Status)
	}

	webReq, err := http.NewRequestWithContext(ctx, http.MethodGet, latestWebURL(), nil)
	if err != nil {
		return "", fmt.Errorf("%v; build latest-release fallback: %w", apiErr, err)
	}
	webResp, err := http.DefaultClient.Do(webReq)
	if err != nil {
		return "", fmt.Errorf("%v; github releases/latest fallback: %w", apiErr, err)
	}
	defer webResp.Body.Close()
	if webResp.StatusCode < 200 || webResp.StatusCode >= 400 {
		return "", fmt.Errorf("%v; github releases/latest fallback: %s", apiErr, webResp.Status)
	}
	tag := strings.TrimPrefix(webResp.Request.URL.Path, "/"+Repo()+"/releases/tag/")
	if core := SemverCore(tag); core != "" {
		return core, nil
	}
	return "", fmt.Errorf("%v; github releases/latest fallback had no release tag in %s", apiErr, webResp.Request.URL)
}

func githubToken() string {
	if token := strings.TrimSpace(os.Getenv("GH_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
}

func downloadBytesWithFallback(ctx context.Context, primary string, limit int64, expected string, allowMirror bool) ([]byte, error) {
	download := func(url string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
		}
		return io.ReadAll(io.LimitReader(resp.Body, limit))
	}
	var primaryErr error
	for range 3 {
		body, err := download(primary)
		if err == nil && (expected == "" || checksumHex(body) == strings.ToLower(expected)) {
			return body, nil
		}
		if err == nil {
			err = fmt.Errorf("checksum mismatch from GitHub")
		}
		primaryErr = err
	}
	if !allowMirror {
		return nil, primaryErr
	}
	mirror := githubProxyURL(primary)
	if mirror == "" {
		return nil, primaryErr
	}
	body, err := download(mirror)
	if err != nil {
		return nil, fmt.Errorf("GitHub download failed (%v); mirror failed: %w", primaryErr, err)
	}
	if expected != "" && checksumHex(body) != strings.ToLower(expected) {
		return nil, fmt.Errorf("GitHub download failed (%v); mirror checksum mismatch", primaryErr)
	}
	return body, nil
}

func checksumHex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// skillTree is an extracted, verified skills.tar.gz: one directory per Skill
// of manifest under root.
type skillTree struct {
	root     string
	manifest SkillManifest
	cleanup  func()
}

func (t skillTree) dir(skill string) string { return filepath.Join(t.root, skill) }

// fetchSkillTree downloads skills.tar.gz for the version and extracts it to a
// temp dir.
func fetchSkillTree(ctx context.Context, version string) (skillTree, error) {
	url := tarballURL(version)
	want, checksumErr := fetchChecksum(ctx, version, "skills.tar.gz")
	legacyNoChecksum := errors.Is(checksumErr, errChecksumUnavailable)
	if checksumErr != nil && !legacyNoChecksum {
		return skillTree{}, fmt.Errorf("verify skills.tar.gz: %w", checksumErr)
	}
	archive, err := downloadBytesWithFallback(ctx, url, (64<<20)+1, want, !legacyNoChecksum)
	if err != nil {
		return skillTree{}, fmt.Errorf("download skills.tar.gz: %w", err)
	}
	if len(archive) > 64<<20 {
		return skillTree{}, fmt.Errorf("skills.tar.gz exceeds 64 MiB")
	}
	// Verify the entire archive before trusting or installing any extracted file.
	if !legacyNoChecksum && !strings.EqualFold(want, checksumHex(archive)) {
		return skillTree{}, fmt.Errorf("checksum mismatch for skills.tar.gz")
	}
	return extractSkillTree(archive, version, legacyNoChecksum)
}

// assetSkillTree extracts skills.tar.gz from an explicit asset source (whose
// Fetch has already verified the checksum).
func assetSkillTree(ctx context.Context, src AssetSource) (skillTree, error) {
	archive, err := src.Fetch(ctx, "skills.tar.gz", 64<<20)
	if err != nil {
		return skillTree{}, fmt.Errorf("skills.tar.gz: %w", err)
	}
	return extractSkillTree(archive, strings.TrimPrefix(src.Version(), "v"), false)
}

// extractSkillTree unpacks a verified skills.tar.gz into a temp dir, reads its
// pcbpilot/.public-skills.json (absent: a single-Skill archive from before
// 0.11) and checks that every listed Skill has a SKILL.md declaring version.
// Nothing is ever written outside the temp dir and links are never created; a
// manifest archive must in addition hold nothing but regular files and dirs
// under its listed Skill roots.
func extractSkillTree(archive []byte, version string, legacyNoChecksum bool) (skillTree, error) {
	tmp, err := os.MkdirTemp("", "easyeda-skill-*")
	if err != nil {
		return skillTree{}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	fail := func(err error) (skillTree, error) {
		cleanup()
		return skillTree{}, err
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fail(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var extractedBytes int64
	var unsafe []string
	tops := map[string]bool{}
	folded2name := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail(err)
		}
		// Guard against path traversal; only accept entries under the temp dir.
		clean := filepath.Clean(hdr.Name)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || filepath.IsAbs(clean) ||
			strings.Contains(hdr.Name, `\`) || strings.Contains(hdr.Name, ":") || slices.Contains(strings.Split(hdr.Name, "/"), "..") {
			unsafe = append(unsafe, hdr.Name)
			continue
		}
		dst := filepath.Join(tmp, clean)
		if !strings.HasPrefix(dst, filepath.Clean(tmp)+string(os.PathSeparator)) {
			unsafe = append(unsafe, hdr.Name)
			continue
		}
		if hdr.Typeflag != tar.TypeDir && hdr.Typeflag != tar.TypeReg {
			unsafe = append(unsafe, hdr.Name) // links, devices: never extracted
			continue
		}
		top, _, _ := strings.Cut(filepath.ToSlash(clean), "/")
		tops[top] = true
		// Names that differ only in case collide on Windows and macOS.
		if folded := strings.ToLower(filepath.ToSlash(clean)); folded != "" {
			if prev, dup := folded2name[folded]; dup && (prev != filepath.ToSlash(clean) || hdr.Typeflag != tar.TypeDir) {
				return fail(fmt.Errorf("skills.tar.gz: %q and %q collide on a case-insensitive filesystem", prev, hdr.Name))
			}
			folded2name[folded] = filepath.ToSlash(clean)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0755); err != nil {
				return fail(err)
			}
		case tar.TypeReg:
			extractedBytes += hdr.Size
			if hdr.Size > 64<<20 || extractedBytes > 256<<20 {
				return fail(fmt.Errorf("skills.tar.gz extracted content exceeds size limit"))
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
				return fail(err)
			}
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0777|0600)
			if err != nil {
				return fail(err)
			}
			if _, err := io.Copy(f, io.LimitReader(tr, 64<<20)); err != nil {
				f.Close()
				return fail(err)
			}
			if err := f.Close(); err != nil {
				return fail(err)
			}
		}
	}

	manifest := legacySkillManifest()
	legacyArchive := true
	manifestPath := filepath.Join(tmp, filepath.FromSlash(SkillManifestEntry))
	if fi, err := os.Lstat(manifestPath); err == nil {
		if !fi.Mode().IsRegular() {
			return fail(fmt.Errorf("skills.tar.gz: %s is not a regular file", SkillManifestEntry))
		}
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			return fail(err)
		}
		if manifest, err = parseSkillManifest(raw); err != nil {
			return fail(fmt.Errorf("skills.tar.gz: %w", err))
		}
		legacyArchive = false
		if len(unsafe) > 0 {
			return fail(fmt.Errorf("skills.tar.gz: unsafe entries %q", unsafe))
		}
		for top := range tops {
			if !slices.Contains(manifest.Skills, top) {
				return fail(fmt.Errorf("skills.tar.gz: %s is not a listed Skill root", top))
			}
		}
	}
	for _, name := range manifest.Skills {
		root := filepath.Join(tmp, name)
		skill, readErr := os.ReadFile(filepath.Join(root, "SKILL.md"))
		if !isDir(root) || readErr != nil || len(strings.TrimSpace(string(skill))) == 0 {
			return fail(fmt.Errorf("skills.tar.gz did not contain a nonempty %s/SKILL.md", name))
		}
		declared, err := skillMetadataVersion(string(skill))
		// Releases before 1.4.0 without checksums shipped a router without
		// metadata.version; that leniency never extends to a manifest archive.
		legacyBlank := declared == "" && legacyArchive && legacyNoChecksum && SemverLess(version, "1.4.0")
		if err != nil || (declared == "" && !legacyBlank) || (declared != "" && declared != version) {
			return fail(fmt.Errorf("%s/SKILL.md metadata.version %q does not match target %s (parse error: %v)", name, declared, version, err))
		}
	}
	return skillTree{root: tmp, manifest: manifest, cleanup: cleanup}, nil
}

// skillMetadataVersion reads the published frontmatter contract: metadata is a
// top-level mapping and its version is an indented scalar. It does not scan
// prose or accept a similarly named field elsewhere in the document.
func skillMetadataVersion(body string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", nil
	}
	inMetadata, seenMetadata, closed := false, false, false
	version := ""
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			closed = true
			break
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inMetadata = trimmed == "metadata:"
			if inMetadata {
				if seenMetadata {
					return "", fmt.Errorf("duplicate metadata mapping")
				}
				seenMetadata = true
			}
			continue
		}
		if inMetadata && strings.HasPrefix(line, "  version:") {
			if version != "" {
				return "", fmt.Errorf("duplicate metadata.version")
			}
			value := strings.TrimSpace(strings.TrimPrefix(line, "  version:"))
			value = strings.Trim(value, "\"'")
			if !IsCleanRelease(value) && !IsLocalVersion(value) {
				return "", fmt.Errorf("invalid metadata.version %q", value)
			}
			version = strings.TrimPrefix(value, "v")
		}
	}
	if !closed {
		return "", fmt.Errorf("unclosed YAML frontmatter")
	}
	return version, nil
}

func copyTree(src, dst string, preserve, skipMarker bool) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." || (skipMarker && rel == versionMarker) {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill tree contains unsupported symlink %s", path)
		}
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("skill tree contains non-regular file %s", path)
		}
		if preserve {
			if _, err := os.Stat(target); err == nil {
				return nil
			}
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode&0777|0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// PreserveFromEnv reports whether PCBPILOT_SKILL_PRESERVE requests preserve mode.
func PreserveFromEnv() bool {
	return os.Getenv(PreserveEnv) == "1"
}

// ── semver helpers (self-contained; mirror internal/daemon) ─────────────────

// SemverCore extracts the "x.y.z" core from a version string, dropping a leading
// 'v' and any "-suffix". Returns "" if not x.y.z.
func SemverCore(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return ""
	}
	for _, p := range parts {
		if p == "" {
			return ""
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return ""
			}
		}
	}
	return v
}

// SemverLess reports whether core a < b. Empty is lower than any real version.
func SemverLess(a, b string) bool {
	a, b = SemverCore(a), SemverCore(b)
	if a == b {
		return false
	}
	if a == "" {
		return true
	}
	if b == "" {
		return false
	}
	ap, bp := strings.Split(a, "."), strings.Split(b, ".")
	for i := range 3 {
		x, _ := strconv.Atoi(ap[i])
		y, _ := strconv.Atoi(bp[i])
		if x != y {
			return x < y
		}
	}
	return false
}

// IsCleanRelease reports whether v is a bare release tag (vX.Y.Z, no suffix).
func IsCleanRelease(v string) bool {
	core := SemverCore(v)
	return core != "" && core == strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// StartupSync aligns installed skills with the running released CLI. A pinned
// installation must remain pinned even when a newer release becomes available.
// Development builds skip automatic writes. Checking latest only prints a nudge.
func StartupSync(ctx context.Context, daemonVersion string, logf func(string, ...any)) {
	log := func(format string, a ...any) {
		if logf != nil {
			logf(format, a...)
		}
	}
	if !IsCleanRelease(daemonVersion) || len(Targets(true)) == 0 {
		return
	}
	target := SemverCore(daemonVersion)
	res, err := SyncSkills(ctx, SyncOptions{
		TargetVersion: target,
		Preserve:      PreserveFromEnv(),
	}, log)
	if err != nil {
		log("skill-sync: %v", err)
	}
	if res.Changed > 0 {
		log("skill-sync: refreshed %d skill dir(s) for CLI v%s", res.Changed, target)
	}
	latest, err := LatestReleaseVersion(ctx)
	if err == nil && SemverLess(target, latest) {
		log("update available: v%s < latest v%s — with auto-update on the daemon applies it when EasyEDA is idle; "+
			"otherwise run `pcbpilot update` (the connector .eext is re-imported by hand; health prints the steps)", target, latest)
	}
}
