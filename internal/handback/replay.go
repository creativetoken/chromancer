package handback

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"reflect"
	"strings"
	"time"
)

// ReplayChecker answers "can the caller use plain HTTP with this bundle, or
// must it keep a browser?" by replaying one captured authenticated XHR with a
// plain Go HTTP client using only the bundle's cookies + headers.
type ReplayChecker interface {
	Check(ctx context.Context, b SessionBundle, rec AuthRecording, varying []string) (replayable bool, reason string, err error)
}

// HTTPReplayChecker is the default ReplayChecker.
type HTTPReplayChecker struct {
	Client *http.Client
}

// NewHTTPReplayChecker builds a checker with a fresh cookie-jar client.
func NewHTTPReplayChecker() *HTTPReplayChecker {
	jar, _ := cookiejar.New(nil)
	return &HTTPReplayChecker{Client: &http.Client{Jar: jar, Timeout: 15 * time.Second}}
}

// pickCandidate chooses the XHR to replay: a post-login GET with a 2xx status
// and a JSON-ish body, preferring /me-style URLs.
func pickCandidate(rec AuthRecording) (HAREntry, bool) {
	var best HAREntry
	found := false
	score := -1
	for _, e := range rec.Entries {
		if e.Phase != "post_login" || !isXHR(e.Request.Type) {
			continue
		}
		if e.Response.Status < 200 || e.Response.Status >= 300 || e.Request.Method != http.MethodGet {
			continue
		}
		s := 1
		if strings.Contains(e.Response.MimeType, "json") {
			s += 2
		}
		if strings.Contains(strings.ToLower(e.Request.URL), "/me") || strings.Contains(strings.ToLower(e.Request.URL), "profile") || strings.Contains(strings.ToLower(e.Request.URL), "session") {
			s += 3
		}
		if s > score {
			score, best, found = s, e, true
		}
	}
	return best, found
}

// Check implements ReplayChecker.
func (c *HTTPReplayChecker) Check(ctx context.Context, b SessionBundle, rec AuthRecording, varying []string) (bool, string, error) {
	cand, ok := pickCandidate(rec)
	if !ok {
		return false, "no_authenticated_xhr_captured", nil
	}
	u, err := url.Parse(cand.Request.URL)
	if err != nil {
		return false, "bad_url", err
	}
	if c.Client.Jar != nil {
		var cookies []*http.Cookie
		for _, ck := range b.Cookies {
			cookies = append(cookies, &http.Cookie{Name: ck.Name, Value: ck.Value, Path: ck.Path, Domain: ck.Domain})
		}
		c.Client.Jar.SetCookies(u, cookies)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cand.Request.URL, nil)
	if err != nil {
		return false, "bad_request", err
	}
	req.Header.Set("User-Agent", b.UserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	for k, v := range b.AuthHeaders {
		req.Header.Set(k, v)
	}
	// Also carry the exact headers the browser sent on this request (minus cookie/UA).
	for _, h := range cand.Request.Headers {
		n := strings.ToLower(h.Name)
		if n == "cookie" || n == "user-agent" || strings.HasPrefix(n, ":") || n == "host" || n == "content-length" {
			continue
		}
		if req.Header.Get(h.Name) == "" {
			req.Header.Set(h.Name, h.Value)
		}
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return false, "network_error", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != cand.Response.Status {
		return false, inferReason(resp.StatusCode, b, varying), nil
	}
	if !bodiesEquivalent(cand.Response.Body, string(body)) {
		return false, "body_mismatch", nil
	}
	return true, "", nil
}

func inferReason(status int, b SessionBundle, varying []string) string {
	switch {
	case len(varying) > 0:
		return fmt.Sprintf("js_computed_headers(%s)", strings.Join(varying, ","))
	case b.Proxy != "":
		return "ip_bound_or_proxy"
	case status == 401 || status == 403:
		return "rejected_outside_browser"
	default:
		return fmt.Sprintf("status_mismatch_%d", status)
	}
}

// bodiesEquivalent compares JSON structurally when possible, else trims.
func bodiesEquivalent(a, b string) bool {
	var ja, jb any
	if json.Unmarshal([]byte(a), &ja) == nil && json.Unmarshal([]byte(b), &jb) == nil {
		return reflect.DeepEqual(ja, jb)
	}
	return strings.TrimSpace(a) == strings.TrimSpace(b)
}
