package captcha

import "testing"

func TestResultPayload(t *testing.T) {
	if got := (Result{Kind: ResultToken, Token: "tok123"}).Payload(); got != "tok123" {
		t.Errorf("token payload = %q", got)
	}
	if got := (Result{Kind: ResultText, Text: "A1B2C3"}).Payload(); got != "A1B2C3" {
		t.Errorf("text payload = %q", got)
	}
	if got := (Result{Kind: ResultUIActions, Actions: []RegionClick{{X: 1}}}).Payload(); got != "" {
		t.Errorf("uiactions payload should be empty, got %q", got)
	}
	if got := (Result{Kind: ResultHandoff}).Payload(); got != "" {
		t.Errorf("handoff payload should be empty, got %q", got)
	}
}
