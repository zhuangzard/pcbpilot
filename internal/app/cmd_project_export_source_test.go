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

func TestProjectExportSourceHelpAndRequiredIdentity(t *testing.T) {
	var out bytes.Buffer
	cmd := newProjectCmd(nil, &out, &out)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"export-source", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--uuid", "--out", ".epro2", "8 MiB"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help missing %q", want)
		}
	}
	out.Reset()
	cmd = newProjectCmd(nil, &out, &out)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"export-source"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--uuid") {
		t.Fatalf("expected local identity validation, got %v", err)
	}
}

func TestVerifyProjectSourceArtifact(t *testing.T) {
	data := []byte{0x50, 0x4b, 0x03, 0x04, 0, 255}
	path := filepath.Join(t.TempDir(), "source.epro2")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	res := &actionResult{OK: true, Result: map[string]any{
		"uuid": "p", "fileType": "epro2", "size": float64(len(data)), "artifactId": "a",
	}, Artifacts: []artifactRef{{ID: "a", Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}}
	if err := verifyProjectSourceArtifact(res, "p"); err != nil {
		t.Fatalf("valid original rejected: %v", err)
	}
	res.Result["uuid"] = "wrong"
	if err := verifyProjectSourceArtifact(res, "p"); err == nil {
		t.Fatal("wrong project identity accepted")
	}
	res.Result["uuid"] = "p"
	res.Artifacts[0].SHA256 = strings.Repeat("0", 64)
	if err := verifyProjectSourceArtifact(res, "p"); err == nil {
		t.Fatal("corrupt artifact hash accepted")
	}
	res.Artifacts[0].SHA256 = hex.EncodeToString(sum[:])
	res.Artifacts[0].Size = maxProjectSourceBytes + 1
	if err := verifyProjectSourceArtifact(res, "p"); err == nil {
		t.Fatal("oversized artifact accepted")
	}
}
