package pcbauto

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
)

// via-current: the vias of a power path rated against the current they
// carry (pcb check --intent). The vias of a net are grouped into layer
// transitions — vias within a link distance of each other (an array at a
// track's layer change, a fan-out field around a pad) —, each group's
// Σ ampacity (ViaAmpacity per barrel) is compared with the current it has
// to pass:
//
//   - a group at pads whose pins carry an intent current: those pins' sum
//     (bounded by the net current);
//   - else a group with tracks attached: the net's intent current, bounded
//     by what the widest attached track carries (IPC-2152 at the intent ΔT)
//     — a transition cannot pass more than its copper brings;
//   - a group with neither (plane stitching) or a net without current is
//     not rated.
//
// ERROR when Σ ampacity < the current, WARN when the margin is below the
// required margin (default 20 %).

// ViaCurrentFinding is one rated transition.
type ViaCurrentFinding struct {
	Level      string   `json:"level"` // ERROR | WARN
	Net        string   `json:"net"`
	At         Point    `json:"at"`
	Vias       int      `json:"vias"`
	Sizes      []string `json:"sizes"` // drill/dia mil ×count
	AmpacityA  float64  `json:"ampacityA"`
	RequiredA  float64  `json:"requiredA"`
	MarginPct  float64  `json:"marginPct"`
	DropMV     float64  `json:"dropMV"`
	Source     string   `json:"source"` // pin | net | net≤track
	Primitives []string `json:"primitives,omitempty"`
	Message    string   `json:"message"`
	Fix        string   `json:"fix,omitempty"`
}

// ViaCurrentCheck is the rule's result.
type ViaCurrentCheck struct {
	Groups   int                 `json:"groups"` // rated transitions
	Findings []ViaCurrentFinding `json:"findings"`
	Notes    []string            `json:"notes,omitempty"`
}

// viaCheckVia is a via with its primitive id.
type viaCheckVia struct {
	Via
	ID string
}

// CheckViaCurrentSnapshot runs the rule on a `pcb dump --include-copper`
// snapshot with an intent.
func CheckViaCurrentSnapshot(raw []byte, in *Intent) (*ViaCurrentCheck, error) {
	b, err := FromSnapshot(raw)
	if err != nil {
		return nil, err
	}
	var s struct {
		Copper *struct {
			Lines []map[string]any `json:"lines"`
			Arcs  []map[string]any `json:"arcs"`
			Vias  []map[string]any `json:"vias"`
		} `json:"copper"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if s.Copper == nil {
		return &ViaCurrentCheck{Notes: []string{"via-current: snapshot has no copper section (pcb dump --include-copper) — not rated"}}, nil
	}
	var tracks []Track
	for _, l := range append(append([]map[string]any(nil), s.Copper.Lines...), s.Copper.Arcs...) {
		tracks = append(tracks, Track{Net: str(l["net"]), Layer: int(num(l["layer"])), Width: num(l["lineWidth"]),
			A: Point{num(l["startX"]), num(l["startY"])}, B: Point{num(l["endX"]), num(l["endY"])}})
	}
	var vias []viaCheckVia
	for _, v := range s.Copper.Vias {
		vias = append(vias, viaCheckVia{Via{Net: str(v["net"]), C: Point{num(v["x"]), num(v["y"])}, Dia: num(v["diameter"]), Drill: num(v["holeDiameter"])}, str(v["primitiveId"])})
	}
	return checkViaCurrent(b, tracks, vias, in), nil
}

// CheckViaCurrent rates the vias of routed copper (tracks, vias) on b.
func CheckViaCurrent(b *Board, tracks []Track, vias []Via, in *Intent) *ViaCurrentCheck {
	vs := make([]viaCheckVia, len(vias))
	for i, v := range vias {
		vs[i] = viaCheckVia{Via: v}
	}
	return checkViaCurrent(b, tracks, vs, in)
}

func checkViaCurrent(b *Board, tracks []Track, vias []viaCheckVia, in *Intent) *ViaCurrentCheck {
	out := &ViaCurrentCheck{}
	if in == nil {
		out.Notes = append(out.Notes, "via-current: no intent — no currents to rate the vias against")
		return out
	}
	cu := in.Copper
	if cu == nil {
		cu = &IntentCopper{}
	}
	tempRise, plating, margin := cu.TempRiseC, cu.ViaPlatingMil, ViaMarginOr(cu.ViaMarginPct)
	if tempRise <= 0 {
		tempRise = 10
	}
	if plating <= 0 {
		plating = DefaultViaPlatingMil
	}
	outerOz, innerOz := cu.OuterOz, cu.InnerOz
	if outerOz <= 0 {
		outerOz = b.Rules.CopperOz
	}
	if innerOz <= 0 {
		innerOz = b.Rules.InnerCopperOz
	}
	length := b.Rules.BoardThickMil
	if length <= 0 {
		length = DefaultRules().BoardThickMil
	}
	pinA := map[string]float64{}
	for net, n := range in.Nets {
		if n == nil {
			continue
		}
		for _, p := range n.Pins {
			pinA[upper(net)+"|"+p.Ref+"."+p.Pin] = p.CurrentA
		}
	}
	byNet := map[string][]int{}
	for i, v := range vias {
		if v.Net != "" {
			byNet[v.Net] = append(byNet[v.Net], i)
		}
	}
	nets := make([]string, 0, len(byNet))
	for n := range byNet {
		nets = append(nets, n)
	}
	sort.Strings(nets)
	var pads []*Pad
	for _, p := range b.Parts {
		pads = append(pads, p.Pads...)
	}
	skipped := 0
	for _, net := range nets {
		n := intentLookup(in, net)
		if n == nil {
			continue
		}
		netA := n.CurrentA
		if netA <= 0 {
			continue
		}
		idx := byNet[net]
		// Group: single link at max(2.5 × pad, 60 mil).
		parent := make([]int, len(idx))
		for i := range parent {
			parent[i] = i
		}
		var find func(int) int
		find = func(i int) int {
			for parent[i] != i {
				parent[i] = parent[parent[i]]
				i = parent[i]
			}
			return i
		}
		for i := range idx {
			for j := i + 1; j < len(idx); j++ {
				a, c := vias[idx[i]], vias[idx[j]]
				if a.C.Dist(c.C) <= ViaGroupLinkMil(a.Dia, c.Dia) {
					parent[find(i)] = find(j)
				}
			}
		}
		groups := map[int][]viaCheckVia{}
		var roots []int
		for i := range idx {
			r := find(i)
			if groups[r] == nil {
				roots = append(roots, r)
			}
			groups[r] = append(groups[r], vias[idx[i]])
		}
		for _, r := range roots {
			g := groups[r]
			// Pads the group fans out from, and the widest attached track.
			pinSum, pinKnown := 0.0, false
			seen := map[*Pad]bool{}
			for _, pd := range pads {
				if pd.Net != net || seen[pd] {
					continue
				}
				for _, v := range g {
					if pd.Box.Dist(v.C) <= v.Dia/2+40 {
						seen[pd] = true
						if a, ok := pinA[upper(net)+"|"+pd.Key()]; ok {
							pinKnown = true
							pinSum += a
						}
						break
					}
				}
			}
			trackA := 0.0
			for _, t := range tracks {
				if t.Net != net {
					continue
				}
				for _, v := range g {
					if PointSegDist(v.C, t.A, t.B) <= t.Width/2+v.Dia/2+1 {
						oz := outerOz
						if t.Layer != LayerTop && t.Layer != LayerBottom {
							oz = innerOz
						}
						trackA = math.Max(trackA, CurrentForWidth(t.Width, tempRise, oz, false))
						break
					}
				}
			}
			req, src := 0.0, ""
			switch {
			case pinKnown && pinSum > 0:
				req, src = math.Min(pinSum, netA), "pin"
			case pinKnown:
				// Pins known to carry nothing (a decoupling capacitor).
			case trackA > 0:
				req, src = math.Min(netA, trackA), "net"
				if trackA < netA {
					src = "net≤track"
				}
			}
			if req <= 0 {
				skipped++
				continue
			}
			out.Groups++
			amp, cond := 0.0, 0.0
			sizes := map[string]int{}
			var ids []string
			c := Point{}
			for _, v := range g {
				amp += ViaAmpacity(v.Drill, plating, tempRise)
				if rv := ViaResistance(v.Drill, plating, length); rv > 0 {
					cond += 1 / rv
				}
				sizes[sprintf("%.1f/%.1f", v.Drill, v.Dia)]++
				if v.ID != "" {
					ids = append(ids, v.ID)
				}
				c = c.Add(v.C)
			}
			c = c.Scale(1 / float64(len(g)))
			m := math.Min((amp/req-1)*100, viaMarginCapPct)
			level := ""
			switch {
			case amp < req-1e-9:
				level = "ERROR"
			case m < margin-1e-6:
				level = "WARN"
			default:
				continue
			}
			var sz []string
			for k, v := range sizes {
				sz = append(sz, sprintf("%s×%d", k, v))
			}
			sort.Strings(sz)
			drop := 0.0
			if cond > 0 {
				drop = req / cond * 1000
			}
			f := ViaCurrentFinding{Level: level, Net: net, At: Point{round2(c.X), round2(c.Y)}, Vias: len(g), Sizes: sz, AmpacityA: round3(amp),
				RequiredA: round3(req), MarginPct: round2(m), DropMV: round3(drop), Source: src, Primitives: ids}
			fix := SizeVias(ViaSizing{CurrentA: req, TempRiseC: tempRise, PlatingMil: plating, MarginPct: cu.ViaMarginPct,
				Class: ViaSize{b.Rules.ViaDrill, b.Rules.ViaDia}, Space: TrackViaSpace(math.Max(n.WidthMil.Outer, 10)), LengthMil: length,
				Clearance: b.Rules.Clearance, HoleGap: b.Rules.HoleGap})
			f.Fix = sprintf("%d × %.1f/%.1f mil (%.2f A, margin %.0f %%)", fix.Count, fix.DrillMil, fix.DiaMil, fix.AmpacityA, fix.MarginPct)
			if v := n.Via; v != nil && v.CountPerTransition > 0 && v.DrillMil > 0 {
				f.Fix = sprintf("the intent's %d × %.1f/%.1f mil per transition (%.2f A)", v.CountPerTransition, v.DrillMil, v.DiaMil, v.AmpacityA)
			}
			what := map[string]string{"pin": "its pads' pin current", "net": "the net current", "net≤track": "the current its widest track carries"}[src]
			f.Message = sprintf("%s: %d via(s) %s at (%.0f, %.0f) carry %.2f A (IPC-2221 barrel, t=%.2f mil, ΔT %.0f °C) for %.2f A (%s): margin %.1f %% (need ≥ %.0f %%), %.2f mV across the barrels — the via limits the path; use %s",
				net, len(g), strings.Join(sz, " "), c.X, c.Y, amp, plating, tempRise, req, what, m, margin, drop, f.Fix)
			out.Findings = append(out.Findings, f)
		}
	}
	out.Notes = append(out.Notes, sprintf("via-current: %d transition(s) rated (Σ via ampacity vs current, margin ≥ %.0f %%); %d via group(s) without a current to pass (plane stitching, zero-current pins) not rated", out.Groups, margin, skipped))
	return out
}

// ViaGroupLinkMil is the distance under which two same-net vias belong to
// one layer transition (single link): max(60 mil, 2.5 × the larger
// diameter). Via arrays are placed within it so the check rates them as one.
func ViaGroupLinkMil(diaA, diaB float64) float64 {
	return math.Max(60, 2.5*math.Max(diaA, diaB))
}
