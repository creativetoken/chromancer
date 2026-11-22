// Package lab is the offline benchmark: a set of mock web apps, each exercising
// one auth flow variant, served by one binary (cmd/authlab) under path
// prefixes. Every app exposes machine-readable ground truth at
// /<app>/__lab/expect and a shared inbox at /__lab/inbox backs the labinbox
// VerificationProvider.
package lab

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Default lab credentials shared by all apps.
const (
	DefaultUser = "alice@example.com"
	DefaultPass = "correct horse"
)

// Expect is an app's machine-readable ground truth for e2e assertions.
type Expect struct {
	App            string `json:"app"`
	Flow           string `json:"flow"` // form | multistep | modal | iframe | spa | hidden | bound | signup | magiclink | otp | totp | errors | captcha | block | sso
	Root           string `json:"root"`
	LoginURL       string `json:"login_url,omitempty"`
	SignupURL      string `json:"signup_url,omitempty"`
	LandingURL     string `json:"landing_url"`
	MeURL          string `json:"me_url"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	AuthMechanism  string `json:"auth_mechanism"` // cookie | bearer
	CookieName     string `json:"cookie_name,omitempty"`
	BearerHeader   string `json:"bearer_header,omitempty"`
	Replayable     bool   `json:"replayable"`
	DiscoverableBy string `json:"discoverable_by"` // link | path | modal | llm | hint
	Notes          string `json:"notes,omitempty"`
}

// Message is a lab inbox message (email or SMS).
type Message struct {
	To      string    `json:"to"`
	Channel string    `json:"channel"` // email | sms
	Subject string    `json:"subject"`
	Body    string    `json:"body"`
	Code    string    `json:"code,omitempty"`
	Link    string    `json:"link,omitempty"`
	At      time.Time `json:"at"`
}

// Inbox stores delivered messages.
type Inbox struct {
	mu   sync.Mutex
	msgs []Message
}

// Deliver appends a message.
func (i *Inbox) Deliver(m Message) {
	i.mu.Lock()
	defer i.mu.Unlock()
	m.At = time.Now().UTC()
	i.msgs = append(i.msgs, m)
}

// For returns messages addressed to `to`, newest last.
func (i *Inbox) For(to string) []Message {
	i.mu.Lock()
	defer i.mu.Unlock()
	var out []Message
	for _, m := range i.msgs {
		if to == "" || strings.EqualFold(m.To, to) {
			out = append(out, m)
		}
	}
	return out
}

// Reset clears the inbox.
func (i *Inbox) Reset() {
	i.mu.Lock()
	i.msgs = nil
	i.mu.Unlock()
}

// Sessions is a per-app session store (token → username).
type Sessions struct {
	mu sync.Mutex
	m  map[string]string
}

// New creates a session for user and returns the token.
func (s *Sessions) New(user string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]string{}
	}
	t := randHex(16)
	s.m[t] = user
	return t
}

// User returns the user for a token.
func (s *Sessions) User(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.m[token]
	return u, ok
}

// Delete removes a token.
func (s *Sessions) Delete(token string) {
	s.mu.Lock()
	delete(s.m, token)
	s.mu.Unlock()
}

// Reset clears all sessions.
func (s *Sessions) Reset() {
	s.mu.Lock()
	s.m = map[string]string{}
	s.mu.Unlock()
}

// App is one mock application.
type App interface {
	Name() string
	Expect(base string) Expect
	Register(mux *http.ServeMux, s *Server)
	Reset()
}

// Server hosts all apps.
type Server struct {
	Inbox *Inbox
	mux   *http.ServeMux
	apps  map[string]App
	order []string
	base  string // external base URL (scheme://host:port) set by SetBase
	mu    sync.RWMutex
}

// New builds a Server with all registered apps.
func New() *Server {
	s := &Server{Inbox: &Inbox{}, mux: http.NewServeMux(), apps: map[string]App{}}
	for _, a := range allApps() {
		s.apps[a.Name()] = a
		s.order = append(s.order, a.Name())
		a.Register(s.mux, s)
	}
	sort.Strings(s.order)
	s.mux.HandleFunc("/__lab/inbox", s.handleInbox)
	s.mux.HandleFunc("/__lab/reset", s.handleReset)
	// Local stub for a reCAPTCHA v3 render= script (keeps the lab offline).
	s.mux.HandleFunc("/__lab/stub/recaptcha/api.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte("/* lab recaptcha stub */"))
	})
	s.mux.HandleFunc("/__lab/apps", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.order)
	})
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		var b strings.Builder
		b.WriteString("<h1>authlab</h1><ul>")
		for _, n := range s.order {
			fmt.Fprintf(&b, `<li><a href="/%s/">%s</a></li>`, n, n)
		}
		b.WriteString("</ul>")
		writeHTML(w, Page("authlab", b.String()))
	})
	return s
}

// SetBase records the external base URL used in Expect and in links that must
// be absolute (magic links).
func (s *Server) SetBase(base string) {
	s.mu.Lock()
	s.base = strings.TrimRight(base, "/")
	s.mu.Unlock()
}

// Base returns the external base URL.
func (s *Server) Base() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.base
}

// Handler returns the root handler.
func (s *Server) Handler() http.Handler { return s.mux }

// Apps lists app names.
func (s *Server) Apps() []string { return append([]string(nil), s.order...) }

// ExpectFor returns the ground truth for app.
func (s *Server) ExpectFor(app string) (Expect, bool) {
	a, ok := s.apps[app]
	if !ok {
		return Expect{}, false
	}
	return a.Expect(s.Base()), true
}

// Reset clears all app state and the inbox.
func (s *Server) Reset() {
	for _, a := range s.apps {
		a.Reset()
	}
	s.Inbox.Reset()
}

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Inbox.For(r.URL.Query().Get("to")))
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	s.Reset()
	writeJSON(w, map[string]string{"status": "reset"})
}

// registerExpect mounts /<app>/__lab/expect.
func registerExpect(mux *http.ServeMux, s *Server, a App) {
	mux.HandleFunc("/"+a.Name()+"/__lab/expect", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.Expect(s.Base()))
	})
}

// ---- helpers ----

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeHTML(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(s))
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Page wraps body in a minimal HTML document.
func Page(title, body string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>` + html.EscapeString(title) + `</title>
<style>body{font-family:system-ui,sans-serif;margin:2rem;max-width:720px}label{display:block;margin:.6rem 0 .2rem}input,button{font:inherit;padding:.4rem .6rem}button{margin-top:.8rem}.err{color:#b00020}.modal{position:fixed;inset:0;background:rgba(0,0,0,.4);display:none;align-items:center;justify-content:center}.modal.open{display:flex}.modal .box{background:#fff;padding:1.5rem;border-radius:8px;min-width:320px}nav a,nav button{margin-right:1rem}</style>
</head><body>` + body + `</body></html>`
}

// LoginForm renders a standard email+password form posting to action.
func LoginForm(action, submitLabel, errMsg string, extraHidden map[string]string) string {
	var b strings.Builder
	b.WriteString(`<form method="post" action="` + html.EscapeString(action) + `" id="login-form">`)
	for k, v := range extraHidden {
		fmt.Fprintf(&b, `<input type="hidden" name="%s" value="%s">`, html.EscapeString(k), html.EscapeString(v))
	}
	if errMsg != "" {
		b.WriteString(`<p class="err" role="alert">` + html.EscapeString(errMsg) + `</p>`)
	}
	b.WriteString(`<label for="email">Email</label><input id="email" name="email" type="email" autocomplete="username" required>`)
	b.WriteString(`<label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required>`)
	b.WriteString(`<button type="submit">` + html.EscapeString(submitLabel) + `</button></form>`)
	return b.String()
}

// AppPage renders the authenticated landing page with a logout link and an
// XHR to the me endpoint (so there is always something to capture post-login).
func AppPage(appName, user, meURL, logoutURL string) string {
	return Page(appName+" – Dashboard", fmt.Sprintf(`<nav><a href="/%s/">Home</a><a href="%s" id="logout">Logout</a></nav>
<h1>Dashboard</h1><p>Welcome, <span id="user">%s</span>.</p><p>Account: <span id="me">…</span></p>
<script>fetch(%q,{credentials:'same-origin'}).then(r=>r.ok?r.json():Promise.reject(r.status)).then(j=>{document.getElementById('me').textContent=j.email}).catch(e=>{document.getElementById('me').textContent='error '+e});</script>`,
		appName, logoutURL, html.EscapeString(user), meURL))
}

// cookieValue reads a cookie by name.
func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

func setSessionCookie(w http.ResponseWriter, name, value, path string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: path, MaxAge: -1})
}

// checkCreds validates the default lab credentials.
func checkCreds(email, pass string) bool {
	return strings.EqualFold(strings.TrimSpace(email), DefaultUser) && pass == DefaultPass
}

// meHandler returns a cookie-session /api/me handler.
func meHandler(sess *Sessions, cookie string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := sess.User(cookieValue(r, cookie))
		if !ok {
			jsonErr(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		writeJSON(w, map[string]any{"email": u, "id": 1, "app": strings.Trim(strings.SplitN(r.URL.Path, "/", 3)[1], "/")})
	}
}

// requireCookie redirects to loginURL if the session cookie is invalid.
func requireCookie(sess *Sessions, cookie, loginURL string, next func(w http.ResponseWriter, r *http.Request, user string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := sess.User(cookieValue(r, cookie))
		if !ok {
			http.Redirect(w, r, loginURL, http.StatusSeeOther)
			return
		}
		next(w, r, u)
	}
}

func allApps() []App {
	apps := []App{
		&simpleApp{},
		&multistepApp{},
		&modalApp{},
		&iframeApp{},
		&spaJWTApp{},
		&hiddenEntryApp{},
		&boundSessionApp{},
		&signupVerifyApp{},
		&magicLinkApp{},
		&otpSMSApp{},
		&totpApp{},
		&formErrorsApp{},
		&ssoOnlyApp{},
		&captchaMockApp{},
		&captchaBlockApp{},
		&captchaRerenderApp{},
		&combinedVerifyApp{name: "combined-verify", cookie: "cv_sid"},
		&combinedVerifyApp{name: "combined-verify-intl", cookie: "cvi_sid", intl: true},
		&combinedVerifyApp{name: "combined-verify-guarded", cookie: "cvg_sid", guarded: true},
		&iconSigninApp{},
		&shadowLoginApp{},
		&canvasSigninApp{},
		&verifyRerenderApp{},
		&verifyShadowApp{},
		&interstitialApp{name: "takeover-trust", cookie: "tkt_sid", variant: "trust"},
		&interstitialApp{name: "takeover-terms", cookie: "tkm_sid", variant: "terms"},
		&interstitialApp{name: "takeover-injection", cookie: "tki_sid", variant: "injection"},
		&resendOTPApp{name: "takeover-resend", cookie: "tkr_sid"},
		&botWallApp{name: "takeover-botwall", cookie: "tkb_sid"},
		&hydrationLoginApp{cookie: "hyd_sid"},
		&popupLoginApp{cookie: "pop_sid"},
		&stuckLoginApp{},
		&crawlApp{},
		&menuSignupApp{},
		&pwlessSignupApp{},
	}
	return append(apps, captchaLabApps()...)
}
