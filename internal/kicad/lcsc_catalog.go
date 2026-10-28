package kicad

// lcsc_catalog.go — the JLC/LCSC catalog as `kicad lcsc --health` and
// `--alternatives` see it: the record shape (written by parts-select.py's
// normalize), the Catalog interface the command talks to (production: the
// skill's parts-select.py; tests: a fake), SI value parsing for key-parameter
// comparison and the footprint ↔ catalog package matcher.
//
// Everything here is conservative in one direction: a value or package that
// cannot be read is "unknown", never a match and never a mismatch.

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// PriceTier is one quantity break of the catalog price list.
type PriceTier struct {
	From  int64   `json:"from"`
	To    int64   `json:"to"` // -1 or 0: open ended
	Price float64 `json:"price"`
}

// CatalogPart is one catalog record. Pointer fields are nil when the catalog
// did not state them (a missing stock figure is not zero stock).
type CatalogPart struct {
	LCSC             string            `json:"lcsc"`
	MPN              string            `json:"mpn"`
	Brand            string            `json:"brand,omitempty"`
	Package          string            `json:"package"`
	Category         string            `json:"category,omitempty"`
	Group            string            `json:"group,omitempty"`
	Base             *bool             `json:"base"`
	Preferred        bool              `json:"preferred,omitempty"`
	Stock            *int64            `json:"stock"`
	Buyable          *bool             `json:"buyable"`
	NoBuyReason      string            `json:"noBuyReason,omitempty"`
	MergedCode       string            `json:"mergedCode,omitempty"`
	AlternativesCode string            `json:"alternativesCode,omitempty"`
	Loss             *int64            `json:"loss,omitempty"`
	LeastPatch       *int64            `json:"leastPatch,omitempty"`
	Prices           []PriceTier       `json:"prices,omitempty"`
	Attrs            map[string]string `json:"attrs,omitempty"`
	Desc             string            `json:"desc,omitempty"`
	URL              string            `json:"url,omitempty"`
}

// IsBase: JLC basic library (no per-part-type setup fee). Unknown → false, ok=false.
func (p CatalogPart) IsBase() (base, known bool) {
	if p.Base == nil {
		return false, false
	}
	return *p.Base, true
}

// UnitPrice is the unit price at qty (first tier when none covers it); ok=false without prices.
func (p CatalogPart) UnitPrice(qty int64) (float64, bool) {
	if len(p.Prices) == 0 {
		return 0, false
	}
	for _, t := range p.Prices {
		hi := t.To
		if hi <= 0 {
			hi = math.MaxInt64
		}
		if qty >= t.From && qty <= hi {
			return t.Price, true
		}
	}
	return p.Prices[0].Price, true
}

// CatalogLookup is the answer for one LCSC number: Part set (found), or
// neither Part nor Err (the catalog has no such number), or Err (the question
// could not be asked — not the same as "no such part").
type CatalogLookup struct {
	Part *CatalogPart
	Err  string
}

// Catalog is the live JLC catalog.
type Catalog interface {
	// Lookup resolves LCSC numbers by exact match.
	Lookup(codes []string) (map[string]CatalogLookup, error)
	// Search is a keyword search (value + package, or an MPN).
	Search(query string, qty int) ([]CatalogPart, error)
}

// ── SI values ──────────────────────────────────────────────────────────────

var siRe = regexp.MustCompile(`^\s*[±+]?\s*([0-9]*\.?[0-9]+)\s*([pnuµμmkKMG]?)\s*(Ω|ohms?|F|V|W|H|A|%)?\s*$`)

var siScale = map[string]float64{"": 1, "p": 1e-12, "n": 1e-9, "u": 1e-6, "µ": 1e-6, "μ": 1e-6, "m": 1e-3, "k": 1e3, "K": 1e3, "M": 1e6, "G": 1e9}

// Quantity is a parsed catalog value: "10kΩ" → {1e4, "Ω"}.
type Quantity struct {
	V    float64
	Unit string
}

// ParseQuantity reads a single-number value with an optional SI prefix and
// unit. Ranges, asymmetric tolerances ("+80%,-20%") and text are not parsed.
func ParseQuantity(s string) (Quantity, bool) {
	s = strings.TrimSpace(s)
	m := siRe.FindStringSubmatch(s)
	if m == nil {
		return Quantity{}, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return Quantity{}, false
	}
	unit := m[3]
	if unit == "ohm" || unit == "ohms" {
		unit = "Ω"
	}
	// A bare "m"/"M" with no unit is a prefix without a base: not a value we trust.
	if unit == "" && m[2] != "" {
		return Quantity{}, false
	}
	return Quantity{V: n * siScale[m[2]], Unit: unit}, true
}

// sameQty: equal within 0.1 % (decimal text round-off), same unit.
func sameQty(a, b Quantity) bool {
	if a.Unit != b.Unit {
		return false
	}
	if a.V == b.V {
		return true
	}
	return math.Abs(a.V-b.V) <= 1e-3*math.Max(math.Abs(a.V), math.Abs(b.V))
}

// attr returns an attribute by case-insensitive name.
func (p CatalogPart) attr(name string) (string, bool) {
	for k, v := range p.Attrs {
		if strings.EqualFold(strings.TrimSpace(k), name) && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// ── package matching ───────────────────────────────────────────────────────

// Compat is the outcome of comparing a board footprint with a catalog package.
type Compat string

const (
	CompatMatch    Compat = "match"
	CompatMismatch Compat = "mismatch"
	CompatUnknown  Compat = "unknown"
)

// pkgID is a recognised package: a chip size (imperial code) or a named
// family with a pin count (0 = not stated).
type pkgID struct {
	chip   string
	family string
	pins   int
}

func (p pkgID) String() string {
	switch {
	case p.chip != "":
		return "chip " + p.chip
	case p.pins > 0:
		return p.family + "-" + strconv.Itoa(p.pins)
	}
	return p.family
}

var chipImperial = map[string]bool{"01005": true, "0201": true, "0402": true, "0603": true, "0805": true, "1206": true, "1210": true, "1812": true, "2010": true, "2512": true}

// KiCad names chips by imperial size and "<metric>Metric"; this maps the metric code back.
var chipMetricToImperial = map[string]string{"0402": "01005", "0603": "0201", "1005": "0402", "1608": "0603", "2012": "0805", "3216": "1206", "3225": "1210", "4532": "1812", "5025": "2010", "6332": "2512"}

var (
	fpCodeRe   = regexp.MustCompile(`(?i)^(\d{4,5})(Metric)?$`)
	catChipRe  = regexp.MustCompile(`^[RCLD]?(\d{4,5})$`)
	namedPinRe = regexp.MustCompile(`(?i)(SOIC|SOP|SSOP|TSSOP|MSOP|VSSOP|QFN|DFN|LQFP|TQFP|QFP|DIP|TSOP)[-_]?(\d+)`)
	sotRe      = regexp.MustCompile(`(?i)SOT[-_]?(\d{2,3})(?:[-_](\d+))?`)
	plainRe    = regexp.MustCompile(`(?i)(SOD[-_]?\d+|TO[-_]?(?:92|220|252|263)|SMA|SMB|SMC)`)
)

// wellKnown families: a mismatch is only called when both sides are in here.
func isWellKnown(p pkgID) bool { return p.chip != "" || p.family != "" }

// parseNamed extracts a family+pins from free text (footprint name or catalog package).
func parseNamed(s string) (pkgID, bool) {
	if m := sotRe.FindStringSubmatch(s); m != nil {
		fam := "SOT-" + m[1]
		pins := 0
		if m[2] != "" {
			pins, _ = strconv.Atoi(m[2])
		}
		// SOT-23 without a suffix is the 3-pin part; SOT-25/26 are SOT-23-5/6.
		switch {
		case fam == "SOT-23" && pins == 0:
			pins = 3
		case fam == "SOT-25":
			fam, pins = "SOT-23", 5
		case fam == "SOT-26":
			fam, pins = "SOT-23", 6
		}
		return pkgID{family: fam, pins: pins}, true
	}
	if m := namedPinRe.FindStringSubmatch(s); m != nil {
		fam := strings.ToUpper(m[1])
		if fam == "SOIC" { // JLC lists the same 150 mil package as SOP-n
			fam = "SOP"
		}
		n, _ := strconv.Atoi(m[2])
		return pkgID{family: fam, pins: n}, true
	}
	if m := plainRe.FindStringSubmatch(s); m != nil {
		return pkgID{family: strings.ToUpper(strings.ReplaceAll(m[1], "_", "-"))}, true
	}
	return pkgID{}, false
}

func footprintPackage(libID string) (pkgID, bool) {
	name := footprintName(libID)
	// Named families first: "SOT-23-5" holds no chip code, "R_0402_1005Metric" no family.
	if p, ok := parseNamed(name); ok {
		return p, true
	}
	imp, metric := "", ""
	for _, tok := range strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' }) {
		m := fpCodeRe.FindStringSubmatch(tok)
		if m == nil {
			continue
		}
		switch {
		case m[2] == "" && chipImperial[m[1]] && imp == "":
			imp = m[1]
		case m[2] != "" && metric == "":
			metric = m[1]
		}
	}
	if imp == "" && metric != "" {
		imp = chipMetricToImperial[metric]
	}
	if imp != "" {
		return pkgID{chip: imp}, true
	}
	return pkgID{}, false
}

func catalogPackage(spec string) (pkgID, bool) {
	spec = strings.TrimSpace(spec)
	if m := catChipRe.FindStringSubmatch(spec); m != nil && chipImperial[m[1]] {
		return pkgID{chip: m[1]}, true
	}
	return parseNamed(spec)
}

// PackageCompat compares a board footprint ("Lib:Name") with the catalog
// package string. Chip footprints compare by imperial size, named families by
// family and pin count; anything else (connectors, modules, odd names) is
// unknown, never a pass.
func PackageCompat(footprint, catalogPkg string) (Compat, string) {
	f, fok := footprintPackage(footprint)
	c, cok := catalogPackage(catalogPkg)
	switch {
	case !fok && !cok:
		return CompatUnknown, "neither footprint " + footprintName(footprint) + " nor catalog package " + quoteOrEmpty(catalogPkg) + " is recognised"
	case !fok:
		return CompatUnknown, "footprint " + footprintName(footprint) + " is not recognised (catalog package " + quoteOrEmpty(catalogPkg) + ")"
	case !cok:
		return CompatUnknown, "catalog package " + quoteOrEmpty(catalogPkg) + " is not recognised (footprint " + footprintName(footprint) + ")"
	}
	if f == c {
		return CompatMatch, ""
	}
	// Same family, pin count stated on one side only: cannot tell.
	if f.family != "" && f.family == c.family && (f.pins == 0 || c.pins == 0) {
		return CompatUnknown, "pin count not stated on both sides (footprint " + footprintName(footprint) + ", catalog " + quoteOrEmpty(catalogPkg) + ")"
	}
	if isWellKnown(f) && isWellKnown(c) {
		return CompatMismatch, "footprint " + footprintName(footprint) + " is " + f.String() + ", catalog package " + quoteOrEmpty(catalogPkg) + " is " + c.String()
	}
	return CompatUnknown, "cannot compare " + f.String() + " with " + c.String()
}

func quoteOrEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(empty)"
	}
	return strconv.Quote(s)
}

// ── MPN ────────────────────────────────────────────────────────────────────

// MPNFieldNames are the footprint/symbol fields read as the manufacturer part number.
var MPNFieldNames = []string{"MPN", "Manufacturer_Part_Number", "Manufacturer Part Number", "Mfr. Part #", "MFR.Part #", "Manufacturer_PN", "Manufacturer Part"}

// LookupMPN is LookupLCSC for MPN fields.
func LookupMPN(fields map[string]string) string {
	for _, want := range MPNFieldNames {
		for k, v := range fields {
			if strings.EqualFold(strings.TrimSpace(k), want) && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// NormMPN is the comparison key of an MPN: upper case, letters and digits only.
func NormMPN(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
