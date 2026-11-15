package e2e

import (
	"bufio"
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/app"
	"github.com/creativetoken/chromancer/internal/config"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/server"
)

// TestE2EMCPCaptchaHumanHandoff drives captcha-block over MCP: the job surfaces
// needs_human, the test (as the operator) solves it in the lab and answers
// resolve_challenge(done), and the job completes.
func TestE2EMCPCaptchaHumanHandoff(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("captcha-block")

	ws := filepath.Join(h.ws, "mcphuman")
	key, _ := identity.NewRandomKey()
	t.Setenv(identity.KeyEnv, key)
	// testmode answers the lab sitekeys so the chain exhausts into a handoff.
	t.Setenv("AGENTBROWSER_LLM", "mock")
	cfg := config.Default()
	cfg.Workspace = ws
	cfg.Headless = true
	a, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Creds.PutCredentials(context.Background(), e.Root, "alice", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}); err != nil {
		t.Fatal(err)
	}
	jm := server.NewJobManager(a)
	srv := server.NewMCP(jm, a)
	cin, sin := io.Pipe()
	sout, cout := io.Pipe()
	sctx, cancel := context.WithCancel(h.ctx)
	go func() { _ = srv.Serve(sctx, cin, cout) }()
	defer func() { cancel(); sin.Close() }()
	c := &rpcClient{t: t, in: sin, out: bufio.NewReader(sout)}
	c.call("initialize", map[string]any{"protocolVersion": "2024-11-05"})

	sc, isErr := c.tool("start_auth", map[string]any{"account": "alice", "mode": "login", "login_url": e.LoginURL})
	if isErr {
		t.Fatalf("start_auth: %v", sc)
	}
	jobID := sc["job_id"].(string)

	deadline := time.After(120 * time.Second)
	answered := false
	for {
		job, _ := c.tool("get_job", map[string]any{"job_id": jobID})
		switch job["state"] {
		case "needs_human":
			if !answered {
				pend := job["pending"].(map[string]any)
				if pend["reason"] == "" {
					t.Fatalf("needs_human should carry a reason: %v", pend)
				}
				// Act as the operator: solve the block in the lab, then signal done.
				httpGet(t, h.ts.URL+"/captcha-block/__human-solve")
				if _, e2 := c.tool("resolve_challenge", map[string]any{"job_id": jobID, "challenge_id": pend["challenge_id"], "done": true}); e2 {
					t.Fatal("resolve_challenge(done) errored")
				}
				answered = true
			}
		case "done":
			if !answered {
				t.Fatal("completed without needs_human")
			}
			return
		case "failed":
			t.Fatalf("job failed: %v", job)
		}
		select {
		case <-deadline:
			t.Fatal("job never finished")
		case <-time.After(300 * time.Millisecond):
		}
	}
}
