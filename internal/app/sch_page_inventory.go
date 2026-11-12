package app

import (
	"fmt"
	"strings"
)

// schPageListedInInventory reports whether pageUUID is present in a
// schematic.pages.list result. It fails closed: absence is only proven by a
// present, correctly typed `pages` array whose every entry is an object with a
// non-empty string uuid. A missing, null or malformed inventory is an error,
// never "not listed" — otherwise a broken readback would read as a successful
// delete.
func schPageListedInInventory(result map[string]any, pageUUID string) (name string, listed bool, err error) {
	raw, present := result["pages"]
	if !present {
		return "", false, fmt.Errorf("result has no pages field")
	}
	pages, ok := raw.([]any)
	if !ok {
		return "", false, fmt.Errorf("pages is %T, want an array", raw)
	}
	for i, entry := range pages {
		p, ok := entry.(map[string]any)
		if !ok {
			return "", false, fmt.Errorf("pages[%d] is %T, want an object", i, entry)
		}
		uuid, ok := p["uuid"].(string)
		if !ok || strings.TrimSpace(uuid) == "" {
			return "", false, fmt.Errorf("pages[%d] has no string uuid", i)
		}
		if uuid == pageUUID {
			listed = true
			name, _ = p["name"].(string)
		}
	}
	return name, listed, nil
}
