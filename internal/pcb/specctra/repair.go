package specctra

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Track is a copper track as pcb.line.list reports it (mil, EasyEDA layer id).
type Track struct {
	ID     string  `json:"primitiveId"`
	Net    string  `json:"net"`
	Layer  int     `json:"layer"`
	X1     float64 `json:"startX"`
	Y1     float64 `json:"startY"`
	X2     float64 `json:"endX"`
	Y2     float64 `json:"endY"`
	Width  float64 `json:"lineWidth"`
	Locked bool    `json:"locked"`
}

// NewTrack is a track to create.
type NewTrack struct {
	Net   string  `json:"net"`
	Layer int     `json:"layer"`
	X1    float64 `json:"startX"`
	Y1    float64 `json:"startY"`
	X2    float64 `json:"endX"`
	Y2    float64 `json:"endY"`
	Width float64 `json:"lineWidth"`
}

// NewVia is a via to create.
type NewVia struct {
	Net         string  `json:"net"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	DiameterMil float64 `json:"diameter,omitempty"`
}

// TrackFix replaces one imported track with the routed pieces it stands for.
type TrackFix struct {
	Delete Track      `json:"delete"`
	Create []NewTrack `json:"create"`
	Reason string     `json:"reason"`
}

// RepairPlan is everything the SES import repair would change.
type RepairPlan struct {
	Fixes []TrackFix `json:"fixes"`
	// LayerMoves / WidthRestores count fixes by cause (a fix can be both).
	LayerMoves    int `json:"layerMoves"`
	WidthRestores int `json:"widthRestores"`
	// Unmatched are unlocked tracks no session segment explains; they are
	// left untouched and reported.
	Unmatched []Track `json:"unmatched,omitempty"`
}

// MatchTolMil is how far (mil) a session segment end may sit off an imported
// track and still count as part of it. EasyEDA snaps endpoints slightly when
// it merges collinear pieces.
const MatchTolMil = 0.6

// widthEpsMil ignores width differences from float round-trips.
const widthEpsMil = 0.1

// LayerID maps a Specctra layer name to the EasyEDA copper layer id
// (TopLayer=1, BottomLayer=2, InnerN=14+N).
func LayerID(name string) (int, bool) {
	switch name {
	case "TopLayer":
		return 1, true
	case "BottomLayer":
		return 2, true
	}
	if k, err := strconv.Atoi(strings.TrimPrefix(name, "Inner")); err == nil && strings.HasPrefix(name, "Inner") && k >= 1 {
		return 14 + k, true
	}
	return 0, false
}

// PlanImportRepair compares the tracks EasyEDA created from a session with
// the session itself and plans the fixes for two importer defects:
//
//   - inner-layer tracks land on the wrong layer id (Inner1/Inner2 → 21/22
//     instead of 15/16): the track is recreated on the session's layer;
//   - neck-down widths are replaced by the net-rule width and collinear
//     same-net pieces are merged keeping the widest: the merged track is
//     deleted and recreated piece by piece with the routed widths.
//
// Where session pieces overlap, the narrower width wins. If the matched
// pieces do not cover the whole track, it is recreated as one piece with the
// narrowest matched width.
func PlanImportRepair(tracks []Track, ses *Wiring) RepairPlan {
	byNet := map[string][]seg{}
	for _, s := range ses.Segments {
		id, ok := LayerID(s.Layer)
		if !ok {
			continue
		}
		k := strings.ToUpper(s.Net)
		byNet[k] = append(byNet[k], seg{s, id})
	}
	var plan RepairPlan
	for _, t := range tracks {
		if t.Locked {
			continue
		}
		a, b := [2]float64{t.X1, t.Y1}, [2]float64{t.X2, t.Y2}
		length := math.Hypot(b[0]-a[0], b[1]-a[1])
		if length < 1e-9 {
			continue
		}
		var same, any []seg
		for _, s := range byNet[strings.ToUpper(t.Net)] {
			if pointSegDist(s.A, a, b) <= MatchTolMil && pointSegDist(s.B, a, b) <= MatchTolMil {
				any = append(any, s)
				if s.layer == t.Layer {
					same = append(same, s)
				}
			}
		}
		target := t.Layer
		cands := same
		if len(cands) == 0 {
			// Wrong layer id. The layer is the one of the segment whose two
			// ends coincide with the track; failing that, the only layer
			// any matching segment is on. (Same-net copper often overlaps
			// on another layer, e.g. a Top GND stub above an Inner1 trunk.)
			exact := exactSegments(any, a, b, -1)
			layer, ok := uniqueLayer(exact)
			if !ok {
				layer, ok = uniqueLayer(any)
			}
			if !ok {
				// The same segment on two inner layers (Gas Module v9:
				// SV3_DRV on Inner1 and Inner2, imported to 21 and 22):
				// EasyEDA's importer puts InnerN on 20+N, so the wrong id
				// itself names the layer.
				layer, ok = importerLayer(t.Layer, exact)
			}
			if !ok {
				plan.Unmatched = append(plan.Unmatched, t)
				continue
			}
			target = layer
			for _, s := range any {
				if s.layer == layer {
					cands = append(cands, s)
				}
			}
		}
		// A track that is exactly one session segment (same ends, layer and
		// width) was imported faithfully, even if an overlapping segment of
		// another width exists (fastroute can emit both).
		if target == t.Layer && len(exactSegments(cands, a, b, t.Width)) > 0 {
			continue
		}
		type piece struct{ t0, t1, w float64 }
		var ps []piece
		for _, s := range cands {
			t0, t1 := project(s.A, a, b), project(s.B, a, b)
			if t0 > t1 {
				t0, t1 = t1, t0
			}
			ps = append(ps, piece{math.Max(0, t0), math.Min(length, t1), s.WidthMil})
		}
		// Elementary intervals between all piece ends; each takes the
		// narrowest piece covering it.
		cuts := []float64{0, length}
		for _, p := range ps {
			cuts = append(cuts, p.t0, p.t1)
		}
		sort.Float64s(cuts)
		type span struct{ t0, t1, w float64 }
		var spans []span
		covered := true
		minW := math.Inf(1)
		for _, p := range ps {
			minW = math.Min(minW, p.w)
		}
		for i := 0; i+1 < len(cuts); i++ {
			t0, t1 := cuts[i], cuts[i+1]
			if t1-t0 < 1e-6 {
				continue
			}
			mid, w := (t0+t1)/2, math.Inf(1)
			for _, p := range ps {
				if p.t0 <= mid && mid <= p.t1 {
					w = math.Min(w, p.w)
				}
			}
			if math.IsInf(w, 1) {
				if t1-t0 > MatchTolMil {
					covered = false
				}
				continue
			}
			if n := len(spans); n > 0 && math.Abs(spans[n-1].w-w) < 1e-9 {
				spans[n-1].t1 = t1
				continue
			}
			spans = append(spans, span{t0, t1, w})
		}
		if !covered || len(spans) == 0 {
			spans = []span{{0, length, minW}}
		}
		spans[0].t0, spans[len(spans)-1].t1 = 0, length
		widthWrong := false
		for _, s := range spans {
			if math.Abs(s.w-t.Width) > widthEpsMil {
				widthWrong = true
			}
		}
		layerWrong := target != t.Layer
		if !widthWrong && !layerWrong {
			continue
		}
		var reasons []string
		if layerWrong {
			reasons = append(reasons, fmt.Sprintf("layer %d→%d", t.Layer, target))
			plan.LayerMoves++
		}
		if widthWrong {
			ws := make([]string, len(spans))
			for i, s := range spans {
				ws[i] = fnum(s.w)
			}
			reasons = append(reasons, fmt.Sprintf("width %s→%s mil", fnum(t.Width), strings.Join(ws, "/")))
			plan.WidthRestores++
		}
		fix := TrackFix{Delete: t, Reason: strings.Join(reasons, ", ")}
		for _, s := range spans {
			p0, p1 := lerp(a, b, s.t0/length), lerp(a, b, s.t1/length)
			fix.Create = append(fix.Create, NewTrack{Net: t.Net, Layer: target, X1: p0[0], Y1: p0[1], X2: p1[0], Y2: p1[1], Width: s.w})
		}
		plan.Fixes = append(plan.Fixes, fix)
	}
	return plan
}

// PlanFixedWiring returns the fixed DSN wires and vias that are not on the
// board yet. The session never contains them, so the importer drops them.
func PlanFixedWiring(fixed *Wiring, tracks []Track, vias [][2]float64, viaNets []string, viaDiameterMil float64) ([]NewTrack, []NewVia) {
	var nt []NewTrack
	for _, s := range fixed.Segments {
		layer, ok := LayerID(s.Layer)
		if !ok {
			continue
		}
		exists := false
		for _, t := range tracks {
			if t.Layer != layer || !strings.EqualFold(t.Net, s.Net) {
				continue
			}
			ta, tb := [2]float64{t.X1, t.Y1}, [2]float64{t.X2, t.Y2}
			if pointSegDist(s.A, ta, tb) <= MatchTolMil && pointSegDist(s.B, ta, tb) <= MatchTolMil {
				exists = true
				break
			}
		}
		if !exists {
			nt = append(nt, NewTrack{Net: s.Net, Layer: layer, X1: s.A[0], Y1: s.A[1], X2: s.B[0], Y2: s.B[1], Width: s.WidthMil})
		}
	}
	var nv []NewVia
	for _, v := range fixed.Vias {
		exists := false
		for i, p := range vias {
			if math.Hypot(p[0]-v.At[0], p[1]-v.At[1]) <= MatchTolMil && strings.EqualFold(viaNets[i], v.Net) {
				exists = true
				break
			}
		}
		if !exists {
			nv = append(nv, NewVia{Net: v.Net, X: v.At[0], Y: v.At[1], DiameterMil: viaDiameterMil})
		}
	}
	return nt, nv
}

// seg is a session segment with its EasyEDA layer id.
type seg struct {
	Segment
	layer int
}

// exactSegments returns the candidates whose ends coincide with a and b
// (either direction) and, when width >= 0, whose width equals it.
func exactSegments(cands []seg, a, b [2]float64, width float64) []seg {
	near := func(p, q [2]float64) bool { return math.Hypot(p[0]-q[0], p[1]-q[1]) <= MatchTolMil }
	var out []seg
	for _, s := range cands {
		if width >= 0 && math.Abs(s.WidthMil-width) > widthEpsMil {
			continue
		}
		if (near(s.A, a) && near(s.B, b)) || (near(s.A, b) && near(s.B, a)) {
			out = append(out, s)
		}
	}
	return out
}

// ImporterLayerOffset: EasyEDA's importAutoRouteSes puts InnerN tracks on
// layer id 20+N instead of 14+N (observed on 3.2.149: Inner1 → 21, Inner2 → 22).
const ImporterLayerOffset = 6

// importerLayer picks, among exactly matching segments, the one on the layer
// the importer's offset maps the track's wrong layer id back to.
func importerLayer(wrong int, exact []seg) (int, bool) {
	want := wrong - ImporterLayerOffset
	for _, s := range exact {
		if s.layer == want {
			return want, true
		}
	}
	return 0, false
}

func uniqueLayer(segs []seg) (int, bool) {
	if len(segs) == 0 {
		return 0, false
	}
	for _, s := range segs[1:] {
		if s.layer != segs[0].layer {
			return 0, false
		}
	}
	return segs[0].layer, true
}

func project(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	l := math.Hypot(dx, dy)
	return ((p[0]-a[0])*dx + (p[1]-a[1])*dy) / l
}

func pointSegDist(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/l2))
	}
	return math.Hypot(p[0]-a[0]-t*dx, p[1]-a[1]-t*dy)
}

func lerp(a, b [2]float64, f float64) [2]float64 {
	return [2]float64{
		math.Round((a[0]+(b[0]-a[0])*f)*1000) / 1000,
		math.Round((a[1]+(b[1]-a[1])*f)*1000) / 1000,
	}
}

// Reconcile is what the board still lacks compared with the session after
// the import and its repair, and the tracks left on layers the board does
// not have.
type Reconcile struct {
	MissingTracks []NewTrack `json:"missingTracks,omitempty"`
	MissingVias   []NewVia   `json:"missingVias,omitempty"`
	Stray         []Track    `json:"strayTracks,omitempty"`
}

// PlanReconcile compares every session segment and via with the live copper:
// a segment is present when a same-net track on its layer contains both of its
// ends (EasyEDA merges collinear pieces), a via when a same-net via sits
// within MatchTolMil. Tracks on a layer id outside copper are stray (Gas
// Module v9: importer leftovers on 21/22 cut +3V3 and SV3_DRV; the vias at
// those points were missing too). viaDia sizes the vias to create.
func PlanReconcile(ses *Wiring, tracks []Track, vias [][2]float64, viaNets []string, copper []int, viaDia float64) Reconcile {
	var r Reconcile
	valid := map[int]bool{}
	for _, l := range copper {
		valid[l] = true
	}
	for _, s := range ses.Segments {
		layer, ok := LayerID(s.Layer)
		if !ok || !valid[layer] {
			continue
		}
		// EasyEDA splits and merges collinear same-net pieces, so a session
		// segment is present when the union of the collinear tracks on its
		// layer covers it; only the uncovered stretches are missing (Gas
		// Module v10: a GND segment split into 21.7 + 16.24 mil pieces was
		// reported missing and then duplicated).
		for _, gap := range uncovered(s, layer, tracks) {
			// EasyEDA also merges a short piece into overlapping same-net
			// copper that is not collinear (Gas Module V5 B v18: a 12.5 mil
			// SV1_DRV stub between a via and two 40 mil tracks): present
			// when same-net copper covers 90 % of its width all along.
			if copperCovers(gap, s.WidthMil, s.Net, layer, tracks, vias, viaNets, viaDia) {
				continue
			}
			r.MissingTracks = append(r.MissingTracks, NewTrack{Net: s.Net, Layer: layer, X1: gap[0][0], Y1: gap[0][1], X2: gap[1][0], Y2: gap[1][1], Width: s.WidthMil})
		}
	}
	for _, v := range ses.Vias {
		found := false
		for i, p := range vias {
			if strings.EqualFold(viaNets[i], v.Net) && math.Hypot(p[0]-v.At[0], p[1]-v.At[1]) <= MatchTolMil {
				found = true
				break
			}
		}
		if !found {
			r.MissingVias = append(r.MissingVias, NewVia{Net: v.Net, X: v.At[0], Y: v.At[1], DiameterMil: viaDia})
		}
	}
	for _, t := range tracks {
		if !valid[t.Layer] {
			r.Stray = append(r.Stray, t)
		}
	}
	return r
}

// CopperLayerIDs returns the EasyEDA ids of a board's copper layers.
func CopperLayerIDs(count int) []int {
	ids := []int{1, 2}
	for k := 1; k <= count-2; k++ {
		ids = append(ids, 14+k)
	}
	return ids
}

// uncovered returns the stretches of a session segment that no collinear
// same-net track on its layer covers (each longer than MatchTolMil).
func uncovered(s Segment, layer int, tracks []Track) [][2][2]float64 {
	length := math.Hypot(s.B[0]-s.A[0], s.B[1]-s.A[1])
	if length <= MatchTolMil {
		return nil
	}
	ux, uy := (s.B[0]-s.A[0])/length, (s.B[1]-s.A[1])/length
	lineDist := func(p [2]float64) float64 { return math.Abs((p[0]-s.A[0])*uy - (p[1]-s.A[1])*ux) }
	along := func(p [2]float64) float64 { return (p[0]-s.A[0])*ux + (p[1]-s.A[1])*uy }
	// e is how far a track's round end still holds 90 % of the segment's
	// width: two collinear pieces whose ends overlap that much are one
	// continuous copper (Gas Module V5 B v17: a 1.1 mil GND stub dropped on
	// import sat in a 2.2 mil gap between two 21.65 mil tracks).
	type iv struct{ a, b, e float64 }
	var cover []iv
	for _, t := range tracks {
		if t.Layer != layer || !strings.EqualFold(t.Net, s.Net) {
			continue
		}
		p, q := [2]float64{t.X1, t.Y1}, [2]float64{t.X2, t.Y2}
		if lineDist(p) > MatchTolMil || lineDist(q) > MatchTolMil {
			continue // not collinear
		}
		a, b := along(p), along(q)
		if a > b {
			a, b = b, a
		}
		r, h := t.Width/2, 0.45*s.WidthMil
		e := 0.0
		if r > h {
			e = math.Sqrt(r*r - h*h)
		}
		cover = append(cover, iv{a - MatchTolMil, b + MatchTolMil, e})
	}
	sort.Slice(cover, func(i, j int) bool { return cover[i].a < cover[j].a })
	var gaps [][2][2]float64
	at, atE, started := 0.0, 0.0, false
	emit := func(from, to float64) {
		if to-from > MatchTolMil {
			p := func(t float64) [2]float64 {
				return [2]float64{math.Round((s.A[0]+ux*t)*1000) / 1000, math.Round((s.A[1]+uy*t)*1000) / 1000}
			}
			gaps = append(gaps, [2][2]float64{p(from), p(to)})
		}
	}
	for _, c := range cover {
		if c.b <= at {
			continue
		}
		if c.a > at && !(started && c.a-at+2*MatchTolMil <= atE+c.e) {
			emit(at, math.Min(c.a, length))
		}
		if c.b > at {
			atE = c.e
		}
		at, started = math.Max(at, c.b), true
		if at >= length {
			break
		}
	}
	if at < length {
		emit(at, length)
	}
	return gaps
}

// copperCovers reports whether same-net tracks on layer and same-net vias
// cover the stretch a→b of width w: sample points every 0.5 mil along it, on
// its axis and at ±45 % of its width, each inside some track capsule or via.
func copperCovers(seg [2][2]float64, w float64, net string, layer int, tracks []Track, vias [][2]float64, viaNets []string, viaDia float64) bool {
	a, b := seg[0], seg[1]
	l := math.Hypot(b[0]-a[0], b[1]-a[1])
	if l == 0 {
		return false
	}
	ux, uy := (b[0]-a[0])/l, (b[1]-a[1])/l
	in := func(x, y float64) bool {
		for _, t := range tracks {
			if t.Layer == layer && strings.EqualFold(t.Net, net) && segDist(x, y, t.X1, t.Y1, t.X2, t.Y2) <= t.Width/2+1e-6 {
				return true
			}
		}
		for i, v := range vias {
			if strings.EqualFold(viaNets[i], net) && math.Hypot(x-v[0], y-v[1]) <= viaDia/2+1e-6 {
				return true
			}
		}
		return false
	}
	n := int(math.Ceil(l/0.5)) + 1
	for i := 0; i < n; i++ {
		t := l * float64(i) / float64(n-1)
		for _, o := range []float64{0, 0.45 * w, -0.45 * w} {
			if !in(a[0]+ux*t-uy*o, a[1]+uy*t+ux*o) {
				return false
			}
		}
	}
	return true
}

func segDist(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, ((px-ax)*dx+(py-ay)*dy)/l2))
	}
	return math.Hypot(px-ax-t*dx, py-ay-t*dy)
}
