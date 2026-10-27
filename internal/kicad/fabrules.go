package kicad

// fabrules.go — the board's effective manufacturing minimums and the order
// contract they are checked against (`kicad fab --preflight`).
//
// KiCad's DRC report lists violations, never the rules in force: a clean
// report proves the board meets its own rules, not the fab's limits. The
// smallest value the rules allow anywhere is read from every source DRC
// uses — Board Setup constraints (.kicad_pro), netclass clearances, custom
// rules (.kicad_dru) and local clearance overrides on footprints, pads and
// zones — and the smallest of them must not be below the contract's limit.
// Only a global source (Board Setup, the Default netclass, an unconditional
// error-severity custom rule) proves the item is bounded everywhere; what
// pcbpilot cannot read is Unknown, never skipped.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RuleSource is one rule value bounding an item.
type RuleSource struct {
	Source string  `json:"source"` // .kicad_pro | netclass | .kicad_dru | local
	Where  string  `json:"where"`
	Mm     float64 `json:"mm"`
	// Global: the value applies to every object (Board Setup, the Default
	// netclass, an unconditional custom rule), so it bounds the item
	// everywhere. Local overrides and conditional rules only lower it.
	Global bool `json:"global,omitempty"`
	// Holes (drill only): the hole class the value bounds — "through" or
	// "micro"; empty for all holes (a custom hole_size rule).
	Holes string `json:"holes,omitempty"`
}

// EffectiveMin is the smallest value the rules allow for one item.
type EffectiveMin struct {
	Item    string       `json:"item"` // track | space | drill | annular
	MinMm   *float64     `json:"minMm,omitempty"`
	Sources []RuleSource `json:"sources,omitempty"`
	// Unknown: rules that bound the item in a way pcbpilot cannot prove
	// (an invalid value, an ignored or warning-only constraint, a rule
	// without a min); the minimum is then unproven.
	Unknown []string `json:"unknown,omitempty"`
	// Unset: sources present but 0 (KiCad: no constraint).
	Unset []string `json:"unset,omitempty"`
	// HoleClasses (drill only): the hole classes the board allows; each
	// needs its own global bound.
	HoleClasses []string `json:"holeClasses,omitempty"`
}

func (e *EffectiveMin) add(src, where string, mm float64, global bool) {
	e.Sources = append(e.Sources, RuleSource{Source: src, Where: where, Mm: mm, Global: global})
	if e.MinMm == nil || mm < *e.MinMm {
		v := mm
		e.MinMm = &v
	}
}

// Covered: a global source bounds the item — for drills, every hole class
// the board allows (a through-hole minimum says nothing about microvias).
func (e *EffectiveMin) Covered() bool {
	classes := e.HoleClasses
	if len(classes) == 0 {
		classes = []string{""}
	}
	for _, cl := range classes {
		ok := false
		for _, s := range e.Sources {
			ok = ok || (s.Global && (cl == "" || s.Holes == "" || s.Holes == cl))
		}
		if !ok {
			return false
		}
	}
	return true
}

// Measured is the smallest copper geometry actually on the board (mm).
type Measured struct {
	MinTrackMm    *float64 `json:"minTrackMm,omitempty"`
	MinViaDrillMm *float64 `json:"minViaDrillMm,omitempty"`
	MinPadDrillMm *float64 `json:"minPadDrillMm,omitempty"`
}

// EffectiveRules are a board's manufacturing minimums.
type EffectiveRules struct {
	Track           EffectiveMin `json:"track"`
	Space           EffectiveMin `json:"space"`
	Drill           EffectiveMin `json:"drill"`
	Annular         EffectiveMin `json:"annular"`
	MicroVias       bool         `json:"microVias"`
	BlindBuriedVias bool         `json:"blindBuriedVias"`
	Measured        Measured     `json:"measured"`
	Files           []string     `json:"files"` // sources read
}

// Items lists the four minimums in contract order.
func (r *EffectiveRules) Items() []*EffectiveMin {
	return []*EffectiveMin{&r.Track, &r.Space, &r.Drill, &r.Annular}
}

// ReadEffectiveRules reads the rules of pcb and of the .kicad_pro /
// .kicad_dru next to it (a missing file is no source; an unreadable one is
// an error).
func ReadEffectiveRules(pcb string) (*EffectiveRules, error) {
	r := &EffectiveRules{Track: EffectiveMin{Item: "track"}, Space: EffectiveMin{Item: "space"},
		Drill: EffectiveMin{Item: "drill"}, Annular: EffectiveMin{Item: "annular"}}
	base := strings.TrimSuffix(pcb, ".kicad_pcb")
	if b, err := os.ReadFile(base + ".kicad_pro"); err == nil {
		if err := r.addProject(b); err != nil {
			return nil, fmt.Errorf("%s: %w", base+".kicad_pro", err)
		}
		r.Files = append(r.Files, filepath.Base(base)+".kicad_pro")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if b, err := os.ReadFile(base + ".kicad_dru"); err == nil {
		r.addDRU(b)
		r.Files = append(r.Files, filepath.Base(base)+".kicad_dru")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	b, err := os.ReadFile(pcb)
	if err != nil {
		return nil, err
	}
	if err := r.addBoard(b); err != nil {
		return nil, fmt.Errorf("%s: %w", pcb, err)
	}
	r.Files = append(r.Files, filepath.Base(pcb))
	return r, nil
}

// rawNumber reads a JSON number; ok false for a present value of another type.
func rawNumber(raw json.RawMessage) (v float64, present, ok bool) {
	if raw == nil || string(bytes.TrimSpace(raw)) == "null" {
		return 0, false, true
	}
	if err := json.Unmarshal(raw, &v); err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, true, false
	}
	return v, true, true
}

func (r *EffectiveRules) addProject(b []byte) error {
	var raw struct {
		Board struct {
			DesignSettings struct {
				Rules map[string]json.RawMessage `json:"rules"`
			} `json:"design_settings"`
		} `json:"board"`
		NetSettings struct {
			Classes []map[string]json.RawMessage `json:"classes"`
		} `json:"net_settings"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	rules := raw.Board.DesignSettings.Rules
	// A present flag must be a JSON boolean: a malformed allow_microvias
	// would hide the (smaller) microvia drill minimum.
	flag := func(k string) (bool, bool) {
		raw := bytes.TrimSpace(rules[k])
		if len(raw) == 0 || string(raw) == "null" {
			return false, true
		}
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			return false, false
		}
		return v, true
	}
	var okMicro, okBlind bool
	r.MicroVias, okMicro = flag("allow_microvias")
	r.BlindBuriedVias, okBlind = flag("allow_blind_buried_vias")
	if !okMicro {
		r.Drill.Unknown = append(r.Drill.Unknown, fmt.Sprintf(".kicad_pro allow_microvias is %s, not true / false: whether the microvia drill minimum applies is unknown", string(rules["allow_microvias"])))
	}
	if !okBlind {
		r.Drill.Unknown = append(r.Drill.Unknown, fmt.Sprintf(".kicad_pro allow_blind_buried_vias is %s, not true / false", string(rules["allow_blind_buried_vias"])))
	}
	put := func(e *EffectiveMin, key string) {
		v, present, ok := rawNumber(rules[key])
		switch {
		case !ok:
			e.Unknown = append(e.Unknown, fmt.Sprintf(".kicad_pro %s is %s, not a length in mm", key, string(rules[key])))
		case !present:
		case v > 0:
			e.add(".kicad_pro", "board.design_settings.rules."+key, v, true)
		default:
			e.Unset = append(e.Unset, ".kicad_pro "+key+" = 0 (no constraint)")
		}
	}
	put(&r.Track, "min_track_width")
	put(&r.Space, "min_clearance")
	put(&r.Annular, "min_via_annular_width")
	r.Drill.HoleClasses = []string{"through"}
	put(&r.Drill, "min_through_hole_diameter")
	if r.MicroVias {
		r.Drill.HoleClasses = append(r.Drill.HoleClasses, "micro")
		put(&r.Drill, "min_microvia_drill")
	}
	for i := range r.Drill.Sources {
		switch r.Drill.Sources[i].Where {
		case "board.design_settings.rules.min_through_hole_diameter":
			r.Drill.Sources[i].Holes = "through"
		case "board.design_settings.rules.min_microvia_drill":
			r.Drill.Sources[i].Holes = "micro"
		}
	}
	// Every net is at least in Default, so its clearance is global. Another
	// class without a clearance inherits Default's (KiCad 9+); one whose
	// clearance is 0 has no proven value of its own: Unknown.
	var classes []string
	defClr := 0.0
	for i, c := range raw.NetSettings.Classes {
		var name string
		_ = json.Unmarshal(c["name"], &name)
		if name == "" {
			name = strconv.Itoa(i)
		}
		classes = append(classes, name)
		v, present, ok := rawNumber(c["clearance"])
		switch {
		case !ok:
			r.Space.Unknown = append(r.Space.Unknown, fmt.Sprintf("netclass %s clearance is %s, not a length in mm", name, string(c["clearance"])))
		case present && v > 0:
			r.Space.add("netclass", "net_settings.classes["+name+"].clearance", v, name == "Default")
			if name == "Default" {
				defClr = v
			}
		case present && name == "Default":
			r.Space.Unset = append(r.Space.Unset, "netclass Default clearance = 0 (no constraint)")
		case present:
			r.Space.Unknown = append(r.Space.Unknown, fmt.Sprintf("netclass %s clearance = 0: inherited or unset, its effective clearance is unproven (fix: give the class its clearance)", name))
		}
	}
	for i, c := range raw.NetSettings.Classes {
		if _, present, _ := rawNumber(c["clearance"]); !present && classes[i] != "Default" && defClr == 0 {
			r.Space.Unknown = append(r.Space.Unknown, fmt.Sprintf("netclass %s has no clearance and Default none to inherit: its effective clearance is unproven", classes[i]))
		}
	}
	return nil
}

// druConstraintItem maps the custom-rule constraints a contract bounds.
var druConstraintItem = map[string]string{"track_width": "track", "clearance": "space", "hole_size": "drill", "annular_width": "annular"}

func (r *EffectiveRules) item(name string) *EffectiveMin {
	switch name {
	case "track":
		return &r.Track
	case "space":
		return &r.Space
	case "drill":
		return &r.Drill
	}
	return &r.Annular
}

// addDRU reads the (rule …) entries of a .kicad_dru. A constraint of a
// contract item is Unknown when its rule is "ignore" or "warning" severity
// (not an enforced minimum where its condition holds), when it has no min
// (only opt / max: no lower bound there) or when its min cannot be read.
// A rule with a condition or a layer only lowers the minimum; an
// unconditional one also bounds the item everywhere.
func (r *EffectiveRules) addDRU(b []byte) {
	root, err := parseSexpr(append(append([]byte("(pcbpilot_dru\n"), b...), []byte("\n)")...))
	if err != nil {
		for _, e := range r.Items() {
			e.Unknown = append(e.Unknown, ".kicad_dru does not parse: "+err.Error())
		}
		return
	}
	for _, rule := range root.children("rule") {
		name := rule.arg(0)
		sev := ""
		if s := rule.child("severity"); s != nil {
			sev = s.arg(0)
		}
		global := rule.child("condition") == nil && rule.child("layer") == nil
		for _, c := range rule.children("constraint") {
			item, ok := druConstraintItem[c.arg(0)]
			if !ok {
				continue
			}
			e := r.item(item)
			where := fmt.Sprintf("rule %q constraint %s", name, c.arg(0))
			if sev != "" && sev != "error" {
				e.Unknown = append(e.Unknown, fmt.Sprintf("%s: (severity %s) is not an enforced minimum where its condition holds", where, sev))
				continue
			}
			m := c.child("min")
			if m == nil {
				e.Unknown = append(e.Unknown, where+": no min (only opt / max), so no lower bound where its condition holds")
				continue
			}
			mm, err := parseDRULength(m.arg(0))
			if err != nil {
				e.Unknown = append(e.Unknown, fmt.Sprintf("%s: min %q: %v", where, m.arg(0), err))
				continue
			}
			e.add(".kicad_dru", where, mm, global)
		}
	}
}

var reDRULength = regexp.MustCompile(`^([0-9]*\.?[0-9]+(?:[eE][-+]?[0-9]+)?)\s*(mm|mil|mils|in|um)?$`)

// parseDRULength reads a custom-rule length: a number with mm / mil / in /
// um, or a bare number (mm).
func parseDRULength(s string) (float64, error) {
	m := reDRULength.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("not a length pcbpilot can read (number + mm/mil/in/um)")
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	switch m[2] {
	case "mil", "mils":
		v *= 0.0254
	case "in":
		v *= 25.4
	case "um":
		v /= 1000
	}
	return v, nil
}

// addBoard reads local clearance overrides of footprints, pads and zones
// (they replace the netclass clearance for that object, so may be smaller)
// and the smallest track width and drills on the board.
func (r *EffectiveRules) addBoard(b []byte) error {
	root, err := parseSexpr(b)
	if err != nil {
		return err
	}
	if root.head() != "kicad_pcb" {
		return fmt.Errorf("not a kicad_pcb file (root %q)", root.head())
	}
	setMin := func(p **float64, v float64) {
		if v > 0 && (*p == nil || v < **p) {
			x := v
			*p = &x
		}
	}
	num := func(n *sx) (float64, bool) {
		if n == nil {
			return 0, false
		}
		v, err := strconv.ParseFloat(n.arg(0), 64)
		return v, err == nil
	}
	// KiCad 10 writes a footprint's or pad's (clearance …) only when the
	// override is set (verified with pcbnew 10.0.6: SetLocalClearance(0)
	// saves "(clearance 0)", an unset override writes nothing), so an
	// explicit 0 is a 0 mm override. A zone always writes its
	// connect_pads clearance; whether 0 there lowers anything below the
	// netclass is not something pcbpilot can prove: Unknown.
	local := func(n *sx, where string, zone bool) {
		c := n.child("clearance")
		if c == nil {
			return
		}
		v, ok := num(c)
		switch {
		case !ok || v < 0:
			r.Space.Unknown = append(r.Space.Unknown, fmt.Sprintf("%s clearance override %q is not a length in mm", where, c.arg(0)))
		case v == 0 && zone:
			r.Space.Unknown = append(r.Space.Unknown, where+" clearance 0: whether the zone then follows its netclass or allows 0 mm is unproven (fix: give the zone its clearance)")
		default:
			r.Space.add("local", where+" clearance override", v, false)
		}
	}
	for _, k := range root.Kids[1:] {
		switch k.head() {
		case "segment", "arc":
			if w, ok := num(k.child("width")); ok {
				setMin(&r.Measured.MinTrackMm, w)
			}
		case "via":
			if d, ok := num(k.child("drill")); ok {
				setMin(&r.Measured.MinViaDrillMm, d)
			}
		case "zone":
			if cp := k.child("connect_pads"); cp != nil {
				net := ""
				if n := k.child("net_name"); n != nil {
					net = n.arg(0)
				}
				local(cp, "zone ["+net+"]", true)
			}
		case "footprint", "module":
			ref := ""
			for _, p := range k.children("property") {
				if p.arg(0) == "Reference" {
					ref = p.arg(1)
				}
			}
			local(k, "footprint "+ref, false)
			for _, p := range k.children("pad") {
				pad := p.arg(0)
				if pad == "" {
					pad = "(unnamed)"
				}
				local(p, fmt.Sprintf("pad %s of %s", pad, ref), false)
				if p.arg(1) == "thru_hole" || p.arg(1) == "np_thru_hole" {
					if d := p.child("drill"); d != nil {
						// (drill D) or (drill oval W H): the smallest dimension.
						for i := 0; i+1 < len(d.Kids); i++ {
							if v, err := strconv.ParseFloat(d.arg(i), 64); err == nil {
								setMin(&r.Measured.MinPadDrillMm, v)
							}
						}
					}
				}
			}
		}
	}
	return nil
}

// FabContract is the order contract file (`kicad fab --preflight
// --contract`); the format is documented in the pcbpilot-fab skill
// (fab-acceptance.md §1).
type FabContract struct {
	Vendor      string             `json:"vendor"`
	Service     string             `json:"service,omitempty"`
	Layers      int                `json:"layers"`
	ThicknessMm float64            `json:"thicknessMm,omitempty"`
	CopperOz    map[string]float64 `json:"copperOz,omitempty"`
	Finish      string             `json:"finish,omitempty"`
	Mask        string             `json:"mask,omitempty"`
	Silk        string             `json:"silk,omitempty"`
	Impedance   json.RawMessage    `json:"impedance,omitempty"`
	Assembly    *struct {
		Sides string `json:"sides"`
		Qty   int    `json:"qty,omitempty"`
	} `json:"assembly,omitempty"`
	ViaProcess string `json:"viaProcess,omitempty"`
	Limits     struct {
		MinTrackMm   float64 `json:"minTrackMm"`
		MinSpaceMm   float64 `json:"minSpaceMm"`
		MinDrillMm   float64 `json:"minDrillMm"`
		MinAnnularMm float64 `json:"minAnnularMm"`
	} `json:"limits"`
	Sources []struct {
		Item      string `json:"item"`
		URL       string `json:"url"`
		Retrieved string `json:"retrieved"`
	} `json:"sources"`
}

// Limit is the contract's limit for an EffectiveMin item (0 = not given).
func (c *FabContract) Limit(item string) float64 {
	switch item {
	case "track":
		return c.Limits.MinTrackMm
	case "space":
		return c.Limits.MinSpaceMm
	case "drill":
		return c.Limits.MinDrillMm
	}
	return c.Limits.MinAnnularMm
}

// contractSourceItems are the values a sources[].item may name; a parent
// ("limits", "assembly") covers its fields.
var contractSourceItems = map[string]bool{
	"vendor": true, "service": true, "layers": true, "thicknessMm": true, "copperOz": true, "finish": true,
	"mask": true, "silk": true, "impedance": true, "viaProcess": true,
	"assembly": true, "assembly.sides": true, "assembly.qty": true,
	"limits": true, "limits.minTrackMm": true, "limits.minSpaceMm": true, "limits.minDrillMm": true, "limits.minAnnularMm": true,
}

// LoadFabContract reads and validates a contract file: unknown fields are
// refused (a misspelt limit must not read as "no limit"); vendor and layers
// are required; every source has a known item, an http(s) url and a
// YYYY-MM-DD retrieval date; every value preflight checks (each limit, the
// finish, assembly.sides) is covered by a source naming it or its parent;
// assembly.sides is top | bottom | both.
func LoadFabContract(path string) (*FabContract, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c FabContract
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("CONTRACT_INVALID: %s: %v (fix: use the fields of the contract template in the pcbpilot-fab skill, fab-acceptance.md §1)", path, err)
	}
	var bad []string
	if strings.TrimSpace(c.Vendor) == "" {
		bad = append(bad, "vendor is empty")
	}
	if c.Layers <= 0 {
		bad = append(bad, "layers must be a positive copper layer count")
	}
	if c.Assembly != nil && c.Assembly.Sides != "top" && c.Assembly.Sides != "bottom" && c.Assembly.Sides != "both" {
		bad = append(bad, fmt.Sprintf("assembly.sides %q is not top, bottom or both", c.Assembly.Sides))
	}
	checked := map[string]bool{}
	for k, v := range map[string]float64{"minTrackMm": c.Limits.MinTrackMm, "minSpaceMm": c.Limits.MinSpaceMm, "minDrillMm": c.Limits.MinDrillMm, "minAnnularMm": c.Limits.MinAnnularMm} {
		if v < 0 || math.IsNaN(v) {
			bad = append(bad, "limits."+k+" is negative")
		}
		if v > 0 {
			checked["limits."+k] = true
		}
	}
	if c.Finish != "" {
		checked["finish"] = true
	}
	if c.Layers > 0 {
		checked["layers"] = true // the layer-count check
	}
	if c.Assembly != nil && c.Assembly.Sides != "" {
		checked["assembly.sides"] = true
	}
	if len(c.Sources) == 0 {
		bad = append(bad, "no sources: every value needs the vendor page (url) and the date it was read (retrieved)")
	}
	covered := map[string]bool{}
	for i, s := range c.Sources {
		if !contractSourceItems[s.Item] {
			bad = append(bad, fmt.Sprintf("sources[%d].item %q is not a contract field (vendor, layers, finish, assembly[.sides], limits[.minTrackMm …], …)", i, s.Item))
		}
		covered[s.Item] = true
		if u, err := url.Parse(s.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad = append(bad, fmt.Sprintf("sources[%d].url %q is not an http(s) URL", i, s.URL))
		}
		if _, err := time.Parse("2006-01-02", s.Retrieved); err != nil {
			bad = append(bad, fmt.Sprintf("sources[%d].retrieved %q is not a YYYY-MM-DD date", i, s.Retrieved))
		}
	}
	for k := range checked {
		parent, _, _ := strings.Cut(k, ".")
		if !covered[k] && !covered[parent] {
			bad = append(bad, fmt.Sprintf("no source for %s (add a sources entry with item %q or %q)", k, k, parent))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return &c, fmt.Errorf("CONTRACT_INVALID: %s: %s (fix: complete the contract with the user; values come from the vendor's current page)", path, strings.Join(bad, "; "))
	}
	return &c, nil
}

// Surface-finish processes pcbpilot tells apart. A fab quote names one of
// these; anything else (or a bare "HASL", leaded or lead-free?) is
// ambiguous.
const (
	FinishHASLLeaded   = "hasl-leaded"
	FinishHASLLeadFree = "hasl-lead-free"
	FinishENIG         = "enig"
	FinishENEPIG       = "enepig"
	FinishOSP          = "osp"
	FinishImmSilver    = "immersion-silver"
	FinishImmTin       = "immersion-tin"
	FinishHardGold     = "hard-gold"
)

var finishAliases = map[string]string{
	"hal snpb": FinishHASLLeaded, "hasl snpb": FinishHASLLeaded, "hasl leaded": FinishHASLLeaded, "hasl with lead": FinishHASLLeaded,
	"leaded hasl": FinishHASLLeaded, "hal leaded": FinishHASLLeaded, "hasl lead": FinishHASLLeaded,
	"hal lead free": FinishHASLLeadFree, "hasl lead free": FinishHASLLeadFree, "lead free hasl": FinishHASLLeadFree,
	"lf hasl": FinishHASLLeadFree, "hasl lf": FinishHASLLeadFree, "leadfree hasl": FinishHASLLeadFree, "hasl leadfree": FinishHASLLeadFree,
	"enig": FinishENIG, "electroless nickel immersion gold": FinishENIG,
	"enepig": FinishENEPIG,
	"osp":    FinishOSP, "ht osp": FinishOSP, "organic solderability preservative": FinishOSP,
	"immersion silver": FinishImmSilver, "imag": FinishImmSilver,
	"immersion tin": FinishImmTin, "imsn": FinishImmTin,
	"hard gold": FinishHardGold, "electrolytic hard gold": FinishHardGold,
}

var reFinishSep = regexp.MustCompile(`[^a-z0-9]+`)

// FinishProcess names the process of a finish label (KiCad's stackup
// names, common fab names); unknown or ambiguous labels are an error.
func FinishProcess(label string) (string, error) {
	k := strings.TrimSpace(reFinishSep.ReplaceAllString(strings.ToLower(label), " "))
	if p, ok := finishAliases[k]; ok {
		return p, nil
	}
	if k == "hasl" || k == "hal" {
		return "", fmt.Errorf("%q is ambiguous: say leaded or lead-free (e.g. \"HASL lead-free\")", label)
	}
	return "", fmt.Errorf("%q is not a finish pcbpilot can name (HASL leaded / lead-free, ENIG, ENEPIG, OSP, immersion silver / tin, hard gold)", label)
}

// BoardFinish is the copper finish of the board's stackup ("" when the
// board has no stackup or names none).
func BoardFinish(b []byte) string {
	root, err := parseSexpr(b)
	if err != nil {
		return ""
	}
	setup := root.child("setup")
	if setup == nil {
		return ""
	}
	if st := setup.child("stackup"); st != nil {
		if f := st.child("copper_finish"); f != nil {
			if v := f.arg(0); !strings.EqualFold(v, "none") {
				return v
			}
		}
	}
	return ""
}

// HasEdgeCuts reports whether the board draws anything on Edge.Cuts (a
// board-level graphic or a footprint's).
func HasEdgeCuts(b []byte) bool {
	return bytes.Contains(b, []byte(`(layer "Edge.Cuts")`)) || bytes.Contains(b, []byte(`(layer Edge.Cuts)`))
}
