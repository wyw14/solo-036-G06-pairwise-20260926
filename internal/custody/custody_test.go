package custody

import (
	"errors"
	"testing"
	"time"

	"github.com/wyw14/solo-036/internal/model"
)

func testPig() model.Pig {
	return model.Pig{
		Code:           "PIG-1",
		RunID:          "run-1",
		ReceiveStation: "station-c",
		RegisteredAt:   time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC),
	}
}

func seal(number string, state model.SealState) model.SealSnapshot {
	return model.SealSnapshot{Number: number, State: state}
}

func TestRegisterAndDuplicate(t *testing.T) {
	store := NewStore()
	if _, err := store.Register(testPig()); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := store.Register(testPig()); !errors.Is(err, ErrPigExists) {
		t.Fatalf("duplicate register: want ErrPigExists, got %v", err)
	}
	if _, ok := store.GetPig("PIG-1"); !ok {
		t.Fatal("registered pig not found")
	}
	if pigs := store.Pigs(); len(pigs) != 1 || pigs[0].Code != "PIG-1" {
		t.Fatalf("unexpected pig list: %+v", pigs)
	}
}

func TestReleasePendingGuardAndOrigin(t *testing.T) {
	store := NewStore()
	if _, err := store.Register(testPig()); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)

	if _, err := Release(store, "PIG-1", ReleaseInput{Station: "station-b", Crew: "crew-a", Seal: seal("S1", model.SealIntact), At: at}); !errors.Is(err, ErrCustodianMismatch) {
		t.Fatalf("first release away from receive station: want ErrCustodianMismatch, got %v", err)
	}
	first, err := Release(store, "PIG-1", ReleaseInput{Station: "station-c", Crew: "crew-a", Seal: seal("S1", model.SealIntact), At: at})
	if err != nil {
		t.Fatalf("first release: %v", err)
	}
	if first.Status != model.HandoverPending || first.Sequence != 1 {
		t.Fatalf("unexpected first handover: %+v", first)
	}
	if pending, ok := store.Pending("PIG-1"); !ok || pending.ID != first.ID {
		t.Fatal("pending lookup failed")
	}
	if _, err := Release(store, "PIG-1", ReleaseInput{Station: "station-c", Crew: "crew-a", Seal: seal("S1", model.SealIntact), At: at}); !errors.Is(err, ErrPendingHandover) {
		t.Fatalf("overlapping release: want ErrPendingHandover, got %v", err)
	}
	if _, err := Release(store, "MISSING", ReleaseInput{Station: "station-c"}); !errors.Is(err, ErrPigNotFound) {
		t.Fatalf("release for unknown pig: want ErrPigNotFound, got %v", err)
	}
}

func TestAcceptSealAnomalyAndContinuity(t *testing.T) {
	store := NewStore()
	if _, err := store.Register(testPig()); err != nil {
		t.Fatal(err)
	}
	releasedAt := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	first, err := Release(store, "PIG-1", ReleaseInput{Station: "station-c", Crew: "crew-a", Seal: seal("S1", model.SealIntact), At: releasedAt})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Accept(store, "PIG-1", "missing-id", AcceptInput{}); !errors.Is(err, ErrHandoverNotFound) {
		t.Fatalf("accept unknown: want ErrHandoverNotFound, got %v", err)
	}
	accepted, err := Accept(store, "PIG-1", first.ID, AcceptInput{
		Station: "station-b",
		Crew:    "crew-b",
		Seal:    seal("S2", model.SealBroken),
		At:      releasedAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !accepted.SealAnomaly {
		t.Fatal("changed seal number and intact->broken must flag an anomaly")
	}
	if _, err := Accept(store, "PIG-1", first.ID, AcceptInput{}); !errors.Is(err, ErrHandoverClosed) {
		t.Fatalf("double accept: want ErrHandoverClosed, got %v", err)
	}

	if _, err := Release(store, "PIG-1", ReleaseInput{Station: "station-c", Crew: "crew-a", Seal: seal("S2", model.SealBroken), At: releasedAt.Add(2 * time.Hour)}); !errors.Is(err, ErrCustodianMismatch) {
		t.Fatalf("release from wrong station/crew: want ErrCustodianMismatch, got %v", err)
	}
	second, err := Release(store, "PIG-1", ReleaseInput{Station: "station-b", Crew: "crew-b", Seal: seal("S2", model.SealBroken), At: releasedAt.Add(2 * time.Hour)})
	if err != nil {
		t.Fatalf("second release from current custodian: %v", err)
	}
	if second.Sequence != 2 {
		t.Fatalf("second handover sequence=%d want 2", second.Sequence)
	}

	chain, _ := store.Chain("PIG-1")
	if len(chain) != 2 || chain[0].ID != first.ID || chain[1].ID != second.ID {
		t.Fatalf("chain not ordered correctly: %+v", chain)
	}
	all := store.Handovers()
	if len(all) != 2 || all[0].Sequence != 1 || all[1].Sequence != 2 {
		t.Fatalf("flat handover list wrong: %+v", all)
	}
}

func TestRestoreFiltersAndOrders(t *testing.T) {
	store := NewStore()
	good := testPig()
	at := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	later := at.Add(time.Hour)
	acceptSeal := seal("S1", model.SealIntact)
	h1 := model.Handover{
		ID: "h-2", PigCode: "PIG-1", Sequence: 2, Status: model.HandoverAccepted,
		ReleaseStation: "station-c", ReleaseCrew: "crew-a", ReleaseSeal: seal("S1", model.SealIntact), ReleasedAt: later,
		AcceptStation: "station-b", AcceptCrew: "crew-b", AcceptSeal: &acceptSeal, AcceptedAt: &later,
	}
	h0 := model.Handover{
		ID: "h-1", PigCode: "PIG-1", Sequence: 1, Status: model.HandoverAccepted,
		ReleaseStation: "station-c", ReleaseCrew: "crew-a", ReleaseSeal: seal("S1", model.SealIntact), ReleasedAt: at,
		AcceptStation: "station-c", AcceptCrew: "crew-a", AcceptSeal: &acceptSeal, AcceptedAt: &at,
	}
	badPig := model.Pig{Code: ""}
	orphan := h1
	orphan.PigCode = "GHOST"
	invalidHandover := h1
	invalidHandover.ID = ""

	Restore(store,
		[]model.Pig{good, badPig},
		[]model.Handover{h1, h0, orphan, invalidHandover},
	)
	if pigs := store.Pigs(); len(pigs) != 1 || pigs[0].Code != "PIG-1" {
		t.Fatalf("restore pigs: %+v", pigs)
	}
	chain, ok := store.Chain("PIG-1")
	if !ok || len(chain) != 2 || chain[0].ID != "h-1" || chain[1].ID != "h-2" {
		t.Fatalf("restore chain: ok=%v chain=%+v", ok, chain)
	}
}
