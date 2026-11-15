package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/handback"
)

// mcpClient drives the MCP server over an in-memory pipe.
type mcpClient struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Reader
	id  int
}

func startMCP(t *testing.T, jm *JobManager, arts ArtefactReader) *mcpClient {
	t.Helper()
	cin, sin := io.Pipe()   // client writes sin-> server reads cin
	sout, cout := io.Pipe() // server writes cout -> client reads sout
	srv := NewMCP(jm, arts)
	go func() { _ = srv.Serve(context.Background(), cin, cout) }()
	c := &mcpClient{t: t, in: sin, out: bufio.NewReader(sout)}
	t.Cleanup(func() { sin.Close() })
	return c
}

func (c *mcpClient) call(method string, params any) map[string]any {
	c.t.Helper()
	c.id++
	req := map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method}
	if params != nil {
		req["params"] = params
	}
	data, _ := json.Marshal(req)
	if _, err := c.in.Write(append(data, '\n')); err != nil {
		c.t.Fatalf("write: %v", err)
	}
	line, err := c.out.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		c.t.Fatalf("decode %q: %v", line, err)
	}
	return resp
}

func (c *mcpClient) notify(method string, params any) {
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		req["params"] = params
	}
	data, _ := json.Marshal(req)
	_, _ = c.in.Write(append(data, '\n'))
}

// toolCall calls a tool and returns its structuredContent.
func (c *mcpClient) toolCall(name string, args map[string]any) (map[string]any, bool) {
	resp := c.call("tools/call", map[string]any{"name": name, "arguments": args})
	res, ok := resp["result"].(map[string]any)
	if !ok {
		c.t.Fatalf("tools/call %s no result: %v", name, resp)
	}
	isErr, _ := res["isError"].(bool)
	sc, _ := res["structuredContent"].(map[string]any)
	return sc, isErr
}

type fakeArts struct{ data string }

func (f fakeArts) ReadArtefactURI(uri string) ([]byte, string, error) {
	return []byte(f.data), "application/json", nil
}

func TestMCPInitializeAndToolsList(t *testing.T) {
	jm := NewJobManager(&fakeRunner{})
	c := startMCP(t, jm, fakeArts{})
	init := c.call("initialize", map[string]any{"protocolVersion": ProtocolVersion})
	res := init["result"].(map[string]any)
	if res["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocol: %v", res)
	}
	c.notify("notifications/initialized", nil)
	tl := c.call("tools/list", nil)
	tools := tl["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tt := range tools {
		names[tt.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"start_auth", "get_job", "resolve_challenge", "cancel_job", "export_session"} {
		if !names[want] {
			t.Fatalf("missing tool %s (have %v)", want, names)
		}
	}
	// unknown method -> error
	un := c.call("bogus/method", nil)
	if un["error"] == nil {
		t.Fatal("expected error for unknown method")
	}
}

func TestMCPFullJobFlowAndResource(t *testing.T) {
	run := &fakeRunner{result: handback.Result{Trace: handback.Trace{FlowVariant: "form"}}}
	jm := NewJobManager(run)
	c := startMCP(t, jm, fakeArts{data: `{"cookies":[]}`})

	sc, isErr := c.toolCall("start_auth", map[string]any{"account": "alice", "mode": "login"})
	if isErr {
		t.Fatalf("start_auth error: %v", sc)
	}
	jobID, _ := sc["job_id"].(string)
	if jobID == "" {
		t.Fatalf("no job_id: %v", sc)
	}
	// Poll get_job until done.
	deadline := time.After(3 * time.Second)
	var state string
	for {
		gs, _ := c.toolCall("get_job", map[string]any{"job_id": jobID})
		state, _ = gs["state"].(string)
		if state == "done" || state == "failed" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("job never finished; last state %q", state)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if state != "done" {
		t.Fatalf("state %q", state)
	}
	// export_session
	es, isErr := c.toolCall("export_session", map[string]any{"account": "alice"})
	if isErr || es["origin"] != "http://app" {
		t.Fatalf("export: %v %v", es, isErr)
	}
	ref := es["bundle_ref"].(map[string]any)
	uri := ref["uri"].(string)
	// resources/read the bundle uri.
	rr := c.call("resources/read", map[string]any{"uri": uri})
	contents := rr["result"].(map[string]any)["contents"].([]any)
	text := contents[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "cookies") {
		t.Fatalf("resource body: %s", text)
	}
	// get_job on unknown id -> isError
	_, isErr = c.toolCall("get_job", map[string]any{"job_id": "nope"})
	if !isErr {
		t.Fatal("expected error for unknown job")
	}
}
