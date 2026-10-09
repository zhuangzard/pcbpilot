package app

// cmd_kicad_sch_auto.go — the `--backend kicad` path of the schematic
// automation commands that used to need one connector round trip per pin
// or per step: `sch autoconnect`, `sch connect` and `sch layout-plan`
// (apply). The planners are the EasyEDA ones, fed with geometry read from
// the .kicad_sch (internal/kicad/schscene.go) in EasyEDA planner units
// (10 mil, y up: x = mm/0.254, y = −mm/0.254); the result is written in
// one file write, after `kicad-cli sch export netlist` on a copy of the
// project showed the connectivity the plan expects (the original file is
// untouched otherwise).

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

const eeMM = 0.254 // mm per EasyEDA schematic unit

func mmToEE(p kicad.Pt) (float64, float64) {
	r := func(v float64) float64 { return math.Round(v*1e6) / 1e6 }
	return r(p.X / eeMM), r(-p.Y / eeMM)
}

func eeToMM(x, y float64) kicad.Pt {
	r := func(v float64) float64 { return math.Round(v*1e4) / 1e4 }
	return kicad.Pt{X: r(x * eeMM), Y: r(-y * eeMM)}
}

func boxToEE(b kicad.Box) layoutBBox {
	return layoutBBox{MinX: b.MinX / eeMM, MinY: -b.MaxY / eeMM, MaxX: b.MaxX / eeMM, MaxY: -b.MinY / eeMM}
}

// kicadPinNet is a KiCad netlist name as the planner's "current net":
// unconnected pins are floating, sheet paths are dropped.
func kicadPinNet(n string) string {
	if strings.HasPrefix(n, "unconnected-(") {
		return ""
	}
	return stripSheetPath(n)
}

// kicadAcScene converts a sheet into the autoconnect scene (planner units)
// and returns each part pin's KiCad position by "REF.PIN". nets (KiCad
// netlist pin → net) makes the idempotency check work; nil = unknown.
func kicadAcScene(sc *kicad.SchScene, nets map[string]string) (acScene, map[string]kicad.Pt) {
	scene := acScene{}
	pinMM := map[string]kicad.Pt{}
	pt := func(p kicad.Pt) layoutBBox {
		x, y := mmToEE(p)
		return layoutBBox{MinX: x - 4, MinY: y - 4, MaxX: x + 4, MaxY: y + 4}
	}
	seen := map[string]bool{}
	for _, s := range sc.Symbols {
		box := pt(s.At)
		if s.HasBox {
			box = boxToEE(s.Box)
		}
		if s.Power {
			scene.Flags = append(scene.Flags, box)
			continue
		}
		scene.Parts = append(scene.Parts, box)
		owner := box
		for _, p := range s.Pins {
			x, y := mmToEE(p.At)
			rot := p.Outward
			net, known := "", false
			if nets != nil {
				n, ok := nets[s.Ref+"."+p.Number]
				net, known = kicadPinNet(n), ok
			}
			scene.Pins = append(scene.Pins, acPin{X: x, Y: y, Designator: s.Ref, PinNumber: p.Number, PinName: p.Name,
				OwnerBBox: &owner, PinRotation: &rot, Net: net, NetKnown: known})
			pinMM[s.Ref+"."+p.Number] = p.At
		}
		if !seen[s.Ref] {
			seen[s.Ref] = true
			scene.Components = append(scene.Components, acComponent{Designator: s.Ref, HasPins: len(s.Pins) > 0})
		}
	}
	for _, l := range sc.Labels {
		scene.Flags = append(scene.Flags, boxToEE(l.Box))
	}
	for _, t := range sc.Texts {
		scene.Texts = append(scene.Texts, boxToEE(t))
	}
	for _, w := range sc.Wires {
		x0, y0 := mmToEE(w[0])
		x1, y1 := mmToEE(w[1])
		// net unknown → treated as foreign: a stub never touches an existing wire
		scene.Wires = append(scene.Wires, wireSegment{X0: x0, Y0: y0, X1: x1, Y1: y1})
	}
	if sc.TitleBlock != nil {
		tb := boxToEE(*sc.TitleBlock)
		scene.TitleBlock = &tb
	} else {
		scene.TitleBlockProvisional = true
	}
	return scene, pinMM
}

// kicadPinAt finds the part pin at a sheet point ("" when none).
func kicadPinAt(pinMM map[string]kicad.Pt, p kicad.Pt) string {
	keys := make([]string, 0, len(pinMM))
	for k := range pinMM {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if q := pinMM[k]; math.Abs(q.X-p.X) < 1e-3 && math.Abs(q.Y-p.Y) < 1e-3 {
			return k
		}
	}
	return ""
}

var acDirIndex = map[string]int{"up": 0, "left": 1, "down": 2, "right": 3}

// kicadPlaceMarker puts the marker of canonicalKind for net at the stub end
// at, its body pointing dir (the connector's orientation rule: power and
// ground symbols turn counter-clockwise from up/down, labels read along the
// stub). rot overrides the computed rotation. Returns what was placed.
func kicadPlaceMarker(e *kicad.SchEditor, canonicalKind, net string, at kicad.Pt, dir string, rot *float64) (string, error) {
	idx, ok := acDirIndex[dir]
	if !ok {
		return "", fmt.Errorf("unknown direction %q; expected up/down/left/right", dir)
	}
	angle := func(anchor int) float64 {
		if rot != nil {
			return *rot
		}
		return float64((idx-anchor+4)%4) * 90
	}
	labelAngle := map[string]float64{"right": 0, "up": 90, "left": 180, "down": 270}[dir]
	if rot != nil {
		labelAngle = *rot
	}
	switch canonicalKind {
	case "power":
		return e.AddPower(net, at, angle(0), false)
	case "ground", "analog_ground", "protective_ground", "protect_ground":
		return e.AddPower(net, at, angle(2), true)
	case "net_port_in", "net_port_out", "net_port_bi":
		shape := map[string]string{"net_port_in": "input", "net_port_out": "output", "net_port_bi": "bidirectional"}[canonicalKind]
		return "global_label " + net, e.AddLabel(kicad.LabelGlobal, net, at, labelAngle, shape)
	case "net_label":
		return "label " + net, e.AddLabel(kicad.LabelLocal, net, at, labelAngle, "")
	}
	return "", fmt.Errorf("--backend kicad: kind %q not supported", canonicalKind)
}

// kicadBeforeNets exports the netlist of the hierarchy holding path.
func kicadBeforeNets(path string) (map[string]string, error) {
	root, err := kicad.RootSheetFor(path)
	if err != nil {
		return nil, err
	}
	nl, err := kicad.ExportSchNetlist(root)
	if err != nil {
		return nil, err
	}
	return nl.PinNets(), nil
}

// kicadSchCommitVerified writes text (optionally page-fitted) to path only
// after the netlist of a copy of the project with the edit in place passed
// check; on any failure the original file is untouched.
func kicadSchCommitVerified(path, text string, fit bool, check func(after map[string]string) error) (map[string]any, error) {
	info := map[string]any{}
	if fit {
		out, r, err := kicad.FitSheet(text)
		if err != nil {
			return nil, err
		}
		if r.TooBig {
			return nil, fmt.Errorf("--fit: content %.0f×%.0f mm does not fit A0 — split the sheet (nothing written)", r.Content.W(), r.Content.H())
		}
		text, info["fit"] = out, r
	}
	root, err := kicad.RootSheetFor(path)
	if err != nil {
		return nil, err
	}
	files, err := kicad.SheetFiles(root)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "pcbpilot-sch-verify-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	rootDir := filepath.Dir(root)
	absPath, _ := filepath.Abs(path)
	pros, _ := filepath.Glob(filepath.Join(rootDir, "*.kicad_pro"))
	for _, f := range append(files, pros...) {
		rel, err := filepath.Rel(rootDir, f)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("sheet %s lies outside the root sheet's directory (not supported)", f)
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		if a, _ := filepath.Abs(f); a == absPath {
			data = []byte(text)
		}
		dst := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return nil, err
		}
	}
	rel, _ := filepath.Rel(rootDir, root)
	nl, err := kicad.ExportSchNetlist(filepath.Join(tmp, rel))
	if err != nil {
		return nil, err
	}
	if err := check(nl.PinNets()); err != nil {
		return nil, fmt.Errorf("%w — %s left unchanged", err, path)
	}
	part := path + ".pcbpilot-tmp"
	if err := os.WriteFile(part, []byte(text), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(part, path); err != nil {
		os.Remove(part)
		return nil, err
	}
	info["verified"] = true
	return info, nil
}

// kicadExpectNets checks the netlist after an edit that put each planned
// pin ("REF.PIN" → net) on its net: the before partition with every planned
// pin's net merged into its target net (and any existing net of that name),
// planned pins carrying the target name.
func kicadExpectNets(before, planned map[string]string) func(after map[string]string) error {
	return func(after map[string]string) error {
		parent := map[string]string{}
		var find func(string) string
		find = func(x string) string {
			if p, ok := parent[x]; ok && p != x {
				r := find(p)
				parent[x] = r
				return r
			}
			return x
		}
		union := func(a, b string) { parent[find(a)] = find(b) }
		names := map[string]bool{}
		for _, n := range before {
			names[n] = true
		}
		for pin, t := range planned {
			if n, ok := before[pin]; ok {
				union("net:"+n, "target:"+t)
			}
			for n := range names {
				if stripSheetPath(n) == t {
					union("net:"+n, "target:"+t)
				}
			}
		}
		expected := map[string]string{}
		for pin, n := range before {
			r := find("net:" + n)
			if strings.HasPrefix(r, "target:") {
				expected[pin] = strings.TrimPrefix(r, "target:")
			} else {
				expected[pin] = strings.TrimPrefix(r, "net:")
			}
		}
		cmp := kicad.ComparePinNets(expected, after, stripSheetPath)
		var bad []string
		if !cmp.Equal {
			bad = append(bad, cmp.Mismatched...)
			for _, p := range cmp.OnlyA {
				bad = append(bad, "missing pin "+p)
			}
			for _, p := range cmp.OnlyB {
				bad = append(bad, "new pin "+p)
			}
		}
		pins := make([]string, 0, len(planned))
		for p := range planned {
			pins = append(pins, p)
		}
		sort.Strings(pins)
		for _, p := range pins {
			if got := stripSheetPath(after[p]); got != planned[p] {
				bad = append(bad, fmt.Sprintf("%s on %q, planned %q", p, got, planned[p]))
			}
		}
		if len(bad) > 0 {
			if len(bad) > 8 {
				bad = append(bad[:8], fmt.Sprintf("… %d more", len(bad)-8))
			}
			return fmt.Errorf("KiCad netlist does not match the plan: %s", strings.Join(bad, "; "))
		}
		return nil
	}
}

// kicadSchAutoconnect is `sch autoconnect --backend kicad`: plan every
// connection exactly like the connector path, write all stubs and markers
// in one verified file write. Explicit --x/--y are KiCad mm.
func kicadSchAutoconnect(path string, conns []acConnSpec, rules autoconnectRules, opts acRunOpts, fit bool, stdout, stderr io.Writer) error {
	if opts.AllPages {
		return fmt.Errorf("--backend kicad: --all-pages is not supported (pass the sheet with --kicad-sch)")
	}
	e, err := kicad.OpenSchematicFile(path)
	if err != nil {
		return err
	}
	sc, err := e.Scene()
	if err != nil {
		return err
	}
	before, nerr := kicadBeforeNets(path)
	if nerr != nil {
		if !opts.DryRun {
			return fmt.Errorf("--backend kicad needs the KiCad netlist to check and verify the connections: %w", nerr)
		}
		fmt.Fprintf(stderr, "warn: no KiCad netlist (%v) — pins' current nets unknown, idempotency not checked\n", nerr)
	}
	scene, pinMM := kicadAcScene(sc, before)
	for i, c := range conns {
		if c.X != nil && c.Y != nil {
			x, y := mmToEE(kicad.Pt{X: *c.X, Y: *c.Y})
			conns[i].X, conns[i].Y = &x, &y
		}
	}
	conns = expandPinFanouts(scene, conns)
	report := newAcReport(scene, rules)
	planned := map[string]string{}
	planAutoconnectBatch(&scene, conns, rules, opts, &report, acConnectHooks{
		connect: func(pin acPin, canonicalKind, net string, sel acCandidate, cr *acConnResult) error {
			key := pin.Designator + "." + pin.PinNumber
			from, ok := pinMM[key]
			if !ok {
				from = eeToMM(pin.X, pin.Y)
				key = kicadPinAt(pinMM, from)
			}
			to := eeToMM(sel.EndPoint.X, sel.EndPoint.Y)
			e.AddWire(from, to)
			marker, err := kicadPlaceMarker(e, canonicalKind, net, to, sel.Direction, nil)
			if err != nil {
				return err
			}
			if key != "" {
				planned[key] = net
			}
			cr.WirePrimitiveID = fmt.Sprintf("wire (%s,%s)→(%s,%s) mm", kicad.F(from.X), kicad.F(from.Y), kicad.F(to.X), kicad.F(to.Y))
			cr.FlagPrimitiveID = marker
			return nil
		},
	})
	report.Kicad = map[string]any{"file": path, "units": "planner units (10 mil, y up); mm = 0.254·x, −0.254·y"}
	if !opts.DryRun {
		written := 0
		for _, c := range report.Connections {
			if c.Error == "" && c.State != acStateAlreadyConnected {
				written++
			}
		}
		if written > 0 {
			text, err := e.Render()
			if err == nil {
				var info map[string]any
				info, err = kicadSchCommitVerified(path, text, fit, kicadExpectNets(before, planned))
				for k, v := range info {
					report.Kicad[k] = v
				}
			}
			if err != nil {
				report.OK = false
				report.Kicad["error"] = err.Error()
				for i := range report.Connections {
					c := &report.Connections[i]
					if c.Error == "" && c.State != acStateAlreadyConnected {
						c.Error = "not written: " + err.Error()
					}
				}
			}
		}
		report.Kicad["written"] = written
	}
	report.Succeeded, report.Failed, report.Partial = splitConnResults(report.Connections, opts.DryRun)
	if opts.JSON {
		if err := writeJSON(stdout, report); err != nil {
			return err
		}
	} else {
		renderAutoconnectReport(report, opts.DryRun, stdout)
		if msg, ok := report.Kicad["error"].(string); ok {
			fmt.Fprintf(stdout, "  ✗ kicad: %s\n", msg)
		} else if !opts.DryRun {
			fmt.Fprintf(stdout, "  kicad: %d connection(s) written to %s (netlist verified)\n", report.Kicad["written"], path)
		}
	}
	if !report.OK {
		return fmt.Errorf("autoconnect: %d connection(s) failed", countFailed(report))
	}
	return nil
}

// kicadSchConnect is `sch connect --backend kicad`: one stub + marker in KiCad
// mm (offset default 7.62 mm = the connector's 30 units), written after the
// netlist check.
func kicadSchConnect(path, pinRef string, x, y float64, canonicalKind, net, direction string, offset float64, rot *float64, fit bool, stdout io.Writer) error {
	e, err := kicad.OpenSchematicFile(path)
	if err != nil {
		return err
	}
	sc, err := e.Scene()
	if err != nil {
		return err
	}
	before, err := kicadBeforeNets(path)
	if err != nil {
		return fmt.Errorf("--backend kicad needs the KiCad netlist to verify the connection: %w", err)
	}
	scene, pinMM := kicadAcScene(sc, before)
	from, key := kicad.Pt{X: x, Y: y}, ""
	if pinRef != "" {
		p, err := resolvePinCoord(scene, pinRef)
		if err != nil {
			return err
		}
		key = p.Designator + "." + p.PinNumber
		from = pinMM[key]
	} else {
		key = kicadPinAt(pinMM, from)
	}
	if direction == "" {
		direction = kindDefaultDirection(canonicalKind)
		if canonicalKind == "net_label" {
			direction = "right" // the connector's default for labels
		}
	}
	snap := func(v float64) float64 { return math.Round(math.Round(v/1.27)*1.27*1e4) / 1e4 }
	to := from
	switch direction {
	case "up":
		to.Y = snap(from.Y - offset)
	case "down":
		to.Y = snap(from.Y + offset)
	case "left":
		to.X = snap(from.X - offset)
	case "right":
		to.X = snap(from.X + offset)
	default:
		return fmt.Errorf("unknown --direction %q; expected up/down/left/right", direction)
	}
	if to == from {
		return fmt.Errorf("offset %g mm puts the marker on the pin", offset)
	}
	e.AddWire(from, to)
	marker, err := kicadPlaceMarker(e, canonicalKind, net, to, direction, rot)
	if err != nil {
		return err
	}
	text, err := e.Render()
	if err != nil {
		return err
	}
	planned := map[string]string{}
	if key != "" {
		planned[key] = net
	}
	info, err := kicadSchCommitVerified(path, text, fit, kicadExpectNets(before, planned))
	if err != nil {
		return err
	}
	res := map[string]any{"ok": true, "file": path, "backend": "kicad", "pin": key, "net": net, "kind": canonicalKind,
		"direction": direction, "from": from, "to": to, "marker": marker}
	for k, v := range info {
		res[k] = v
	}
	return writeJSON(stdout, res)
}

// ---- sch layout-plan --backend kicad ----------------------------------------------

// kicadMeasureLayoutInput replaces every component's measurement (position,
// rotation, mirror, bbox, pin positions and outward angles) with the KiCad
// sheet's, in planner units, keyed by designator. Pin nets and the rest of
// the input are kept. Host-measured text boxes do not describe the KiCad
// fields and are dropped.
func kicadMeasureLayoutInput(sc *kicad.SchScene, in SchematicLayoutInput) (SchematicLayoutInput, error) {
	byRef := map[string][]kicad.SceneSymbol{}
	for _, s := range sc.Symbols {
		if !s.Power {
			byRef[s.Ref] = append(byRef[s.Ref], s)
		}
	}
	for i := range in.Components {
		m := &in.Components[i].Measurement
		ss := byRef[m.Designator]
		if len(ss) != 1 {
			return in, fmt.Errorf("component %s: %d symbol instances of %q on the KiCad sheet (want exactly 1)", in.Components[i].ID, len(ss), m.Designator)
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
				return in, fmt.Errorf("%s has no pin %s on the KiCad sheet", m.Designator, m.Pins[j].Number)
			}
			m.Pins[j].X, m.Pins[j].Y = mmToEE(p.At)
			r := p.Outward
			m.Pins[j].Rotation = &r
		}
		if a := in.Components[i].AllowedRotations; len(a) > 0 {
			found := false
			for _, v := range a {
				found = found || v == m.Rotation
			}
			if !found {
				return in, fmt.Errorf("component %s: allowedRotations %v lack the KiCad rotation %g", in.Components[i].ID, a, m.Rotation)
			}
		}
	}
	return in, nil
}

// kicadApplyLayout moves the planned symbols on the KiCad sheet: the core
// stays where it is, every other placement keeps its planned offset from
// it; each symbol's KiCad rotation is the one whose pins land on the planned
// pin positions. Wires, labels and power symbols on moved pins follow; the
// write happens only when the netlist is unchanged.
func kicadApplyLayout(path string, e *kicad.SchEditor, sc *kicad.SchScene, in SchematicLayoutInput, res *SchematicLayoutResult, fit bool, stdout io.Writer) error {
	core := ""
	var coreX, coreY float64
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
	syms := map[string]kicad.SceneSymbol{}
	for _, s := range sc.Symbols {
		syms[s.Ref] = s
	}
	poses := map[string]kicad.SymPose{}
	for _, p := range res.Placements {
		s, ok := syms[p.Designator]
		if !ok {
			return fmt.Errorf("placement %s is not on the KiCad sheet", p.Designator)
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
				return err
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
			return fmt.Errorf("placement %s: the planned pin positions match the KiCad symbol at no rotation", p.Designator)
		}
		if math.Abs(at.X-s.At.X) < 1e-4 && math.Abs(at.Y-s.At.Y) < 1e-4 && math.Mod(rot-s.Rot+360, 360) == 0 {
			continue
		}
		poses[p.Designator] = kicad.SymPose{At: at, Rot: rot}
	}
	if len(poses) == 0 {
		return writeJSON(stdout, map[string]any{"ok": true, "file": path, "backend": "kicad", "moved": kicad.DragResult{}, "placements": len(res.Placements), "score": res.Score, "note": "every symbol is already where the layout puts it; nothing written"})
	}
	before, err := kicadBeforeNets(path)
	if err != nil {
		return fmt.Errorf("--backend kicad needs the KiCad netlist to verify the layout: %w", err)
	}
	drag, err := e.DragSymbols(poses)
	if err != nil {
		return err
	}
	text, err := e.Render()
	if err != nil {
		return err
	}
	info, err := kicadSchCommitVerified(path, text, fit, func(after map[string]string) error {
		cmp := kicad.ComparePinNets(before, after, stripSheetPath)
		if !cmp.Equal {
			return fmt.Errorf("the layout changes the KiCad netlist (nets %v, only before %v, only after %v)", cmp.Mismatched, cmp.OnlyA, cmp.OnlyB)
		}
		return nil
	})
	if err != nil {
		return err
	}
	out := map[string]any{"ok": true, "file": path, "backend": "kicad", "moved": drag, "placements": len(res.Placements), "score": res.Score}
	for k, v := range info {
		out[k] = v
	}
	return writeJSON(stdout, out)
}
