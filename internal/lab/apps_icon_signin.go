package lab

import (
	"net/http"
)

// iconSigninApp reproduces a site whose only sign-in entry is an unlabeled
// account icon in the top-right header (as bambulab.com now does): the homepage
// shows no "sign in" text link, and the sign-in link lives inside a dropdown
// that only appears after the account icon is clicked. The agent must recognise
// the icon-only control (perceiver names it "account (icon)" from its
// data-testid, or "icon (unlabeled)" with a position), open the menu, then
// follow the revealed sign-in link — rather than giving up because no sign-in
// text is visible.
type iconSigninApp struct{ sess Sessions }

func (a *iconSigninApp) Name() string { return "icon-signin" }
func (a *iconSigninApp) Reset()       { a.sess.Reset() }

func (a *iconSigninApp) Expect(base string) Expect {
	return Expect{
		App: "icon-signin", Flow: "form",
		Root: base + "/icon-signin/", LoginURL: base + "/icon-signin/",
		LandingURL: base + "/icon-signin/app", MeURL: base + "/icon-signin/api/me",
		Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "icon_sid", Replayable: true,
		DiscoverableBy: "link",
		Notes:          "sign-in is only reachable via an unlabeled account icon (top-right header) that opens a dropdown; no visible sign-in text on the homepage",
	}
}

// header renders a top-right account icon (icon-only, no text, no aria-label;
// identifiable only via data-testid/class) that toggles a dropdown holding the
// sign-in link. The link is display:none until the icon is clicked, so it is
// absent from perception until then.
func (a *iconSigninApp) header() string {
	return `<header style="display:flex;justify-content:space-between;align-items:center;padding:12px 20px;border-bottom:1px solid #ddd">
  <div class="brand" style="font-weight:bold">IconApp</div>
  <div style="position:relative">
    <button class="account-btn" data-testid="account-menu" aria-haspopup="true"
            style="background:none;border:none;cursor:pointer;padding:6px"
            onclick="var m=document.getElementById('acct-menu');m.style.display=(m.style.display==='block'?'none':'block')">
      <svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="#333" stroke-width="2">
        <circle cx="12" cy="8" r="4"></circle><path d="M4 20c0-4 4-6 8-6s8 2 8 6"></path>
      </svg>
    </button>
    <div id="acct-menu" style="display:none;position:absolute;right:0;top:36px;background:#fff;border:1px solid #ccc;min-width:140px">
      <a href="/icon-signin/login" style="display:block;padding:10px 14px">Sign in</a>
      <a href="/icon-signin/register" style="display:block;padding:10px 14px">Register</a>
    </div>
  </div>
</header>`
}

func (a *iconSigninApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.Reset()

	mux.HandleFunc("/icon-signin/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/icon-signin/" {
			http.NotFound(w, r)
			return
		}
		// Homepage: header icon + marketing copy, NO visible sign-in text link.
		writeHTML(w, Page("IconApp", a.header()+`<main style="padding:40px 20px"><h1>Welcome to IconApp</h1><p>Make something today.</p></main>`))
	})

	mux.HandleFunc("/icon-signin/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if checkCreds(r.FormValue("email"), r.FormValue("password")) {
				setSessionCookie(w, "icon_sid", a.sess.New(DefaultUser), "/icon-signin")
				http.Redirect(w, r, "/icon-signin/app", http.StatusSeeOther)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, Page("Sign in", a.header()+`<h1>Sign in</h1>`+LoginForm("/icon-signin/login", "Sign in", "Invalid email or password", nil)))
			return
		}
		writeHTML(w, Page("Sign in", a.header()+`<h1>Sign in</h1>`+LoginForm("/icon-signin/login", "Sign in", "", nil)))
	})

	mux.HandleFunc("/icon-signin/app", requireCookie(&a.sess, "icon_sid", "/icon-signin/", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("icon-signin", u, "/icon-signin/api/me", "/icon-signin/logout"))
	}))
	mux.HandleFunc("/icon-signin/api/me", meHandler(&a.sess, "icon_sid"))
	mux.HandleFunc("/icon-signin/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "icon_sid"))
		clearCookie(w, "icon_sid", "/icon-signin")
		http.Redirect(w, r, "/icon-signin/", http.StatusSeeOther)
	})
}
