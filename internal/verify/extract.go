package verify

import (
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
	linkRejectRe  = regexp.MustCompile(`(?i)(unsubscribe|privacy|help|support|terms|facebook|twitter|instagram|linkedin|/track|/pixel|utm_|\.png|\.gif|\.jpg|mailto:)`)
	tokenParamRe  = regexp.MustCompile(`(?i)[?&](token|code|t|k|key|otp|confirmation|verification|oobCode|hash)=`)
)

// ExtractLink returns the most likely verification link in a message. text is
// plain text (HTML already converted); targetDomain is the app's domain (may be
// empty). ok=false when nothing plausible is found.
func ExtractLink(text, targetDomain string) (string, bool) {
	urls := linkRe.FindAllString(text, -1)
	if len(urls) == 0 {
		return "", false
	}
	best, bestScore := "", -1<<30
	for _, u := range urls {
		u = strings.TrimRight(u, ".,);]")
		if linkRejectRe.MatchString(u) {
			continue
		}
		score := 0
		if linkKeywordRe.MatchString(u) {
			score += 3
		}
		if tokenParamRe.MatchString(u) {
			score += 3
		}
		if targetDomain != "" && strings.Contains(u, targetDomain) {
			score += 2
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
