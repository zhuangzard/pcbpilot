package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Unlike the HTTP fixture tests, this uses a real native executable and leaves
// verifyBinary untouched. In particular, Windows must load downloadTo's actual
// temporary filename, which currently has no .exe suffix. Everything stays in
// t.TempDir; replacement never touches the test process or an installed CLI.
func TestNativeDownloadedBinaryVerificationAndReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	work := t.TempDir()
	source := filepath.Join(work, "version.go")
	const program = `package main
import "os"
func main() {
	if len(os.Args) != 2 || os.Args[1] != "version" { os.Exit(2) }
	_, _ = os.Stdout.WriteString("pcbpilot v1.4.20\n")
}
`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	built := filepath.Join(work, "fixture.exe")
	build := exec.CommandContext(ctx, "go", "build", "-o", built, source)
	build.Dir = work
	// Only standard-library packages are needed. Do not resolve modules or fetch
	// another toolchain when this regression runs on a user's offline machine.
	build.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off",
		"CGO_ENABLED=0", "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native fixture: %v\n%s", err, output)
	}
	image, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/binary" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(image)
	}))
	defer server.Close()
	staging := filepath.Join(work, "install with spaces")
	if err := os.Mkdir(staging, 0700); err != nil {
		t.Fatal(err)
	}
	downloaded, sum, err := downloadTo(ctx, server.URL+"/binary", staging, 0755)
	if err != nil {
		t.Fatalf("download native executable: %v", err)
	}
	defer os.Remove(downloaded)
	wantSum := sha256.Sum256(image)
	if sum != hex.EncodeToString(wantSum[:]) {
		t.Fatal("download changed native executable bytes")
	}
	if filepath.Dir(downloaded) != staging {
		t.Fatalf("download was not staged beside the destination: %s", downloaded)
	}
	t.Logf("native %s/%s loader path: %s", runtime.GOOS, runtime.GOARCH, downloaded)
	if err := verifyBinary(ctx, downloaded, "1.4.20"); err != nil {
		t.Fatalf("load downloaded native executable: %v", err)
	}
	// This also catches a substring comparison accidentally accepting 1.4.20
	// as the requested 1.4.2 release.
	if err := verifyBinary(ctx, downloaded, "1.4.2"); err == nil || !strings.Contains(err.Error(), "expected version 1.4.2") {
		t.Fatalf("wrong release was not rejected: %v", err)
	}
	destination := filepath.Join(staging, "pcbpilot.exe")
	if err := os.WriteFile(destination, []byte("old temporary target"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(downloaded, destination); err != nil {
		t.Fatalf("replace ordinary temporary target: %v", err)
	}
	if _, err := os.Stat(downloaded); !os.IsNotExist(err) {
		t.Fatalf("staged file remained after replacement: %v", err)
	}
	replaced, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(replaced, image) {
		t.Fatalf("replacement did not preserve native executable: %v", err)
	}
	if err := verifyBinary(ctx, destination, "1.4.20"); err != nil {
		t.Fatalf("load replaced temporary executable: %v", err)
	}
}
