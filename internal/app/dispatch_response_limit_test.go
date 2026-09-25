package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A native project archive is returned as base64 inside one action response;
// 1.2 MiB used to be cut at 1 MiB and fail as "unexpected end of JSON input".
func TestRequestActionLargeResponse(t *testing.T) {
	blob := strings.Repeat("A", 3<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			fmt.Fprint(w, `{"service":"pcbpilot","windows":[{"windowId":"w","easyedaVersion":"3.2.149"}]}`)
			return
		}
		fmt.Fprintf(w, `{"id":"r","type":"response","version":"v1","ok":true,"result":{"base64":%q}}`, blob)
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	cfg := &appConfig{host: host, ports: port + "-" + port}
	res, err := requestActionTimed(cfg, "project.export", "w", map[string]any{}, 10*time.Second)
	if err != nil {
		t.Fatalf("large response: %v", err)
	}
	if got := len(fmt.Sprint(res.Result["base64"])); got != len(blob) {
		t.Fatalf("base64 length %d, want %d", got, len(blob))
	}
}
