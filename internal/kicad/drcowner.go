package kicad

// drcowner.go — who owns a DRC violation. kicad-cli names each object of a
// violation by its uuid; the board file says which top-level object
// (footprint, track, via, zone, Edge.Cuts graphic, …) every uuid belongs to,
// so a pad, a footprint's silk line or a courtyard all resolve to their
// footprint. Read from the .kicad_pcb S-expression, no pcbnew needed.

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// BoardItem is the top-level board object a uuid belongs to.
type BoardItem struct {
	// Kind: footprint | track | via | zone | outline (Edge.Cuts graphic) |
	// graphic | text | dimension | other.
	Kind  string `json:"kind"`
	UUID  string `json:"uuid,omitempty"` // the top-level object's own uuid
	Ref   string `json:"ref,omitempty"`  // footprint designator
	Net   string `json:"net,omitempty"`
	Layer string `json:"layer,omitempty"`
}

// BoardIndex maps every uuid of a board file to its top-level object.
type BoardIndex struct {
	byUUID map[string]BoardItem
	refs   map[string]int // designator → number of footprints carrying it
}

// ReadBoardIndex indexes a .kicad_pcb file.
func ReadBoardIndex(path string) (*BoardIndex, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ix, err := ParseBoardIndex(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ix, nil
}

// ParseBoardIndex indexes .kicad_pcb text.
func ParseBoardIndex(b []byte) (*BoardIndex, error) {
	root, err := parseSexpr(b)
	if err != nil {
		return nil, err
	}
	if root.head() != "kicad_pcb" {
		return nil, fmt.Errorf("not a kicad_pcb file (root %q)", root.head())
	}
	nets := map[string]string{}
	for _, n := range root.children("net") {
		nets[n.arg(0)] = n.arg(1)
	}
	netOf := func(n *sx) string {
		if c := n.child("net_name"); c != nil {
			return c.arg(0)
		}
		c := n.child("net")
		if c == nil {
			return ""
		}
		// (net 3) refers to the net table; KiCad files without one name it.
		if len(c.Kids) > 1 && !c.Kids[1].Quoted {
			if name, ok := nets[c.arg(0)]; ok {
				return name
			}
		}
		return c.arg(0)
	}
	ix := &BoardIndex{byUUID: map[string]BoardItem{}, refs: map[string]int{}}
	for _, k := range root.Kids[1:] {
		if !k.isList {
			continue
		}
		it := BoardItem{}
		if l := k.child("layer"); l != nil {
			it.Layer = l.arg(0)
		}
		switch h := k.head(); h {
		case "footprint", "module":
			it.Kind = "footprint"
			for _, p := range k.children("property") {
				if p.arg(0) == "Reference" {
					it.Ref = p.arg(1)
				}
			}
			for _, t := range k.children("fp_text") {
				if t.arg(0) == "reference" && it.Ref == "" {
					it.Ref = t.arg(1)
				}
			}
			ix.refs[it.Ref]++
		case "segment", "arc":
			it.Kind, it.Net = "track", netOf(k)
		case "via":
			it.Kind, it.Net = "via", netOf(k)
		case "zone":
			it.Kind, it.Net = "zone", netOf(k)
			if it.Layer == "" {
				if ls := k.child("layers"); ls != nil && len(ls.Kids) > 1 {
					it.Layer = ls.arg(0)
				}
			}
		case "gr_line", "gr_arc", "gr_circle", "gr_rect", "gr_poly", "gr_curve", "gr_bbox", "bezier":
			it.Kind = "graphic"
			if it.Layer == "Edge.Cuts" {
				it.Kind = "outline"
			}
		case "gr_text", "gr_text_box", "text":
			it.Kind = "text"
		case "dimension":
			it.Kind = "dimension"
		case "net", "layers", "setup", "general", "paper", "title_block", "version", "generator", "generator_version", "property", "embedded_fonts", "embedded_files":
			continue
		default:
			it.Kind = "other"
		}
		it.UUID = ownUUID(k)
		walkUUIDs(k, func(u string) { ix.byUUID[u] = it })
	}
	return ix, nil
}

// ownUUID is a list's own (uuid …) / KiCad 6 (tstamp …).
func ownUUID(n *sx) string {
	if c := n.child("uuid"); c != nil {
		return c.arg(0)
	}
	if c := n.child("tstamp"); c != nil {
		return c.arg(0)
	}
	return ""
}

func walkUUIDs(n *sx, f func(string)) {
	for _, k := range n.Kids {
		if !k.isList {
			continue
		}
		if h := k.head(); (h == "uuid" || h == "tstamp") && k.arg(0) != "" {
			f(k.arg(0))
			continue
		}
		walkUUIDs(k, f)
	}
}

// Lookup is the top-level object owning uuid.
func (ix *BoardIndex) Lookup(uuid string) (BoardItem, bool) {
	if ix == nil {
		return BoardItem{}, false
	}
	it, ok := ix.byUUID[uuid]
	return it, ok
}

// Len is the number of indexed uuids.
func (ix *BoardIndex) Len() int {
	if ix == nil {
		return 0
	}
	return len(ix.byUUID)
}

// uniqueRef: a designator naming exactly one footprint and not a
// placeholder (REF**, R?), so it identifies that footprint.
func (ix *BoardIndex) uniqueRef(ref string) bool {
	return ref != "" && !strings.ContainsAny(ref, "*?") && ix.refs[ref] == 1
}

// DRCItemOwner is one violation object and the board object it belongs to.
type DRCItemOwner struct {
	Description string `json:"description"`
	UUID        string `json:"uuid,omitempty"`
	// ID is the owner's identity in DRCOwner.Key.
	ID string `json:"id"`
	// Owner is the board object (Kind "unresolved": the uuid is not in the
	// board file, e.g. a schematic symbol of a parity item).
	Owner BoardItem `json:"owner"`
}

// DRCOwner is who a violation belongs to.
type DRCOwner struct {
	// Class, first match: environment (library / configuration rules),
	// schematic-parity (the parity section), board-outline (an Edge.Cuts
	// object is involved), routing (a track, via or zone), footprint (one
	// footprint's own objects), placement (two or more footprints),
	// graphic (board graphics / text only), unresolved.
	Class string         `json:"class"`
	Refs  []string       `json:"refs,omitempty"`
	Nets  []string       `json:"nets,omitempty"`
	Items []DRCItemOwner `json:"items,omitempty"`
	// Key identifies the owners for baseline matching (with the rule and
	// position): class and the sorted owner identities.
	Key string `json:"key"`
}

// drcEnvironmentRules depend on the KiCad configuration (library tables),
// not on the design.
var drcEnvironmentRules = map[string]bool{"lib_footprint_issues": true, "lib_footprint_mismatch": true, "footprint_link_issues": true}

var reItemRef = regexp.MustCompile(`^(?:Footprint|Symbol) (\S+)`)

// reToken splits a parity summary into words; reMeasure is a word that
// is a measurement or plain number (0.1000, 2, 12.5mm, 90deg), the only
// thing normalised: identifiers with digits (MPN1, R12, 10k) stay.
var (
	reToken   = regexp.MustCompile(`[A-Za-z0-9_.+-]+`)
	reMeasure = regexp.MustCompile(`^[-+]?[0-9]+(?:\.[0-9]+)?(?:mm|mil|mils|in|um|deg|%)?$`)
)

// normaliseMeasures replaces the measurement words of s with "#".
func normaliseMeasures(s string) string {
	return reToken.ReplaceAllStringFunc(s, func(w string) string {
		if reMeasure.MatchString(w) {
			return "#"
		}
		return w
	})
}

// Attribute names the owners of v from the board index (nil: none resolve).
func (ix *BoardIndex) Attribute(v Violation) DRCOwner {
	o := DRCOwner{}
	items := v.Items
	if len(items) == 0 { // a report parsed before Items existed
		for _, u := range v.Objs {
			items = append(items, DRCItem{UUID: u})
		}
	}
	kinds := map[string]bool{}
	fps := map[string]bool{}
	var ids []string
	addRef := func(r string) {
		if r != "" && !contains(o.Refs, r) {
			o.Refs = append(o.Refs, r)
		}
	}
	for _, it := range items {
		io := DRCItemOwner{Description: it.Description, UUID: it.UUID}
		bi, ok := ix.Lookup(it.UUID)
		if !ok {
			bi = BoardItem{Kind: "unresolved"}
			if m := reItemRef.FindStringSubmatch(it.Description); m != nil {
				bi.Ref = m[1]
			}
		}
		io.Owner = bi
		kinds[bi.Kind] = true
		var id string
		switch bi.Kind {
		case "footprint":
			addRef(bi.Ref)
			id = "fp:" + bi.UUID
			if ix.uniqueRef(bi.Ref) {
				id = "fp:" + bi.Ref
			}
			fps[id] = true
		case "track", "via", "zone":
			if bi.Net != "" && !contains(o.Nets, bi.Net) {
				o.Nets = append(o.Nets, bi.Net)
			}
			id = bi.Kind + ":" + bi.Net
		case "outline":
			id = "outline"
		case "unresolved":
			addRef(bi.Ref)
			// The description names the object; its net may be renamed.
			id = "?:" + strings.TrimSpace(reItemNet.ReplaceAllString(it.Description, ""))
		default:
			id = bi.Kind + ":" + bi.Layer
		}
		io.ID = id
		ids = append(ids, id)
		o.Items = append(o.Items, io)
	}
	switch {
	case drcEnvironmentRules[v.Rule]:
		o.Class = "environment"
	case v.Section == "schematic_parity":
		o.Class = "schematic-parity"
	case kinds["outline"]:
		o.Class = "board-outline"
	case kinds["track"] || kinds["via"] || kinds["zone"]:
		o.Class = "routing"
	case len(fps) == 1:
		o.Class = "footprint"
	case len(fps) > 1:
		o.Class = "placement"
	case kinds["graphic"] || kinds["text"] || kinds["dimension"] || kinds["other"]:
		o.Class = "graphic"
	default:
		o.Class = "unresolved"
	}
	sort.Strings(o.Refs)
	sort.Strings(o.Nets)
	sort.Strings(ids)
	o.Key = o.Class + "|" + strings.Join(dedupeSorted(ids), ",")
	// A parity rule can report several problems on one footprint (a field
	// mismatch per field): KiCad's summary, numbers normalised, tells them
	// apart.
	if o.Class == "schematic-parity" {
		summary, _, _ := strings.Cut(v.Message, "; ")
		o.Key += "|" + normaliseMeasures(strings.TrimSpace(summary))
	}
	return o
}

func dedupeSorted(xs []string) []string {
	var out []string
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}

// OwnedViolation is a violation with its owners, position in KiCad's own
// frame (mm, y down) for people reading it next to the board.
type OwnedViolation struct {
	Violation
	Owner DRCOwner `json:"owner"`
	XMm   *float64 `json:"xMm,omitempty"`
	YMm   *float64 `json:"yMm,omitempty"`
}

// Own attributes every violation of vs.
func (ix *BoardIndex) Own(vs []Violation) []OwnedViolation {
	out := make([]OwnedViolation, 0, len(vs))
	for _, v := range vs {
		ov := OwnedViolation{Violation: v, Owner: ix.Attribute(v)}
		if v.X != nil && v.Y != nil {
			x, y := roundMm(*v.X*0.0254), roundMm(-*v.Y*0.0254)
			ov.XMm, ov.YMm = &x, &y
		}
		out = append(out, ov)
	}
	return out
}

func roundMm(v float64) float64 { return math.Round(v*1e4) / 1e4 }

// Item is the one-line gate item of a violation: rule, severity, owners,
// position and KiCad's description (a waiver's match is a substring of it).
func (v OwnedViolation) Item() string {
	var who []string
	for _, r := range v.Owner.Refs {
		who = append(who, r)
	}
	for _, n := range v.Owner.Nets {
		who = append(who, "["+n+"]")
	}
	s := fmt.Sprintf("%s (%s, %s", v.Rule, v.Severity, v.Owner.Class)
	if len(who) > 0 {
		s += ": " + strings.Join(who, " ")
	}
	s += ")"
	if v.XMm != nil {
		s += " @ (" + strconv.FormatFloat(*v.XMm, 'f', -1, 64) + ", " + strconv.FormatFloat(*v.YMm, 'f', -1, 64) + ") mm"
	}
	return s + ": " + v.Message
}

// DRCDiff is a report compared with a baseline.
type DRCDiff struct {
	New      []OwnedViolation `json:"new"`
	Resolved []OwnedViolation `json:"resolved"` // in the baseline, gone now
	Matched  int              `json:"matched"`
	TolMm    float64          `json:"toleranceMm"`
}

// DiffDRC matches cur against base, one to one: same rule, same owner Key,
// not escalated (a warning in the baseline that is an error now is new),
// and the same offending objects — the multiset of item uuids, each object
// within tolMm of its baseline position (parity and environment items name
// a footprint wherever it is: position does not count for them). When an
// item has no uuid on either side the fallback is stricter by position: the
// same number of items, in order, each within tolMm. The nearest unused
// baseline violation wins.
func DiffDRC(base, cur []OwnedViolation, tolMm float64) DRCDiff {
	d := DRCDiff{New: []OwnedViolation{}, Resolved: []OwnedViolation{}, TolMm: tolMm}
	used := make([]bool, len(base))
	for _, c := range cur {
		best, bestD := -1, math.Inf(1)
		for i, b := range base {
			if used[i] || b.Rule != c.Rule || b.Owner.Key != c.Owner.Key || escalated(b, c) {
				continue
			}
			dist, ok := matchDistanceMm(b, c)
			if !ok || dist > tolMm {
				continue
			}
			if dist < bestD {
				best, bestD = i, dist
			}
		}
		if best < 0 {
			d.New = append(d.New, c)
			continue
		}
		used[best] = true
		d.Matched++
	}
	for i, b := range base {
		if !used[i] {
			d.Resolved = append(d.Resolved, b)
		}
	}
	return d
}

func isError(v OwnedViolation) bool { return v.Severity == "" || v.Severity == "error" }

// escalated: the baseline item was not an error, the current one is.
func escalated(b, c OwnedViolation) bool { return isError(c) && !isError(b) }

// matchDistanceMm is how far apart a and b are as the same pair of objects:
// the largest distance between items with the same uuid (ok false when the
// uuid multisets differ), or with the fallback the largest in-order item
// distance. Parity / environment items are at distance 0 once matched.
func matchDistanceMm(a, b OwnedViolation) (float64, bool) {
	ignorePos := a.Owner.Class == "schematic-parity" || a.Owner.Class == "environment"
	pos := func(dx, dy float64) float64 {
		if ignorePos {
			return 0
		}
		return math.Hypot(dx, dy) * 0.0254
	}
	if len(a.Items) == 0 && len(b.Items) == 0 {
		switch {
		case ignorePos || a.XMm == nil && b.XMm == nil:
			return 0, true
		case a.XMm == nil || b.XMm == nil:
			return 0, false
		}
		return math.Hypot(*a.XMm-*b.XMm, *a.YMm-*b.YMm), true
	}
	if len(a.Items) != len(b.Items) {
		return 0, false
	}
	// Items identified on both sides pair by uuid; only the rest (a uuid on
	// at most one side) pair by position, in order. An identified object
	// with no counterpart is a different violation, unless the other side
	// has unidentified items it could be.
	inA, inB := map[string]bool{}, map[string]bool{}
	anonA, anonB := 0, 0
	for i := range a.Items {
		if u := a.Items[i].UUID; u != "" {
			inA[u] = true
		} else {
			anonA++
		}
		if u := b.Items[i].UUID; u != "" {
			inB[u] = true
		} else {
			anonB++
		}
	}
	worst := 0.0
	takenA, takenB := make([]bool, len(a.Items)), make([]bool, len(b.Items))
	for i, ai := range a.Items {
		if ai.UUID == "" || !inB[ai.UUID] {
			if ai.UUID != "" && anonB == 0 {
				return 0, false
			}
			continue
		}
		j, dj := -1, math.Inf(1)
		for k, bk := range b.Items {
			if takenB[k] || bk.UUID != ai.UUID {
				continue
			}
			if dk := pos(ai.X-bk.X, ai.Y-bk.Y); dk < dj {
				j, dj = k, dk
			}
		}
		if j < 0 {
			return 0, false
		}
		takenA[i], takenB[j] = true, true
		worst = math.Max(worst, dj)
	}
	for k, bk := range b.Items {
		if !takenB[k] && bk.UUID != "" && !inA[bk.UUID] && anonA == 0 {
			return 0, false
		}
	}
	var restA, restB []DRCItem
	for i, ai := range a.Items {
		if !takenA[i] {
			restA = append(restA, ai)
		}
	}
	for k, bk := range b.Items {
		if !takenB[k] {
			restB = append(restB, bk)
		}
	}
	if len(restA) != len(restB) {
		return 0, false
	}
	for i := range restA {
		if restA[i].UUID != "" && restB[i].UUID != "" {
			return 0, false // two identified, different objects
		}
		worst = math.Max(worst, pos(restA[i].X-restB[i].X, restA[i].Y-restB[i].Y))
	}
	return worst, true
}
