package selfupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// An unsigned darwin/amd64 binary gets an ad-hoc signature; a signed one is left alone.
func TestEnsureLocalSignatureSignsUnsignedBinary(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "unsigned")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH=amd64", "GO111MODULE=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot cross-build an amd64 binary: %v %s", err, out)
	}
	if exec.Command("/usr/bin/codesign", "--verify", bin).Run() == nil {
		t.Skip("toolchain already signs amd64 binaries; nothing to test")
	}
	if err := ensureLocalSignature(bin); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := exec.Command("/usr/bin/codesign", "--verify", bin).Run(); err != nil {
		t.Fatalf("binary still fails codesign --verify after ensureLocalSignature: %v", err)
	}
	before, _ := os.ReadFile(bin)
	if err := ensureLocalSignature(bin); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(bin)
	if string(before) != string(after) {
		t.Fatal("an already signed binary was re-signed")
	}
}

func TestReplaceBinarySignsOnInstall(t *testing.T) {
	var got []string
	old := ensureLocalSignature
	ensureLocalSignature = func(p string) error { got = append(got, p); return nil }
	t.Cleanup(func() { ensureLocalSignature = old })
	dir := t.TempDir()
	tmp, dst := filepath.Join(dir, "new"), filepath.Join(dir, "pcbpilot")
	if err := os.WriteFile(tmp, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(tmp, dst); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && (len(got) != 1 || got[0] != tmp) {
		t.Fatalf("ensureLocalSignature calls = %v, want exactly the staged file", got)
	}
}
