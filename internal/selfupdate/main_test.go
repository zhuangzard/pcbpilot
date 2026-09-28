package selfupdate

import (
	"os"
	"testing"
)

// TestMain runs the package in a temp HOME so no test can write the real
// ~/.pcbpilot or a real client config, even one that forgets to isolate.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "pcbpilot-selfupdate-test-home-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	os.Setenv(GitHubProxyEnv, "off")
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
