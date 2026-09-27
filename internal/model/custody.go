package model

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// SealState is the observed condition of a pig seal (封签状态).
type SealState string

const (
	SealIntact SealState = "intact"
	SealBroken SealState = "broken"
)

func (s SealState) Valid() bool {
	return s == SealIntact || s == SealBroken
}

// HandoverStatus tracks whether released equipment has been accepted.
type HandoverStatus string

const (
	HandoverPending  HandoverStatus = "pending"  // 已交出未接收
	HandoverAccepted HandoverStatus = "accepted" // 已接收
)

// ValidHandoverTransition mirrors the explicit transition allowlist used
// for RunState: a handover can only move from pending to accepted.
func ValidHandoverTransition(from, to HandoverStatus) bool {
	return from == HandoverPending && to == HandoverAccepted
}

// SealSnapshot is one side's record of the seal attached to the pig.
type SealSnapshot struct {
	Number string    `json:"number"`
	State  SealState `json:"state"`
}

func (s SealSnapshot) Validate() error {
	if strings.TrimSpace(s.Number) == "" {
		return errors.New("seal number is required")
	}
	if !s.State.Valid() {
		return fmt.Errorf("invalid seal state %q", s.State)
	}
	return nil
}

// Pig is a registered piece of equipment (器具) tracked by its business
// code after the carrying run has been received.
type Pig struct {
	Code           string    `json:"code"`
	RunID          string    `json:"run_id"`
	ReceiveStation string    `json:"receive_station"`
	RegisteredAt   time.Time `json:"registered_at"`
}

func (p Pig) Validate() error {
	if strings.TrimSpace(p.Code) == "" {
		return errors.New("pig code is required")
	}
	if strings.TrimSpace(p.RunID) == "" {
		return errors.New("pig run reference is required")
	}
	if strings.TrimSpace(p.ReceiveStation) == "" {
		return errors.New("pig receive station is required")
	}
	if p.RegisteredAt.IsZero() {
		return errors.New("pig registered_at is required")
	}
	return nil
}

// ValidatePig is the restore-time filter, mirroring model.ValidateRun.
func ValidatePig(p Pig) bool {
	return p.Validate() == nil
}

// Handover is one link in the custody chain of a pig: a release side
// (交出) followed at most once by an acceptance side (接收).
type Handover struct {
	ID       string         `json:"id"`
	PigCode  string         `json:"pig_code"`
	Sequence int64          `json:"sequence"`
	Status   HandoverStatus `json:"status"`

	ReleaseStation string       `json:"release_station"`
	ReleaseCrew    string       `json:"release_crew"`
	ReleaseSeal    SealSnapshot `json:"release_seal"`
	ReleasedAt     time.Time    `json:"released_at"`

	AcceptStation string        `json:"accept_station,omitempty"`
	AcceptCrew    string        `json:"accept_crew,omitempty"`
	AcceptSeal    *SealSnapshot `json:"accept_seal,omitempty"`
	AcceptedAt    *time.Time    `json:"accepted_at,omitempty"`

	SealAnomaly bool `json:"seal_anomaly"`
}

func (h Handover) Validate() error {
	if strings.TrimSpace(h.ID) == "" {
		return errors.New("handover id is required")
	}
	if strings.TrimSpace(h.PigCode) == "" {
		return errors.New("handover pig code is required")
	}
	if h.Sequence <= 0 {
		return errors.New("handover sequence must be positive")
	}
	switch h.Status {
	case HandoverPending, HandoverAccepted:
	default:
		return fmt.Errorf("invalid handover status %q", h.Status)
	}
	if strings.TrimSpace(h.ReleaseStation) == "" {
		return errors.New("release station is required")
	}
	if strings.TrimSpace(h.ReleaseCrew) == "" {
		return errors.New("release crew is required")
	}
	if err := h.ReleaseSeal.Validate(); err != nil {
		return fmt.Errorf("release seal: %w", err)
	}
	if h.ReleasedAt.IsZero() {
		return errors.New("released_at is required")
	}
	switch h.Status {
	case HandoverPending:
		if h.AcceptStation != "" || h.AcceptCrew != "" || h.AcceptSeal != nil || h.AcceptedAt != nil {
			return errors.New("pending handover must not carry acceptance data")
		}
		if h.SealAnomaly {
			return errors.New("pending handover cannot carry a seal anomaly")
		}
	case HandoverAccepted:
		if strings.TrimSpace(h.AcceptStation) == "" {
			return errors.New("accept station is required")
		}
		if strings.TrimSpace(h.AcceptCrew) == "" {
			return errors.New("accept crew is required")
		}
		if h.AcceptSeal == nil {
			return errors.New("accept seal snapshot is required")
		}
		if err := h.AcceptSeal.Validate(); err != nil {
			return fmt.Errorf("accept seal: %w", err)
		}
		if h.AcceptedAt == nil || h.AcceptedAt.IsZero() {
			return errors.New("accepted_at is required")
		}
		if h.AcceptedAt.Before(h.ReleasedAt) {
			return errors.New("accepted_at must not precede released_at")
		}
	}
	return nil
}

// SealMismatch compares the two sides' seal records. A different seal
// number, or an intact seal released but broken on acceptance, is a mismatch.
func (h Handover) SealMismatch() bool {
	if h.Status != HandoverAccepted || h.AcceptSeal == nil {
		return false
	}
	if h.AcceptSeal.Number != h.ReleaseSeal.Number {
		return true
	}
	return h.ReleaseSeal.State == SealIntact && h.AcceptSeal.State == SealBroken
}
