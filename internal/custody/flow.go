package custody

import (
	"time"

	"github.com/wyw14/solo-036/internal/model"
)

// ReleaseInput is the release (交出) side of a handover.
type ReleaseInput struct {
	Station string
	Crew    string
	Seal    model.SealSnapshot
	At      time.Time
}

// AcceptInput is the acceptance (接收) side of a handover.
type AcceptInput struct {
	Station string
	Crew    string
	Seal    model.SealSnapshot
	At      time.Time
}

// Release appends a pending handover to a pig's chain. A pending handover
// cannot be overwritten by a new release, and the release station/crew must
// match whoever currently holds the pig so the chain stays continuous.
func Release(store *Store, code string, in ReleaseInput) (model.Handover, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	pig, ok := store.pigs[code]
	if !ok {
		return model.Handover{}, ErrPigNotFound
	}
	chain := store.handovers[code]
	if len(chain) > 0 && chain[len(chain)-1].Status == model.HandoverPending {
		return model.Handover{}, ErrPendingHandover
	}
	if len(chain) == 0 {
		if in.Station != pig.ReceiveStation {
			return model.Handover{}, ErrCustodianMismatch
		}
	} else {
		prev := chain[len(chain)-1]
		if in.Station != prev.AcceptStation || in.Crew != prev.AcceptCrew {
			return model.Handover{}, ErrCustodianMismatch
		}
	}
	handover := model.Handover{
		ID:             model.NewRevision(),
		PigCode:        code,
		Sequence:       int64(len(chain) + 1),
		Status:         model.HandoverPending,
		ReleaseStation: in.Station,
		ReleaseCrew:    in.Crew,
		ReleaseSeal:    in.Seal,
		ReleasedAt:     in.At,
	}
	store.handovers[code] = append(chain, handover)
	return handover, nil
}

// Accept closes a pending handover and records the receiving side.
func Accept(store *Store, code, id string, in AcceptInput) (model.Handover, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.pigs[code]; !ok {
		return model.Handover{}, ErrPigNotFound
	}
	chain := store.handovers[code]
	index := -1
	for i := range chain {
		if chain[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return model.Handover{}, ErrHandoverNotFound
	}
	handover := chain[index]
	if handover.Status == model.HandoverAccepted {
		return model.Handover{}, ErrHandoverClosed
	}
	acceptedAt := in.At
	handover.AcceptStation = in.Station
	handover.AcceptCrew = in.Crew
	seal := in.Seal
	handover.AcceptSeal = &seal
	handover.AcceptedAt = &acceptedAt
	if !model.ValidHandoverTransition(handover.Status, model.HandoverAccepted) {
		return model.Handover{}, ErrHandoverClosed
	}
	handover.Status = model.HandoverAccepted
	handover.SealAnomaly = handover.SealMismatch()
	chain[index] = handover
	store.handovers[code] = chain
	return handover, nil
}
