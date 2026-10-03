//go:build windows

package client

import "syscall"

// nativeErrnoClass classifies a Winsock error (see winsockClass).
func nativeErrnoClass(errno syscall.Errno) (string, bool, bool) {
	return winsockClass(uintptr(errno))
}
