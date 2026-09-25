package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDaemonServiceInstallStatusDarwinLinux(t *testing.T) {
	daemonServiceSleep = func(time.Duration) {}
	var calls []string
	old := daemonServiceRunner
	daemonServiceRunner = func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "systemctl" && len(args) > 1 && args[1] == "is-enabled" {
			return "enabled\n", nil
		}
		return "", nil
	}
	defer func() { daemonServiceRunner = old }()
	bin := filepath.Join(t.TempDir(), "pcb pilot") // a space must survive quoting
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, goos := range []string{"darwin", "linux"} {
		home := t.TempDir()
		if st := readDaemonServiceStatus(goos, home); st.Installed {
			t.Fatalf("%s: installed before install", goos)
		}
		calls = nil
		if err := installDaemonService(goos, home, bin, true, &bytes.Buffer{}); err != nil {
			t.Fatalf("%s install: %v", goos, err)
		}
		st := readDaemonServiceStatus(goos, home)
		if !st.Installed || !st.Loaded || st.Binary != bin || !st.BinaryOK {
			t.Fatalf("%s status %+v", goos, st)
		}
		want := "launchctl bootstrap"
		if goos == "linux" {
			want = "systemctl --user enable --now"
		}
		if !strings.Contains(strings.Join(calls, "\n"), want) {
			t.Fatalf("%s: %q not called: %v", goos, want, calls)
		}
		if err := uninstallDaemonService(goos, home, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if st := readDaemonServiceStatus(goos, home); st.Installed {
			t.Fatalf("%s: still installed after uninstall", goos)
		}
	}
}

func TestDaemonServiceNoStartDoesNotBootstrap(t *testing.T) {
	var calls []string
	old := daemonServiceRunner
	daemonServiceRunner = func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}
	defer func() { daemonServiceRunner = old }()
	if err := installDaemonService("darwin", t.TempDir(), "/bin/sh", false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("--no-start ran %v", calls)
	}
}

func TestDaemonServiceWindowsRunValue(t *testing.T) {
	v := daemonServiceRunValue(`C:\Users\o'neil\bin\pcbpilot.exe`)
	if !strings.Contains(v, `& 'C:\Users\o''neil\bin\pcbpilot.exe' daemon start`) || !strings.Contains(v, "-WindowStyle Hidden") {
		t.Fatalf("run value %s", v)
	}
}

// launchd finishes a bootout asynchronously: the first bootstrap right behind
// it fails with "5: Input/output error" (live 2026-09-25) — install retries.
func TestDaemonServiceBootstrapRetries(t *testing.T) {
	daemonServiceSleep = func(time.Duration) {}
	n := 0
	old := daemonServiceRunner
	daemonServiceRunner = func(name string, args ...string) (string, error) {
		if name == "launchctl" && args[0] == "bootstrap" {
			n++
			if n < 3 {
				return "Bootstrap failed: 5: Input/output error", os.ErrInvalid
			}
		}
		return "", nil
	}
	defer func() { daemonServiceRunner = old }()
	if err := installDaemonService("darwin", t.TempDir(), "/bin/sh", true, &bytes.Buffer{}); err != nil || n != 3 {
		t.Fatalf("err %v after %d bootstrap attempts", err, n)
	}
}
