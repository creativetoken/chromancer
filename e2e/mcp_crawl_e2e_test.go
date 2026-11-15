package e2e

import (
	"bufio"
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/app"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/config"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/server"
)

// TestE2EMCPCrawl drives login then start_crawl over MCP and checks the crawl
// job completes with a report.
func TestE2EMCPCrawl(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("crawl")

	ws := filepath.Join(h.ws, "mcpcrawl")
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
	if err := a.Creds.PutCredentials(context.Background(), e.Root, "alice", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}); err != nil {
		t.Fatal(err)
	}
	// Produce a saved session first (direct login).
	if r := a.RunAuth(context.Background(), "alice", auth.Login, auth.Hints{LoginURL: e.LoginURL}, nil); r.Err != nil {
		t.Fatalf("login: %v", r.Err)
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

	sc, isErr := c.tool("start_crawl", map[string]any{"account": "alice"})
	if isErr {
		t.Fatalf("start_crawl: %v", sc)
	}
	jobID := sc["job_id"].(string)

	deadline := time.After(200 * time.Second)
	for {
		job, _ := c.tool("get_job", map[string]any{"job_id": jobID})
		switch job["state"] {
		case "done":
			crawl, _ := job["crawl"].(map[string]any)
			rep, _ := crawl["report"].(map[string]any)
			if rep == nil || rep["states_visited"] == nil {
				t.Fatalf("crawl job done but no report: %v", job)
			}
			return
		case "failed":
			t.Fatalf("crawl job failed: %v", job)
		}
		select {
		case <-deadline:
			t.Fatal("crawl job never finished")
		case <-time.After(400 * time.Millisecond):
		}
	}
}
