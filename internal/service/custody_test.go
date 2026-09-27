package service

import (
	"testing"
	"time"

	"github.com/wyw14/solo-036/internal/custody"
	"github.com/wyw14/solo-036/internal/model"
)

const (
	testLauncher  = "station-a"
	testReceiverS = "station-c"
	testRouteID   = "main-station-a-station-c"
)

func driveToTracking(t *testing.T, r *Runtime) string {
	t.Helper()
	operation, err := r.Launch(t.Context(), LaunchRequest{
		StationID:      testLauncher,
		RouteID:        testRouteID,
		TargetPressure: 8.0,
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if _, err := r.Track(operation.Run.ID, TrackingRequest{
		StationID:  testReceiverS,
		SourceID:   "tracker-1",
		Kind:       "magnetic",
		Position:   173.0,
		ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("track: %v", err)
	}
	return operation.Run.ID
}

func receiveRun(t *testing.T, r *Runtime, runID string, pigs []ReceivedPigInput) {
	t.Helper()
	current, ok := r.Runs.Get(runID)
	if !ok {
		t.Fatalf("run %s missing", runID)
	}
	if _, err := r.Receive(runID, ReceiveRequest{
		StationID:      testReceiverS,
		IsolationEpoch: current.Epoch + 1,
		Pigs:           pigs,
	}); err != nil {
		t.Fatalf("receive: %v", err)
	}
}

func TestEndToEndCustodyChainAndReconcile(t *testing.T) {
	r := NewRuntime()
	r.Bootstrap()
	runID := driveToTracking(t, r)
	receiveRun(t, r, runID, []ReceivedPigInput{{
		PigID:       "PIG-100",
		Crew:        "crew-south",
		CollectedBy: "alice",
		SealStatus:  model.SealIntact,
		SealNo:      "S-100",
		Location:    "station-c/barrel",
	}})

	chain, err := r.CustodyChain("PIG-100")
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if chain.Collection.RunID != runID || chain.Custody.Current.Keeper != "alice" {
		t.Fatalf("auto-registered custody wrong: %+v", chain.Custody)
	}

	report := r.CustodyReconcile()
	if !report.Consistent {
		t.Fatalf("fresh chain should reconcile, findings: %+v", report.Findings)
	}

	handover, err := r.HandOverPig("PIG-100", PigHandoverInput{
		To:         model.Custodian{StationID: "station-b", Crew: "crew-central", Keeper: "bob"},
		Location:   "yard/gate-3",
		SealStatus: model.SealIntact,
		SealNo:     "S-100",
		HandedBy:   "alice",
	})
	if err != nil {
		t.Fatalf("handover: %v", err)
	}

	// The pending handover must block the next handover and be reported.
	if _, err := r.HandOverPig("PIG-100", PigHandoverInput{
		To:         model.Custodian{StationID: "station-a", Crew: "crew-north", Keeper: "carol"},
		Location:   "yard/gate-4",
		SealStatus: model.SealIntact,
		SealNo:     "S-100",
		HandedBy:   "alice",
	}); err == nil {
		t.Fatal("second handover must fail while the first is unreceived")
	}
	report = r.CustodyReconcile()
	if len(report.Pending) != 1 || report.Pending[0].ID != handover.ID {
		t.Fatalf("pending report wrong: %+v", report.Pending)
	}

	if _, err := r.ReceivePig("PIG-100", PigReceiptInput{
		HandoverID: handover.ID,
		ReceivedBy: "bob",
		Location:   "station-b/store",
		SealStatus: model.SealIntact,
		SealNo:     "S-100",
	}); err != nil {
		t.Fatalf("receive handover: %v", err)
	}
	current, _ := r.Custody.Current("PIG-100")
	if current.Current.Keeper != "bob" || current.Location != "station-b/store" {
		t.Fatalf("custody after receipt wrong: %+v", current)
	}

	operation, err := r.Operation(runID)
	if err != nil {
		t.Fatalf("operation: %v", err)
	}
	if len(operation.Handovers) != 1 || len(operation.Collection) != 1 {
		t.Fatalf("operation custody records wrong: %+v / %+v", operation.Collection, operation.Handovers)
	}

	// Snapshot must carry the ledger and remain valid for persistence.
	snapshot := r.Snapshot()
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("snapshot with custody invalid: %v", err)
	}
	if len(snapshot.Collections) != 1 || len(snapshot.Handovers) != 1 {
		t.Fatal("snapshot lost custody records")
	}

	report = r.CustodyReconcile()
	if !report.Consistent || len(report.Pending) != 0 {
		t.Fatalf("closed chain should reconcile: %+v", report)
	}
}

func TestSealDiscrepancyAndReceiveWithoutCollection(t *testing.T) {
	r := NewRuntime()
	r.Bootstrap()
	runID := driveToTracking(t, r)
	// Received in the field but the operator never registered the pig.
	receiveRun(t, r, runID, nil)
	report := r.CustodyReconcile()
	found := false
	for _, finding := range report.Findings {
		if finding.Kind == "receive_without_collection" && finding.RunID == runID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected receive_without_collection finding, got %+v", report.Findings)
	}

	// Late registration is still allowed and clears the gap.
	if _, err := r.RegisterCollection(runID, testReceiverS, PigCollectionInput{
		PigID:       "PIG-200",
		Crew:        "crew-south",
		CollectedBy: "alice",
		SealStatus:  model.SealIntact,
		SealNo:      "S-200",
		Location:    "station-c/barrel",
	}); err != nil {
		t.Fatalf("late register: %v", err)
	}
	handover, err := r.HandOverPig("PIG-200", PigHandoverInput{
		To:         model.Custodian{StationID: "station-b", Crew: "crew-central", Keeper: "bob"},
		Location:   "yard/gate-3",
		SealStatus: model.SealIntact,
		SealNo:     "S-200",
		HandedBy:   "alice",
	})
	if err != nil {
		t.Fatalf("handover: %v", err)
	}
	if _, err := r.ReceivePig("PIG-200", PigReceiptInput{
		HandoverID: handover.ID,
		ReceivedBy: "bob",
		Location:   "station-b/store",
		SealStatus: model.SealBroken,
	}); err != nil {
		t.Fatalf("receive with broken seal: %v", err)
	}
	report = r.CustodyReconcile()
	found = false
	for _, finding := range report.Findings {
		if finding.Kind == "seal_discrepancy" && finding.HandoverID == handover.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected seal_discrepancy finding, got %+v", report.Findings)
	}
}

func TestCollectionBeforeRunReceiveRejected(t *testing.T) {
	r := NewRuntime()
	r.Bootstrap()
	runID := driveToTracking(t, r)
	// Still tracking: registering a collection must fail so records cannot
	// precede the receiving journal event.
	if _, err := r.RegisterCollection(runID, testReceiverS, PigCollectionInput{
		PigID:       "PIG-300",
		Crew:        "crew-south",
		CollectedBy: "alice",
		SealStatus:  model.SealNone,
		Location:    "station-c/barrel",
	}); err == nil {
		t.Fatal("collection before run receive must be rejected")
	}
}

func TestCustodyRestoreAfterSnapshot(t *testing.T) {
	r := NewRuntime()
	r.Bootstrap()
	runID := driveToTracking(t, r)
	receiveRun(t, r, runID, []ReceivedPigInput{{
		PigID:       "PIG-400",
		Crew:        "crew-south",
		CollectedBy: "alice",
		SealStatus:  model.SealNone,
		Location:    "station-c/barrel",
	}})
	handover, err := r.HandOverPig("PIG-400", PigHandoverInput{
		To:         model.Custodian{StationID: "station-b", Crew: "crew-central", Keeper: "bob"},
		Location:   "yard/gate-3",
		SealStatus: model.SealNone,
		HandedBy:   "alice",
	})
	if err != nil {
		t.Fatalf("handover: %v", err)
	}
	snapshot := r.Snapshot()
	restored, err := custody.Restore(snapshot.Collections, snapshot.Handovers)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	current, ok := restored.Current("PIG-400")
	if !ok || current.PendingHandoverID != handover.ID {
		t.Fatalf("restored pending state wrong: %+v", current)
	}
}
