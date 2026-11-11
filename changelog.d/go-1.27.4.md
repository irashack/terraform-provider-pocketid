- Built with Go 1.27.4 (`go.mod` toolchain go1.27.4), whose standard library
  fixes GO-2026-6859, an HTTP/1 server crash in `net/http`. The provider runs
  no HTTP/1 server; 3.2.2, built with go1.27.3, is not exposed to it.
