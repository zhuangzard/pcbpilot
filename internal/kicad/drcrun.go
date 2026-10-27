package kicad

// drcrun.go — kicad-cli DRC on a private copy of a board.
//
// The board (and its .kicad_pro / .kicad_dru, plus the schematic hierarchy
// for --schematic-parity) is copied into a fresh temporary directory and
// checked there, so the user's project never gets a .kicad_prl, a lock file
// or a refilled zone from a check. kicad-cli runs with the isolated KiCad
// configuration of kicadenv.go: with the user's global library tables
// `pcb drc --schematic-parity` loaded every bundled library (field report
// 2026-10-26, Windows + KiCad 10: no output after 10 minutes; isolated 26 s).
// The project's own fp-lib-table / sym-lib-table travel with the copy,
// ${KIPRJMOD} resolved to the original project directory, so project
// libraries still resolve; global-only libraries show as the
// lib_footprint_* environment warnings.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DRCTimeout bounds one kicad-cli DRC run (large boards take minutes).
var DRCTimeout = 30 * time.Minute

// DRCRunOptions drives RunDRC.
type DRCRunOptions struct {
	PCB             string
	SchematicParity bool
	CLI             string // kicad-cli
	// SaveRaw: copy kicad-cli's JSON report here ("" = keep nothing).
	SaveRaw string
}

// DRCRun is one finished DRC run.
type DRCRun struct {
	PCB       string     `json:"pcb"`
	PCBSha256 string     `json:"pcbSha256"`
	Report    *DRCReport `json:"-"`
	Raw       []byte     `json:"-"`
	// Policy is the project's DRC policy (nil: no .kicad_pro); PolicyErr
	// a .kicad_pro that could not be read.
	Policy    *DRCPolicy  `json:"-"`
	PolicyErr error       `json:"-"`
	Index     *BoardIndex `json:"-"`
	Staged    []string    `json:"staged"` // files copied, relative to the project dir
	Seconds   float64     `json:"seconds"`
	Isolated  bool        `json:"isolatedConfig"`
	Notes     []string    `json:"notes,omitempty"`
}

// ErrDRCStage marks a project that could not be copied for the check.
var ErrDRCStage = errors.New("cannot stage the board for DRC")

// RunDRC copies the board into a temporary directory, runs `kicad-cli pcb
// drc --format json --severity-all [--schematic-parity]` there and parses
// the report strictly. Any failure is an error wrapping ErrDRCReport or
// ErrDRCStage: the check did not happen.
func RunDRC(o DRCRunOptions) (*DRCRun, error) {
	start := time.Now()
	pcb, err := filepath.Abs(o.PCB)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(pcb)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDRCStage, err)
	}
	sum := sha256.Sum256(data)
	run := &DRCRun{PCB: pcb, PCBSha256: hex.EncodeToString(sum[:])}
	if run.Index, err = ParseBoardIndex(data); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrDRCStage, pcb, err)
	}
	dir := filepath.Dir(pcb)
	base := strings.TrimSuffix(filepath.Base(pcb), ".kicad_pcb")
	if locks, _ := filepath.Glob(filepath.Join(dir, "~"+base+".*.lck")); len(locks) > 0 {
		run.Notes = append(run.Notes, "KiCad lock file present ("+filepath.Base(locks[0])+"): edits not saved to disk are not in this check")
	}
	pro := filepath.Join(dir, base+".kicad_pro")
	if _, err := os.Stat(pro); err == nil {
		run.Policy, run.PolicyErr = ReadDRCPolicy(pro)
	} else if !os.IsNotExist(err) {
		run.PolicyErr = err
	} else {
		run.Notes = append(run.Notes, "no "+base+".kicad_pro next to the board: KiCad's default rules and severities apply")
	}
	tmp, err := os.MkdirTemp("", "pcbpilot-drc-")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDRCStage, err)
	}
	defer os.RemoveAll(tmp)
	if run.Staged, err = StageProject(pcb, tmp, o.SchematicParity); err != nil {
		return nil, err
	}
	staged := filepath.Join(tmp, filepath.Base(pcb))
	out := filepath.Join(tmp, "pcbpilot-drc.json")
	args := []string{"pcb", "drc", "--format", "json", "--severity-all"}
	if o.SchematicParity {
		args = append(args, "--schematic-parity")
	}
	args = append(args, "-o", out, staged)
	ctx, cancel := context.WithTimeout(context.Background(), DRCTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.CLI, args...)
	cmd.Dir = tmp
	cmd.Env = SchCLIEnv()
	run.Isolated = cmd.Env != nil
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err = cmd.Run()
	raw, rerr := os.ReadFile(out)
	// Without --exit-code-violations a non-zero exit is kicad-cli failing.
	if err != nil || rerr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("timed out after %s", DRCTimeout)
		} else if err == nil {
			err = rerr
		}
		return nil, fmt.Errorf("%w: kicad-cli pcb drc: %v: %s (fix: check the board opens in KiCad 10; set %s to kicad-cli)", ErrDRCReport, err, strings.TrimSpace(buf.String()), EnvCLI)
	}
	if run.Report, err = ParseDRCWith(raw, DRCParseOptions{SchematicParity: o.SchematicParity}); err != nil {
		return nil, err
	}
	run.Raw = raw
	if o.SaveRaw != "" {
		if err := os.WriteFile(o.SaveRaw, raw, 0o644); err != nil {
			return nil, err
		}
	}
	run.Seconds = time.Since(start).Round(10 * time.Millisecond).Seconds()
	return run, nil
}

// StageProject copies what DRC reads of the board pcb into dst: the board,
// its .kicad_pro / .kicad_dru, and the project library tables with
// ${KIPRJMOD} pointing back at the original directory; with sheets, also
// the root schematic <board>.kicad_sch and every sheet of its hierarchy
// (same relative paths). It returns the copied paths relative to the
// project directory.
func StageProject(pcb, dst string, sheets bool) ([]string, error) {
	base := strings.TrimSuffix(filepath.Base(pcb), ".kicad_pcb")
	s := &stager{dir: filepath.Dir(pcb), dst: dst}
	if err := s.cp(base+".kicad_pcb", false); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDRCStage, err)
	}
	if err := s.project(base); err != nil {
		return nil, err
	}
	if !sheets {
		return s.copied, nil
	}
	root := filepath.Join(s.dir, base+".kicad_sch")
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("%w: --schematic-parity needs the project's root schematic %s next to the board: %v", ErrDRCStage, filepath.Base(root), err)
	}
	if err := s.sheets(root); err != nil {
		return nil, err
	}
	return s.copied, nil
}

// StageSchematic copies the hierarchy of the root schematic root, with its
// project file and library tables, into dst and returns the staged root.
func StageSchematic(root, dst string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	s := &stager{dir: filepath.Dir(abs), dst: dst}
	if err := s.project(strings.TrimSuffix(filepath.Base(abs), ".kicad_sch")); err != nil {
		return "", err
	}
	if err := s.sheets(abs); err != nil {
		return "", err
	}
	return filepath.Join(dst, filepath.Base(abs)), nil
}

// stager copies project files from dir to dst under the same relative paths.
type stager struct {
	dir, dst string
	copied   []string
}

// cp copies dir/rel; rewrite points ${KIPRJMOD} at dir (library tables).
func (s *stager) cp(rel string, rewrite bool) error {
	b, err := os.ReadFile(filepath.Join(s.dir, rel))
	if err != nil {
		return err
	}
	if rewrite {
		t := strings.ReplaceAll(string(b), "${KIPRJMOD}", filepath.ToSlash(s.dir))
		b = []byte(strings.ReplaceAll(t, "$(KIPRJMOD)", filepath.ToSlash(s.dir)))
	}
	to := filepath.Join(s.dst, rel)
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		return err
	}
	s.copied = append(s.copied, filepath.ToSlash(rel))
	return nil
}

// project copies <base>.kicad_pro / .kicad_dru and the library tables
// that exist.
func (s *stager) project(base string) error {
	for _, rel := range []string{base + ".kicad_pro", base + ".kicad_dru", "fp-lib-table", "sym-lib-table"} {
		if contains(s.copied, rel) {
			continue
		}
		if err := s.cp(rel, !strings.HasPrefix(rel, base)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("%w: %v", ErrDRCStage, err)
		}
	}
	return nil
}

// sheets copies root and every sheet of its hierarchy (all under dir).
func (s *stager) sheets(root string) error {
	files, err := SheetFiles(root)
	if err != nil {
		return fmt.Errorf("%w: schematic hierarchy of %s: %v", ErrDRCStage, root, err)
	}
	for _, f := range files {
		rel, err := filepath.Rel(s.dir, f)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("%w: sheet %s lies outside the project directory %s (fix: keep every sheet under the project directory)", ErrDRCStage, f, s.dir)
		}
		if contains(s.copied, filepath.ToSlash(rel)) {
			continue
		}
		if err := s.cp(rel, false); err != nil {
			return fmt.Errorf("%w: %v", ErrDRCStage, err)
		}
	}
	return nil
}
