package e2e

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
)

// trustedModalHTML is a step-up verification modal that reproduces the live
// camoufox bambulab OTP failure in isolation, from two DOM/Playwright invariants:
//
//   - The code <input> and the Confirm <button> oscillate by a sub-pixel transform
//     every animation frame, so their bounding box is NEVER "stable". Playwright's
//     actionability-gated ElementHandle.Click never settles on such a target and
//     times out ("camoufox: click: context deadline exceeded" — the live note).
//   - The Confirm's handler acts only on a TRUSTED click (isTrusted). A page-JS
//     el.click() fallback dispatches an UNTRUSTED event, which the handler ignores.
//
// So the handle-only path cannot submit the form; only a TRUSTED coordinate click
// (what go-rod dispatches via CDP on the chromium backend, and what the camoufox
// coordinate fallback adds) does. On a trusted click the handler POSTs the code via
// fetch (as a real React OTP form does) and records the server's verdict in a DOM
// attribute the test reads back.
const trustedModalHTML = `<!doctype html><html><head><meta charset="utf-8"><title>verify</title></head>
<body style="font-family:sans-serif;padding:24px">
<h1>Verification Code</h1>
<form id="f" onsubmit="return false">
  <input id="code" name="code" type="text" inputmode="numeric" maxlength="6" placeholder="Verification code" autocomplete="one-time-code" style="will-change:transform">
  <button type="button" id="confirm" style="will-change:transform">Confirm</button>
</form>
<script>
(function(){
  var codeEl=document.getElementById('code');
  var confirmEl=document.getElementById('confirm');
  var committed="";
  codeEl.addEventListener('input',function(e){ committed=codeEl.value; });
  // Submit ONLY on a TRUSTED click; a page-JS el.click() (isTrusted===false) no-ops.
  // A real React OTP form POSTs via fetch rather than a full-page navigation.
  confirmEl.addEventListener('click',function(e){
    if(!e.isTrusted) return;
    fetch('/submit', {method:'POST', headers:{'Content-Type':'application/x-www-form-urlencoded'}, body:'code='+encodeURIComponent(committed)})
      .then(function(r){ return r.text(); })
      .then(function(t){ document.body.setAttribute('data-result', t); });
  });
  // Oscillate both controls every frame so their box is never "stable".
  var phase=0;
  (function tick(){
    phase^=1;
    var dy=phase?'0.4px':'0px';
    codeEl.style.transform='translateY('+dy+')';
    confirmEl.style.transform='translateY('+dy+')';
    requestAnimationFrame(tick);
  })();
})();
</script>
</body></html>`

// TestE2ECoordEntryTrustedModal drives the camoufox backend's TypeInto+Click on the
// trusted/animating modal directly — no auth, fetch, or agent-takeover machinery —
// a fast, deterministic red→green for the trusted coordinate fallback. The
// handle-only path (AGENTBROWSER_DISABLE_COORD_FALLBACK=1) cannot submit; the default
// trusted coordinate click does.
//
// camoufox-only on purpose: the modal animates every frame as a deterministic stress
// model so Playwright's actionability gate never settles, which the coordinate path
// (move+trusted click, no stability wait) bypasses. go-rod's own interaction waits on
// WaitStableRAF, which never settles on a perpetually-animating element and would
// hang — a pre-existing rodbackend trait that real, briefly-animating sites do not
// trigger, and out of scope here.
func TestE2ECoordEntryTrustedModal(t *testing.T) {
	if os.Getenv("AGENTBROWSER_E2E_BACKEND") != "camoufox" {
		t.Skip("exercises the camoufox trusted-coordinate fallback; chromium uses go-rod's native CDP input")
	}
	const code = "123456"
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(trustedModalHTML))
	})
	mux.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("code") == code {
			_, _ = w.Write([]byte("VERIFIED"))
			return
		}
		_, _ = w.Write([]byte("BAD"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	h := newHarness(t)
	p, err := h.b.NewPage(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Navigate(h.ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}

	// Type the code into the animating field (el.Type delivers trusted keys).
	codeEl, err := p.Element(h.ctx, nil, "#code", 5*time.Second)
	if err != nil {
		t.Fatalf("resolve code field: %v", err)
	}
	if err := p.TypeInto(h.ctx, codeEl, code); err != nil {
		t.Fatalf("type code: %v", err)
	}

	// Click Confirm. The actionable click cannot settle on the animating target; the
	// trusted coordinate fallback is what makes the gated handler fire and POST.
	confirmEl, err := p.Element(h.ctx, nil, "#confirm", 5*time.Second)
	if err != nil {
		t.Fatalf("resolve confirm: %v", err)
	}
	if err := p.Click(h.ctx, confirmEl); err != nil {
		t.Fatalf("click confirm: %v", err)
	}

	// Wait for the fetch verdict to land in the DOM (read via a DOM attribute so it
	// is visible regardless of the JS world Eval runs in).
	deadline := time.Now().Add(10 * time.Second)
	var result string
	for time.Now().Before(deadline) {
		_ = p.WaitStable(h.ctx, browser.DefaultStableTimeout)
		v, err := p.Eval(h.ctx, "() => document.body.getAttribute('data-result') || ''")
		if err == nil {
			result = v.Str()
		}
		if result != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if result != "VERIFIED" {
		dbg, _ := p.Eval(h.ctx, "() => (document.getElementById('code')||{}).value")
		t.Fatalf("trusted coordinate click did not submit the gated form: data-result=%q codeVal=%q url=%s", result, dbg.Str(), p.URL())
	}
}
