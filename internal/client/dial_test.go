package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflogtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// earlyResponse is what the early-bytes servers send before any request: a
// complete response whose reason phrase carries key, standing for an API
// key the server remembered from an earlier request.
func earlyResponse(key string) string {
	return "HTTP/1.1 200 " + key + "\r\nContent-Length: 0\r\n\r\n"
}

// earlyBytesServer accepts one connection and sends earlyResponse on it as
// soon as it is established (after the TLS handshake when config is set),
// before the request arrives; then it reads the request and waits for the
// client to close the connection. written is closed once the early bytes
// are sent, finished once the client has closed the connection.
type earlyBytesServer struct {
	url               string
	written, finished chan struct{}
}

func startEarlyBytesServer(t *testing.T, key string, config *tls.Config) *earlyBytesServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	server := &earlyBytesServer{url: "http://", written: make(chan struct{}), finished: make(chan struct{})}
	if config != nil {
		listener = tls.NewListener(listener, config)
		server.url = "https://"
	}
	server.url += listener.Addr().String()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer close(server.finished)
		defer func() { _ = conn.Close() }()
		if tlsConn, ok := conn.(*tls.Conn); ok {
			if tlsConn.Handshake() != nil {
				return
			}
		}
		if _, err := io.WriteString(conn, earlyResponse(key)); err != nil {
			return
		}
		close(server.written)
		if req, err := http.ReadRequest(bufio.NewReader(conn)); err == nil {
			_, _ = io.Copy(io.Discard, req.Body)
		}
		_, _ = io.Copy(io.Discard, conn) // until the client closes the connection
	}()
	return server
}

// Go's transport starts reading a new connection before it sends the
// request. Without the gate, bytes the server sends in that interval are
// logged with their content through the standard logger as an "unsolicited
// response". Here, over real connections, the request waits in
// httptrace.GotConn until the server has sent its bytes, so they are there
// before the request is written. With the gate they are read only once the
// request is going out, as its response, and never reach either log, which
// are inspected once the client has closed the connection (Go logs an
// unsolicited response before it closes the connection it came on).
//
// Whether the transport's reader would have looked at the bytes before the
// request went out depends on scheduling here; the synctest tests below
// establish that deterministically.
func TestEarlyBytesNeverReachAnyLog(t *testing.T) {
	const key = "remembered-api-key-0123456789abcdef"
	serverTLS := httptest.NewUnstartedServer(http.NotFoundHandler())
	serverTLS.StartTLS()
	defer serverTLS.Close()

	for name, config := range map[string]*tls.Config{"plain": nil, "TLS": serverTLS.TLS} {
		t.Run(name, func(t *testing.T) {
			standard := captureStandardLog(t)
			server := startEarlyBytesServer(t, key, config)
			var logs bytes.Buffer
			ctx := tflogtest.RootLogger(context.Background(), &logs)
			sentFirst := false
			ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
				GotConn: func(httptrace.GotConnInfo) {
					select {
					case <-server.written:
						sentFirst = true
					case <-time.After(10 * time.Second): // a failure bound, not a delay
					}
				},
			})
			c, err := NewClient(server.url, "test-token", true, 30)
			require.NoError(t, err)

			_, err = c.doRequest(ctx, http.MethodPost, "/api/x", nil)
			require.NoError(t, err, "the early bytes are read as the request's response")
			require.True(t, sentFirst, "the server sent its bytes before the request was written")
			select {
			case <-server.finished:
			case <-time.After(10 * time.Second):
				t.Fatal("the client never closed the connection")
			}
			assert.NotContains(t, standard.String(), key)
			assert.NotContains(t, logs.String(), key)
		})
	}
}

// instrumentedConn records the reads that reach it and the writes made on it.
type instrumentedConn struct {
	net.Conn
	reads, writes atomic.Int32
}

func (c *instrumentedConn) Read(p []byte) (int, error) {
	c.reads.Add(1)
	return c.Conn.Read(p)
}

func (c *instrumentedConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	return c.Conn.Write(p)
}

// A gated connection passes no read to the connection beneath it until the
// first write, however long the reader has been waiting and although the
// peer's bytes are ready. In fake time (testing/synctest), synctest.Wait
// returns only once every goroutine is blocked for good, so "the read has
// not reached the connection" is a fact at that point, not a race.
func TestGatedConnPassesNoReadBeforeTheFirstWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		local, remote := net.Pipe()
		defer func() { _ = remote.Close() }()
		under := &instrumentedConn{Conn: local}
		conn := newGatedConn(under)
		defer func() { _ = conn.Close() }()

		early := []byte("early bytes from the peer")
		go func() { _, _ = remote.Write(early) }() // blocks until something reads it
		received := make(chan string, 1)
		go func() {
			buf := make([]byte, 64)
			n, _ := conn.Read(buf)
			received <- string(buf[:n])
		}()

		synctest.Wait()
		assert.Zero(t, under.reads.Load(), "no read reached the connection before a write")
		assert.Empty(t, received)

		go func() { _, _ = io.Copy(io.Discard, remote) }()
		_, err := conn.Write([]byte("the request"))
		require.NoError(t, err)
		synctest.Wait()
		assert.Equal(t, int32(1), under.writes.Load())
		assert.Equal(t, int32(1), under.reads.Load(), "the waiting read went through once the request was written")
		assert.Equal(t, string(early), <-received)
	})
}

// The transport this package builds, run over in-memory connections in fake
// time, reads nothing a server sends before the request: the server writes
// its early bytes as soon as the connection is up (after the TLS handshake
// for TLS), and at httptrace.GotConn, once every other goroutine (the
// transport's reader and writer, the server) is blocked for good, nothing
// has read them. net.Pipe has no buffer, so the server's write ends exactly
// when something reads the bytes. They are then read as the response to the
// request, and reach no log.
func TestTransportReadsNothingBeforeTheRequest(t *testing.T) {
	const key = "remembered-api-key-0123456789abcdef"
	certificates := httptest.NewUnstartedServer(http.NotFoundHandler())
	certificates.StartTLS()
	serverConfig := certificates.TLS.Clone()
	certificates.Close()
	// A TLS 1.3 server sends session tickets after its Finished message; over
	// a pipe with no buffer the handshake would wait for the client to read
	// them. Tickets play no part in this test.
	serverConfig.SessionTicketsDisabled = true

	for name, config := range map[string]*tls.Config{"plain": nil, "TLS": serverConfig} {
		t.Run(name, func(t *testing.T) {
			standard := captureStandardLog(t)
			synctest.Test(t, func(t *testing.T) {
				var earlyRead, serverDone atomic.Bool
				dialer := gatedDialer{
					tlsConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- in-memory test server
					connectRaw: func(context.Context, string, string) (net.Conn, error) {
						local, remote := net.Pipe()
						go func() {
							defer serverDone.Store(true)
							serveEarlyBytes(remote, config, key, &earlyRead)
						}()
						return local, nil
					},
				}
				url := "http://pocket-id.test"
				if config != nil {
					url = "https://pocket-id.test"
				}
				c, err := NewClient(url, "test-token", true, 30)
				require.NoError(t, err)
				c.httpClient.Transport = dialer.transport()

				var logs bytes.Buffer
				ctx := tflogtest.RootLogger(context.Background(), &logs)
				checked := false
				ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
					GotConn: func(httptrace.GotConnInfo) {
						synctest.Wait()
						assert.False(t, earlyRead.Load(), "nothing read the server's bytes before the request was written")
						checked = true
					},
				})

				_, err = c.doRequest(ctx, http.MethodPost, "/api/x", nil)
				require.NoError(t, err, "the early bytes are read as the request's response")
				require.True(t, checked)
				synctest.Wait()
				assert.True(t, earlyRead.Load())
				assert.True(t, serverDone.Load(), "the client closed the connection")
				assert.NotContains(t, logs.String(), key)
			})
			assert.NotContains(t, standard.String(), key)
		})
	}
}

// serveEarlyBytes is the server side of TestTransportReadsNothingBeforeTheRequest.
// It sets earlyRead once the client has read its early bytes.
func serveEarlyBytes(raw net.Conn, config *tls.Config, key string, earlyRead *atomic.Bool) {
	defer func() { _ = raw.Close() }()
	conn := raw
	if config != nil {
		tlsConn := tls.Server(raw, config)
		if tlsConn.Handshake() != nil {
			return
		}
		conn = tlsConn
	}
	if _, err := io.WriteString(conn, earlyResponse(key)); err != nil {
		return
	}
	earlyRead.Store(true)
	if req, err := http.ReadRequest(bufio.NewReader(conn)); err == nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
	_, _ = io.Copy(io.Discard, conn) // until the client closes the connection
}

// A read waiting at the gate ends when the connection is closed, so a
// cancelled request never leaves the transport's reader stuck.
func TestGatedConnCloseReleasesARead(t *testing.T) {
	local, remote := net.Pipe()
	defer func() { _ = remote.Close() }()
	conn := newGatedConn(local)
	done := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		done <- err
	}()
	require.NoError(t, conn.Close())
	select {
	case err := <-done:
		assert.ErrorIs(t, err, net.ErrClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("the read did not end when the connection closed")
	}
}

// The TLS connections this package sets up verify the server as Go's own
// transport would: against the URL's host, with the configured roots, and
// not at all only when skip_tls_verify is set.
func TestTLSConnectionsVerifyTheServer(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server.StartTLS()
	defer server.Close()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())

	trusted := &http.Client{Transport: newTransport(&tls.Config{RootCAs: roots})}
	resp, err := trusted.Get("https://127.0.0.1:" + port + "/") // the certificate names 127.0.0.1
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	_, err = trusted.Get("https://localhost:" + port + "/") // not a name the certificate holds
	var certErr *tls.CertificateVerificationError
	require.True(t, errors.As(err, &certErr), "the host name is checked: %v", err)

	strict, err := NewClient(server.URL, "test-token", false, 5)
	require.NoError(t, err)
	_, err = strict.send(context.Background(), http.MethodGet, "/", "application/json", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS certificate verification failed", "an untrusted certificate is refused")

	insecure, err := NewClient(server.URL, "test-token", true, 5)
	require.NoError(t, err)
	_, err = insecure.send(context.Background(), http.MethodGet, "/", "application/json", nil)
	assert.NoError(t, err, "skip_tls_verify accepts it")
}

// A server that accepts the TCP connection and never answers the TLS
// handshake holds nothing once the request is over, whether the request was
// cancelled or reached the provider's timeout: the server sees the
// connection end well before tlsHandshakeTimeout, which would otherwise be
// the only limit (and before it, nothing: Go dials on a context detached from
// the request's).
func TestStalledHandshakeEndsWithTheRequest(t *testing.T) {
	cases := map[string]struct {
		timeout int64
		cancel  bool
	}{
		"request cancelled":   {timeout: 30, cancel: true},
		"provider timeout 1s": {timeout: 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer func() { _ = listener.Close() }()
			accepted := make(chan net.Conn, 1)
			go func() {
				if conn, err := listener.Accept(); err == nil {
					accepted <- conn // and never written to: the handshake stalls
				}
			}()

			c, err := NewClient("https://"+listener.Addr().String(), "test-token", true, tc.timeout)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := c.doRequest(ctx, http.MethodPost, "/api/x", nil)
				result <- err
			}()

			var conn net.Conn
			select {
			case conn = <-accepted:
			case <-time.After(10 * time.Second):
				t.Fatal("the client never connected")
			}
			defer func() { _ = conn.Close() }()
			if tc.cancel {
				cancel()
			}
			select {
			case err := <-result:
				require.Error(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("the request did not end")
			}

			// The ClientHello is read, then the read ends because the client
			// closed the connection (EOF, or a reset).
			ended := make(chan struct{})
			go func() {
				_, _ = io.Copy(io.Discard, conn)
				close(ended)
			}()
			select {
			case <-ended:
			case <-time.After(tlsHandshakeTimeout / 2):
				t.Fatal("the connection outlived its request")
			}
		})
	}
}

// Each connection stage ends at its own limit, at the request's deadline if
// that is earlier, and when the request is cancelled, even though the
// transport hands the dialer a context detached from the request.
func TestStageContextFollowsTheRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// What Go's transport passes the dialer: the request context's
		// values without its cancellation or deadline.
		detached := func(request context.Context) context.Context {
			return context.WithoutCancel(withRequestContext(request))
		}

		ctx, release := stageContext(context.Background(), time.Minute)
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		assert.Equal(t, time.Now().Add(time.Minute), deadline, "the stage's own limit, with no request")
		release()

		request, cancelRequest := context.WithTimeout(context.Background(), 2*time.Second)
		ctx, release = stageContext(detached(request), time.Minute)
		deadline, _ = ctx.Deadline()
		want, _ := request.Deadline()
		assert.Equal(t, want, deadline, "the request's earlier deadline")
		cancelRequest()
		synctest.Wait()
		assert.ErrorIs(t, ctx.Err(), context.Canceled, "the request's cancellation")
		release()

		request, cancelRequest = context.WithTimeout(context.Background(), time.Hour)
		ctx, release = stageContext(detached(request), time.Minute)
		deadline, _ = ctx.Deadline()
		assert.Equal(t, time.Now().Add(time.Minute), deadline, "the stage's limit, when the request allows more")
		time.Sleep(time.Minute)
		synctest.Wait()
		assert.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
		release()
		cancelRequest()

		// release ends the stage and stops following the request.
		request, cancelRequest = context.WithCancel(context.Background())
		ctx, release = stageContext(detached(request), time.Minute)
		release()
		assert.ErrorIs(t, ctx.Err(), context.Canceled)
		cancelRequest()
		synctest.Wait()
	})
}
