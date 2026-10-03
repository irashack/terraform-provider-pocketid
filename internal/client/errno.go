package client

// Windows reports socket failures with Winsock error codes. Go's syscall
// package on Windows defines the POSIX names (ECONNREFUSED, ECONNRESET, ...)
// as invented values that a real socket error never equals, so a refused or
// reset connection there would read as an unknown failure and a GET would not
// be retried. winsockClass maps the real codes; it is plain arithmetic so it
// is tested on every platform, and nativeErrnoClass (errno_windows.go,
// errno_other.go) applies it only on Windows.
const (
	wsaENETUNREACH  = 10051
	wsaECONNRESET   = 10054
	wsaETIMEDOUT    = 10060
	wsaECONNREFUSED = 10061
	wsaEHOSTUNREACH = 10065
)

// winsockClass returns the fixed description and retry decision for a
// Winsock error code, and false for any other code. The descriptions and
// decisions are the ones classifyTransportError gives the POSIX errors.
func winsockClass(code uintptr) (string, bool, bool) {
	switch code {
	case wsaECONNREFUSED:
		return "connection refused", true, true
	case wsaECONNRESET:
		return "connection reset by peer", true, true
	case wsaETIMEDOUT:
		return "network timeout", true, true
	case wsaENETUNREACH:
		return "network unreachable", false, true
	case wsaEHOSTUNREACH:
		return "host unreachable", false, true
	}
	return "", false, false
}
