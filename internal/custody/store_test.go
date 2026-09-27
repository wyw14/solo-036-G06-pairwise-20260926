package custody

import (
	"errors"
	"testing"
	"time"

	"github.com/wyw14/solo-036/internal/model"
)

func sampleCollection(t *testing.T, store *Store, pigID string) model.CollectionRecord {
	t.Helper()
	at := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	record, err := store.Collect(CollectionInput{
		PigID:       pigID,
		RunID:       "run-1",
		StationID:   "station-c",
		Crew:        "crew-south",
		CollectedBy: "alice",
		SealStatus:  model.SealIntact,
		SealNo:      "S-001",
		Location:    "station-c/barrel",
		CollectedAt: at,
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return record
}

func stationBTeam() model.Custodian {
	return model.Custodian{StationID: "station-b", Crew: "crew-central", Keeper: "bob"}
}

func stationATeam() model.Custodian {
	return model.Custodian{StationID: "station-a", Crew: "crew-north", Keeper: "carol"}
}

func TestCollectHandOverReceiveFlow(t *testing.T) {
	store := NewStore()
	sampleCollection(t, store, "PIG-7")

	handedAt := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	handover, err := store.HandOver(HandoverInput{
		PigID:      "PIG-7",
		To:         stationBTeam(),
		Location:   "yard/gate-3",
		SealStatus: model.SealIntact,
		SealNo:     "S-001",
		HandedBy:   "alice",
		HandedAt:   handedAt,
	})
	if err != nil {
		t.Fatalf("handover: %v", err)
	}
	if handover.Seq != 1 {
		t.Fatalf("seq = %d, want 1", handover.Seq)
	}
	if handover.From.Keeper != "alice" {
		t.Fatalf("handover origin = %+v, want alice", handover.From)
	}
	if !handover.Pending() {
		t.Fatal("new handover should be pending")
	}
	current, ok := store.Current("PIG-7")
	if !ok || current.PendingHandoverID != handover.ID {
		t.Fatalf("pending handover not tracked: %+v", current)
	}

	receivedAt := handedAt.Add(30 * time.Minute)
	if _, err := store.Receive(ReceiptInput{
		PigID:      "PIG-7",
		HandoverID: handover.ID,
		ReceivedBy: "bob",
		ReceivedAt: receivedAt,
		Location:   "station-b/store-room",
		SealStatus: model.SealIntact,
		SealNo:     "S-001",
	}); err != nil {
		t.Fatalf("receive: %v", err)
	}
	current, _ = store.Current("PIG-7")
	if current.Current != stationBTeam() {
		t.Fatalf("custodian = %+v, want station-b team", current.Current)
	}
	if current.Location != "station-b/store-room" {
		t.Fatalf("location = %q", current.Location)
	}
	if current.PendingHandoverID != "" {
		t.Fatal("pending handover should clear after receipt")
	}
}

func TestPendingHandoverCannotBeOverwritten(t *testing.T) {
	store := NewStore()
	sampleCollection(t, store, "PIG-8")

	_, err := store.HandOver(HandoverInput{
		PigID:      "PIG-8",
		To:         stationBTeam(),
		Location:   "yard/gate-3",
		SealStatus: model.SealIntact,
		SealNo:     "S-002",
		HandedBy:   "alice",
		HandedAt:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("first handover: %v", err)
	}
	_, err = store.HandOver(HandoverInput{
		PigID:      "PIG-8",
		To:         stationATeam(),
		Location:   "yard/gate-4",
		SealStatus: model.SealIntact,
		SealNo:     "S-002",
		HandedBy:   "alice",
		HandedAt:   time.Now().UTC().Add(time.Minute),
	})
	if !errors.Is(err, ErrHandoverPending) {
		t.Fatalf("second handover error = %v, want ErrHandoverPending", err)
	}
	current, _ := store.Current("PIG-8")
	if current.Current.Keeper != "alice" {
		t.Fatalf("custodian changed while handover pending: %+v", current.Current)
	}
}

func TestRejectionKeepsOriginalCustodianAndAllowsNextHandover(t *testing.T) {
	store := NewStore()
	sampleCollection(t, store, "PIG-9")
	handedAt := time.Date(2026, 9, 26, 11, 0, 0, 0, time.UTC)
	first, err := store.HandOver(HandoverInput{
		PigID:      "PIG-9",
		To:         stationBTeam(),
		Location:   "yard/gate-3",
		SealStatus: model.SealBroken,
		SealNo:     "",
		HandedBy:   "alice",
		HandedAt:   handedAt,
	})
	if err != nil {
		t.Fatalf("handover: %v", err)
	}
	rejected, err := store.Receive(ReceiptInput{
		PigID:        "PIG-9",
		HandoverID:   first.ID,
		ReceivedBy:   "bob",
		ReceivedAt:   handedAt.Add(10 * time.Minute),
		Rejected:     true,
		RejectReason: "seal broken",
	})
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.Pending() || !rejected.Rejected {
		t.Fatal("rejected handover should be closed and marked rejected")
	}
	current, _ := store.Current("PIG-9")
	if current.Current.Keeper != "alice" {
		t.Fatalf("custodian after rejection = %+v, want alice", current.Current)
	}
	second, err := store.HandOver(HandoverInput{
		PigID:      "PIG-9",
		To:         stationATeam(),
		Location:   "yard/gate-5",
		SealStatus: model.SealResealed,
		SealNo:     "S-010",
		HandedBy:   "alice",
		HandedAt:   handedAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("handover after rejection: %v", err)
	}
	if second.Seq != 2 || second.From.Keeper != "alice" {
		t.Fatalf("second handover = %+v, want seq 2 from alice", second)
	}
	chain, _ := store.Chain("PIG-9")
	if err := model.ValidateHandoverChain(chain.Handovers); err != nil {
		t.Fatalf("chain invalid after rejection: %v", err)
	}
}

func TestCannotHandOverUnknownOrSameCustodian(t *testing.T) {
	store := NewStore()
	_, err := store.HandOver(HandoverInput{
		PigID:      "PIG-X",
		To:         stationBTeam(),
		Location:   "yard",
		SealStatus: model.SealNone,
		HandedBy:   "alice",
		HandedAt:   time.Now().UTC(),
	})
	if !errors.Is(err, ErrPigNotCollected) {
		t.Fatalf("err = %v, want ErrPigNotCollected", err)
	}
	sampleCollection(t, store, "PIG-10")
	same := model.Custodian{StationID: "station-c", Crew: "crew-south", Keeper: "alice"}
	_, err = store.HandOver(HandoverInput{
		PigID:      "PIG-10",
		To:         same,
		Location:   "station-c/barrel",
		SealStatus: model.SealIntact,
		SealNo:     "S-001",
		HandedBy:   "alice",
		HandedAt:   time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("handover to the current custodian should be rejected")
	}
}

func TestRestoreRecomputesCustody(t *testing.T) {
	store := NewStore()
	sampleCollection(t, store, "PIG-11")
	handedAt := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	pending, err := store.HandOver(HandoverInput{
		PigID:      "PIG-11",
		To:         stationBTeam(),
		Location:   "yard",
		SealStatus: model.SealIntact,
		SealNo:     "S-001",
		HandedBy:   "alice",
		HandedAt:   handedAt,
	})
	if err != nil {
		t.Fatalf("handover: %v", err)
	}
	collections := store.Collections()
	handovers := store.Handovers()
	restored, err := Restore(collections, handovers)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	current, ok := restored.Current("PIG-11")
	if !ok {
		t.Fatal("restored ledger lost the pig")
	}
	if current.PendingHandoverID != pending.ID {
		t.Fatalf("pending id = %q, want %q", current.PendingHandoverID, pending.ID)
	}
	chain, _ := restored.Chain("PIG-11")
	if err := model.ValidateHandoverChain(chain.Handovers); err != nil {
		t.Fatalf("restored chain invalid: %v", err)
	}
}
