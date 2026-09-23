package pcbauto

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Synthetic BGA board: which connections fail with and without escapes.
func TestDebugEscapeSynthetic(t *testing.T) {
	if os.Getenv("PCBAUTO_ESCSYN") == "" {
		t.Skip()
	}
	defer func() { bgaSkipEscape = false }()
	for _, skip := range []bool{true, false} {
		bgaSkipEscape = skip
		b := bgaBoard()
		out, err := Run(context.Background(), b, Options{Stack: StackOptions{Force: 6}, NoEscalate: true,
			Route: RouteOptions{Timeout: 60 * time.Second, BGA: true}})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("skipEscape=%v completion %.1f%%", skip, out.Route.Stats.Completion)
		for _, nt := range out.Route.Notes {
			if strings.Contains(nt, "BGA") {
				t.Log("   ", nt)
			}
		}
		for _, u := range out.Route.Unrouted {
			if u.Reason == "no-legal-path" || strings.Contains(u.Reason, "drc") {
				t.Logf("   %s %s %v", u.Reason, u.Net, u.Pads)
			}
		}
	}
}
