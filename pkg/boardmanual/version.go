package boardmanual

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Snapshot is what one manual version records in index.json: enough to
// diff the next version against it.
type Snapshot struct {
	Version   int                          `json:"version"`
	BoardSHA  string                       `json:"boardSha256"`
	Generated string                       `json:"generated"`
	Tool      string                       `json:"tool,omitempty"`
	Project   string                       `json:"project,omitempty"`
	Doc       string                       `json:"doc,omitempty"`
	File      string                       `json:"file"`
	Conns     map[string]map[string]string `json:"connectors"` // ref → pin → net
	ConnNames map[string]string            `json:"connectorNames,omitempty"`
	LEDs      map[string]string            `json:"leds,omitempty"` // ref → "name | net"
	Power     map[string]string            `json:"power,omitempty"`
	Pass      bool                         `json:"gatePass"`
	Changes   []string                     `json:"changes,omitempty"`
}

// Index is <manual dir>/index.json.
type Index struct {
	SchemaVersion int        `json:"schemaVersion"`
	Name          string     `json:"name"`
	Versions      []Snapshot `json:"versions"`
}

// SnapshotOf records a built manual.
func SnapshotOf(m *Manual) Snapshot {
	s := Snapshot{BoardSHA: m.BoardSHA, Generated: m.Generated, Tool: m.Tool, Project: m.Project, Doc: m.Doc,
		Conns: map[string]map[string]string{}, ConnNames: map[string]string{}, LEDs: map[string]string{}, Power: map[string]string{}}
	for _, c := range m.Connectors {
		pins := map[string]string{}
		for _, r := range c.Pins {
			pins[r.Pin] = r.Net
		}
		s.Conns[c.Ref] = pins
		s.ConnNames[c.Ref] = c.Name
	}
	for _, l := range m.LEDs {
		s.LEDs[l.Ref] = l.Name + " | " + l.Net
	}
	p := m.Power
	for k, v := range map[string]string{"input": p.InputRef + " " + p.InputPins, "voltage": p.Voltage, "typical": p.Typ, "peak": p.Peak, "worst": p.Worst, "recommended": p.Recommend} {
		s.Power[k] = v
	}
	for _, r := range p.Rails {
		s.Power["rail "+r.Net] = strings.Join(r.Values, " · ")
	}
	return s
}

// Diff lists the changes from prev to cur: connectors added / removed, pin
// net changes, LED and power changes. nil prev = first version.
func Diff(prev, cur Snapshot, lang string) []string {
	var out []string
	add := func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) }
	refs := func(m map[string]map[string]string) []string {
		var r []string
		for k := range m {
			r = append(r, k)
		}
		sortNat(r)
		return r
	}
	zh := lang != "en"
	t := func(z, e string) string {
		if zh {
			return z
		}
		return e
	}
	for _, ref := range refs(cur.Conns) {
		if _, ok := prev.Conns[ref]; !ok {
			add("%s %s（%d %s）", t("新增接口", "connector added"), ref, len(cur.Conns[ref]), t("脚", "pins"))
		}
	}
	for _, ref := range refs(prev.Conns) {
		if _, ok := cur.Conns[ref]; !ok {
			add("%s %s", t("删除接口", "connector removed"), ref)
		}
	}
	for _, ref := range refs(cur.Conns) {
		old, ok := prev.Conns[ref]
		if !ok {
			continue
		}
		var pins []string
		for p := range cur.Conns[ref] {
			pins = append(pins, p)
		}
		for p := range old {
			if _, ok := cur.Conns[ref][p]; !ok {
				pins = append(pins, p)
			}
		}
		sortNat(pins)
		seen := map[string]bool{}
		for _, p := range pins {
			if seen[p] {
				continue
			}
			seen[p] = true
			a, inOld := old[p]
			b, inCur := cur.Conns[ref][p]
			switch {
			case !inOld:
				add("%s.%s %s：%s", ref, p, t("新增焊盘", "pad added"), nz(b))
			case !inCur:
				add("%s.%s %s（%s）", ref, p, t("焊盘删除", "pad removed"), nz(a))
			case a != b:
				add("%s.%s %s：%s → %s", ref, p, t("网络变更", "net changed"), nz(a), nz(b))
			}
		}
		if prev.ConnNames[ref] != cur.ConnNames[ref] && prev.ConnNames[ref] != "" {
			add("%s %s：%s → %s", ref, t("名称", "name"), prev.ConnNames[ref], cur.ConnNames[ref])
		}
	}
	keys := func(m map[string]string) []string {
		var r []string
		for k := range m {
			r = append(r, k)
		}
		sortNat(r)
		return r
	}
	union := func(a, b map[string]string) []string {
		m := map[string]string{}
		for k, v := range a {
			m[k] = v
		}
		for k, v := range b {
			m[k] = v
		}
		return keys(m)
	}
	for _, k := range union(prev.LEDs, cur.LEDs) {
		if prev.LEDs[k] != cur.LEDs[k] {
			add("LED %s：%s → %s", k, nz(prev.LEDs[k]), nz(cur.LEDs[k]))
		}
	}
	for _, k := range union(prev.Power, cur.Power) {
		if strings.TrimSpace(prev.Power[k]) != strings.TrimSpace(cur.Power[k]) {
			add("%s %s：%s → %s", t("电源", "power"), k, nz(prev.Power[k]), nz(cur.Power[k]))
		}
	}
	if prev.BoardSHA != cur.BoardSHA && len(out) == 0 {
		add("%s", t("板数据有变化（sha256 不同），接口、LED 与电源数据无变化", "board data changed (sha256), no connector / LED / power change"))
	}
	return out
}

func nz(s string) string {
	if strings.TrimSpace(s) == "" {
		return "NC"
	}
	return s
}

// LoadIndex reads <dir>/index.json (empty index when absent).
func LoadIndex(dir string) (*Index, error) {
	b, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if os.IsNotExist(err) {
		return &Index{SchemaVersion: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	var ix Index
	if err := json.Unmarshal(b, &ix); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, "index.json"), err)
	}
	sort.Slice(ix.Versions, func(i, j int) bool { return ix.Versions[i].Version < ix.Versions[j].Version })
	return &ix, nil
}

// FileName is <Board>_使用说明.html (or _manual.html for en).
func FileName(name, lang string) string {
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>| `, r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	if name == "" {
		name = "board"
	}
	if lang == "en" {
		return name + "_manual.html"
	}
	return name + "_使用说明.html"
}

// Published is the result of Publish.
type Published struct {
	Version int
	Current string // <dir>/<file>
	VerFile string // <dir>/vN/<file>
	Bumped  bool   // false = same board sha256, version rebuilt in place
}

// Publish versions the manual under dir: vN/<file> plus the current copy
// <dir>/<file> and index.json. A board with the same sha256 as the latest
// version rebuilds that version (no bump); its changes stay relative to
// the version before it. render renders the manual after Version, BoardSHA
// and Changes are set.
func Publish(dir, name string, m *Manual, pass bool, render func(*Manual) ([]byte, error)) (*Published, error) {
	ix, err := LoadIndex(dir)
	if err != nil {
		return nil, err
	}
	ix.Name = name
	file := FileName(name, m.Lang)
	cur := SnapshotOf(m)
	cur.Pass = pass
	var prev *Snapshot
	v, bumped := 1, true
	if n := len(ix.Versions); n > 0 {
		last := ix.Versions[n-1]
		if last.BoardSHA != "" && last.BoardSHA == m.BoardSHA {
			v, bumped = last.Version, false
			if n > 1 {
				prev = &ix.Versions[n-2]
			}
			ix.Versions = ix.Versions[:n-1]
		} else {
			v, prev = last.Version+1, &last
		}
	}
	m.Version = v
	if prev != nil {
		m.PrevVersion = prev.Version
		m.Changes = Diff(*prev, cur, m.Lang)
	}
	cur.Version, cur.File, cur.Changes = v, filepath.ToSlash(filepath.Join(fmt.Sprintf("v%d", v), file)), m.Changes
	html, err := render(m)
	if err != nil {
		return nil, err
	}
	vdir := filepath.Join(dir, fmt.Sprintf("v%d", v))
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		return nil, err
	}
	p := &Published{Version: v, Current: filepath.Join(dir, file), VerFile: filepath.Join(vdir, file), Bumped: bumped}
	if err := os.WriteFile(p.VerFile, html, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p.Current, html, 0o644); err != nil {
		return nil, err
	}
	ix.Versions = append(ix.Versions, cur)
	b, _ := json.MarshalIndent(ix, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "index.json"), append(b, '\n'), 0o644); err != nil {
		return nil, err
	}
	return p, nil
}
