// Package explore is Phase 2: an authenticated crawl that starts from a saved
// session and uses the app like a normal user, deduping visited views, never
// taking account-ending actions, and re-logging-in if the session drops.
package explore

import "regexp"

// accountSafetyRe matches controls that would end or damage our account — the
// only guardrail the crawl enforces (ARCHITECTURE §9). It is about ACTIONS, not
// hosts; there is still no scope allowlist.
var accountSafetyRe = regexp.MustCompile(`(?i)(log\s?out|sign\s?out|delete (my |your )?account|close (my |your )?account|deactivate|change (your |my )?(password|email)|update (your |my )?(password|email)|revoke|reset password|remove account|cancel (my )?account|api\s?keys?)`)

// DenyReason returns a non-empty reason if the label/href names an
// account-safety action (logout, delete account, change creds, revoke).
func DenyReason(label, href string) string {
	if accountSafetyRe.MatchString(label) {
		return "label:" + label
	}
	if href != "" && accountSafetyRe.MatchString(href) {
		return "href:" + href
	}
	return ""
}
