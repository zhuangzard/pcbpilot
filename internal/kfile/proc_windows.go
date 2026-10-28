//go:build windows

package kfile

import (
	"errors"
	"syscall"
	"unsafe"
)

// SystemProcs lists processes with CreateToolhelp32Snapshot.
func SystemProcs() ([]Proc, error) {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(snap)
	var e syscall.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []Proc
	for err = syscall.Process32First(snap, &e); err == nil; err = syscall.Process32Next(snap, &e) {
		out = append(out, Proc{PID: int(e.ProcessID), Name: syscall.UTF16ToString(e.ExeFile[:])})
	}
	if !errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return out, nil
}
