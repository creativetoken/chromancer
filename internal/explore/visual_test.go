package explore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/creativetoken/chromancer/internal/model"
	"github.com/creativetoken/chromancer/internal/perceive"
)

type stubModel struct{}

func (stubModel) Structured(context.Context, []model.Message, json.RawMessage, model.ChatOpts) (json.RawMessage, *model.Usage, error) {
	return json.RawMessage(`{"action":"note","why":"stub"}`), &model.Usage{}, nil
}

func pwNode() perceive.Node {
	return perceive.Node{Handle: 1, Visible: true, Tag: "input", Type: "password", Rect: []int{0, 0, 10, 10}}
}

func TestHasPerceivableLoginForm(t *testing.T) {
	if !hasPerceivableLoginForm(perceive.State{Nodes: []perceive.Node{pwNode()}}) {
		t.Error("password input should count as a perceivable form")
	}
	if hasPerceivableLoginForm(perceive.State{Nodes: []perceive.Node{{Tag: "input", Type: "text", Visible: true}}}) {
		t.Error("a text input alone is not a login form")
	}
	// A non-visible password field does not count.
	if hasPerceivableLoginForm(perceive.State{Nodes: []perceive.Node{{Tag: "input", Type: "password"}}}) {
		t.Error("hidden password input should not count")
	}
}

func TestNoSignInAffordance(t *testing.T) {
	// Visible "Sign in" text → affordance present.
	if noSignInAffordance(perceive.State{Text: "Welcome. Please Sign in to continue."}) {
		t.Error("visible sign-in text is an affordance")
	}
	// A sign-in link.
	if noSignInAffordance(perceive.State{Nodes: []perceive.Node{{Tag: "a", Href: "/en/login"}}}) {
		t.Error("a login href is an affordance")
	}
	// An account icon control.
	if noSignInAffordance(perceive.State{Nodes: []perceive.Node{{Name: "account (icon)", Pos: "top-right header"}}}) {
		t.Error("an account icon is an affordance")
	}
	// A bare page with only a logo and product links → no affordance.
	bare := perceive.State{
		Text:  "Shop 3D printers and filament.",
		Nodes: []perceive.Node{{Tag: "a", Href: "/products", Name: "Products"}, {Tag: "a", Href: "/store", Name: "Store"}},
	}
	if !noSignInAffordance(bare) {
		t.Error("a page with no sign-in text/link/icon should report no affordance")
	}
}

func TestIsNoProgress(t *testing.T) {
	cases := []struct {
		action, outcome string
		want            bool
	}{
		{"click", "no visible change", true},
		{"fill_login_form", "fill login form error: no visible email or password fields", true},
		{"click", "navigated to /sign-in", false},
		{"submit", "message shown: invalid code", false},
		{"enter_verification", "verification: entered", false},
		{"look", "took a screenshot", false},
		{"check_logged_in", "not logged in yet — keep going", true},
	}
	for _, c := range cases {
		if got := isNoProgress(c.action, c.outcome); got != c.want {
			t.Errorf("isNoProgress(%q,%q)=%v want %v", c.action, c.outcome, got, c.want)
		}
	}
}

func TestWantVisualEscalationAndDropback(t *testing.T) {
	// visionAvailable requires takeover mode + a VisionModel.
	e := &Explorer{
		VisionModel:    stubModel{},
		CheckLoggedIn:  func(context.Context) (bool, []string) { return false, nil }, // takeover on
		MaxScreenshots: 5,
	}
	bare := perceive.State{Text: "shop printers and filament", Nodes: []perceive.Node{{Tag: "a", Href: "/products"}}}

	// No affordance → escalate to visual.
	if !e.wantVisual(bare) {
		t.Fatal("should escalate to visual on a page with no sign-in affordance")
	}
	if !e.visualActive {
		t.Fatal("visualActive should latch true")
	}
	// Stays visual while no form appears.
	withAffordance := perceive.State{Text: "Sign in"}
	// A perceivable password form drops back to text even while latched.
	form := perceive.State{Nodes: []perceive.Node{pwNode()}}
	if e.wantVisual(form) {
		t.Fatal("a perceivable login form should drop back to the text path")
	}
	if e.visualActive {
		t.Fatal("visualActive should clear when a form is readable")
	}
	_ = withAffordance

	// Without a VisionModel, never visual.
	e2 := &Explorer{CheckLoggedIn: func(context.Context) (bool, []string) { return false, nil }}
	if e2.wantVisual(bare) {
		t.Fatal("no VisionModel → no visual mode")
	}

	// Screenshot cap stops further visual steps.
	e3 := &Explorer{VisionModel: stubModel{}, CheckLoggedIn: func(context.Context) (bool, []string) { return false, nil }, MaxScreenshots: 1}
	e3.visualActive = true
	e3.screenshotsUsed = 1
	if e3.wantVisual(bare) {
		t.Fatal("exhausted screenshot budget should stop visual mode")
	}
}
