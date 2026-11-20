package browser

import "time"

// Cookie is a browser cookie with its scoping flags.
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires,omitempty"` // unix seconds; <=0 = session cookie
	HTTPOnly bool    `json:"http_only"`
	Secure   bool    `json:"secure"`
	SameSite string  `json:"same_site,omitempty"`
}

// SessionState is the exportable/importable browser state for one origin.
type SessionState struct {
	Origin         string            `json:"origin"`
	Cookies        []Cookie          `json:"cookies"`
	LocalStorage   map[string]string `json:"local_storage"`
	SessionStorage map[string]string `json:"session_storage"`
	UserAgent      string            `json:"user_agent"`
	CapturedAt     time.Time         `json:"captured_at"`
}

// RequestEvent is one observed request (headers merged from the main event and
// the ExtraInfo event when available).
type RequestEvent struct {
	ID        string            `json:"id"`
	At        time.Time         `json:"at"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	PostData  string            `json:"post_data,omitempty"`
	Type      string            `json:"type"` // Document | XHR | Fetch | Script | …
	Initiator string            `json:"initiator,omitempty"`
}

// ResponseEvent is one observed response. Body is filled when available.
type ResponseEvent struct {
	ID       string            `json:"id"`
	At       time.Time         `json:"at"`
	URL      string            `json:"url"`
	Status   int               `json:"status"`
	Headers  map[string]string `json:"headers"`
	MimeType string            `json:"mime_type"`
	Body     string            `json:"body,omitempty"`
	Base64   bool              `json:"base64,omitempty"`
	Type     string            `json:"type"`
}

// NetworkSink receives network events from Page.Subscribe.
type NetworkSink interface {
	OnRequest(RequestEvent)
	OnResponse(ResponseEvent)
}
