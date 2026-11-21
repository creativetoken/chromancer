package camoufox

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	pw "github.com/mxschmitt/playwright-go"

	"github.com/creativetoken/chromancer/internal/browser"
)

func init() { browser.Register("camoufox", Launch) }

// defaultIdleTimeout shuts an inactive account sidecar down to free the Xvfb/
// Firefox resources; the next NewPage relaunches it. Override with
// AGENTBROWSER_CAMOUFOX_IDLE (Go duration; "0" disables).
const defaultIdleTimeout = 5 * time.Minute

// cfBrowser is one Camoufox (Firefox) instance for one account: a Supervisor
// owning the Python sidecar's Playwright server (restart-on-crash + idle
// shutdown), a playwright-go connection to it, and one browser context whose
// cookies/storage are restored per run (no persistent profile).
type cfBrowser struct {
	pw   *pw.Playwright
	sup  *Supervisor
	caps browser.Capabilities
	opts browser.Options

	mu   sync.Mutex
	br   pw.Browser
	bctx pw.BrowserContext
	ua   string
}

func idleTimeout() time.Duration {
	if v := os.Getenv("AGENTBROWSER_CAMOUFOX_IDLE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return defaultIdleTimeout
}

// Launch is the registered "camoufox" backend Factory. It starts the Playwright
// driver, then a Supervisor that keeps one sidecar alive for the account; on each
// (re)launch it connects playwright-go to the new ws endpoint and opens a fresh
// context. No UA/client-hint override is applied — Camoufox keeps its own
// consistent fingerprint (Capabilities.SupportsUAOverride = false).
func Launch(ctx context.Context, o browser.Options) (browser.Browser, error) {
	pwr, err := pw.Run(&pw.RunOptions{SkipInstallBrowsers: true})
	if err != nil {
		return nil, fmt.Errorf("camoufox: start playwright driver: %w (install playwright-go's driver: see docs/CAMOUFOX.md)", err)
	}

	fpPath := ""
	region := regionFromProxy(o.ProxyURL)
	if o.AccountDir != "" {
		fpPath = fingerprintPath(o.AccountDir)
		// If the saved fingerprint was generated for a different proxy region,
		// drop it so the sidecar regenerates one consistent with the new exit IP
		// (keeping a US timezone/locale against an EU IP is a worse tell).
		if _, regen, _ := ResolveFingerprint(o.AccountDir, region, warnLogger(o.Logger)); regen {
			_ = os.Remove(fpPath)
		}
	}

	b := &cfBrowser{pw: pwr, opts: o}
	b.sup = &Supervisor{
		Launch: func(lctx context.Context) (runner, error) {
			return StartSidecar(lctx, SidecarOptions{
				ProxyURL:        o.ProxyURL,
				GeoIP:           o.ProxyURL != "",
				FingerprintPath: fpPath,
				Region:          region,
				Logger:          o.Logger,
			})
		},
		OnStart:     b.connect,
		IdleTimeout: idleTimeout(),
		MaxRestarts: 2,
		Logger:      o.Logger,
	}
	if err := b.sup.Start(ctx); err != nil {
		_ = pwr.Stop()
		return nil, err
	}

	sc, _ := b.sup.Current().(*Sidecar)
	b.caps = browser.Capabilities{
		Name:               "camoufox",
		Version:            versionLabel(sc),
		SupportsUAOverride: false, // Camoufox owns the fingerprint; never override Emulation
		Display:            displayOf(sc),
	}
	return b, nil
}

// connect is the Supervisor OnStart hook: (re)connect playwright to the sidecar
// and open a fresh context, replacing any previous (dead) connection. Runs on the
// first launch and on every crash-restart.
func (b *cfBrowser) connect(r runner) error {
	loopback := "<loopback>"
	br, err := b.pw.Firefox.Connect(r.Endpoint(), pw.BrowserTypeConnectOptions{ExposeNetwork: &loopback})
	if err != nil {
		return fmt.Errorf("camoufox: connect %s: %w", r.Endpoint(), err)
	}
	bctx, err := br.NewContext()
	if err != nil {
		_ = br.Close()
		return fmt.Errorf("camoufox: new context: %w", err)
	}
	b.mu.Lock()
	oldBr, oldCtx := b.br, b.bctx
	b.br, b.bctx = br, bctx
	b.mu.Unlock()
	if oldCtx != nil {
		_ = oldCtx.Close()
	}
	if oldBr != nil {
		_ = oldBr.Close()
	}
	return nil
}

func versionLabel(sc *Sidecar) string {
	if sc == nil {
		return "camoufox"
	}
	if sc.CamoufoxVersion != "" {
		return "camoufox " + sc.CamoufoxVersion + " (playwright " + sc.PlaywrightVersion + ")"
	}
	return "camoufox (playwright " + sc.PlaywrightVersion + ")"
}

func displayOf(sc *Sidecar) string {
	if sc == nil {
		return ""
	}
	return sc.Display
}

// warnLogger adapts an Options logger to the warn(format, args) shape.
func warnLogger(l func(format string, args ...any)) func(string, ...any) {
	if l == nil {
		return nil
	}
	return func(format string, args ...any) { l(format, args...) }
}

// NewPage opens a tab in the account's context, relaunching the sidecar first if
// it was idle-shut.
func (b *cfBrowser) NewPage(ctx context.Context) (browser.Page, error) {
	if _, err := b.sup.Ensure(); err != nil {
		return nil, err
	}
	b.sup.Touch()
	b.mu.Lock()
	bctx := b.bctx
	b.mu.Unlock()
	if bctx == nil {
		return nil, fmt.Errorf("camoufox: no browser context")
	}
	pg, err := bctx.NewPage()
	if err != nil {
		return nil, fmt.Errorf("camoufox: new page: %w", err)
	}
	b.mu.Lock()
	if b.ua == "" {
		if v, err := pg.Evaluate("() => navigator.userAgent"); err == nil {
			if s, ok := v.(string); ok {
				b.ua = s
			}
		}
	}
	b.mu.Unlock()
	return &cfPage{pg: pg, bctx: bctx, b: b}, nil
}

// UserAgent returns Camoufox's own user agent (read once from the browser, never
// overridden).
func (b *cfBrowser) UserAgent() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ua
}

// Capabilities reports the Camoufox backend's capabilities.
func (b *cfBrowser) Capabilities() browser.Capabilities { return b.caps }

// Close tears down the supervisor (which process-group-kills the sidecar), the
// context, the connection and the driver.
func (b *cfBrowser) Close() error {
	var first error
	if b.sup != nil {
		b.sup.Stop()
	}
	b.mu.Lock()
	bctx, br := b.bctx, b.br
	b.bctx, b.br = nil, nil
	b.mu.Unlock()
	if bctx != nil {
		if err := bctx.Close(); err != nil && first == nil {
			first = err
		}
	}
	if br != nil {
		if err := br.Close(); err != nil && first == nil {
			first = err
		}
	}
	if b.pw != nil {
		if err := b.pw.Stop(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

var _ browser.Browser = (*cfBrowser)(nil)
