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
	// confirmSubmitRe matches a verification submit button label. Anchored words
	// so it does not fire on "Log in" / "Register" / "Forgot password".
	confirmSubmitRe = regexp.MustCompile(`(?i)\b(confirm|verify|continue|submit|next|done)\b`)
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

	// Visual entry: the code field is not perceivable (closed shadow root), so the
	// code is typed by coordinate — focus VisualX,VisualY (viewport CSS px) then
	// type into focus. Set by the agent path when the model supplies coordinates.
	Visual  bool
	VisualX int
	VisualY int
}

// DetectVerifyStep inspects a state for a verification challenge: a one-time
// code input, or copy instructing us to open a magic link.
func DetectVerifyStep(st perceive.State) VerifyStep {
	text := strings.ToLower(st.Title + " " + strings.Join(st.Headings, " ") + " " + st.Text)
	f := Classify(st)
	// DOM order of the credential inputs; handles increase with document order, so
	// a code field whose handle is larger sits below the email/password (a strong
	// step-up signal that is independent of any on-page copy or language).
	credPos := -1
	if f.Identifier != nil && f.Identifier.Handle > credPos {
		credPos = f.Identifier.Handle
	}
	if f.Password != nil && f.Password.Handle > credPos {
		credPos = f.Password.Handle
	}
	var codeField, submit, resend, confirm *perceive.Node
	bestScore := 0
	for i := range st.Nodes {
		n := &st.Nodes[i]
		if !n.Visible || n.Disabled {
			continue
		}
		// Score candidate one-time-code inputs by structural (language-independent)
		// signals, ignoring the classified identifier and password fields.
		if n.Tag == "input" && isTextualInput(n.Type) && n != f.Identifier && n != f.Password {
			if sc := otpFieldStrength(n, credPos >= 0 && n.Handle > credPos); sc > bestScore {
				bestScore, codeField = sc, n
			}
		}
		label := n.Name
		if label == "" {
			label = n.Text
		}
		if n.Tag == "button" || n.Role == "button" || n.Tag == "a" {
			if resendRe.MatchString(label) {
				resend = n
			} else if confirm == nil && confirmSubmitRe.MatchString(label) {
				// A verify/confirm/continue-style submit, preferred over a generic
				// "Log in" when a code field is revealed in the same form.
				confirm = n
			}
		}
	}
	// On a combined credentials+code form, prefer a confirm/verify button over
	// the classifier's primary submit (often still "Log in") so the code is the
	// field that gets submitted.
	submit = f.Submit
	if confirm != nil {
		submit = confirm
	}

	step := VerifyStep{CodeField: codeField, Submit: submit, Resend: resend}
	hasCodeCopy := codeTextRe.MatchString(text)
	hasLinkCopy := magicLinkRe.MatchString(text)
	// A code field distinct from the identifier and password inputs. Some sites
	// keep email + password on the page and reveal a code field in the same form
	// for step-up verification (e.g. Bambu Lab's sign-in: account + password +
	// "Verification code" all visible, one "Confirm" button). That is still a
	// verification step even though a password input is present. We treat it as a
	// code step when the field is structurally strong (otpStrong: e.g.
	// autocomplete=one-time-code, or numeric inputmode with a short maxlength),
	// which needs no on-page wording, OR when the copy asks for a code — so a
	// localized form with no English copy is still recognised.
	distinctCode := codeField != nil // already excludes the identifier/password

	switch {
	case codeField != nil && f.Password == nil && (bestScore >= otpStrong || hasCodeCopy || codeFieldNamed(codeField)):
		step.Needed = true
	case distinctCode && (bestScore >= otpStrong || hasCodeCopy):
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

// otpStrong is the structural score at which a field is treated as a one-time
// code input with no supporting on-page copy (so detection is language-neutral).
const otpStrong = 3

// isTextualInput reports whether an input type can hold a typed one-time code.
// type=password is excluded: OTP fields are virtually never masked, and
// including it would mistake a "confirm password" signup field for a code field.
func isTextualInput(t string) bool {
	switch t {
	case "", "text", "tel", "number":
		return true
	}
	return false
}

// codeFieldNamed reports whether a field's attributes name it as a verification
// code. Language-dependent, so used only as a secondary signal.
func codeFieldNamed(n *perceive.Node) bool {
	return n.Autocomplete == "one-time-code" ||
		codeFieldRe.MatchString(n.Name) || codeFieldRe.MatchString(n.NameAttr) ||
		codeFieldRe.MatchString(n.ID) || codeFieldRe.MatchString(n.Placeholder)
}

// otpFieldStrength scores how strongly an input looks like a one-time-code field
// from structural, language-independent signals. autocomplete=one-time-code is
// decisive on its own; otherwise a numeric inputmode, a numeric/tel type, a
// short maxlength (4..8, the usual code length), and a position below the
// credential fields combine. A matching name/placeholder adds a weak boost.
func otpFieldStrength(n *perceive.Node, belowCreds bool) int {
	s := 0
	if n.Autocomplete == "one-time-code" {
		s += 3
	}
	if n.InputMode == "numeric" {
		s++
	}
	if n.Type == "number" || n.Type == "tel" {
		s++
	}
	if n.MaxLength >= 4 && n.MaxLength <= 8 {
		s += 2
	}
	if belowCreds {
		s++
	}
	if codeFieldRe.MatchString(n.Name) || codeFieldRe.MatchString(n.NameAttr) ||
		codeFieldRe.MatchString(n.ID) || codeFieldRe.MatchString(n.Placeholder) {
		s++
	}
	return s
}
