package app

// cmd_kicad_sch_layout.go — moving symbols on a .kicad_sch as one
// transaction: `sch layout-plan [--zones] --backend kicad`, `sch group-move
// --backend kicad`. Every move goes through kicadDragCommit: drag (the wire
// islands follow rigidly or are re-routed orthogonally, kicad.DragSymbols),
// the strict quality gate on the planned page, then kicad-cli's netlist of a
// project copy must equal the one before — only then is the file replaced.
// Candidate placements (whole-set offsets) are scored against the symbols
// that stay, the title block and the page before any is tried.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// kicadMeasureComponents replaces every component's measurement (position,
// rotation, mirror, bbox, pin positions and outward angles) with the KiCad
// sheet's, in planner units. Components are matched by designator, or by
// "REF:UNIT" for one unit of a multi-unit symbol. Pin nets and the rest of
// the input are kept; host-measured text boxes are dropped.
func kicadMeasureComponents(sc *kicad.SchScene, comps []SchematicLayoutComponent) error {
	byKey := map[string][]kicad.SceneSymbol{}
	for _, s := range sc.Symbols {
		if s.Power {
			continue
		}
		byKey[s.Key] = append(byKey[s.Key], s)
		if s.Key != s.Ref {
			byKey[s.Ref+"#units"] = append(byKey[s.Ref+"#units"], s)
		}
	}
	for i := range comps {
		m := &comps[i].Measurement
		ss := byKey[m.Designator]
		if len(ss) != 1 {
			if us := byKey[m.Designator+"#units"]; len(us) > 0 {
				var keys []string
				for _, u := range us {
					keys = append(keys, u.Key)
				}
				sort.Strings(keys)
				return fmt.Errorf("component %s: %s has %d units on the KiCad sheet — give one measured component per unit with designator %s", comps[i].ID, m.Designator, len(us), strings.Join(keys, " / "))
			}
			return fmt.Errorf("component %s: %d symbol instances of %q on the KiCad sheet (want exactly 1)", comps[i].ID, len(ss), m.Designator)
		}
		s := ss[0]
		m.X, m.Y = mmToEE(s.At)
		m.Rotation = math.Mod(math.Mod(s.Rot, 360)+360, 360)
		m.Mirror = s.Mirror != ""
		if s.HasBox {
			m.BBox = boxToEE(s.Box)
		}
		m.TextBBoxes, m.TextBBoxesByRotation = nil, nil
		at := map[string]kicad.ScenePin{}
		for _, p := range s.Pins {
			at[p.Number] = p
		}
		for j := range m.Pins {
			p, ok := at[m.Pins[j].Number]
			if !ok {
				return fmt.Errorf("%s has no pin %s on the KiCad sheet", m.Designator, m.Pins[j].Number)
			}
			m.Pins[j].X, m.Pins[j].Y = mmToEE(p.At)
			r := p.Outward
			m.Pins[j].Rotation = &r
		}
		if a := comps[i].AllowedRotations; len(a) > 0 {
			found := false
			for _, v := range a {
				found = found || v == m.Rotation
			}
			if !found {
				return fmt.Errorf("component %s: allowedRotations %v lack the KiCad rotation %g", comps[i].ID, a, m.Rotation)
			}
		}
	}
	return nil
}

func kicadMeasureLayoutInput(sc *kicad.SchScene, in SchematicLayoutInput) (SchematicLayoutInput, error) {
	return in, kicadMeasureComponents(sc, in.Components)
}

func kicadMeasureZonesInput(sc *kicad.SchScene, in SchematicZonesInput) (SchematicZonesInput, error) {
	return in, kicadMeasureComponents(sc, in.Components)
}

// kicadPosesOf turns planned placements (planner units, offset dx,dy) into
// KiCad poses: each symbol gets the rotation whose pins land on the planned
// pin positions.
func kicadPosesOf(e *kicad.SchEditor, sc *kicad.SchScene, placements []SchematicPlacement, dx, dy float64) (map[string]kicad.SymPose, error) {
	syms := map[string]kicad.SceneSymbol{}
	for _, s := range sc.Symbols {
		syms[s.Key] = s
	}
	poses := map[string]kicad.SymPose{}
	for _, p := range placements {
		s, ok := syms[p.Designator]
		if !ok {
			return nil, fmt.Errorf("placement %s is not on the KiCad sheet", p.Designator)
		}
		at := eeToMM(p.X+dx, p.Y+dy)
		want := map[string]kicad.Pt{}
		for _, pin := range p.Pins {
			want[pin.Number] = eeToMM(pin.X+dx, pin.Y+dy)
		}
		rot, ok := -1.0, false
		for _, r := range []float64{math.Mod(p.Rotation+360, 360), 0, 90, 180, 270} {
			pins, err := e.PinsAt(s.LibID, s.Unit, at, r, s.Mirror)
			if err != nil {
				return nil, err
			}
			match := len(pins) > 0
			for _, q := range pins {
				w, has := want[q.Number]
				if has && (math.Abs(w.X-q.At.X) > 0.01 || math.Abs(w.Y-q.At.Y) > 0.01) {
					match = false
					break
				}
			}
			if match {
				rot, ok = r, true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("placement %s: the planned pin positions match the KiCad symbol at no rotation", p.Designator)
		}
		poses[p.Designator] = kicad.SymPose{At: at, Rot: rot}
	}
	return poses, nil
}

// kicadOffsetCandidates scores whole-set offsets (multiples of 2.54 mm
// around 0,0) of the planned poses: collisions with the symbols that stay,
// the title block keep-out and the drawing area. Clean offsets come first,
// nearest first; at most n are returned.
func kicadOffsetCandidates(e *kicad.SchEditor, sc *kicad.SchScene, poses map[string]kicad.SymPose, radius, n int) []kicad.Pt {
	syms := map[string]kicad.SceneSymbol{}
	for _, s := range sc.Symbols {
		syms[s.Key] = s
	}
	var planned []kicad.Box
	for k, p := range poses {
		s := syms[k]
		if b, ok := e.SymbolBoxAt(s.LibID, p.At, p.Rot, s.Mirror); ok {
			planned = append(planned, b)
		}
	}
	var static []kicad.Box
	for _, s := range sc.Symbols {
		if _, moving := poses[s.Key]; !moving && !s.Power && s.HasBox {
			static = append(static, s.Box)
		}
	}
	const gap = 1.27
	hit := func(a, b kicad.Box) bool {
		return a.MinX < b.MaxX+gap && b.MinX < a.MaxX+gap && a.MinY < b.MaxY+gap && b.MinY < a.MaxY+gap
	}
	type cand struct {
		off   kicad.Pt
		score int
		dist  float64
	}
	var cs []cand
	for i := -radius; i <= radius; i++ {
		for j := -radius; j <= radius; j++ {
			off := kicad.Pt{X: float64(i) * 2.54, Y: float64(j) * 2.54}
			score := 0
			for _, b := range planned {
				b = kicad.Box{MinX: b.MinX + off.X, MinY: b.MinY + off.Y, MaxX: b.MaxX + off.X, MaxY: b.MaxY + off.Y}
				for _, s := range static {
					if hit(b, s) {
						score += 2
					}
				}
				if sc.TitleBlock != nil && hit(b, *sc.TitleBlock) {
					score += 3 // title-block keep-out
				}
				if sc.Page != nil && (b.MinX < sc.Page.MinX+5 || b.MinY < sc.Page.MinY+5 || b.MaxX > sc.Page.MaxX-5 || b.MaxY > sc.Page.MaxY-5) {
					score++
				}
			}
			cs = append(cs, cand{off, score, math.Hypot(off.X, off.Y)})
		}
	}
	sort.SliceStable(cs, func(a, b int) bool {
		if cs[a].score != cs[b].score {
			return cs[a].score < cs[b].score
		}
		return cs[a].dist < cs[b].dist
	})
	var out []kicad.Pt
	for _, c := range cs {
		if len(out) == n {
			break
		}
		out = append(out, c.off)
	}
	return out
}

// kicadDragCommit applies the poses (shifted by each candidate offset in
// turn) until one passes the strict gate, then writes it after the netlist
// check. decorate may add items (zone frames) for an offset.
func kicadDragCommit(path string, poses map[string]kicad.SymPose, offsets []kicad.Pt, fit bool, decorate func(e *kicad.SchEditor, off kicad.Pt) error) (map[string]any, error) {
	if len(offsets) == 0 {
		offsets = []kicad.Pt{{}}
	}
	before, err := kicadBeforeNets(path)
	if err != nil {
		return nil, fmt.Errorf("--backend kicad needs the KiCad netlist to verify the move: %w", err)
	}
	orig, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tried []string
	for i, off := range offsets {
		e, err := kicad.OpenSchematicFile(path)
		if err != nil {
			return nil, err
		}
		shifted := map[string]kicad.SymPose{}
		for k, p := range poses {
			shifted[k] = kicad.SymPose{At: kicad.Pt{X: round4(p.At.X + off.X), Y: round4(p.At.Y + off.Y)}, Rot: p.Rot}
		}
		drag, err := e.DragSymbolsOpt(shifted, kicad.DragOptions{PinNets: before})
		if err != nil {
			return nil, err
		}
		if decorate != nil {
			if err := decorate(e, off); err != nil {
				return nil, err
			}
		}
		text, err := e.Render()
		if err != nil {
			return nil, err
		}
		planned := text
		if fit {
			if planned, _, err = kicad.FitSheet(text); err != nil {
				return nil, err
			}
		}
		if g := kicad.GateSchematic(string(orig), planned, kicad.CheckOptions{}); !g.OK {
			tried = append(tried, fmt.Sprintf("offset (%s, %s) mm: %s", kicad.F(off.X), kicad.F(off.Y), kicad.Summary(g.New, 3)))
			continue
		}
		renamed := map[string]bool{}
		for _, r := range drag.Renamed {
			renamed[strings.SplitN(r, " → ", 2)[0]] = true
		}
		info, err := kicadSchCommitVerified(path, text, fit, func(after map[string]string) error {
			cmp := kicad.ComparePinNets(before, after, stripSheetPath)
			if !cmp.Equal {
				return fmt.Errorf("the move changes the KiCad netlist (nets %v, only before %v, only after %v)", cmp.Mismatched, cmp.OnlyA, cmp.OnlyB)
			}
			for _, r := range cmp.RenamedNets {
				if !renamed[strings.SplitN(r, " → ", 2)[0]] {
					return fmt.Errorf("the move renames net %s", r)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		info["moved"], info["offset"], info["attempts"] = drag, off, i+1
		return info, nil
	}
	return nil, fmt.Errorf("strict gate: every candidate placement adds quality findings (%d tried) — %s left unchanged:\n  %s", len(offsets), path, strings.Join(tried, "\n  "))
}

// kicadApplyLayout moves the planned symbols on the KiCad sheet: every
// placement keeps its planned offset from the core; the set as a whole is
// anchored where the core is, or at the nearest offset clear of the symbols
// that stay, the title block and the page edge.
func kicadApplyLayout(path string, e *kicad.SchEditor, sc *kicad.SchScene, in SchematicLayoutInput, res *SchematicLayoutResult, fit bool, stdout io.Writer) error {
	var coreX, coreY float64
	core := ""
	for _, c := range in.Components {
		if c.ID == in.CoreComponentID {
			core, coreX, coreY = c.Measurement.Designator, c.Measurement.X, c.Measurement.Y
		}
	}
	var dx, dy float64
	found := false
	for _, p := range res.Placements {
		if p.Designator == core {
			dx, dy, found = coreX-p.X, coreY-p.Y, true
		}
	}
	if !found {
		return fmt.Errorf("the layout has no placement for the core %q", core)
	}
	poses, err := kicadPosesOf(e, sc, res.Placements, dx, dy)
	if err != nil {
		return err
	}
	if kicadPosesUnchanged(sc, poses) {
		return writeJSON(stdout, map[string]any{"ok": true, "file": path, "backend": "kicad", "moved": kicad.DragResult{}, "placements": len(res.Placements), "score": res.Score, "note": "every symbol is already where the layout puts it; nothing written"})
	}
	info, err := kicadDragCommit(path, poses, kicadOffsetCandidates(e, sc, poses, 24, 8), fit, nil)
	if err != nil {
		return err
	}
	out := map[string]any{"ok": true, "file": path, "backend": "kicad", "placements": len(res.Placements), "score": res.Score}
	for k, v := range info {
		out[k] = v
	}
	return writeJSON(stdout, out)
}

func kicadPosesUnchanged(sc *kicad.SchScene, poses map[string]kicad.SymPose) bool {
	for _, s := range sc.Symbols {
		if p, ok := poses[s.Key]; ok {
			if math.Abs(p.At.X-s.At.X) > 1e-4 || math.Abs(p.At.Y-s.At.Y) > 1e-4 || math.Mod(p.Rot-s.Rot+360, 360) != 0 {
				return false
			}
		}
	}
	return true
}

// ---- sch layout-plan --zones --backend kicad -----------------------------------

// kicadZoneFrame is one zone's frame on the sheet (mm, y down).
type kicadZoneFrame struct {
	ID, Title string
	Rect      kicad.Box
	core      kicad.Pt // where the zone's core goes
	result    *SchematicZoneResult
}

// kicadPackZones lays the zone frames out in reading order, left to right in
// rows inside the drawing area of a paper, keeping out of the title block and
// off the symbols that stay (static). Returns false when they do not fit.
func kicadPackZones(zones []SchematicZoneResult, page, title kicad.Box, static []kicad.Box) ([]kicadZoneFrame, bool) {
	const gap, margin = 5.08, 5.08
	snap := func(v float64) float64 { return math.Ceil(v/1.27-1e-6) * 1.27 }
	x0, y0 := snap(page.MinX+margin), snap(page.MinY+margin)
	x, y, rowH := x0, y0, 0.0
	fits := true
	hit := func(a, b kicad.Box) bool {
		return a.MinX < b.MaxX+gap && b.MinX < a.MaxX+gap && a.MinY < b.MaxY+gap && b.MinY < a.MaxY+gap
	}
	var out []kicadZoneFrame
	for i := range zones {
		z := &zones[i]
		r := z.Frame.Rect
		w, h := (r.MaxX-r.MinX)*eeMM, (r.MaxY-r.MinY)*eeMM
		place := func() kicad.Box { return kicad.Box{MinX: x, MinY: y, MaxX: x + w, MaxY: y + h} }
		newRow := func() { x, y, rowH = x0, snap(y+rowH+gap), 0 }
		for {
			b := place()
			if b.MaxY > page.MaxY-margin {
				fits = false
				break
			}
			if b.MaxX > page.MaxX-margin || hit(b, title) {
				if x == x0 {
					fits = false // wider than the page, or the title block takes the row
					break
				}
				newRow()
				continue
			}
			moved := false
			for _, s := range static {
				if hit(b, s) {
					x, moved = snap(s.MaxX+gap), true
					break
				}
			}
			if !moved {
				break
			}
		}
		b := place()
		// the frame's top-left is (Rect.MinX, Rect.MaxY) in zone units (y up)
		core := kicad.Pt{X: round4(math.Round((x-r.MinX*eeMM)/1.27) * 1.27), Y: round4(math.Round((y+r.MaxY*eeMM)/1.27) * 1.27)}
		out = append(out, kicadZoneFrame{ID: z.ID, Title: z.Title, Rect: b, core: core, result: z})
		x = snap(x + w + gap)
		rowH = math.Max(rowH, h)
	}
	return out, fits
}

// kicadApplyZones places every zone of a --zones plan on the KiCad sheet:
// frames packed in rows (the smallest paper that holds them with --fit, the
// current paper otherwise), each zone's symbols at their planned offsets
// from its core, a dashed frame and the zone title drawn; earlier pcbpilot
// frames of these zones are replaced. One verified write.
func kicadApplyZones(path string, e *kicad.SchEditor, sc *kicad.SchScene, res *SchematicZonesResult, fit bool, stdout io.Writer) error {
	papers := kicad.Papers
	if !fit {
		papers = nil
		for _, p := range kicad.Papers {
			if p.Name == sc.Paper {
				papers = append(papers, p)
			}
		}
		if len(papers) == 0 {
			return fmt.Errorf("--zones: unknown paper %q on the sheet (use --fit)", sc.Paper)
		}
	}
	inZone := map[string]bool{}
	for _, z := range res.Zones {
		for _, p := range z.Layout.Placements {
			inZone[p.Designator] = true
		}
	}
	var static []kicad.Box
	for _, s := range sc.Symbols {
		if !s.Power && s.HasBox && !inZone[s.Key] {
			static = append(static, s.Box)
		}
	}
	var frames []kicadZoneFrame
	paper := ""
	for _, p := range papers {
		page := kicad.Box{MinX: 10, MinY: 10, MaxX: p.W - 10, MaxY: p.H - 10}
		title := kicad.Box{MinX: p.W - 10 - 112, MinY: p.H - 10 - 34, MaxX: p.W - 10, MaxY: p.H - 10}
		fs, ok := kicadPackZones(res.Zones, page, title, static)
		frames, paper = fs, p.Name
		if ok {
			break
		}
	}
	poses := map[string]kicad.SymPose{}
	for _, f := range frames {
		l := f.result.Layout
		var core *SchematicPlacement
		for i := range l.Placements {
			if l.ComponentIDs != nil && l.ComponentIDs[l.Placements[i].Designator] == f.result.CoreComponentID {
				core = &l.Placements[i]
			}
		}
		if core == nil {
			for i := range l.Placements {
				if l.Placements[i].X == 0 && l.Placements[i].Y == 0 {
					core = &l.Placements[i]
				}
			}
		}
		if core == nil {
			return fmt.Errorf("zone %s: no core placement in its layout", f.ID)
		}
		cx, cy := mmToEE(f.core)
		ps, err := kicadPosesOf(e, sc, l.Placements, cx-core.X, cy-core.Y)
		if err != nil {
			return fmt.Errorf("zone %s: %w", f.ID, err)
		}
		for k, v := range ps {
			poses[k] = v
		}
	}
	titles := map[string]bool{}
	for _, f := range frames {
		titles[f.Title] = true
	}
	decorate := func(ed *kicad.SchEditor, off kicad.Pt) error {
		ed.DeleteZoneDecor(titles)
		if paper != "" && paper != sc.Paper {
			ed.SetPaper(paper)
		}
		for _, f := range frames {
			ed.AddRect(kicad.Pt{X: f.Rect.MinX, Y: f.Rect.MinY}, kicad.Pt{X: f.Rect.MaxX, Y: f.Rect.MaxY})
			ed.AddText(f.Title, kicad.Pt{X: round4(f.Rect.MinX + 1.27), Y: round4(f.Rect.MinY + 2.54)}, 0, 1.524)
		}
		return nil
	}
	info, err := kicadDragCommit(path, poses, nil, fit, decorate)
	if err != nil {
		return err
	}
	var zs []map[string]any
	for _, f := range frames {
		zs = append(zs, map[string]any{"id": f.ID, "title": f.Title, "frame": f.Rect, "core": f.core})
	}
	out := map[string]any{"ok": true, "file": path, "backend": "kicad", "paper": paper, "zones": zs}
	for k, v := range info {
		out[k] = v
	}
	return writeJSON(stdout, out)
}

// ---- sch group-move --backend kicad --------------------------------------------

// kicadGroupMove translates the named symbols (every unit of each
// reference, or "REF:UNIT" for one unit) by dx, dy mm (snapped to the 1.27 mm
// grid). Wires among them and their stubs and markers move rigidly; wires
// to symbols that stay are re-routed. All or nothing.
func kicadGroupMove(path string, refs []string, dx, dy float64, fit bool, stdout io.Writer) error {
	sdx, sdy := math.Round(dx/1.27)*1.27, math.Round(dy/1.27)*1.27
	if sdx == 0 && sdy == 0 {
		return fmt.Errorf("--dx/--dy round to 0 on the 1.27 mm grid; nothing to move")
	}
	e, err := kicad.OpenSchematicFile(path)
	if err != nil {
		return err
	}
	sc, err := e.Scene()
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, r := range refs {
		want[r] = true
	}
	poses := map[string]kicad.SymPose{}
	for _, s := range sc.Symbols {
		if s.Power || !(want[s.Ref] || want[s.Key]) {
			continue
		}
		poses[s.Key] = kicad.SymPose{At: kicad.Pt{X: round4(s.At.X + sdx), Y: round4(s.At.Y + sdy)}, Rot: s.Rot}
		delete(want, s.Ref)
		delete(want, s.Key)
	}
	if len(want) > 0 {
		var miss []string
		for r := range want {
			miss = append(miss, r)
		}
		sort.Strings(miss)
		return fmt.Errorf("not on the sheet: %s", strings.Join(miss, ", "))
	}
	info, err := kicadDragCommit(path, poses, nil, fit, nil)
	if err != nil {
		return err
	}
	out := map[string]any{"ok": true, "file": path, "backend": "kicad", "dx": round4(sdx), "dy": round4(sdy), "symbols": len(poses)}
	for k, v := range info {
		out[k] = v
	}
	return writeJSON(stdout, out)
}

// ---- sch titleblock --backend kicad --------------------------------------------

type kicadTitleFlags struct {
	title, rev, date, company string
	comments                  []string
}

func (f *kicadTitleFlags) register(c *cobra.Command) {
	c.Flags().StringVar(&f.title, "title", "", "--backend kicad: title")
	c.Flags().StringVar(&f.rev, "rev", "", "--backend kicad: revision")
	c.Flags().StringVar(&f.date, "date", "", "--backend kicad: date (e.g. 2026-10-09)")
	c.Flags().StringVar(&f.company, "company", "", "--backend kicad: company")
	c.Flags().StringArrayVar(&f.comments, "comment", nil, "--backend kicad: comment N=TEXT (N 1–9, repeatable)")
}

// kicadSchTitleblock writes the title block of a KiCad sheet.
func kicadSchTitleblock(path string, f kicadTitleFlags, dataJSON string, stdout io.Writer) error {
	tb := kicad.TitleBlock{Title: f.title, Rev: f.rev, Date: f.date, Company: f.company, Comments: map[int]string{}}
	for _, c := range f.comments {
		k, v, ok := strings.Cut(c, "=")
		n, err := strconv.Atoi(strings.TrimSpace(k))
		if !ok || err != nil {
			return fmt.Errorf("--comment %q: want N=TEXT", c)
		}
		tb.Comments[n] = v
	}
	if dataJSON != "" {
		var data map[string]any
		if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
			return fmt.Errorf("invalid --data json: %w", err)
		}
		for k, raw := range data {
			v, ok := raw.(string)
			if m, isMap := raw.(map[string]any); isMap {
				v, ok = m["value"].(string)
			}
			if !ok {
				return fmt.Errorf("--data %s: want a string or {\"value\": string}", k)
			}
			switch lk := strings.ToLower(strings.ReplaceAll(k, " ", "")); {
			case lk == "title" || lk == "name":
				tb.Title = v
			case lk == "date":
				tb.Date = v
			case lk == "rev" || lk == "revision" || lk == "version":
				tb.Rev = v
			case lk == "company":
				tb.Company = v
			case strings.HasPrefix(lk, "comment"):
				n, err := strconv.Atoi(strings.TrimPrefix(lk, "comment"))
				if err != nil {
					return fmt.Errorf("--data %s: want Comment1…Comment9", k)
				}
				tb.Comments[n] = v
			default:
				return fmt.Errorf("--data %s: KiCad's title block has Title, Date, Rev, Company, Comment1…9", k)
			}
		}
	}
	if tb.Title == "" && tb.Rev == "" && tb.Date == "" && tb.Company == "" && len(tb.Comments) == 0 {
		return fmt.Errorf("pass --title/--rev/--date/--company/--comment or --data")
	}
	return kicadSchEdit(path, stdout, func(e *kicad.SchEditor) (map[string]any, error) {
		if err := e.SetTitleBlock(tb); err != nil {
			return nil, err
		}
		return map[string]any{"titleBlock": tb}, nil
	})
}
