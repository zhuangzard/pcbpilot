package powersim

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Pin is one component pin and the net it lands on ("" = unconnected).
type Pin struct {
	Number string
	Name   string
	Net    string
}

// Part is one schematic component with the attributes the models need.
type Part struct {
	Ref          string
	Value        string
	MPN          string
	LCSC         string
	Manufacturer string
	Description  string
	DeviceName   string
	Page         string
	Pins         []Pin
}

// Design is the merged multi-page schematic.
type Design struct {
	Parts   []*Part
	NetRole map[string]string // schematic role per net name (power|ground|signal|"")
	Sources []string          // provenance of the inputs
}

// PartValues are the per-ref attributes from `sch list` or a values file.
type PartValues struct {
	Value        string `json:"value,omitempty"`
	MPN          string `json:"mpn,omitempty"`
	LCSC         string `json:"lcsc,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Description  string `json:"description,omitempty"`
}

// ConnDoc is the subset of the 1.4 connectivity IR the simulator reads.
type ConnDoc struct {
	ProjectID  string `json:"projectId"`
	DocumentID string `json:"documentId"`
	Components []struct {
		ID     string `json:"id"`
		Ref    string `json:"ref"`
		PageID string `json:"pageId"`
		Device struct {
			Name       string `json:"name"`
			SupplierID string `json:"supplierId"`
		} `json:"device"`
		Pins []struct {
			Number string `json:"number"`
			Name   string `json:"name"`
		} `json:"pins"`
	} `json:"components"`
	Nets []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Role string `json:"role"`
	} `json:"nets"`
	Connections []struct {
		ComponentID string `json:"componentId"`
		PinNumber   string `json:"pinNumber"`
		NetID       string `json:"netId"`
	} `json:"connections"`
}

// unwrap finds the object holding key inside common envelopes
// ({"connectivity":…}, {"result":…}).
func unwrap(raw map[string]json.RawMessage, key string) (map[string]json.RawMessage, bool) {
	for depth := 0; depth < 4; depth++ {
		if _, ok := raw[key]; ok {
			return raw, true
		}
		var next map[string]json.RawMessage
		found := false
		for _, k := range []string{"connectivity", "result", "data"} {
			if v, ok := raw[k]; ok && json.Unmarshal(v, &next) == nil {
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
		raw = next
	}
	return nil, false
}

var reTemplate = regexp.MustCompile(`^=\{.*\}$`)
var reLCSC = regexp.MustCompile(`^C[0-9]+$`)

// ParseConnectivity decodes one `sch connectivity` export (1.4 IR, possibly
// wrapped in {"connectivity":…} or a daemon response envelope).
func ParseConnectivity(b []byte) (*ConnDoc, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	inner, ok := unwrap(raw, "connections")
	if !ok {
		return nil, fmt.Errorf("not a connectivity document (no components/nets/connections)")
	}
	buf, _ := json.Marshal(inner)
	var d ConnDoc
	if err := json.Unmarshal(buf, &d); err != nil {
		return nil, err
	}
	if len(d.Components) == 0 {
		return nil, fmt.Errorf("connectivity document has no components")
	}
	return &d, nil
}

// ParseValues decodes either a `sch list` response (components[] with
// designator/manufacturerId/supplierId/otherProperty) or a plain
// {"R1": {"value": "10k", "mpn": …}} / {"R1": "10k"} map (optionally under
// "parts" or "values").
func ParseValues(b []byte) (map[string]PartValues, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	if inner, ok := unwrap(raw, "components"); ok {
		var list struct {
			Components []map[string]any `json:"components"`
		}
		buf, _ := json.Marshal(inner)
		if err := json.Unmarshal(buf, &list); err == nil {
			for _, c := range list.Components {
				if _, ok := c["designator"]; ok {
					return ValuesFromSchList(list.Components), nil
				}
			}
		}
	}
	for _, k := range []string{"parts", "values"} {
		if v, ok := raw[k]; ok {
			var inner map[string]json.RawMessage
			if json.Unmarshal(v, &inner) == nil {
				raw = inner
				break
			}
		}
	}
	out := map[string]PartValues{}
	for ref, v := range raw {
		if strings.HasPrefix(ref, "_") || ref == "schemaVersion" || ref == "source" || ref == "provenance" {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[ref] = PartValues{Value: s}
			continue
		}
		var pv PartValues
		if err := json.Unmarshal(v, &pv); err != nil {
			return nil, fmt.Errorf("values[%s]: %w", ref, err)
		}
		out[ref] = pv
	}
	return out, nil
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return strings.TrimSpace(s)
}

// ValuesFromSchList converts `schematic.components.list` records.
func ValuesFromSchList(components []map[string]any) map[string]PartValues {
	out := map[string]PartValues{}
	for _, c := range components {
		ref := str(c, "designator")
		if ref == "" || str(c, "componentType") == "sheet" {
			continue
		}
		pv := out[ref]
		other, _ := c["otherProperty"].(map[string]any)
		if pv.Value == "" && other != nil {
			pv.Value = str(other, "Value")
		}
		if pv.Description == "" && other != nil {
			pv.Description = str(other, "Description")
		}
		if pv.MPN == "" {
			pv.MPN = str(c, "manufacturerId")
		}
		if pv.MPN == "" && other != nil {
			pv.MPN = str(other, "Manufacturer Part")
		}
		if pv.Manufacturer == "" {
			pv.Manufacturer = str(c, "manufacturer")
		}
		if sid := str(c, "supplierId"); pv.LCSC == "" && reLCSC.MatchString(sid) {
			pv.LCSC = sid
		}
		if res, ok := c["deviceResolution"].(map[string]any); ok && pv.LCSC == "" {
			if l := str(res, "lcsc"); reLCSC.MatchString(l) {
				pv.LCSC = l
			}
		}
		if pv.MPN == "" {
			if n := str(c, "name"); n != "" && !reTemplate.MatchString(n) {
				pv.MPN = n
			}
		}
		out[ref] = pv
	}
	return out
}

// BuildDesign merges connectivity pages (by net name) and attaches values.
func BuildDesign(pages []*ConnDoc, values map[string]PartValues) (*Design, []string, error) {
	d := &Design{NetRole: map[string]string{}}
	var warnings []string
	byRef := map[string]*Part{}
	for pi, pg := range pages {
		netName := map[string]string{}
		for _, n := range pg.Nets {
			name := n.Name
			if name == "" {
				name = n.ID
			}
			netName[n.ID] = name
			if n.Role != "" {
				if prev, ok := d.NetRole[name]; ok && prev != n.Role && prev != "" {
					warnings = append(warnings, fmt.Sprintf("net %s has role %q on one page and %q on another; using %q", name, prev, n.Role, prev))
				} else {
					d.NetRole[name] = n.Role
				}
			}
		}
		conn := map[string]map[string]string{}
		for _, c := range pg.Connections {
			if conn[c.ComponentID] == nil {
				conn[c.ComponentID] = map[string]string{}
			}
			name, ok := netName[c.NetID]
			if !ok {
				name = c.NetID
			}
			conn[c.ComponentID][c.PinNumber] = name
		}
		page := pg.DocumentID
		if page == "" {
			page = fmt.Sprintf("page%d", pi+1)
		}
		for _, c := range pg.Components {
			ref := c.Ref
			if ref == "" {
				ref = c.ID
			}
			p := byRef[ref]
			if p == nil {
				p = &Part{Ref: ref, Page: page}
				byRef[ref] = p
				d.Parts = append(d.Parts, p)
			}
			if n := c.Device.Name; n != "" && !reTemplate.MatchString(n) && p.DeviceName == "" {
				p.DeviceName = n
			}
			if reLCSC.MatchString(c.Device.SupplierID) && p.LCSC == "" {
				p.LCSC = c.Device.SupplierID
			}
			seen := map[string]bool{}
			for _, existing := range p.Pins {
				seen[existing.Number] = true
			}
			for _, pin := range c.Pins {
				if seen[pin.Number] {
					continue
				}
				seen[pin.Number] = true
				p.Pins = append(p.Pins, Pin{Number: pin.Number, Name: pin.Name, Net: conn[c.ID][pin.Number]})
			}
		}
	}
	for _, p := range d.Parts {
		v, ok := values[p.Ref]
		if !ok {
			continue
		}
		if v.Value != "" {
			p.Value = v.Value
		}
		if v.MPN != "" {
			p.MPN = v.MPN
		}
		if v.LCSC != "" {
			p.LCSC = v.LCSC
		}
		if v.Manufacturer != "" {
			p.Manufacturer = v.Manufacturer
		}
		if v.Description != "" {
			p.Description = v.Description
		}
	}
	if p := d.Parts; len(p) == 0 {
		return nil, warnings, fmt.Errorf("no components in the connectivity input")
	}
	sort.Slice(d.Parts, func(i, j int) bool { return refLess(d.Parts[i].Ref, d.Parts[j].Ref) })
	for _, p := range d.Parts {
		sort.Slice(p.Pins, func(i, j int) bool { return refLess(p.Pins[i].Number, p.Pins[j].Number) })
	}
	if len(values) > 0 {
		var missing []string
		for _, p := range d.Parts {
			if _, ok := values[p.Ref]; !ok {
				missing = append(missing, p.Ref)
			}
		}
		if len(missing) > 0 {
			warnings = append(warnings, "no values/MPN for "+strings.Join(missing, ", "))
		}
	}
	return d, warnings, nil
}

var reRefSplit = regexp.MustCompile(`^([^0-9]*)([0-9]*)(.*)$`)

// refLess orders "R2" before "R10".
func refLess(a, b string) bool {
	ma, mb := reRefSplit.FindStringSubmatch(a), reRefSplit.FindStringSubmatch(b)
	if ma[1] != mb[1] {
		return ma[1] < mb[1]
	}
	if len(ma[2]) != len(mb[2]) {
		return len(ma[2]) < len(mb[2])
	}
	if ma[2] != mb[2] {
		return ma[2] < mb[2]
	}
	return ma[3] < mb[3]
}

// Nets returns every net name that has at least one pin, sorted.
func (d *Design) Nets() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range d.Parts {
		for _, pin := range p.Pins {
			if pin.Net != "" && !seen[pin.Net] {
				seen[pin.Net] = true
				out = append(out, pin.Net)
			}
		}
	}
	sort.Strings(out)
	return out
}
