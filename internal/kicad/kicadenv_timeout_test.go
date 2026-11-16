package kicad

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A kicad-cli run stopped at the timeout says so; it does not read like a
// crash with no message ("exit status 1:").
func TestSchCLIFailNamesTheTimeout(t *testing.T) {
	if os.Getenv("PCBPILOT_TEST_SLEEP") == "1" {
		time.Sleep(time.Minute)
		return
	}
	old := SchCLITimeout
	SchCLITimeout = 300 * time.Millisecond
	defer func() { SchCLITimeout = old }()
	t.Setenv("PCBPILOT_TEST_SLEEP", "1")
	t.Setenv(EnvIsolateConfig, "0")
	cmd, cancel := SchCLICommand(os.Args[0], "-test.run", "^TestSchCLIFailNamesTheTimeout$")
	defer cancel()
	err := cmd.Run()
	if err == nil {
		t.Fatal("the sleeping child was not stopped")
	}
	if msg := SchCLIFail(cmd, err, ""); !strings.Contains(msg, "timed out after 300ms") {
		t.Fatalf("msg = %q", msg)
	}

	// an ordinary failure keeps its status and its output
	SchCLITimeout = old
	os.Unsetenv("PCBPILOT_TEST_SLEEP")
	cmd2, cancel2 := SchCLICommand(os.Args[0], "-test.run", "^$", "-no-such-flag")
	defer cancel2()
	out, err := cmd2.CombinedOutput()
	if err == nil {
		t.Fatal("expected a failure")
	}
	msg := SchCLIFail(cmd2, err, string(out))
	if strings.Contains(msg, "timed out") || !strings.Contains(msg, "exit status") {
		t.Fatalf("msg = %q", msg)
	}
	if msg := SchCLIFail(cmd2, err, "  "); !strings.Contains(msg, "printed nothing") {
		t.Fatalf("msg = %q", msg)
	}
}
