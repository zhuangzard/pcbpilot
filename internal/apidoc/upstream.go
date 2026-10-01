package apidoc

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ── pinned surface ────────────────────────────────────────────────────────

//go:embed connector-usage.json
var usageJSON []byte

// Usage is the connector's static eda.* footprint, extracted from
// extension/src (tests excluded) and embedded so the binary can flag which
// upstream changes touch code we ship. Regenerate with
// `go test ./internal/apidoc -run TestConnectorUsageFresh -update-usage`.
type Usage struct {
	Source     string   `json:"source"`
	Namespaces []string `json:"namespaces"` // eda.<ns> referenced at all (incl. indirect `const api = eda.x`)
	Methods    []string `json:"methods"`    // eda.<ns>.<method> referenced directly
}

// ConnectorUsage returns the embedded connector usage.
func ConnectorUsage() Usage {
	var u Usage
	_ = json.Unmarshal(usageJSON, &u)
	return u
}

// PinnedVersion is the @jlceda/pro-api-types version the embedded index was
// generated from (the version the connector pins in extension/package-lock.json).
func PinnedVersion() string { return loaded.Version }

var (
	usageMethodRE = regexp.MustCompile(`\beda\??\.([a-z][A-Za-z0-9_]*[A-Za-z0-9])\??\.([A-Za-z_][A-Za-z0-9_]*)`)
	usageNSRE     = regexp.MustCompile(`\beda\??\.([a-z][A-Za-z0-9_]*[A-Za-z0-9])\b`)
)

// ExtractConnectorUsage scans connector TypeScript sources (non-test *.ts) for
// eda.* references. Static and best-effort: computed property access
// (`api[method]`) is visible only at namespace level.
func ExtractConnectorUsage(srcDir string) (Usage, error) {
	ns := map[string]bool{}
	methods := map[string]bool{}
	err := filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".test.ts") || strings.HasSuffix(p, ".d.ts") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range usageMethodRE.FindAllSubmatch(b, -1) {
			methods["eda."+string(m[1])+"."+string(m[2])] = true
		}
		for _, m := range usageNSRE.FindAllSubmatch(b, -1) {
			ns["eda."+string(m[1])] = true
		}
		return nil
	})
	if err != nil {
		return Usage{}, err
	}
	return Usage{Source: "extension/src/**/*.ts (tests excluded), static eda.* references", Namespaces: keys(ns), Methods: keys(methods)}, nil
}

// ── snapshots ─────────────────────────────────────────────────────────────

// Snapshot is one API surface.
type Snapshot struct {
	Source  string   `json:"source"`
	Version string   `json:"version,omitempty"`
	Format  string   `json:"format"` // dts | api-index | doc-api
	Methods []Method `json:"-"`
	// MethodCount is filled by Diff for the report.
	MethodCount int `json:"methodCount"`
	// HasSignatures is false for doc-api dumps (names only), so signature and
	// stability changes are not compared against them.
	HasSignatures bool `json:"hasSignatures"`
}

// PinnedSnapshot is the embedded index.
func PinnedSnapshot() Snapshot {
	return Snapshot{Source: "embedded api-index.json (@jlceda/pro-api-types)", Version: loaded.Version,
		Format: "api-index", Methods: loaded.Records, HasSignatures: true}
}

// LoadSnapshot reads a newer API source from disk:
//   - an npm tarball (.tgz / .tar.gz) of @jlceda/pro-api-types;
//   - an index.d.ts, or a directory containing index.d.ts (or package/index.d.ts);
//   - a JSON api-index (gen.py output) or a saved official `doc api` dump.
func LoadSnapshot(path string) (Snapshot, error) {
	st, err := os.Stat(path)
	if err != nil {
		return Snapshot{}, err
	}
	lower := strings.ToLower(path)
	switch {
	case st.IsDir():
		for _, cand := range []string{filepath.Join(path, "index.d.ts"), filepath.Join(path, "package", "index.d.ts")} {
			if _, err := os.Stat(cand); err == nil {
				s, err := LoadSnapshot(cand)
				if err != nil {
					return s, err
				}
				s.Source = path
				return s, nil
			}
		}
		return Snapshot{}, fmt.Errorf("%s: no index.d.ts or package/index.d.ts", path)
	case strings.HasSuffix(lower, ".tgz") || strings.HasSuffix(lower, ".tar.gz"):
		b, err := os.ReadFile(path)
		if err != nil {
			return Snapshot{}, err
		}
		s, err := snapshotFromTarball(b)
		s.Source = path
		return s, err
	case strings.HasSuffix(lower, ".d.ts"):
		b, err := os.ReadFile(path)
		if err != nil {
			return Snapshot{}, err
		}
		m, err := ParseDTS(b)
		if err != nil {
			return Snapshot{}, fmt.Errorf("%s: %w", path, err)
		}
		s := Snapshot{Source: path, Format: "dts", Methods: m, HasSignatures: true}
		s.Version = packageVersion(filepath.Join(filepath.Dir(path), "package.json"))
		return s, nil
	case strings.HasSuffix(lower, ".json"):
		b, err := os.ReadFile(path)
		if err != nil {
			return Snapshot{}, err
		}
		s, err := snapshotFromJSON(b)
		s.Source = path
		return s, err
	}
	return Snapshot{}, fmt.Errorf("%s: unsupported source (want .tgz/.tar.gz, .d.ts, a directory, or .json)", path)
}

func packageVersion(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(b, &pkg)
	return pkg.Version
}

func snapshotFromTarball(b []byte) (Snapshot, error) {
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return Snapshot{}, fmt.Errorf("not a gzip tarball: %w", err)
	}
	tr := tar.NewReader(gz)
	var dts []byte
	version := ""
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Snapshot{}, fmt.Errorf("read tarball: %w", err)
		}
		name := strings.TrimPrefix(h.Name, "./")
		// npm packs under package/; accept any single top-level dir.
		base := name
		if i := strings.Index(name, "/"); i >= 0 {
			base = name[i+1:]
		}
		switch base {
		case "index.d.ts":
			if dts, err = io.ReadAll(io.LimitReader(tr, 64<<20)); err != nil {
				return Snapshot{}, err
			}
		case "package.json":
			pj, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return Snapshot{}, err
			}
			var pkg struct {
				Version string `json:"version"`
			}
			_ = json.Unmarshal(pj, &pkg)
			version = pkg.Version
		}
	}
	if dts == nil {
		return Snapshot{}, errors.New("tarball has no index.d.ts")
	}
	m, err := ParseDTS(dts)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Format: "dts", Version: version, Methods: m, HasSignatures: true}, nil
}

// docAPIClass is one class of a saved official `easyeda-pro doc api` dump
// (shape per the easyeda-client-cli docs: {title, methods:[{name,comment}],
// callPath}); the dump may wrap it in the CLI's {ok,value} envelope.
type docAPIClass struct {
	Name      string `json:"name"`
	ClassName string `json:"className"`
	CallPath  string `json:"callPath"`
	Methods   []struct {
		Name    string `json:"name"`
		Comment string `json:"comment"`
	} `json:"methods"`
}

func snapshotFromJSON(b []byte) (Snapshot, error) {
	var idx index
	if json.Unmarshal(b, &idx) == nil && len(idx.Records) > 0 {
		return Snapshot{Format: "api-index", Version: idx.Version, Methods: idx.Records, HasSignatures: true}, nil
	}
	// doc-api dump: an array, or {"classes": [...]} / {"classes": {name: cls}},
	// of classes or {ok,value} envelopes.
	var raw []json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		var wrap struct {
			Version string          `json:"version"`
			Classes json.RawMessage `json:"classes"`
		}
		if json.Unmarshal(b, &wrap) != nil || len(wrap.Classes) == 0 {
			return Snapshot{}, errors.New("JSON is neither an api-index (records) nor a doc-api dump (classes)")
		}
		if json.Unmarshal(wrap.Classes, &raw) != nil {
			var byName map[string]json.RawMessage
			if err := json.Unmarshal(wrap.Classes, &byName); err != nil {
				return Snapshot{}, errors.New("doc-api dump: `classes` must be an array or an object")
			}
			for _, k := range keys(byName) {
				raw = append(raw, byName[k])
			}
		}
		s, err := docAPISnapshot(raw)
		s.Version = wrap.Version
		return s, err
	}
	return docAPISnapshot(raw)
}

func docAPISnapshot(raw []json.RawMessage) (Snapshot, error) {
	var out []Method
	skipped := 0
	for _, r := range raw {
		var env struct {
			OK    *bool           `json:"ok"`
			Value json.RawMessage `json:"value"`
		}
		if json.Unmarshal(r, &env) == nil && env.OK != nil && len(env.Value) > 0 {
			r = env.Value
		}
		var c docAPIClass
		if json.Unmarshal(r, &c) != nil || c.CallPath == "" {
			skipped++ // enums / external classes have no eda.* call path
			continue
		}
		for _, m := range c.Methods {
			out = append(out, Method{NS: "eda." + c.CallPath, Method: m.Name, Summary: m.Comment})
		}
	}
	if len(out) == 0 {
		return Snapshot{}, fmt.Errorf("doc-api dump has no class with callPath and methods (%d entries skipped)", skipped)
	}
	return Snapshot{Format: "doc-api", Methods: out}, nil
}

// ── online (opt-in) ───────────────────────────────────────────────────────

// DefaultRegistry is the npm registry `--fetch` queries.
const DefaultRegistry = "https://registry.npmjs.org"

// FetchLatest downloads @jlceda/pro-api-types@latest from an npm registry.
// Only called behind `--fetch`; bounded by timeout and response size limits.
func FetchLatest(ctx context.Context, registry string, timeout time.Duration) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{}
	get := func(url string, limit int64) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, limit))
	}
	registry = strings.TrimRight(registry, "/")
	meta, err := get(registry+"/@jlceda/pro-api-types/latest", 1<<20)
	if err != nil {
		return Snapshot{}, err
	}
	var latest struct {
		Version string `json:"version"`
		Dist    struct {
			Tarball string `json:"tarball"`
		} `json:"dist"`
	}
	if err := json.Unmarshal(meta, &latest); err != nil || latest.Dist.Tarball == "" {
		return Snapshot{}, errors.New("registry response has no dist.tarball")
	}
	tgz, err := get(latest.Dist.Tarball, 64<<20)
	if err != nil {
		return Snapshot{}, err
	}
	s, err := snapshotFromTarball(tgz)
	if err != nil {
		return s, err
	}
	s.Source = latest.Dist.Tarball
	if s.Version == "" {
		s.Version = latest.Version
	}
	return s, nil
}

// ── diff ──────────────────────────────────────────────────────────────────

// Watch is a notable API whose appearance or stability change unlocks a
// planned capability. Reasons cite where we depend on or plan for it.
type Watch struct {
	NS, Method, Reason string
}

// Watchlist is the curated set of unlocks pcbpilot tracks.
var Watchlist = []Watch{
	{"eda.pcb_Document", "autoRouting", "host autorouter; was @alpha (dev-build gated) in our 2026-09 probe — comparison baseline for pkg/pcbauto, never a replacement"},
	{"eda.pcb_Document", "autoLayout", "host auto-placement; comparison baseline only"},
	{"eda.sch_PrimitiveBus", "create", "native schematic buses (connector schematic-bus.ts, @beta, live-unverified)"},
	{"eda.sch_PrimitiveBus", "getAll", "native bus inventory (connector schematic-bus.ts)"},
	{"eda.sch_PrimitiveAttribute", "createNetLabel", "native net labels; V3 3.2.149 returns undefined (connector actions.ts fallback)"},
	{"eda.sys_FileSystem", "getProjectsPaths", "local .eprj3 project roots for offline inspect-eprj3 fixtures"},
}

// Change is one class or method difference.
type Change struct {
	NS             string   `json:"namespace"`
	Method         string   `json:"method,omitempty"`
	Kind           string   `json:"kind"` // added | removed | signature | signature-cosmetic | stability
	Old            []string `json:"old,omitempty"`
	New            []string `json:"new,omitempty"`
	ConnectorUses  bool     `json:"connectorUses"`
	NamespaceInUse bool     `json:"namespaceInUse"`
	Notable        string   `json:"notable,omitempty"`
}

// DiffReport is the `api upstream-diff` result.
type DiffReport struct {
	Pinned         Snapshot `json:"pinned"`
	Newer          Snapshot `json:"newer"`
	SameVersion    bool     `json:"sameVersion"`
	AddedClasses   []string `json:"addedClasses"`
	RemovedClasses []string `json:"removedClasses"`
	Changes        []Change `json:"changes"`
	BreakingForUs  []Change `json:"breakingForConnector"`
	NotableUnlocks []Change `json:"notableUnlocks"`
	Counts         struct {
		Added, Removed, Signature, Cosmetic, Stability int
	} `json:"counts"`
	UsageSource string `json:"usageSource"`
	Note        string `json:"note"`
}

type methodInfo struct {
	sigs      []string
	stability []string
}

func group(ms []Method) map[string]map[string]*methodInfo {
	out := map[string]map[string]*methodInfo{}
	for _, m := range ms {
		if out[m.NS] == nil {
			out[m.NS] = map[string]*methodInfo{}
		}
		mi := out[m.NS][m.Method]
		if mi == nil {
			mi = &methodInfo{}
			out[m.NS][m.Method] = mi
		}
		if m.Sig != "" && !contains(mi.sigs, m.Sig) {
			mi.sigs = append(mi.sigs, m.Sig)
		}
		st := m.Stability
		if st == "" {
			st = "stable"
		}
		if !contains(mi.stability, st) {
			mi.stability = append(mi.stability, st)
		}
	}
	for _, ns := range out {
		for _, mi := range ns {
			sort.Strings(mi.sigs)
			sort.Strings(mi.stability)
		}
	}
	return out
}

// Diff compares pinned against newer and flags connector usage and watchlist
// hits. Signatures and stability are compared only when both sides have them.
func Diff(pinned, newer Snapshot, usage Usage) DiffReport {
	pinned.MethodCount, newer.MethodCount = len(pinned.Methods), len(newer.Methods)
	r := DiffReport{Pinned: pinned, Newer: newer, UsageSource: usage.Source,
		AddedClasses: []string{}, RemovedClasses: []string{}, Changes: []Change{},
		BreakingForUs: []Change{}, NotableUnlocks: []Change{}}
	r.SameVersion = pinned.Version != "" && pinned.Version == newer.Version
	useM := map[string]bool{}
	for _, m := range usage.Methods {
		useM[m] = true
	}
	useNS := map[string]bool{}
	for _, n := range usage.Namespaces {
		useNS[n] = true
	}
	watch := map[string]string{}
	for _, w := range Watchlist {
		watch[w.NS+"."+w.Method] = w.Reason
	}
	sigs := pinned.HasSignatures && newer.HasSignatures
	a, b := group(pinned.Methods), group(newer.Methods)
	mk := func(ns, method, kind string) Change {
		return Change{NS: ns, Method: method, Kind: kind, ConnectorUses: useM[ns+"."+method], NamespaceInUse: useNS[ns], Notable: watch[ns+"."+method]}
	}
	for _, ns := range keys(b) {
		if a[ns] == nil {
			r.AddedClasses = append(r.AddedClasses, ns)
		}
	}
	for _, ns := range keys(a) {
		if b[ns] == nil {
			r.RemovedClasses = append(r.RemovedClasses, ns)
		}
	}
	allNS := keys(a)
	for _, ns := range keys(b) {
		if a[ns] == nil {
			allNS = append(allNS, ns)
		}
	}
	sort.Strings(allNS)
	for _, ns := range allNS {
		names := map[string]bool{}
		for m := range a[ns] {
			names[m] = true
		}
		for m := range b[ns] {
			names[m] = true
		}
		for _, m := range keys(names) {
			oldM, newM := a[ns][m], b[ns][m]
			switch {
			case oldM == nil:
				c := mk(ns, m, "added")
				if sigs {
					c.New = newM.sigs
				}
				r.Changes = append(r.Changes, c)
				r.Counts.Added++
			case newM == nil:
				c := mk(ns, m, "removed")
				c.Old = oldM.sigs
				r.Changes = append(r.Changes, c)
				r.Counts.Removed++
			case sigs && strings.Join(oldM.sigs, "\n") != strings.Join(newM.sigs, "\n"):
				kind := "signature"
				if canonicalSigs(oldM.sigs) == canonicalSigs(newM.sigs) {
					kind = "signature-cosmetic"
					r.Counts.Cosmetic++
				} else {
					r.Counts.Signature++
				}
				c := mk(ns, m, kind)
				c.Old, c.New = oldM.sigs, newM.sigs
				r.Changes = append(r.Changes, c)
				if strings.Join(oldM.stability, ",") != strings.Join(newM.stability, ",") {
					s := mk(ns, m, "stability")
					s.Old, s.New = oldM.stability, newM.stability
					r.Changes = append(r.Changes, s)
					r.Counts.Stability++
				}
			case sigs && strings.Join(oldM.stability, ",") != strings.Join(newM.stability, ","):
				c := mk(ns, m, "stability")
				c.Old, c.New = oldM.stability, newM.stability
				r.Changes = append(r.Changes, c)
				r.Counts.Stability++
			}
		}
	}
	for _, c := range r.Changes {
		if c.ConnectorUses && (c.Kind == "removed" || c.Kind == "signature") {
			r.BreakingForUs = append(r.BreakingForUs, c)
		}
		if c.Notable != "" {
			r.NotableUnlocks = append(r.NotableUnlocks, c)
		}
	}
	r.Note = "types are a lead, not proof: a method present in pro-api-types may still be gated or absent on a given host; confirm with `pcbpilot api probe` before building a typed action on it."
	if !sigs {
		r.Note += " One side has no signatures (doc-api dump): only added/removed names are compared."
	}
	return r
}

var (
	cosmeticIndexRE = regexp.MustCompile(`\{ \[key: string\]: ([^{}]*) \}`)
	cosmeticArrayRE = regexp.MustCompile(`Array<([A-Za-z0-9_.]+(?:<[^<>]*>)?)>`)
)

// canonicalSigs folds spellings TypeScript treats as identical
// (`{ [key: string]: T }` ≡ `Record<string, T>`, `Array<T>` ≡ `T[]`) so a
// re-spelled declaration is reported as signature-cosmetic, not breaking.
func canonicalSigs(sigs []string) string {
	out := make([]string, len(sigs))
	for i, s := range sigs {
		for {
			n := cosmeticIndexRE.ReplaceAllString(s, "Record<string, $1>")
			n = cosmeticArrayRE.ReplaceAllString(n, "$1[]")
			if n == s {
				break
			}
			s = n
		}
		out[i] = s
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
