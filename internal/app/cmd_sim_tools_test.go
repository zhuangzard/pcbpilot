package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/simtools"
)

// fakeSimEnv: macOS with Homebrew; bins lists what is on PATH.
func fakeSimEnv(bins map[string]string, ran *[][]string) func() *simtools.Env {
	return func() *simtools.Env {
		return &simtools.Env{
			GOOS: "darwin",
			LookPath: func(n string) (string, error) {
				if p, ok := bins[n]; ok {
					return p, nil
				}
				return "", exec.ErrNotFound
			},
			Output: func(name string, args ...string) (string, error) {
				if strings.HasSuffix(name, "ngspice") {
					return "** ngspice-47 : Circuit level simulation program", nil
				}
				return "", errors.New("no")
			},
			Stream: func(argv []string) error { *ran = append(*ran, argv); return nil },
			Glob:   func(string) ([]string, error) { return nil, nil },
			Getenv: func(string) string { return "" },
		}
	}
}

func TestSimToolsCheckCLI(t *testing.T) {
	var ran [][]string
	defer func(old func() *simtools.Env) { simToolsEnv = old }(simToolsEnv)
	simToolsEnv = fakeSimEnv(map[string]string{"brew": "/b/brew", "ngspice": "/b/ngspice"}, &ran)

	var out, errb bytes.Buffer
	if code := Run([]string{"sim", "tools", "check", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("ngspice ok, elmer missing (optional) → exit 0, got %d\n%s", code, errb.String())
	}
	var rep simtools.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.Tools[0].Name != "ngspice" || rep.Tools[0].Status != "ok" || rep.Tools[1].Status != "missing" {
		t.Fatalf("%+v", rep)
	}

	// required ngspice missing → exit 1, human output names the install command
	simToolsEnv = fakeSimEnv(map[string]string{"brew": "/b/brew"}, &ran)
	out.Reset()
	if code := Run([]string{"sim", "tools", "check"}, &out, &errb); code != 1 {
		t.Fatalf("want exit 1, got %d", code)
	}
	if !strings.Contains(out.String(), "install: brew install ngspice") || !strings.Contains(out.String(), "built into the binary") {
		t.Fatal(out.String())
	}
	if code := Run([]string{"sim", "tools", "check", "--only", "spice"}, &out, &errb); code != 1 {
		t.Fatal("bad --only must fail")
	}
}

func TestSimToolsInstallCLI(t *testing.T) {
	var ran [][]string
	defer func(old func() *simtools.Env) { simToolsEnv = old }(simToolsEnv)
	simToolsEnv = fakeSimEnv(map[string]string{"brew": "/b/brew", "ngspice": "/b/ngspice"}, &ran)

	var out, errb bytes.Buffer
	if code := Run([]string{"sim", "tools", "install", "--only", "elmer", "--dry-run"}, &out, &errb); code != 0 || len(ran) != 0 {
		t.Fatalf("dry-run: code %d ran %v", code, ran)
	}
	if !strings.Contains(out.String(), "$ brew tap elmercsc/elmerfem") {
		t.Fatal(out.String())
	}
	// --yes: elmer "installs" but is still absent → optional → exit 0 ...
	if code := Run([]string{"sim", "tools", "install", "--yes"}, &out, &errb); code != 0 || len(ran) != 2 {
		t.Fatalf("code %d ran %v", code, ran)
	}
	// ... and non-zero with --require-elmer
	if code := Run([]string{"sim", "tools", "install", "--yes", "--require-elmer"}, &out, &errb); code != 1 {
		t.Fatalf("code %d", code)
	}
}
