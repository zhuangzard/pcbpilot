package app

// kicad_route_vias.go — fastroute's vias at each net's own size.
//
// fastroute 0.1.14 puts one via padstack on every net: the first one of the
// DSN's structure (via …) list, ignoring the classes' use_via (a 1443-part
// board got 262 Default 0.6 mm vias on nets whose class via is 0.6096 or
// 0.7 mm; kicad rules --check failed them). So the via list is ordered
// largest first (diameter, then drill): fastroute plans every via with the
// largest class via, and after the import each router via that is larger
// than its net's effective class via is shrunk to it. Shrinking keeps
// every clearance fastroute planned (the copper only gets smaller) and
// gives each net exactly the via the plan and kicad rules expect.

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	reDSNViaList = regexp.MustCompile(`\(via((?:\s+"[^"]*")+)\s*\)`)
	reDSNViaName = regexp.MustCompile(`"[^"]*_([0-9.]+):([0-9.]+)_um"`)
)

// dsnViaLargestFirst reorders the structure's via list largest first; the
// text is unchanged when the list or a size cannot be read.
func dsnViaLargestFirst(dsn string) (string, []string) {
	loc := reDSNViaList.FindStringSubmatchIndex(dsn)
	if loc == nil {
		return dsn, nil
	}
	names := regexp.MustCompile(`"[^"]*"`).FindAllString(dsn[loc[2]:loc[3]], -1)
	type v struct {
		name       string
		dia, drill float64
	}
	var vs []v
	for _, n := range names {
		m := reDSNViaName.FindStringSubmatch(n)
		if m == nil {
			return dsn, nil
		}
		d, e1 := strconv.ParseFloat(m[1], 64)
		h, e2 := strconv.ParseFloat(m[2], 64)
		if e1 != nil || e2 != nil {
			return dsn, nil
		}
		vs = append(vs, v{n, d, h})
	}
	sort.SliceStable(vs, func(i, j int) bool {
		if vs[i].dia != vs[j].dia {
			return vs[i].dia > vs[j].dia
		}
		return vs[i].drill > vs[j].drill
	})
	order := make([]string, len(vs))
	for i, x := range vs {
		order[i] = x.name
	}
	return dsn[:loc[0]] + "(via " + strings.Join(order, " ") + ")" + dsn[loc[1]:], order
}

type routeVia struct {
	ID    string  `json:"primitiveId"`
	Net   string  `json:"net"`
	Dia   float64 `json:"diameter"`
	Drill float64 `json:"holeDiameter"`
}

func snapshotVias(s *boardSnapshot) []routeVia {
	var out []routeVia
	if s == nil || s.Copper == nil {
		return nil
	}
	_ = decodeAny(s.Copper.Vias, &out)
	return out
}

// routerViasToClass shrinks the vias the router added (not on before) to
// their net's effective class via, read back by KiCad on board.
func (r *kicadRun) routerViasToClass(before *boardSnapshot, board string) (string, error) {
	old := map[string]bool{}
	for _, v := range snapshotVias(before) {
		old[v.ID] = true
	}
	snap, _, err := r.snapshot(board)
	if err != nil {
		return board, err
	}
	rb, err := r.kt.RulesRead(board)
	if err != nil {
		return board, err
	}
	var ops []map[string]any
	n := 0
	for _, v := range snapshotVias(snap) {
		if old[v.ID] {
			continue
		}
		n++
		eff, ok := rb.Nets[v.Net]
		if !ok || eff.ViaDiameter <= 0 || eff.ViaDrill <= 0 || eff.ViaDrill >= eff.ViaDiameter {
			continue
		}
		dia, drill := int64(math.Round(v.Dia*nmPerMil)), int64(math.Round(v.Drill*nmPerMil))
		if dia <= eff.ViaDiameter+500 && drill <= eff.ViaDrill+500 {
			continue // already the class via (or smaller: judged by kicad rules)
		}
		if dia < eff.ViaDiameter || drill < eff.ViaDrill {
			continue // never grow a via: clearance was planned for this size
		}
		ops = append(ops, map[string]any{"id": v.ID, "diameter": float64(eff.ViaDiameter) / nmPerMil, "drill": float64(eff.ViaDrill) / nmPerMil})
	}
	r.summary["routerVias"] = map[string]any{"routerVias": n, "shrunkToClass": len(ops)}
	if len(ops) == 0 {
		return board, nil
	}
	out := r.next("vias-to-class")
	if _, err := r.kt.Edit(board, map[string]any{"setVia": ops}, out); err != nil {
		return board, err
	}
	fmt.Fprintf(r.stderr, "vias: %d of %d router via(s) shrunk to their net's class via\n", len(ops), n)
	return out, nil
}
