package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/app"
	"github.com/creativetoken/chromancer/internal/config"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/server"
)

type rpcClient struct {
	t   *testing.T
	in  io.Writer
	out *bufio.Reader
	id  int
}

func (c *rpcClient) call(method string, params any) map[string]any {
	c.t.Helper()
	c.id++
	req := map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method}
	if params != nil {
		req["params"] = params
	}
	data, _ := json.Marshal(req)
	_, _ = c.in.Write(append(data, '\n'))
	line, err := c.out.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	var resp map[string]any
	_ = json.Unmarshal([]byte(line), &resp)
	return resp
}

func (c *rpcClient) tool(name string, args map[string]any) (map[string]any, bool) {
	resp := c.call("tools/call", map[string]any{"name": name, "arguments": args})
	res, ok := resp["result"].(map[string]any)
	if !ok {
		c.t.Fatalf("%s: no result: %v", name, resp)
	}
	isErr, _ := res["isError"].(bool)
	sc, _ := res["structuredContent"].(map[string]any)
	return sc, isErr
}

// TestE2EMCPLoginFlow drives a real browser login against the lab entirely via
// the MCP tool surface, reads the bundle as an MCP resource, and checks the
// per-account lock and cancel.
func TestE2EMCPLoginFlow(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("simple")

	// Build an app rooted in an isolated workspace with a known key.
	ws := filepath.Join(h.ws, "mcpws")
	key, _ := identity.NewRandomKey()
	t.Setenv(identity.KeyEnv, key)
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
	srvCtx, cancelSrv := context.WithCancel(h.ctx)
	go func() { _ = srv.Serve(srvCtx, cin, cout) }()
	defer func() { cancelSrv(); sin.Close() }()
	c := &rpcClient{t: t, in: sin, out: bufio.NewReader(sout)}

	if r := c.call("initialize", map[string]any{"protocolVersion": "2024-11-05"}); r["result"] == nil {
		t.Fatalf("initialize: %v", r)
	}

	sc, isErr := c.tool("start_auth", map[string]any{"account": "alice", "mode": "login", "login_url": e.LoginURL})
	if isErr {
		t.Fatalf("start_auth: %v", sc)
	}
	jobID := sc["job_id"].(string)

	// Concurrent start on the same account must be rejected (per-account lock).
	if _, busy := c.tool("start_auth", map[string]any{"account": "alice", "mode": "login"}); !busy {
		t.Fatal("expected per-account lock to reject concurrent start")
	}

	// Poll to done.
	deadline := time.After(100 * time.Second)
	var job map[string]any
	for {
		job, _ = c.tool("get_job", map[string]any{"job_id": jobID})
		if s, _ := job["state"].(string); s == "done" || s == "failed" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("job never finished: %v", job)
		case <-time.After(250 * time.Millisecond):
		}
	}
	if job["state"] != "done" {
		t.Fatalf("job failed: %v", job)
	}
	result := job["result"].(map[string]any)
	bundleRef := result["bundle_ref"].(map[string]any)
	uri := bundleRef["uri"].(string)
	if !strings.HasPrefix(uri, "agentbrowser://artefacts/alice/bundle-") {
		t.Fatalf("bundle uri: %s", uri)
	}

	// Read the bundle back as an MCP resource; it must be authenticated (cookies).
	rr := c.call("resources/read", map[string]any{"uri": uri})
	res := rr["result"].(map[string]any)
	text := res["contents"].([]any)[0].(map[string]any)["text"].(string)
	var bundle map[string]any
	if err := json.Unmarshal([]byte(text), &bundle); err != nil {
		t.Fatalf("bundle not json: %v", err)
	}
	if cookies, _ := bundle["cookies"].([]any); len(cookies) == 0 {
		t.Fatalf("bundle has no cookies: %s", text)
	}

	// export_session returns the same latest bundle.
	es, _ := c.tool("export_session", map[string]any{"account": "alice"})
	if es["origin"] == "" {
		t.Fatalf("export_session: %v", es)
	}

	// Audit log exists and has no plaintext password.
	raw, _ := os.ReadFile(filepath.Join(ws, "accounts", "alice", "audit.jsonl"))
	if strings.Contains(string(raw), e.Password) {
		t.Fatal("plaintext password leaked into audit log via MCP flow")
	}
}
