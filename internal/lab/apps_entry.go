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

// pwlessSignupApp is a PASSWORDLESS signup: the form takes only an email, a code
// is emailed, and verifying it creates the account and signs in — no password
// at any point. Exercises the signup handler on a form with no password field.
type pwlessSignupApp struct {
	sess  Sessions
	inbox *Inbox
	mu    sync.Mutex
	codes map[string]string
}

func (a *pwlessSignupApp) Name() string { return "pwless-signup" }
func (a *pwlessSignupApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.codes = map[string]string{}
	a.mu.Unlock()
}
func (a *pwlessSignupApp) Expect(base string) Expect {
	return Expect{App: "pwless-signup", Flow: "signup", Root: base + "/pwless-signup/",
		LoginURL: base + "/pwless-signup/signup", SignupURL: base + "/pwless-signup/signup",
		LandingURL: base + "/pwless-signup/app", MeURL: base + "/pwless-signup/api/me",
		Username: DefaultUser, Password: "", AuthMechanism: "cookie", CookieName: "pw_sid",
		Replayable: true, DiscoverableBy: "path", Notes: "passwordless signup: email → code → account created + signed in"}
}

func (a *pwlessSignupApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.inbox = s.Inbox
	a.Reset()

	mux.HandleFunc("/pwless-signup/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pwless-signup/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Passwordless", `<nav><a href="/pwless-signup/">Home</a><a href="/pwless-signup/signup">Sign up</a></nav><h1>Passwordless App</h1>`))
	})
	// Email-only signup form (no password field).
	mux.HandleFunc("/pwless-signup/signup", func(w http.ResponseWriter, r *http.Request) {
		form := `<h1>Create your account</h1><p>Sign up with just your email.</p>
<form method="post" action="/pwless-signup/signup">
<label for="email">Email</label><input id="email" name="email" type="email" autocomplete="email">
<button type="submit">Continue</button></form>`
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
			if email == "" {
				w.WriteHeader(http.StatusBadRequest)
				writeHTML(w, Page("Sign up", `<p class="err" role="alert">Email is required.</p>`+form))
				return
			}
			code := "778899"
			a.mu.Lock()
			a.codes[email] = code
			a.mu.Unlock()
			a.inbox.Deliver(Message{To: email, Channel: "email", Subject: "Your signup code", Body: "Your verification code is " + code, Code: code})
			http.Redirect(w, r, "/pwless-signup/verify?e="+email, http.StatusSeeOther)
			return
		}
		writeHTML(w, Page("Sign up", form))
	})
	// Code entry → creates the account and signs in (no password, no separate login).
	mux.HandleFunc("/pwless-signup/verify", func(w http.ResponseWriter, r *http.Request) {
		email := strings.ToLower(r.URL.Query().Get("e"))
		vform := func(e string) string {
			return `<h1>Verify your email</h1><p>We emailed a code to ` + e + `.</p>
<form method="post" action="/pwless-signup/verify"><input type="hidden" name="email" value="` + e + `">
<label for="code">Code</label><input id="code" name="code" type="text" inputmode="numeric">
<button type="submit">Verify</button></form>`
		}
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			email = strings.ToLower(r.FormValue("email"))
			a.mu.Lock()
			want := a.codes[email]
			a.mu.Unlock()
			if want != "" && r.FormValue("code") == want {
				setSessionCookie(w, "pw_sid", a.sess.New(DefaultUser), "/pwless-signup")
				http.Redirect(w, r, "/pwless-signup/app", http.StatusSeeOther)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			writeHTML(w, Page("Verify", `<p class="err" role="alert">That code is incorrect.</p>`+vform(email)))
			return
		}
		writeHTML(w, Page("Verify", vform(email)))
	})
	mux.HandleFunc("/pwless-signup/app", requireCookie(&a.sess, "pw_sid", "/pwless-signup/signup", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("pwless-signup", u, "/pwless-signup/api/me", "/pwless-signup/logout"))
	}))
	mux.HandleFunc("/pwless-signup/api/me", meHandler(&a.sess, "pw_sid"))
	mux.HandleFunc("/pwless-signup/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "pw_sid"))
		clearCookie(w, "pw_sid", "/pwless-signup")
		http.Redirect(w, r, "/pwless-signup/", http.StatusSeeOther)
	})
}
