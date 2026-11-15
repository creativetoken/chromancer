// Command captcha-generic is an out-of-process CAPTCHA solver plugin: a generic
// adapter for the common token-solving service API shape (create task → poll
// result → report correct/incorrect). It is configured entirely by environment
// (endpoint, API key, supported vendors, timeout, poll interval, price) so one
// binary fronts any such service. It only ever receives a Challenge — never the
// page — and speaks the host's stdio-JSON plugin protocol.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/creativetoken/chromancer/internal/captcha"
)

// Config configures the generic adapter.
type Config struct {
	Endpoint   string           // base URL of the solving service
	ClientKey  string           // API key (from the configured env var)
	Vendors    []captcha.Vendor // vendors this service solves
	Kinds      []captcha.Kind   // challenge kinds (default interactive+score)
	Timeout    time.Duration    // overall per-solve budget
	PollEvery  time.Duration    // result poll interval
	PriceUSD   float64          // price per solve (recorded into Budget)
	NeedsProxy bool             // service needs the caller's proxy/UA
}

// GenericSolver implements captcha.Solver against a token-solving service.
type GenericSolver struct {
	cfg  Config
	http *http.Client

	mu     sync.Mutex
	taskOf map[string]string // solveID -> service taskId (for Report)
}

// NewGeneric builds a GenericSolver.
func NewGeneric(cfg Config) *GenericSolver {
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	if cfg.PollEvery == 0 {
		cfg.PollEvery = 3 * time.Second
	}
	if len(cfg.Kinds) == 0 {
		cfg.Kinds = []captcha.Kind{captcha.KindInteractive, captcha.KindScore}
	}
	return &GenericSolver{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}, taskOf: map[string]string{}}
}

// Name implements captcha.Solver.
func (g *GenericSolver) Name() string { return "external:generic" }

// Describe implements captcha.Solver.
func (g *GenericSolver) Describe() captcha.Capabilities {
	return captcha.Capabilities{
		Vendors:        g.cfg.Vendors,
		Kinds:          g.cfg.Kinds,
		NeedsProxy:     g.cfg.NeedsProxy,
		EgressExternal: true,
		EstCostUSD:     g.cfg.PriceUSD,
		EstLatencyMS:   10000,
	}
}

type createReq struct {
	ClientKey string   `json:"clientKey"`
	Task      taskSpec `json:"task"`
}

type taskSpec struct {
	Type         string `json:"type"`
	Vendor       string `json:"vendor"`
	WebsiteURL   string `json:"websiteURL"`
	WebsiteKey   string `json:"websiteKey"`
	Action       string `json:"action,omitempty"`
	IsEnterprise bool   `json:"isEnterprise,omitempty"`
	UserAgent    string `json:"userAgent,omitempty"`
	Proxy        string `json:"proxy,omitempty"`
}

type createResp struct {
	TaskID string `json:"taskId"`
	Error  string `json:"error,omitempty"`
}

type resultReq struct {
	ClientKey string `json:"clientKey"`
	TaskID    string `json:"taskId"`
}

type resultResp struct {
	Status   string `json:"status"` // processing | ready | failed
	Solution struct {
		Token string `json:"token"`
	} `json:"solution"`
	Error string `json:"error,omitempty"`
}

// Solve implements captcha.Solver: create a task, poll for the token.
func (g *GenericSolver) Solve(ctx context.Context, c captcha.Challenge) (captcha.Result, error) {
	if !g.supports(c.Vendor) {
		return captcha.Result{}, captcha.ErrUnsupported
	}
	if g.cfg.Endpoint == "" {
		return captcha.Result{}, fmt.Errorf("generic: no endpoint configured")
	}
	ctx, cancel := context.WithTimeout(ctx, g.cfg.Timeout)
	defer cancel()

	spec := taskSpec{
		Type:         "token",
		Vendor:       string(c.Vendor),
		WebsiteURL:   c.PageURL,
		WebsiteKey:   c.SiteKey,
		Action:       c.Params[captcha.ParamAction],
		IsEnterprise: c.Params[captcha.ParamEnterprise] == "true",
		UserAgent:    c.Params[captcha.ParamUserAgent],
		Proxy:        c.Params[captcha.ParamProxy],
	}
	var cr createResp
	if err := g.post(ctx, "/createTask", createReq{ClientKey: g.cfg.ClientKey, Task: spec}, &cr); err != nil {
		return captcha.Result{}, err
	}
	if cr.Error != "" || cr.TaskID == "" {
		return captcha.Result{}, fmt.Errorf("generic: createTask: %s", firstNonEmpty(cr.Error, "no taskId"))
	}

	id := c.Params[captcha.ParamSolveID]
	for {
		var rr resultResp
		if err := g.post(ctx, "/getTaskResult", resultReq{ClientKey: g.cfg.ClientKey, TaskID: cr.TaskID}, &rr); err != nil {
			return captcha.Result{}, err
		}
		switch rr.Status {
		case "ready":
			if rr.Solution.Token == "" {
				return captcha.Result{}, captcha.ErrDeclined
			}
			g.mu.Lock()
			g.taskOf[idOr(id, cr.TaskID)] = cr.TaskID
			g.mu.Unlock()
			return captcha.Result{Kind: captcha.ResultToken, Token: rr.Solution.Token, SolveID: idOr(id, cr.TaskID)}, nil
		case "failed":
			return captcha.Result{}, fmt.Errorf("generic: task failed: %s", rr.Error)
		default: // processing
		}
		select {
		case <-ctx.Done():
			return captcha.Result{}, ctx.Err()
		case <-time.After(g.cfg.PollEvery):
		}
	}
}

// Report implements captcha.Solver: tell the service whether the token worked.
func (g *GenericSolver) Report(ctx context.Context, solveID string, accepted bool) error {
	g.mu.Lock()
	task := g.taskOf[solveID]
	delete(g.taskOf, solveID)
	g.mu.Unlock()
	if task == "" || g.cfg.Endpoint == "" {
		return nil
	}
	path := "/reportIncorrect"
	if accepted {
		path = "/reportCorrect"
	}
	var ignore map[string]any
	return g.post(ctx, path, resultReq{ClientKey: g.cfg.ClientKey, TaskID: task}, &ignore)
}

func (g *GenericSolver) supports(v captcha.Vendor) bool {
	for _, x := range g.cfg.Vendors {
		if x == v {
			return true
		}
	}
	return false
}

func (g *GenericSolver) post(ctx context.Context, path string, body, out any) error {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(g.cfg.Endpoint, "/")+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("generic: %s: http %d", path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func idOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
