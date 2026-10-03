//go:build !windows

package client

import "syscall"

// nativeErrnoClass has nothing to add where the POSIX errno names are the
// real ones (see winsockClass).
func nativeErrnoClass(syscall.Errno) (string, bool, bool) {
	return "", false, false
}
