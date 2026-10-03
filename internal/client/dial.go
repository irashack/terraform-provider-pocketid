package client

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"
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
//
// Go's transport dials on a context it detaches from the request's
// cancellation and deadline, so that a connection being set up can serve a
// later request; it would let a server that accepts TCP and never completes
// the TLS handshake hold the connection and its goroutine indefinitely after
// the request gave up. Connections here are never reused, so each stage
// (connecting, then the TLS handshake) runs under the request's own
// cancellation and deadline again (see withRequestContext), and under a
// fixed limit of its own besides (connectTimeout, tlsHandshakeTimeout).
func newTransport(config *tls.Config) *http.Transport {
	dialer := gatedDialer{tlsConfig: config}
	return &http.Transport{
		TLSClientConfig:   config,
		DialContext:       dialer.dial,
		DialTLSContext:    dialer.dialTLS,
		DisableKeepAlives: true,
	}
}

// Fixed limits for the stages of setting up a connection, applied whatever
// the request allows; the request's own deadline usually ends them sooner
// (the provider's timeout defaults to 30 seconds). They are the values of
// Go's DefaultTransport, so a connection is never held longer than with the
// standard library's default client.
const (
	// connectTimeout bounds name resolution and the TCP connection.
	connectTimeout = 30 * time.Second
	// tlsHandshakeTimeout bounds the TLS handshake.
	tlsHandshakeTimeout = 10 * time.Second
)

// requestContextKey is the context key under which withRequestContext
// stores a request's context for the dialer.
type requestContextKey struct{}

// withRequestContext returns ctx carrying itself, for the request built
// from it. Go's transport keeps a request context's values in the dial
// context it detaches, so stageContext can find the request's cancellation
// and deadline again.
func withRequestContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestContextKey{}, ctx)
}

// stageContext returns the context one connection stage runs under: ctx,
// the transport's dial context, ended at limit from now, at the request's
// deadline if that is earlier, and as soon as the request is cancelled.
// release frees it once the stage is over; an established connection is not
// affected by its later end.
func stageContext(ctx context.Context, limit time.Duration) (_ context.Context, release func()) {
	deadline := time.Now().Add(limit)
	request, scoped := ctx.Value(requestContextKey{}).(context.Context)
	if scoped {
		if requestDeadline, ok := request.Deadline(); ok && requestDeadline.Before(deadline) {
			deadline = requestDeadline
		}
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	if !scoped {
		return ctx, cancel
	}
	stop := context.AfterFunc(request, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

type gatedDialer struct {
	tlsConfig *tls.Config
}

// connect opens the TCP connection, within connectTimeout and the request's
// own bounds.
func (d gatedDialer) connect(ctx context.Context, network, addr string) (net.Conn, error) {
	ctx, release := stageContext(ctx, connectTimeout)
	defer release()
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

func (d gatedDialer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := d.connect(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return newGatedConn(conn), nil
}

func (d gatedDialer) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	raw, err := d.connect(ctx, network, addr)
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
	handshakeCtx, release := stageContext(ctx, tlsHandshakeTimeout)
	defer release()
	conn := tls.Client(raw, config)
	// An interrupted handshake closes raw itself; closing it again is
	// harmless and covers every other failure.
	if err := conn.HandshakeContext(handshakeCtx); err != nil {
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
