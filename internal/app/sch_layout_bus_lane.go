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
// Native buses (schematic.bus.create) are NOT drawn. With
// SchematicAestheticsOptions.NativeBus a complete lane additionally carries a
// proposal (name + line) marked live-unverified; applying it is a separate,
// explicit `pcbpilot sch bus create` after the live verification checklist
// (docs/reviews/2026-10-schematic-aesthetics/README.md §3) has passed.

import (
	"math"
	"sort"

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

// SchematicNativeBusProposal is an opt-in, never-applied native bus line.
type SchematicNativeBusProposal struct {
	Name   string       `json:"name"`
	Line   [][2]float64 `json:"line"`
	Status string       `json:"status"` // always live-unverified until §3 passes
	Apply  string       `json:"apply"`
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

// proposeNativeBuses attaches a live-unverified native bus proposal to every
// complete lane (all members aligned, same direction). Report only.
func (e *aesEngine) proposeNativeBuses(p *powerLayoutPlan) {
	e.recordLanes(p)
	for i := range e.lanes {
		l := &e.lanes[i]
		if l.Markers < 2 || l.Aligned < 1 || l.SameDir < 1 || (l.Kind == "usb" || l.Kind == "mipi") {
			continue
		}
		sign := 1.0
		if l.Direction == "left" || l.Direction == "down" {
			sign = -1
		}
		lo, hi := math.MaxFloat64, -math.MaxFloat64
		for _, f := range p.Flags {
			for _, n := range l.Members {
				if f.Net == n && schBusLaneMarkerKind(f.Kind) {
					lo, hi = math.Min(lo, schLaneAlong(f)), math.Max(hi, schLaneAlong(f))
				}
			}
		}
		// The bus runs just beyond the label bodies (port box ≤ 8 + 6/char).
		reach := 0.0
		for _, n := range l.Members {
			reach = math.Max(reach, acPortTotalLen(n))
		}
		col := l.Column + sign*(math.Ceil((reach+10)/schAnchorGrid)*schAnchorGrid)
		line := [][2]float64{{col, lo}, {col, hi}}
		if l.Axis == "y" {
			line = [][2]float64{{lo, col}, {hi, col}}
		}
		l.Native = &SchematicNativeBusProposal{Name: l.Group, Line: line, Status: "live-unverified",
			Apply: "not applied; after the §3 live checklist passes: pcbpilot sch bus create --name '" + l.Group + "' --points …"}
	}
}
