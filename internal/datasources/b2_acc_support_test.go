//go:build acc
// +build acc

package datasources_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
)

// b2AccAPI calls the fixture's API directly, as the fixture's admin. It sets
// up and inspects what a data source reads, independently of the provider's
// resources. Response bodies are decoded into out and never printed.
func b2AccAPI(t *testing.T, method, path string, body, out any) int {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encoding the request for %s %s: %v", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, os.Getenv("POCKETID_BASE_URL")+path, reader)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, path, err)
	}
	req.Header.Set("X-API-KEY", os.Getenv("POCKETID_API_TOKEN"))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed", method, path)
	}
	defer func() { _ = response.Body.Close() }()
	if out != nil && response.StatusCode < 300 {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			t.Fatalf("%s %s returned an unreadable body", method, path)
		}
	}
	return response.StatusCode
}

// b2AccCreate posts a new object, requires 201 and returns its ID. The object
// is deleted again when the test ends.
func b2AccCreate(t *testing.T, collection string, body any) string {
	t.Helper()
	var created struct {
		ID string `json:"id"`
	}
	if status := b2AccAPI(t, "POST", "/api/"+collection, body, &created); status != http.StatusCreated || created.ID == "" {
		t.Fatalf("creating in %s answered %d", collection, status)
	}
	t.Cleanup(func() { b2AccAPI(t, "DELETE", fmt.Sprintf("/api/%s/%s", collection, created.ID), nil, nil) })
	return created.ID
}
