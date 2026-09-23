package protocol

import "testing"

func TestNativeNetLabelCompatibility(t *testing.T) {
	for version, supported := range map[string]bool{"3.2.186": false, "3.2.149.88089769": false, "": false, "dev": false, "4.bad.1": false, "4.0.0": true, "v4.1.60": true, "4.1.60.123": true} {
		if (NativeNetLabelSupport(version) == nil) != supported {
			t.Fatalf("version %q support mismatch", version)
		}
	}
	for _, action := range []string{"schematic.netflag.create", "schematic.power.connect_pin"} {
		if !UsesNativeNetLabel(action, map[string]any{"kind": "net_label"}) || UsesNativeNetLabel(action, map[string]any{"kind": "net_port_bi"}) {
			t.Fatal("capability incorrectly scoped")
		}
	}
}
