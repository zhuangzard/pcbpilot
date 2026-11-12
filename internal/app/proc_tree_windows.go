//go:build windows

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// killTreeOnCancel makes a cancelled or timed-out cmd take its descendants
// with it: a launcher that starts the real program as a child (a packaged
// Java application may) would otherwise leave that child running and writing
// its output after pcbpilot moved on. taskkill walks the tree while the
// launcher is still alive; the launcher itself is then killed in any case.
func killTreeOnCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		// from the system directory: PATH may not hold it
		taskkill := "taskkill"
		if root := os.Getenv("SystemRoot"); root != "" {
			if p := filepath.Join(root, "System32", "taskkill.exe"); fileExists(p) {
				taskkill = p
			}
		}
		_ = exec.Command(taskkill, "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		return cmd.Process.Kill()
	}
}
