package kicad

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeSlowBridgeEnv makes this test binary, run as the bridge's Python, sleep
// (TestMain).
const fakeSlowBridgeEnv = "PCBPILOT_FAKE_SLOW_BRIDGE"

func fakeSlowBridge() {
	if os.Getenv(fakeSlowBridgeEnv) == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

// A deadline reaches the bridge process: it is killed, the caller gets the
// context's error at once, and an edit leaves no result behind.
func TestBridgeContextKillsBridge(t *testing.T) {
	t.Setenv(fakeSlowBridgeEnv, "1")
	tools := &Tools{Python: os.Args[0]}
	dir := t.TempDir()
	calls := map[string]func(ctx context.Context) error{
		"snapshot": func(ctx context.Context) error {
			_, err := tools.SnapshotContext(ctx, filepath.Join(dir, "in.kicad_pcb"))
			return err
		},
		"edit": func(ctx context.Context) error {
			_, err := tools.EditContext(ctx, filepath.Join(dir, "in.kicad_pcb"), map[string]any{"setText": []any{}}, filepath.Join(dir, "out.kicad_pcb"))
			return err
		},
	}
	for name, call := range calls {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		start := time.Now()
		err := call(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s: %v", name, err)
		}
		if d := time.Since(start); d > 30*time.Second {
			t.Fatalf("%s: returned after %s", name, d)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "out.kicad_pcb")); !os.IsNotExist(err) {
		t.Fatalf("edit left a result: %v", err)
	}
}
