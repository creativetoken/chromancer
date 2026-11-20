package rodbackend

import (
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod/lib/proto"

	"github.com/creativetoken/chromancer/internal/browser"
)

// Subscribe streams CDP network events for this page to sink until the returned
// cancel func is called or ctx ends. Response bodies are fetched for
// XHR/Fetch/Document responses (bounded size) when the loading finishes.
func (p *rodPage) Subscribe(ctx context.Context, sink browser.NetworkSink) (cancel func()) {
	sctx, stop := context.WithCancel(ctx)
	rp := p.rp.Context(sctx)
	var (
		mu       sync.Mutex
		pending  = map[proto.NetworkRequestID]*browser.ResponseEvent{}
		reqTypes = map[proto.NetworkRequestID]string{}
		extra    = map[proto.NetworkRequestID]map[string]string{}
	)
	headersOf := func(h proto.NetworkHeaders) map[string]string {
		out := make(map[string]string, len(h))
		for k, v := range h {
			out[strings.ToLower(k)] = v.Str()
		}
		return out
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		rp.EachEvent(
			func(e *proto.NetworkRequestWillBeSent) {
				ev := browser.RequestEvent{ID: string(e.RequestID), At: time.Now().UTC(), Method: e.Request.Method, URL: e.Request.URL,
					Headers: headersOf(e.Request.Headers), Type: string(e.Type)}
				if e.Request.HasPostData {
					ev.PostData = e.Request.PostData
				}
				if e.Initiator != nil {
					ev.Initiator = string(e.Initiator.Type)
				}
				mu.Lock()
				reqTypes[e.RequestID] = string(e.Type)
				if x, ok := extra[e.RequestID]; ok {
					for k, v := range x {
						if _, exists := ev.Headers[k]; !exists {
							ev.Headers[k] = v
						}
					}
					delete(extra, e.RequestID)
				}
				mu.Unlock()
				sink.OnRequest(ev)
			},
			func(e *proto.NetworkRequestWillBeSentExtraInfo) {
				// Carries the full header set (incl. Cookie). Stash for merge.
				mu.Lock()
				extra[e.RequestID] = headersOf(e.Headers)
				mu.Unlock()
			},
			func(e *proto.NetworkResponseReceived) {
				mu.Lock()
				pending[e.RequestID] = &browser.ResponseEvent{ID: string(e.RequestID), At: time.Now().UTC(), URL: e.Response.URL,
					Status: e.Response.Status, Headers: headersOf(e.Response.Headers), MimeType: e.Response.MIMEType, Type: string(e.Type)}
				mu.Unlock()
			},
			func(e *proto.NetworkLoadingFinished) {
				mu.Lock()
				ev, ok := pending[e.RequestID]
				delete(pending, e.RequestID)
				t := reqTypes[e.RequestID]
				delete(reqTypes, e.RequestID)
				mu.Unlock()
				if !ok {
					return
				}
				if wantBody(t, ev.MimeType) {
					if body, err := (proto.NetworkGetResponseBody{RequestID: e.RequestID}).Call(rp); err == nil {
						if body.Base64Encoded {
							if raw, derr := base64.StdEncoding.DecodeString(body.Body); derr == nil && len(raw) < 512*1024 {
								ev.Body = string(raw)
							} else {
								ev.Body = body.Body
								ev.Base64 = true
							}
						} else if len(body.Body) < 512*1024 {
							ev.Body = body.Body
						}
					}
				}
				sink.OnResponse(*ev)
			},
			func(e *proto.NetworkLoadingFailed) {
				mu.Lock()
				ev, ok := pending[e.RequestID]
				delete(pending, e.RequestID)
				mu.Unlock()
				if ok {
					sink.OnResponse(*ev)
				}
			},
		)()
	}()
	return func() {
		stop()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

func wantBody(reqType, mime string) bool {
	switch reqType {
	case "XHR", "Fetch", "Document":
		return true
	}
	return strings.Contains(mime, "json") || strings.Contains(mime, "text/html")
}
