package lab

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"
)

// verifyShadowApp reproduces a step-up code page whose code field is inside a
// CLOSED shadow root: the perceiver cannot see it, so the text verification path
// has no handle to type into. The agent must escalate to VISUAL mode, locate the
// code field in the screenshot, and enter the code by COORDINATE —
// enter_verification(x,y): the callback clicks to focus the (non-perceivable)
// field and types the fetched code into focus (the code is typed by the browser,
// never surfaced to the model).
//
// Login itself is an ordinary, perceivable form (text mode); only the verify step
// is closed-shadow, so the test isolates visual OTP entry. The closed-shadow
// field auto-submits (fetch) once six digits are entered — no JS navigation, so
// it works on camoufox too (as shadow-login does). The field is a solid marker
// colour so the test visual policy can find it offline.
type verifyShadowApp struct {
	mu    sync.Mutex
	sess  Sessions
	codes map[string]string
	inbox *Inbox
}

func (a *verifyShadowApp) Name() string { return "verify-shadow" }

func (a *verifyShadowApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.codes = map[string]string{}
	a.mu.Unlock()
}

func (a *verifyShadowApp) Expect(base string) Expect {
	return Expect{
		App: "verify-shadow", Flow: "form",
		Root: base + "/verify-shadow/", LoginURL: base + "/verify-shadow/login",
		LandingURL: base + "/verify-shadow/app", MeURL: base + "/verify-shadow/api/me",
		Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "vs_sid", Replayable: true, DiscoverableBy: "link",
		Notes: "step-up code field inside a CLOSED shadow root; only visual mode can enter it (enter_verification by coordinate)",
	}
}

func (a *verifyShadowApp) loginForm(email, errMsg string) string {
	e := ""
	if errMsg != "" {
		e = `<p class="err" role="alert">` + html.EscapeString(errMsg) + `</p>`
	}
	return `<form method="post" action="/verify-shadow/login" id="login-form">` + e +
		`<label for="email">Email</label><input id="email" name="email" type="text" autocomplete="username" value="` + html.EscapeString(email) + `" required>` +
		`<label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required>` +
		`<button type="submit">Log in</button></form>`
}

// verifyPage renders the code step: light-DOM copy asking for the code, plus a
// <code-box> custom element whose code input lives in a CLOSED shadow root. The
// input is painted markerCode so the visual policy can locate it; it auto-submits
// via fetch when six digits are entered.
func (a *verifyShadowApp) verifyPage(email string) string {
	return fmt.Sprintf(`<main style="padding:28px">
<h1>Verify it's you</h1>
<p>Enter the verification code we emailed you.</p>
<code-box></code-box>
<script>
(function(){
  var host=document.querySelector('code-box');
  var root=host.attachShadow({mode:'closed'});
  root.innerHTML=
    '<style>#code{display:block;width:260px;height:48px;margin:12px 0;font-size:20px;'
    + 'letter-spacing:6px;text-align:center;border:2px solid #333;box-sizing:border-box;'
    + 'background:%s}#err{color:#c00;min-height:18px}</style>'
    + '<input id="code" type="text" inputmode="numeric" maxlength="6" placeholder="------">'
    + '<p id="err"></p>';
  var inp=root.getElementById('code');
  inp.addEventListener('input',function(){
    if(inp.value.length===6){
      fetch('/verify-shadow/verify',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},
        body:'email=%s&code='+encodeURIComponent(inp.value)})
        .then(function(r){ if(r.ok){ location.href='/verify-shadow/app'; } else { root.getElementById('err').textContent='That code is incorrect.'; inp.value=''; } });
    }
  });
})();
</script>
</main>`, rgbCSS(markerCode), html.EscapeString(email))
}

func (a *verifyShadowApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.inbox = s.Inbox
	a.Reset()

	mux.HandleFunc("/verify-shadow/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/verify-shadow/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("verify-shadow", `<nav><a href="/verify-shadow/">Home</a><a href="/verify-shadow/login">Log in</a></nav><h1>verify-shadow</h1>`))
	})

	mux.HandleFunc("/verify-shadow/login", func(w http.ResponseWriter, r *http.Request) {
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
		key := strings.ToLower(strings.TrimSpace(email))
		c := fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
		a.mu.Lock()
		a.codes[key] = c
		a.mu.Unlock()
		a.inbox.Deliver(Message{To: email, Channel: "email", Subject: "Your verification code", Body: "Your verification code is " + c, Code: c})
		writeHTML(w, Page("Verify", a.verifyPage(email)))
	})

	mux.HandleFunc("/verify-shadow/verify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/verify-shadow/login", http.StatusSeeOther)
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
			_, _ = w.Write([]byte(`{"ok":false}`))
			return
		}
		a.mu.Lock()
		delete(a.codes, key)
		a.mu.Unlock()
		setSessionCookie(w, "vs_sid", a.sess.New(email), "/verify-shadow")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	mux.HandleFunc("/verify-shadow/app", requireCookie(&a.sess, "vs_sid", "/verify-shadow/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("verify-shadow", u, "/verify-shadow/api/me", "/verify-shadow/logout"))
	}))
	mux.HandleFunc("/verify-shadow/api/me", meHandler(&a.sess, "vs_sid"))
	mux.HandleFunc("/verify-shadow/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "vs_sid"))
		clearCookie(w, "vs_sid", "/verify-shadow")
		http.Redirect(w, r, "/verify-shadow/", http.StatusSeeOther)
	})
}
