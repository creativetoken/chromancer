package lab

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// crawlApp is the Phase-2 target: a nav menu, a paginated list, item detail
// pages, a settings form, a logout link and a delete-account button, and a
// session that expires after a number of authenticated requests (forcing a
// mid-crawl re-login).
type crawlApp struct {
	sess        Sessions
	mu          sync.Mutex
	reqCount    map[string]int // token -> authed request count
	expireAfter int
	deleted     bool
}

// CrawlDeleted reports whether the crawl app's delete-account endpoint was ever
// hit (a ground-truth failure for the crawl test).
func (s *Server) CrawlDeleted() bool {
	a, ok := s.apps["crawl"].(*crawlApp)
	if !ok {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.deleted
}

func (a *crawlApp) Name() string { return "crawl" }
func (a *crawlApp) Reset() {
	a.sess.Reset()
	a.mu.Lock()
	a.reqCount = map[string]int{}
	a.expireAfter = 5 // force a mid-crawl session expiry → re-login
	a.mu.Unlock()
}
func (a *crawlApp) Expect(base string) Expect {
	return Expect{App: "crawl", Flow: "form", Root: base + "/crawl/", LoginURL: base + "/crawl/login",
		LandingURL: base + "/crawl/app", MeURL: base + "/crawl/api/me", Username: DefaultUser, Password: DefaultPass,
		AuthMechanism: "cookie", CookieName: "crawl_sid", Replayable: true, DiscoverableBy: "link",
		Notes: "authenticated crawl target; session expires after N authed requests"}
}

// CrawlStates are the distinct URL templates a crawl should reach.
var CrawlStates = []string{"/crawl/app", "/crawl/items", "/crawl/item/{id}", "/crawl/settings", "/crawl/profile"}

func (a *crawlApp) Register(mux *http.ServeMux, s *Server) {
	registerExpect(mux, s, a)
	a.Reset()
	mux.HandleFunc("/crawl/__expect-states", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, CrawlStates)
	})
	mux.HandleFunc("/crawl/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crawl/" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, Page("Crawl", `<nav><a href="/crawl/">Home</a><a href="/crawl/login">Log in</a></nav><h1>Crawl App</h1>`))
	})
	mux.HandleFunc("/crawl/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if checkCreds(r.FormValue("email"), r.FormValue("password")) {
				tok := a.sess.New(DefaultUser)
				a.mu.Lock()
				a.reqCount[tok] = 0
				a.mu.Unlock()
				setSessionCookie(w, "crawl_sid", tok, "/crawl")
				http.Redirect(w, r, "/crawl/app", http.StatusSeeOther)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/crawl/login", "Log in", "Invalid email or password", nil)))
			return
		}
		writeHTML(w, Page("Log in", `<h1>Log in</h1>`+LoginForm("/crawl/login", "Log in", "", nil)))
	})

	// authed wraps a handler with session + expiry-after-N logic.
	authed := func(next func(w http.ResponseWriter, r *http.Request, u string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			tok := cookieValue(r, "crawl_sid")
			u, ok := a.sess.User(tok)
			if !ok {
				http.Redirect(w, r, "/crawl/login", http.StatusSeeOther)
				return
			}
			a.mu.Lock()
			a.reqCount[tok]++
			n, exp := a.reqCount[tok], a.expireAfter
			a.mu.Unlock()
			if n > exp {
				a.sess.Delete(tok)
				clearCookie(w, "crawl_sid", "/crawl")
				http.Redirect(w, r, "/crawl/login", http.StatusSeeOther)
				return
			}
			next(w, r, u)
		}
	}

	menu := `<nav><a href="/crawl/app">Home</a><a href="/crawl/items">Items</a><a href="/crawl/settings">Settings</a><a href="/crawl/profile">Profile</a><a href="/crawl/logout" id="logout">Log out</a></nav>`

	mux.HandleFunc("/crawl/app", authed(func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, Page("crawl – Home", menu+`<h1>Dashboard</h1><p>Welcome, `+html.EscapeString(u)+`.</p>
<p><span id="me">…</span></p><script>fetch('/crawl/api/me',{credentials:'same-origin'}).then(r=>r.json()).then(j=>document.getElementById('me').textContent=j.email).catch(()=>{});</script>
<p><button type="button">Danger zone</button> <a href="/crawl/delete-account" role="button">Delete account</a></p>`))
	}))
	mux.HandleFunc("/crawl/items", authed(func(w http.ResponseWriter, r *http.Request, u string) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		var b strings.Builder
		b.WriteString(menu + `<h1>Items</h1><ul>`)
		for i := (page-1)*5 + 1; i <= page*5; i++ {
			fmt.Fprintf(&b, `<li><a href="/crawl/item/%d">Item %d</a></li>`, i, i)
		}
		b.WriteString("</ul>")
		if page < 2 {
			fmt.Fprintf(&b, `<a href="/crawl/items?page=%d" rel="next">Next page</a>`, page+1)
		} else {
			b.WriteString(`<a href="/crawl/items?page=1">First page</a>`)
		}
		writeHTML(w, Page("crawl – Items", b.String()))
	}))
	mux.HandleFunc("/crawl/item/", authed(func(w http.ResponseWriter, r *http.Request, u string) {
		id := strings.TrimPrefix(r.URL.Path, "/crawl/item/")
		writeHTML(w, Page("crawl – Item "+html.EscapeString(id), menu+`<h1>Item `+html.EscapeString(id)+`</h1><p>Detail for item `+html.EscapeString(id)+`.</p><a href="/crawl/items">Back to items</a>`))
	}))
	mux.HandleFunc("/crawl/settings", authed(func(w http.ResponseWriter, r *http.Request, u string) {
		if r.Method == http.MethodPost {
			writeHTML(w, Page("crawl – Settings", menu+`<h1>Settings</h1><p class="ok" role="status">Settings saved.</p>`+settingsForm()))
			return
		}
		writeHTML(w, Page("crawl – Settings", menu+`<h1>Settings</h1>`+settingsForm()))
	}))
	mux.HandleFunc("/crawl/profile", authed(func(w http.ResponseWriter, r *http.Request, u string) {
		writeHTML(w, Page("crawl – Profile", menu+`<h1>Profile</h1><p>Signed in as `+html.EscapeString(u)+`.</p>`))
	}))
	mux.HandleFunc("/crawl/api/me", meHandler(&a.sess, "crawl_sid"))

	// Account-safety endpoints the crawl must never hit.
	mux.HandleFunc("/crawl/logout", func(w http.ResponseWriter, r *http.Request) {
		a.sess.Delete(cookieValue(r, "crawl_sid"))
		clearCookie(w, "crawl_sid", "/crawl")
		http.Redirect(w, r, "/crawl/", http.StatusSeeOther)
	})
	mux.HandleFunc("/crawl/delete-account", func(w http.ResponseWriter, r *http.Request) {
		// If the crawl ever reaches this, the test's ground-truth check fails.
		a.mu.Lock()
		a.deleted = true
		a.mu.Unlock()
		writeHTML(w, Page("Deleted", `<h1>Account deleted</h1>`))
	})
}

// settingsForm is a representative form the crawl should fill + submit.
func settingsForm() string {
	return `<form method="post" action="/crawl/settings"><label for="display">Display name</label><input id="display" name="display" type="text" value=""><label for="bio">Bio</label><textarea id="bio" name="bio"></textarea><button type="submit">Save settings</button></form>`
}

// deleted is set if the crawl ever hit delete-account (ground-truth failure).
var _ = 0
