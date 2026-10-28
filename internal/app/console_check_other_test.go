//go:build !windows

package app

// ctrlBreakDeliverable: SIGINT needs no console on Unix.
func ctrlBreakDeliverable() (bool, string) { return true, "" }
