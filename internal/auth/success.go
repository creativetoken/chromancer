package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/handback"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// Baseline is the pre-login snapshot success detection compares against.
type Baseline struct {
	Cookies     map[string]string
	LocalKeys   map[string]bool
	SessionKeys map[string]bool
	URL         string
}

// TakeBaseline snapshots cookies/storage before the flow runs.
func TakeBaseline(ctx context.Context, p *browser.Page) Baseline {
	b := Baseline{Cookies: map[string]string{}, LocalKeys: map[string]bool{}, SessionKeys: map[string]bool{}, URL: p.URL()}
	st, err := p.ExportState(ctx)
	if err != nil {
		return b
	}
	for _, c := range st.Cookies {
		b.Cookies[c.Name] = c.Value
	}
	for k := range st.LocalStorage {
		b.LocalKeys[k] = true
	}
	for k := range st.SessionStorage {
		b.SessionKeys[k] = true
	}
	return b
}

var (
	authCookieRe = regexp.MustCompile(`(?i)(sess|sid|auth|token|jwt|login|user|id$|_id|remember)`)
	jwtRe        = regexp.MustCompile(`^eyJ[A-Za-z0-9\-_]+\.[A-Za-z0-9\-_]+\.[A-Za-z0-9\-_]*$`)
	authedElRe   = regexp.MustCompile(`(?i)^(log ?out|sign ?out|my account|account|profile|dashboard|settings)\b`)
	meURLRe      = regexp.MustCompile(`(?i)/(me|session|account|profile|user|whoami|current[-_]?user)(/|\?|$)`)
)

// SuccessDetector asserts login succeeded from deterministic signals.
type SuccessDetector struct{}

// IsAuthenticated returns whether the page is authenticated and the evidence.
// Signals (cheapest first): (1) new auth cookie / JWT in storage; (2) a
// /me-style request returned 2xx; (3) an authenticated-only element is
// present; (4) the login form is gone and the URL left the auth route.
func (SuccessDetector) IsAuthenticated(ctx context.Context, p *browser.Page, st perceive.State, base Baseline, xhrs []handback.HAREntry) (bool, []string) {
	var evidence []string
	strong := false
	f := Classify(st)
	if f.Password != nil && !f.HasAlert {
		return false, nil
	}
	if f.HasAlert {
		return false, []string{"error_alert: " + f.AlertText}
	}
	if cur, err := p.ExportState(ctx); err == nil {
		for _, c := range cur.Cookies {
			if old, had := base.Cookies[c.Name]; !had || old != c.Value {
				if authCookieRe.MatchString(c.Name) || c.HTTPOnly {
					evidence = append(evidence, "new_auth_cookie:"+c.Name)
					strong = true
				} else {
					evidence = append(evidence, "new_cookie:"+c.Name)
				}
			}
		}
		for k, v := range cur.LocalStorage {
			if !base.LocalKeys[k] && (jwtRe.MatchString(v) || authCookieRe.MatchString(k)) {
				evidence = append(evidence, "new_storage_token:"+k)
				strong = true
			}
		}
		for k, v := range cur.SessionStorage {
			if !base.SessionKeys[k] && (jwtRe.MatchString(v) || authCookieRe.MatchString(k)) {
				evidence = append(evidence, "new_session_storage_token:"+k)
				strong = true
			}
		}
	}
	for _, e := range xhrs {
		if e.Response.Status >= 200 && e.Response.Status < 300 && meURLRe.MatchString(e.Request.URL) {
			evidence = append(evidence, "me_endpoint_200:"+e.Request.URL)
			strong = true
			break
		}
	}
	for _, n := range st.Nodes {
		if !n.Visible {
			continue
		}
		label := n.Name
		if label == "" {
			label = n.Text
		}
		if (n.Tag == "a" || n.Tag == "button" || n.Role == "button") && authedElRe.MatchString(strings.TrimSpace(label)) {
			evidence = append(evidence, "authed_element:"+strings.TrimSpace(label))
			strong = true
			break
		}
	}
	if strong {
		return true, evidence
	}
	if f.Password == nil && f.Identifier == nil && base.URL != "" && st.URL != base.URL && !authWordRe.MatchString(st.Title) {
		evidence = append(evidence, "left_auth_route:"+st.URL)
		return true, evidence
	}
	return false, evidence
}

// JWTExpiry extracts the exp claim from the first JWT found among the values.
func JWTExpiry(values ...string) *time.Time {
	for _, v := range values {
		v = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(v, "Bearer "), "bearer "))
		if !jwtRe.MatchString(v) {
			continue
		}
		parts := strings.Split(v, ".")
		if len(parts) < 2 {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		var claims struct {
			Exp int64 `json:"exp"`
		}
		if json.Unmarshal(raw, &claims) == nil && claims.Exp > 0 {
			t := time.Unix(claims.Exp, 0).UTC()
			return &t
		}
	}
	return nil
}
