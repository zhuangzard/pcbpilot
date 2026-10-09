package kicad

// schnet.go — schematic connectivity from KiCad: `kicad-cli sch export
// netlist --format kicadxml` → pcbpilot's schematic connectivity IR
// (internal/connectivity, schemaVersion 1.4 — the shape `sch connectivity`
// writes and `intent derive --connectivity` / pad-net-diff read).

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

// KicadCLI locates kicad-cli ($KICAD_CLI, PATH, the macOS bundle).
func KicadCLI() (string, error) {
	if p := os.Getenv("KICAD_CLI"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("kicad-cli"); err == nil {
		return p, nil
	}
	if runtime.GOOS == "darwin" {
		p := "/Applications/KiCad/KiCad.app/Contents/MacOS/kicad-cli"
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("kicad-cli not found (install KiCad 10 or set KICAD_CLI)")
}

// SchNetlist is the parsed kicadxml netlist.
type SchNetlist struct {
	Components []SchNetComp
	Nets       []SchNet
}

// SchNetComp is one component (all units merged by KiCad).
type SchNetComp struct {
	Ref, Value, Footprint, Lib, Part, Sheet string
	Fields                                  map[string]string
	Pins                                    []SchNetPin // library pins (libparts)
}

// SchNetPin is a library pin.
type SchNetPin struct{ Number, Name, Type string }

// SchNet is a net and its nodes.
type SchNet struct {
	Code, Name string
	Nodes      []SchNetNode
}

// SchNetNode is a component pin on a net.
type SchNetNode struct{ Ref, Pin, Function, Type string }

type schNetXML struct {
	Comps []struct {
		Ref       string `xml:"ref,attr"`
		Value     string `xml:"value"`
		Footprint string `xml:"footprint"`
		Fields    []struct {
			Name  string `xml:"name,attr"`
			Value string `xml:",chardata"`
		} `xml:"fields>field"`
		Lib struct {
			Lib  string `xml:"lib,attr"`
			Part string `xml:"part,attr"`
		} `xml:"libsource"`
		Sheet struct {
			Names string `xml:"names,attr"`
		} `xml:"sheetpath"`
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
			Ref  string `xml:"ref,attr"`
			Pin  string `xml:"pin,attr"`
			Func string `xml:"pinfunction,attr"`
			Type string `xml:"pintype,attr"`
		} `xml:"node"`
	} `xml:"nets>net"`
}

// ParseSchNetlist reads kicadxml.
func ParseSchNetlist(data []byte) (*SchNetlist, error) {
	var x schNetXML
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("parse KiCad netlist: %w", err)
	}
	lp := map[string][]SchNetPin{}
	for _, l := range x.LibParts {
		for _, p := range l.Pins {
			lp[l.Lib+":"+l.Part] = append(lp[l.Lib+":"+l.Part], SchNetPin{p.Num, p.Name, p.Type})
		}
	}
	nl := &SchNetlist{}
	for _, c := range x.Comps {
		sc := SchNetComp{Ref: c.Ref, Value: c.Value, Footprint: c.Footprint, Lib: c.Lib.Lib, Part: c.Lib.Part,
			Sheet: c.Sheet.Names, Fields: map[string]string{}, Pins: lp[c.Lib.Lib+":"+c.Lib.Part]}
		for _, f := range c.Fields {
			sc.Fields[f.Name] = strings.TrimSpace(f.Value)
		}
		nl.Components = append(nl.Components, sc)
	}
	for _, n := range x.Nets {
		sn := SchNet{Code: n.Code, Name: n.Name}
		for _, d := range n.Nodes {
			sn.Nodes = append(sn.Nodes, SchNetNode{d.Ref, d.Pin, d.Func, d.Type})
		}
		nl.Nets = append(nl.Nets, sn)
	}
	return nl, nil
}

// ExportSchNetlist runs kicad-cli on the root sheet and parses the result.
func ExportSchNetlist(sch string) (*SchNetlist, error) {
	cli, err := KicadCLI()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "pcbpilot-net-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "net.xml")
	var buf bytes.Buffer
	cmd := exec.Command(cli, "sch", "export", "netlist", "--format", "kicadxml", "-o", out, sch)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kicad-cli sch export netlist: %v: %s", err, strings.TrimSpace(buf.String()))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("kicad-cli wrote no netlist: %s", strings.TrimSpace(buf.String()))
	}
	return ParseSchNetlist(data)
}

// netNames maps each KiCad net name to the pcbpilot name: sheet-local
// names "/Sheet/NAME" lose the path when NAME is unique in the design;
// KiCad's unnamed nets ("Net-(R1-Pad2)", "unconnected-(U1-NC-Pad5)") are
// kept as they are.
func (nl *SchNetlist) netNames() map[string]string {
	base := func(n string) string {
		if strings.HasPrefix(n, "/") {
			return n[strings.LastIndex(n, "/")+1:]
		}
		return n
	}
	count := map[string]int{}
	for _, n := range nl.Nets {
		count[base(n.Name)]++
	}
	out := map[string]string{}
	for _, n := range nl.Nets {
		b := base(n.Name)
		if count[b] == 1 && b != "" {
			out[n.Name] = b
		} else {
			out[n.Name] = n.Name
		}
	}
	return out
}

// lcscKeys are field names that hold the LCSC C-number.
var lcscKeys = []string{"LCSC", "LCSC Part", "LCSC Part #", "JLCPCB Part #", "Supplier Part"}

// ToConnectivityDoc converts the netlist into the schematic connectivity
// IR. Power symbols (#PWR, #FLG) are not components. Pins of unconnected
// nets ("unconnected-(…)") become connectionState "unconnected"; pins on a
// no-connect flag are reported by KiCad as such single-node nets too.
func (nl *SchNetlist) ToConnectivityDoc(projectID string) (*connectivity.Document, error) {
	doc := &connectivity.Document{SchemaVersion: "1.4", ProjectID: projectID, DocumentID: "kicad-sch"}
	names := nl.netNames()
	idx := map[string]int{}
	pinIdx := map[string]map[string]int{}
	for _, c := range nl.Components {
		if strings.HasPrefix(c.Ref, "#") {
			continue
		}
		dev := connectivity.Device{UUID: "kicad:" + c.Lib + ":" + c.Part, Name: c.Value}
		for _, k := range lcscKeys {
			if v := c.Fields[k]; v != "" && v != "~" {
				dev.SupplierID = v
				break
			}
		}
		comp := connectivity.Component{ID: "cmp-" + c.Ref, Ref: c.Ref, Device: dev, Footprint: c.Footprint, PageName: c.Sheet}
		pinIdx[c.Ref] = map[string]int{}
		for _, p := range c.Pins {
			if _, dup := pinIdx[c.Ref][p.Number]; dup {
				continue
			}
			pinIdx[c.Ref][p.Number] = len(comp.Pins)
			comp.Pins = append(comp.Pins, connectivity.Pin{Number: p.Number, Name: p.Name, Type: p.Type})
		}
		idx[c.Ref] = len(doc.Components)
		doc.Components = append(doc.Components, comp)
	}
	pin := func(ref, num, name string) *connectivity.Pin {
		ci, ok := idx[ref]
		if !ok {
			return nil
		}
		c := &doc.Components[ci]
		if i, ok := pinIdx[ref][num]; ok {
			return &c.Pins[i]
		}
		pinIdx[ref][num] = len(c.Pins)
		c.Pins = append(c.Pins, connectivity.Pin{Number: num, Name: name})
		return &c.Pins[len(c.Pins)-1]
	}
	for _, n := range nl.Nets {
		var real []SchNetNode
		for _, d := range n.Nodes {
			if _, ok := idx[d.Ref]; ok {
				real = append(real, d)
			}
		}
		if len(real) == 0 {
			continue
		}
		if strings.HasPrefix(n.Name, "unconnected-(") && len(real) == 1 {
			p := pin(real[0].Ref, real[0].Pin, real[0].Function)
			if strings.Contains(real[0].Type, "no_connect") {
				p.NoConnected = true
			} else {
				p.ConnectionState = "unconnected"
			}
			continue
		}
		id := "net-" + n.Code
		role := "signal"
		switch nm := names[n.Name]; {
		case IsGroundNet(nm):
			role = "ground"
		case isPowerName(nm):
			role = "power"
		}
		scope := "local"
		if !strings.HasPrefix(n.Name, "/") && !strings.HasPrefix(n.Name, "Net-(") {
			scope = "global"
		}
		doc.Nets = append(doc.Nets, connectivity.Net{ID: id, Name: names[n.Name], Scope: scope, Role: role})
		for _, d := range real {
			p := pin(d.Ref, d.Pin, d.Function)
			if strings.Contains(d.Type, "no_connect") {
				p.NoConnected = true
			}
			doc.Connections = append(doc.Connections, connectivity.Connection{ComponentID: "cmp-" + d.Ref, PinNumber: d.Pin, NetID: id, Kind: "netlist"})
		}
	}
	if err := doc.Validate(); err != nil {
		return doc, fmt.Errorf("connectivity from the KiCad netlist: %w", err)
	}
	return doc, nil
}

func isPowerName(n string) bool {
	u := strings.ToUpper(n)
	return strings.HasPrefix(u, "+") || strings.HasPrefix(u, "VCC") || strings.HasPrefix(u, "VDD") || strings.HasPrefix(u, "VBAT") || strings.HasPrefix(u, "VIN")
}

// PinNets maps "REF.PIN" → net name (KiCad name), components without '#'.
func (nl *SchNetlist) PinNets() map[string]string {
	out := map[string]string{}
	for _, n := range nl.Nets {
		for _, d := range n.Nodes {
			if !strings.HasPrefix(d.Ref, "#") {
				out[d.Ref+"."+d.Pin] = n.Name
			}
		}
	}
	return out
}

// NetCompare is the net-by-net, pin-by-pin comparison of two pin→net maps.
type NetCompare struct {
	NetsA       int      `json:"netsA"`
	NetsB       int      `json:"netsB"`
	PinsA       int      `json:"pinsA"`
	PinsB       int      `json:"pinsB"`
	NetsEqual   int      `json:"netsEqual"`  // nets whose pin set is identical on both sides
	NamesEqual  int      `json:"namesEqual"` // … and whose names agree (after normalisation)
	Mismatched  []string `json:"mismatched"` // nets of A whose pin set differs on B
	OnlyA       []string `json:"onlyA"`      // pins present on A only
	OnlyB       []string `json:"onlyB"`      // pins present on B only
	RenamedNets []string `json:"renamed"`    // same pins, different name: "A → B"
	Equal       bool     `json:"equal"`      // identical partitions
}

// ComparePinNets compares two pin→net maps as partitions. Single-pin nets
// are included. norm normalises names before the name check (nil: exact).
func ComparePinNets(a, b map[string]string, norm func(string) string) NetCompare {
	if norm == nil {
		norm = func(s string) string { return s }
	}
	group := func(m map[string]string) map[string][]string {
		g := map[string][]string{}
		for p, n := range m {
			g[n] = append(g[n], p)
		}
		for n := range g {
			sort.Strings(g[n])
		}
		return g
	}
	ga, gb := group(a), group(b)
	r := NetCompare{NetsA: len(ga), NetsB: len(gb), PinsA: len(a), PinsB: len(b)}
	for p := range a {
		if _, ok := b[p]; !ok {
			r.OnlyA = append(r.OnlyA, p)
		}
	}
	for p := range b {
		if _, ok := a[p]; !ok {
			r.OnlyB = append(r.OnlyB, p)
		}
	}
	for n, pins := range ga {
		nb, ok := b[pins[0]]
		if !ok || strings.Join(gb[nb], " ") != strings.Join(pins, " ") {
			r.Mismatched = append(r.Mismatched, n)
			continue
		}
		r.NetsEqual++
		if norm(n) == norm(nb) {
			r.NamesEqual++
		} else {
			r.RenamedNets = append(r.RenamedNets, n+" → "+nb)
		}
	}
	sort.Strings(r.OnlyA)
	sort.Strings(r.OnlyB)
	sort.Strings(r.Mismatched)
	sort.Strings(r.RenamedNets)
	r.Equal = len(r.Mismatched) == 0 && len(r.OnlyA) == 0 && len(r.OnlyB) == 0 && r.NetsA == r.NetsB
	return r
}
