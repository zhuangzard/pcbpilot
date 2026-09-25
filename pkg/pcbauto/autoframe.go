package pcbauto

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Auto-sized boards (mech board.autoSize without width/height/outline).
//
// The placer's own shrink-to-fit (placer.autosize) wraps only the part
// bodies, so mechanics resolved against the pre-shrink frame fall off the
// board: corner holes land outside the outline and edge connectors stay flush
// with an edge that no longer exists. Without an outline the pre-shrink frame
// was the import spread of the parts (4.9" × 1.9" on the ESP32 demo), which
// also left a third of the board empty.
//
// AutoFrame instead searches the frame itself: for a few aspect ratios it
// tries rectangles from dense to loose fill, resolves the full mechanics
// (corner holes, edge parts, keepouts) against each, runs the placer, and
// keeps the smallest rectangle the placer fills with zero overlaps, zero
// parts off the board, zero keepout hits and zero hole hits. The result is an
// ordinary fixed-size spec — every later stage sees a real outline.

// FrameTrial is one tried rectangle (mm when the spec is mm).
type FrameTrial struct {
	Width     float64 `json:"width"`
	Height    float64 `json:"height"`
	Aspect    float64 `json:"aspect"`
	Fill      float64 `json:"fill"`
	Feasible  bool    `json:"feasible"`
	Reason    string  `json:"reason,omitempty"`
	WireIn    float64 `json:"weightedWirelengthIn,omitempty"`
	TetherMil float64 `json:"tetherExcessMil,omitempty"`
}

// FrameSearch reports the chosen frame and every trial.
type FrameSearch struct {
	Width  float64      `json:"width"`
	Height float64      `json:"height"`
	Units  string       `json:"units"`
	Trials []FrameTrial `json:"trials"`
	// The loose reference frame's quality the chosen frame had to keep.
	RefWireIn    float64 `json:"referenceWirelengthIn"`
	RefTetherMil float64 `json:"referenceTetherExcessMil"`
}

var (
	frameAspects        = []float64{1.0, 1.35, 1.7}
	frameRefFill        = 0.2
	frameWireSlack      = 1.15
	frameTetherSlackMil = 150.0
	frameFills          = []float64{0.6, 0.54, 0.48, 0.42, 0.37, 0.33, 0.29, 0.26, 0.23, 0.2, 0.17, 0.145, 0.12}
)

// NeedsAutoFrame reports whether spec asks for an auto-sized board with no
// explicit size or outline.
func NeedsAutoFrame(m *MechSpec) bool {
	return m != nil && m.Board.AutoSize && m.Board.Width <= 0 && m.Board.Height <= 0 && len(m.Board.Outline) < 3
}

// AutoFrame returns a copy of spec with board.width/height set to the
// smallest feasible frame and autoSize cleared. The board is left exactly as
// it was (poses, holes, keepouts, outline).
func AutoFrame(b *Board, an *Analysis, c *Circuit, spec *MechSpec, opt PlaceOptions) (*MechSpec, *FrameSearch, error) {
	if !NeedsAutoFrame(spec) {
		return spec, nil, nil
	}
	k := 1 / 0.0254
	units := "mm"
	if spec.Units == "mil" {
		k, units = 1, "mil"
	}
	restore := snapshotBoard(b)
	defer restore()

	partArea := 0.0
	for _, p := range b.Parts {
		partArea += p.Body().Area()
	}
	// Corner holes occupy a square of 2×inset at each corner.
	holeArea := 0.0
	if ch := spec.CornerHoles; ch != nil {
		if s, ok := screwHoles[strings.ToUpper(ch.Size)]; ok {
			in := ch.Inset
			if in <= 0 {
				in = s[1]/2 + 0.5
			}
			holeArea = 4 * math.Pow(2*in*k, 2)
		}
	}
	// Every part must fit across the frame in at least its narrow direction.
	minSide := 0.0
	for _, p := range b.Parts {
		bd := p.Body()
		minSide = math.Max(minSide, math.Min(bd.W(), bd.H()))
	}
	step := 0.5 * k // round frames up to 0.5 mm (or 20 mil)
	if units == "mil" {
		step = 20
	}
	up := func(v float64) float64 { return math.Ceil(v/step) * step }

	fs := &FrameSearch{Units: units}
	// Reference: a loose frame shows what the placer achieves unconstrained.
	// A tighter frame must keep wirelength and the auxiliary tethers (decap →
	// pin, inductor → switch pin, …) close to it: hard legality alone accepted
	// a 60 %-fill board whose buck inductor sat 13 mm from the regulator.
	refArea := partArea/frameRefFill + holeArea
	refW := up(math.Max(minSide, math.Sqrt(refArea*1.35)))
	refH := up(math.Max(minSide, refArea/refW))
	restore()
	refSpec := *spec
	refSpec.Board.AutoSize = false
	refSpec.Board.Width, refSpec.Board.Height = refW/k, refH/k
	refMc, err := ApplyMech(b, &refSpec)
	if err != nil {
		return nil, fs, err
	}
	ref, err := Place(b, an, c, refMc, opt)
	if err != nil {
		return nil, fs, err
	}
	fs.RefWireIn, fs.RefTetherMil = ref.Metrics.WirelengthIn, ref.Metrics.CriticalExcessMil
	type best struct {
		w, h, area, wire float64
		ok               bool
	}
	var chosen best
	for _, aspect := range frameAspects {
		for _, fill := range frameFills {
			area := partArea/fill + holeArea
			w := up(math.Max(minSide, math.Sqrt(area*aspect)))
			h := up(math.Max(minSide, area/w))
			if chosen.ok && w*h >= chosen.area {
				break // looser frames of this aspect cannot win
			}
			tr := FrameTrial{Width: round2(w / k), Height: round2(h / k), Aspect: aspect, Fill: fill}
			restore()
			trial := *spec
			trial.Board.AutoSize = false
			trial.Board.Width, trial.Board.Height = w/k, h/k
			mc, err := ApplyMech(b, &trial)
			if err != nil {
				tr.Reason = err.Error()
				fs.Trials = append(fs.Trials, tr)
				continue
			}
			pr, err := Place(b, an, c, mc, opt)
			if err != nil {
				return nil, fs, err
			}
			tr.WireIn, tr.TetherMil = pr.Metrics.WirelengthIn, pr.Metrics.CriticalExcessMil
			tr.Reason = frameFault(b, pr)
			if tr.Reason == "" {
				switch {
				case pr.Metrics.WirelengthIn > fs.RefWireIn*frameWireSlack:
					tr.Reason = fmt.Sprintf("wirelength %.1f in > %.0f%% of loose frame %.1f in", pr.Metrics.WirelengthIn, frameWireSlack*100, fs.RefWireIn)
				case pr.Metrics.CriticalExcessMil > fs.RefTetherMil+math.Max(frameTetherSlackMil, fs.RefTetherMil/2):
					tr.Reason = fmt.Sprintf("critical auxiliaries %.0f mil beyond their pins (loose frame %.0f)", pr.Metrics.CriticalExcessMil, fs.RefTetherMil)
				}
			}
			tr.Feasible = tr.Reason == ""
			fs.Trials = append(fs.Trials, tr)
			if tr.Feasible {
				if !chosen.ok || w*h < chosen.area-1 || (math.Abs(w*h-chosen.area) <= 1 && pr.Metrics.WirelengthIn < chosen.wire) {
					chosen = best{w, h, w * h, pr.Metrics.WirelengthIn, true}
				}
				break // the next fill of this aspect is looser
			}
		}
	}
	if !chosen.ok {
		return nil, fs, fmt.Errorf("autoSize: no frame up to fill %.0f%% placed cleanly; give board.width/height", frameFills[len(frameFills)-1]*100)
	}
	out := *spec
	out.Board.AutoSize = false
	out.Board.Width, out.Board.Height = round2(chosen.w/k), round2(chosen.h/k)
	fs.Width, fs.Height = out.Board.Width, out.Board.Height
	sort.SliceStable(fs.Trials, func(i, j int) bool {
		return fs.Trials[i].Width*fs.Trials[i].Height < fs.Trials[j].Width*fs.Trials[j].Height
	})
	return &out, fs, nil
}

// frameFault names the first reason a placement does not fit its frame.
func frameFault(b *Board, pr *PlaceResult) string {
	m := pr.Metrics
	switch {
	case m.Overlaps > 0:
		return fmt.Sprintf("%d overlaps", m.Overlaps)
	case m.OutOfBoard > 0:
		return fmt.Sprintf("%d parts off the board", m.OutOfBoard)
	case m.KeepoutHits > 0:
		return fmt.Sprintf("%d keepout hits", m.KeepoutHits)
	}
	for _, h := range b.Holes {
		hr := Rect{h.C.X, h.C.Y, h.C.X, h.C.Y}.Expand(h.Dia/2 + h.Keep)
		for _, p := range b.Parts {
			if p.Body().OverlapArea(hr) > 1 {
				return fmt.Sprintf("%s on hole %s", p.Ref, h.Name)
			}
		}
	}
	return ""
}

// snapshotBoard captures what ApplyMech and Place mutate and returns a
// function restoring it (callable repeatedly).
func snapshotBoard(b *Board) func() {
	type pose struct {
		pos   Point
		rot   float64
		fixed bool
	}
	poses := make([]pose, len(b.Parts))
	for i, p := range b.Parts {
		poses[i] = pose{p.Pos, p.Rotation, p.Fixed}
	}
	outline := append([]Point(nil), b.Outline...)
	holes := append([]*Hole(nil), b.Holes...)
	keeps := append([]*Keepout(nil), b.Keepouts...)
	thick := b.Rules.BoardThickMil
	return func() {
		for i, p := range b.Parts {
			p.MoveTo(poses[i].pos, poses[i].rot)
			p.Fixed = poses[i].fixed
		}
		b.Outline = append([]Point(nil), outline...)
		b.Holes = append([]*Hole(nil), holes...)
		b.Keepouts = append([]*Keepout(nil), keeps...)
		b.Rules.BoardThickMil = thick
	}
}
