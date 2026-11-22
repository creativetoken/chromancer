package lab

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/creativetoken/chromancer/internal/verify"
)

// ---------------------------------------------------------------- signup-verify

type signupVerifyApp struct {
	mu       sync.Mutex
	sess     Sessions
	accounts map[string]string // email -> password
	codes    map[string]string // email -> pending code
	verified map[string]bool
	inbox    *Inbox
}

func (a *signupVerifyApp) Name() string { return "signup-verify" }
func (a *signupVerifyApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.accounts, a.codes, a.verified = map[string]string{}, map[string]string{}, map[string]bool{}
	a.mu.Unlock()
}
func (a *signupVerifyApp) Expect(base string) Expect {
	return Expect{App: "signup-verify", Flow: "signup", Root: base + "/signup-verify/", LoginURL: base + "/signup-verify/login",
		SignupURL: base + "/signup-verify/signup", LandingURL: base + "/signup-verify/app", MeURL: base + "/signup-verify/api/me",
		Username: DefaultUser, Password: DefaultPass, AuthMechanism: "cookie", CookieName: "sv_sid", Replayable: true,
		DiscoverableBy: "path", Notes: "signup → email code (lab inbox) → must log in after verifying"}
}

func (a *signupVerifyApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.inbox = s.Inbox
	a.Reset()
	mux.HandleFunc("/signup-verify/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/signup-verify/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Signup Verify", `<nav><a href="/signup-verify/">Home</a><a href="/signup-verify/login">Log in</a><a href="/signup-verify/signup">Sign up</a></nav><h1>Signup Verify App</h1>`))
	})
	mux.HandleFunc("/signup-verify/signup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			email, pass := strings.TrimSpace(r.FormValue("email")), r.FormValue("password")
			a.mu.Lock()
			_, exists := a.accounts[strings.ToLower(email)]
			if exists {
				a.mu.Unlock()
				w.WriteHeader(http.StatusConflict)
				writeHTML(w, Page("Sign up", `<h1>Sign up</h1><p class="err" role="alert">An account with that email already exists. Try logging in.</p>`+signupForm("/signup-verify/signup")))
				return
			}
			code := fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
			a.accounts[strings.ToLower(email)] = pass
			a.codes[strings.ToLower(email)] = code
			a.mu.Unlock()
			a.inbox.Deliver(Message{To: email, Channel: "email", Subject: "Verify your account", Body: "Your verification code is " + code, Code: code})
			http.Redirect(w, r, "/signup-verify/verify?e="+email, http.StatusSeeOther)
			return
		}
		writeHTML(w, Page("Sign up", `<h1>Sign up</h1>`+signupForm("/signup-verify/signup")))
	})
	mux.HandleFunc("/signup-verify/verify", func(w http.ResponseWriter, r *http.Request) {
		email := strings.ToLower(r.URL.Query().Get("e"))
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			email = strings.ToLower(r.FormValue("email"))
			a.mu.Lock()
			want := a.codes[email]
			if want != "" && r.FormValue("code") == want {
				a.verified[email] = true
				delete(a.codes, email)
				a.mu.Unlock()
				writeHTML(w, Page("Verified", `<h1>Email verified</h1><p>Your email is confirmed. Please <a href="/signup-verify/login">log in</a> to continue.</p>`))
				return
			}
			a.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			writeHTML(w, Page("Verify", `<h1>Verify your email</h1><p class="err" role="alert">That code is incorrect.</p>`+verifyForm("/signup-verify/verify", email)))
			return
		}
		writeHTML(w, Page("Verify", `<h1>Verify your email</h1><p>We emailed a 6-digit verification code to `+html.EscapeString(email)+`. Enter it below.</p>`+verifyForm("/signup-verify/verify", email)))
	})
	mux.HandleFunc("/signup-verify/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
			a.mu.Lock()
			pass, ok := a.accounts[email]
			verified := a.verified[email]
			a.mu.Unlock()
			if ok && verified && pass == r.FormValue("password") {
				setSessionCookie(w, "sv_sid", a.sess.New(email), "/signup-verify")
				http.Redirect(w, r, "/signup-verify/app", http.StatusSeeOther)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/signup-verify/login", "Log in", "Invalid credentials or email not verified", nil)))
			return
		}
		writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/signup-verify/login", "Log in", "", nil)))
	})
	mux.HandleFunc("/signup-verify/app", requireCookie(&a.sess, "sv_sid", "/signup-verify/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("signup-verify", u, "/signup-verify/api/me", "/signup-verify/logout"))
	}))
	mux.HandleFunc("/signup-verify/api/me", meHandler(&a.sess, "sv_sid"))
	mux.HandleFunc("/signup-verify/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "sv_sid"))
		clearCookie(w, "sv_sid", "/signup-verify")
		http.Redirect(w, r, "/signup-verify/", http.StatusSeeOther)
	})
}

func signupForm(action string) string {
	return `<form method="post" action="` + action + `"><label for="email">Email</label><input id="email" name="email" type="email" autocomplete="username" required><label for="name">Full name</label><input id="name" name="name" type="text" autocomplete="name"><label for="password">Password</label><input id="password" name="password" type="password" autocomplete="new-password" required><label for="password2">Confirm password</label><input id="password2" name="password2" type="password" autocomplete="new-password" required><button type="submit">Create account</button></form>`
}

func verifyForm(action, email string) string {
	return `<form method="post" action="` + action + `"><input type="hidden" name="email" value="` + html.EscapeString(email) + `"><label for="code">Verification code</label><input id="code" name="code" type="text" inputmode="numeric" autocomplete="one-time-code" required><button type="submit">Verify</button></form>`
}

// ---------------------------------------------------------------- magic-link

type magicLinkApp struct {
	mu     sync.Mutex
	sess   Sessions
	tokens map[string]string // token -> email
	inbox  *Inbox
	base   func() string
}

func (a *magicLinkApp) Name() string { return "magic-link" }
func (a *magicLinkApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.tokens = map[string]string{}
	a.mu.Unlock()
}
func (a *magicLinkApp) Expect(base string) Expect {
	return Expect{App: "magic-link", Flow: "magiclink", Root: base + "/magic-link/", LoginURL: base + "/magic-link/login",
		LandingURL: base + "/magic-link/app", MeURL: base + "/magic-link/api/me", Username: DefaultUser, Password: "",
		AuthMechanism: "cookie", CookieName: "ml_sid", Replayable: true, DiscoverableBy: "link", Notes: "passwordless; link via lab inbox"}
}

func (a *magicLinkApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.inbox = s.Inbox
	a.base = s.Base
	a.Reset()
	mux.HandleFunc("/magic-link/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/magic-link/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Magic Link", `<nav><a href="/magic-link/">Home</a><a href="/magic-link/login">Sign in</a></nav><h1>Magic Link App</h1>`))
	})
	mux.HandleFunc("/magic-link/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			email := strings.TrimSpace(r.FormValue("email"))
			token := randHex(16)
			a.mu.Lock()
			a.tokens[token] = email
			a.mu.Unlock()
			link := a.base() + "/magic-link/callback?token=" + token
			a.inbox.Deliver(Message{To: email, Channel: "email", Subject: "Your sign-in link", Body: "Click to sign in: " + link, Link: link})
			writeHTML(w, Page("Check your email", `<h1>Check your email</h1><p>We emailed you a magic link to sign in. Click the link in your inbox to continue.</p>`))
			return
		}
		writeHTML(w, Page("Sign in", `<h1>Sign in</h1><p>Enter your email and we'll send you a sign-in link.</p><form method="post" action="/magic-link/login"><label for="email">Email</label><input id="email" name="email" type="email" autocomplete="username" required><button type="submit">Email me a link</button></form>`))
	})
	mux.HandleFunc("/magic-link/callback", func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		a.mu.Lock()
		email, ok := a.tokens[token]
		delete(a.tokens, token)
		a.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, Page("Invalid", `<h1>Invalid or expired link</h1>`))
			return
		}
		setSessionCookie(w, "ml_sid", a.sess.New(email), "/magic-link")
		http.Redirect(w, r, "/magic-link/app", http.StatusSeeOther)
	})
	mux.HandleFunc("/magic-link/app", requireCookie(&a.sess, "ml_sid", "/magic-link/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("magic-link", u, "/magic-link/api/me", "/magic-link/logout"))
	}))
	mux.HandleFunc("/magic-link/api/me", meHandler(&a.sess, "ml_sid"))
	mux.HandleFunc("/magic-link/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "ml_sid"))
		clearCookie(w, "ml_sid", "/magic-link")
		http.Redirect(w, r, "/magic-link/", http.StatusSeeOther)
	})
}

// ---------------------------------------------------------------- otp-sms

type otpSMSApp struct {
	mu    sync.Mutex
	sess  Sessions
	codes map[string]string
	inbox *Inbox
}

func (a *otpSMSApp) Name() string { return "otp-sms" }
func (a *otpSMSApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.codes = map[string]string{}
	a.mu.Unlock()
}
func (a *otpSMSApp) Expect(base string) Expect {
	return Expect{App: "otp-sms", Flow: "otp", Root: base + "/otp-sms/", LoginURL: base + "/otp-sms/login",
		LandingURL: base + "/otp-sms/app", MeURL: base + "/otp-sms/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "sms_sid", Replayable: true, DiscoverableBy: "link", Notes: "password then SMS OTP via lab inbox"}
}

// PhoneFor is the lab SMS address for the default user.
const PhoneFor = "+15555550100"

func (a *otpSMSApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.inbox = s.Inbox
	a.Reset()
	mux.HandleFunc("/otp-sms/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/otp-sms/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("OTP SMS", `<nav><a href="/otp-sms/">Home</a><a href="/otp-sms/login">Log in</a></nav><h1>OTP SMS App</h1>`))
	})
	mux.HandleFunc("/otp-sms/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if !checkCreds(r.FormValue("email"), r.FormValue("password")) {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/otp-sms/login", "Log in", "Invalid email or password", nil)))
				return
			}
			code := fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
			a.mu.Lock()
			a.codes[DefaultUser] = code
			a.mu.Unlock()
			a.inbox.Deliver(Message{To: PhoneFor, Channel: "sms", Body: "Your login code is " + code, Code: code})
			http.Redirect(w, r, "/otp-sms/otp", http.StatusSeeOther)
			return
		}
		writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/otp-sms/login", "Log in", "", nil)))
	})
	mux.HandleFunc("/otp-sms/otp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			a.mu.Lock()
			want := a.codes[DefaultUser]
			a.mu.Unlock()
			if want != "" && r.FormValue("code") == want {
				setSessionCookie(w, "sms_sid", a.sess.New(DefaultUser), "/otp-sms")
				http.Redirect(w, r, "/otp-sms/app", http.StatusSeeOther)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			writeHTML(w, Page("Enter code", `<h1>Enter code</h1><p class="err" role="alert">Incorrect code.</p>`+smsOTPForm()))
			return
		}
		writeHTML(w, Page("Enter code", `<h1>Two-step verification</h1><p>We sent a text message with a 6-digit code to your phone ending in **0100. Enter it below.</p>`+smsOTPForm()))
	})
	mux.HandleFunc("/otp-sms/app", requireCookie(&a.sess, "sms_sid", "/otp-sms/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("otp-sms", u, "/otp-sms/api/me", "/otp-sms/logout"))
	}))
	mux.HandleFunc("/otp-sms/api/me", meHandler(&a.sess, "sms_sid"))
	mux.HandleFunc("/otp-sms/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "sms_sid"))
		clearCookie(w, "sms_sid", "/otp-sms")
		http.Redirect(w, r, "/otp-sms/", http.StatusSeeOther)
	})
}

func smsOTPForm() string {
	return `<form method="post" action="/otp-sms/otp"><label for="code">Code</label><input id="code" name="code" type="text" inputmode="numeric" autocomplete="one-time-code" required><button type="submit">Verify</button></form>`
}

// ---------------------------------------------------------------- totp

// TOTPSeed is the known seed the totp lab app and tests share (base32).
const TOTPSeed = "JBSWY3DPEHPK3PXP"

type totpApp struct {
	sess Sessions
}

func (a *totpApp) Name() string { return "totp" }
func (a *totpApp) Reset()       { a.sess.Reset() }
func (a *totpApp) Expect(base string) Expect {
	return Expect{App: "totp", Flow: "totp", Root: base + "/totp/", LoginURL: base + "/totp/login",
		LandingURL: base + "/totp/app", MeURL: base + "/totp/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "totp_sid", Replayable: true, DiscoverableBy: "link", Notes: "password then TOTP MFA (known seed)"}
}

func (a *totpApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	mux.HandleFunc("/totp/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/totp/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("TOTP", `<nav><a href="/totp/">Home</a><a href="/totp/login">Log in</a></nav><h1>TOTP App</h1>`))
	})
	mux.HandleFunc("/totp/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if !checkCreds(r.FormValue("email"), r.FormValue("password")) {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/totp/login", "Log in", "Invalid email or password", nil)))
				return
			}
			http.Redirect(w, r, "/totp/mfa", http.StatusSeeOther)
			return
		}
		writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/totp/login", "Log in", "", nil)))
	})
	mux.HandleFunc("/totp/mfa", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if validTOTP(r.FormValue("code")) {
				setSessionCookie(w, "totp_sid", a.sess.New(DefaultUser), "/totp")
				http.Redirect(w, r, "/totp/app", http.StatusSeeOther)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			writeHTML(w, Page("MFA", `<h1>Two-factor authentication</h1><p class="err" role="alert">Invalid authentication code.</p>`+totpForm()))
			return
		}
		writeHTML(w, Page("MFA", `<h1>Two-factor authentication</h1><p>Open your authenticator app and enter the 6-digit code.</p>`+totpForm()))
	})
	mux.HandleFunc("/totp/app", requireCookie(&a.sess, "totp_sid", "/totp/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("totp", u, "/totp/api/me", "/totp/logout"))
	}))
	mux.HandleFunc("/totp/api/me", meHandler(&a.sess, "totp_sid"))
	mux.HandleFunc("/totp/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "totp_sid"))
		clearCookie(w, "totp_sid", "/totp")
		http.Redirect(w, r, "/totp/", http.StatusSeeOther)
	})
}

func totpForm() string {
	return `<form method="post" action="/totp/mfa"><label for="code">Authentication code</label><input id="code" name="code" type="text" inputmode="numeric" autocomplete="one-time-code" required><button type="submit">Verify</button></form>`
}

func validTOTP(code string) bool {
	now := time.Now()
	for _, skew := range []time.Duration{0, -30 * time.Second, 30 * time.Second} {
		if c, err := verify.Code(TOTPSeed, now.Add(skew), 6, 30*time.Second); err == nil && c == code {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- form-errors

type formErrorsApp struct {
	mu       sync.Mutex
	sess     Sessions
	attempts map[string]int
}

func (a *formErrorsApp) Name() string { return "form-errors" }
func (a *formErrorsApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.attempts = map[string]int{}
	a.mu.Unlock()
}
func (a *formErrorsApp) Expect(base string) Expect {
	return Expect{App: "form-errors", Flow: "errors", Root: base + "/form-errors/", LoginURL: base + "/form-errors/login",
		LandingURL: base + "/form-errors/app", MeURL: base + "/form-errors/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "fe_sid", Replayable: true, DiscoverableBy: "link", Notes: "rejects the first login submit with a validation error, accepts the second"}
}

func (a *formErrorsApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.Reset()
	mux.HandleFunc("/form-errors/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/form-errors/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Form Errors", `<nav><a href="/form-errors/">Home</a><a href="/form-errors/login">Log in</a></nav><h1>Form Errors App</h1>`))
	})
	mux.HandleFunc("/form-errors/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			// Key the attempt counter on the account (not RemoteAddr): a real
			// browser driven step-by-step may use a fresh keep-alive connection per
			// submit, so a port-keyed counter would never reach the second attempt.
			key := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
			a.mu.Lock()
			a.attempts[key]++
			n := a.attempts[key]
			a.mu.Unlock()
			if !checkCreds(r.FormValue("email"), r.FormValue("password")) {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/form-errors/login", "Log in", "Invalid email or password", nil)))
				return
			}
			if n < 2 {
				w.WriteHeader(http.StatusBadRequest)
				writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/form-errors/login", "Log in", "Something went wrong, please try again", nil)))
				return
			}
			setSessionCookie(w, "fe_sid", a.sess.New(DefaultUser), "/form-errors")
			http.Redirect(w, r, "/form-errors/app", http.StatusSeeOther)
			return
		}
		writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/form-errors/login", "Log in", "", nil)))
	})
	mux.HandleFunc("/form-errors/app", requireCookie(&a.sess, "fe_sid", "/form-errors/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("form-errors", u, "/form-errors/api/me", "/form-errors/logout"))
	}))
	mux.HandleFunc("/form-errors/api/me", meHandler(&a.sess, "fe_sid"))
	mux.HandleFunc("/form-errors/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "fe_sid"))
		clearCookie(w, "fe_sid", "/form-errors")
		http.Redirect(w, r, "/form-errors/", http.StatusSeeOther)
	})
}

// ---------------------------------------------------------------- sso-only

type ssoOnlyApp struct{}

func (a *ssoOnlyApp) Name() string { return "sso-only" }
func (a *ssoOnlyApp) Reset()       {}
func (a *ssoOnlyApp) Expect(base string) Expect {
	return Expect{App: "sso-only", Flow: "sso", Root: base + "/sso-only/", LoginURL: base + "/sso-only/login",
		LandingURL: base + "/sso-only/app", MeURL: base + "/sso-only/api/me", Username: DefaultUser, Password: "",
		AuthMechanism: "cookie", DiscoverableBy: "link", Notes: "only third-party sign-in buttons; auth must fail sso_only"}
}

func (a *ssoOnlyApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	mux.HandleFunc("/sso-only/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sso-only/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("SSO Only", `<nav><a href="/sso-only/">Home</a><a href="/sso-only/login">Sign in</a></nav><h1>SSO Only App</h1>`))
	})
	mux.HandleFunc("/sso-only/login", func(w http.ResponseWriter, r *http.Request) {
		writeHTML(w, Page("Sign in", `<h1>Sign in</h1><p>Sign in to continue.</p>
<a href="/sso-only/oauth/google" role="button">Continue with Google</a>
<a href="/sso-only/oauth/github" role="button">Sign in with GitHub</a>
<a href="/sso-only/oauth/microsoft" role="button">Continue with Microsoft</a>`))
	})
}

var _ = html.EscapeString
