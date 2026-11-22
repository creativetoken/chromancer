package perceive

import "testing"

func TestMatchNodeByID(t *testing.T) {
	ref := Node{Handle: 7, Frame: 0, Tag: "input", Type: "text", ID: "code",
		Autocomplete: "one-time-code", InputMode: "numeric", MaxLength: 6, Rect: []int{10, 200, 260, 40}}
	// Fresh perception: the field remounted — new handle, same stable identity —
	// plus an unrelated button that shifted into the old handle slot.
	st := State{Nodes: []Node{
		{Handle: 7, Frame: 0, Tag: "button", Type: "submit", Name: "Confirm"},
		{Handle: 8, Frame: 0, Tag: "input", Type: "text", ID: "code",
			Autocomplete: "one-time-code", InputMode: "numeric", MaxLength: 6, Rect: []int{10, 200, 260, 40}},
	}}
	m, ok := MatchNode(st, ref)
	if !ok {
		t.Fatalf("expected a confident match")
	}
	if m.Handle != 8 {
		t.Fatalf("matched handle %d, want 8 (the remounted field, not the old slot)", m.Handle)
	}
}

func TestMatchNodeByNameAttrAndStructure(t *testing.T) {
	ref := Node{Frame: 0, Tag: "input", Type: "text", NameAttr: "f6",
		InputMode: "numeric", MaxLength: 6, Rect: []int{0, 300, 200, 40}}
	st := State{Nodes: []Node{
		{Handle: 3, Frame: 0, Tag: "input", Type: "text", NameAttr: "email", Autocomplete: "username"},
		{Handle: 4, Frame: 0, Tag: "input", Type: "text", NameAttr: "f6",
			InputMode: "numeric", MaxLength: 6, Rect: []int{0, 300, 200, 40}},
	}}
	m, ok := MatchNode(st, ref)
	if !ok || m.NameAttr != "f6" {
		t.Fatalf("expected match on name attr f6, got ok=%v node=%+v", ok, m)
	}
}

func TestMatchNodeNoConfidentMatch(t *testing.T) {
	ref := Node{Frame: 0, Tag: "input", Type: "text", ID: "code", MaxLength: 6}
	// Only unrelated controls remain (the code field is gone, e.g. moved into a
	// closed shadow root): no confident match.
	st := State{Nodes: []Node{
		{Handle: 1, Frame: 0, Tag: "button", Type: "submit", Name: "Confirm"},
		{Handle: 2, Frame: 0, Tag: "a", Text: "Resend"},
	}}
	if m, ok := MatchNode(st, ref); ok {
		t.Fatalf("expected no match, got %+v", m)
	}
}

func TestMatchNodeRespectsFrame(t *testing.T) {
	ref := Node{Frame: 1, Tag: "input", Type: "text", ID: "code", MaxLength: 6, Autocomplete: "one-time-code"}
	st := State{Nodes: []Node{
		// Same identity but WRONG frame — must not match.
		{Handle: 5, Frame: 0, Tag: "input", Type: "text", ID: "code", MaxLength: 6, Autocomplete: "one-time-code"},
	}}
	if _, ok := MatchNode(st, ref); ok {
		t.Fatalf("must not match a node in a different frame")
	}
}

func TestMatchNodeAmbiguousTwinsNoGuess(t *testing.T) {
	// Two structurally identical inputs with no stable id/name → ambiguous, so
	// MatchNode declines rather than guess (rect tiebreak only breaks a near-tie).
	ref := Node{Frame: 0, Tag: "input", Type: "text", MaxLength: 6, InputMode: "numeric"}
	st := State{Nodes: []Node{
		{Handle: 1, Frame: 0, Tag: "input", Type: "text", MaxLength: 6, InputMode: "numeric", Rect: []int{0, 100, 100, 30}},
		{Handle: 2, Frame: 0, Tag: "input", Type: "text", MaxLength: 6, InputMode: "numeric", Rect: []int{0, 400, 100, 30}},
	}}
	if _, ok := MatchNode(st, ref); ok {
		t.Fatalf("ambiguous twins with no stable identity should not match")
	}
}
