package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDeHeadless(t *testing.T) {
	in := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/157.0.0.0 Safari/537.36"
	want := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36"
	if got := deHeadless(in); got != want {
		t.Errorf("deHeadless:\n got %q\nwant %q", got, want)
	}
	// A real (headful) UA must be unchanged.
	if got := deHeadless(want); got != want {
		t.Errorf("deHeadless altered a clean UA: %q", got)
	}
}

func TestCleanBrands(t *testing.T) {
	in := []uaBrand{
		{Brand: "Chromium", Version: "157"},
		{Brand: "HeadlessChrome", Version: "157"},
		{Brand: "Not=A?Brand", Version: "24"},
	}
	got := cleanBrands(in)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[1].Brand != "Google Chrome" || got[1].Version != "157" {
		t.Errorf("headless brand not rewritten: %+v", got[1])
	}
	if got[0].Brand != "Chromium" || got[2].Brand != "Not=A?Brand" {
		t.Errorf("non-headless brands altered: %+v", got)
	}
	for _, b := range got {
		if strings.Contains(b.Brand, "Headless") {
			t.Errorf("headless marker survived in %q", b.Brand)
		}
	}
	if cleanBrands(nil) != nil {
		t.Error("empty input should yield nil")
	}
}

// TestUAStripsHeadlessMarker launches real headless Chrome and verifies that
// neither the User-Agent string nor the Sec-CH-UA client-hint header announces
// headless, and that the JS-visible navigator.userAgentData agrees. Gated
// behind AGENTBROWSER_BROWSER_TEST=1 so the normal suite needs no browser.
func TestUAStripsHeadlessMarker(t *testing.T) {
	if os.Getenv("AGENTBROWSER_BROWSER_TEST") != "1" {
		t.Skip("set AGENTBROWSER_BROWSER_TEST=1 to run the browser integration test")
	}
	var gotUA, gotCHUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			gotUA = r.Header.Get("User-Agent")
			gotCHUA = r.Header.Get("Sec-CH-UA")
		}
		w.Header().Set("Accept-CH", "Sec-CH-UA, Sec-CH-UA-Platform, Sec-CH-UA-Full-Version-List")
		w.Write([]byte(`<!doctype html><title>t</title><body>ok`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	b, err := Launch(ctx, Options{Headless: true, ProfileDir: t.TempDir()})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer b.Close()

	if strings.Contains(b.UserAgent(), "HeadlessChrome") {
		t.Errorf("effective UA still contains HeadlessChrome: %q", b.UserAgent())
	}
	if !strings.Contains(b.UserAgent(), "Chrome/") {
		t.Errorf("effective UA is not a Chrome UA: %q", b.UserAgent())
	}

	p, err := b.NewPage(ctx)
	if err != nil {
		t.Fatalf("new page: %v", err)
	}
	if err := p.Navigate(ctx, srv.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	// Second navigation so the client sends Sec-CH-UA per the Accept-CH grant.
	if err := p.Navigate(ctx, srv.URL); err != nil {
		t.Fatalf("navigate 2: %v", err)
	}

	brands, err := p.rp.Eval(`() => JSON.stringify((navigator.userAgentData && navigator.userAgentData.brands) || [])`)
	if err != nil {
		t.Fatalf("eval brands: %v", err)
	}

	t.Logf("effective UA:   %s", b.UserAgent())
	t.Logf("request UA:     %s", gotUA)
	t.Logf("Sec-CH-UA:      %s", gotCHUA)
	t.Logf("navigator.brands: %s", brands.Value.Str())

	if strings.Contains(gotUA, "HeadlessChrome") {
		t.Errorf("request User-Agent leaked HeadlessChrome: %q", gotUA)
	}
	if strings.Contains(gotCHUA, "HeadlessChrome") || strings.Contains(gotCHUA, "Headless") {
		t.Errorf("Sec-CH-UA leaked headless: %q", gotCHUA)
	}
	if strings.Contains(brands.Value.Str(), "Headless") {
		t.Errorf("navigator.userAgentData.brands leaked headless: %q", brands.Value.Str())
	}
}
