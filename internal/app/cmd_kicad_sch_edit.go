package app

// cmd_kicad_sch_edit.go — incremental, high-level edits of a project built
// by `kicad sch-build`, one call each: `kicad sch-edit` (add/remove/replace
// parts and blocks, connect/disconnect by pin name, rename nets, move
// zones), `kicad sch-checkpoint list|restore`, and `kicad sch-read` (the
// compact parts/nets JSON the AI reads instead of the S-expression; it is a
// valid sch-build spec). An edit rewrites the stored spec and rebuilds: only
// the zones whose inputs changed are re-planned, every other zone keeps its
// drawing, position and symbol uuids. Same hard gates as sch-build; the
// netlist gate is transactional (nothing written on a mismatch).

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// sbOp is one edit operation.
type sbOp struct {
	Op    string            `json:"op"`
	Part  *sbPart           `json:"part,omitempty"`  // add_part
	Block *sbBlock          `json:"block,omitempty"` // add_block
	Ref   string            `json:"ref,omitempty"`   // remove_part / replace_part / set_value / set_field
	With  *sbPart           `json:"with,omitempty"`  // replace_part
	Net   string            `json:"net,omitempty"`   // connect / disconnect
	Pins  []string          `json:"pins,omitempty"`  // connect / disconnect (REF:PIN, pin number or name)
	Kind  string            `json:"kind,omitempty"`  // connect: power | ground | signal for a new net
	From  string            `json:"from,omitempty"`  // rename_net
	To    string            `json:"to,omitempty"`    // rename_net
	Zone  string            `json:"zone,omitempty"`  // move_zone
	Page  string            `json:"page,omitempty"`  // move_zone
	At    *kicad.Pt         `json:"at,omitempty"`    // move_zone: frame top-left (mm)
	Name  string            `json:"name,omitempty"`  // set_field
	Value string            `json:"value,omitempty"` // set_value / set_field
	Title *sbTitle          `json:"title,omitempty"` // set_title
	Extra map[string]string `json:"-"`
}

type sbDelta struct {
	Schema string `json:"schema,omitempty"`
	Ops    []sbOp `json:"ops"`
}

func loadSbDelta(path string) (*sbDelta, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d sbDelta
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(d.Ops) == 0 {
		return nil, fmt.Errorf("%s: no ops", path)
	}
	return &d, nil
}

// applySbOps edits a stored (normalized: pins REF:NUMBER) spec. old is the
// design built from it (pin names for replace/connect/disconnect).
func applySbOps(s *sbSpec, old *sbDesign, ops []sbOp) (*sbEditCtx, []string, error) {
	ec := &sbEditCtx{Rename: map[string]string{}, ZoneAt: map[string]kicad.Pt{}}
	var log []string
	partIdx := func(ref string) int {
		for i, p := range s.Parts {
			if p.Ref == ref {
				return i
			}
		}
		return -1
	}
	// expand a pin token of an existing part to REF:NUM keys
	pinKeys := func(tok string) ([]string, error) {
		ref, pin, ok := sbSplitPin(tok)
		if !ok {
			return nil, fmt.Errorf("bad pin %q (want REF:PIN)", tok)
		}
		p := old.ByRef[ref]
		if p == nil {
			return nil, nil // a part added by this delta: resolved at build
		}
		nums, err := p.resolvePins(pin)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, n := range nums {
			out = append(out, ref+":"+n)
		}
		return out, nil
	}
	dropPins := func(keys map[string]bool) {
		for i := range s.Nets {
			var keep []string
			for _, p := range s.Nets[i].Pins {
				if !keys[p] {
					keep = append(keep, p)
				}
			}
			s.Nets[i].Pins = keep
		}
		var nc []string
		for _, p := range s.NoConnect {
			if !keys[p] {
				nc = append(nc, p)
			}
		}
		s.NoConnect = nc
	}
	for i, op := range ops {
		where := fmt.Sprintf("ops[%d] %s", i, op.Op)
		switch op.Op {
		case "add_part":
			if op.Part == nil || op.Part.Ref == "" {
				return nil, nil, fmt.Errorf("%s: needs part.ref", where)
			}
			if partIdx(op.Part.Ref) >= 0 {
				return nil, nil, fmt.Errorf("%s: %s exists", where, op.Part.Ref)
			}
			s.Parts = append(s.Parts, *op.Part)
			log = append(log, "add "+op.Part.Ref)
		case "add_block":
			if op.Block == nil || op.Block.Block == "" {
				return nil, nil, fmt.Errorf("%s: needs block.block", where)
			}
			s.Blocks = append(s.Blocks, *op.Block)
			log = append(log, "add block "+op.Block.Block)
		case "remove_part":
			j := partIdx(op.Ref)
			if j < 0 {
				return nil, nil, fmt.Errorf("%s: no part %s", where, op.Ref)
			}
			s.Parts = append(s.Parts[:j], s.Parts[j+1:]...)
			keys := map[string]bool{}
			if p := old.ByRef[op.Ref]; p != nil {
				for _, q := range p.LPins {
					keys[op.Ref+":"+q.Number] = true
				}
			}
			dropPins(keys)
			log = append(log, "remove "+op.Ref)
		case "replace_part":
			j := partIdx(op.Ref)
			if j < 0 || op.With == nil {
				return nil, nil, fmt.Errorf("%s: needs an existing ref and with", where)
			}
			p := old.ByRef[op.Ref]
			// connections follow the pin NAMES of the old symbol
			names := map[string]string{}
			if p != nil {
				for _, q := range p.LPins {
					if q.Name != "" && q.Name != "~" {
						names[q.Number] = q.Name
					}
				}
			}
			for ni := range s.Nets {
				seen := map[string]bool{}
				var pins []string
				for _, tok := range s.Nets[ni].Pins {
					r, num, _ := sbSplitPin(tok)
					if r == op.Ref {
						if nm, ok := names[num]; ok {
							tok = r + ":" + nm
						}
					}
					if !seen[tok] {
						seen[tok] = true
						pins = append(pins, tok)
					}
				}
				s.Nets[ni].Pins = pins
			}
			var nc []string
			for _, tok := range s.NoConnect {
				if r, _, _ := sbSplitPin(tok); r != op.Ref {
					nc = append(nc, tok)
				}
			}
			s.NoConnect = nc
			w := *op.With
			w.Ref = op.Ref
			if w.Zone == "" {
				w.Zone = s.Parts[j].Zone
			}
			s.Parts[j] = w
			log = append(log, "replace "+op.Ref)
		case "connect":
			if op.Net == "" || len(op.Pins) == 0 {
				return nil, nil, fmt.Errorf("%s: needs net and pins", where)
			}
			keys := map[string]bool{}
			var add []string
			for _, tok := range op.Pins {
				ks, err := pinKeys(tok)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: %w", where, err)
				}
				if ks == nil {
					add = append(add, tok)
				}
				for _, k := range ks {
					keys[k] = true
					add = append(add, k)
				}
			}
			dropPins(keys) // a pin moves from its old net
			found := false
			for ni := range s.Nets {
				if s.Nets[ni].Name == op.Net {
					s.Nets[ni].Pins = append(s.Nets[ni].Pins, add...)
					found = true
				}
			}
			if !found {
				s.Nets = append(s.Nets, sbNet{Name: op.Net, Pins: add, Kind: op.Kind})
			}
			log = append(log, fmt.Sprintf("connect %s: %s", op.Net, strings.Join(add, " ")))
		case "disconnect":
			keys := map[string]bool{}
			for _, tok := range op.Pins {
				ks, err := pinKeys(tok)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: %w", where, err)
				}
				for _, k := range ks {
					keys[k] = true
				}
			}
			if op.Net != "" && len(op.Pins) == 0 { // the whole net
				for _, n := range s.Nets {
					if n.Name == op.Net {
						for _, p := range n.Pins {
							keys[p] = true
						}
					}
				}
			}
			if len(keys) == 0 {
				return nil, nil, fmt.Errorf("%s: nothing to disconnect", where)
			}
			dropPins(keys)
			log = append(log, fmt.Sprintf("disconnect %d pin(s)", len(keys)))
		case "rename_net":
			if op.From == "" || op.To == "" {
				return nil, nil, fmt.Errorf("%s: needs from and to", where)
			}
			n := 0
			for ni := range s.Nets {
				if s.Nets[ni].Name == op.From {
					s.Nets[ni].Name = op.To
					n++
				}
			}
			if n == 0 {
				return nil, nil, fmt.Errorf("%s: no net %s", where, op.From)
			}
			for ri := range s.Rails {
				if s.Rails[ri].Net == op.From {
					s.Rails[ri].Net = op.To
				}
			}
			ec.Rename[op.From] = op.To
			log = append(log, "rename "+op.From+" → "+op.To)
		case "move_zone", "move_block":
			zi := -1
			for k, z := range s.Zones {
				if z.ID == op.Zone {
					zi = k
				}
			}
			if zi < 0 {
				return nil, nil, fmt.Errorf("%s: no zone %q", where, op.Zone)
			}
			if op.Page != "" {
				s.Zones[zi].Page = op.Page
				hasPage := false
				for _, pg := range s.Pages {
					hasPage = hasPage || pg.ID == op.Page
				}
				if !hasPage {
					s.Pages = append(s.Pages, sbPage{ID: op.Page})
				}
			}
			if op.At != nil {
				ec.ZoneAt[op.Zone] = *op.At
			}
			log = append(log, "move zone "+op.Zone)
		case "set_value":
			j := partIdx(op.Ref)
			if j < 0 {
				return nil, nil, fmt.Errorf("%s: no part %s", where, op.Ref)
			}
			s.Parts[j].Value = op.Value
			log = append(log, "value "+op.Ref+"="+op.Value)
		case "set_field":
			j := partIdx(op.Ref)
			if j < 0 || op.Name == "" {
				return nil, nil, fmt.Errorf("%s: needs an existing ref and name", where)
			}
			if s.Parts[j].Fields == nil {
				s.Parts[j].Fields = map[string]string{}
			}
			s.Parts[j].Fields[op.Name] = op.Value
			log = append(log, "field "+op.Ref+"."+op.Name)
		case "set_title":
			if op.Title == nil {
				return nil, nil, fmt.Errorf("%s: needs title", where)
			}
			t := *op.Title
			if t.Title != "" {
				s.Title.Title = t.Title
			}
			if t.Date != "" {
				s.Title.Date = t.Date
			}
			if t.Rev != "" {
				s.Title.Rev = t.Rev
			}
			if t.Company != "" {
				s.Title.Company = t.Company
			}
			if len(t.Comments) > 0 {
				s.Title.Comments = t.Comments
			}
			log = append(log, "title")
		default:
			return nil, nil, fmt.Errorf("%s: unknown op (add_part, add_block, remove_part, replace_part, connect, disconnect, rename_net, move_zone, set_value, set_field, set_title)", where)
		}
	}
	// nets an edit emptied disappear; a pin left on no net becomes NC
	var nets []sbNet
	for _, n := range s.Nets {
		if len(n.Pins) > 0 {
			nets = append(nets, n)
		}
	}
	s.Nets = nets
	return ec, log, nil
}

func newKicadSchEditCmd(stdout, stderr io.Writer) *cobra.Command {
	var o sbOptions
	var delta string
	c := &cobra.Command{
		Use:   "sch-edit",
		Short: "Change a sch-build project in one call (add/remove/replace parts or blocks, connect by pin name, rename nets, move zones)",
		Args:  cobra.NoArgs,
		Long: `Applies a delta to the spec stored by kicad sch-build (<project>/.pcbpilot/sch-build/spec.json)
and rebuilds: only zones whose parts or connections changed are re-planned; every other zone
keeps its drawing, position and symbol uuids (the schematic ↔ PCB link). Pins are named
REF:PIN with a pin number or a pin NAME (all pins of that name; NAME* = prefix).

  {"ops":[
    {"op":"add_part","part":{"ref":"R10","value":"10k","symbol":"Device:R","zone":"mcu"}},
    {"op":"add_block","block":{"block":"block.ams1117_ldo_3v3","zone":"ldo","page":"power","bind":{"VIN_5V":"VSYS_5V"}}},
    {"op":"remove_part","ref":"R9"},
    {"op":"replace_part","ref":"U2","with":{"lcsc":"C6186","value":"AMS1117-3.3"}},
    {"op":"connect","net":"I2C_SDA","pins":["U3:IO8","R10:2"]},
    {"op":"disconnect","pins":["U3:IO2"]},
    {"op":"rename_net","from":"LED_CTRL","to":"LED_EN"},
    {"op":"move_zone","zone":"led","page":"power","at":{"X":200,"Y":40}},
    {"op":"set_value","ref":"C1","value":"1uF"},
    {"op":"set_field","ref":"U1","name":"MPN","value":"CH340C"},
    {"op":"set_title","title":{"rev":"B"}}
  ]}

Same gates as sch-build; KiCad netlist == edited spec is transactional (nothing written on a
mismatch). Each successful edit saves a checkpoint (kicad sch-checkpoint list|restore N).
--dry-run prints the plan (zones re-planned/reused, coordinates, connections) and writes nothing.`,
		Example: `  pcbpilot kicad sch-edit --project build/ --spec delta.json
  pcbpilot kicad sch-edit --project build/ --spec delta.json --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.OutDir == "" || delta == "" {
				return fmt.Errorf("--project and --spec are required")
			}
			d, err := loadSbDelta(delta)
			if err != nil {
				return err
			}
			res, err := runSchEdit(o, d.Ops)
			return finishSbRun(res, err, o.Report, stdout, stderr)
		},
	}
	f := c.Flags()
	f.StringVar(&o.OutDir, "project", "", "project directory written by kicad sch-build")
	f.StringVar(&delta, "spec", "", "delta JSON {\"ops\":[…]}")
	sbFlags(c, &o)
	return c
}

// runSchEdit loads the stored spec, applies ops and rebuilds incrementally.
func runSchEdit(o sbOptions, ops []sbOp) (*sbReport, error) {
	out, err := filepath.Abs(o.OutDir)
	if err != nil {
		return nil, err
	}
	o.OutDir = out
	st, spec := loadSbState(out)
	if st == nil || spec == nil {
		return nil, fmt.Errorf("%s has no sch-build state (.pcbpilot/sch-build); build it with kicad sch-build first", out)
	}
	cp := *spec
	old, _, err := buildDesign(&cp, sbResolveOpts{OutDir: out, Offline: true, Jobs: o.Jobs, PartsPath: o.PartsPath})
	if err != nil {
		return nil, fmt.Errorf("stored spec: %w", err)
	}
	next := *spec
	next.Parts = append([]sbPart(nil), spec.Parts...)
	next.Nets = nil
	for _, n := range spec.Nets {
		nn := n
		nn.Pins = append([]string(nil), n.Pins...)
		next.Nets = append(next.Nets, nn)
	}
	next.Zones = append([]sbZone(nil), spec.Zones...)
	next.Pages = append([]sbPage(nil), spec.Pages...)
	ec, log, err := applySbOps(&next, old, ops)
	if err != nil {
		return nil, err
	}
	// zones left without parts disappear
	used := map[string]bool{}
	for _, p := range next.Parts {
		used[p.Zone] = true
	}
	for _, b := range next.Blocks {
		used[b.Zone] = true
	}
	var zs []sbZone
	for _, z := range next.Zones {
		if used[z.ID] || z.ID == "" {
			zs = append(zs, z)
		}
	}
	next.Zones = zs
	o.Name = st.Name
	ec.Summary = map[string]any{"ops": log}
	return runSchBuild(&next, o, ec)
}

// ── checkpoints ─────────────────────────────────────────────────────────────

func newKicadSchCheckpointCmd(stdout io.Writer) *cobra.Command {
	var project string
	c := &cobra.Command{
		Use:   "sch-checkpoint list|restore N",
		Short: "List or restore the checkpoints kicad sch-build / sch-edit saved",
		Args:  cobra.RangeArgs(1, 2),
		Long: `Every successful sch-build / sch-edit copies the project's sheets, libraries, spec and
build state to <project>/.pcbpilot/checkpoints/N. restore N puts checkpoint N back
(sheets, libraries, spec, state); files the later builds added are removed.`,
		Example: `  pcbpilot kicad sch-checkpoint list --project build/
  pcbpilot kicad sch-checkpoint restore 3 --project build/`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" {
				return fmt.Errorf("--project is required")
			}
			out, err := filepath.Abs(project)
			if err != nil {
				return err
			}
			switch args[0] {
			case "list":
				list, _ := listSbCheckpoints(out)
				if list == nil {
					list = []sbCheckpointMeta{}
				}
				return writeJSON(stdout, map[string]any{"project": out, "checkpoints": list})
			case "restore":
				if len(args) != 2 {
					return fmt.Errorf("restore needs N")
				}
				n, err := strconv.Atoi(args[1])
				if err != nil || n < 1 {
					return fmt.Errorf("checkpoint %q: want a positive number", args[1])
				}
				meta, err := restoreSbCheckpoint(out, n)
				if err != nil {
					return err
				}
				return writeJSON(stdout, map[string]any{"ok": true, "project": out, "restored": meta})
			}
			return fmt.Errorf("unknown action %q (list | restore N)", args[0])
		},
	}
	c.Flags().StringVar(&project, "project", "", "project directory written by kicad sch-build")
	return c
}

func restoreSbCheckpoint(out string, n int) (*sbCheckpointMeta, error) {
	dir := filepath.Join(sbCheckpointDir(out), strconv.Itoa(n))
	b, err := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
	if err != nil {
		return nil, fmt.Errorf("no checkpoint %d in %s", n, out)
	}
	var meta sbCheckpointMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return nil, err
	}
	for _, f := range meta.Files {
		if _, err := sbSafeJoin(out, f); err != nil {
			return nil, fmt.Errorf("checkpoint %d: %w", n, err)
		}
	}
	if st, _ := loadSbState(out); st != nil {
		for _, f := range st.Files {
			if !sbContains(meta.Files, f) {
				if p, err := sbSafeJoin(out, f); err == nil {
					_ = os.Remove(p)
				}
			}
		}
	}
	if err := copySbFiles(dir, out, meta.Files); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(sbMetaDir(out), 0o755); err != nil {
		return nil, err
	}
	if err := copySbFiles(filepath.Join(dir, ".sch-build"), sbMetaDir(out), []string{"spec.json", "state.json"}); err != nil {
		return nil, err
	}
	return &meta, nil
}

// ── compact read ────────────────────────────────────────────────────────────

func newKicadSchReadCmd(stdout io.Writer) *cobra.Command {
	var project, sch, outPath string
	c := &cobra.Command{
		Use:   "sch-read",
		Short: "Compact parts/nets JSON of a KiCad schematic (a valid sch-build spec; read this, not the S-expression)",
		Args:  cobra.NoArgs,
		Long: `Reads a KiCad schematic through kicad-cli's netlist and prints a small JSON: parts (ref,
value, lcsc, symbol, footprint, zone), nets (name, pins REF:NUMBER) and no-connect pins.
It is a pcbpilot.kicad.sch-build/1 spec: kicad sch-build --spec turns it back into the
same netlist (project libraries are referenced through libDirs). On a project built by
sch-build the zones, pages, title block and rails come from its stored spec.`,
		Example: `  pcbpilot kicad sch-read --project build/
  pcbpilot kicad sch-read --sch board.kicad_sch --out board.spec.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if (project == "") == (sch == "") {
				return fmt.Errorf("give exactly one of --project or --sch")
			}
			spec, err := readSbSpec(project, sch)
			if err != nil {
				return err
			}
			if outPath == "" && len(spec.Parts) > 40 { // keep the AI's context small
				base := project
				if base == "" {
					base = filepath.Dir(sch)
				}
				outPath = filepath.Join(sbMetaDir(base), "sch-read.json")
				if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
					return err
				}
			}
			if outPath != "" {
				if err := writeJSONFile(outPath, spec); err != nil {
					return err
				}
				return writeJSON(stdout, map[string]any{"ok": true, "path": outPath, "parts": len(spec.Parts), "nets": len(spec.Nets),
					"note": "spec written to path (large designs are spilled to a file)"})
			}
			return writeJSON(stdout, spec)
		},
	}
	c.Flags().StringVar(&project, "project", "", "project directory (its root .kicad_sch)")
	c.Flags().StringVar(&sch, "sch", "", "root .kicad_sch")
	c.Flags().StringVar(&outPath, "out", "", "write the JSON here instead of stdout")
	return c
}

// readSbSpec builds a spec from the KiCad netlist of a schematic.
func readSbSpec(project, sch string) (*sbSpec, error) {
	var stored *sbSpec
	if project != "" {
		dir, err := filepath.Abs(project)
		if err != nil {
			return nil, err
		}
		st, sp := loadSbState(dir)
		stored = sp
		if st != nil && st.Name != "" && fileExists(filepath.Join(dir, st.Name+".kicad_sch")) {
			sch = filepath.Join(dir, st.Name+".kicad_sch")
		} else {
			pros, _ := filepath.Glob(filepath.Join(dir, "*.kicad_pro"))
			if len(pros) != 1 {
				return nil, fmt.Errorf("%s: want exactly one .kicad_pro", dir)
			}
			sch = strings.TrimSuffix(pros[0], ".kicad_pro") + ".kicad_sch"
		}
	}
	root, err := kicad.RootSheetFor(sch)
	if err != nil {
		return nil, err
	}
	nl, err := kicad.ExportSchNetlist(root)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(root)
	s := &sbSpec{Schema: sbSchema, Name: strings.TrimSuffix(filepath.Base(root), ".kicad_sch"), LibDirs: []string{dir}}
	if stored != nil {
		s.Name, s.Title, s.Pages, s.Zones, s.Rails, s.Intent = stored.Name, stored.Title, stored.Pages, stored.Zones, stored.Rails, stored.Intent
	}
	storedPart := map[string]sbPart{}
	if stored != nil {
		for _, p := range stored.Parts {
			storedPart[p.Ref] = p
		}
	}
	pages := map[string]bool{}
	for _, c := range nl.Components {
		if strings.HasPrefix(c.Ref, "#") {
			continue
		}
		p := sbPart{Ref: c.Ref, Value: c.Value, Footprint: c.Footprint, Symbol: c.Lib + ":" + c.Part}
		if v, _ := kicad.LookupLCSC(c.Fields); v != "" {
			p.LCSC = v
		}
		if v := c.Fields["MPN"]; v != "" {
			p.MPN = v
		}
		p.Zone = c.Fields["pcbpilot_zone"]
		if sp, ok := storedPart[c.Ref]; ok {
			p.Block, p.Role = sp.Block, sp.Role
			if sp.Symbol == "" && len(sp.Pins) > 0 { // generated: rebuilt from its pins
				p.Symbol, p.Pins = "", sp.Pins
			}
		}
		if stored == nil && c.Sheet != "" && c.Sheet != "/" {
			pg := sbSanitize(strings.Trim(c.Sheet, "/"))
			if pg != "" {
				p.Page = pg
				if !pages[pg] {
					pages[pg] = true
					s.Pages = append(s.Pages, sbPage{ID: pg, Title: strings.Trim(c.Sheet, "/")})
				}
			}
		}
		s.Parts = append(s.Parts, p)
	}
	if files, err := kicad.SheetFiles(root); err == nil {
		var rels []string
		for _, f := range files {
			if r, err := filepath.Rel(dir, f); err == nil {
				rels = append(rels, filepath.ToSlash(r))
			}
		}
		boxes := sbPartBoxes(dir, rels)
		zb := map[string]kicad.Box{}
		for i := range s.Parts {
			if b, ok := boxes[s.Parts[i].Ref]; ok {
				s.Parts[i].BBox = b.Box
				if z := s.Parts[i].Zone; z != "" {
					zb[z] = sbBoxUnion(zb[z], kicad.Box{MinX: b.Box[0], MinY: b.Box[1], MaxX: b.Box[2], MaxY: b.Box[3]})
				}
			}
		}
		for i := range s.Zones {
			if b, ok := zb[s.Zones[i].ID]; ok {
				s.Zones[i].BBox = []float64{sbRound2(b.MinX), sbRound2(b.MinY), sbRound2(b.MaxX), sbRound2(b.MaxY)}
			}
		}
	}
	sort.Slice(s.Parts, func(i, j int) bool { return refLessSB(s.Parts[i].Ref, s.Parts[j].Ref) })
	storedKind := map[string]sbNet{}
	if stored != nil {
		for _, n := range stored.Nets {
			storedKind[n.Name] = n
		}
	}
	for _, n := range nl.Nets {
		var pins []string
		for _, nd := range n.Nodes {
			if !strings.HasPrefix(nd.Ref, "#") {
				pins = append(pins, nd.Ref+":"+nd.Pin)
			}
		}
		if len(pins) == 0 {
			continue
		}
		if strings.HasPrefix(n.Name, "unconnected-(") {
			for _, nd := range n.Nodes {
				if strings.Contains(nd.Type, "no_connect") {
					s.NoConnect = append(s.NoConnect, nd.Ref+":"+nd.Pin)
				}
			}
			continue
		}
		name := stripSheetPath(n.Name)
		sort.Slice(pins, func(i, j int) bool { return pins[i] < pins[j] })
		sn := sbNet{Name: name, Pins: pins}
		if k, ok := storedKind[name]; ok {
			sn.Kind, sn.VoltageV, sn.CurrentA = k.Kind, k.VoltageV, k.CurrentA
		}
		s.Nets = append(s.Nets, sn)
	}
	sort.Slice(s.Nets, func(i, j int) bool { return s.Nets[i].Name < s.Nets[j].Name })
	sort.Strings(s.NoConnect)
	s.UnusedPins = "open"
	return s, nil
}
