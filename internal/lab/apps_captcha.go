package lab

import (
	"fmt"
	"html"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Vendor test keys/tokens the testmode solver answers (from the vendors' docs).
const (
	RecaptchaV2TestKey = "6LeIxAcTAAAAAJcZVRqyHh71UMIEGNQ_MXjiZKhI"
	RecaptchaV2TestTok = "test-recaptcha-token"
	HCaptchaTestKey    = "10000000-ffff-ffff-ffff-000000000001"
	HCaptchaTestTok    = "10000000-aaaa-bbbb-cccc-000000000001"
)

// vendorLoginForm renders an email+password login form with a vendor widget.
func vendorLoginForm(action, widget, errMsg string) string {
	e := ""
	if errMsg != "" {
		e = `<p class="err" role="alert">` + html.EscapeString(errMsg) + `</p>`
	}
	return `<form method="post" action="` + action + `" id="login-form">` + e +
		`<label for="email">Email</label><input id="email" name="email" type="email" autocomplete="username" required>` +
		`<label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required>` +
		widget +
		`<button type="submit">Sign in</button></form>`
}

const widgetStyle = `style="width:300px;height:78px;border:1px solid #ccc;display:inline-block;line-height:78px;text-align:center"`

// NOTE: the mock widgets deliberately do NOT load the real vendor CDN scripts —
// the lab is offline, and detection keys off the DOM widget + data attributes,
// not the vendor JS. (reCAPTCHA v3 is the one case that needs a `render=` script
// src for detection, so it points at a local 404 path that still carries it.)

func recaptchaV2Widget(sitekey string) string {
	return `<div class="g-recaptcha" data-sitekey="` + sitekey + `" data-callback="onCap" ` + widgetStyle + `>[ reCAPTCHA ]</div>
<textarea name="g-recaptcha-response" style="display:none"></textarea>`
}

func hcaptchaWidget(sitekey string) string {
	return `<div class="h-captcha" data-sitekey="` + sitekey + `" ` + widgetStyle + `>[ hCaptcha ]</div>
<textarea name="h-captcha-response" style="display:none"></textarea>`
}

// turnstileManagedWidget auto-fills its response field after a short delay (the
// "managed" challenge resolves itself in a real browser → solved by `native`).
func turnstileManagedWidget(sitekey, token string) string {
	return `<div class="cf-turnstile" data-sitekey="` + sitekey + `" data-action="login" ` + widgetStyle + `>[ Turnstile ]</div>
<input type="hidden" name="cf-turnstile-response">
<script>setTimeout(function(){var el=document.querySelector('input[name="cf-turnstile-response"]');if(el)el.value=` + strconv.Quote(token) + `;},500);</script>`
}

// scoreWidget renders an invisible/score widget that attaches its token on
// submit (the non-blocking path): reCAPTCHA v2 invisible, v3, invisible Turnstile.
func scoreWidget(kind, sitekey, token string) string {
	switch kind {
	case "recaptcha_invisible":
		return `<div class="g-recaptcha" data-sitekey="` + sitekey + `" data-size="invisible"></div>
<input type="hidden" id="cap-resp" name="g-recaptcha-response">` + submitFiller("cap-resp", token)
	case "recaptcha_v3":
		// Local 404 path — the src still carries recaptcha/api.js?render=<key> so
		// detection classifies it as v3/score without any external fetch.
		return `<input type="hidden" id="cap-resp" name="g-recaptcha-response">
<script src="/__lab/stub/recaptcha/api.js?render=` + sitekey + `"></script>` + submitFiller("cap-resp", token)
	case "turnstile_invisible":
		return `<div class="cf-turnstile" data-sitekey="` + sitekey + `" data-size="invisible"></div>
<input type="hidden" id="cap-resp" name="cf-turnstile-response">` + submitFiller("cap-resp", token)
	}
	return ""
}

func submitFiller(fieldID, token string) string {
	return `<script>document.addEventListener('DOMContentLoaded',function(){var f=document.getElementById('login-form');if(f)f.addEventListener('submit',function(){var el=document.getElementById(` + strconv.Quote(fieldID) + `);if(el)el.value=` + strconv.Quote(token) + `;});});</script>`
}

// tokenCaptchaApp is a login app guarded by a vendor widget whose response field
// the server verifies. One struct covers every token-verified vendor variant.
type tokenCaptchaApp struct {
	name, cookie, flow, notes string
	widget                    string // widget HTML
	field                     string // response field name the server checks
	token                     string // expected token ("" = any non-empty)
	sess                      Sessions
}

func (a *tokenCaptchaApp) Name() string { return a.name }
func (a *tokenCaptchaApp) Reset()       { a.sess.Reset() }
func (a *tokenCaptchaApp) Expect(base string) Expect {
	return Expect{App: a.name, Flow: a.flow, Root: base + "/" + a.name + "/", LoginURL: base + "/" + a.name + "/login",
		LandingURL: base + "/" + a.name + "/app", MeURL: base + "/" + a.name + "/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: a.cookie, Replayable: true, DiscoverableBy: "link", Notes: a.notes}
}

func (a *tokenCaptchaApp) ok(r *http.Request) bool {
	v := r.FormValue(a.field)
	if a.token == "" {
		return v != ""
	}
	return v == a.token
}

func (a *tokenCaptchaApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	p := "/" + a.name
	form := func(err string) string { return `<h1>Log in</h1>` + vendorLoginForm(p+"/login", a.widget, err) }
	mux.HandleFunc(p+"/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != p+"/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page(a.name, `<nav><a href="`+p+`/">Home</a><a href="`+p+`/login">Log in</a></nav><h1>`+a.name+`</h1>`))
	})
	mux.HandleFunc(p+"/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if !a.ok(r) {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", form("Please complete the CAPTCHA to continue.")))
				return
			}
			if !checkCreds(r.FormValue("email"), r.FormValue("password")) {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", form("Invalid email or password.")))
				return
			}
			setSessionCookie(w, a.cookie, a.sess.New(DefaultUser), p)
			http.Redirect(w, r, p+"/app", http.StatusSeeOther)
			return
		}
		writeHTML(w, Page("Log in", form("")))
	})
	registerAuthedTail(mux, &a.sess, a.name, a.cookie)
}

// registerAuthedTail mounts the shared /app, /api/me and /logout endpoints.
func registerAuthedTail(mux *http.ServeMux, sess *Sessions, name, cookie string) {
	p := "/" + name
	mux.HandleFunc(p+"/app", requireCookie(sess, cookie, p+"/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage(name, u, p+"/api/me", p+"/logout"))
	}))
	mux.HandleFunc(p+"/api/me", meHandler(sess, cookie))
	mux.HandleFunc(p+"/logout", func(w http.ResponseWriter, r *http.Request) {
		sess.Delete(cookieValue(r, cookie))
		clearCookie(w, cookie, p)
		http.Redirect(w, r, p+"/", http.StatusSeeOther)
	})
}

// captchaVendorApps returns the token-verified vendor mock apps.
func captchaVendorApps() []App {
	return []App{
		&tokenCaptchaApp{name: "mock-recaptcha-v2", cookie: "rv2_sid", flow: "captcha",
			widget: recaptchaV2Widget(RecaptchaV2TestKey), field: "g-recaptcha-response", token: RecaptchaV2TestTok,
			notes: "visible reCAPTCHA v2 checkbox (test sitekey) — solved by testmode"},
		&tokenCaptchaApp{name: "mock-hcaptcha", cookie: "hc_sid", flow: "captcha",
			widget: hcaptchaWidget(HCaptchaTestKey), field: "h-captcha-response", token: HCaptchaTestTok,
			notes: "visible hCaptcha (test sitekey) — solved by testmode"},
		&tokenCaptchaApp{name: "mock-turnstile-managed", cookie: "tm_sid", flow: "captcha",
			widget: turnstileManagedWidget("0x4AAAAAAA_lab_managed", "managed-auto-pass"), field: "cf-turnstile-response", token: "managed-auto-pass",
			notes: "managed Turnstile auto-passes after a delay — solved by native"},
		&tokenCaptchaApp{name: "mock-recaptcha-invisible", cookie: "ri_sid", flow: "captcha",
			widget: scoreWidget("recaptcha_invisible", "6Lc_lab_invisible", "rc-invisible-auto"), field: "g-recaptcha-response", token: "rc-invisible-auto",
			notes: "invisible reCAPTCHA v2 (score) — non-blocking, token attached on submit"},
		&tokenCaptchaApp{name: "mock-recaptcha-v3", cookie: "rv3_sid", flow: "captcha",
			widget: scoreWidget("recaptcha_v3", "6Lc_lab_v3", "rc-v3-auto"), field: "g-recaptcha-response", token: "rc-v3-auto",
			notes: "reCAPTCHA v3 (score) — non-blocking, token attached on submit"},
		&tokenCaptchaApp{name: "mock-turnstile-invisible", cookie: "ti_sid", flow: "captcha",
			widget: scoreWidget("turnstile_invisible", "0x4AAAAAAA_lab_invis", "ts-invisible-auto"), field: "cf-turnstile-response", token: "ts-invisible-auto",
			notes: "invisible Turnstile (score) — non-blocking, token attached on submit"},
	}
}

// ---------------------------------------------------------------- captcha-hard

// captchaHardApp presents an hCaptcha widget with a NON-test sitekey that no
// automated solver can defeat; every submit is rejected until a human solves it.
type captchaHardApp struct {
	sess        Sessions
	mu          sync.Mutex
	humanSolved bool
}

func (a *captchaHardApp) Name() string { return "captcha-hard" }
func (a *captchaHardApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.humanSolved = false
	a.mu.Unlock()
}
func (a *captchaHardApp) Expect(base string) Expect {
	return Expect{App: "captcha-hard", Flow: "block", Root: base + "/captcha-hard/", LoginURL: base + "/captcha-hard/login",
		LandingURL: base + "/captcha-hard/app", MeURL: base + "/captcha-hard/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "ch_sid", Replayable: true, DiscoverableBy: "link",
		Notes: "hCaptcha with a non-test sitekey; rejects every automated solve → human handoff (POST /captcha-hard/__human-solve)"}
}

func (a *captchaHardApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.Reset()
	form := func(err string) string {
		return `<h1>Log in</h1>` + vendorLoginForm("/captcha-hard/login", hcaptchaWidget("10000000-aaaa-hard-real-000000000099"), err)
	}
	mux.HandleFunc("/captcha-hard/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/captcha-hard/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Captcha Hard", `<nav><a href="/captcha-hard/">Home</a><a href="/captcha-hard/login">Log in</a></nav><h1>Captcha Hard</h1>`))
	})
	mux.HandleFunc("/captcha-hard/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			a.mu.Lock()
			solved := a.humanSolved
			a.mu.Unlock()
			if !solved {
				w.WriteHeader(http.StatusForbidden)
				writeHTML(w, Page("Verification required", form("We could not verify that you are human.")))
				return
			}
			if !checkCreds(r.FormValue("email"), r.FormValue("password")) {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", form("Invalid email or password.")))
				return
			}
			setSessionCookie(w, "ch_sid", a.sess.New(DefaultUser), "/captcha-hard")
			http.Redirect(w, r, "/captcha-hard/app", http.StatusSeeOther)
			return
		}
		writeHTML(w, Page("Log in", form("")))
	})
	mux.HandleFunc("/captcha-hard/__human-solve", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.humanSolved = true
		a.mu.Unlock()
		writeJSON(w, map[string]string{"status": "solved"})
	})
	registerAuthedTail(mux, &a.sess, "captcha-hard", "ch_sid")
}

// ------------------------------------------------- captcha-on-failed-submit

// failedSubmitApp shows a plain login form; the FIRST login attempt is rejected
// and the form re-renders WITH a reCAPTCHA v2 (test) widget, which testmode then
// solves on the retry.
type failedSubmitApp struct {
	sess Sessions
	mu   sync.Mutex
	seen map[string]bool // identifier -> has attempted once
}

func (a *failedSubmitApp) Name() string { return "captcha-on-failed-submit" }
func (a *failedSubmitApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.seen = map[string]bool{}
	a.mu.Unlock()
}
func (a *failedSubmitApp) Expect(base string) Expect {
	return Expect{App: "captcha-on-failed-submit", Flow: "captcha", Root: base + "/captcha-on-failed-submit/",
		LoginURL: base + "/captcha-on-failed-submit/login", LandingURL: base + "/captcha-on-failed-submit/app",
		MeURL: base + "/captcha-on-failed-submit/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "fs_sid", Replayable: true, DiscoverableBy: "link",
		Notes: "no widget until the first login attempt is rejected; then a reCAPTCHA v2 test widget appears"}
}

func (a *failedSubmitApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.Reset()
	p := "/captcha-on-failed-submit"
	plain := func(err string) string { return `<h1>Log in</h1>` + vendorLoginForm(p+"/login", "", err) }
	withCaptcha := func(err string) string {
		return `<h1>Log in</h1>` + vendorLoginForm(p+"/login", recaptchaV2Widget(RecaptchaV2TestKey), err)
	}
	mux.HandleFunc(p+"/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != p+"/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Captcha On Failed Submit", `<nav><a href="`+p+`/">Home</a><a href="`+p+`/login">Log in</a></nav><h1>captcha-on-failed-submit</h1>`))
	})
	mux.HandleFunc(p+"/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			id := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
			a.mu.Lock()
			first := !a.seen[id]
			a.seen[id] = true
			a.mu.Unlock()
			if first {
				// Reject the first attempt and now demand a CAPTCHA.
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", withCaptcha("Additional verification required.")))
				return
			}
			if r.FormValue("g-recaptcha-response") != RecaptchaV2TestTok {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", withCaptcha("Please complete the CAPTCHA to continue.")))
				return
			}
			if !checkCreds(r.FormValue("email"), r.FormValue("password")) {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", withCaptcha("Invalid email or password.")))
				return
			}
			setSessionCookie(w, "fs_sid", a.sess.New(DefaultUser), p)
			http.Redirect(w, r, p+"/app", http.StatusSeeOther)
			return
		}
		writeHTML(w, Page("Log in", plain("")))
	})
	registerAuthedTail(mux, &a.sess, "captcha-on-failed-submit", "fs_sid")
}

// ---------------------------------------------------------------- text-captcha

// textCaptchaApp renders a fixed-code text CAPTCHA (ground truth at
// /__lab/truth) solved by the vision solver.
type textCaptchaApp struct {
	sess Sessions
}

const textCaptchaCode = "KX7Q9A2M"

func (a *textCaptchaApp) Name() string { return "text-captcha" }
func (a *textCaptchaApp) Reset()       { a.sess.Reset() }
func (a *textCaptchaApp) Expect(base string) Expect {
	return Expect{App: "text-captcha", Flow: "captcha", Root: base + "/text-captcha/", LoginURL: base + "/text-captcha/login",
		LandingURL: base + "/text-captcha/app", MeURL: base + "/text-captcha/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "tc_sid", Replayable: true, DiscoverableBy: "link",
		Notes: "distorted-text CAPTCHA; ground truth at /text-captcha/__lab/truth; solved by vision"}
}

func (a *textCaptchaApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	p := "/text-captcha"
	widget := `<div class="captcha"><img id="captcha-image" class="captcha" src="` + p + `/captcha.svg" alt="CAPTCHA" width="180" height="60">
<label for="captcha_code">Enter the characters shown above</label>
<input id="captcha_code" name="captcha_code" type="text" maxlength="8" autocomplete="off" required></div>`
	form := func(err string) string { return `<h1>Log in</h1>` + vendorLoginForm(p+"/login", widget, err) }
	mux.HandleFunc(p+"/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != p+"/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Text Captcha", `<nav><a href="`+p+`/">Home</a><a href="`+p+`/login">Log in</a></nav><h1>text-captcha</h1>`))
	})
	mux.HandleFunc(p+"/captcha.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" width="180" height="60"><rect width="180" height="60" fill="#eef"/><text x="12" y="42" font-size="34" font-family="monospace" letter-spacing="6" transform="skewX(-8)">%s</text></svg>`, textCaptchaCode)
	})
	mux.HandleFunc(p+"/__lab/truth", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"code": textCaptchaCode})
	})
	mux.HandleFunc(p+"/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if !strings.EqualFold(strings.TrimSpace(r.FormValue("captcha_code")), textCaptchaCode) {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", form("Incorrect characters. Try again.")))
				return
			}
			if !checkCreds(r.FormValue("email"), r.FormValue("password")) {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", form("Invalid email or password.")))
				return
			}
			setSessionCookie(w, "tc_sid", a.sess.New(DefaultUser), p)
			http.Redirect(w, r, p+"/app", http.StatusSeeOther)
			return
		}
		writeHTML(w, Page("Log in", form("")))
	})
	registerAuthedTail(mux, &a.sess, "text-captcha", "tc_sid")
}

// ---------------------------------------------------------------- image-grid

// imageGridApp renders a 3x3 grid of fixed-position tiles; clicking a tile
// toggles its index in a hidden selection field. The correct set is ground truth
// at /__lab/truth (with tile geometry) so the vision solver can click the right
// tiles. The login submit posts the selection.
type imageGridApp struct {
	sess Sessions
}

var gridCorrect = []int{0, 2, 5} // ground-truth tiles to select

// grid geometry (fixed, viewport coordinates) so clicks are deterministic.
const (
	gridX0, gridY0, gridTile, gridGap = 20, 380, 90, 10
)

func (a *imageGridApp) Name() string { return "image-grid" }
func (a *imageGridApp) Reset()       { a.sess.Reset() }
func (a *imageGridApp) Expect(base string) Expect {
	return Expect{App: "image-grid", Flow: "captcha", Root: base + "/image-grid/", LoginURL: base + "/image-grid/login",
		LandingURL: base + "/image-grid/app", MeURL: base + "/image-grid/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "ig_sid", Replayable: true, DiscoverableBy: "link",
		Notes: "3x3 image grid; ground truth + tile geometry at /image-grid/__lab/truth; solved by vision"}
}

func gridTileRect(i int) (x, y, w, h int) {
	col, row := i%3, i/3
	return gridX0 + col*(gridTile+gridGap), gridY0 + row*(gridTile+gridGap), gridTile, gridTile
}

func (a *imageGridApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	p := "/image-grid"
	var tiles strings.Builder
	for i := 0; i < 9; i++ {
		x, y, w, h := gridTileRect(i)
		fmt.Fprintf(&tiles, `<img class="tile" data-i="%d" src="%s/t%d.svg" onclick="tgl(%d)" style="position:fixed;left:%dpx;top:%dpx;width:%dpx;height:%dpx;border:2px solid #888">`, i, p, i, i, x, y, w, h)
	}
	widget := `<div class="captcha-grid" data-captcha="grid" aria-label="Select all images with a traffic light"><p>Select all squares with <b>traffic lights</b></p>` +
		tiles.String() + `<input type="hidden" name="captcha-token" id="sel" value="">
<script>var sel=new Set();function tgl(i){if(sel.has(i))sel.delete(i);else sel.add(i);document.getElementById('sel').value=[...sel].sort((a,b)=>a-b).join(',');}</script></div>`
	form := func(err string) string { return `<h1>Log in</h1>` + vendorLoginForm(p+"/login", widget, err) }

	mux.HandleFunc(p+"/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != p+"/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Image Grid", `<nav><a href="`+p+`/">Home</a><a href="`+p+`/login">Log in</a></nav><h1>image-grid</h1>`))
	})
	for i := 0; i < 9; i++ {
		mux.HandleFunc(fmt.Sprintf("%s/t%d.svg", p, i), func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/svg+xml")
			fmt.Fprint(w, `<svg xmlns="http://www.w3.org/2000/svg" width="90" height="90"><rect width="90" height="90" fill="#ccd"/></svg>`)
		})
	}
	mux.HandleFunc(p+"/__lab/truth", func(w http.ResponseWriter, r *http.Request) {
		type tile struct{ X, Y, W, H int }
		var rects []tile
		for i := 0; i < 9; i++ {
			x, y, ww, hh := gridTileRect(i)
			rects = append(rects, tile{x, y, ww, hh})
		}
		writeJSON(w, map[string]any{"correct": gridCorrect, "tiles": rects})
	})
	mux.HandleFunc(p+"/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if r.FormValue("captcha-token") != gridSelection() {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", form("Please select the correct images.")))
				return
			}
			if !checkCreds(r.FormValue("email"), r.FormValue("password")) {
				w.WriteHeader(http.StatusUnauthorized)
				writeHTML(w, Page("Log in", form("Invalid email or password.")))
				return
			}
			setSessionCookie(w, "ig_sid", a.sess.New(DefaultUser), p)
			http.Redirect(w, r, p+"/app", http.StatusSeeOther)
			return
		}
		writeHTML(w, Page("Log in", form("")))
	})
	registerAuthedTail(mux, &a.sess, "image-grid", "ig_sid")
}

func gridSelection() string {
	cp := append([]int(nil), gridCorrect...)
	sort.Ints(cp)
	parts := make([]string, len(cp))
	for i, v := range cp {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

// captchaLabApps returns every CAPTCHA mock app.
func captchaLabApps() []App {
	apps := captchaVendorApps()
	apps = append(apps, &captchaHardApp{}, &failedSubmitApp{}, &textCaptchaApp{}, &imageGridApp{})
	return apps
}
