package kicad

// fabrules.go — the board's effective manufacturing minimums and the order
// contract they are checked against (`kicad fab --preflight`).
//
// KiCad's DRC report lists violations, never the rules in force: a clean
// report proves the board meets its own rules, not the fab's limits. The
// smallest value the rules allow anywhere is read from every source DRC
// uses — Board Setup constraints (.kicad_pro), netclass clearances, custom
// rules (.kicad_dru) and local clearance overrides on footprints and pads —
// and the smallest of them must not be below the contract's limit.

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
}

// EffectiveMin is the smallest value the rules allow for one item.
type EffectiveMin struct {
	Item    string       `json:"item"` // track | space | drill | annular
	MinMm   *float64     `json:"minMm,omitempty"`
	Sources []RuleSource `json:"sources,omitempty"`
	// Unknown: rules that bound the item in a way pcbpilot cannot read
	// (an ignored constraint, an unparsable value); the minimum is then
	// unproven.
	Unknown []string `json:"unknown,omitempty"`
	// Unset: sources present but 0 (KiCad: no constraint).
	Unset []string `json:"unset,omitempty"`
}

func (e *EffectiveMin) add(src, where string, mm float64) {
	e.Sources = append(e.Sources, RuleSource{Source: src, Where: where, Mm: mm})
	if e.MinMm == nil || mm < *e.MinMm {
		v := mm
		e.MinMm = &v
	}
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

func (r *EffectiveRules) addProject(b []byte) error {
	var raw struct {
		Board struct {
			DesignSettings struct {
				Rules map[string]json.RawMessage `json:"rules"`
			} `json:"design_settings"`
		} `json:"board"`
		NetSettings struct {
			Classes []struct {
				Name      string   `json:"name"`
				Clearance *float64 `json:"clearance"`
			} `json:"classes"`
		} `json:"net_settings"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	rules := raw.Board.DesignSettings.Rules
	num := func(k string) (float64, bool) {
		var v float64
		if rules[k] == nil || json.Unmarshal(rules[k], &v) != nil {
			return 0, false
		}
		return v, true
	}
	flag := func(k string) bool {
		var v bool
		_ = json.Unmarshal(rules[k], &v)
		return v
	}
	r.MicroVias, r.BlindBuriedVias = flag("allow_microvias"), flag("allow_blind_buried_vias")
	put := func(e *EffectiveMin, key string) {
		v, ok := num(key)
		switch {
		case !ok:
		case v > 0:
			e.add(".kicad_pro", "board.design_settings.rules."+key, v)
		default:
			e.Unset = append(e.Unset, ".kicad_pro "+key+" = 0 (no constraint)")
		}
	}
	put(&r.Track, "min_track_width")
	put(&r.Space, "min_clearance")
	put(&r.Drill, "min_through_hole_diameter")
	put(&r.Annular, "min_via_annular_width")
	if r.MicroVias {
		put(&r.Drill, "min_microvia_drill")
	}
	for _, c := range raw.NetSettings.Classes {
		if c.Clearance != nil && *c.Clearance > 0 {
			r.Space.add("netclass", "net_settings.classes["+c.Name+"].clearance", *c.Clearance)
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
// contract item whose min cannot be read, or a rule that sets it to
// "ignore", makes that item Unknown.
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
		ignored := false
		if s := rule.child("severity"); s != nil && s.arg(0) == "ignore" {
			ignored = true
		}
		for _, c := range rule.children("constraint") {
			item, ok := druConstraintItem[c.arg(0)]
			if !ok {
				continue
			}
			e := r.item(item)
			where := fmt.Sprintf("rule %q constraint %s", name, c.arg(0))
			if ignored {
				e.Unknown = append(e.Unknown, where+": (severity ignore) turns the check off where its condition holds")
				continue
			}
			m := c.child("min")
			if m == nil {
				continue // only opt / max: no lower bound from this rule
			}
			mm, err := parseDRULength(m.arg(0))
			if err != nil {
				e.Unknown = append(e.Unknown, fmt.Sprintf("%s: min %q: %v", where, m.arg(0), err))
				continue
			}
			e.add(".kicad_dru", where, mm)
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

// addBoard reads local clearance overrides of footprints and pads (they
// replace the netclass clearance for that object, so may be smaller) and the
// smallest track width and drills on the board.
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
		case "footprint", "module":
			ref := ""
			for _, p := range k.children("property") {
				if p.arg(0) == "Reference" {
					ref = p.arg(1)
				}
			}
			if v, ok := num(k.child("clearance")); ok && v > 0 {
				r.Space.add("local", "footprint "+ref+" clearance override", v)
			}
			for _, p := range k.children("pad") {
				if v, ok := num(p.child("clearance")); ok && v > 0 {
					pad := p.arg(0)
					if pad == "" {
						pad = "(unnamed)"
					}
					r.Space.add("local", fmt.Sprintf("pad %s of %s clearance override", pad, ref), v)
				}
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

// LoadFabContract reads and validates a contract file: unknown fields are
// refused (a misspelt limit must not read as "no limit"), vendor, layers
// and at least one source with an http(s) url and a YYYY-MM-DD retrieval
// date are required, assembly.sides is top | bottom | both.
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
	for k, v := range map[string]float64{"minTrackMm": c.Limits.MinTrackMm, "minSpaceMm": c.Limits.MinSpaceMm, "minDrillMm": c.Limits.MinDrillMm, "minAnnularMm": c.Limits.MinAnnularMm} {
		if v < 0 || math.IsNaN(v) {
			bad = append(bad, "limits."+k+" is negative")
		}
	}
	if len(c.Sources) == 0 {
		bad = append(bad, "no sources: every value needs the vendor page (url) and the date it was read (retrieved)")
	}
	for i, s := range c.Sources {
		if u, err := url.Parse(s.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad = append(bad, fmt.Sprintf("sources[%d].url %q is not an http(s) URL", i, s.URL))
		}
		if _, err := time.Parse("2006-01-02", s.Retrieved); err != nil {
			bad = append(bad, fmt.Sprintf("sources[%d].retrieved %q is not a YYYY-MM-DD date", i, s.Retrieved))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return &c, fmt.Errorf("CONTRACT_INVALID: %s: %s (fix: complete the contract with the user; values come from the vendor's current page)", path, strings.Join(bad, "; "))
	}
	return &c, nil
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
