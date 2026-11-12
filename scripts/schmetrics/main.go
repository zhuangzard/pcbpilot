// Command schmetrics measures how a `kicad sch-build` project is drawn:
// paper per page, fill, zone methods, and — read from the sheets themselves —
// how pins are joined (real wires vs net labels / supply symbols) and how
// many small parts sit wired to the pin they serve.
//
//	go run ./scripts/schmetrics --out build/ --report rep.json [--json]
//
// It is a regression tool for the sch-build layout (docs/kicad/sch-build.md):
// compare two builds of one spec by their summaries.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

type pageM struct {
	Sheet     string  `json:"sheet"`
	Paper     string  `json:"paper"`
	AreaM2    float64 `json:"areaM2"`
	Fill      float64 `json:"fillRatio"` // engineer-grade gate's fill (content bbox / drawing area)
	Occupancy float64 `json:"occupancy"` // Σ symbol boxes / drawing area
	Parts     int     `json:"parts"`
	Wires     int     `json:"wires"`
	Labels    int     `json:"labels"`
	PowerSyms int     `json:"powerSymbols"`
}

type result struct {
	Board         string            `json:"board"`
	OK            bool              `json:"ok"`
	Gates         map[string]string `json:"gates"`
	Pages         []pageM           `json:"pages"`
	PageCount     int               `json:"pageCount"`
	PaperAreaM2   float64           `json:"paperAreaM2"`
	FillMedian    float64           `json:"fillMedian"`
	Methods       map[string]int    `json:"methods"`
	Parts         int               `json:"parts"`
	MM2PerPart    float64           `json:"mm2PerPart"`
	SigConns      int               `json:"signalConnections"` // Σ(pins-1) over signal nets per sheet
	SigWired      int               `json:"signalWired"`       // of which joined by wire
	PwrConns      int               `json:"supplyConnections"`
	PwrWired      int               `json:"supplyWired"`
	SigLabelShare float64           `json:"signalLabelShare"`
	AllLabelShare float64           `json:"allLabelShare"`
	Small         int               `json:"smallParts"`   // ≤ 3-pin parts with a connected signal pin
	SmallWired    int               `json:"smallWired"`   // wired to another part's pin
	SmallWiredIC  int               `json:"smallWiredIC"` // wired to a pin of a part with > 3 pins (directly or via another small part)
	AttachedShare float64           `json:"attachedShare"`
	WireMM        float64           `json:"wireMm"`
	LongWires     int               `json:"wiresOver50mm"`
	Crossings     int               `json:"crossings"`
	Zones         []*zoneM          `json:"zones,omitempty"`
}

// zoneM is one zone's in-zone connection statistics (pins of the zone's
// own parts only), read from the sheet.
type zoneM struct {
	ID         string  `json:"id"`
	Page       string  `json:"page"`
	Parts      int     `json:"parts"`
	Method     string  `json:"method"`
	Variant    string  `json:"variant,omitempty"`
	SigConns   int     `json:"signalConnections"`
	SigWired   int     `json:"signalWired"`
	LabelShare float64 `json:"signalLabelShare"`
	Small      int     `json:"smallParts"`
	SmallWired int     `json:"smallWired"`
	Attached   float64 `json:"attachedShare"`
	Density    float64 `json:"density"` // Σ symbol boxes / zone box
	LongestMM  float64 `json:"longestWireMm"`
	AreaMM2    float64 `json:"areaMm2"`
	boxArea    float64
	symArea    float64
}

var (
	zoneOf  = map[string]*zoneM{} // ref → zone
	zoneAll []*zoneM
)

func main() {
	out := flag.String("out", "", "sch-build output directory")
	rep := flag.String("report", "", "sch-build --report JSON")
	board := flag.String("board", "", "label")
	asJSON := flag.Bool("json", false, "print JSON")
	flag.Parse()
	r, err := measure(*out, *rep)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	r.Board = *board
	if *asJSON {
		b, _ := json.MarshalIndent(r, "", " ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("%s ok=%v pages=%d paper=%.3fm² fillMedian=%.2f sigLabel=%.2f allLabel=%.2f attached=%.2f mm²/part=%.0f methods=%v\n",
		r.Board, r.OK, r.PageCount, r.PaperAreaM2, r.FillMedian, r.SigLabelShare, r.AllLabelShare, r.AttachedShare, r.MM2PerPart, r.Methods)
}

func measure(out, rep string) (*result, error) {
	r := &result{Gates: map[string]string{}, Methods: map[string]int{}}
	fills := map[string]float64{}
	if rep != "" {
		b, err := os.ReadFile(rep)
		if err != nil {
			return nil, err
		}
		var d struct {
			OK    bool `json:"ok"`
			Gates []struct {
				Name, Status string
				Data         json.RawMessage
			} `json:"gates"`
			Zones []struct {
				ID, Page, Method, Variant string
				Parts                     []string
				Box                       []float64 `json:"boxMm"`
			} `json:"zones"`
		}
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, err
		}
		r.OK = d.OK
		for _, g := range d.Gates {
			r.Gates[g.Name] = g.Status
			if g.Name == "engineer-grade" {
				var eg struct {
					Pages []struct {
						Sheet     string  `json:"sheet"`
						FillRatio float64 `json:"fillRatio"`
					} `json:"pages"`
				}
				_ = json.Unmarshal(g.Data, &eg)
				for _, p := range eg.Pages {
					fills[p.Sheet] = p.FillRatio
				}
			}
		}
		for _, z := range d.Zones {
			r.Methods[z.Method]++
			zm := &zoneM{ID: z.ID, Page: z.Page, Parts: len(z.Parts), Method: z.Method, Variant: z.Variant}
			if len(z.Box) == 4 {
				zm.boxArea = (z.Box[2] - z.Box[0]) * (z.Box[3] - z.Box[1])
				zm.AreaMM2 = math.Round(zm.boxArea)
			}
			for _, ref := range z.Parts {
				zoneOf[ref] = zm
			}
			zoneAll = append(zoneAll, zm)
		}
	}
	files, _ := filepath.Glob(filepath.Join(out, "*.kicad_sch"))
	sort.Strings(files)
	var fl []float64
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		e, err := kicad.OpenSchematic(string(raw))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		sc, err := e.Scene()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		pm := sheet(r, sc)
		pm.Sheet = filepath.Base(f)
		if pm.Parts == 0 {
			continue // hierarchy root
		}
		if v, ok := fills[pm.Sheet]; ok {
			pm.Fill = v
			fl = append(fl, v)
		}
		r.Pages = append(r.Pages, pm)
		r.PaperAreaM2 += pm.AreaM2
		r.Parts += pm.Parts
	}
	for _, z := range zoneAll {
		if z.SigConns > 0 {
			z.LabelShare = round2(1 - float64(z.SigWired)/float64(z.SigConns))
		}
		z.Attached = 1
		if z.Small > 0 {
			z.Attached = round2(float64(z.SmallWired) / float64(z.Small))
		}
		if z.boxArea > 0 {
			z.Density = round2(z.symArea / z.boxArea)
		}
		z.LongestMM = round2(z.LongestMM)
	}
	r.Zones = zoneAll
	r.PageCount = len(r.Pages)
	sort.Float64s(fl)
	if n := len(fl); n > 0 {
		if n%2 == 1 {
			r.FillMedian = fl[n/2]
		} else {
			r.FillMedian = (fl[n/2-1] + fl[n/2]) / 2
		}
	}
	if r.Parts > 0 {
		r.MM2PerPart = r.PaperAreaM2 * 1e6 / float64(r.Parts)
	}
	if r.SigConns > 0 {
		r.SigLabelShare = 1 - float64(r.SigWired)/float64(r.SigConns)
	}
	if c := r.SigConns + r.PwrConns; c > 0 {
		r.AllLabelShare = 1 - float64(r.SigWired+r.PwrWired)/float64(c)
	}
	if r.Small > 0 {
		r.AttachedShare = float64(r.SmallWired) / float64(r.Small)
	}
	return r, nil
}

// sheet adds one sheet's connection statistics to r.
func sheet(r *result, sc *kicad.SchScene) pageM {
	pm := pageM{Paper: sc.Paper}
	for _, p := range kicad.Papers {
		if p.Name == sc.Paper {
			pm.AreaM2 = p.W * p.H / 1e6
		}
	}
	// union-find over: wires (segments), symbol pins, labels
	type node struct{ kind, ref, name string } // kind: wire | pin | power | label
	var nodes []node
	parent := []int{}
	add := func(n node) int {
		nodes = append(nodes, n)
		parent = append(parent, len(parent))
		return len(nodes) - 1
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) { parent[find(a)] = find(b) }
	onSeg := func(p kicad.Pt, s [2]kicad.Pt) bool {
		const e = 1e-3
		if math.Abs(s[0].X-s[1].X) < e {
			return math.Abs(p.X-s[0].X) < e && p.Y >= math.Min(s[0].Y, s[1].Y)-e && p.Y <= math.Max(s[0].Y, s[1].Y)+e
		}
		if math.Abs(s[0].Y-s[1].Y) < e {
			return math.Abs(p.Y-s[0].Y) < e && p.X >= math.Min(s[0].X, s[1].X)-e && p.X <= math.Max(s[0].X, s[1].X)+e
		}
		return p == s[0] || p == s[1]
	}
	wi := make([]int, len(sc.Wires))
	for i, w := range sc.Wires {
		wi[i] = add(node{kind: "wire"})
		pm.Wires++
		l := math.Hypot(w[0].X-w[1].X, w[0].Y-w[1].Y)
		r.WireMM += l
		if l > 50 {
			r.LongWires++
		}
	}
	for i := range sc.Wires {
		for j := i + 1; j < len(sc.Wires); j++ {
			a, b := sc.Wires[i], sc.Wires[j]
			if onSeg(a[0], b) || onSeg(a[1], b) || onSeg(b[0], a) || onSeg(b[1], a) {
				union(wi[i], wi[j])
			} else if strictCross(a, b) {
				r.Crossings++
			}
		}
	}
	attachPt := func(id int, p kicad.Pt, pts map[kicad.Pt]int) {
		for i, w := range sc.Wires {
			if onSeg(p, w) {
				union(id, wi[i])
			}
		}
		if o, ok := pts[p]; ok {
			union(id, o)
		} else {
			pts[p] = id
		}
	}
	pts := map[kicad.Pt]int{}
	pinCount := map[string]int{} // pins per part on this sheet
	small := map[string]bool{}
	type pinRef struct {
		id  int
		ref string
	}
	var partPins []pinRef
	for _, s := range sc.Symbols {
		if s.Power {
			if strings.HasPrefix(s.Ref, "#FLG") {
				continue
			}
			pm.PowerSyms++
			for _, q := range s.Pins {
				id := add(node{kind: "power", name: valueOf(s)})
				attachPt(id, q.At, pts)
			}
			continue
		}
		if s.HasBox {
			pm.Occupancy += s.Box.W() * s.Box.H()
		}
		pinCount[s.Ref] += len(s.Pins)
	}
	parts := map[string]bool{}
	for _, s := range sc.Symbols {
		if s.Power {
			continue
		}
		parts[s.Ref] = true
		for _, q := range s.Pins {
			id := add(node{kind: "pin", ref: s.Ref})
			attachPt(id, q.At, pts)
			partPins = append(partPins, pinRef{id, s.Ref})
		}
	}
	pm.Parts = len(parts)
	for _, l := range sc.Labels {
		pm.Labels++
		id := add(node{kind: "label", name: l.Name})
		attachPt(id, l.At, pts)
	}
	for ref, n := range pinCount {
		if n <= 3 {
			small[ref] = true
		}
	}
	nc := map[kicad.Pt]bool{}
	for _, p := range sc.NoConnects {
		nc[p] = true
	}
	// islands
	type island struct {
		pins   int
		refs   map[string]int
		names  map[string]bool
		supply bool
	}
	isl := map[int]*island{}
	get := func(i int) *island {
		root := find(i)
		if isl[root] == nil {
			isl[root] = &island{refs: map[string]int{}, names: map[string]bool{}}
		}
		return isl[root]
	}
	for i, n := range nodes {
		switch n.kind {
		case "pin":
			x := get(i)
			x.pins++
			x.refs[n.ref]++
		case "power":
			x := get(i)
			x.names["PWR:"+n.name] = true
			x.supply = true
		case "label":
			get(i).names[n.name] = true
		}
	}
	// group pin islands by net name (label / supply value); an unnamed island
	// is its own net
	type grp struct {
		pins, islands int
		supply        bool
	}
	groups := map[string]*grp{}
	for root, x := range isl {
		if x.pins == 0 {
			continue
		}
		key := fmt.Sprintf("#%d", root)
		var names []string
		for n := range x.names {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) > 0 {
			key = names[0]
		}
		g := groups[key]
		if g == nil {
			g = &grp{}
			groups[key] = g
		}
		g.pins += x.pins
		g.islands++
		g.supply = g.supply || x.supply
	}
	for _, g := range groups {
		if g.pins < 2 {
			continue
		}
		conns, wired := g.pins-1, g.pins-g.islands
		if g.supply {
			r.PwrConns += conns
			r.PwrWired += wired
		} else {
			r.SigConns += conns
			r.SigWired += wired
		}
	}
	// per zone: the same counts over the zone's own pins
	keyOf := func(root int) string {
		x := isl[root]
		var names []string
		for n := range x.names {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) > 0 {
			return names[0]
		}
		return fmt.Sprintf("#%d", root)
	}
	type zg struct {
		pins   int
		isl    map[int]bool
		supply bool
	}
	zgs := map[*zoneM]map[string]*zg{}
	islLong := map[int]float64{}
	for i, w := range sc.Wires {
		root := find(wi[i])
		islLong[root] = math.Max(islLong[root], math.Hypot(w[0].X-w[1].X, w[0].Y-w[1].Y))
	}
	for i, n := range nodes {
		if n.kind != "pin" || zoneOf[n.ref] == nil {
			continue
		}
		z, root := zoneOf[n.ref], find(i)
		if zgs[z] == nil {
			zgs[z] = map[string]*zg{}
		}
		k := keyOf(root)
		g := zgs[z][k]
		if g == nil {
			g = &zg{isl: map[int]bool{}}
			zgs[z][k] = g
		}
		g.pins++
		g.isl[root] = true
		g.supply = g.supply || isl[root].supply
		z.LongestMM = math.Max(z.LongestMM, islLong[root])
	}
	for z, m := range zgs {
		for _, g := range m {
			if g.pins < 2 || g.supply {
				continue
			}
			z.SigConns += g.pins - 1
			z.SigWired += g.pins - len(g.isl)
		}
	}
	for _, s := range sc.Symbols {
		if z := zoneOf[s.Ref]; z != nil && !s.Power && s.HasBox {
			z.symArea += s.Box.W() * s.Box.H()
		}
	}
	// small parts: wired to another part's pin; to an IC (> 3 pins) through
	// at most one other small part (a chain pin → R → C)
	partIsl := map[string]map[int]bool{}
	for _, pr := range partPins {
		if partIsl[pr.ref] == nil {
			partIsl[pr.ref] = map[int]bool{}
		}
		partIsl[pr.ref][find(pr.id)] = true
	}
	icIsl := func(root int, self string) (other, ic bool) {
		for ref := range isl[root].refs {
			if ref == self {
				continue
			}
			other = true
			if !small[ref] {
				ic = true
			}
		}
		return
	}
	for ref := range small {
		// a part with a connected SIGNAL pin: a decoupling cap or a bleeder
		// between two supplies is drawn with supply symbols by design
		connected := false
		for root := range partIsl[ref] {
			x := isl[root]
			if !x.supply && (len(x.names) > 0 || x.pins > 1) {
				connected = true
			}
		}
		if !connected {
			continue
		}
		r.Small++
		zsm := zoneOf[ref]
		if zsm != nil {
			zsm.Small++
		}
		wired, ic := false, false
		for root := range partIsl[ref] {
			o, i := icIsl(root, ref)
			wired = wired || o
			ic = ic || i
			if o && !i { // one hop through another small part
				for nb := range isl[root].refs {
					if nb == ref || !small[nb] {
						continue
					}
					for r2 := range partIsl[nb] {
						if r2 != root {
							if _, i2 := icIsl(r2, nb); i2 {
								ic = true
							}
						}
					}
				}
			}
		}
		if wired {
			r.SmallWired++
			if zsm != nil {
				zsm.SmallWired++
			}
		}
		if ic {
			r.SmallWiredIC++
		}
	}
	if sc.Page != nil {
		if a := sc.Page.W() * sc.Page.H(); a > 0 {
			pm.Occupancy = math.Round(pm.Occupancy/a*100) / 100
		}
	}
	return pm
}

func valueOf(s kicad.SceneSymbol) string {
	for _, f := range s.Fields {
		if f.Name == "Value" {
			return f.Value
		}
	}
	return s.LibID
}

func strictCross(a, b [2]kicad.Pt) bool {
	ah, bh := a[0].Y == a[1].Y, b[0].Y == b[1].Y
	if ah == bh {
		return false
	}
	if !ah {
		a, b = b, a
	}
	// a horizontal, b vertical
	x, y := b[0].X, a[0].Y
	return x > math.Min(a[0].X, a[1].X)+1e-6 && x < math.Max(a[0].X, a[1].X)-1e-6 &&
		y > math.Min(b[0].Y, b[1].Y)+1e-6 && y < math.Max(b[0].Y, b[1].Y)-1e-6
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
