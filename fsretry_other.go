//go:build !windows

package main

// isTransientFSError reports whether err is a transient filesystem error
// worth retrying. Only Windows has them (sharing violations between a
// reader and a concurrent rename); elsewhere the answer is always no.
func isTransientFSError(error) bool { return false }
