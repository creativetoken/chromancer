package camoufox

import (
	"context"
	"fmt"

	pw "github.com/playwright-community/playwright-go"

	"github.com/creativetoken/chromancer/internal/browser"
)

func init() { browser.Register("camoufox", Launch) }

// cfBrowser is one Camoufox (Firefox) instance for one account: the Python
// sidecar's Playwright server, a playwright-go connection to it, and one browser
// context whose cookies/storage are restored per run (no persistent profile).
type cfBrowser struct {
	pw      *pw.Playwright
	br      pw.Browser
	bctx    pw.BrowserContext
	sidecar *Sidecar
	caps    browser.Capabilities
	opts    browser.Options
	ua      string
}

// Launch is the registered "camoufox" backend Factory. It starts the sidecar,
// connects playwright-go to it over the ws endpoint, and opens one context. It
// does NOT apply any UA/client-hint override — Camoufox keeps its own consistent
// fingerprint (Capabilities.SupportsUAOverride = false).
func Launch(ctx context.Context, o browser.Options) (browser.Browser, error) {
	fpPath := ""
	region := regionFromProxy(o.ProxyURL)
	if o.AccountDir != "" {
		fpPath = fingerprintPath(o.AccountDir)
	}
	sc, err := StartSidecar(ctx, SidecarOptions{
		ProxyURL:        o.ProxyURL,
		GeoIP:           o.ProxyURL != "",
		FingerprintPath: fpPath,
		Region:          region,
		Logger:          o.Logger,
	})
	if err != nil {
		return nil, err
	}

	pwr, err := pw.Run(&pw.RunOptions{SkipInstallBrowsers: true})
	if err != nil {
		sc.Close()
		return nil, fmt.Errorf("camoufox: start playwright driver: %w (install playwright-go's driver: see docs/CAMOUFOX.md)", err)
	}

	loopback := "<loopback>"
	br, err := pwr.Firefox.Connect(sc.WS, pw.BrowserTypeConnectOptions{ExposeNetwork: &loopback})
	if err != nil {
		_ = pwr.Stop()
		sc.Close()
		return nil, fmt.Errorf("camoufox: connect %s: %w", sc.WS, err)
	}

	bctx, err := br.NewContext()
	if err != nil {
		_ = br.Close()
		_ = pwr.Stop()
		sc.Close()
		return nil, fmt.Errorf("camoufox: new context: %w", err)
	}

	b := &cfBrowser{
		pw: pwr, br: br, bctx: bctx, sidecar: sc, opts: o,
		caps: browser.Capabilities{
			Name:               "camoufox",
			Version:            versionLabel(sc),
			SupportsUAOverride: false, // Camoufox owns the fingerprint; never override Emulation
			Display:            sc.Display,
		},
	}
	return b, nil
}

func versionLabel(sc *Sidecar) string {
	if sc.CamoufoxVersion != "" {
		return "camoufox " + sc.CamoufoxVersion + " (playwright " + sc.PlaywrightVersion + ")"
	}
	return "camoufox (playwright " + sc.PlaywrightVersion + ")"
}

// NewPage opens a tab in the account's context.
func (b *cfBrowser) NewPage(ctx context.Context) (browser.Page, error) {
	pg, err := b.bctx.NewPage()
	if err != nil {
		return nil, fmt.Errorf("camoufox: new page: %w", err)
	}
	if b.ua == "" {
		if v, err := pg.Evaluate("() => navigator.userAgent"); err == nil {
			if s, ok := v.(string); ok {
				b.ua = s
			}
		}
	}
	return &cfPage{pg: pg, bctx: b.bctx, b: b}, nil
}

// UserAgent returns Camoufox's own user agent (read once from the browser, never
// overridden).
func (b *cfBrowser) UserAgent() string { return b.ua }

// Capabilities reports the Camoufox backend's capabilities.
func (b *cfBrowser) Capabilities() browser.Capabilities { return b.caps }

// Close tears down the context, the connection, the driver and the sidecar
// (process-group kill of Xvfb + Firefox).
func (b *cfBrowser) Close() error {
	var first error
	if b.bctx != nil {
		if err := b.bctx.Close(); err != nil && first == nil {
			first = err
		}
	}
	if b.br != nil {
		if err := b.br.Close(); err != nil && first == nil {
			first = err
		}
	}
	if b.pw != nil {
		if err := b.pw.Stop(); err != nil && first == nil {
			first = err
		}
	}
	if b.sidecar != nil {
		b.sidecar.Close()
	}
	return first
}

var _ browser.Browser = (*cfBrowser)(nil)
