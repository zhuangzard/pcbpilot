package app

// pcb_widen.go — `pcb widen`: widen the tracks of chosen nets (e.g. valve
// drain nets) up to a maximum width wherever the clearance to other-net copper
// allows. Ported from the Gas Module V5 widen_drv.py, which was used after a
// fastroute route; the router keeps every net at its class width, and
// high-current nets want more copper where there is room.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

type widenVia struct {
	Net      string  `json:"net"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Diameter float64 `json:"diameter"`
}

type widenOp struct {
	Track    specctra.Track `json:"track"`
	NewWidth float64        `json:"newWidth"`
}

// planWiden returns the tracks of nets that can grow by more than 2 mil.
// Free space is measured from points every ~4 mil along the track to the
// edges of other-net tracks on the same layer (including tracks widened
// earlier in this plan), other-net vias and other-net pads on the same layer
// or MULTI. Pads use their axis-aligned rectangle (90° rotations swap W/H).
func planWiden(tracks []specctra.Track, vias []widenVia, pads []boardPad, nets map[string]bool, maxMil, clearanceMil float64) []widenOp {
	type wide struct {
		a, b  [2]float64
		layer int
		width float64
		net   string
	}
	var widened []wide
	var ops []widenOp
	for _, t := range tracks {
		if !nets[t.Net] || t.Locked {
			continue
		}
		a, b := [2]float64{t.X1, t.Y1}, [2]float64{t.X2, t.Y2}
		n := max(6, int(math.Hypot(b[0]-a[0], b[1]-a[1])/4))
		samples := make([][2]float64, n+1)
		for k := 0; k <= n; k++ {
			f := float64(k) / float64(n)
			samples[k] = [2]float64{a[0] + (b[0]-a[0])*f, a[1] + (b[1]-a[1])*f}
		}
		minTo := func(d func(q [2]float64) float64) float64 {
			m := math.Inf(1)
			for _, q := range samples {
				m = math.Min(m, d(q))
			}
			return m
		}
		free := math.Inf(1)
		for _, o := range tracks {
			if o.Net == t.Net || o.Layer != t.Layer {
				continue
			}
			oa, ob := [2]float64{o.X1, o.Y1}, [2]float64{o.X2, o.Y2}
			free = math.Min(free, minTo(func(q [2]float64) float64 { return segDist(q, oa, ob) })-o.Width/2)
		}
		for _, w := range widened {
			if w.layer == t.Layer && w.net != t.Net {
				free = math.Min(free, minTo(func(q [2]float64) float64 { return segDist(q, w.a, w.b) })-w.width/2)
			}
		}
		for _, v := range vias {
			if v.Net != t.Net {
				free = math.Min(free, minTo(func(q [2]float64) float64 { return math.Hypot(q[0]-v.X, q[1]-v.Y) })-v.Diameter/2)
			}
		}
		for _, p := range pads {
			if p.Net == t.Net || (p.Layer != t.Layer && p.Layer != pcbLayerMulti) {
				continue
			}
			w, h := p.W, p.H
			if int(math.Round(p.Rotation))%180 == 90 {
				w, h = h, w
			}
			free = math.Min(free, minTo(func(q [2]float64) float64 {
				return math.Hypot(math.Max(math.Abs(q[0]-p.X)-w/2, 0), math.Max(math.Abs(q[1]-p.Y)-h/2, 0))
			}))
		}
		nw := math.Round(math.Min(maxMil, 2*(free-clearanceMil))*10) / 10
		if nw > t.Width+2 {
			ops = append(ops, widenOp{Track: t, NewWidth: nw})
			widened = append(widened, wide{a: a, b: b, layer: t.Layer, width: nw, net: t.Net})
		}
	}
	return ops
}

func segDist(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/l2))
	}
	return math.Hypot(p[0]-a[0]-t*dx, p[1]-a[1]-t*dy)
}

// decodeAny re-decodes a []any snapshot list into typed rows.
func decodeAny(src []any, dst any) error {
	raw, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

// widenNets plans and (unless dryRun) applies planWiden on the live board.
// New tracks are created before the old ones are deleted.
func widenNets(cfg *appConfig, window string, nets map[string]bool, maxMil, clearanceMil float64, dryRun bool, stderr io.Writer) ([]widenOp, error) {
	if !dryRun {
		// The ids below are deleted: read them from a reloaded board.
		if err := saveAndReload(cfg, window); err != nil {
			return nil, err
		}
	}
	snap, err := fetchBoardSnapshot(cfg, window, boardSnapshotOpts{withCopper: true, withRules: clearanceMil <= 0})
	if err != nil {
		return nil, err
	}
	if snap.Copper == nil {
		return nil, fmt.Errorf("board copper unreadable")
	}
	if clearanceMil <= 0 {
		clearanceMil = 8
		if snap.Rules != nil && snap.Rules.ClearanceMil > 0 {
			clearanceMil = snap.Rules.ClearanceMil
		}
	}
	var tracks []specctra.Track
	var vias []widenVia
	if err := decodeAny(snap.Copper.Lines, &tracks); err != nil {
		return nil, fmt.Errorf("decode tracks: %w", err)
	}
	if err := decodeAny(snap.Copper.Vias, &vias); err != nil {
		return nil, fmt.Errorf("decode vias: %w", err)
	}
	var pads []boardPad
	for _, c := range snap.Components {
		pads = append(pads, c.Pads...)
	}
	ops := planWiden(tracks, vias, pads, nets, maxMil, clearanceMil)
	fmt.Fprintf(stderr, "widen: %d track(s) can grow (max %.1f mil, clearance %.1f mil)\n", len(ops), maxMil, clearanceMil)
	if dryRun || len(ops) == 0 {
		return ops, nil
	}
	var del []string
	for _, op := range ops {
		t := op.Track
		if err := createPcbTrack(cfg, window, specctra.NewTrack{Net: t.Net, Layer: t.Layer, X1: t.X1, Y1: t.Y1, X2: t.X2, Y2: t.Y2, Width: op.NewWidth}); err != nil {
			return ops, fmt.Errorf("widen %s: %w", t.ID, err)
		}
		del = append(del, t.ID)
	}
	if _, err := requestActionTimed(cfg, "pcb.route.delete", window, map[string]any{"primitiveIds": del, "kind": "track"}, 5*time.Minute); err != nil {
		return ops, fmt.Errorf("delete widened originals: %w", err)
	}
	return ops, nil
}

func newPcbWidenCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var netsCSV string
	var maxMil, clearanceMil float64
	var dryRun, noPost bool
	c := &cobra.Command{
		Use:   "widen",
		Short: "Widen the tracks of chosen nets up to --max-mil where clearance allows",
		Long: `Widen every unlocked track of --net up to --max-mil, limited by the free space to
other-net tracks, vias and pads (minus --clearance-mil, default the live
clearance rule). Only tracks that gain more than 2 mil change. Then pour
rebuild → save → reload → pour rebuild → native DRC (--no-post skips).`,
		Args:    cobra.NoArgs,
		Example: `  pcbpilot pcb widen --net SV1_DRV,SV2_DRV,SV3_DRV,PV1_DRV --max-mil 40 --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			nets := map[string]bool{}
			for _, n := range strings.Split(netsCSV, ",") {
				if n = strings.TrimSpace(n); n != "" {
					nets[n] = true
				}
			}
			if len(nets) == 0 {
				return fmt.Errorf("--net is required")
			}
			ops, err := widenNets(cfg, *window, nets, maxMil, clearanceMil, dryRun, stderr)
			out := map[string]any{"dryRun": dryRun, "widened": ops}
			if err != nil {
				_ = writeJSON(stdout, out)
				return err
			}
			if !dryRun && !noPost && len(ops) > 0 {
				post, perr := postImportChecks(cfg, *window, nil, "", stderr)
				out["post"] = post
				if perr != nil {
					_ = writeJSON(stdout, out)
					return perr
				}
			}
			return writeJSON(stdout, out)
		},
	}
	c.Flags().StringVar(&netsCSV, "net", "", "nets to widen, comma-separated (required)")
	c.Flags().Float64Var(&maxMil, "max-mil", 40, "maximum width (mil)")
	c.Flags().Float64Var(&clearanceMil, "clearance-mil", 0, "clearance kept to other-net copper (mil; 0 = live rule, else 8)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "plan only")
	c.Flags().BoolVar(&noPost, "no-post", false, "skip pour rebuild / save / reload / DRC")
	return c
}
