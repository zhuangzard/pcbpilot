package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

func composeOriginalRefsFixture(refs []string) schCompositionSource {
	s := composeFixture(len(refs))
	s.Sheet = layoutBBox{MinX: 0, MinY: 0, MaxX: 4000, MaxY: 1200}
	m := schCompositionModule{ID: "original-Lib", Title: "Original designators"}
	canonical := connectivity.Module{ID: m.ID, Name: m.Title}
	for i, ref := range refs {
		s.Connectivity.Components[i].Ref = ref
		p := powerLayoutPlan{Placements: s.Modules[i].Placements, Wires: s.Modules[i].Wires, Flags: s.Modules[i].Flags}
		p.Placements[0].Designator = ref
		translatePowerLayout(&p, float64(i)*300, 0)
		m.Placements = append(m.Placements, p.Placements...)
		m.Wires = append(m.Wires, p.Wires...)
		m.Flags = append(m.Flags, p.Flags...)
		// These are independent copies, not dedicated peripherals. Keep the
		// fixture about ref spelling rather than inventing disconnected ownership.
		canonical.CoreComponents = append(canonical.CoreComponents, s.Connectivity.Components[i].ID)
	}
	s.Modules = []schCompositionModule{m}
	s.Connectivity.Modules = []connectivity.Module{canonical}
	return s
}

func TestComposePreservesOriginalDesignatorSpellingAndDeclarationOrder(t *testing.T) {
	refs := []string{"C1", "C10", "C2", "U3", "u04", "C007"}
	s := composeOriginalRefsFixture(refs)
	original, _ := json.Marshal(s)
	p, err := planSchComposition(s)
	if err != nil {
		t.Fatal(err)
	}
	var components, placements []string
	for _, c := range p.Connectivity.Components {
		components = append(components, c.Ref)
	}
	for _, c := range p.Layout.Placements {
		placements = append(placements, c.Designator)
	}
	if !reflect.DeepEqual(components, refs) || !reflect.DeepEqual(placements, refs) {
		t.Fatalf("reference identity/order changed: components=%v placements=%v", components, placements)
	}
	after, _ := json.Marshal(s)
	if string(original) != string(after) {
		t.Fatal("composition rewrote source data")
	}
	before := composeEmptyPageBefore(p)
	pb, err := schCompositionPlaybook(p, composeApplyBytes(t, before), true)
	if err != nil {
		t.Fatal(err)
	}
	var queued []string
	for _, step := range pb.Steps {
		if step.Action == "schematic.component.place" {
			queued = append(queued, step.Payload["designator"].(string))
		}
		if step.Run == "sch group create" && step.Flags["members"] != strings.Join(refs, ",") {
			t.Fatalf("Lib member sequence was rewritten: %v", step.Flags["members"])
		}
	}
	if !reflect.DeepEqual(queued, refs) {
		t.Fatalf("place queue changed designators or order: %v", queued)
	}
	_, g, err := groupsCreate(nil, "original-Lib", refs)
	if err != nil || !reflect.DeepEqual(g.Members, refs) {
		t.Fatalf("registration changed reference identities: %+v %v", g, err)
	}
}

func TestComposeRejectsRenumberedPrefixedOrRecasedDesignatorsBeforeApply(t *testing.T) {
	for _, wrong := range []string{"U3", "RF_U03", "u03", "U031"} {
		s := composeOriginalRefsFixture([]string{"C1", "C10", "C2", "U03", "C007"})
		s.Modules[0].Placements[3].Designator = wrong
		if p, err := planSchComposition(s); err == nil || p != nil {
			t.Fatalf("rewritten %q produced a plan from canonical U03: %v", wrong, err)
		}
	}
	s := composeOriginalRefsFixture([]string{"C007"})
	s.Modules[0].Placements[0].Designator = "C7"
	if _, err := planSchComposition(s); err == nil {
		t.Fatal("removed leading zero was accepted")
	}
}

func TestComposeRejectsLegacyFunctionalDesignatorsBeforeMutationQueue(t *testing.T) {
	for _, legacy := range []string{"U_RF", "J_PROG_RF", "uRf_01"} {
		t.Run(legacy, func(t *testing.T) {
			s := composeOriginalRefsFixture([]string{"U1", legacy})
			p, err := planSchComposition(s)
			if err != nil {
				t.Fatalf("historical data must remain readable for repair: %v", err)
			}
			before, _ := json.Marshal(p)
			// Even perfectly matching historical geometry cannot authorize replay.
			// Designator preflight must run before the baseline is read or reset.
			if pb, err := schCompositionPlaybook(p, nil, true); err == nil || pb != nil || !strings.Contains(err.Error(), "nonstandard designator") || !strings.Contains(err.Error(), legacy) {
				t.Fatalf("historical reference produced a mutation queue: %v", err)
			}
			after, _ := json.Marshal(p)
			if string(before) != string(after) {
				t.Fatal("failed preflight altered the canonical source")
			}
		})
	}
}

func TestGroupStableRefsSurviveIdempotentReorderingAndEdits(t *testing.T) {
	refs := []string{"C2", "C1", "C10", "u04", "C007"}
	groups, g, _, err := groupsCreateWithProvenance(nil, "original-Lib", refs, "", "", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(groups)
	reordered := []string{"C007", "u04", "C10", "C1", "C2"}
	_, same, unchanged, err := groupsCreateWithProvenance(groups, "original-Lib", reordered, "", "", nil, true)
	if err != nil || !unchanged || same != g {
		t.Fatalf("same exact identities should no-op despite input order: %v", err)
	}
	after, _ := json.Marshal(groups)
	if string(before) != string(after) {
		t.Fatal("idempotent call rewrote original order/timestamp")
	}
	reordered[1] = "U04"
	if _, _, _, err := groupsCreateWithProvenance(groups, "original-Lib", reordered, "", "", nil, true); err == nil {
		t.Fatal("recased declaration silently treated as equivalent")
	}
	if _, _, err := groupsAddMembers(groups, g.ID, []string{"r09"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g.Members, append(append([]string{}, refs...), "r09")) {
		t.Fatal("append reordered or recased existing members")
	}
	if _, _, _, err := groupsRemoveMembers(groups, g.ID, []string{"U04"}); err != nil {
		t.Fatalf("legacy case-insensitive removal must still locate original spelling: %v", err)
	}
	if !reflect.DeepEqual(g.Members, []string{"C2", "C1", "C10", "C007", "r09"}) {
		t.Fatalf("removal changed surviving identities: %v", g.Members)
	}
	roles, err := parseGroupRolesFlag("CORE=u04,OUT=C007")
	if err != nil || roles["CORE"] != "u04" || roles["OUT"] != "C007" {
		t.Fatalf("role references were recased: %v %v", roles, err)
	}
}

func TestApplyRejectsEditorRenumberingBeforeWiring(t *testing.T) {
	p, live := composeApplyFixture(t, true)
	part := live["result"].(map[string]any)["components"].([]any)[1].(map[string]any)
	part["designator"] = "LIB_U1"
	if err := schCompositionExpectation(p, false).check(live["result"], nil); err == nil {
		t.Fatal("unchanged geometry/device with changed designator escaped pre-wire guard")
	}
}
