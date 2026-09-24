package app

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Runs one zone of a real zones source through the planner with the annealer
// hooks attached (diagnostics; PCBPILOT_REALZONE=<file>:<zoneId>).
func TestDebugRealZone(t *testing.T) {
	spec := os.Getenv("PCBPILOT_REALZONE")
	if spec == "" {
		t.Skip()
	}
	var file, zone string
	for i := len(spec) - 1; i >= 0; i-- {
		if spec[i] == ':' {
			file, zone = spec[:i], spec[i+1:]
			break
		}
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var zs SchematicZonesInput
	if err := json.Unmarshal(raw, &zs); err != nil {
		t.Fatal(err)
	}
	var z *SchematicZone
	for i := range zs.Zones {
		if zs.Zones[i].ID == zone {
			z = &zs.Zones[i]
		}
	}
	in := SchematicLayoutInput{SchemaVersion: 1, CoreComponentID: z.CoreComponentID, NetPolicies: map[string]string{}, MaxCandidates: zs.MaxCandidates}
	member := map[string]bool{}
	for _, id := range z.ComponentIDs {
		member[id] = true
	}
	for _, c := range zs.Components {
		if member[c.ID] {
			in.Components = append(in.Components, c)
			for _, p := range c.Measurement.Pins {
				if p.Net != "" {
					in.NetPolicies[p.Net] = zs.NetPolicies[p.Net]
				}
			}
		}
	}
	for _, a := range zs.Attachments {
		if member[a.ComponentID] {
			in.Attachments = append(in.Attachments, a)
		}
	}
	annealAttemptHook = func(round, spent int, e error) { t.Logf("  anneal round %d attempt spent %d: %.300v", round, spent, e) }
	annealHook = func(e error) { t.Logf("  anneal result: %.300v", e) }
	defer func() { annealAttemptHook, annealHook = nil, nil }()
	start := time.Now()
	out, err := PlanSchematicLayout(in)
	if err != nil {
		t.Logf("zone %s FAILED in %.1fs: %.400v", zone, time.Since(start).Seconds(), err)
		return
	}
	t.Logf("zone %s ok in %.1fs: %s, %d wires, %d flags", zone, time.Since(start).Seconds(), out.Search.Strategy, len(out.Wires), len(out.Flags))
}
