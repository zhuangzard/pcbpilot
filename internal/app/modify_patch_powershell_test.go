package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// Exercise the real legacy PowerShell -> native exe boundary from issue #192.
// Byte fixtures alone cannot verify PowerShell's native argument marshalling.
func TestModifyPatchWindowsPowerShell51(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows PowerShell 5.1")
	}
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "pcbpilot.exe")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/pcbpilot")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, domain := range []string{"sch", "pcb"} {
		t.Run(domain, func(t *testing.T) {
			cfg, captured, cleanup := newCapturingDaemon(t)
			defer cleanup()
			script := filepath.Join(dir, "patch-test.ps1")
			body := `param($Binary, $PatchPath, $HostName, $Ports, $Domain)
$ErrorActionPreference = 'Stop'
if ($PSVersionTable.PSVersion.Major -ne 5 -or $PSVersionTable.PSVersion.Minor -ne 1) { throw 'requires PowerShell 5.1' }
'{"rotation":90}' | Set-Content -Encoding UTF8 -LiteralPath $PatchPath
& $Binary --host $HostName --ports $Ports --skip-version-check $Domain modify --id p1 --patch-file $PatchPath
exit $LASTEXITCODE
`
			if err := os.WriteFile(script, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, binary, filepath.Join(dir, "patch with spaces.json"), cfg.host, cfg.ports, domain)
			cmd.Env = append(os.Environ(), "PCBPILOT_AUDIT_DIR="+filepath.Join(dir, "state"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("PowerShell: %v\n%s", err, out)
			}
			captured.mu.Lock()
			defer captured.mu.Unlock()
			wantAction := "pcb.component.modify"
			if domain == "sch" {
				wantAction = "schematic.component.modify"
			}
			patch, ok := captured.payload["patch"].(map[string]any)
			if captured.action != wantAction || !ok || patch["rotation"] != float64(90) {
				t.Fatalf("unexpected dispatch: %s %#v", captured.action, captured.payload)
			}
		})
	}
}
