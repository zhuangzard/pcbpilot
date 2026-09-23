package app

// python.go — locating a Python 3 interpreter for the skill's helper scripts.
//
// `bom export` / `bom enrich` shell out to bom-enrich.py. The launcher used to
// be hard-wired to "python3", which Windows normally does not have: python.org
// installs python.exe plus the `py` launcher, and Windows 10/11 additionally
// ship a Microsoft Store *stub* named python3.exe that exec.LookPath happily
// finds but which only prints "Python was not found" and exits 9009. The
// symptom was `bom export --type csv` printing
//
//	warning: BOM exported but enrichment failed (file left un-enriched): exit status 9009
//
// on every Windows machine, with the BOM silently left without C-numbers.
//
// So the interpreter is resolved once, here: $PCBPILOT_PYTHON, then
// python3 → python → py -3. On Windows every candidate is probed by actually
// running it, so the Store stub fails and the ladder falls through. On
// macOS/Linux a python3 found on PATH is used as before with no probe —
// behaviour there is unchanged — and only the new fallbacks are probed,
// because `python` may still be Python 2 on an old host.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// envPython overrides interpreter discovery with an explicit executable
// (a venv python, a pinned 3.11, …). Same contract as PCBPILOT_SKILLS_DIR /
// FREEROUTING_CMD elsewhere in this package: set-but-unusable is a hard error,
// never a silent fall-through to some other interpreter.
const envPython = "PCBPILOT_PYTHON"

// pythonCandidate is one interpreter to try: an executable name resolved
// through PATH plus any leading args (the `py` launcher needs -3).
type pythonCandidate struct {
	name string
	args []string
}

// pythonCandidates is the fallback ladder, most specific first.
var pythonCandidates = []pythonCandidate{
	{name: "python3"},
	{name: "python"},
	{name: "py", args: []string{"-3"}},
}

// pythonProbeArg is run by the probe: exit 0 only for a working Python 3.
const pythonProbeArg = "import sys; sys.exit(0 if sys.version_info[0] >= 3 else 1)"

// pythonResolver walks pythonCandidates. goos, getenv, lookPath and probe are
// injectable so the ladder is testable on a machine without those binaries.
type pythonResolver struct {
	goos     string
	getenv   func(key string) string
	lookPath func(file string) (string, error)
	probe    func(path string, args ...string) error
}

func defaultPythonResolver() pythonResolver {
	return pythonResolver{
		goos:     runtime.GOOS,
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		probe: func(path string, args ...string) error {
			return exec.Command(path, append(append([]string{}, args...), "-c", pythonProbeArg)...).Run()
		},
	}
}

// needsProbe reports whether a candidate found on PATH must also be executed
// before it is trusted. On Windows every hit is suspect (Store stubs); elsewhere
// python3 is trusted as it always was, and only the fallbacks are checked.
func (r pythonResolver) needsProbe(c pythonCandidate) bool {
	return r.goos == "windows" || c.name != "python3"
}

// resolve returns the interpreter path and its leading args, or an error that
// names every candidate tried.
func (r pythonResolver) resolve() (string, []string, error) {
	if r.getenv != nil {
		if explicit := strings.TrimSpace(r.getenv(envPython)); explicit != "" {
			path, err := r.lookPath(explicit)
			if err != nil {
				return "", nil, fmt.Errorf("%s=%s is not an executable python: %w", envPython, explicit, err)
			}
			return path, nil, nil
		}
	}

	tried := make([]string, 0, len(pythonCandidates))
	for _, c := range pythonCandidates {
		tried = append(tried, strings.Join(append([]string{c.name}, c.args...), " "))
		path, err := r.lookPath(c.name)
		if err != nil {
			continue
		}
		if r.needsProbe(c) && r.probe != nil {
			if err := r.probe(path, c.args...); err != nil {
				continue
			}
		}
		return path, c.args, nil
	}
	msg := fmt.Sprintf("python 3 interpreter not found (tried: %s); install Python 3 and put it on PATH, or set %s",
		strings.Join(tried, ", "), envPython)
	if r.goos == "windows" {
		msg += " — the Microsoft Store python3.exe stub does not count"
	}
	return "", nil, errors.New(msg)
}

// pythonCommand builds the command that runs a Python script with args,
// resolving the interpreter through the ladder above.
func pythonCommand(script string, args ...string) (*exec.Cmd, error) {
	path, lead, err := defaultPythonResolver().resolve()
	if err != nil {
		return nil, err
	}
	argv := append(append([]string{}, lead...), script)
	argv = append(argv, args...)
	return exec.Command(path, argv...), nil
}
