package lab

import (
	"net/http"
)

// popupLoginApp reproduces a login that opens in a NEW WINDOW: the home page has
// an "Open login" button that window.open()s the login form in a popup. The form
// posts, sets the session cookie (shared with the opener on the same origin), and
// closes the popup; the opener then sees itself logged in. Exercises the
// backends' popup/new-tab handling (browser.Page.ExpectPopup).
type popupLoginApp struct {
	cookie string
	sess   Sessions
}

func (a *popupLoginApp) Name() string { return "popup-login" }
func (a *popupLoginApp) Reset()       { a.sess.Reset() }

func (a *popupLoginApp) Expect(base string) Expect {
	return Expect{App: "popup-login", Flow: "form", Root: base + "/popup-login/",
		LoginURL: base + "/popup-login/", LandingURL: base + "/popup-login/app",
		MeURL: base + "/popup-login/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: a.cookie, Replayable: true, DiscoverableBy: "link",
		Notes: "login opens in a popup window; the opener becomes logged in after the popup posts"}
}

func (a *popupLoginApp) homePage() string {
	return Page("Popup login", `<h1>Popup login</h1>
<button type="button" id="open-login">Open login</button>
<div id="status">Not logged in</div>
<script>
  document.getElementById('open-login').addEventListener('click', function () {
    window.open('/popup-login/form', 'login', 'width=480,height=640');
  });
  // Poll for the session the popup establishes, then reflect logged-in state.
  setInterval(async function () {
    try {
      const r = await fetch('/popup-login/api/me', {credentials: 'same-origin'});
      if (r.ok) { document.getElementById('status').innerHTML = '<span id="welcome">Welcome</span>'; }
    } catch (e) {}
  }, 400);
</script>`)
}

func (a *popupLoginApp) formPage(errMsg string) string {
	err := ""
	if errMsg != "" {
		err = `<p class="err" role="alert">` + errMsg + `</p>`
	}
	return Page("Sign in", `<h1>Sign in</h1>`+err+`
<form method="post" action="/popup-login/login" id="login-form">
  <label for="email">Email</label><input id="email" name="email" type="email" autocomplete="username" required>
  <label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required>
  <button type="submit" id="submit">Sign in</button>
</form>`)
}

func (a *popupLoginApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.Reset()

	mux.HandleFunc("/popup-login/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/popup-login/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, a.homePage())
	})
	mux.HandleFunc("/popup-login/form", func(w http.ResponseWriter, r *http.Request) {
		writeHTML(w, a.formPage(""))
	})
	mux.HandleFunc("/popup-login/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeHTML(w, a.formPage(""))
			return
		}
		_ = r.ParseForm()
		if !checkCreds(r.FormValue("email"), r.FormValue("password")) {
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, a.formPage("Invalid email or password."))
			return
		}
		setSessionCookie(w, a.cookie, a.sess.New(r.FormValue("email")), "/popup-login")
		// Signal the opener and close the popup.
		writeHTML(w, Page("ok", `<p id="ok">Signed in</p><script>
  try { if (window.opener) window.opener.location.reload(); } catch (e) {}
  window.close();
</script>`))
	})

	mux.HandleFunc("/popup-login/app", requireCookie(&a.sess, a.cookie, "/popup-login/", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("popup-login", u, "/popup-login/api/me", "/popup-login/logout"))
	}))
	mux.HandleFunc("/popup-login/api/me", meHandler(&a.sess, a.cookie))
	mux.HandleFunc("/popup-login/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, a.cookie))
		clearCookie(w, a.cookie, "/popup-login")
		http.Redirect(w, r, "/popup-login/", http.StatusSeeOther)
	})
}
