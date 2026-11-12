package daemon

import (
	"testing"

	"github.com/coder/websocket"
)

// schematic.geometry.read has a connector handler; the daemon must know the
// action, treat it as a read, and forward it unchanged.
func TestGeometryReadIsDispatchedToConnector(t *testing.T) {
	const action = "schematic.geometry.read"
	if !knownActions[action] {
		t.Fatalf("%s is not in the daemon action set", action)
	}
	if mutatesAction[action] {
		t.Fatalf("%s must not be classified as a mutation", action)
	}

	base, cleanup := startDaemon(t)
	defer cleanup()

	// Without a connector the request reaches routing (NO_CONNECTOR) instead
	// of being refused as an unknown action.
	resp := postAction(t, base, `{"action":"`+action+`"}`)
	if resp.OK || resp.Error == nil || resp.Error.Code != "NO_CONNECTOR" {
		t.Fatalf("without a connector want NO_CONNECTOR, got ok=%v err=%+v", resp.OK, resp.Error)
	}

	c := dialConnector(t, base, "win-geo")
	defer c.Close(websocket.StatusNormalClosure, "")
	go echoRequests(t.Context(), c)
	waitForWindow(t, base, "win-geo")

	resp = postAction(t, base, `{"action":"`+action+`","windowId":"win-geo"}`)
	if !resp.OK {
		t.Fatalf("expected the read to be forwarded, got error: %+v", resp.Error)
	}
	if got, _ := resp.Result["echo"].(string); got != action {
		t.Fatalf("connector received %q, want %q", got, action)
	}
}
