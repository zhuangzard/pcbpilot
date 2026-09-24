package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLibrarySymbolExportSourceHelpAndIdentity(t *testing.T) {
	var out bytes.Buffer
	lib := newLibCmd(nil, &out, &out)
	lib.SetOut(&out)
	lib.SetErr(&out)
	lib.SetArgs([]string{"symbol", "export-source", "--help"})
	if err := lib.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--uuid", "--library", "--out", ".elibz2", "8 MiB"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help missing %q", want)
		}
	}
	out.Reset()
	lib = newLibCmd(nil, &out, &out)
	lib.SetOut(&out)
	lib.SetErr(&out)
	lib.SetArgs([]string{"symbol", "export-source", "--uuid", "s"})
	if err := lib.Execute(); err == nil || !strings.Contains(err.Error(), "--uuid and --library") {
		t.Fatalf("expected local identity validation, got %v", err)
	}
}

func TestVerifyLibrarySymbolSourceArtifact(t *testing.T) {
	data := []byte{0x50, 0x4b, 0x03, 0x04, 0, 255}
	path := filepath.Join(t.TempDir(), "source.elibz2")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	res := &actionResult{OK: true, Result: map[string]any{
		"uuid": "s", "libraryUuid": "l", "fileType": "elibz2", "size": float64(len(data)), "artifactId": "a",
	}, Artifacts: []artifactRef{{ID: "a", Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}}
	if err := verifyLibrarySymbolSourceArtifact(res, "s", "l"); err != nil {
		t.Fatalf("valid original rejected: %v", err)
	}
	res.Result["uuid"] = "wrong"
	if err := verifyLibrarySymbolSourceArtifact(res, "s", "l"); err == nil {
		t.Fatal("wrong symbol identity accepted")
	}
	res.Result["uuid"] = "s"
	res.Artifacts[0].SHA256 = strings.Repeat("0", 64)
	if err := verifyLibrarySymbolSourceArtifact(res, "s", "l"); err == nil {
		t.Fatal("corrupt artifact hash accepted")
	}
	res.Artifacts[0].SHA256 = hex.EncodeToString(sum[:])
	res.Artifacts[0].Size = maxSymbolSourceBytes + 1
	if err := verifyLibrarySymbolSourceArtifact(res, "s", "l"); err == nil {
		t.Fatal("oversized artifact accepted")
	}
}
