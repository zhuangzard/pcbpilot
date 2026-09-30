package kb

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// Extracted is the text of one document, split by page (one element for
// formats without pages).
type Extracted struct {
	Pages     []string
	Extractor string
	Title     string
	// Note explains a partial or empty extraction (e.g. scanned PDF).
	Note string
}

// Limits keep one hostile or huge file from exhausting the daemon.
const (
	maxPages     = 3000
	maxTextBytes = 32 << 20
)

// ErrUnsupported marks a file that is stored but not text-indexed.
var ErrUnsupported = errors.New("format not text-indexed")

// indexable lists extensions with a text extractor.
var indexable = map[string]string{
	".pdf": "pdf", ".md": "text", ".markdown": "text", ".txt": "text", ".csv": "text",
	".json": "text", ".html": "html", ".htm": "html", ".docx": "docx",
}

// Indexable reports whether a file name has a text extractor.
func Indexable(name string) bool {
	_, ok := indexable[strings.ToLower(filepath.Ext(name))]
	return ok
}

// Extract returns the text of a file.
func Extract(path string) (*Extracted, error) {
	switch indexable[strings.ToLower(filepath.Ext(path))] {
	case "pdf":
		return extractPDF(path)
	case "text":
		b, err := readLimited(path)
		if err != nil {
			return nil, err
		}
		return &Extracted{Pages: []string{toUTF8(b)}, Extractor: "text", Title: firstHeading(string(b))}, nil
	case "html":
		b, err := readLimited(path)
		if err != nil {
			return nil, err
		}
		title := ""
		if m := titleRe.FindSubmatch(b); m != nil {
			title = strings.TrimSpace(html.UnescapeString(string(m[1])))
		}
		return &Extracted{Pages: []string{stripHTML(toUTF8(b))}, Extractor: "html", Title: title}, nil
	case "docx":
		return extractDOCX(path)
	}
	return nil, ErrUnsupported
}

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxTextBytes))
}

func toUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}

var headingRe = regexp.MustCompile(`(?m)^#\s+(.+)$`)

func firstHeading(s string) string {
	if m := headingRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

var (
	titleRe   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	scriptRe  = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	blockRe   = regexp.MustCompile(`(?i)</?(p|div|br|li|tr|h[1-6]|section|article|table)[^>]*>`)
	tagRe     = regexp.MustCompile(`(?s)<[^>]*>`)
	spacesRe  = regexp.MustCompile(`[ \t\r\f\v]+`)
	newlineRe = regexp.MustCompile(`\n{3,}`)
)

func stripHTML(s string) string {
	s = scriptRe.ReplaceAllString(s, " ")
	s = blockRe.ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return normalizeSpace(s)
}

func normalizeSpace(s string) string {
	s = spacesRe.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(newlineRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// extractDOCX reads word/document.xml and keeps paragraph breaks.
func extractDOCX(path string) (*Extracted, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("docx: %w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		dec := xml.NewDecoder(io.LimitReader(rc, maxTextBytes))
		var b strings.Builder
		inText := false
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("docx: %w", err)
			}
			switch t := tok.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "t":
					inText = true
				case "tab":
					b.WriteByte('\t')
				case "br", "cr":
					b.WriteByte('\n')
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "t":
					inText = false
				case "p":
					b.WriteByte('\n')
				}
			case xml.CharData:
				if inText {
					b.Write(t)
				}
			}
		}
		return &Extracted{Pages: []string{normalizeSpace(b.String())}, Extractor: "docx"}, nil
	}
	return nil, fmt.Errorf("docx: word/document.xml not found")
}

// extractPDF uses the pure-Go reader first (single binary, no system deps) and
// falls back to poppler's pdftotext when present and the pure-Go pass returns
// little text (common for CID fonts without ToUnicode maps).
func extractPDF(path string) (*Extracted, error) {
	ex, goErr := extractPDFGo(path)
	if goErr == nil && !lowText(ex) {
		return ex, nil
	}
	if bin, err := exec.LookPath("pdftotext"); err == nil {
		if alt, err := extractPDFPoppler(bin, path); err == nil && (ex == nil || textLen(alt) > textLen(ex)) {
			if goErr != nil {
				alt.Note = "pure-Go reader failed (" + goErr.Error() + "); used pdftotext"
			}
			return alt, nil
		}
	}
	if goErr != nil {
		return nil, goErr
	}
	if lowText(ex) {
		ex.Note = "little or no extractable text (scanned PDF?) — OCR is not built in; install poppler (pdftotext) or add an OCR'd copy"
	}
	return ex, nil
}

func textLen(e *Extracted) int {
	n := 0
	for _, p := range e.Pages {
		n += len(strings.TrimSpace(p))
	}
	return n
}

func lowText(e *Extracted) bool {
	if e == nil || len(e.Pages) == 0 {
		return true
	}
	return textLen(e) < 40*len(e.Pages)
}

func extractPDFGo(path string) (ex *Extracted, err error) {
	defer func() {
		if r := recover(); r != nil {
			ex, err = nil, fmt.Errorf("pdf: reader panic: %v", r)
		}
	}()
	f, r, err := pdf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("pdf: %w", err)
	}
	defer f.Close()
	n := r.NumPage()
	if n > maxPages {
		n = maxPages
	}
	ex = &Extracted{Extractor: "go-pdf"}
	total := 0
	fonts := map[string]*pdf.Font{}
	for i := 1; i <= n; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			ex.Pages = append(ex.Pages, "")
			continue
		}
		for _, name := range p.Fonts() {
			if _, ok := fonts[name]; !ok {
				fnt := p.Font(name)
				fonts[name] = &fnt
			}
		}
		text, perr := pageText(p, fonts)
		if perr != nil {
			text = ""
		}
		total += len(text)
		if total > maxTextBytes {
			ex.Note = "text truncated at size limit"
			break
		}
		ex.Pages = append(ex.Pages, normalizeSpace(text))
	}
	if info := r.Trailer().Key("Info"); !info.IsNull() {
		ex.Title = strings.TrimSpace(info.Key("Title").Text())
	}
	return ex, nil
}

// pageText prefers row-ordered text (keeps table rows together) and falls back
// to the plain content stream order.
func pageText(p pdf.Page, fonts map[string]*pdf.Font) (s string, err error) {
	defer func() {
		if r := recover(); r != nil {
			s, err = "", fmt.Errorf("page panic: %v", r)
		}
	}()
	rows, err := p.GetTextByRow()
	if err == nil && len(rows) > 0 {
		var b strings.Builder
		for _, row := range rows {
			for i, w := range row.Content {
				if i > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(w.S)
			}
			b.WriteByte('\n')
		}
		if strings.TrimSpace(b.String()) != "" {
			return b.String(), nil
		}
	}
	return p.GetPlainText(fonts)
}

func extractPDFPoppler(bin, path string) (*Extracted, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "-layout", "-enc", "UTF-8", path, "-")
	cmd.Stdout = &limitedWriter{w: &out, n: maxTextBytes}
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	pages := strings.Split(out.String(), "\f")
	if len(pages) > 0 && strings.TrimSpace(pages[len(pages)-1]) == "" {
		pages = pages[:len(pages)-1]
	}
	for i := range pages {
		pages[i] = normalizeSpace(pages[i])
	}
	return &Extracted{Pages: pages, Extractor: "pdftotext"}, nil
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	q := p
	if len(q) > l.n {
		q = q[:l.n]
	}
	l.n -= len(q)
	if _, err := l.w.Write(q); err != nil {
		return 0, err
	}
	return len(p), nil
}
