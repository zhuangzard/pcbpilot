package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fakePython builds a resolver whose PATH holds exactly `onPath` (name → path)
// and whose probe succeeds only for paths in `working`. Every probe call is
// recorded so tests can assert which candidates were actually executed.
func fakePython(goos string, env map[string]string, onPath map[string]string, working map[string]bool) (pythonResolver, *[]string) {
	probed := &[]string{}
	r := pythonResolver{
		goos:   goos,
		getenv: func(key string) string { return env[key] },
		lookPath: func(file string) (string, error) {
			if p, ok := onPath[file]; ok {
				return p, nil
			}
			return "", errors.New("executable file not found in PATH")
		},
		probe: func(path string, args ...string) error {
			*probed = append(*probed, strings.Join(append([]string{path}, args...), " "))
			if working[path] {
				return nil
			}
			return errors.New("exit status 9009")
		},
	}
	return r, probed
}

func TestResolvePython(t *testing.T) {
	const (
		stub = `C:\Users\u\AppData\Local\Microsoft\WindowsApps\python3.exe`
		py   = `C:\Windows\py.exe`
		pyth = `C:\Python313\python.exe`
	)
	cases := []struct {
		name       string
		goos       string
		env        map[string]string
		onPath     map[string]string
		working    map[string]bool
		wantPath   string
		wantArgs   []string
		wantErr    string
		wantProbed []string
	}{
		{
			name:       "linux python3 is used without a probe (unchanged behaviour)",
			goos:       "linux",
			onPath:     map[string]string{"python3": "/usr/bin/python3", "python": "/usr/bin/python"},
			wantPath:   "/usr/bin/python3",
			wantProbed: []string{},
		},
		{
			name:       "darwin falls back to a working python when python3 is missing",
			goos:       "darwin",
			onPath:     map[string]string{"python": "/usr/local/bin/python"},
			working:    map[string]bool{"/usr/local/bin/python": true},
			wantPath:   "/usr/local/bin/python",
			wantProbed: []string{"/usr/local/bin/python"},
		},
		{
			name:       "linux skips a python that is not Python 3",
			goos:       "linux",
			onPath:     map[string]string{"python": "/usr/bin/python"},
			working:    map[string]bool{},
			wantErr:    "tried: python3, python, py -3",
			wantProbed: []string{"/usr/bin/python"},
		},
		{
			name:       "windows: Store python3 stub (exit 9009) falls through to python",
			goos:       "windows",
			onPath:     map[string]string{"python3": stub, "python": pyth, "py": py},
			working:    map[string]bool{pyth: true, py: true},
			wantPath:   pyth,
			wantProbed: []string{stub, pyth},
		},
		{
			name:       "windows: only the py launcher works",
			goos:       "windows",
			onPath:     map[string]string{"python3": stub, "py": py},
			working:    map[string]bool{py: true},
			wantPath:   py,
			wantArgs:   []string{"-3"},
			wantProbed: []string{stub, py + " -3"},
		},
		{
			name:       "windows: a real python3 passes its probe and wins",
			goos:       "windows",
			onPath:     map[string]string{"python3": `C:\Python313\python3.exe`, "python": pyth},
			working:    map[string]bool{`C:\Python313\python3.exe`: true, pyth: true},
			wantPath:   `C:\Python313\python3.exe`,
			wantProbed: []string{`C:\Python313\python3.exe`},
		},
		{
			name:       "windows: nothing usable names every candidate and the stub",
			goos:       "windows",
			onPath:     map[string]string{"python3": stub},
			working:    map[string]bool{},
			wantErr:    "Microsoft Store python3.exe stub",
			wantProbed: []string{stub},
		},
		{
			name:       "PCBPILOT_PYTHON wins over the ladder and is never probed",
			goos:       "windows",
			env:        map[string]string{envPython: `C:\venv\Scripts\python.exe`},
			onPath:     map[string]string{`C:\venv\Scripts\python.exe`: `C:\venv\Scripts\python.exe`, "python3": stub},
			wantPath:   `C:\venv\Scripts\python.exe`,
			wantProbed: []string{},
		},
		{
			name:       "PCBPILOT_PYTHON pointing nowhere is a hard error, not a fall-through",
			goos:       "linux",
			env:        map[string]string{envPython: "/opt/nope/python"},
			onPath:     map[string]string{"python3": "/usr/bin/python3"},
			wantErr:    "PCBPILOT_PYTHON=/opt/nope/python is not an executable python",
			wantProbed: []string{},
		},
		{
			name:       "blank PCBPILOT_PYTHON is ignored",
			goos:       "linux",
			env:        map[string]string{envPython: "   "},
			onPath:     map[string]string{"python3": "/usr/bin/python3"},
			wantPath:   "/usr/bin/python3",
			wantProbed: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, probed := fakePython(tc.goos, tc.env, tc.onPath, tc.working)
			gotPath, gotArgs, err := r.resolve()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v; want containing %q", err, tc.wantErr)
				}
				if gotPath != "" {
					t.Fatalf("path = %q on error; want empty", gotPath)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if gotPath != tc.wantPath {
					t.Fatalf("path = %q; want %q", gotPath, tc.wantPath)
				}
				if len(gotArgs) != len(tc.wantArgs) || (len(gotArgs) > 0 && !reflect.DeepEqual(gotArgs, tc.wantArgs)) {
					t.Fatalf("args = %v; want %v", gotArgs, tc.wantArgs)
				}
			}
			if !reflect.DeepEqual(*probed, tc.wantProbed) {
				t.Fatalf("probed = %v; want %v", *probed, tc.wantProbed)
			}
		})
	}
}

// TestResolvePythonNilProbe: a resolver without a probe trusts the first PATH
// hit, so a nil probe can never turn a found interpreter into an error.
func TestResolvePythonNilProbe(t *testing.T) {
	r := pythonResolver{
		goos:     "windows",
		lookPath: func(file string) (string, error) { return `C:\x\` + file + ".exe", nil },
	}
	path, args, err := r.resolve()
	if err != nil || path != `C:\x\python3.exe` || len(args) != 0 {
		t.Fatalf("got %q %v %v; want python3, no args, nil", path, args, err)
	}
}

// TestPythonCommandArgv: the script goes after the interpreter's own leading
// args (`py -3 script.py …`), never before them.
func TestPythonCommandArgv(t *testing.T) {
	r := pythonResolver{
		goos:     "windows",
		getenv:   func(string) string { return "" },
		lookPath: func(file string) (string, error) { return `C:\Windows\` + file + ".exe", nil },
		probe: func(path string, args ...string) error {
			if path == `C:\Windows\py.exe` {
				return nil
			}
			return errors.New("exit status 9009")
		},
	}
	// python3/python both fail the probe, so the ladder lands on `py -3`.
	path, lead, err := r.resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argv := append(append([]string{}, lead...), "enrich.py", "bom.csv")
	want := []string{"-3", "enrich.py", "bom.csv"}
	if path != `C:\Windows\py.exe` || !reflect.DeepEqual(argv, want) {
		t.Fatalf("got %q %v; want %q %v", path, argv, `C:\Windows\py.exe`, want)
	}
}
