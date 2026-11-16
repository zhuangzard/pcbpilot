package kicad

import "testing"

// A hierarchical label and a local label on neighbouring 2.54 mm rows do not
// overlap; on the same row they do.
func TestLabelCheckBoxNeighbouringRows(t *testing.T) {
	for _, ang := range []float64{0, 180} {
		hier := SceneLabel{Kind: LabelHier, Name: "SIG_ACROSS", At: Pt{50.8, 50.8}, Angle: ang}
		local := SceneLabel{Kind: LabelLocal, Name: "SIG_LOCAL_N", At: Pt{50.8, 53.34}, Angle: ang}
		if overlaps(labelCheckBox(hier), labelCheckBox(local)) {
			t.Errorf("angle %v: labels one row apart overlap", ang)
		}
		other := SceneLabel{Kind: LabelHier, Name: "SIG_NEXT", At: Pt{50.8, 53.34}, Angle: ang}
		if overlaps(labelCheckBox(hier), labelCheckBox(other)) {
			t.Errorf("angle %v: hierarchical labels one row apart overlap", ang)
		}
		other.At.Y = 51.5
		if !overlaps(labelCheckBox(hier), labelCheckBox(other)) {
			t.Errorf("angle %v: hierarchical labels 0.7 mm apart do not overlap", ang)
		}
		local.At.Y = 50.8
		if !overlaps(labelCheckBox(hier), labelCheckBox(local)) {
			t.Errorf("angle %v: labels on one row do not overlap", ang)
		}
	}
	for _, ang := range []float64{90, 270} {
		a := SceneLabel{Kind: LabelHier, Name: "SIG_ACROSS", At: Pt{50.8, 50.8}, Angle: ang}
		b := SceneLabel{Kind: LabelHier, Name: "SIG_NEXT", At: Pt{53.34, 50.8}, Angle: ang}
		if overlaps(labelCheckBox(a), labelCheckBox(b)) {
			t.Errorf("angle %v: hierarchical labels one column apart overlap", ang)
		}
		b.At.X = 51.5
		if !overlaps(labelCheckBox(a), labelCheckBox(b)) {
			t.Errorf("angle %v: hierarchical labels 0.7 mm apart do not overlap", ang)
		}
	}
}
