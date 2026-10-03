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
	"testing"
	"testing/synctest"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflogtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// earlyBytesServer sends a response carrying key as soon as a connection is
// established (after the TLS handshake when config is set), before the
// request arrives, then reads the request and closes. key stands for an API
// key the server remembered from an earlier request.
func earlyBytesServer(t *testing.T, key string, config *tls.Config) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	scheme := "http"
	if config != nil {
		listener = tls.NewListener(listener, config)
		scheme = "https"
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				if tlsConn, ok := conn.(*tls.Conn); ok {
					if tlsConn.Handshake() != nil {
						return
					}
				}
				_, _ = io.WriteString(conn, "HTTP/1.1 200 "+key+"\r\nContent-Length: 0\r\n\r\n")
				if req, err := http.ReadRequest(bufio.NewReader(conn)); err == nil {
					_, _ = io.Copy(io.Discard, req.Body)
				}
			}()
		}
	}()
	return scheme + "://" + listener.Addr().String()
}

// Go's transport starts reading a new connection before it sends the
// request. Without the gate, bytes the server sends in that interval are
// logged with their content through the standard logger as an "unsolicited
// response". Holding the connection in httptrace.GotConn widens the
// interval: the server's bytes are certainly there before the request is
// written. With the gate they are read only once the request is going out,
// as its response, and never reach either log.
func TestEarlyBytesNeverReachAnyLog(t *testing.T) {
	const key = "remembered-api-key-0123456789abcdef"
	serverTLS := httptest.NewUnstartedServer(http.NotFoundHandler())
	serverTLS.StartTLS()
	defer serverTLS.Close()

	for name, config := range map[string]*tls.Config{"plain": nil, "TLS": serverTLS.TLS} {
		t.Run(name, func(t *testing.T) {
			standard := captureStandardLog(t)
			url := earlyBytesServer(t, key, config)
			var logs bytes.Buffer
			ctx := tflogtest.RootLogger(context.Background(), &logs)
			ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
				GotConn: func(httptrace.GotConnInfo) { time.Sleep(500 * time.Millisecond) },
			})
			c, err := NewClient(url, "test-token", true, 5)
			require.NoError(t, err)

			_, err = c.doRequest(ctx, http.MethodPost, "/api/x", nil)
			require.NoError(t, err, "the early bytes are read as the request's response")
			time.Sleep(100 * time.Millisecond) // let any transport goroutine finish logging
			assert.NotContains(t, standard.String(), key)
			assert.NotContains(t, logs.String(), key)
		})
	}
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
