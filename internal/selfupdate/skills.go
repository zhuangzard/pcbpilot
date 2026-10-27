package selfupdate

// Public Skill set. A release ships one router Skill (`pcbpilot`) plus focused
// Skills next to it; skills.tar.gz carries one top-level directory per Skill
// and a public-skills.json manifest at its root. The manifest of the archive
// being installed is authoritative for that install. An archive without one is
// a pre-0.11 single-Skill package and installs only the router.
//
// publicSkills is this CLI's own copy of .agents/skills/public-skills.json
// (go:embed cannot reach `..`; TestPublicSkillsMatchCheckoutManifest keeps the
// two equal). It is used where no archive is at hand: `skill status`, health,
// update --check and rollback snapshots.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// SkillManifestName is the manifest file at the root of skills.tar.gz.
const SkillManifestName = "public-skills.json"

// SkillManifest lists the public Skills of one release.
type SkillManifest struct {
	SchemaVersion int      `json:"schemaVersion"`
	Router        string   `json:"router"`
	Skills        []string `json:"skills"`
	// Retired names Skills an earlier release shipped. Installers remove them
	// only from directories pcbpilot itself wrote (a .version marker, no link).
	Retired []string `json:"retired"`
}

var publicSkills = SkillManifest{
	SchemaVersion: 1,
	Router:        SkillName,
	Skills:        []string{SkillName, "pcbpilot-review"},
	Retired:       []string{},
}

// PublicSkills returns this CLI's public Skill manifest.
func PublicSkills() SkillManifest { return publicSkills.clone() }

// OverridePublicSkillsForTest swaps this CLI's Skill list, for tests of
// packages built on selfupdate that need a focused Skill fixture. Call the
// returned func to restore. Never used outside tests.
func OverridePublicSkillsForTest(skills []string, retired ...string) (restore func(), err error) {
	m := SkillManifest{SchemaVersion: 1, Router: SkillName, Skills: slices.Clone(skills), Retired: append([]string{}, retired...)}
	if err := m.validate(); err != nil {
		return func() {}, err
	}
	old := publicSkills
	publicSkills = m
	return func() { publicSkills = old }, nil
}

// legacySkillManifest describes an archive that predates public-skills.json.
func legacySkillManifest() SkillManifest {
	return SkillManifest{SchemaVersion: 1, Router: SkillName, Skills: []string{SkillName}, Retired: []string{}}
}

func (m SkillManifest) clone() SkillManifest {
	m.Skills = slices.Clone(m.Skills)
	m.Retired = slices.Clone(m.Retired)
	return m
}

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// isPublicSkillName reports whether name can be a pcbpilot public Skill: the
// router itself or router-prefixed, never a repository-only pcbpilot-repo-*.
// Every name the updater may create or delete passes through this check.
func isPublicSkillName(name string) bool {
	if !skillNamePattern.MatchString(name) || strings.HasPrefix(name, SkillName+"-repo-") {
		return false
	}
	return name == SkillName || strings.HasPrefix(name, SkillName+"-")
}

func (m SkillManifest) validate() error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("%s: schemaVersion must be 1", SkillManifestName)
	}
	if m.Router != SkillName {
		return fmt.Errorf("%s: router must be %q", SkillManifestName, SkillName)
	}
	seen := map[string]string{}
	for field, names := range map[string][]string{"skills": m.Skills, "retired": m.Retired} {
		for _, name := range names {
			if !isPublicSkillName(name) {
				return fmt.Errorf("%s: %s entry %q must be %s or %s-<slug>", SkillManifestName, field, name, SkillName, SkillName)
			}
			if prev, dup := seen[name]; dup {
				return fmt.Errorf("%s: %q appears in both %s and %s", SkillManifestName, name, prev, field)
			}
			seen[name] = field
		}
	}
	if !slices.Contains(m.Skills, m.Router) {
		return fmt.Errorf("%s: skills must include the router %q", SkillManifestName, m.Router)
	}
	return nil
}

func parseSkillManifest(raw []byte) (SkillManifest, error) {
	var m SkillManifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return SkillManifest{}, fmt.Errorf("%s: %w", SkillManifestName, err)
	}
	if m.Retired == nil {
		m.Retired = []string{}
	}
	if err := m.validate(); err != nil {
		return SkillManifest{}, err
	}
	return m, nil
}

// skillsBase returns <client home>/skills, or "" for an unknown client or a
// relative client home.
func skillsBase(client string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	var base string
	switch client {
	case "claude":
		base = os.Getenv("CLAUDE_CONFIG_DIR")
		if base == "" {
			base = filepath.Join(home, ".claude")
		}
	case "codex":
		base = os.Getenv("CODEX_HOME")
		if base == "" {
			base = filepath.Join(home, ".codex")
		}
	case "agents":
		base = filepath.Join(home, ".agents")
	case "zcode":
		base = filepath.Join(home, ".zcode")
	default:
		return ""
	}
	if !filepath.IsAbs(base) {
		return ""
	}
	return filepath.Join(base, "skills")
}

// SkillDirFor returns the directory of one public Skill for a client, or "".
func SkillDirFor(client, skill string) string {
	base := skillsBase(client)
	if base == "" || !isPublicSkillName(skill) {
		return ""
	}
	return filepath.Join(base, skill)
}

// SkillLabel names one (client, Skill) pair in reports: the router keeps the
// bare client name ("claude") that older output used; focused Skills append
// their name ("claude/pcbpilot-review").
func SkillLabel(client, skill string) string {
	if skill == "" || skill == SkillName {
		return client
	}
	return client + "/" + skill
}

// ownedSkillDir reports whether dir is a Skill directory pcbpilot wrote: a
// real directory (not a link into a checkout) holding a regular .version
// marker. Only such a directory is ever removed.
func ownedSkillDir(dir string) bool {
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return false
	}
	marker, err := os.Lstat(filepath.Join(dir, versionMarker))
	return err == nil && marker.Mode().IsRegular()
}

// removeOwnedSkill moves an owned Skill directory aside and deletes it. A
// directory without the marker, or a link, is refused and left untouched.
func removeOwnedSkill(dir string) error {
	if !ownedSkillDir(dir) {
		return fmt.Errorf("%s is not a pcbpilot-installed Skill (no %s marker, or a link); left in place", dir, versionMarker)
	}
	trash, err := os.MkdirTemp(filepath.Dir(dir), ".easyeda-skill-retired-*")
	if err != nil {
		return err
	}
	if err := os.Remove(trash); err != nil {
		return err
	}
	if err := os.Rename(dir, trash); err != nil {
		return err
	}
	return os.RemoveAll(trash)
}
