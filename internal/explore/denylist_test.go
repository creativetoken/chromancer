package explore

import "testing"

func TestDenyReasonSeparatorForms(t *testing.T) {
	blocked := []struct{ label, href string }{
		{"Delete account", ""},
		{"", "/crawl/delete-account"},
		{"", "/settings/delete_account"},
		{"", "https://app.example.com/account/delete-account?confirm=1"},
		{"Log out", ""},
		{"", "/logout"},
		{"", "/user/sign-out"},
		{"Change password", ""},
		{"", "/settings/change-password"},
		{"API keys", ""},
		{"", "/settings/api-keys"},
		{"Revoke", ""},
		{"", "/dev/revoke-token"},
	}
	for _, c := range blocked {
		if DenyReason(c.label, c.href) == "" {
			t.Errorf("expected DenyReason to block label=%q href=%q", c.label, c.href)
		}
	}

	allowed := []struct{ label, href string }{
		{"Projects", "/projects"},
		{"Items", "/crawl/items"},
		{"Create account", "/signup"}, // signup is not account-ending
		{"Settings", "/settings"},
		{"Save settings", ""},
	}
	for _, c := range allowed {
		if r := DenyReason(c.label, c.href); r != "" {
			t.Errorf("expected DenyReason to allow label=%q href=%q, got %q", c.label, c.href, r)
		}
	}
}
