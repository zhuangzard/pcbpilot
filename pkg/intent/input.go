package intent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// Input is everything Derive needs. Design and/or Sim must be set: with a
// design and no sim the power simulation runs in-process with Libs; with only
// a sim the design is reconstructed from its per-net pin lists (no part
// values — block classification then leans on the bound model ids).
type Input struct {
	Design     *powersim.Design
	Sim        *powersim.Output
	Libs       powersim.Libraries
	SimOptions powersim.Options
	Spec       *Spec
	Sources    Sources
}

// ParseSimOutput decodes a `pcbpilot sim power` document (schemaVersion 1).
func ParseSimOutput(b []byte) (*powersim.Output, error) {
	var out powersim.Output
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("sim: %w", err)
	}
	if out.SchemaVersion != 1 {
		return nil, fmt.Errorf("sim: schemaVersion %d unsupported (want 1)", out.SchemaVersion)
	}
	if len(out.Results) == 0 {
		return nil, fmt.Errorf("sim: no results")
	}
	return &out, nil
}

// DesignFromSim rebuilds a design (refs, pins, nets) from a sim document's
// per-net pin lists. Part values are unknown; the bound model id is used as
// the device name so classification still sees "esp32-s3-wroom-1" etc.
func DesignFromSim(out *powersim.Output) *powersim.Design {
	d := &powersim.Design{NetRole: map[string]string{}, Sources: []string{"sim pin lists"}}
	byRef := map[string]*powersim.Part{}
	model := map[string]string{}
	for _, m := range out.Models {
		model[m.Ref] = m.ModelID
	}
	res := pickResult(out, "worst")
	if res == nil {
		res = &out.Results[len(out.Results)-1]
	}
	nets := make([]string, 0, len(res.Nets))
	for n := range res.Nets {
		nets = append(nets, n)
	}
	sort.Strings(nets)
	for _, net := range nets {
		nr := res.Nets[net]
		if nr.Role == "ground" {
			d.NetRole[net] = "ground"
		}
		for _, pin := range nr.Pins {
			p := byRef[pin.Ref]
			if p == nil {
				p = &powersim.Part{Ref: pin.Ref}
				if id := model[pin.Ref]; id != "" && !strings.HasPrefix(id, "generic-") {
					p.DeviceName = id
					p.MPN = id
				}
				byRef[pin.Ref] = p
				d.Parts = append(d.Parts, p)
			}
			p.Pins = append(p.Pins, powersim.Pin{Number: pin.Pin, Name: pin.Name, Net: net})
		}
	}
	sort.Slice(d.Parts, func(i, j int) bool { return refLess(d.Parts[i].Ref, d.Parts[j].Ref) })
	return d
}

func pickResult(out *powersim.Output, name string) *powersim.Result {
	for i := range out.Results {
		if out.Results[i].Scenario == name {
			return &out.Results[i]
		}
	}
	return nil
}

// buildBoard makes a geometry-free pcbauto board (pads = pins) so the circuit
// understanding and electrical analysis of pcbauto run on the schematic.
func buildBoard(d *powersim.Design, rules pcbauto.Rules, layers int) (*pcbauto.Board, error) {
	b := &pcbauto.Board{Rules: rules, CopperLayers: layers}
	for i, p := range d.Parts {
		dev := firstNonEmpty(p.MPN, p.DeviceName, p.Value)
		part := &pcbauto.Part{Ref: p.Ref, Device: dev, Pos: pcbauto.Point{X: float64(i%20) * 2000, Y: float64(i/20) * 2000}, Side: pcbauto.LayerTop}
		for j, pin := range p.Pins {
			c := pcbauto.Point{X: part.Pos.X + float64(j%10)*50, Y: part.Pos.Y + float64(j/10)*50}
			part.Pads = append(part.Pads, &pcbauto.Pad{Part: p.Ref, Number: pin.Number, Net: pin.Net, Layer: pcbauto.LayerTop,
				Box: pcbauto.OrientedBox{C: c, W: 20, H: 20}})
		}
		b.Parts = append(b.Parts, part)
	}
	if err := b.Index(); err != nil {
		return nil, err
	}
	return b, nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

// refLess orders "R2" before "R10".
func refLess(a, b string) bool {
	pa, na := splitRef(a)
	pb, nb := splitRef(b)
	if pa != pb {
		return pa < pb
	}
	if len(na) != len(nb) {
		return len(na) < len(nb)
	}
	return na < nb || na == nb && a < b
}

func splitRef(s string) (string, string) {
	i := 0
	for i < len(s) && (s[i] < '0' || s[i] > '9') {
		i++
	}
	j := i
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	return s[:i], s[i:j]
}

func sortRefs(xs []string) []string {
	sort.Slice(xs, func(i, j int) bool { return refLess(xs[i], xs[j]) })
	return xs
}
