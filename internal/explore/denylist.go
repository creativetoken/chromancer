// Package explore is Phase 2: an authenticated crawl that starts from a saved
// session and uses the app like a normal user, deduping visited views, never
// taking account-ending actions, and re-logging-in if the session drops.
package explore

import (
	"regexp"
	"strings"
)

// accountSafetyRe matches controls that would end or damage our account — the
// only guardrail the crawl enforces (ARCHITECTURE §9). It is about ACTIONS, not
// hosts; there is still no scope allowlist. Input is normalised first (see
// normalizeDeny) so URL/path forms like "delete-account", "delete_account" and
// "/settings/delete-account" match the same as the visible label "Delete
// account".
var accountSafetyRe = regexp.MustCompile(`(?i)(log\s?out|sign\s?out|delete (my |your )?account|close (my |your )?account|deactivate|change (your |my )?(password|email)|update (your |my )?(password|email)|revoke|reset password|remove account|cancel (my )?account|api\s?keys?)`)

var denySepRe = regexp.MustCompile(`[-_/.?&=+]+`)

// normalizeDeny lowercases and turns URL/identifier separators into spaces so a
// hyphenated or path-encoded action name matches the space-separated pattern.
func normalizeDeny(s string) string {
	return strings.TrimSpace(denySepRe.ReplaceAllString(strings.ToLower(s), " "))
}

// DenyReason returns a non-empty reason if the label/href names an
// account-safety action (logout, delete account, change creds, revoke, API
// keys). Both the visible label and the href/URL are checked after normalising
// separators, so "/crawl/delete-account" is caught as well as "Delete account".
func DenyReason(label, href string) string {
	if label != "" && accountSafetyRe.MatchString(normalizeDeny(label)) {
		return "label:" + label
	}
	if href != "" && accountSafetyRe.MatchString(normalizeDeny(href)) {
		return "href:" + href
	}
	return ""
}
