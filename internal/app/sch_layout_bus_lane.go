package app

// sch_layout_bus_lane.go — schematic aesthetics Phase B3: virtual bus lanes.
//
// Indexed / grouped nets (D0..D7, SPI/QSPI, I2C, UART, SDIO; MIPI/USB pairs)
// detected by schaes.DetectBusCandidates get their port/label markers drawn
// as one lane: the same direction, the label ends on one column (or row) and
// an equal pitch along it (metric N3). Electrically nothing changes: each
// member keeps its own net, its own pin, its own real stub; only lead lengths
// and short outward jogs move. Every lane is a normal beautify candidate, so
// it is kept only if geometry, nets, ownership and check/lint counts hold.
//
// Native buses (user decision 2026-10-03): with --aesthetics balanced |
// precision (and --native-bus not false) every COMPLETE lane — every member
// has exactly one label/port, all point one way and end on one column — whose
// group reaches generate.nativeBusMinMembers is also drawn as a native bus
// primitive (result.buses, sch_native_bus.go): the trunk runs beside the
// aligned label column, comb branches stop 5 units short of each label, and
// the bus touches no wire, pin, body, designator or marker. Members keep their
// wire + same-name label taps; the bus is never connectivity evidence. Names:
// indexed groups NAME[a:b] (contiguous indices only), protocol groups the
// group name (schaes.NativeBusName: SPI1, UART0, ESP_UART, SPI). Lanes that do
// not qualify, or a host without the bus API (--bus-host absent), stay
// virtual lanes; the reason is in busLanes[].native.

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"

	"github.com/zhuangzard/pcbpilot/pkg/schaes"
)

// SchematicBusLane reports one virtual bus lane after the pass.
type SchematicBusLane struct {
	Group     string                      `json:"group"`
	Kind      string                      `json:"kind"`
	Members   []string                    `json:"members"`
	Markers   int                         `json:"markers"`
	Direction string                      `json:"direction,omitempty"`
	Axis      string                      `json:"axis,omitempty"` // x: labels end on one column; y: on one row
	Column    float64                     `json:"column"`
	Pitch     float64                     `json:"pitch,omitempty"`
	PitchCV   float64                     `json:"pitchCV"`
	Aligned   float64                     `json:"aligned"`
	SameDir   float64                     `json:"sameDir"`
	Native    *SchematicNativeBusProposal `json:"native,omitempty"`
}

// SchematicNativeBusProposal records the native bus decision of one lane.
type SchematicNativeBusProposal struct {
	Name   string      `json:"name"`
	Line   [][]float64 `json:"line,omitempty"`
	Status string      `json:"status"` // planned | host-unverified | fallback-virtual | skipped
	Reason string      `json:"reason,omitempty"`
}

func schBusLaneMarkerKind(kind string) bool { return isNetPortKind(kind) || kind == "net_label" }

func schLaneAxis(direction string) string {
	if direction == "left" || direction == "right" {
		return "x"
	}
	return "y"
}

func schLaneEnd(f powerLayoutFlag) float64 {
	x, y := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
	if schLaneAxis(f.Direction) == "x" {
		return x
	}
	return y
}

func schLaneAlong(f powerLayoutFlag) float64 {
	x, y := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
	if schLaneAxis(f.Direction) == "x" {
		return y
	}
	return x
}

func schBusCandidatesOf(p *powerLayoutPlan) []schaes.BusCandidate {
	seen := map[string]bool{}
	var nets []string
	for _, f := range p.Flags {
		if !seen[f.Net] {
			seen[f.Net] = true
			nets = append(nets, f.Net)
		}
	}
	sort.Strings(nets)
	return schaes.DetectBusCandidates(nets)
}

func schLaneMembers(p *powerLayoutPlan, c schaes.BusCandidate) []int {
	member := map[string]bool{}
	for _, n := range c.Members {
		member[n] = true
	}
	var idx []int
	for i, f := range p.Flags {
		if member[f.Net] && schBusLaneMarkerKind(f.Kind) {
			idx = append(idx, i)
		}
	}
	return idx
}

// busLanePass tries, per candidate group: (1) one shared direction, (2) one
// shared end column, (3) an equal pitch via short outward jogs.
func (e *aesEngine) busLanePass(p *powerLayoutPlan, obj libAesObjective) (libAesObjective, bool) {
	improved := false
	for _, c := range schBusCandidatesOf(p) {
		if !e.budgetLeft() {
			break
		}
		idx := schLaneMembers(p, c)
		if len(idx) < 2 {
			continue
		}
		// (1) majority direction; horizontal wins ties (ports must stay horizontal).
		count := map[string]int{}
		for _, i := range idx {
			count[p.Flags[i].Direction]++
		}
		dir := ""
		for _, d := range []string{"right", "left", "up", "down"} {
			if dir == "" || count[d] > count[dir] {
				dir = d
			}
		}
		if count[dir] < len(idx) {
			cand := clonePowerLayoutPlan(p)
			ok := true
			for _, i := range idx {
				f := cand.Flags[i]
				if f.Direction == dir {
					continue
				}
				f.Direction = dir
				placed := false
				start, cap := e.markerCap(cand, f.Net, f.Kind)
				for off := start; off <= cap && !placed; off += 5 {
					f.Offset = off
					trial := *cand
					trial.Flags = append([]powerLayoutFlag(nil), cand.Flags...)
					trial.Flags[i] = f
					if validateLibGeometry(&trial) == nil {
						cand.Flags, placed = trial.Flags, true
					}
				}
				ok = ok && placed
			}
			if ok {
				if o, acc := e.try("bus-lane", p, obj, cand, false); acc {
					obj, improved = o, true
				}
			}
		}
		// (2) one end column for every member already pointing `dir`.
		idx = schLaneMembers(p, c)
		var same []int
		for _, i := range idx {
			if p.Flags[i].Direction == dir {
				same = append(same, i)
			}
		}
		if len(same) >= 2 {
			sign := 1.0
			if dir == "left" || dir == "down" {
				sign = -1
			}
			target := -math.MaxFloat64
			for _, i := range same {
				target = math.Max(target, sign*schLaneEnd(p.Flags[i]))
			}
			aligned := true
			for _, i := range same {
				aligned = aligned && sign*schLaneEnd(p.Flags[i]) == target
			}
			for extra := 0.0; !aligned && extra <= 20 && e.budgetLeft(); extra += 5 {
				cand := clonePowerLayoutPlan(p)
				for _, i := range same {
					f := &cand.Flags[i]
					pin := f.PinX
					if schLaneAxis(dir) == "y" {
						pin = f.PinY
					}
					// target is in sign space: the column is sign·(target+extra).
					f.Offset = target + extra - sign*pin
				}
				if o, acc := e.try("bus-lane", p, obj, cand, false); acc {
					obj, improved = o, true
					break
				}
			}
		}
		// (3) equal pitch for ≥3 aligned members via short outward jogs.
		idx = schLaneMembers(p, c)
		same = same[:0]
		for _, i := range idx {
			if p.Flags[i].Direction == dir {
				same = append(same, i)
			}
		}
		if len(same) >= 3 && e.budgetLeft() {
			if cand := e.repitchLane(p, same, dir); cand != nil {
				if o, acc := e.try("bus-lane", p, obj, cand, false); acc {
					obj, improved = o, true
				}
			}
		}
	}
	e.recordLanes(p)
	return obj, improved
}

// repitchLane moves lane members onto equal-pitch slots: from the original
// tap a 5-unit outward run, a perpendicular jog to the slot, then the label
// on the shared end column. Returns nil when the pitch is already uniform.
func (e *aesEngine) repitchLane(p *powerLayoutPlan, same []int, dir string) *powerLayoutPlan {
	sort.SliceStable(same, func(a, b int) bool { return schLaneAlong(p.Flags[same[a]]) < schLaneAlong(p.Flags[same[b]]) })
	var gaps []float64
	for k := 1; k < len(same); k++ {
		gaps = append(gaps, schLaneAlong(p.Flags[same[k]])-schLaneAlong(p.Flags[same[k-1]]))
	}
	uniform := true
	for _, g := range gaps {
		uniform = uniform && g == gaps[0]
	}
	if uniform {
		return nil
	}
	sorted := append([]float64(nil), gaps...)
	sort.Float64s(sorted)
	pitch := math.Max(e.profile.Generate.BusPitch, math.Ceil(sorted[len(sorted)/2]/schAnchorGrid)*schAnchorGrid)
	first := schLaneAlong(p.Flags[same[0]])
	sign := 1.0
	if dir == "left" || dir == "down" {
		sign = -1
	}
	end := -math.MaxFloat64
	for _, i := range same {
		end = math.Max(end, sign*schLaneEnd(p.Flags[i]))
	}
	end *= sign
	cand := clonePowerLayoutPlan(p)
	for k, i := range same {
		f := cand.Flags[i]
		slot := first + float64(k)*pitch
		if schLaneAlong(f) == slot {
			continue
		}
		jx, jy := endpointFor(f.PinX, f.PinY, 5, dir)
		var tap [2]float64
		if schLaneAxis(dir) == "x" {
			tap = [2]float64{jx, slot}
		} else {
			tap = [2]float64{slot, jy}
		}
		cand.Wires = libAppendRoute(cand.Wires, libPointsRoute(f.Net, [2]float64{f.PinX, f.PinY}, [2]float64{jx, jy}, tap))
		f.PinX, f.PinY = tap[0], tap[1]
		if schLaneAxis(dir) == "x" {
			f.Offset = math.Abs(end - tap[0])
		} else {
			f.Offset = math.Abs(end - tap[1])
		}
		if f.Offset < 10 {
			return nil
		}
		cand.Flags[i] = f
	}
	return cand
}

// recordLanes summarises every candidate group's lane on the final plan.
func (e *aesEngine) recordLanes(p *powerLayoutPlan) {
	e.lanes = nil
	for _, c := range schBusCandidatesOf(p) {
		idx := schLaneMembers(p, c)
		lane := SchematicBusLane{Group: c.Suggested, Kind: c.Kind, Members: c.Members, Markers: len(idx)}
		if len(idx) < 2 {
			e.lanes = append(e.lanes, lane)
			continue
		}
		dirs := map[string]int{}
		for _, i := range idx {
			dirs[p.Flags[i].Direction]++
		}
		for d, n := range dirs {
			if lane.Direction == "" || n > dirs[lane.Direction] || (n == dirs[lane.Direction] && d < lane.Direction) {
				lane.Direction = d
			}
		}
		lane.SameDir = float64(dirs[lane.Direction]) / float64(len(idx))
		lane.Axis = schLaneAxis(lane.Direction)
		ends := map[float64]int{}
		var along []float64
		for _, i := range idx {
			if p.Flags[i].Direction != lane.Direction {
				continue
			}
			ends[schLaneEnd(p.Flags[i])]++
			along = append(along, schLaneAlong(p.Flags[i]))
		}
		best := 0
		for v, n := range ends {
			if n > best || (n == best && v > lane.Column) {
				best, lane.Column = n, v
			}
		}
		lane.Aligned = float64(best) / float64(len(idx))
		sort.Float64s(along)
		var gaps []float64
		for k := 1; k < len(along); k++ {
			gaps = append(gaps, along[k]-along[k-1])
		}
		if len(gaps) > 0 {
			m, sd := 0.0, 0.0
			for _, g := range gaps {
				m += g
			}
			m /= float64(len(gaps))
			for _, g := range gaps {
				sd += (g - m) * (g - m)
			}
			sd = math.Sqrt(sd / float64(len(gaps)))
			lane.Pitch = m
			if m > 0 {
				lane.PitchCV = math.Round(sd/m*1000) / 1000
			}
		}
		e.lanes = append(e.lanes, lane)
	}
}

// planNativeBuses records the lanes and returns the native buses of every
// qualifying complete lane (see the file comment). Lanes that do not qualify
// carry the reason in Native.
func (e *aesEngine) planNativeBuses(p *powerLayoutPlan) []SchematicNativeBus {
	e.recordLanes(p)
	cands := map[string]schaes.BusCandidate{}
	for _, c := range schBusCandidatesOf(p) {
		cands[c.Suggested] = c
	}
	var out []SchematicNativeBus
	for i := range e.lanes {
		l := &e.lanes[i]
		c := cands[l.Group]
		name := schaes.NativeBusName(c)
		if l.Markers < 2 {
			continue // wired directly / off-page
		}
		if name == "" {
			if c.Kind != "usb" && c.Kind != "mipi" {
				l.Native = &SchematicNativeBusProposal{Name: c.Suggested, Status: "skipped", Reason: schaes.NativeBusSkipReason(c)}
			}
			continue
		}
		prop := &SchematicNativeBusProposal{Name: name, Status: "skipped"}
		l.Native = prop
		if reason := e.laneBusBlocker(p, l, c); reason != "" {
			prop.Reason = reason
			if e.hostBusAPI == "absent" {
				prop.Status = "fallback-virtual"
			}
			continue
		}
		bus, err := e.laneBusGeometry(p, l, c, name, out)
		if err != nil {
			prop.Reason = err.Error()
			continue
		}
		prop.Line, prop.Status = bus.Line, bus.Status
		out = append(out, bus)
	}
	return out
}

// laneBusBlocker returns why a lane is not drawn as a native bus ("" = ok).
func (e *aesEngine) laneBusBlocker(p *powerLayoutPlan, l *SchematicBusLane, c schaes.BusCandidate) string {
	if e.hostBusAPI == "absent" {
		return "host lacks sch_PrimitiveBus (health / api probe): virtual bus lane only"
	}
	if e.nativeBusMin == 0 {
		return "native buses off (--native-bus=false or a profile default of 0)"
	}
	if len(c.Members) < e.nativeBusMin {
		return fmt.Sprintf("%d members < profile threshold %d (generate.nativeBusMinMembers)", len(c.Members), e.nativeBusMin)
	}
	count := map[string]int{}
	for _, f := range p.Flags {
		if schBusLaneMarkerKind(f.Kind) {
			count[f.Net]++
		}
	}
	for _, n := range c.Members {
		if count[n] != 1 {
			return fmt.Sprintf("member %s has %d labels/ports in this zone; a bus lane needs exactly one per member", n, count[n])
		}
	}
	if l.Aligned < 1 || l.SameDir < 1 {
		return fmt.Sprintf("lane incomplete (aligned %.0f%%, same direction %.0f%%): virtual lane only", 100*l.Aligned, 100*l.SameDir)
	}
	if c.Kind == "indexed" {
		var idx []int
		for _, n := range c.Members {
			if m := reBusIndex.FindStringSubmatch(n); m != nil {
				k, _ := strconv.Atoi(m[1])
				idx = append(idx, k)
			}
		}
		sort.Ints(idx)
		for k := 1; k < len(idx); k++ {
			if idx[k] != idx[k-1]+1 {
				return "indexed members are not contiguous: NAME[a:b] would name a member that is not in the lane"
			}
		}
	}
	return ""
}

var reBusIndex = regexp.MustCompile(`(\d+)$`)

// laneBusGeometry places the trunk beside the label column (≥15 units past
// the farthest label body/text, on the 5-unit grid) with one comb branch per
// member ending 5 units short of its label, then checks the keep-outs. It
// steps the trunk outward up to 20 units and finally tries a trunk without
// branches before giving up.
func (e *aesEngine) laneBusGeometry(p *powerLayoutPlan, l *SchematicBusLane, c schaes.BusCandidate, name string, others []SchematicNativeBus) (SchematicNativeBus, error) {
	sign := 1.0
	if l.Direction == "left" || l.Direction == "down" {
		sign = -1
	}
	member := map[string]bool{}
	for _, n := range c.Members {
		member[n] = true
	}
	type tap struct{ along, outer float64 }
	var taps []tap
	reach := -math.MaxFloat64
	for _, f := range p.Flags {
		if !member[f.Net] || !schBusLaneMarkerKind(f.Kind) {
			continue
		}
		outer := -math.MaxFloat64
		for _, b := range schTerminalMarkerBoxes(f) {
			if l.Axis == "x" {
				outer = math.Max(outer, math.Max(sign*b.MinX, sign*b.MaxX))
			} else {
				outer = math.Max(outer, math.Max(sign*b.MinY, sign*b.MaxY))
			}
		}
		along := schLaneAlong(f)
		if !plGrid(along) {
			return SchematicNativeBus{}, fmt.Errorf("member %s label anchor is off the 5-unit grid", f.Net)
		}
		taps = append(taps, tap{along, outer})
		reach = math.Max(reach, outer)
	}
	sort.Slice(taps, func(a, b int) bool { return taps[a].along < taps[b].along })
	lo, hi := taps[0].along, taps[len(taps)-1].along
	pt := func(col, along float64) []float64 {
		if l.Axis == "x" {
			return []float64{col, along}
		}
		return []float64{along, col}
	}
	status := "planned"
	if e.hostBusAPI == "unverified" {
		status = "host-unverified"
	}
	var lastErr error
	for _, branches := range []bool{true, false} {
		for shift := 0.0; shift <= 20; shift += 5 {
			ts := plCeil(reach+15) + shift // trunk, sign space (branches ≥10)
			trunk := append(pt(sign*ts, lo), pt(sign*ts, hi)...)
			line := [][]float64{trunk}
			if branches {
				for _, t := range taps {
					es := plCeil(t.outer + 5)
					if es >= ts {
						continue
					}
					line = append(line, append(pt(sign*ts, t.along), pt(sign*es, t.along)...))
				}
			}
			bus := SchematicNativeBus{BusName: name, Line: line, Group: c.Suggested, Kind: c.Kind, Members: append([]string(nil), c.Members...), Status: status}
			if err := validateSchNativeBusShape(bus); err != nil {
				lastErr = err
				continue
			}
			if err := schNativeBusKeepout(p, bus, others); err != nil {
				lastErr = err
				continue
			}
			return bus, nil
		}
	}
	return SchematicNativeBus{}, fmt.Errorf("no keep-out-clean trunk within 20 units of the lane: %v", lastErr)
}
