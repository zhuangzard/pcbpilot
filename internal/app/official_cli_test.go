package app

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// fakeOfficialEnv builds a host view from a file map; run records every call.
type fakeOfficial struct {
	files map[string]string
	path  map[string]string
	calls []string
	out   string
	err   error
	sleep time.Duration
}

func (f *fakeOfficial) env(goos string) officialCLIEnv {
	return officialCLIEnv{
		goos: goos,
		lookPath: func(n string) (string, error) {
			if p, ok := f.path[n]; ok {
				return p, nil
			}
			return "", exec.ErrNotFound
		},
		exists: func(p string) bool { _, ok := f.files[p]; return ok },
		readFile: func(p string) ([]byte, error) {
			if b, ok := f.files[p]; ok {
				return []byte(b), nil
			}
			return nil, errors.New("not found")
		},
		resolve: func(p string) (string, error) { return p, nil },
		run: func(ctx context.Context, path string, args ...string) ([]byte, error) {
			f.calls = append(f.calls, path+" "+strings.Join(args, " "))
			if f.sleep > 0 {
				select {
				case <-time.After(f.sleep):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return []byte(f.out), f.err
		},
		getenv:  func(string) string { return "" },
		timeout: 50 * time.Millisecond,
		locations: []officialCLILocation{
			{"easyeda-pro", "/Applications/EasyEDA-Pro.app", "observed"},
			{"easyeda-pro", `C:\Program Files\EasyEDA-Pro\easyeda-pro.exe`, "documented"},
		},
	}
}

func TestOfficialCLINotInstalled(t *testing.T) {
	f := &fakeOfficial{}
	rep := detectOfficialCLI(context.Background(), f.env("darwin"))
	if rep.Status != "not-installed" || rep.Summary != "not installed (requires EasyEDA Pro desktop V4.1.60+)" || len(f.calls) != 0 {
		t.Fatalf("rep = %+v calls=%v", rep, f.calls)
	}
	if !strings.Contains(rep.WritePath, "not a pcbpilot write path") {
		t.Errorf("write policy missing: %q", rep.WritePath)
	}
}

// The real macOS layout seen on this machine: a 3.2.149 app bundle. It must be
// recognised from metadata and never executed.
func TestOfficialCLIOldMacBundleNotExecuted(t *testing.T) {
	f := &fakeOfficial{files: map[string]string{
		"/Applications/EasyEDA-Pro.app":                                     "",
		"/Applications/EasyEDA-Pro.app/Contents/Resources/app/package.json": `{"version":"3.2.149.88089769"}`,
	}}
	rep := detectOfficialCLI(context.Background(), f.env("darwin"))
	if rep.Status != "present-unsupported" || rep.Version != "3.2.149.88089769" || len(f.calls) != 0 {
		t.Fatalf("rep = %+v calls=%v", rep, f.calls)
	}
	if c := rep.Candidates[0]; c.Found != "default-location" || c.Provenance != "observed" || !strings.HasSuffix(c.VersionSource, "package.json") {
		t.Errorf("candidate = %+v", c)
	}
}

func TestOfficialCLIPlistFallbackAndMacNeverProbed(t *testing.T) {
	f := &fakeOfficial{files: map[string]string{
		"/Applications/EasyEDA-Pro.app":                     "",
		"/Applications/EasyEDA-Pro.app/Contents/Info.plist": "<dict><key>CFBundleShortVersionString</key>\n\t<string>4.1.60</string></dict>",
	}, path: map[string]string{"easyeda-pro": "/Applications/EasyEDA-Pro.app/Contents/MacOS/easyeda-pro"}}
	rep := detectOfficialCLI(context.Background(), f.env("darwin"))
	if rep.Status != "present-not-probed" || rep.Version != "4.1.60" || len(f.calls) != 0 {
		t.Fatalf("rep = %+v calls=%v", rep, f.calls)
	}
	if !strings.Contains(rep.NotProbedReason, "Windows client only") || !strings.HasPrefix(rep.Summary, "present (not probed)") {
		t.Errorf("reason = %q summary = %q", rep.NotProbedReason, rep.Summary)
	}
	// PATH and bundle resolve to two different paths here; both are listed.
	if len(rep.Candidates) != 2 || rep.Candidates[0].Found != "PATH" {
		t.Errorf("candidates = %+v", rep.Candidates)
	}
}

func TestOfficialCLIUnknownVersionNotExecuted(t *testing.T) {
	f := &fakeOfficial{path: map[string]string{"lceda-pro": `D:\tools\lceda-pro.exe`}}
	rep := detectOfficialCLI(context.Background(), f.env("windows"))
	if rep.Status != "present-not-probed" || len(f.calls) != 0 || !strings.Contains(rep.NotProbedReason, "version unreadable") {
		t.Fatalf("rep = %+v calls=%v", rep, f.calls)
	}
}

const winExe = `C:\Program Files\EasyEDA-Pro\easyeda-pro.exe`

func winSupported(out string) *fakeOfficial {
	return &fakeOfficial{files: map[string]string{
		winExe: "",
		`C:\Program Files\EasyEDA-Pro\resources\app\package.json`: `{"version":"4.1.62.abcdef01"}`,
	}, out: out}
}

func TestOfficialCLIWindowsDoctorProbed(t *testing.T) {
	f := winSupported(`{"ok":true,"value":{"endpoint":"EasyEDAProf126dbc1","connected":false,"bridgeVersion":null,"versionMatch":null,"resultChannel":null},"logs":[],"durationMs":207}`)
	rep := detectOfficialCLI(context.Background(), f.env("windows"))
	if rep.Status != "probed" || rep.Doctor == nil || !rep.Doctor.OK || rep.Doctor.Endpoint != "EasyEDAProf126dbc1" ||
		rep.Doctor.Connected == nil || *rep.Doctor.Connected {
		t.Fatalf("rep = %+v doctor=%+v", rep, rep.Doctor)
	}
	// doctor is the only command ever executed.
	if len(f.calls) != 1 || f.calls[0] != winExe+" doctor" {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestOfficialCLIDoctorFailuresNeverPanic(t *testing.T) {
	for name, f := range map[string]*fakeOfficial{
		"not json":   winSupported("EasyEDA Pro 4.1.62"),
		"ok false":   winSupported(`{"ok":false,"error":{"code":"E_BRIDGE","message":"no bridge"}}`),
		"exec error": func() *fakeOfficial { x := winSupported(""); x.err = errors.New("boom"); return x }(),
		"timeout":    func() *fakeOfficial { x := winSupported("{}"); x.sleep = time.Second; return x }(),
	} {
		rep := detectOfficialCLI(context.Background(), f.env("windows"))
		if rep.Status != "probe-failed" || rep.Doctor == nil || rep.Doctor.Error == "" {
			t.Errorf("%s: rep = %+v doctor = %+v", name, rep, rep.Doctor)
		}
	}
}

func TestOfficialCLIProbeOptOut(t *testing.T) {
	f := winSupported(`{"ok":true,"value":{}}`)
	env := f.env("windows")
	env.getenv = func(k string) string {
		if k == "PCBPILOT_OFFICIAL_CLI_PROBE" {
			return "0"
		}
		return ""
	}
	rep := detectOfficialCLI(context.Background(), env)
	if rep.Status != "present-not-probed" || len(f.calls) != 0 {
		t.Fatalf("rep = %+v calls = %v", rep, f.calls)
	}
}

func TestVersionAtLeast(t *testing.T) {
	for _, c := range []struct {
		v    string
		want bool
	}{{"4.1.60", true}, {"4.1.60.abcdef01", true}, {"4.2.0", true}, {"5.0.0", true}, {"4.1.59", false}, {"3.2.149.88089769", false}, {"", false}, {"4.1", false}, {"x.y.z", false}} {
		if got := versionAtLeast(c.v, "4.1.60"); got != c.want {
			t.Errorf("versionAtLeast(%q) = %v", c.v, got)
		}
	}
}
