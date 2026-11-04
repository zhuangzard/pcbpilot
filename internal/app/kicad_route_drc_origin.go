package app

// kicad_route_drc_origin.go — where each DRC error of a routed board comes from.
// Purely a report: every error stays in the kicad-drc gate. An error between two
// objects of one footprint is the same on any placement; neither it nor a pad too
// near the edge changes with a routing setting. The designer settles them per
// footprint, with the part's evidence.

import (
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

var (
	reDRCItemRef = regexp.MustCompile(` of (\S+)`)
	reDRCNet     = regexp.MustCompile(`\[[^\]]*\]`)
)

// drcOrigin counts a board's errors by where they come from.
type drcOrigin struct {
	FootprintInternal int            `json:"footprintInternal"` // between two objects of one footprint
	EdgePad           int            `json:"edgePad"`           // a footprint's pad against the board edge
	Other             int            `json:"other"`             // routing, or between footprints
	Footprints        map[string]int `json:"footprints,omitempty"`
}

func drcItemRef(description string) string {
	if m := reDRCItemRef.FindStringSubmatch(reDRCNet.ReplaceAllString(description, "")); m != nil {
		return m[1]
	}
	return ""
}

// drcOrigins sorts the violations of a report; rules without two objects on
// copper (silkscreen, library) and unconnected items are left out.
func drcOrigins(vs []kicad.Violation) drcOrigin {
	o := drcOrigin{Footprints: map[string]int{}}
	for _, v := range vs {
		if v.Section != "violations" || kicad.DRCRuleClass(v.Rule) != "hard" || len(v.Items) < 2 {
			continue
		}
		var refs []string
		edge := false
		for _, it := range v.Items {
			if strings.Contains(it.Description, "Edge.Cuts") {
				edge = true
				continue
			}
			refs = append(refs, drcItemRef(it.Description))
		}
		sort.Strings(refs)
		switch {
		case edge && len(refs) == 1 && refs[0] != "":
			o.EdgePad++
			o.Footprints[refs[0]]++
		case !edge && len(refs) == 2 && refs[0] != "" && refs[0] == refs[1]:
			o.FootprintInternal++
			o.Footprints[refs[0]]++
		default:
			o.Other++
		}
	}
	return o
}
