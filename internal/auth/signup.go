package auth

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// accountExistsRe matches an app telling us the account already exists.
var accountExistsRe = regexp.MustCompile(`(?i)(already (exists|registered|taken|in use|have an account)|email .*taken|account .*exists|user .*exists|try (logging|signing) in)`)

// IsAccountExists reports whether a state shows an "account already exists" error.
func IsAccountExists(st perceive.State) bool {
	return accountExistsRe.MatchString(st.Text) || accountExistsRe.MatchString(strings.Join(st.Headings, " "))
}

// SignupHandler fills a signup form with the operator credentials plus profile
// fields (generated when absent) and submits. Credentials are never generated —
// only incidental fields.
type SignupHandler struct {
	Profile identity.Profile
}

// Variant implements FlowHandler.
func (SignupHandler) Variant() string { return "signup" }

// CanHandle implements FlowHandler: a form with a password field (signup forms
// look like login forms plus extra fields; the Authenticator chooses this
// handler by mode, so CanHandle just needs a password present).
func (SignupHandler) CanHandle(st perceive.State) bool {
	return Classify(st).Password != nil
}

var (
	nameFieldRe   = regexp.MustCompile(`(?i)(^|[^a-z])(name|full.?name|your.?name)([^a-z]|$)`)
	firstNameRe   = regexp.MustCompile(`(?i)(first.?name|given.?name|fname)`)
	lastNameRe    = regexp.MustCompile(`(?i)(last.?name|surname|family.?name|lname)`)
	companyRe     = regexp.MustCompile(`(?i)(company|organ[iz]ation|org|business)`)
	confirmPassRe = regexp.MustCompile(`(?i)(confirm|repeat|re.?enter|verify|again)`)
	usernameRe    = regexp.MustCompile(`(?i)(^|[^a-z])(user.?name|handle|display.?name)([^a-z]|$)`)
	acceptTermsRe = regexp.MustCompile(`(?i)(agree|accept|terms|privacy|consent|i am|over 1\d)`)
)

// Run implements FlowHandler.
func (h SignupHandler) Run(ctx context.Context, st perceive.State, creds identity.Credentials, d FlowDeps) error {
	a := actor{d}
	f := Classify(st)
	if f.Password == nil {
		return fmt.Errorf("%w: signup has no password field", ErrUnknownFlow)
	}
	// Identifier (email/username).
	if f.Identifier != nil {
		if err := a.typeInto(ctx, f.Identifier, creds.UsernameOrEmail, "identifier"); err != nil {
			return err
		}
	}
	// Incidental fields + confirm-password + checkboxes, in handle order.
	for i := range st.Nodes {
		n := &st.Nodes[i]
		if !n.Visible || n.Disabled {
			continue
		}
		if f.Identifier != nil && n.Handle == f.Identifier.Handle {
			continue
		}
		switch {
		case n.Tag == "input" && n.Type == "password":
			val := creds.Password
			if n.Handle != f.Password.Handle {
				label := n.Name + " " + n.Placeholder + " " + n.NameAttr + " " + n.ID
				if confirmPassRe.MatchString(label) {
					val = creds.Password // confirm matches
				} else {
					continue // first password handled below once
				}
			}
			if err := a.typeInto(ctx, n, val, "password"); err != nil {
				return err
			}
		case n.Tag == "input" && (n.Type == "text" || n.Type == "email" || n.Type == "tel" || n.Type == ""):
			val := h.profileValue(n, creds)
			if val == "" {
				continue
			}
			if err := a.typeInto(ctx, n, val, "profile"); err != nil {
				return err
			}
		case n.Tag == "input" && n.Type == "checkbox":
			label := n.Name + " " + n.Text
			if acceptTermsRe.MatchString(label) {
				_ = a.click(ctx, n, "accept")
			}
		}
	}
	d.Trace("signup: filled form, submitting")
	if err := a.submit(ctx, f, f.Password); err != nil {
		return err
	}
	return d.Page.WaitStable(ctx, browser.DefaultStableTimeout)
}

// profileValue returns the value for an incidental field: operator profile
// first, else a generated sensible value based on the field's semantics.
func (h SignupHandler) profileValue(n *perceive.Node, creds identity.Credentials) string {
	label := strings.ToLower(n.Name + " " + n.Placeholder + " " + n.NameAttr + " " + n.ID + " " + n.Autocomplete)
	// Operator-supplied profile fields win when their key appears in the label.
	for k, v := range h.Profile.Fields {
		if v != "" && strings.Contains(label, strings.ToLower(k)) {
			return v
		}
	}
	switch {
	case n.Type == "email":
		return creds.UsernameOrEmail
	case n.Type == "tel":
		if creds.Phone != "" {
			return creds.Phone
		}
		return "+15555550123"
	case usernameRe.MatchString(label):
		return localPart(creds.UsernameOrEmail)
	case firstNameRe.MatchString(label):
		return "Alex"
	case lastNameRe.MatchString(label):
		return "Taylor"
	case nameFieldRe.MatchString(label):
		return "Alex Taylor"
	case companyRe.MatchString(label):
		return "Example Co"
	default:
		return ""
	}
}

func localPart(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[:i]
	}
	return email
}

var _ = time.Second
