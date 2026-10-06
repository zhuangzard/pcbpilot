package app

// pcb_fastroute_escapes.go — automatic inward escapes for ground pins that
// fastroute reports as blocked. A GND pin between fine-pitch signal pins
// (TQFP 0.5 mm) often has no way out: the neighbours' escapes fence it in.
// The manual fix on Gas Module V5 was a fixed stub from the pad centre into
// the part body, ending in a via down to the ground layers; the router then
// treats the via as the pin. This plans those stubs from the router report.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

// frEndpoint is one end of a fastroute unrouted connection, in mil.
type frEndpoint struct {
	Net   string
	Mil   [2]float64
	Layer string
}

// readFastrouteBlocked returns both endpoints of every unrouted connection
// the report's --diagnose classified as "blocked". fastroute reports xy in
// inches with y negated relative to the DSN.
func readFastrouteBlocked(path string) ([]frEndpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r struct {
		Unrouted []struct {
			Net  string `json:"net"`
			From struct {
				Layers []string `json:"layers"`
			} `json:"from"`
			To struct {
				Layers []string `json:"layers"`
			} `json:"to"`
			FromXY    [2]float64 `json:"from_xy"`
			ToXY      [2]float64 `json:"to_xy"`
			Diagnosis struct {
				Class string `json:"class"`
			} `json:"diagnosis"`
		} `json:"unrouted"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse fastroute report %s: %w", path, err)
	}
	layer := func(ls []string) string {
		if len(ls) > 0 {
			return ls[0]
		}
		return "TopLayer"
	}
	var out []frEndpoint
	for _, u := range r.Unrouted {
		if u.Diagnosis.Class != "blocked" {
			continue
		}
		out = append(out,
			frEndpoint{Net: u.Net, Mil: [2]float64{u.FromXY[0] * 1000, -u.FromXY[1] * 1000}, Layer: layer(u.From.Layers)},
			frEndpoint{Net: u.Net, Mil: [2]float64{u.ToXY[0] * 1000, -u.ToXY[1] * 1000}, Layer: layer(u.To.Layers)})
	}
	return out, nil
}

// finePitchMil: a pad whose nearest same-part neighbour is closer than this
// (centre to centre) is fine-pitch (0.5 mm = 19.7 mil, 0.65 mm = 25.6 mil).
const finePitchMil = 32

// escapeBeyondMil is how far past the pad's inner end the stub reaches.
const escapeBeyondMil = 30

// planInwardEscapes plans one escape per blocked ground endpoint that sits on
// a fine-pitch SMD pad: a stub from the pad centre toward the part's pad
// centroid, along the dominant axis, ending escapeBeyondMil past the pad's
// inner end with a via. An escape is skipped when its via or stub comes
// within clearance of another net's pad or of an escape via of another net.
// Pads use their axis-aligned W×H (board axes, as pcb.components.list gives).
func planInwardEscapes(blocked []frEndpoint, pads []pcbPadP, isGround func(string) bool, viaDia, clearance, width float64) (esc []specctra.Escape, skipped []string) {
	byRef := map[string][]pcbPadP{}
	for _, p := range pads {
		byRef[p.Designator] = append(byRef[p.Designator], p)
	}
	done := map[string]bool{}
	var vias []struct {
		at  [2]float64
		net string
	}
	for _, e := range blocked {
		if !isGround(e.Net) {
			continue
		}
		var pad *pcbPadP
		for i := range pads {
			p := &pads[i]
			if strings.EqualFold(p.Net, e.Net) && math.Hypot(p.X-e.Mil[0], p.Y-e.Mil[1]) <= 2 {
				pad = p
				break
			}
		}
		if pad == nil || pad.Layer == pcbLayerMulti {
			continue
		}
		key := pad.Designator + "." + pad.Number
		if done[key] {
			continue
		}
		done[key] = true
		part := byRef[pad.Designator]
		pitch, cx, cy := math.Inf(1), 0.0, 0.0
		for _, q := range part {
			cx += q.X / float64(len(part))
			cy += q.Y / float64(len(part))
			if q.Number != pad.Number {
				pitch = math.Min(pitch, math.Hypot(q.X-pad.X, q.Y-pad.Y))
			}
		}
		if pitch > finePitchMil {
			continue
		}
		dx, dy := cx-pad.X, cy-pad.Y
		var dir [2]float64
		var half float64
		if math.Abs(dx) >= math.Abs(dy) {
			dir, half = [2]float64{math.Copysign(1, dx), 0}, pad.W/2
		} else {
			dir, half = [2]float64{0, math.Copysign(1, dy)}, pad.H/2
		}
		reach := half + escapeBeyondMil
		end := [2]float64{round3(pad.X + dir[0]*reach), round3(pad.Y + dir[1]*reach)}
		start := [2]float64{pad.X, pad.Y}

		blockedBy := ""
		for _, q := range pads {
			if strings.EqualFold(q.Net, e.Net) {
				continue
			}
			viaGap := math.Hypot(math.Max(math.Abs(end[0]-q.X)-q.W/2, 0), math.Max(math.Abs(end[1]-q.Y)-q.H/2, 0)) - viaDia/2
			stubGap := rectSegDist(q.X-q.W/2, q.Y-q.H/2, q.X+q.W/2, q.Y+q.H/2, start[0], start[1], end[0], end[1]) - width/2
			if viaGap < clearance || (q.Layer == pad.Layer || q.Layer == pcbLayerMulti) && stubGap < clearance {
				blockedBy = q.Designator + "." + q.Number
				break
			}
		}
		for _, v := range vias {
			if blockedBy == "" && !strings.EqualFold(v.net, e.Net) && math.Hypot(v.at[0]-end[0], v.at[1]-end[1]) < viaDia+clearance {
				blockedBy = "another escape via"
			}
		}
		if blockedBy != "" {
			skipped = append(skipped, fmt.Sprintf("%s (%s): escape would hit %s", key, e.Net, blockedBy))
			continue
		}
		layer := "TopLayer"
		if pad.Layer == 2 {
			layer = "BottomLayer"
		}
		esc = append(esc, specctra.Escape{Net: e.Net, Layer: layer, WidthMil: width, Path: [][2]float64{start, end}, Via: true})
		vias = append(vias, struct {
			at  [2]float64
			net string
		}{end, e.Net})
	}
	return esc, skipped
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
