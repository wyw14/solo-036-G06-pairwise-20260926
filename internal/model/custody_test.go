package model

import (
	"strings"
	"testing"
	"time"
)

func validReleasedAt() time.Time { return time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC) }

func TestSealSnapshotValidate(t *testing.T) {
	cases := []struct {
		name string
		seal SealSnapshot
		want bool
	}{
		{"intact", SealSnapshot{Number: "SEAL-1", State: SealIntact}, true},
		{"broken", SealSnapshot{Number: "SEAL-1", State: SealBroken}, true},
		{"blank number", SealSnapshot{Number: "  ", State: SealIntact}, false},
		{"bad state", SealSnapshot{Number: "SEAL-1", State: "melted"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.seal.Validate() == nil; got != tc.want {
				t.Fatalf("Validate() ok=%v want %v, err=%v", got, tc.want, tc.seal.Validate())
			}
		})
	}
}

func TestPigValidate(t *testing.T) {
	base := Pig{Code: "PIG-1", RunID: "run-1", ReceiveStation: "station-c", RegisteredAt: validReleasedAt()}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid pig rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Pig){
		"empty code":    func(p *Pig) { p.Code = " " },
		"empty run":     func(p *Pig) { p.RunID = "" },
		"empty station": func(p *Pig) { p.ReceiveStation = "" },
		"zero time":     func(p *Pig) { p.RegisteredAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			bad := base
			mutate(&bad)
			if bad.Validate() == nil {
				t.Fatalf("expected validation error for %s", name)
			}
		})
	}
}

func pendingHandover() Handover {
	return Handover{
		ID:             "h-1",
		PigCode:        "PIG-1",
		Sequence:       1,
		Status:         HandoverPending,
		ReleaseStation: "station-c",
		ReleaseCrew:    "recv-crew",
		ReleaseSeal:    SealSnapshot{Number: "SEAL-1", State: SealIntact},
		ReleasedAt:     validReleasedAt(),
	}
}

func TestHandoverValidatePending(t *testing.T) {
	h := pendingHandover()
	if err := h.Validate(); err != nil {
		t.Fatalf("pending handover rejected: %v", err)
	}
	early := validReleasedAt().Add(time.Minute)
	seal := SealSnapshot{Number: "SEAL-1", State: SealIntact}
	cases := map[string]func(*Handover){
		"missing id":            func(h *Handover) { h.ID = "" },
		"sequence not positive": func(h *Handover) { h.Sequence = 0 },
		"bad status":            func(h *Handover) { h.Status = "shipped" },
		"blank release crew":    func(h *Handover) { h.ReleaseCrew = " " },
		"bad release seal":      func(h *Handover) { h.ReleaseSeal.State = "" },
		"zero released at":      func(h *Handover) { h.ReleasedAt = time.Time{} },
		"pending with accept station": func(h *Handover) {
			h.AcceptStation = "station-b"
		},
		"pending with accept seal": func(h *Handover) { h.AcceptSeal = &seal },
		"pending anomaly":          func(h *Handover) { h.SealAnomaly = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bad := pendingHandover()
			mutate(&bad)
			if bad.Validate() == nil {
				t.Fatalf("expected validation error for %s", name)
			}
		})
	}
	accepted := pendingHandover()
	accepted.Status = HandoverAccepted
	if accepted.Validate() == nil {
		t.Fatal("accepted handover without acceptance data should be invalid")
	}
	seal2 := SealSnapshot{Number: "SEAL-1", State: SealIntact}
	accepted.AcceptStation = "station-b"
	accepted.AcceptCrew = "central-crew"
	accepted.AcceptSeal = &seal2
	accepted.AcceptedAt = &early
	if err := accepted.Validate(); err != nil {
		t.Fatalf("valid accepted handover rejected: %v", err)
	}
	before := validReleasedAt().Add(-time.Minute)
	accepted.AcceptedAt = &before
	if accepted.Validate() == nil {
		t.Fatal("accepted_at before released_at should be invalid")
	}
}

func TestValidHandoverTransition(t *testing.T) {
	if !ValidHandoverTransition(HandoverPending, HandoverAccepted) {
		t.Fatal("pending -> accepted should be valid")
	}
	if ValidHandoverTransition(HandoverAccepted, HandoverPending) {
		t.Fatal("accepted -> pending should be invalid")
	}
	if ValidHandoverTransition(HandoverPending, "") {
		t.Fatal("pending -> empty should be invalid")
	}
}

func TestSealMismatch(t *testing.T) {
	at := validReleasedAt()
	later := at.Add(time.Hour)
	makeAccepted := func(releaseSeal, acceptSeal SealSnapshot) Handover {
		return Handover{
			ID:             "h-1",
			PigCode:        "PIG-1",
			Sequence:       1,
			Status:         HandoverAccepted,
			ReleaseStation: "station-c",
			ReleaseCrew:    "recv-crew",
			ReleaseSeal:    releaseSeal,
			ReleasedAt:     at,
			AcceptStation:  "station-b",
			AcceptCrew:     "central-crew",
			AcceptSeal:     &acceptSeal,
			AcceptedAt:     &later,
		}
	}
	cases := []struct {
		name        string
		releaseSeal SealSnapshot
		acceptSeal  SealSnapshot
		want        bool
	}{
		{"identical intact", SealSnapshot{Number: "S1", State: SealIntact}, SealSnapshot{Number: "S1", State: SealIntact}, false},
		{"changed number", SealSnapshot{Number: "S1", State: SealIntact}, SealSnapshot{Number: "S2", State: SealIntact}, true},
		{"intact to broken", SealSnapshot{Number: "S1", State: SealIntact}, SealSnapshot{Number: "S1", State: SealBroken}, true},
		{"broken stays broken", SealSnapshot{Number: "S1", State: SealBroken}, SealSnapshot{Number: "S1", State: SealBroken}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := makeAccepted(tc.releaseSeal, tc.acceptSeal).SealMismatch(); got != tc.want {
				t.Fatalf("SealMismatch()=%v want %v", got, tc.want)
			}
		})
	}
	pending := pendingHandover()
	if pending.SealMismatch() {
		t.Fatal("pending handover cannot have a mismatch")
	}
	if !strings.Contains(pending.ReleaseSeal.Number, "SEAL") {
		t.Fatal("sanity check of fixture failed")
	}
}
