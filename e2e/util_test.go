package e2e

import (
	"io"
	"net/http"
	"testing"
)

var httpClient = &http.Client{}

func httpGet(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data
}

// httpGetNoT fetches a URL outside a test helper (used by solver/asker stubs).
func httpGetNoT(url string) []byte {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data
}
