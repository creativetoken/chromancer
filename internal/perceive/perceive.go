// Package perceive turns a live page into a compact State: interactive and
// landmark nodes with stable integer handles, accessible names computed from
// DOM accessibility semantics (aria-label, labels, placeholders, text), across
// the main frame and first-level iframes. Set-of-marks vision escalation is
// exposed behind the same interface.
package perceive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/go-rod/rod"

	"github.com/creativetoken/chromancer/internal/browser"
)

// Node is one interactive/landmark element.
type Node struct {
	Handle       int    `json:"h"`
	Frame        int    `json:"f"` // 0 = main frame
	Tag          string `json:"tag"`
	Type         string `json:"type,omitempty"`
	Role         string `json:"role,omitempty"`
	Name         string `json:"name,omitempty"`
	Placeholder  string `json:"placeholder,omitempty"`
	Value        string `json:"value,omitempty"`
	Href         string `json:"href,omitempty"`
	ID           string `json:"id,omitempty"`
	NameAttr     string `json:"name_attr,omitempty"`
	Autocomplete string `json:"autocomplete,omitempty"`
	InputMode    string `json:"inputmode,omitempty"` // inputmode attr, lowercased (e.g. "numeric")
	MaxLength    int    `json:"maxlength,omitempty"` // maxlength attr when 1..64, else 0
	Disabled     bool   `json:"disabled,omitempty"`
	Visible      bool   `json:"visible"`
	Text         string `json:"text,omitempty"` // for buttons/links: visible text
}

// State is the perceived page.
type State struct {
	URL      string   `json:"url"`
	Title    string   `json:"title"`
	Headings []string `json:"headings,omitempty"`
	Text     string   `json:"text,omitempty"` // bounded visible text excerpt
	Nodes    []Node   `json:"nodes"`
	Frames   int      `json:"frames"`
}

// StateHash is a structural fingerprint for dedup (simhash over the
// structural signature in Phase 2; a sha256 of the signature in v1).
type StateHash string

// Perceiver turns a live Page into a State.
type Perceiver interface {
	Perceive(ctx context.Context, p *browser.Page) (State, StateHash, error)
	// NeedsVision reports whether structured perception is insufficient (no
	// interactive nodes, canvas-only UI, suspected CAPTCHA). v1 escalation is a
	// stub: callers may take a screenshot and hand it to a vision model.
	NeedsVision(s State) bool
}

// Selector returns the CSS selector that targets a perceived node.
func Selector(h int) string { return fmt.Sprintf(`[data-ab-handle="%d"]`, h) }

// A11yPerceiver is the default a11y-first perceiver.
type A11yPerceiver struct {
	MaxNodes int // cap on nodes serialised (default 150)
	MaxText  int // cap on text excerpt bytes (default 1500)
}

// New returns an A11yPerceiver with defaults.
func New() *A11yPerceiver { return &A11yPerceiver{MaxNodes: 150, MaxText: 1500} }

// Perceive implements Perceiver.
func (a *A11yPerceiver) Perceive(ctx context.Context, p *browser.Page) (State, StateHash, error) {
	frames, err := p.Frames(ctx)
	if err != nil {
		return State{}, "", err
	}
	st := State{URL: p.URL(), Title: p.Title(), Frames: len(frames)}
	base := 1
	for fi, fr := range frames {
		res, err := fr.Context(ctx).Eval(walkJS, base, fi)
		if err != nil {
			if fi == 0 {
				return State{}, "", fmt.Errorf("perceive: eval: %w", err)
			}
			continue
		}
		var out frameOut
		if err := json.Unmarshal([]byte(res.Value.Str()), &out); err != nil {
			if fi == 0 {
				return State{}, "", fmt.Errorf("perceive: decode: %w", err)
			}
			continue
		}
		st.Nodes = append(st.Nodes, out.Nodes...)
		if fi == 0 {
			st.Headings = out.Headings
			st.Text = out.Text
		} else if out.Text != "" {
			st.Text += "\n[frame] " + out.Text
		}
		base += len(out.Nodes)
	}
	if a.MaxNodes > 0 && len(st.Nodes) > a.MaxNodes {
		st.Nodes = st.Nodes[:a.MaxNodes]
	}
	if a.MaxText > 0 && len(st.Text) > a.MaxText {
		st.Text = st.Text[:a.MaxText]
	}
	return st, Hash(st), nil
}

// NeedsVision implements Perceiver.
func (a *A11yPerceiver) NeedsVision(s State) bool {
	if len(s.Nodes) == 0 {
		return true
	}
	for _, n := range s.Nodes {
		if n.Tag == "canvas" {
			return true
		}
	}
	return false
}

// FrameOf returns the rod page for a node's frame.
func FrameOf(ctx context.Context, p *browser.Page, n Node) (*rod.Page, error) {
	frames, err := p.Frames(ctx)
	if err != nil {
		return nil, err
	}
	if n.Frame < 0 || n.Frame >= len(frames) {
		return nil, fmt.Errorf("perceive: frame %d out of range", n.Frame)
	}
	return frames[n.Frame], nil
}

// Find returns the node with handle h.
func (s State) Find(h int) (Node, bool) {
	for _, n := range s.Nodes {
		if n.Handle == h {
			return n, true
		}
	}
	return Node{}, false
}

var idRe = regexp.MustCompile(`/(\d+|[0-9a-fA-F-]{8,})(/|$)`)

// PathTemplate collapses numeric / uuid-ish path segments to {id}.
func PathTemplate(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if j := strings.IndexAny(u, "?#"); j >= 0 {
		u = u[:j]
	}
	return idRe.ReplaceAllString(u, "/{id}$2")
}

// Signature is the structural signature used for hashing: path template +
// sorted (role|tag|type|name) of interactive nodes + headings.
func Signature(s State) string {
	parts := make([]string, 0, len(s.Nodes)+1)
	for _, n := range s.Nodes {
		if !n.Visible {
			continue
		}
		parts = append(parts, n.Role+"|"+n.Tag+"|"+n.Type+"|"+strings.ToLower(strings.TrimSpace(n.Name)))
	}
	sort.Strings(parts)
	return PathTemplate(s.URL) + "\n" + strings.Join(s.Headings, "|") + "\n" + strings.Join(parts, "\n")
}

// Hash returns the StateHash for a state.
func Hash(s State) StateHash {
	sum := sha256.Sum256([]byte(Signature(s)))
	return StateHash(hex.EncodeToString(sum[:8]))
}

// Compact serialises a State for an LLM prompt: one line per node.
func Compact(s State) string {
	var b strings.Builder
	fmt.Fprintf(&b, "URL: %s\nTITLE: %s\n", s.URL, s.Title)
	if len(s.Headings) > 0 {
		fmt.Fprintf(&b, "HEADINGS: %s\n", strings.Join(s.Headings, " | "))
	}
	b.WriteString("NODES:\n")
	for _, n := range s.Nodes {
		if !n.Visible {
			continue
		}
		fmt.Fprintf(&b, "#%d %s", n.Handle, n.Tag)
		if n.Type != "" {
			fmt.Fprintf(&b, "[%s]", n.Type)
		}
		if n.Role != "" {
			fmt.Fprintf(&b, " role=%s", n.Role)
		}
		if n.Name != "" {
			fmt.Fprintf(&b, " name=%q", n.Name)
		}
		if n.Placeholder != "" {
			fmt.Fprintf(&b, " placeholder=%q", n.Placeholder)
		}
		if n.Href != "" {
			fmt.Fprintf(&b, " href=%q", n.Href)
		}
		if n.Frame != 0 {
			fmt.Fprintf(&b, " frame=%d", n.Frame)
		}
		b.WriteString("\n")
	}
	if s.Text != "" {
		fmt.Fprintf(&b, "TEXT: %s\n", s.Text)
	}
	return b.String()
}

type frameOut struct {
	Nodes    []Node   `json:"nodes"`
	Headings []string `json:"headings"`
	Text     string   `json:"text"`
}

// walkJS enumerates interactive elements, computes accessible names, tags each
// with data-ab-handle starting at `base`, and returns JSON.
const walkJS = `(base, frameIndex) => {
  const sel = 'a[href], button, input, select, textarea, [role="button"], [role="link"], [role="textbox"], [role="checkbox"], [role="radio"], [role="menuitem"], [role="tab"], [role="switch"], [contenteditable="true"], summary, [onclick], canvas';
  const els = Array.from(document.querySelectorAll(sel));
  const seen = new Set();
  const txt = (el) => (el && (el.innerText || el.textContent) || '').replace(/\s+/g, ' ').trim();
  const labelFor = (el) => {
    if (el.id) { const l = document.querySelector('label[for="' + CSS.escape(el.id) + '"]'); if (l) return txt(l); }
    const wrap = el.closest('label'); if (wrap) return txt(wrap);
    return '';
  };
  const accName = (el) => {
    const al = el.getAttribute('aria-label'); if (al) return al.trim();
    const lb = el.getAttribute('aria-labelledby');
    if (lb) { const parts = lb.split(/\s+/).map(id => { const n = document.getElementById(id); return n ? txt(n) : ''; }).filter(Boolean); if (parts.length) return parts.join(' '); }
    const tag = el.tagName.toLowerCase();
    if (tag === 'input' || tag === 'select' || tag === 'textarea') {
      const l = labelFor(el); if (l) return l;
      if (el.placeholder) return el.placeholder;
      if (el.type === 'submit' || el.type === 'button') return el.value || '';
      return el.getAttribute('title') || el.name || '';
    }
    const t = txt(el); if (t) return t.slice(0, 120);
    const img = el.querySelector('img[alt]'); if (img) return img.getAttribute('alt');
    return el.getAttribute('title') || el.getAttribute('value') || '';
  };
  const visible = (el) => {
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden' || cs.opacity === '0') return false;
    if (el.type === 'hidden') return false;
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  };
  const nodes = [];
  let h = base;
  for (const el of els) {
    if (seen.has(el)) continue; seen.add(el);
    const tag = el.tagName.toLowerCase();
    const vis = visible(el);
    if (!vis && tag !== 'input') continue;
    el.setAttribute('data-ab-handle', String(h));
    nodes.push({
      h: h, f: frameIndex, tag: tag,
      type: (el.getAttribute('type') || '').toLowerCase(),
      role: el.getAttribute('role') || '',
      name: accName(el),
      placeholder: el.placeholder || '',
      value: (tag === 'input' && (el.type === 'password')) ? (el.value ? '***' : '') : ((el.value || '') + '').slice(0, 80),
      href: tag === 'a' ? (el.getAttribute('href') || '') : '',
      id: el.id || '',
      name_attr: el.getAttribute('name') || '',
      autocomplete: el.getAttribute('autocomplete') || '',
      inputmode: (el.getAttribute('inputmode') || '').toLowerCase(),
      maxlength: (function(){ const m = parseInt(el.getAttribute('maxlength'), 10); return (m > 0 && m <= 64) ? m : 0; })(),
      disabled: !!el.disabled,
      visible: vis,
      text: (tag === 'a' || tag === 'button' || el.getAttribute('role') === 'button') ? txt(el).slice(0, 120) : ''
    });
    h++;
  }
  const headings = Array.from(document.querySelectorAll('h1, h2, h3')).map(txt).filter(Boolean).slice(0, 12);
  const text = txt(document.body).slice(0, 4000);
  return JSON.stringify({ nodes, headings, text });
}`
