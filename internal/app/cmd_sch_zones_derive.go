package app

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// zonesDerivePart is the design intent for one part: which functional zone it
// belongs to and what each pin connects to. Pins are named by pin number or by
// the symbol's pin name ("terminal"); a symbol that names pins only by number
// needs an explicit "pin" with its source (datasheet), never a guess.
type zonesDerivePart struct {
	ID               string            `json:"id"`
	Designator       string            `json:"designator"`
	FunctionalZone   string            `json:"functionalZone"`
	Pins             []zonesDerivePin  `json:"pins"`
	AllowedRotations []float64         `json:"allowedRotations,omitempty"`
	Notes            map[string]string `json:"notes,omitempty"`
}

type zonesDerivePin struct {
	Terminal string `json:"terminal,omitempty"`
	Pin      string `json:"pin,omitempty"`
	Source   string `json:"source,omitempty"` // required with an explicit pin override
	Net      string `json:"net,omitempty"`
	State    string `json:"state,omitempty"` // "nc"
}

type zonesDeriveOptions struct {
	Power, Ground map[string]bool
	ArrayMin      int
	Spacing       float64
	MaxCandidates int
}

type zonesDeriveDecision struct {
	Zone   string   `json:"zone"`
	Rule   string   `json:"rule"`
	Parts  []string `json:"parts"`
	Detail string   `json:"detail,omitempty"`
}

type zonesDeriveReport struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Parts         int                   `json:"parts"`
	Zones         int                   `json:"zones"`
	Decisions     []zonesDeriveDecision `json:"decisions"`
	OpenPins      map[string][]string   `json:"openPins,omitempty"`
	Policies      map[string]int        `json:"policies"`
}

func newSchZonesDeriveCmd(stdout io.Writer) *cobra.Command {
	var partsPath, listPath, desigPath, rotPath, out, report, power, ground string
	var arrayMin, maxCandidates int
	var spacing float64
	c := &cobra.Command{Use: "zones-derive", Short: "Derive a layout-plan --zones source from design intent and measured symbols (offline)", Long: `Builds the zones source for 'sch layout-plan --zones' from:
  --parts        design intent: [{id, designator, functionalZone, pins:[{terminal|pin, net|state:"nc", source?}], allowedRotations?}]
  --list         'sch list --include-pins --include-bbox' of a scratch page holding every
                 part at rotation 0 with its final designator
  --designators  'sch designator-geometry' of the same page
  --rotations    optional measure-designator-rotations.py output (measured poses)

Pin mapping: terminal == pin number (a "/" is dropped: "B1/A12" -> "B1A12"), else a
unique symbol pin name; an explicit {"pin", "source"} overrides it. A measured pin
the intent does not mention is recorded as connectionState "unconnected" (open, with
an electrical warning) - never guessed as NC.

Zoning (reported per zone in --report):
  1. inside each functional zone, parts group by shared SIGNAL nets (power and
     ground excluded); each group is a zone, core = the member with most pins;
  2. rail-only parts (decoupling) join the functional zone's primary group;
  3. dense connector: a core with >= --array-min signal pins that each carry one
     two-pin peripheral whose signal also leaves the zone -> those peripherals form
     an "<ZONE>_ARRAY" zone (10-raw pin pitch has no room for a part AND a port
     lead per pin);
  4. net policy: ground -> local_ground, power -> local_power, a signal inside one
     zone with >= 2 pins -> direct, otherwise module_port.
Two-pin parts default to allowedRotations [0,90,180,270]. No editor calls.`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if partsPath == "" || listPath == "" || desigPath == "" || out == "" {
			return fmt.Errorf("--parts, --list, --designators and --out are required")
		}
		var parts []zonesDerivePart
		var list, desig map[string]any
		var rotations map[string]map[string][]layoutBBox
		for _, f := range []struct {
			path string
			into any
		}{{partsPath, &parts}, {listPath, &list}, {desigPath, &desig}} {
			raw, err := os.ReadFile(f.path)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(raw, f.into); err != nil {
				return fmt.Errorf("%s: %w", f.path, err)
			}
		}
		if rotPath != "" {
			raw, err := os.ReadFile(rotPath)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &rotations); err != nil {
				return fmt.Errorf("%s: %w", rotPath, err)
			}
		}
		opts := zonesDeriveOptions{Power: zonesDeriveSet(power), Ground: zonesDeriveSet(ground), ArrayMin: arrayMin, Spacing: spacing, MaxCandidates: maxCandidates}
		src, rep, err := deriveSchematicZones(parts, list, desig, rotations, opts)
		if err != nil {
			return err
		}
		if err := zonesDeriveWrite(out, src); err != nil {
			return err
		}
		if report != "" {
			if err := zonesDeriveWrite(report, rep); err != nil {
				return err
			}
		}
		fmt.Fprintf(stdout, "zones-derive: %d parts, %d zones -> %s\n", rep.Parts, rep.Zones, out)
		for _, d := range rep.Decisions {
			fmt.Fprintf(stdout, "  %-22s %-16s %s\n", d.Zone, d.Rule, strings.Join(d.Parts, ","))
		}
		return nil
	}}
	c.Flags().StringVar(&partsPath, "parts", "", "design intent JSON")
	c.Flags().StringVar(&listPath, "list", "", "sch list snapshot with pins and bbox")
	c.Flags().StringVar(&desigPath, "designators", "", "sch designator-geometry output")
	c.Flags().StringVar(&rotPath, "rotations", "", "optional measured designator poses")
	c.Flags().StringVar(&out, "out", "", "write zones source JSON")
	c.Flags().StringVar(&report, "report", "", "write the zoning decisions")
	c.Flags().StringVar(&power, "power", "", "power nets, CSV (local_power)")
	c.Flags().StringVar(&ground, "ground", "GND", "ground nets, CSV (local_ground)")
	c.Flags().IntVar(&arrayMin, "array-min", 4, "dense-connector threshold for array zones (0 disables)")
	c.Flags().Float64Var(&spacing, "spacing", 10, "zones source spacing (raw)")
	c.Flags().IntVar(&maxCandidates, "max-candidates", 200000, "per-zone candidate budget")
	return c
}

func zonesDeriveSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out[v] = true
		}
	}
	return out
}

func zonesDeriveWrite(path string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}

func deriveNum(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok && !math.IsNaN(f) && !math.IsInf(f, 0)
}

func deriveSchematicZones(parts []zonesDerivePart, list, desig map[string]any, rotations map[string]map[string][]layoutBBox, opts zonesDeriveOptions) (*SchematicZonesInput, *zonesDeriveReport, error) {
	if result, ok := list["result"].(map[string]any); ok {
		list = result
	}
	measured := map[string]map[string]any{}
	comps, _ := list["components"].([]any)
	for _, item := range comps {
		c, _ := item.(map[string]any)
		if c["componentType"] == "part" {
			measured[stringVal(c["designator"])] = c
		}
	}
	if result, ok := desig["result"].(map[string]any); ok {
		desig = result
	}
	text := map[string][]layoutBBox{}
	ds, _ := desig["designators"].([]any)
	for _, item := range ds {
		d, _ := item.(map[string]any)
		b, _ := d["bbox"].(map[string]any)
		minX, a := deriveNum(b["minX"])
		minY, bb := deriveNum(b["minY"])
		maxX, c := deriveNum(b["maxX"])
		maxY, e := deriveNum(b["maxY"])
		if !a || !bb || !c || !e {
			return nil, nil, fmt.Errorf("designator %v has no measured bbox", d["value"])
		}
		text[stringVal(d["parentId"])] = append(text[stringVal(d["parentId"])], layoutBBox{minX, minY, maxX, maxY})
	}
	rep := &zonesDeriveReport{SchemaVersion: 1, OpenPins: map[string][]string{}, Policies: map[string]int{}}
	src := &SchematicZonesInput{SchemaVersion: 1, NetPolicies: map[string]string{}, Attachments: []SchematicLayoutPeripheral{}}
	sp := opts.Spacing
	src.Spacing = &sp
	src.MaxCandidates = opts.MaxCandidates
	byID := map[string]*SchematicLayoutComponent{}
	zoneOf := map[string]string{}
	var order []string
	for _, p := range parts {
		m, ok := measured[p.Designator]
		if !ok {
			return nil, nil, fmt.Errorf("%s (%s) is not on the measured page", p.ID, p.Designator)
		}
		if r, _ := deriveNum(m["rotation"]); r != 0 || m["mirror"] == true {
			return nil, nil, fmt.Errorf("%s must be measured at rotation 0 without mirror", p.Designator)
		}
		mpins, _ := m["pins"].([]any)
		byNum := map[string]map[string]any{}
		byName := map[string][]map[string]any{}
		for _, item := range mpins {
			q, _ := item.(map[string]any)
			byNum[stringVal(q["pinNumber"])] = q
			byName[stringVal(q["pinName"])] = append(byName[stringVal(q["pinName"])], q)
		}
		nets, states := map[string]string{}, map[string]string{}
		for _, pin := range p.Pins {
			var q map[string]any
			switch {
			case pin.Pin != "":
				if pin.Source == "" {
					return nil, nil, fmt.Errorf("%s.%s: an explicit pin override needs its source", p.Designator, pin.Terminal)
				}
				q = byNum[pin.Pin]
			case byNum[strings.ReplaceAll(pin.Terminal, "/", "")] != nil:
				q = byNum[strings.ReplaceAll(pin.Terminal, "/", "")]
			case len(byName[pin.Terminal]) == 1:
				q = byName[pin.Terminal][0]
			}
			if q == nil {
				return nil, nil, fmt.Errorf("%s.%s does not map to exactly one measured pin; add {\"pin\",\"source\"}", p.Designator, pin.Terminal)
			}
			n := stringVal(q["pinNumber"])
			if _, dup := nets[n]; dup || states[n] != "" {
				return nil, nil, fmt.Errorf("%s pin %s is mapped twice", p.Designator, n)
			}
			switch {
			case pin.State == "nc" || strings.EqualFold(pin.Net, "nc"):
				states[n] = "nc"
			case pin.Net != "":
				nets[n] = pin.Net
			default:
				return nil, nil, fmt.Errorf("%s.%s needs a net or state nc", p.Designator, pin.Terminal)
			}
		}
		x, _ := deriveNum(m["x"])
		y, _ := deriveNum(m["y"])
		b, _ := m["bbox"].(map[string]any)
		minX, _ := deriveNum(b["minX"])
		minY, _ := deriveNum(b["minY"])
		maxX, _ := deriveNum(b["maxX"])
		maxY, _ := deriveNum(b["maxY"])
		pl := SchematicPlacement{PrimitiveID: stringVal(m["primitiveId"]), Designator: p.Designator, Value: stringVal(m["value"]), X: x, Y: y,
			BBox: layoutBBox{minX, minY, maxX, maxY}, TextBBoxes: text[stringVal(m["primitiveId"])], TextBBoxesByRotation: rotations[p.ID]}
		if len(pl.TextBBoxes) == 0 {
			return nil, nil, fmt.Errorf("%s has no measured designator box", p.Designator)
		}
		for _, item := range mpins {
			q, _ := item.(map[string]any)
			n := stringVal(q["pinNumber"])
			px, _ := deriveNum(q["x"])
			py, _ := deriveNum(q["y"])
			pin := powerLayoutPin{Number: n, Name: stringVal(q["pinName"]), Net: nets[n], X: px, Y: py}
			if r, ok := deriveNum(q["rotation"]); ok {
				pin.Rotation = &r
			}
			if nets[n] == "" && states[n] == "" {
				states[n] = "unconnected"
				rep.OpenPins[p.Designator] = append(rep.OpenPins[p.Designator], n)
			}
			pl.Pins = append(pl.Pins, pin)
		}
		comp := &SchematicLayoutComponent{ID: p.ID, Measurement: pl, PinStates: states, AllowedRotations: p.AllowedRotations}
		if comp.AllowedRotations == nil && len(pl.Pins) == 2 {
			comp.AllowedRotations = []float64{0, 90, 180, 270}
		}
		byID[p.ID] = comp
		zoneOf[p.ID] = p.FunctionalZone
		order = append(order, p.ID)
	}
	signal := func(id string) map[string]bool {
		out := map[string]bool{}
		for _, q := range byID[id].Measurement.Pins {
			if q.Net != "" && !opts.Power[q.Net] && !opts.Ground[q.Net] {
				out[q.Net] = true
			}
		}
		return out
	}
	shares := func(a, b map[string]bool) int {
		n := 0
		for k := range a {
			if b[k] {
				n++
			}
		}
		return n
	}
	pinCount := func(id string) int { return len(byID[id].Measurement.Pins) }
	pickCore := func(ids []string) string {
		best := ids[0]
		for _, id := range ids[1:] {
			if pinCount(id) > pinCount(best) || pinCount(id) == pinCount(best) && id < best {
				best = id
			}
		}
		return best
	}
	zoneID := func(name string, k int) string {
		id := strings.ToUpper(strings.NewReplacer("-", "_", " ", "_").Replace(name))
		if k > 1 {
			id += fmt.Sprintf("_%d", k)
		}
		return id
	}
	var fzOrder []string
	members := map[string][]string{}
	for _, id := range order {
		if _, ok := members[zoneOf[id]]; !ok {
			fzOrder = append(fzOrder, zoneOf[id])
		}
		members[zoneOf[id]] = append(members[zoneOf[id]], id)
	}
	var zones []SchematicZone
	for _, fz := range fzOrder {
		var railOnly, left []string
		for _, id := range members[fz] {
			if len(signal(id)) == 0 {
				railOnly = append(railOnly, id)
			} else {
				left = append(left, id)
			}
		}
		sort.Strings(left)
		k := 0
		for len(left) > 0 {
			core := pickCore(left)
			group := map[string]bool{core: true}
			frontier := []string{core}
			for len(frontier) > 0 {
				x := frontier[len(frontier)-1]
				frontier = frontier[:len(frontier)-1]
				for _, y := range left {
					if !group[y] && shares(signal(x), signal(y)) > 0 {
						group[y] = true
						frontier = append(frontier, y)
					}
				}
			}
			var rest, ids []string
			for _, id := range left {
				if group[id] {
					if id != core {
						ids = append(ids, id)
					}
				} else {
					rest = append(rest, id)
				}
			}
			left = rest
			k++
			rule := "signal-group"
			if k == 1 && len(railOnly) > 0 {
				ids = append(ids, railOnly...)
				rule = "signal-group+rail"
			} else if k > 1 {
				rule = "detached-signal-group"
			}
			sort.Strings(ids)
			z := SchematicZone{ID: zoneID(fz, k), Title: fmt.Sprintf("%s (%s)", fz, byID[core].Measurement.Designator), CoreComponentID: core, ComponentIDs: append([]string{core}, ids...)}
			zones = append(zones, z)
			rep.Decisions = append(rep.Decisions, zonesDeriveDecision{Zone: z.ID, Rule: rule, Parts: z.ComponentIDs})
		}
		if k == 0 && len(railOnly) > 0 {
			core := pickCore(railOnly)
			ids := []string{core}
			for _, id := range railOnly {
				if id != core {
					ids = append(ids, id)
				}
			}
			z := SchematicZone{ID: zoneID(fz, 1), Title: fmt.Sprintf("%s (%s)", fz, byID[core].Measurement.Designator), CoreComponentID: core, ComponentIDs: ids}
			zones = append(zones, z)
			rep.Decisions = append(rep.Decisions, zonesDeriveDecision{Zone: z.ID, Rule: "rail-only", Parts: ids})
		}
	}
	// Dense connector -> array zone.
	if opts.ArrayMin > 0 {
		netZones := map[string]map[string]bool{}
		for _, z := range zones {
			for _, id := range z.ComponentIDs {
				for _, q := range byID[id].Measurement.Pins {
					if q.Net != "" {
						if netZones[q.Net] == nil {
							netZones[q.Net] = map[string]bool{}
						}
						netZones[q.Net][z.ID] = true
					}
				}
			}
		}
		var split []SchematicZone
		for _, z := range zones {
			coreSig := signal(z.CoreComponentID)
			var arr, rest []string
			for _, id := range z.ComponentIDs {
				if id == z.CoreComponentID || pinCount(id) != 2 {
					rest = append(rest, id)
					continue
				}
				common := 0
				leaves := true
				for n := range signal(id) {
					if coreSig[n] {
						common++
						leaves = leaves && len(netZones[n]) > 1
					}
				}
				if common == 1 && leaves {
					arr = append(arr, id)
				} else {
					rest = append(rest, id)
				}
			}
			if len(arr) < opts.ArrayMin {
				split = append(split, z)
				continue
			}
			sort.Strings(arr)
			z.ComponentIDs = rest
			split = append(split, z)
			a := SchematicZone{ID: z.ID + "_ARRAY", Title: strings.SplitN(z.Title, " (", 2)[0] + " 上拉/串阻阵列", CoreComponentID: arr[0], ComponentIDs: arr}
			split = append(split, a)
			rep.Decisions = append(rep.Decisions, zonesDeriveDecision{Zone: a.ID, Rule: "dense-connector-array", Parts: arr,
				Detail: fmt.Sprintf("%d two-pin peripherals on %s signal pins leave the zone", len(arr), byID[z.CoreComponentID].Measurement.Designator)})
		}
		zones = split
	}
	final := map[string]string{}
	for _, z := range zones {
		for _, id := range z.ComponentIDs {
			final[id] = z.ID
		}
	}
	for i := range rep.Decisions {
		for _, z := range zones {
			if z.ID == rep.Decisions[i].Zone {
				rep.Decisions[i].Parts = z.ComponentIDs
			}
		}
	}
	netZones := map[string]map[string]bool{}
	netPins := map[string]int{}
	for _, id := range order {
		for _, q := range byID[id].Measurement.Pins {
			if q.Net == "" {
				continue
			}
			if netZones[q.Net] == nil {
				netZones[q.Net] = map[string]bool{}
			}
			netZones[q.Net][final[id]] = true
			netPins[q.Net]++
		}
	}
	for n, zs := range netZones {
		policy := "module_port"
		switch {
		case opts.Ground[n]:
			policy = "local_ground"
		case opts.Power[n]:
			policy = "local_power"
		case len(zs) == 1 && netPins[n] > 1:
			policy = "direct"
		}
		src.NetPolicies[n] = policy
		rep.Policies[policy]++
	}
	for _, id := range order {
		src.Components = append(src.Components, *byID[id])
	}
	src.Zones = zones
	rep.Parts, rep.Zones = len(order), len(zones)
	return src, rep, nil
}
