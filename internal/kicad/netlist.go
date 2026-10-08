package kicad

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

// Netlist is a KiCad schematic netlist (`kicad-cli sch export netlist
// --format kicadxml`).
type Netlist struct {
	Components []NetlistComponent
	Nets       []NetlistNet
}

// NetlistComponent is one schematic symbol instance.
type NetlistComponent struct {
	Ref, Value, Footprint, LCSC, MPN, Description, UUID, Lib, Part string
	// Pins of the library symbol (number → name/type), from <libparts>.
	Pins []NetlistPin
}

// NetlistPin is a library pin.
type NetlistPin struct{ Number, Name, Type string }

// NetlistNet is one net with its nodes.
type NetlistNet struct {
	Code, Name string
	Nodes      []NetlistNode
}

// NetlistNode is a component pin on a net.
type NetlistNode struct{ Ref, Pin, PinType, PinFunction string }

type xmlNetlist struct {
	Components []struct {
		Ref       string `xml:"ref,attr"`
		Value     string `xml:"value"`
		Footprint string `xml:"footprint"`
		Desc      string `xml:"description"`
		Fields    []struct {
			Name  string `xml:"name,attr"`
			Value string `xml:",chardata"`
		} `xml:"fields>field"`
		Props []struct {
			Name  string `xml:"name,attr"`
			Value string `xml:"value,attr"`
		} `xml:"property"`
		Lib struct {
			Lib  string `xml:"lib,attr"`
			Part string `xml:"part,attr"`
		} `xml:"libsource"`
		TStamps string `xml:"tstamps"`
	} `xml:"components>comp"`
	LibParts []struct {
		Lib  string `xml:"lib,attr"`
		Part string `xml:"part,attr"`
		Pins []struct {
			Num  string `xml:"num,attr"`
			Name string `xml:"name,attr"`
			Type string `xml:"type,attr"`
		} `xml:"pins>pin"`
	} `xml:"libparts>libpart"`
	Nets []struct {
		Code  string `xml:"code,attr"`
		Name  string `xml:"name,attr"`
		Nodes []struct {
			Ref      string `xml:"ref,attr"`
			Pin      string `xml:"pin,attr"`
			PinType  string `xml:"pintype,attr"`
			Function string `xml:"pinfunction,attr"`
		} `xml:"node"`
	} `xml:"nets>net"`
}

var lcscFieldNames = map[string]bool{"lcsc": true, "lcsc part": true, "lcsc part #": true, "jlcpcb part #": true, "jlcpcb part": true, "lcsc#": true}
var mpnFieldNames = map[string]bool{"mpn": true, "manufacturer part": true, "manufacturer part number": true, "mfr part": true, "mfr. part #": true}

// ParseNetlistXML reads a kicadxml netlist.
func ParseNetlistXML(data []byte) (*Netlist, error) {
	var x xmlNetlist
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("parse KiCad netlist: %w", err)
	}
	pins := map[string][]NetlistPin{}
	for _, lp := range x.LibParts {
		for _, p := range lp.Pins {
			pins[lp.Lib+":"+lp.Part] = append(pins[lp.Lib+":"+lp.Part], NetlistPin{Number: p.Num, Name: p.Name, Type: p.Type})
		}
	}
	nl := &Netlist{}
	for _, c := range x.Components {
		nc := NetlistComponent{Ref: c.Ref, Value: c.Value, Footprint: c.Footprint, Description: c.Desc,
			UUID: strings.TrimSpace(c.TStamps), Lib: c.Lib.Lib, Part: c.Lib.Part, Pins: pins[c.Lib.Lib+":"+c.Lib.Part]}
		field := func(name, v string) {
			n := strings.ToLower(strings.TrimSpace(name))
			v = strings.TrimSpace(v)
			if v == "" || v == "~" {
				return
			}
			if lcscFieldNames[n] && nc.LCSC == "" {
				nc.LCSC = v
			}
			if mpnFieldNames[n] && nc.MPN == "" {
				nc.MPN = v
			}
		}
		for _, f := range c.Fields {
			field(f.Name, f.Value)
		}
		for _, p := range c.Props {
			field(p.Name, p.Value)
		}
		nl.Components = append(nl.Components, nc)
	}
	for _, n := range x.Nets {
		nn := NetlistNet{Code: n.Code, Name: n.Name}
		for _, d := range n.Nodes {
			nn.Nodes = append(nn.Nodes, NetlistNode{Ref: d.Ref, Pin: d.Pin, PinType: d.PinType, PinFunction: d.Function})
		}
		nl.Nets = append(nl.Nets, nn)
	}
	return nl, nil
}

// ExportNetlist runs `kicad-cli sch export netlist --format kicadxml`.
func (t *Tools) ExportNetlist(sch, out string) (*Netlist, error) {
	ctx, cancel := context.WithTimeout(context.Background(), BridgeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.CLI, "sch", "export", "netlist", "--format", "kicadxml", "-o", out, sch)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kicad-cli sch export netlist: %v: %s", err, strings.TrimSpace(buf.String()))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	return ParseNetlistXML(data)
}

// unconnectedNet is KiCad's name for the net of a lone unconnected pin.
func unconnectedNet(name string) bool { return strings.HasPrefix(name, "unconnected-(") }

// ToConnectivity converts the netlist into pcbpilot's schematic
// connectivity IR (schemaVersion 1.4, the `sch connectivity` shape that
// intent derive, sim power and pad-net-diff read). Component ids are the
// symbol uuids (the reference when absent); a pin alone on KiCad's
// "unconnected-(…)" net carries no connection — NoConnected when the pin
// has a no-connect flag, else connectionState "unconnected".
func (nl *Netlist) ToConnectivity(projectID string) (*connectivity.Document, error) {
	doc := &connectivity.Document{SchemaVersion: "1.4", ProjectID: projectID, DocumentID: "kicad-netlist"}
	idOf := map[string]string{}
	pinsOf := map[string]map[string]*connectivity.Pin{}
	var order []string
	for _, c := range nl.Components {
		id := c.UUID
		if id == "" {
			id = c.Ref
		}
		if _, dup := idOf[c.Ref]; dup {
			continue // multi-unit symbols repeat the reference
		}
		idOf[c.Ref] = id
		comp := connectivity.Component{ID: id, Ref: c.Ref, Footprint: c.Footprint,
			Device: connectivity.Device{UUID: "kicad:" + c.Lib + ":" + c.Part, Name: c.Value, SupplierID: c.LCSC}}
		doc.Components = append(doc.Components, comp)
		pinsOf[c.Ref] = map[string]*connectivity.Pin{}
		for _, p := range c.Pins {
			pinsOf[c.Ref][p.Number] = &connectivity.Pin{Number: p.Number, Name: p.Name, Type: p.Type}
		}
		order = append(order, c.Ref)
	}
	pin := func(ref, num string) *connectivity.Pin {
		m := pinsOf[ref]
		if m == nil {
			return nil
		}
		if m[num] == nil {
			m[num] = &connectivity.Pin{Number: num}
		}
		return m[num]
	}
	for _, n := range nl.Nets {
		lone := unconnectedNet(n.Name)
		if !lone {
			doc.Nets = append(doc.Nets, connectivity.Net{ID: "kicad-net-" + n.Code, Name: n.Name})
		}
		for _, d := range n.Nodes {
			p := pin(d.Ref, d.Pin)
			if p == nil {
				continue
			}
			if d.PinFunction != "" && p.Name == "" {
				p.Name = d.PinFunction
			}
			if lone {
				if strings.Contains(d.PinType, "no_connect") {
					p.NoConnected = true
				} else {
					p.ConnectionState = "unconnected"
				}
				continue
			}
			doc.Connections = append(doc.Connections, connectivity.Connection{ComponentID: idOf[d.Ref], PinNumber: d.Pin, NetID: "kicad-net-" + n.Code})
		}
	}
	for i, ref := range order {
		var nums []string
		for n := range pinsOf[ref] {
			nums = append(nums, n)
		}
		sort.Strings(nums)
		for _, n := range nums {
			doc.Components[i].Pins = append(doc.Components[i].Pins, *pinsOf[ref][n])
		}
	}
	if err := doc.Validate(); err != nil {
		return doc, fmt.Errorf("connectivity from the KiCad netlist: %w", err)
	}
	return doc, nil
}

// Values returns {"parts":{ref:{value,mpn,lcsc,description}}} for intent
// derive --values / sim power.
func (nl *Netlist) Values() map[string]any {
	parts := map[string]any{}
	for _, c := range nl.Components {
		v := map[string]string{"value": c.Value}
		if c.MPN != "" {
			v["mpn"] = c.MPN
		}
		if c.LCSC != "" {
			v["lcsc"] = c.LCSC
		}
		if c.Description != "" {
			v["description"] = c.Description
		}
		parts[c.Ref] = v
	}
	return map[string]any{"parts": parts}
}

// PadNetDiff compares the board's pads (designator, pad number → net) with
// the schematic netlist. Pads without a net on either side are skipped
// (mounting pads, unconnected pins).
type PadNetDiff struct {
	Passed bool     `json:"passed"`
	Diffs  []string `json:"diffs"`
	Pins   int      `json:"pins"`
}

// DiffPads compares board pads [{ref, pad, net}] with the netlist.
func (nl *Netlist) DiffPads(boardPads map[[2]string]string) PadNetDiff {
	sch := map[[2]string]string{}
	for _, n := range nl.Nets {
		if unconnectedNet(n.Name) {
			continue
		}
		for _, d := range n.Nodes {
			sch[[2]string{d.Ref, d.Pin}] = n.Name
		}
	}
	var diffs []string
	for k, want := range sch {
		got, ok := boardPads[k]
		switch {
		case !ok:
			diffs = append(diffs, fmt.Sprintf("%s.%s: on net %s in the schematic, no such pad on the board", k[0], k[1], want))
		case got != want:
			diffs = append(diffs, fmt.Sprintf("%s.%s: board net %q, schematic net %q", k[0], k[1], got, want))
		}
	}
	for k, got := range boardPads {
		if _, ok := sch[k]; !ok && got != "" && !unconnectedNet(got) {
			diffs = append(diffs, fmt.Sprintf("%s.%s: board net %q, not connected in the schematic", k[0], k[1], got))
		}
	}
	sort.Strings(diffs)
	return PadNetDiff{Passed: len(diffs) == 0, Diffs: diffs, Pins: len(sch)}
}
