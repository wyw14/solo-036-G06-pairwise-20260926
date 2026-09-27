package model

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type SealStatus string

const (
	SealIntact   SealStatus = "intact"
	SealBroken   SealStatus = "broken"
	SealNone     SealStatus = "none"
	SealResealed SealStatus = "resealed"
)

func ValidSealStatus(s SealStatus) bool {
	switch s {
	case SealIntact, SealBroken, SealNone, SealResealed:
		return true
	default:
		return false
	}
}

func sealNeedsNumber(s SealStatus) bool { return s == SealIntact || s == SealResealed }

type Custodian struct {
	StationID string `json:"station_id"`
	Crew      string `json:"crew"`
	Keeper    string `json:"keeper"`
}

func (c Custodian) Validate() error {
	if strings.TrimSpace(c.StationID) == "" {
		return errors.New("custodian station is required")
	}
	if strings.TrimSpace(c.Crew) == "" {
		return errors.New("custodian crew is required")
	}
	if strings.TrimSpace(c.Keeper) == "" {
		return errors.New("custodian keeper is required")
	}
	return nil
}

type CollectionRecord struct {
	PigID       string     `json:"pig_id"`
	RunID       string     `json:"run_id"`
	StationID   string     `json:"station_id"`
	Crew        string     `json:"crew"`
	CollectedBy string     `json:"collected_by"`
	SealStatus  SealStatus `json:"seal_status"`
	SealNo      string     `json:"seal_no,omitempty"`
	Location    string     `json:"location"`
	CollectedAt time.Time  `json:"collected_at"`
	Note        string     `json:"note,omitempty"`
}

func (c CollectionRecord) Validate() error {
	if strings.TrimSpace(c.PigID) == "" {
		return errors.New("pig id is required")
	}
	if strings.TrimSpace(c.RunID) == "" {
		return errors.New("collection run is required")
	}
	if strings.TrimSpace(c.StationID) == "" {
		return errors.New("collection station is required")
	}
	if strings.TrimSpace(c.Crew) == "" || strings.TrimSpace(c.CollectedBy) == "" {
		return errors.New("collection crew and collector are required")
	}
	if !ValidSealStatus(c.SealStatus) {
		return fmt.Errorf("invalid seal status %q", c.SealStatus)
	}
	if sealNeedsNumber(c.SealStatus) && strings.TrimSpace(c.SealNo) == "" {
		return fmt.Errorf("seal number is required when seal is %s", c.SealStatus)
	}
	if strings.TrimSpace(c.Location) == "" {
		return errors.New("collection location is required")
	}
	if c.CollectedAt.IsZero() {
		return errors.New("collection time is required")
	}
	return nil
}

type HandoverRecord struct {
	ID                string     `json:"id"`
	PigID             string     `json:"pig_id"`
	Seq               int        `json:"seq"`
	From              Custodian  `json:"from"`
	To                Custodian  `json:"to"`
	Location          string     `json:"location"`
	SealStatus        SealStatus `json:"seal_status"`
	SealNo            string     `json:"seal_no,omitempty"`
	HandedBy          string     `json:"handed_by"`
	HandedAt          time.Time  `json:"handed_at"`
	ReceivedBy        string     `json:"received_by,omitempty"`
	ReceivedAt        *time.Time `json:"received_at,omitempty"`
	ReceiveLocation   string     `json:"receive_location,omitempty"`
	ReceiveSealStatus SealStatus `json:"receive_seal_status,omitempty"`
	ReceiveSealNo     string     `json:"receive_seal_no,omitempty"`
	Rejected          bool       `json:"rejected,omitempty"`
	RejectReason      string     `json:"reject_reason,omitempty"`
	RejectedAt        *time.Time `json:"rejected_at,omitempty"`
	Note              string     `json:"note,omitempty"`
}

func (h HandoverRecord) Pending() bool { return h.ReceivedAt == nil && h.RejectedAt == nil }

func (h HandoverRecord) Validate() error {
	if strings.TrimSpace(h.ID) == "" || strings.TrimSpace(h.PigID) == "" {
		return errors.New("handover identity is incomplete")
	}
	if h.Seq <= 0 {
		return errors.New("handover sequence must be positive")
	}
	if err := h.From.Validate(); err != nil {
		return fmt.Errorf("handover origin: %w", err)
	}
	if err := h.To.Validate(); err != nil {
		return fmt.Errorf("handover destination: %w", err)
	}
	if strings.TrimSpace(h.Location) == "" {
		return errors.New("handover location is required")
	}
	if !ValidSealStatus(h.SealStatus) {
		return fmt.Errorf("invalid seal status %q", h.SealStatus)
	}
	if sealNeedsNumber(h.SealStatus) && strings.TrimSpace(h.SealNo) == "" {
		return fmt.Errorf("seal number is required when seal is %s", h.SealStatus)
	}
	if strings.TrimSpace(h.HandedBy) == "" {
		return errors.New("handover operator is required")
	}
	if h.HandedAt.IsZero() {
		return errors.New("handover time is required")
	}
	if h.Rejected && h.ReceivedAt != nil {
		return errors.New("handover cannot be both rejected and received")
	}
	if h.ReceivedAt != nil {
		if strings.TrimSpace(h.ReceivedBy) == "" {
			return errors.New("handover receiver is required")
		}
		if h.ReceivedAt.Before(h.HandedAt) {
			return errors.New("handover cannot be received before it is handed")
		}
		if h.ReceiveSealStatus != "" && !ValidSealStatus(h.ReceiveSealStatus) {
			return fmt.Errorf("invalid receive seal status %q", h.ReceiveSealStatus)
		}
		if sealNeedsNumber(h.ReceiveSealStatus) && strings.TrimSpace(h.ReceiveSealNo) == "" {
			return fmt.Errorf("receive seal number is required when seal is %s", h.ReceiveSealStatus)
		}
	}
	if h.Rejected {
		if strings.TrimSpace(h.RejectReason) == "" {
			return errors.New("rejection reason is required")
		}
		if h.RejectedAt == nil {
			return errors.New("rejection time is required")
		}
		if h.RejectedAt.Before(h.HandedAt) {
			return errors.New("handover cannot be rejected before it is handed")
		}
	}
	return nil
}

func ValidateHandoverChain(handovers []HandoverRecord) error {
	for i, h := range handovers {
		if err := h.Validate(); err != nil {
			return fmt.Errorf("handover %s: %w", h.ID, err)
		}
		if h.Seq != i+1 {
			return fmt.Errorf("handover %s: sequence gap", h.ID)
		}
		if i > 0 {
			previous := handovers[i-1]
			if previous.Pending() {
				return fmt.Errorf("handover %s: previous handover %s is still pending", h.ID, previous.ID)
			}
			expectedFrom := previous.To
			if previous.Rejected {
				// A rejected handover leaves the equipment with the original custodian.
				expectedFrom = previous.From
			}
			if h.From != expectedFrom {
				return fmt.Errorf("handover %s: origin does not match current custodian", h.ID)
			}
		}
	}
	return nil
}

type PigCustody struct {
	PigID             string     `json:"pig_id"`
	RunID             string     `json:"run_id"`
	StationID         string     `json:"station_id"`
	Collected         bool       `json:"collected"`
	Current           Custodian  `json:"current"`
	Location          string     `json:"location"`
	SealStatus        SealStatus `json:"seal_status"`
	SealNo            string     `json:"seal_no,omitempty"`
	PendingHandoverID string     `json:"pending_handover_id,omitempty"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type CustodyChain struct {
	PigID      string           `json:"pig_id"`
	Collection CollectionRecord `json:"collection"`
	Handovers  []HandoverRecord `json:"handovers"`
	Custody    PigCustody       `json:"custody"`
}

type CustodyFinding struct {
	Kind       string `json:"kind"`
	PigID      string `json:"pig_id,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	HandoverID string `json:"handover_id,omitempty"`
	Detail     string `json:"detail"`
}

type CustodyReport struct {
	CheckedAt  time.Time        `json:"checked_at"`
	Consistent bool             `json:"consistent"`
	Findings   []CustodyFinding `json:"findings"`
	Pending    []HandoverRecord `json:"pending"`
}
