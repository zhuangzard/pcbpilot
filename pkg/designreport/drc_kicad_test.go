package designreport

import "testing"

// A KiCad DRC result (flat: rule + objType per violation) is counted and
// named as KiCad's; it used to read "0 违规" under an EasyEDA label.
func TestParseDRCKiCadFlat(t *testing.T) {
	d, err := ParseDRC([]byte(`{"passed":false,"total":2,"tool":"KiCad","violations":[
		{"rule":"hole_clearance","objType":"Pad 1 to NPTH pad"},
		{"rule":"copper_edge_clearance","objType":"Segment to PTH pad 2"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.Tool != "KiCad" || d.Violations != 2 || len(d.Kinds) != 2 || d.Kinds[0] != "copper_edge_clearance" {
		t.Fatalf("%+v", d)
	}
	// the EasyEDA tree form is unchanged
	e, err := ParseDRC([]byte(`{"passed":false,"violations":[{"count":1,"list":[{"errorType":"Clearance","errorObjType":"Track to Pad"}]}]}`))
	if err != nil || e.Tool != "" || e.Violations != 1 || e.Kinds[0] != "Track to Pad Clearance" {
		t.Fatalf("%+v %v", e, err)
	}
}
