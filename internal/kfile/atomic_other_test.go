//go:build !windows

package kfile

import "syscall"

// retryableErr is an error isRetryable accepts on this platform.
var retryableErr error = syscall.EBUSY
