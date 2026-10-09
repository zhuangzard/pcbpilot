package kicad

// schbuild_lib.go — library and project helpers for `pcbpilot kicad
// sch-build` (one deterministic call: spec → complete KiCad schematic
// project). Symbols come from (in order) a project/stock .kicad_sym, the
// LCSC import (`kicad lcsc --import`, cached per part under
// ~/.pcbpilot/cache/kicad-lcsc so a repeat build is offline and instant), or
// a generated box symbol from the spec's pin list. ERC is `kicad-cli sch
// erc --format json`.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// EnvCacheDir overrides the pcbpilot cache root (default ~/.pcbpilot/cache).
const EnvCacheDir = "PCBPILOT_CACHE_DIR"

// EnvStockSymbols overrides KiCad's stock symbol library directory.
const EnvStockSymbols = "PCBPILOT_KICAD_SYMBOLS"

// CacheRoot is the pcbpilot cache directory.
func CacheRoot() string {
	if v := os.Getenv(EnvCacheDir); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "pcbpilot-cache")
	}
	return filepath.Join(home, ".pcbpilot", "cache")
}

// StockSymbolDir is KiCad's bundled symbol library directory ("" if absent).
func StockSymbolDir() string {
	if v := os.Getenv(EnvStockSymbols); v != "" {
		return v
	}
	app := os.Getenv(EnvApp)
	if app == "" {
		app = DefaultApp()
	}
	var cands []string
	switch runtime.GOOS {
	case "darwin":
		cands = []string{filepath.Join(app, "Contents/SharedSupport/symbols")}
	case "windows":
		cands = []string{filepath.Join(app, "share", "kicad", "symbols")}
	default:
		cands = []string{"/usr/share/kicad/symbols", "/usr/local/share/kicad/symbols"}
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return ""
}

// CachedPart is one LCSC part converted to KiCad and kept in the cache.
type CachedPart struct {
	LCSC          string `json:"lcsc"`
	Title         string `json:"title,omitempty"`
	JLCPartClass  string `json:"jlcPartClass,omitempty"`
	SymbolName    string `json:"symbolName"`    // bare name in the cached lcsc.kicad_sym
	FootprintName string `json:"footprintName"` // bare name in the cached lcsc.pretty
	Dir           string `json:"-"`             // cache directory of this part
	FromCache     bool   `json:"-"`
}

// SymbolText is the cached `(symbol "Name" …)` text.
func (c *CachedPart) SymbolText() (string, error) {
	return LibSymbolFromFile(filepath.Join(c.Dir, "lcsc.kicad_sym"), c.SymbolName)
}

// FootprintFile is the cached .kicad_mod path.
func (c *CachedPart) FootprintFile() string {
	return filepath.Join(c.Dir, "lcsc.pretty", c.FootprintName+".kicad_mod")
}

// LCSCCacheDir is the per-part cache directory.
func LCSCCacheDir(lcsc string) string { return filepath.Join(CacheRoot(), "kicad-lcsc", lcsc) }

// CachedImportLCSC returns the cached conversion of lcsc, importing it
// (network) on a miss. Safe to call concurrently for different parts: each
// part converts into its own temporary directory that is renamed into place.
func CachedImportLCSC(tools FabTools, lcsc string) (*CachedPart, error) {
	dir := LCSCCacheDir(lcsc)
	if b, err := os.ReadFile(filepath.Join(dir, "part.json")); err == nil {
		var c CachedPart
		if json.Unmarshal(b, &c) == nil && c.SymbolName != "" {
			c.Dir, c.FromCache = dir, true
			return &c, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), lcsc+".tmp-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	r, err := ImportLCSC(tools, lcsc, tmp, "lcsc")
	if err != nil {
		return nil, err
	}
	c := CachedPart{LCSC: lcsc, Title: r.Title, JLCPartClass: r.JLCPartClass,
		SymbolName: strings.TrimPrefix(r.Symbol, "lcsc:"), FootprintName: strings.TrimPrefix(r.Footprint, "lcsc:")}
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, "part.json"), append(b, '\n'), 0o644); err != nil {
		return nil, err
	}
	_ = os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		if _, serr := os.Stat(filepath.Join(dir, "part.json")); serr != nil { // a concurrent winner is fine
			return nil, err
		}
	}
	c.Dir = dir
	return &c, nil
}

// GenPin is one pin of a generated symbol.
type GenPin struct {
	Number, Name, Type string
}

var kicadPinTypes = map[string]bool{"input": true, "output": true, "bidirectional": true, "tri_state": true,
	"passive": true, "free": true, "unspecified": true, "power_in": true, "power_out": true,
	"open_collector": true, "open_emitter": true, "no_connect": true}

// ValidPinType reports whether t is a KiCad electrical pin type.
func ValidPinType(t string) bool { return kicadPinTypes[t] }

// GenericSymbolText generates a box symbol for pins: two pins → a small
// horizontal two-terminal body (pin 1 left); more → a rectangle with the
// first half of the pins on the left and the rest on the right, 2.54 mm
// pitch. Pin types default to passive.
func GenericSymbolText(name, refPrefix string, pins []GenPin) string {
	if refPrefix == "" {
		refPrefix = "U"
	}
	pinText := func(p GenPin, x, y, ang float64) string {
		t := p.Type
		if !ValidPinType(t) {
			t = "passive"
		}
		nm := p.Name
		if nm == "" {
			nm = "~"
		}
		return fmt.Sprintf("(pin %s line (at %s %s %s) (length 2.54) (name %s (effects (font (size 1.27 1.27)))) (number %s (effects (font (size 1.27 1.27)))))",
			t, F(x), F(y), F(ang), Q(nm), Q(p.Number))
	}
	var body, pinsTxt []string
	var top, bottom float64
	hidePinNames := false
	if len(pins) == 2 {
		hidePinNames = true
		top, bottom = 1.016, -1.016
		body = append(body, "(rectangle (start -2.54 1.016) (end 2.54 -1.016) (stroke (width 0.254) (type default)) (fill (type none)))")
		pinsTxt = append(pinsTxt, pinText(pins[0], -5.08, 0, 0), pinText(pins[1], 5.08, 0, 180))
	} else {
		nl := (len(pins) + 1) / 2
		left, right := pins[:nl], pins[nl:]
		maxName := 0
		for _, p := range pins {
			maxName = max(maxName, len([]rune(p.Name)))
		}
		half := math.Ceil((float64(maxName)*1.27+2.54)/2.54) * 2.54
		if half < 5.08 {
			half = 5.08
		}
		rows := max(len(left), len(right))
		yTop := math.Floor(float64(rows-1)/2) * 2.54
		top, bottom = yTop+2.54, yTop-float64(rows)*2.54
		body = append(body, fmt.Sprintf("(rectangle (start %s %s) (end %s %s) (stroke (width 0.254) (type default)) (fill (type background)))",
			F(-half), F(top), F(half), F(bottom)))
		for i, p := range left {
			pinsTxt = append(pinsTxt, pinText(p, -half-2.54, yTop-float64(i)*2.54, 0))
		}
		for i, p := range right {
			pinsTxt = append(pinsTxt, pinText(p, half+2.54, yTop-float64(i)*2.54, 180))
		}
	}
	pn := ""
	if hidePinNames {
		pn = "\n\t\t(pin_names (offset 0) (hide yes))"
	}
	return fmt.Sprintf(`(symbol %s%s
		(exclude_from_sim no)
		(in_bom yes)
		(on_board yes)
		(property "Reference" %s (at 0 %s 0) (effects (font (size 1.27 1.27))))
		(property "Value" %s (at 0 %s 0) (effects (font (size 1.27 1.27))))
		(property "Footprint" "" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "Datasheet" "" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "Description" "generated by pcbpilot kicad sch-build" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(symbol %s %s)
		(symbol %s %s)
	)`, Q(name), pn, Q(refPrefix), F(top+1.27), Q(name), F(bottom-1.27),
		Q(name+"_0_1"), strings.Join(body, " "), Q(name+"_1_1"), strings.Join(pinsTxt, "\n\t\t\t"))
}

// PwrFlagText is KiCad's PWR_FLAG (a power_out pin that marks a net driven).
func PwrFlagText() string {
	return `(symbol "PWR_FLAG"
		(power)
		(pin_numbers (hide yes))
		(pin_names (offset 0) (hide yes))
		(exclude_from_sim no)
		(in_bom yes)
		(on_board yes)
		(property "Reference" "#FLG" (at 0 1.905 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "Value" "PWR_FLAG" (at 0 3.81 0) (effects (font (size 1.27 1.27))))
		(property "Footprint" "" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "Datasheet" "~" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "Description" "Special symbol for telling ERC where power comes from" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(symbol "PWR_FLAG_0_0" (pin power_out line (at 0 0 90) (length 0) (name "~" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27))))))
		(symbol "PWR_FLAG_0_1" (polyline (pts (xy 0 0) (xy 0 1.27) (xy -1.016 1.905) (xy 0 2.54) (xy 1.016 1.905) (xy 0 1.27)) (stroke (width 0) (type default)) (fill (type none))))
	)`
}

// AddPwrFlag places a PWR_FLAG with its pin at p and returns its reference.
func (e *SchEditor) AddPwrFlag(p Pt, n int) (string, error) {
	const id = "pcbpilot_power:PWR_FLAG"
	if err := e.AddLibSymbol(id, PwrFlagText()); err != nil {
		return "", err
	}
	ref := fmt.Sprintf("#FLG%04d", n)
	va := Pt{p.X, p.Y - 3.81}
	_, err := e.PlaceSymbol(SymbolInstance{LibID: id, Ref: ref, At: p, Value: "PWR_FLAG",
		Fields: []Field{{Name: "Value", Value: "PWR_FLAG", At: &va}}})
	return ref, err
}

// LibSymbolBox is a library symbol's extent (symbol frame, y up), pins
// included, and whether it is a power symbol.
func LibSymbolBox(sym string) (Box, error) {
	n, err := parseSx(strings.TrimSpace(sym))
	if err != nil {
		return Box{}, err
	}
	return libBox(n), nil
}

// LibSymbolPins lists the pins of a `(symbol …)` text (unit 0 = common).
func LibSymbolPins(sym string) ([]LibPin, error) {
	e := &SchEditor{libs: map[string]*sexp{}}
	n, err := parseSx(strings.TrimSpace(sym))
	if err != nil {
		return nil, err
	}
	e.libs["x"] = n
	return e.LibPins("x")
}

// LibSymbolProperty returns the value of a property of a `(symbol …)` text.
func LibSymbolProperty(sym, name string) string {
	n, err := parseSx(strings.TrimSpace(sym))
	if err != nil {
		return ""
	}
	return propVal(n, name)
}

// SymbolExtends reports the parent name of a derived library symbol.
func SymbolExtends(sym string) string {
	n, err := parseSx(strings.TrimSpace(sym))
	if err != nil {
		return ""
	}
	if x := n.child("extends"); x != nil && len(x.list) > 1 {
		return x.list[1].atom
	}
	return ""
}

// LibSymbolNames lists the top-level symbol names of a .kicad_sym file.
func LibSymbolNames(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r, err := parseSx(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []string
	for _, s := range r.list {
		if s.head() == "symbol" && len(s.list) > 1 {
			out = append(out, s.list[1].atom)
		}
	}
	return out, nil
}

// SymbolLibText renders a .kicad_sym library from `(symbol "bare" …)` texts.
func SymbolLibText(symbols map[string]string) string {
	names := make([]string, 0, len(symbols))
	for n := range symbols {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("(kicad_symbol_lib\n\t(version 20251024)\n\t(generator \"pcbpilot\")\n")
	for _, n := range names {
		b.WriteString("\t" + symbols[n] + "\n")
	}
	b.WriteString(")\n")
	return b.String()
}

// LibTableText renders a sym-lib-table / fp-lib-table with project libs
// (name → ${KIPRJMOD}-relative uri).
func LibTableText(kind string, libs map[string]string) string {
	names := make([]string, 0, len(libs))
	for n := range libs {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "(%s\n\t(version 7)\n", kind)
	for _, n := range names {
		fmt.Fprintf(&b, "\t(lib (name %s) (type \"KiCad\") (uri %s) (options \"\") (descr \"pcbpilot sch-build\"))\n", Q(n), Q("${KIPRJMOD}/"+libs[n]))
	}
	b.WriteString(")\n")
	return b.String()
}

// ── ERC ─────────────────────────────────────────────────────────────────────

// ERCViolation is one kicad-cli ERC item.
type ERCViolation struct {
	Type        string   `json:"type"`
	Severity    string   `json:"severity"`
	Description string   `json:"description"`
	Sheet       string   `json:"sheet,omitempty"`
	Items       []string `json:"items,omitempty"`
}

// ERCReport is a parsed `kicad-cli sch erc --format json` report.
type ERCReport struct {
	Errors     int            `json:"errors"`
	Warnings   int            `json:"warnings"`
	ByType     map[string]int `json:"byType"`
	Violations []ERCViolation `json:"violations"`
}

// ParseERC reads a kicad-cli ERC JSON report.
func ParseERC(data []byte) (*ERCReport, error) {
	var raw struct {
		Sheets []struct {
			Path       string `json:"path"`
			Violations []struct {
				Type        string `json:"type"`
				Severity    string `json:"severity"`
				Description string `json:"description"`
				Items       []struct {
					Description string `json:"description"`
				} `json:"items"`
			} `json:"violations"`
		} `json:"sheets"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse ERC report: %w", err)
	}
	r := &ERCReport{ByType: map[string]int{}}
	for _, s := range raw.Sheets {
		for _, v := range s.Violations {
			ev := ERCViolation{Type: v.Type, Severity: v.Severity, Description: v.Description, Sheet: s.Path}
			for _, it := range v.Items {
				ev.Items = append(ev.Items, it.Description)
			}
			r.Violations = append(r.Violations, ev)
			r.ByType[v.Type]++
			if v.Severity == "error" {
				r.Errors++
			} else {
				r.Warnings++
			}
		}
	}
	return r, nil
}

// RunERC runs kicad-cli ERC (all severities) on the root sheet; raw is the
// written report path (kept when non-empty).
func RunERC(root, raw string) (*ERCReport, error) {
	cli, err := KicadCLI()
	if err != nil {
		return nil, err
	}
	out := raw
	if out == "" {
		dir, err := os.MkdirTemp("", "pcbpilot-erc-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		out = filepath.Join(dir, "erc.json")
	}
	var buf bytes.Buffer
	cmd := exec.Command(cli, "sch", "erc", "--format", "json", "--severity-all", "--units", "mm", "-o", out, root)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		if _, serr := os.Stat(out); serr != nil {
			return nil, fmt.Errorf("kicad-cli sch erc: %v: %s", err, strings.TrimSpace(buf.String()))
		}
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	return ParseERC(data)
}

// ── title block ─────────────────────────────────────────────────────────────

// TitleBlock is the sheet title block.
type TitleBlock struct {
	Title    string   `json:"title,omitempty"`
	Date     string   `json:"date,omitempty"`
	Rev      string   `json:"rev,omitempty"`
	Company  string   `json:"company,omitempty"`
	Comments []string `json:"comments,omitempty"`
}

// Text is the `(title_block …)` S-expression ("" when empty).
func (t TitleBlock) Text() string {
	var parts []string
	if t.Title != "" {
		parts = append(parts, "(title "+Q(t.Title)+")")
	}
	if t.Date != "" {
		parts = append(parts, "(date "+Q(t.Date)+")")
	}
	if t.Rev != "" {
		parts = append(parts, "(rev "+Q(t.Rev)+")")
	}
	if t.Company != "" {
		parts = append(parts, "(company "+Q(t.Company)+")")
	}
	for i, c := range t.Comments {
		if i >= 9 {
			break
		}
		if c != "" {
			parts = append(parts, fmt.Sprintf("(comment %d %s)", i+1, Q(c)))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "(title_block\n\t\t" + strings.Join(parts, "\n\t\t") + "\n\t)"
}

// NewSheetText is NewSchematicText with a title block.
func NewSheetText(paper, uuid string, root bool, tb TitleBlock) string {
	s := NewSchematicText(paper, uuid, root)
	if t := tb.Text(); t != "" {
		s = strings.Replace(s, "(paper "+Q(paper)+")", "(paper "+Q(paper)+")\n\t"+t, 1)
	}
	return s
}

// PowerRefNext is the number the next power symbol reference (#PWRnnnn)
// gets; SetPowerRefNext continues a numbering across sheets.
func (e *SchEditor) PowerRefNext() int { return e.pwrNext }

// SetPowerRefNext sets the next power symbol number (n ≥ 1).
func (e *SchEditor) SetPowerRefNext(n int) {
	if n > e.pwrNext {
		e.pwrNext = n
	}
}

var libFileCache = struct {
	sync.Mutex
	m map[string]map[string]string
}{m: map[string]map[string]string{}}

// LibSymbolCached is LibSymbolFromFile with the library parsed once per
// process (KiCad's stock libraries are megabytes).
func LibSymbolCached(path, name string) (string, error) {
	libFileCache.Lock()
	syms, ok := libFileCache.m[path]
	libFileCache.Unlock()
	if !ok {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		src := string(b)
		r, err := parseSx(src)
		if err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		syms = map[string]string{}
		for _, s := range r.list {
			if s.head() == "symbol" && len(s.list) > 1 {
				syms[s.list[1].atom] = src[s.beg:s.end]
			}
		}
		libFileCache.Lock()
		libFileCache.m[path] = syms
		libFileCache.Unlock()
	}
	t, ok := syms[name]
	if !ok {
		return "", fmt.Errorf("symbol %q not in %s", name, path)
	}
	return t, nil
}

// ForgetLibFile drops a library from the parse cache (after it changed).
func ForgetLibFile(path string) {
	libFileCache.Lock()
	delete(libFileCache.m, path)
	libFileCache.Unlock()
}
