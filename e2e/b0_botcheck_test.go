package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// b0Sites are public bot-detection TEST pages (the brief left the <SITE>
// placeholders unfilled; these are purpose-built, ethical to load, and give a
// clear per-page verdict — see DECISIONS 2026-11-20). We load each page only,
// never submit anything.
var b0Sites = []struct{ name, url string }{
	{"areyouheadless", "https://arh.antoinevastel.com/bots/areyouheadless"},
	{"sannysoft", "https://bot.sannysoft.com/"},
	{"creepjs", "https://abrahamjuliot.github.io/creepjs/"},
}

// TestB0BotDetection loads each bot-detection page on both backends from this
// host's IP, screenshots it into .b0/, and records a short text verdict. It is a
// MEASUREMENT, not a pass/fail gate (it never t.Fail()s on a detection result),
// and only runs when AGENTBROWSER_B0=1 since it reaches external sites.
func TestB0BotDetection(t *testing.T) {
	if os.Getenv("AGENTBROWSER_B0") != "1" {
		t.Skip("set AGENTBROWSER_B0=1 to run the external bot-detection measurement")
	}
	outDir := ".b0"
	_ = os.MkdirAll(outDir, 0o755)

	for _, backend := range []string{"chromium", "camoufox"} {
		t.Run(backend, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			b, err := browser.Open(ctx, backend, browser.Options{Headless: true, AccountDir: t.TempDir(), Logger: t.Logf})
			if err != nil {
				t.Skipf("%s backend unavailable: %v", backend, err)
			}
			defer b.Close()

			for _, site := range b0Sites {
				p, err := b.NewPage(ctx)
				if err != nil {
					t.Errorf("[%s/%s] new page: %v", backend, site.name, err)
					continue
				}
				nav := site.url
				if err := p.Navigate(ctx, nav); err != nil {
					t.Logf("[%s/%s] navigate error (recorded as a result): %v", backend, site.name, err)
				}
				_ = p.WaitStable(ctx, 6*time.Second)
				time.Sleep(2 * time.Second) // let client-side detection settle

				// Screenshot for the record.
				if png, err := p.Screenshot(ctx); err == nil {
					_ = os.WriteFile(filepath.Join(outDir, fmt.Sprintf("%s-%s.png", site.name, backend)), png, 0o644)
				}
				// A short text snapshot for an at-a-glance verdict.
				snippet := ""
				if v, err := p.Eval(ctx, `() => (document.body && document.body.innerText || "").replace(/\s+/g," ").slice(0,300)`); err == nil {
					snippet = v.Str()
				}
				st, _, _ := perceive.New().Perceive(ctx, p)
				t.Logf("[%s/%s] title=%q\n  verdict-text: %s", backend, site.name, st.Title, strings.TrimSpace(snippet))
				_ = p.Close()
			}
		})
	}
}
