// Package camoufox is the Camoufox (anti-detect Firefox over Playwright)
// browser backend. It runs Firefox in a virtual display (Xvfb) via a Python
// sidecar (Camoufox launch_server), drives it with playwright-go, keeps one
// server per account for a stable fingerprint, and exposes a token-guarded
// human-handoff view onto the virtual display.
//
// This file holds the handoff view proxy, which has no browser dependency and
// is the enforcement point for the one-time handoff token.
package camoufox

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// ViewProxy is a reverse proxy, bound to loopback, in front of the account's
// noVNC/websockify endpoint for the virtual display. It requires a valid
// one-time handoff token on EVERY request — including the WebSocket upgrade the
// VNC client opens — so the view URL cannot be used by anyone without the token
// the handoff minted. This is the enforcement that makes HumanHandoff.ValidToken
// actually gate access (previously the token was minted but never checked).
type ViewProxy struct {
	// Valid reports whether a token is currently valid (wire to
	// captcha.HumanHandoff.ValidToken).
	Valid func(token string) bool
	// Upstream is the local noVNC/websockify base URL (e.g. http://127.0.0.1:PORT).
	Upstream *url.URL
	// proxy is the underlying reverse proxy to Upstream.
	proxy *httputil.ReverseProxy
}

// NewViewProxy builds a ViewProxy for upstream with the given token validator.
func NewViewProxy(upstream *url.URL, valid func(token string) bool) *ViewProxy {
	return &ViewProxy{
		Valid:    valid,
		Upstream: upstream,
		proxy:    httputil.NewSingleHostReverseProxy(upstream),
	}
}

// handoffCookie is the cookie name used to carry the token to sub-resource
// requests that cannot include the query parameter.
const handoffCookie = "handoff_token"

// tokenFromRequest reads the handoff token from the query string, an
// X-Handoff-Token header, the Sec-WebSocket-Protocol header (so a browser
// WebSocket can carry it without a custom header), or the handoff cookie (set on
// the first authenticated request so noVNC's relative-URL sub-resources — CSS,
// JS, images — which carry no query token still authenticate).
func tokenFromRequest(r *http.Request) string {
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	if t := strings.TrimSpace(r.Header.Get("X-Handoff-Token")); t != "" {
		return t
	}
	// Browsers can only set the WebSocket subprotocol, so accept a
	// "handoff.<token>" subprotocol on the upgrade request.
	for _, proto := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		proto = strings.TrimSpace(proto)
		if rest, ok := strings.CutPrefix(proto, "handoff."); ok && rest != "" {
			return rest
		}
	}
	if c, err := r.Cookie(handoffCookie); err == nil && strings.TrimSpace(c.Value) != "" {
		return c.Value
	}
	return ""
}

// ServeHTTP enforces the token on every request (plain HTTP and WebSocket
// upgrade alike) before proxying to the upstream noVNC endpoint.
func (v *ViewProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := tokenFromRequest(r)
	if v.Valid == nil || tok == "" || !v.Valid(tok) {
		// Do not reveal whether the upstream exists; a flat 401 for every
		// unauthenticated request, upgrade requests included.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Set a cookie carrying the (already-validated) token so noVNC's relative-URL
	// sub-resources — which cannot include ?token= — authenticate on this same
	// loopback, token-gated origin. HttpOnly + SameSite=Strict; it lives only as
	// long as the handoff token stays valid.
	http.SetCookie(w, &http.Cookie{
		Name:     handoffCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	v.proxy.ServeHTTP(w, r)
}
