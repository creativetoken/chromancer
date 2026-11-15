package verify

import "sync"

// ClaimStore records which provider messages (by SID / Message-ID) have already
// been consumed by a verification, so two concurrent waits never read the same
// message and a message is used at most once. In-memory and process-local;
// claiming only needs to hold for the life of a run.
type ClaimStore struct {
	mu      sync.Mutex
	claimed map[string]bool
}

// NewClaimStore builds an empty store.
func NewClaimStore() *ClaimStore { return &ClaimStore{claimed: map[string]bool{}} }

// Claim marks id as used and reports whether the caller won it (true the first
// time, false if it was already claimed). Safe for concurrent callers.
func (s *ClaimStore) Claim(id string) bool {
	if id == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed[id] {
		return false
	}
	s.claimed[id] = true
	return true
}

// Claimed reports whether id has been claimed, without claiming it.
func (s *ClaimStore) Claimed(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimed[id]
}
