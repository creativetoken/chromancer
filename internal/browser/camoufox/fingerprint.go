package camoufox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// fingerprintFile is the per-account fingerprint/config file name, stored in the
// account's workspace dir so the same account presents a STABLE fingerprint
// across runs (one Camoufox server per account). It is never regenerated for an
// existing account unless the proxy region changed (see ResolveFingerprint).
const fingerprintFile = "camoufox.json"

// Fingerprint is the persisted Camoufox fingerprint/config for one account. The
// Config blob is opaque (produced by Camoufox); Region records the geoip/proxy
// region it was generated for so a later run with a different-region proxy does
// not mix a US timezone/locale fingerprint with an EU exit IP.
type Fingerprint struct {
	Region    string          `json:"region"`
	Config    json.RawMessage `json:"config"`
	CreatedAt time.Time       `json:"created_at"`
}

func fingerprintPath(accountDir string) string {
	return filepath.Join(accountDir, fingerprintFile)
}

// regionFromProxy returns a region tag for fingerprint keying. The sidecar does
// the real geoip (timezone/locale matched to the proxy exit); the Go side only
// needs a stable tag that changes when the proxy changes, so it uses the proxy
// host. No proxy → "" (region-agnostic: a saved fingerprint is always reused).
func regionFromProxy(proxyURL string) string {
	s := proxyURL
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.Index(s, "@"); i >= 0 { // strip credentials
		s = s[i+1:]
	}
	if j := strings.IndexAny(s, "/?#"); j >= 0 {
		s = s[:j]
	}
	return strings.ToLower(s)
}

// LoadFingerprint reads the account's saved fingerprint. It returns (nil, nil)
// when none exists yet (first run for the account).
func LoadFingerprint(accountDir string) (*Fingerprint, error) {
	b, err := os.ReadFile(fingerprintPath(accountDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("camoufox: read fingerprint: %w", err)
	}
	var fp Fingerprint
	if err := json.Unmarshal(b, &fp); err != nil {
		return nil, fmt.Errorf("camoufox: decode fingerprint: %w", err)
	}
	return &fp, nil
}

// SaveFingerprint persists fp for the account (atomic write).
func SaveFingerprint(accountDir string, fp *Fingerprint) error {
	if err := os.MkdirAll(accountDir, 0o700); err != nil {
		return fmt.Errorf("camoufox: account dir: %w", err)
	}
	b, err := json.MarshalIndent(fp, "", "  ")
	if err != nil {
		return fmt.Errorf("camoufox: encode fingerprint: %w", err)
	}
	tmp := fingerprintPath(accountDir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("camoufox: write fingerprint: %w", err)
	}
	if err := os.Rename(tmp, fingerprintPath(accountDir)); err != nil {
		return fmt.Errorf("camoufox: replace fingerprint: %w", err)
	}
	return nil
}

// ResolveFingerprint returns the fingerprint to use for the account given the
// current proxy region, and whether the caller must (re)generate it.
//
//   - No saved fingerprint → regenerate = true (first run).
//   - Saved fingerprint whose Region matches → reuse it, regenerate = false.
//   - Saved fingerprint for a DIFFERENT region → regenerate = true and a warning,
//     because keeping a mismatched timezone/locale against a new exit IP is a
//     stronger bot tell than a fresh, consistent fingerprint (addition #3).
//
// region "" means "unknown/not region-pinned": a saved fingerprint is reused as
// long as it exists, since there is no region to conflict with.
func ResolveFingerprint(accountDir, region string, warn func(format string, args ...any)) (*Fingerprint, bool, error) {
	fp, err := LoadFingerprint(accountDir)
	if err != nil {
		return nil, false, err
	}
	if fp == nil {
		return nil, true, nil
	}
	if region != "" && fp.Region != "" && fp.Region != region {
		if warn != nil {
			warn("camoufox: saved fingerprint region %q != proxy region %q; regenerating to keep timezone/locale consistent with the exit IP", fp.Region, region)
		}
		return nil, true, nil
	}
	return fp, false, nil
}
