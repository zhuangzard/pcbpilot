package intent

import (
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// BoardSource marks a design rebuilt from PCB pads (`intent derive --board`).
const BoardSource = "pcb pads"

// DesignFromBoard rebuilds a netlist from a measured PCB (`pcb dump` →
// pcbauto.FromSnapshot) for boards whose schematic is not available: every
// footprint becomes a part, every netted pad a pin. Pin names and part
// values are unknown — the footprint device name is the only attribute, so
// the power simulation falls back to generic models and the net plan leans
// on name heuristics. Derive marks such a document (finding
// "netlist-from-board", currentSource "heuristic").
func DesignFromBoard(b *pcbauto.Board) *powersim.Design {
	d := &powersim.Design{NetRole: map[string]string{}, Sources: []string{BoardSource}}
	for _, p := range b.Parts {
		part := &powersim.Part{Ref: p.Ref, DeviceName: p.Device, MPN: p.Device, Value: boardValue(p.Ref, p.Device)}
		seen := map[string]bool{}
		for _, pd := range p.Pads {
			if pd.Net == "" || seen[pd.Number+"\x00"+pd.Net] {
				continue
			}
			seen[pd.Number+"\x00"+pd.Net] = true
			part.Pins = append(part.Pins, powersim.Pin{Number: pd.Number, Name: pd.Number, Net: pd.Net})
		}
		if len(part.Pins) == 0 {
			continue
		}
		d.Parts = append(d.Parts, part)
	}
	for _, n := range b.Nets() {
		if reGroundName.MatchString(strings.TrimSpace(n.Name)) {
			d.NetRole[n.Name] = "ground"
		}
	}
	sort.Slice(d.Parts, func(i, j int) bool { return refLess(d.Parts[i].Ref, d.Parts[j].Ref) })
	return d
}

// reBoardValue recognises a passive value spelled as the device name
// ("100nF", "10K", "4.7uF", "0R"). Footprint-only names give "".
var reBoardValue = regexp.MustCompile(`(?i)^[0-9]+(\.[0-9]+)?\s*(p|n|u|µ|m|k|M|R)?(F|H|Ω|OHM|R)?[0-9]*$`)

// boardValue keeps a device name as the part value only when it reads as a
// passive value on a passive ref (C/R/L/FB); anything else stays unknown.
func boardValue(ref, dev string) string {
	r := strings.ToUpper(ref)
	if !(strings.HasPrefix(r, "C") || strings.HasPrefix(r, "R") || strings.HasPrefix(r, "L") || strings.HasPrefix(r, "FB")) {
		return ""
	}
	dev = strings.TrimSpace(dev)
	if dev != "" && reBoardValue.MatchString(dev) {
		return dev
	}
	return ""
}

// fromBoard reports whether the design was rebuilt from PCB pads.
func fromBoard(d *powersim.Design) bool {
	return d != nil && len(d.Sources) > 0 && d.Sources[0] == BoardSource
}
