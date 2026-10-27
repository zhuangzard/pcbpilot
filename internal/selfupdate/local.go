package selfupdate

// Local development packages are an explicit, offline trust boundary. They are
// built by the user, not authenticated releases; checksums detect corruption.
import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

var localVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-dev\.[1-9][0-9]*$`)

func IsLocalVersion(v string) bool {
	return localVersionPattern.MatchString(strings.TrimPrefix(v, "v"))
}

type LocalBundle struct {
	Version string
	Binary  []byte
	// Skill is the router's files (Skills[SkillName]), kept for callers that
	// predate focused Skills.
	Skill map[string][]byte
	// Skills maps every packaged Skill to its files (relative slash paths).
	Skills    map[string]map[string][]byte
	Manifest  SkillManifest
	Connector string
	// Adopt lets Install replace a same-named dir pcbpilot did not install.
	Adopt bool
}

// ReadLocalBundle snapshots validated bytes before any installation writes.
func ReadLocalBundle(dir string) (*LocalBundle, error) {
	asset, err := AssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		return nil, err
	}
	sums := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || filepath.Base(f[1]) != f[1] || strings.ContainsAny(f[1], `/\`) {
			return nil, fmt.Errorf("invalid checksum entry")
		}
		if _, exists := sums[f[1]]; exists {
			return nil, fmt.Errorf("duplicate checksum entry")
		}
		if d, e := hex.DecodeString(f[0]); e != nil || len(d) != 32 {
			return nil, fmt.Errorf("invalid SHA-256")
		}
		sums[f[1]] = f[0]
	}
	read := func(name string) ([]byte, error) {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			return nil, e
		}
		if len(b) == 0 || fmt.Sprintf("%x", sha256.Sum256(b)) != sums[name] {
			return nil, fmt.Errorf("checksum mismatch: %s", name)
		}
		return b, nil
	}
	b := &LocalBundle{Skills: map[string]map[string][]byte{}, Connector: filepath.Join(dir, "pcbpilot-connector.eext")}
	if b.Binary, err = read(asset); err != nil {
		return nil, err
	}
	connector, err := read("pcbpilot-connector.eext")
	if err != nil {
		return nil, err
	}
	z, err := zip.NewReader(bytes.NewReader(connector), int64(len(connector)))
	if err != nil {
		return nil, err
	}
	compiled, manifests := false, 0
	for _, f := range z.File {
		if f.Name == "dist/index.js" && f.UncompressedSize64 > 0 {
			compiled = true
		}
		if f.Name != "extension.json" {
			continue
		}
		manifests++
		r, e := f.Open()
		if e != nil {
			return nil, e
		}
		var m struct {
			Version string `json:"version"`
		}
		e = json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&m)
		r.Close()
		if e != nil {
			return nil, e
		}
		b.Version = m.Version
	}
	if manifests != 1 || !compiled || !IsLocalVersion(b.Version) {
		return nil, fmt.Errorf("not a compiled X.Y.Z-dev.N connector package")
	}
	packed, err := read("skills.tar.gz")
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	seenFolded := map[string]bool{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("unsafe skill entry: %s", h.Name)
		}
		total += h.Size
		if h.Size < 0 || h.Size > 64<<20 || total > 256<<20 {
			return nil, fmt.Errorf("skill package too large")
		}
		// Slash paths are the archive format on every OS: path.Clean, not
		// filepath.Clean (which rewrites separators on Windows).
		skill, name, ok := strings.Cut(h.Name, "/")
		if !ok || !isPublicSkillName(skill) || name == "" || name == "." || name == versionMarker || strings.Contains(name, `\`) ||
			strings.Contains(name, ":") || filepath.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("unsafe skill entry: %s", h.Name)
		}
		folded := strings.ToLower(h.Name)
		if seenFolded[folded] {
			// Two names that differ only in case land on one file on Windows
			// and macOS; refuse them everywhere so packages stay portable.
			return nil, fmt.Errorf("duplicate skill entry (case-insensitive): %s", h.Name)
		}
		seenFolded[folded] = true
		files := b.Skills[skill]
		if files == nil {
			files = map[string][]byte{}
			b.Skills[skill] = files
		}
		if _, exists := files[name]; exists {
			return nil, fmt.Errorf("duplicate skill entry: %s", h.Name)
		}
		if files[name], e = io.ReadAll(tr); e != nil {
			return nil, e
		}
	}
	b.Manifest = legacySkillManifest()
	if raw, ok := b.Skills[SkillName][installedManifestName]; ok {
		if b.Manifest, err = parseSkillManifest(raw); err != nil {
			return nil, err
		}
	}
	for skill := range b.Skills {
		if !slices.Contains(b.Manifest.Skills, skill) {
			return nil, fmt.Errorf("skills.tar.gz: %s/ is not listed in %s", skill, SkillManifestName)
		}
	}
	for _, skill := range b.Manifest.Skills {
		v, err := skillMetadataVersion(string(b.Skills[skill]["SKILL.md"]))
		if err != nil || v != b.Version {
			return nil, fmt.Errorf("%s Skill metadata does not match connector %s", skill, b.Version)
		}
	}
	b.Skill = b.Skills[SkillName]
	return b, nil
}

func (b *LocalBundle) CheckBinary(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, b.Binary) {
		return fmt.Errorf("CLI bytes differ from local bundle")
	}
	return nil
}

// CheckSkill compares an installed router dir with the bundle.
func (b *LocalBundle) CheckSkill(dir string) error { return b.CheckSkillNamed(SkillName, dir) }

// CheckSkillNamed compares one installed Skill dir with the bundle byte for byte.
func (b *LocalBundle) CheckSkillNamed(skill, dir string) error {
	want, listed := b.Skills[skill]
	if !listed {
		return fmt.Errorf("%s is not in the local package", skill)
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	seen := map[string]bool{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		name, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		if !info.Mode().IsRegular() {
			return fmt.Errorf("nonregular Skill file: %s", name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if name == ".version" {
			if strings.TrimSpace(string(data)) != b.Version {
				return fmt.Errorf("Skill version marker differs")
			}
		} else if expected, ok := want[name]; !ok || !bytes.Equal(data, expected) {
			return fmt.Errorf("Skill content differs: %s", name)
		}
		seen[name] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(want)+1 || !seen[".version"] {
		return fmt.Errorf("Skill files/marker missing")
	}
	return nil
}

// Install stages complete files and retains recoverable backups. No process
// restart, network access, or connector import occurs here.
func (b *LocalBundle) Install(binary string, log io.Writer) (retErr error) {
	if !filepath.IsAbs(binary) {
		return fmt.Errorf("--binary must be an absolute installation path")
	}
	if p, err := filepath.EvalSymlinks(binary); err == nil {
		binary = p
	}
	stage, err := os.CreateTemp(filepath.Dir(binary), ".easyeda-local-*")
	if err != nil {
		return err
	}
	stageName := stage.Name()
	defer os.Remove(stageName)
	if _, err = stage.Write(b.Binary); err != nil {
		stage.Close()
		return err
	}
	if err = stage.Close(); err != nil {
		return err
	}
	if err = os.Chmod(stageName, 0755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, stageName, "--version").Output()
	if err != nil || strings.TrimSpace(string(output)) != "pcbpilot v"+b.Version {
		return fmt.Errorf("local CLI execution/version check failed: %v", err)
	}
	if _, err = os.Stat(binary); err == nil {
		backup, e := os.CreateTemp(filepath.Dir(binary), ".pcbpilot-backup-*")
		if e != nil {
			return e
		}
		backup.Close()
		if e = copyFile(binary, backup.Name(), 0755); e != nil {
			return e
		}
		fmt.Fprintf(log, "CLI backup: %s\n", backup.Name())
	}
	if err = replaceBinary(stageName, binary); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			retErr = fmt.Errorf("partial local installation: CLI was replaced; use printed backups to recover and restart the Agent session: %w", retErr)
		}
	}()
	if err := b.CheckBinary(binary); err != nil {
		return err
	}
	fmt.Fprintf(log, "Installed CLI: %s\n", binary)
	return b.installSkills(log)
}

// installSkills gives every client with the router every packaged Skill, with
// the same rules as SyncSkills: links are left alone, a same-named dir
// pcbpilot did not install is a conflict unless Adopt, retired Skills go only
// where pcbpilot wrote them, and each client is staged completely and switched
// as one transaction. Every dir about to change is first copied to a printed
// .pcbpilot-local-backup-* directory.
func (b *LocalBundle) installSkills(log io.Writer) error {
	root, err := os.MkdirTemp("", "easyeda-local-skills-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	for _, skill := range b.Manifest.Skills {
		for name, data := range b.Skills[skill] {
			p := filepath.Join(root, skill, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				return err
			}
			mode := os.FileMode(0644)
			if bytes.HasPrefix(data, []byte("#!")) {
				mode = 0755
			}
			if err := os.WriteFile(p, data, mode); err != nil {
				return err
			}
		}
	}
	tree := skillTree{root: root, manifest: b.Manifest, cleanup: func() {}}
	var clients []string
	for _, c := range clientOrder {
		if ClientInstalled(c) {
			clients = append(clients, c)
		}
	}
	// Every staged tree and every switched dir must equal the package byte for
	// byte; any mismatch restores the client before the switch is final.
	opts := SyncOptions{Force: true, Adopt: b.Adopt, verify: b.CheckSkillNamed}
	items := planSkillSync(clients, b.Manifest, retiredSkills(b.Manifest, knownSkills(clients)), opts, b.Version)
	for _, it := range items {
		if it.action == syncKeep || !isDir(it.out.Dir) {
			continue
		}
		backup, err := os.MkdirTemp(filepath.Dir(it.out.Dir), ".pcbpilot-local-backup-*")
		if err != nil {
			return err
		}
		if err := copyTree(it.out.Dir, backup, false, false); err != nil {
			return err
		}
		fmt.Fprintf(log, "Skill backup: %s\n", backup)
	}
	changed := 0
	var failures []error
	for _, client := range clients {
		var mine []syncItem
		for _, it := range items {
			if it.out.Client == client {
				mine = append(mine, it)
			}
		}
		for _, o := range applyClientSync(tree, mine, opts, b.Version, &changed, func(string, ...any) {}) {
			switch o.Status {
			case "error":
				failures = append(failures, fmt.Errorf("Skill %s: %s", o.Dir, o.Err))
			case "created", "updated":
				fmt.Fprintf(log, "Installed Skill: %s\n", o.Dir)
			case "removed":
				fmt.Fprintf(log, "Removed retired Skill: %s\n", o.Dir)
			case "linked", "conflict", "kept":
				detail := o.Err
				if o.Status == "linked" {
					detail = "linked to " + o.Linked + " (source checkout; left alone)"
				}
				fmt.Fprintf(log, "Skipped Skill %s: %s\n", o.Dir, detail)
			}
		}
	}
	return errors.Join(failures...)
}
