package custody

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wyw14/solo-036/internal/model"
)

var (
	ErrPigNotCollected = errors.New("pig has no collection record")
	ErrPigCollected    = errors.New("pig is already collected")
	ErrHandoverPending = errors.New("an unreceived handover is still open")
	ErrCustodianMatch  = errors.New("handover origin does not match the current custodian")
)

type CollectionInput struct {
	PigID       string
	RunID       string
	StationID   string
	Crew        string
	CollectedBy string
	SealStatus  model.SealStatus
	SealNo      string
	Location    string
	CollectedAt time.Time
	Note        string
}

type HandoverInput struct {
	PigID      string
	To         model.Custodian
	Location   string
	SealStatus model.SealStatus
	SealNo     string
	HandedBy   string
	HandedAt   time.Time
	Note       string
}

type ReceiptInput struct {
	PigID        string
	HandoverID   string
	ReceivedBy   string
	ReceivedAt   time.Time
	Location     string
	SealStatus   model.SealStatus
	SealNo       string
	Note         string
	Rejected     bool
	RejectReason string
}

type Store struct {
	mu          sync.RWMutex
	collections map[string]model.CollectionRecord
	handovers   map[string][]model.HandoverRecord
	custody     map[string]model.PigCustody
}

func NewStore() *Store {
	return &Store{
		collections: make(map[string]model.CollectionRecord),
		handovers:   make(map[string][]model.HandoverRecord),
		custody:     make(map[string]model.PigCustody),
	}
}

func cleanPigID(id string) string { return strings.TrimSpace(id) }

func (s *Store) Collect(input CollectionInput) (model.CollectionRecord, error) {
	input.PigID = cleanPigID(input.PigID)
	record := model.CollectionRecord{
		PigID:       input.PigID,
		RunID:       strings.TrimSpace(input.RunID),
		StationID:   strings.TrimSpace(input.StationID),
		Crew:        strings.TrimSpace(input.Crew),
		CollectedBy: strings.TrimSpace(input.CollectedBy),
		SealStatus:  input.SealStatus,
		SealNo:      strings.TrimSpace(input.SealNo),
		Location:    strings.TrimSpace(input.Location),
		CollectedAt: input.CollectedAt.UTC(),
		Note:        strings.TrimSpace(input.Note),
	}
	if err := record.Validate(); err != nil {
		return model.CollectionRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.collections[record.PigID]; exists {
		return model.CollectionRecord{}, ErrPigCollected
	}
	s.collections[record.PigID] = record
	s.custody[record.PigID] = model.PigCustody{
		PigID:      record.PigID,
		RunID:      record.RunID,
		StationID:  record.StationID,
		Collected:  true,
		Current:    model.Custodian{StationID: record.StationID, Crew: record.Crew, Keeper: record.CollectedBy},
		Location:   record.Location,
		SealStatus: record.SealStatus,
		SealNo:     record.SealNo,
		UpdatedAt:  record.CollectedAt,
	}
	return record, nil
}

func (s *Store) HandOver(input HandoverInput) (model.HandoverRecord, error) {
	input.PigID = cleanPigID(input.PigID)
	input.To = model.Custodian{
		StationID: strings.TrimSpace(input.To.StationID),
		Crew:      strings.TrimSpace(input.To.Crew),
		Keeper:    strings.TrimSpace(input.To.Keeper),
	}
	handedAt := input.HandedAt.UTC()
	record := model.HandoverRecord{
		ID:         model.NewRevision(),
		PigID:      input.PigID,
		From:       model.Custodian{},
		To:         input.To,
		Location:   strings.TrimSpace(input.Location),
		SealStatus: input.SealStatus,
		SealNo:     strings.TrimSpace(input.SealNo),
		HandedBy:   strings.TrimSpace(input.HandedBy),
		HandedAt:   handedAt,
		Note:       strings.TrimSpace(input.Note),
	}
	if err := record.To.Validate(); err != nil {
		return model.HandoverRecord{}, fmt.Errorf("handover destination: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.custody[input.PigID]
	if !ok {
		return model.HandoverRecord{}, ErrPigNotCollected
	}
	if !current.Collected {
		return model.HandoverRecord{}, ErrPigNotCollected
	}
	if current.PendingHandoverID != "" {
		return model.HandoverRecord{}, fmt.Errorf("%w: %s", ErrHandoverPending, current.PendingHandoverID)
	}
	record.From = current.Current
	record.Seq = len(s.handovers[input.PigID]) + 1
	if err := record.Validate(); err != nil {
		return model.HandoverRecord{}, err
	}
	if record.To == record.From {
		return model.HandoverRecord{}, errors.New("handover destination must differ from the current custodian")
	}
	s.handovers[input.PigID] = append(s.handovers[input.PigID], record)
	current.PendingHandoverID = record.ID
	current.UpdatedAt = handedAt
	s.custody[input.PigID] = current
	return record, nil
}

func (s *Store) Receive(input ReceiptInput) (model.HandoverRecord, error) {
	input.PigID = cleanPigID(input.PigID)
	input.HandoverID = strings.TrimSpace(input.HandoverID)
	s.mu.Lock()
	defer s.mu.Unlock()
	chain, ok := s.handovers[input.PigID]
	if !ok {
		return model.HandoverRecord{}, ErrPigNotCollected
	}
	index := -1
	for i := range chain {
		if chain[i].ID == input.HandoverID {
			index = i
			break
		}
	}
	if index < 0 {
		return model.HandoverRecord{}, fmt.Errorf("handover %s not found for pig %s", input.HandoverID, input.PigID)
	}
	record := chain[index]
	if !record.Pending() {
		return model.HandoverRecord{}, fmt.Errorf("handover %s is already closed", input.HandoverID)
	}
	receivedAt := input.ReceivedAt.UTC()
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	if receivedAt.Before(record.HandedAt) {
		return model.HandoverRecord{}, errors.New("handover cannot be received before it is handed")
	}
	if input.Rejected {
		if strings.TrimSpace(input.RejectReason) == "" {
			return model.HandoverRecord{}, errors.New("rejection reason is required")
		}
		rejectedAt := receivedAt
		record.Rejected = true
		record.RejectReason = strings.TrimSpace(input.RejectReason)
		record.RejectedAt = &rejectedAt
		if note := strings.TrimSpace(input.Note); note != "" {
			record.Note = note
		}
	} else {
		record.ReceivedBy = strings.TrimSpace(input.ReceivedBy)
		receivedRef := receivedAt
		record.ReceivedAt = &receivedRef
		if location := strings.TrimSpace(input.Location); location != "" {
			record.ReceiveLocation = location
		} else {
			record.ReceiveLocation = record.Location
		}
		if input.SealStatus != "" {
			record.ReceiveSealStatus = input.SealStatus
			record.ReceiveSealNo = strings.TrimSpace(input.SealNo)
		} else {
			record.ReceiveSealStatus = record.SealStatus
			record.ReceiveSealNo = record.SealNo
		}
		if note := strings.TrimSpace(input.Note); note != "" {
			record.Note = note
		}
		if err := record.Validate(); err != nil {
			return model.HandoverRecord{}, err
		}
	}
	chain[index] = record
	s.handovers[input.PigID] = chain
	current := s.custody[input.PigID]
	current.PendingHandoverID = ""
	current.UpdatedAt = receivedAt
	if !record.Rejected {
		current.Current = record.To
		current.StationID = record.To.StationID
		current.Location = record.ReceiveLocation
		current.SealStatus = record.ReceiveSealStatus
		current.SealNo = record.ReceiveSealNo
	}
	s.custody[input.PigID] = current
	return record, nil
}

func (s *Store) Current(pigID string) (model.PigCustody, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	current, ok := s.custody[cleanPigID(pigID)]
	return current, ok
}

func (s *Store) Chain(pigID string) (model.CustodyChain, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.chainLocked(cleanPigID(pigID))
}

func (s *Store) chainLocked(pigID string) (model.CustodyChain, bool) {
	collection, ok := s.collections[pigID]
	if !ok {
		return model.CustodyChain{}, false
	}
	return model.CustodyChain{
		PigID:      pigID,
		Collection: collection,
		Handovers:  append([]model.HandoverRecord(nil), s.handovers[pigID]...),
		Custody:    s.custody[pigID],
	}, true
}

func (s *Store) Chains() []model.CustodyChain {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pigIDs := make([]string, 0, len(s.collections))
	for pigID := range s.collections {
		pigIDs = append(pigIDs, pigID)
	}
	sort.Strings(pigIDs)
	chains := make([]model.CustodyChain, 0, len(pigIDs))
	for _, pigID := range pigIDs {
		chain, ok := s.chainLocked(pigID)
		if ok {
			chains = append(chains, chain)
		}
	}
	return chains
}

func (s *Store) Collections() []model.CollectionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pigIDs := make([]string, 0, len(s.collections))
	for pigID := range s.collections {
		pigIDs = append(pigIDs, pigID)
	}
	sort.Strings(pigIDs)
	records := make([]model.CollectionRecord, 0, len(pigIDs))
	for _, pigID := range pigIDs {
		records = append(records, s.collections[pigID])
	}
	return records
}

func (s *Store) Handovers() []model.HandoverRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := make([]model.HandoverRecord, 0)
	pigIDs := make([]string, 0, len(s.handovers))
	for pigID := range s.handovers {
		pigIDs = append(pigIDs, pigID)
	}
	sort.Strings(pigIDs)
	for _, pigID := range pigIDs {
		records = append(records, s.handovers[pigID]...)
	}
	return records
}

func (s *Store) PendingHandovers() []model.HandoverRecord {
	pending := make([]model.HandoverRecord, 0)
	for _, chain := range s.Chains() {
		for _, handover := range chain.Handovers {
			if handover.Pending() {
				pending = append(pending, handover)
			}
		}
	}
	return pending
}

// Restore rebuilds the ledger from durable records and replays every handover
// so that the current custodian and any pending handover are recomputed.
func Restore(records []model.CollectionRecord, handoverRecords []model.HandoverRecord) (*Store, error) {
	store := NewStore()
	collections := append([]model.CollectionRecord(nil), records...)
	sort.Slice(collections, func(i, j int) bool {
		if collections[i].CollectedAt.Equal(collections[j].CollectedAt) {
			return collections[i].PigID < collections[j].PigID
		}
		return collections[i].CollectedAt.Before(collections[j].CollectedAt)
	})
	for _, record := range collections {
		if _, err := store.Collect(CollectionInput{
			PigID:       record.PigID,
			RunID:       record.RunID,
			StationID:   record.StationID,
			Crew:        record.Crew,
			CollectedBy: record.CollectedBy,
			SealStatus:  record.SealStatus,
			SealNo:      record.SealNo,
			Location:    record.Location,
			CollectedAt: record.CollectedAt,
			Note:        record.Note,
		}); err != nil {
			return nil, fmt.Errorf("restore collection %s: %w", record.PigID, err)
		}
	}
	handovers := append([]model.HandoverRecord(nil), handoverRecords...)
	sort.SliceStable(handovers, func(i, j int) bool {
		if handovers[i].PigID == handovers[j].PigID {
			return handovers[i].Seq < handovers[j].Seq
		}
		return handovers[i].PigID < handovers[j].PigID
	})
	byPig := make(map[string][]model.HandoverRecord)
	for _, record := range handovers {
		byPig[record.PigID] = append(byPig[record.PigID], record)
	}
	pigIDs := make([]string, 0, len(byPig))
	for pigID := range byPig {
		pigIDs = append(pigIDs, pigID)
	}
	sort.Strings(pigIDs)
	for _, pigID := range pigIDs {
		if _, ok := store.collections[pigID]; !ok {
			return nil, fmt.Errorf("restore: handovers reference pig %s without a collection record", pigID)
		}
		if err := model.ValidateHandoverChain(byPig[pigID]); err != nil {
			return nil, fmt.Errorf("restore: %w", err)
		}
	}
	for _, record := range handovers {
		if record.ReceivedAt != nil || record.RejectedAt != nil {
			// Closed handovers are replayed directly so the custody state matches.
			store.mu.Lock()
			store.handovers[record.PigID] = append(store.handovers[record.PigID], record)
			current := store.custody[record.PigID]
			if record.ReceivedAt != nil && !record.Rejected {
				current.Current = record.To
				current.StationID = record.To.StationID
				current.Location = record.ReceiveLocation
				current.SealStatus = record.ReceiveSealStatus
				current.SealNo = record.ReceiveSealNo
				current.UpdatedAt = *record.ReceivedAt
			} else if record.RejectedAt != nil {
				current.UpdatedAt = *record.RejectedAt
			}
			store.custody[record.PigID] = current
			store.mu.Unlock()
			continue
		}
		if _, err := store.HandOver(HandoverInput{
			PigID:      record.PigID,
			To:         record.To,
			Location:   record.Location,
			SealStatus: record.SealStatus,
			SealNo:     record.SealNo,
			HandedBy:   record.HandedBy,
			HandedAt:   record.HandedAt,
			Note:       record.Note,
		}); err != nil {
			return nil, fmt.Errorf("restore handover %s: %w", record.ID, err)
		}
		// HandOver assigns a fresh ID; put back the durable identity.
		store.mu.Lock()
		chain := store.handovers[record.PigID]
		chain[len(chain)-1].ID = record.ID
		custodyState := store.custody[record.PigID]
		custodyState.PendingHandoverID = record.ID
		store.custody[record.PigID] = custodyState
		store.mu.Unlock()
	}
	return store, nil
}
