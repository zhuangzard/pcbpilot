package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

// Decision Q6: an intent widthMil.min at or above widthMil.outer forbids the
// neck-down; a smaller min is the net's neck floor.
func TestFromPcbautoNoNeckDown(t *testing.T) {
	b := &pcbauto.Board{Rules: pcbauto.DefaultRules(), CopperLayers: 2}
	a := &pcbauto.Analysis{TempRiseC: 10, Nets: []*pcbauto.NetPlan{
		{Net: "VIN", Role: pcbauto.RolePower, CurrentA: 1, Voltage: 12, WidthMil: 30, InnerWidthMil: 30, ClearanceMil: 8},
		{Net: "VOUT", Role: pcbauto.RolePower, CurrentA: 1, Voltage: 5, WidthMil: 30, InnerWidthMil: 30, ClearanceMil: 6},
		{Net: "SIG", Role: pcbauto.RoleSignal, CurrentA: 0.05, WidthMil: 6, InnerWidthMil: 6, ClearanceMil: 6},
		{Net: "USB_DP", Role: pcbauto.RoleDiff, CurrentA: 0.05, WidthMil: 8, InnerWidthMil: 8, ClearanceMil: 6},
	}}
	in := &pcbauto.Intent{Nets: map[string]*pcbauto.IntentNet{
		"VIN":  {WidthMil: pcbauto.IntentWidth{Outer: 30, Min: 30}},
		"vout": {WidthMil: pcbauto.IntentWidth{Outer: 30, Min: 12}},
	}}
	bk, ids, err := FromPcbauto(b, nil, a, PcbautoExtra{Intent: in})
	if err != nil {
		t.Fatal(err)
	}
	r, err := bk.Build()
	if err != nil {
		t.Fatal(err)
	}
	pad := PadSize{Long: mil(60), Narrow: mil(20)}
	if n := r.Neck(ids["VIN"], 0, pad); n.Zone != 0 {
		t.Errorf("VIN min = outer must not neck down: %+v", n)
	}
	if n := r.Neck(ids["VOUT"], 0, pad); n.Zone != mil(60) || n.MinWidth != mil(12) {
		t.Errorf("VOUT necks to its own min 12 mil: %+v", n)
	}
	// No per-net min: the global floor is the narrowest intent min (12 mil);
	// the signal is already narrower, so it cannot neck at all.
	if n := r.Neck(ids["SIG"], 0, pad); n.Zone != 0 {
		t.Errorf("SIG: %+v", n)
	}
	if n := r.Neck(ids["USB_DP"], 0, pad); n.Zone != 0 {
		t.Errorf("a diff pair is impedance-controlled: %+v", n)
	}
	if got, want := r.Clearance(Obj{Kind: Wire, Net: ids["VIN"]}, Obj{Kind: Wire, Net: ids["SIG"]}, 0), mil(8); got != want {
		t.Errorf("VIN clearance %d, want %d", got, want)
	}
	if len(r.Layers()) != 2 || !r.Layers()[0].Outer || r.Layers()[0].CopperUm < 34.9 {
		t.Errorf("layers %+v", r.Layers())
	}
}

const boardsDir = "../../../internal/app/testdata/boards"

// Bench acceptance (PLAN.md M2): on every fixture, the resolver built from
// pcbauto's Analysis gives each net the width (outer and inner) and the
// clearance of its NetPlan within 1 µm. A difference is allowed only where an
// intent floor (04 §4.6) raised the value; every one is listed in the log.
func TestPcbautoParityFixtures(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(boardsDir, "*.json"))
	n := 0
	for _, f := range files {
		base := filepath.Base(f)
		if strings.HasSuffix(base, ".expect.json") || strings.HasSuffix(base, ".spec.json") {
			continue
		}
		n++
		t.Run(strings.TrimSuffix(base, ".json"), func(t *testing.T) { parity(t, f) })
	}
	if n == 0 {
		t.Skip("no board fixtures")
	}
}

func parity(t *testing.T, path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := pcbauto.FromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	pre := pcbauto.Analyze(b, pcbauto.PowerSpec{}, nil)
	st := pcbauto.DecideStackup(b, pre, pcbauto.StackOptions{Force: b.CopperLayers})
	a := pcbauto.Analyze(b, pcbauto.PowerSpec{}, st)
	bk, ids, err := FromPcbauto(b, st, a, PcbautoExtra{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := bk.Build()
	if err != nil {
		t.Fatal(err)
	}
	ref := Obj{Kind: Wire, Net: geom.NetID(len(ids) + 1000)} // a net with board rules only
	const tol = 1000                                         // 1 µm
	reasons := map[string][]string{}
	checked := 0
	for _, np := range a.Nets {
		id := ids[np.Net]
		for _, l := range r.Layers() {
			want := mil(np.InnerWidthMil)
			if l.Outer {
				want = mil(np.WidthMil)
			}
			got := r.Width(id, l.ID)
			fl := r.IntentFloors(id, l.ID)
			checked++
			switch {
			case abs64(got-want) <= tol:
			case got > want && got == fl.Width:
				key := "inner IPC-2221 k=0.024 floor (pcbauto sizes inner copper with the outer constant)"
				if l.Outer {
					key = "outer IPC-2221 floor above the plan width"
				}
				reasons[key] = append(reasons[key], fmt.Sprintf("%s@%s %.2f→%.2f mil (%.2f A)", np.Net, l.Name, float64(want)/nmPerMil, float64(got)/nmPerMil, np.CurrentA))
			default:
				t.Errorf("%s on %s: width %d nm, plan %d nm, floor %d nm: unexplained", np.Net, l.Name, got, want, fl.Width)
			}
			want = mil(np.ClearanceMil)
			got = r.Clearance(Obj{Kind: Wire, Net: id}, ref, l.ID)
			checked++
			switch {
			case abs64(got-want) <= tol:
			case got > want && got == fl.Clearance:
				key := "voltage floor above the plan clearance"
				reasons[key] = append(reasons[key], fmt.Sprintf("%s@%s %.2f→%.2f mil (%g V)", np.Net, l.Name, float64(want)/nmPerMil, float64(got)/nmPerMil, np.Voltage))
			default:
				t.Errorf("%s on %s: clearance %d nm, plan %d nm, floor %d nm: unexplained", np.Net, l.Name, got, want, fl.Clearance)
			}
		}
	}
	keys := make([]string, 0, len(reasons))
	diffs := 0
	for k := range reasons {
		keys = append(keys, k)
		diffs += len(reasons[k])
	}
	sort.Strings(keys)
	t.Logf("%d nets, %d layers, %d values checked, %d differ (all from intent floors)", len(a.Nets), len(r.Layers()), checked, diffs)
	for _, k := range keys {
		ex := reasons[k]
		if len(ex) > 6 {
			ex = append(ex[:6:6], fmt.Sprintf("… %d more", len(reasons[k])-6))
		}
		t.Logf("  %s: %d: %s", k, len(reasons[k]), strings.Join(ex, "; "))
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
