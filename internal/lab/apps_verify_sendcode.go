package lab

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"
)

// verifySendCodeApp reproduces bambulab's verification modal: the code is NOT
// emailed when the step appears — a "Send code" control must be clicked first,
// and that control is a React-style <span> with an addEventListener handler and
// NO onclick attribute / role / href, so the DOM perceiver cannot see it. The
// verification flow must therefore trigger delivery by finding the control by its
// text and clicking it directly (clickSendCodeJS), before fetching the code.
// Without that, the code field stays empty and GetCode times out.
type verifySendCodeApp struct {
	mu    sync.Mutex
	sess  Sessions
	codes map[string]string // email -> code, set only after "Send code" is clicked
	inbox *Inbox
}

func (a *verifySendCodeApp) Name() string { return "verify-sendcode" }

func (a *verifySendCodeApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.codes = map[string]string{}
	a.mu.Unlock()
}

func (a *verifySendCodeApp) Expect(base string) Expect {
	return Expect{
		App: "verify-sendcode", Flow: "form",
		Root: base + "/verify-sendcode/", LoginURL: base + "/verify-sendcode/login",
		LandingURL: base + "/verify-sendcode/app", MeURL: base + "/verify-sendcode/api/me",
		Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "vsc_sid", Replayable: true, DiscoverableBy: "link",
		Notes: "OTP modal whose code is dispatched only after an UNPERCEIVABLE React-style 'Send code' span is clicked; needs the verify flow's send-code trigger",
	}
}

func (a *verifySendCodeApp) loginForm(email, errMsg string) string {
	e := ""
	if errMsg != "" {
		e = `<p class="err" role="alert">` + html.EscapeString(errMsg) + `</p>`
	}
	return `<form method="post" action="/verify-sendcode/login" id="login-form">` + e +
		`<label for="email">Email</label><input id="email" name="email" type="text" autocomplete="username" value="` + html.EscapeString(email) + `" required>` +
		`<label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required>` +
		`<button type="submit">Log in</button></form>`
}

// verifyPage renders the OTP modal. "Send code" is a <span> with an
// addEventListener click handler and no onclick attribute — invisible to the
// perceiver. Clicking it POSTs /send, which emails the code.
func (a *verifySendCodeApp) verifyPage(email string) string {
	return fmt.Sprintf(`<main style="padding:28px">
<h1>Verification Code</h1>
<p>To keep your account secure, please enter the verification code sent to %s</p>
<form method="post" action="/verify-sendcode/verify" id="verify-form">
  <input type="hidden" name="email" value="%s">
  <input id="code" name="code" type="text" inputmode="numeric" maxlength="6" placeholder="Verification code*" autocomplete="one-time-code" required>
  <span id="sendcode" style="cursor:pointer;color:#06c">Send code</span>
  <button type="submit">Confirm</button>
</form>
<script>
(function(){
  // React-style: a listener, NOT an onclick attribute — so the DOM perceiver
  // (which keys on button/a/[role=button]/[onclick]) cannot see this control.
  document.getElementById('sendcode').addEventListener('click', function(){
    fetch('/verify-sendcode/send', {method:'POST', headers:{'Content-Type':'application/x-www-form-urlencoded'},
      body:'email=%s'});
  });
})();
</script>
</main>`, html.EscapeString(email), html.EscapeString(email), html.EscapeString(email))
}

func (a *verifySendCodeApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.inbox = s.Inbox
	a.Reset()

	mux.HandleFunc("/verify-sendcode/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/verify-sendcode/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("verify-sendcode", `<nav><a href="/verify-sendcode/">Home</a><a href="/verify-sendcode/login">Log in</a></nav><h1>verify-sendcode</h1>`))
	})

	mux.HandleFunc("/verify-sendcode/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeHTML(w, Page("Log in", `<h1>Log in</h1>`+a.loginForm("", "")))
			return
		}
		_ = r.ParseForm()
		email, pass := r.FormValue("email"), r.FormValue("password")
		if !checkCreds(email, pass) {
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, Page("Log in", `<h1>Log in</h1>`+a.loginForm(email, "Invalid email or password.")))
			return
		}
		// Credentials accepted — show the modal, but DO NOT send a code yet.
		writeHTML(w, Page("Verification Code", a.verifyPage(email)))
	})

	// Clicking "Send code" lands here: now dispatch the code.
	mux.HandleFunc("/verify-sendcode/send", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		email := r.FormValue("email")
		key := strings.ToLower(strings.TrimSpace(email))
		c := fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
		a.mu.Lock()
		a.codes[key] = c
		a.mu.Unlock()
		a.inbox.Deliver(Message{To: email, Channel: "email", Subject: "Your verification code", Body: "Your verification code is " + c, Code: c})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	mux.HandleFunc("/verify-sendcode/verify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/verify-sendcode/login", http.StatusSeeOther)
			return
		}
		_ = r.ParseForm()
		email, code := r.FormValue("email"), r.FormValue("code")
		key := strings.ToLower(strings.TrimSpace(email))
		a.mu.Lock()
		want := a.codes[key]
		a.mu.Unlock()
		if want == "" || code != want {
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, Page("Verification Code", a.verifyPage(email)))
			return
		}
		a.mu.Lock()
		delete(a.codes, key)
		a.mu.Unlock()
		setSessionCookie(w, "vsc_sid", a.sess.New(email), "/verify-sendcode")
		http.Redirect(w, r, "/verify-sendcode/app", http.StatusSeeOther)
	})

	mux.HandleFunc("/verify-sendcode/app", requireCookie(&a.sess, "vsc_sid", "/verify-sendcode/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("verify-sendcode", u, "/verify-sendcode/api/me", "/verify-sendcode/logout"))
	}))
	mux.HandleFunc("/verify-sendcode/api/me", meHandler(&a.sess, "vsc_sid"))
	mux.HandleFunc("/verify-sendcode/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "vsc_sid"))
		clearCookie(w, "vsc_sid", "/verify-sendcode")
		http.Redirect(w, r, "/verify-sendcode/", http.StatusSeeOther)
	})
}
