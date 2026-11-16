package lab

import (
	"net/http"
	"sync"
)

// rerenderLoginForm is a captcha login form whose submit button is REPLACED in
// the DOM once the challenge is solved. The page polls the solve status and, on
// solve, swaps the submit button for a fresh element. This mimics a real site
// (e.g. one fronted by reCAPTCHA) whose form re-renders when the challenge
// callback fires during the human solve — after the CAPTCHA hook's last
// perception. The submit control perceived before the hook is then a detached
// element by the time the flow submits, so a handler that clicked the stale
// handle times out. The flow must re-perceive after the CAPTCHA hook to find
// the fresh control.
func rerenderLoginForm(action, sitekey, errMsg string) string {
	e := ""
	if errMsg != "" {
		e = `<p class="err" role="alert">` + errMsg + `</p>`
	}
	return `<form method="post" action="` + action + `" id="login-form">` + e +
		`<label for="email">Email</label><input id="email" name="email" type="email" autocomplete="username" required>` +
		`<label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required>` +
		captchaWidget(sitekey) +
		`<button type="submit">Log in</button></form>` +
		`<script>(function(){var done=false;
function swap(){if(done)return;done=true;var f=document.getElementById('login-form');var old=f.querySelector('button[type=submit]');if(!old)return;var b=document.createElement('button');b.type='submit';b.textContent='Log in';old.parentNode.replaceChild(b,old);}
function poll(){fetch('/captcha-rerender/__status').then(function(r){return r.json();}).then(function(j){if(j&&j.solved){swap();}else{setTimeout(poll,80);}}).catch(function(){setTimeout(poll,80);});}
poll();})();</script>`
}

// captchaRerenderApp is captcha-block plus a submit button that is swapped out
// in the DOM when the password is entered. Every submit is rejected until a
// human solves it (POST /captcha-rerender/__human-solve); the stale submit
// handle makes this reproduce the "submit: element … context deadline exceeded"
// failure unless the handler re-perceives after the CAPTCHA hook.
type captchaRerenderApp struct {
	sess        Sessions
	mu          sync.Mutex
	humanSolved bool
}

func (a *captchaRerenderApp) Name() string { return "captcha-rerender" }
func (a *captchaRerenderApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.humanSolved = false
	a.mu.Unlock()
}
func (a *captchaRerenderApp) Expect(base string) Expect {
	return Expect{App: "captcha-rerender", Flow: "block", Root: base + "/captcha-rerender/", LoginURL: base + "/captcha-rerender/login",
		LandingURL: base + "/captcha-rerender/app", MeURL: base + "/captcha-rerender/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "cr_sid", Replayable: true, DiscoverableBy: "link",
		Notes: "captcha block whose submit button is replaced on password input (stale handle); needs re-perceive after the captcha hook"}
}

func (a *captchaRerenderApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.Reset()
	mux.HandleFunc("/captcha-rerender/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/captcha-rerender/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Captcha Rerender", `<nav><a href="/captcha-rerender/">Home</a><a href="/captcha-rerender/login">Log in</a></nav><h1>Captcha Rerender App</h1>`))
	})
	mux.HandleFunc("/captcha-rerender/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			a.mu.Lock()
			solved := a.humanSolved
			a.mu.Unlock()
			if !solved {
				w.WriteHeader(http.StatusForbidden)
				writeHTML(w, Page("Verification required", `<h1>Additional verification required</h1><p class="err" role="alert">We could not verify that you are human. Please complete the challenge.</p>`+rerenderLoginForm("/captcha-rerender/login", "block-sitekey", "")))
				return
			}
			if !checkCreds(r.FormValue("email"), r.FormValue("password")) {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", `<h1>Log in</h1>`+rerenderLoginForm("/captcha-rerender/login", "block-sitekey", "Invalid email or password.")))
				return
			}
			setSessionCookie(w, "cr_sid", a.sess.New(DefaultUser), "/captcha-rerender")
			http.Redirect(w, r, "/captcha-rerender/app", http.StatusSeeOther)
			return
		}
		writeHTML(w, Page("Log in", `<h1>Log in</h1>`+rerenderLoginForm("/captcha-rerender/login", "block-sitekey", "")))
	})
	mux.HandleFunc("/captcha-rerender/__human-solve", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.humanSolved = true
		a.mu.Unlock()
		writeJSON(w, map[string]string{"status": "solved"})
	})
	// The page polls this; on solve it swaps the submit button in the DOM.
	mux.HandleFunc("/captcha-rerender/__status", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		solved := a.humanSolved
		a.mu.Unlock()
		writeJSON(w, map[string]bool{"solved": solved})
	})
	mux.HandleFunc("/captcha-rerender/app", requireCookie(&a.sess, "cr_sid", "/captcha-rerender/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("captcha-rerender", u, "/captcha-rerender/api/me", "/captcha-rerender/logout"))
	}))
	mux.HandleFunc("/captcha-rerender/api/me", meHandler(&a.sess, "cr_sid"))
	mux.HandleFunc("/captcha-rerender/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "cr_sid"))
		clearCookie(w, "cr_sid", "/captcha-rerender")
		http.Redirect(w, r, "/captcha-rerender/", http.StatusSeeOther)
	})
}
