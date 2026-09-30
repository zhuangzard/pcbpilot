package kb

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalPDF builds a valid multi-page PDF (Helvetica, one text line per page)
// with a correct xref table, so the pure-Go extractor is exercised end to end.
func minimalPDF(pages []string) []byte {
	var objs []string
	n := len(pages)
	// 1 catalog, 2 pages, 3 font, then per page: page obj + content obj.
	kids := make([]string, n)
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	objs = append(objs,
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	)
	for i, text := range pages {
		stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
		objs = append(objs,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", 5+2*i),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		)
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

func minimalDOCX(t *testing.T, text string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create("word/document.xml")
	fmt.Fprintf(w, `<?xml version="1.0"?><w:document xmlns:w="x"><w:body><w:p><w:r><w:t>%s</w:t></w:r></w:p></w:body></w:document>`, text)
	zw.Close()
	return b.Bytes()
}

func writeFile(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractPDF(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "ldo.pdf", minimalPDF([]string{"AMS1117 dropout voltage 1.1 V", "Thermal resistance junction to ambient"}))
	ex, err := Extract(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.Pages) != 2 || !strings.Contains(ex.Pages[0], "dropout") || !strings.Contains(ex.Pages[1], "Thermal") {
		t.Fatalf("pages = %q (extractor %s)", ex.Pages, ex.Extractor)
	}
}

func TestTokenize(t *testing.T) {
	got := strings.Join(Tokenize("The AMS1117-3.3 LDO: 输入电压 IPC-2221"), "|")
	want := "ams1117|3.3|ldo|输入|入电|电压|ipc|2221"
	if got != want {
		t.Fatalf("tokens = %s, want %s", got, want)
	}
}

func TestChunkPagesBoundaries(t *testing.T) {
	long := strings.Repeat("Sentence about decoupling capacitors. ", 100)
	ch := ChunkPages([]string{long, "short page"}, true)
	if len(ch) < 3 {
		t.Fatalf("want several chunks, got %d", len(ch))
	}
	last := ch[len(ch)-1]
	if last.Page != 2 || last.Text != "short page" {
		t.Fatalf("chunks must not span pages: %+v", last)
	}
	for _, c := range ch[:len(ch)-1] {
		if len([]rune(c.Text)) > chunkRunes {
			t.Fatalf("chunk too long: %d", len([]rune(c.Text)))
		}
	}
}

func TestLibraryAddSearchDedupeSummary(t *testing.T) {
	work := t.TempDir()
	src := t.TempDir()
	pdfPath := writeFile(t, src, "AMS1117 datasheet.pdf", minimalPDF([]string{"AMS1117 dropout voltage 1.1 V at 800 mA", "Package SOT-223 thermal pad"}))
	writeFile(t, src, "req.md", []byte("# 客户需求\n\n板子需要 5V 供电端子，降压到 3V3，四角 M3 固定孔。"))
	writeFile(t, src, "notes.html", []byte("<html><head><title>Ref design</title><script>var x='dropout';</script></head><body><p>USB-C CC resistors 5.1k</p></body></html>"))
	writeFile(t, src, "spec.docx", minimalDOCX(t, "Creepage distance 2.5 mm for mains"))
	writeFile(t, src, "board.step", []byte("ISO-10303-21;"))

	lib, err := Open(work, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := lib.Add([]string{src}, AddOptions{Kind: "datasheet", Tags: []string{"Power"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 5 {
		t.Fatalf("added %d, want 5", len(res))
	}
	cat, _ := lib.Load()
	status := map[string]string{}
	for _, d := range cat.Docs {
		status[filepath.Ext(d.Name)] = d.Status
		if !strings.HasPrefix(d.Path, "resources/datasheet/") {
			t.Fatalf("stored outside resources: %s", d.Path)
		}
	}
	if status[".pdf"] != "indexed" || status[".md"] != "indexed" || status[".html"] != "indexed" || status[".docx"] != "indexed" || status[".step"] != "stored-only" {
		t.Fatalf("statuses = %v", status)
	}
	// Sanitized name: spaces → dash.
	if _, err := os.Stat(filepath.Join(work, "resources", "datasheet", "AMS1117-datasheet.pdf")); err != nil {
		t.Fatal(err)
	}

	// Dedupe: same bytes under another name → alias, no new doc.
	dup := writeFile(t, t.TempDir(), "copy.pdf", mustRead(t, pdfPath))
	res, err = lib.Add([]string{dup}, AddOptions{Kind: "paper"})
	if err != nil || !res[0].Duplicate {
		t.Fatalf("duplicate not detected: %+v %v", res, err)
	}
	cat, _ = lib.Load()
	if len(cat.Docs) != 5 {
		t.Fatalf("dedupe failed: %d docs", len(cat.Docs))
	}

	r, err := lib.Search("dropout voltage", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hits) == 0 || !strings.HasSuffix(r.Hits[0].Path, ".pdf") || r.Hits[0].Page != 1 || !strings.HasPrefix(r.Hits[0].Cite, "kb:") {
		t.Fatalf("search hits = %+v", r.Hits)
	}
	// script text must not be indexed
	for _, h := range r.Hits {
		if strings.HasSuffix(h.Path, ".html") {
			t.Fatalf("script content leaked into index: %+v", h)
		}
	}
	r, _ = lib.Search("降压 3V3", SearchOptions{})
	if len(r.Hits) == 0 || !strings.HasSuffix(r.Hits[0].Path, ".md") {
		t.Fatalf("CJK search failed: %+v", r)
	}
	r, _ = lib.Search("creepage", SearchOptions{Kind: "paper"})
	if len(r.Hits) != 0 {
		t.Fatalf("kind filter ignored: %+v", r.Hits)
	}
	r, _ = lib.Search("creepage", SearchOptions{Tag: "power"})
	if len(r.Hits) != 1 {
		t.Fatalf("tag filter: %+v", r.Hits)
	}

	id := r.Hits[0].DocID
	if _, err := lib.SetSummary(id[:6], Summary{Text: "Insulation spec", By: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.SetTags(id, []string{"Safety"}, []string{"power"}); err != nil {
		t.Fatal(err)
	}
	cat, _ = lib.Load()
	d, _ := cat.Find(id)
	if d.Summary == nil || d.Summary.Text != "Insulation spec" || strings.Join(d.Tags, ",") != "safety" {
		t.Fatalf("summary/tags not saved: %+v", d)
	}
	chunks, err := lib.Chunks(id)
	if err != nil || len(chunks) == 0 {
		t.Fatalf("chunks: %v %v", chunks, err)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd": "passwd",
		"a b (c).pdf":      "a-b-c-.pdf",
		".hidden":          "hidden",
		"数据手册 v2.pdf":      "数据手册-v2.pdf",
		`C:\evil\x..y.pdf`: "x.y.pdf",
		"<script>.html":    "script.html",
		"":                 "file",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
