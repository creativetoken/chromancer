package captcha

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"golang.org/x/net/html"

	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// Challenge.Params keys populated by detection (extraction).
const (
	ParamCallback   = "callback"   // data-callback / client-config callback name
	ParamAction     = "action"     // v3/enterprise action or Turnstile data-action
	ParamEnterprise = "enterprise" // "true" when the Enterprise API is in use
	ParamDataS      = "data_s"     // reCAPTCHA data-s (rare)
	ParamRQData     = "rqdata"     // hCaptcha rqdata
	ParamCData      = "cdata"      // Turnstile cData
	ParamSize       = "size"       // reCAPTCHA/Turnstile data-size (invisible|normal|compact)
	ParamClass      = "class"      // subclass marker: cloudflare_interstitial|arkose|geetest|image_grid|text
	ParamInputSel   = "input_sel"  // text-captcha answer input selector
	ParamImgSel     = "img_sel"    // image/text-captcha image selector
	ParamTiles      = "tiles"      // image-grid tile count
	ParamSolveID    = "solve_id"   // carried to the solver
	ParamUserAgent  = "user_agent" // browser UA, passed to NeedsProxy solvers
	ParamProxy      = "proxy"      // upstream proxy URL, passed to NeedsProxy solvers
)

// widgetHit is one DOM widget container found by a probe.
type widgetHit struct {
	Vendor     string `json:"vendor"` // recaptcha_v2|hcaptcha|turnstile|mock
	SiteKey    string `json:"sitekey"`
	FieldSel   string `json:"field_sel"`
	Visible    bool   `json:"visible"`
	Size       string `json:"size"`
	Callback   string `json:"callback"`
	Action     string `json:"action"`
	Enterprise bool   `json:"enterprise"`
	DataS      string `json:"data_s"`
	RQData     string `json:"rqdata"`
	CData      string `json:"cdata"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	W          int    `json:"w"`
	H          int    `json:"h"`
}

// imageHit is a candidate image/text CAPTCHA (no vendor widget).
type imageHit struct {
	Kind     string `json:"kind"` // grid|text
	ImgSel   string `json:"img_sel"`
	InputSel string `json:"input_sel"`
	Tiles    int    `json:"tiles"`
}

// execCall is a parsed grecaptcha(.enterprise).execute(sitekey,{action}) call.
type execCall struct {
	SiteKey    string `json:"sitekey"`
	Action     string `json:"action"`
	Enterprise bool   `json:"enterprise"`
}

// domProbe is the raw scan of a page, produced identically by the browser JS
// probe (computed visibility) and the pure-Go HTML probe (fixtures and
// failed-submit response bodies). Classification is a pure function of it.
type domProbe struct {
	Scripts   []string    `json:"scripts"`
	Iframes   []string    `json:"iframes"`
	Widgets   []widgetHit `json:"widgets"`
	Images    []imageHit  `json:"images"`
	ExecCalls []execCall  `json:"exec_calls"`
	Text      string      `json:"text"`
}

// probeJS scans the live DOM for CAPTCHA signatures and returns JSON matching
// domProbe. Visibility is judged by computed style, not bounding-box size: an
// *invisible* reCAPTCHA still renders a visibly-sized badge iframe and a
// full-width (visibility:hidden) challenge iframe, so size alone false-positives.
const probeJS = `() => {
  const shown = (el) => {
    const r = el.getBoundingClientRect();
    const cs = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && cs.display !== 'none' && cs.visibility !== 'hidden' && cs.opacity !== '0';
  };
  const scripts = Array.from(document.scripts).map(s => s.src).filter(Boolean);
  const iframes = Array.from(document.querySelectorAll('iframe')).map(f => f.src).filter(Boolean);
  const widgets = [];
  const resp = (v) => v==='hcaptcha' ? 'textarea[name="h-captcha-response"]' : v==='turnstile' ? 'input[name="cf-turnstile-response"]' : v==='mock' ? 'input[name="captcha-token"], [data-captcha-token]' : 'textarea[name="g-recaptcha-response"], #g-recaptcha-response';
  const addW = (vendor, el) => { const r = el.getBoundingClientRect(); widgets.push({
    vendor, sitekey: el.getAttribute('data-sitekey')||el.getAttribute('data-site-key')||'',
    field_sel: resp(vendor), visible: shown(el),
    size: el.getAttribute('data-size')||'', callback: el.getAttribute('data-callback')||'',
    action: el.getAttribute('data-action')||'', enterprise: false,
    data_s: el.getAttribute('data-s')||'', rqdata: el.getAttribute('data-rqdata')||'', cdata: el.getAttribute('data-cdata')||'',
    x: Math.round(r.left), y: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height)
  }); };
  document.querySelectorAll('.g-recaptcha:not(.grecaptcha-badge), div[data-sitekey].recaptcha, #g-recaptcha').forEach(el => addW('recaptcha_v2', el));
  document.querySelectorAll('.h-captcha').forEach(el => addW('hcaptcha', el));
  document.querySelectorAll('.cf-turnstile').forEach(el => addW('turnstile', el));
  document.querySelectorAll('.lab-captcha, [data-lab-captcha]').forEach(el => addW('mock', el));
  // Actively shown challenge popup = real interactive block even without a container.
  const addPopup = (vendor, f) => { if (shown(f)) { const r=f.getBoundingClientRect(); widgets.push({vendor, sitekey:'', field_sel: resp(vendor), visible:true, size:'', callback:'', action:'', enterprise:false, data_s:'', rqdata:'', cdata:'', x:Math.round(r.left), y:Math.round(r.top), w:Math.round(r.width), h:Math.round(r.height)}); } };
  document.querySelectorAll('iframe[src*="api2/bframe"]').forEach(f => addPopup('recaptcha_v2', f));
  document.querySelectorAll('iframe[src*="hcaptcha.com"]').forEach(f => { if (/frame=challenge/.test(f.src)) addPopup('hcaptcha', f); });
  // Parse grecaptcha(.enterprise).execute(sitekey,{action}) from inline scripts.
  const execCalls = [];
  const re = /grecaptcha(\.enterprise)?\.execute\(\s*['"]([^'"]+)['"]\s*(?:,\s*\{[^}]*action\s*:\s*['"]([^'"]+)['"])?/g;
  Array.from(document.scripts).filter(s => !s.src).forEach(s => { let m; const t=s.textContent||''; while ((m = re.exec(t))) execCalls.push({sitekey: m[2], action: m[3]||'', enterprise: !!m[1]}); });
  // Image / text CAPTCHA heuristics.
  const images = [];
  document.querySelectorAll('[data-captcha="grid"], .captcha-grid, .rc-imageselect').forEach(el => {
    const tiles = el.querySelectorAll('img').length;
    if (tiles >= 6) images.push({kind:'grid', img_sel:'.tile, img', input_sel:'input[name="captcha-token"], [data-captcha-token]', tiles});
  });
  document.querySelectorAll('img').forEach(img => {
    const s = ((img.id||'')+' '+(img.className||'')+' '+(img.alt||'')+' '+(img.getAttribute('src')||'')).toLowerCase();
    if (!/captcha/.test(s)) return;
    const inp = document.querySelector('input[name*="captcha" i], input[id*="captcha" i], input[maxlength]');
    if (inp && (inp.type==='text'||inp.type==='')) images.push({kind:'text', img_sel:'#'+(img.id||'')||'img', input_sel: inp.id?('#'+inp.id):('input[name="'+inp.name+'"]'), tiles:0});
  });
  const text = (document.body ? (document.body.innerText||'') : '').slice(0, 2000);
  return JSON.stringify({scripts, iframes, widgets, images, exec_calls: execCalls, text});
}`

func probe(ctx context.Context, p *browser.Page) (domProbe, error) {
	var out domProbe
	if p == nil {
		return out, nil
	}
	v, err := p.Eval(ctx, probeJS)
	if err != nil {
		return out, err
	}
	_ = json.Unmarshal([]byte(v.Str()), &out)
	return out, nil
}

// probeHTML builds a domProbe from a static HTML document (fixtures and
// failed-submit response bodies). Visibility is attribute-based: an element is
// shown unless it is hidden, display:none/visibility:hidden, data-size=invisible,
// or the reCAPTCHA badge.
func probeHTML(data []byte) domProbe {
	var pr domProbe
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return pr
	}
	var text strings.Builder
	var textInput string // first captcha-ish / maxlength text input selector
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
			text.WriteByte(' ')
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script":
				if src := attr(n, "src"); src != "" {
					pr.Scripts = append(pr.Scripts, src)
				} else if txt := nodeText(n); txt != "" {
					pr.ExecCalls = append(pr.ExecCalls, parseExecCalls(txt)...)
				}
			case "iframe":
				if src := attr(n, "src"); src != "" {
					pr.Iframes = append(pr.Iframes, src)
				}
			case "input":
				if textInput == "" {
					if sel := captchaInputSel(n); sel != "" {
						textInput = sel
					}
				}
			case "img":
				considerImage(n, &pr)
			case "div", "span", "section":
				if w, ok := widgetFromNode(n); ok {
					pr.Widgets = append(pr.Widgets, w)
				}
				considerGrid(n, &pr)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	// Finalise text-captcha hits with the resolved input selector.
	for i := range pr.Images {
		if pr.Images[i].Kind == "text" && pr.Images[i].InputSel == "" {
			pr.Images[i].InputSel = textInput
		}
	}
	pr.Text = strings.TrimSpace(text.String())
	if len(pr.Text) > 2000 {
		pr.Text = pr.Text[:2000]
	}
	return pr
}

// captchaInputSel returns a selector for a short-text / captcha-labelled answer
// input, or "" if n is not a plausible text-captcha answer field.
func captchaInputSel(n *html.Node) string {
	typ := strings.ToLower(attr(n, "type"))
	if typ != "" && typ != "text" {
		return ""
	}
	name, id := attr(n, "name"), attr(n, "id")
	sig := strings.ToLower(name + " " + id)
	captchaish := strings.Contains(sig, "captcha")
	short := hasAttr(n, "maxlength")
	if !captchaish && !short {
		return ""
	}
	if id != "" {
		return "#" + id
	}
	if name != "" {
		return `input[name="` + name + `"]`
	}
	return ""
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		} else {
			b.WriteString(nodeText(c))
		}
	}
	return b.String()
}

func classOf(n *html.Node) string { return attr(n, "class") }

// widgetFromNode recognises a vendor widget container and extracts its params.
func widgetFromNode(n *html.Node) (widgetHit, bool) {
	cls := classOf(n)
	var vendor string
	switch {
	case containsClass(cls, "grecaptcha-badge"):
		return widgetHit{}, false // the badge is not a challenge
	case containsClass(cls, "g-recaptcha") || attr(n, "id") == "g-recaptcha" || (hasAttr(n, "data-sitekey") && containsClass(cls, "recaptcha")):
		vendor = "recaptcha_v2"
	case containsClass(cls, "h-captcha"):
		vendor = "hcaptcha"
	case containsClass(cls, "cf-turnstile"):
		vendor = "turnstile"
	case containsClass(cls, "lab-captcha") || hasAttr(n, "data-lab-captcha"):
		vendor = "mock"
	default:
		return widgetHit{}, false
	}
	w := widgetHit{
		Vendor:   vendor,
		SiteKey:  firstNonEmpty(attr(n, "data-sitekey"), attr(n, "data-site-key")),
		FieldSel: responseFieldFor(vendor),
		Size:     attr(n, "data-size"),
		Callback: attr(n, "data-callback"),
		Action:   attr(n, "data-action"),
		DataS:    attr(n, "data-s"),
		RQData:   attr(n, "data-rqdata"),
		CData:    attr(n, "data-cdata"),
		Visible:  htmlVisible(n),
	}
	return w, true
}

func responseFieldFor(vendor string) string {
	switch vendor {
	case "hcaptcha":
		return `textarea[name="h-captcha-response"]`
	case "turnstile":
		return `input[name="cf-turnstile-response"]`
	case "mock":
		return `input[name="captcha-token"], [data-captcha-token]`
	default:
		return `textarea[name="g-recaptcha-response"], #g-recaptcha-response`
	}
}

// htmlVisible is the static-HTML visibility heuristic.
func htmlVisible(n *html.Node) bool {
	if hasAttr(n, "hidden") {
		return false
	}
	if strings.EqualFold(attr(n, "data-size"), "invisible") {
		return false
	}
	style := strings.ToLower(strings.ReplaceAll(attr(n, "style"), " ", ""))
	if strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden") || strings.Contains(style, "opacity:0") {
		return false
	}
	return true
}

func considerImage(n *html.Node, pr *domProbe) {
	s := strings.ToLower(attr(n, "id") + " " + classOf(n) + " " + attr(n, "alt") + " " + attr(n, "src"))
	if !strings.Contains(s, "captcha") {
		return
	}
	imgSel := "img"
	if id := attr(n, "id"); id != "" {
		imgSel = "#" + id
	}
	pr.Images = append(pr.Images, imageHit{Kind: "text", ImgSel: imgSel, InputSel: "", Tiles: 0})
}

func considerGrid(n *html.Node, pr *domProbe) {
	cls := classOf(n)
	ctx := strings.ToLower(cls + " " + attr(n, "aria-label") + " " + attr(n, "data-captcha") + " " + nodeText(n))
	grid := strings.EqualFold(attr(n, "data-captcha"), "grid") || containsClass(cls, "captcha-grid") || containsClass(cls, "rc-imageselect") ||
		(strings.Contains(ctx, "captcha") && strings.Contains(ctx, "select all"))
	if !grid {
		return
	}
	tiles := countTag(n, "img")
	if tiles >= 6 {
		pr.Images = append(pr.Images, imageHit{Kind: "grid", ImgSel: ".tile, img", InputSel: `input[name="captcha-token"], [data-captcha-token]`, Tiles: tiles})
	}
}

func countTag(n *html.Node, tag string) int {
	c := 0
	var w func(*html.Node)
	w = func(x *html.Node) {
		if x.Type == html.ElementNode && x.Data == tag {
			c++
		}
		for ch := x.FirstChild; ch != nil; ch = ch.NextSibling {
			w(ch)
		}
	}
	w(n)
	return c
}

var execRe = regexp.MustCompile(`grecaptcha(\.enterprise)?\.execute\(\s*['"]([^'"]+)['"]\s*(?:,\s*\{[^}]*action\s*:\s*['"]([^'"]+)['"])?`)

func parseExecCalls(s string) []execCall {
	var out []execCall
	for _, m := range execRe.FindAllStringSubmatch(s, -1) {
		out = append(out, execCall{SiteKey: m[2], Action: m[3], Enterprise: m[1] != ""})
	}
	return out
}

func containsClass(classAttr, want string) bool {
	for _, f := range strings.Fields(classAttr) {
		if f == want {
			return true
		}
	}
	return false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// ---- classification (shared by both probes) ----

var interstitialPhrases = []string{"just a moment", "checking your browser", "verifying you are human", "verify you are human", "please wait while we check"}

// classify is the single source of truth: given a probe (and out-of-DOM
// signals), return the highest-priority challenge, or ok=false for none.
func classify(pr domProbe, origin string) (Challenge, bool) {
	all := append(append([]string{}, pr.Scripts...), pr.Iframes...)
	lowerText := strings.ToLower(pr.Text)

	// 1. Cloudflare interstitial — a full-page block. Classify only (native may
	//    wait it out; otherwise human handoff).
	if anyScriptContains(all, "challenge-platform") || (anyScriptContains(all, "challenges.cloudflare.com") && hasPhrase(lowerText)) {
		if hasPhrase(lowerText) || anyScriptContains(all, "cdn-cgi/challenge-platform") {
			return Challenge{Vendor: VendorCustom, Kind: KindCustom, PageURL: origin,
				Params: map[string]string{ParamClass: "cloudflare_interstitial"}}, true
		}
	}

	// 2. Visibly-shown interactive widget (checkbox / popup / managed Turnstile /
	//    hCaptcha / lab mock).
	for _, w := range pr.Widgets {
		if !w.Visible || strings.EqualFold(w.Size, "invisible") {
			continue
		}
		v := vendorOf(w.Vendor)
		ch := Challenge{Vendor: v, Kind: KindInteractive, SiteKey: w.SiteKey, PageURL: origin, fieldSel: w.FieldSel, Params: widgetParams(w)}
		if w.W > 0 && w.H > 0 {
			ch.Rect = &RegionClick{X: w.X, Y: w.Y, W: w.W, H: w.H}
		}
		return ch, true
	}

	// 3. Image grid, then 4. text CAPTCHA.
	for _, im := range pr.Images {
		if im.Kind == "grid" {
			return Challenge{Vendor: VendorCustom, Kind: KindImage, PageURL: origin, fieldSel: im.InputSel,
				Params: map[string]string{ParamClass: "image_grid", ParamImgSel: im.ImgSel, ParamInputSel: im.InputSel, ParamTiles: itoa(im.Tiles)}}, true
		}
	}
	for _, im := range pr.Images {
		if im.Kind == "text" {
			return Challenge{Vendor: VendorCustom, Kind: KindImage, PageURL: origin, fieldSel: im.InputSel,
				Params: map[string]string{ParamClass: "text", ParamImgSel: im.ImgSel, ParamInputSel: im.InputSel}}, true
		}
	}

	// 5. Arkose/FunCaptcha and GeeTest — classify only, route to human handoff.
	if anyScriptContains(all, "arkoselabs.com") || anyScriptContains(all, "funcaptcha") {
		return Challenge{Vendor: VendorArkose, Kind: KindCustom, PageURL: origin, Params: map[string]string{ParamClass: "arkose"}}, true
	}
	if anyScriptContains(all, "geetest.com") || anyScriptContains(all, "/gt.") {
		return Challenge{Vendor: VendorCustom, Kind: KindCustom, PageURL: origin, Params: map[string]string{ParamClass: "geetest"}}, true
	}

	// 6. Score / invisible — reCAPTCHA v3 / Enterprise (render= or execute()),
	//    invisible reCAPTCHA v2, invisible Turnstile. Non-blocking.
	if ch, ok := classifyScore(pr, all, origin); ok {
		return ch, true
	}
	return Challenge{}, false
}

func classifyScore(pr domProbe, all []string, origin string) (Challenge, bool) {
	// Explicit execute() calls carry the richest params.
	for _, e := range pr.ExecCalls {
		params := map[string]string{ParamAction: e.Action}
		if e.Enterprise {
			params[ParamEnterprise] = "true"
		}
		return Challenge{Vendor: VendorRecaptchaV3, Kind: KindScore, SiteKey: e.SiteKey, PageURL: origin, Params: params}, true
	}
	// render=<sitekey> script param (v3 or Enterprise).
	for _, s := range all {
		if sk := siteKeyFromRender(s); sk != "" && strings.Contains(s, "recaptcha") {
			params := map[string]string{}
			if strings.Contains(s, "enterprise.js") {
				params[ParamEnterprise] = "true"
			}
			return Challenge{Vendor: VendorRecaptchaV3, Kind: KindScore, SiteKey: sk, PageURL: origin, Params: params}, true
		}
	}
	// Invisible widget containers (reCAPTCHA invisible / invisible Turnstile).
	for _, w := range pr.Widgets {
		if strings.EqualFold(w.Size, "invisible") {
			return Challenge{Vendor: vendorOf(w.Vendor), Kind: KindScore, SiteKey: w.SiteKey, PageURL: origin, Params: widgetParams(w)}, true
		}
	}
	return Challenge{}, false
}

func widgetParams(w widgetHit) map[string]string {
	p := map[string]string{}
	put := func(k, v string) {
		if v != "" {
			p[k] = v
		}
	}
	put(ParamCallback, w.Callback)
	put(ParamAction, w.Action)
	put(ParamDataS, w.DataS)
	put(ParamRQData, w.RQData)
	put(ParamCData, w.CData)
	put(ParamSize, w.Size)
	if w.Enterprise {
		p[ParamEnterprise] = "true"
	}
	return p
}

func vendorOf(s string) Vendor {
	switch s {
	case "recaptcha_v2":
		return VendorRecaptchaV2
	case "recaptcha_v3":
		return VendorRecaptchaV3
	case "hcaptcha":
		return VendorHCaptcha
	case "turnstile":
		return VendorTurnstile
	case "mock":
		return VendorMock
	default:
		return VendorCustom
	}
}

func anyScriptContains(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func hasPhrase(lowerText string) bool {
	for _, p := range interstitialPhrases {
		if strings.Contains(lowerText, p) {
			return true
		}
	}
	return false
}

func siteKeyFromRender(src string) string {
	if i := strings.Index(src, "render="); i >= 0 {
		rest := src[i+len("render="):]
		if j := strings.IndexAny(rest, "&#"); j >= 0 {
			rest = rest[:j]
		}
		if rest != "explicit" && rest != "onload" {
			return rest
		}
	}
	return ""
}

func origin(p *browser.Page) string {
	if p == nil {
		return ""
	}
	return p.Origin()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// ---- Detector facade (unchanged public surface) ----

// unifiedDetector runs one probe and classifies all vendors/classes. It also
// honours out-of-DOM signals: script srcs seen on the page and a failed-submit
// response body (a login POST rejected with a captcha error).
type unifiedDetector struct{}

// Vendor implements Detector.
func (unifiedDetector) Vendor() Vendor { return VendorCustom }

// Detect implements Detector.
func (unifiedDetector) Detect(ctx context.Context, p *browser.Page, _ perceive.State, sig NetSignals) (Challenge, bool, error) {
	pr, err := probe(ctx, p)
	if err != nil {
		pr = domProbe{Scripts: sig.ScriptSrcs}
	} else {
		pr.Scripts = append(pr.Scripts, sig.ScriptSrcs...)
	}
	if ch, ok := classify(pr, origin(p)); ok {
		return ch, true, nil
	}
	// Detection from a failed-submit response body (captcha appeared only after
	// the first login attempt was rejected).
	if sig.FailedSubmit && sig.FailedSubmitBody != "" {
		if ch, ok := classify(probeHTML([]byte(sig.FailedSubmitBody)), origin(p)); ok {
			return ch, true, nil
		}
	}
	return Challenge{}, false, nil
}

// DefaultDetectors returns the built-in detection (one unified detector).
func DefaultDetectors() []Detector { return []Detector{unifiedDetector{}} }

// DetectAny runs detectors in order and returns the first challenge found.
func DetectAny(ctx context.Context, dets []Detector, p *browser.Page, st perceive.State, sig NetSignals) (Challenge, bool) {
	for _, d := range dets {
		if c, ok, err := d.Detect(ctx, p, st, sig); err == nil && ok {
			return c, true
		}
	}
	return Challenge{}, false
}
