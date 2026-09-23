package app

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWindowsListenerPID(t *testing.T) {
	base := " TCP  0.0.0.0:61832  0.0.0.0:0 LISTENING 456\r\n TCP [::]:61832 [::]:0 LISTENING 456\n TCP 127.0.0.1:61832 127.0.0.1:50000 ESTABLISHED 999\n TCP 0.0.0.0:161832 0.0.0.0:0 LISTENING 777"
	if n := windowsListenerPID(base, 61832); n != 456 {
		t.Fatalf("got %d", n)
	}
	if n := windowsListenerPID(base+"\n TCP 127.0.0.1:61832 0.0.0.0:0 LISTENING 789", 61832); n != 0 {
		t.Fatal("ambiguous owners accepted")
	}
	if windowsListenerPID(base, 60833) != 0 {
		t.Fatal("wrong port matched")
	}
}

func TestDaemonLifecycleRefusesRemoteHostAndInvalidPID(t *testing.T) {
	for _, host := range []string{"192.0.2.1", "example.com", "0.0.0.0", "::"} {
		if stopLocalDaemon(host, 61832, io.Discard) == nil {
			t.Fatal("remote/wildcard host accepted", host)
		}
	}
	for _, pid := range []int{0, -1, os.Getpid()} {
		if termPID(pid) == nil {
			t.Fatal("invalid PID accepted", pid)
		}
	}
}

// Child listener is disposable and independent of the developer's real daemon.
func TestDaemonLifecycleHelper(t *testing.T) {
	port := os.Getenv("EASYEDA_TEST_LIFECYCLE_PORT")
	if port == "" {
		return
	}
	mode := os.Getenv("EASYEDA_TEST_LIFECYCLE_MODE")
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "foreign":
			fmt.Fprint(w, `{"service":"other"}`)
		case "upstream":
			// The upstream easyeda-agent daemon runs side by side on its own
			// ports; pcbpilot must never stop it, even with a live PID.
			fmt.Fprintf(w, `{"service":"easyeda-agent","pid":%d}`, os.Getpid())
		case "unknown":
			fmt.Fprint(w, `{"service":"pcbpilot","pid":-1}`)
		case "old":
			fmt.Fprint(w, `{"service":"pcbpilot"}`)
		default:
			fmt.Fprintf(w, `{"service":"pcbpilot","pid":%d}`, os.Getpid())
		}
	})
	if err := http.ListenAndServe("127.0.0.1:"+port, nil); err != nil {
		os.Exit(2)
	}
}

func TestDaemonLifecycleStopsOnlyIdentifiedProcess(t *testing.T) {
	for _, mode := range []string{"current", "old", "foreign", "upstream", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			port := freeTCPPort(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestDaemonLifecycleHelper$")
			cmd.Env = append(os.Environ(), "EASYEDA_TEST_LIFECYCLE_PORT="+strconv.Itoa(port), "EASYEDA_TEST_LIFECYCLE_MODE="+mode)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("child failed to stop")
				}
			})
			deadline := time.Now().Add(5 * time.Second)
			for {
				conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
				if err == nil {
					conn.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child did not listen", err)
				}
				time.Sleep(20 * time.Millisecond)
			}
			if mode == "old" && listenerPID(port) == 0 {
				t.Skip("OS port owner lookup unavailable for legacy daemon")
			}
			err := stopLocalDaemon("127.0.0.1", port, io.Discard)
			wantStop := mode == "current" || mode == "old"
			if (err == nil) != wantStop || portFree("127.0.0.1", port) != wantStop {
				t.Fatalf("mode %s err=%v", mode, err)
			}
			if !wantStop && !strings.Contains(err.Error(), "no process was terminated") {
				t.Fatal(err)
			}
			if wantStop && stopLocalDaemon("127.0.0.1", port, io.Discard) != nil {
				t.Fatal("stop should be idempotent")
			}
		})
	}
}

func TestDaemonRestartExposesStartFlags(t *testing.T) {
	c := newDaemonRestartCmd(&appConfig{}, io.Discard, io.Discard)
	for _, name := range []string{"auto-update-skill", "autosave-debounce"} {
		if c.Flags().Lookup(name) == nil {
			t.Fatal("missing start flag", name)
		}
	}
}
