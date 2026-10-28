package app

// Stand-in fastroute / tracemaker for the router tests. The test binary
// itself is the fake (TestMain dispatches on PCBPILOT_FAKE_ROUTER), so the
// tests run the real exec path on every OS — a #!/bin/sh script cannot be
// started on Windows.

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	envFakeRouter    = "PCBPILOT_FAKE_ROUTER" // fastroute | tracemaker
	envFakeCalls     = "PCBPILOT_FAKE_CALLS"  // fastroute: file that gets one "call" line per run
	envFakeReport    = "PCBPILOT_FAKE_REPORT" // fastroute: report JSON to write
	envFakeTMBehaves = "PCBPILOT_FAKE_TM"     // tracemaker: ok | incomplete | crash | norep | sleep
)

// fakeRouterBin makes this test binary act as the named router for the rest
// of the test and returns the path to run it by.
func fakeRouterBin(t *testing.T, router string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(envFakeRouter, router)
	return exe
}

// argAfter returns the value following flag in args ("" if absent).
func argAfter(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func fakeRouterMain(router string, args []string) int {
	switch router {
	case "fastroute":
		if calls := os.Getenv(envFakeCalls); calls != "" {
			f, err := os.OpenFile(calls, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return 2
			}
			fmt.Fprintln(f, "call")
			f.Close()
		}
		rep := ""
		for _, a := range args {
			if r, ok := strings.CutPrefix(a, "--report="); ok {
				rep = r
			}
		}
		if os.WriteFile(argAfter(args, "-do"), []byte("(session x)\n"), 0o644) != nil ||
			os.WriteFile(rep, []byte(os.Getenv(envFakeReport)+"\n"), 0o644) != nil {
			return 2
		}
		return 0
	case "tracemaker":
		if len(args) > 0 && args[0] == "version" {
			fmt.Println("tracemaker 9.9.9")
			return 0
		}
		out, js := argAfter(args, "-o"), argAfter(args, "--json")
		switch os.Getenv(envFakeTMBehaves) {
		case "ok":
			if _, err := os.Stat(argAfter(args, "--constraints")); err != nil {
				return 3
			}
			os.WriteFile(out, []byte("routed\n"), 0o644)
			os.WriteFile(js, []byte(`{"connections":5,"routed":4,"seconds":1.5,"unrouted":[{"net":"N","from":"U1.1","to":"U2.2","blocked":false,"reason":"x"}]}`), 0o644)
			return 0
		case "incomplete": // exit 3 = finished with unrouted connections
			os.WriteFile(out, []byte("routed\n"), 0o644)
			os.WriteFile(js, []byte(`{"connections":5,"routed":3,"seconds":2,"unrouted":[{"net":"N","from":"U1.1","to":"U2.2","blocked":false},{"net":"M","from":"U1.2","to":"U2.3","blocked":false}]}`), 0o644)
			return 3
		case "crash":
			return 1
		case "norep":
			os.WriteFile(out, []byte("x\n"), 0o644)
			return 0
		case "sleep":
			time.Sleep(30 * time.Second)
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "fake router: unknown %s / %s\n", router, os.Getenv(envFakeTMBehaves))
	return 2
}
