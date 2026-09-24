package protocol

import (
	"fmt"
)

// NativeNetLabelSupport describes a specific SDK compatibility boundary, not a
// general version gate. createNetLabel is a V4 API and hangs on V3.2.186.
// A V4 version permits an attempt; it never proves the mutation has landed.
func NativeNetLabelSupport(hostVersion string) error {
	if ParseHostProfile(hostVersion).Has("nativeNetLabel") {
		return nil
	}
	return fmt.Errorf("native net_label requires a confirmed EasyEDA Pro V4+ product version (reported %q); V3 createNetLabel can hang. No write was dispatched. Upgrade the host, or explicitly choose an electrically equivalent netport/netflag", hostVersion)
}

func UsesNativeNetLabel(action string, payload map[string]any) bool {
	kind, _ := payload["kind"].(string)
	return kind == "net_label" && (action == "schematic.netflag.create" || action == "schematic.power.connect_pin")
}
