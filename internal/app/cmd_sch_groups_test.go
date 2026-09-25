package app

import (
	"reflect"
	"testing"
)

func TestBuildSchGroups(t *testing.T) {
	c := func(d string, x, y float64) schGroupComp {
		return schGroupComp{Designator: d, Box: schGroupRect{x - 10, y - 10, x + 10, y + 10}}
	}
	pg := schGroupPage{
		Page: "p1",
		Rects: []schGroupRect{
			{0, 0, 1000, 800},    // sheet border: holds everything -> ignored
			{50, 50, 450, 400},   // BUCK
			{500, 50, 950, 700},  // MCU
			{520, 400, 700, 650}, // XTAL nested in MCU: inner wins
			{800, 720, 820, 740}, // decorative box, 0 parts
		},
		Texts: []schGroupText{{"BUCK 5V→3V3", 55, 405}, {"MCU", 505, 705}, {"XTAL", 525, 655}, {"note", 900, 100}},
		Comps: []schGroupComp{c("U1", 200, 200), c("L1", 300, 200), c("C1", 120, 300),
			c("U3", 700, 200), c("C5", 850, 300), c("Y1", 600, 500), c("C8", 650, 450), c("J9", 980, 780)},
	}
	got := buildSchGroups([]schGroupPage{pg}, 2)
	want := map[string][]string{"BUCK 5V→3V3": {"C1", "L1", "U1"}, "MCU": {"C5", "U3"}, "XTAL": {"C8", "Y1"}}
	if len(got.Groups) != len(want) {
		t.Fatalf("groups %+v", got.Groups)
	}
	for _, g := range got.Groups {
		if !reflect.DeepEqual(g.Members, want[g.ID]) {
			t.Errorf("%s = %v, want %v", g.ID, g.Members, want[g.ID])
		}
	}
	if !reflect.DeepEqual(got.Unframed, []string{"J9"}) {
		t.Errorf("unframed %v", got.Unframed)
	}
}
