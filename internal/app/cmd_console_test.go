package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/console"
	"github.com/zhuangzard/pcbpilot/pkg/projectconfig"
)

func runCLI(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb)
	return out.String(), errb.String(), code
}

func TestProjectConfigCLI(t *testing.T) {
	t.Setenv("PCBPILOT_HOME", t.TempDir())
	dir := t.TempDir()
	if _, e, code := runCLI(t, "project-config", "init", "--dir", dir, "--template", "quick-proto", "--name", "demo", "--eda-project", "ceshi"); code != 0 {
		t.Fatalf("init: %s", e)
	}
	if _, _, code := runCLI(t, "project-config", "init", "--dir", dir); code == 0 {
		t.Fatal("second init must refuse")
	}
	dirs, _ := console.LoadWorkDirs(console.Home())
	if len(dirs) != 1 {
		t.Fatalf("init must register the work dir: %+v", dirs)
	}
	if _, e, code := runCLI(t, "project-config", "set", "--dir", dir, "--step", "P9=off", "--reason", "无丝印要求", "--add-standard", "IPC-2221B", "--fab", "jlcpcb", "--layers", "4", "--prefer", "C6186"); code != 0 {
		t.Fatalf("set: %s", e)
	}
	c, err := projectconfig.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.StepEnabled("P9") || c.Steps["P9"].Reason != "无丝印要求" || c.Constraints.Fab.Layers != 4 || c.Constraints.Standards[0] != "IPC-2221B" {
		t.Fatalf("set not applied: %+v", c)
	}
	// A guard violation is refused and leaves the file unchanged.
	if _, _, code := runCLI(t, "project-config", "set", "--dir", dir, "--step", "P10=off"); code == 0 {
		t.Fatal("skipping P10 while routing must be refused")
	}
	if _, _, code := runCLI(t, "project-config", "set", "--dir", dir, "--step", "P99=off"); code == 0 {
		t.Fatal("unknown step must be refused")
	}
	out, _, code := runCLI(t, "project-config", "validate", "--dir", dir)
	if code != 0 || !strings.Contains(out, "SEVERITY") {
		t.Fatalf("validate: %d %s", code, out)
	}
	out, _, _ = runCLI(t, "project-config", "show", "--dir", dir)
	var shown struct {
		SkippedSteps []projectconfig.Skip `json:"skippedSteps"`
	}
	if err := json.Unmarshal([]byte(out), &shown); err != nil || len(shown.SkippedSteps) != 3 {
		t.Fatalf("show: %v %s", err, out)
	}
}

func TestKBCLI(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "notes.md")
	os.WriteFile(src, []byte("# USB\n\nUSB-C CC pins need 5.1k pulldown resistors.\n"), 0o644)
	if _, e, code := runCLI(t, "kb", "add", src, "--dir", dir, "--kind", "reference-design", "--tag", "usb"); code != 0 {
		t.Fatalf("add: %s", e)
	}
	out, _, code := runCLI(t, "kb", "search", "pulldown resistors", "--dir", dir, "--json")
	if code != 0 {
		t.Fatal(out)
	}
	var res struct {
		Hits []struct{ DocID, Cite string } `json:"hits"`
	}
	json.Unmarshal([]byte(out), &res)
	if len(res.Hits) != 1 {
		t.Fatalf("search: %s", out)
	}
	id := res.Hits[0].DocID
	if _, e, code := runCLI(t, "kb", "set-summary", id, "--dir", dir, "--text", "USB-C sink needs Rd=5.1k on CC1/CC2", "--by", "test"); code != 0 {
		t.Fatal(e)
	}
	out, _, _ = runCLI(t, "kb", "show", id, "--dir", dir, "--chunk", "0")
	if !strings.Contains(out, "Rd=5.1k") || !strings.Contains(out, "5.1k pulldown") {
		t.Fatalf("show: %s", out)
	}
	out, _, _ = runCLI(t, "kb", "summarize-status", "--dir", dir)
	if !strings.Contains(out, `"summarized": 1`) {
		t.Fatalf("summarize-status: %s", out)
	}
}

func TestAskTerminalFallback(t *testing.T) {
	req := console.AskRequest{Question: "进入布线？", Options: []console.Option{{ID: "ok", Label: "确认"}, {ID: "adjust", Label: "调整"}}, Default: "adjust"}
	var out bytes.Buffer
	q, err := askInTerminal(req, strings.NewReader("bogus\nok\n"), &out)
	if err != nil || q.Answer.Choice != "ok" || q.Answer.By != "cli" {
		t.Fatalf("answer: %+v %v", q, err)
	}
	if !strings.Contains(out.String(), "not an option") {
		t.Fatal("invalid input must be re-prompted")
	}
	q, _ = askInTerminal(req, strings.NewReader("\n"), &out)
	if q.Answer.Choice != "adjust" {
		t.Fatalf("empty input takes the default: %+v", q.Answer)
	}
	if _, err := askInTerminal(req, strings.NewReader(""), &out); err == nil {
		t.Fatal("closed stdin must error")
	}
}

func TestAskWithoutDaemonAndNoTerminal(t *testing.T) {
	t.Setenv("PCBPILOT_HOME", t.TempDir()) // no token → console unreachable
	_, e, code := runCLI(t, "ask", "--question", "x", "--option", "a=A", "--fallback", "none", "--ports", "61899-61899")
	if code != askExitUnavailable || !strings.Contains(e, "console unreachable") {
		t.Fatalf("code %d: %s", code, e)
	}
}

func TestConsoleURLCarriesTokenInFragment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PCBPILOT_HOME", home)
	out, _, code := runCLI(t, "console", "url", "--ports", "61840-61841")
	if code != 0 || !strings.HasPrefix(out, "http://127.0.0.1:61840/ui/#token=") {
		t.Fatalf("url: %q", out)
	}
	st, err := os.Stat(filepath.Join(home, console.TokenFile))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", st, err)
	}
}
