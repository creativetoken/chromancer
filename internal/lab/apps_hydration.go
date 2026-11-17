package lab

import (
	"net/http"
)

// hydrationLoginApp reproduces a slow client-rendered (SPA) login page whose
// form is present in the DOM during an early pre-hydration lull but whose submit
// control is only wired up after a delayed re-render. The skeleton submit button
// is a non-functional <button type="button">; after HYDRATE_MS the script swaps
// the slot's contents for a real <button type="submit">, detaching the node the
// authenticator first perceived (exactly the stranded-submit race Uniqlo UK
// exhibits). A naive "perceive once, click immediately" flow strands the submit;
// the authenticator must wait for the control to be actionable and/or re-perceive
// a fresh handle and retry.
type hydrationLoginApp struct {
	cookie string
	sess   Sessions
}

func (a *hydrationLoginApp) Name() string { return "hydration-login" }
func (a *hydrationLoginApp) Reset()       { a.sess.Reset() }

func (a *hydrationLoginApp) Expect(base string) Expect {
	return Expect{App: "hydration-login", Flow: "form", Root: base + "/hydration-login/",
		LoginURL: base + "/hydration-login/login", LandingURL: base + "/hydration-login/app",
		MeURL: base + "/hydration-login/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: a.cookie, Replayable: true, DiscoverableBy: "link",
		Notes: "slow-hydrating SPA: skeleton submit present early, real submit wired only after a delayed re-render"}
}

func (a *hydrationLoginApp) loginPage(errMsg string) string {
	err := ""
	if errMsg != "" {
		err = `<p class="err" role="alert">` + errMsg + `</p>`
	}
	// The skeleton submit is a <button type="button"> (classified as a submit
	// control by its label, but it does nothing). After 600ms the slot is
	// replaced with a real type=submit button, detaching the skeleton node.
	return Page("Log in", `<h1>Log in</h1>`+err+`
<form method="post" action="/hydration-login/login" id="login-form">
  <label for="email">Email</label><input id="email" name="email" type="email" autocomplete="username" required>
  <label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required>
  <div id="submit-slot"><button type="button" id="skeleton-submit">Log In</button></div>
</form>
<script>
  // Simulate slow hydration: the real, working submit button is wired only after
  // a delay, replacing (detaching) the skeleton the page shipped with.
  setTimeout(function () {
    var slot = document.getElementById('submit-slot');
    if (slot) { slot.innerHTML = '<button type="submit" id="real-submit">Log In</button>'; }
  }, 600);
</script>`)
}

func (a *hydrationLoginApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.Reset()

	mux.HandleFunc("/hydration-login/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hydration-login/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("hydration-login", `<nav><a href="/hydration-login/">Home</a><a href="/hydration-login/login">Log in</a></nav><h1>hydration-login</h1>`))
	})

	mux.HandleFunc("/hydration-login/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeHTML(w, a.loginPage(""))
			return
		}
		_ = r.ParseForm()
		if !checkCreds(r.FormValue("email"), r.FormValue("password")) {
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, a.loginPage("Invalid email or password."))
			return
		}
		setSessionCookie(w, a.cookie, a.sess.New(r.FormValue("email")), "/hydration-login")
		http.Redirect(w, r, "/hydration-login/app", http.StatusSeeOther)
	})

	mux.HandleFunc("/hydration-login/app", requireCookie(&a.sess, a.cookie, "/hydration-login/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("hydration-login", u, "/hydration-login/api/me", "/hydration-login/logout"))
	}))
	mux.HandleFunc("/hydration-login/api/me", meHandler(&a.sess, a.cookie))
	mux.HandleFunc("/hydration-login/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, a.cookie))
		clearCookie(w, a.cookie, "/hydration-login")
		http.Redirect(w, r, "/hydration-login/", http.StatusSeeOther)
	})
}
