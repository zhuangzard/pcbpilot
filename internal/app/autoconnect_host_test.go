package app

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// An unidentified pre-3 host is refused before any write; V3 and V4 label.
func TestAutoconnectRejectsUnsupportedLabelBeforeEntireBatch(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		var actions atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				fmt.Fprint(w, `{"service":"pcbpilot","windows":[{"windowId":"w","easyedaVersion":"2.2.40"}]}`)
			} else {
				actions.Add(1)
				http.Error(w, "unexpected action", 500)
			}
		}))
		hostPort := strings.TrimPrefix(srv.URL, "http://")
		host, port, _ := strings.Cut(hostPort, ":")
		cfg := &appConfig{host: host, ports: port + "-" + port}
		conns := []acConnSpec{{PinRef: "R1:1", Kind: "ground", Net: "GND"}, {PinRef: "R2:1", Kind: "net_label", Net: "SW"}}
		_, err := runAutoconnectOpts(cfg, "w", conns, autoconnectRules{}, acRunOpts{DryRun: dryRun}, io.Discard, io.Discard)
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "HOST_API_UNSUPPORTED") || actions.Load() != 0 {
			t.Fatalf("dry=%v actions=%d err=%v", dryRun, actions.Load(), err)
		}
	}
}
