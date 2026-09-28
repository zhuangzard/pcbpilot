// Package simtools locates, version-checks and installs the external,
// open-source simulators pcbpilot cross-checks against:
//
//	ngspice       circuit simulator (sim power --spice-check, analog SPICE flow) — required
//	Elmer FEM     ElmerSolver + ElmerGrid (thermal cross-check of sim post-layout) — optional
//
// pcbpilot's own simulators (DC power-tree MNA, trace IR/thermal estimates) are
// compiled into the binary and need nothing from here; these tools only provide
// an independent second opinion.
//
// Everything that touches the machine goes through Env, so the command
// selection per OS/distro, version parsing and exit semantics are unit-testable
// with a fake runner.
package simtools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Elmer's official Windows binaries (NSIS installer, needs admin, `/S` = silent)
// live on the CSC mirror at nic.funet.fi. rel26.1 is the latest release folder
// with a stable file name (checked 2026-09-28; the top-level folder only holds
// rolling devel builds). If the file is gone, ElmerWindowsFallback tells the
// user where to look instead of guessing another URL.
const (
	ElmerWindowsRelease  = "26.1"
	ElmerWindowsURL      = "https://www.nic.funet.fi/pub/sci/physics/elmer/bin/windows/rel26.1/ElmerFEM-nogui-nompi-Windows-AMD64-rel26.1.exe"
	ElmerWindowsFallback = "download the Windows installer (ElmerFEM-*-Windows-AMD64*.exe) from https://www.nic.funet.fi/pub/sci/physics/elmer/bin/windows/ (see https://www.elmerfem.org/blog/binaries/), run it, and put its bin folder on PATH"
	ElmerSourceHelp      = "no packaged Elmer for this system: build from source (https://github.com/ElmerCSC/elmerfem, cmake + gfortran + BLAS/LAPACK) or see https://www.elmerfem.org/blog/binaries/"
	NgspiceWindowsHelp   = "download ngspice for Windows from https://ngspice.sourceforge.io/download.html, unpack it (e.g. C:\\Spice64) and put its bin folder on PATH"
	HomebrewHelp         = `Homebrew is required on macOS and is not installed; install it yourself from https://brew.sh:  /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"  then re-run: pcbpilot sim tools install`
	BuiltinNote          = "pcbpilot's own simulators (sim power DC MNA, trace IR/thermal estimates) are built into the binary — nothing to install; ngspice/Elmer are independent cross-checks"
)

// Status values of a tool check.
const (
	StatusOK       = "ok"
	StatusMissing  = "missing"
	StatusOutdated = "outdated"
)

// Tool describes one external simulator.
type Tool struct {
	Name        string   // ngspice | elmer
	Title       string   // human label
	Binaries    []string // all must resolve; the first carries the version
	VersionArgs [][]string
	VersionRe   *regexp.Regexp
	MinVersion  string
	Required    bool
	UsedBy      []string
	// WinNames are alternative Windows executable names for Binaries[0]
	// (ngspice ships a console build as ngspice_con.exe).
	WinNames []string
	// WinDirs are glob patterns (env vars expanded) searched on Windows when
	// the binary is not on PATH — installers there often skip PATH.
	WinDirs []string
}

// Tools is the catalog, in install order.
func Tools() []Tool {
	return []Tool{
		{
			Name: "ngspice", Title: "ngspice (SPICE circuit simulator)",
			Binaries:    []string{"ngspice"},
			VersionArgs: [][]string{{"-v"}, {"--version"}},
			VersionRe:   regexp.MustCompile(`(?i)ngspice[- ]?(?:version\s*)?(\d+(?:\.\d+)?)`),
			MinVersion:  "30",
			Required:    true,
			UsedBy:      []string{"pcbpilot sim power --spice-check", "analog SPICE simulation flow"},
			WinNames:    []string{"ngspice_con", "ngspice"},
			WinDirs:     []string{`C:\Spice64\bin`, `%ProgramFiles%\Spice64\bin`, `%ProgramFiles%\ngspice*\bin`, `%ProgramData%\chocolatey\bin`},
		},
		{
			Name: "elmer", Title: "Elmer FEM (ElmerSolver + ElmerGrid)",
			Binaries:    []string{"ElmerSolver", "ElmerGrid"},
			VersionArgs: [][]string{{"-v"}, {"--version"}},
			VersionRe:   regexp.MustCompile(`(?i)(?:ELMER SOLVER \(v\s*|Version:\s*|elmer\S*\s+)(\d+\.\d+)`),
			MinVersion:  "9.0",
			Required:    false,
			UsedBy:      []string{"pcbpilot sim post-layout --elmer-check (thermal cross-check)"},
			WinDirs:     []string{`%ProgramFiles%\Elmer*\bin`, `%ProgramFiles(x86)%\Elmer*\bin`, `C:\Elmer*\bin`},
		},
	}
}

// ToolByName returns the catalog entry for name.
func ToolByName(name string) (Tool, bool) {
	for _, t := range Tools() {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Env abstracts the machine so tests can fake it.
type Env struct {
	GOOS      string
	OSRelease map[string]string // /etc/os-release (linux only)
	IsRoot    bool
	StdinTTY  bool
	LookPath  func(string) (string, error)
	// Output runs a probe command and returns combined output (bounded time).
	Output func(name string, args ...string) (string, error)
	// Stream runs an install command with inherited stdio.
	Stream func(argv []string) error
	// Download fetches url to a temp file and returns its path.
	Download func(url string) (string, error)
	Glob     func(pattern string) ([]string, error)
	Getenv   func(string) string
}

// Real returns the Env of this machine.
func Real() *Env {
	e := &Env{
		GOOS:     runtime.GOOS,
		IsRoot:   os.Geteuid() == 0,
		LookPath: exec.LookPath,
		Glob:     filepath.Glob,
		Getenv:   os.Getenv,
		Download: download,
	}
	e.StdinTTY = stdinIsTerminal()
	if e.GOOS == "linux" {
		e.OSRelease = readOSRelease("/etc/os-release")
	}
	e.Output = func(name string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, name, args...)
		c.Stdin = nil // /dev/null: ElmerSolver must not wait for a .sif on stdin
		out, err := c.CombinedOutput()
		return string(out), err
	}
	e.Stream = func(argv []string) error {
		c := exec.Command(argv[0], argv[1:]...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		return c.Run()
	}
	return e
}

// stdinIsTerminal: a character device that is not the null device (</dev/null
// is a char device too, and must count as non-interactive).
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if nul, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, nul) {
		return false
	}
	return true
}

func readOSRelease(path string) map[string]string {
	m := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		m[k] = strings.Trim(v, `"'`)
	}
	return m
}

func download(url string) (string, error) {
	f, err := os.CreateTemp("", "pcbpilot-elmer-*.exe")
	if err != nil {
		return "", err
	}
	f.Close()
	// curl.exe ships with Windows 10+; PowerShell is the fallback.
	if p, err := exec.LookPath("curl"); err == nil {
		if err := exec.Command(p, "-fL", "--retry", "2", "-o", f.Name(), url).Run(); err == nil {
			return f.Name(), nil
		}
	}
	ps := fmt.Sprintf("$ProgressPreference='SilentlyContinue'; Invoke-WebRequest -UseBasicParsing -Uri '%s' -OutFile '%s'", url, f.Name())
	if err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Run(); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	return f.Name(), nil
}

// ── check ─────────────────────────────────────────────────────────────────

// Result is one tool's check.
type Result struct {
	Name       string            `json:"name"`
	Title      string            `json:"title"`
	Required   bool              `json:"required"`
	Status     string            `json:"status"`
	Path       string            `json:"path,omitempty"`
	Paths      map[string]string `json:"paths,omitempty"`
	OnPath     bool              `json:"onPath"`
	Version    string            `json:"version,omitempty"`
	MinVersion string            `json:"minVersion"`
	Detail     string            `json:"detail,omitempty"`
	UsedBy     []string          `json:"usedBy"`
	Install    []string          `json:"install"`
	Manual     string            `json:"manual,omitempty"`
	Warning    string            `json:"warning,omitempty"`
}

// Report is the `sim tools check --json` document.
type Report struct {
	SchemaVersion int      `json:"schemaVersion"`
	Platform      Platform `json:"platform"`
	Builtin       string   `json:"builtin"`
	Tools         []Result `json:"tools"`
	// OK is true when every required tool is ok.
	OK bool `json:"ok"`
}

// Platform is what the install commands were chosen for.
type Platform struct {
	OS             string `json:"os"`
	Distro         string `json:"distro,omitempty"`
	PackageManager string `json:"packageManager,omitempty"`
}

// Locate finds a binary: PATH first, then the tool's Windows install dirs.
// onPath reports which. Other pcbpilot code (the SPICE / Elmer cross-checks)
// should call this instead of exec.LookPath so a Windows install that skipped
// PATH still works.
func (e *Env) Locate(t Tool, bin string) (path string, onPath bool) {
	names := []string{bin}
	if e.GOOS == "windows" && bin == t.Binaries[0] && len(t.WinNames) > 0 {
		names = t.WinNames
	}
	for _, n := range names {
		if p, err := e.LookPath(n); err == nil {
			return p, true
		}
	}
	if e.GOOS != "windows" {
		return "", false
	}
	for _, d := range t.WinDirs {
		pat := expandWin(d, e.Getenv)
		dirs, _ := e.Glob(pat)
		sort.Sort(sort.Reverse(sort.StringSlice(dirs))) // newest release folder first
		for _, dir := range dirs {
			for _, n := range names {
				cand := dir + `\` + n + ".exe"
				if m, _ := e.Glob(cand); len(m) > 0 {
					return m[0], false
				}
			}
		}
	}
	return "", false
}

var reWinVar = regexp.MustCompile(`%([A-Za-z0-9_()]+)%`)

func expandWin(s string, getenv func(string) string) string {
	return reWinVar.ReplaceAllStringFunc(s, func(m string) string {
		v := getenv(m[1 : len(m)-1])
		if v == "" {
			return m
		}
		return v
	})
}

// Find locates a catalog binary on the real machine (PATH, then Windows
// install dirs). Returns exec.ErrNotFound when absent.
func Find(bin string) (string, error) {
	e := Real()
	for _, t := range Tools() {
		for _, b := range t.Binaries {
			if b == bin {
				if p, _ := e.Locate(t, bin); p != "" {
					return p, nil
				}
				return "", exec.ErrNotFound
			}
		}
	}
	return exec.LookPath(bin)
}

// ParseVersion extracts the version from probe output.
func ParseVersion(t Tool, out string) string {
	if m := t.VersionRe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// CompareVersions compares dotted numeric versions ("47" vs "30", "26.1" vs "9.0").
func CompareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// CheckTool probes one tool.
func (e *Env) CheckTool(t Tool) Result {
	r := Result{Name: t.Name, Title: t.Title, Required: t.Required, MinVersion: t.MinVersion, UsedBy: t.UsedBy, Paths: map[string]string{}}
	plan := e.Plan(t, false)
	r.Install = plan.Commands()
	r.Manual = plan.Manual
	if plan.Blocker != "" {
		r.Manual = plan.Blocker
	}
	r.Warning = plan.Warning
	var missing []string
	r.OnPath = true
	for _, b := range t.Binaries {
		p, on := e.Locate(t, b)
		if p == "" {
			missing = append(missing, b)
			continue
		}
		r.Paths[b] = p
		if !on {
			r.OnPath = false
		}
	}
	if p := r.Paths[t.Binaries[0]]; p != "" {
		r.Path = p
		for _, args := range t.VersionArgs {
			out, _ := e.Output(p, args...) // version probes often exit non-zero
			if v := ParseVersion(t, out); v != "" {
				r.Version = v
				break
			}
		}
	}
	switch {
	case len(missing) > 0:
		r.Status = StatusMissing
		r.Detail = "not found: " + strings.Join(missing, ", ")
		r.OnPath = false
	case r.Version == "":
		r.Status = StatusOK
		r.Detail = "found, version not reported (accepted)"
	case CompareVersions(r.Version, t.MinVersion) < 0:
		r.Status = StatusOutdated
		r.Detail = fmt.Sprintf("version %s < minimum %s", r.Version, t.MinVersion)
	default:
		r.Status = StatusOK
	}
	if r.Status == StatusOK && !r.OnPath {
		r.Detail = strings.TrimSpace(r.Detail + " (found outside PATH; add its folder to PATH for other tools)")
	}
	return r
}

// Check probes the given tools (all when names is empty).
func (e *Env) Check(names ...string) Report {
	rep := Report{SchemaVersion: 1, Platform: e.Platform(), Builtin: BuiltinNote, OK: true}
	for _, t := range Tools() {
		if len(names) > 0 && !contains(names, t.Name) {
			continue
		}
		r := e.CheckTool(t)
		if r.Required && r.Status != StatusOK {
			rep.OK = false
		}
		rep.Tools = append(rep.Tools, r)
	}
	return rep
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ── platform + plans ──────────────────────────────────────────────────────

func (e *Env) has(bin string) bool { _, err := e.LookPath(bin); return err == nil }

// linuxFamily is "ubuntu" (Ubuntu and derivatives with UBUNTU_CODENAME),
// "debian", "fedora" (Fedora/RHEL/CentOS/Rocky/Alma) or "".
func (e *Env) linuxFamily() string {
	id := strings.ToLower(e.OSRelease["ID"])
	like := " " + strings.ToLower(e.OSRelease["ID_LIKE"]) + " "
	switch {
	case id == "ubuntu" || e.OSRelease["UBUNTU_CODENAME"] != "":
		return "ubuntu"
	case id == "debian" || strings.Contains(like, " debian ") || strings.Contains(like, " ubuntu "):
		return "debian"
	case id == "fedora" || id == "rhel" || id == "centos" || id == "rocky" || id == "almalinux" ||
		strings.Contains(like, " fedora ") || strings.Contains(like, " rhel "):
		return "fedora"
	}
	return ""
}

// Platform describes this machine for the report.
func (e *Env) Platform() Platform {
	p := Platform{OS: e.GOOS}
	switch e.GOOS {
	case "darwin":
		p.PackageManager = "brew"
	case "linux":
		p.Distro = e.OSRelease["ID"]
		switch e.linuxFamily() {
		case "ubuntu", "debian":
			p.PackageManager = "apt"
		case "fedora":
			p.PackageManager = "dnf"
		}
	case "windows":
		switch {
		case e.has("winget"):
			p.PackageManager = "winget"
		case e.has("choco"):
			p.PackageManager = "choco"
		}
	}
	return p
}

// Step is one install action.
type Step struct {
	Argv     []string `json:"argv"`
	Sudo     bool     `json:"sudo,omitempty"`     // needs root (Linux)
	Admin    bool     `json:"admin,omitempty"`    // Windows elevation (UAC)
	Download string   `json:"download,omitempty"` // fetch this URL first; "{file}" in Argv is the result
	Optional bool     `json:"optional,omitempty"` // failure does not stop the plan
}

// String renders the step as a shell line.
func (s Step) String() string {
	argv := s.Argv
	if s.Sudo {
		argv = append([]string{"sudo"}, argv...)
	}
	line := shellJoin(argv)
	if s.Download != "" {
		line = "download " + s.Download + " → {file}; " + line
	}
	if s.Admin {
		line += "   (Windows will ask for administrator approval)"
	}
	return line
}

func shellJoin(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\"'$&|;<>()*?") {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			out[i] = a
		}
	}
	return strings.Join(out, " ")
}

// Plan is how to install one tool on this machine.
type Plan struct {
	Tool    string `json:"tool"`
	Steps   []Step `json:"steps"`
	Manual  string `json:"manual,omitempty"`  // shown when Steps is empty, or as a follow-up
	Blocker string `json:"blocker,omitempty"` // prerequisite missing (Homebrew); Steps are then not runnable
	Warning string `json:"warning,omitempty"`
}

// Commands renders the steps (or the manual hint when there are none).
func (p Plan) Commands() []string {
	var out []string
	for _, s := range p.Steps {
		out = append(out, s.String())
	}
	return out
}

// NeedsSudo reports whether any step needs root.
func (p Plan) NeedsSudo() bool {
	for _, s := range p.Steps {
		if s.Sudo {
			return true
		}
	}
	return false
}

// Plan selects the install commands for t on this machine. upgrade=true when
// the tool is present but outdated.
func (e *Env) Plan(t Tool, upgrade bool) Plan {
	p := Plan{Tool: t.Name}
	switch e.GOOS {
	case "darwin":
		if !e.has("brew") {
			p.Blocker = HomebrewHelp
		}
		verb := "install"
		if upgrade {
			verb = "upgrade"
		}
		switch t.Name {
		case "ngspice":
			p.Steps = []Step{{Argv: []string{"brew", verb, "ngspice"}}}
		case "elmer":
			p.Steps = []Step{
				{Argv: []string{"brew", "tap", "elmercsc/elmerfem"}},
				{Argv: []string{"brew", verb, "elmercsc/elmerfem/elmer"}},
			}
			p.Warning = "Elmer builds from source on macOS (Homebrew tap elmercsc/elmerfem, needs gcc/gfortran + cmake): expect 20–60+ minutes"
		}
	case "linux":
		sudo := !e.IsRoot
		switch fam := e.linuxFamily(); {
		case (fam == "ubuntu" || fam == "debian") && t.Name == "ngspice":
			p.Steps = []Step{
				{Argv: []string{"apt-get", "update"}, Sudo: sudo, Optional: true},
				{Argv: []string{"apt-get", "install", "-y", "ngspice"}, Sudo: sudo},
			}
		case fam == "ubuntu" && t.Name == "elmer":
			if !e.has("add-apt-repository") {
				p.Steps = append(p.Steps, Step{Argv: []string{"apt-get", "install", "-y", "software-properties-common"}, Sudo: sudo})
			}
			p.Steps = append(p.Steps,
				Step{Argv: []string{"add-apt-repository", "-y", "ppa:elmer-csc-ubuntu/elmer-csc-ppa"}, Sudo: sudo},
				Step{Argv: []string{"apt-get", "install", "-y", "elmerfem-csc"}, Sudo: sudo},
			)
		case fam == "fedora" && t.Name == "ngspice":
			p.Steps = []Step{{Argv: []string{"dnf", "install", "-y", "ngspice"}, Sudo: sudo}}
		case t.Name == "elmer":
			p.Manual = ElmerSourceHelp
		default:
			p.Manual = "install ngspice with your distribution's package manager (package name: ngspice), or build it from https://ngspice.sourceforge.io/download.html"
		}
	case "windows":
		switch t.Name {
		case "ngspice":
			switch {
			case e.has("winget") && e.wingetHas("ngspice.ngspice"):
				p.Steps = []Step{{Argv: []string{"winget", "install", "--id", "ngspice.ngspice", "-e", "--accept-source-agreements", "--accept-package-agreements"}}}
			case e.has("choco"):
				p.Steps = []Step{{Argv: []string{"choco", "install", "ngspice", "-y"}, Admin: true}}
			default:
				p.Manual = NgspiceWindowsHelp
			}
		case "elmer":
			p.Steps = []Step{{Download: ElmerWindowsURL, Argv: []string{"{file}", "/S"}, Admin: true}}
			p.Manual = "if the download fails: " + ElmerWindowsFallback
			p.Warning = "the Elmer " + ElmerWindowsRelease + " installer needs administrator approval and may not add its bin folder to PATH; pcbpilot also looks in Program Files\\Elmer*\\bin"
		}
	default:
		p.Manual = "no automatic install for " + e.GOOS + "; install " + t.Title + " yourself"
	}
	return p
}

// wingetHas asks winget (at run time) whether a package id exists. The
// winget-pkgs repo had no ngspice manifest on 2026-09-28, so this normally
// falls through to Chocolatey — but picks winget up if one is published.
func (e *Env) wingetHas(id string) bool {
	out, err := e.Output("winget", "show", "--id", id, "-e", "--accept-source-agreements", "--disable-interactivity")
	return err == nil && strings.Contains(strings.ToLower(out), strings.ToLower(id))
}

// ── install ───────────────────────────────────────────────────────────────

// InstallOptions controls Install.
type InstallOptions struct {
	Only         string // "", ngspice, elmer
	Yes          bool
	DryRun       bool
	RequireElmer bool
	Confirm      func(prompt string) bool // interactive y/N (nil = no)
}

// InstallOutcome is one tool's result.
type InstallOutcome struct {
	Tool     string `json:"tool"`
	Required bool   `json:"required"`
	Before   string `json:"before"`
	After    string `json:"after"`
	Action   string `json:"action"` // already-ok | installed | failed | skipped | planned | manual
	Message  string `json:"message,omitempty"`
}

// ErrRequired is returned when a required tool is not ok after Install.
var ErrRequired = errors.New("required simulation tool not installed")

// Install brings the selected tools up to date. It prints every command before
// running it and never runs anything without Yes (or an interactive yes).
// Only a REQUIRED tool that is still not ok afterwards makes it return ErrRequired;
// optional tools only warn.
func (e *Env) Install(w io.Writer, o InstallOptions) ([]InstallOutcome, error) {
	fmt.Fprintln(w, "ℹ "+BuiltinNote)
	var outs []InstallOutcome
	failedRequired := false
	for _, t := range Tools() {
		if o.Only != "" && o.Only != t.Name {
			continue
		}
		if t.Name == "elmer" && o.RequireElmer {
			t.Required = true
		}
		kind := "optional"
		if t.Required {
			kind = "required"
		}
		before := e.CheckTool(t)
		out := InstallOutcome{Tool: t.Name, Required: t.Required, Before: before.Status}
		fmt.Fprintf(w, "\n== %s [%s] — %s", t.Title, kind, before.Status)
		if before.Version != "" {
			fmt.Fprintf(w, " (v%s at %s)", before.Version, before.Path)
		}
		fmt.Fprintln(w)
		if before.Status == StatusOK {
			out.After, out.Action = StatusOK, "already-ok"
			outs = append(outs, out)
			continue
		}
		plan := e.Plan(t, before.Status == StatusOutdated)
		if plan.Warning != "" {
			fmt.Fprintln(w, "⚠ "+plan.Warning)
		}
		action, msg := e.runPlan(w, plan, o)
		out.Action, out.Message = action, msg
		after := before
		if action == "installed" || action == "failed" {
			after = e.CheckTool(t)
		}
		out.After = after.Status
		if action == "installed" && after.Status != StatusOK {
			out.Action = "failed"
			out.Message = strings.TrimSpace(out.Message + " commands finished but the check still says " + after.Status + ": " + after.Detail)
		}
		switch {
		case out.After == StatusOK:
			fmt.Fprintf(w, "✔ %s ok (v%s at %s)\n", t.Name, after.Version, after.Path)
		case o.DryRun || action == "planned":
			// nothing ran
		case t.Required:
			failedRequired = true
			fmt.Fprintf(w, "✘ %s (required) is %s: %s\n", t.Name, out.After, out.Message)
		default:
			fmt.Fprintf(w, "⚠ %s (optional) is %s — pcbpilot works without it; the %s cross-check is skipped until it is installed. %s\n",
				t.Name, out.After, strings.Join(t.UsedBy, ", "), out.Message)
		}
		if action == "planned" && !o.DryRun && t.Required {
			failedRequired = true
		}
		outs = append(outs, out)
	}
	if failedRequired {
		return outs, ErrRequired
	}
	return outs, nil
}

// runPlan prints and (unless dry-run / not confirmed) executes a plan.
func (e *Env) runPlan(w io.Writer, p Plan, o InstallOptions) (action, msg string) {
	if len(p.Steps) == 0 {
		fmt.Fprintln(w, "  manual: "+p.Manual)
		return "manual", p.Manual
	}
	for _, s := range p.Steps {
		fmt.Fprintln(w, "  $ "+s.String())
	}
	if p.Blocker != "" {
		fmt.Fprintln(w, "  ✘ "+p.Blocker)
		return "manual", p.Blocker
	}
	if o.DryRun {
		fmt.Fprintln(w, "  (dry run — nothing executed)")
		return "planned", ""
	}
	if p.NeedsSudo() && !e.IsRoot && !e.canSudo() {
		m := "needs root: sudo is unavailable or would prompt in this non-interactive shell — run the commands above yourself"
		fmt.Fprintln(w, "  ✘ "+m)
		return "manual", m
	}
	if !o.Yes {
		if o.Confirm == nil || !e.StdinTTY || !o.Confirm("  run these commands? [y/N] ") {
			m := "not confirmed — re-run with --yes to execute"
			fmt.Fprintln(w, "  "+m)
			return "planned", m
		}
	}
	if p.NeedsSudo() && !e.IsRoot {
		fmt.Fprintln(w, "  (these commands use sudo and may ask for your password)")
	}
	for _, s := range p.Steps {
		argv := append([]string{}, s.Argv...)
		if s.Download != "" {
			fmt.Fprintln(w, "  → downloading "+s.Download)
			f, err := e.Download(s.Download)
			if err != nil {
				return "failed", err.Error() + " — " + p.Manual
			}
			for i := range argv {
				argv[i] = strings.ReplaceAll(argv[i], "{file}", f)
			}
		}
		if s.Sudo && !e.IsRoot {
			argv = append([]string{"sudo"}, argv...)
		}
		fmt.Fprintln(w, "  → "+shellJoin(argv))
		if err := e.Stream(argv); err != nil {
			if s.Optional {
				fmt.Fprintf(w, "  ⚠ %s failed (%v), continuing\n", argv[0], err)
				continue
			}
			m := fmt.Sprintf("%s failed: %v", shellJoin(argv), err)
			if e.GOOS == "darwin" && len(argv) > 0 && argv[0] == "brew" {
				m += " — " + brewToolchainHint
			}
			if p.Manual != "" {
				m += " — " + p.Manual
			}
			return "failed", m
		}
	}
	return "installed", ""
}

// canSudo: sudo exists and either we can prompt (TTY) or it needs no password.
func (e *Env) canSudo() bool {
	if !e.has("sudo") {
		return false
	}
	if e.StdinTTY {
		return true
	}
	_, err := e.Output("sudo", "-n", "true")
	return err == nil
}

// brewToolchainHint: a source-built formula (Elmer's tap) needs current Xcode
// Command Line Tools; live 2026-09-28 the install stopped at "Your Command
// Line Tools are too outdated". Updating them needs the user's password, so
// the installer only names the fix.
const brewToolchainHint = "if Homebrew reported \"Command Line Tools are too outdated\" (source builds need current tools), update them first: " +
	"`softwareupdate --list` then `softwareupdate -i \"Command Line Tools for Xcode-<version>\"`, or " +
	"`sudo rm -rf /Library/Developer/CommandLineTools && sudo xcode-select --install`; then re-run `pcbpilot sim tools install --only elmer`"
