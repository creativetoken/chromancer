package handback

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
)

// CaptureWindow bounds the post-login recording: after success detection the
// agent loads the landing page once, then records until N authenticated
// requests OR T seconds, whichever first.
type CaptureWindow struct {
	N int
	T time.Duration
}

// HARHeader is a name/value pair with a secret flag.
type HARHeader struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
}

// HAREntry is one request/response pair (HAR-like, with secrets flagged).
type HAREntry struct {
	StartedAt time.Time `json:"startedDateTime"`
	Phase     string    `json:"phase"` // auth | post_login
	Request   struct {
		Method   string      `json:"method"`
		URL      string      `json:"url"`
		Headers  []HARHeader `json:"headers"`
		PostData string      `json:"postData,omitempty"`
		Type     string      `json:"resourceType"`
	} `json:"request"`
	Response struct {
		Status   int         `json:"status"`
		Headers  []HARHeader `json:"headers"`
		MimeType string      `json:"mimeType"`
		Body     string      `json:"body,omitempty"`
		Base64   bool        `json:"base64,omitempty"`
	} `json:"response"`
	SecretFields []string `json:"secretFields,omitempty"` // dotted paths / names of flagged secrets in bodies
}

// AuthRecording is the HAR-style artefact with secrets flagged.
type AuthRecording struct {
	Format    string     `json:"format"` // har-1.2-json (agentbrowser flavour)
	StartedAt time.Time  `json:"started_at"`
	EndedAt   time.Time  `json:"ended_at"`
	Entries   []HAREntry `json:"entries"`
	Note      string     `json:"note"`
}

// AuthRecorder captures the auth flow as HAR-style JSON: every
// request/response during signup/login PLUS a bounded post-login capture
// window. It records the AUTH FLOW ONLY — not a general traffic engine.
type AuthRecorder interface {
	Begin(ctx context.Context, p browser.Page) error
	MarkAuthenticated(ctx context.Context, w CaptureWindow) error
	// WaitWindow blocks until the post-login window is satisfied (N requests or T).
	WaitWindow(ctx context.Context)
	Finalize(ctx context.Context) (AuthRecording, error)
	// AuthHeaders returns auth-relevant request headers observed on post-login
	// XHR/fetch (Authorization, CSRF, custom X- headers), lower-cased names.
	AuthHeaders() map[string]string
	// PostLoginXHRs returns post-login XHR/fetch requests paired with responses.
	PostLoginXHRs() []HAREntry
	// VaryingHeaders returns request header names whose values differed across
	// post-login XHRs — a signal for JS-computed per-request headers.
	VaryingHeaders() []string
	// TokenSubmitted reports whether a captcha token/answer value was carried in
	// an outgoing request (post body or header) — proof the injected solve
	// actually reached the backend before judging acceptance.
	TokenSubmitted(value string) bool
}

var secretHeaderRe = regexp.MustCompile(`(?i)^(authorization|cookie|set-cookie|x-csrf-token|x-xsrf-token|x-api-key|x-auth-token|proxy-authorization)$`)
var secretBodyKeyRe = regexp.MustCompile(`(?i)(pass(word|wd)?|secret|token|otp|code|csrf|api[-_]?key|seed)`)
var authHeaderRe = regexp.MustCompile(`(?i)^(authorization|x-csrf-token|x-xsrf-token|x-api-key|x-auth-token|x-requested-with|x-[a-z0-9-]+)$`)

// Recorder is the default AuthRecorder.
type Recorder struct {
	mu        sync.Mutex
	rec       AuthRecording
	reqs      map[string]browser.RequestEvent
	phase     string
	cancel    func()
	window    CaptureWindow
	windowAt  time.Time
	postCount int
	windowCh  chan struct{}
	closed    bool
	skipTypes map[string]bool
}

// NewRecorder creates a Recorder.
func NewRecorder() *Recorder {
	return &Recorder{
		reqs:      map[string]browser.RequestEvent{},
		phase:     "auth",
		windowCh:  make(chan struct{}),
		skipTypes: map[string]bool{"Image": true, "Font": true, "Stylesheet": true, "Media": true, "Ping": true},
	}
}

// Begin implements AuthRecorder.
func (r *Recorder) Begin(ctx context.Context, p browser.Page) error {
	r.mu.Lock()
	r.rec = AuthRecording{Format: "har-1.2-json", StartedAt: time.Now().UTC(),
		Note: "auth flow + bounded post-login window; secrets flagged, not removed"}
	r.mu.Unlock()
	r.cancel = p.Subscribe(ctx, r)
	return nil
}

// OnRequest implements browser.NetworkSink.
func (r *Recorder) OnRequest(e browser.RequestEvent) {
	if r.skipTypes[e.Type] {
		return
	}
	r.mu.Lock()
	r.reqs[e.ID] = e
	r.mu.Unlock()
}

// OnResponse implements browser.NetworkSink.
func (r *Recorder) OnResponse(e browser.ResponseEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	req, ok := r.reqs[e.ID]
	if !ok {
		return
	}
	delete(r.reqs, e.ID)
	var h HAREntry
	h.StartedAt = req.At
	h.Phase = r.phase
	h.Request.Method = req.Method
	h.Request.URL = req.URL
	h.Request.Type = req.Type
	h.Request.PostData = req.PostData
	h.Request.Headers = flagHeaders(req.Headers)
	h.Response.Status = e.Status
	h.Response.Headers = flagHeaders(e.Headers)
	h.Response.MimeType = e.MimeType
	h.Response.Body = e.Body
	h.Response.Base64 = e.Base64
	h.SecretFields = append(secretFieldsIn("postData", req.PostData), secretFieldsIn("body", e.Body)...)
	r.rec.Entries = append(r.rec.Entries, h)
	if r.phase == "post_login" && isXHR(req.Type) && e.Status > 0 {
		r.postCount++
		if r.window.N > 0 && r.postCount >= r.window.N && !r.closed {
			r.closed = true
			close(r.windowCh)
		}
	}
}

// MarkAuthenticated implements AuthRecorder.
func (r *Recorder) MarkAuthenticated(_ context.Context, w CaptureWindow) error {
	r.mu.Lock()
	r.phase = "post_login"
	r.window = w
	r.windowAt = time.Now()
	r.mu.Unlock()
	return nil
}

// WaitWindow implements AuthRecorder.
func (r *Recorder) WaitWindow(ctx context.Context) {
	r.mu.Lock()
	t := r.window.T
	r.mu.Unlock()
	if t <= 0 {
		t = 10 * time.Second
	}
	select {
	case <-r.windowCh:
	case <-time.After(t):
	case <-ctx.Done():
	}
}

// Finalize implements AuthRecorder.
func (r *Recorder) Finalize(_ context.Context) (AuthRecording, error) {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.EndedAt = time.Now().UTC()
	out := r.rec
	out.Entries = append([]HAREntry(nil), r.rec.Entries...)
	return out, nil
}

// AuthHeaders implements AuthRecorder. It reads auth-relevant request headers
// (Authorization, CSRF, custom X-) from post-login XHR/fetch, falling back to
// any-phase XHR/fetch when the post-login window captured none (e.g. the first
// authenticated call fired during the auth phase).
func (r *Recorder) AuthHeaders() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	collect := func(postOnly bool) map[string]string {
		out := map[string]string{}
		for _, e := range r.rec.Entries {
			if !isXHR(e.Request.Type) {
				continue
			}
			if postOnly && e.Phase != "post_login" {
				continue
			}
			for _, h := range e.Request.Headers {
				n := strings.ToLower(h.Name)
				if authHeaderRe.MatchString(n) && n != "x-requested-with" {
					out[n] = h.Value // latest wins
				}
			}
		}
		return out
	}
	if out := collect(true); len(out) > 0 {
		return out
	}
	return collect(false)
}

// PostLoginXHRs implements AuthRecorder.
func (r *Recorder) PostLoginXHRs() []HAREntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []HAREntry
	for _, e := range r.rec.Entries {
		if e.Phase == "post_login" && isXHR(e.Request.Type) {
			out = append(out, e)
		}
	}
	return out
}

// VaryingHeaders implements AuthRecorder.
func (r *Recorder) VaryingHeaders() []string {
	xhrs := r.PostLoginXHRs()
	seen := map[string]map[string]bool{}
	for _, e := range xhrs {
		for _, h := range e.Request.Headers {
			n := strings.ToLower(h.Name)
			if !authHeaderRe.MatchString(n) || n == "x-requested-with" {
				continue
			}
			if seen[n] == nil {
				seen[n] = map[string]bool{}
			}
			seen[n][h.Value] = true
		}
	}
	var out []string
	for n, vals := range seen {
		if len(vals) > 1 {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// TokenSubmitted implements AuthRecorder: true if `value` appears in any
// captured request's post body or header values. A short value is ignored to
// avoid coincidental matches.
func (r *Recorder) TokenSubmitted(value string) bool {
	if len(value) < 8 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.rec.Entries {
		if strings.Contains(e.Request.PostData, value) {
			return true
		}
		for _, h := range e.Request.Headers {
			if strings.Contains(h.Value, value) {
				return true
			}
		}
	}
	return false
}

func isXHR(t string) bool { return t == "XHR" || t == "Fetch" }

func flagHeaders(h map[string]string) []HARHeader {
	out := make([]HARHeader, 0, len(h))
	for k, v := range h {
		out = append(out, HARHeader{Name: k, Value: v, Secret: secretHeaderRe.MatchString(k)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// secretFieldsIn lists JSON keys / form fields in body that look secret.
func secretFieldsIn(prefix, body string) []string {
	if body == "" {
		return nil
	}
	var out []string
	trim := strings.TrimSpace(body)
	if strings.HasPrefix(trim, "{") {
		var m map[string]any
		if json.Unmarshal([]byte(trim), &m) == nil {
			for k := range m {
				if secretBodyKeyRe.MatchString(k) {
					out = append(out, prefix+"."+k)
				}
			}
			sort.Strings(out)
			return out
		}
	}
	for _, kv := range strings.Split(trim, "&") {
		k := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			k = kv[:i]
		}
		if secretBodyKeyRe.MatchString(k) {
			out = append(out, prefix+"."+k)
		}
	}
	sort.Strings(out)
	return out
}
