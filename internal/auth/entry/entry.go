// Package entry resolves the login/signup entrypoint before any flow runs:
// operator hints -> per-target cache -> deterministic discovery from the root
// (sign-in links/buttons, common paths, header controls opening a modal) ->
// cheap LLM fallback.
package entry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/model"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// Entrypoint is where a login/signup form was found and how.
type Entrypoint struct {
	URL     string    `json:"url"`
	How     string    `json:"how"` // hint | cache | link | path | modal | llm
	Variant string    `json:"variant"`
	Reached time.Time `json:"reached"`
}

// Mode mirrors auth.Mode without importing it (avoids an import cycle): 0=login, 1=signup.
type Mode int

// Modes.
const (
	Login  Mode = 0
	Signup Mode = 1
)

func (m Mode) String() string {
	if m == Signup {
		return "signup"
	}
	return "login"
}

// FormCheck reports whether the current page shows a usable auth form and the
// detected variant. The authenticator supplies this (it owns field
// classification) so discovery stays decoupled from flow logic.
type FormCheck func(st perceive.State, mode Mode) (ok bool, variant string)

// Deps are discovery's collaborators.
type Deps struct {
	Page      *browser.Page
	Perceiver perceive.Perceiver
	Router    model.ModelRouter // optional; nil disables the LLM fallback
	Check     FormCheck
	Trace     func(format string, args ...any)
	Timeout   time.Duration
}

var (
	loginLinkRe  = regexp.MustCompile(`(?i)\b(sign ?in|log ?in|login|members|my account|account|sign ?in now)\b`)
	signupLinkRe = regexp.MustCompile(`(?i)\b(sign ?up|register|create (an )?account|get started|join( now)?|try (it )?free)\b`)
)

func commonPaths(mode Mode) []string {
	if mode == Signup {
		return []string{"/signup", "/sign-up", "/register", "/join", "/users/sign_up", "/account/register", "/auth/signup"}
	}
	return []string{"/login", "/signin", "/sign-in", "/auth", "/account/login", "/users/sign_in", "/user/login", "/session/new"}
}

// Resolver performs entrypoint discovery with a per-target cache on disk.
type Resolver struct {
	CacheDir string
}

type cacheFile struct {
	Login  *Entrypoint `json:"login,omitempty"`
	Signup *Entrypoint `json:"signup,omitempty"`
}

func (r *Resolver) cachePath(target string) string {
	sum := sha256.Sum256([]byte(target))
	return filepath.Join(r.CacheDir, "entry-"+hex.EncodeToString(sum[:8])+".json")
}

func (r *Resolver) readCache(target string, mode Mode) *Entrypoint {
	if r.CacheDir == "" {
		return nil
	}
	data, err := os.ReadFile(r.cachePath(target))
	if err != nil {
		return nil
	}
	var cf cacheFile
	if json.Unmarshal(data, &cf) != nil {
		return nil
	}
	if mode == Signup {
		return cf.Signup
	}
	return cf.Login
}

func (r *Resolver) writeCache(target string, mode Mode, e Entrypoint) {
	if r.CacheDir == "" {
		return
	}
	var cf cacheFile
	if data, err := os.ReadFile(r.cachePath(target)); err == nil {
		_ = json.Unmarshal(data, &cf)
	}
	if mode == Signup {
		cf.Signup = &e
	} else {
		cf.Login = &e
	}
	data, _ := json.MarshalIndent(cf, "", "  ")
	_ = os.MkdirAll(r.CacheDir, 0o700)
	_ = os.WriteFile(r.cachePath(target), data, 0o600)
}

// Invalidate drops the cached entrypoint for a target+mode.
func (r *Resolver) Invalidate(target string, mode Mode) {
	if r.CacheDir == "" {
		return
	}
	var cf cacheFile
	if data, err := os.ReadFile(r.cachePath(target)); err == nil {
		_ = json.Unmarshal(data, &cf)
	}
	if mode == Signup {
		cf.Signup = nil
	} else {
		cf.Login = nil
	}
	data, _ := json.MarshalIndent(cf, "", "  ")
	_ = os.WriteFile(r.cachePath(target), data, 0o600)
}

// Resolve navigates the page to a login/signup form and returns where/how it
// was found. target is the app root URL; root is where discovery starts.
func (r *Resolver) Resolve(ctx context.Context, target, root string, mode Mode, hints []string, d Deps) (Entrypoint, error) {
	trace := d.Trace
	if trace == nil {
		trace = func(string, ...any) {}
	}
	// 1. operator hints
	for _, h := range hints {
		if h == "" {
			continue
		}
		if ok, variant := r.tryURL(ctx, h, mode, d); ok {
			e := Entrypoint{URL: d.Page.URL(), How: "hint", Variant: variant, Reached: time.Now().UTC()}
			r.writeCache(target, mode, e)
			trace("entrypoint via hint: %s (%s)", e.URL, variant)
			return e, nil
		}
	}
	// 2. per-target cache
	if c := r.readCache(target, mode); c != nil {
		if ok, variant := r.tryURL(ctx, c.URL, mode, d); ok {
			e := Entrypoint{URL: d.Page.URL(), How: "cache", Variant: variant, Reached: time.Now().UTC()}
			trace("entrypoint via cache: %s", e.URL)
			return e, nil
		}
		r.Invalidate(target, mode)
	}
	// Load the root for discovery.
	if err := d.Page.Navigate(ctx, root); err != nil {
		return Entrypoint{}, fmt.Errorf("entry: navigate root: %w", err)
	}
	st, _, err := d.Perceiver.Perceive(ctx, d.Page)
	if err != nil {
		return Entrypoint{}, fmt.Errorf("entry: perceive root: %w", err)
	}
	// The root itself might already be the form.
	if ok, variant := d.Check(st, perceive2Mode(mode)); ok {
		e := Entrypoint{URL: d.Page.URL(), How: "link", Variant: variant, Reached: time.Now().UTC()}
		r.writeCache(target, mode, e)
		return e, nil
	}
	// 3a. links/buttons whose text matches sign-in/up.
	if e, ok := r.tryLinks(ctx, st, mode, d); ok {
		r.writeCache(target, mode, e)
		trace("entrypoint via link/modal: %s (%s)", e.URL, e.How)
		return e, nil
	}
	// 3b. common paths.
	for _, p := range commonPaths(mode) {
		u := strings.TrimRight(root, "/") + p
		if ok, variant := r.tryURL(ctx, u, mode, d); ok {
			e := Entrypoint{URL: d.Page.URL(), How: "path", Variant: variant, Reached: time.Now().UTC()}
			r.writeCache(target, mode, e)
			trace("entrypoint via path: %s", e.URL)
			return e, nil
		}
	}
	// 4. LLM fallback: re-load root, ask which control starts login/signup.
	if d.Router != nil {
		if err := d.Page.Navigate(ctx, root); err == nil {
			if st2, _, perr := d.Perceiver.Perceive(ctx, d.Page); perr == nil {
				if e, ok := r.tryLLM(ctx, st2, mode, d); ok {
					r.writeCache(target, mode, e)
					trace("entrypoint via llm: %s", e.URL)
					return e, nil
				}
			}
		}
	}
	return Entrypoint{}, fmt.Errorf("entry: could not locate %s form from %s", mode, root)
}

func perceive2Mode(m Mode) Mode { return m }

// tryURL navigates to u and reports whether it shows the wanted form.
func (r *Resolver) tryURL(ctx context.Context, u string, mode Mode, d Deps) (bool, string) {
	if err := d.Page.Navigate(ctx, u); err != nil {
		return false, ""
	}
	st, _, err := d.Perceiver.Perceive(ctx, d.Page)
	if err != nil {
		return false, ""
	}
	return d.Check(st, mode)
}

// tryLinks clicks the best-matching sign-in/up control (including ones that
// open a modal) and checks whether a form appears.
func (r *Resolver) tryLinks(ctx context.Context, st perceive.State, mode Mode, d Deps) (Entrypoint, bool) {
	re := loginLinkRe
	if mode == Signup {
		re = signupLinkRe
	}
	rootURL := d.Page.URL()
	type cand struct {
		node perceive.Node
		link bool
	}
	var cands []cand
	for _, n := range st.Nodes {
		if !n.Visible {
			continue
		}
		label := n.Name
		if label == "" {
			label = n.Text
		}
		if !re.MatchString(label) {
			continue
		}
		if n.Tag == "a" && n.Href != "" && !strings.HasPrefix(n.Href, "javascript:") && n.Href != "#" {
			cands = append(cands, cand{n, true})
		} else if n.Tag == "button" || n.Role == "button" || n.Tag == "a" {
			cands = append(cands, cand{n, false})
		}
	}
	// Prefer real links first (cheap nav), then buttons (modal openers).
	for _, pass := range []bool{true, false} {
		for _, c := range cands {
			if c.link != pass {
				continue
			}
			fr, err := perceive.FrameOf(ctx, d.Page, c.node)
			if err != nil {
				continue
			}
			el, err := d.Page.Element(ctx, fr, perceive.Selector(c.node.Handle), d.Timeout)
			if err != nil {
				continue
			}
			if err := d.Page.Click(ctx, el); err != nil {
				continue
			}
			_ = d.Page.WaitStable(ctx, browser.DefaultStableTimeout)
			st2, _, err := d.Perceiver.Perceive(ctx, d.Page)
			if err != nil {
				continue
			}
			if ok, variant := d.Check(st2, mode); ok {
				how := "link"
				if !c.link && d.Page.URL() == rootURL {
					how = "modal"
				}
				return Entrypoint{URL: d.Page.URL(), How: how, Variant: variant, Reached: time.Now().UTC()}, true
			}
			// Reset to root for the next candidate.
			_ = d.Page.Navigate(ctx, rootURL)
			if st3, _, e3 := d.Perceiver.Perceive(ctx, d.Page); e3 == nil {
				st = st3
			}
		}
	}
	return Entrypoint{}, false
}

type llmPick struct {
	Handle int    `json:"handle"`
	Reason string `json:"reason"`
}

// tryLLM asks the classify model which node starts the login/signup flow.
func (r *Resolver) tryLLM(ctx context.Context, st perceive.State, mode Mode, d Deps) (Entrypoint, bool) {
	prov := d.Router.For(model.TaskClassify)
	if prov == nil {
		return Entrypoint{}, false
	}
	prompt := fmt.Sprintf(`You are helping an automation agent find where a web app's %s flow begins.
Below are the interactive elements on the page, each with a handle (#N).
Pick the single element a user would click to START %s. Reply with JSON: {"handle": N, "reason": "..."}.
If none fits, reply {"handle": 0, "reason": "none"}.

%s`, mode, mode, perceive.Compact(st))
	raw, _, err := prov.Structured(ctx, []model.Message{{Role: model.User, Text: prompt}}, json.RawMessage(`{"type":"object","properties":{"handle":{"type":"integer"},"reason":{"type":"string"}},"required":["handle","reason"]}`), model.ChatOpts{})
	if err != nil {
		return Entrypoint{}, false
	}
	var pick llmPick
	if json.Unmarshal(raw, &pick) != nil || pick.Handle == 0 {
		return Entrypoint{}, false
	}
	n, ok := st.Find(pick.Handle)
	if !ok {
		return Entrypoint{}, false
	}
	rootURL := d.Page.URL()
	fr, err := perceive.FrameOf(ctx, d.Page, n)
	if err != nil {
		return Entrypoint{}, false
	}
	el, err := d.Page.Element(ctx, fr, perceive.Selector(n.Handle), d.Timeout)
	if err != nil {
		return Entrypoint{}, false
	}
	if err := d.Page.Click(ctx, el); err != nil {
		return Entrypoint{}, false
	}
	_ = d.Page.WaitStable(ctx, browser.DefaultStableTimeout)
	st2, _, err := d.Perceiver.Perceive(ctx, d.Page)
	if err != nil {
		return Entrypoint{}, false
	}
	if ok, variant := d.Check(st2, mode); ok {
		how := "llm"
		if d.Page.URL() == rootURL {
			how = "modal"
		}
		return Entrypoint{URL: d.Page.URL(), How: how, Variant: variant, Reached: time.Now().UTC()}, true
	}
	return Entrypoint{}, false
}
