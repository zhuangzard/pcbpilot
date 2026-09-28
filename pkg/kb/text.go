package kb

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Chunk is one searchable window of a document.
type Chunk struct {
	N    int    `json:"n"`
	Page int    `json:"page"` // 1-based; 0 for page-less formats
	Text string `json:"text"`
}

// Chunking parameters (runes). ~1200 runes is ~300 English words or ~600
// Chinese characters: large enough to keep a table row or a paragraph with its
// heading, small enough that a search hit is cheap to deep-read.
const (
	chunkRunes   = 1200
	overlapRunes = 150
)

// ChunkPages splits page texts into overlapping chunks, preferring paragraph
// and sentence boundaries. Chunks never span pages so a hit cites one page.
func ChunkPages(pages []string, paged bool) []Chunk {
	var out []Chunk
	for i, p := range pages {
		page := 0
		if paged {
			page = i + 1
		}
		for _, t := range splitRunes(p, chunkRunes, overlapRunes) {
			out = append(out, Chunk{N: len(out), Page: page, Text: t})
		}
	}
	return out
}

func splitRunes(s string, size, overlap int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	r := []rune(s)
	if len(r) <= size {
		return []string{s}
	}
	var out []string
	start := 0
	for start < len(r) {
		end := start + size
		if end >= len(r) {
			out = append(out, strings.TrimSpace(string(r[start:])))
			break
		}
		// Back off to a boundary in the last third of the window.
		cut := end
		for j := end; j > start+size*2/3; j-- {
			if r[j] == '\n' {
				cut = j
				break
			}
		}
		if cut == end {
			for j := end; j > start+size*2/3; j-- {
				if isSentenceEnd(r[j-1]) {
					cut = j
					break
				}
			}
		}
		out = append(out, strings.TrimSpace(string(r[start:cut])))
		next := cut - overlap
		if next <= start {
			next = cut
		}
		start = next
	}
	return out
}

func isSentenceEnd(r rune) bool {
	switch r {
	case '.', '!', '?', ';', '。', '！', '？', '；':
		return true
	}
	return false
}

// stopwords are dropped from English queries and documents.
var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true, "by": true,
	"for": true, "from": true, "in": true, "is": true, "it": true, "of": true, "on": true, "or": true,
	"that": true, "the": true, "this": true, "to": true, "was": true, "with": true, "which": true,
}

// isCJK reports Han, Hiragana, Katakana and Hangul letters.
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// Tokenize lowercases and splits text into search terms. Latin/digit runs are
// words (so part numbers like "ams1117" or "lm358" stay whole, and "ipc-2221"
// yields "ipc" and "2221"); CJK runs yield overlapping bigrams (plus the single
// character for one-character runs), the standard trick for word-segmentation
// free Chinese search.
func Tokenize(s string) []string {
	var out []string
	var word []rune
	var cjk []rune
	flushWord := func() {
		if len(word) > 0 {
			w := string(word)
			if !stopwords[w] && (len(word) > 1 || unicode.IsDigit(word[0])) {
				out = append(out, w)
			}
			word = word[:0]
		}
	}
	flushCJK := func() {
		switch {
		case len(cjk) == 1:
			out = append(out, string(cjk))
		case len(cjk) > 1:
			for i := 0; i+1 < len(cjk); i++ {
				out = append(out, string(cjk[i:i+2]))
			}
		}
		cjk = cjk[:0]
	}
	for _, r := range s {
		switch {
		case isCJK(r):
			flushWord()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			flushCJK()
			word = append(word, unicode.ToLower(r))
		case r == '.' && len(word) > 0 && unicode.IsDigit(word[len(word)-1]):
			// keep decimals like 3.3 together
			word = append(word, r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	for i, t := range out {
		out[i] = strings.TrimRight(t, ".")
	}
	return out
}

// snippet returns ~240 runes around the first query-term occurrence.
func snippet(text string, terms []string) string {
	lower := strings.ToLower(text)
	pos := -1
	for _, t := range terms {
		if i := strings.Index(lower, t); i >= 0 && (pos < 0 || i < pos) {
			pos = i
		}
	}
	const width = 240
	r := []rune(text)
	if pos < 0 {
		if len(r) > width {
			return string(r[:width]) + "…"
		}
		return text
	}
	runePos := utf8.RuneCountInString(lower[:pos])
	start := runePos - width/3
	if start < 0 {
		start = 0
	}
	end := start + width
	if end > len(r) {
		end = len(r)
	}
	s := strings.ReplaceAll(string(r[start:end]), "\n", " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(r) {
		s += "…"
	}
	return s
}
