package pcbauto

import (
	"math"
	"sort"
	"strings"
)

// Delivery verdict: what keeps a routed board from being deliverable beyond
// completion and DRC. The priority is safety > electrical > DRC >
// completion > aesthetics, and no time budget may decide a safety or
// electrical outcome silently: a fix-up that could not finish leaves its
// finding here, and the board is not called deliverable.
//
//   - safety (joint gates, overall capped like a short): isolation findings
//     on the routed copper (creepage/clearance below an intent pair),
//     bridge parts that cannot meet their pair, copper-to-edge/hole ERRORs of a
//     domain insulated from an accessible edge;
//   - blockers (not deliverable, score unchanged): via arrays short of
//     their current sizing, power nets over their IR-drop budget or open,
//     SELV copper below the fabrication edge rule.

// deliveryBlockers returns the safety and electrical reasons a result is not
// deliverable (nil inputs contribute nothing).
func deliveryBlockers(iso *IsolationReport, edge *EdgeCheck, rr *RouteResult) (safety, electrical []string) {
	if iso != nil {
		var errs []IsoFinding
		for _, f := range iso.Findings {
			if f.Severity == "error" {
				errs = append(errs, f)
			}
		}
		if len(errs) > 0 {
			safety = append(safety, sprintf("isolation: %d creepage/clearance finding(s) on the routed copper (worst: %s)", len(errs), errs[0].Message))
		}
		if len(iso.Infeasible) > 0 {
			x := iso.Infeasible[0]
			safety = append(safety, sprintf("isolation: %d bridge part(s) cannot meet the pair (%s: %s)", len(iso.Infeasible), x.Ref, x.Reason))
		}
	}
	if edge != nil {
		// Copper of a domain with an insulation requirement to an
		// accessible edge (mains, hazardous, patient) is safety; the
		// fabrication edge rule of SELV copper (an edge connector's shell
		// pad on the outline) blocks delivery without capping the score.
		var hv, fab []EdgeFinding
		for _, f := range edge.Findings {
			if f.Level != "ERROR" {
				continue
			}
			if edge.Policy != nil && edge.Policy.NetReq(f.Net) > 0 {
				hv = append(hv, f)
			} else {
				fab = append(fab, f)
			}
		}
		worst := func(fs []EdgeFinding) EdgeFinding {
			sort.SliceStable(fs, func(i, j int) bool { return fs[i].GapMil-fs[i].RequiredMil < fs[j].GapMil-fs[j].RequiredMil })
			return fs[0]
		}
		if len(hv) > 0 {
			safety = append(safety, sprintf("board edge: %d copper-to-edge/hole finding(s) of an insulated domain (worst: %s)", len(hv), worst(hv).Message))
		}
		if len(fab) > 0 {
			electrical = append(electrical, sprintf("board edge: %d copper-to-edge/hole finding(s) below the fabrication edge rule (worst: %s)", len(fab), worst(fab).Message))
		}
	}
	if rr != nil {
		if n := len(rr.ViaShortfalls); n > 0 {
			s := rr.ViaShortfalls[0]
			electrical = append(electrical, sprintf("via current: %d transition group(s) short of their sized via array (%s: %s)", n, s.Net, s.Reason))
		}
		if pw := rr.Power; pw != nil && pw.Violations() > 0 {
			var open, over []string
			for _, x := range pw.Nets {
				switch x.Status {
				case "open":
					open = append(open, x.Net)
				case "over-budget":
					over = append(over, sprintf("%s %.2f× budget", x.Net, x.WorstMV/math.Max(x.BudgetMV, 1e-9)))
				}
			}
			if len(over) > 0 {
				electrical = append(electrical, sprintf("IR drop: %d power net(s) over their drop budget (%s)", len(over), strings.Join(over, ", ")))
			}
			if len(open) > 0 {
				electrical = append(electrical, sprintf("IR drop: %d simulated power net(s) not connected from source to load (%s)", len(open), strings.Join(open, ", ")))
			}
		}
	}
	return safety, electrical
}

// NotDeliverable lists every reason js is not deliverable, safety first:
// the gates (shorts, overlaps, isolation, board edge), the electrical
// blockers, then completion, plane connections and DRC. Empty when the
// board is deliverable.
func (js *JointScore) NotDeliverable() []string {
	if js == nil {
		return nil
	}
	out := append(append([]string(nil), js.Gates...), js.Blockers...)
	if js.Completion < 100 {
		out = append(out, sprintf("routing incomplete: %.1f%% of the signal connections", js.Completion))
	}
	if js.PlaneOpen > 0 {
		out = append(out, sprintf("%d of %d plane/ground connections open", js.PlaneOpen, js.PlanePads))
	}
	if js.DRC > 0 {
		out = append(out, sprintf("%d DRC violation(s)", js.DRC))
	}
	return out
}
