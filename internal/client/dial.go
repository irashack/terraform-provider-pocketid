package client

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"
)

// newTransport builds the HTTP transport every request goes through.
//
// Go's HTTP/1 transport starts reading a new connection before it has sent
// the request on it. Bytes a server sends in that interval, or while a
// connection is idle, are treated as an "unsolicited response" and written
// with their content to Go's standard logger, outside every redaction in this
// package; a server could send the API key it received on an earlier request
// that way. Two measures close this:
//   - connections are never reused (DisableKeepAlives, and Connection: close
//     on every request), so none is ever idle;
//   - every connection, plain or TLS, holds back reads until the request is
//     being written (gatedConn), so the transport never sees bytes it is not
//     expecting a response for. For TLS the gate sits above the handshake,
//     so it opens with the first HTTP write, not the ClientHello.
//
// The TLS connection is set up here (DialTLSContext) with config, the same
// settings Go's transport would use: the server name is the URL's host.
func newTransport(config *tls.Config) *http.Transport {
	dialer := gatedDialer{tlsConfig: config}
	return &http.Transport{
		TLSClientConfig:   config,
		DialContext:       dialer.dial,
		DialTLSContext:    dialer.dialTLS,
		DisableKeepAlives: true,
	}
}

type gatedDialer struct {
	tlsConfig *tls.Config
}

func (d gatedDialer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return newGatedConn(conn), nil
}

func (d gatedDialer) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	raw, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	config := d.tlsConfig.Clone()
	if config.ServerName == "" {
		host, _, splitErr := net.SplitHostPort(addr)
		if splitErr != nil {
			host = addr
		}
		config.ServerName = host
	}
	conn := tls.Client(raw, config)
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return newGatedConn(conn), nil
}

// gatedConn holds back reads until the first write: until the transport is
// sending a request, it reads nothing, so it never sees bytes it expects no
// response for. Closing the connection releases a read waiting at the gate.
type gatedConn struct {
	net.Conn
	writeOnce sync.Once
	closeOnce sync.Once
	written   chan struct{}
	closed    chan struct{}
}

func newGatedConn(conn net.Conn) *gatedConn {
	return &gatedConn{Conn: conn, written: make(chan struct{}), closed: make(chan struct{})}
}

func (c *gatedConn) Write(p []byte) (int, error) {
	c.writeOnce.Do(func() { close(c.written) })
	return c.Conn.Write(p)
}

func (c *gatedConn) Read(p []byte) (int, error) {
	select {
	case <-c.written:
		return c.Conn.Read(p)
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *gatedConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}
