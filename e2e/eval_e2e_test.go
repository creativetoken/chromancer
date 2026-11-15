package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creativetoken/chromancer/internal/app"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/config"
	"github.com/creativetoken/chromancer/internal/eval"
	"github.com/creativetoken/chromancer/internal/identity"
)

// TestE2EEvalHarness runs the benchmark harness over a few scenarios with the
// mock model and checks it reports a table with passing results.
func TestE2EEvalHarness(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)

	ws := filepath.Join(h.ws, "evalws")
	key, _ := identity.NewRandomKey()
	t.Setenv(identity.KeyEnv, key)
	t.Setenv("AGENTBROWSER_LLM", "mock")
	cfg := config.Default()
	cfg.Workspace = ws
	cfg.Headless = true
	a, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	hw := &eval.Harness{App: a, Lab: h.lab, LabBase: h.ts.URL, ModelName: "mock", Runs: 1}
	scenarios := []eval.Scenario{
		{App: "simple", Mode: auth.Login},
		{App: "multistep", Mode: auth.Login},
		{App: "totp", Mode: auth.Login, Hint: true},
		{App: "signup-verify", Mode: auth.Signup, Hint: true},
	}
	rows, err := hw.Run(context.Background(), scenarios)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(scenarios) {
		t.Fatalf("expected %d rows, got %d", len(scenarios), len(rows))
	}
	for _, r := range rows {
		if r.Successes != r.Runs {
			t.Errorf("scenario %s failed: %d/%d (%s)", r.Scenario, r.Successes, r.Runs, r.LastError)
		}
		if r.AvgMillis <= 0 {
			t.Errorf("scenario %s has no timing", r.Scenario)
		}
	}
	table := eval.Table(rows)
	if !strings.Contains(table, "SCENARIO") || !strings.Contains(table, "simple/login") {
		t.Fatalf("table malformed:\n%s", table)
	}
	t.Logf("\n%s", table)
}
