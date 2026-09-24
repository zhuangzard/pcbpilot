package protocol

import "testing"

func TestParseHostProfileLinesAndFeatures(t *testing.T) {
	cases := []struct {
		version, line string
		on, off       []string
	}{
		{"3.2.149.88089769", "v3", []string{}, []string{"nativeNetLabel", "pinLevelAttributes", "unsetStyleAsNull", "editorVersionApi"}},
		{"3.2.203", "v3", []string{"editorVersionApi", "schematicImageExport"}, []string{"nativeNetLabel"}},
		{"4.1.60", "v4", []string{"nativeNetLabel", "pinLevelAttributes", "multiVariantDevices", "unsetStyleAsNull", "editorVersionApi", "schematicImageExport"}, nil},
		{"4.1.10", "v4", []string{"nativeNetLabel"}, []string{"editorVersionApi", "schematicImageExport"}},
		{"5.0.0", "unknown", nil, []string{"nativeNetLabel"}},
		{"", "unknown", nil, []string{"nativeNetLabel", "editorVersionApi"}},
	}
	for _, c := range cases {
		p := ParseHostProfile(c.version)
		if p.Line != c.line {
			t.Fatalf("%q line %s want %s", c.version, p.Line, c.line)
		}
		for _, f := range c.on {
			if !p.Has(f) {
				t.Fatalf("%q should have %s", c.version, f)
			}
		}
		for _, f := range c.off {
			if p.Has(f) {
				t.Fatalf("%q must not have %s", c.version, f)
			}
		}
	}
	if NativeNetLabelSupport("3.2.149") == nil || NativeNetLabelSupport("4.1.60") != nil {
		t.Fatal("net_label gate must follow the profile")
	}
}
