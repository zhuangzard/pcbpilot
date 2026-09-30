package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// /health (and every list) prunes stale connector sessions inline. Closing a
// websocket waits for the peer's close frame (up to ~5 s + 5 s in
// coder/websocket); a wedged connector that stopped sending frames — exactly
// what makes a session stale — never answers, so the close must not run on
// the /health path.
func TestPruneStaleDoesNotWaitForTheCloseHandshake(t *testing.T) {
	serverSide := make(chan *websocket.Conn, 1)
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		serverSide <- ws
		<-hold
	}))
	defer srv.Close()
	defer close(hold)
	// The client dials and never reads: it will not answer a close frame.
	client, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	ws := <-serverSide

	c := newConn(ws, time.Now().UTC())
	c.windowID = "wedged"
	c.lastSeen = time.Now().UTC().Add(-connectorTTL - time.Second)
	h := newHub()
	h.windows["wedged"] = c
	start := time.Now()
	if n := h.pruneStale(time.Now().UTC()); n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	if d := time.Since(start); d >= time.Second {
		t.Fatalf("pruneStale blocked %s on the close handshake", d)
	}
	select {
	case <-c.done:
	default:
		t.Fatal("pending requests must still be failed synchronously")
	}
}
