package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/daemon"
)

func newDaemonStopCmd(cfg *appConfig, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use: "stop", Short: "Stop the identified local daemon on --ports' first port",
		Long: "Stop the identified local daemon. Save documents first. A daemon restart does not cancel in-flight editor actions. Refuses remote hosts, unidentified processes and unreadable health responses. Stop make dev in its own terminal to prevent it restarting the daemon.",
		Args: cobra.NoArgs, Example: "  pcbpilot daemon stop\n  pcbpilot daemon stop --ports 61832-61832",
		RunE: func(cmd *cobra.Command, args []string) error {
			port, _, err := cfg.portRange()
			if err != nil {
				return err
			}
			return stopLocalDaemon(cfg.host, port, stdout)
		},
	}
}

func newDaemonRestartCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	c := newDaemonStartCmd(cfg, stdout, stderr)
	start := c.RunE
	c.Use = "restart"
	c.Short = "Stop the local daemon, then start this CLI version in the foreground"
	c.Long = "Save readable documents first; restarting does not cancel editor actions. Stops only an identified local daemon, then starts this binary with the supplied start flags (blocks until SIGINT/SIGTERM). Startup flags are not inherited from the old process.\n\n" + c.Long
	c.Example = "  pcbpilot daemon restart --auto-update-skill=false\n  pcbpilot daemon restart --autosave-debounce 5s"
	c.RunE = func(cmd *cobra.Command, args []string) error {
		port, _, err := cfg.portRange()
		if err != nil {
			return err
		}
		if err := stopLocalDaemon(cfg.host, port, stdout); err != nil {
			return err
		}
		return start(cmd, args)
	}
	return c
}

func requireLocalDaemonHost(host string) error {
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("daemon lifecycle requires a loopback --host, got %q; refusing to terminate a local PID for a remote/wildcard address", host)
}

// Resolve from live health rather than trusting a possibly stale/unwritable PID
// file. Old daemons without health.pid use the OS port owner. Conflicting
// identities or an unresponsive service are never sufficient authority to kill.
func identifiedDaemonPID(host string, port int) (int, error) {
	owner := listenerPID(port)
	fail := func(reason string) (int, error) {
		return 0, fmt.Errorf("port %d owner pid %d (%s): %s; no process was terminated", port, owner, pidCommand(owner), reason)
	}
	client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get("http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/health")
	if err != nil {
		return fail("cannot verify daemon health: " + err.Error())
	}
	defer resp.Body.Close()
	var health struct {
		Service string `json:"service"`
		PID     int    `json:"pid"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&health) != nil || health.Service != daemon.Service {
		return fail("listener is not an identified pcbpilot daemon")
	}
	pid := health.PID
	if pid == 0 {
		pid = owner
	}
	if pid <= 0 || pid == os.Getpid() || (owner != 0 && owner != pid) {
		return fail(fmt.Sprintf("unusable or conflicting health PID %d", health.PID))
	}
	return pid, nil
}

func stopLocalDaemon(host string, port int, log io.Writer) error {
	if err := requireLocalDaemonHost(host); err != nil {
		return err
	}
	if portFree(host, port) {
		fmt.Fprintf(log, "No daemon listening on %s:%d\n", host, port)
		return nil
	}
	pid, err := identifiedDaemonPID(host, port)
	if err != nil {
		return err
	}
	if err := termPID(pid); err != nil {
		return fmt.Errorf("stop daemon pid %d: %w", pid, err)
	}
	if !waitPortFree(host, port, 3*time.Second) {
		return fmt.Errorf("port %d still busy after stopping daemon pid %d; check for a supervisor such as make dev", port, pid)
	}
	fmt.Fprintf(log, "Stopped daemon pid %d on port %d\n", pid, port)
	return nil
}

// Parse numeric netstat output independently of column spacing / address family.
// Ambiguous owners are rejected, never resolved by guessing the first PID.
func windowsListenerPID(output string, port int) int {
	pid := 0
	for _, line := range strings.Split(output, "\n") {
		f := strings.Fields(line)
		if len(f) != 5 || f[0] != "TCP" || f[3] != "LISTENING" {
			continue
		}
		_, p, err := net.SplitHostPort(f[1])
		if err != nil || p != strconv.Itoa(port) {
			continue
		}
		n, err := strconv.Atoi(f[4])
		if err != nil || n <= 0 || (pid != 0 && pid != n) {
			return 0
		}
		pid = n
	}
	return pid
}
