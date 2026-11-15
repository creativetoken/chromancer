package lab

import (
	"net/http"
	"sync"
)

// LabCaptchaToken is the token the lab's mock widget accepts (the operator's
// own test token; the testmode solver and mock plugin return it).
const LabCaptchaToken = "lab-captcha-ok"

// captchaWidget renders the mock widget + hidden response field the injector
// fills, plus a tiny script exposing a success callback.
func captchaWidget(sitekey string) string {
	return `<div class="lab-captcha" data-sitekey="` + sitekey + `" role="group" aria-label="CAPTCHA">[ captcha ]</div>
<input type="hidden" name="captcha-token" data-captcha-token value="">
<script>window.__labCaptchaCallback = function(t){ var el=document.querySelector('input[name="captcha-token"]'); if(el) el.value=t; };</script>`
}

func captchaLoginForm(action, sitekey, errMsg string) string {
	e := ""
	if errMsg != "" {
		e = `<p class="err" role="alert">` + errMsg + `</p>`
	}
	return `<form method="post" action="` + action + `" id="login-form">` + e +
		`<label for="email">Email</label><input id="email" name="email" type="email" autocomplete="username" required>` +
		`<label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required>` +
		captchaWidget(sitekey) +
		`<button type="submit">Log in</button></form>`
}

// ---------------------------------------------------------------- captcha-mock

type captchaMockApp struct{ sess Sessions }

func (a *captchaMockApp) Name() string { return "captcha-mock" }
func (a *captchaMockApp) Reset()       { a.sess.Reset() }
func (a *captchaMockApp) Expect(base string) Expect {
	return Expect{App: "captcha-mock", Flow: "captcha", Root: base + "/captcha-mock/", LoginURL: base + "/captcha-mock/login",
		LandingURL: base + "/captcha-mock/app", MeURL: base + "/captcha-mock/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "cm_sid", Replayable: true, DiscoverableBy: "link", Notes: "login guarded by the mock CAPTCHA widget (sitekey lab-sitekey); server verifies the token"}
}

func (a *captchaMockApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	mux.HandleFunc("/captcha-mock/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/captcha-mock/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Captcha Mock", `<nav><a href="/captcha-mock/">Home</a><a href="/captcha-mock/login">Log in</a></nav><h1>Captcha Mock App</h1>`))
	})
	mux.HandleFunc("/captcha-mock/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if r.FormValue("captcha-token") != LabCaptchaToken {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", `<h1>Log in</h1>`+captchaLoginForm("/captcha-mock/login", "lab-sitekey", "Please complete the CAPTCHA to continue.")))
				return
			}
			if !checkCreds(r.FormValue("email"), r.FormValue("password")) {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", `<h1>Log in</h1>`+captchaLoginForm("/captcha-mock/login", "lab-sitekey", "Invalid email or password.")))
				return
			}
			setSessionCookie(w, "cm_sid", a.sess.New(DefaultUser), "/captcha-mock")
			http.Redirect(w, r, "/captcha-mock/app", http.StatusSeeOther)
			return
		}
		writeHTML(w, Page("Log in", `<h1>Log in</h1>`+captchaLoginForm("/captcha-mock/login", "lab-sitekey", "")))
	})
	mux.HandleFunc("/captcha-mock/app", requireCookie(&a.sess, "cm_sid", "/captcha-mock/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("captcha-mock", u, "/captcha-mock/api/me", "/captcha-mock/logout"))
	}))
	mux.HandleFunc("/captcha-mock/api/me", meHandler(&a.sess, "cm_sid"))
	mux.HandleFunc("/captcha-mock/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "cm_sid"))
		clearCookie(w, "cm_sid", "/captcha-mock")
		http.Redirect(w, r, "/captcha-mock/", http.StatusSeeOther)
	})
}

// ---------------------------------------------------------------- stuck-login

// stuckLoginApp presents a normal login form that never authenticates (it keeps
// re-rendering the same form with no error), simulating an unrecognised/broken
// flow — until a human intervenes via POST /stuck-login/__human-solve.
type stuckLoginApp struct {
	sess        Sessions
	mu          sync.Mutex
	humanSolved bool
}

func (a *stuckLoginApp) Name() string { return "stuck-login" }
func (a *stuckLoginApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.humanSolved = false
	a.mu.Unlock()
}
func (a *stuckLoginApp) Expect(base string) Expect {
	return Expect{App: "stuck-login", Flow: "stuck", Root: base + "/stuck-login/", LoginURL: base + "/stuck-login/login",
		LandingURL: base + "/stuck-login/app", MeURL: base + "/stuck-login/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "sl_sid", Replayable: true, DiscoverableBy: "link",
		Notes: "login never progresses (no error) until a human solves it via POST /stuck-login/__human-solve"}
}

func (a *stuckLoginApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.Reset()
	mux.HandleFunc("/stuck-login/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stuck-login/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Stuck Login", `<nav><a href="/stuck-login/">Home</a><a href="/stuck-login/login">Log in</a></nav><h1>Stuck Login App</h1>`))
	})
	mux.HandleFunc("/stuck-login/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			a.mu.Lock()
			solved := a.humanSolved
			a.mu.Unlock()
			_ = r.ParseForm()
			if solved && checkCreds(r.FormValue("email"), r.FormValue("password")) {
				setSessionCookie(w, "sl_sid", a.sess.New(DefaultUser), "/stuck-login")
				http.Redirect(w, r, "/stuck-login/app", http.StatusSeeOther)
				return
			}
			// No error, just the same form again — the agent cannot make progress.
			writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/stuck-login/login", "Log in", "", nil)))
			return
		}
		writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/stuck-login/login", "Log in", "", nil)))
	})
	mux.HandleFunc("/stuck-login/__human-solve", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.humanSolved = true
		a.mu.Unlock()
		writeJSON(w, map[string]string{"status": "solved"})
	})
	mux.HandleFunc("/stuck-login/app", requireCookie(&a.sess, "sl_sid", "/stuck-login/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("stuck-login", u, "/stuck-login/api/me", "/stuck-login/logout"))
	}))
	mux.HandleFunc("/stuck-login/api/me", meHandler(&a.sess, "sl_sid"))
	mux.HandleFunc("/stuck-login/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "sl_sid"))
		clearCookie(w, "sl_sid", "/stuck-login")
		http.Redirect(w, r, "/stuck-login/", http.StatusSeeOther)
	})
}

// ---------------------------------------------------------------- captcha-block

type captchaBlockApp struct {
	sess        Sessions
	mu          sync.Mutex
	humanSolved bool
}

func (a *captchaBlockApp) Name() string { return "captcha-block" }
func (a *captchaBlockApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.humanSolved = false
	a.mu.Unlock()
}
func (a *captchaBlockApp) Expect(base string) Expect {
	return Expect{App: "captcha-block", Flow: "block", Root: base + "/captcha-block/", LoginURL: base + "/captcha-block/login",
		LandingURL: base + "/captcha-block/app", MeURL: base + "/captcha-block/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "cb_sid", Replayable: true, DiscoverableBy: "link",
		Notes: "always re-challenges (a block page) until a human solves it via POST /captcha-block/__human-solve"}
}

func (a *captchaBlockApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.Reset()
	mux.HandleFunc("/captcha-block/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/captcha-block/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Captcha Block", `<nav><a href="/captcha-block/">Home</a><a href="/captcha-block/login">Log in</a></nav><h1>Captcha Block App</h1>`))
	})
	mux.HandleFunc("/captcha-block/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			a.mu.Lock()
			solved := a.humanSolved
			a.mu.Unlock()
			if !solved {
				// Always a block page, regardless of the token (automation defeated).
				w.WriteHeader(http.StatusForbidden)
				writeHTML(w, Page("Verification required", `<h1>Additional verification required</h1><p class="err" role="alert">We could not verify that you are human. Please complete the challenge.</p>`+captchaLoginForm("/captcha-block/login", "block-sitekey", "")))
				return
			}
			if !checkCreds(r.FormValue("email"), r.FormValue("password")) {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", `<h1>Log in</h1>`+captchaLoginForm("/captcha-block/login", "block-sitekey", "Invalid email or password.")))
				return
			}
			setSessionCookie(w, "cb_sid", a.sess.New(DefaultUser), "/captcha-block")
			http.Redirect(w, r, "/captcha-block/app", http.StatusSeeOther)
			return
		}
		writeHTML(w, Page("Log in", `<h1>Log in</h1>`+captchaLoginForm("/captcha-block/login", "block-sitekey", "")))
	})
	// The "human" solving endpoint: after this, logins are accepted.
	mux.HandleFunc("/captcha-block/__human-solve", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.humanSolved = true
		a.mu.Unlock()
		writeJSON(w, map[string]string{"status": "solved"})
	})
	mux.HandleFunc("/captcha-block/app", requireCookie(&a.sess, "cb_sid", "/captcha-block/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("captcha-block", u, "/captcha-block/api/me", "/captcha-block/logout"))
	}))
	mux.HandleFunc("/captcha-block/api/me", meHandler(&a.sess, "cb_sid"))
	mux.HandleFunc("/captcha-block/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "cb_sid"))
		clearCookie(w, "cb_sid", "/captcha-block")
		http.Redirect(w, r, "/captcha-block/", http.StatusSeeOther)
	})
}
