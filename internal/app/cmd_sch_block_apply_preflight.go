package app

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Device preflight: prove every device the plan needs resolves in THIS site's
// library before the first component lands on the canvas.
//
// standard-parts.json ships deviceUuids resolved against one EasyEDA site. On a
// different site (lceda.cn vs easyeda.com) those uuids resolve to nothing, and
// block-apply used to discover that one placement at a time: the first place
// failed, the run entered the adoption/rollback path, and the report named a
// single role even though every part in the block was equally unresolvable. The
// remedy re-resolves the whole library, so naming one role at a time turns one
// fix into N runs.
//
// library.device.get is a read, so this also runs under --dry-run, where the
// dispatch purity guard only rejects mutations.

// bapDeviceRef is one distinct device the plan will place, plus the part keys
// that asked for it — a failure must name the part a human can fix, not a uuid.
type bapDeviceRef struct {
	LibraryUUID string
	DeviceUUID  string
	LCSC        string
	PartKeys    []string
}

// bapUnresolvedDevice is a device the library would not hand back, with the
// connector's own words for why.
type bapUnresolvedDevice struct {
	bapDeviceRef
	Reason string
}

// bapPlanDevices collapses the placements to distinct devices. A block repeats a
// device across roles (two 5.1k CC resistors are one device), so this is what
// keeps the sweep at one round-trip per device rather than per placement.
func bapPlanDevices(placements []bapPlacement) []bapDeviceRef {
	index := map[string]*bapDeviceRef{}
	var order []string
	for _, p := range placements {
		if strings.TrimSpace(p.DeviceUUID) == "" {
			continue // planBlockApply already refuses a part with no deviceUuid
		}
		key := p.LibraryUUID + "|" + p.DeviceUUID
		ref, seen := index[key]
		if !seen {
			ref = &bapDeviceRef{LibraryUUID: p.LibraryUUID, DeviceUUID: p.DeviceUUID, LCSC: p.LCSC}
			index[key] = ref
			order = append(order, key)
		}
		if p.PartKey != "" && !containsString(ref.PartKeys, p.PartKey) {
			ref.PartKeys = append(ref.PartKeys, p.PartKey)
		}
		if ref.LCSC == "" {
			ref.LCSC = p.LCSC
		}
	}
	out := make([]bapDeviceRef, 0, len(order))
	for _, key := range order {
		ref := index[key]
		sort.Strings(ref.PartKeys)
		out = append(out, *ref)
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// bapPreflightDevices asks the library for each distinct device and returns the
// ones it would not return. The reason is reported verbatim: this cannot tell
// "the uuid does not exist here" apart from "the read failed", and claiming the
// stronger one would send someone re-resolving a library that was fine.
func bapPreflightDevices(cfg *appConfig, window string, devices []bapDeviceRef) []bapUnresolvedDevice {
	var bad []bapUnresolvedDevice
	for _, d := range devices {
		payload := map[string]any{"uuid": d.DeviceUUID}
		if d.LibraryUUID != "" {
			payload["libraryUuid"] = d.LibraryUUID
		}
		if _, err := requestActionTimed(cfg, "library.device.get", window, payload, placeTimeout); err != nil {
			bad = append(bad, bapUnresolvedDevice{bapDeviceRef: d, Reason: err.Error()})
		}
	}
	return bad
}

// bapUnresolvedDevicesError is the refusal. It lists every unresolvable device
// at once and states the canvas is untouched, because the whole point of moving
// this check ahead of the placement loop is that there is nothing to roll back.
func bapUnresolvedDevicesError(bad []bapUnresolvedDevice, total int, partsPath string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "device preflight: %d of %d device(s) could not be resolved in this site's library — nothing was placed\n", len(bad), total)
	for _, d := range bad {
		lcsc := d.LCSC
		if lcsc == "" {
			lcsc = "(no LCSC in the parts file)"
		}
		fmt.Fprintf(&b, "  %-24s %s  uuid=%s\n      %s\n", strings.Join(d.PartKeys, ", "), lcsc, d.DeviceUUID, d.Reason)
	}
	if len(bad) == total {
		b.WriteString("\nEVERY device failed, which is the signature of a parts file resolved against a\n" +
			"different EasyEDA site (lceda.cn vs easyeda.com): the uuids are real, just not here.\n")
	}
	// The remedy must be a command that exists: lib by-lcsc takes --lcsc and
	// nothing else, so hand over the C-numbers already collected here instead of
	// inventing a file-rewriting flag. A device with no C-number cannot be looked
	// up that way and is called out rather than silently dropped.
	var numbers, noLCSC []string
	for _, d := range bad {
		if d.LCSC != "" {
			if !containsString(numbers, d.LCSC) {
				numbers = append(numbers, d.LCSC)
			}
			continue
		}
		noLCSC = append(noLCSC, strings.Join(d.PartKeys, ", "))
	}
	if len(numbers) > 0 {
		fmt.Fprintf(&b, "\nResolve these C-numbers against the site you are on and write the returned\n"+
			"libraryUuid/uuid back into %s:\n  pcbpilot lib by-lcsc --lcsc %s\n",
			bapPartsFileLabel(partsPath), strings.Join(numbers, ","))
	}
	if len(noLCSC) > 0 {
		fmt.Fprintf(&b, "\nNo LCSC number in the parts file for: %s - look those up with `pcbpilot lib search`\n"+
			"and fill in both the LCSC number and the new uuid.\n", strings.Join(noLCSC, ", "))
	}
	fmt.Fprintf(&b, "\nThen: pcbpilot sch block-apply <block> --parts %s\n", bapPartsFileLabel(partsPath))
	return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
}

// bapPartsFileLabel keeps the refusal readable when --parts was auto-detected.
func bapPartsFileLabel(partsPath string) string {
	if p := strings.TrimSpace(partsPath); p != "" {
		return p
	}
	return "standard-parts.json"
}

// bapReportPreflightOK is the quiet confirmation. Without it a passing sweep is
// indistinguishable from one that never ran, and "it was skipped" is exactly
// what a reader assumes when a guard produces no output.
func bapReportPreflightOK(stderr io.Writer, total int) {
	fmt.Fprintf(stderr, "device preflight: %d/%d device(s) resolved\n", total, total)
}
