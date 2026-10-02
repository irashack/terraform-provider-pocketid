package client

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-log/tflogtest"
	"github.com/stretchr/testify/require"
)

func TestHTTPLogsAndDiagnosticsExcludeCredentials(t *testing.T) {
	for _, status := range []int{201, 400, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var logs bytes.Buffer
			ctx := tflogtest.RootLogger(context.Background(), &logs)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, `{"secret":"fixture-secret","error":"fixture-token fixture-secret"}`)
			}))
			defer s.Close()
			c, _ := NewClient(s.URL, "fixture-token", false, 1)
			_, err := c.doRequestWithContext(ctx, "POST", "/api/oidc/clients/fixture/secrets", map[string]string{"secret": "fixture-secret"})
			require.NotEmpty(t, logs.String())
			require.NotContains(t, logs.String(), "fixture-token")
			require.NotContains(t, logs.String(), "fixture-secret")
			if err != nil {
				require.NotContains(t, err.Error(), "fixture-token")
				require.NotContains(t, err.Error(), "fixture-secret")
			}
		})
	}
}
