package rodbackend

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-rod/rod/lib/proto"

	"github.com/creativetoken/chromancer/internal/browser"
)

// ExportState captures all cookies plus the current origin's local/session
// storage.
func (p *rodPage) ExportState(ctx context.Context) (browser.SessionState, error) {
	rp := p.rp.Context(ctx)
	st := browser.SessionState{Origin: p.Origin(), CapturedAt: time.Now().UTC(), UserAgent: p.b.UserAgent(),
		LocalStorage: map[string]string{}, SessionStorage: map[string]string{}}
	res, err := proto.NetworkGetAllCookies{}.Call(rp)
	if err != nil {
		return st, fmt.Errorf("browser: get cookies: %w", err)
	}
	for _, c := range res.Cookies {
		st.Cookies = append(st.Cookies, browser.Cookie{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			Expires: float64(c.Expires), HTTPOnly: c.HTTPOnly, Secure: c.Secure, SameSite: string(c.SameSite),
		})
	}
	for _, kind := range []string{"localStorage", "sessionStorage"} {
		val, err := rp.Eval(`(k) => { const o = {}; const s = window[k]; for (let i = 0; i < s.length; i++) { const key = s.key(i); o[key] = s.getItem(key); } return JSON.stringify(o); }`, kind)
		if err != nil {
			continue
		}
		m := map[string]string{}
		_ = json.Unmarshal([]byte(val.Value.Str()), &m)
		if kind == "localStorage" {
			st.LocalStorage = m
		} else {
			st.SessionStorage = m
		}
	}
	return st, nil
}

// ImportState sets cookies, then navigates to the origin and restores
// local/session storage. The caller should navigate to the landing page
// afterwards.
func (p *rodPage) ImportState(ctx context.Context, st browser.SessionState) error {
	rp := p.rp.Context(ctx)
	if len(st.Cookies) > 0 {
		params := make([]*proto.NetworkCookieParam, 0, len(st.Cookies))
		for _, c := range st.Cookies {
			cp := &proto.NetworkCookieParam{
				Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
				HTTPOnly: c.HTTPOnly, Secure: c.Secure,
			}
			if c.Expires > 0 {
				cp.Expires = proto.TimeSinceEpoch(c.Expires)
			}
			if c.SameSite != "" {
				cp.SameSite = proto.NetworkCookieSameSite(c.SameSite)
			}
			params = append(params, cp)
		}
		if err := rp.SetCookies(params); err != nil {
			return fmt.Errorf("browser: set cookies: %w", err)
		}
	}
	if st.Origin != "" && (len(st.LocalStorage) > 0 || len(st.SessionStorage) > 0) {
		if err := rp.Navigate(st.Origin + "/"); err != nil {
			return fmt.Errorf("browser: navigate origin: %w", err)
		}
		_ = rp.WaitLoad()
		for kind, m := range map[string]map[string]string{"localStorage": st.LocalStorage, "sessionStorage": st.SessionStorage} {
			if len(m) == 0 {
				continue
			}
			data, _ := json.Marshal(m)
			if _, err := rp.Eval(`(k, j) => { const s = window[k]; const o = JSON.parse(j); for (const key in o) s.setItem(key, o[key]); return true; }`, kind, string(data)); err != nil {
				return fmt.Errorf("browser: restore %s: %w", kind, err)
			}
		}
	}
	return nil
}
