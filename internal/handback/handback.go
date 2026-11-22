// Package handback produces the v1 product: a reusable SessionBundle and an
// auth-flow AuthRecording (HAR-style, secrets flagged), a replayability check
// that tests the bundle outside the browser, and an artefact writer that
// returns references (paths / MCP resource URIs), never inline payloads.
package handback

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
)

// ArtefactRef points at a written artefact.
type ArtefactRef struct {
	Path string `json:"path"`
	URI  string `json:"uri"`
}

// SessionBundle is everything a caller needs to replay the authenticated
// session elsewhere.
type SessionBundle struct {
	Target         string            `json:"target"`
	Account        string            `json:"account"`
	Origin         string            `json:"origin"`
	Cookies        []browser.Cookie  `json:"cookies"`
	LocalStorage   map[string]string `json:"local_storage"`
	SessionStorage map[string]string `json:"session_storage"`
	AuthHeaders    map[string]string `json:"auth_headers"` // Authorization/bearer, CSRF, custom headers seen on post-login XHR/fetch
	UserAgent      string            `json:"user_agent"`
	Proxy          string            `json:"proxy,omitempty"`
	TokenExpiry    *time.Time        `json:"token_expiry,omitempty"`
	Replayable     bool              `json:"replayable"`
	ReplayReason   string            `json:"replay_reason,omitempty"`
	LandingURL     string            `json:"landing_url"`
	CapturedAt     time.Time         `json:"captured_at"`
	Backend        string            `json:"backend,omitempty"`         // "chromium" | "camoufox"
	BackendVersion string            `json:"backend_version,omitempty"` // engine/build version
}

// Trace is the short, human-readable account of what the job did.
type Trace struct {
	Entrypoint      string            `json:"entrypoint,omitempty"`
	EntrypointHow   string            `json:"entrypoint_how,omitempty"`
	FlowVariant     string            `json:"flow_variant,omitempty"`
	Restored        bool              `json:"restored"`
	Steps           []string          `json:"steps"`
	Verification    []string          `json:"verification,omitempty"`
	Captcha         []string          `json:"captcha,omitempty"`
	AgentTakeover   []string          `json:"agent_takeover,omitempty"`
	AgentSteps      int               `json:"agent_steps,omitempty"` // number of agent actions (agent-primary / takeover)
	HumanHandoff    string            `json:"human_handoff,omitempty"`
	Replayable      bool              `json:"replayable"`
	ReplayReason    string            `json:"replay_reason,omitempty"`
	Timings         map[string]string `json:"timings,omitempty"`
	StartedAt       time.Time         `json:"started_at"`
	FinishedAt      time.Time         `json:"finished_at"`
	SuccessEvidence []string          `json:"success_evidence,omitempty"`
	Backend         string            `json:"backend,omitempty"`         // browser engine used ("chromium" | "camoufox")
	BackendVersion  string            `json:"backend_version,omitempty"` // engine/build version
}

// Add appends a step to the trace.
func (t *Trace) Add(format string, args ...any) {
	t.Steps = append(t.Steps, fmt.Sprintf(format, args...))
}

// Result is what Authenticator.Run returns on success.
type Result struct {
	BundleRef    ArtefactRef `json:"bundle_ref"`
	RecordingRef ArtefactRef `json:"recording_ref"`
	Trace        Trace       `json:"trace"`
}

// ArtefactWriter persists the bundle and recording into the per-account
// workspace and returns references.
type ArtefactWriter interface {
	WriteBundle(ctx context.Context, b SessionBundle) (ArtefactRef, error)
	WriteRecording(ctx context.Context, r AuthRecording) (ArtefactRef, error)
	// LatestBundle returns the most recent bundle reference for the account.
	LatestBundle(ctx context.Context) (ArtefactRef, error)
	// ReadBundle loads a bundle from a reference.
	ReadBundle(ctx context.Context, ref ArtefactRef) (SessionBundle, error)
}

// FileWriter writes artefacts under dir and references them as file paths and
// `agentbrowser://artefacts/<account>/<file>` URIs.
type FileWriter struct {
	Dir     string
	Account string
}

func (w *FileWriter) ref(name string) ArtefactRef {
	return ArtefactRef{Path: filepath.Join(w.Dir, name), URI: "agentbrowser://artefacts/" + w.Account + "/" + name}
}

func (w *FileWriter) writeJSON(name string, v any) (ArtefactRef, error) {
	if err := os.MkdirAll(w.Dir, 0o700); err != nil {
		return ArtefactRef{}, fmt.Errorf("handback: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ArtefactRef{}, fmt.Errorf("handback: marshal: %w", err)
	}
	r := w.ref(name)
	tmp := r.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return ArtefactRef{}, fmt.Errorf("handback: write: %w", err)
	}
	if err := os.Rename(tmp, r.Path); err != nil {
		return ArtefactRef{}, fmt.Errorf("handback: rename: %w", err)
	}
	return r, nil
}

// WriteBundle implements ArtefactWriter.
func (w *FileWriter) WriteBundle(_ context.Context, b SessionBundle) (ArtefactRef, error) {
	name := fmt.Sprintf("bundle-%s.json", b.CapturedAt.UTC().Format("20060102T150405.000Z"))
	r, err := w.writeJSON(name, b)
	if err != nil {
		return r, err
	}
	// Pointer to latest.
	_ = os.WriteFile(filepath.Join(w.Dir, "bundle-latest.txt"), []byte(name), 0o600)
	return r, nil
}

// WriteRecording implements ArtefactWriter.
func (w *FileWriter) WriteRecording(_ context.Context, rec AuthRecording) (ArtefactRef, error) {
	name := fmt.Sprintf("recording-%s.har.json", rec.StartedAt.UTC().Format("20060102T150405.000Z"))
	return w.writeJSON(name, rec)
}

// LatestBundle implements ArtefactWriter.
func (w *FileWriter) LatestBundle(_ context.Context) (ArtefactRef, error) {
	name, err := os.ReadFile(filepath.Join(w.Dir, "bundle-latest.txt"))
	if err != nil {
		return ArtefactRef{}, fmt.Errorf("handback: no bundle for account %s: %w", w.Account, err)
	}
	return w.ref(string(name)), nil
}

// ReadBundle implements ArtefactWriter.
func (w *FileWriter) ReadBundle(_ context.Context, ref ArtefactRef) (SessionBundle, error) {
	data, err := os.ReadFile(ref.Path)
	if err != nil {
		return SessionBundle{}, fmt.Errorf("handback: read bundle: %w", err)
	}
	var b SessionBundle
	if err := json.Unmarshal(data, &b); err != nil {
		return SessionBundle{}, fmt.Errorf("handback: decode bundle: %w", err)
	}
	return b, nil
}
