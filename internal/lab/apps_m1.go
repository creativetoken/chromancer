package lab

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------- simple

type simpleApp struct{ sess Sessions }

func (a *simpleApp) Name() string { return "simple" }
func (a *simpleApp) Reset()       { a.sess.Reset() }
func (a *simpleApp) Expect(base string) Expect {
	return Expect{App: "simple", Flow: "form", Root: base + "/simple/", LoginURL: base + "/simple/login",
		LandingURL: base + "/simple/app", MeURL: base + "/simple/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "simple_sid", Replayable: true, DiscoverableBy: "link"}
}

func (a *simpleApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	mux.HandleFunc("/simple/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/simple/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Simple", `<nav><a href="/simple/">Home</a><a href="/simple/about">About</a><a href="/simple/login">Log in</a></nav><h1>Simple App</h1><p>A plain cookie-session login.</p>`))
	})
	mux.HandleFunc("/simple/about", func(w http.ResponseWriter, r *http.Request) {
		writeHTML(w, Page("About", `<h1>About</h1><p>Nothing here.</p><a href="/simple/">Home</a>`))
	})
	mux.HandleFunc("/simple/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if checkCreds(r.FormValue("email"), r.FormValue("password")) {
				setSessionCookie(w, "simple_sid", a.sess.New(DefaultUser), "/simple")
				http.Redirect(w, r, "/simple/app", http.StatusSeeOther)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/simple/login", "Log in", "Invalid email or password", nil)))
			return
		}
		writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/simple/login", "Log in", "", nil)))
	})
	mux.HandleFunc("/simple/app", requireCookie(&a.sess, "simple_sid", "/simple/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("simple", u, "/simple/api/me", "/simple/logout"))
	}))
	mux.HandleFunc("/simple/api/me", meHandler(&a.sess, "simple_sid"))
	mux.HandleFunc("/simple/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "simple_sid"))
		clearCookie(w, "simple_sid", "/simple")
		http.Redirect(w, r, "/simple/", http.StatusSeeOther)
	})
}

// ---------------------------------------------------------------- multistep

type multistepApp struct{ sess Sessions }

func (a *multistepApp) Name() string { return "multistep" }
func (a *multistepApp) Reset()       { a.sess.Reset() }
func (a *multistepApp) Expect(base string) Expect {
	return Expect{App: "multistep", Flow: "multistep", Root: base + "/multistep/", LoginURL: base + "/multistep/login",
		LandingURL: base + "/multistep/app", MeURL: base + "/multistep/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "ms_sid", Replayable: true, DiscoverableBy: "link"}
}

func (a *multistepApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	mux.HandleFunc("/multistep/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/multistep/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Multistep", `<nav><a href="/multistep/">Home</a><a href="/multistep/login">Sign in</a></nav><h1>Multistep App</h1>`))
	})
	mux.HandleFunc("/multistep/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			email := strings.TrimSpace(r.FormValue("email"))
			if !strings.EqualFold(email, DefaultUser) {
				w.WriteHeader(http.StatusNotFound)
				writeHTML(w, Page("Sign in", `<h1>Sign in</h1><p class="err" role="alert">No account for that email.</p>`+emailOnlyForm()))
				return
			}
			http.Redirect(w, r, "/multistep/login/password?e="+email, http.StatusSeeOther)
			return
		}
		writeHTML(w, Page("Sign in", `<h1>Sign in</h1><p>Step 1 of 2</p>`+emailOnlyForm()))
	})
	mux.HandleFunc("/multistep/login/password", func(w http.ResponseWriter, r *http.Request) {
		email := r.URL.Query().Get("e")
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if checkCreds(r.FormValue("email"), r.FormValue("password")) {
				setSessionCookie(w, "ms_sid", a.sess.New(DefaultUser), "/multistep")
				http.Redirect(w, r, "/multistep/app", http.StatusSeeOther)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, Page("Sign in", `<h1>Sign in</h1><p>Step 2 of 2</p>`+passwordOnlyForm(r.FormValue("email"), "Wrong password")))
			return
		}
		writeHTML(w, Page("Sign in", `<h1>Sign in</h1><p>Step 2 of 2 — signing in as `+html.EscapeString(email)+`</p>`+passwordOnlyForm(email, "")))
	})
	mux.HandleFunc("/multistep/app", requireCookie(&a.sess, "ms_sid", "/multistep/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("multistep", u, "/multistep/api/me", "/multistep/logout"))
	}))
	mux.HandleFunc("/multistep/api/me", meHandler(&a.sess, "ms_sid"))
	mux.HandleFunc("/multistep/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "ms_sid"))
		clearCookie(w, "ms_sid", "/multistep")
		http.Redirect(w, r, "/multistep/", http.StatusSeeOther)
	})
}

func emailOnlyForm() string {
	return `<form method="post" action="/multistep/login"><label for="email">Email address</label><input id="email" name="email" type="email" autocomplete="username" required><button type="submit">Next</button></form>`
}

func passwordOnlyForm(email, errMsg string) string {
	e := ""
	if errMsg != "" {
		e = `<p class="err" role="alert">` + html.EscapeString(errMsg) + `</p>`
	}
	return `<form method="post" action="/multistep/login/password?e=` + html.EscapeString(email) + `">` + e +
		`<input type="hidden" name="email" value="` + html.EscapeString(email) + `"><label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required><button type="submit">Sign in</button></form>`
}

// ---------------------------------------------------------------- modal

type modalApp struct{ sess Sessions }

func (a *modalApp) Name() string { return "modal" }
func (a *modalApp) Reset()       { a.sess.Reset() }
func (a *modalApp) Expect(base string) Expect {
	return Expect{App: "modal", Flow: "modal", Root: base + "/modal/", LoginURL: base + "/modal/",
		LandingURL: base + "/modal/app", MeURL: base + "/modal/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "modal_sid", Replayable: true, DiscoverableBy: "modal",
		Notes: "no /login route; login form is in a modal opened by the header 'Sign in' button"}
}

func (a *modalApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	mux.HandleFunc("/modal/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/modal/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Modal", `<nav><a href="/modal/">Home</a><a href="/modal/pricing">Pricing</a><button id="signin-btn" type="button">Sign in</button></nav>
<h1>Modal App</h1><p>Sign in via the header button.</p>
<div class="modal" id="signin-modal" role="dialog" aria-label="Sign in"><div class="box"><h2>Sign in</h2>
<form id="login-form"><p class="err" id="err" role="alert"></p>
<label for="email">Email</label><input id="email" name="email" type="email" autocomplete="username" required>
<label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required>
<button type="submit">Sign in</button> <button type="button" id="cancel">Cancel</button></form></div></div>
<script>
const m=document.getElementById('signin-modal');
document.getElementById('signin-btn').onclick=()=>{m.classList.add('open');document.getElementById('email').focus();};
document.getElementById('cancel').onclick=()=>m.classList.remove('open');
document.getElementById('login-form').onsubmit=async(e)=>{e.preventDefault();
 const r=await fetch('/modal/api/login',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'same-origin',body:JSON.stringify({email:email.value,password:password.value})});
 if(r.ok){location.href='/modal/app';}else{document.getElementById('err').textContent='Invalid email or password';}
};
</script>`))
	})
	mux.HandleFunc("/modal/pricing", func(w http.ResponseWriter, r *http.Request) {
		writeHTML(w, Page("Pricing", `<h1>Pricing</h1><a href="/modal/">Home</a>`))
	})
	mux.HandleFunc("/modal/api/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Email, Password string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if !checkCreds(in.Email, in.Password) {
			jsonErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		setSessionCookie(w, "modal_sid", a.sess.New(DefaultUser), "/modal")
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/modal/app", requireCookie(&a.sess, "modal_sid", "/modal/", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("modal", u, "/modal/api/me", "/modal/logout"))
	}))
	mux.HandleFunc("/modal/api/me", meHandler(&a.sess, "modal_sid"))
	mux.HandleFunc("/modal/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "modal_sid"))
		clearCookie(w, "modal_sid", "/modal")
		http.Redirect(w, r, "/modal/", http.StatusSeeOther)
	})
}

// ---------------------------------------------------------------- iframe

type iframeApp struct{ sess Sessions }

func (a *iframeApp) Name() string { return "iframe" }
func (a *iframeApp) Reset()       { a.sess.Reset() }
func (a *iframeApp) Expect(base string) Expect {
	return Expect{App: "iframe", Flow: "iframe", Root: base + "/iframe/", LoginURL: base + "/iframe/",
		LandingURL: base + "/iframe/app", MeURL: base + "/iframe/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "iframe_sid", Replayable: true, DiscoverableBy: "link",
		Notes: "login form lives inside an iframe on the root page"}
}

func (a *iframeApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	mux.HandleFunc("/iframe/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/iframe/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Iframe", `<h1>Iframe App</h1><p>Please sign in below.</p><iframe src="/iframe/frame" title="Sign in" style="width:420px;height:300px;border:1px solid #ccc"></iframe>`))
	})
	mux.HandleFunc("/iframe/frame", func(w http.ResponseWriter, r *http.Request) {
		writeHTML(w, Page("Sign in", `<h2>Sign in</h2><form method="post" action="/iframe/login" target="_top"><label for="email">Email</label><input id="email" name="email" type="email" required><label for="password">Password</label><input id="password" name="password" type="password" required><button type="submit">Sign in</button></form>`))
	})
	mux.HandleFunc("/iframe/login", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Method == http.MethodPost && checkCreds(r.FormValue("email"), r.FormValue("password")) {
			setSessionCookie(w, "iframe_sid", a.sess.New(DefaultUser), "/iframe")
			http.Redirect(w, r, "/iframe/app", http.StatusSeeOther)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		writeHTML(w, Page("Sign in", `<h1>Invalid credentials</h1><a href="/iframe/">Try again</a>`))
	})
	mux.HandleFunc("/iframe/app", requireCookie(&a.sess, "iframe_sid", "/iframe/", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("iframe", u, "/iframe/api/me", "/iframe/logout"))
	}))
	mux.HandleFunc("/iframe/api/me", meHandler(&a.sess, "iframe_sid"))
	mux.HandleFunc("/iframe/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "iframe_sid"))
		clearCookie(w, "iframe_sid", "/iframe")
		http.Redirect(w, r, "/iframe/", http.StatusSeeOther)
	})
}

// ---------------------------------------------------------------- spa-jwt

type spaJWTApp struct {
	secret []byte
}

func (a *spaJWTApp) Name() string { return "spa-jwt" }
func (a *spaJWTApp) Reset()       {}
func (a *spaJWTApp) Expect(base string) Expect {
	return Expect{App: "spa-jwt", Flow: "spa", Root: base + "/spa-jwt/", LoginURL: base + "/spa-jwt/",
		LandingURL: base + "/spa-jwt/#/app", MeURL: base + "/spa-jwt/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "bearer", BearerHeader: "authorization", Replayable: true, DiscoverableBy: "link",
		Notes: "JWT in localStorage key spa_jwt_token; sent as Authorization: Bearer on XHR"}
}

func (a *spaJWTApp) key() []byte {
	if a.secret == nil {
		a.secret = []byte("lab-spa-secret")
	}
	return a.secret
}

func (a *spaJWTApp) sign(payload string) string {
	mac := hmac.New(sha256.New, a.key())
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *spaJWTApp) token(user string) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body, _ := json.Marshal(map[string]any{"sub": user, "exp": time.Now().Add(2 * time.Hour).Unix(), "iat": time.Now().Unix()})
	pl := base64.RawURLEncoding.EncodeToString(body)
	return hdr + "." + pl + "." + a.sign(hdr+"."+pl)
}

func (a *spaJWTApp) verify(tok string) (string, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || a.sign(parts[0]+"."+parts[1]) != parts[2] {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var claims struct {
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || time.Now().Unix() > claims.Exp {
		return "", false
	}
	return claims.Sub, true
}

func (a *spaJWTApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	mux.HandleFunc("/spa-jwt/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/spa-jwt/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("SPA", `<div id="root"><h1>SPA App</h1><p>Loading…</p></div>
<script>
const root=document.getElementById('root');
const KEY='spa_jwt_token';
async function me(){const t=localStorage.getItem(KEY);if(!t)return null;const r=await fetch('/spa-jwt/api/me',{headers:{'Authorization':'Bearer '+t}});return r.ok?r.json():null;}
function renderLogin(err){root.innerHTML='<h1>SPA App</h1><nav><a href="#/">Home</a><a href="#/login">Log in</a></nav><h2>Log in</h2>'+(err?'<p class="err" role="alert">'+err+'</p>':'')+'<form id="f"><label for="email">Email</label><input id="email" name="email" type="email" autocomplete="username" required><label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required><button type="submit">Log in</button></form>';
 document.getElementById('f').onsubmit=async(e)=>{e.preventDefault();const r=await fetch('/spa-jwt/api/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({email:email.value,password:password.value})});
 if(r.ok){const j=await r.json();localStorage.setItem(KEY,j.token);location.hash='#/app';boot();}else{renderLogin('Invalid email or password');}};}
async function renderApp(u){root.innerHTML='<nav><a href="#/">Home</a><button id="logout" type="button">Logout</button></nav><h1>Dashboard</h1><p>Welcome, <span id="user">'+u.email+'</span>.</p><p>Account: <span id="me">'+u.email+'</span></p>';
 document.getElementById('logout').onclick=()=>{localStorage.removeItem(KEY);location.hash='#/';boot();};
 setTimeout(()=>me(),300);}
async function boot(){const u=await me();if(u){location.hash='#/app';renderApp(u);}else{renderLogin('');}}
boot();
</script>`))
	})
	mux.HandleFunc("/spa-jwt/api/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Email, Password string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if !checkCreds(in.Email, in.Password) {
			jsonErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		writeJSON(w, map[string]string{"token": a.token(DefaultUser)})
	})
	mux.HandleFunc("/spa-jwt/api/me", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			jsonErr(w, http.StatusUnauthorized, "missing bearer")
			return
		}
		u, ok := a.verify(strings.TrimPrefix(auth, "Bearer "))
		if !ok {
			jsonErr(w, http.StatusUnauthorized, "bad token")
			return
		}
		writeJSON(w, map[string]any{"email": u, "id": 1, "app": "spa-jwt"})
	})
}

// ---------------------------------------------------------------- hidden-entry

type hiddenEntryApp struct{ sess Sessions }

func (a *hiddenEntryApp) Name() string { return "hidden-entry" }
func (a *hiddenEntryApp) Reset()       { a.sess.Reset() }
func (a *hiddenEntryApp) Expect(base string) Expect {
	return Expect{App: "hidden-entry", Flow: "hidden", Root: base + "/hidden-entry/", LoginURL: base + "/hidden-entry/members-portal",
		LandingURL: base + "/hidden-entry/app", MeURL: base + "/hidden-entry/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "he_sid", Replayable: true, DiscoverableBy: "llm",
		Notes: "login reachable only via the 'Members area' link; path not in the common probe list"}
}

func (a *hiddenEntryApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	mux.HandleFunc("/hidden-entry/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hidden-entry/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Hidden Entry", `<nav><a href="/hidden-entry/">Home</a><a href="/hidden-entry/about">About</a><a href="/hidden-entry/pricing">Pricing</a><a href="/hidden-entry/blog">Blog</a><a href="/hidden-entry/careers">Careers</a><a href="/hidden-entry/members-portal">Members area</a><a href="/hidden-entry/contact">Contact</a></nav>
<h1>Acme Widgets</h1><p>The finest widgets. Existing customers can manage their widgets in the members area.</p>`))
	})
	for _, p := range []string{"about", "pricing", "blog", "careers", "contact"} {
		p := p
		mux.HandleFunc("/hidden-entry/"+p, func(w http.ResponseWriter, r *http.Request) {
			writeHTML(w, Page(p, `<h1>`+p+`</h1><a href="/hidden-entry/">Home</a>`))
		})
	}
	mux.HandleFunc("/hidden-entry/members-portal", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if checkCreds(r.FormValue("email"), r.FormValue("password")) {
				setSessionCookie(w, "he_sid", a.sess.New(DefaultUser), "/hidden-entry")
				http.Redirect(w, r, "/hidden-entry/app", http.StatusSeeOther)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, Page("Members", `<h1>Members area</h1>`+LoginForm("/hidden-entry/members-portal", "Enter", "Invalid email or password", nil)))
			return
		}
		writeHTML(w, Page("Members", `<h1>Members area</h1>`+LoginForm("/hidden-entry/members-portal", "Enter", "", nil)))
	})
	mux.HandleFunc("/hidden-entry/app", requireCookie(&a.sess, "he_sid", "/hidden-entry/members-portal", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, AppPage("hidden-entry", u, "/hidden-entry/api/me", "/hidden-entry/logout"))
	}))
	mux.HandleFunc("/hidden-entry/api/me", meHandler(&a.sess, "he_sid"))
	mux.HandleFunc("/hidden-entry/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "he_sid"))
		clearCookie(w, "he_sid", "/hidden-entry")
		http.Redirect(w, r, "/hidden-entry/", http.StatusSeeOther)
	})
}

// ---------------------------------------------------------------- bound-session

type boundSessionApp struct {
	sess   Sessions
	mu     sync.Mutex
	nonces map[string]bool
}

const boundClientSecret = "client-secret-xyz"

func (a *boundSessionApp) Name() string { return "bound-session" }
func (a *boundSessionApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.nonces = map[string]bool{}
	a.mu.Unlock()
}
func (a *boundSessionApp) Expect(base string) Expect {
	return Expect{App: "bound-session", Flow: "bound", Root: base + "/bound-session/", LoginURL: base + "/bound-session/login",
		LandingURL: base + "/bound-session/app", MeURL: base + "/bound-session/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "bs_sid", Replayable: false, DiscoverableBy: "link",
		Notes: "API requires a JS-computed per-request X-Req-Nonce/X-Req-Sig; nonce reuse is rejected so a captured request cannot be replayed"}
}

func (a *boundSessionApp) useNonce(n string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.nonces == nil {
		a.nonces = map[string]bool{}
	}
	if a.nonces[n] {
		return false
	}
	a.nonces[n] = true
	return true
}

func (a *boundSessionApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	mux.HandleFunc("/bound-session/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bound-session/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Bound", `<nav><a href="/bound-session/">Home</a><a href="/bound-session/login">Log in</a></nav><h1>Bound Session App</h1>`))
	})
	mux.HandleFunc("/bound-session/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if checkCreds(r.FormValue("email"), r.FormValue("password")) {
				setSessionCookie(w, "bs_sid", a.sess.New(DefaultUser), "/bound-session")
				http.Redirect(w, r, "/bound-session/app", http.StatusSeeOther)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/bound-session/login", "Log in", "Invalid email or password", nil)))
			return
		}
		writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/bound-session/login", "Log in", "", nil)))
	})
	mux.HandleFunc("/bound-session/app", requireCookie(&a.sess, "bs_sid", "/bound-session/login", func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, Page("bound-session – Dashboard", fmt.Sprintf(`<nav><a href="/bound-session/">Home</a><a href="/bound-session/logout" id="logout">Logout</a></nav>
<h1>Dashboard</h1><p>Welcome, <span id="user">%s</span>.</p><p>Account: <span id="me">…</span></p>
<script>
const SECRET=%q;
async function sha256hex(s){const b=new TextEncoder().encode(s);const d=await crypto.subtle.digest('SHA-256',b);return Array.from(new Uint8Array(d)).map(x=>x.toString(16).padStart(2,'0')).join('');}
async function me(){const nonce=Array.from(crypto.getRandomValues(new Uint8Array(12))).map(x=>x.toString(16).padStart(2,'0')).join('');const sig=await sha256hex(nonce+':'+SECRET);
 const r=await fetch('/bound-session/api/me',{credentials:'same-origin',headers:{'X-Req-Nonce':nonce,'X-Req-Sig':sig}});if(r.ok){const j=await r.json();document.getElementById('me').textContent=j.email;}else{document.getElementById('me').textContent='error '+r.status;}}
me();setTimeout(me,400);setTimeout(me,900);
</script>`, html.EscapeString(u), boundClientSecret)))
	}))
	mux.HandleFunc("/bound-session/api/me", func(w http.ResponseWriter, r *http.Request) {
		u, ok := a.sess.User(cookieValue(r, "bs_sid"))
		if !ok {
			jsonErr(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		nonce, sig := r.Header.Get("X-Req-Nonce"), r.Header.Get("X-Req-Sig")
		if nonce == "" || sig == "" {
			jsonErr(w, http.StatusUnauthorized, "missing request signature")
			return
		}
		sum := sha256.Sum256([]byte(nonce + ":" + boundClientSecret))
		if hex.EncodeToString(sum[:]) != sig {
			jsonErr(w, http.StatusUnauthorized, "bad signature")
			return
		}
		if !a.useNonce(nonce) {
			jsonErr(w, http.StatusUnauthorized, "nonce reused")
			return
		}
		writeJSON(w, map[string]any{"email": u, "id": 1, "app": "bound-session"})
	})
	mux.HandleFunc("/bound-session/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "bs_sid"))
		clearCookie(w, "bs_sid", "/bound-session")
		http.Redirect(w, r, "/bound-session/", http.StatusSeeOther)
	})
}
