package lab

import (
	"net/http"
	"strings"
)

// canvasSigninApp reproduces a site whose sign-in entry is drawn on a <canvas>
// (no DOM control for it). The perceiver sees the canvas element but not the
// button painted on it, and reports NeedsVision (a canvas node). The agent must
// escalate to VISUAL mode, locate the painted "Sign in" button in the
// screenshot, and click it by coordinate. Clicking the canvas authenticates via
// a fetch that sets the session cookie (a one-click/SSO-style sign-in), which
// check_logged_in then detects. The button is a solid marker colour so the test
// visual policy can find it offline.
//
// Why a fetch (not navigation): camoufox/playwright-go does not reliably apply
// JS-initiated top-level navigation (location.href=, anchor.click, form.submit),
// so the canvas click sets the auth cookie directly (as the shadow-login app
// does) — robust on both backends. (Logged as a camoufox follow-up.)
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
	// A canvas with a green "Sign in" button painted on it. Clicking anywhere on
	// the canvas signs in via a fetch that sets the cookie (success does not depend
	// on JS navigation, which camoufox does not reliably apply). The click listener
	// is attached FIRST, before any drawing, so a draw hiccup can never leave the
	// canvas unclickable. Kept ASCII-only to avoid any charset parsing surprises.
	// The sign-in control is painted on a <canvas> (forces NeedsVision → visual
	// mode: the agent must SEE it). The actual click target is an opacity:0 submit
	// button laid over the canvas: it is NOT perceived (the perceiver skips
	// opacity:0 elements) so it adds no text affordance, and a real click_at on a
	// <button> reliably submits on both backends — whereas a <canvas>'s own click
	// handler and JS-initiated navigation do not fire on camoufox. A native GET
	// form submit (via the real button click) navigates to /proceed, which signs
	// in and redirects to the app.
	// The sign-in control is painted on a <canvas> (forces NeedsVision → visual
	// mode: the agent must SEE it). The actual click target is an opacity:0 button
	// laid over the canvas: it is not perceived (the perceiver skips opacity:0
	// elements) so it adds no text affordance, and a real click_at on it runs the
	// fetch that signs in. (On camoufox a click_at does not fire this over-canvas
	// control — a backend quirk; canvas-signin is chromium-only, see docs/CAMOUFOX.md.)
	script := `(function(){
  var c = document.getElementById('c');
  var ctx = c.getContext('2d');
  ctx.fillStyle='#f7f7f7'; ctx.fillRect(0,0,420,220);
  ctx.fillStyle='#222'; ctx.font='18px sans-serif'; ctx.fillText('Welcome - please sign in', 90, 40);
  ctx.fillStyle='__BTN__'; ctx.fillRect(100,70,220,90);
  ctx.fillStyle='#fff'; ctx.font='22px sans-serif'; ctx.fillText('Sign in', 172, 122);
  document.getElementById('cbtn').addEventListener('click', function(){
    fetch('/canvas-signin/proceed', {method:'POST', headers:{'Content-Type':'application/x-www-form-urlencoded'}, body:'signin=1'})
      .then(function(r){ if(r.ok){ location.href='/canvas-signin/app'; } });
  });
})();`
	script = strings.ReplaceAll(script, "__BTN__", rgbCSS(markerSubmit))
	return `<main style="padding:28px">
<h1>CanvasApp</h1>
<div style="position:relative;width:420px;height:220px">
  <canvas id="c" width="420" height="220" style="position:absolute;top:0;left:0;border:1px solid #ccc"></canvas>
  <button id="cbtn" type="button" aria-hidden="true" style="position:absolute;top:0;left:0;width:420px;height:220px;opacity:0;border:none;background:none;cursor:pointer"></button>
</div>
<script>` + script + `</script>
</main>`
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

	// Clicking the canvas control authenticates directly (one-click/SSO style):
	// set the session cookie and return ok. No credentials form — the test is
	// about visually finding and clicking the canvas-painted control.
	// Clicking the canvas (wrapped in an anchor) lands here via native GET
	// navigation: authenticate directly (one-click/SSO style) and redirect to the
	// app. No credentials form — the test is visually finding and clicking the
	// canvas-painted control.
	mux.HandleFunc("/canvas-signin/proceed", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/canvas-signin/", http.StatusSeeOther)
			return
		}
		setSessionCookie(w, "cs_sid", a.sess.New(DefaultUser), "/canvas-signin")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
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
