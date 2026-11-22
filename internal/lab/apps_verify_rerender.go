package lab

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"
)

// verifyRerenderApp reproduces the stale-OTP-field failure: a step-up code page
// whose code <input> is REPLACED in the DOM (a framework remount) right after the
// perceiver tags it, so the element perceived when enter_verification was chosen
// is detached by the time the fetched code is typed. Typing by the old
// data-ab-handle then hits nothing. The flow must re-resolve the field against a
// fresh perception by stable attributes (perceive.MatchNode) before typing.
//
// The swap is triggered on the SECOND data-ab-handle stamp of the input (each
// perceive stamps it once), so it lands AFTER the field has been detected for
// enter_verification but is still recoverable by a re-perceive right before the
// type — deterministic, with no timing assumptions.
type verifyRerenderApp struct {
	mu    sync.Mutex
	sess  Sessions
	codes map[string]string
	inbox *Inbox
}

func (a *verifyRerenderApp) Name() string { return "verify-rerender" }

func (a *verifyRerenderApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.codes = map[string]string{}
	a.mu.Unlock()
}

func (a *verifyRerenderApp) Expect(base string) Expect {
	return Expect{
		App: "verify-rerender", Flow: "form",
		Root: base + "/verify-rerender/", LoginURL: base + "/verify-rerender/login",
		LandingURL: base + "/verify-rerender/app", MeURL: base + "/verify-rerender/api/me",
		Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "vr_sid", Replayable: true, DiscoverableBy: "link",
		Notes: "step-up code page whose code input is remounted after it is perceived (stale handle); needs stable-attribute re-resolution before typing",
	}
}

func (a *verifyRerenderApp) loginForm(email, errMsg string) string {
	e := ""
	if errMsg != "" {
		e = `<p class="err" role="alert">` + html.EscapeString(errMsg) + `</p>`
	}
	return `<form method="post" action="/verify-rerender/login" id="login-form">` + e +
		`<label for="email">Email</label><input id="email" name="email" type="text" autocomplete="username" value="` + html.EscapeString(email) + `" required>` +
		`<label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required>` +
		`<button type="submit">Log in</button></form>`
}

// verifyForm renders the code page. The code input is wrapped in a script that
// replaces it on the second data-ab-handle stamp (see the type doc).
func (a *verifyRerenderApp) verifyForm(email, errMsg string) string {
	var b strings.Builder
	if errMsg != "" {
		b.WriteString(`<p class="err" role="alert">` + html.EscapeString(errMsg) + `</p>`)
	}
	b.WriteString(`<p>We emailed you a 6-digit verification code. Enter it below to continue.</p>`)
	b.WriteString(`<form method="post" action="/verify-rerender/verify" id="verify-form">`)
	fmt.Fprintf(&b, `<input type="hidden" name="email" value="%s">`, html.EscapeString(email))
	b.WriteString(`<label for="code">Verification code</label>`)
	b.WriteString(`<input id="code" name="code" type="text" inputmode="numeric" maxlength="6" autocomplete="one-time-code" placeholder="Verification code" required>`)
	b.WriteString(`<button type="submit">Confirm</button></form>`)
	// Remount the code input on its second perceive stamp: a fresh element with
	// identical stable attributes (so MatchNode re-resolves it), but no stale
	// data-ab-handle. The label's for="code" re-associates by id.
	b.WriteString(`<script>(function(){
  var swapped=false, stamps=0;
  function build(){
    var i=document.createElement('input');
    i.id='code'; i.name='code'; i.type='text';
    i.setAttribute('inputmode','numeric'); i.maxLength=6;
    i.setAttribute('autocomplete','one-time-code');
    i.placeholder='Verification code'; i.required=true;
    return i;
  }
  var cur=document.getElementById('code');
  var mo=new MutationObserver(function(recs){
    for(var k=0;k<recs.length;k++){
      if(recs[k].attributeName==='data-ab-handle'){
        stamps++;
        if(stamps>=2 && !swapped){
          swapped=true;
          var fresh=build();
          cur.parentNode.replaceChild(fresh, cur);
          mo.disconnect();
          return;
        }
      }
    }
  });
  mo.observe(cur,{attributes:true,attributeFilter:['data-ab-handle']});
})();</script>`)
	return b.String()
}

func (a *verifyRerenderApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.inbox = s.Inbox
	a.Reset()

	mux.HandleFunc("/verify-rerender/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/verify-rerender/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("verify-rerender", `<nav><a href="/verify-rerender/">Home</a><a href="/verify-rerender/login">Log in</a></nav><h1>verify-rerender</h1>`))
	})

	mux.HandleFunc("/verify-rerender/login", func(w http.ResponseWriter, r *http.Request) {
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
		// Credentials accepted: email a code and serve the code page.
		key := strings.ToLower(strings.TrimSpace(email))
		c := fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
		a.mu.Lock()
		a.codes[key] = c
		a.mu.Unlock()
		a.inbox.Deliver(Message{To: email, Channel: "email", Subject: "Your verification code", Body: "Your verification code is " + c, Code: c})
		writeHTML(w, Page("Verify", `<h1>Verify it's you</h1>`+a.verifyForm(email, "")))
	})

	mux.HandleFunc("/verify-rerender/verify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/verify-rerender/login", http.StatusSeeOther)
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
			writeHTML(w, Page("Verify", `<h1>Verify it's you</h1>`+a.verifyForm(email, "That code is incorrect.")))
			return
		}
		a.mu.Lock()
		delete(a.codes, key)
		a.mu.Unlock()
		setSessionCookie(w, "vr_sid", a.sess.New(email), "/verify-rerender")
		http.Redirect(w, r, "/verify-rerender/app", http.StatusSeeOther)
	})

	mux.HandleFunc("/verify-rerender/app", requireCookie(&a.sess, "vr_sid", "/verify-rerender/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("verify-rerender", u, "/verify-rerender/api/me", "/verify-rerender/logout"))
	}))
	mux.HandleFunc("/verify-rerender/api/me", meHandler(&a.sess, "vr_sid"))
	mux.HandleFunc("/verify-rerender/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "vr_sid"))
		clearCookie(w, "vr_sid", "/verify-rerender")
		http.Redirect(w, r, "/verify-rerender/", http.StatusSeeOther)
	})
}
