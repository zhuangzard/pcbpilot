package app

// pcb_check_pourlayer.go — `pcb check` pour-layer-mismatch rule (ERROR) and
// its route gate.
//
// The platform names an auto pour AUTO_<net>_L<layer id> after the layer the
// pour was REQUESTED on. When that layer is an inner PLANE (内电层) the pour
// can silently land on layer 1 instead: US-Zed2 (2026-10-12) shipped
// AUTO_GND_L15 / _L15_1 / _L15_2 with layer id 1 — a full-board GND pour on the
// Top layer over every other net's tracks, and JLC reported the top-layer
// lines shorted (顶层线路和顶层阻焊对不上/乱线短路). The name carries the intended
// layer, so a name/layer disagreement is a hard defect. There is no documented
// alias between layer ids, so none is accepted.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// autoPourNameRe matches AUTO_<net>_L<id> plus the platform's _<n> duplicate
// suffix (AUTO_GND_L15_1). The net may itself contain underscores.
var autoPourNameRe = regexp.MustCompile(`^AUTO_(.+)_L(\d+)(?:_\d+)?$`)

// findPourLayerMismatch flags every pour whose AUTO_<net>_L<id> name disagrees
// with the layer it actually sits on. Pours with any other name are skipped.
func findPourLayerMismatch(pours []pcbPourP) []pcbCheckFinding {
	var out []pcbCheckFinding
	for _, p := range pours {
		m := autoPourNameRe.FindStringSubmatch(strings.TrimSpace(p.Name))
		if m == nil {
			continue
		}
		want, err := strconv.Atoi(m[2])
		if err != nil || want == p.Layer {
			continue
		}
		f := pcbCheckFinding{
			Type: "pour-layer-mismatch", Level: "ERROR", Net: p.Net, Layer: p.Layer,
			Message: fmt.Sprintf("pour %q is named for layer %d but sits on layer %d — copper lands on the wrong layer (an inner-PLANE pour that fell back to Top covers other nets' tracks and shorts them); delete it and re-pour on layer %d while it is SIGNAL, then flip to PLANE and pour-rebuild (`pcb power-planes` does this)", p.Name, want, p.Layer, want),
		}
		if p.ID != "" {
			f.Primitives = []string{p.ID}
		}
		out = append(out, f)
	}
	return out
}

// pourLayerMismatchFromSnapshot decodes the board snapshot's pour.list rows.
func pourLayerMismatchFromSnapshot(snap *boardSnapshot) []pcbCheckFinding {
	var pours []pcbPourP
	if snap == nil || snap.Copper == nil {
		return nil
	}
	for _, raw := range snap.Copper.Pours {
		pm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		pours = append(pours, pourFromListRow(pm))
	}
	return findPourLayerMismatch(pours)
}

// pourFromListRow reads one pcb.pour.list row.
func pourFromListRow(pm map[string]any) pcbPourP {
	id, _ := pm["primitiveId"].(string)
	net, _ := pm["net"].(string)
	name, _ := pm["pourName"].(string)
	layer, _ := asFloatOK(pm["layer"])
	return pcbPourP{ID: id, Net: net, Name: name, Layer: int(layer)}
}
