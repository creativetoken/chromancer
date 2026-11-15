package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/captcha"
	"github.com/creativetoken/chromancer/internal/captcha/plugin"
)

// mockService is a local create/poll/report service (never a real one).
type mockService struct {
	mu          sync.Mutex
	pollsBefore int // replies "processing" this many times before "ready"
	polls       int
	lastTask    taskSpec
	reports     []string // "/reportCorrect" or "/reportIncorrect"
}

func (m *mockService) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/createTask", func(w http.ResponseWriter, r *http.Request) {
		var req createReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		m.mu.Lock()
		m.lastTask = req.Task
		m.mu.Unlock()
		writeJSON(w, createResp{TaskID: "task-123"})
	})
	mux.HandleFunc("/getTaskResult", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.polls++
		ready := m.polls > m.pollsBefore
		m.mu.Unlock()
		if !ready {
			writeJSON(w, resultResp{Status: "processing"})
			return
		}
		var rr resultResp
		rr.Status = "ready"
		rr.Solution.Token = "SERVICE-SOLVED-TOKEN-abcdef"
		writeJSON(w, rr)
	})
	for _, p := range []string{"/reportCorrect", "/reportIncorrect"} {
		p := p
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			m.mu.Lock()
			m.reports = append(m.reports, p)
			m.mu.Unlock()
			writeJSON(w, map[string]string{"status": "ok"})
		})
	}
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newSolver(endpoint string) *GenericSolver {
	return NewGeneric(Config{
		Endpoint: endpoint, ClientKey: "test-key",
		Vendors:   []captcha.Vendor{captcha.VendorRecaptchaV2, captcha.VendorHCaptcha},
		PollEvery: 5 * time.Millisecond, PriceUSD: 0.002, NeedsProxy: true,
	})
}

func TestGenericSolveCreatePollReport(t *testing.T) {
	ms := &mockService{pollsBefore: 2}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()
	g := newSolver(srv.URL)

	ch := captcha.Challenge{Vendor: captcha.VendorRecaptchaV2, Kind: captcha.KindInteractive,
		SiteKey: "6Lc_real", PageURL: "https://app.test/login",
		Params: map[string]string{
			captcha.ParamSolveID: "s1", captcha.ParamAction: "login",
			captcha.ParamUserAgent: "Mozilla/5.0 test", captcha.ParamProxy: "http://127.0.0.1:8080",
		}}
	res, err := g.Solve(context.Background(), ch)
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if res.Kind != captcha.ResultToken || res.Token != "SERVICE-SOLVED-TOKEN-abcdef" {
		t.Fatalf("result = %+v", res)
	}
	// The adapter forwarded sitekey, URL, action, UA and proxy.
	ms.mu.Lock()
	task := ms.lastTask
	ms.mu.Unlock()
	if task.WebsiteKey != "6Lc_real" || task.WebsiteURL != "https://app.test/login" ||
		task.Action != "login" || task.UserAgent != "Mozilla/5.0 test" || task.Proxy != "http://127.0.0.1:8080" {
		t.Fatalf("task not fully forwarded: %+v", task)
	}
	// Report maps to the service's report endpoint.
	if err := g.Report(context.Background(), res.SolveID, true); err != nil {
		t.Fatalf("report: %v", err)
	}
	ms.mu.Lock()
	reports := ms.reports
	ms.mu.Unlock()
	if len(reports) != 1 || reports[0] != "/reportCorrect" {
		t.Fatalf("reports = %v, want one /reportCorrect", reports)
	}
}

func TestGenericUnsupportedVendorDeclines(t *testing.T) {
	g := newSolver("http://127.0.0.1:0")
	if _, err := g.Solve(context.Background(), captcha.Challenge{Vendor: captcha.VendorArkose}); err != captcha.ErrUnsupported {
		t.Fatalf("arkose: err = %v, want ErrUnsupported", err)
	}
}

func TestGenericConformance(t *testing.T) {
	// A service that is always immediately ready, so conformance's single solve
	// returns a token (no real network).
	ms := &mockService{pollsBefore: 0}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()
	plugin.Conform(t, func() captcha.Solver { return newSolver(srv.URL) })
}
