package camoufox

import (
	"fmt"
	"strconv"
	"strings"
)

// PlaywrightVersion is the Playwright version this backend is built against. The
// playwright-go module version encodes the Playwright version as v0.MMmm.patch
// (e.g. v0.5200.0 → Playwright 1.52.0), and the Camoufox sidecar embeds its own
// Playwright build. If the sidecar's Playwright major.minor differs from this,
// the CDP/Playwright protocol handshake can mis-negotiate, so we check at
// startup and fail with a clear message.
//
// Keep this in lockstep with:
//   - the playwright-go `require` in go.mod (github.com/mxschmitt/playwright-go
//     v0.6100.0 ↔ Playwright 1.61.0),
//   - the camoufox / playwright pins in scripts/camoufox-setup.sh and
//     Dockerfile.camoufox (camoufox 0.5.9 allows playwright<1.63, so we pin
//     playwright 1.61.0 to match the Go client).
const PlaywrightVersion = "1.61.0"

// majorMinor returns the "MAJOR.MINOR" prefix of a dotted version string.
func majorMinor(v string) (string, bool) {
	v = strings.TrimSpace(strings.TrimPrefix(v, "v"))
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return "", false
	}
	for _, p := range parts[:2] {
		if _, err := strconv.Atoi(p); err != nil {
			return "", false
		}
	}
	return parts[0] + "." + parts[1], true
}

// CheckPlaywrightVersion compares the Playwright version the sidecar reports
// against the pinned PlaywrightVersion. A patch-level difference is tolerated; a
// major or minor difference is a hard error so a version skew fails loudly
// instead of hanging on a bad handshake.
func CheckPlaywrightVersion(reported string) error {
	want, ok := majorMinor(PlaywrightVersion)
	if !ok {
		return fmt.Errorf("camoufox: bad pinned PlaywrightVersion %q", PlaywrightVersion)
	}
	got, ok := majorMinor(reported)
	if !ok {
		return fmt.Errorf("camoufox: sidecar reported an unparseable Playwright version %q", reported)
	}
	if got != want {
		return fmt.Errorf("camoufox: Playwright version mismatch — sidecar is %s, this build expects %s.x; "+
			"align the camoufox package and the playwright-go module (see scripts/camoufox-setup.sh)", reported, want)
	}
	return nil
}
