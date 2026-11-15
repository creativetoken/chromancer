// Package audit provides an append-only, hash-chained audit log whose entries
// are redacted BEFORE they are written. A failed append is a kill-switch
// condition: the agent never acts un-audited.
package audit

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Entry is one audit record. Payload is free-form but is passed through the
// Redactor before persistence; the stored form is the only form.
type Entry struct {
	Seq      int64          `json:"seq"`
	At       time.Time      `json:"at"`
	RunID    string         `json:"run_id,omitempty"`
	Actor    string         `json:"actor"`
	Kind     string         `json:"kind"`
	Payload  map[string]any `json:"payload,omitempty"`
	PrevHash string         `json:"prev_hash"`
	Hash     string         `json:"hash"`
}

// AuditLog is the append-only record of everything the agent did.
type AuditLog interface {
	// Append redacts e, chains it to the previous entry and persists it durably
	// before returning. Returns the stored (redacted) entry.
	Append(ctx context.Context, e Entry) (Entry, error)
	// Redact returns a redacted copy of e without persisting it.
	Redact(e Entry) Entry
	// Entries returns all entries (for tests / reporting).
	Entries(ctx context.Context) ([]Entry, error)
	// Verify checks the hash chain; it returns the first broken sequence number
	// or 0 if intact.
	Verify(ctx context.Context) (broken int64, err error)
	// Close releases the underlying file.
	Close() error
}

// Redactor replaces secrets in payloads. It redacts by key name (password,
// token, authorization, cookie, otp, code …) and by known secret values the
// run has registered (so the literal password never appears anywhere).
type Redactor struct {
	mu      sync.RWMutex
	secrets []string
	keyRe   *regexp.Regexp
	valRe   *regexp.Regexp
}

// NewRedactor builds a Redactor with the default sensitive key patterns.
func NewRedactor() *Redactor {
	return &Redactor{
		keyRe: regexp.MustCompile(`(?i)(pass(word|wd)?|secret|token|authorization|auth|cookie|set-cookie|otp|code|csrf|xsrf|session|bearer|api[-_]?key|seed)`),
		// Bearer tokens / JWTs / long opaque hex or base64 blobs inside strings.
		valRe: regexp.MustCompile(`(?i)(bearer\s+[A-Za-z0-9\-_\.=]+|eyJ[A-Za-z0-9\-_]+\.[A-Za-z0-9\-_]+\.[A-Za-z0-9\-_]+|\b[a-f0-9]{32,}\b)`),
	}
}

// AddSecret registers a literal secret value to scrub everywhere.
func (r *Redactor) AddSecret(v string) {
	if strings.TrimSpace(v) == "" {
		return
	}
	r.mu.Lock()
	r.secrets = append(r.secrets, v)
	r.mu.Unlock()
}

// String redacts a free-form string.
func (r *Redactor) String(s string) string {
	r.mu.RLock()
	secrets := append([]string(nil), r.secrets...)
	r.mu.RUnlock()
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "[REDACTED]")
		}
	}
	return r.valRe.ReplaceAllString(s, "[REDACTED]")
}

// Value redacts an arbitrary JSON-ish value, descending into maps and slices.
// keyHint is the key the value sits under (used for key-based redaction).
func (r *Redactor) Value(keyHint string, v any) any {
	switch t := v.(type) {
	case string:
		if keyHint != "" && r.keyRe.MatchString(keyHint) && !isSafeKey(keyHint) {
			return "[REDACTED]"
		}
		return r.String(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = r.Value(k, vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = r.Value(keyHint, vv)
		}
		return out
	case []string:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = r.Value(keyHint, vv)
		}
		return out
	default:
		return v
	}
}

// isSafeKey whitelists keys that merely *mention* a sensitive word but carry
// no secret (e.g. "status_code", "auth_url", "has_password_field").
func isSafeKey(k string) bool {
	lk := strings.ToLower(k)
	for _, s := range []string{"status_code", "_url", "has_", "count", "kind", "variant", "channel", "reason", "name", "label", "field", "selector"} {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return false
}

type fileLog struct {
	mu   sync.Mutex
	f    *os.File
	path string
	seq  int64
	last string
	red  *Redactor
}

// OpenFile opens (or creates) a JSONL audit log at path and recovers the
// chain tail so appends continue the existing chain.
func OpenFile(path string, red *Redactor) (AuditLog, error) {
	if red == nil {
		red = NewRedactor()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("audit: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", path, err)
	}
	l := &fileLog{f: f, path: path, red: red}
	// Recover tail.
	if _, err := f.Seek(0, io.SeekStart); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				l.seq = e.Seq
				l.last = e.Hash
			}
		}
	}
	return l, nil
}

func (l *fileLog) Redact(e Entry) Entry {
	out := e
	if e.Payload != nil {
		m := l.red.Value("", e.Payload)
		if mm, ok := m.(map[string]any); ok {
			out.Payload = mm
		}
	}
	return out
}

func (l *fileLog) Append(ctx context.Context, e Entry) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e = l.Redact(e)
	l.seq++
	e.Seq = l.seq
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	e.PrevHash = l.last
	e.Hash = ""
	canon, err := json.Marshal(e)
	if err != nil {
		return Entry{}, fmt.Errorf("audit: marshal: %w", err)
	}
	sum := sha256.Sum256(append([]byte(e.PrevHash), canon...))
	e.Hash = hex.EncodeToString(sum[:])
	line, err := json.Marshal(e)
	if err != nil {
		return Entry{}, fmt.Errorf("audit: marshal: %w", err)
	}
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return Entry{}, fmt.Errorf("audit: write: %w", err)
	}
	if err := l.f.Sync(); err != nil {
		return Entry{}, fmt.Errorf("audit: sync: %w", err)
	}
	l.last = e.Hash
	return e, nil
}

func (l *fileLog) Entries(ctx context.Context) ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	data, err := os.ReadFile(l.path)
	if err != nil {
		return nil, fmt.Errorf("audit: read: %w", err)
	}
	var out []Entry
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("audit: parse: %w", err)
		}
		out = append(out, e)
	}
	return out, nil
}

func (l *fileLog) Verify(ctx context.Context) (int64, error) {
	entries, err := l.Entries(ctx)
	if err != nil {
		return 0, err
	}
	prev := ""
	for _, e := range entries {
		if e.PrevHash != prev {
			return e.Seq, nil
		}
		h := e.Hash
		e.Hash = ""
		canon, err := json.Marshal(e)
		if err != nil {
			return e.Seq, err
		}
		sum := sha256.Sum256(append([]byte(e.PrevHash), canon...))
		if hex.EncodeToString(sum[:]) != h {
			return e.Seq, nil
		}
		prev = h
	}
	return 0, nil
}

func (l *fileLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// ErrClosed is returned by a closed log.
var ErrClosed = errors.New("audit log closed")

// Nop is an AuditLog that discards everything (tests / dry runs).
type Nop struct{ red *Redactor }

// NewNop returns a discarding AuditLog.
func NewNop() AuditLog { return &Nop{red: NewRedactor()} }

// Append implements AuditLog.
func (n *Nop) Append(_ context.Context, e Entry) (Entry, error) { return n.Redact(e), nil }

// Redact implements AuditLog.
func (n *Nop) Redact(e Entry) Entry {
	if e.Payload != nil {
		if mm, ok := n.red.Value("", e.Payload).(map[string]any); ok {
			e.Payload = mm
		}
	}
	return e
}

// Entries implements AuditLog.
func (n *Nop) Entries(context.Context) ([]Entry, error) { return nil, nil }

// Verify implements AuditLog.
func (n *Nop) Verify(context.Context) (int64, error) { return 0, nil }

// Close implements AuditLog.
func (n *Nop) Close() error { return nil }
