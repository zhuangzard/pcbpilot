package app

// sch_compose_buses.go — native buses in the compose Apply queue.

import (
	"fmt"
	"path/filepath"
)

// schComposeBusJournal resolves the per-page bus journal of a composition.
func schComposeBusJournal(p *schCompositionPlan) string {
	if p.BusJournal != "" {
		if abs, err := filepath.Abs(p.BusJournal); err == nil {
			return abs
		}
		return p.BusJournal
	}
	path := defaultSchBusJournal(p.Connectivity.ProjectID, p.Connectivity.DocumentID)
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// schComposeUserBusGuard refuses a rebuild that would let `sch clear`
// delete a bus pcbpilot did not create: every bus in the fresh before
// snapshot must be in the page's bus journal with the same id, name and
// geometry (segment-set comparison). User buses are never deleted.
func schComposeUserBusGuard(pagePrimitives map[string]any, journalPath string) error {
	rows, _ := pagePrimitives["buses"].([]any)
	if len(rows) == 0 {
		return nil
	}
	j, err := readSchBusJournal(journalPath)
	if err != nil {
		return err
	}
	ours := map[string]SchematicNativeBus{}
	for _, b := range j.Buses {
		ours[b.PrimitiveID] = b
	}
	for _, item := range rows {
		row, _ := item.(map[string]any)
		id := stringVal(row["primitiveId"])
		name := stringVal(row["BusName"])
		line := decodeAnyBusLine(row["Line"])
		b, ok := ours[id]
		if !ok || b.BusName != name || !schSameBusLine(b.Line, line) {
			return fmt.Errorf("target page holds bus %s (%q) that pcbpilot did not create (not in bus journal %s with the same name/geometry); a guarded rebuild clears the page and would delete it — never deleted automatically: move or delete it yourself, then recapture --before", id, name, journalPath)
		}
	}
	return nil
}

// decodeAnyBusLine reads a host bus line (flat or nested numbers).
func decodeAnyBusLine(v any) [][]float64 {
	xs, ok := v.([]any)
	if !ok || len(xs) == 0 {
		return nil
	}
	num := func(x any) (float64, bool) {
		f, ok := x.(float64)
		return f, ok
	}
	if _, flat := num(xs[0]); flat {
		var l []float64
		for _, x := range xs {
			if f, ok := num(x); ok {
				l = append(l, f)
			}
		}
		return [][]float64{l}
	}
	var out [][]float64
	for _, x := range xs {
		if inner, ok := x.([]any); ok {
			var l []float64
			for _, y := range inner {
				if f, ok := num(y); ok {
					l = append(l, f)
				}
			}
			out = append(out, l)
		}
	}
	return out
}
