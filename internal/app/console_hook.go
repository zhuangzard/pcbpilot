package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/console"
)

// Long offline commands (simulations, reports, intent, pcb auto, kb indexing)
// never touch the daemon, so the console could not see them. The CLI reports
// their start/end to POST /api/runs/events — best-effort, ≤300 ms, silent when
// no daemon runs. PCBPILOT_CONSOLE_HOOK=0 disables it.

var trackedCommands = []string{
	"pcbpilot sim power", "pcbpilot sim analog", "pcbpilot sim post-layout",
	"pcbpilot report design", "pcbpilot intent derive", "pcbpilot pcb auto run",
	"pcbpilot kb add", "pcbpilot kb reindex",
}

func trackedCommand(path string) bool {
	for _, t := range trackedCommands {
		if path == t || strings.HasPrefix(path, t+" ") {
			return true
		}
	}
	return false
}

// startRunHook posts a start event for tracked commands and returns the end
// callback (nil when not tracked).
func startRunHook(root *cobra.Command, args []string) func(code int) {
	if os.Getenv("PCBPILOT_CONSOLE_HOOK") == "0" || testing.Testing() {
		return nil
	}
	cmd, _, err := root.Find(args)
	if err != nil || cmd == nil || !trackedCommand(cmd.CommandPath()) {
		return nil
	}
	cfg := &appConfig{host: defaultHost, ports: fmt.Sprintf("%d-%d", defaultPortStart, defaultPortEnd)}
	// Honour --host/--ports on the command line (flags are not parsed yet).
	for i, a := range args {
		for _, f := range []struct {
			name string
			dst  *string
		}{{"--ports", &cfg.ports}, {"--host", &cfg.host}} {
			if v, ok := strings.CutPrefix(a, f.name+"="); ok {
				*f.dst = v
			} else if a == f.name && i+1 < len(args) {
				*f.dst = args[i+1]
			}
		}
	}
	cl, err := newConsoleClient(cfg, 300*time.Millisecond)
	if err != nil {
		return nil
	}
	cwd, _ := os.Getwd()
	runID := fmt.Sprintf("cli-%d-%d", os.Getpid(), time.Now().UnixNano())
	title := cmd.CommandPath()
	command := strings.Join(append([]string{"pcbpilot"}, args...), " ")
	post := func(ev console.RunEvent) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		_ = cl.do(ctx, "POST", "/api/runs/events", ev, nil)
	}
	post(console.RunEvent{RunID: runID, Agent: "cli", Kind: "start", Title: title, Command: truncateStr(command, 900),
		Cwd: cwd, Project: os.Getenv("PCBPILOT_PROJECT")})
	started := time.Now()
	return func(code int) {
		status := "ok"
		if code != 0 {
			status = "failed"
		}
		post(console.RunEvent{RunID: runID, Kind: "end", Status: status,
			Detail: fmt.Sprintf("exit %d after %s", code, time.Since(started).Round(time.Millisecond))})
	}
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
