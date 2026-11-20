package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// TestE2EPopupLogin drives a login that opens in a NEW WINDOW on every available
// backend: it clicks the "Open login" button (expecting a popup), fills and
// submits the form IN THE POPUP, then asserts the opener becomes logged in. This
// guards browser.Page.ExpectPopup on both chromium and camoufox. camoufox is
// skipped when its sidecar isn't installed.
func TestE2EPopupLogin(t *testing.T) {
	for _, backend := range backends {
		t.Run(backend, func(t *testing.T) {
			h := newHarnessBackend(t, backend)
			e := h.expect("popup-login")

			parent, err := h.b.NewPage(h.ctx)
			if err != nil {
				t.Fatalf("new page: %v", err)
			}
			defer parent.Close()
			if err := parent.Navigate(h.ctx, e.Root); err != nil {
				t.Fatalf("navigate home: %v", err)
			}

			// Click "Open login", expecting a popup window to open.
			popup, err := parent.ExpectPopup(h.ctx, func() error {
				el, err := parent.Element(h.ctx, nil, "#open-login", 5*time.Second)
				if err != nil {
					return err
				}
				return parent.Click(h.ctx, el)
			}, 15*time.Second)
			if err != nil {
				t.Fatalf("[%s] expect popup: %v", backend, err)
			}
			defer popup.Close()

			// Fill and submit the login form inside the popup.
			_ = popup.WaitStable(h.ctx, 4*time.Second)
			if err := fillField(h, popup, "#email", e.Username); err != nil {
				t.Fatalf("[%s] email: %v", backend, err)
			}
			if err := fillField(h, popup, "#password", e.Password); err != nil {
				t.Fatalf("[%s] password: %v", backend, err)
			}
			sub, err := popup.Element(h.ctx, nil, "#submit", 5*time.Second)
			if err != nil {
				t.Fatalf("[%s] submit control: %v", backend, err)
			}
			if err := popup.Click(h.ctx, sub); err != nil {
				t.Fatalf("[%s] submit click: %v", backend, err)
			}

			// The opener should become logged in once the popup set the cookie.
			if !becomesLoggedIn(h, parent) {
				t.Fatalf("[%s] opener never became logged in after the popup login", backend)
			}
		})
	}
}

func fillField(h *harness, p browser.Page, sel, val string) error {
	el, err := p.Element(h.ctx, nil, sel, 5*time.Second)
	if err != nil {
		return err
	}
	return p.TypeInto(h.ctx, el, val)
}

// becomesLoggedIn reloads the opener a few times and reports whether it shows the
// logged-in state the popup established (the "welcome" marker the home page's
// poll renders once /api/me succeeds).
func becomesLoggedIn(h *harness, parent browser.Page) bool {
	for i := 0; i < 10; i++ {
		_ = parent.Reload(h.ctx)
		st, _, err := perceive.New().Perceive(h.ctx, parent)
		if err == nil {
			for _, n := range st.Nodes {
				if n.Name == "Welcome" {
					return true
				}
			}
			if strings.Contains(st.Text, "Welcome") {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
