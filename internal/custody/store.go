package custody

import (
	"errors"
	"sort"

	"github.com/wyw14/solo-036/internal/model"
)

// Sentinel conflicts let the service/API layers map failures to 404/409.
var (
	ErrPigExists         = errors.New("pig code already registered")
	ErrPigNotFound       = errors.New("pig not found")
	ErrPendingHandover   = errors.New("pending handover must be accepted before a new release")
	ErrHandoverNotFound  = errors.New("handover not found")
	ErrHandoverClosed    = errors.New("handover is already accepted")
	ErrCustodianMismatch = errors.New("release side does not match current custodian")
)

// Register records a pig keyed by its business code.
func (s *Store) Register(pig model.Pig) (model.Pig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.pigs[pig.Code]; exists {
		return model.Pig{}, ErrPigExists
	}
	s.pigs[pig.Code] = pig
	if _, exists := s.handovers[pig.Code]; !exists {
		s.handovers[pig.Code] = nil
	}
	return pig, nil
}

func (s *Store) GetPig(code string) (model.Pig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pig, ok := s.pigs[code]
	return pig, ok
}

// Pigs returns all registered pigs sorted by code.
func (s *Store) Pigs() []model.Pig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Pig, 0, len(s.pigs))
	for _, pig := range s.pigs {
		out = append(out, pig)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// Chain returns a copy of one pig's handover chain sorted by sequence.
func (s *Store) Chain(code string) ([]model.Handover, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.pigs[code]; !ok {
		return nil, false
	}
	return append([]model.Handover(nil), s.handovers[code]...), true
}

// Handovers returns every chain flattened and sorted by (pig code, sequence).
func (s *Store) Handovers() []model.Handover {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Handover, 0)
	for _, chain := range s.handovers {
		out = append(out, chain...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PigCode != out[j].PigCode {
			return out[i].PigCode < out[j].PigCode
		}
		return out[i].Sequence < out[j].Sequence
	})
	return out
}

// Pending returns the open handover for a pig, if any.
func (s *Store) Pending(code string) (model.Handover, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	chain, ok := s.handovers[code]
	if !ok || len(chain) == 0 {
		return model.Handover{}, false
	}
	last := chain[len(chain)-1]
	if last.Status != model.HandoverPending {
		return model.Handover{}, false
	}
	return last, true
}

func (s *Store) GetHandover(code, id string) (model.Handover, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, handover := range s.handovers[code] {
		if handover.ID == id {
			return handover, true
		}
	}
	return model.Handover{}, false
}
