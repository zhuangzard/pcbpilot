package app

// cmd_kicad_sch_build.go — `pcbpilot kicad sch-build`: one deterministic call
// from a design spec (parts, nets, rails, blocks, pages, title block) to a
// complete, gate-checked KiCad schematic project. The AI decides (writes the
// spec); the tool draws: symbols from LCSC (cached), zone layout with the
// offline layout planner, every pin connected in one batch, title block,
// page fit, project + library tables, then hard gates (KiCad netlist ==
// spec, ERC) and connectivity.json / intent.json for the PCB flow.
//
// The step-by-step S0–S6 path drives ~80 `sch` subcommands, one LLM turn
// each; this replaces it for KiCad (docs/kicad/sch-build.md).

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/blocks"
	"github.com/zhuangzard/pcbpilot/internal/connectivity"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// ── spec ────────────────────────────────────────────────────────────────────

const sbSchema = "pcbpilot.kicad.sch-build/1"

// sbSpec is the only thing the AI writes.
type sbSpec struct {
	Schema    string           `json:"schema,omitempty"`
	Name      string           `json:"name,omitempty"`
	Title     sbTitle          `json:"title,omitempty"`
	Pages     []sbPage         `json:"pages,omitempty"`
	Zones     []sbZone         `json:"zones,omitempty"`
	Parts     []sbPart         `json:"parts"`
	Blocks    []sbBlock        `json:"blocks,omitempty"`
	Nets      []sbNet          `json:"nets"`
	Rails     []sbRail         `json:"rails,omitempty"`
	NoConnect []string         `json:"noConnect,omitempty"`
	// UnusedPins: "nc" (default) puts a no-connect flag on every pin no net
	// names; "open" leaves them (ERC then reports pin_not_connected).
	UnusedPins string   `json:"unusedPins,omitempty"`
	LibDirs    []string `json:"libDirs,omitempty"`
	// Intent is passed to `intent derive --spec` (standard, layers, rules …);
	// rails/net voltage+current from this spec are merged into its rails.
	Intent json.RawMessage `json:"intent,omitempty"`
}

// sbTitle is the title block (written with kicad's SetTitleBlock).
type sbTitle struct {
	Title    string   `json:"title,omitempty"`
	Date     string   `json:"date,omitempty"`
	Rev      string   `json:"rev,omitempty"`
	Company  string   `json:"company,omitempty"`
	Comments []string `json:"comments,omitempty"` // comment 1…9
}

func (t sbTitle) kicad() kicad.TitleBlock {
	tb := kicad.TitleBlock{Title: t.Title, Date: t.Date, Rev: t.Rev, Company: t.Company, Comments: map[int]string{}}
	for i, c := range t.Comments {
		if i < 9 && c != "" {
			tb.Comments[i+1] = c
		}
	}
	return tb
}

type sbPage struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

type sbZone struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Page  string `json:"page,omitempty"`
	Core  string `json:"core,omitempty"`
}

type sbPart struct {
	Ref       string            `json:"ref"`
	Value     string            `json:"value,omitempty"`
	LCSC      string            `json:"lcsc,omitempty"`
	MPN       string            `json:"mpn,omitempty"`
	Symbol    string            `json:"symbol,omitempty"`
	Footprint string            `json:"footprint,omitempty"`
	Zone      string            `json:"zone,omitempty"`
	Page      string            `json:"page,omitempty"`
	Pins      []sbPin           `json:"pins,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
	DNP       bool              `json:"dnp,omitempty"`
	// Block / Role trace a part back to the block instance it came from.
	Block string `json:"block,omitempty"`
	Role  string `json:"role,omitempty"`
}

type sbPin struct {
	Number string `json:"number"`
	Name   string `json:"name,omitempty"`
	Type   string `json:"type,omitempty"`
}

type sbBlock struct {
	Block    string            `json:"block"`
	Instance string            `json:"instance,omitempty"`
	Zone     string            `json:"zone,omitempty"`
	Page     string            `json:"page,omitempty"`
	Bind     map[string]string `json:"bind,omitempty"`
	Refs     map[string]string `json:"refs,omitempty"`    // role → designator
	LCSC     map[string]string `json:"lcsc,omitempty"`    // role → LCSC number (replaces the library part's)
	Values   map[string]string `json:"values,omitempty"`  // role → value
	Symbols  map[string]string `json:"symbols,omitempty"` // role → Lib:Name
}

type sbNet struct {
	Name     string   `json:"name"`
	Pins     []string `json:"pins"`
	Kind     string   `json:"kind,omitempty"` // power | ground | signal (default: by name)
	VoltageV float64  `json:"voltage,omitempty"`
	CurrentA float64  `json:"currentA,omitempty"`
	// Soft pins (from blocks) may be absent from the LCSC symbol (an EPAD
	// the EasyEDA device had); they are dropped with a warning.
	Soft []string `json:"-"`
}

type sbRail struct {
	Net      string  `json:"net"`
	Voltage  float64 `json:"voltage,omitempty"`
	CurrentA float64 `json:"currentA,omitempty"`
}

func loadSbSpec(path string) (*sbSpec, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s sbSpec
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Schema != "" && s.Schema != sbSchema {
		return nil, fmt.Errorf("%s: schema %q, want %q", path, s.Schema, sbSchema)
	}
	return &s, nil
}

// ── resolved design ─────────────────────────────────────────────────────────

// sbRPart is a part with its symbol resolved.
type sbRPart struct {
	sbPart
	LibID     string         `json:"libId"`
	LibName   string         `json:"-"` // project library nickname ("" = KiCad stock)
	SymName   string         `json:"-"`
	SymText   string         `json:"-"` // (symbol "SymName" …)
	LPins     []kicad.LibPin `json:"-"` // unit 1 + common pins
	Multi     bool           `json:"-"` // more than one unit
	FPFile    string         `json:"-"` // footprint file to copy into the project
	Source    string         `json:"source"`
	ZoneID    string         `json:"zone"`
	PageID    string         `json:"page"`
	PowerPins []string       `json:"-"`
}

// sbDesign is the expanded, resolved spec the builder draws.
type sbDesign struct {
	Spec     *sbSpec
	Parts    []*sbRPart
	ByRef    map[string]*sbRPart
	Nets     []sbNet           // pins "REF.NUM"
	PinNet   map[string]string // "REF.NUM" → net
	NetKind  map[string]string // net → power|ground|signal
	NC       map[string]bool   // "REF.NUM" with a no-connect flag
	Zones    []sbZone          // ordered
	Pages    []sbPage          // ordered
	NetPages map[string]map[string]bool
	Warnings []string
}

func sbPinKey(ref, num string) string { return ref + "." + num }

// sbSplitPin splits "REF:PIN" or "REF.PIN".
func sbSplitPin(s string) (string, string, bool) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ":"); i > 0 {
		return s[:i], s[i+1:], s[i+1:] != ""
	}
	if i := strings.Index(s, "."); i > 0 {
		return s[:i], s[i+1:], s[i+1:] != ""
	}
	return "", "", false
}

// resolvePins maps a pin token (number, name, NAME* prefix) to pin numbers.
func (p *sbRPart) resolvePins(tok string) ([]string, error) {
	for _, q := range p.LPins {
		if q.Number == tok {
			return []string{q.Number}, nil
		}
	}
	match := func(f func(string) bool) []string {
		var out []string
		seen := map[string]bool{}
		for _, q := range p.LPins {
			if f(q.Name) && !seen[q.Number] {
				seen[q.Number] = true
				out = append(out, q.Number)
			}
		}
		sort.Slice(out, func(i, j int) bool { return sbPinLess(out[i], out[j]) })
		return out
	}
	if strings.HasSuffix(tok, "*") {
		pre := strings.TrimSuffix(tok, "*")
		if out := match(func(n string) bool { return strings.HasPrefix(n, pre) }); len(out) > 0 {
			return out, nil
		}
		if out := match(func(n string) bool { return strings.HasPrefix(strings.ToUpper(n), strings.ToUpper(pre)) }); len(out) > 0 {
			return out, nil
		}
	} else {
		if out := match(func(n string) bool { return n == tok }); len(out) > 0 {
			return out, nil
		}
		if out := match(func(n string) bool { return strings.EqualFold(n, tok) }); len(out) > 0 {
			return out, nil
		}
	}
	var names []string
	for _, q := range p.LPins {
		names = append(names, q.Number+"="+q.Name)
	}
	if len(names) > 24 {
		names = append(names[:24], "…")
	}
	return nil, fmt.Errorf("%s has no pin %q (pins: %s)", p.Ref, tok, strings.Join(names, " "))
}

func sbPinLess(a, b string) bool {
	var ai, bi int
	_, ea := fmt.Sscanf(a, "%d", &ai)
	_, eb := fmt.Sscanf(b, "%d", &bi)
	if ea == nil && eb == nil && fmt.Sprint(ai) == a && fmt.Sprint(bi) == b {
		return ai < bi
	}
	return a < b
}

// netKindOf classifies a net: explicit kind, then the block-apply name rule.
func netKindOf(n sbNet) string {
	switch strings.ToLower(n.Kind) {
	case "power":
		return "power"
	case "ground", "gnd":
		return "ground"
	case "signal":
		return "signal"
	}
	switch bapFlagKind(n.Name) {
	case "gnd", "agnd", "pgnd":
		return "ground"
	case "power":
		return "power"
	}
	return "signal"
}

// ── block expansion ─────────────────────────────────────────────────────────

// expandBlocks turns spec.Blocks into parts + nets (block-apply's planner:
// standard-parts devices, designators, internal_nets, port binding).
func expandBlocks(s *sbSpec, partsPath string) error {
	if len(s.Blocks) == 0 {
		return nil
	}
	path, err := resolveStandardParts(partsPath)
	if err != nil {
		return fmt.Errorf("blocks need standard-parts.json: %w", err)
	}
	devices, err := loadStandardParts(path)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, p := range s.Parts {
		existing[strings.ToUpper(p.Ref)] = true
	}
	netIdx := map[string]int{}
	for i, n := range s.Nets {
		netIdx[n.Name] = i
	}
	for bi, sb := range s.Blocks {
		b, ok, err := blocks.Get(sb.Block)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("blocks[%d]: no such block %q (pcbpilot blocks ls)", bi, sb.Block)
		}
		topo, err := blockTopology(b)
		if err != nil {
			return err
		}
		plan, err := planBlockApply(bapInput{Block: b, Topology: topo, Devices: devices, Existing: existing,
			Instance: sb.Instance, Bind: sb.Bind})
		if err != nil {
			return fmt.Errorf("blocks[%d] %s: %w", bi, sb.Block, err)
		}
		if len(sb.Refs) > 0 {
			ren := map[string]string{}
			for _, pl := range plan.Placements {
				if to, ok := sb.Refs[pl.Role]; ok && to != pl.Designator {
					if existing[strings.ToUpper(to)] {
						return fmt.Errorf("blocks[%d] %s: refs %s=%s is already used", bi, sb.Block, pl.Role, to)
					}
					ren[strings.ToUpper(pl.Designator)] = to
				}
			}
			for role := range sb.Refs {
				if _, ok := b.Parts[role]; !ok {
					return fmt.Errorf("blocks[%d] %s: refs names unknown role %q", bi, sb.Block, role)
				}
			}
			bapRemapDesignators(&plan, ren)
		}
		zone := sb.Zone
		if zone == "" {
			zone = strings.ToLower(plan.Instance)
		}
		for _, pl := range plan.Placements {
			dev := devices[pl.PartKey]
			val := dev.Value
			if v := b.Parts[pl.Role].ValueOverride; v != "" {
				val = v
			}
			if val == "" {
				val = dev.MPN
			}
			lcsc, mpn := dev.LCSC, dev.MPN
			if c, ok := sb.LCSC[pl.Role]; ok {
				lcsc, mpn = c, ""
			}
			if v, ok := sb.Values[pl.Role]; ok {
				val = v
			}
			existing[strings.ToUpper(pl.Designator)] = true
			s.Parts = append(s.Parts, sbPart{Ref: pl.Designator, Value: val, LCSC: lcsc, MPN: mpn, Symbol: sb.Symbols[pl.Role],
				Zone: zone, Page: sb.Page, Block: b.ID + "#" + plan.Instance, Role: pl.Role})
		}
		for _, n := range plan.Nets {
			var pins []string
			for _, m := range n.Members {
				pins = append(pins, m)
			}
			kind := ""
			switch n.Kind {
			case "gnd", "agnd", "pgnd":
				kind = "ground"
			case "power":
				kind = "power"
			}
			if i, ok := netIdx[n.Net]; ok {
				s.Nets[i].Pins = append(s.Nets[i].Pins, pins...)
				s.Nets[i].Soft = append(s.Nets[i].Soft, pins...)
				if s.Nets[i].Kind == "" {
					s.Nets[i].Kind = kind
				}
				continue
			}
			netIdx[n.Net] = len(s.Nets)
			s.Nets = append(s.Nets, sbNet{Name: n.Net, Pins: pins, Kind: kind, Soft: pins})
		}
	}
	s.Blocks = nil
	return nil
}

// ── symbol resolution ───────────────────────────────────────────────────────

type sbResolveOpts struct {
	OutDir    string
	Offline   bool // never touch the network (cache / local symbols / generated only)
	Jobs      int
	PartsPath string
}

type sbResolveStats struct {
	LCSCCached   int      `json:"lcscCached"`
	LCSCImported int      `json:"lcscImported"`
	Stock        int      `json:"stock"`
	Local        int      `json:"local"`
	Generated    int      `json:"generated"`
	Notes        []string `json:"notes,omitempty"`
}

// sbFindSymbol looks up "Lib:Name" in the project dir, the spec's libDirs and
// KiCad's stock library (stock = KiCad resolves it from its global table).
func sbFindSymbol(id string, dirs []string) (text, file string, stock bool, err error) {
	lib, name, ok := strings.Cut(id, ":")
	if !ok || lib == "" || name == "" {
		if strings.HasSuffix(strings.ToLower(lib), ".kicad_sym") {
			return "", "", false, fmt.Errorf("symbol %q: want Lib:Name or path/to/lib.kicad_sym:Name", id)
		}
		return "", "", false, fmt.Errorf("symbol %q: want Lib:Name", id)
	}
	if strings.HasSuffix(lib, ".kicad_sym") { // explicit file path
		t, err := kicad.LibSymbolFromFile(lib, name)
		return t, lib, false, err
	}
	for _, d := range dirs {
		f := filepath.Join(d, lib+".kicad_sym")
		if _, err := os.Stat(f); err == nil {
			if t, err := kicad.LibSymbolFromFile(f, name); err == nil {
				return t, f, false, nil
			}
		}
	}
	if sd := kicad.StockSymbolDir(); sd != "" {
		f := filepath.Join(sd, lib+".kicad_sym")
		if _, err := os.Stat(f); err == nil {
			t, err := kicad.LibSymbolCached(f, name)
			return t, f, true, err
		}
	}
	return "", "", false, fmt.Errorf("symbol %s not found (project dir, libDirs, KiCad stock library)", id)
}

func sbRefPrefix(ref string) string {
	i := 0
	for i < len(ref) && (ref[i] < '0' || ref[i] > '9') {
		i++
	}
	if i == 0 {
		return "U"
	}
	return ref[:i]
}

// resolveSymbols gives every part a library symbol: an explicit symbol, the
// LCSC conversion (cache, then network import in parallel), or a generated
// box from the spec pin list.
func resolveSymbols(parts []*sbRPart, opts sbResolveOpts, libDirs []string) (sbResolveStats, error) {
	var st sbResolveStats
	dirs := append([]string{opts.OutDir}, libDirs...)
	// LCSC parts without an explicit symbol: fetch distinct numbers in parallel.
	need := map[string]bool{}
	for _, p := range parts {
		if p.Symbol == "" && p.LCSC != "" {
			if !kicad.ValidLCSC(p.LCSC) {
				return st, fmt.Errorf("%s: %q is not an LCSC number", p.Ref, p.LCSC)
			}
			need[p.LCSC] = true
		}
	}
	cached := map[string]*kicad.CachedPart{}
	failed := map[string]error{}
	if len(need) > 0 {
		var tools kicad.FabTools
		var toolErr error
		if !opts.Offline {
			if tools.CLI, toolErr = kicad.ResolveFabCLI(); toolErr == nil {
				tools.Python, toolErr = kicad.ResolveFabPython()
			}
		}
		jobs := opts.Jobs
		if jobs <= 0 {
			jobs = 8
		}
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, jobs)
		for c := range need {
			wg.Add(1)
			go func(c string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				var cp *kicad.CachedPart
				var err error
				if b, rerr := os.ReadFile(filepath.Join(kicad.LCSCCacheDir(c), "part.json")); rerr == nil && len(b) > 0 {
					cp, err = kicad.CachedImportLCSC(tools, c) // cache hit: no tools used
				} else if opts.Offline {
					err = fmt.Errorf("%s not in the cache (offline)", c)
				} else if toolErr != nil {
					err = toolErr
				} else {
					cp, err = kicad.CachedImportLCSC(tools, c)
				}
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					failed[c] = err
				} else {
					cached[c] = cp
				}
			}(c)
		}
		wg.Wait()
	}
	for _, p := range parts {
		var text, src string
		switch {
		case p.Symbol != "":
			t, file, stock, err := sbFindSymbol(p.Symbol, dirs)
			if err != nil {
				return st, fmt.Errorf("%s: %w", p.Ref, err)
			}
			if parent := kicad.SymbolExtends(t); parent != "" {
				return st, fmt.Errorf("%s: symbol %s extends %s (derived symbols are not supported; use the parent, an LCSC number or a pin list)", p.Ref, p.Symbol, parent)
			}
			lib, name, _ := strings.Cut(p.Symbol, ":")
			if strings.HasSuffix(lib, ".kicad_sym") {
				lib = strings.TrimSuffix(filepath.Base(lib), ".kicad_sym")
			}
			p.SymName, text = name, t
			if stock {
				p.LibID, src = lib+":"+name, "stock"
				st.Stock++
			} else {
				p.LibName, p.LibID, src = lib, lib+":"+name, "local:"+file
				st.Local++
			}
		case p.LCSC != "" && cached[p.LCSC] != nil:
			cp := cached[p.LCSC]
			t, err := cp.SymbolText()
			if err != nil {
				return st, fmt.Errorf("%s: cached %s: %w", p.Ref, p.LCSC, err)
			}
			p.SymName, p.LibName, p.LibID, text = cp.SymbolName, "lcsc", "lcsc:"+cp.SymbolName, t
			if p.Footprint == "" {
				p.Footprint = "lcsc:" + cp.FootprintName
				p.FPFile = cp.FootprintFile()
			}
			if cp.FromCache {
				src = "lcsc-cache"
				st.LCSCCached++
			} else {
				src = "lcsc-import"
				st.LCSCImported++
			}
		case len(p.Pins) > 0:
			if p.LCSC != "" {
				st.Notes = append(st.Notes, fmt.Sprintf("%s: LCSC %s unavailable (%v) — generated symbol from the spec pins", p.Ref, p.LCSC, failed[p.LCSC]))
			}
			var gp []kicad.GenPin
			for _, q := range p.Pins {
				gp = append(gp, kicad.GenPin{Number: q.Number, Name: q.Name, Type: q.Type})
			}
			name := sbGenName(p)
			p.SymName, p.LibName, p.LibID = name, "pcbpilot_gen", "pcbpilot_gen:"+name
			text, src = kicad.GenericSymbolText(name, sbRefPrefix(p.Ref), gp), "generated"
			st.Generated++
		case p.LCSC != "":
			return st, fmt.Errorf("%s: LCSC %s: %v (give \"symbol\" or \"pins\" to build without it)", p.Ref, p.LCSC, failed[p.LCSC])
		default:
			return st, fmt.Errorf("%s: no symbol, lcsc or pins", p.Ref)
		}
		if strings.HasPrefix(src, "lcsc") {
			text = sbPassivePins(text, p.Ref)
		}
		pins, err := kicad.LibSymbolPins(text)
		if err != nil {
			return st, fmt.Errorf("%s: %w", p.Ref, err)
		}
		units := map[int]bool{}
		for _, q := range pins {
			if q.Unit > 0 {
				units[q.Unit] = true
			}
		}
		p.Multi = len(units) > 1
		for _, q := range pins {
			if q.Unit == 0 || q.Unit == 1 || p.Multi {
				p.LPins = append(p.LPins, q)
			}
			if q.Type == "power_out" {
				p.PowerPins = append(p.PowerPins, q.Number)
			}
		}
		if len(p.LPins) == 0 && len(pins) > 0 {
			p.LPins = pins
		}
		p.SymText, p.Source = text, src
		if p.Value == "" {
			p.Value = kicad.LibSymbolProperty(text, "Value")
		}
		if p.Footprint == "" {
			p.Footprint = kicad.LibSymbolProperty(text, "Footprint")
		}
	}
	return st, nil
}

// sbPassiveRefs are designator classes whose pins are electrically passive.
var sbPassiveRefs = map[string]bool{"R": true, "C": true, "L": true, "FB": true, "SW": true, "F": true, "Y": true, "X": true,
	"D": true, "LED": true, "J": true, "CN": true, "P": true, "TP": true, "RN": true, "K": true, "BZ": true}

// sbPassivePins retypes the pins of a converted EasyEDA symbol of a passive
// class (EasyEDA symbols carry no reliable pin type: resistors come in as
// "input") so ERC judges real drivers only.
func sbPassivePins(sym, ref string) string {
	if !sbPassiveRefs[sbRefPrefix(ref)] {
		return sym
	}
	for _, t := range []string{"input", "output", "bidirectional", "unspecified", "tri_state"} {
		sym = strings.ReplaceAll(sym, "(pin "+t+" ", "(pin passive ")
	}
	return sym
}

// sbGenName is the generated symbol's name: value (or MPN) made safe, plus a
// pin-count suffix so two different pinouts never share a name.
func sbGenName(p *sbRPart) string {
	base := p.MPN
	if base == "" {
		base = p.Value
	}
	if base == "" {
		base = sbRefPrefix(p.Ref)
	}
	var b strings.Builder
	for _, r := range base {
		if r == ':' || r == '"' || r == '/' || r == '\\' || r == ' ' {
			r = '_'
		}
		b.WriteRune(r)
	}
	h := ""
	for _, q := range p.Pins {
		h += q.Number + "=" + q.Name + "/" + q.Type + ";"
	}
	return fmt.Sprintf("%s_%dP_%s", b.String(), len(p.Pins), sbShortHash(h))
}

// ── expansion + checks ──────────────────────────────────────────────────────

// buildDesign expands blocks, resolves symbols and pins, and checks the
// spec (every pin on at most one net, every net named, …).
func buildDesign(s *sbSpec, opts sbResolveOpts) (*sbDesign, sbResolveStats, error) {
	var st sbResolveStats
	if err := expandBlocks(s, opts.PartsPath); err != nil {
		return nil, st, err
	}
	if len(s.Parts) == 0 {
		return nil, st, fmt.Errorf("spec has no parts")
	}
	d := &sbDesign{Spec: s, ByRef: map[string]*sbRPart{}, PinNet: map[string]string{}, NetKind: map[string]string{},
		NC: map[string]bool{}, NetPages: map[string]map[string]bool{}}
	for i := range s.Parts {
		p := &sbRPart{sbPart: s.Parts[i]}
		if p.Ref == "" || strings.ContainsAny(p.Ref, " .:#") {
			return nil, st, fmt.Errorf("parts[%d]: bad ref %q", i, p.Ref)
		}
		if d.ByRef[p.Ref] != nil {
			return nil, st, fmt.Errorf("duplicate ref %s", p.Ref)
		}
		d.ByRef[p.Ref] = p
		d.Parts = append(d.Parts, p)
	}
	var err error
	if st, err = resolveSymbols(d.Parts, opts, s.LibDirs); err != nil {
		return nil, st, err
	}
	// pages and zones
	pageIdx := map[string]bool{}
	for _, pg := range s.Pages {
		if pg.ID == "" || pageIdx[pg.ID] {
			return nil, st, fmt.Errorf("pages: empty or duplicate id %q", pg.ID)
		}
		pageIdx[pg.ID] = true
		d.Pages = append(d.Pages, pg)
	}
	defPage := "main"
	if len(d.Pages) > 0 {
		defPage = d.Pages[0].ID
	}
	zoneIdx := map[string]int{}
	for _, z := range s.Zones {
		if z.ID == "" {
			return nil, st, fmt.Errorf("zones: empty id")
		}
		if _, dup := zoneIdx[z.ID]; dup {
			return nil, st, fmt.Errorf("zones: duplicate id %q", z.ID)
		}
		zoneIdx[z.ID] = len(d.Zones)
		d.Zones = append(d.Zones, z)
	}
	for _, p := range d.Parts {
		z := p.Zone
		if z == "" {
			z = "main"
		}
		i, ok := zoneIdx[z]
		if !ok {
			i = len(d.Zones)
			zoneIdx[z] = i
			d.Zones = append(d.Zones, sbZone{ID: z, Page: p.Page})
		}
		if d.Zones[i].Page == "" {
			d.Zones[i].Page = p.Page
		}
		p.ZoneID = z
	}
	for i := range d.Zones {
		if d.Zones[i].Page == "" {
			d.Zones[i].Page = defPage
		}
		if !pageIdx[d.Zones[i].Page] {
			pageIdx[d.Zones[i].Page] = true
			d.Pages = append(d.Pages, sbPage{ID: d.Zones[i].Page})
		}
		if d.Zones[i].Title == "" {
			d.Zones[i].Title = d.Zones[i].ID
		}
	}
	zonePage := map[string]string{}
	for _, z := range d.Zones {
		zonePage[z.ID] = z.Page
	}
	for _, p := range d.Parts {
		p.PageID = zonePage[p.ZoneID]
	}
	// nets
	seenNet := map[string]int{}
	for _, n := range s.Nets {
		name := strings.TrimSpace(n.Name)
		if name == "" {
			return nil, st, fmt.Errorf("a net has no name (pins %v)", n.Pins)
		}
		if strings.ContainsAny(name, "\n\"") {
			return nil, st, fmt.Errorf("net name %q has a quote or newline", name)
		}
		idx, merged := seenNet[name]
		if !merged {
			idx = len(d.Nets)
			seenNet[name] = idx
			d.Nets = append(d.Nets, sbNet{Name: name, Kind: n.Kind, VoltageV: n.VoltageV, CurrentA: n.CurrentA})
		}
		for _, tok := range n.Pins {
			ref, pin, ok := sbSplitPin(tok)
			if !ok {
				return nil, st, fmt.Errorf("net %s: bad pin %q (want REF:PIN)", name, tok)
			}
			p := d.ByRef[ref]
			if p == nil {
				return nil, st, fmt.Errorf("net %s: unknown part %s", name, ref)
			}
			nums, err := p.resolvePins(pin)
			if err != nil {
				if sbContains(n.Soft, tok) {
					d.Warnings = append(d.Warnings, fmt.Sprintf("net %s: block pin %s dropped: %v", name, tok, err))
					continue
				}
				return nil, st, fmt.Errorf("net %s: %w", name, err)
			}
			for _, num := range nums {
				k := sbPinKey(ref, num)
				if prev, ok := d.PinNet[k]; ok && prev != name {
					return nil, st, fmt.Errorf("pin %s (%s) is on two nets: %s and %s", k, tok, prev, name)
				} else if ok {
					continue
				}
				d.PinNet[k] = name
				d.Nets[idx].Pins = append(d.Nets[idx].Pins, k)
			}
		}
	}
	for i := range d.Nets {
		n := &d.Nets[i]
		if len(n.Pins) == 0 {
			return nil, st, fmt.Errorf("net %s has no pins", n.Name)
		}
		d.NetKind[n.Name] = netKindOf(*n)
		d.NetPages[n.Name] = map[string]bool{}
		for _, k := range n.Pins {
			ref, _, _ := strings.Cut(k, ".")
			d.NetPages[n.Name][d.ByRef[ref].PageID] = true
		}
	}
	for _, r := range s.Rails {
		if _, ok := seenNet[r.Net]; !ok {
			return nil, st, fmt.Errorf("rails: net %s is not in nets", r.Net)
		}
		if d.NetKind[r.Net] == "signal" {
			d.NetKind[r.Net] = "power"
			if kicad.IsGroundNet(r.Net) {
				d.NetKind[r.Net] = "ground"
			}
		}
	}
	for _, tok := range s.NoConnect {
		ref, pin, ok := sbSplitPin(tok)
		if !ok || d.ByRef[ref] == nil {
			return nil, st, fmt.Errorf("noConnect: bad pin %q", tok)
		}
		nums, err := d.ByRef[ref].resolvePins(pin)
		if err != nil {
			return nil, st, fmt.Errorf("noConnect: %w", err)
		}
		for _, num := range nums {
			k := sbPinKey(ref, num)
			if n, ok := d.PinNet[k]; ok {
				return nil, st, fmt.Errorf("noConnect %s: pin is on net %s", tok, n)
			}
			d.NC[k] = true
		}
	}
	switch s.UnusedPins {
	case "", "nc":
		for _, p := range d.Parts {
			for _, q := range p.LPins {
				k := sbPinKey(p.Ref, q.Number)
				if _, on := d.PinNet[k]; !on && q.Type != "no_connect" {
					d.NC[k] = true
				}
			}
		}
	case "open":
	default:
		return nil, st, fmt.Errorf("unusedPins %q: want nc or open", s.UnusedPins)
	}
	for _, p := range d.Parts {
		if p.Multi {
			d.Warnings = append(d.Warnings, fmt.Sprintf("%s: multi-unit symbol — every unit is placed (grid layout)", p.Ref))
		}
	}
	return d, st, nil
}

// normalizedSpec is the expanded spec with every pin as REF:NUMBER (stored
// in the project; sch-edit edits it, sch-read round-trips to it).
func (d *sbDesign) normalizedSpec() *sbSpec {
	s := *d.Spec
	s.Schema = sbSchema
	s.Blocks = nil
	s.Parts = nil
	for _, p := range d.Parts {
		sp := p.sbPart
		sp.Zone, sp.Page = p.ZoneID, ""
		if sp.Symbol == "" && p.Source == "generated" {
			// keep the pins: the generated symbol is rebuilt from them
		} else if sp.Symbol == "" && p.LibID != "" && !strings.HasPrefix(p.Source, "lcsc") {
			sp.Symbol = p.LibID
		}
		s.Parts = append(s.Parts, sp)
	}
	s.Zones = d.Zones
	s.Pages = d.Pages
	s.Nets = nil
	for _, n := range d.Nets {
		nn := n
		nn.Pins = nil
		for _, k := range n.Pins {
			ref, num, _ := strings.Cut(k, ".")
			nn.Pins = append(nn.Pins, ref+":"+num)
		}
		if nn.Kind == "" {
			nn.Kind = d.NetKind[n.Name]
		}
		s.Nets = append(s.Nets, nn)
	}
	s.NoConnect = nil
	if s.UnusedPins == "open" {
		var nc []string
		for k := range d.NC {
			ref, num, _ := strings.Cut(k, ".")
			nc = append(nc, ref+":"+num)
		}
		sort.Strings(nc)
		s.NoConnect = nc
	}
	return &s
}

// ── command ─────────────────────────────────────────────────────────────────

type sbOptions struct {
	SpecPath, OutDir, Name, Report string
	FromConn                       []string
	Offline, DryRun, NoIntent      bool
	Analog, Fresh, NoPlanner       bool
	PartsPath                      string
	Jobs                           int
	PlannerBudget                  int
}

func newKicadSchBuildCmd(stdout, stderr io.Writer) *cobra.Command {
	var o sbOptions
	c := &cobra.Command{
		Use:   "sch-build",
		Short: "Build a complete, gate-checked KiCad schematic project from one design spec (one call, seconds)",
		Args:  cobra.NoArgs,
		Long: `Write the design as data (parts, nets, rails, blocks, pages, title block — schema
pcbpilot.kicad.sch-build/1, docs/kicad/sch-build.md) and let the tool draw it in one
deterministic call instead of ~80 step-by-step sch commands:

  1. symbols/footprints: explicit Lib:Name (project dir, "libDirs", KiCad stock), LCSC
     number (kicad lcsc --import, cached under ~/.pcbpilot/cache/kicad-lcsc — repeat
     builds are offline), or a generated box from the part's "pins"
  2. blocks (pcbpilot blocks) expand into parts + nets; parts go to zones and pages
  3. each zone is placed and wired by the offline layout planner (sch layout-plan's
     engine): short real wires inside a zone, power/ground symbols, labels between
     zones (global labels between pages); a zone the planner cannot solve falls back to
     a grid + the autoconnect planner
  4. title block, zone frames, PWR_FLAGs, sch-fit page size, .kicad_pro, .kicad_sch
     (hierarchical sheets for "pages"), sym-lib-table / fp-lib-table + project libraries
  5. gates: KiCad netlist == spec (hard: on mismatch nothing is written), ERC
     (kicad-cli), offline layout checks, connectivity.json + intent derive
     (intent.json → trace widths, IR, safety downstream)

--from-connectivity X.json (repeat per page) regenerates an EasyEDA design (connectivity
IR 1.4, pcbpilot sch connectivity) in KiCad: parts by LCSC number (else generated from
the pin list), nets, no-connects, modules → zones.

Every build saves a checkpoint (<out>/.pcbpilot/checkpoints/N); change the design later
with kicad sch-edit, read it compactly with kicad sch-read.`,
		Example: `  pcbpilot kicad sch-build --spec design.json --out build/
  pcbpilot kicad sch-build --spec design.json --out build/ --dry-run
  pcbpilot kicad sch-build --from-connectivity p1.json --from-connectivity p2.json --out gas/ --name gas`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if (o.SpecPath == "") == (len(o.FromConn) == 0) {
				return fmt.Errorf("give exactly one of --spec or --from-connectivity")
			}
			if o.OutDir == "" {
				return fmt.Errorf("--out is required")
			}
			var spec *sbSpec
			var err error
			if o.SpecPath != "" {
				spec, err = loadSbSpec(o.SpecPath)
			} else {
				spec, err = specFromConnectivity(o.FromConn)
			}
			if err != nil {
				return err
			}
			if o.Name != "" {
				spec.Name = o.Name
			}
			res, err := runSchBuild(spec, o, nil)
			return finishSbRun(res, err, o.Report, stdout, stderr)
		},
	}
	f := c.Flags()
	f.StringVar(&o.SpecPath, "spec", "", "design spec JSON (pcbpilot.kicad.sch-build/1)")
	f.StringArrayVar(&o.FromConn, "from-connectivity", nil, "EasyEDA connectivity IR 1.4 JSON (repeat: one per page)")
	f.StringVar(&o.OutDir, "out", "", "project directory to write")
	f.StringVar(&o.Name, "name", "", "project name (default: spec name, else the --out directory name)")
	f.StringVar(&o.Report, "report", "", "also write the report JSON here (default <out>/sch-build-report.json)")
	f.BoolVar(&o.DryRun, "dry-run", false, "print the full plan (parts, zones, coordinates, connections, gates) and write nothing")
	f.BoolVar(&o.Offline, "offline", false, "never use the network (LCSC parts must be cached, else use pins)")
	f.BoolVar(&o.NoIntent, "no-intent", false, "skip intent derive (connectivity.json is still written)")
	f.BoolVar(&o.Analog, "analog", false, "run the analog SPICE step inside intent derive (slower)")
	f.BoolVar(&o.Fresh, "fresh", false, "ignore the layout/uuid state of an existing build in --out")
	f.BoolVar(&o.NoPlanner, "no-planner", false, "skip the layout planner (grid + autoconnect for every zone)")
	f.StringVar(&o.PartsPath, "parts", "", "standard-parts.json for blocks (default: the installed skill's)")
	f.IntVar(&o.Jobs, "jobs", 8, "parallel LCSC imports / zone plans")
	f.IntVar(&o.PlannerBudget, "planner-budget", 20000, "layout planner candidate budget per zone")
	return c
}

// finishSbRun prints the report and maps hard-gate failures to the exit code.
func finishSbRun(res *sbReport, err error, reportPath string, stdout, stderr io.Writer) error {
	if res != nil {
		if reportPath != "" {
			_ = writeJSONFile(reportPath, res)
		}
		if werr := writeJSON(stdout, res); werr != nil {
			return werr
		}
	}
	if err != nil {
		return err
	}
	if res != nil && !res.OK {
		var bad []string
		for _, g := range res.Gates {
			if g.Status == "fail" {
				bad = append(bad, g.Name)
			}
		}
		fmt.Fprintf(stderr, "sch-build: gate(s) failed: %s\n", strings.Join(bad, ", "))
		return errActionFailed
	}
	return nil
}

// ── --from-connectivity ─────────────────────────────────────────────────────

// specFromConnectivity turns EasyEDA connectivity IR documents (one per
// page) into a build spec.
func specFromConnectivity(paths []string) (*sbSpec, error) {
	s := &sbSpec{Schema: sbSchema}
	netPins := map[string][]string{}
	var netOrder []string
	seenRef := map[string]bool{}
	for pi, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var doc connectivity.Document
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		page := fmt.Sprintf("p%d", pi+1)
		title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if len(paths) > 1 {
			s.Pages = append(s.Pages, sbPage{ID: page, Title: title})
		}
		if s.Name == "" && doc.ProjectID != "" {
			s.Name = sbSanitize(doc.ProjectID)
		}
		zoneOf := map[string]string{}
		for _, m := range doc.Modules {
			zid := sbSanitize(m.Name)
			if zid == "" {
				zid = sbSanitize(m.ID)
			}
			if len(paths) > 1 {
				zid = page + "-" + zid
			}
			s.Zones = append(s.Zones, sbZone{ID: zid, Title: m.Name, Page: page})
			for _, c := range append(append([]string{}, m.CoreComponents...), m.PeripheralComponents...) {
				zoneOf[c] = zid
			}
		}
		netName := map[string]string{}
		for _, n := range doc.Nets {
			nm := n.Name
			if nm == "" {
				nm = n.ID
			}
			netName[n.ID] = nm
		}
		refOf := map[string]string{}
		for _, c := range doc.Components {
			if c.Ref == "" || strings.HasPrefix(c.Ref, "#") {
				continue
			}
			refOf[c.ID] = c.Ref
			if seenRef[c.Ref] {
				return nil, fmt.Errorf("%s: designator %s appears on two pages", path, c.Ref)
			}
			seenRef[c.Ref] = true
			p := sbPart{Ref: c.Ref, Value: c.Device.Name, Zone: zoneOf[c.ID]}
			if p.Zone == "" && len(paths) > 1 {
				p.Page = page
			}
			if kicad.ValidLCSC(c.Device.SupplierID) {
				p.LCSC = c.Device.SupplierID
			}
			for _, q := range c.Pins {
				p.Pins = append(p.Pins, sbPin{Number: q.Number, Name: q.Name})
				if q.NoConnected {
					s.NoConnect = append(s.NoConnect, c.Ref+":"+q.Number)
				}
			}
			s.Parts = append(s.Parts, p)
		}
		for _, cn := range doc.Connections {
			ref := refOf[cn.ComponentID]
			if ref == "" {
				continue
			}
			nm := netName[cn.NetID]
			if nm == "" {
				nm = cn.NetID
			}
			if _, ok := netPins[nm]; !ok {
				netOrder = append(netOrder, nm)
			}
			netPins[nm] = append(netPins[nm], ref+":"+cn.PinNumber)
		}
	}
	for _, n := range netOrder {
		s.Nets = append(s.Nets, sbNet{Name: n, Pins: netPins[n]})
	}
	if len(s.Parts) == 0 {
		return nil, fmt.Errorf("no components in %v", paths)
	}
	return s, nil
}

func sbSanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.' || r == '/':
			b.WriteRune('_')
		default:
			if r > 127 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// sbTimer records per-stage wall time.
type sbTimer struct {
	start time.Time
	last  time.Time
	mu    sync.Mutex
	Steps []sbStage
}

type sbStage struct {
	Stage string  `json:"stage"`
	Ms    float64 `json:"ms"`
}

func newSbTimer() *sbTimer { t := time.Now(); return &sbTimer{start: t, last: t} }

func (t *sbTimer) mark(stage string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.Steps = append(t.Steps, sbStage{stage, float64(now.Sub(t.last).Microseconds()) / 1000})
	t.last = now
}

func (t *sbTimer) total() float64 { return float64(time.Since(t.start).Microseconds()) / 1000 }
