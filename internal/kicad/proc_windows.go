//go:build windows

package kicad

import "os/exec"

func ownGroup(cmd *exec.Cmd) {}
