package app

// kicad_sch_build_eg.go — the engineer-grade gate of `kicad sch-build`
// (docs/research/engineer-grade-schematic-2026-10-09.md, rules EG-xx): per
// page metrics with thresholds, measured on the written sheets. Hard rules
// fail the build (transactional, like the netlist gate); soft rules are
// reported.

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// sbEGPage is one sheet's metrics.
type sbEGPage struct {
	Sheet           string   `json:"sheet"`
	PowerSymbols    int      `json:"powerSymbols"`
	PowerInverted   int      `json:"powerInverted"`   // EG-05 hard 0
	Crossings       int      `json:"crossings"`       // EG-08 hard ≤ 1
	JunctionDeg4    int      `json:"junctionDegree4"` // EG-09 hard 0
	DanglingLabels  int      `json:"danglingLabels"`  // EG-13 hard 0
	AutoNetNames    []string `json:"autoNetNames,omitempty"`
	LongWires       int      `json:"wiresOver50mm"`           // soft
	AvgBends        float64  `json:"avgBendsPerWire"`         // soft ≤ 2
	FillRatio       float64  `json:"fillRatio"`               // soft
	Decoupling      []string `json:"decouplingFar,omitempty"` // EG-06 hard: cap > 25.4 mm from its IC supply pin
	DecouplingCount int      `json:"decouplingCaps"`
	Flow            string   `json:"signalFlow"` // soft: driver left of receiver
	Hard            []string `json:"hard,omitempty"`
}

var (
	sbAutoName = regexp.MustCompile(`^Net-\(|_N\d+$|^N\$\d+`)
	sbJuncRe   = regexp.MustCompile(`\(junction\s*\(at ([-\d.]+) ([-\d.]+)\)`)
)

// sbEngineerGrade measures every sheet of the staged project.
func sbEngineerGrade(dir string, files []string, d *sbDesign) sbGate {
	specNames := map[string]bool{}
	for _, n := range d.Spec.Nets {
		specNames[n.Name] = true
	}
	var pages []sbEGPage
	hard := 0
	for _, f := range files {
		if !strings.HasSuffix(f, ".kicad_sch") {
			continue
		}
		p, err := sbSafeJoin(dir, f)
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		pg, ok := sbEGPageOf(f, string(raw), d, specNames)
		if !ok {
			continue
		}
		hard += len(pg.Hard)
		pages = append(pages, pg)
	}
	// EG-22: title block filled
	var tbMissing []string
	tb := d.Spec.Title
	for k, v := range map[string]string{"title": tb.Title, "date": tb.Date, "rev": tb.Rev, "company": tb.Company} {
		if strings.TrimSpace(v) == "" {
			tbMissing = append(tbMissing, k)
		}
	}
	sort.Strings(tbMissing)
	data := map[string]any{"pages": pages}
	var msgs []string
	for _, pg := range pages {
		for _, h := range pg.Hard {
			msgs = append(msgs, pg.Sheet+": "+h)
		}
	}
	if len(tbMissing) > 0 {
		hard++
		msgs = append(msgs, "EG-22 title block lacks "+strings.Join(tbMissing, ", "))
	}
	if hard > 0 {
		return sbGate{Name: "engineer-grade", Status: "fail", Detail: strings.Join(msgs, "; "), Data: data}
	}
	var sum []string
	for _, pg := range pages {
		sum = append(sum, fmt.Sprintf("%s: %d supply/ground symbols upright, %d crossings, %d decoupling caps ≤ 25.4 mm, fill %.2f, flow %s",
			pg.Sheet, pg.PowerSymbols, pg.Crossings, pg.DecouplingCount, pg.FillRatio, pg.Flow))
	}
	return sbGate{Name: "engineer-grade", Status: "pass", Detail: strings.Join(sum, "; "), Data: data}
}

// sbEGPageOf measures one sheet (false: no parts on it).
func sbEGPageOf(f, raw string, d *sbDesign, specNames map[string]bool) (sbEGPage, bool) {
	e, err := kicad.OpenSchematic(raw)
	if err != nil {
		return sbEGPage{}, false
	}
	sc, err := e.Scene()
	if err != nil {
		return sbEGPage{}, false
	}
	pg := sbEGPage{Sheet: f}
	hasParts := false
	for _, s := range sc.Symbols {
		if !s.Power {
			hasParts = true
		}
	}
	if !hasParts { // the root of a hierarchy: sheet symbols only
		return pg, false
	}
	// EG-05: supply symbols up, ground down (rotation 0 for both)
	for _, s := range sc.Symbols {
		if !s.Power || strings.HasPrefix(s.Ref, "#FLG") {
			continue
		}
		pg.PowerSymbols++
		if math.Mod(s.Rot+360, 360) != 0 {
			pg.PowerInverted++
		}
	}
	// wires: crossings, long runs, bends
	segs := sc.Wires
	for i := 0; i < len(segs); i++ {
		for j := i + 1; j < len(segs); j++ {
			if sbStrictCross(segs[i], segs[j]) {
				pg.Crossings++
			}
		}
		if math.Hypot(segs[i][0].X-segs[i][1].X, segs[i][0].Y-segs[i][1].Y) > 50 {
			pg.LongWires++
		}
	}
	pg.AvgBends = sbAvgBends(segs)
	// EG-09: junction degree
	ends := map[kicad.Pt]int{}
	for _, s := range segs {
		ends[s[0]]++
		ends[s[1]]++
	}
	for _, m := range sbJuncRe.FindAllStringSubmatch(raw, -1) {
		var x, y float64
		fmt.Sscanf(m[1]+" "+m[2], "%g %g", &x, &y)
		pt := kicad.Pt{X: x, Y: y}
		deg := ends[pt]
		for _, s := range segs {
			if sbOnInterior(pt, s) {
				deg += 2
			}
		}
		if deg >= 4 {
			pg.JunctionDeg4++
		}
	}
	// EG-13: a label sits on a wire or a pin; EG-20: no automatic names
	pins := map[kicad.Pt]bool{}
	for _, s := range sc.Symbols {
		for _, q := range s.Pins {
			pins[q.At] = true
		}
	}
	for _, l := range sc.Labels {
		on := pins[l.At] || ends[l.At] > 0
		for _, s := range segs {
			on = on || sbOnInterior(l.At, s)
		}
		if !on {
			pg.DanglingLabels++
		}
		if sbAutoName.MatchString(l.Name) && !specNames[l.Name] {
			pg.AutoNetNames = append(pg.AutoNetNames, l.Name)
		}
	}
	// EG-06: decoupling caps near the supply pin of an IC
	pinAt := map[string]kicad.Pt{}
	boxes := map[string]kicad.Box{}
	for _, s := range sc.Symbols {
		if s.Power {
			continue
		}
		boxes[s.Ref] = s.Box
		for _, q := range s.Pins {
			pinAt[sbPinKey(s.Ref, q.Number)] = q.At
		}
	}
	for ref, b := range boxes {
		p := d.ByRef[ref]
		if p == nil || sbRefPrefix(ref) != "C" || len(p.LPins) != 2 {
			continue
		}
		var rail string
		gnd := false
		for _, q := range p.LPins {
			switch n := d.PinNet[sbPinKey(ref, q.Number)]; d.NetKind[n] {
			case "power":
				rail = n
			case "ground":
				gnd = true
			}
		}
		if rail == "" || !gnd {
			continue
		}
		best := math.Inf(1)
		for _, k := range d.netPins(rail) {
			r, _, _ := strings.Cut(k, ".")
			ic := d.ByRef[r]
			at, ok := pinAt[k]
			if ic == nil || !ok || len(ic.LPins) < 3 || ic.ZoneID != p.ZoneID {
				continue
			}
			best = math.Min(best, sbBoxPointDist(b, at))
		}
		if math.IsInf(best, 1) {
			continue // no IC on this sheet/zone uses the rail
		}
		pg.DecouplingCount++
		if best > 25.4 {
			pg.Decoupling = append(pg.Decoupling, fmt.Sprintf("%s %.1f mm from its %s pin", ref, best, rail))
		}
	}
	sort.Strings(pg.Decoupling)
	// soft: signal flow (typed drivers left of receivers), fill ratio
	pg.Flow = sbFlow(d, pinAt)
	if cb, ok := sbContent(sc); ok && sc.TitleBlock != nil {
		pw, ph := sc.TitleBlock.MaxX+10, sc.TitleBlock.MaxY+10
		area := (pw - 20) * (ph - 20)
		if area > 0 {
			pg.FillRatio = math.Round(cb.W()*cb.H()/area*100) / 100
		}
	}
	if pg.PowerInverted > 0 {
		pg.Hard = append(pg.Hard, fmt.Sprintf("EG-05 %d inverted power/ground symbol(s)", pg.PowerInverted))
	}
	if pg.Crossings > 1 {
		pg.Hard = append(pg.Hard, fmt.Sprintf("EG-08 %d wire crossings (≤ 1)", pg.Crossings))
	}
	if pg.JunctionDeg4 > 0 {
		pg.Hard = append(pg.Hard, fmt.Sprintf("EG-09 %d four-way junction(s)", pg.JunctionDeg4))
	}
	if pg.DanglingLabels > 0 {
		pg.Hard = append(pg.Hard, fmt.Sprintf("EG-13 %d dangling label(s)", pg.DanglingLabels))
	}
	if len(pg.AutoNetNames) > 0 {
		pg.Hard = append(pg.Hard, "EG-20 automatic net names: "+strings.Join(pg.AutoNetNames, ", "))
	}
	if len(pg.Decoupling) > 0 {
		pg.Hard = append(pg.Hard, "EG-06 decoupling > 25.4 mm: "+strings.Join(pg.Decoupling, "; "))
	}
	return pg, true
}

// sbStrictCross: two axis-aligned segments cross at a point interior to both.
func sbStrictCross(a, b [2]kicad.Pt) bool {
	ah, bh := math.Abs(a[0].Y-a[1].Y) < 1e-6, math.Abs(b[0].Y-b[1].Y) < 1e-6
	if ah == bh {
		return false
	}
	if !ah {
		a, b = b, a
	}
	// a horizontal, b vertical
	x, y := b[0].X, a[0].Y
	in := func(v, p, q float64) bool { return v > math.Min(p, q)+1e-6 && v < math.Max(p, q)-1e-6 }
	return in(x, a[0].X, a[1].X) && in(y, b[0].Y, b[1].Y)
}

// sbAvgBends: polyline corners per connected wire chain.
func sbAvgBends(segs [][2]kicad.Pt) float64 {
	if len(segs) == 0 {
		return 0
	}
	ends := map[kicad.Pt][]int{}
	for i, s := range segs {
		ends[s[0]] = append(ends[s[0]], i)
		ends[s[1]] = append(ends[s[1]], i)
	}
	bends, chains := 0, 0
	for _, ix := range ends {
		if len(ix) == 2 {
			a, b := segs[ix[0]], segs[ix[1]]
			ah, bh := math.Abs(a[0].Y-a[1].Y) < 1e-6, math.Abs(b[0].Y-b[1].Y) < 1e-6
			if ah != bh {
				bends++
			}
		}
		if len(ix) == 1 {
			chains++
		}
	}
	if chains < 2 {
		chains = 2
	}
	return math.Round(float64(bends)/float64(chains/2)*100) / 100
}

func sbBoxPointDist(b kicad.Box, p kicad.Pt) float64 {
	dx := math.Max(0, math.Max(b.MinX-p.X, p.X-b.MaxX))
	dy := math.Max(0, math.Max(b.MinY-p.Y, p.Y-b.MaxY))
	return math.Hypot(dx, dy)
}

// sbFlow: share of signal nets whose typed driver pin (output / power_out)
// sits left of their receivers ("n/a" when no net has a typed driver).
func sbFlow(d *sbDesign, pinAt map[string]kicad.Pt) string {
	typ := map[string]string{}
	for _, p := range d.Parts {
		for _, q := range p.LPins {
			typ[sbPinKey(p.Ref, q.Number)] = q.Type
		}
	}
	ok, all := 0, 0
	for _, n := range d.Nets {
		if d.NetKind[n.Name] != "signal" {
			continue
		}
		var drv, rcv []kicad.Pt
		for _, k := range n.Pins {
			at, on := pinAt[k]
			if !on {
				continue
			}
			switch typ[k] {
			case "output", "power_out":
				drv = append(drv, at)
			case "input":
				rcv = append(rcv, at)
			}
		}
		if len(drv) == 1 && len(rcv) > 0 {
			all++
			left := true
			for _, r := range rcv {
				left = left && drv[0].X <= r.X
			}
			if left {
				ok++
			}
		}
	}
	if all == 0 {
		return "n/a (no typed driver)"
	}
	return fmt.Sprintf("%d/%d", ok, all)
}

// sbContent is the extent of the symbols, wires and labels on a sheet.
func sbContent(sc *kicad.SchScene) (kicad.Box, bool) {
	b := kicad.Box{MinX: math.Inf(1), MinY: math.Inf(1), MaxX: math.Inf(-1), MaxY: math.Inf(-1)}
	add := func(x kicad.Box) {
		b.MinX, b.MinY = math.Min(b.MinX, x.MinX), math.Min(b.MinY, x.MinY)
		b.MaxX, b.MaxY = math.Max(b.MaxX, x.MaxX), math.Max(b.MaxY, x.MaxY)
	}
	for _, s := range sc.Symbols {
		if s.HasBox {
			add(s.Box)
		}
	}
	for _, w := range sc.Wires {
		add(kicad.Box{MinX: math.Min(w[0].X, w[1].X), MinY: math.Min(w[0].Y, w[1].Y), MaxX: math.Max(w[0].X, w[1].X), MaxY: math.Max(w[0].Y, w[1].Y)})
	}
	for _, l := range sc.Labels {
		add(l.Box)
	}
	return b, !math.IsInf(b.MinX, 1)
}
