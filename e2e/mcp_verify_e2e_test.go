package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/app"
	"github.com/creativetoken/chromancer/internal/config"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/lab"
	"github.com/creativetoken/chromancer/internal/server"
)

// TestE2EMCPSignupVerify drives signup+email-OTP over MCP: the job surfaces
// needs_verification, the test (acting as the harness) reads the lab inbox and
// answers via resolve_challenge, and the job completes done.
func TestE2EMCPSignupVerify(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("signup-verify")

	ws := filepath.Join(h.ws, "mcpverify")
	key, _ := identity.NewRandomKey()
	t.Setenv(identity.KeyEnv, key)
	cfg := config.Default()
	cfg.Workspace = ws
	cfg.Headless = true
	a, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Account id == the email, so the lab inbox (addressed by email) matches.
	acct := identity.AccountID(e.Username)
	if err := a.Creds.PutCredentials(context.Background(), e.Root, acct, identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}); err != nil {
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

	sc, isErr := c.tool("start_auth", map[string]any{"account": string(acct), "mode": "signup", "signup_url": e.SignupURL})
	if isErr {
		t.Fatalf("start_auth: %v", sc)
	}
	jobID := sc["job_id"].(string)

	deadline := time.After(100 * time.Second)
	answered := false
	for {
		job, _ := c.tool("get_job", map[string]any{"job_id": jobID})
		state, _ := job["state"].(string)
		switch state {
		case "needs_verification":
			if !answered {
				pend := job["pending"].(map[string]any)
				chalID := pend["challenge_id"].(string)
				// Read the code from the lab inbox (the operator's "tool").
				code := readInboxCode(t, h.ts.URL, e.Username)
				if _, e2 := c.tool("resolve_challenge", map[string]any{"job_id": jobID, "challenge_id": chalID, "code": code}); e2 {
					t.Fatal("resolve_challenge errored")
				}
				answered = true
			}
		case "done":
			if !answered {
				t.Fatal("job completed without surfacing verification")
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

func readInboxCode(t *testing.T, base, to string) string {
	t.Helper()
	// Poll the lab inbox until a code arrives.
	for i := 0; i < 40; i++ {
		resp := httpGet(t, base+"/__lab/inbox?to="+to)
		var msgs []lab.Message
		if json.Unmarshal(resp, &msgs) == nil {
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].Code != "" {
					return msgs[i].Code
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatal("no code in inbox")
	return ""
}
