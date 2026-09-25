package protocol

import (
	"fmt"
)

// NativeNetLabelSupport describes a specific SDK compatibility boundary, not a
// general version gate. createNetLabel is a V4 API and hangs on V3.2.186.
// A V4 version permits an attempt; it never proves the mutation has landed.
func NativeNetLabelSupport(hostVersion string) error {
	// V4: native createNetLabel. V3 (measured 3.2.149, 2026-09-25):
	// createNetLabel returns undefined immediately (no hang); connector 0.2.6+
	// draws the label as the stub wire's visible Name attribute, older
	// connectors fail cleanly. Only an unidentified host is refused.
	switch ParseHostProfile(hostVersion).Line {
	case "v3", "v4":
		return nil
	}
	return fmt.Errorf("net_label requires an identified EasyEDA Pro V3/V4 product version (reported %q). No write was dispatched", hostVersion)
}

func UsesNativeNetLabel(action string, payload map[string]any) bool {
	kind, _ := payload["kind"].(string)
	return kind == "net_label" && (action == "schematic.netflag.create" || action == "schematic.power.connect_pin")
}
