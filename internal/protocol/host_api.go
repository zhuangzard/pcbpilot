package protocol

import (
	"fmt"
	"strconv"
	"strings"
)

// NativeNetLabelSupport describes a specific SDK compatibility boundary, not a
// general version gate. createNetLabel is a V4 API and hangs on V3.2.186.
// A V4 version permits an attempt; it never proves the mutation has landed.
func NativeNetLabelSupport(hostVersion string) error {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(hostVersion), "v"), ".")
	if len(parts) >= 3 {
		major, err := strconv.Atoi(parts[0])
		_, minorErr := strconv.ParseUint(parts[1], 10, 32)
		_, patchErr := strconv.ParseUint(parts[2], 10, 32)
		if err == nil && minorErr == nil && patchErr == nil && major >= 4 {
			return nil
		}
	}
	return fmt.Errorf("native net_label requires a confirmed EasyEDA Pro V4+ product version (reported %q); V3 createNetLabel can hang. No write was dispatched. Upgrade the host, or explicitly choose an electrically equivalent netport/netflag", hostVersion)
}

func UsesNativeNetLabel(action string, payload map[string]any) bool {
	kind, _ := payload["kind"].(string)
	return kind == "net_label" && (action == "schematic.netflag.create" || action == "schematic.power.connect_pin")
}
