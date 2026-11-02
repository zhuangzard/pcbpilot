package app

// kicad_route_vias.go — fastroute's vias at each net's own size.
//
// fastroute 0.1.14 does not keep every via at its class's use_via: its
// fanout takes a via from the DSN's structure (via …) list (a 1443-part
// board got 262 Default 0.6 mm vias on nets whose class via is 0.6096 or
// 0.7 mm; kicad rules --check failed them; a small board got a 600/300 via
// on a net whose class used another). So the list holds one padstack, the
// componentwise maximum of every class via — the largest diameter AND the
// largest drill (classes 0.8/0.2 and 0.7/0.4 give 0.8/0.4) — added to the
// DSN when no class has it; the classes keep their own use_via.
// After the import each router via is shrunk to its net's effective class
// via: both dimensions only ever shrink, so every clearance fastroute
// planned stands and each class via is reachable. A router via that cannot
// become its class via (smaller in a dimension, or a net without a known
// class via) is never skipped: the router-vias gate fails / cannot-checks.
//
// KiCad's SES import reads a via's drill from the padstack name
// "Via[…]_<diameter>:<drill>_um" (the names its DSN export writes), so the
// added padstack carries both numbers in its name. Two observed exceptions
// shape the rest of this file:
//
//   - fastroute 0.1.14 does not honour a use_via whose name has a fractional
//     µm (a 24 mil class exports "…_609.6:304.8_um"): that class's routed vias
//     take the smallest class via of the DSN. dsnWholeUmVias rounds such
//     padstacks up to whole µm, so the class via stands and the step after
//     the import still only shrinks.
//   - KiCad's import ignores the name's drill when the name equals the
//     Default class via and takes the net's class drill instead: a 600:300 via
//     on a 0.7/0.4 net comes in as 0.6/0.4, an illegal annular ring. Such a via
//     cannot be shrunk to its class; it is repaired only per via, in a
//     transaction (kicad_route_via_repair.go), or the gate fails.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

var (
	reDSNViaList = regexp.MustCompile(`\(via((?:\s+"[^"]*")+)\s*\)`)
	reDSNViaName = regexp.MustCompile(`^"(Via\[[^\]]*\])_([0-9.]+):([0-9.]+)_um"$`)
)

// dsnVia is one via padstack of the DSN list (µm, from its name).
type dsnVia struct {
	name       string // quoted, as in the DSN
	prefix     string // Via[0-5]
	dia, drill float64
}

// dsnRouterVia makes the componentwise-maximum via the first of the
// structure's via list (adding its padstack when no class via has both
// maxima). It returns the DSN, the list as written and the router via; the
// text is unchanged when the list or a size cannot be read.
func dsnRouterVia(dsn string) (string, []string, *dsnVia) {
	dsn = dsnWholeUmVias(dsn)
	loc := reDSNViaList.FindStringSubmatchIndex(dsn)
	if loc == nil {
		return dsn, nil, nil
	}
	names := regexp.MustCompile(`"[^"]*"`).FindAllString(dsn[loc[2]:loc[3]], -1)
	var vs []dsnVia
	for _, n := range names {
		m := reDSNViaName.FindStringSubmatch(n)
		if m == nil {
			return dsn, nil, nil
		}
		d, e1 := strconv.ParseFloat(m[2], 64)
		h, e2 := strconv.ParseFloat(m[3], 64)
		if e1 != nil || e2 != nil || d <= 0 || h <= 0 {
			return dsn, nil, nil
		}
		vs = append(vs, dsnVia{name: n, prefix: m[1], dia: d, drill: h})
	}
	if len(vs) == 0 {
		return dsn, nil, nil
	}
	top := dsnVia{prefix: vs[0].prefix}
	src := vs[0]
	for _, v := range vs {
		top.dia, top.drill = math.Max(top.dia, v.dia), math.Max(top.drill, v.drill)
		if v.dia > src.dia {
			src = v
		}
	}
	var existing *dsnVia
	for i := range vs {
		if vs[i].dia == top.dia && vs[i].drill == top.drill {
			existing = &vs[i]
		}
	}
	if existing != nil {
		top = *existing
	} else {
		// A padstack with the largest diameter and the largest drill: the
		// largest-diameter padstack's shapes, at that diameter.
		f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
		top.name = fmt.Sprintf(`"%s_%s:%s_um"`, top.prefix, f(top.dia), f(top.drill))
		at := strings.Index(dsn, "(padstack "+src.name)
		if at < 0 {
			return dsn, nil, nil
		}
		end := sexprEnd(dsn, at)
		if end < 0 {
			return dsn, nil, nil
		}
		ps := strings.Replace(dsn[at:end], src.name, top.name, 1)
		ps = regexp.MustCompile(`(\(circle\s+\S+\s+)`+regexp.QuoteMeta(f(src.dia))+`\b`).ReplaceAllString(ps, "${1}"+f(top.dia))
		dsn = dsn[:at] + ps + "\n    " + dsn[at:]
		loc = reDSNViaList.FindStringSubmatchIndex(dsn)
	}
	// The list holds the router via alone: fastroute's autorouter still
	// takes each class's use_via from the library (verified, 0.1.14), and
	// every other via it makes (fanout) is the router via — never a smaller
	// one it would pick from the list (a 600/300 via appeared on a net whose
	// class used 800/400 while the list still held 600/300).
	order := []string{top.name}
	return dsn[:loc[0]] + "(via " + top.name + ")" + dsn[loc[1]:], order, &top
}

var (
	reDSNViaPadstack = regexp.MustCompile(`\(padstack ("Via\[[^\]]*\]_([0-9.]+):([0-9.]+)_um")`)
	reDSNCircle      = regexp.MustCompile(`(\(circle\s+\S+\s+)[0-9.]+`)
)

// dsnWholeUmVias rounds every via padstack with a fractional µm in its name
// up to whole µm (name, circles and every reference), merging padstacks that
// land on one name. A DSN without such a name comes back unchanged.
func dsnWholeUmVias(dsn string) string {
	ms := reDSNViaPadstack.FindAllStringSubmatch(dsn, -1)
	have := map[string]bool{}
	for _, m := range ms {
		have[m[1]] = true
	}
	up := func(s string) (string, bool) {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || v <= 0 {
			return s, false
		}
		return strconv.FormatFloat(math.Ceil(v-1e-6), 'f', -1, 64), true
	}
	var rename []string
	for _, m := range ms {
		if !strings.Contains(m[2]+m[3], ".") {
			continue
		}
		d, ok1 := up(m[2])
		h, ok2 := up(m[3])
		if !ok1 || !ok2 {
			continue
		}
		name := m[1][:strings.LastIndex(m[1], "_"+m[2]+":")] + "_" + d + ":" + h + `_um"`
		at := strings.Index(dsn, "(padstack "+m[1])
		if at < 0 {
			continue
		}
		end := sexprEnd(dsn, at)
		if end < 0 {
			continue
		}
		if have[name] {
			dsn = dsn[:at] + strings.TrimLeft(dsn[end:], " \t\r\n")
		} else {
			dsn = dsn[:at] + reDSNCircle.ReplaceAllString(dsn[at:end], "${1}"+d) + dsn[end:]
			have[name] = true
		}
		rename = append(rename, m[1], name)
	}
	if len(rename) == 0 {
		return dsn
	}
	return strings.NewReplacer(rename...).Replace(dsn)
}

// sexprEnd is the index just past the list that opens at dsn[at] ('(').
func sexprEnd(dsn string, at int) int {
	depth, inQ := 0, false
	for i := at; i < len(dsn); i++ {
		switch c := dsn[i]; {
		case c == '"':
			inQ = !inQ
		case inQ:
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

type routeVia struct {
	ID    string  `json:"primitiveId"`
	Net   string  `json:"net"`
	Dia   float64 `json:"diameter"`
	Drill float64 `json:"holeDiameter"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	From  int     `json:"fromLayer"`
	To    int     `json:"toLayer"`
}

// snapshotVias reads a snapshot's vias. A snapshot without its copper
// section, an absent or null vias list, a vias list that does not decode or a via without an id or a
// size is an error: never an empty list (that would hide router vias, or
// make a board's own vias look router-added).
func snapshotVias(s *boardSnapshot) ([]routeVia, error) {
	if s == nil || s.Copper == nil {
		return nil, fmt.Errorf("the snapshot has no copper section")
	}
	// encoding/json leaves the slice nil for an absent or null "vias" and
	// makes it non-nil for an explicit []: only the latter means no vias.
	if s.Copper.Vias == nil {
		return nil, fmt.Errorf("copper.vias is absent or null")
	}
	var out []routeVia
	if err := decodeAny(s.Copper.Vias, &out); err != nil {
		return nil, fmt.Errorf("copper.vias: %w", err)
	}
	for i, v := range out {
		if v.ID == "" || v.Dia <= 0 || v.Drill <= 0 {
			return nil, fmt.Errorf("copper.vias[%d]: no id, diameter or hole diameter (%+v)", i, v)
		}
	}
	return out, nil
}

// viaExactTolNM covers the snapshot's mil rounding (4 decimals, ±1.3 nm).
const viaExactTolNM = 100

// viaNotReachable: shrinking cannot make this via its class via (nm).
func viaNotReachable(dia, drill, classDia, classDrill int64) bool {
	const tol = 500 // what a router via may be short of its class via
	return dia+tol < classDia || drill+tol < classDrill
}

// classViaPlan sorts the router vias (not in old) into shrink operations
// to their net's class via (eff: diameter / drill nm per net) and problems:
// fail (the class via is not reachable by shrinking) and unknown (no class
// via for the net).
func classViaPlan(vias []routeVia, old map[string]bool, eff func(net string) (dia, drill int64, ok bool)) (ops []map[string]any, fail, unknown []string, n int) {
	for _, v := range vias {
		if old[v.ID] {
			continue
		}
		n++
		cd, ch, ok := eff(v.Net)
		if !ok || cd <= 0 || ch <= 0 || ch >= cd {
			unknown = append(unknown, fmt.Sprintf("%s via %s: the net has no valid class via to size it to (%.2f/%.2f mil kept)", v.Net, short12(v.ID), v.Dia, v.Drill))
			continue
		}
		dia, drill := int64(math.Round(v.Dia*nmPerMil)), int64(math.Round(v.Drill*nmPerMil))
		if viaNotReachable(dia, drill, cd, ch) {
			fail = append(fail, fmt.Sprintf("%s via %s: router via %.2f/%.2f mil cannot be shrunk to the class via %.2f/%.2f mil", v.Net, short12(v.ID), v.Dia, v.Drill, float64(cd)/nmPerMil, float64(ch)/nmPerMil))
			continue
		}
		if dia <= cd+viaExactTolNM && drill <= ch+viaExactTolNM {
			continue // already the class via
		}
		ops = append(ops, map[string]any{"id": v.ID, "diameter": float64(cd) / nmPerMil, "drill": float64(ch) / nmPerMil})
	}
	return ops, fail, unknown, n
}

// routerViaTools are the KiCad calls sizing the router vias needs
// (*kicad.Tools).
type routerViaTools interface {
	Snapshot(pcb string) ([]byte, error)
	RulesRead(pcb string) (*kicad.RulesReadback, error)
	Edit(pcb string, ops map[string]any, out string) (json.RawMessage, error)
	DRC(pcb, out string) (*kicad.DRCReport, error)
}

// sizeRouterVias shrinks the vias the router added on board (those not on
// classed) to their net's effective class via, read back by KiCad. It
// never fails silently: every reason the vias could not be sized is a
// router-vias gate — cannot-check when the data to judge them is missing
// (a snapshot, the class readback), failing when the vias are known to be
// wrong (not reachable, the resize not written) — and the board it
// returns is the one the gates describe.
func sizeRouterVias(t routerViaTools, classed, board string, next func(string) string, summary map[string]any, stderr io.Writer) (string, []gateResult) {
	return sizeRouterViasProv(t, classed, board, next, summary, stderr, nil)
}

// sizeRouterViasProv is sizeRouterVias, also recording which via came from
// where for the final via audit (prov may be nil).
func sizeRouterViasProv(t routerViaTools, classed, board string, next func(string) string, summary map[string]any, stderr io.Writer, prov *viaProvenance) (string, []gateResult) {
	gate := func(pass bool, detail string, items ...string) []gateResult {
		return []gateResult{{Gate: "router-vias", Pass: pass, Detail: detail, Items: items}}
	}
	snap := func(pcb string) (*boardSnapshot, error) {
		raw, err := t.Snapshot(pcb)
		if err != nil {
			return nil, err
		}
		var s boardSnapshot
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("decode KiCad snapshot: %w", err)
		}
		return &s, nil
	}
	before, err := snap(classed)
	if err != nil {
		summary["routerVias"] = map[string]any{"error": err.Error()}
		return board, gate(false, "cannot check: the board before the import could not be read, so the router vias are unknown (none resized): "+err.Error())
	}
	beforeVias, err := snapshotVias(before)
	if err != nil {
		summary["routerVias"] = map[string]any{"error": err.Error()}
		return board, gate(false, "cannot check: the board before the import has no readable vias, so the router vias are unknown (none resized): "+err.Error())
	}
	old := map[string]bool{}
	for _, v := range beforeVias {
		old[v.ID] = true
	}
	after, err := snap(board)
	if err != nil {
		summary["routerVias"] = map[string]any{"error": err.Error()}
		return board, gate(false, "cannot check: the imported board could not be read, so the router vias were not sized to their class (none resized): "+err.Error())
	}
	rb, err := t.RulesRead(board)
	if err != nil {
		summary["routerVias"] = map[string]any{"error": err.Error()}
		return board, gate(false, "cannot check: KiCad's class readback failed, so the router vias were not sized to their class: "+err.Error())
	}
	afterVias, err := snapshotVias(after)
	if err != nil {
		summary["routerVias"] = map[string]any{"error": err.Error()}
		return board, gate(false, "cannot check: the imported board has no readable vias, so the router vias were not sized to their class (none resized): "+err.Error())
	}
	eff := func(net string) (int64, int64, bool) {
		e, ok := rb.Nets[net]
		return e.ViaDiameter, e.ViaDrill, ok
	}
	prov.record(old, afterVias)
	ops, fail, unknown, n := classViaPlan(afterVias, old, eff)
	stats := map[string]any{"routerVias": n, "shrunkToClass": len(ops), "notReachable": len(fail), "noClassVia": len(unknown)}
	summary["routerVias"] = stats
	if len(ops) > 0 {
		out := next("vias-to-class")
		if _, err := t.Edit(board, map[string]any{"setVia": ops}, out); err != nil {
			stats["error"] = err.Error()
			g := gate(false, fmt.Sprintf("%d router via(s) are larger than their class via and the resize could not be written: %v", len(ops), err))
			return board, append(g, problemGates(gate, fail, unknown)...)
		}
		fmt.Fprintf(stderr, "vias: %d of %d router via(s) shrunk to their net's class via\n", len(ops), n)
		board = out
	}
	var info []string
	if len(fail) > 0 {
		var rep viaRepairResult
		board, rep = repairRouterVias(t, board, viaGrowCandidates(afterVias, old, eff), rb, next, stats)
		prov.recordGrown(rep.grown)
		fail, info = rep.evidence, rep.info
		fmt.Fprintf(stderr, "vias: %d via(s) below their class via grown in a checked transaction, %d not\n", len(rep.grown), len(fail))
	}
	gates := problemGates(gate, fail, unknown)
	if len(info) > 0 && len(gates) == 0 {
		gates = []gateResult{{Gate: "router-vias", Pass: true, Detail: fmt.Sprintf("%d router via(s) grown to their class via (checked)", len(info)), Info: info}}
	}
	return board, gates
}

// problemGates is the router-vias gate for vias that are known wrong (fail)
// or cannot be judged (unknown); none when both are empty.
func problemGates(gate func(bool, string, ...string) []gateResult, fail, unknown []string) []gateResult {
	if len(fail)+len(unknown) == 0 {
		return nil
	}
	items := append(append([]string{}, fail...), unknown...)
	if len(fail) > 0 {
		return gate(false, fmt.Sprintf("%d router via(s) cannot be shrunk to their class via, %d without a class via", len(fail), len(unknown)), items...)
	}
	return gate(false, fmt.Sprintf("cannot check: %d router via(s) on nets without a class via", len(unknown)), items...)
}

// routerViasToClass is sizeRouterVias on this run's board files.
func (r *kicadRun) routerViasToClass(classed, board string) (string, []gateResult) {
	if r.viaProv == nil {
		r.viaProv = &viaProvenance{}
	}
	return sizeRouterViasProv(r.kt, classed, board, r.next, r.summary, r.stderr, r.viaProv)
}
