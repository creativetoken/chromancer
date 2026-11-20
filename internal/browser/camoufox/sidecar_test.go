package camoufox

import (
	"strings"
	"testing"
)

func TestParseReady(t *testing.T) {
	t.Run("ready line after noise", func(t *testing.T) {
		in := "some stderr-ish log\nanother line\n" +
			`{"ws":"ws://127.0.0.1:5123/camoufox","display":":99","playwright_version":"1.52.0"}` + "\n"
		rd, err := parseReady(strings.NewReader(in))
		if err != nil {
			t.Fatalf("parseReady: %v", err)
		}
		if rd.WS != "ws://127.0.0.1:5123/camoufox" || rd.Display != ":99" || rd.PlaywrightVersion != "1.52.0" {
			t.Errorf("parsed wrong: %+v", rd)
		}
	})

	t.Run("error line surfaces", func(t *testing.T) {
		in := `{"error":"camoufox/playwright not installed: No module named 'camoufox'"}` + "\n"
		_, err := parseReady(strings.NewReader(in))
		if err == nil || !strings.Contains(err.Error(), "not installed") {
			t.Fatalf("expected an install error, got %v", err)
		}
	})

	t.Run("no ready line", func(t *testing.T) {
		_, err := parseReady(strings.NewReader("just logs\nno json here\n"))
		if err == nil || !strings.Contains(err.Error(), "ready line") {
			t.Fatalf("expected an exited-before-ready error, got %v", err)
		}
	})

	t.Run("missing ws endpoint", func(t *testing.T) {
		_, err := parseReady(strings.NewReader(`{"display":":99"}` + "\n"))
		if err == nil || !strings.Contains(err.Error(), "ws endpoint") {
			t.Fatalf("expected a missing-ws error, got %v", err)
		}
	})
}
