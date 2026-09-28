package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// AssetSource supplies verified release assets for one version. Two
// implementations: a GitHub (or PCBPILOT_RELEASE_BASE_URL) release, and a
// local asset directory (`pcbpilot update --local-dir`). Fetch returns bytes
// whose SHA-256 matched the source's checksums.txt, or an error wrapping
// ErrAssetMissing when the release does not carry that asset at all.
type AssetSource interface {
	Version() string // x.y.z (or x.y.z-dev.N for a local package), no leading v
	Fetch(ctx context.Context, name string, limit int64) ([]byte, error)
	Describe() string
	Local() bool
}

type releaseAssets struct{ version string }

// ReleaseAssets is the published release of version.
func ReleaseAssets(version string) AssetSource {
	return releaseAssets{version: strings.TrimPrefix(SemverCore(version), "v")}
}

func (r releaseAssets) Version() string  { return r.version }
func (r releaseAssets) Local() bool      { return false }
func (r releaseAssets) Describe() string { return ReleaseAssetURL(r.version, "") }

func (r releaseAssets) Fetch(ctx context.Context, name string, limit int64) ([]byte, error) {
	want, err := fetchChecksum(ctx, r.version, name)
	if err != nil {
		if errors.Is(err, errChecksumUnavailable) {
			return nil, fmt.Errorf("%s: release v%s has no checksums.txt: %w", name, r.version, ErrAssetMissing)
		}
		return nil, err
	}
	body, err := downloadBytesWithFallback(ctx, ReleaseAssetURL(r.version, name), limit+1, want, true)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", name, limit)
	}
	if checksumHex(body) != strings.ToLower(want) {
		return nil, fmt.Errorf("checksum mismatch for %s", name)
	}
	return body, nil
}

type localAssets struct {
	dir     string
	version string
	sums    map[string]string
}

// LocalAssets reads a local asset dir (the output of `make release-build` /
// `make local-build`: checksums.txt plus the assets). The version is the one
// declared by the packaged SKILL.md; every asset is checked against
// checksums.txt on Fetch.
func LocalAssets(dir string) (AssetSource, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(abs, "checksums.txt"))
	if err != nil {
		return nil, err
	}
	sums, err := parseChecksums(raw)
	if err != nil {
		return nil, err
	}
	l := &localAssets{dir: abs, sums: sums}
	archive, err := l.Fetch(context.Background(), "skills.tar.gz", 64<<20)
	if err != nil {
		return nil, err
	}
	v, err := archiveSkillVersion(archive)
	if err != nil {
		return nil, err
	}
	if !IsCleanRelease(v) && !IsLocalVersion(v) {
		return nil, fmt.Errorf("local assets: SKILL.md version %q is neither X.Y.Z nor X.Y.Z-dev.N", v)
	}
	l.version = strings.TrimPrefix(v, "v")
	return l, nil
}

func (l *localAssets) Version() string  { return l.version }
func (l *localAssets) Local() bool      { return true }
func (l *localAssets) Describe() string { return l.dir }

func (l *localAssets) Fetch(_ context.Context, name string, limit int64) ([]byte, error) {
	want, ok := l.sums[name]
	if !ok {
		return nil, fmt.Errorf("%s not listed in %s/checksums.txt: %w", name, l.dir, ErrAssetMissing)
	}
	f, err := os.Open(filepath.Join(l.dir, name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", name, limit)
	}
	if len(body) == 0 || checksumHex(body) != want {
		return nil, fmt.Errorf("checksum mismatch: %s", name)
	}
	return body, nil
}

func parseChecksums(raw []byte) (map[string]string, error) {
	sums := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) != 2 {
			return nil, fmt.Errorf("invalid checksum entry %q", line)
		}
		name := strings.TrimPrefix(f[1], "*")
		if filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
			return nil, fmt.Errorf("invalid checksum entry %q", line)
		}
		if _, dup := sums[name]; dup {
			return nil, fmt.Errorf("duplicate checksum entry for %s", name)
		}
		if d, e := hex.DecodeString(f[0]); e != nil || len(d) != sha256.Size {
			return nil, fmt.Errorf("invalid SHA-256 for %s", name)
		}
		sums[name] = strings.ToLower(f[0])
	}
	return sums, nil
}

// archiveSkillVersion reads metadata.version from pcbpilot/SKILL.md inside a
// skills.tar.gz without extracting anything else.
func archiveSkillVersion(archive []byte) (string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("skills.tar.gz has no %s/SKILL.md", SkillName)
		}
		if err != nil {
			return "", err
		}
		if filepath.ToSlash(filepath.Clean(h.Name)) != SkillName+"/SKILL.md" {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			return "", err
		}
		v, err := skillMetadataVersion(string(body))
		if err != nil {
			return "", err
		}
		if v == "" {
			return "", fmt.Errorf("SKILL.md has no metadata.version")
		}
		return v, nil
	}
}

// ConnectorManifestVersion returns extension.json's version inside a .eext and
// checks that the compiled entry exists.
func ConnectorManifestVersion(eext []byte) (string, error) {
	z, err := zip.NewReader(bytes.NewReader(eext), int64(len(eext)))
	if err != nil {
		return "", fmt.Errorf("connector .eext is not a zip: %w", err)
	}
	version, compiled, manifests := "", false, 0
	for _, f := range z.File {
		switch f.Name {
		case "dist/index.js":
			compiled = f.UncompressedSize64 > 0
		case "extension.json":
			manifests++
			r, err := f.Open()
			if err != nil {
				return "", err
			}
			var m struct {
				Version string `json:"version"`
			}
			err = json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&m)
			r.Close()
			if err != nil {
				return "", err
			}
			version = m.Version
		}
	}
	if manifests != 1 || !compiled || version == "" {
		return "", fmt.Errorf("not a compiled connector package (manifest=%d compiled=%v)", manifests, compiled)
	}
	return version, nil
}
