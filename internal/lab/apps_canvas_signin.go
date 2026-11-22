package lab

import (
	"fmt"
	"net/http"
)

// canvasSigninApp reproduces a site whose sign-in entry is drawn on a <canvas>
// (no DOM control at all for it). The perceiver sees the canvas element but not
// the button painted on it, and reports NeedsVision (a canvas node). The agent
// must escalate to VISUAL mode, locate the painted "Sign in" button in the
// screenshot, and click it by coordinate. Clicking the painted button navigates
// to a normal (perceivable) login form, at which point the agent drops back to
// the text path and signs in. The button is a solid marker colour so the test
// visual policy can find it offline.
type canvasSigninApp struct{ sess Sessions }

func (a *canvasSigninApp) Name() string { return "canvas-signin" }
func (a *canvasSigninApp) Reset()       { a.sess.Reset() }

func (a *canvasSigninApp) Expect(base string) Expect {
	return Expect{
		App: "canvas-signin", Flow: "form",
		Root: base + "/canvas-signin/", LoginURL: base + "/canvas-signin/",
		LandingURL: base + "/canvas-signin/app", MeURL: base + "/canvas-signin/api/me",
		Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "cs_sid", Replayable: true,
		DiscoverableBy: "vision",
		Notes:          "sign-in control is painted on a <canvas>; visual mode must click it, then a normal form appears",
	}
}

func (a *canvasSigninApp) page() string {
	// A canvas with a green "Sign in" button painted at a fixed rect; clicking
	// within that rect navigates to the real form.
	return fmt.Sprintf(`<main style="padding:28px">
<h1>CanvasApp</h1>
<canvas id="c" width="420" height="220" style="border:1px solid #ccc"></canvas>
<script>
(function(){
  var c = document.getElementById('c');
  var ctx = c.getContext('2d');
  var bx=120, by=90, bw=180, bh=56;
  ctx.fillStyle='#f7f7f7'; ctx.fillRect(0,0,420,220);
  ctx.fillStyle='#222'; ctx.font='18px sans-serif'; ctx.fillText('Welcome — please sign in', 90, 50);
  ctx.fillStyle='%s'; ctx.fillRect(bx,by,bw,bh);
  ctx.fillStyle='#fff'; ctx.font='20px sans-serif'; ctx.fillText('Sign in', bx+52, by+36);
  c.addEventListener('click', function(e){
    var r=c.getBoundingClientRect();
    var x=e.clientX-r.left, y=e.clientY-r.top;
    if (x>=bx && x<=bx+bw && y>=by && y<=by+bh) location.href='/canvas-signin/login';
  });
})();
</script>
</main>`, rgbCSS(markerSubmit))
}

func (a *canvasSigninApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.Reset()

	mux.HandleFunc("/canvas-signin/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/canvas-signin/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("CanvasApp", a.page()))
	})

	mux.HandleFunc("/canvas-signin/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if checkCreds(r.FormValue("email"), r.FormValue("password")) {
				setSessionCookie(w, "cs_sid", a.sess.New(DefaultUser), "/canvas-signin")
				http.Redirect(w, r, "/canvas-signin/app", http.StatusSeeOther)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, Page("Sign in", `<h1>Sign in</h1>`+LoginForm("/canvas-signin/login", "Sign in", "Invalid email or password", nil)))
			return
		}
		writeHTML(w, Page("Sign in", `<h1>Sign in</h1>`+LoginForm("/canvas-signin/login", "Sign in", "", nil)))
	})

	mux.HandleFunc("/canvas-signin/app", requireCookie(&a.sess, "cs_sid", "/canvas-signin/", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("canvas-signin", u, "/canvas-signin/api/me", "/canvas-signin/logout"))
	}))
	mux.HandleFunc("/canvas-signin/api/me", meHandler(&a.sess, "cs_sid"))
	mux.HandleFunc("/canvas-signin/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "cs_sid"))
		clearCookie(w, "cs_sid", "/canvas-signin")
		http.Redirect(w, r, "/canvas-signin/", http.StatusSeeOther)
	})
}
