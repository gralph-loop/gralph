//go:build windows

package main

import (
	"errors"
	"syscall"
)

// isTransientFSError reports whether err is a Windows sharing violation /
// access-denied error: one process opened or renamed a state file while
// another still had it open. Parallel gralph workers hit this between a
// read outside the state lock and a concurrent commit's rename; the
// condition clears as soon as the other handle closes, so it is safe to
// retry.
func isTransientFSError(err error) bool {
	// ERROR_SHARING_VIOLATION (32) has no stdlib syscall constant.
	return errors.Is(err, syscall.Errno(32)) ||
		errors.Is(err, syscall.ERROR_ACCESS_DENIED)
}
