package service

import (
	"errors"
	"testing"
	"time"

	"github.com/wyw14/solo-036/internal/custody"
	"github.com/wyw14/solo-036/internal/model"
	"github.com/wyw14/solo-036/internal/run"
)

// receivedRun drives a fresh run through to the post-receive
// (depressurizing) state and records the durable receiving event.
func receivedRun(t *testing.T, r *Runtime) string {
	t.Helper()
	created, err := r.CreateOperation("station-a")
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	if err := run.Start(r.Runs, created.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := run.Track(r.Runs, created.ID); err != nil {
		t.Fatalf("track: %v", err)
	}
	if err := run.Receive(r.Runs, created.ID); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if err := r.appendEvent(model.Event{
		RunID:   created.ID,
		Kind:    "receiving",
		Payload: "station-c",
	}); err != nil {
		t.Fatalf("append receiving event: %v", err)
	}
	return created.ID
}

func TestRegisterPigRequiresReceive(t *testing.T) {
	r := NewRuntime()
	created, err := r.CreateOperation("station-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterPig(RegisterPigRequest{Code: "PIG-1", RunID: created.ID, Station: "station-c"}); !errors.Is(err, ErrRunNotReceived) {
		t.Fatalf("register before receive: want ErrRunNotReceived, got %v", err)
	}
}

func TestRegisterPigFullChainAndReconciliation(t *testing.T) {
	r := NewRuntime()
	runID := receivedRun(t, r)

	if _, err := r.RegisterPig(RegisterPigRequest{Code: "PIG-1", RunID: "missing", Station: "station-c"}); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("missing run: want ErrRunNotFound, got %v", err)
	}
	if _, err := r.RegisterPig(RegisterPigRequest{Code: "PIG-1", RunID: runID, Station: "station-b"}); !errors.Is(err, ErrReceiveStationMismatch) {
		t.Fatalf("wrong station: want ErrReceiveStationMismatch, got %v", err)
	}
	if _, err := r.RegisterPig(RegisterPigRequest{Code: "PIG/1", RunID: runID, Station: "station-c"}); err == nil {
		t.Fatal("pig code with slash must be rejected")
	}
	pig, err := r.RegisterPig(RegisterPigRequest{Code: "PIG-1", RunID: runID, Station: "station-c"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if pig.ReceiveStation != "station-c" {
		t.Fatalf("receive station=%s", pig.ReceiveStation)
	}
	if _, err := r.RegisterPig(RegisterPigRequest{Code: "PIG-1", RunID: runID, Station: "station-c"}); !errors.Is(err, custody.ErrPigExists) {
		t.Fatalf("duplicate: want ErrPigExists, got %v", err)
	}
	if _, err := r.RegisterPig(RegisterPigRequest{Code: "PIG-2", RunID: runID, Station: "station-c"}); !errors.Is(err, ErrRunAlreadyRegistered) {
		t.Fatalf("second pig for same run: want ErrRunAlreadyRegistered, got %v", err)
	}

	at := time.Now().UTC()
	h1, err := r.ReleaseHandover("PIG-1", ReleaseHandoverRequest{
		Station: "station-c", Crew: "crew-a",
		Seal:      model.SealSnapshot{Number: "S1", State: model.SealIntact},
		ReleaseAt: at,
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := r.ReleaseHandover("PIG-1", ReleaseHandoverRequest{
		Station: "station-c", Crew: "crew-a",
		Seal: model.SealSnapshot{Number: "S1", State: model.SealIntact},
	}); !errors.Is(err, custody.ErrPendingHandover) {
		t.Fatalf("overlapping release: want ErrPendingHandover, got %v", err)
	}
	accepted, err := r.AcceptHandover("PIG-1", h1.ID, AcceptHandoverRequest{
		Station: "station-b", Crew: "crew-b",
		Seal:     model.SealSnapshot{Number: "S2", State: model.SealBroken},
		AcceptAt: at.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !accepted.SealAnomaly {
		t.Fatal("seal mismatch should be flagged")
	}

	chain, ok := r.PigChain("PIG-1")
	if !ok {
		t.Fatal("chain lookup failed")
	}
	if chain.CurrentStation != "station-b" || chain.CurrentCrew != "crew-b" || chain.Pending {
		t.Fatalf("unexpected chain head: %+v", chain)
	}

	report := r.ReconcileCustody()
	kinds := map[string]bool{}
	for _, issue := range report.Issues {
		kinds[issue.Kind] = true
	}
	if !kinds[IssueSealMismatch] {
		t.Fatalf("reconciliation missing seal_mismatch: %+v", report.Issues)
	}
	if kinds[IssueUnregisteredReceivedRun] {
		t.Fatal("registered run should not be reported unregistered")
	}
	if report.Summary[IssueSealMismatch] != 1 {
		t.Fatalf("summary: %+v", report.Summary)
	}

	// A second received run left unregistered must appear in the report.
	unregistered := receivedRun(t, r)
	report = r.ReconcileCustody()
	found := false
	for _, issue := range report.Issues {
		if issue.Kind == IssueUnregisteredReceivedRun && issue.RunID == unregistered {
			found = true
		}
	}
	if !found {
		t.Fatalf("unregistered received run missing from report: %+v", report.Issues)
	}

	// The full snapshot, including custody records, must validate and
	// survive a store restore round-trip.
	snapshot := r.Snapshot()
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("snapshot validation: %v", err)
	}
	fresh := NewRuntime()
	custody.Restore(fresh.Custody, snapshot.Pigs, snapshot.Handovers)
	restored, ok := fresh.PigChain("PIG-1")
	if !ok || len(restored.Handovers) != 1 || !restored.Handovers[0].SealAnomaly {
		t.Fatalf("restore round-trip failed: %+v ok=%v", restored, ok)
	}
}

func TestCustodyHTTPStatusMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{custody.ErrPigNotFound, 404},
		{custody.ErrHandoverNotFound, 404},
		{ErrRunNotFound, 404},
		{custody.ErrPigExists, 409},
		{ErrRunAlreadyRegistered, 409},
		{custody.ErrPendingHandover, 409},
		{custody.ErrHandoverClosed, 409},
		{custody.ErrCustodianMismatch, 409},
		{ErrRunNotReceived, 409},
		{ErrReceiveStationMismatch, 409},
		{errors.New("some validation error"), 400},
	}
	for _, tc := range cases {
		if got := CustodyHTTPStatus(tc.err); got != tc.want {
			t.Errorf("status for %v = %d, want %d", tc.err, got, tc.want)
		}
	}
}
