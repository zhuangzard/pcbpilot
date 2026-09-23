package app

import (
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/workflow"
)

// TestSplitPowerNotPoured is the #114 gate criterion, table-driven: a power net
// power-planes recorded as routed-as-tracks must NOT block the post_route_checked
// gate; every other power-not-poured finding still must.
func TestSplitPowerNotPoured(t *testing.T) {
	findings := []pcbCheckFinding{
		{Type: "power-not-poured", Level: "WARN", Net: "VDD_SPI", Message: "power net VDD_SPI (2 pads) has no copper pour"},
		{Type: "power-not-poured", Level: "WARN", Net: "5V", Message: "power net 5V (4 pads) has no copper pour"},
		{Type: "width-under-spec", Level: "WARN", Net: "3V3", Message: "track under spec"},
		{Type: "clearance", Level: "ERROR", Net: "GND", Message: "copper too close"},
	}

	cases := []struct {
		name         string
		tracksNets   []string
		wantBlocking []string
		wantExempt   []string
	}{
		{
			name:         "no state — everything blocks (pre-#114 behavior)",
			tracksNets:   nil,
			wantBlocking: []string{"VDD_SPI", "5V"},
		},
		{
			name:         "recorded net is exempt, the other still blocks",
			tracksNets:   []string{"VDD_SPI"},
			wantBlocking: []string{"5V"},
			wantExempt:   []string{"VDD_SPI"},
		},
		{
			name:       "both recorded — gate has nothing left to block on",
			tracksNets: []string{"VDD_SPI", "5V"},
			wantExempt: []string{"VDD_SPI", "5V"},
		},
		{
			name:         "net names match case-insensitively",
			tracksNets:   []string{"vdd_spi"},
			wantBlocking: []string{"5V"},
			wantExempt:   []string{"VDD_SPI"},
		},
		{
			name:         "an unrelated recorded net exempts nothing",
			tracksNets:   []string{"VBUS"},
			wantBlocking: []string{"VDD_SPI", "5V"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &workflow.State{Project: "ceshi"}
			st.SetPowerTracksNets(tc.tracksNets)
			blocking, exempt := splitPowerNotPoured(findings, st.IsPowerTracksNet)
			if got := nets(blocking); !equal(got, tc.wantBlocking) {
				t.Errorf("blocking nets = %v, want %v", got, tc.wantBlocking)
			}
			if got := nets(exempt); !equal(got, tc.wantExempt) {
				t.Errorf("exempt nets = %v, want %v", got, tc.wantExempt)
			}
			// Non power-not-poured findings are never claimed by the split.
			if len(blocking)+len(exempt) != 2 {
				t.Errorf("split touched non power-not-poured findings: %d+%d", len(blocking), len(exempt))
			}
		})
	}
}

// TestSplitPowerNotPouredPlaneAndInfo is #117: a net recorded as poured-into-
// PLANE by power-planes must not block (pour.list cannot see it after reload,
// #110), and an INFO-level finding (the check's own blind-plane downgrade) never
// blocks even with no state at all.
func TestSplitPowerNotPouredPlaneAndInfo(t *testing.T) {
	findings := []pcbCheckFinding{
		{Type: "power-not-poured", Level: "WARN", Net: "GND", Message: "no visible pour"},
		{Type: "power-not-poured", Level: "WARN", Net: "5V", Message: "no copper pour"},
	}
	st := &workflow.State{Project: "ceshi"}
	st.SetPlanePouredNets([]string{"GND"})
	isExempt := func(net string) bool { return st.IsPowerTracksNet(net) || st.IsPlanePouredNet(net) }
	blocking, exempt := splitPowerNotPoured(findings, isExempt)
	if got := nets(blocking); !equal(got, []string{"5V"}) {
		t.Errorf("blocking = %v, want [5V]", got)
	}
	if got := nets(exempt); !equal(got, []string{"GND"}) {
		t.Errorf("exempt = %v, want [GND]", got)
	}

	// INFO level is exempt even with a nil membership test.
	blocking, exempt = splitPowerNotPoured([]pcbCheckFinding{
		{Type: "power-not-poured", Level: "INFO", Net: "GND"},
	}, nil)
	if len(blocking) != 0 || len(exempt) != 1 {
		t.Fatalf("INFO finding: blocking=%d exempt=%d, want 0/1", len(blocking), len(exempt))
	}
}

// TestSplitPowerNotPouredNilTest: a nil membership test (no state at all) must
// keep the gate exactly as strict as before.
func TestSplitPowerNotPouredNilTest(t *testing.T) {
	blocking, exempt := splitPowerNotPoured([]pcbCheckFinding{
		{Type: "power-not-poured", Net: "VDD_SPI"},
	}, nil)
	if len(blocking) != 1 || len(exempt) != 0 {
		t.Fatalf("nil exempt test: blocking=%d exempt=%d, want 1/0", len(blocking), len(exempt))
	}
}

func nets(fs []pcbCheckFinding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Net)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
