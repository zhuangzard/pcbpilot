// Package kicad drives an installed KiCad (10.x) for pcbpilot's KiCad
// backend: a Python bridge run with KiCad's own interpreter (pcbnew) for
// board reads and the Specctra DSN/SES round trip, and kicad-cli for DRC.
//
// Only KiCad's public Python API and CLI are used; KiCad is never linked.
// The bridge script is embedded (bridge.py) and written to the user cache
// directory before it runs.
package kicad

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

//go:embed bridge.py
var bridgeSource []byte

// resultPrefix marks the bridge's single result line on stdout.
const resultPrefix = "PCBPILOT_JSON:"

// Tools are the KiCad executables pcbpilot uses.
type Tools struct {
	App    string `json:"app"`
	Python string `json:"python"`
	CLI    string `json:"cli"`
}

// Env overrides (all optional):
//
//	PCBPILOT_KICAD_APP     KiCad install root (macOS: the KiCad.app bundle;
//	                       Linux: the prefix holding bin/, default /usr;
//	                       Windows: e.g. C:\Program Files\KiCad\10.0)
//	PCBPILOT_KICAD_PYTHON  Python that can `import pcbnew`
//	PCBPILOT_KICAD_CLI     kicad-cli
const (
	EnvApp    = "PCBPILOT_KICAD_APP"
	EnvPython = "PCBPILOT_KICAD_PYTHON"
	EnvCLI    = "PCBPILOT_KICAD_CLI"
)

// DefaultApp is the platform's default KiCad install root.
func DefaultApp() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Applications/KiCad/KiCad.app"
	case "windows":
		// Newest "C:\Program Files\KiCad\<version>".
		matches, _ := filepath.Glob(`C:\Program Files\KiCad\*`)
		sort.Strings(matches)
		if len(matches) > 0 {
			return matches[len(matches)-1]
		}
		return `C:\Program Files\KiCad\10.0`
	default:
		return "/usr"
	}
}

// toolPaths returns the bridge Python and kicad-cli under an install root
// for goos (best effort on Linux/Windows: distribution packages put
// pcbnew into the system Python, the Windows installer ships bin\python.exe).
func toolPaths(goos, app string) (python, cli string) {
	switch goos {
	case "darwin":
		matches, _ := filepath.Glob(filepath.Join(app, "Contents/Frameworks/Python.framework/Versions/*/bin/python3"))
		sort.Strings(matches)
		if len(matches) > 0 {
			python = matches[len(matches)-1]
		} else {
			python = filepath.Join(app, "Contents/Frameworks/Python.framework/Versions/Current/bin/python3")
		}
		return python, filepath.Join(app, "Contents/MacOS/kicad-cli")
	case "windows":
		return filepath.Join(app, "bin", "python.exe"), filepath.Join(app, "bin", "kicad-cli.exe")
	default:
		return filepath.Join(app, "bin", "python3"), filepath.Join(app, "bin", "kicad-cli")
	}
}

// Locate finds KiCad's Python and kicad-cli (env overrides first).
func Locate() (*Tools, error) {
	app := os.Getenv(EnvApp)
	if app == "" {
		app = DefaultApp()
	}
	py, cli := toolPaths(runtime.GOOS, app)
	if v := os.Getenv(EnvPython); v != "" {
		py = v
	}
	if v := os.Getenv(EnvCLI); v != "" {
		cli = v
	}
	t := &Tools{App: app, Python: py, CLI: cli}
	var missing []string
	for _, p := range []string{py, cli} {
		if _, err := os.Stat(p); err != nil {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return t, fmt.Errorf("KiCad not found (%s missing); install KiCad 10 or set %s / %s / %s",
			strings.Join(missing, ", "), EnvApp, EnvPython, EnvCLI)
	}
	return t, nil
}

// BridgeTimeout bounds one bridge run (a zone fill of a large board is the
// slowest step).
var BridgeTimeout = 15 * time.Minute

func bridgeScript() (string, error) {
	sum := sha256.Sum256(bridgeSource)
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "pcbpilot", "kicad")
	path := filepath.Join(dir, "bridge-"+hex.EncodeToString(sum[:6])+".py")
	if b, err := os.ReadFile(path); err == nil && bytes.Equal(b, bridgeSource) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bridgeSource, 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

// RunBridge runs one bridge subcommand and returns its result object.
func (t *Tools) RunBridge(args ...string) (json.RawMessage, error) {
	script, err := bridgeScript()
	if err != nil {
		return nil, fmt.Errorf("kicad bridge: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), BridgeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.Python, append([]string{script}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	raw, perr := parseBridgeOutput(stdout.Bytes())
	if perr != nil {
		why := perr.Error()
		if runErr != nil {
			why = runErr.Error() + "; " + why
		}
		return nil, fmt.Errorf("kicad bridge %s: %s%s", args[0], why, stderrTail(stderr.Bytes()))
	}
	var head struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("kicad bridge %s: %w", args[0], err)
	}
	if !head.OK {
		return raw, fmt.Errorf("kicad bridge %s: %s%s", args[0], head.Error, stderrTail(stderr.Bytes()))
	}
	return raw, nil
}

// parseBridgeOutput returns the JSON after the last result marker (pcbnew
// may print noise on stdout, even on the same line before it).
func parseBridgeOutput(out []byte) (json.RawMessage, error) {
	i := bytes.LastIndex(out, []byte(resultPrefix))
	if i < 0 {
		return nil, errors.New("no result line on stdout")
	}
	rest := out[i+len(resultPrefix):]
	if j := bytes.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[:j]
	}
	rest = bytes.TrimSpace(rest)
	if !json.Valid(rest) {
		return nil, errors.New("result line is not JSON")
	}
	return json.RawMessage(rest), nil
}

// stderrTail keeps the last meaningful stderr lines (pcbnew's wx assert
// noise filtered) for error messages.
func stderrTail(b []byte) string {
	var keep []string
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.Contains(l, "assert") || strings.Contains(l, "memory leak") {
			continue
		}
		keep = append(keep, l)
	}
	if len(keep) > 6 {
		keep = keep[len(keep)-6:]
	}
	if len(keep) == 0 {
		return ""
	}
	return " (stderr: " + strings.Join(keep, " | ") + ")"
}

// Snapshot returns pcbpilot's board snapshot JSON (mil, y-up; the shape of
// `pcb dump`, decodable by the boardSnapshot / pcbauto.FromSnapshot /
// postsim readers) of a .kicad_pcb.
func Snapshot(pcbPath string) ([]byte, error) {
	t, err := Locate()
	if err != nil {
		return nil, err
	}
	return t.Snapshot(pcbPath)
}

// Snapshot is the package-level Snapshot with explicit tools.
func (t *Tools) Snapshot(pcbPath string) ([]byte, error) {
	raw, err := t.RunBridge("snapshot", pcbPath)
	if err != nil {
		return nil, err
	}
	return SnapshotFromResult(raw)
}

// SnapshotFromResult extracts result.snapshot from a bridge snapshot result.
func SnapshotFromResult(raw []byte) ([]byte, error) {
	var res struct {
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	if len(res.Snapshot) == 0 {
		return nil, errors.New("kicad bridge: no snapshot in the result")
	}
	return res.Snapshot, nil
}

// NetRequirement is one net's intent (mil), the bridge's REQS.json value.
type NetRequirement struct {
	OuterMil     float64 `json:"outerMil"`
	InnerMil     float64 `json:"innerMil"`
	MinMil       float64 `json:"minMil"`
	ClearanceMil float64 `json:"clearanceMil"`
}

// NetClass is one netclass the bridge wrote.
type NetClass struct {
	Name           string            `json:"name"`
	Nets           []string          `json:"nets"`
	TrackWidthMil  float64           `json:"trackWidthMil"`
	InnerWidthMil  float64           `json:"innerWidthMil"`
	MinMil         float64           `json:"minMil"`
	ClearanceMil   float64           `json:"clearanceMil"`
	IntentOuterMil float64           `json:"intentOuterMil"`
	Effective      map[string]string `json:"effective,omitempty"`
}

// NetclassResult is the bridge's netclasses result.
type NetclassResult struct {
	Out         string     `json:"out"`
	Classes     []NetClass `json:"classes"`
	MissingNets []string   `json:"missingNets"`
	Kept        []string   `json:"kept"`
	Mismatched  []string   `json:"mismatched"`
	DRU         string     `json:"dru"`
	DRURules    int        `json:"druRules"`
}

// Netclasses writes reqs as netclasses (one per distinct requirement) into
// a copy of pcb at out (+ out's .kicad_pro and .kicad_dru).
func (t *Tools) Netclasses(pcb string, reqs map[string]NetRequirement, out string) (*NetclassResult, error) {
	path := strings.TrimSuffix(out, filepath.Ext(out)) + "-reqs.json"
	b, err := json.MarshalIndent(reqs, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return nil, err
	}
	raw, err := t.RunBridge("netclasses", pcb, path, out)
	if err != nil {
		return nil, err
	}
	var res NetclassResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// RipUp writes pcb without its unlocked tracks and vias to out.
func (t *Tools) RipUp(pcb, out string) (json.RawMessage, error) {
	return t.RunBridge("ripup", pcb, out)
}

// ExportDSN writes pcb's Specctra DSN (KiCad's native exporter).
func (t *Tools) ExportDSN(pcb, out string) (json.RawMessage, error) {
	return t.RunBridge("dsn", pcb, out)
}

// ImportSES imports a session into pcb, fills the zones and saves out.
func (t *Tools) ImportSES(pcb, ses, out string) (json.RawMessage, error) {
	return t.RunBridge("ses", pcb, ses, out)
}

// Fill refills every zone of pcb and saves it in place.
func (t *Tools) Fill(pcb string) (json.RawMessage, error) { return t.RunBridge("fill", pcb) }

// DRC runs `kicad-cli pcb drc` (errors only, JSON) on pcb, writes the raw
// report to out and returns it parsed. The board's .kicad_pro (and
// .kicad_dru) must sit next to it for the project rules.
func (t *Tools) DRC(pcb, out string) (*DRCReport, error) { return t.DRCSeverity(pcb, out, "error") }

// DRCSeverity is DRC with a kicad-cli severity: error | warning | all.
func (t *Tools) DRCSeverity(pcb, out, severity string) (*DRCReport, error) {
	ctx, cancel := context.WithTimeout(context.Background(), BridgeTimeout)
	defer cancel()
	_ = os.Remove(out)
	cmd := exec.CommandContext(ctx, t.CLI, "pcb", "drc", "--format", "json", "--severity-"+severity, "-o", out, pcb)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	data, rerr := os.ReadFile(out)
	if rerr != nil {
		if err == nil {
			err = rerr
		}
		return nil, fmt.Errorf("kicad-cli pcb drc: %v: %s", err, strings.TrimSpace(buf.String()))
	}
	return ParseDRC(data)
}

// Rules are board design rules and the Default netclass (mil; 0 = keep).
type Rules struct {
	ClearanceMil float64 `json:"clearanceMil,omitempty"`
	MinTrackMil  float64 `json:"minTrackMil,omitempty"`
	TrackMil     float64 `json:"trackMil,omitempty"`
	ViaDiaMil    float64 `json:"viaDiaMil,omitempty"`
	ViaDrillMil  float64 `json:"viaDrillMil,omitempty"`
	EdgeMil      float64 `json:"edgeMil,omitempty"`
}

// MinimalProject is the .kicad_pro written for a board that has none: KiCad
// fills every other setting with its defaults on load, and the next
// SaveBoard writes the complete project.
func MinimalProject(pcbPath string) []byte {
	name := strings.TrimSuffix(filepath.Base(pcbPath), ".kicad_pcb") + ".kicad_pro"
	b, _ := json.MarshalIndent(map[string]any{"meta": map[string]any{"filename": name, "version": 3}}, "", "  ")
	return append(b, '\n')
}

// SetRules writes r into a copy of pcb at out (its .kicad_pro must exist).
func (t *Tools) SetRules(pcb string, r Rules, out string) (json.RawMessage, error) {
	path := strings.TrimSuffix(out, filepath.Ext(out)) + "-rules.json"
	b, _ := json.MarshalIndent(r, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return nil, err
	}
	return t.RunBridge("rules", pcb, path, out)
}

// Pour is one board-outline copper zone (layer: pcbpilot id 1/2/15+).
type Pour struct {
	Net          string  `json:"net"`
	Layer        int     `json:"layer"`
	ClearanceMil float64 `json:"clearanceMil,omitempty"`
}

// Pours replaces pcbpilot's pours on pcb with ps, fills and saves out.
func (t *Tools) Pours(pcb string, ps []Pour, out string) (json.RawMessage, error) {
	path := strings.TrimSuffix(out, filepath.Ext(out)) + "-pours.json"
	b, _ := json.MarshalIndent(map[string]any{"zones": ps}, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return nil, err
	}
	return t.RunBridge("pours", pcb, path, out)
}
