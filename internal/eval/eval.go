// Package eval is a reproducible benchmark harness: it runs the lab auth
// scenarios for a configured model and reports success rate, steps, tokens,
// cost and time so models and prompts can be compared on the same tasks.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/creativetoken/chromancer/internal/app"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/lab"
	"github.com/creativetoken/chromancer/internal/model"
)

// Scenario is one benchmark task against a lab app.
type Scenario struct {
	App  string
	Mode auth.Mode
	Hint bool // pass the entrypoint hint
}

// DefaultScenarios covers the auth surface across the lab.
func DefaultScenarios() []Scenario {
	return []Scenario{
		{App: "simple", Mode: auth.Login},
		{App: "multistep", Mode: auth.Login},
		{App: "modal", Mode: auth.Login},
		{App: "iframe", Mode: auth.Login},
		{App: "spa-jwt", Mode: auth.Login},
		{App: "hidden-entry", Mode: auth.Login, Hint: true},
		{App: "bound-session", Mode: auth.Login, Hint: true},
		{App: "form-errors", Mode: auth.Login, Hint: true},
		{App: "totp", Mode: auth.Login, Hint: true},
		{App: "otp-sms", Mode: auth.Login, Hint: true},
		{App: "captcha-mock", Mode: auth.Login, Hint: true},
		{App: "signup-verify", Mode: auth.Signup, Hint: true},
	}
}

// Row is one scenario's aggregated result for a model.
type Row struct {
	Scenario   string  `json:"scenario"`
	Model      string  `json:"model"`
	Runs       int     `json:"runs"`
	Successes  int     `json:"successes"`
	AvgMillis  int64   `json:"avg_ms"`
	AvgSteps   float64 `json:"avg_steps"`
	AvgTokens  float64 `json:"avg_tokens"`
	AvgCostUSD float64 `json:"avg_cost_usd"`
	LastError  string  `json:"last_error,omitempty"`
}

// SuccessRate is successes/runs.
func (r Row) SuccessRate() float64 {
	if r.Runs == 0 {
		return 0
	}
	return float64(r.Successes) / float64(r.Runs)
}

// CountingProvider wraps a provider and accumulates token/cost usage.
type CountingProvider struct {
	model.LLMProvider
	mu   sync.Mutex
	in   int
	out  int
	cost float64
}

// Chat implements model.LLMProvider.
func (c *CountingProvider) Chat(ctx context.Context, msgs []model.Message, opts model.ChatOpts) (*model.Completion, error) {
	r, err := c.LLMProvider.Chat(ctx, msgs, opts)
	if r != nil {
		c.add(r.Usage)
	}
	return r, err
}

// Structured implements model.LLMProvider.
func (c *CountingProvider) Structured(ctx context.Context, msgs []model.Message, schema json.RawMessage, opts model.ChatOpts) (json.RawMessage, *model.Usage, error) {
	raw, u, err := c.LLMProvider.Structured(ctx, msgs, schema, opts)
	if u != nil {
		c.add(*u)
	}
	return raw, u, err
}

func (c *CountingProvider) add(u model.Usage) {
	c.mu.Lock()
	c.in += u.InputTokens
	c.out += u.OutputTokens
	c.cost += u.CostUSD
	c.mu.Unlock()
}

func (c *CountingProvider) snapshot() (tokens int, cost float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.in + c.out, c.cost
}

func (c *CountingProvider) reset() {
	c.mu.Lock()
	c.in, c.out, c.cost = 0, 0, 0
	c.mu.Unlock()
}

// Harness runs scenarios against a live lab using an app.
type Harness struct {
	App       *app.App
	Lab       *lab.Server
	LabBase   string
	ModelName string
	Counter   *CountingProvider // optional; enables token/cost columns
	Runs      int
}

// Run executes every scenario Runs times and returns aggregated rows. It seeds
// credentials for each app from the lab's ground truth.
func (h *Harness) Run(ctx context.Context, scenarios []Scenario) ([]Row, error) {
	runs := h.Runs
	if runs == 0 {
		runs = 1
	}
	var rows []Row
	for _, sc := range scenarios {
		e, ok := h.Lab.ExpectFor(sc.App)
		if !ok {
			continue
		}
		row := Row{Scenario: sc.App + "/" + sc.Mode.String(), Model: h.ModelName, Runs: runs}
		for i := 0; i < runs; i++ {
			h.Lab.Reset()
			account := fmt.Sprintf("%s-%d", sc.App, i)
			creds := identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}
			if sc.App == "totp" {
				creds.TOTPSeed = lab.TOTPSeed
			}
			_ = h.App.Creds.PutCredentials(ctx, e.Root, identity.AccountID(account), creds)
			hints := auth.Hints{}
			if sc.Hint {
				hints = auth.Hints{LoginURL: e.LoginURL, SignupURL: e.SignupURL}
			}
			if h.Counter != nil {
				h.Counter.reset()
			}
			start := time.Now()
			res := h.App.RunAuth(ctx, account, sc.Mode, hints, h.verifierFor(sc, e))
			elapsed := time.Since(start).Milliseconds()
			row.AvgMillis += elapsed
			if res.Err == nil {
				row.Successes++
				row.AvgSteps += float64(len(res.Result.Trace.Steps))
			} else {
				row.LastError = res.Err.Error()
			}
			if h.Counter != nil {
				tok, cost := h.Counter.snapshot()
				row.AvgTokens += float64(tok)
				row.AvgCostUSD += cost
			}
		}
		if runs > 0 {
			row.AvgMillis /= int64(runs)
			row.AvgTokens /= float64(runs)
			row.AvgCostUSD /= float64(runs)
			if row.Successes > 0 {
				row.AvgSteps /= float64(row.Successes)
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// verifierFor wires a labinbox/TOTP asker so OTP/magic-link/TOTP scenarios can
// complete offline. Non-verification scenarios get a nil asker.
func (h *Harness) verifierFor(sc Scenario, e lab.Expect) app.Asker {
	// The app builds TOTP from creds and a CLI/ask provider otherwise. For the
	// eval harness we inject a lab-inbox-backed asker.
	return &labAsker{base: h.LabBase, app: sc.App}
}

// Table renders rows as an aligned text table.
func Table(rows []Row) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-26s %-22s %6s %8s %7s %8s %10s\n", "SCENARIO", "MODEL", "OK%", "avg_ms", "steps", "tokens", "cost_usd")
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Scenario < rows[j].Scenario })
	for _, r := range rows {
		fmt.Fprintf(&b, "%-26s %-22s %5.0f%% %8d %7.1f %8.0f %10.4f\n",
			r.Scenario, r.Model, r.SuccessRate()*100, r.AvgMillis, r.AvgSteps, r.AvgTokens, r.AvgCostUSD)
	}
	return b.String()
}
