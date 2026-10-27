package kicad

// syncnetlist.go — the schematic side of `kicad pcb-sync`: a kicad-cli
// `sch export netlist` (--format kicadxml or kicadsexpr) parsed into
// SyncNetlist, with what matching footprints needs and ParseSchNetlist
// (schnet.go) drops: symbol UUIDs per unit, sheet path UUIDs, the
// dnp / exclude_from_board / exclude_from_bom flags and net classes.
//
// Footprint ↔ symbol key (KiCad's own convention, checked on the ecc83 and
// complex_hierarchy demos): a footprint's (path …) is the symbol's sheet
// path tstamps followed by the symbol UUID, e.g. "/<sheet-uuid>/<sym-uuid>",
// "/<sym-uuid>" on the root sheet. A multi-unit symbol exports one UUID per
// placed unit; its footprint carries the FIRST one (U1 of ecc83).

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Netlist export formats kicad-cli writes and ParseSyncNetlist reads.
const (
	NetlistKicadXML   = "kicadxml"
	NetlistKicadSexpr = "kicadsexpr"
)

// SyncNetlist is one schematic netlist export.
type SyncNetlist struct {
	Source     string      `json:"source,omitempty"` // design source as exported (root sheet)
	Tool       string      `json:"tool,omitempty"`   // e.g. "Eeschema 10.0.6"
	Sheets     []SyncSheet `json:"sheets"`
	Components []SyncComp  `json:"components"`
	Nets       []SyncNet   `json:"nets"`
}

// SyncSheet is one sheet instance (a sheet used twice appears twice).
type SyncSheet struct {
	Number  string `json:"number"`
	Name    string `json:"name"`    // human path, "/" or "/amp/"
	Tstamps string `json:"tstamps"` // UUID path, "/" or "/<uuid>/"
}

// SyncComp is one symbol instance (all units of a multi-unit symbol merged).
type SyncComp struct {
	Ref          string            `json:"ref"`
	Value        string            `json:"value"`
	Footprint    string            `json:"footprint"` // "Lib:Name" or ""
	Lib          string            `json:"lib"`       // libsource lib
	Part         string            `json:"part"`      // libsource part
	Description  string            `json:"description,omitempty"`
	SheetNames   string            `json:"sheetNames"`   // sheetpath names
	SheetTstamps string            `json:"sheetTstamps"` // sheetpath tstamps, ends with "/"
	Tstamps      []string          `json:"tstamps"`      // symbol UUID per unit, export order
	Units        []SyncUnit        `json:"units,omitempty"`
	Fields       map[string]string `json:"fields"`     // <fields> (Footprint, Datasheet, user fields)
	Properties   map[string]string `json:"properties"` // <property> (Sheetname, Sheetfile, ki_*, flags)

	DNP              bool `json:"dnp,omitempty"`
	ExcludeFromBoard bool `json:"excludeFromBoard,omitempty"`
	ExcludeFromBOM   bool `json:"excludeFromBom,omitempty"`
}

// SyncUnit is one unit of a symbol and its pin numbers.
type SyncUnit struct {
	Name string   `json:"name"`
	Pins []string `json:"pins"`
}

// SyncNet is one net.
type SyncNet struct {
	Code  string     `json:"code"`
	Name  string     `json:"name"`
	Class string     `json:"class,omitempty"`
	Nodes []SyncNode `json:"nodes"`
}

// SyncNode is one symbol pin on a net.
type SyncNode struct {
	Ref         string `json:"ref"`
	Pin         string `json:"pin"`
	PinFunction string `json:"pinfunction,omitempty"`
	PinType     string `json:"pintype,omitempty"`
}

// LibID is "lib:part" of the symbol.
func (c *SyncComp) LibID() string { return c.Lib + ":" + c.Part }

// Keys are the footprint paths that identify this symbol on a board: the
// sheet path tstamps + each unit UUID. Keys()[0] is the path pcbnew gives a
// new footprint.
func (c *SyncComp) Keys() []string {
	out := make([]string, 0, len(c.Tstamps))
	prefix := c.SheetTstamps
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	for _, t := range c.Tstamps {
		out = append(out, prefix+t)
	}
	return out
}

// Sheetname / Sheetfile are the footprint sheetname/sheetfile values.
func (c *SyncComp) Sheetname() string { return c.Properties["Sheetname"] }
func (c *SyncComp) Sheetfile() string { return c.Properties["Sheetfile"] }

// setFlags derives the boolean flags from the properties KiCad emits for
// them (a valueless <property name="dnp"/>).
func (c *SyncComp) setFlags() {
	flag := func(name string) bool {
		v, ok := c.Properties[name]
		if !ok {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "0", "no", "false":
			return false
		}
		return true
	}
	c.DNP = flag("dnp")
	c.ExcludeFromBoard = flag("exclude_from_board")
	c.ExcludeFromBOM = flag("exclude_from_bom")
}

// ParseSyncNetlist parses a kicad-cli netlist export; format is
// NetlistKicadXML, NetlistKicadSexpr or "" to detect from the first byte.
func ParseSyncNetlist(data []byte, format string) (*SyncNetlist, error) {
	if format == "" {
		if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '(' {
			format = NetlistKicadSexpr
		} else {
			format = NetlistKicadXML
		}
	}
	switch format {
	case NetlistKicadXML:
		return parseSyncNetlistXML(data)
	case NetlistKicadSexpr:
		return parseSyncNetlistSexpr(data)
	}
	return nil, fmt.Errorf("netlist format %q: want %s or %s", format, NetlistKicadXML, NetlistKicadSexpr)
}

type syncNetXML struct {
	XMLName xml.Name `xml:"export"`
	Design  struct {
		Source string `xml:"source"`
		Tool   string `xml:"tool"`
		Sheets []struct {
			Number  string `xml:"number,attr"`
			Name    string `xml:"name,attr"`
			Tstamps string `xml:"tstamps,attr"`
		} `xml:"sheet"`
	} `xml:"design"`
	Comps *struct {
		Comp []struct {
			Ref       string `xml:"ref,attr"`
			Value     string `xml:"value"`
			Footprint string `xml:"footprint"`
			Fields    []struct {
				Name  string `xml:"name,attr"`
				Value string `xml:",chardata"`
			} `xml:"fields>field"`
			Lib struct {
				Lib         string `xml:"lib,attr"`
				Part        string `xml:"part,attr"`
				Description string `xml:"description,attr"`
			} `xml:"libsource"`
			Props []struct {
				Name  string `xml:"name,attr"`
				Value string `xml:"value,attr"`
			} `xml:"property"`
			Sheet struct {
				Names   string `xml:"names,attr"`
				Tstamps string `xml:"tstamps,attr"`
			} `xml:"sheetpath"`
			Tstamps string `xml:"tstamps"`
			Units   []struct {
				Name string `xml:"name,attr"`
				Pins []struct {
					Num string `xml:"num,attr"`
				} `xml:"pins>pin"`
			} `xml:"units>unit"`
		} `xml:"comp"`
	} `xml:"components"`
	Nets *struct {
		Net []struct {
			Code  string `xml:"code,attr"`
			Name  string `xml:"name,attr"`
			Class string `xml:"class,attr"`
			Nodes []struct {
				Ref  string `xml:"ref,attr"`
				Pin  string `xml:"pin,attr"`
				Func string `xml:"pinfunction,attr"`
				Type string `xml:"pintype,attr"`
			} `xml:"node"`
		} `xml:"net"`
	} `xml:"nets"`
}

func parseSyncNetlistXML(data []byte) (*SyncNetlist, error) {
	var x syncNetXML
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("parse KiCad kicadxml netlist: %w", err)
	}
	if x.Comps == nil || x.Nets == nil {
		return nil, fmt.Errorf("parse KiCad kicadxml netlist: %w", errNetlistSections)
	}
	nl := &SyncNetlist{Source: x.Design.Source, Tool: x.Design.Tool,
		Sheets: []SyncSheet{}, Components: []SyncComp{}, Nets: []SyncNet{}}
	for _, s := range x.Design.Sheets {
		nl.Sheets = append(nl.Sheets, SyncSheet{s.Number, s.Name, s.Tstamps})
	}
	for _, c := range x.Comps.Comp {
		sc := SyncComp{Ref: c.Ref, Value: c.Value, Footprint: c.Footprint, Lib: c.Lib.Lib, Part: c.Lib.Part,
			Description: c.Lib.Description, SheetNames: c.Sheet.Names, SheetTstamps: c.Sheet.Tstamps,
			Tstamps: strings.Fields(c.Tstamps), Fields: map[string]string{}, Properties: map[string]string{}}
		for _, f := range c.Fields {
			sc.Fields[f.Name] = f.Value
		}
		for _, p := range c.Props {
			sc.Properties[p.Name] = p.Value
		}
		for _, u := range c.Units {
			su := SyncUnit{Name: u.Name, Pins: []string{}}
			for _, p := range u.Pins {
				su.Pins = append(su.Pins, p.Num)
			}
			sc.Units = append(sc.Units, su)
		}
		sc.setFlags()
		nl.Components = append(nl.Components, sc)
	}
	for _, n := range x.Nets.Net {
		sn := SyncNet{Code: n.Code, Name: n.Name, Class: n.Class, Nodes: []SyncNode{}}
		for _, d := range n.Nodes {
			sn.Nodes = append(sn.Nodes, SyncNode{d.Ref, d.Pin, d.Func, d.Type})
		}
		nl.Nets = append(nl.Nets, sn)
	}
	return nl, nil
}

// parseSyncNetlistSexpr reads the kicadsexpr form with the shared
// S-expression reader (fab.go). Its shape mirrors kicadxml: attributes and
// child elements both become (name value) lists, a field's value follows
// its (name …) list, and (tstamps …) holds one atom per unit.
func parseSyncNetlistSexpr(data []byte) (*SyncNetlist, error) {
	root, err := parseSexpr(data)
	if err != nil {
		return nil, fmt.Errorf("parse KiCad kicadsexpr netlist: %w", err)
	}
	if root.head() != "export" {
		return nil, fmt.Errorf("parse KiCad kicadsexpr netlist: top-level form is %q, want export", root.head())
	}
	if root.child("components") == nil || root.child("nets") == nil {
		return nil, fmt.Errorf("parse KiCad kicadsexpr netlist: %w", errNetlistSections)
	}
	val := func(n *sx, name string) string { return sxChild(n, name).arg(0) }
	nl := &SyncNetlist{Sheets: []SyncSheet{}, Components: []SyncComp{}, Nets: []SyncNet{}}
	if d := sxChild(root, "design"); d.head() != "" {
		nl.Source, nl.Tool = val(d, "source"), val(d, "tool")
		for _, s := range d.children("sheet") {
			nl.Sheets = append(nl.Sheets, SyncSheet{val(s, "number"), val(s, "name"), val(s, "tstamps")})
		}
	}
	for _, c := range sxChild(root, "components").children("comp") {
		ls, sp := sxChild(c, "libsource"), sxChild(c, "sheetpath")
		sc := SyncComp{Ref: val(c, "ref"), Value: val(c, "value"), Footprint: val(c, "footprint"),
			Lib: val(ls, "lib"), Part: val(ls, "part"), Description: val(ls, "description"),
			SheetNames: val(sp, "names"), SheetTstamps: val(sp, "tstamps"),
			Tstamps: []string{}, Fields: map[string]string{}, Properties: map[string]string{}}
		for _, f := range sxChild(c, "fields").children("field") {
			sc.Fields[val(f, "name")] = f.arg(1) // (field (name "N") "value")
		}
		for _, p := range c.children("property") {
			sc.Properties[val(p, "name")] = val(p, "value")
		}
		if ts := sxChild(c, "tstamps"); len(ts.Kids) > 1 {
			for _, k := range ts.Kids[1:] {
				if !k.isList {
					sc.Tstamps = append(sc.Tstamps, strings.Fields(k.Atom)...)
				}
			}
		}
		for _, u := range sxChild(c, "units").children("unit") {
			su := SyncUnit{Name: val(u, "name"), Pins: []string{}}
			for _, p := range sxChild(u, "pins").children("pin") {
				su.Pins = append(su.Pins, val(p, "num"))
			}
			sc.Units = append(sc.Units, su)
		}
		sc.setFlags()
		nl.Components = append(nl.Components, sc)
	}
	for _, n := range sxChild(root, "nets").children("net") {
		sn := SyncNet{Code: val(n, "code"), Name: val(n, "name"), Class: val(n, "class"), Nodes: []SyncNode{}}
		for _, d := range n.children("node") {
			sn.Nodes = append(sn.Nodes, SyncNode{val(d, "ref"), val(d, "pin"), val(d, "pinfunction"), val(d, "pintype")})
		}
		nl.Nets = append(nl.Nets, sn)
	}
	return nl, nil
}

// sxChild is a nil-safe child lookup: an absent child is an empty list, so
// chained .children / .arg calls on optional sections cannot panic.
func sxChild(n *sx, name string) *sx {
	if n != nil {
		if c := n.child(name); c != nil {
			return c
		}
	}
	return &sx{isList: true}
}

// OnBoard is the netlist without exclude_from_board symbols and their net
// nodes. The two export formats differ here (KiCad 10.0.6, ecc83 fixture):
// kicadxml lists such a symbol with an exclude_from_board property, while
// kicadsexpr — the netlist pcbnew reads — leaves the symbol and its nodes
// out. OnBoard of either format is the same. Nets left without nodes are
// kept (code and class unchanged).
func (nl *SyncNetlist) OnBoard() *SyncNetlist {
	off := map[string]bool{}
	out := &SyncNetlist{Source: nl.Source, Tool: nl.Tool, Sheets: append([]SyncSheet{}, nl.Sheets...),
		Components: []SyncComp{}, Nets: []SyncNet{}}
	for _, c := range nl.Components {
		if c.ExcludeFromBoard {
			off[c.Ref] = true
			continue
		}
		out.Components = append(out.Components, c)
	}
	for _, n := range nl.Nets {
		m := n
		m.Nodes = []SyncNode{}
		for _, d := range n.Nodes {
			if !off[d.Ref] {
				m.Nodes = append(m.Nodes, d)
			}
		}
		out.Nets = append(out.Nets, m)
	}
	return out
}

// Component returns the component with ref, or nil when there is none or
// the ref is ambiguous (duplicates are a Validate error).
func (nl *SyncNetlist) Component(ref string) *SyncComp {
	var hit *SyncComp
	for i := range nl.Components {
		if nl.Components[i].Ref == ref {
			if hit != nil {
				return nil
			}
			hit = &nl.Components[i]
		}
	}
	return hit
}

// Netlist validation codes (SyncNetlistIssue.Code).
const (
	ErrNetlistInvalid = "E_NETLIST_INVALID" // a component without ref / UUID / sheet path, a node without its component
	ErrDupRef         = "E_DUP_REF"         // two components share a reference
	ErrDupKey         = "E_DUP_KEY"         // two units/components share a footprint path key
	ErrUnannotated    = "E_UNANNOTATED"     // a reference still ends in '?'
)

var errNetlistSections = errors.New("missing <components> or <nets> section (not a complete kicad-cli netlist export)")

// kiidRe is a KiCad UUID as KiCad writes it (lowercase 8-4-4-4-12 hex;
// legacy timestamps are stored as "00000000-0000-0000-0000-<8 hex>", the
// same shape).
var kiidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// validSheetTstamps accepts "/" (root) or "/<uuid>/.../<uuid>/".
func validSheetTstamps(s string) bool {
	if s == "/" {
		return true
	}
	if len(s) < 2 || s[0] != '/' || s[len(s)-1] != '/' {
		return false
	}
	for _, seg := range strings.Split(s[1:len(s)-1], "/") {
		if !kiidRe.MatchString(seg) {
			return false
		}
	}
	return true
}

// SyncNetlistIssue is one reason a netlist cannot drive a board sync.
type SyncNetlistIssue struct {
	Code   string `json:"code"`
	Ref    string `json:"ref,omitempty"`
	Key    string `json:"key,omitempty"`
	Detail string `json:"detail"`
}

// Validate reports what makes the netlist unusable for matching
// footprints: components without a reference, a malformed sheet path or
// symbol UUID, unannotated or duplicate references, duplicate footprint
// path keys; nets with an empty or duplicate code or an empty name, nodes
// without a pin or naming no component, and a pin on two different nets (kicadsexpr drops exclude_from_board
// symbols together with their nodes, so that never fires on a real export).
// Empty component and net lists are valid. Issues are in netlist order.
func (nl *SyncNetlist) Validate() []SyncNetlistIssue {
	var out []SyncNetlistIssue
	add := func(code, ref, key, format string, a ...any) {
		out = append(out, SyncNetlistIssue{Code: code, Ref: ref, Key: key, Detail: fmt.Sprintf(format, a...)})
	}
	refs := map[string]int{}
	keys := map[string]string{}
	for i, c := range nl.Components {
		if c.Ref == "" {
			add(ErrNetlistInvalid, "", "", "component #%d (%s) has no reference", i+1, c.LibID())
			continue
		}
		if refs[c.Ref]++; refs[c.Ref] == 2 {
			add(ErrDupRef, c.Ref, "", "reference %s is used by more than one symbol", c.Ref)
		}
		if strings.HasSuffix(c.Ref, "?") {
			add(ErrUnannotated, c.Ref, "", "%s is not annotated", c.Ref)
		}
		if !validSheetTstamps(c.SheetTstamps) {
			add(ErrNetlistInvalid, c.Ref, "", "%s has sheet path tstamps %q, want \"/\" or \"/<uuid>/.../\"", c.Ref, c.SheetTstamps)
			continue
		}
		if len(c.Tstamps) == 0 {
			add(ErrNetlistInvalid, c.Ref, "", "%s has no symbol UUID (tstamps)", c.Ref)
			continue
		}
		badUUID := false
		for _, u := range c.Tstamps {
			if !kiidRe.MatchString(u) {
				add(ErrNetlistInvalid, c.Ref, "", "%s has symbol UUID %q, not a KiCad UUID", c.Ref, u)
				badUUID = true
			}
		}
		if badUUID {
			continue
		}
		for _, k := range c.Keys() {
			if prev, dup := keys[k]; dup {
				add(ErrDupKey, c.Ref, k, "path %s belongs to both %s and %s", k, prev, c.Ref)
				continue
			}
			keys[k] = c.Ref
		}
	}
	codes := map[string]string{}
	type netID struct{ code, name string }
	pinNet := map[[2]string]netID{} // (ref, pin) -> the net it is on
	for i, n := range nl.Nets {
		switch prev, dup := codes[n.Code]; {
		case n.Code == "":
			add(ErrNetlistInvalid, "", "", "net #%d (%q) has no code", i+1, n.Name)
		case dup:
			add(ErrNetlistInvalid, "", "", "net code %s is used by both %q and %q", n.Code, prev, n.Name)
		default:
			codes[n.Code] = n.Name
		}
		if n.Name == "" {
			add(ErrNetlistInvalid, "", "", "net code %s has no name", n.Code)
		}
		for _, d := range n.Nodes {
			if d.Pin == "" {
				add(ErrNetlistInvalid, d.Ref, "", "net %q has a node on %s without a pin", n.Name, d.Ref)
				continue
			}
			if refs[d.Ref] == 0 {
				add(ErrNetlistInvalid, d.Ref, "", "net %q has a node on %s.%s, which is not a component", n.Name, d.Ref, d.Pin)
			}
			// Net identity is the net code, not the name: two nets sharing a
			// name are still two nets; two entries sharing a code are the
			// duplicate-code issue above, not a pin conflict. A net without
			// a code (already reported) is identified by its position.
			id := netID{n.Code, n.Name}
			if id.code == "" {
				id.code = fmt.Sprintf("#%d", i+1)
			}
			k := [2]string{d.Ref, d.Pin}
			if prev, seen := pinNet[k]; seen && prev.code != id.code {
				add(ErrNetlistInvalid, d.Ref, "", "pin %s.%s is on both net %q (code %s) and net %q (code %s)",
					d.Ref, d.Pin, prev.name, prev.code, n.Name, id.code)
			} else if !seen {
				pinNet[k] = id
			}
		}
	}
	return out
}
