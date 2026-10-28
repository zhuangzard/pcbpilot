package app

// Stand-in fastroute / tracemaker for the router tests. The test binary
// itself is the fake (TestMain dispatches on PCBPILOT_FAKE_ROUTER), so the
// tests run the real exec path on every OS — a #!/bin/sh script cannot be
// started on Windows.

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"testing"
	"time"
)

const (
	envFakeRouter    = "PCBPILOT_FAKE_ROUTER" // fastroute | tracemaker
	envFakeCalls     = "PCBPILOT_FAKE_CALLS"  // fastroute: file that gets one "call" line per run
	envFakeReport    = "PCBPILOT_FAKE_REPORT" // fastroute: report JSON to write
	envFakeFRMode    = "PCBPILOT_FAKE_FR"     // fastroute: "" | wait-stop | ignore-stop | crash-parallel | crash
	envFakeFRReady   = "PCBPILOT_FAKE_READY"  // fastroute wait-stop / ignore-stop: file written once the stop handler is in place
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
		rep, single := "", false
		for _, a := range args {
			if r, ok := strings.CutPrefix(a, "--report="); ok {
				rep = r
			}
			single = single || a == "--multi-start=1"
		}
		report := os.Getenv(envFakeReport)
		if report == "" {
			report = `{"stats":{"unrouted":0,"violations":0}}`
		}
		switch os.Getenv(envFakeFRMode) {
		case "crash": // dies before writing anything
			return 101
		case "crash-parallel": // fastroute 0.1.7: a checkpoint session, then a panic unless single-threaded
			os.WriteFile(argAfter(args, "-do"), []byte("(session x)\n"), 0o644)
			if !single {
				fmt.Fprintln(os.Stderr, "thread panicked: free list corrupted")
				return 101
			}
		case "wait-stop": // routes until asked to stop (SIGINT / Ctrl+Break), then writes its best board
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt)
			fakeReady()
			select {
			case <-sig:
			case <-time.After(20 * time.Second):
				return 9 // never asked to stop gracefully
			}
		case "ignore-stop": // catches the stop request and keeps routing: must be killed
			signal.Notify(make(chan os.Signal, 1), os.Interrupt)
			fakeReady()
			time.Sleep(60 * time.Second)
			return 9
		}
		if os.WriteFile(argAfter(args, "-do"), []byte("(session x)\n"), 0o644) != nil ||
			os.WriteFile(rep, []byte(report+"\n"), 0o644) != nil {
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

// fakeReady tells the test (envFakeFRReady) that the stop handler is in place.
func fakeReady() {
	if p := os.Getenv(envFakeFRReady); p != "" {
		_ = os.WriteFile(p, []byte("ready\n"), 0o644)
	}
}
