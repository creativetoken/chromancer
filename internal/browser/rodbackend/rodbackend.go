// Package rodbackend is the go-rod/Chromium implementation of the browser
// interfaces. It launches Chrome with a persistent profile per account (and an
// optional upstream proxy), applies a de-headlessed user agent + client hints,
// and implements Browser/Page/Frame/Element/Value. It is the ONLY package that
// imports go-rod; it registers itself as the "chromium" backend in init.
package rodbackend

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"

	"github.com/creativetoken/chromancer/internal/browser"
)

func init() { browser.Register("chromium", Launch) }

// rodBrowser is one launched Chrome bound to one account's persistent profile.
type rodBrowser struct {
	rb     *rod.Browser
	l      *launcher.Launcher
	opts   browser.Options
	ua     string
	uaMeta *proto.EmulationUserAgentMetadata // Sec-CH-UA client hints, headless marker stripped
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

// Launch starts Chrome. If no local binary is found, rod downloads one. It is
// the registered "chromium" backend Factory.
func Launch(ctx context.Context, o browser.Options) (browser.Browser, error) {
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
	b := &rodBrowser{rb: rb, l: l, opts: o}
	// Resolve the effective user agent and matching client hints once, with the
	// "HeadlessChrome" marker stripped so a headless run does not announce itself.
	if p, err := rb.Page(proto.TargetCreateTarget{URL: "about:blank"}); err == nil {
		b.ua, b.uaMeta = resolveUA(p, o.UserAgent)
		_ = b.applyUA(p)
		_ = p.Close()
	}
	return b, nil
}

// uaProbeJS reads the real browser's UA string plus its User-Agent Client Hints
// (navigator.userAgentData), including the high-entropy values, as JSON.
const uaProbeJS = `async () => {
  const out = {ua: navigator.userAgent, data: null};
  try {
    const d = navigator.userAgentData;
    if (d) {
      const he = await d.getHighEntropyValues(["platform","platformVersion","architecture","bitness","model","uaFullVersion","fullVersionList"]);
      out.data = {
        brands: d.brands, mobile: d.mobile,
        platform: he.platform, platformVersion: he.platformVersion,
        architecture: he.architecture, bitness: he.bitness, model: he.model,
        uaFullVersion: he.uaFullVersion, fullVersionList: he.fullVersionList,
      };
    }
  } catch (e) {}
  return JSON.stringify(out);
}`

type uaBrand struct {
	Brand   string `json:"brand"`
	Version string `json:"version"`
}

type uaProbe struct {
	UA   string `json:"ua"`
	Data *struct {
		Brands          []uaBrand `json:"brands"`
		Mobile          bool      `json:"mobile"`
		Platform        string    `json:"platform"`
		PlatformVersion string    `json:"platformVersion"`
		Architecture    string    `json:"architecture"`
		Bitness         string    `json:"bitness"`
		Model           string    `json:"model"`
		UAFullVersion   string    `json:"uaFullVersion"`
		FullVersionList []uaBrand `json:"fullVersionList"`
	} `json:"data"`
}

// deHeadless rewrites the headless marker to the normal Chrome one. Headless
// Chromium reports "HeadlessChrome" in both the UA string and the Sec-CH-UA
// brand list, which is a bot tell; this makes it indistinguishable from a
// normal Google Chrome of the same build.
func deHeadless(s string) string { return strings.ReplaceAll(s, "HeadlessChrome", "Chrome") }

func cleanBrands(in []uaBrand) []*proto.EmulationUserAgentBrandVersion {
	if len(in) == 0 {
		return nil
	}
	out := make([]*proto.EmulationUserAgentBrandVersion, 0, len(in))
	for _, b := range in {
		name := b.Brand
		if strings.Contains(name, "HeadlessChrome") {
			name = "Google Chrome"
		}
		out = append(out, &proto.EmulationUserAgentBrandVersion{Brand: name, Version: b.Version})
	}
	return out
}

// resolveUA returns the effective UA string and client-hint metadata for every
// page. An explicit override wins for the UA string; otherwise the real browser
// UA is used with the headless marker stripped. Client hints are always derived
// from the real browser (headless marker stripped) so the Sec-CH-UA headers and
// navigator.userAgentData stay consistent with the UA and never leak headless.
func resolveUA(p *rod.Page, override string) (string, *proto.EmulationUserAgentMetadata) {
	res, err := p.Eval(uaProbeJS)
	if err != nil {
		return override, nil
	}
	var probe uaProbe
	if err := json.Unmarshal([]byte(res.Value.Str()), &probe); err != nil {
		return override, nil
	}
	ua := override
	if ua == "" {
		ua = deHeadless(probe.UA)
	}
	if probe.Data == nil {
		return ua, nil
	}
	d := probe.Data
	meta := &proto.EmulationUserAgentMetadata{
		Brands:          cleanBrands(d.Brands),
		FullVersionList: cleanBrands(d.FullVersionList),
		FullVersion:     d.UAFullVersion,
		Platform:        d.Platform,
		PlatformVersion: d.PlatformVersion,
		Architecture:    d.Architecture,
		Model:           d.Model,
		Mobile:          d.Mobile,
		Bitness:         d.Bitness,
	}
	return ua, meta
}

// applyUA sets the resolved UA and client hints on a page. A no-op if the UA
// could not be resolved.
func (b *rodBrowser) applyUA(p *rod.Page) error {
	if b.ua == "" {
		return nil
	}
	return p.SetUserAgent(&proto.NetworkSetUserAgentOverride{UserAgent: b.ua, UserAgentMetadata: b.uaMeta})
}

// UserAgent returns the browser's effective user agent string.
func (b *rodBrowser) UserAgent() string { return b.ua }

// Capabilities reports the Chromium backend's capabilities. It applies UA /
// client-hint Emulation overrides and runs headless/headful without a virtual
// display.
func (b *rodBrowser) Capabilities() browser.Capabilities {
	return browser.Capabilities{Name: "chromium", SupportsUAOverride: true}
}

// Close shuts the browser down and releases the profile lock.
func (b *rodBrowser) Close() error {
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
func (b *rodBrowser) NewPage(ctx context.Context) (browser.Page, error) {
	p, err := b.rb.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("browser: new page: %w", err)
	}
	if err := b.applyUA(p); err != nil {
		return nil, fmt.Errorf("browser: set UA: %w", err)
	}
	// Enable the Network domain so events and cookies are available.
	if err := (proto.NetworkEnable{}).Call(p); err != nil {
		return nil, fmt.Errorf("browser: enable network: %w", err)
	}
	return &rodPage{rp: p.Context(ctx), b: b}, nil
}

func (b *rodBrowser) logf(format string, args ...any) {
	if b.opts.Logger != nil {
		b.opts.Logger(format, args...)
	}
}

var _ browser.Browser = (*rodBrowser)(nil)
