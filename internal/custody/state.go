package custody

import (
	"sync"

	"github.com/wyw14/solo-036/internal/model"
)

// Store keeps the registered pigs and their handover chains. Chains are
// always held sorted by sequence, mirroring the mutex-plus-map style of
// run.Store.
type Store struct {
	mu        sync.RWMutex
	pigs      map[string]model.Pig
	handovers map[string][]model.Handover
}

func NewStore() *Store {
	return &Store{
		pigs:      make(map[string]model.Pig),
		handovers: make(map[string][]model.Handover),
	}
}
