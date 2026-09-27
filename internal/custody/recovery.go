package custody

import (
	"sort"

	"github.com/wyw14/solo-036/internal/model"
)

// Restore rehydrates custody state from a snapshot. Records are validated
// and handovers whose pig is absent are dropped, following run.Restore's
// never-trust-the-input approach. Custody state is snapshot-sourced only;
// there is no journal replay.
func Restore(store *Store, pigs []model.Pig, handovers []model.Handover) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pigs = make(map[string]model.Pig)
	store.handovers = make(map[string][]model.Handover)
	for _, pig := range pigs {
		if !model.ValidatePig(pig) {
			continue
		}
		if _, exists := store.pigs[pig.Code]; exists {
			continue
		}
		store.pigs[pig.Code] = pig
		store.handovers[pig.Code] = nil
	}
	for _, handover := range handovers {
		if handover.Validate() != nil {
			continue
		}
		if _, ok := store.pigs[handover.PigCode]; !ok {
			continue
		}
		store.handovers[handover.PigCode] = append(store.handovers[handover.PigCode], handover)
	}
	for code, chain := range store.handovers {
		sort.Slice(chain, func(i, j int) bool { return chain[i].Sequence < chain[j].Sequence })
		store.handovers[code] = chain
	}
}
