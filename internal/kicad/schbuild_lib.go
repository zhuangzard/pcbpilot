package kicad

// schbuild_lib.go — library and project helpers for `pcbpilot kicad
// sch-build` (one deterministic call: spec → complete KiCad schematic
// project). Symbols come from (in order) a project/stock .kicad_sym, the
// LCSC import (`kicad lcsc --import`, cached per part under
// ~/.pcbpilot/cache/kicad-lcsc so a repeat build is offline and instant), or
// a generated box symbol from the spec's pin list. ERC is `kicad-cli sch
// erc --format json`.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
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
