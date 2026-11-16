package simtools

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// zipEntry is one synthetic archive member; a name ending in "/" is a folder.
type zipEntry struct {
	name string
	body string
	mode os.FileMode
}

func makeZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeZip(t *testing.T, entries []zipEntry) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pkg.zip")
	if err := os.WriteFile(p, makeZip(t, entries), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A synthetic stand-in for the official package layout (no real binaries).
func fakeElmerZip() []zipEntry {
	const top = "ElmerFEM-nogui-nompi-Windows-AMD64/"
	return []zipEntry{
		{name: top + "bin/"},
		{name: top + "bin/ElmerSolver.exe", body: "solver"},
		{name: top + "bin/ElmerGrid.exe", body: "grid"},
		{name: top + "bin/libelmersolver.dll", body: "dll"},
		{name: top + "share/elmersolver/lib/HeatSolve.dll", body: "heat"},
		{name: top + "stripped_gfortran/bin/gfortran.exe", body: strings.Repeat("x", 4096)},
	}
}

var elmerExpect = []string{"bin/ElmerSolver.exe", "bin/ElmerGrid.exe"}

func TestUnpackZipStripsTopFolderAndSkips(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "elmer", "rel1")
	if err := UnpackZip(writeZip(t, fakeElmerZip()), dest, []string{"stripped_gfortran"}, elmerExpect); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"bin/ElmerSolver.exe", "bin/ElmerGrid.exe", "bin/libelmersolver.dll", "share/elmersolver/lib/HeatSolve.dll"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(f))); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "stripped_gfortran")); !os.IsNotExist(err) {
		t.Fatalf("skipped folder was unpacked: %v", err)
	}
	if l := leftovers(t, dest); len(l) != 0 {
		t.Fatalf("staging folder left behind: %v", l)
	}
	if got := ElmerHome(filepath.Join(dest, "bin", "ElmerSolver.exe")); got != dest {
		t.Fatalf("ElmerHome = %q, want %q", got, dest)
	}
	if got := ElmerHome(filepath.Join(dest, "share", "ElmerSolver.exe")); got != "" {
		t.Fatalf("ElmerHome outside bin = %q", got)
	}

	// A second unpack replaces the tree (stale files go away).
	stale := filepath.Join(dest, "bin", "stale.txt")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UnpackZip(writeZip(t, fakeElmerZip()), dest, nil, elmerExpect); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("old tree was not replaced")
	}
	if _, err := os.Stat(filepath.Join(dest, "stripped_gfortran", "bin", "gfortran.exe")); err != nil {
		t.Fatalf("without skip everything is unpacked: %v", err)
	}
}

// A wrong or hostile archive never replaces a working install and never
// writes outside the destination.
func TestUnpackZipRefusesBadArchives(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "elmer", "rel1")
	if err := UnpackZip(writeZip(t, fakeElmerZip()), dest, nil, elmerExpect); err != nil {
		t.Fatal(err)
	}
	bad := map[string][]zipEntry{
		"does not contain": {{name: "top/bin/other.exe", body: "x"}, {name: "top/readme.txt", body: "x"}},
		"unsafe path":      {{name: "top/bin/ElmerSolver.exe", body: "x"}, {name: "top/../../escape.txt", body: "x"}},
		"unsafe path in":   {{name: "/abs/ElmerSolver.exe", body: "x"}},
		`unsafe path in a`: {{name: `C:\Windows\evil.exe`, body: "x"}},
		"not a plain file": {{name: "top/bin/ElmerSolver.exe", body: "x"}, {name: "top/bin/ElmerGrid.exe", body: "x"}, {name: "top/link", body: "../../x", mode: os.ModeSymlink | 0o777}},
	}
	for want, entries := range bad {
		err := UnpackZip(writeZip(t, entries), dest, nil, elmerExpect)
		if err == nil || !strings.Contains(err.Error(), strings.TrimSuffix(want, " a")) {
			t.Fatalf("%s: err = %v", want, err)
		}
		if b, rerr := os.ReadFile(filepath.Join(dest, "bin", "ElmerSolver.exe")); rerr != nil || string(b) != "solver" {
			t.Fatalf("%s: the existing install was damaged: %v %q", want, rerr, b)
		}
		if l := leftovers(t, dest); len(l) != 0 {
			t.Fatalf("%s: staging folder left behind", want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "escape.txt")); !os.IsNotExist(err) {
		t.Fatal("an entry escaped the destination")
	}
	// not a zip at all (e.g. an HTML error page saved by the download)
	junk := filepath.Join(t.TempDir(), "junk.zip")
	if err := os.WriteFile(junk, []byte("<html>404</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UnpackZip(junk, dest, nil, elmerExpect); err == nil || !strings.Contains(err.Error(), "not a readable .zip") {
		t.Fatalf("err = %v", err)
	}
}

// End to end on a real folder, with the archive served by a local test
// server (never the network): download → SHA-256 → unpack under
// <home>/.pcbpilot/elmer → found by the check without PATH.
func TestWindowsElmerPerUserInstallEndToEnd(t *testing.T) {
	archive := makeZip(t, fakeElmerZip())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rel/elmer.zip" {
			http.NotFound(w, r)
			return
		}
		w.Write(archive)
	}))
	defer srv.Close()

	home := t.TempDir()
	e, f := newFake("windows")
	e.Home = func() (string, error) { return home, nil }
	e.Glob = filepath.Glob
	e.SHA256 = FileSHA256
	e.Unpack = UnpackZip
	var temp string
	e.Download = func(url string) (string, error) {
		resp, err := http.Get(url)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		out, err := os.CreateTemp(t.TempDir(), "dl-*"+downloadExt(url))
		if err != nil {
			return "", err
		}
		defer out.Close()
		_, err = io.Copy(out, resp.Body)
		temp = out.Name()
		return temp, err
	}
	e.Remove = os.Remove
	el := tool(t, "elmer")
	dest := filepath.Join(e.UserToolDir(el), "rel-test")
	f.outputs[filepath.Join(dest, "bin", "ElmerSolver.exe")+" -v"] = "ELMER SOLVER (v 9.0) STARTED"

	sum, err := FileSHA256(writeZip(t, fakeElmerZip()))
	if err != nil {
		t.Fatal(err)
	}
	step := Step{Download: srv.URL + "/rel/elmer.zip", Unpack: dest, Skip: []string{"stripped_gfortran"}, Expect: elmerExpect}

	// a download that does not match the pin is refused and leaves nothing
	step.SHA256 = strings.Repeat("0", 64)
	var buf bytes.Buffer
	if err := e.runUnpack(&buf, step); err == nil || !strings.Contains(err.Error(), "pinned SHA-256") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("a download with the wrong SHA-256 was unpacked")
	}
	if r := e.CheckTool(el); r.Status != StatusMissing {
		t.Fatalf("%+v", r)
	}

	// a 404 from the server is a download error, nothing unpacked
	miss := step
	miss.Download = srv.URL + "/gone.zip"
	miss.SHA256 = sum
	if err := e.runUnpack(&buf, miss); err == nil {
		t.Fatal("a 404 page must not be accepted")
	}

	// the right archive installs
	step.SHA256 = strings.ToUpper(sum) // hex case does not matter
	if err := e.runUnpack(&buf, step); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Fatal("the downloaded archive was not removed")
	}
	r := e.CheckTool(el)
	if r.Status != StatusOK || r.OnPath || r.Version != "9.0" {
		t.Fatalf("%+v", r)
	}
	for _, b := range []string{"ElmerSolver", "ElmerGrid"} {
		if want := filepath.Join(dest, "bin", b+".exe"); r.Paths[b] != want {
			t.Fatalf("%s at %q, want %q", b, r.Paths[b], want)
		}
	}
	if len(f.ran) != 0 {
		t.Fatalf("nothing may be executed: %v", f.ran)
	}
	// everything written is inside <home>/.pcbpilot/elmer
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".pcbpilot" {
		t.Fatalf("home holds %v (%v)", entries, err)
	}
}

func TestDownloadExt(t *testing.T) {
	for url, want := range map[string]string{
		ElmerWindowsURL:                     ".zip",
		"https://example.invalid/a/b.EXE":   ".exe",
		"https://example.invalid/a/b":       ".bin",
		"https://example.invalid/a.zip?x=1": ".zip",
	} {
		if got := downloadExt(url); got != want {
			t.Errorf("%s → %q, want %q", url, got, want)
		}
	}
}
