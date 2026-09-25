package pcbauto

import "testing"

func TestMechFromJournalAndReplaceSteps(t *testing.T) {
	journal := `{"playbookSha256":"x","name":"prev"}
{"idx":2,"id":"hole-1","status":"ok","ms":3,"captured":{"MECH_FILL_HOLE_1":"f1"}}
{"idx":3,"id":"hole-keep-1","status":"ok","ms":3,"captured":{"MECH_REGION_HOLE_KEEP_1":"r1"}}
{"idx":4,"id":"keepout-1","status":"fail","ms":3,"captured":{"MECH_REGION_KEEPOUT_1":"r2"}}
{"idx":5,"id":"place-U1","status":"ok","ms":3}`
	rp := MechFromJournal([]byte(journal))
	if len(rp.Fills) != 1 || rp.Fills[0] != "f1" || len(rp.Regions) != 1 || rp.Regions[0] != "r1" {
		t.Fatalf("replace = %+v, want fills [f1] regions [r1] (failed steps created nothing)", rp)
	}
	b := &Board{Holes: []*Hole{{Name: "MH1", C: Point{100, 100}, Dia: 126, Keep: 64}}}
	pb := BuildPlaybook(PlaybookInput{Board: b, Result: &Result{}, NewHoles: b.Holes, Replace: rp})
	if pb.Steps[0].Action != "pcb.fill.delete" || pb.Steps[1].Action != "pcb.region.delete" {
		t.Fatalf("replace steps must come first: %+v", pb.Steps[:2])
	}
	var fill, keep *Step
	for i := range pb.Steps {
		switch pb.Steps[i].ID {
		case "hole-1":
			fill = &pb.Steps[i]
		case "hole-keep-1":
			keep = &pb.Steps[i]
		}
	}
	if fill == nil || fill.Capture["MECH_FILL_HOLE_1"] != "$.primitiveId" || keep == nil || keep.Capture["MECH_REGION_HOLE_KEEP_1"] != "$.primitiveId" {
		t.Fatalf("mechanics must capture their primitiveId: %+v %+v", fill, keep)
	}
}
