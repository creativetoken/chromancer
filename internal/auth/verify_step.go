package auth

import (
	"regexp"
	"strings"

	"github.com/creativetoken/chromancer/internal/perceive"
	"github.com/creativetoken/chromancer/internal/verify"
)

var (
	codeFieldRe = regexp.MustCompile(`(?i)(otp|one.?time|verification|verify|confirm|security|auth(enticator)?|code|token|passcode|2fa|mfa)`)
	codeTextRe  = regexp.MustCompile(`(?i)(enter (the|your) code|verification code|one.?time (code|password)|we (sent|emailed|texted)|check your (email|phone|inbox|messages)|confirm your (email|account)|6.?digit|authenticator app)`)
	magicLinkRe = regexp.MustCompile(`(?i)(magic link|click the link|sign.?in link|we (sent|emailed) you a link|check your (email|inbox).{0,40}link|link to (sign|log) in)`)
	totpTextRe  = regexp.MustCompile(`(?i)(authenticator app|google authenticator|authy|totp|2fa code|time.?based)`)
	smsTextRe   = regexp.MustCompile(`(?i)(text message|sms|your phone|texted|phone number)`)
	emailTextRe = regexp.MustCompile(`(?i)(email|inbox|e-?mail)`)
	hintMaskRe  = regexp.MustCompile(`(?i)(sent|emailed|texted)[^.]{0,60}?([*•x\x{2022}]{2,}[\dx*]{0,4}|\b[\w.+-]+@[\w-]+\.\w+|\+?\d[\d\s().-]{5,})`)
	resendRe    = regexp.MustCompile(`(?i)(resend|send again|didn'?t (get|receive)|new code)`)
)

// VerifyStep describes a detected verification challenge on the page.
type VerifyStep struct {
	Needed    bool
	Channel   verify.Channel
	Hint      string
	CodeField *perceive.Node
	Submit    *perceive.Node
	LinkOnly  bool // page expects us to open a link, no code field
	Resend    *perceive.Node
}

// DetectVerifyStep inspects a state for a verification challenge: a one-time
// code input, or copy instructing us to open a magic link.
func DetectVerifyStep(st perceive.State) VerifyStep {
	text := strings.ToLower(st.Title + " " + strings.Join(st.Headings, " ") + " " + st.Text)
	f := Classify(st)
	var codeField, submit, resend *perceive.Node
	for i := range st.Nodes {
		n := &st.Nodes[i]
		if !n.Visible || n.Disabled {
			continue
		}
		if n.Tag == "input" && (n.Type == "text" || n.Type == "tel" || n.Type == "number" || n.Type == "" || n.Type == "password") {
			if n.Autocomplete == "one-time-code" || codeFieldRe.MatchString(n.Name) || codeFieldRe.MatchString(n.NameAttr) || codeFieldRe.MatchString(n.ID) || codeFieldRe.MatchString(n.Placeholder) {
				if codeField == nil {
					codeField = n
				}
			}
		}
		label := n.Name
		if label == "" {
			label = n.Text
		}
		if (n.Tag == "button" || n.Role == "button" || n.Tag == "a") && resendRe.MatchString(label) {
			resend = n
		}
	}
	submit = f.Submit

	step := VerifyStep{CodeField: codeField, Submit: submit, Resend: resend}
	hasCodeCopy := codeTextRe.MatchString(text)
	hasLinkCopy := magicLinkRe.MatchString(text)

	switch {
	case codeField != nil && f.Password == nil:
		step.Needed = true
	case hasLinkCopy && codeField == nil && f.Password == nil:
		step.Needed = true
		step.LinkOnly = true
	case hasCodeCopy && f.Password == nil && f.Identifier == nil:
		step.Needed = true
	default:
		return VerifyStep{}
	}

	// Channel detection.
	switch {
	case totpTextRe.MatchString(text):
		step.Channel = verify.TOTP
	case step.LinkOnly:
		step.Channel = verify.Email
	case smsTextRe.MatchString(text) && !emailTextRe.MatchString(text):
		step.Channel = verify.SMS
	case emailTextRe.MatchString(text):
		step.Channel = verify.Email
	default:
		step.Channel = verify.Email
	}
	if m := hintMaskRe.FindString(st.Text); m != "" {
		step.Hint = strings.TrimSpace(m)
	}
	return step
}
