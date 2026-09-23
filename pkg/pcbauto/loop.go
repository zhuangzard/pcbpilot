package pcbauto

import (
	"context"
	"fmt"
	"math"
)

// Place ↔ route closed loop.
//
// A placement is only as good as its routing. The loop places, routes with a
// short budget, scores the routed board (Joint), and — where the router
// failed — inflates the parts around the failures by one routing channel
// before placing again. The best routed pass wins. Failures are local far more
// often than global (a pad walled in by neighbours, a connector edge blocked
// by a cluster), which a congestion estimate cannot see but the router
// reports exactly: unrouted pads and DRC violation points.

// LoopOptions tune the place/route loop.
type LoopOptions struct {
	Passes int `json:"passes"` // maximum passes (default 3)
	// RadiusMil: parts with a body within this distance of a failure point
	// are inflated (default 150).
	RadiusMil float64 `json:"radiusMil"`
	// MaxHaloMil caps the accumulated inflation per part (default 60).
	MaxHaloMil float64 `json:"maxHaloMil"`
}

// LoopPass records one pass.
type LoopPass struct {
	Pass       int     `json:"pass"`
	Completion float64 `json:"completion"`
	DRC        int     `json:"drc"`
	Joint      float64 `json:"joint"`
	Inflated   int     `json:"inflatedParts"`
	Note       string  `json:"note,omitempty"`
}

// LoopResult is the best pass and the loop history.
type LoopResult struct {
	Place  *PlaceResult `json:"place"`
	Result *Result      `json:"result"`
	Joint  *JointScore  `json:"joint"`
	Passes []LoopPass   `json:"passes"`
	Best   int          `json:"bestPass"`
}

// PlaceRoute runs the loop and leaves the board at the best pass's placement.
func PlaceRoute(ctx context.Context, b *Board, an *Analysis, c *Circuit, m *Mechanics, popt PlaceOptions, ropt Options, lopt LoopOptions) (*LoopResult, error) {
	if lopt.Passes <= 0 {
		lopt.Passes = 3
	}
	if lopt.RadiusMil <= 0 {
		lopt.RadiusMil = 150
	}
	if lopt.MaxHaloMil <= 0 {
		lopt.MaxHaloMil = 60
	}
	channel := b.Rules.TrackWidth + 2*b.Rules.Clearance
	halo := map[string]float64{}
	for k, v := range popt.Halo {
		halo[k] = v
	}
	type pose struct {
		pos Point
		rot float64
	}
	res := &LoopResult{Best: -1}
	var bestPoses map[*Part]pose
	bestScore := math.Inf(-1)
	for pass := 1; pass <= lopt.Passes; pass++ {
		if err := ctx.Err(); err != nil {
			break
		}
		po := popt
		po.Halo = map[string]float64{}
		for k, v := range halo {
			po.Halo[k] = v
		}
		pr, err := Place(b, an, c, m, po)
		if err != nil {
			return nil, err
		}
		out, err := Run(ctx, b, ropt)
		if err != nil {
			return nil, err
		}
		js := Joint(b, out.Analysis, c, out.Stackup, out.Route, out.DRC, JointOptions{PlacementScore: -1, Overlaps: pr.Metrics.Overlaps})
		lp := LoopPass{Pass: pass, Completion: out.Route.Stats.Completion, DRC: len(out.DRC.Violations), Joint: js.Overall}
		if js.Overall > bestScore {
			bestScore = js.Overall
			res.Place, res.Result, res.Joint, res.Best = pr, out, js, pass
			bestPoses = map[*Part]pose{}
			for _, p := range b.Parts {
				bestPoses[p] = pose{p.Pos, p.Rotation}
			}
		}
		if js.Deliverable {
			lp.Note = "complete and DRC-clean"
			res.Passes = append(res.Passes, lp)
			break
		}
		// Failure points: unrouted pads and DRC violation locations.
		var pts []Point
		for _, u := range out.Route.Unrouted {
			for _, key := range u.Pads {
				if pd := padAt(b, key); pd != nil {
					pts = append(pts, pd.Box.C)
				}
			}
		}
		for _, v := range out.DRC.Violations {
			pts = append(pts, v.At)
		}
		grown := 0
		for _, p := range b.Parts {
			if p.Fixed {
				continue
			}
			body := p.Body()
			for _, pt := range pts {
				if distToRect(pt, body) <= lopt.RadiusMil {
					if halo[p.Ref] < lopt.MaxHaloMil {
						halo[p.Ref] = math.Min(lopt.MaxHaloMil, halo[p.Ref]+channel/2)
						grown++
					}
					break
				}
			}
		}
		lp.Inflated = grown
		if grown == 0 {
			lp.Note = "nothing left to inflate"
		}
		res.Passes = append(res.Passes, lp)
		if grown == 0 {
			break
		}
	}
	// Leave the board at the best pass.
	for p, ps := range bestPoses {
		p.MoveTo(ps.pos, ps.rot)
	}
	if res.Place != nil {
		res.Place.Notes = append(res.Place.Notes, fmt.Sprintf("place/route loop: best of %d passes is pass %d (joint %.1f)", len(res.Passes), res.Best, bestScore))
	}
	return res, nil
}

func distToRect(p Point, r Rect) float64 {
	dx := math.Max(0, math.Max(r.MinX-p.X, p.X-r.MaxX))
	dy := math.Max(0, math.Max(r.MinY-p.Y, p.Y-r.MaxY))
	return math.Hypot(dx, dy)
}
