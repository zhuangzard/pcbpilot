package app

// Anchor ↔ bbox-center conversion for `pcb modify --center` (issue #105).
//
// `pcb.component.modify` writes x/y as the component's ANCHOR (footprint
// origin), which is usually NOT the geometric center of its rendered bbox —
// and the offset between the two rotates with the part. Agents plan in bbox
// centers ("put U1's body at (cx,cy)"), so writing the planned center into
// x/y lands the part offset by anchor−center, worse for rotated parts.
//
// The conversion here needs NO rotation math: `pcb.components.list
// --include-bbox` returns the bbox as RENDERED, i.e. with the current
// rotation already baked in. The anchor→center offset at the current
// rotation is therefore a pure translation, and moving a component never
// changes it. That exactness is why `--center` refuses a patch that ALSO
// changes rotation: rotating moves the anchor-to-center offset, so the
// pre-rotation bbox we read would give a wrong anchor. Decision: rotate
// first (`--patch '{"rotation":…}'`), then re-run with `--center` — two
// calls, each exact — instead of a fragile rotate-reread-move compound.

import (
	"encoding/json"
	"fmt"
)

// bboxCenterXY returns the geometric center of an axis-aligned bbox.
func bboxCenterXY(minX, minY, maxX, maxY float64) (cx, cy float64) {
	return (minX + maxX) / 2, (minY + maxY) / 2
}

// anchorForCenter converts a desired bbox-center (targetCX, targetCY) into
// the anchor x/y to write via pcb.component.modify, given the component's
// CURRENT anchor and CURRENT rendered bbox (which already reflects the
// current rotation). Pure translation: newAnchor = target + (anchor − center).
// Valid only while rotation (and layer flip) stay unchanged — see file header.
func anchorForCenter(anchorX, anchorY, minX, minY, maxX, maxY, targetCX, targetCY float64) (ax, ay float64) {
	curCX, curCY := bboxCenterXY(minX, minY, maxX, maxY)
	return targetCX + (anchorX - curCX), targetCY + (anchorY - curCY)
}

// resolveAnchorForCenter reads the live board (pcb.components.list with
// includeBBox), locates the component by primitiveId, and converts the
// desired bbox-center into anchor coordinates. Thin I/O wrapper over
// anchorForCenter so the math stays unit-testable.
func resolveAnchorForCenter(cfg *appConfig, window, primitiveID string, targetCX, targetCY float64) (ax, ay float64, err error) {
	res, err := requestAction(cfg, "pcb.components.list", window, map[string]any{"includeBBox": true})
	if err != nil {
		return 0, 0, fmt.Errorf("--center needs the live bbox but the read failed: %w", err)
	}
	for _, c := range parseApComps(res.Result) {
		if c.id != primitiveID {
			continue
		}
		if !c.hasBBox {
			return 0, 0, fmt.Errorf("component %s (%s) has no rendered bbox — cannot convert --center to an anchor", primitiveID, c.designator)
		}
		ax, ay = anchorForCenter(c.x, c.y, c.minX, c.minY, c.maxX, c.maxY, targetCX, targetCY)
		return ax, ay, nil
	}
	return 0, 0, fmt.Errorf("component %q not found on the active PCB (fresh ids: `pcbpilot pcb list`)", primitiveID)
}

// injectBBoxCenters post-processes a raw pcb.components.list response: for
// every component that carries a bbox it adds `center: {x,y}` (the bbox
// geometric center) so planners can consume center coordinates directly
// (read-side symmetry of `pcb modify --center`, issue #105). The daemon /
// connector are untouched — this is a pure CLI-side annotation. On any parse
// problem the original bytes are returned unchanged (output shape is never
// worse than before).
func injectBBoxCenters(respBody []byte) []byte {
	var env map[string]any
	if err := json.Unmarshal(respBody, &env); err != nil {
		return respBody
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		return respBody
	}
	comps, ok := result["components"].([]any)
	if !ok {
		return respBody
	}
	changed := false
	for _, ci := range comps {
		cm, ok := ci.(map[string]any)
		if !ok {
			continue
		}
		bb, ok := cm["bbox"].(map[string]any)
		if !ok {
			continue
		}
		cx, cy := bboxCenterXY(asFloat(bb["minX"]), asFloat(bb["minY"]), asFloat(bb["maxX"]), asFloat(bb["maxY"]))
		cm["center"] = map[string]any{"x": cx, "y": cy}
		changed = true
	}
	if !changed {
		return respBody
	}
	out, err := json.Marshal(env)
	if err != nil {
		return respBody
	}
	return out
}
