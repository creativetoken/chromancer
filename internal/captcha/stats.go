package captcha

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
)

// SolverStat is the accumulated performance of one solver on one (target,vendor).
type SolverStat struct {
	Attempts  int     `json:"attempts"`
	Accepted  int     `json:"accepted"`
	CostUSD   float64 `json:"cost_usd"`
	Latencies []int64 `json:"latencies_ms"` // most recent first, capped
}

const maxLatencies = 50

// SuccessRate is accepted/attempts (0 when no attempts).
func (s SolverStat) SuccessRate() float64 {
	if s.Attempts == 0 {
		return 0
	}
	return float64(s.Accepted) / float64(s.Attempts)
}

// MedianLatencyMS returns the median recorded latency.
func (s SolverStat) MedianLatencyMS() int64 {
	if len(s.Latencies) == 0 {
		return 0
	}
	cp := append([]int64(nil), s.Latencies...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp[len(cp)/2]
}

// Stats persists per-(target,vendor,solver) solve outcomes. Safe for concurrent
// use; file-backed when a path is set.
type Stats struct {
	mu   sync.Mutex
	M    map[string]*SolverStat `json:"stats"`
	path string
}

// NewStats returns an in-memory stats store.
func NewStats() *Stats { return &Stats{M: map[string]*SolverStat{}} }

// LoadStats loads (or initialises) a file-backed stats store.
func LoadStats(path string) (*Stats, error) {
	s := &Stats{M: map[string]*SolverStat{}, path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	_ = json.Unmarshal(data, &struct {
		M *map[string]*SolverStat `json:"stats"`
	}{M: &s.M})
	if s.M == nil {
		s.M = map[string]*SolverStat{}
	}
	return s, nil
}

func statKey(target, vendor, solver string) string {
	return target + "|" + vendor + "|" + solver
}

func (s *Stats) at(key string) *SolverStat {
	if s.M[key] == nil {
		s.M[key] = &SolverStat{}
	}
	return s.M[key]
}

// RecordAttempt records one solve attempt's latency and cost.
func (s *Stats) RecordAttempt(target, vendor, solver string, latencyMS int64, cost float64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.at(statKey(target, vendor, solver))
	st.Attempts++
	st.CostUSD += cost
	st.Latencies = append([]int64{latencyMS}, st.Latencies...)
	if len(st.Latencies) > maxLatencies {
		st.Latencies = st.Latencies[:maxLatencies]
	}
}

// RecordOutcome records whether a prior attempt was accepted.
func (s *Stats) RecordOutcome(target, vendor, solver string, accepted bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.at(statKey(target, vendor, solver))
	if accepted {
		st.Accepted++
	}
}

// Rate returns the success rate for one (target,vendor,solver).
func (s *Stats) Rate(target, vendor, solver string) float64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.M[statKey(target, vendor, solver)]; st != nil {
		return st.SuccessRate()
	}
	return 0
}

// Save writes the store to its file (no-op when unbacked).
func (s *Stats) Save() error {
	if s == nil || s.path == "" {
		return nil
	}
	s.mu.Lock()
	data, err := json.MarshalIndent(struct {
		M map[string]*SolverStat `json:"stats"`
	}{M: s.M}, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

// StatRow is one flattened stats entry for reporting.
type StatRow struct {
	Target      string  `json:"target"`
	Vendor      string  `json:"vendor"`
	Solver      string  `json:"solver"`
	Attempts    int     `json:"attempts"`
	Accepted    int     `json:"accepted"`
	SuccessRate float64 `json:"success_rate"`
	MedianMS    int64   `json:"median_ms"`
	CostUSD     float64 `json:"cost_usd"`
}

// Rows returns the stats flattened and sorted (for the eval table).
func (s *Stats) Rows() []StatRow {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []StatRow
	for k, st := range s.M {
		t, v, sv := splitKey(k)
		out = append(out, StatRow{Target: t, Vendor: v, Solver: sv, Attempts: st.Attempts, Accepted: st.Accepted,
			SuccessRate: st.SuccessRate(), MedianMS: st.MedianLatencyMS(), CostUSD: st.CostUSD})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Vendor != out[j].Vendor {
			return out[i].Vendor < out[j].Vendor
		}
		return out[i].Solver < out[j].Solver
	})
	return out
}

func splitKey(k string) (target, vendor, solver string) {
	parts := []string{"", "", ""}
	i, start := 0, 0
	for j := 0; j < len(k) && i < 3; j++ {
		if k[j] == '|' {
			parts[i] = k[start:j]
			i++
			start = j + 1
		}
	}
	if i < 3 {
		parts[i] = k[start:]
	}
	return parts[0], parts[1], parts[2]
}
