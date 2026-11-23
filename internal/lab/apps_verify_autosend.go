package lab

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"
)

// verifyAutoSendApp reproduces the TWO bugs that broke the live bambulab OTP run:
//
//  1. The verification code is AUTO-SENT the moment the login form is submitted
//     (it is in the inbox before the code screen even renders) and the "Send
//     code" control is a no-op in cooldown — it never dispatches a second code.
//     A verifier that reflexively clicks "Send code" and advances its freshness
//     window past the already-delivered code will time out on a code sitting in
//     the inbox. The fix must poll for the auto-sent code first and must not move
//     the window. (The "Send code" control is a React-style <span> with an
//     addEventListener handler and no onclick/role, invisible to the perceiver —
//     so a flow that clicks it does so via the text-based JS fallback.)
//
//  2. The Confirm button sits under a transparent pointer-events overlay, so the
//     normal Playwright actionable click (and a Force click, which still dispatches
//     a real mouse event at the point) cannot reach it — only a JS click dispatched
//     directly on the element does. This reproduces the camoufox "click: context
//     deadline exceeded" Confirm failure and proves the click's fallback chain is
//     actually reachable within the caller's short (~8s) budget.
type verifyAutoSendApp struct {
	mu    sync.Mutex
	sess  Sessions
	codes map[string]string // email -> the auto-sent code (set on login submit)
	inbox *Inbox
}

func (a *verifyAutoSendApp) Name() string { return "verify-autosend" }

func (a *verifyAutoSendApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.codes = map[string]string{}
	a.mu.Unlock()
}

func (a *verifyAutoSendApp) Expect(base string) Expect {
	return Expect{
		App: "verify-autosend", Flow: "form",
		Root: base + "/verify-autosend/", LoginURL: base + "/verify-autosend/login",
		LandingURL: base + "/verify-autosend/app", MeURL: base + "/verify-autosend/api/me",
		Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "vas_sid", Replayable: true, DiscoverableBy: "link",
		Notes: "OTP auto-sent on login submit (Send-code is a no-op cooldown) with a Confirm button under a pointer-events overlay; needs poll-first verification + a JS click fallback",
	}
}

func (a *verifyAutoSendApp) loginForm(email, errMsg string) string {
	e := ""
	if errMsg != "" {
		e = `<p class="err" role="alert">` + html.EscapeString(errMsg) + `</p>`
	}
	return `<form method="post" action="/verify-autosend/login" id="login-form">` + e +
		`<label for="email">Email</label><input id="email" name="email" type="text" autocomplete="username" value="` + html.EscapeString(email) + `" required>` +
		`<label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required>` +
		`<button type="submit">Log in</button></form>`
}

// verifyPage renders the OTP modal. The code was ALREADY auto-sent. "Send code"
// is an unperceivable <span> that only hits the cooldown endpoint. The Confirm
// button is covered by a transparent overlay that intercepts pointer events, so
// only a JS click dispatched on the button itself submits the form.
func (a *verifyAutoSendApp) verifyPage(email string) string {
	return fmt.Sprintf(`<main style="padding:28px">
<h1>Verification Code</h1>
<p>To keep your account secure, please enter the verification code sent to %s</p>
<form method="post" action="/verify-autosend/verify" id="verify-form">
  <input type="hidden" name="email" value="%s">
  <input id="code" name="code" type="text" inputmode="numeric" maxlength="6" placeholder="Verification code*" autocomplete="one-time-code" required>
  <span id="sendcode" style="cursor:pointer;color:#06c">Send code</span>
  <div style="position:relative;display:inline-block">
    <button type="submit" id="confirm">Confirm</button>
    <!-- transparent overlay intercepting pointer events over the Confirm button -->
    <div id="shield" style="position:absolute;inset:0;background:transparent;pointer-events:auto;z-index:9999"></div>
  </div>
</form>
<script>
(function(){
  // React-style listener, NOT an onclick attribute: invisible to the DOM perceiver.
  document.getElementById('sendcode').addEventListener('click', function(){
    fetch('/verify-autosend/send', {method:'POST', headers:{'Content-Type':'application/x-www-form-urlencoded'},
      body:'email=%s'});
  });
})();
</script>
</main>`, html.EscapeString(email), html.EscapeString(email), html.EscapeString(email))
}

func (a *verifyAutoSendApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.inbox = s.Inbox
	a.Reset()

	mux.HandleFunc("/verify-autosend/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/verify-autosend/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("verify-autosend", `<nav><a href="/verify-autosend/">Home</a><a href="/verify-autosend/login">Log in</a></nav><h1>verify-autosend</h1>`))
	})

	mux.HandleFunc("/verify-autosend/login", func(w http.ResponseWriter, r *http.Request) {
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
		// Credentials accepted — AUTO-SEND the code immediately (before the modal
		// renders), the way bambulab does. No "Send code" click is required.
		key := strings.ToLower(strings.TrimSpace(email))
		c := fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
		a.mu.Lock()
		a.codes[key] = c
		a.mu.Unlock()
		a.inbox.Deliver(Message{To: email, Channel: "email", Subject: "Your verification code", Body: "Your verification code is " + c, Code: c})
		writeHTML(w, Page("Verification Code", a.verifyPage(email)))
	})

	// "Send code" endpoint: the code was already auto-sent, so this is a no-op
	// cooldown — it never dispatches another code. A verifier that clicks it and
	// advances its freshness window past the auto-sent code would strand that code.
	mux.HandleFunc("/verify-autosend/send", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"cooldown":true}`))
	})

	mux.HandleFunc("/verify-autosend/verify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/verify-autosend/login", http.StatusSeeOther)
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
		setSessionCookie(w, "vas_sid", a.sess.New(email), "/verify-autosend")
		http.Redirect(w, r, "/verify-autosend/app", http.StatusSeeOther)
	})

	mux.HandleFunc("/verify-autosend/app", requireCookie(&a.sess, "vas_sid", "/verify-autosend/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("verify-autosend", u, "/verify-autosend/api/me", "/verify-autosend/logout"))
	}))
	mux.HandleFunc("/verify-autosend/api/me", meHandler(&a.sess, "vas_sid"))
	mux.HandleFunc("/verify-autosend/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "vas_sid"))
		clearCookie(w, "vas_sid", "/verify-autosend")
		http.Redirect(w, r, "/verify-autosend/", http.StatusSeeOther)
	})
}
