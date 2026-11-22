package perceive

import "strings"

// MatchNode re-resolves a reference node — captured in an EARLIER perception,
// whose integer handle may now be stale after a re-render — to the best-matching
// node in a fresh State, by STABLE attributes only, never the handle number.
// Handles are reassigned in document order on every perceive, and an element's
// data-ab-handle attribute disappears when a framework remounts it, so acting on
// the old handle hits the wrong element or nothing at all. MatchNode finds the
// element again from identity that survives a remount (id, name attr,
// autocomplete, type, role, accessible name, placeholder, inputmode, maxlength,
// and — as a tiebreak — box position).
//
// It returns the match and whether it is confident enough to act on. A caller
// should keep its original node when ok is false (so a non-match never makes
// things worse than trusting the stale handle did).
func MatchNode(st State, ref Node) (*Node, bool) {
	bestIdx, bestScore, secondScore := -1, 0, 0
	for i := range st.Nodes {
		c := &st.Nodes[i]
		if c.Frame != ref.Frame {
			continue
		}
		s := matchScore(c, &ref)
		if s <= 0 {
			continue
		}
		if s > bestScore {
			secondScore = bestScore
			bestScore, bestIdx = s, i
		} else if s > secondScore {
			secondScore = s
		}
	}
	if bestIdx < 0 {
		return nil, false
	}
	best := &st.Nodes[bestIdx]
	// Decisive when a stable unique identifier matched (id / name attr /
	// autocomplete=one-time-code), or when a solid score clears the bar and is not
	// tied with a runner-up (ambiguous → don't guess).
	decisive := (ref.ID != "" && best.ID == ref.ID) ||
		(ref.NameAttr != "" && best.NameAttr == ref.NameAttr) ||
		(ref.Autocomplete != "" && ref.Autocomplete != "off" && best.Autocomplete == ref.Autocomplete)
	if decisive {
		return best, true
	}
	if bestScore >= minMatchScore && bestScore-secondScore >= 2 {
		return best, true
	}
	return nil, false
}

const minMatchScore = 6

// matchScore scores how strongly candidate c is the same element as ref, from
// stable attributes. The tag must match (a code <input> never remounts as a
// <button>); everything else adds weight, with unique identifiers weighted
// highest. Returns 0 when the tag differs.
func matchScore(c, ref *Node) int {
	if !strings.EqualFold(c.Tag, ref.Tag) {
		return 0
	}
	s := 2 // tag match
	if ref.ID != "" && c.ID == ref.ID {
		s += 6
	}
	if ref.NameAttr != "" && c.NameAttr == ref.NameAttr {
		s += 5
	}
	if ref.Autocomplete != "" && ref.Autocomplete != "off" && c.Autocomplete == ref.Autocomplete {
		s += 4
	}
	if ref.Type != "" && c.Type == ref.Type {
		s += 2
	}
	if ref.Role != "" && c.Role == ref.Role {
		s += 2
	}
	if ref.Name != "" && strings.EqualFold(c.Name, ref.Name) {
		s += 2
	}
	if ref.Placeholder != "" && c.Placeholder == ref.Placeholder {
		s += 2
	}
	if ref.InputMode != "" && c.InputMode == ref.InputMode {
		s++
	}
	if ref.MaxLength > 0 && c.MaxLength == ref.MaxLength {
		s++
	}
	// Position tiebreak: same rough box location adds a point (helps pick between
	// otherwise-identical fields, e.g. two code inputs). Never decisive on its own.
	if rectClose(c.Rect, ref.Rect) {
		s++
	}
	return s
}

// rectClose reports whether two viewport rects sit at roughly the same place
// (centres within 40 CSS px). Used only as a tiebreak.
func rectClose(a, b []int) bool {
	if len(a) != 4 || len(b) != 4 {
		return false
	}
	acx, acy := a[0]+a[2]/2, a[1]+a[3]/2
	bcx, bcy := b[0]+b[2]/2, b[1]+b[3]/2
	return abs(acx-bcx) <= 40 && abs(acy-bcy) <= 40
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
