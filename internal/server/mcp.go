package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/creativetoken/chromancer/internal/auth"
)

// ProtocolVersion is the MCP protocol version this server speaks.
const ProtocolVersion = "2024-11-05"

// ArtefactReader resolves an agentbrowser://artefacts/<account>/<file> URI to
// its bytes and media type.
type ArtefactReader interface {
	ReadArtefactURI(uri string) (data []byte, mime string, err error)
}

// MCP is a minimal Model Context Protocol server over a line-delimited
// JSON-RPC 2.0 stream (the MCP stdio transport). It exposes the five job tools
// and reads artefacts as resources.
type MCP struct {
	jobs *JobManager
	arts ArtefactReader
	name string
	ver  string
	mu   sync.Mutex
	outW *bufio.Writer
}

// NewMCP builds the server.
func NewMCP(jobs *JobManager, arts ArtefactReader) *MCP {
	return &MCP{jobs: jobs, arts: arts, name: "agentbrowser", ver: "0.1.0"}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Serve runs the JSON-RPC loop until the input closes or ctx is cancelled.
func (s *MCP) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.outW = bufio.NewWriter(out)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<16), 8<<20)
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.write(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		s.dispatch(ctx, req)
	}
	return sc.Err()
}

func (s *MCP) dispatch(ctx context.Context, req rpcRequest) {
	isNotification := len(req.ID) == 0
	result, rerr := s.handle(ctx, req.Method, req.Params)
	if isNotification {
		return // notifications get no response
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	s.write(resp)
}

func (s *MCP) write(resp rpcResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = s.outW.Write(data)
	_ = s.outW.WriteByte('\n')
	_ = s.outW.Flush()
}

func (s *MCP) handle(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}, "resources": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.name, "version": s.ver},
		}, nil
	case "notifications/initialized", "notifications/cancelled":
		return map[string]any{}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefs()}, nil
	case "tools/call":
		return s.toolsCall(ctx, params)
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "resources/read":
		return s.resourcesRead(params)
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + method}
	}
}

// ---- tools ----

func (s *MCP) toolsCall(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params"}
	}
	payload, err := s.callTool(ctx, p.Name, p.Arguments)
	if err != nil {
		return toolResult(map[string]any{"error": err.Error()}, true), nil
	}
	return toolResult(payload, false), nil
}

func (s *MCP) callTool(ctx context.Context, name string, args json.RawMessage) (any, error) {
	switch name {
	case "start_auth":
		var a struct {
			Account   string `json:"account"`
			Mode      string `json:"mode"`
			LoginURL  string `json:"login_url"`
			SignupURL string `json:"signup_url"`
		}
		_ = json.Unmarshal(args, &a)
		if a.Account == "" {
			return nil, fmt.Errorf("account is required")
		}
		mode := auth.Login
		if a.Mode == "signup" {
			mode = auth.Signup
		} else if a.Mode != "" && a.Mode != "login" {
			return nil, fmt.Errorf("mode must be login or signup")
		}
		id, err := s.jobs.StartAuth(ctx, a.Account, mode, auth.Hints{LoginURL: a.LoginURL, SignupURL: a.SignupURL})
		if err != nil {
			return nil, err
		}
		return map[string]any{"job_id": id}, nil
	case "start_crawl":
		var a struct {
			Account string `json:"account"`
		}
		_ = json.Unmarshal(args, &a)
		if a.Account == "" {
			return nil, fmt.Errorf("account is required")
		}
		id, err := s.jobs.StartCrawl(ctx, a.Account)
		if err != nil {
			return nil, err
		}
		return map[string]any{"job_id": id}, nil
	case "get_job":
		var a struct {
			JobID string `json:"job_id"`
		}
		_ = json.Unmarshal(args, &a)
		j, err := s.jobs.GetJob(a.JobID)
		if err != nil {
			return nil, err
		}
		return j, nil
	case "resolve_challenge":
		var a struct {
			JobID       string `json:"job_id"`
			ChallengeID string `json:"challenge_id"`
			Code        string `json:"code"`
			Link        string `json:"link"`
			Approved    bool   `json:"approved"`
			Done        bool   `json:"done"`
		}
		_ = json.Unmarshal(args, &a)
		if err := s.jobs.ResolveChallenge(a.JobID, a.ChallengeID, Answer{Code: a.Code, Link: a.Link, Approved: a.Approved, Done: a.Done}); err != nil {
			return nil, err
		}
		return map[string]any{"status": "accepted"}, nil
	case "cancel_job":
		var a struct {
			JobID string `json:"job_id"`
		}
		_ = json.Unmarshal(args, &a)
		if err := s.jobs.CancelJob(a.JobID); err != nil {
			return nil, err
		}
		return map[string]any{"status": "cancelled"}, nil
	case "export_session":
		var a struct {
			Account string `json:"account"`
		}
		_ = json.Unmarshal(args, &a)
		ref, bundle, err := s.jobs.ExportSession(ctx, a.Account)
		if err != nil {
			return nil, err
		}
		return map[string]any{"bundle_ref": ref, "replayable": bundle.Replayable, "replay_reason": bundle.ReplayReason, "origin": bundle.Origin}, nil
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

func toolResult(payload any, isError bool) map[string]any {
	text, _ := json.MarshalIndent(payload, "", "  ")
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(text)}},
		"isError":           isError,
		"structuredContent": payload,
	}
}

func (s *MCP) resourcesRead(params json.RawMessage) (any, *rpcError) {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params"}
	}
	if s.arts == nil {
		return nil, &rpcError{Code: -32002, Message: "resources unavailable"}
	}
	data, mime, err := s.arts.ReadArtefactURI(p.URI)
	if err != nil {
		return nil, &rpcError{Code: -32002, Message: err.Error()}
	}
	return map[string]any{
		"contents": []any{map[string]any{"uri": p.URI, "mimeType": mime, "text": string(data)}},
	}, nil
}

func strObj(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func toolDefs() []any {
	str := map[string]any{"type": "string"}
	boolean := map[string]any{"type": "boolean"}
	return []any{
		map[string]any{
			"name":        "start_auth",
			"description": "Authenticate an account (login or signup) with operator-provided credentials; returns a job_id. Poll get_job for status and the done{bundle_ref,recording_ref,trace} result.",
			"inputSchema": strObj(map[string]any{
				"account":    str,
				"mode":       map[string]any{"type": "string", "enum": []string{"login", "signup"}},
				"login_url":  str,
				"signup_url": str,
			}, "account"),
		},
		map[string]any{
			"name":        "start_crawl",
			"description": "Phase 2: start an authenticated crawl of the app for an account (uses the saved session; re-logs-in if it drops; never takes account-ending actions). Returns a job_id.",
			"inputSchema": strObj(map[string]any{"account": str}, "account"),
		},
		map[string]any{
			"name":        "get_job",
			"description": "Get a job's status: running | needs_verification | needs_human | done | failed | cancelled, with its pending challenge or result (auth done carries bundle_ref/recording_ref; crawl done carries a report).",
			"inputSchema": strObj(map[string]any{"job_id": str}, "job_id"),
		},
		map[string]any{
			"name":        "resolve_challenge",
			"description": "Answer a pending challenge: a verification code/link/approval, or a human-handoff 'done' signal.",
			"inputSchema": strObj(map[string]any{"job_id": str, "challenge_id": str, "code": str, "link": str, "approved": boolean, "done": boolean}, "job_id", "challenge_id"),
		},
		map[string]any{
			"name":        "cancel_job",
			"description": "Cancel a running job and clean up its browser context.",
			"inputSchema": strObj(map[string]any{"job_id": str}, "job_id"),
		},
		map[string]any{
			"name":        "export_session",
			"description": "Return a reference to the latest SessionBundle for an account (read it via resources/read on bundle_ref.uri).",
			"inputSchema": strObj(map[string]any{"account": str}, "account"),
		},
	}
}
