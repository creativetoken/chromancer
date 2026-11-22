package lab

import "fmt"

// Visual lab marker colours. The closed-shadow and canvas apps render their
// controls as solid fills in these exact colours so the test-only visual policy
// (which decodes the screenshot) can locate them and act by coordinate — a
// deterministic, offline stand-in for a real vision model. They are chosen to
// not collide with the set-of-marks overlay colours drawn by the explorer
// (outline rgb(255,40,40), label rgb(20,20,20)/rgb(255,235,59)).
var (
	markerEmail  = [3]uint8{0, 128, 255} // email field
	markerPass   = [3]uint8{200, 0, 255} // password field
	markerSubmit = [3]uint8{0, 200, 0}   // submit / primary control
)

func rgbCSS(c [3]uint8) string { return fmt.Sprintf("rgb(%d,%d,%d)", c[0], c[1], c[2]) }
