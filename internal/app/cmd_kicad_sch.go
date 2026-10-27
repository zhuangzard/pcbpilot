package app

// cmd_kicad_sch.go — KiCad schematic side: `kicad sch-import` (EasyEDA Pro
// schematic → .kicad_sch), `kicad netlist` (schematic connectivity JSON from
// a .kicad_sch) and the `--backend kicad` path of the `sch` editing
// commands (place / wire / netflag / no-connect / modify), which write the
// .kicad_sch through internal/kicad/schwrite.go instead of the EasyEDA
// connector.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func newKicadSchImportCmd(stdout, stderr io.Writer) *cobra.Command {
	var epro, board, out, name, pcb string
	var noCheck bool
	c := &cobra.Command{
		Use:   "sch-import",
		Short: "Convert an EasyEDA Pro schematic (.epro export) into a hierarchical KiCad schematic and check its netlist",
		Long: `Converts one board's schematic of an EasyEDA Pro project export (.epro) into
<out>/<name>.kicad_sch (root) with one sub-sheet per EasyEDA page, plus
<name>.kicad_pro (kept when present), easyeda.kicad_sym and a sym-lib-table.

Symbols come from the export's .esym (pins, units, body graphics); parts keep
reference, value, LCSC number (field LCSC), MPN and manufacturer; the
Footprint field is the --pcb board's footprint of the same reference (else
the EasyEDA footprint title). Power flags become KiCad power symbols, net
ports global labels, EasyEDA wire net names labels (global when the name is
used on another page).

Unless --no-check, kicad-cli exports the netlist of the result and it is
compared net by net and pin by pin with the connectivity derived from the
EasyEDA data (and with the --pcb pads' nets); the command fails when the
partitions differ.`,
		Example: `  pcbpilot kicad sch-import --epro Project.epro --board GasV5_A --out kicad/ --pcb kicad/GasV5_A.kicad_pcb`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := kicad.ConvertEasyedaSchematic(epro, kicad.EasyedaSchOptions{Board: board, OutDir: out, Name: name, PCB: pcb})
			if err != nil {
				return err
			}
			report := map[string]any{"conversion": res}
			for _, w := range res.Warnings {
				fmt.Fprintln(stderr, "warning:", w)
			}
			failed := false
			if !noCheck {
				nl, err := kicad.ExportSchNetlist(res.Root)
				if err != nil {
					return err
				}
				kn := nl.PinNets()
				cmpE := kicad.ComparePinNets(res.PinNets, kn, stripSheetPath)
				report["vsEasyeda"] = cmpE
				failed = !cmpE.Equal
				fmt.Fprintf(stderr, "KiCad netlist vs EasyEDA schematic: %d/%d nets equal (%d same name), %d pins, mismatched %d\n",
					cmpE.NetsEqual, cmpE.NetsA, cmpE.NamesEqual, cmpE.PinsB, len(cmpE.Mismatched))
				if pcb != "" {
					pads, err := kicad.PCBPadNets(pcb)
					if err != nil {
						return err
					}
					// only connected pins can be compared with board pads
					conn := map[string]string{}
					for k, v := range kn {
						if !strings.HasPrefix(v, "unconnected-(") {
							conn[k] = v
						}
					}
					cmpP := kicad.ComparePinNets(conn, pads, stripSheetPath)
					report["vsPcbPads"] = cmpP
					fmt.Fprintf(stderr, "KiCad netlist vs board pads: %d/%d nets equal (%d same name), only-schematic pins %d, only-board pins %d\n",
						cmpP.NetsEqual, cmpP.NetsA, cmpP.NamesEqual, len(cmpP.OnlyA), len(cmpP.OnlyB))
				}
			}
			if err := writeJSON(stdout, report); err != nil {
				return err
			}
			if failed {
				return fmt.Errorf("the KiCad netlist differs from the EasyEDA connectivity")
			}
			return nil
		},
	}
	c.Flags().StringVar(&epro, "epro", "", "EasyEDA Pro project export (.epro, required)")
	c.Flags().StringVar(&board, "board", "", "board name in the project (required when the project has several)")
	c.Flags().StringVar(&out, "out", "", "output directory (required)")
	c.Flags().StringVar(&name, "name", "", "root file name without extension (default: the board name)")
	c.Flags().StringVar(&pcb, "pcb", "", "KiCad board of the same design: footprint names by reference, and pad nets to compare")
	c.Flags().BoolVar(&noCheck, "no-check", false, "skip the kicad-cli netlist comparison")
	_ = c.MarkFlagRequired("epro")
	_ = c.MarkFlagRequired("out")
	return c
}

// stripSheetPath drops KiCad's "/Sheet/" prefix of sheet-local net names.
func stripSheetPath(n string) string {
	if strings.HasPrefix(n, "/") {
		return n[strings.LastIndex(n, "/")+1:]
	}
	return n
}

func newKicadSchNetlistCmd(stdout, stderr io.Writer) *cobra.Command {
	var sch, out, projectID string
	c := &cobra.Command{
		Use:   "netlist",
		Short: "Schematic connectivity JSON (the IR intent derive and pad-net-diff read) from a .kicad_sch",
		Long: `Runs 'kicad-cli sch export netlist --format kicadxml' on the root sheet and
converts it into pcbpilot's schematic connectivity IR (schemaVersion 1.4 —
the shape 'sch connectivity' writes; 'intent derive --connectivity' and
pad-net-diff read it). Power symbols are not components; sheet-local names
"/Sheet/NAME" become NAME when unique in the design; single-pin
"unconnected-(…)" nets become connectionState "unconnected" (or noConnected
on a no-connect flag); the LCSC field becomes device.supplierId.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			nl, err := kicad.ExportSchNetlist(sch)
			if err != nil {
				return err
			}
			if projectID == "" {
				projectID = strings.TrimSuffix(filepath.Base(sch), ".kicad_sch")
			}
			doc, err := nl.ToConnectivityDoc(projectID)
			if err != nil {
				return err
			}
			fmt.Fprintf(stderr, "%d components, %d nets, %d connections\n", len(doc.Components), len(doc.Nets), len(doc.Connections))
			if out == "" || out == "-" {
				return writeJSON(stdout, doc)
			}
			b, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				return err
			}
			return os.WriteFile(out, append(b, '\n'), 0o644)
		},
	}
	c.Flags().StringVar(&sch, "sch", "", "root .kicad_sch (required)")
	c.Flags().StringVar(&out, "out", "", "output JSON (default stdout)")
	c.Flags().StringVar(&projectID, "project-id", "", "projectId written into the document (default: the sheet name)")
	_ = c.MarkFlagRequired("sch")
	return c
}

// ---- sch --backend kicad -------------------------------------------------------

// kicadSchTarget reports whether the sch command runs on the KiCad backend
// and which .kicad_sch it edits.
func kicadSchTarget(cmd *cobra.Command) (string, bool, error) {
	backend, _ := cmd.Flags().GetString("backend")
	file, _ := cmd.Flags().GetString("kicad-sch")
	switch backend {
	case "", "easyeda":
		if file != "" {
			return "", false, fmt.Errorf("--kicad-sch needs --backend kicad")
		}
		return "", false, nil
	case "kicad":
		if file == "" {
			return "", false, fmt.Errorf("--backend kicad needs --kicad-sch <sheet.kicad_sch>")
		}
		return file, true, nil
	}
	return "", false, fmt.Errorf("unknown --backend %q (easyeda or kicad)", backend)
}

func kicadSchEdit(path string, stdout io.Writer, edit func(e *kicad.SchEditor) (map[string]any, error)) error {
	e, err := kicad.OpenSchematicFile(path)
	if err != nil {
		return err
	}
	res, err := edit(e)
	if err != nil {
		return err
	}
	text, err := e.Render()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return err
	}
	if res == nil {
		res = map[string]any{}
	}
	res["ok"], res["file"], res["backend"] = true, path, "kicad"
	return writeJSON(stdout, res)
}

// kicadSymbolText resolves "Lib:Name" from --symbol-lib (a .kicad_sym), the
// sheet's directory (<Lib>.kicad_sym) or KiCad's stock libraries.
func kicadSymbolText(sheet, libID, symLib string) (string, error) {
	lib, name, ok := strings.Cut(libID, ":")
	if !ok {
		return "", fmt.Errorf("--symbol must be Lib:Name, got %q", libID)
	}
	var cands []string
	if symLib != "" {
		cands = append(cands, symLib)
	}
	cands = append(cands, filepath.Join(filepath.Dir(sheet), lib+".kicad_sym"))
	if d := os.Getenv("KICAD10_SYMBOL_DIR"); d != "" {
		cands = append(cands, filepath.Join(d, lib+".kicad_sym"))
	}
	if d := kicad.StockSymbolDir(); d != "" { // PCBPILOT_KICAD_SYMBOLS or the installed KiCad's
		cands = append(cands, filepath.Join(d, lib+".kicad_sym"))
	}
	cands = append(cands, "/Applications/KiCad/KiCad.app/Contents/SharedSupport/symbols/"+lib+".kicad_sym",
		"/usr/share/kicad/symbols/"+lib+".kicad_sym")
	for _, p := range cands {
		if _, err := os.Stat(p); err == nil {
			return kicad.LibSymbolFromFile(p, name)
		}
	}
	return "", fmt.Errorf("symbol library %s.kicad_sym not found (pass --symbol-lib)", lib)
}

func kicadSchPlace(path string, libID, symLib, ref, value, footprint, lcsc string, unit int, x, y, rot float64, mirror bool, stdout io.Writer) error {
	if libID == "" || ref == "" {
		return fmt.Errorf("--backend kicad place needs --symbol Lib:Name and --designator")
	}
	return kicadSchEdit(path, stdout, func(e *kicad.SchEditor) (map[string]any, error) {
		if !e.HasLibSymbol(libID) {
			t, err := kicadSymbolText(path, libID, symLib)
			if err != nil {
				return nil, err
			}
			if err := e.AddLibSymbol(libID, t); err != nil {
				return nil, err
			}
		}
		if unit < 1 {
			unit = 1
		}
		if err := kicad.ValidReference(ref); err != nil {
			return nil, err
		}
		if err := e.HierarchyErr(); err != nil {
			return nil, fmt.Errorf("placing %s needs the project's sheet hierarchy, which is unknown: %v (open the sheet inside its KiCad project)", ref, err)
		}
		if e.RepeatedSheet() {
			return nil, fmt.Errorf("this sheet is used by several sheet symbols (each use has its own references); placing symbols on it is not supported")
		}
		if others := e.RefElsewhere(ref); len(others) > 0 {
			return nil, fmt.Errorf("%s is already placed on %s; place every unit of a component on one sheet", ref, strings.Join(others, ", "))
		}
		if n, err := e.LibUnitCount(libID); err != nil {
			return nil, err
		} else if unit > n {
			return nil, fmt.Errorf("%s has %d unit(s); --unit %d does not exist", libID, n, unit)
		}
		var fields []kicad.Field
		if placed := e.SymbolUnits(ref); len(placed) > 0 {
			// another unit of a placed symbol: same library symbol, shared fields
			if lid := e.SymbolLibID(ref); lid != libID {
				return nil, fmt.Errorf("%s is already on this sheet as %s; another unit must use the same --symbol", ref, lid)
			}
			for _, u := range placed {
				if u == unit {
					return nil, fmt.Errorf("%s unit %d is already on this sheet (placed units: %v; pass --unit N for another)", ref, unit, placed)
				}
			}
			inherited, err := kicadInheritUnitFields(e.SymbolUnitFields(ref), ref, value, footprint, lcsc)
			if err != nil {
				return nil, err
			}
			// the standard fields come through Value/Footprint (one each, with
			// the library's position); the rest are passed as extra fields
			for _, f := range inherited {
				switch f.Name {
				case "Value":
					value = f.Value
				case "Footprint":
					footprint = f.Value
				default:
					fields = append(fields, f)
				}
			}
		} else if lcsc != "" {
			fields = append(fields, kicad.Field{Name: "LCSC", Value: lcsc, Hide: true})
		}
		m := ""
		if mirror {
			m = "y"
		}
		id, err := e.PlaceSymbol(kicad.SymbolInstance{LibID: libID, Ref: ref, Unit: unit, At: kicad.Pt{X: x, Y: y}, Rot: rot, Mirror: m,
			Value: value, Footprint: footprint, Fields: fields})
		if err != nil {
			return nil, err
		}
		pins, _ := e.SymbolPinPositions(fmt.Sprintf("%s:%d", ref, unit))
		return map[string]any{"uuid": id, "designator": ref, "unit": unit, "units": e.SymbolUnits(ref), "pins": pins}, nil
	})
}

// kicadInheritUnitFields gives a new unit of a placed symbol the fields its
// placed units share (KiCad keeps them equal on every unit). Units that
// already disagree — a value, or a field one of them lacks — are refused, as
// is a --value, --footprint or --lcsc that differs from them or names a
// field they do not have, so the units never diverge (sch modify --id REF
// sets a field on every unit).
func kicadInheritUnitFields(byUnit map[int][]kicad.Field, ref, value, footprint, lcsc string) ([]kicad.Field, error) {
	units := make([]int, 0, len(byUnit))
	for u := range byUnit {
		units = append(units, u)
	}
	sort.Ints(units)
	if len(units) == 0 {
		return nil, nil
	}
	// the fields of the first unit, checked against every other unit
	first := byUnit[units[0]]
	val := map[string]string{}
	for _, f := range first {
		val[f.Name] = f.Value
	}
	for _, u := range units[1:] {
		seen := map[string]bool{}
		for _, f := range byUnit[u] {
			seen[f.Name] = true
			if f.Name == "Reference" {
				continue
			}
			v, ok := val[f.Name]
			if !ok {
				return nil, fmt.Errorf("%s: unit %d has a %s field unit %d lacks; make the placed units agree first (sch modify --id %s)", ref, u, f.Name, units[0], ref)
			}
			if v != f.Value {
				return nil, fmt.Errorf("%s: %s is %q on unit %d but %q on unit %d; make the placed units agree first (sch modify --id %s)", ref, f.Name, v, units[0], f.Value, u, ref)
			}
		}
		for name := range val {
			if name != "Reference" && !seen[name] {
				return nil, fmt.Errorf("%s: unit %d lacks the %s field unit %d has; make the placed units agree first (sch modify --id %s)", ref, u, name, units[0], ref)
			}
		}
	}
	given := map[string]string{"Value": value, "Footprint": footprint, "LCSC": lcsc}
	var out []kicad.Field
	for _, f := range first {
		if f.Name == "Reference" {
			continue
		}
		if g := given[f.Name]; g != "" && g != f.Value {
			return nil, fmt.Errorf("%s: %s is %q on its placed units, not %q (units share fields; change them with sch modify --id %s)", ref, f.Name, f.Value, g, ref)
		}
		delete(given, f.Name)
		out = append(out, kicad.Field{Name: f.Name, Value: f.Value, Hide: f.Hide})
	}
	for _, name := range []string{"Value", "Footprint", "LCSC"} {
		if g := given[name]; g != "" {
			return nil, fmt.Errorf("%s: its placed units have no %s field, so a new unit cannot get %q alone (set it on every unit first: sch modify --id %s); omit it here", ref, name, g, ref)
		}
	}
	return out, nil
}

// kicadPinOrPoint parses "x,y" (mm) or "REF.PIN" (that pin's position).
func kicadPinOrPoint(e *kicad.SchEditor, s string) (kicad.Pt, error) {
	var x, y float64
	if n, _ := fmt.Sscanf(s, "%g,%g", &x, &y); n == 2 {
		return kicad.Pt{X: x, Y: y}, nil
	}
	ref, pin, ok := strings.Cut(s, ".")
	if !ok {
		return kicad.Pt{}, fmt.Errorf("point %q: want x,y (mm) or REF.PIN", s)
	}
	pins, err := e.SymbolPinPositions(ref)
	if err != nil {
		return kicad.Pt{}, err
	}
	p, ok := pins[pin]
	if !ok {
		return kicad.Pt{}, fmt.Errorf("%s has no pin %s", ref, pin)
	}
	return p, nil
}

// kicadSchWire draws a polyline through points ([[x,y],…] JSON in mm, or
// REF.PIN / x,y tokens); --net puts a local label on its first point.
func kicadSchWire(path, pointsJSON string, through []string, net string, stdout io.Writer) error {
	return kicadSchEdit(path, stdout, func(e *kicad.SchEditor) (map[string]any, error) {
		var pts []kicad.Pt
		if pointsJSON != "" {
			var raw []any
			if err := json.Unmarshal([]byte(pointsJSON), &raw); err != nil {
				return nil, fmt.Errorf("invalid --points json: %w", err)
			}
			var flat []float64
			for _, v := range raw {
				switch t := v.(type) {
				case []any:
					for _, w := range t {
						f, _ := w.(float64)
						flat = append(flat, f)
					}
				case float64:
					flat = append(flat, t)
				}
			}
			for i := 0; i+1 < len(flat); i += 2 {
				pts = append(pts, kicad.Pt{X: flat[i], Y: flat[i+1]})
			}
		}
		for _, s := range through {
			p, err := kicadPinOrPoint(e, s)
			if err != nil {
				return nil, err
			}
			pts = append(pts, p)
		}
		if len(pts) < 2 {
			return nil, fmt.Errorf("a wire needs at least two points")
		}
		e.AddWire(pts...)
		if net != "" {
			if err := e.AddLabel(kicad.LabelLocal, net, pts[0], 0, ""); err != nil {
				return nil, err
			}
		}
		return map[string]any{"points": pts, "segments": len(pts) - 1}, nil
	})
}

// kicadSchNetflag: power/ground kinds → power symbol; net ports → global
// label; net labels → local label. --at REF.PIN anchors it on a pin.
func kicadSchNetflag(path, kind, net, at string, x, y, rot float64, stdout io.Writer) error {
	return kicadSchEdit(path, stdout, func(e *kicad.SchEditor) (map[string]any, error) {
		p := kicad.Pt{X: x, Y: y}
		if at != "" {
			var err error
			if p, err = kicadPinOrPoint(e, at); err != nil {
				return nil, err
			}
		}
		switch kind {
		case "power":
			ref, err := e.AddPower(net, p, rot, false)
			return map[string]any{"designator": ref, "kind": "power"}, err
		case "ground", "analog_ground", "protective_ground", "protect_ground":
			ref, err := e.AddPower(net, p, rot, true)
			return map[string]any{"designator": ref, "kind": "ground"}, err
		case "net_port_in", "net_port_out", "net_port_bi":
			shape := map[string]string{"net_port_in": "input", "net_port_out": "output", "net_port_bi": "bidirectional"}[kind]
			return map[string]any{"kind": "global_label"}, e.AddLabel(kicad.LabelGlobal, net, p, rot, shape)
		case "net_label", "netlabel":
			return map[string]any{"kind": "label"}, e.AddLabel(kicad.LabelLocal, net, p, rot, "")
		}
		return nil, fmt.Errorf("--backend kicad: netflag kind %q not supported", kind)
	})
}

func kicadSchNoConnect(path, ref string, pins []string, stdout io.Writer) error {
	return kicadSchEdit(path, stdout, func(e *kicad.SchEditor) (map[string]any, error) {
		pos, err := e.SymbolPinPositions(ref)
		if err != nil {
			return nil, err
		}
		for _, n := range pins {
			p, ok := pos[n]
			if !ok {
				return nil, fmt.Errorf("%s has no pin %s", ref, n)
			}
			e.AddNoConnect(p)
		}
		return map[string]any{"designator": ref, "pins": pins}, nil
	})
}

// kicadSchModify: --id is the reference, or REF:UNIT for one unit of a
// multi-unit symbol; x/y/rotation move that symbol unit (wires are not
// dragged; a bare REF with several units placed is refused), designator
// renames, the patch's customAttributes/otherProperty (and
// name/supplierId/manufacturerId shortcuts) set fields — a rename and the
// fields always apply to every unit of the reference.
func kicadSchModify(path, id string, patch map[string]any, stdout io.Writer) error {
	ref, unit, err := kicad.SplitUnitKey(id)
	if err != nil {
		return err
	}
	return kicadSchEdit(path, stdout, func(e *kicad.SchEditor) (map[string]any, error) {
		set := map[string]string{}
		for _, k := range []string{"customAttributes", "otherProperty"} {
			if m, ok := patch[k].(map[string]any); ok {
				for f, v := range m {
					set[f] = fmt.Sprint(v)
				}
			}
		}
		for pk, field := range map[string]string{"name": "Value", "supplierId": "LCSC", "manufacturerId": "MPN", "manufacturer": "Manufacturer"} {
			if v, ok := patch[pk]; ok {
				set[field] = fmt.Sprint(v)
			}
		}
		_, hasX := patch["x"]
		_, hasY := patch["y"]
		_, hasR := patch["rotation"]
		if hasX || hasY || hasR {
			x, y, r, err := e.SymbolAt(id)
			if err != nil {
				return nil, err
			}
			if v, ok := patch["x"].(float64); ok {
				x = v
			}
			if v, ok := patch["y"].(float64); ok {
				y = v
			}
			if v, ok := patch["rotation"].(float64); ok {
				r = v
			}
			if err := e.MoveSymbol(id, kicad.Pt{X: x, Y: y}, r); err != nil {
				return nil, err
			}
		}
		units := e.SymbolUnits(ref)
		if len(units) == 0 {
			return nil, fmt.Errorf("no symbol %s on this sheet", ref)
		}
		if unit > 0 { // an explicit unit must be placed, field-only edits included
			if _, _, _, err := e.SymbolAt(id); err != nil {
				return nil, err
			}
		}
		// a Reference field in the patch is a rename like --designator
		rename, hasRename := patch["designator"].(string)
		if r, ok := set["Reference"]; ok {
			if hasRename && r != rename {
				return nil, fmt.Errorf("designator %q and Reference field %q disagree", rename, r)
			}
			rename, hasRename = r, true
			delete(set, "Reference")
		}
		keys := make([]string, 0, len(set))
		for k := range set {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := e.SetField(ref, k, set[k]); err != nil {
				return nil, err
			}
		}
		if hasRename && rename != ref {
			if err := e.SetField(ref, "Reference", rename); err != nil { // validates and checks collisions
				return nil, err
			}
		}
		return map[string]any{"designator": ref, "units": units, "fields": set}, nil
	})
}

// kicadSchDelete is `sch prim-delete --backend kicad`: each id is a
// designator (every placed unit) or REF:UNIT (that unit only). Wires and
// flags on the removed pins stay; overlapping ids (U1:1,U1) delete each unit
// once; all or nothing.
func kicadSchDelete(path string, ids []string, stdout io.Writer) error {
	return kicadSchEdit(path, stdout, func(e *kicad.SchEditor) (map[string]any, error) {
		removed, err := e.DeleteSymbols(ids)
		if err != nil {
			return nil, err
		}
		return map[string]any{"deleted": removed}, nil
	})
}
