package captcha

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStatsRateAndMedian(t *testing.T) {
	s := NewStats()
	for i := 0; i < 4; i++ {
		s.RecordAttempt("app", "recaptcha_v2", "native", int64(100+i*10), 0)
	}
	s.RecordOutcome("app", "recaptcha_v2", "native", true)
	s.RecordOutcome("app", "recaptcha_v2", "native", true)
	s.RecordOutcome("app", "recaptcha_v2", "native", false)
	if got := s.Rate("app", "recaptcha_v2", "native"); got < 0.49 || got > 0.51 {
		t.Fatalf("rate = %v, want ~0.5", got)
	}
	rows := s.Rows()
	if len(rows) != 1 || rows[0].MedianMS == 0 || rows[0].Attempts != 4 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestStatsPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.json")
	s, _ := LoadStats(path)
	s.RecordAttempt("app", "hcaptcha", "external", 500, 0.002)
	s.RecordOutcome("app", "hcaptcha", "external", true)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s2, err := LoadStats(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Rate("app", "hcaptcha", "external"); got != 1.0 {
		t.Fatalf("reloaded rate = %v, want 1.0", got)
	}
}

// Adaptive ordering prefers the solver with the higher observed success rate.
func TestAdaptiveOrdering(t *testing.T) {
	// Two solvers that both "support" mock interactive; both decline so no page
	// is touched — we only assert which one the resolver tries first.
	lo := &fakeSolver{name: "lo", caps: Capabilities{Vendors: []Vendor{VendorMock}, Kinds: []Kind{KindInteractive}}, err: ErrDeclined}
	hi := &fakeSolver{name: "hi", caps: Capabilities{Vendors: []Vendor{VendorMock}, Kinds: []Kind{KindInteractive}}, err: ErrDeclined}
	st := NewStats()
	// Give "hi" a strong track record, "lo" a poor one.
	for i := 0; i < 5; i++ {
		st.RecordAttempt("app", "mock", "hi", 10, 0)
		st.RecordOutcome("app", "mock", "hi", true)
		st.RecordAttempt("app", "mock", "lo", 10, 0)
	}
	r := NewResolver([]Solver{lo, hi}, nil, nil, 3, nil, WithStats(st, "app"), WithAdaptiveOrder(true)).(*chainResolver)
	el := r.eligible(Challenge{Vendor: VendorMock, Kind: KindInteractive})
	if el[0].s.Name() != "hi" {
		t.Fatalf("adaptive order put %q first, want hi", el[0].s.Name())
	}
	_ = context.Background()
}
