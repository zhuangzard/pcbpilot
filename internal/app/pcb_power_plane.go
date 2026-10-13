package app

// pcb_power_plane.go — the explicit power-plane decision of `kicad route`
// (--power-plane NET:LAYER or intent power_planes) and the advice printed
// when none is given.
//
// Turning an inner GND zone into a power plane changes the board's stackup,
// so it is never done implicitly: only a plane named by the user is made
// (the full-board zone on that layer is replaced and the replaced zone is
// printed). Without one, the summary says which plane would help.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// powerPlane is one resolved plane decision.
type powerPlane struct {
	Net     string `json:"net"`
	Layer   string `json:"layer"`
	LayerID int    `json:"layerId"` // pcbpilot id: F.Cu 1, B.Cu 2, InN.Cu 14+N
}

var reInnerLayer = regexp.MustCompile(`(?i)^in(\d+)(\.cu)?$`)

// parsePlaneLayer reads "In2", "In2.Cu" or a pcbpilot layer id (15+) on a
// board with copperLayers copper layers (the outer layers are no plane).
func parsePlaneLayer(s string, copperLayers int) (int, error) {
	s = strings.TrimSpace(s)
	id := 0
	if m := reInnerLayer.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		id = 14 + n
	} else if n, err := strconv.Atoi(s); err == nil {
		id = n
	}
	if id < 15 || id > 14+copperLayers-2 {
		return 0, fmt.Errorf("plane layer %q is not an inner copper layer of this %d-layer board (In1..In%d)", s, copperLayers, copperLayers-2)
	}
	return id, nil
}

// resolvePlanes: --power-plane flags, else the intent's power_planes.
func (r *kicadRun) resolvePlanes(copperLayers int) ([]powerPlane, error) {
	var in []intentPowerPlane
	for _, f := range r.o.powerPlanes {
		i := strings.LastIndex(f, ":")
		if i <= 0 || i == len(f)-1 {
			return nil, fmt.Errorf("--power-plane %q: want NET:LAYER (e.g. +3V3:In2)", f)
		}
		in = append(in, intentPowerPlane{Net: f[:i], Layer: f[i+1:]})
	}
	if len(in) == 0 && r.in != nil {
		in = r.in.PowerPlanes
	}
	var out []powerPlane
	seen := map[int]string{}
	for _, p := range in {
		id, err := parsePlaneLayer(p.Layer, copperLayers)
		if err != nil {
			return nil, err
		}
		if p.Net == "" {
			return nil, fmt.Errorf("power plane on %s: net is empty", p.Layer)
		}
		if prev, dup := seen[id]; dup {
			return nil, fmt.Errorf("power planes %s and %s both name layer %s", prev, p.Net, p.Layer)
		}
		seen[id] = p.Net
		out = append(out, powerPlane{Net: p.Net, Layer: p.Layer, LayerID: id})
	}
	return out, nil
}

// applyPlanes makes the explicit planes on board (zone fill included) and
// records which zones they replaced. KiCad's DRC then refills the zones
// before the gates judge connectivity.
func (r *kicadRun) applyPlanes(board string, planes []powerPlane) (string, error) {
	var ps []kicad.Pour
	clr := 0.0
	if r.inRules != nil {
		clr = r.inRules.ClearanceMil
	}
	for _, p := range planes {
		ps = append(ps, kicad.Pour{Net: p.Net, Layer: p.LayerID, ClearanceMil: clr, Replace: true})
	}
	out := r.next("planes")
	raw, err := r.kt.Pours(board, ps, out)
	if err != nil {
		return board, fmt.Errorf("power plane: %w", err)
	}
	var res struct {
		Replaced []struct {
			Name  string `json:"name"`
			Net   string `json:"net"`
			Layer int    `json:"layer"`
		} `json:"replaced"`
	}
	_ = json.Unmarshal(raw, &res)
	var lines []string
	for _, p := range planes {
		msg := fmt.Sprintf("plane %s on %s: no zone replaced (the layer had no full-board zone)", p.Net, p.Layer)
		for _, z := range res.Replaced {
			if z.Layer == p.LayerID {
				msg = fmt.Sprintf("plane %s on %s REPLACES zone %q (net %s)", p.Net, p.Layer, z.Name, z.Net)
			}
		}
		lines = append(lines, msg)
		fmt.Fprintf(r.stderr, "power plane: %s\n", msg)
	}
	r.summary["powerPlanes"] = map[string]any{"planes": planes, "result": lines, "replaced": res.Replaced}
	r.kt.RefillZones = true
	return out, nil
}

// planeAdvice: with no plane decided, power pads that no stub could serve
// and a stackup without a free inner layer get a printed suggestion (never
// acted on).
func (r *kicadRun) planeAdvice(s *boardSnapshot) {
	if _, done := r.summary["powerPlanes"]; done || s == nil || s.Copper == nil || s.CopperLayers < 4 {
		return
	}
	rep := r.powerStubRep
	if rep == nil || rep.Candidates == 0 {
		return
	}
	// nets with fine-pitch power pads that need a stub, most pads first
	count := map[string]int{}
	for _, p := range rep.Planned {
		count[p.Net]++
	}
	for _, b := range rep.GeometryBlocked {
		if i, j := strings.Index(b, "("), strings.Index(b, ")"); i > 0 && j > i {
			count[b[i+1:j]]++
		}
	}
	// inner layers and the full zones on them
	zoneOn := map[int][]map[string]any{}
	for _, z := range s.Copper.Pours {
		m, ok := z.(map[string]any)
		if !ok {
			continue
		}
		if l, ok := m["layer"].(float64); ok && int(l) >= 15 {
			zoneOn[int(l)] = append(zoneOn[int(l)], m)
		}
	}
	for l := 15; l <= 14+s.CopperLayers-2; l++ {
		if len(zoneOn[l]) == 0 {
			return // a free inner layer exists: nothing to replace
		}
	}
	var nets []string
	for n := range count {
		nets = append(nets, n)
	}
	sort.Slice(nets, func(i, j int) bool {
		if count[nets[i]] != count[nets[j]] {
			return count[nets[i]] > count[nets[j]]
		}
		return nets[i] < nets[j]
	})
	netOfZone := func(m map[string]any) string { n, _ := m["net"].(string); return n }
	var advice []string
	for _, n := range nets {
		if count[n] == 0 || strings.Contains(strings.ToUpper(n), "GND") {
			continue
		}
		// prefer replacing a zone whose net also keeps another inner zone (GND stays a plane)
		best, bestScore := 0, -1
		for l := 15; l <= 14+s.CopperLayers-2; l++ {
			zn := netOfZone(zoneOn[l][0])
			if strings.EqualFold(zn, n) {
				best = 0
				break
			}
			score := 0
			for l2 := 15; l2 <= 14+s.CopperLayers-2; l2++ {
				if l2 != l && strings.EqualFold(netOfZone(zoneOn[l2][0]), zn) {
					score = 1
				}
			}
			if score > bestScore {
				best, bestScore = l, score
			}
		}
		if best == 0 {
			continue
		}
		z := zoneOn[best][0]
		name, _ := z["name"].(string)
		advice = append(advice, fmt.Sprintf("plane suggested: net %s on layer In%d would replace zone %q (net %s); %d fine-pitch pad(s) of %s are served by stub+via, a plane gives every pad of the net a via-to-plane connection. A stackup decision: re-run with --power-plane %s:In%d (or intent power_planes) to apply it; nothing was changed.",
			n, best-14, name, netOfZone(z), count[n], n, n, best-14))
	}
	if len(advice) > 0 {
		r.summary["powerPlaneAdvice"] = advice
		for _, a := range advice {
			fmt.Fprintln(r.stderr, a)
		}
	}
}

// settle moves joint-solve failures the sequential planner then escaped to
// Sequential; what stays in GeometryBlocked has no stub+via and no escape.
func (rep *powerStubReport) settle(esc []intentEscape, skipped []string) {
	have := map[string]bool{}
	for _, e := range esc {
		have[e.Pad] = true
	}
	var blocked []string
	for _, b := range rep.GeometryBlocked {
		key := strings.SplitN(b, " ", 2)[0]
		if have[key] {
			rep.Sequential = append(rep.Sequential, key)
			continue
		}
		blocked = append(blocked, b)
	}
	rep.GeometryBlocked = blocked
	_ = skipped
}
