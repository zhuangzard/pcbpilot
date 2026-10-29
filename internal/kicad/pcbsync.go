package kicad

// pcbsync.go — the plan of `kicad pcb-sync` (headless "Update PCB from
// Schematic"): a schematic netlist (SyncNetlist) against a board inventory
// (bridge sync-read) → the ordered list of edits that brings the board in
// line, or a refusal. Pure: the bridge applies the plan (sync-apply) and the
// CLI (internal/app/cmd_kicad_pcb_sync.go) runs the files, the transaction
// and the verify gates.
//
// Matching (KiCad's own key, see syncnetlist.go): a footprint belongs to a
// symbol when its (path …) equals one of the symbol's keys (sheet path
// tstamps + unit UUID; a multi-unit symbol is one footprint carrying any of
// its unit keys, a new one gets Keys()[0]). --match path-then-ref falls back
// to the reference when it is unique on both sides and relinks the
// footprint (setPath). Units are mm in KiCad board coordinates (y down).
//
// Nothing is guessed: a netlist that cannot be matched (unannotated or
// duplicate references, duplicate keys), a board with two footprints on one
// path, a symbol without a footprint, a footprint library that does not
// resolve or a library footprint that does not load refuses the whole plan.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
)

// Sync error codes (report errors[].code) beyond the netlist and footprint
// library ones (syncnetlist.go, fplib.go).
const (
	ErrDupPath         = "E_DUP_PATH"         // two board footprints carry one symbol path
	ErrFPMissingField  = "E_FP_MISSING_FIELD" // a symbol on the board has an empty Footprint field
	ErrPinPadMismatch  = "E_PIN_PAD_MISMATCH" // a netlist pin has no pad on its footprint
	SyncApplySchema    = "pcbpilot.kicad.pcb-sync.apply/1"
	SyncMatchPathOrRef = "path-then-ref"
	SyncMatchPath      = "path"
	SyncMatchRef       = "ref"
)

// Update categories (--update).
var SyncUpdateAll = []string{"values", "footprints", "fields", "refs", "dnp"}

// SyncInventory is the bridge's sync-read result.
type SyncInventory struct {
	KiCadVersion string         `json:"kicadVersion"`
	Footprints   []SyncFP       `json:"footprints"`
	Nets         []SyncBoardNet `json:"nets"`
	Outline      *SyncBox       `json:"outline"`
}

// SyncFP is one board footprint. ID is its footprint_index key (the uuid
// unless the board repeats it), the handle every op uses.
type SyncFP struct {
	ID             string            `json:"id"`
	UUID           string            `json:"uuid"`
	Ref            string            `json:"ref"`
	Path           string            `json:"path"`
	Sheetname      string            `json:"sheetname"`
	Sheetfile      string            `json:"sheetfile"`
	FPID           string            `json:"fpid"`
	Value          string            `json:"value"`
	Fields         map[string]string `json:"fields"`
	DNP            bool              `json:"dnp"`
	ExcludeFromBOM bool              `json:"excludeFromBom"`
	ExcludeFromPos bool              `json:"excludeFromPos"`
	BoardOnly      bool              `json:"boardOnly"`
	Locked         bool              `json:"locked"`
	X              float64           `json:"x"`
	Y              float64           `json:"y"`
	Rot            float64           `json:"rot"`
	Side           string            `json:"side"`
	Box            *SyncBox          `json:"box"`
	Pads           []SyncPad         `json:"pads"`
}

// SyncPad is one pad: number, net ("" = none) and type pth|smd|conn|npth.
type SyncPad struct {
	Number string `json:"number"`
	Net    string `json:"net,omitempty"`
	Type   string `json:"type"`
}

// copper is true for a pad that carries a net: numbered and not a
// non-plated hole.
func (p SyncPad) copper() bool { return p.Number != "" && p.Type != "npth" }

// SyncBoardNet is one board net and what is on it.
type SyncBoardNet struct {
	Name   string `json:"name"`
	Pads   int    `json:"pads"`
	Tracks int    `json:"tracks"`
	Vias   int    `json:"vias"`
	Zones  int    `json:"zones"`
	Shapes int    `json:"shapes,omitempty"` // board drawings on copper with the net
}

func (n SyncBoardNet) copper() bool { return n.Tracks+n.Vias+n.Zones+n.Shapes > 0 }

// SyncBox is a rectangle in mm.
type SyncBox struct {
	MinX float64 `json:"minX"`
	MinY float64 `json:"minY"`
	MaxX float64 `json:"maxX"`
	MaxY float64 `json:"maxY"`
}

// SyncFPInfo is a library footprint as the bridge loaded it (fp-info): its
// pads and its courtyard box at the origin, rotation 0.
type SyncFPInfo struct {
	Pads      []SyncPad `json:"pads"`
	Box       *SyncBox  `json:"box"`
	BoxSource string    `json:"boxSource"`
	// SHA256 of the .kicad_mod the info was loaded from (set by the
	// caller): it rides in the add / swap op (fpSha) and so in the
	// revision; the apply installs exactly those bytes.
	SHA256 string `json:"sha256,omitempty"`
}

// FPResolver resolves "Lib:Name" (FPLibTables).
type FPResolver interface {
	Resolve(id string) (FPRef, error)
}

// SyncOptions are the pcb-sync flags that shape the plan.
type SyncOptions struct {
	Match        string          // path-then-ref (default) | path | ref
	Orphans      string          // flag (default) | remove
	Update       map[string]bool // values, footprints, fields, refs, dnp (nil = all)
	FieldsIgnore map[string]bool
	RenameNets   bool
	Staging      *[2]float64 // fixed staging origin (mm); nil = beside the outline
	StagingGap   float64     // mm between staged footprints
}

func (o SyncOptions) updates(cat string) bool { return o.Update == nil || o.Update[cat] }

// SyncInput is everything BuildSyncPlan reads. Info loads library
// footprints (the bridge's fp-info); it is called once with every
// footprint the plan adds or swaps in, and returns their infos plus an
// error per footprint that did not load.
type SyncInput struct {
	Netlist   *SyncNetlist
	Inventory *SyncInventory
	Libs      FPResolver
	Info      func(refs []FPRef) (map[string]SyncFPInfo, []SyncError)
	Opts      SyncOptions
}

// SyncError is one problem: a refusal (SyncRefusal) or a finding of a plan
// that is still applied (E_PIN_PAD_MISMATCH, which the pad-net gate fails).
type SyncError struct {
	Code   string `json:"code"`
	Path   string `json:"path,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Var    string `json:"var,omitempty"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// SyncRefusal is the error of a plan that cannot be made: nothing is
// applied.
type SyncRefusal struct{ Errors []SyncError }

func (r *SyncRefusal) Error() string {
	var parts []string
	for i, e := range r.Errors {
		if i == 5 {
			parts = append(parts, fmt.Sprintf("… %d more", len(r.Errors)-5))
			break
		}
		parts = append(parts, e.Code+" "+e.Detail)
	}
	return "pcb-sync refused: " + strings.Join(parts, "; ")
}

// SyncOp is one edit. Ops with Info set change nothing (an orphan that is
// only flagged, a net left without pads) and never reach the bridge.
type SyncOp struct {
	Op             string            `json:"op"`
	FP             string            `json:"fp,omitempty"`  // board footprint id
	Ref            string            `json:"ref,omitempty"` // the reference after the sync (setRef: the new one)
	Key            string            `json:"key,omitempty"` // the symbol's path key
	From           string            `json:"from,omitempty"`
	To             string            `json:"to,omitempty"`
	FPID           string            `json:"fpid,omitempty"`
	LibDir         string            `json:"libDir,omitempty"`
	Name           string            `json:"name,omitempty"`
	FPSha          string            `json:"fpSha,omitempty"` // sha256 of the library footprint planned with
	Value          *string           `json:"value,omitempty"`
	Field          string            `json:"field,omitempty"`
	Fields         map[string]string `json:"fields,omitempty"`
	Path           string            `json:"path,omitempty"`
	Sheetname      *string           `json:"sheetname,omitempty"`
	Sheetfile      *string           `json:"sheetfile,omitempty"`
	DNP            *bool             `json:"dnp,omitempty"`
	ExcludeFromBOM *bool             `json:"excludeFromBom,omitempty"`
	X              *float64          `json:"x,omitempty"`
	Y              *float64          `json:"y,omitempty"`
	Rot            *float64          `json:"rot,omitempty"`
	Side           string            `json:"side,omitempty"`
	Pad            string            `json:"pad,omitempty"`
	Net            *string           `json:"net,omitempty"`
	PadNets        map[string]string `json:"padNets,omitempty"`
	Action         string            `json:"action,omitempty"`
	MatchedBy      string            `json:"matchedBy,omitempty"`
	Reason         string            `json:"reason,omitempty"`
	Info           bool              `json:"info,omitempty"`
}

// SyncMatch is how symbols and footprints were paired.
type SyncMatch struct {
	ByPath              int      `json:"byPath"`
	ByRef               int      `json:"byRef"`
	UnmatchedSymbols    []string `json:"unmatchedSymbols"`
	UnmatchedFootprints []string `json:"unmatchedFootprints"`
}

// SyncCounts sums the ops by kind.
type SyncCounts struct {
	Add          int `json:"add"`
	Swap         int `json:"swap"`
	Update       int `json:"update"`
	PadNet       int `json:"padNet"`
	RenameNet    int `json:"renameNet"`
	OrphanFlag   int `json:"orphanFlag"`
	OrphanRemove int `json:"orphanRemove"`
}

// SyncStaged is where a new footprint was put.
type SyncStaged struct {
	Ref string  `json:"ref"`
	X   float64 `json:"xMm"`
	Y   float64 `json:"yMm"`
}

// SyncCopper is copper the sync leaves without its pad: a pad a swap
// dropped or a removed orphan's pad (orphanCopper), or a pad moved off a
// net that has tracks / vias / zones, or a net left with copper but no pad
// (danglingCopper). Tracks and zones are never moved or deleted.
type SyncCopper struct {
	Ref    string `json:"ref,omitempty"`
	Pad    string `json:"pad,omitempty"`
	Net    string `json:"net"`
	To     string `json:"to,omitempty"`
	Tracks int    `json:"tracks,omitempty"`
	Vias   int    `json:"vias,omitempty"`
	Zones  int    `json:"zones,omitempty"`
	Reason string `json:"reason"`
}

// SyncPlan is the result of BuildSyncPlan.
type SyncPlan struct {
	Ops            []SyncOp     `json:"ops"`
	Match          SyncMatch    `json:"match"`
	Counts         SyncCounts   `json:"counts"`
	Staged         []SyncStaged `json:"staged"`
	OrphanCopper   []SyncCopper `json:"orphanCopper"`
	DanglingCopper []SyncCopper `json:"danglingCopper"`
	Errors         []SyncError  `json:"errors"`
}

// Mutating are the ops that change the board (what sync-apply receives).
func (p *SyncPlan) Mutating() []SyncOp {
	var out []SyncOp
	for _, o := range p.Ops {
		if !o.Info {
			out = append(out, o)
		}
	}
	return out
}

// Drift reports a board out of line with the schematic: an op that changes
// it, or an orphan left on it.
func (p *SyncPlan) Drift() bool { return len(p.Mutating()) > 0 || p.Counts.OrphanFlag > 0 }

// ApplyDoc is the sync-apply request.
func (p *SyncPlan) ApplyDoc() map[string]any {
	ops := p.Mutating()
	if ops == nil {
		ops = []SyncOp{}
	}
	return map[string]any{"schema": SyncApplySchema, "ops": ops}
}

// SyncRevision identifies a plan made from given files: sha256 over the
// board and project base hashes ("" = absent), the options, the ops (which
// carry each library footprint's hash) and extra — the state of every
// other file the result depends on (the effective project and rules, …).
// The same edit of the same files always gets the same revision.
func SyncRevision(boardSHA, proSHA string, o SyncOptions, p *SyncPlan, extra ...string) string {
	h := sha256.New()
	ops, _ := json.Marshal(p.Ops)
	upd := []string{}
	for _, c := range SyncUpdateAll {
		if o.updates(c) {
			upd = append(upd, c)
		}
	}
	ign := sortedKeys(o.FieldsIgnore)
	opt, _ := json.Marshal([]any{o.Match, o.Orphans, upd, ign, o.RenameNets, o.Staging, o.StagingGap})
	fmt.Fprintf(h, "pcb-sync/1\x00%s\x00%s\x00%s\x00%s", boardSHA, proSHA, opt, ops)
	for _, e := range extra {
		fmt.Fprintf(h, "\x00%s", e)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func strp(s string) *string     { return &s }
func boolp(b bool) *bool        { return &b }
func floatp(f float64) *float64 { return &f }

// syncOpOrder is the stable op order: net renames first (the pad nets
// below are written against the new names), new and swapped footprints,
// footprint edits, pad nets, orphans, then the report-only ops.
var syncOpOrder = map[string]int{"renameNet": 0, "addFootprint": 1, "swapFootprint": 2, "setPath": 3, "setRef": 4,
	"setValue": 5, "setField": 6, "removeField": 7, "setSheet": 8, "setFlags": 9, "setPadNet": 10, "orphan": 11, "removeNet": 12}

// pairing is one symbol matched to one board footprint.
type pairing struct {
	comp *SyncComp
	fp   *SyncFP
	by   string // "path" | "ref"
}

// BuildSyncPlan computes the plan, or a *SyncRefusal.
func BuildSyncPlan(in SyncInput) (*SyncPlan, error) {
	nl, inv, opts := in.Netlist, in.Inventory, in.Opts
	if opts.Match == "" {
		opts.Match = SyncMatchPathOrRef
	}
	if opts.Orphans == "" {
		opts.Orphans = "flag"
	}
	var refuse []SyncError
	for _, is := range nl.Validate() {
		e := SyncError{Code: is.Code, Ref: is.Ref, Detail: is.Detail}
		switch is.Code {
		case ErrUnannotated:
			e.Fix = "annotate the schematic (KiCad Tools > Annotate, or pcbpilot kicad sch-edit) and sync again"
		case ErrDupRef:
			e.Fix = "give every symbol its own reference"
		default:
			e.Fix = "re-export the netlist with kicad-cli sch export netlist"
		}
		refuse = append(refuse, e)
	}
	// Symbols on the board: exclude_from_board ones are absent (their
	// footprints become orphans), power symbols (#…) have no footprint.
	var comps []*SyncComp
	for i := range nl.Components {
		c := &nl.Components[i]
		if c.ExcludeFromBoard || strings.HasPrefix(c.Ref, "#") {
			continue
		}
		if strings.TrimSpace(c.Footprint) == "" {
			refuse = append(refuse, SyncError{Code: ErrFPMissingField, Ref: c.Ref,
				Detail: fmt.Sprintf("%s (%s) has no footprint", c.Ref, c.LibID()),
				Fix:    "set the symbol's Footprint field (Library:Footprint), or mark it exclude from board"})
		}
		comps = append(comps, c)
	}
	fps := make([]*SyncFP, 0, len(inv.Footprints))
	byPath := map[string]*SyncFP{}
	for i := range inv.Footprints {
		f := &inv.Footprints[i]
		if f.BoardOnly {
			continue
		}
		fps = append(fps, f)
		if f.Path == "" {
			continue
		}
		if prev := byPath[f.Path]; prev != nil {
			refuse = append(refuse, SyncError{Code: ErrDupPath, Ref: f.Ref, Path: f.Path,
				Detail: fmt.Sprintf("footprints %s and %s both carry symbol path %s", prev.Ref, f.Ref, f.Path),
				Fix:    "delete the duplicate footprint (or clear its path) so each symbol has one footprint"})
			continue
		}
		byPath[f.Path] = f
	}
	// A symbol is one footprint: two footprints on keys of one symbol
	// (different units of a multi-unit symbol) are a duplicate, never a
	// pick of one and an orphan of the other.
	for _, c := range comps {
		var hits []*SyncFP
		for _, k := range c.Keys() {
			if f := byPath[k]; f != nil && !slices.Contains(hits, f) {
				hits = append(hits, f)
			}
		}
		if len(hits) > 1 {
			var refs []string
			for _, f := range hits {
				refs = append(refs, f.Ref+" ("+f.Path+")")
			}
			sort.Strings(refs)
			refuse = append(refuse, SyncError{Code: ErrDupPath, Ref: c.Ref,
				Detail: fmt.Sprintf("%s is on %d footprints, one per unit key: %s", c.Ref, len(hits), strings.Join(refs, ", ")),
				Fix:    "a multi-unit symbol has one footprint: delete the extra footprint(s) so one remains"})
		}
	}
	if len(refuse) > 0 {
		return nil, &SyncRefusal{Errors: refuse}
	}

	// Match.
	plan := &SyncPlan{Ops: []SyncOp{}, Staged: []SyncStaged{}, OrphanCopper: []SyncCopper{}, DanglingCopper: []SyncCopper{},
		Errors: []SyncError{}, Match: SyncMatch{UnmatchedSymbols: []string{}, UnmatchedFootprints: []string{}}}
	used := map[*SyncFP]bool{}
	var pairs []pairing
	matched := map[*SyncComp]bool{}
	if opts.Match != SyncMatchRef {
		for _, c := range comps {
			for _, k := range c.Keys() {
				if f := byPath[k]; f != nil && !used[f] {
					used[f], matched[c] = true, true
					pairs = append(pairs, pairing{c, f, "path"})
					break
				}
			}
		}
	}
	if opts.Match != SyncMatchPath {
		// A reference is a key only when it is unique on the whole board
		// (board-only footprints aside), not just among the footprints
		// left over after path matching: a duplicated "R1" stays ambiguous
		// even when one copy is linked by path.
		refCount := map[string]int{}
		for _, f := range fps {
			refCount[f.Ref]++
		}
		free := map[string][]*SyncFP{}
		for _, f := range fps {
			if !used[f] {
				free[f.Ref] = append(free[f.Ref], f)
			}
		}
		for _, c := range comps {
			if matched[c] || len(free[c.Ref]) != 1 || refCount[c.Ref] != 1 {
				continue
			}
			f := free[c.Ref][0]
			used[f], matched[c] = true, true
			pairs = append(pairs, pairing{c, f, "ref"})
		}
	}

	// Footprints to load: every new symbol's, every swap's.
	type need struct {
		comp *SyncComp
		fp   *SyncFP // nil: add
	}
	var needs []need
	for _, c := range comps {
		if !matched[c] {
			needs = append(needs, need{comp: c})
		}
	}
	for _, p := range pairs {
		if opts.updates("footprints") && p.fp.FPID != p.comp.Footprint {
			needs = append(needs, need{comp: p.comp, fp: p.fp})
		}
	}
	refs := map[string]FPRef{}
	var load []FPRef
	for _, n := range needs {
		id := n.comp.Footprint
		if _, ok := refs[id]; ok {
			continue
		}
		r, err := in.Libs.Resolve(id)
		if err != nil {
			e := SyncError{Code: ErrFPLibUnresolved, Ref: n.comp.Ref, Detail: err.Error()}
			var le *FPLibError
			if errors.As(err, &le) {
				e = SyncError{Code: le.Code, Ref: n.comp.Ref, Path: le.Path, Var: le.Var, Detail: le.Detail, Fix: le.Fix}
			}
			refuse = append(refuse, e)
			refs[id] = FPRef{}
			continue
		}
		refs[id] = r
		load = append(load, r)
	}
	if len(refuse) > 0 {
		return nil, &SyncRefusal{Errors: refuse}
	}
	infos := map[string]SyncFPInfo{}
	if len(load) > 0 {
		var bad []SyncError
		infos, bad = in.Info(load)
		refuse = append(refuse, bad...)
		for _, r := range load {
			if _, ok := infos[r.Lib+":"+r.Name]; !ok && len(bad) == 0 {
				refuse = append(refuse, SyncError{Code: ErrFPNotFound, Path: r.Path,
					Detail: fmt.Sprintf("footprint %s:%s did not load", r.Lib, r.Name), Fix: "check the footprint file"})
			}
		}
		if len(refuse) > 0 {
			return nil, &SyncRefusal{Errors: refuse}
		}
	}

	// Pin → net of the schematic (identities; the CLI resolved them).
	pinNet := map[[2]string]string{}
	pinsOf := map[string][]string{}
	for _, n := range nl.Nets {
		for _, d := range n.Nodes {
			k := [2]string{d.Ref, d.Pin}
			if _, dup := pinNet[k]; !dup {
				pinsOf[d.Ref] = append(pinsOf[d.Ref], d.Pin)
			}
			pinNet[k] = n.Name
		}
	}

	// Future pads: (fp id or "+ref" for a new footprint) → pad number → net.
	type futurePad struct {
		ref, num, net, cur string // cur: the board net now ("" for new pads)
		fp                 *SyncFP
		kept               bool // an existing pad that stays
	}
	var future []futurePad
	boardNet := map[string]SyncBoardNet{}
	for _, n := range inv.Nets {
		boardNet[n.Name] = n
	}
	desired := func(ref, num string) string { return pinNet[[2]string{ref, num}] }
	swapped := map[*SyncFP]SyncFPInfo{}
	for _, p := range pairs {
		if info, ok := infos[p.comp.Footprint]; ok && opts.updates("footprints") && p.fp.FPID != p.comp.Footprint {
			swapped[p.fp] = info
		}
	}
	checkPins := func(c *SyncComp, pads []SyncPad, what string) {
		have := map[string]bool{}
		for _, pd := range pads {
			if pd.copper() {
				have[pd.Number] = true
			}
		}
		var miss []string
		for _, pin := range pinsOf[c.Ref] {
			if !have[pin] {
				miss = append(miss, pin)
			}
		}
		if len(miss) > 0 {
			sort.Slice(miss, func(i, j int) bool { return refLess(miss[i], miss[j]) })
			plan.Errors = append(plan.Errors, SyncError{Code: ErrPinPadMismatch, Ref: c.Ref,
				Detail: fmt.Sprintf("%s pin(s) %s have no pad on %s %s", c.Ref, strings.Join(miss, ", "), what, c.Footprint),
				Fix:    "pick a footprint whose pad numbers match the symbol's pin numbers"})
		}
	}
	for _, p := range pairs {
		if info, ok := swapped[p.fp]; ok {
			checkPins(p.comp, info.Pads, "the new footprint")
			for _, pd := range info.Pads {
				if pd.copper() {
					future = append(future, futurePad{ref: p.comp.Ref, num: pd.Number, net: desired(p.comp.Ref, pd.Number), fp: p.fp})
				}
			}
			continue
		}
		checkPins(p.comp, p.fp.Pads, "footprint")
		for _, pd := range p.fp.Pads {
			if pd.copper() {
				future = append(future, futurePad{ref: p.comp.Ref, num: pd.Number, net: desired(p.comp.Ref, pd.Number), cur: pd.Net, fp: p.fp, kept: true})
			}
		}
	}

	// Net renames: a board net whose pads all go to one schematic net that
	// has exactly those pads, under a name the board does not use yet. Any
	// doubt (pads of an orphan or board-only footprint on it, a split, two
	// nets claiming one name) means no rename: the pads are re-netted one by
	// one and the copper is reported.
	renames := map[string]string{}
	if opts.RenameNets {
		curPads := map[string]map[[2]string]bool{} // board net → (ref,num) of pads on it
		strays := map[string]bool{}                // nets with a pad outside the matched footprints
		// A stray is a connected pad no schematic pin speaks for: any pad
		// of an unmatched or board-only footprint, and an unnumbered pad
		// or a hole on a matched one. A rename would move it to the new
		// net unauthorised, so its net is not renamed.
		for i := range inv.Footprints {
			f := &inv.Footprints[i]
			for _, pd := range f.Pads {
				if pd.Net != "" && (!used[f] || !pd.copper()) {
					strays[pd.Net] = true
				}
			}
		}
		refOf := map[*SyncFP]string{}
		for _, p := range pairs {
			refOf[p.fp] = p.comp.Ref
		}
		for _, p := range pairs {
			for _, pd := range p.fp.Pads {
				if pd.Net != "" && pd.copper() {
					m := curPads[pd.Net]
					if m == nil {
						m = map[[2]string]bool{}
						curPads[pd.Net] = m
					}
					m[[2]string{refOf[p.fp], pd.Number}] = true
				}
			}
		}
		wantPads := map[string]map[[2]string]bool{}
		for _, fp := range future {
			if fp.net == "" {
				continue
			}
			m := wantPads[fp.net]
			if m == nil {
				m = map[[2]string]bool{}
				wantPads[fp.net] = m
			}
			m[[2]string{fp.ref, fp.num}] = true
		}
		claims := map[string][]string{}
		for _, n := range sortedKeys(curPads) {
			if strays[n] {
				continue
			}
			target := ""
			ok := true
			for k := range curPads[n] {
				s := desired(k[0], k[1])
				if s == "" || (target != "" && s != target) {
					ok = false
					break
				}
				target = s
			}
			if !ok || target == n || target == "" {
				continue
			}
			if _, exists := boardNet[target]; exists {
				continue
			}
			if !samePadSet(curPads[n], wantPads[target]) {
				continue
			}
			claims[target] = append(claims[target], n)
		}
		for target, from := range claims {
			if len(from) == 1 {
				renames[from[0]] = target
			}
		}
		for _, from := range sortedKeys(renames) {
			bn := boardNet[from]
			plan.Ops = append(plan.Ops, SyncOp{Op: "renameNet", From: from, To: renames[from],
				Reason: fmt.Sprintf("same %d pad(s); tracks %d, vias %d, zones %d move with it", bn.Pads, bn.Tracks, bn.Vias, bn.Zones)})
		}
	}
	afterRename := func(n string) string {
		if t, ok := renames[n]; ok {
			return t
		}
		return n
	}

	// Per matched footprint.
	for _, p := range pairs {
		c, f := p.comp, p.fp
		if p.by == "ref" {
			plan.Match.ByRef++
		} else {
			plan.Match.ByPath++
		}
		base := SyncOp{FP: f.ID, Ref: c.Ref, Key: c.Keys()[0], MatchedBy: p.by}
		with := func(o SyncOp) SyncOp {
			o.FP, o.Key, o.MatchedBy = base.FP, base.Key, base.MatchedBy
			if o.Ref == "" {
				o.Ref = base.Ref
			}
			return o
		}
		if info, ok := swapped[f]; ok {
			r := refs[c.Footprint]
			pn := map[string]string{}
			newNums := map[string]bool{}
			for _, pd := range info.Pads {
				if pd.copper() {
					newNums[pd.Number] = true
					if n := desired(c.Ref, pd.Number); n != "" {
						pn[pd.Number] = n
					}
				}
			}
			plan.Ops = append(plan.Ops, with(SyncOp{Op: "swapFootprint", From: f.FPID, To: c.Footprint, FPID: c.Footprint,
				LibDir: r.LibDir, Name: r.Name, FPSha: info.SHA256, PadNets: pn, Reason: "the symbol's Footprint field names another footprint"}))
			for _, pd := range f.Pads {
				if !pd.copper() || pd.Net == "" || !boardNet[pd.Net].copper() {
					continue
				}
				if !newNums[pd.Number] {
					plan.OrphanCopper = append(plan.OrphanCopper, SyncCopper{Ref: c.Ref, Pad: pd.Number, Net: pd.Net,
						Reason: "the new footprint has no pad " + pd.Number + "; copper on its net stays"})
					continue
				}
				// The pad stays (same number) but its net changes: the copper
				// on the old net no longer reaches it.
				if to := pn[pd.Number]; afterRename(pd.Net) != to {
					bn := boardNet[pd.Net]
					plan.DanglingCopper = append(plan.DanglingCopper, SyncCopper{Ref: c.Ref, Pad: pd.Number, Net: pd.Net, To: to,
						Tracks: bn.Tracks, Vias: bn.Vias, Zones: bn.Zones, Reason: "the swapped pad leaves a net that has copper; that copper stays on " + pd.Net})
				}
			}
		}
		if opts.updates("refs") {
			keyed := false
			for _, k := range c.Keys() {
				keyed = keyed || k == f.Path
			}
			if !keyed {
				plan.Ops = append(plan.Ops, with(SyncOp{Op: "setPath", Path: c.Keys()[0], From: f.Path,
					Reason: "matched by reference; the footprint is relinked to its symbol"}))
			}
			if f.Ref != c.Ref {
				plan.Ops = append(plan.Ops, with(SyncOp{Op: "setRef", Ref: c.Ref, From: f.Ref, Reason: "the symbol was re-annotated"}))
			}
		}
		if opts.updates("values") && f.Value != c.Value {
			plan.Ops = append(plan.Ops, with(SyncOp{Op: "setValue", Value: strp(c.Value), From: f.Value}))
		}
		if opts.updates("fields") {
			want := syncWantFields(c, opts.FieldsIgnore)
			for _, name := range sortedKeys(want) {
				if cur, ok := f.Fields[name]; !ok || cur != want[name] {
					plan.Ops = append(plan.Ops, with(SyncOp{Op: "setField", Field: name, Value: strp(want[name]), From: cur}))
				}
			}
			for _, name := range sortedKeys(f.Fields) {
				if _, ok := want[name]; ok || opts.FieldsIgnore[name] || mandatoryFPField(name) {
					continue
				}
				plan.Ops = append(plan.Ops, with(SyncOp{Op: "removeField", Field: name, From: f.Fields[name], Reason: "the symbol has no such field"}))
			}
			if f.Sheetname != c.SheetNames || f.Sheetfile != c.Sheetfile() {
				plan.Ops = append(plan.Ops, with(SyncOp{Op: "setSheet", Sheetname: strp(c.SheetNames), Sheetfile: strp(c.Sheetfile()),
					From: f.Sheetname + " " + f.Sheetfile}))
			}
		}
		if opts.updates("dnp") && (f.DNP != c.DNP || f.ExcludeFromBOM != c.ExcludeFromBOM) {
			plan.Ops = append(plan.Ops, with(SyncOp{Op: "setFlags", DNP: boolp(c.DNP), ExcludeFromBOM: boolp(c.ExcludeFromBOM),
				From: fmt.Sprintf("dnp=%t excludeFromBom=%t", f.DNP, f.ExcludeFromBOM)}))
		}
	}
	// Pad nets of the footprints that stay.
	for _, fpd := range future {
		if !fpd.kept {
			continue
		}
		cur := afterRename(fpd.cur)
		if cur == fpd.net {
			continue
		}
		plan.Ops = append(plan.Ops, SyncOp{Op: "setPadNet", FP: fpd.fp.ID, Ref: fpd.ref, Pad: fpd.num, Net: strp(fpd.net), From: fpd.cur})
		if fpd.cur != "" && boardNet[fpd.cur].copper() {
			bn := boardNet[fpd.cur]
			plan.DanglingCopper = append(plan.DanglingCopper, SyncCopper{Ref: fpd.ref, Pad: fpd.num, Net: fpd.cur, To: fpd.net,
				Tracks: bn.Tracks, Vias: bn.Vias, Zones: bn.Zones, Reason: "the pad leaves a net that has copper; that copper stays on " + fpd.cur})
		}
	}

	// New footprints.
	var adds []*SyncComp
	for _, c := range comps {
		if !matched[c] {
			adds = append(adds, c)
		}
	}
	sort.SliceStable(adds, func(i, j int) bool {
		if adds[i].SheetNames != adds[j].SheetNames {
			return adds[i].SheetNames < adds[j].SheetNames
		}
		return refLess(adds[i].Ref, adds[j].Ref)
	})
	var boxes []*SyncBox
	for _, c := range adds {
		info := infos[c.Footprint]
		boxes = append(boxes, info.Box)
	}
	at := stagePositions(boxes, inv, opts)
	for i, c := range adds {
		r, info := refs[c.Footprint], infos[c.Footprint]
		checkPins(c, info.Pads, "footprint")
		pn := map[string]string{}
		for _, pd := range info.Pads {
			if pd.copper() {
				if n := desired(c.Ref, pd.Number); n != "" {
					pn[pd.Number] = n
				}
				future = append(future, futurePad{ref: c.Ref, num: pd.Number, net: desired(c.Ref, pd.Number)})
			}
		}
		plan.Ops = append(plan.Ops, SyncOp{Op: "addFootprint", Ref: c.Ref, Key: c.Keys()[0], FPID: c.Footprint, LibDir: r.LibDir, Name: r.Name, FPSha: info.SHA256,
			Value: strp(c.Value), Fields: syncWantFields(c, opts.FieldsIgnore), Sheetname: strp(c.SheetNames), Sheetfile: strp(c.Sheetfile()),
			DNP: boolp(c.DNP), ExcludeFromBOM: boolp(c.ExcludeFromBOM), X: floatp(at[i][0]), Y: floatp(at[i][1]), Rot: floatp(0), Side: "F",
			PadNets: pn, Reason: "new symbol"})
		plan.Staged = append(plan.Staged, SyncStaged{Ref: c.Ref, X: at[i][0], Y: at[i][1]})
		plan.Match.UnmatchedSymbols = append(plan.Match.UnmatchedSymbols, c.Ref)
	}

	// Orphans: board footprints no symbol claims (board-only ones never).
	for _, f := range fps {
		if used[f] {
			continue
		}
		plan.Match.UnmatchedFootprints = append(plan.Match.UnmatchedFootprints, f.Ref)
		o := SyncOp{Op: "orphan", FP: f.ID, Ref: f.Ref, Action: opts.Orphans, Reason: "no symbol on the board has this footprint"}
		if c := nl.Component(f.Ref); c != nil && c.ExcludeFromBoard {
			o.Reason = "its symbol is excluded from the board"
		}
		if opts.Orphans == "remove" && f.Locked {
			o.Action, o.Reason = "flag", o.Reason+"; locked, so kept"
		}
		o.Info = o.Action != "remove"
		plan.Ops = append(plan.Ops, o)
		if o.Action == "remove" {
			for _, pd := range f.Pads {
				if pd.Net != "" && boardNet[pd.Net].copper() {
					plan.OrphanCopper = append(plan.OrphanCopper, SyncCopper{Ref: f.Ref, Pad: pd.Number, Net: pd.Net,
						Reason: "the footprint is removed; copper on its net stays"})
				}
			}
		}
	}

	// Nets left without a pad (report only: copper is never deleted).
	left := map[string]int{}
	for _, fpd := range future {
		if fpd.net != "" {
			left[fpd.net]++
		}
	}
	for i := range inv.Footprints {
		f := &inv.Footprints[i]
		if used[f] {
			continue
		}
		removed := false
		for _, o := range plan.Ops {
			removed = removed || (o.Op == "orphan" && o.FP == f.ID && o.Action == "remove")
		}
		if removed {
			continue
		}
		for _, pd := range f.Pads {
			if pd.Net != "" {
				left[pd.Net]++
			}
		}
	}
	for _, n := range inv.Nets {
		if n.Pads == 0 || renames[n.Name] != "" || left[n.Name] > 0 {
			continue
		}
		plan.Ops = append(plan.Ops, SyncOp{Op: "removeNet", Net: strp(n.Name), Info: true,
			Reason: fmt.Sprintf("no pad is left on the net (tracks %d, vias %d, zones %d stay)", n.Tracks, n.Vias, n.Zones)})
		if n.copper() {
			plan.DanglingCopper = append(plan.DanglingCopper, SyncCopper{Net: n.Name, Tracks: n.Tracks, Vias: n.Vias, Zones: n.Zones,
				Reason: "no pad is left on the net; its copper stays"})
		}
	}

	sortSyncOps(plan.Ops)
	for _, o := range plan.Ops {
		switch o.Op {
		case "addFootprint":
			plan.Counts.Add++
		case "swapFootprint":
			plan.Counts.Swap++
		case "setPadNet":
			plan.Counts.PadNet++
		case "renameNet":
			plan.Counts.RenameNet++
		case "orphan":
			if o.Action == "remove" {
				plan.Counts.OrphanRemove++
			} else {
				plan.Counts.OrphanFlag++
			}
		case "removeNet":
		default:
			plan.Counts.Update++
		}
	}
	sort.Strings(plan.Match.UnmatchedFootprints)
	return plan, nil
}

func samePadSet(a, b map[[2]string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// mandatoryFPField: fields every KiCad 10 footprint has; never removed.
func mandatoryFPField(name string) bool {
	switch name {
	case "Reference", "Value", "Datasheet", "Description", "Footprint":
		return true
	}
	return false
}

// syncWantFields are the footprint fields a symbol gives: its fields but
// Reference, Value and Footprint (the footprint itself), minus ignored ones.
func syncWantFields(c *SyncComp, ignore map[string]bool) map[string]string {
	out := map[string]string{}
	for k, v := range c.Fields {
		if k == "Reference" || k == "Value" || k == "Footprint" || ignore[k] {
			continue
		}
		out[k] = v
	}
	return out
}

func sortSyncOps(ops []SyncOp) {
	sort.SliceStable(ops, func(i, j int) bool {
		a, b := ops[i], ops[j]
		if oa, ob := syncOpOrder[a.Op], syncOpOrder[b.Op]; oa != ob {
			return oa < ob
		}
		if a.Ref != b.Ref {
			return refLess(a.Ref, b.Ref)
		}
		if a.Pad != b.Pad {
			return refLess(a.Pad, b.Pad)
		}
		if a.Field != b.Field {
			return a.Field < b.Field
		}
		if a.From != b.From {
			return a.From < b.From
		}
		return a.FP < b.FP
	})
}

// stagePositions shelf-packs new footprints (boxes at rotation 0, in the
// given order) in rows starting 10 mm right of the board outline (right of
// any footprint already parked there, too), or at --staging X,Y. A board
// with neither outline nor footprints (a new one, pcb-sync --create) gets
// its batch left of the origin, ending 10 mm before x = 0, so an outline
// drawn later from 0,0 (kicad board outline --origin 0,0) is clear of it.
// Rows are as wide as the outline, at least 100 mm, at least the widest
// footprint and at least the square root of the batch's area (a large batch
// is a compact block, not one long column). Deterministic: same input,
// same places.
func stagePositions(boxes []*SyncBox, inv *SyncInventory, o SyncOptions) [][2]float64 {
	gap := o.StagingGap
	if gap <= 0 {
		gap = 2
	}
	// A large batch is packed about square: rows at least as wide as the
	// root of the area it needs, not one 100 mm column (v0.12.0: a first
	// sync of a large board without an outline was a strip ~0.8 m tall).
	area, widest := 0.0, 0.0
	for _, b := range boxes {
		w, h := 1.0, 1.0
		if b != nil {
			w, h = b.MaxX-b.MinX, b.MaxY-b.MinY
		}
		area += (w + gap) * (h + gap)
		widest = math.Max(widest, w)
	}
	x0, y0 := 0.0, 0.0
	width := math.Max(100, math.Max(widest, math.Ceil(math.Sqrt(area))))
	switch {
	case o.Staging != nil:
		x0, y0 = o.Staging[0], o.Staging[1]
	case inv.Outline != nil:
		x0, y0 = inv.Outline.MaxX+10, inv.Outline.MinY
		width = math.Max(width, inv.Outline.MaxX-inv.Outline.MinX)
		for _, f := range inv.Footprints {
			if f.Box != nil && f.Box.MinX >= inv.Outline.MaxX {
				x0 = math.Max(x0, f.Box.MaxX+10)
			}
		}
	default:
		have := false
		for _, f := range inv.Footprints {
			if f.Box != nil {
				if !have || f.Box.MaxX+10 > x0 {
					x0 = f.Box.MaxX + 10
				}
				if !have || f.Box.MinY < y0 {
					y0 = f.Box.MinY
				}
				have = true
			}
		}
		if !have { // a new board: left of where its outline will start
			x0 = -(width + 10)
		}
	}
	out := make([][2]float64, len(boxes))
	cx, cy, rowH := x0, y0, 0.0
	for i, b := range boxes {
		bb := SyncBox{MinX: -0.5, MinY: -0.5, MaxX: 0.5, MaxY: 0.5}
		if b != nil {
			bb = *b
		}
		w, h := bb.MaxX-bb.MinX, bb.MaxY-bb.MinY
		if cx > x0 && cx+w > x0+width {
			cx, cy, rowH = x0, cy+rowH+gap, 0
		}
		out[i] = [2]float64{syncRound(cx - bb.MinX), syncRound(cy - bb.MinY)}
		cx += w + gap
		rowH = math.Max(rowH, h)
	}
	return out
}

func syncRound(v float64) float64 { return math.Round(v*1e4) / 1e4 }
