package verify

import (
	"html"
	"net/url"
	"regexp"
	"strings"
)

// Deterministic extraction of one-time codes and verification links from a
// single message. Used by the SMS and email providers. The LLM is only a
// fallback the caller invokes when these return ambiguous (ok=false).

var (
	// A code token: 4–8 digits, or 5–8 alphanumerics with at least one digit.
	codeTokenRe = regexp.MustCompile(`\b([0-9]{4,8}|[A-Z0-9]{5,8})\b`)
	// Words that sit next to a verification code.
	codeKeywordRe = regexp.MustCompile(`(?i)\b(code|otp|one[- ]?time|verification|verify|passcode|pin|security code|confirm(ation)?|auth(entication)?)\b`)
	// Tokens that look like something else and must not be read as a code.
	phoneLikeRe = regexp.MustCompile(`^\+?\d[\d\s().-]{7,}$`)
	digitsOnly  = regexp.MustCompile(`^[0-9]+$`)
	linkRe      = regexp.MustCompile(`https?://[^\s"'<>)\]]+`)
)

// codeCandidate is a token with its distance (in bytes) to the nearest keyword.
type codeCandidate struct {
	value   string
	dist    int
	hasKey  bool
	numeric bool
}

// ExtractCode returns the most likely one-time code in text, and ok=false when
// nothing plausible is found or the best candidate is ambiguous.
func ExtractCode(text string) (string, bool) {
	cands := codeCandidates(text)
	if len(cands) == 0 {
		return "", false
	}
	// Best = nearest to a keyword; prefer ones that actually have a keyword
	// nearby, then shorter distance, then numeric codes.
	best := cands[0]
	for _, c := range cands[1:] {
		if better(c, best) {
			best = c
		}
	}
	// With no keyword anywhere and more than one candidate, it is ambiguous.
	if !best.hasKey && len(cands) > 1 {
		return "", false
	}
	return best.value, true
}

func better(a, b codeCandidate) bool {
	if a.hasKey != b.hasKey {
		return a.hasKey
	}
	if a.dist != b.dist {
		return a.dist < b.dist
	}
	if a.numeric != b.numeric {
		return a.numeric // prefer a numeric code on a tie
	}
	return false
}

func codeCandidates(text string) []codeCandidate {
	keyLocs := codeKeywordRe.FindAllStringIndex(text, -1)
	var out []codeCandidate
	for _, m := range codeTokenRe.FindAllStringIndex(text, -1) {
		tok := text[m[0]:m[1]]
		if !plausibleCode(tok, text, m[0]) {
			continue
		}
		dist, has := nearestKeyword(m[0], m[1], keyLocs)
		out = append(out, codeCandidate{value: tok, dist: dist, hasKey: has, numeric: digitsOnly.MatchString(tok)})
	}
	return out
}

// plausibleCode rejects tokens that are part of a phone number, a date/amount,
// or a long digit run (account numbers etc.).
func plausibleCode(tok, text string, at int) bool {
	// Immediately adjacent to currency / phone punctuation?
	if at > 0 {
		switch text[at-1] {
		case '$', '.', '-', '+', '/', ':':
			return false
		}
	}
	if end := at + len(tok); end < len(text) {
		switch text[end] {
		case '.', '-', '/', ':', '%':
			// A trailing '.' ending a sentence is fine; a decimal/date is not.
			if text[end] != '.' || (end+1 < len(text) && text[end+1] >= '0' && text[end+1] <= '9') {
				return false
			}
		}
	}
	// Part of a longer phone-like run around it.
	lo := at
	for lo > 0 && isPhoneByte(text[lo-1]) {
		lo--
	}
	hi := at + len(tok)
	for hi < len(text) && isPhoneByte(text[hi]) {
		hi++
	}
	if hi-lo > len(tok) && phoneLikeRe.MatchString(strings.TrimSpace(text[lo:hi])) {
		return false
	}
	return true
}

func isPhoneByte(b byte) bool {
	return (b >= '0' && b <= '9') || b == '+' || b == '-' || b == ' ' || b == '(' || b == ')' || b == '.'
}

func nearestKeyword(start, end int, keyLocs [][]int) (int, bool) {
	best := 1 << 30
	for _, k := range keyLocs {
		d := 0
		switch {
		case k[1] <= start:
			d = start - k[1]
		case k[0] >= end:
			d = k[0] - end
		default:
			d = 0
		}
		if d < best {
			best = d
		}
	}
	// "near" means within ~40 bytes.
	return best, best <= 40
}

// Link extraction --------------------------------------------------------------

var (
	linkKeywordRe = regexp.MustCompile(`(?i)(verify|confirm|activate|magic|sign[- ]?in|log[- ]?in|validate|authenticate|account)`)
	// Links that are never a verification target, whatever else they contain.
	// NOTE: utm_ and click-tracking redirects are deliberately NOT rejected here —
	// a genuine verification link often carries utm_ params or is wrapped by a
	// click-tracker; those are handled by stripping params / scoring anchor text.
	linkRejectRe = regexp.MustCompile(`(?i)(unsubscribe|/privacy|/help|/support|/terms|facebook\.com|twitter\.com|x\.com/|instagram\.com|linkedin\.com|/pixel|\.png|\.gif|\.jpg|mailto:)`)
	tokenParamRe = regexp.MustCompile(`(?i)[?&](token|code|t|k|key|otp|confirmation|verification|oob[cC]ode|hash)=`)
	// Known click-tracking / redirect hosts: the real destination is hidden, so
	// such a link is scored by its visible anchor text instead of its URL.
	trackerHostRe = regexp.MustCompile(`(?i)^https?://[^/]*(sendgrid\.net|list-manage\.com|mailchimp\.com|mandrillapp\.com|sparkpostmail|hubspotlinks|hs-sendgrid|rs6\.net|awstrack\.me|sendgrid\.com|ct\.sendgrid|clicks?\.|links?\.|email\.|e\.|track|redirect)`)
	// <a href="URL">anchor text</a>, so a link can be scored by what it says.
	anchorRe = regexp.MustCompile(`(?is)<a\b[^>]*\bhref\s*=\s*["']([^"']+)["'][^>]*>(.*?)</a>`)
	tagRe    = regexp.MustCompile(`(?s)<[^>]*>`)
)

// candidate is one extracted link with the anchor text it was shown under.
type candidate struct {
	url, anchor string
}

// ExtractLink returns the most likely verification link in a message. text may
// be plain text or raw HTML (the caller passes both concatenated); targetDomain
// is the app's domain (may be empty). ok=false when nothing plausible is found.
//
// It decodes HTML entities (so `&amp;` in a query string is not left in the
// URL), reads <a href> anchors so a click-tracking redirect can be scored by its
// visible text, and strips tracking params (utm_*, etc.) rather than rejecting a
// link that carries them.
func ExtractLink(text, targetDomain string) (string, bool) {
	cands := linkCandidates(text)
	if len(cands) == 0 {
		return "", false
	}
	best, bestScore := "", -1<<30
	for _, c := range cands {
		u := cleanURL(c.url)
		if u == "" || linkRejectRe.MatchString(u) {
			continue
		}
		score := 0
		tracker := trackerHostRe.MatchString(u)
		if linkKeywordRe.MatchString(u) {
			score += 3
		}
		if tokenParamRe.MatchString(u) {
			score += 3
		}
		if targetDomain != "" && strings.Contains(u, targetDomain) {
			score += 2
		}
		// Score by anchor text too — decisive for a tracker whose URL is opaque.
		if a := strings.ToLower(c.anchor); a != "" && linkKeywordRe.MatchString(a) {
			if tracker {
				score += 3
			} else {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = u, score
		}
	}
	if best == "" || bestScore <= 0 {
		return "", false
	}
	return best, true
}

// linkCandidates collects links from <a href> anchors (with their text) and from
// bare URLs in the text. HTML entities are decoded first.
func linkCandidates(text string) []candidate {
	decoded := html.UnescapeString(text)
	var out []candidate
	seen := map[string]bool{}
	for _, m := range anchorRe.FindAllStringSubmatch(text, -1) {
		href := html.UnescapeString(strings.TrimSpace(m[1]))
		anchor := strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(m[2], " ")))
		if strings.HasPrefix(strings.ToLower(href), "http") {
			out = append(out, candidate{url: href, anchor: anchor})
			seen[cleanURL(href)] = true
		}
	}
	for _, u := range linkRe.FindAllString(decoded, -1) {
		cu := cleanURL(u)
		if cu != "" && !seen[cu] {
			out = append(out, candidate{url: u})
			seen[cu] = true
		}
	}
	return out
}

// cleanURL trims trailing punctuation and strips tracking query params (utm_*,
// mc_*, and similar) while preserving the rest of the URL.
func cleanURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), ".,);]>\"'")
	parsed, err := url.Parse(u)
	if err != nil {
		return u
	}
	q := parsed.Query()
	removed := false
	for k := range q {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "utm_") || strings.HasPrefix(lk, "mc_") ||
			lk == "mkt_tok" || lk == "_hsenc" || lk == "_hsmi" || lk == "gclid" || lk == "fbclid" {
			q.Del(k)
			removed = true
		}
	}
	if !removed {
		return u // leave a clean URL byte-for-byte unchanged
	}
	parsed.RawQuery = q.Encode()
	return parsed.String()
}
