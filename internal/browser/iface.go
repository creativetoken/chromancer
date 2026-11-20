// Package browser is the backend-neutral browser layer. It defines the
// interfaces the rest of the system programs against (Browser, Page, Frame,
// Element, Value) plus the neutral value types (Cookie, SessionState,
// RequestEvent, ResponseEvent, NetworkSink) and a backend registry. No browser
// engine type appears in any exported signature, so an engine can be swapped
// without touching callers.
//
// Concrete engines live in sub-packages (internal/browser/rodbackend for
// go-rod/Chromium, internal/browser/camoufox for Camoufox/Firefox) and register
// themselves via Register in their package init. Callers obtain a Browser with
// Open(ctx, name, Options). go-rod is imported ONLY inside rodbackend.
package browser

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// DefaultStableTimeout bounds WaitStable.
const DefaultStableTimeout = 8 * time.Second

// Options configures a browser launch. Fields a given backend does not use are
// ignored (e.g. Camoufox ignores UserAgent/ChromeBin and keeps its own
// fingerprint; it uses AccountDir for per-account fingerprint persistence).
type Options struct {
	Headless   bool
	ProfileDir string // persistent user-data-dir (one per account); Chromium only
	ProxyURL   string // optional upstream proxy
	UserAgent  string // optional UA override; Chromium only (see Capabilities.SupportsUAOverride)
	ChromeBin  string // optional explicit Chromium binary; Chromium only
	AccountDir string // per-account workspace dir (e.g. for Camoufox fingerprint persistence)
	Logger     func(format string, args ...any)
}

// Capabilities describes what a backend supports, so callers can adapt (and so
// the trace/bundle can record which engine ran).
type Capabilities struct {
	Name               string // "chromium" | "camoufox"
	Version            string // engine/build version, when known
	SupportsUAOverride bool   // Chromium applies UA/client-hint Emulation; Camoufox keeps its own fingerprint
	Display            string // X display backing a virtual-display backend ("" = none), for the handoff view
}

// Value is a neutral JSON result from Eval. Backends wrap their native result
// (rodbackend wraps gson.JSON; camoufox parses JSON). Only these accessors are
// used across the codebase.
type Value interface {
	Str() string
	Bool() bool
	Int() int
	Float() float64
	Nil() bool
	// Unmarshal decodes the value into v (JSON semantics).
	Unmarshal(v any) error
}

// Element is an opaque handle to a DOM element. Callers never call engine
// methods on it — they pass it back to Page methods (Click, TypeInto, …), which
// assert it to their own backend's concrete element type and reject a foreign or
// nil handle. It is a named empty interface because the concrete implementation
// lives in a different package (a backend) than this interface.
type Element interface{}

// Frame is a single frame (the main document or a first-level iframe). Only
// per-frame evaluation is needed by callers (the perceiver walks each frame).
type Frame interface {
	Eval(ctx context.Context, js string, args ...any) (Value, error)
}

// Page is one tab. All methods respect the ctx the page was created with (and
// any ctx passed), so budgets/kill-switch unwind cleanly.
type Page interface {
	Navigate(ctx context.Context, url string) error
	Reload(ctx context.Context) error
	URL() string
	Title() string
	Origin() string
	WaitStable(ctx context.Context, timeout time.Duration) error
	Eval(ctx context.Context, js string, args ...any) (Value, error)
	Frames(ctx context.Context) ([]Frame, error)
	Screenshot(ctx context.Context) ([]byte, error)
	Element(ctx context.Context, frame Frame, selector string, timeout time.Duration) (Element, error)
	WaitElementReady(ctx context.Context, el Element, stableFor time.Duration) error
	Click(ctx context.Context, el Element) error
	ClickAt(ctx context.Context, x, y int) error
	TypeInto(ctx context.Context, el Element, text string) error
	SelectOption(ctx context.Context, el Element, value string) error
	PressEnter(ctx context.Context, el Element) error
	Back(ctx context.Context) error
	Close() error
	Subscribe(ctx context.Context, sink NetworkSink) (cancel func())
	ExportState(ctx context.Context) (SessionState, error)
	ImportState(ctx context.Context, st SessionState) error
}

// Browser is one launched engine instance bound to one account.
type Browser interface {
	NewPage(ctx context.Context) (Page, error)
	UserAgent() string
	Capabilities() Capabilities
	Close() error
}

// Factory launches a backend.
type Factory func(ctx context.Context, o Options) (Browser, error)

var (
	regMu    sync.RWMutex
	registry = map[string]Factory{}
)

// Register makes a backend available to Open under name. Backends call this in
// their package init. A duplicate name panics (programmer error).
func Register(name string, f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("browser: backend already registered: " + name)
	}
	registry[name] = f
}

// Registered returns the registered backend names, sorted.
func Registered() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Open launches the named backend. The backend package must be imported (for
// its side-effect registration) somewhere in the build.
func Open(ctx context.Context, name string, o Options) (Browser, error) {
	if name == "" {
		name = "chromium"
	}
	regMu.RLock()
	f, ok := registry[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("browser: unknown backend %q (registered: %v) — is its package imported?", name, Registered())
	}
	return f(ctx, o)
}
