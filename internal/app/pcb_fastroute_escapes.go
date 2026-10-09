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
// DSN units / 1000 with y negated relative to the DSN; milPerUnit
// (specctra.ReportMilPerUnit) converts them to mil.
func readFastrouteBlocked(path string, milPerUnit float64) ([]frEndpoint, error) {
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
			frEndpoint{Net: u.Net, Mil: [2]float64{u.FromXY[0] * milPerUnit, -u.FromXY[1] * milPerUnit}, Layer: layer(u.From.Layers)},
			frEndpoint{Net: u.Net, Mil: [2]float64{u.ToXY[0] * milPerUnit, -u.ToXY[1] * milPerUnit}, Layer: layer(u.To.Layers)})
	}
	return out, nil
}

// finePitchMil: a pad whose nearest same-part neighbour is closer than this
// (centre to centre) is fine-pitch (0.5 mm = 19.7 mil, 0.65 mm = 25.6 mil).
const finePitchMil = 32

// preEscapeMinPins: parts with more pins than this (and fine pitch) get their
// ground pins escaped before the first route (TQFP-100/144, QFN-68+).
const preEscapeMinPins = 64

// escapeDepthsMil are the tried stub lengths past the pad's inner end: the
// second staggers a via 40 mil deeper when the first would crowd a
// neighbour's escape via (adjacent ground pins on a 0.5 mm pitch).
var escapeDepthsMil = []float64{30, 70}

type escapeVia struct {
	at  [2]float64
	net string
}

// escapePlanner plans inward stub + via escapes for pads, keeping each new
// via clear of other nets' pads and of every escape via already planned.
type escapePlanner struct {
	pads               []pcbPadP
	byRef              map[string][]pcbPadP
	viaDia, clr, width float64
	vias               []escapeVia
	done               map[string]bool
}

func newEscapePlanner(pads []pcbPadP, viaDia, clr, width float64) *escapePlanner {
	p := &escapePlanner{pads: pads, byRef: map[string][]pcbPadP{}, viaDia: viaDia, clr: clr, width: width, done: map[string]bool{}}
	for _, q := range pads {
		p.byRef[q.Designator] = append(p.byRef[q.Designator], q)
	}
	return p
}

// seed records escapes already in the DSN (their pads count as done).
func (p *escapePlanner) seed(esc []specctra.Escape) {
	for _, e := range esc {
		if len(e.Path) < 2 {
			continue
		}
		if e.Via {
			p.vias = append(p.vias, escapeVia{e.Path[len(e.Path)-1], e.Net})
		}
		for _, q := range p.pads {
			if math.Hypot(q.X-e.Path[0][0], q.Y-e.Path[0][1]) <= 1 {
				p.done[q.Designator+"."+q.Number] = true
			}
		}
	}
}

// pitch returns the pad's nearest same-part distance and its part size.
func (p *escapePlanner) pitch(pad pcbPadP) (float64, int) {
	part := p.byRef[pad.Designator]
	d := math.Inf(1)
	for _, q := range part {
		if q.Number != pad.Number {
			d = math.Min(d, math.Hypot(q.X-pad.X, q.Y-pad.Y))
		}
	}
	return d, len(part)
}

// escape plans one pad's stub toward the part's pad centroid.
func (p *escapePlanner) escape(pad pcbPadP) (specctra.Escape, string) {
	key := pad.Designator + "." + pad.Number
	if p.done[key] {
		return specctra.Escape{}, key + ": already escaped"
	}
	part := p.byRef[pad.Designator]
	cx, cy := 0.0, 0.0
	for _, q := range part {
		cx += q.X / float64(len(part))
		cy += q.Y / float64(len(part))
	}
	dx, dy := cx-pad.X, cy-pad.Y
	var dir [2]float64
	var half float64
	if math.Abs(dx) >= math.Abs(dy) {
		dir, half = [2]float64{math.Copysign(1, dx), 0}, pad.W/2
	} else {
		dir, half = [2]float64{0, math.Copysign(1, dy)}, pad.H/2
	}
	start := [2]float64{pad.X, pad.Y}
	why := ""
	for _, depth := range escapeDepthsMil {
		end := [2]float64{round3(pad.X + dir[0]*(half+depth)), round3(pad.Y + dir[1]*(half+depth))}
		if why = p.blocked(pad, start, end); why != "" {
			continue
		}
		layer := "TopLayer"
		if pad.Layer == 2 {
			layer = "BottomLayer"
		}
		p.done[key] = true
		p.vias = append(p.vias, escapeVia{end, pad.Net})
		return specctra.Escape{Net: pad.Net, Layer: layer, WidthMil: p.width, Path: [][2]float64{start, end}, Via: true}, ""
	}
	return specctra.Escape{}, fmt.Sprintf("%s (%s): escape would hit %s", key, pad.Net, why)
}

func (p *escapePlanner) blocked(pad pcbPadP, start, end [2]float64) string {
	for _, q := range p.pads {
		if strings.EqualFold(q.Net, pad.Net) {
			continue
		}
		viaGap := math.Hypot(math.Max(math.Abs(end[0]-q.X)-q.W/2, 0), math.Max(math.Abs(end[1]-q.Y)-q.H/2, 0)) - p.viaDia/2
		stubGap := rectSegDist(q.X-q.W/2, q.Y-q.H/2, q.X+q.W/2, q.Y+q.H/2, start[0], start[1], end[0], end[1]) - p.width/2
		if viaGap < p.clr || (q.Layer == pad.Layer || q.Layer == pcbLayerMulti) && stubGap < p.clr {
			return q.Designator + "." + q.Number
		}
	}
	for _, v := range p.vias {
		if math.Hypot(v.at[0]-end[0], v.at[1]-end[1]) < p.viaDia+p.clr {
			return "another escape via"
		}
	}
	return ""
}

// planPreEscapes escapes every ground pin of every fine-pitch part with more
// than preEscapeMinPins pins, around the escapes already planned (have).
func planPreEscapes(pads []pcbPadP, have []specctra.Escape, isGround func(string) bool, viaDia, clr, width float64) (esc []specctra.Escape, skipped []string) {
	p := newEscapePlanner(pads, viaDia, clr, width)
	p.seed(have)
	for _, pad := range pads {
		if !isGround(pad.Net) || pad.Layer == pcbLayerMulti || p.done[pad.Designator+"."+pad.Number] {
			continue
		}
		if pitch, n := p.pitch(pad); pitch > finePitchMil || n <= preEscapeMinPins {
			continue
		}
		if e, why := p.escape(pad); why == "" {
			esc = append(esc, e)
		} else {
			skipped = append(skipped, why)
		}
	}
	return esc, skipped
}

// planInwardEscapes escapes the blocked ground endpoints of a router report
// that sit on fine-pitch pads, around the escapes already planned (have).
func planInwardEscapes(blocked []frEndpoint, pads []pcbPadP, have []specctra.Escape, isGround func(string) bool, viaDia, clr, width float64) (esc []specctra.Escape, skipped []string) {
	p := newEscapePlanner(pads, viaDia, clr, width)
	p.seed(have)
	for _, e := range blocked {
		if !isGround(e.Net) {
			continue
		}
		var pad *pcbPadP
		for i := range pads {
			q := &pads[i]
			if strings.EqualFold(q.Net, e.Net) && math.Hypot(q.X-e.Mil[0], q.Y-e.Mil[1]) <= 2 {
				pad = q
				break
			}
		}
		if pad == nil || pad.Layer == pcbLayerMulti {
			continue
		}
		if pitch, _ := p.pitch(*pad); pitch > finePitchMil {
			continue
		}
		if p.done[pad.Designator+"."+pad.Number] {
			continue
		}
		if x, why := p.escape(*pad); why == "" {
			esc = append(esc, x)
		} else {
			skipped = append(skipped, why)
		}
	}
	return esc, skipped
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
