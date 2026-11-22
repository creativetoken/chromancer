package lab

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"
)

// combinedVerifyApp reproduces a sign-in that keeps the email and password
// inputs visible and reveals a one-time-code field in the SAME form for step-up
// verification (as Bambu Lab's /sign-in does). The first submit with valid
// credentials emails a code (to the lab inbox) and re-renders the same form with
// the code field added and the credentials still filled. The flow must then
// recognise the code step even though a password input is still present, fetch
// the code and submit it — a plain "keep re-submitting the login form" loop gets
// stuck here.
//
// With intl=true the labels are non-English and the page carries NO recognizable
// verification wording, and the code field's name/id/placeholder do not name it
// as a code either, so it can only be detected from structural signals
// (inputmode=numeric + a short maxlength on a field below the credentials).
type combinedVerifyApp struct {
	name     string
	cookie   string
	intl     bool
	guarded  bool // code field rejects key events until a real click un-guards it
	keyguard bool // code field blocks ALL real key events; only a JS value-set + input event enters it
	mu       sync.Mutex
	sess     Sessions
	codes    map[string]string // email -> pending code
	inbox    *Inbox
}

func (a *combinedVerifyApp) Name() string { return a.name }

func (a *combinedVerifyApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.codes = map[string]string{}
	a.mu.Unlock()
}

func (a *combinedVerifyApp) Expect(base string) Expect {
	notes := "combined email+password+code sign-in (code revealed in place); needs code-step detection with credentials still present"
	if a.intl {
		notes = "non-English combined email+password+code form with no code wording; code field detected from inputmode/maxlength alone"
	}
	if a.guarded {
		notes = "combined form whose code field ignores key events until clicked; the code only lands via click + direct value-set (TypeInto's guarded-input fallback)"
	}
	if a.keyguard {
		notes = "combined form whose code field blocks ALL real key events; the code lands only via the backend's verify-value + direct value-set fallback (camoufox TypeInto parity)"
	}
	return Expect{App: a.name, Flow: "form", Root: base + "/" + a.name + "/", LoginURL: base + "/" + a.name + "/login",
		LandingURL: base + "/" + a.name + "/app", MeURL: base + "/" + a.name + "/api/me",
		Username: DefaultUser, Password: DefaultPass, AuthMechanism: "cookie", CookieName: a.cookie, Replayable: true,
		DiscoverableBy: "link", Notes: notes}
}

// form renders the sign-in form. When withCode is false it is the plain
// credentials form (first step); when true the one-time-code field is present
// and the credentials are pre-filled so they submit alongside the code.
func (a *combinedVerifyApp) form(email, password string, withCode bool, errMsg string) string {
	lbl := map[string]string{
		"email": "Email", "password": "Password", "code": "Verification code",
		"submit": "Log In", "confirm": "Confirm",
		"copy": "To keep your account secure, please enter the verification code we emailed you.",
	}
	if a.intl {
		lbl = map[string]string{
			"email": "メールアドレス", "password": "パスワード", "code": "コード",
			"submit": "ログイン", "confirm": "確認", "copy": "", // no verification wording at all
		}
	}
	var b strings.Builder
	action := "/" + a.name + "/login"
	b.WriteString(`<form method="post" action="` + action + `" id="login-form">`)
	if errMsg != "" {
		b.WriteString(`<p class="err" role="alert">` + html.EscapeString(errMsg) + `</p>`)
	}
	fmt.Fprintf(&b, `<label for="email">%s</label><input id="email" name="email" type="text" autocomplete="username" value="%s" required>`,
		html.EscapeString(lbl["email"]), html.EscapeString(email))
	fmt.Fprintf(&b, `<label for="password">%s</label><input id="password" name="password" type="password" autocomplete="current-password" value="%s" required>`,
		html.EscapeString(lbl["password"]), html.EscapeString(password))
	submit := lbl["submit"]
	if withCode {
		submit = lbl["confirm"]
		if lbl["copy"] != "" {
			b.WriteString(`<p>` + html.EscapeString(lbl["copy"]) + `</p>`)
		}
		if a.intl {
			// Structural detection only: name/id/placeholder reveal nothing; the
			// numeric inputmode and 6-char maxlength (below the credentials) carry it.
			fmt.Fprintf(&b, `<label for="f6">%s</label><input id="f6" name="f6" type="text" inputmode="numeric" maxlength="6" autocomplete="off" required>`,
				html.EscapeString(lbl["code"]))
		} else if a.guarded {
			// The code field silently ignores typed key events until a real click
			// un-guards it (readonly removed on mousedown). Reproduces real OTP
			// fields where programmatic focus/typing does not land and only a
			// click + direct value-set enters the code. A user can still type it
			// after clicking; the submitted value is honoured either way.
			fmt.Fprintf(&b, `<label for="code">%s</label><input id="code" name="code" type="text" inputmode="numeric" maxlength="6" placeholder="Verification code" readonly onmousedown="this.removeAttribute('readonly')" required>`,
				html.EscapeString(lbl["code"]))
		} else if a.keyguard {
			// The code field blocks ALL real key events (onkeydown returns false), so
			// a programmatic Type lands nothing — reproducing an OTP field (as on
			// bambulab via camoufox) where only the backend's verify-value +
			// direct-value-set fallback enters the code. A JS value-set + input event
			// (what the fallback does) is still honoured.
			fmt.Fprintf(&b, `<label for="code">%s</label><input id="code" name="code" type="text" inputmode="numeric" maxlength="6" placeholder="Verification code" onkeydown="return false" onpaste="return false" required>`,
				html.EscapeString(lbl["code"]))
		} else {
			fmt.Fprintf(&b, `<label for="code">%s</label><input id="code" name="code" type="text" inputmode="numeric" maxlength="6" placeholder="Verification code" required>`,
				html.EscapeString(lbl["code"]))
		}
	}
	b.WriteString(`<button type="submit">` + html.EscapeString(submit) + `</button></form>`)
	return b.String()
}

// codeParam is the submitted code field name (differs between variants).
func (a *combinedVerifyApp) codeParam() string {
	if a.intl {
		return "f6"
	}
	return "code"
}

func (a *combinedVerifyApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.inbox = s.Inbox
	a.Reset()

	mux.HandleFunc("/"+a.name+"/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+a.name+"/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page(a.name, `<nav><a href="/`+a.name+`/">Home</a><a href="/`+a.name+`/login">Log in</a></nav><h1>`+a.name+`</h1>`))
	})

	mux.HandleFunc("/"+a.name+"/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeHTML(w, Page("Log in", `<h1>Log in</h1>`+a.form("", "", false, "")))
			return
		}
		_ = r.ParseForm()
		email, pass, code := r.FormValue("email"), r.FormValue("password"), r.FormValue(a.codeParam())
		if !checkCreds(email, pass) {
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, Page("Log in", `<h1>Log in</h1>`+a.form(email, "", false, "Invalid email or password.")))
			return
		}
		key := strings.ToLower(strings.TrimSpace(email))
		if code == "" {
			// Step 1: credentials accepted — email a code and reveal the code field.
			c := fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
			a.mu.Lock()
			a.codes[key] = c
			a.mu.Unlock()
			a.inbox.Deliver(Message{To: email, Channel: "email", Subject: "Your verification code", Body: "Your verification code is " + c, Code: c})
			writeHTML(w, Page("Verify", `<h1>Log in</h1>`+a.form(email, pass, true, "")))
			return
		}
		// Step 2: a code was submitted along with the (pre-filled) credentials.
		a.mu.Lock()
		want := a.codes[key]
		a.mu.Unlock()
		if want == "" || code != want {
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, Page("Verify", `<h1>Log in</h1>`+a.form(email, pass, true, "That code is incorrect.")))
			return
		}
		a.mu.Lock()
		delete(a.codes, key)
		a.mu.Unlock()
		setSessionCookie(w, a.cookie, a.sess.New(email), "/"+a.name)
		http.Redirect(w, r, "/"+a.name+"/app", http.StatusSeeOther)
	})

	mux.HandleFunc("/"+a.name+"/app", requireCookie(&a.sess, a.cookie, "/"+a.name+"/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage(a.name, u, "/"+a.name+"/api/me", "/"+a.name+"/logout"))
	}))
	mux.HandleFunc("/"+a.name+"/api/me", meHandler(&a.sess, a.cookie))
	mux.HandleFunc("/"+a.name+"/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, a.cookie))
		clearCookie(w, a.cookie, "/"+a.name)
		http.Redirect(w, r, "/"+a.name+"/", http.StatusSeeOther)
	})
}
