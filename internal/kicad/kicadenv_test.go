package kicad

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func resetIsolated() {
	isolatedOnce = sync.Once{}
	isolatedDir = ""
}

func envValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// field report 2026-10-15: `kicad-cli sch erc` hung loading every stock library
// from the global sym-lib-table; schematic runs get empty global tables.
func TestSchCLIEnvIsolatesGlobalLibTables(t *testing.T) {
	t.Setenv(EnvCacheDir, t.TempDir())
	t.Setenv("KICAD_CONFIG_HOME", "")
	os.Unsetenv("KICAD_CONFIG_HOME")
	t.Setenv(EnvIsolateConfig, "")
	resetIsolated()
	defer resetIsolated()

	dir, ok := envValue(SchCLIEnv(), "KICAD_CONFIG_HOME")
	if !ok || dir == "" {
		t.Fatal("KICAD_CONFIG_HOME not set for a schematic kicad-cli run")
	}
	for _, sub := range append([]string{""}, kicadSettingsVersions...) {
		for _, name := range []string{"sym-lib-table", "fp-lib-table"} {
			b, err := os.ReadFile(filepath.Join(dir, sub, name))
			if err != nil {
				t.Fatalf("%s/%s: %v", sub, name, err)
			}
			if strings.Contains(string(b), "(lib ") {
				t.Fatalf("%s/%s is not empty:\n%s", sub, name, b)
			}
		}
	}
}

func TestSchCLIEnvRespectsUserConfigAndOptOut(t *testing.T) {
	t.Setenv(EnvCacheDir, t.TempDir())
	resetIsolated()
	defer resetIsolated()

	t.Setenv("KICAD_CONFIG_HOME", "/user/choice")
	if env := SchCLIEnv(); env != nil {
		t.Fatalf("a user-set KICAD_CONFIG_HOME must be inherited unchanged, got override")
	}
	os.Unsetenv("KICAD_CONFIG_HOME")
	t.Setenv(EnvIsolateConfig, "0")
	if env := SchCLIEnv(); env != nil {
		t.Fatal("PCBPILOT_KICAD_ISOLATE=0 must keep the inherited environment")
	}
}
