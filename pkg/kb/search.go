package kb

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SearchOptions filters a search.
type SearchOptions struct {
	K    int    // max hits (default 10)
	Kind string // only this kind
	Tag  string // only docs with this tag
	Doc  string // only this doc id
	// PerDoc caps hits per document (default 3) so one long datasheet cannot
	// crowd out every other source.
	PerDoc int
}

// Hit is one ranked chunk.
type Hit struct {
	DocID   string  `json:"docId"`
	Title   string  `json:"title"`
	Path    string  `json:"path"`
	Kind    string  `json:"kind"`
	Chunk   int     `json:"chunk"`
	Page    int     `json:"page,omitempty"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
	// Cite is the stable reference agents paste into reports: kb:<id>#p<page>
	// (or #c<chunk> for page-less formats).
	Cite string `json:"cite"`
}

// DocHit aggregates hits per document (the "screen" step).
type DocHit struct {
	DocID   string  `json:"docId"`
	Title   string  `json:"title"`
	Path    string  `json:"path"`
	Kind    string  `json:"kind"`
	Score   float64 `json:"score"` // best chunk
	Hits    int     `json:"hits"`
	Pages   []int   `json:"pages,omitempty"`
	Summary string  `json:"summary,omitempty"`
}

// Result is a search answer.
type Result struct {
	Query  string   `json:"query"`
	Terms  []string `json:"terms"`
	Docs   int      `json:"docsSearched"`
	Chunks int      `json:"chunksSearched"`
	Hits   []Hit    `json:"hits"`
	ByDoc  []DocHit `json:"byDoc"`
	TookMs int64    `json:"tookMs"`
}

// postingsCache keeps decoded postings across searches in one process (the
// console); keyed by path with mtime+size invalidation.
var postingsCache = struct {
	sync.Mutex
	m map[string]cachedPostings
}{m: map[string]cachedPostings{}}

type cachedPostings struct {
	mod  time.Time
	size int64
	p    *Postings
}

func (l *Library) postings(id string) (*Postings, error) {
	path := filepath.Join(l.indexDir(), "terms", id+".json")
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	postingsCache.Lock()
	c, ok := postingsCache.m[path]
	postingsCache.Unlock()
	if ok && c.mod.Equal(st.ModTime()) && c.size == st.Size() {
		return c.p, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Postings
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	postingsCache.Lock()
	postingsCache.m[path] = cachedPostings{st.ModTime(), st.Size(), &p}
	postingsCache.Unlock()
	return &p, nil
}

// Search ranks chunks with Okapi BM25 (k1 = 1.2, b = 0.75) over the whole
// library; IDF is computed across chunks of the filtered set.
func (l *Library) Search(query string, opt SearchOptions) (*Result, error) {
	started := time.Now()
	if opt.K <= 0 {
		opt.K = 10
	}
	if opt.PerDoc <= 0 {
		opt.PerDoc = 3
	}
	cat, err := l.Load()
	if err != nil {
		return nil, err
	}
	terms := uniq(Tokenize(query))
	res := &Result{Query: query, Terms: terms, Hits: []Hit{}, ByDoc: []DocHit{}}
	if len(terms) == 0 {
		return res, nil
	}
	type docP struct {
		d *Doc
		p *Postings
	}
	var set []docP
	totalLen, nChunks := 0, 0
	for _, d := range cat.Docs {
		if d.Status != "indexed" || (opt.Kind != "" && d.Kind != opt.Kind) ||
			(opt.Tag != "" && !contains(d.Tags, strings.ToLower(opt.Tag))) ||
			(opt.Doc != "" && !strings.HasPrefix(d.ID, opt.Doc)) {
			continue
		}
		p, err := l.postings(d.ID)
		if err != nil {
			continue
		}
		set = append(set, docP{d, p})
		for _, n := range p.Lens {
			totalLen += n
		}
		nChunks += len(p.Lens)
	}
	res.Docs, res.Chunks = len(set), nChunks
	if nChunks == 0 {
		return res, nil
	}
	avg := float64(totalLen) / float64(nChunks)
	df := map[string]int{}
	for _, dp := range set {
		for _, t := range terms {
			df[t] += len(dp.p.TF[t])
		}
	}
	const k1, b = 1.2, 0.75
	type scored struct {
		dp    int
		chunk int
		score float64
	}
	var all []scored
	for i, dp := range set {
		acc := map[int]float64{}
		for _, t := range terms {
			if df[t] == 0 {
				continue
			}
			idf := math.Log(1 + (float64(nChunks)-float64(df[t])+0.5)/(float64(df[t])+0.5))
			for _, pc := range dp.p.TF[t] {
				chunk, tf := pc[0], float64(pc[1])
				dl := float64(dp.p.Lens[chunk])
				acc[chunk] += idf * tf * (k1 + 1) / (tf + k1*(1-b+b*dl/avg))
			}
		}
		for c, s := range acc {
			all = append(all, scored{i, c, s})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		if all[i].dp != all[j].dp {
			return set[all[i].dp].d.ID < set[all[j].dp].d.ID
		}
		return all[i].chunk < all[j].chunk
	})
	byDoc := map[string]*DocHit{}
	perDoc := map[string]int{}
	chunkCache := map[string][]Chunk{}
	for _, s := range all {
		d := set[s.dp].d
		dh := byDoc[d.ID]
		if dh == nil {
			dh = &DocHit{DocID: d.ID, Title: title(d), Path: d.Path, Kind: d.Kind, Score: round3(s.score)}
			if d.Summary != nil {
				dh.Summary = d.Summary.Text
			}
			byDoc[d.ID] = dh
		}
		dh.Hits++
		if len(res.Hits) >= opt.K || perDoc[d.ID] >= opt.PerDoc {
			continue
		}
		chunks, ok := chunkCache[d.ID]
		if !ok {
			chunks, _ = l.Chunks(d.ID)
			chunkCache[d.ID] = chunks
		}
		h := Hit{DocID: d.ID, Title: title(d), Path: d.Path, Kind: d.Kind, Chunk: s.chunk, Score: round3(s.score)}
		if s.chunk < len(chunks) {
			h.Page = chunks[s.chunk].Page
			h.Snippet = snippet(chunks[s.chunk].Text, terms)
		}
		h.Cite = Cite(d.ID, h.Page, s.chunk)
		if h.Page > 0 && !containsInt(dh.Pages, h.Page) {
			dh.Pages = append(dh.Pages, h.Page)
		}
		perDoc[d.ID]++
		res.Hits = append(res.Hits, h)
	}
	for _, dh := range byDoc {
		sort.Ints(dh.Pages)
		res.ByDoc = append(res.ByDoc, *dh)
	}
	sort.Slice(res.ByDoc, func(i, j int) bool {
		if res.ByDoc[i].Score != res.ByDoc[j].Score {
			return res.ByDoc[i].Score > res.ByDoc[j].Score
		}
		return res.ByDoc[i].DocID < res.ByDoc[j].DocID
	})
	res.TookMs = time.Since(started).Milliseconds()
	return res, nil
}

// Cite formats the citation key agents put in reports.
func Cite(id string, page, chunk int) string {
	if page > 0 {
		return "kb:" + id + "#p" + itoa(page)
	}
	return "kb:" + id + "#c" + itoa(chunk)
}

func title(d *Doc) string {
	if d.Title != "" {
		return d.Title
	}
	return d.Name
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func containsInt(l []int, v int) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func itoa(i int) string { return strconv.Itoa(i) }
