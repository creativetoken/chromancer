package lab

import (
	"fmt"
	"net/http"
)

// shadowLoginApp reproduces a site whose entire login form lives inside a CLOSED
// shadow root. document.querySelectorAll cannot reach the inputs, so the text
// perceiver sees NO interactive nodes at all — exactly the bambulab failure mode
// ("fill_login_form: no visible email or password fields"). The agent can only
// sign in by escalating to VISUAL mode: read the screenshot, click the fields by
// coordinate, and type into the focused element. The fields are solid marker
// colours so the test visual policy can locate them offline.
//
// The host is a custom element (<login-box>) so that, with a closed root, the
// focused element reported to the page is the host (a hyphenated tag) — which
// the Executor's password focus-check treats as "unverified" (closed root) and
// types anyway, rather than refusing.
type shadowLoginApp struct{ sess Sessions }

func (a *shadowLoginApp) Name() string { return "shadow-login" }
func (a *shadowLoginApp) Reset()       { a.sess.Reset() }

func (a *shadowLoginApp) Expect(base string) Expect {
	return Expect{
		App: "shadow-login", Flow: "form",
		Root: base + "/shadow-login/", LoginURL: base + "/shadow-login/",
		LandingURL: base + "/shadow-login/app", MeURL: base + "/shadow-login/api/me",
		Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "sl_sid", Replayable: true,
		DiscoverableBy: "vision",
		Notes:          "login form is inside a CLOSED shadow root; only visual/coordinate mode can operate it",
	}
}

func (a *shadowLoginApp) page() string {
	// Everything interactive is inside the closed root, so the light DOM has no
	// nodes and the perceiver reports NeedsVision (0 nodes).
	return fmt.Sprintf(`<main style="padding:28px">
<login-box></login-box>
<script>
(function(){
  var host = document.querySelector('login-box');
  var root = host.attachShadow({mode:'closed'});
  root.innerHTML =
    '<style>'
    + '.f{display:block;width:260px;height:44px;margin:14px 0;font-size:16px;border:2px solid #333;box-sizing:border-box}'
    + '#email{background:%s} #password{background:%s} #submit{background:%s;color:#fff;cursor:pointer}'
    + 'h2{margin:6px 0 16px} #err{color:#c00;min-height:18px}'
    + '</style>'
    + '<h2>Members sign in</h2>'
    + '<input id="email" class="f" type="text" placeholder="Email">'
    + '<input id="password" class="f" type="password" placeholder="Password">'
    + '<button id="submit" class="f">Sign in</button>'
    + '<p id="err"></p>';
  root.getElementById('submit').addEventListener('click', function(){
    var email = root.getElementById('email').value;
    var password = root.getElementById('password').value;
    fetch('/shadow-login/login', {method:'POST', headers:{'Content-Type':'application/x-www-form-urlencoded'},
      body:'email='+encodeURIComponent(email)+'&password='+encodeURIComponent(password)})
      .then(function(r){ if(r.ok){ location.href='/shadow-login/app'; } else { root.getElementById('err').textContent='Invalid email or password'; } });
  });
})();
</script>
</main>`, rgbCSS(markerEmail), rgbCSS(markerPass), rgbCSS(markerSubmit))
}

func (a *shadowLoginApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.Reset()

	mux.HandleFunc("/shadow-login/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/shadow-login/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Members", a.page()))
	})

	mux.HandleFunc("/shadow-login/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/shadow-login/", http.StatusSeeOther)
			return
		}
		_ = r.ParseForm()
		if checkCreds(r.FormValue("email"), r.FormValue("password")) {
			setSessionCookie(w, "sl_sid", a.sess.New(DefaultUser), "/shadow-login")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false}`))
	})

	mux.HandleFunc("/shadow-login/app", requireCookie(&a.sess, "sl_sid", "/shadow-login/", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("shadow-login", u, "/shadow-login/api/me", "/shadow-login/logout"))
	}))
	mux.HandleFunc("/shadow-login/api/me", meHandler(&a.sess, "sl_sid"))
	mux.HandleFunc("/shadow-login/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "sl_sid"))
		clearCookie(w, "sl_sid", "/shadow-login")
		http.Redirect(w, r, "/shadow-login/", http.StatusSeeOther)
	})
}
