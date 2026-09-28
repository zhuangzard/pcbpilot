// Package kb is the per-project resource library: papers, datasheets,
// standards, requirements, reference designs and mechanical drawings stored
// under <work dir>/resources/ and indexed under <work dir>/.pcbpilot-kb/.
//
// It is built for "index → screen → deep-read": agents search (BM25 over
// page-bounded chunks), screen hits per document, and only then read the
// chunks or pages they cite. Nothing here ever loads every document into an
// agent's context. Summaries are slots filled later by agents (set-summary).
//
// On-disk layout (all JSON, inspectable):
//
//	resources/<kind>/<file>            the stored original (read-only to the index)
//	.pcbpilot-kb/catalog.json          document metadata (schemaVersion 1)
//	.pcbpilot-kb/text/<id>.jsonl       one chunk per line {n,page,text}
//	.pcbpilot-kb/terms/<id>.json       per-document postings {chunks:[len…], tf:{term:[[chunk,count]…]}}
package kb

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// SchemaVersion of catalog.json.
const SchemaVersion = 1

// IndexDirName is the index directory inside a work dir.
const IndexDirName = ".pcbpilot-kb"

// Kinds are the resource categories (also the resources/ subdirectories).
var Kinds = []string{"paper", "datasheet", "standard", "requirement", "reference-design", "mech", "other"}

// ValidKind reports whether k is a known kind.
func ValidKind(k string) bool {
	for _, v := range Kinds {
		if v == k {
			return true
		}
	}
	return false
}

// Doc is one catalog entry.
type Doc struct {
	ID        string   `json:"id"`
	SHA256    string   `json:"sha256"`
	Path      string   `json:"path"` // relative to the work dir
	Name      string   `json:"name"`
	Title     string   `json:"title,omitempty"`
	Kind      string   `json:"kind"`
	Tags      []string `json:"tags,omitempty"`
	Bytes     int64    `json:"bytes"`
	Pages     int      `json:"pages,omitempty"`
	Chunks    int      `json:"chunks"`
	Chars     int      `json:"chars"`
	Extractor string   `json:"extractor,omitempty"`
	// Status: indexed | stored-only (no text extractor) | no-text | error
	Status  string   `json:"status"`
	Note    string   `json:"note,omitempty"`
	AddedAt string   `json:"addedAt"`
	Summary *Summary `json:"summary,omitempty"`
	// Aliases are other source paths that had the same content (dedupe).
	Aliases []string `json:"aliases,omitempty"`
}

// Summary is the per-document slot an agent fills after deep-reading.
type Summary struct {
	Text      string   `json:"text"`
	KeyPoints []string `json:"keyPoints,omitempty"`
	By        string   `json:"by,omitempty"`
	At        string   `json:"at"`
}

// Catalog is catalog.json.
type Catalog struct {
	SchemaVersion int    `json:"schemaVersion"`
	Docs          []*Doc `json:"docs"`
}

// Library is one work dir's resource library.
type Library struct {
	Dir          string // work dir
	ResourcesDir string // absolute resources dir
	mu           sync.Mutex
}

// Open returns the library of a work dir; resources is relative to it
// (default "resources").
func Open(dir, resources string) (*Library, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if resources == "" {
		resources = "resources"
	}
	if filepath.IsAbs(resources) || strings.HasPrefix(filepath.Clean(resources), "..") {
		return nil, fmt.Errorf("resources dir must be inside the work dir: %s", resources)
	}
	return &Library{Dir: abs, ResourcesDir: filepath.Join(abs, resources)}, nil
}

func (l *Library) indexDir() string { return filepath.Join(l.Dir, IndexDirName) }

// Load reads the catalog (empty when none).
func (l *Library) Load() (*Catalog, error) {
	b, err := os.ReadFile(filepath.Join(l.indexDir(), "catalog.json"))
	if errors.Is(err, os.ErrNotExist) {
		return &Catalog{SchemaVersion: SchemaVersion}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("catalog.json: %w", err)
	}
	if c.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("catalog.json schemaVersion %d is newer than this pcbpilot", c.SchemaVersion)
	}
	return &c, nil
}

func (l *Library) save(c *Catalog) error {
	c.SchemaVersion = SchemaVersion
	sort.SliceStable(c.Docs, func(i, j int) bool { return c.Docs[i].AddedAt < c.Docs[j].AddedAt })
	return writeJSONAtomic(filepath.Join(l.indexDir(), "catalog.json"), c)
}

func writeJSONAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// lock takes the cross-process index lock (a lock file; stale after 2 min).
func (l *Library) lock() (func(), error) {
	l.mu.Lock()
	if err := os.MkdirAll(l.indexDir(), 0o755); err != nil {
		l.mu.Unlock()
		return nil, err
	}
	path := filepath.Join(l.indexDir(), "lock")
	deadline := time.Now().Add(30 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { os.Remove(path); l.mu.Unlock() }, nil
		}
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > 2*time.Minute {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			l.mu.Unlock()
			return nil, fmt.Errorf("kb index is locked by another process (%s)", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// AddOptions controls Add.
type AddOptions struct {
	Kind  string
	Tags  []string
	Title string
	// Move removes the source after a successful copy (console uploads use a
	// temp file). Sources already inside the resources dir are never copied.
	Move bool
	// Name overrides the stored file name (sanitized).
	Name string
}

// AddResult reports one Add.
type AddResult struct {
	Doc       *Doc   `json:"doc"`
	Duplicate bool   `json:"duplicate,omitempty"`
	Source    string `json:"source"`
	Error     string `json:"error,omitempty"`
}

// Add stores and indexes files. Directories are walked (hidden entries and the
// index dir skipped). Content already in the catalog (same sha256) is not
// stored twice; the new source path is recorded as an alias.
func (l *Library) Add(paths []string, opt AddOptions) ([]AddResult, error) {
	if opt.Kind == "" {
		opt.Kind = "other"
	}
	if !ValidKind(opt.Kind) {
		return nil, fmt.Errorf("unknown kind %q (have: %s)", opt.Kind, strings.Join(Kinds, ", "))
	}
	var files []string
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			files = append(files, p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if strings.HasPrefix(d.Name(), ".") && path != p {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.IsDir() && d.Type().IsRegular() {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	unlock, err := l.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	cat, err := l.Load()
	if err != nil {
		return nil, err
	}
	bySHA := map[string]*Doc{}
	for _, d := range cat.Docs {
		bySHA[d.SHA256] = d
	}
	var out []AddResult
	for _, f := range files {
		res := l.addOne(cat, bySHA, f, opt)
		out = append(out, res)
	}
	if err := l.save(cat); err != nil {
		return out, err
	}
	return out, nil
}

func (l *Library) addOne(cat *Catalog, bySHA map[string]*Doc, src string, opt AddOptions) AddResult {
	res := AddResult{Source: src}
	sum, size, err := fileSHA256(src)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if d, ok := bySHA[sum]; ok {
		res.Doc, res.Duplicate = d, true
		rel := l.rel(src)
		if rel != d.Path && !contains(d.Aliases, rel) {
			d.Aliases = append(d.Aliases, rel)
		}
		d.Tags = mergeTags(d.Tags, opt.Tags)
		if opt.Move {
			os.Remove(src)
		}
		return res
	}
	absSrc, _ := filepath.Abs(src)
	stored := absSrc
	if !within(l.ResourcesDir, absSrc) {
		name := opt.Name
		if name == "" {
			name = filepath.Base(src)
		}
		name = SanitizeName(name)
		dst, err := uniquePath(filepath.Join(l.ResourcesDir, opt.Kind), name)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		if err := copyFile(absSrc, dst); err != nil {
			res.Error = err.Error()
			return res
		}
		if opt.Move {
			os.Remove(absSrc)
		}
		stored = dst
	}
	d := &Doc{ID: sum[:12], SHA256: sum, Path: l.rel(stored), Name: filepath.Base(stored), Kind: opt.Kind,
		Tags: mergeTags(nil, opt.Tags), Bytes: size, AddedAt: time.Now().UTC().Format(time.RFC3339Nano), Title: opt.Title}
	l.index(d, stored)
	cat.Docs = append(cat.Docs, d)
	bySHA[sum] = d
	res.Doc = d
	return res
}

// index extracts, chunks and writes the text + postings of one doc.
func (l *Library) index(d *Doc, path string) {
	ex, err := Extract(path)
	if errors.Is(err, ErrUnsupported) {
		d.Status = "stored-only"
		d.Note = "no text extractor for this format; stored for reference"
		return
	}
	if err != nil {
		d.Status, d.Note = "error", err.Error()
		return
	}
	d.Extractor = ex.Extractor
	d.Note = ex.Note
	if d.Title == "" {
		d.Title = ex.Title
	}
	paged := ex.Extractor == "go-pdf" || ex.Extractor == "pdftotext"
	if paged {
		d.Pages = len(ex.Pages)
	}
	chunks := ChunkPages(ex.Pages, paged)
	for _, c := range chunks {
		d.Chars += len([]rune(c.Text))
	}
	d.Chunks = len(chunks)
	if len(chunks) == 0 {
		d.Status = "no-text"
		if d.Note == "" {
			d.Note = "no extractable text"
		}
		return
	}
	if err := l.writeChunks(d.ID, chunks); err != nil {
		d.Status, d.Note = "error", err.Error()
		return
	}
	d.Status = "indexed"
}

// Postings is terms/<id>.json.
type Postings struct {
	Lens []int               `json:"lens"` // tokens per chunk
	TF   map[string][][2]int `json:"tf"`   // term → [[chunk, count]…]
}

func (l *Library) writeChunks(id string, chunks []Chunk) error {
	textDir := filepath.Join(l.indexDir(), "text")
	if err := os.MkdirAll(textDir, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(textDir, id+".jsonl"))
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	post := Postings{TF: map[string][][2]int{}}
	for _, c := range chunks {
		if err := enc.Encode(c); err != nil {
			f.Close()
			return err
		}
		toks := Tokenize(c.Text)
		post.Lens = append(post.Lens, len(toks))
		counts := map[string]int{}
		for _, t := range toks {
			counts[t]++
		}
		for t, n := range counts {
			post.TF[t] = append(post.TF[t], [2]int{c.N, n})
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	b, err := json.Marshal(post)
	if err != nil {
		return err
	}
	termDir := filepath.Join(l.indexDir(), "terms")
	if err := os.MkdirAll(termDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(termDir, id+".json"), b, 0o644)
}

// Reindex re-extracts every document (after an extractor upgrade).
func (l *Library) Reindex() (*Catalog, error) {
	unlock, err := l.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	cat, err := l.Load()
	if err != nil {
		return nil, err
	}
	for _, d := range cat.Docs {
		d.Status, d.Note, d.Chunks, d.Chars, d.Pages = "", "", 0, 0, 0
		l.index(d, filepath.Join(l.Dir, d.Path))
	}
	return cat, l.save(cat)
}

// Find returns a doc by id or unique id prefix.
func (c *Catalog) Find(id string) (*Doc, error) {
	var hit *Doc
	for _, d := range c.Docs {
		if d.ID == id {
			return d, nil
		}
		if len(id) >= 4 && strings.HasPrefix(d.ID, id) {
			if hit != nil {
				return nil, fmt.Errorf("id prefix %q is ambiguous", id)
			}
			hit = d
		}
	}
	if hit == nil {
		return nil, fmt.Errorf("no document %q", id)
	}
	return hit, nil
}

// Chunks reads the stored chunks of a doc.
func (l *Library) Chunks(id string) ([]Chunk, error) {
	f, err := os.Open(filepath.Join(l.indexDir(), "text", id+".jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Chunk
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var c Chunk
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, sc.Err()
}

// Update applies fn to one doc under the lock and saves.
func (l *Library) Update(id string, fn func(*Doc) error) (*Doc, error) {
	unlock, err := l.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	cat, err := l.Load()
	if err != nil {
		return nil, err
	}
	d, err := cat.Find(id)
	if err != nil {
		return nil, err
	}
	if err := fn(d); err != nil {
		return nil, err
	}
	return d, l.save(cat)
}

// SetSummary fills a doc's summary slot.
func (l *Library) SetSummary(id string, s Summary) (*Doc, error) {
	if strings.TrimSpace(s.Text) == "" {
		return nil, fmt.Errorf("summary text is empty")
	}
	if s.At == "" {
		s.At = time.Now().UTC().Format(time.RFC3339)
	}
	return l.Update(id, func(d *Doc) error { d.Summary = &s; return nil })
}

// SetTags adds and removes tags.
func (l *Library) SetTags(id string, add, remove []string) (*Doc, error) {
	return l.Update(id, func(d *Doc) error {
		d.Tags = mergeTags(d.Tags, add)
		var keep []string
		for _, t := range d.Tags {
			if !contains(remove, t) {
				keep = append(keep, t)
			}
		}
		d.Tags = keep
		return nil
	})
}

func (l *Library) rel(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if r, err := filepath.Rel(l.Dir, abs); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return abs
}

func within(root, p string) bool {
	r, err := filepath.Rel(root, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

func fileSHA256(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

func uniquePath(dir, name string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		cand := name
		if i > 0 {
			cand = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		p := filepath.Join(dir, cand)
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p, nil
		}
	}
	return "", fmt.Errorf("too many files named %s", name)
}

// SanitizeName keeps a safe single path component: letters (any script),
// digits, dot, dash, underscore, space → dash; no leading dot; ≤ 120 runes.
func SanitizeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		case unicode.IsSpace(r) || r == '(' || r == ')' || r == '[' || r == ']' || r == '+':
			b.WriteRune('-')
		}
	}
	s := strings.Trim(b.String(), ".-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	for strings.Contains(s, "..") {
		s = strings.ReplaceAll(s, "..", ".")
	}
	if r := []rune(s); len(r) > 120 {
		ext := filepath.Ext(s)
		s = string(r[:120-len([]rune(ext))]) + ext
	}
	if s == "" {
		s = "file"
	}
	return s
}

func mergeTags(have, add []string) []string {
	for _, t := range add {
		t = strings.TrimSpace(strings.ToLower(t))
		if t != "" && !contains(have, t) {
			have = append(have, t)
		}
	}
	sort.Strings(have)
	return have
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
