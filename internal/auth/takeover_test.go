package auth

import (
	"testing"

	"github.com/creativetoken/chromancer/internal/captcha"
	"github.com/creativetoken/chromancer/internal/perceive"
)

func TestIsBlockPage(t *testing.T) {
	block := []string{
		"Verify you are human",
		"Please verify you're human to continue",
		"Access denied",
		"We've detected unusual traffic from your network",
		"Are you a robot?",
		"Too many requests, please try again later",
		"Press and hold to confirm you are human",
	}
	for _, s := range block {
		if !auth_isBlockPage(s) {
			t.Errorf("expected %q to be a block page", s)
		}
	}
	for _, s := range []string{"", "Welcome back", "Enter your verification code", "Trust this device?", "Is this you?"} {
		if auth_isBlockPage(s) {
			t.Errorf("did NOT expect %q to be a block page", s)
		}
	}
}

// auth_isBlockPage wraps IsBlockPage with a page whose Text is s.
func auth_isBlockPage(s string) bool {
	return IsBlockPage(perceive.State{Text: s})
}

func TestIsCredentialsRejection(t *testing.T) {
	reject := []string{
		"Invalid email or password.",
		"Incorrect password",
		"Wrong password, try again",
		"That email or password is incorrect",
		"Invalid login",
		"We don't recognise that username",
		"Invalid credentials",
	}
	for _, s := range reject {
		if !isCredentialsRejection(s) {
			t.Errorf("expected %q to be a credentials rejection", s)
		}
	}
	// Non-credential form errors / verification errors must NOT be classified as a
	// hard credentials rejection (they are recoverable).
	notReject := []string{
		"",
		"That verification code is incorrect",
		"Please accept the terms to continue",
		"Something went wrong, please try again",
		"Your account is locked",
		"Too many attempts",
	}
	for _, s := range notReject {
		if isCredentialsRejection(s) {
			t.Errorf("did NOT expect %q to be a credentials rejection", s)
		}
	}
}

func TestAgentEligible(t *testing.T) {
	eligible := []captcha.BlockReason{captcha.ReasonStuck, captcha.ReasonUnknownFlow, captcha.ReasonVerifyTimeout}
	for _, r := range eligible {
		if !agentEligible(r) {
			t.Errorf("expected %s to be agent-eligible", r)
		}
	}
	// Bot/block-page and captcha-exhausted go straight to a human, never the agent.
	for _, r := range []captcha.BlockReason{captcha.ReasonBlockPage, captcha.ReasonCaptchaExhausted, captcha.ReasonChallengeLoop} {
		if agentEligible(r) {
			t.Errorf("did NOT expect %s to be agent-eligible", r)
		}
	}
}

func TestParseRecoveryMode(t *testing.T) {
	cases := map[string]RecoveryMode{
		"":      RecoveryAgent,
		"agent": RecoveryAgent,
		"AGENT": RecoveryAgent,
		"human": RecoveryHuman,
		"off":   RecoveryOff,
		"none":  RecoveryOff,
	}
	for in, want := range cases {
		got, ok := ParseRecoveryMode(in)
		if !ok || got != want {
			t.Errorf("ParseRecoveryMode(%q) = %v, %v; want %v, true", in, got, ok, want)
		}
	}
	if _, ok := ParseRecoveryMode("banana"); ok {
		t.Errorf("ParseRecoveryMode(banana) should be invalid")
	}
}
