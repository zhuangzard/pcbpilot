package pcbauto

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Pin capability tables drive the schematic pin-swap feedback: which pins of
// a part are interchangeable for which nets. The canonical table ships with
// the Skill (.agents/skills/pcbpilot/references/pin-capabilities.json); the
// embedded copy below is its byte-identical mirror (pincaps_test keeps them
// in sync), because go:embed cannot reach outside the package.

//go:embed data/pin-capabilities.json
var pinCapsRaw []byte

// PinCap is one footprint pin of a remappable part.
type PinCap struct {
	Pin     string   `json:"pin"`
	Name    string   `json:"name"`
	GPIO    *int     `json:"gpio,omitempty"`
	Caps    []string `json:"caps"`
	Fixed   string   `json:"fixed,omitempty"`
	Caution string   `json:"caution,omitempty"`
}

// Has reports whether the pin carries capability c.
func (p *PinCap) Has(c string) bool {
	for _, x := range p.Caps {
		if strings.EqualFold(x, c) {
			return true
		}
	}
	return false
}

// Slot reports whether the pin may take a moved net at all.
func (p *PinCap) Slot() bool { return p.Fixed == "" && p.Has("gpio") }

// PartCaps is the capability table of one part family.
type PartCaps struct {
	ID     string   `json:"id"`
	Match  []string `json:"match"`
	Family string   `json:"family,omitempty"`
	// Remap: gpio-matrix (any digital function on any gpio pin) or af-table.
	Remap  string    `json:"remap"`
	Source string    `json:"source,omitempty"`
	Notes  []string  `json:"notes,omitempty"`
	Pins   []*PinCap `json:"pins"`

	byPin map[string]*PinCap
}

// Pin returns the capability entry of a footprint pin (nil = unknown).
func (pc *PartCaps) Pin(n string) *PinCap {
	if pc.byPin == nil {
		pc.byPin = map[string]*PinCap{}
		for _, p := range pc.Pins {
			pc.byPin[p.Pin] = p
		}
	}
	return pc.byPin[n]
}

// NetNeed maps a net-name pattern to the capability its pin must carry.
type NetNeed struct {
	Pattern string `json:"pattern"`
	Needs   string `json:"needs"`
	re      *regexp.Regexp
}

// ConnectorRule selects generic headers whose signal pins are interchangeable.
type ConnectorRule struct {
	Match   []string `json:"match"`
	Exclude []string `json:"exclude"`
}

// PinCapTable is the whole capability file.
type PinCapTable struct {
	SchemaVersion     int                 `json:"schemaVersion"`
	NetNeeds          []*NetNeed          `json:"netNeeds"`
	CapAliases        map[string][]string `json:"capAliases"`
	Parts             []*PartCaps         `json:"parts"`
	GenericConnectors ConnectorRule       `json:"genericConnectors"`
}

// ParsePinCaps parses and validates a capability file.
func ParsePinCaps(raw []byte) (*PinCapTable, error) {
	var t PinCapTable
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("pin capabilities: %w", err)
	}
	if t.SchemaVersion != 1 {
		return nil, fmt.Errorf("pin capabilities: schemaVersion %d unsupported (want 1)", t.SchemaVersion)
	}
	for _, n := range t.NetNeeds {
		re, err := regexp.Compile(n.Pattern)
		if err != nil {
			return nil, fmt.Errorf("pin capabilities: netNeeds %q: %w", n.Pattern, err)
		}
		n.re = re
	}
	for _, p := range t.Parts {
		if len(p.Match) == 0 {
			return nil, fmt.Errorf("pin capabilities: part %s has no match", p.ID)
		}
		switch p.Remap {
		case "gpio-matrix", "af-table":
		default:
			return nil, fmt.Errorf("pin capabilities: part %s remap %q (want gpio-matrix|af-table)", p.ID, p.Remap)
		}
		seen := map[string]bool{}
		for _, pin := range p.Pins {
			if seen[pin.Pin] {
				return nil, fmt.Errorf("pin capabilities: part %s pin %s listed twice", p.ID, pin.Pin)
			}
			seen[pin.Pin] = true
		}
	}
	return &t, nil
}

// DefaultPinCaps is the embedded table.
func DefaultPinCaps() *PinCapTable {
	t, err := ParsePinCaps(pinCapsRaw)
	if err != nil {
		panic(err)
	}
	return t
}

// Merge adds (and by id replaces) the parts of o; o's net needs go first.
func (t *PinCapTable) Merge(o *PinCapTable) {
	if o == nil {
		return
	}
	t.NetNeeds = append(append([]*NetNeed(nil), o.NetNeeds...), t.NetNeeds...)
	for k, v := range o.CapAliases {
		if t.CapAliases == nil {
			t.CapAliases = map[string][]string{}
		}
		t.CapAliases[k] = v
	}
	for _, p := range o.Parts {
		replaced := false
		for i, q := range t.Parts {
			if q.ID == p.ID {
				t.Parts[i], replaced = p, true
			}
		}
		if !replaced {
			t.Parts = append(t.Parts, p)
		}
	}
	if len(o.GenericConnectors.Match) > 0 {
		t.GenericConnectors = o.GenericConnectors
	}
}

// For returns the table of a part by its device name (nil = not remappable).
func (t *PinCapTable) For(device string) *PartCaps {
	d := upper(device)
	if d == "" {
		return nil
	}
	for _, p := range t.Parts {
		for _, m := range p.Match {
			if strings.Contains(d, upper(m)) {
				return p
			}
		}
	}
	return nil
}

// Need returns the capability a net's pin must carry ("" = plain digital).
func (t *PinCapTable) Need(net string) string {
	for _, n := range t.NetNeeds {
		if n.re != nil && n.re.MatchString(net) {
			return n.Needs
		}
	}
	return ""
}

// Satisfies reports whether pin p can carry need (aliases expanded).
func (t *PinCapTable) Satisfies(p *PinCap, need string) bool {
	if need == "" {
		return p.Has("gpio")
	}
	if p.Has(need) {
		return true
	}
	for _, a := range t.CapAliases[need] {
		if p.Has(a) {
			return true
		}
	}
	return false
}

// GenericConnector reports whether a part is a plain header whose signal
// pins are interchangeable.
func (t *PinCapTable) GenericConnector(p *Part) bool {
	if p == nil || len(p.Pads) < 2 {
		return false
	}
	ref := upper(p.Ref)
	if !(strings.HasPrefix(ref, "J") || strings.HasPrefix(ref, "P") || strings.HasPrefix(ref, "CN") || strings.HasPrefix(ref, "H")) {
		return false
	}
	d := upper(p.Device)
	for _, x := range t.GenericConnectors.Exclude {
		if strings.Contains(d, upper(x)) {
			return false
		}
	}
	for _, m := range t.GenericConnectors.Match {
		if strings.Contains(d, upper(m)) {
			return true
		}
	}
	return false
}
