package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/console"
	"github.com/zhuangzard/pcbpilot/internal/daemon"
	"github.com/zhuangzard/pcbpilot/internal/version"
)

// ── daemon wiring ─────────────────────────────────────────────────────────

// mountConsole attaches the v0.7 web console to a daemon before Run. It is a
// no-op (with a log line) when the daemon binds a non-loopback host: the
// console is a local cockpit and must never be reachable from the network.
func mountConsole(srv *daemon.Server, host string, log io.Writer) *console.Console {
	if !isLoopbackHost(host) {
		fmt.Fprintf(log, "%s daemon: console disabled — daemon host %q is not loopback\n", daemon.Service, host)
		return nil
	}
	userHome, _ := os.UserHomeDir()
	var svcMu sync.Mutex
	var svcAt time.Time
	var svc any
	c, err := console.New(console.Options{
		Version:  version.Version,
		Host:     host,
		AuditDir: defaultAuditDir(),
		Health: func(ctx context.Context) (json.RawMessage, error) {
			return fetchLocal(ctx, fmt.Sprintf("http://%s/health", net.JoinHostPort(loopbackDial(host), fmt.Sprint(srv.Port()))))
		},
		Daemon: func() console.DaemonInfo {
			d := srv.AutosaveDebounce()
			return console.DaemonInfo{PID: os.Getpid(), Version: version.Version, Host: host, Port: srv.Port(),
				StartedAt: srv.StartedAt(), Autosave: d > 0, AutosaveDebounce: d.String()}
		},
		// The login-service probe shells out (launchctl/systemctl); cache it.
		Service: func() any {
			svcMu.Lock()
			defer svcMu.Unlock()
			if time.Since(svcAt) > 30*time.Second {
				svc, svcAt = readDaemonServiceStatus(runtime.GOOS, userHome), time.Now()
			}
			return svc
		},
	})
	if err != nil {
		fmt.Fprintf(log, "%s daemon: console disabled — %v\n", daemon.Service, err)
		return nil
	}
	srv.OnActivity(c.Observe)
	h := c.Handler()
	srv.Handle("/ui/", h)
	srv.Handle("/ui", h)
	srv.Handle("/api/", h)
	c.Start()
	fmt.Fprintf(log, "%s daemon: console at /ui (token in %s; `pcbpilot console open`)\n", daemon.Service, filepath.Join(console.Home(), console.TokenFile))
	return c
}

func isLoopbackHost(h string) bool {
	switch strings.Trim(strings.ToLower(h), "[]") {
	case "", "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

func loopbackDial(h string) string {
	if h == "" || h == "localhost" {
		return "127.0.0.1"
	}
	return strings.Trim(h, "[]")
}

func fetchLocal(ctx context.Context, url string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health: HTTP %d", resp.StatusCode)
	}
	return b, nil
}

// ── console client (CLI side) ─────────────────────────────────────────────

// consoleClient talks to the running daemon's console API with the install
// token. It never starts a daemon.
type consoleClient struct {
	base  string
	token string
	http  *http.Client
}

// newConsoleClient finds the daemon (fixed port) and reads the token.
func newConsoleClient(cfg *appConfig, timeout time.Duration) (*consoleClient, error) {
	port, _, err := cfg.portRange()
	if err != nil {
		return nil, err
	}
	tok, err := console.ReadToken(console.Home())
	if err != nil {
		return nil, fmt.Errorf("console token not found (%w) — start the daemon once (`pcbpilot daemon start`) to create it", err)
	}
	return &consoleClient{base: fmt.Sprintf("http://%s", net.JoinHostPort(loopbackDial(cfg.host), fmt.Sprint(port))),
		token: tok, http: &http.Client{Timeout: timeout}}, nil
}

func (c *consoleClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set(console.TokenHeader, c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
			return &consoleHTTPError{status: resp.StatusCode, code: e.Error.Code, msg: e.Error.Message}
		}
		return &consoleHTTPError{status: resp.StatusCode, msg: strings.TrimSpace(string(b))}
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

type consoleHTTPError struct {
	status    int
	code, msg string
}

func (e *consoleHTTPError) Error() string {
	if e.code != "" {
		return fmt.Sprintf("console API %d %s: %s", e.status, e.code, e.msg)
	}
	return fmt.Sprintf("console API %d: %s", e.status, e.msg)
}

// ── pcbpilot console ──────────────────────────────────────────────────────

func newConsoleCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	c := &cobra.Command{
		Use:   "console",
		Short: "Local web cockpit served by the daemon (/ui): monitor, projects, timeline, sims, reports, library, decisions",
		Long: `The console is a local web page served by the running daemon at http://127.0.0.1:<port>/ui/.
It shows the daemon, component versions, connected EasyEDA windows, every project the system
has worked on (live and finished), the live action stream, design-flow timelines, simulation
rounds, reports, the resource library and pending decision cards.

It binds loopback only and requires the per-install token in ~/.pcbpilot/console.token. The
URL printed by 'console url' carries the token in the #fragment (never sent to the server);
the page exchanges it for an HttpOnly cookie. The console never edits EDA projects.`,
	}
	var noBrowser bool
	open := &cobra.Command{
		Use:   "open",
		Short: "Print the console URL and open it in the default browser",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := consoleURL(cfg)
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, u)
			if noBrowser {
				return nil
			}
			if err := openBrowser(u); err != nil {
				fmt.Fprintf(stderr, "could not open a browser (%v) — paste the URL above\n", err)
			}
			return nil
		},
	}
	open.Flags().BoolVar(&noBrowser, "no-browser", false, "only print the URL")
	url := &cobra.Command{
		Use:   "url",
		Short: "Print the console URL (with the token in the #fragment)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := consoleURL(cfg)
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, u)
			return nil
		},
	}
	c.AddCommand(open, url, newConsoleProjectsCmd(stdout))
	return c
}

func consoleURL(cfg *appConfig) (string, error) {
	port, _, err := cfg.portRange()
	if err != nil {
		return "", err
	}
	tok, err := console.EnsureToken(console.Home())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://%s/ui/#token=%s", net.JoinHostPort(loopbackDial(cfg.host), fmt.Sprint(port)), tok), nil
}

func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}

func newConsoleProjectsCmd(stdout io.Writer) *cobra.Command {
	p := &cobra.Command{Use: "projects", Short: "Manage the work dirs the console tracks (~/.pcbpilot/console/workdirs.json)"}
	var name string
	add := &cobra.Command{
		Use:   "add [dir]",
		Short: "Register a work dir (default: current directory)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			wd, err := console.RegisterWorkDir(console.Home(), dir, name, "cli")
			if err != nil {
				return err
			}
			return writeJSON(stdout, wd)
		},
	}
	add.Flags().StringVar(&name, "name", "", "display name")
	list := &cobra.Command{
		Use:   "list",
		Short: "List registered work dirs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dirs, err := console.LoadWorkDirs(console.Home())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tDIR")
			for _, d := range dirs {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", d.ID, d.Name, d.Dir)
			}
			return tw.Flush()
		},
	}
	rm := &cobra.Command{
		Use:   "remove <id>",
		Short: "Forget a work dir (files are not touched)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return console.UnregisterWorkDir(console.Home(), args[0])
		},
	}
	p.AddCommand(add, list, rm)
	return p
}
