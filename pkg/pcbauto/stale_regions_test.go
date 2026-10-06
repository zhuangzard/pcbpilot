package pcbauto

import "testing"

// Regions recorded on Gas Module V5 B before the v8 run (board copied from a
// 150×110 board, new outline 100×80 = 3937×3150 mil).
func TestStaleBoardRegions(t *testing.T) {
	raw := []byte(`{"copper":{"regions":[
 {"primitiveId":"ring-old-inside","layer":12,"ruleType":[2,5,7,8],"bbox":{"minX":29,"minY":29,"maxX":286,"maxY":286}},
 {"primitiveId":"ring-old-out","layer":12,"ruleType":[2,5,7,8],"bbox":{"minX":5620,"minY":29,"maxX":5876,"maxY":286}},
 {"primitiveId":"band-old-left","layer":12,"ruleType":[8],"bbox":{"minX":0,"minY":13,"maxX":54,"maxY":4308}},
 {"primitiveId":"band-old-top","layer":12,"ruleType":[8],"bbox":{"minX":23,"minY":4277,"maxX":5892,"maxY":4331}},
 {"primitiveId":"user-keepout","layer":12,"ruleType":[5],"bbox":{"minX":1000,"minY":1000,"maxX":1200,"maxY":1100}},
 {"primitiveId":"top-only","layer":1,"ruleType":[5],"bbox":{"minX":9000,"minY":9000,"maxX":9100,"maxY":9100}}]}}`)
	outline := Rect{0, 0, 3937, 3150}.Corners()
	added := []*Keepout{{Poly: Rect{29.5, 29.5, 285.5, 285.5}.Corners()}}
	got, err := StaleBoardRegions(raw, outline, added)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"ring-old-inside": true, "ring-old-out": true, "band-old-left": true, "band-old-top": true}
	if len(got) != len(want) {
		t.Fatalf("stale = %v", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("unexpected stale id %s in %v", id, got)
		}
	}
}
