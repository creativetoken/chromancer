// Package browser wraps go-rod: launching Chrome with a persistent profile per
// account (and an optional upstream proxy), pages, SPA-stable waiting, session
// state export/import and a CDP network subscription for the AuthRecorder.
package browser

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// Options configures a browser launch.
type Options struct {
	Headless   bool
	ProfileDir string // persistent user-data-dir (one per account)
	ProxyURL   string // optional upstream proxy
	UserAgent  string // optional UA override
	ChromeBin  string // optional explicit binary
	Logger     func(format string, args ...any)
}

// Browser is one launched Chrome bound to one account's persistent profile.
type Browser struct {
	rb   *rod.Browser
	l    *launcher.Launcher
	opts Options
	ua   string
}

// FindChrome returns the Chrome/Chromium binary to use: an explicit override,
// then $AGENTBROWSER_CHROME, then common install locations, then rod's lookup.
func FindChrome(override string) (string, bool) {
	if override != "" {
		return override, true
	}
	if env := os.Getenv("AGENTBROWSER_CHROME"); env != "" {
		return env, true
	}
	if runtime.GOOS == "darwin" {
		for _, p := range []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		} {
			if _, err := os.Stat(p); err == nil {
				return p, true
			}
		}
	}
	return launcher.LookPath()
}

// Launch starts Chrome. If no local binary is found, rod downloads one.
func Launch(ctx context.Context, o Options) (*Browser, error) {
	l := launcher.New().
		Headless(o.Headless).
		Set("disable-blink-features", "AutomationControlled").
		Set("no-first-run").
		Set("no-default-browser-check").
		Set("disable-features", "Translate,MediaRouter").
		Set("window-size", "1280,900")
	if o.ProfileDir != "" {
		if err := os.MkdirAll(o.ProfileDir, 0o700); err != nil {
			return nil, fmt.Errorf("browser: profile dir: %w", err)
		}
		l = l.UserDataDir(o.ProfileDir)
	}
	if o.ProxyURL != "" {
		l = l.Proxy(o.ProxyURL)
	}
	if bin, ok := FindChrome(o.ChromeBin); ok {
		l = l.Bin(bin)
	}
	u, err := l.Context(ctx).Launch()
	if err != nil {
		return nil, fmt.Errorf("browser: launch: %w", err)
	}
	rb := rod.New().ControlURL(u).Context(ctx)
	if err := rb.Connect(); err != nil {
		l.Kill()
		return nil, fmt.Errorf("browser: connect: %w", err)
	}
	b := &Browser{rb: rb, l: l, opts: o}
	// Resolve the effective user agent once.
	if p, err := rb.Page(proto.TargetCreateTarget{URL: "about:blank"}); err == nil {
		if o.UserAgent != "" {
			_ = p.SetUserAgent(&proto.NetworkSetUserAgentOverride{UserAgent: o.UserAgent})
		}
		if res, err := p.Eval(`() => navigator.userAgent`); err == nil {
			b.ua = res.Value.Str()
		}
		_ = p.Close()
	}
	return b, nil
}

// UserAgent returns the browser's effective user agent string.
func (b *Browser) UserAgent() string { return b.ua }

// Rod exposes the underlying rod browser for advanced callers.
func (b *Browser) Rod() *rod.Browser { return b.rb }

// Close shuts the browser down and releases the profile lock.
func (b *Browser) Close() error {
	var first error
	if b.rb != nil {
		if err := b.rb.Close(); err != nil {
			first = err
		}
	}
	if b.l != nil {
		b.l.Kill()
		b.l.Cleanup()
	}
	return first
}

// NewPage opens a blank tab bound to ctx.
func (b *Browser) NewPage(ctx context.Context) (*Page, error) {
	p, err := b.rb.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("browser: new page: %w", err)
	}
	if b.opts.UserAgent != "" {
		if err := p.SetUserAgent(&proto.NetworkSetUserAgentOverride{UserAgent: b.opts.UserAgent}); err != nil {
			return nil, fmt.Errorf("browser: set UA: %w", err)
		}
	}
	// Enable the Network domain so events and cookies are available.
	if err := (proto.NetworkEnable{}).Call(p); err != nil {
		return nil, fmt.Errorf("browser: enable network: %w", err)
	}
	return &Page{rp: p.Context(ctx), b: b}, nil
}

func (b *Browser) logf(format string, args ...any) {
	if b.opts.Logger != nil {
		b.opts.Logger(format, args...)
	}
}

// DefaultStableTimeout bounds WaitStable.
const DefaultStableTimeout = 8 * time.Second
