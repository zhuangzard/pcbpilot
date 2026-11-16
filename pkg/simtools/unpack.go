package simtools

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// maxUnpackBytes bounds what one archive may write (the Elmer package is
// about 120 MB without its compiler toolchain); a zip bomb stops here.
const maxUnpackBytes = 4 << 30

// FileSHA256 returns the lowercase hex SHA-256 of a file.
func FileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// UnpackZip extracts a .zip into dest using only the Go standard library — no
// external unpacker, nothing executed, nothing written outside dest's parent.
//
//   - A single top-level folder in the archive is dropped, so dest itself holds
//     bin/, share/, ….
//   - skip lists folders (relative to that root, slash-separated) left out.
//   - expect lists files that must exist afterwards; without them the archive
//     is not the expected package and dest is left as it was.
//   - Entries that would leave dest (absolute paths, "..", drive letters) and
//     symbolic links are refused.
//
// The tree is built in a sibling staging folder of its own (".stage-<name>-*",
// a dot folder that tool lookup ignores) and renamed into place at the end,
// so an interrupted unpack never leaves a half install that looks complete.
// An existing dest is moved aside first and put back when the new tree
// cannot take its place. Installs into one dest run one at a time (a lock
// file beside it).
func UnpackZip(archive, dest string, skip, expect []string) error {
	return unpackZip(archive, dest, skip, expect, os.Rename)
}

// unpackLockWait: how long a second install waits for the first.
var unpackLockWait = 3 * time.Minute

// lockDest takes the per-destination install lock (tryLock: held by the
// operating system for this process, released when it ends).
func lockDest(dest string) (unlock func(), err error) {
	lock := filepath.Join(filepath.Dir(dest), ".lock-"+filepath.Base(dest))
	deadline := time.Now().Add(unpackLockWait)
	for {
		release, busy, err := tryLock(lock)
		if err != nil {
			return nil, fmt.Errorf("install lock %s: %w", lock, err)
		}
		if !busy {
			return release, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another install into %s is still running (lock %s)", dest, lock)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func unpackZip(archive, dest string, skip, expect []string, rename func(from, to string) error) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("not a readable .zip: %w", err)
	}
	defer zr.Close()

	names := make([]string, len(zr.File))
	for i, f := range zr.File {
		n, err := cleanZipName(f.Name)
		if err != nil {
			return err
		}
		names[i] = n
	}
	strip := commonTopDir(names)

	parent, base := filepath.Dir(dest), filepath.Base(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	unlock, err := lockDest(dest)
	if err != nil {
		return err
	}
	defer unlock()
	stage, err := os.MkdirTemp(parent, ".stage-"+base+"-")
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			os.RemoveAll(stage)
		}
	}()

	var written int64
	for i, f := range zr.File {
		rel := names[i]
		if strip != "" {
			if rel == strip {
				continue
			}
			rel = strings.TrimPrefix(rel, strip+"/")
		}
		if rel == "" || skipped(rel, skip) {
			continue
		}
		target := filepath.Join(stage, filepath.FromSlash(rel))
		mode := f.Mode()
		switch {
		case mode.IsDir() || strings.HasSuffix(f.Name, "/"):
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		case !mode.IsRegular():
			return fmt.Errorf("archive entry %q is not a plain file", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		n, err := writeZipFile(f, target, maxUnpackBytes-written)
		if err != nil {
			return err
		}
		written += n
	}
	for _, e := range expect {
		if fi, err := os.Stat(filepath.Join(stage, filepath.FromSlash(e))); err != nil || fi.IsDir() {
			return fmt.Errorf("the archive does not contain %s", e)
		}
	}
	// The old install is moved aside, never deleted first: when the new tree
	// cannot take its place the old one goes back.
	backup := ""
	if _, serr := os.Lstat(dest); serr == nil {
		b, err := os.MkdirTemp(parent, ".old-"+base+"-")
		if err != nil {
			return err
		}
		backup = filepath.Join(b, base)
		if err := rename(dest, backup); err != nil {
			os.RemoveAll(b)
			return fmt.Errorf("cannot replace %s (in use?): %w", dest, err)
		}
	}
	// A virus scanner may still hold a freshly written file for a moment.
	for try := 0; ; try++ {
		if err = rename(stage, dest); err == nil || try >= 5 {
			break
		}
		time.Sleep(time.Duration(try+1) * unpackRetryStep)
	}
	if err != nil {
		if backup != "" {
			var rerr error
			for try := 0; ; try++ { // the same scanner may hold the old tree
				if rerr = rename(backup, dest); rerr == nil || try >= 5 {
					break
				}
				time.Sleep(time.Duration(try+1) * unpackRetryStep)
			}
			if rerr != nil {
				return fmt.Errorf("%w; the previous install is kept in %s (could not be put back: %v)", err, backup, rerr)
			}
			os.RemoveAll(filepath.Dir(backup))
		}
		return err
	}
	done = true
	if backup != "" {
		os.RemoveAll(filepath.Dir(backup)) // best-effort: a leftover dot folder is ignored by tool lookup
	}
	return nil
}

var unpackRetryStep = 300 * time.Millisecond

// winReserved are the Win32 device names: a file called NUL, COM1.txt …
// opens the device, not a file.
var winReserved = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true, "CLOCK$": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true}

// unsafeSegment: a path component Windows would resolve to something else
// than its spelling — ".." in disguise ("..  ", "..."; Win32 drops trailing
// dots and spaces) or a device name.
func unsafeSegment(seg string) bool {
	if seg == ".." {
		return true
	}
	if seg != "." && seg != strings.TrimRight(seg, ". ") {
		return true
	}
	stem, _, _ := strings.Cut(seg, ".")
	stem = strings.ToUpper(strings.TrimSpace(stem))
	// COM¹ … LPT³ (superscript digits) are devices too
	stem = strings.NewReplacer("¹", "1", "²", "2", "³", "3").Replace(stem)
	return winReserved[stem]
}

// cleanZipName returns the slash-separated, cleaned entry name, refusing any
// name that could land outside the destination.
func cleanZipName(name string) (string, error) {
	n := strings.ReplaceAll(name, `\`, "/")
	if n == "" || strings.HasPrefix(n, "/") || strings.Contains(n, ":") || strings.ContainsRune(n, 0) {
		return "", fmt.Errorf("unsafe path in archive: %q", name)
	}
	for _, seg := range strings.Split(n, "/") {
		if unsafeSegment(seg) {
			return "", fmt.Errorf("unsafe path in archive: %q", name)
		}
	}
	n = path.Clean(n)
	if n == "." {
		return "", fmt.Errorf("unsafe path in archive: %q", name)
	}
	return n, nil
}

// commonTopDir is the first path segment shared by every entry when the
// archive wraps everything in one folder, else "".
func commonTopDir(names []string) string {
	top := ""
	nested := false
	for _, n := range names {
		seg, _, has := strings.Cut(n, "/")
		if top == "" {
			top = seg
		} else if seg != top {
			return ""
		}
		if has {
			nested = true
		}
	}
	if !nested {
		return ""
	}
	return top
}

func skipped(rel string, skip []string) bool {
	for _, s := range skip {
		s = strings.Trim(s, "/")
		if s != "" && (rel == s || strings.HasPrefix(rel, s+"/")) {
			return true
		}
	}
	return false
}

func writeZipFile(f *zip.File, target string, budget int64) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(rc, budget+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > budget {
		err = errors.New("archive is larger than expected")
	}
	return n, err
}
