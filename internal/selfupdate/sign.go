package selfupdate

import (
	"os/exec"
	"runtime"
)

// ensureLocalSignature gives a macOS binary that carries no code signature an
// ad-hoc one. The Go linker signs darwin/arm64 binaries itself but leaves
// darwin/amd64 ones unsigned; on Intel Macs macOS then cannot identify the
// process (tccd: "Failed to copy signing info ... -67062 / code object is not
// signed at all"), and a child it starts, such as kicad-cli, can block forever
// on a privacy prompt nobody can answer. A binary that already verifies
// (Developer ID, or a linker signature) is left untouched. Best effort.
var ensureLocalSignature = func(path string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	if exec.Command("/usr/bin/codesign", "--verify", path).Run() == nil {
		return nil
	}
	return exec.Command("/usr/bin/codesign", "--force", "--sign", "-", path).Run()
}
