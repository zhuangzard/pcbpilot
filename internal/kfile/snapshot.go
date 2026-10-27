package kfile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Snapshot is the identity of a file's content at one moment.
type Snapshot struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// Read returns a file's bytes and their snapshot, from the same read.
func Read(path string) ([]byte, Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, Snapshot{Path: path}, err
	}
	return b, snapOf(path, b), nil
}

// Stat hashes path. A missing file is Exists=false with a nil error.
func Stat(path string) (Snapshot, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{Path: path}, nil
	}
	if err != nil {
		return Snapshot{Path: path}, err
	}
	return snapOf(path, b), nil
}

func snapOf(path string, b []byte) Snapshot {
	h := sha256.Sum256(b)
	return Snapshot{Path: path, Exists: true, Size: int64(len(b)), SHA256: hex.EncodeToString(h[:])}
}

// Check re-hashes s.Path and fails with E_FILE_CHANGED unless it still has
// the content s recorded (or is still absent).
func (s Snapshot) Check() error {
	now, err := Stat(s.Path)
	if err != nil {
		return wrapf(err, "re-hash %s", s.Path)
	}
	switch {
	case s.Exists && !now.Exists:
		return changedErr(s.Path, "deleted since it was read (sha256 "+short(s.SHA256)+")")
	case !s.Exists && now.Exists:
		return changedErr(s.Path, "created since it was checked (sha256 "+short(now.SHA256)+")")
	case s.SHA256 != now.SHA256:
		return changedErr(s.Path, fmt.Sprintf("changed since it was read (sha256 %s → %s)", short(s.SHA256), short(now.SHA256)))
	}
	return nil
}
