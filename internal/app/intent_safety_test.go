package app

import (
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/intent"
)

// intent derive must use the pkg/safety tables: IEC 60601-1 2×MOPP at
// 250 Vrms is 8 mm creepage / 5 mm clearance (Table 12).
func TestIntentUsesSafetyTables(t *testing.T) {
	cl, cr, _, _, ref, _ := intent.SafetyDistances(
		intent.Pair{WorkingVrms: 250, WorkingVpeak: 354, Insulation: "reinforced", MOP: "MOPP", MOPCount: 2},
		intent.Standard{Name: "IEC60601-1", MOP: "MOPP", MOPCount: 2, PollutionDegree: 2, MaterialGroup: "IIIa", OvervoltageCategory: "II"})
	if cr < 7.99 || cl < 4.99 || !strings.Contains(ref, "60601") {
		t.Fatalf("2×MOPP 250 V: creepage %.2f clearance %.2f ref %q — want 8 / 5 mm from IEC 60601-1", cr, cl, ref)
	}
}
