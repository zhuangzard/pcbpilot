package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// daemon service: the daemon as a per-user login service. It is a REQUIRED
// part of an install (2026-09-25): the connector only ever talks to a daemon
// on 61832, so a machine that reboots without one silently loses every EDA
// action until someone remembers `pcbpilot daemon start`. Installers
// (scripts/setup-agent.sh, install.sh, install.ps1) call `service install`
// and the post-install verify fails without it.
//
//	macOS   ~/Library/LaunchAgents/com.pcbpilot.daemon.plist (launchd, KeepAlive)
//	Linux   ~/.config/systemd/user/pcbpilot-daemon.service (systemd --user)
//	Windows HKCU\Software\Microsoft\Windows\CurrentVersion\Run  value "pcbpilot-daemon"

const (
	daemonServiceLabel   = "com.pcbpilot.daemon"
	daemonServiceUnit    = "pcbpilot-daemon.service"
	daemonServiceRunKey  = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	daemonServiceRunName = "pcbpilot-daemon"
)

// daemonServiceSleep is swapped in tests.
var daemonServiceSleep = time.Sleep

// daemonServiceRunner is swapped in tests; it runs a platform tool and
// returns combined output.
var daemonServiceRunner = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

type daemonServiceStatus struct {
	Platform  string `json:"platform"`
	Path      string `json:"path"`
	Installed bool   `json:"installed"`
	Binary    string `json:"binary,omitempty"`
	BinaryOK  bool   `json:"binaryExists"`
	Loaded    bool   `json:"loaded"`
	Detail    string `json:"detail,omitempty"`
}

func daemonServicePath(goos, home string) string {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", daemonServiceLabel+".plist")
	case "linux":
		return filepath.Join(home, ".config", "systemd", "user", daemonServiceUnit)
	case "windows":
		return daemonServiceRunKey + `\` + daemonServiceRunName
	}
	return ""
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

func daemonServicePlist(bin, logPath string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + daemonServiceLabel + `</string>
  <key>ProgramArguments</key><array><string>` + xmlEscape(bin) + `</string><string>daemon</string><string>start</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>` + xmlEscape(logPath) + `</string>
  <key>StandardErrorPath</key><string>` + xmlEscape(logPath) + `</string>
</dict></plist>
`
}

func daemonServiceUnitFile(bin string) string {
	return fmt.Sprintf("[Unit]\nDescription=pcbpilot daemon\n\n[Service]\nExecStart=%q daemon start\nRestart=on-failure\n\n[Install]\nWantedBy=default.target\n", bin)
}

// daemonServiceRunValue is the HKCU Run command: a hidden PowerShell so the
// blocking `daemon start` does not leave a console window open.
func daemonServiceRunValue(bin string) string {
	return fmt.Sprintf(`powershell.exe -NoProfile -WindowStyle Hidden -Command "& '%s' daemon start"`, strings.ReplaceAll(bin, "'", "''"))
}

// daemonServiceBinary is the binary the service should run: the installed
// executable with symlinks resolved.
func daemonServiceBinary(override string) (string, error) {
	bin := override
	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		bin = exe
	}
	if abs, err := filepath.Abs(bin); err == nil {
		bin = abs
	}
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		bin = r
	}
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("service binary %s: %w", bin, err)
	}
	return bin, nil
}

func readDaemonServiceStatus(goos, home string) daemonServiceStatus {
	st := daemonServiceStatus{Platform: goos, Path: daemonServicePath(goos, home)}
	switch goos {
	case "darwin", "linux":
		raw, err := os.ReadFile(st.Path)
		if err != nil {
			st.Detail = "service file missing"
			return st
		}
		st.Installed = true
		text := string(raw)
		if goos == "darwin" {
			if i := strings.Index(text, "<array><string>"); i >= 0 {
				rest := text[i+len("<array><string>"):]
				if j := strings.Index(rest, "</string>"); j >= 0 {
					st.Binary = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'").Replace(rest[:j])
				}
			}
			_, err := daemonServiceRunner("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), daemonServiceLabel))
			st.Loaded = err == nil
		} else {
			for _, l := range strings.Split(text, "\n") {
				if v, ok := strings.CutPrefix(l, "ExecStart="); ok {
					if q, err := strconv.QuotedPrefix(v); err == nil {
						st.Binary, _ = strconv.Unquote(q)
					} else if f := strings.Fields(v); len(f) > 0 {
						st.Binary = f[0]
					}
				}
			}
			out, _ := daemonServiceRunner("systemctl", "--user", "is-enabled", daemonServiceUnit)
			st.Loaded = strings.TrimSpace(out) == "enabled"
		}
	case "windows":
		out, err := daemonServiceRunner("reg", "query", daemonServiceRunKey, "/v", daemonServiceRunName)
		if err != nil {
			st.Detail = "Run key missing"
			return st
		}
		st.Installed, st.Loaded = true, true
		if i := strings.Index(out, "& '"); i >= 0 {
			rest := out[i+3:]
			if j := strings.Index(rest, "' daemon start"); j >= 0 {
				st.Binary = strings.ReplaceAll(rest[:j], "''", "'")
			}
		}
	default:
		st.Detail = "no login-service support on " + goos
		return st
	}
	if st.Binary != "" {
		_, err := os.Stat(st.Binary)
		st.BinaryOK = err == nil
	}
	return st
}

func installDaemonService(goos, home, bin string, start bool, stdout io.Writer) error {
	path := daemonServicePath(goos, home)
	logPath := filepath.Join(home, ".pcbpilot", "daemon.log")
	switch goos {
	case "darwin":
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
		if err := os.WriteFile(path, []byte(daemonServicePlist(bin, logPath)), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "wrote %s\n", path)
		if start {
			domain := fmt.Sprintf("gui/%d", os.Getuid())
			_, _ = daemonServiceRunner("launchctl", "bootout", domain+"/"+daemonServiceLabel)
			// launchd finishes a bootout asynchronously; a bootstrap right
			// behind it fails with "5: Input/output error" (seen live), so retry.
			var out string
			var err error
			for i := 0; i < 10; i++ {
				if out, err = daemonServiceRunner("launchctl", "bootstrap", domain, path); err == nil {
					break
				}
				daemonServiceSleep(500 * time.Millisecond)
			}
			if err != nil {
				return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(out))
			}
			fmt.Fprintf(stdout, "loaded %s (starts now and at every login)\n", daemonServiceLabel)
		}
	case "linux":
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(daemonServiceUnitFile(bin)), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "wrote %s\n", path)
		if out, err := daemonServiceRunner("systemctl", "--user", "daemon-reload"); err != nil {
			return fmt.Errorf("systemctl --user daemon-reload: %v: %s", err, strings.TrimSpace(out))
		}
		args := []string{"--user", "enable", daemonServiceUnit}
		if start {
			args = []string{"--user", "enable", "--now", daemonServiceUnit}
		}
		if out, err := daemonServiceRunner("systemctl", args...); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(out))
		}
		fmt.Fprintf(stdout, "enabled %s (systemd --user; runs at login)\n", daemonServiceUnit)
	case "windows":
		if out, err := daemonServiceRunner("reg", "add", daemonServiceRunKey, "/v", daemonServiceRunName, "/t", "REG_SZ", "/d", daemonServiceRunValue(bin), "/f"); err != nil {
			return fmt.Errorf("reg add: %v: %s", err, strings.TrimSpace(out))
		}
		fmt.Fprintf(stdout, "registered %s (runs at login)\n", path)
		if start {
			if out, err := daemonServiceRunner("powershell.exe", "-NoProfile", "-Command",
				fmt.Sprintf("Start-Process -WindowStyle Hidden -FilePath '%s' -ArgumentList 'daemon','start'", strings.ReplaceAll(bin, "'", "''"))); err != nil {
				return fmt.Errorf("start daemon: %v: %s", err, strings.TrimSpace(out))
			}
		}
	default:
		return fmt.Errorf("no login-service support on %s — run `pcbpilot daemon start` from your session startup", goos)
	}
	return nil
}

func uninstallDaemonService(goos, home string, stdout io.Writer) error {
	path := daemonServicePath(goos, home)
	switch goos {
	case "darwin":
		_, _ = daemonServiceRunner("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), daemonServiceLabel))
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	case "linux":
		_, _ = daemonServiceRunner("systemctl", "--user", "disable", "--now", daemonServiceUnit)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		_, _ = daemonServiceRunner("systemctl", "--user", "daemon-reload")
	case "windows":
		_, _ = daemonServiceRunner("reg", "delete", daemonServiceRunKey, "/v", daemonServiceRunName, "/f")
	default:
		return fmt.Errorf("no login-service support on %s", goos)
	}
	fmt.Fprintf(stdout, "removed %s\n", path)
	return nil
}

func newDaemonServiceCmd(stdout, stderr io.Writer) *cobra.Command {
	svc := &cobra.Command{
		Use:   "service",
		Short: "Install / check / remove the daemon login service (required by every install)",
		Long: `The daemon runs as a per-user login service so it is there after every reboot —
the connector only talks to a daemon on 61832. Every installer calls
'service install' and the post-install verify fails without it.

  macOS   ~/Library/LaunchAgents/com.pcbpilot.daemon.plist (launchd, KeepAlive)
  Linux   ~/.config/systemd/user/pcbpilot-daemon.service (systemd --user)
  Windows HKCU Run value "pcbpilot-daemon" (hidden PowerShell at login)

Working on pcbpilot itself with 'make dev'? Its air-managed daemon and the
service both want 61832: install with --no-start, or 'service uninstall'
while developing and 'service install' when done.`,
	}
	var bin string
	var noStart bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Write the login service for this binary and start it",
		Example: `  pcbpilot daemon service install
  pcbpilot daemon service install --no-start      # a daemon already runs (e.g. make dev)`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			b, err := daemonServiceBinary(bin)
			if err != nil {
				return err
			}
			return installDaemonService(runtime.GOOS, home, b, !noStart, stdout)
		},
	}
	install.Flags().StringVar(&bin, "bin", "", "binary the service runs (default: this executable, symlinks resolved)")
	install.Flags().BoolVar(&noStart, "no-start", false, "register for the next login only; do not start it now")
	var asJSON bool
	status := &cobra.Command{
		Use:   "status",
		Short: "Report whether the login service is installed and points at an existing binary (exit 1 if not)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			st := readDaemonServiceStatus(runtime.GOOS, home)
			if asJSON {
				raw, _ := json.MarshalIndent(st, "", "  ")
				fmt.Fprintln(stdout, string(raw))
			} else {
				fmt.Fprintf(stdout, "service %s: installed=%v loaded=%v binary=%s exists=%v %s\n", st.Path, st.Installed, st.Loaded, st.Binary, st.BinaryOK, st.Detail)
			}
			if !st.Installed || !st.BinaryOK {
				cmd.SilenceUsage = true
				return fmt.Errorf("daemon login service not usable — run `pcbpilot daemon service install`")
			}
			return nil
		},
	}
	status.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the login service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			return uninstallDaemonService(runtime.GOOS, home, stdout)
		},
	}
	svc.AddCommand(install, status, uninstall)
	return svc
}
