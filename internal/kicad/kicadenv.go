package kicad

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Schematic kicad-cli runs (ERC, netlist / SVG export) get an isolated KiCad
// config whose global symbol and footprint tables are EMPTY.
//
// Why: KiCad chains the user's GLOBAL sym-lib-table in front of the project
// one, and with the stock table that means loading every bundled library on
// each run. field report 2026-10-15 (Windows, KiCad 10): `kicad-cli sch erc` hung
// there; the board sessions only got through with KICAD_CONFIG_HOME pointed at
// a folder holding empty tables. pcbpilot-generated sheets embed every symbol
// (lib_symbols) and ship a ${KIPRJMOD} project sym-lib-table, so they need
// nothing from the global tables. A sheet whose symbols resolve only through
// the user's global libraries can opt out with PCBPILOT_KICAD_ISOLATE=0.
//
// An explicitly set KICAD_CONFIG_HOME always wins. PCB/fab runs keep the
// inherited environment: user boards may resolve footprints through the
// global fp-lib-table.
const EnvIsolateConfig = "PCBPILOT_KICAD_ISOLATE"

// SchCLITimeout bounds one schematic kicad-cli run, so a hang becomes an
// error instead of blocking a build forever.
var SchCLITimeout = 10 * time.Minute

// kicadSettingsVersions are the settings sub-directories KiCad appends to
// KICAD_CONFIG_HOME; a version directory without a sym-lib-table makes KiCad
// seed it from the stock template again — exactly the slow path.
var kicadSettingsVersions = []string{"7.0", "8.0", "9.0", "10.0", "11.0"}

var (
	isolatedOnce sync.Once
	isolatedDir  string
)

// isolatedConfigDir creates (once per process, atomically across processes)
// a KiCad config directory with empty global library tables.
func isolatedConfigDir() string {
	isolatedOnce.Do(func() {
		dir := filepath.Join(CacheRoot(), "kicad-config", "v1")
		tables := map[string]string{
			"sym-lib-table": LibTableText("sym_lib_table", nil),
			"fp-lib-table":  LibTableText("fp_lib_table", nil),
		}
		for _, sub := range append([]string{""}, kicadSettingsVersions...) {
			d := filepath.Join(dir, sub)
			if err := os.MkdirAll(d, 0o755); err != nil {
				return
			}
			for name, text := range tables {
				p := filepath.Join(d, name)
				if b, err := os.ReadFile(p); err == nil && string(b) == text {
					continue
				}
				tmp, err := os.CreateTemp(d, name+".tmp-*")
				if err != nil {
					return
				}
				_, werr := tmp.WriteString(text)
				cerr := tmp.Close()
				if werr != nil || cerr != nil || os.Rename(tmp.Name(), p) != nil {
					_ = os.Remove(tmp.Name())
					if b, err := os.ReadFile(p); err != nil || string(b) != text {
						return // a concurrent writer may have won the rename
					}
				}
			}
		}
		isolatedDir = dir
	})
	return isolatedDir
}

// SchCLIEnv is the environment for a schematic kicad-cli child, or nil to
// inherit the parent's unchanged (opted out, KICAD_CONFIG_HOME already set,
// or the isolated directory could not be prepared).
func SchCLIEnv() []string {
	if os.Getenv(EnvIsolateConfig) == "0" || os.Getenv("KICAD_CONFIG_HOME") != "" {
		return nil
	}
	dir := isolatedConfigDir()
	if dir == "" {
		return nil
	}
	return append(os.Environ(), "KICAD_CONFIG_HOME="+dir)
}

// SchCLICommand builds a schematic kicad-cli command with the isolated
// environment and SchCLITimeout. Call the returned cancel when done.
func SchCLICommand(cli string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), SchCLITimeout)
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Env = SchCLIEnv()
	return cmd, cancel
}
