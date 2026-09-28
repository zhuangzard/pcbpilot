package app

// report_package.go — one self-contained deliverable per design-report
// version: reports/<name>/vN/{report.html, report.md, report.json,
// manifest.json, assets/, data/} and reports/<name>/pcbpilot-report-<name>-vN.zip.

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zhuangzard/pcbpilot/pkg/postsim"
)

// pkgFile is one file of the version package.
type pkgFile struct {
	Rel      string `json:"path"`
	Role     string `json:"role"`
	SHA256   string `json:"sha256"`
	Bytes    int    `json:"bytes"`
	Producer string `json:"producer"`
	Source   string `json:"source,omitempty"` // original path of an input
	data     []byte
}

// reportManifest is vN/manifest.json.
type reportManifest struct {
	SchemaVersion int       `json:"schemaVersion"`
	Generator     string    `json:"generator"`
	Project       string    `json:"project"`
	Version       string    `json:"version"`
	GeneratedAt   string    `json:"generatedAt"`
	Verdict       string    `json:"verdict"`
	Zip           string    `json:"zip"`
	Files         []pkgFile `json:"files"`
	Notes         []string  `json:"notes,omitempty"`
}

// dataName is the package path of an input kind.
var dataNames = map[string]string{
	"intent": "data/intent.json", "sim": "data/sim.json", "plan": "data/plan.json", "feedback": "data/feedback.json",
	"board": "data/board.json", "reload-board": "data/board.reload.json", "drc": "data/drc.json",
	"rules-check": "data/rules-check.json", "net-diff": "data/net-diff.json", "values": "data/values.json",
	"models": "data/power-models.json", "post": "data/post.json", "spice": "data/sim.cir",
}

var producers = map[string]string{
	"intent":       "pcbpilot intent derive",
	"sim":          "pcbpilot sim power",
	"plan":         "pcbpilot pcb auto run",
	"feedback":     "pcbpilot pcb auto run / sim post-layout --feedback",
	"board":        "pcbpilot pcb dump --include-copper",
	"reload-board": "pcbpilot pcb dump --include-copper (after save → reload)",
	"drc":          "pcbpilot pcb drc",
	"check":        "pcbpilot pcb check",
	"rules-check":  "pcbpilot pcb rules check",
	"net-diff":     "pad-net-diff.py --json",
	"values":       "pcbpilot sch list",
	"models":       "skill references/power-models.json",
	"post":         "pcbpilot sim post-layout",
	"spice":        "pcbpilot sim power --spice",
	"elmer":        "pcbpilot sim post-layout --elmer-dir",
	"heat":         "pcbpilot sim post-layout --svg-dir",
	"image":        "report design --image (supplied file)",
	"preview":      "pcbpilot pcb auto run (preview.svg)",
	"report":       "pcbpilot report design",
}

func newPkgFile(rel, role, producer, source string, data []byte) pkgFile {
	h := sha256.Sum256(data)
	return pkgFile{Rel: rel, Role: role, SHA256: hex.EncodeToString(h[:]), Bytes: len(data), Producer: producer, Source: source, data: data}
}

// dataFileName picks data/<name> for an input kind (check keeps its extension).
func dataFileName(kind, src string) string {
	if n, ok := dataNames[kind]; ok {
		return n
	}
	ext := filepath.Ext(src)
	if ext == "" {
		ext = ".txt"
	}
	return "data/" + kind + ext
}

// maxElmerDeck is the largest Elmer deck copied into a package.
const maxElmerDeck = 8 << 20

// resolveBeside finds a path recorded in a document relative to the cwd of
// the run that wrote it, or beside the document.
func resolveBeside(p, doc string) string {
	if p == "" {
		return ""
	}
	cands := []string{p, filepath.Join(filepath.Dir(doc), p), filepath.Join(filepath.Dir(doc), filepath.Base(p))}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return ""
}

// postHeatImages lists the heat maps a post.json points to.
func postHeatImages(post *postsim.Result, postPath string) (dir string, specs [][3]string) {
	if post == nil || len(post.Maps) == 0 {
		return "", nil
	}
	dir = resolveBeside(post.MapsDir, postPath)
	if dir == "" {
		return "", nil
	}
	for _, m := range post.Maps {
		kind := "温度"
		if m.Kind == "current-density" {
			kind = "电流密度"
		}
		label := sprintfApp("%s %s（%s–%s %s）", m.Layer, kind, trimNum(m.Min), trimNum(m.Max), m.Unit)
		specs = append(specs, [3]string{"heat", label, filepath.Join(dir, m.File)})
	}
	return dir, specs
}

func sprintfApp(f string, a ...any) string { return fmt.Sprintf(f, a...) }

func trimNum(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}

// elmerFiles collects an exported Elmer deck (post.json elmer.deck).
func elmerFiles(post *postsim.Result, postPath string) ([]pkgFile, string) {
	if post == nil || post.Elmer == nil || post.Elmer.Deck == "" {
		return nil, ""
	}
	dir := resolveBeside(post.Elmer.Deck, postPath)
	if dir == "" {
		return nil, "Elmer deck " + post.Elmer.Deck + " not found: not packaged"
	}
	var out []pkgFile
	total := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		total += len(b)
		out = append(out, newPkgFile("data/elmer/"+filepath.ToSlash(rel), "elmer", producers["elmer"], p, b))
		return nil
	})
	if err != nil {
		return nil, "Elmer deck unreadable: " + err.Error()
	}
	if total > maxElmerDeck {
		return nil, sprintfApp("Elmer deck %s is %.1f MB (> %d MB): not packaged — regenerate with sim post-layout --elmer-dir", dir, float64(total)/(1<<20), maxElmerDeck>>20)
	}
	return out, ""
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// zipName is pcbpilot-report-<project>-vN.zip with a file-system safe name.
func zipName(project, label string) string {
	p := strings.Trim(unsafeName.ReplaceAllString(project, "-"), "-")
	if p == "" {
		p = "project"
	}
	return "pcbpilot-report-" + p + "-" + label + ".zip"
}

// writeReportPackage writes the files under dir, manifest.json, and the zip
// beside dir. Entries are sorted and time-stamped with when, so identical
// inputs give an identical zip.
func writeReportPackage(dir, zipPath string, m *reportManifest, files []pkgFile, when time.Time) error {
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.Rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, f.data, 0o644); err != nil {
			return err
		}
	}
	m.Files = files
	mb, _ := json.MarshalIndent(m, "", "  ")
	mb = append(mb, '\n')
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0o644); err != nil {
		return err
	}
	all := append(append([]pkgFile(nil), files...), pkgFile{Rel: "manifest.json", data: mb})
	sort.Slice(all, func(i, j int) bool { return all[i].Rel < all[j].Rel })
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	root := strings.TrimSuffix(filepath.Base(zipPath), ".zip")
	for _, f := range all {
		h := &zip.FileHeader{Name: path.Join(root, f.Rel), Method: zip.Deflate, Modified: when.UTC()}
		h.SetMode(0o644)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if _, err := w.Write(f.data); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(zipPath, buf.Bytes(), 0o644)
}
