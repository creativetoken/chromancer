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
	Pos          string `json:"pos,omitempty"`  // position hint for unlabeled controls, e.g. "top-right header"
	Rect         []int  `json:"rect,omitempty"` // [x,y,w,h] in CSS px, viewport-relative; for set-of-marks + coordinate actions
}

// CenterOf returns the viewport-relative center (CSS px) of a node's bounding
// box, and whether it has one. Visual/coordinate mode acts on a marked node by
// clicking its center, so a node inside an open shadow root (not resolvable by a
// flat querySelector) is still actionable.
func (n Node) CenterOf() (x, y int, ok bool) {
	if len(n.Rect) != 4 || n.Rect[2] <= 0 || n.Rect[3] <= 0 {
		return 0, 0, false
	}
	return n.Rect[0] + n.Rect[2]/2, n.Rect[1] + n.Rect[3]/2, true
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
	Perceive(ctx context.Context, p browser.Page) (State, StateHash, error)
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
func (a *A11yPerceiver) Perceive(ctx context.Context, p browser.Page) (State, StateHash, error) {
	frames, err := p.Frames(ctx)
	if err != nil {
		return State{}, "", err
	}
	st := State{URL: p.URL(), Title: p.Title(), Frames: len(frames)}
	base := 1
	for fi, fr := range frames {
		res, err := fr.Eval(ctx, walkJS, base, fi)
		if err != nil {
			if fi == 0 {
				return State{}, "", fmt.Errorf("perceive: eval: %w", err)
			}
			continue
		}
		var out frameOut
		if err := json.Unmarshal([]byte(res.Str()), &out); err != nil {
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

// FrameOf returns the frame handle for a node's frame.
func FrameOf(ctx context.Context, p browser.Page, n Node) (browser.Frame, error) {
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
  const sel = 'a[href], button, input, select, textarea, [role="button"], [role="link"], [role="textbox"], [role="checkbox"], [role="radio"], [role="menuitem"], [role="tab"], [role="switch"], [contenteditable="true"], summary, [onclick], canvas, [role="alert"], [role="status"], [aria-live="assertive"], [aria-live="polite"]';
  // Collect matches from the document AND every OPEN shadow root, recursively.
  // A closed shadow root cannot be read (null shadowRoot) — those controls are
  // only reachable via visual/coordinate mode. Order is document-first, then
  // shadow hosts depth-first, so handles stay stable across perceives.
  const collect = (root, out) => {
    out.push.apply(out, Array.from(root.querySelectorAll(sel)));
    const hosts = root.querySelectorAll('*');
    for (const host of hosts) { if (host.shadowRoot) collect(host.shadowRoot, out); }
  };
  const els = [];
  collect(document, els);
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
    const img = el.querySelector('img[alt]'); if (img && img.getAttribute('alt')) return img.getAttribute('alt');
    const st = el.querySelector('svg title, svg [id] title, title'); if (st && txt(st)) return txt(st).slice(0, 120);
    const title = el.getAttribute('title'); if (title) return title;
    const v = el.getAttribute('value'); if (v) return v;
    return hintName(el);
  };
  // hintName derives a name for an otherwise-unlabeled control from its
  // data-testid/class/id, or (for links) its href, when they clearly denote an
  // account / sign-in control. This surfaces icon-only header buttons (avatars,
  // account menus) the agent would otherwise not recognise as a sign-in entry.
  const hintName = (el) => {
    const cls = (el.className && el.className.baseVal != null) ? el.className.baseVal : (el.className || '');
    const hay = [el.getAttribute('data-testid') || '', String(cls), el.id || '',
                 el.tagName.toLowerCase() === 'a' ? (el.getAttribute('href') || '') : ''].join(' ').toLowerCase();
    const map = [['sign-in', 'sign in'], ['signin', 'sign in'], ['sign_in', 'sign in'],
                 ['log-in', 'log in'], ['login', 'log in'], ['log_in', 'log in'],
                 ['account', 'account'], ['avatar', 'account'], ['my-account', 'account'],
                 ['profile', 'account'], ['user-menu', 'account'], ['usermenu', 'account'], ['user', 'account']];
    for (const [needle, label] of map) { if (hay.indexOf(needle) >= 0) return label + ' (icon)'; }
    return '';
  };
  // posOf describes where a control sits, so an unlabeled one is still locatable
  // ("top-right header"). Region (header/nav) first, then a 3x3 viewport cell.
  const posOf = (el) => {
    const r = el.getBoundingClientRect();
    const vw = window.innerWidth || 1, vh = window.innerHeight || 1;
    const vert = r.top < vh * 0.33 ? 'top' : (r.top > vh * 0.66 ? 'bottom' : 'middle');
    const horiz = r.left < vw * 0.33 ? 'left' : (r.left > vw * 0.66 ? 'right' : 'center');
    const region = el.closest('header, [role="banner"]') ? ' header'
                 : (el.closest('nav, [role="navigation"]') ? ' nav' : '');
    return vert + '-' + horiz + region;
  };
  const interactive = (el) => {
    const tag = el.tagName.toLowerCase();
    return tag === 'a' || tag === 'button' || el.getAttribute('role') === 'button'
        || el.getAttribute('role') === 'link' || el.getAttribute('role') === 'menuitem'
        || el.hasAttribute('onclick');
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
    let nm = accName(el);
    let pos = '';
    if (!nm && interactive(el)) {
      // An unlabeled interactive control (icon-only button/link): keep it, but
      // name it so the agent can reason about and locate it.
      pos = posOf(el);
      nm = 'icon (unlabeled)';
    }
    nodes.push({
      h: h, f: frameIndex, tag: tag,
      type: (el.getAttribute('type') || '').toLowerCase(),
      role: el.getAttribute('role') || '',
      name: nm,
      pos: pos,
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
      rect: (function(){ const r = el.getBoundingClientRect(); return [Math.round(r.x), Math.round(r.y), Math.round(r.width), Math.round(r.height)]; })(),
      text: (tag === 'a' || tag === 'button' || el.getAttribute('role') === 'button' || el.getAttribute('role') === 'alert' || el.getAttribute('role') === 'status' || el.hasAttribute('aria-live')) ? txt(el).slice(0, 200) : ''
    });
    h++;
  }
  const headings = Array.from(document.querySelectorAll('h1, h2, h3')).map(txt).filter(Boolean).slice(0, 12);
  const text = txt(document.body).slice(0, 4000);
  return JSON.stringify({ nodes, headings, text });
}`
