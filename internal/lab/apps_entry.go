package lab

import (
	"net/http"
	"strings"
	"sync"
)

// menuSignupApp hides its signup entry behind an account menu: the homepage has
// no sign-up link and the signup form lives at a non-guessable path, so neither
// the sign-up-link probe nor the common-path probe can find it. Only the
// iterative LLM entry-finder reaches it: open "Account" → click "Create
// account" → the form. Exercises multi-step entrypoint discovery.
type menuSignupApp struct {
	sess     Sessions
	mu       sync.Mutex
	accounts map[string]string
}

func (a *menuSignupApp) Name() string { return "menu-signup" }
func (a *menuSignupApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.accounts = map[string]string{}
	a.mu.Unlock()
}
func (a *menuSignupApp) Expect(base string) Expect {
	return Expect{App: "menu-signup", Flow: "signup", Root: base + "/menu-signup/",
		LoginURL: base + "/menu-signup/account", SignupURL: base + "/menu-signup/x7k2-register",
		LandingURL: base + "/menu-signup/app", MeURL: base + "/menu-signup/api/me",
		Username: DefaultUser, Password: DefaultPass, AuthMechanism: "cookie", CookieName: "msg_sid",
		Replayable: true, DiscoverableBy: "llm", Notes: "signup entry hidden behind an account menu; needs iterative LLM discovery"}
}

func (a *menuSignupApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.Reset()

	// Homepage: an "Account" menu, NO sign-up link, and no form.
	mux.HandleFunc("/menu-signup/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/menu-signup/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Menu Signup", `<nav><a href="/menu-signup/">Home</a><a href="/menu-signup/about">About</a><a href="/menu-signup/account">Account</a></nav>
<h1>Menu Signup App</h1><p>Welcome to the demo. Use the Account menu to get started.</p>`))
	})
	mux.HandleFunc("/menu-signup/about", func(w http.ResponseWriter, r *http.Request) {
		writeHTML(w, Page("About", `<h1>About</h1><p>Nothing to see.</p><a href="/menu-signup/">Home</a>`))
	})

	// Account menu: reveals "Create account" (and "Sign in").
	mux.HandleFunc("/menu-signup/account", func(w http.ResponseWriter, r *http.Request) {
		writeHTML(w, Page("Account", `<nav><a href="/menu-signup/">Home</a></nav>
<h1>Account</h1><ul>
<li><a href="/menu-signup/x7k2-register">Create account</a></li>
<li><a href="/menu-signup/x7k2-login">Sign in</a></li>
</ul>`))
	})

	// The signup form, at a non-guessable path.
	mux.HandleFunc("/menu-signup/x7k2-register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			email, pass := strings.TrimSpace(r.FormValue("email")), r.FormValue("password")
			a.mu.Lock()
			_, exists := a.accounts[strings.ToLower(email)]
			if exists {
				a.mu.Unlock()
				w.WriteHeader(http.StatusConflict)
				writeHTML(w, Page("Sign up", `<h1>Create account</h1><p class="err" role="alert">An account with that email already exists. Try logging in.</p>`+signupForm("/menu-signup/x7k2-register")))
				return
			}
			a.accounts[strings.ToLower(email)] = pass
			a.mu.Unlock()
			setSessionCookie(w, "msg_sid", a.sess.New(DefaultUser), "/menu-signup")
			http.Redirect(w, r, "/menu-signup/app", http.StatusSeeOther)
			return
		}
		writeHTML(w, Page("Sign up", `<h1>Create account</h1>`+signupForm("/menu-signup/x7k2-register")))
	})

	mux.HandleFunc("/menu-signup/x7k2-login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if checkCreds(r.FormValue("email"), r.FormValue("password")) {
				setSessionCookie(w, "msg_sid", a.sess.New(DefaultUser), "/menu-signup")
				http.Redirect(w, r, "/menu-signup/app", http.StatusSeeOther)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, Page("Sign in", `<h1>Sign in</h1>`+LoginForm("/menu-signup/x7k2-login", "Sign in", "Invalid email or password", nil)))
			return
		}
		writeHTML(w, Page("Sign in", `<h1>Sign in</h1>`+LoginForm("/menu-signup/x7k2-login", "Sign in", "", nil)))
	})

	mux.HandleFunc("/menu-signup/app", requireCookie(&a.sess, "msg_sid", "/menu-signup/account", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("menu-signup", u, "/menu-signup/api/me", "/menu-signup/logout"))
	}))
	mux.HandleFunc("/menu-signup/api/me", meHandler(&a.sess, "msg_sid"))
	mux.HandleFunc("/menu-signup/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "msg_sid"))
		clearCookie(w, "msg_sid", "/menu-signup")
		http.Redirect(w, r, "/menu-signup/", http.StatusSeeOther)
	})
}
