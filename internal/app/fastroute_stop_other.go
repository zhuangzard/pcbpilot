//go:build !windows

package app

import (
	"io"
	"os"
	"os/exec"
)

// prepareFastrouteCmd: on cancel or timeout fastroute gets SIGINT, on which
// it writes its best board so far and exits (fastroute --help, --max-time).
// A Ctrl+C in the terminal reaches it directly (same process group).
func prepareFastrouteCmd(cmd *exec.Cmd, stderr io.Writer) {
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
}

func runFastrouteCmd(cmd *exec.Cmd, stderr io.Writer) error { return cmd.Run() }
