package kicad

// schdestagger.go — de-stagger on a .kicad_sch: a marker (label or power
// symbol) that sits at the end of a straight stub from one pin and takes
// part in a quality finding (overlapping labels/texts/symbols, title block,
// off page) is re-placed — the stub turned to another direction and/or
// lengthened, the marker re-oriented along it — when that strictly reduces
// the findings and adds none (counted per key). The pin never moves, so the
// connectivity cannot change; callers still verify the netlist before
// writing (F14: the EasyEDA destagger merged a re-placed stub into a
// collinear neighbour — here a stub collinear with another wire is a
// wire-overlap / wire-end-on-wire finding, which the move may not add).

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// DestaggerMove is one re-placed stub.
type DestaggerMove struct {
	Pin      string  `json:"pin"`
	Marker   string  `json:"marker"`
	FromDir  string  `json:"fromDir"`
	FromLen  float64 `json:"fromLen"`
	ToDir    string  `json:"toDir"`
	ToLen    float64 `json:"toLen"`
	Findings int     `json:"findingsAfter"`
}

// DestaggerResult is the plan and the edited text.
type DestaggerResult struct {
	Before int             `json:"findingsBefore"`
	After  int             `json:"findingsAfter"`
	Moves  []DestaggerMove `json:"moves"`
	Stuck  []string        `json:"stuck,omitempty"` // involved markers no candidate improved
	Text   string          `json:"-"`
}

var dirName = [4]string{"right", "up", "left", "down"}

type dsStub struct {
	pin, marker string
	pinAt, end  Pt
	wire        *sexp
	item        connItem
	dir         int
	length      float64
}

func (e *SchEditor) stubs() []dsStub {
	items := e.connItems()
	wires := e.wireNodes()
	isl, _ := buildIslands(wires, items)
	var out []dsStub
	for _, is := range isl {
		if len(is.wires) != 1 || len(is.items) != 2 {
			continue
		}
		w := wires[is.wires[0]]
		if len(w.pts) != 2 {
			continue
		}
		var pin, mk *connItem
		for _, j := range is.items {
			it := items[j]
			switch it.kind {
			case "pin":
				pin = &it
			case "power", "label":
				mk = &it
			}
		}
		if pin == nil || mk == nil || !samePt(pin.at, w.pts[0]) && !samePt(pin.at, w.pts[1]) {
			continue
		}
		end := w.pts[0]
		if samePt(end, pin.at) {
			end = w.pts[1]
		}
		if !samePt(mk.at, end) {
			continue
		}
		dx, dy := end.X-pin.at.X, end.Y-pin.at.Y
		if dx != 0 && dy != 0 {
			continue
		}
		d := 0
		switch {
		case dx < 0:
			d = 2
		case dy < 0:
			d = 1
		case dy > 0:
			d = 3
		}
		name := ""
		if mk.kind == "power" {
			name = "power " + propVal(mk.node, "Value")
		} else if len(mk.node.list) > 1 {
			name = "label " + mk.node.list[1].atom
		}
		out = append(out, dsStub{pin: mk.ref, marker: name, pinAt: pin.at, end: end, wire: w.node, item: *mk, dir: d, length: math.Abs(dx + dy)})
		out[len(out)-1].pin = pin.ref
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].pin < out[j].pin })
	return out
}

func countKeys(fs []Finding) map[string]int {
	m := map[string]int{}
	for _, f := range fs {
		m[f.Key()]++
	}
	return m
}

// Destagger plans and applies (in text) up to maxMoves stub re-placements,
// greedily, re-reading the stubs after each one.
func Destagger(text string, maxMoves int) (DestaggerResult, error) {
	cur := text
	fs := CheckSchematic(cur, CheckOptions{})
	res := DestaggerResult{Before: len(fs)}
	stuck := map[string]bool{}
	for len(res.Moves) < maxMoves {
		improved := false
		e, err := OpenSchematic(cur)
		if err != nil {
			return res, err
		}
		for _, st := range e.stubs() {
			involved := false
			for _, f := range fs {
				involved = involved || strings.Contains(f.Subject, st.marker)
			}
			if !involved {
				continue
			}
			was := countKeys(fs)
			bestText, bestN := "", len(fs)
			var best DestaggerMove
			dirOrder := []int{st.dir}
			for d := 0; d < 4; d++ {
				if d != st.dir {
					dirOrder = append(dirOrder, d)
				}
			}
			for _, d := range dirOrder {
				for k := 2; k <= 12; k += 2 {
					l := float64(k) * SchGrid
					if d == st.dir && math.Abs(l-st.length) < 1e-3 {
						continue
					}
					t, err := restub(cur, st, d, l)
					if err != nil {
						return res, err
					}
					nfs := CheckSchematic(t, CheckOptions{})
					if len(nfs) >= bestN {
						continue
					}
					ok := true
					for key, n := range countKeys(nfs) {
						if n > was[key] {
							ok = false
							break
						}
					}
					if ok {
						bestText, bestN = t, len(nfs)
						best = DestaggerMove{Pin: st.pin, Marker: st.marker, FromDir: dirName[st.dir], FromLen: round4mm(st.length), ToDir: dirName[d], ToLen: round4mm(l), Findings: len(nfs)}
					}
				}
			}
			if bestText == "" {
				stuck[st.marker] = true
				continue
			}
			delete(stuck, st.marker)
			res.Moves = append(res.Moves, best)
			cur, improved = bestText, true
			fs = CheckSchematic(cur, CheckOptions{})
			break // the stubs moved: re-read them
		}
		if !improved {
			break
		}
	}
	for m := range stuck {
		res.Stuck = append(res.Stuck, m)
	}
	sort.Strings(res.Stuck)
	res.After, res.Text = len(fs), cur
	return res, nil
}

// restub re-places stub st along direction d with length l in text.
func restub(text string, st dsStub, d int, l float64) (string, error) {
	e, err := OpenSchematic(text)
	if err != nil {
		return "", err
	}
	// find the same nodes in this parse by position
	var wire *sexp
	var mk connItem
	found := false
	for _, n := range e.root.list {
		if n.head() == "wire" {
			pts := wirePts(n)
			if len(pts) == 2 && (samePt(pts[0], st.pinAt) && samePt(pts[1], st.end) || samePt(pts[1], st.pinAt) && samePt(pts[0], st.end)) {
				wire = n
			}
		}
	}
	for _, it := range e.connItems() {
		if it.kind == st.item.kind && samePt(it.at, st.end) {
			mk, found = it, true
		}
	}
	if wire == nil || !found {
		return "", fmt.Errorf("stub of %s not found", st.pin)
	}
	end := Pt{round4mm(st.pinAt.X + float64(dirs[d].X)*l), round4mm(st.pinAt.Y + float64(dirs[d].Y)*l)}
	for _, p := range wire.child("pts").list {
		if p.head() == "xy" && samePt(Pt{p.num(1), p.num(2)}, st.end) {
			e.repl = append(e.repl, textEdit{p.beg, p.end, fmt.Sprintf("(xy %s %s)", F(end.X), F(end.Y))})
		}
	}
	// rotate the marker so it reads away from the pin, then carry it to end
	want := normAngle(float64(d)*90 + 180) // its outward axis points back at the pin
	delta := normAngle(want - mk.outward)
	a := delta * math.Pi / 180
	c, s := math.Round(math.Cos(a)), math.Round(math.Sin(a))
	// p' = end + R(delta)(p - st.end)
	t := xform{A: c, B: s, C: -s, D: c}
	t.TX = end.X - (c*st.end.X + s*st.end.Y)
	t.TY = end.Y - (-s*st.end.X + c*st.end.Y)
	var dr DragResult
	e.moveMarker(mk, t, &dr)
	return e.Render()
}
