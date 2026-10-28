package kicad

// fab_extras.go — optional `kicad fab` outputs beyond the JLC file set: a STEP
// 3D model of the board (`--step`) and an IPC-D-356 netlist for flying-probe
// test (`--ipc356`). Both come from kicad-cli; this file picks the arguments,
// validates what was written (a kicad-cli that exits 0 without a usable file
// must not look like a success) and reports footprints that have no 3D model.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// StepOptions are the user-facing STEP choices.
type StepOptions struct {
	BoardOnly bool // bare board body, no component models
	NoDNP     bool // leave out the models of do-not-populate parts
	// SubstModels: use a STEP model of the same name where a footprint points
	// at a VRML one. Off by default: it can hide a missing model.
	SubstModels bool
}

// StepArgs: kicad-cli pcb export step. --force is safe because the output
// always goes to a fresh staging directory.
func StepArgs(pcb, outFile string, o StepOptions) []string {
	a := []string{"pcb", "export", "step", "--force", "--output", outFile}
	if o.SubstModels {
		a = append(a, "--subst-models")
	}
	if o.BoardOnly {
		a = append(a, "--board-only")
	}
	if o.NoDNP {
		a = append(a, "--no-dnp")
	}
	return append(a, pcb)
}

// IPCD356Args: kicad-cli pcb export ipcd356 (the IPC-D-356A bare-board test
// netlist; kicad-cli's separate ipc2581 export is a different format).
func IPCD356Args(pcb, outFile string) []string {
	return []string{"pcb", "export", "ipcd356", "--output", outFile, pcb}
}

// NoModelFootprint is a footprint that contributes nothing to the STEP.
type NoModelFootprint struct {
	Ref       string `json:"ref"`
	Footprint string `json:"footprint"`
	Reason    string `json:"reason"`    // "no 3D model" | "3D model hidden"
	Assembled bool   `json:"assembled"` // JLC places it (not DNP / excluded)
}

// FootprintsWithoutModel lists footprints with no usable (model …) entry: none
// at all, or only hidden ones. Whether the model file exists is not checked
// here (paths use KiCad variables); kicad-cli's own warnings cover that.
func FootprintsWithoutModel(bd *Board) []NoModelFootprint {
	var out []NoModelFootprint
	for _, f := range bd.Footprints {
		shown := false
		for _, m := range f.Models {
			if !m.Hidden && strings.TrimSpace(m.Path) != "" {
				shown = true
				break
			}
		}
		if shown {
			continue
		}
		reason := "no 3D model"
		if len(f.Models) > 0 {
			reason = "3D model hidden"
		}
		out = append(out, NoModelFootprint{Ref: f.Ref, Footprint: f.LibID, Reason: reason, Assembled: f.Assembled()})
	}
	sortNoModel(out)
	return out
}

// Assembled mirrors Part.Assembled for a board footprint.
func (f Footprint) Assembled() bool { return !f.DNP && !f.ExcludeBOM && !f.ExcludePos }

func sortNoModel(l []NoModelFootprint) {
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && refLess(l[j].Ref, l[j-1].Ref); j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}

// StepReport is the fab-report.json section of a STEP export.
type StepReport struct {
	File      string   `json:"file"`
	SHA256    string   `json:"sha256"`
	Bytes     int64    `json:"bytes"`
	BoardOnly bool     `json:"boardOnly"`
	NoDNP     bool     `json:"noDnp,omitempty"`
	Subst     bool     `json:"substModels,omitempty"`
	Args      []string `json:"args"`
	// ModelCheck: "done", or why it was not (board-only has no components).
	ModelCheck        string             `json:"modelCheck"`
	FootprintsNoModel []NoModelFootprint `json:"footprintsWithoutModel"`
	KiCadWarnings     []string           `json:"kicadWarnings,omitempty"`
}

// IPC356Report is the fab-report.json section of an IPC-D-356 export.
type IPC356Report struct {
	File    string   `json:"file"`
	SHA256  string   `json:"sha256"`
	Bytes   int64    `json:"bytes"`
	Records int      `json:"testRecords"` // 317 (through-hole) / 327 (SMD) / 367 (other) lines
	Args    []string `json:"args"`
}

// runCLICapture runs kicad-cli and returns its combined output; a failure
// carries that output.
func runCLICapture(cli string, args []string, log io.Writer) (string, error) {
	cmd := exec.Command(cli, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	if log != nil && buf.Len() > 0 {
		_, _ = log.Write(buf.Bytes())
	}
	if err != nil {
		return buf.String(), fmt.Errorf("kicad-cli %s %s: %w\n%s", args[0], strings.Join(args[1:3], " "), err, buf.String())
	}
	return buf.String(), nil
}

// kicadModelWarnings keeps the output lines that talk about 3D models.
func kicadModelWarnings(out string) []string {
	var w []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		low := strings.ToLower(l)
		if l == "" || !(strings.Contains(low, "model") || strings.Contains(low, "warning")) {
			continue
		}
		if strings.Contains(low, "created") { // "STEP file 'x' created."
			continue
		}
		w = append(w, l)
		if len(w) == 25 {
			break
		}
	}
	return w
}

// FileSHA256 returns the hex SHA-256 and size of a file.
func FileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// CheckSTEPFile fails unless path is a non-empty ISO-10303-21 file that was
// written to completion (ends with END-ISO-10303-21;).
func CheckSTEPFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("kicad-cli wrote no STEP file: %w", err)
	}
	if len(b) == 0 {
		return fmt.Errorf("STEP file %s is empty", filepath.Base(path))
	}
	if !bytes.HasPrefix(bytes.TrimLeft(b, "\xef\xbb\xbf \r\n\t"), []byte("ISO-10303-21;")) {
		return fmt.Errorf("%s is not a STEP file (no ISO-10303-21 header)", filepath.Base(path))
	}
	if !bytes.Contains(b[max(0, len(b)-256):], []byte("END-ISO-10303-21;")) {
		return fmt.Errorf("STEP file %s is truncated (no END-ISO-10303-21 trailer)", filepath.Base(path))
	}
	return nil
}

// CountIPCD356Records validates an IPC-D-356 file and counts its test records
// (317 through-hole, 327 SMD, 367 other); a file without any is useless for a
// flying-probe program and is an error.
func CountIPCD356Records(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("kicad-cli wrote no IPC-D-356 file: %w", err)
	}
	if len(b) == 0 {
		return 0, fmt.Errorf("IPC-D-356 file %s is empty", filepath.Base(path))
	}
	n := 0
	hdr := false
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "P  VER") && strings.Contains(l, "IPC-D-356"):
			hdr = true
		case strings.HasPrefix(l, "317"), strings.HasPrefix(l, "327"), strings.HasPrefix(l, "367"):
			n++
		}
	}
	if !hdr {
		return 0, fmt.Errorf("%s is not an IPC-D-356 file (no 'P  VER IPC-D-356' header)", filepath.Base(path))
	}
	if n == 0 {
		return 0, fmt.Errorf("%s holds no test records (317/327/367): the board has no pads to probe, or the export failed", filepath.Base(path))
	}
	return n, nil
}
