package lab

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"regexp"
	"sort"
	"strconv"
)

// actVisual is the visual-mode decision function: it decodes the screenshot,
// locates the solid marker colours the visual lab apps paint, and drives sign-in
// by coordinate. This is a deterministic, offline stand-in for a real vision
// model — it proves the whole visual pipeline (screenshot capture → set-of-marks
// → coordinate action → type into focus) without any API call.
//
// Phase per field: click the field marker (focus), then type the placeholder the
// Executor substitutes. Then click submit. With no field markers but a submit/
// primary marker (the canvas app's painted button), click it to advance to the
// real form, after which text mode takes over.
func (p *PolicyModel) actVisual(img []byte, obs string, schema json.RawMessage) json.RawMessage {
	p.calls++
	avail := actionsFromSchema(schema)
	has := func(name string) bool { return avail == nil || avail[name] }

	m, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		return mustJSON(act{"action": "note", "why": "could not decode screenshot"})
	}
	vw, vh := parseViewport(obs)

	ex, ey, hasEmail := findColorCenter(m, markerEmail, vw, vh)
	px, py, hasPw := findColorCenter(m, markerPass, vw, vh)
	sx, sy, hasSubmit := findColorCenter(m, markerSubmit, vw, vh)

	if hasEmail && !p.visEmailDone {
		if p.visFocus != "email" {
			p.visFocus = "email"
			return mustJSON(act{"action": "click_at", "x": ex, "y": ey, "why": "focus the email field (visual)"})
		}
		p.visEmailDone = true
		p.visFocus = ""
		return mustJSON(act{"action": "type_text", "text": "{{account.email}}", "why": "type the email (visual)"})
	}
	if hasPw && !p.visPwDone {
		if p.visFocus != "pw" {
			p.visFocus = "pw"
			return mustJSON(act{"action": "click_at", "x": px, "y": py, "why": "focus the password field (visual)"})
		}
		p.visPwDone = true
		p.visFocus = ""
		return mustJSON(act{"action": "type_text", "text": "{{account.password}}", "why": "type the password (visual)"})
	}
	if hasSubmit && !p.visSubmitted && ((p.visEmailDone && p.visPwDone) || (!hasEmail && !hasPw)) {
		p.visSubmitted = true
		return mustJSON(act{"action": "click_at", "x": sx, "y": sy, "why": "click the sign-in control (visual)"})
	}
	// Nothing left to operate visually — confirm the session (or wait for the
	// post-submit navigation to settle).
	if has("check_logged_in") {
		p.checks++
		if p.checks <= 5 {
			return mustJSON(act{"action": "check_logged_in", "why": "no visual control left; check whether login succeeded"})
		}
	}
	if has("request_human") {
		return mustJSON(act{"action": "request_human", "why": "no visual path", "reason": "could not operate the page visually"})
	}
	return mustJSON(act{"action": "note", "why": "waiting for the page to settle"})
}

func mustJSON(a act) json.RawMessage { b, _ := json.Marshal(a); return b }

var viewportRe = regexp.MustCompile(`viewport is (\d+)x(\d+) CSS px`)

// parseViewport reads the CSS viewport dims the explorer states in the visual
// preamble, so image pixels map to click_at coordinates. Falls back to the image
// being CSS-px (scale 1) when absent.
func parseViewport(obs string) (int, int) {
	m := viewportRe.FindStringSubmatch(obs)
	if len(m) != 3 {
		return 0, 0
	}
	w, _ := strconv.Atoi(m[1])
	h, _ := strconv.Atoi(m[2])
	return w, h
}

// findColorCenter returns the MEDIAN (in CSS viewport px) of pixels matching
// colour c within a per-channel tolerance, and whether enough matched. The
// median — not the mean — is used so scattered false matches (e.g. camoufox's
// anti-fingerprinting canvas noise speckles green across the whole canvas) do
// not pull the result off the dense cluster (the actual button). Image
// coordinates map to CSS px via the viewport dims (vw,vh); unknown → image px.
func findColorCenter(m image.Image, c [3]uint8, vw, vh int) (int, int, bool) {
	const tol = 45
	b := m.Bounds()
	iw, ih := b.Dx(), b.Dy()
	if iw == 0 || ih == 0 {
		return 0, 0, false
	}
	var xs, ys []int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := m.At(x, y).RGBA()
			if absDiff(int(r>>8), int(c[0])) <= tol &&
				absDiff(int(g>>8), int(c[1])) <= tol &&
				absDiff(int(bl>>8), int(c[2])) <= tol {
				xs = append(xs, x-b.Min.X)
				ys = append(ys, y-b.Min.Y)
			}
		}
	}
	if len(xs) < 20 {
		return 0, 0, false
	}
	cx := median(xs)
	cy := median(ys)
	if vw > 0 && vh > 0 {
		cx = cx * vw / iw
		cy = cy * vh / ih
	}
	return cx, cy, true
}

// median returns the median of v (v is modified by sorting).
func median(v []int) int {
	sort.Ints(v)
	return v[len(v)/2]
}

func absDiff(a, b int) int {
	if a < b {
		return b - a
	}
	return a - b
}
