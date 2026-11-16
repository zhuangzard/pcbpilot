package simtools

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fake is a scripted machine: bins on PATH, probe outputs, recorded installs.
type fake struct {
	bins     map[string]string // name → path
	outputs  map[string]string // "name args" → output
	failOut  map[string]bool   // probe commands that fail
	ran      [][]string
	failRun  map[string]bool // argv[0] (after sudo) that fails
	installs map[string][]string
	globs    map[string][]string
	unpacked [][2]string // archive, dest
	removed  []string
	sha      string // digest the fake reports for any download
}

func newFake(goos string) (*Env, *fake) {
	f := &fake{bins: map[string]string{}, outputs: map[string]string{}, failOut: map[string]bool{},
		failRun: map[string]bool{}, installs: map[string][]string{}, globs: map[string][]string{}}
	e := &Env{
		GOOS: goos, OSRelease: map[string]string{},
		LookPath: func(n string) (string, error) {
			if p, ok := f.bins[n]; ok {
				return p, nil
			}
			return "", exec.ErrNotFound
		},
		Output: func(name string, args ...string) (string, error) {
			k := strings.TrimSpace(name + " " + strings.Join(args, " "))
			if f.failOut[k] {
				return f.outputs[k], errors.New("exit 1")
			}
			return f.outputs[k], nil
		},
		Stream: func(argv []string) error {
			f.ran = append(f.ran, argv)
			a := argv[0]
			if a == "sudo" {
				a = argv[1]
			}
			if f.failRun[a] {
				return errors.New("exit 1")
			}
			// "installing" puts the binaries a formula provides on PATH
			for _, b := range f.installs[strings.Join(argv, " ")] {
				f.bins[b] = "/usr/bin/" + b
			}
			return nil
		},
		Download: func(url string) (string, error) { return `C:\Temp\elmer.zip`, nil },
		Home:     func() (string, error) { return `C:\Users\u`, nil },
		SHA256:   func(string) (string, error) { return f.sha, nil },
		Unpack: func(archive, dest string, skip, expect []string) error {
			f.unpacked = append(f.unpacked, [2]string{archive, dest})
			return nil
		},
		Remove: func(p string) error { f.removed = append(f.removed, p); return nil },
		Glob:   func(p string) ([]string, error) { return f.globs[p], nil },
		Getenv: func(k string) string {
			return map[string]string{"ProgramFiles": `C:\Program Files`}[k]
		},
	}
	return e, f
}

func tool(t *testing.T, name string) Tool {
	tl, ok := ToolByName(name)
	if !ok {
		t.Fatal(name)
	}
	return tl
}

func TestParseVersion(t *testing.T) {
	ng, el := Tools()[0], Tools()[1]
	cases := []struct {
		tl   Tool
		out  string
		want string
	}{
		{ng, "******\n** ngspice-47 : Circuit level simulation program\n", "47"},
		{ng, "ngspice-36 : Circuit level simulation program", "36"},
		{ng, "** ngspice-44.2 : Circuit", "44.2"},
		{ng, "nothing here", ""},
		{el, "ELMER SOLVER (v 26.1) STARTED AT: 2026/09/28", "26.1"},
		{el, " MAIN: Version: 9.0 (Rev: Release, Compiled: 2021-11-19)", "9.0"},
		{el, "garbage", ""},
	}
	for _, c := range cases {
		if got := ParseVersion(c.tl, c.out); got != c.want {
			t.Errorf("ParseVersion(%s, %q) = %q, want %q", c.tl.Name, c.out, got, c.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"47", "30", 1}, {"30", "30", 0}, {"29", "30", -1}, {"26.1", "9.0", 1}, {"8.4", "9.0", -1}, {"9", "9.0", 0}} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%s,%s)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCheckStatuses(t *testing.T) {
	e, f := newFake("darwin")
	f.bins["brew"] = "/opt/homebrew/bin/brew"
	f.bins["ngspice"] = "/opt/homebrew/bin/ngspice"
	f.outputs["/opt/homebrew/bin/ngspice -v"] = "** ngspice-47 : Circuit level"
	rep := e.Check()
	if !rep.OK || rep.Tools[0].Status != StatusOK || rep.Tools[0].Version != "47" {
		t.Fatalf("ngspice should be ok: %+v", rep.Tools[0])
	}
	if rep.Tools[1].Status != StatusMissing || rep.Tools[1].Required {
		t.Fatalf("elmer should be optional+missing: %+v", rep.Tools[1])
	}
	if !reflect.DeepEqual(rep.Tools[1].Install, []string{"brew tap elmercsc/elmerfem", "brew install elmercsc/elmerfem/elmer"}) {
		t.Fatalf("elmer install cmds: %v", rep.Tools[1].Install)
	}
	if rep.Builtin == "" || !strings.Contains(rep.Builtin, "built into the binary") {
		t.Fatal("report must say the own simulators are built in")
	}

	// outdated ngspice → not OK; version from the --version fallback
	f.outputs["/opt/homebrew/bin/ngspice -v"] = ""
	f.outputs["/opt/homebrew/bin/ngspice --version"] = "ngspice-27"
	rep = e.Check()
	if rep.OK || rep.Tools[0].Status != StatusOutdated || rep.Tools[0].Version != "27" {
		t.Fatalf("want outdated 27: %+v", rep.Tools[0])
	}
	// Elmer needs BOTH binaries
	delete(f.outputs, "/opt/homebrew/bin/ngspice --version")
	f.bins["ElmerSolver"] = "/opt/homebrew/bin/ElmerSolver"
	r := e.CheckTool(tool(t, "elmer"))
	if r.Status != StatusMissing || !strings.Contains(r.Detail, "ElmerGrid") {
		t.Fatalf("ElmerGrid missing should be missing: %+v", r)
	}
	f.bins["ElmerGrid"] = "/opt/homebrew/bin/ElmerGrid"
	f.outputs["/opt/homebrew/bin/ElmerSolver -v"] = "ELMER SOLVER (v 26.2) STARTED"
	f.failOut["/opt/homebrew/bin/ElmerSolver -v"] = true // non-zero exit still parses
	r = e.CheckTool(tool(t, "elmer"))
	if r.Status != StatusOK || r.Version != "26.2" {
		t.Fatalf("elmer ok 26.2: %+v", r)
	}
}

func cmds(p Plan) string { return strings.Join(p.Commands(), " | ") }

func TestPlanPerPlatform(t *testing.T) {
	ng, el := Tools()[0], Tools()[1]

	e, f := newFake("darwin")
	if p := e.Plan(ng, false); p.Blocker == "" || !strings.Contains(p.Blocker, "brew.sh") {
		t.Fatalf("no brew → blocker with how-to, got %+v", p)
	}
	f.bins["brew"] = "/b/brew"
	if got := cmds(e.Plan(ng, false)); got != "brew install ngspice" {
		t.Fatal(got)
	}
	if got := cmds(e.Plan(ng, true)); got != "brew upgrade ngspice" {
		t.Fatal(got)
	}
	if p := e.Plan(el, false); p.Warning == "" || cmds(p) != "brew tap elmercsc/elmerfem | brew install elmercsc/elmerfem/elmer" {
		t.Fatalf("%+v", p)
	}

	// Ubuntu, non-root, add-apt-repository missing → software-properties-common first
	e, f = newFake("linux")
	e.OSRelease = map[string]string{"ID": "ubuntu", "UBUNTU_CODENAME": "noble"}
	if got := cmds(e.Plan(ng, false)); got != "sudo apt-get update | sudo apt-get install -y ngspice" {
		t.Fatal(got)
	}
	if got := cmds(e.Plan(el, false)); got != "sudo apt-get install -y software-properties-common | sudo add-apt-repository -y ppa:elmer-csc-ubuntu/elmer-csc-ppa | sudo apt-get install -y elmerfem-csc" {
		t.Fatal(got)
	}
	f.bins["add-apt-repository"] = "/usr/bin/add-apt-repository"
	e.IsRoot = true
	if got := cmds(e.Plan(el, false)); got != "add-apt-repository -y ppa:elmer-csc-ubuntu/elmer-csc-ppa | apt-get install -y elmerfem-csc" {
		t.Fatal(got)
	}
	// Linux Mint (Ubuntu-based) uses the PPA too
	e.OSRelease = map[string]string{"ID": "linuxmint", "ID_LIKE": "ubuntu debian", "UBUNTU_CODENAME": "noble"}
	if e.linuxFamily() != "ubuntu" {
		t.Fatal(e.linuxFamily())
	}
	// Debian: ngspice via apt, Elmer → source-build instructions
	e.OSRelease = map[string]string{"ID": "debian"}
	if got := cmds(e.Plan(ng, false)); got != "apt-get update | apt-get install -y ngspice" {
		t.Fatal(got)
	}
	if p := e.Plan(el, false); len(p.Steps) != 0 || p.Manual != ElmerSourceHelp {
		t.Fatalf("%+v", p)
	}
	// Fedora / Rocky
	e.IsRoot = false
	for _, rel := range []map[string]string{{"ID": "fedora"}, {"ID": "rocky", "ID_LIKE": "rhel centos fedora"}} {
		e.OSRelease = rel
		if got := cmds(e.Plan(ng, false)); got != "sudo dnf install -y ngspice" {
			t.Fatal(rel, got)
		}
		if p := e.Plan(el, false); len(p.Steps) != 0 {
			t.Fatal("elmer on fedora must be manual")
		}
	}
	// unknown distro
	e.OSRelease = map[string]string{"ID": "arch"}
	if p := e.Plan(ng, false); len(p.Steps) != 0 || p.Manual == "" {
		t.Fatalf("%+v", p)
	}

	// Windows: winget only when the id exists at run time, else choco, else manual
	e, f = newFake("windows")
	if p := e.Plan(ng, false); len(p.Steps) != 0 || p.Manual != NgspiceWindowsHelp {
		t.Fatalf("%+v", p)
	}
	f.bins["winget"] = `C:\winget.exe`
	f.failOut["winget show --id ngspice.ngspice -e --accept-source-agreements --disable-interactivity"] = true
	f.bins["choco"] = `C:\choco.exe`
	if got := cmds(e.Plan(ng, false)); !strings.HasPrefix(got, "choco install ngspice -y") {
		t.Fatal(got)
	}
	delete(f.failOut, "winget show --id ngspice.ngspice -e --accept-source-agreements --disable-interactivity")
	f.outputs["winget show --id ngspice.ngspice -e --accept-source-agreements --disable-interactivity"] = "Found ngspice [ngspice.ngspice]"
	if got := cmds(e.Plan(ng, false)); !strings.HasPrefix(got, "winget install --id ngspice.ngspice -e") {
		t.Fatal(got)
	}
	p := e.Plan(el, false)
	// Elmer: the official .zip, pinned by SHA-256, unpacked under the user's
	// own ~/.pcbpilot — never the installer, never elevation.
	if len(p.Steps) != 1 {
		t.Fatalf("%+v", p)
	}
	st := p.Steps[0]
	wantDir := filepath.Join(`C:\Users\u`, ".pcbpilot", "elmer", "rel"+ElmerWindowsRelease)
	if st.Download != ElmerWindowsURL || st.SHA256 != ElmerWindowsSHA256 || st.Unpack != wantDir || st.Admin || st.Sudo || len(st.Argv) != 0 {
		t.Fatalf("%+v", st)
	}
	if !strings.HasPrefix(ElmerWindowsURL, "https://www.nic.funet.fi/pub/sci/physics/elmer/bin/windows/") || !strings.HasSuffix(ElmerWindowsURL, ".zip") {
		t.Fatalf("Elmer must come from the official mirror as a .zip: %s", ElmerWindowsURL)
	}
	if len(ElmerWindowsSHA256) != 64 {
		t.Fatal("the Elmer archive must be pinned by a full SHA-256")
	}
	line := st.String()
	if strings.Contains(line, "/S") || strings.Contains(line, "administrator approval") || !strings.Contains(line, "no administrator rights") {
		t.Fatal(line)
	}
	if !strings.Contains(p.Manual, "nic.funet.fi") || !strings.Contains(p.Manual, `.pcbpilot\elmer`) || p.Warning != "" {
		t.Fatalf("elmer windows needs the exact manual step, no elevation warning: %+v", p)
	}
	// no home folder → the manual step only, nothing guessed
	e.Home = func() (string, error) { return "", errors.New("no home") }
	if p := e.Plan(el, false); len(p.Steps) != 0 || !strings.Contains(p.Manual, "nic.funet.fi") {
		t.Fatalf("%+v", p)
	}
}

func TestWindowsLocateOutsidePath(t *testing.T) {
	e, f := newFake("windows")
	f.globs[`C:\Program Files\Elmer*\bin`] = []string{`C:\Program Files\Elmer 9.0-Release\bin`, `C:\Program Files\Elmer 26.1-Release\bin`}
	f.globs[`C:\Program Files\Elmer 26.1-Release\bin\ElmerSolver.exe`] = []string{`C:\Program Files\Elmer 26.1-Release\bin\ElmerSolver.exe`}
	f.globs[`C:\Program Files\Elmer 26.1-Release\bin\ElmerGrid.exe`] = []string{`C:\Program Files\Elmer 26.1-Release\bin\ElmerGrid.exe`}
	f.outputs[`C:\Program Files\Elmer 26.1-Release\bin\ElmerSolver.exe -v`] = "ELMER SOLVER (v 26.1) STARTED"
	r := e.CheckTool(tool(t, "elmer"))
	if r.Status != StatusOK || r.OnPath || !strings.Contains(r.Detail, "outside PATH") || !strings.Contains(r.Path, "26.1") {
		t.Fatalf("%+v", r)
	}
	// ngspice console build name is preferred on Windows
	f.bins["ngspice_con"] = `C:\Spice64\bin\ngspice_con.exe`
	if p, on := e.Locate(tool(t, "ngspice"), "ngspice"); !on || !strings.HasSuffix(p, "ngspice_con.exe") {
		t.Fatal(p)
	}
}

func TestInstallDryRunRunsNothing(t *testing.T) {
	e, f := newFake("darwin")
	f.bins["brew"] = "/b/brew"
	var buf bytes.Buffer
	outs, err := e.Install(&buf, InstallOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry-run must exit 0: %v", err)
	}
	if len(f.ran) != 0 {
		t.Fatalf("dry-run ran %v", f.ran)
	}
	if outs[0].Action != "planned" || !strings.Contains(buf.String(), "$ brew install ngspice") || !strings.Contains(buf.String(), "built into the binary") {
		t.Fatalf("%v\n%s", outs, buf.String())
	}
}

func TestInstallRequiredVsOptional(t *testing.T) {
	e, f := newFake("darwin")
	f.bins["brew"] = "/b/brew"
	f.installs["brew install ngspice"] = []string{"ngspice"}
	f.failRun["brew"] = false
	// Elmer's formula fails; ngspice installs → exit 0 with a warning
	f.installs["brew install elmercsc/elmerfem/elmer"] = nil
	var buf bytes.Buffer
	outs, err := e.Install(&buf, InstallOptions{Yes: true})
	if err != nil {
		t.Fatalf("optional elmer failure must not fail: %v\n%s", err, buf.String())
	}
	if outs[0].After != StatusOK || outs[0].Action != "installed" {
		t.Fatalf("%+v", outs[0])
	}
	if outs[1].Action != "failed" || !strings.Contains(buf.String(), "(optional)") {
		t.Fatalf("%+v\n%s", outs[1], buf.String())
	}
	want := [][]string{{"brew", "install", "ngspice"}, {"brew", "tap", "elmercsc/elmerfem"}, {"brew", "install", "elmercsc/elmerfem/elmer"}}
	if !reflect.DeepEqual(f.ran, want) {
		t.Fatalf("ran %v", f.ran)
	}
	// --require-elmer turns the same failure into a non-zero exit
	f.ran = nil
	if _, err := e.Install(&buf, InstallOptions{Yes: true, RequireElmer: true}); !errors.Is(err, ErrRequired) {
		t.Fatalf("want ErrRequired, got %v", err)
	}
	// ngspice already ok → nothing runs for it
	f.ran = nil
	if _, err := e.Install(&buf, InstallOptions{Yes: true, Only: "ngspice"}); err != nil || len(f.ran) != 0 {
		t.Fatalf("%v %v", err, f.ran)
	}
}

func TestInstallRequiredFailure(t *testing.T) {
	e, f := newFake("darwin") // no Homebrew
	var buf bytes.Buffer
	if _, err := e.Install(&buf, InstallOptions{Yes: true, Only: "ngspice"}); !errors.Is(err, ErrRequired) {
		t.Fatalf("missing brew + missing ngspice must fail: %v", err)
	}
	if len(f.ran) != 0 || !strings.Contains(buf.String(), "brew.sh") {
		t.Fatalf("must not install Homebrew itself: %v\n%s", f.ran, buf.String())
	}
	// not confirmed (no --yes, no TTY) → nothing runs; required still missing → non-zero
	f.bins["brew"] = "/b/brew"
	buf.Reset()
	if _, err := e.Install(&buf, InstallOptions{Only: "ngspice", Confirm: func(string) bool { return true }}); !errors.Is(err, ErrRequired) || len(f.ran) != 0 {
		t.Fatalf("%v %v", err, f.ran)
	}
	if !strings.Contains(buf.String(), "--yes") {
		t.Fatal(buf.String())
	}
	// install command fails
	f.failRun["brew"] = true
	if _, err := e.Install(&buf, InstallOptions{Yes: true, Only: "ngspice"}); !errors.Is(err, ErrRequired) {
		t.Fatal(err)
	}
}

func TestInstallLinuxSudo(t *testing.T) {
	e, f := newFake("linux")
	e.OSRelease = map[string]string{"ID": "ubuntu", "UBUNTU_CODENAME": "noble"}
	f.installs["sudo apt-get install -y ngspice"] = []string{"ngspice"}
	var buf bytes.Buffer
	// no sudo binary → print commands, don't run, required → error
	if _, err := e.Install(&buf, InstallOptions{Yes: true, Only: "ngspice"}); !errors.Is(err, ErrRequired) || len(f.ran) != 0 {
		t.Fatalf("%v %v", err, f.ran)
	}
	if !strings.Contains(buf.String(), "needs root") {
		t.Fatal(buf.String())
	}
	// sudo exists but would prompt in a non-interactive shell → same
	f.bins["sudo"] = "/usr/bin/sudo"
	f.failOut["sudo -n true"] = true
	if _, err := e.Install(&buf, InstallOptions{Yes: true, Only: "ngspice"}); !errors.Is(err, ErrRequired) || len(f.ran) != 0 {
		t.Fatalf("%v %v", err, f.ran)
	}
	// passwordless sudo → runs with sudo; a failing `apt-get update` is optional
	delete(f.failOut, "sudo -n true")
	if _, err := e.Install(&buf, InstallOptions{Yes: true, Only: "ngspice"}); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	want := [][]string{{"sudo", "apt-get", "update"}, {"sudo", "apt-get", "install", "-y", "ngspice"}}
	if !reflect.DeepEqual(f.ran, want) {
		t.Fatalf("ran %v", f.ran)
	}
}

func TestInstallWindowsElmerUnpacksPerUser(t *testing.T) {
	e, f := newFake("windows")
	f.sha = ElmerWindowsSHA256
	dest := filepath.Join(`C:\Users\u`, ".pcbpilot", "elmer", "rel"+ElmerWindowsRelease)
	e.Unpack = func(archive, d string, skip, expect []string) error {
		f.unpacked = append(f.unpacked, [2]string{archive, d})
		if !reflect.DeepEqual(skip, []string{"stripped_gfortran"}) || !reflect.DeepEqual(expect, []string{"bin/ElmerSolver.exe", "bin/ElmerGrid.exe"}) {
			t.Errorf("skip=%v expect=%v", skip, expect)
		}
		// the unpacked tree is what Locate finds — PATH stays untouched
		bin := filepath.Join(d, "bin")
		f.globs[filepath.Join(filepath.Dir(d), "*", "bin")] = []string{bin}
		for _, b := range []string{"ElmerSolver", "ElmerGrid"} {
			f.globs[filepath.Join(bin, b+".exe")] = []string{filepath.Join(bin, b+".exe")}
		}
		f.outputs[filepath.Join(bin, "ElmerSolver.exe")+" -v"] = "ELMER SOLVER (v 9.0) STARTED"
		return nil
	}
	var buf bytes.Buffer
	outs, err := e.Install(&buf, InstallOptions{Yes: true, Only: "elmer"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 0 {
		t.Fatalf("no program may be run to install Elmer on Windows (the installer needs elevation): %v", f.ran)
	}
	if !reflect.DeepEqual(f.unpacked, [][2]string{{`C:\Temp\elmer.zip`, dest}}) || !reflect.DeepEqual(f.removed, []string{`C:\Temp\elmer.zip`}) {
		t.Fatalf("unpacked=%v removed=%v", f.unpacked, f.removed)
	}
	if outs[0].Action != "installed" || outs[0].After != StatusOK || outs[0].Message != "" {
		t.Fatalf("%+v\n%s", outs, buf.String())
	}
	if strings.Contains(buf.String(), "administrator approval") || strings.Contains(buf.String(), "elevation") {
		t.Fatal(buf.String())
	}
	if r := e.CheckTool(tool(t, "elmer")); r.Status != StatusOK || r.OnPath || r.Version != "9.0" || !strings.HasPrefix(r.Path, dest) {
		t.Fatalf("%+v", r)
	}
}

// Every failure of the per-user install is one short line that names the
// manual step, and stays a warning (Elmer is optional).
func TestInstallWindowsElmerFailuresAreShortAndOptional(t *testing.T) {
	cases := map[string]func(e *Env, f *fake){
		"404": func(e *Env, f *fake) {
			e.Download = func(string) (string, error) { return "", errors.New("404") }
		},
		"does not match the pinned SHA-256": func(e *Env, f *fake) { f.sha = strings.Repeat("0", 64) },
		"disk full": func(e *Env, f *fake) {
			e.Unpack = func(string, string, []string, []string) error { return errors.New("disk full") }
		},
	}
	for want, breakIt := range cases {
		e, f := newFake("windows")
		f.sha = ElmerWindowsSHA256
		breakIt(e, f)
		var buf bytes.Buffer
		outs, err := e.Install(&buf, InstallOptions{Yes: true, Only: "elmer"})
		if err != nil {
			t.Fatalf("%s: optional Elmer must not fail the install: %v", want, err)
		}
		o := outs[0]
		if o.Action != "failed" || o.After != StatusMissing || !strings.Contains(o.Message, want) {
			t.Fatalf("%s: %+v", want, o)
		}
		if strings.Count(o.Message, "nic.funet.fi") != 1 || strings.Count(buf.String(), o.Message) != 1 {
			t.Fatalf("%s: the reason and the manual step must be printed once:\n%s", want, buf.String())
		}
		if want != "disk full" && len(f.unpacked) != 0 {
			t.Fatalf("%s: nothing may be unpacked: %v", want, f.unpacked)
		}
		if len(f.ran) != 0 {
			t.Fatalf("%s: ran %v", want, f.ran)
		}
	}
	// --require-elmer turns the same failure into an error
	e, f := newFake("windows")
	f.sha = "bad"
	if _, err := e.Install(&bytes.Buffer{}, InstallOptions{Yes: true, Only: "elmer", RequireElmer: true}); !errors.Is(err, ErrRequired) {
		t.Fatal(err)
	}
}

// A failed brew step on macOS names the Command Line Tools fix (live
// 2026-09-28: Elmer's source build stopped at outdated CLT).
func TestBrewFailureNamesToolchainFix(t *testing.T) {
	if !strings.Contains(brewToolchainHint, "xcode-select --install") || !strings.Contains(brewToolchainHint, "softwareupdate") {
		t.Fatal(brewToolchainHint)
	}
}
