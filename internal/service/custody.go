package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wyw14/solo-036/internal/custody"
	"github.com/wyw14/solo-036/internal/model"
)

// Conflict sentinels supplement the custody package sentinels so the API
// layer can distinguish 400 validation failures from 409 state conflicts.
var (
	ErrRunNotReceived         = errors.New("run has not been received")
	ErrReceiveStationMismatch = errors.New("registration station does not match receive station")
	ErrRunNotFound            = errors.New("run not found")
	ErrRunAlreadyRegistered   = errors.New("run already has a registered pig")
)

// CustodyHTTPStatus maps custody workflow errors onto HTTP status codes:
// unknown entities -> 404, state conflicts -> 409, everything else -> 400.
func CustodyHTTPStatus(err error) int {
	switch {
	case errors.Is(err, custody.ErrPigNotFound),
		errors.Is(err, custody.ErrHandoverNotFound),
		errors.Is(err, ErrRunNotFound):
		return 404
	case errors.Is(err, custody.ErrPigExists),
		errors.Is(err, ErrRunAlreadyRegistered),
		errors.Is(err, custody.ErrPendingHandover),
		errors.Is(err, custody.ErrHandoverClosed),
		errors.Is(err, custody.ErrCustodianMismatch),
		errors.Is(err, ErrRunNotReceived),
		errors.Is(err, ErrReceiveStationMismatch):
		return 409
	default:
		return 400
	}
}

type RegisterPigRequest struct {
	Code    string `json:"code"`
	RunID   string `json:"run_id"`
	Station string `json:"station"`
}

type ReleaseHandoverRequest struct {
	Station   string             `json:"station"`
	Crew      string             `json:"crew"`
	Seal      model.SealSnapshot `json:"seal"`
	ReleaseAt time.Time          `json:"-"`
}

type AcceptHandoverRequest struct {
	Station  string             `json:"station"`
	Crew     string             `json:"crew"`
	Seal     model.SealSnapshot `json:"seal"`
	AcceptAt time.Time          `json:"-"`
}

type CustodyChain struct {
	Pig            model.Pig        `json:"pig"`
	Handovers      []model.Handover `json:"handovers"`
	CurrentStation string           `json:"current_station"`
	CurrentCrew    string           `json:"current_crew,omitempty"`
	Pending        bool             `json:"pending"`
}

type CustodyIssue struct {
	Kind       string `json:"kind"`
	PigCode    string `json:"pig_code,omitempty"`
	HandoverID string `json:"handover_id,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	Detail     string `json:"detail"`
}

type CustodyReport struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Issues      []CustodyIssue `json:"issues"`
	Summary     map[string]int `json:"summary"`
}

const (
	IssueUnregisteredReceivedRun = "unregistered_received_run"
	IssueMissingRun              = "missing_run"
	IssueSealMismatch            = "seal_mismatch"
	IssueStalePending            = "stale_pending"
	IssueChainGap                = "chain_gap"
	IssueUnknownStation          = "unknown_station"
)

// receiveStations maps run IDs to their receive stations. The in-memory
// journal is empty after recovery (durable events are not replayed), so the
// persistent log is merged in and both sources are de-duplicated by event ID.
func (r *Runtime) receiveStations() map[string]string {
	control := r.ensureControl()
	merged := make(map[string]model.Event)
	for _, event := range r.Journal.Events() {
		merged[event.ID] = event
	}
	if control.Persistent != nil {
		if durable, err := control.Persistent.Events(); err == nil {
			for _, event := range durable {
				if _, ok := merged[event.ID]; !ok {
					merged[event.ID] = event
				}
			}
		}
	}
	out := make(map[string]string)
	for _, event := range merged {
		if event.Kind == "receiving" {
			out[event.RunID] = strings.TrimSpace(event.Payload)
		}
	}
	return out
}

func (r *Runtime) touchRevision() {
	r.mu.Lock()
	r.control.Revision = model.NewRevision()
	r.mu.Unlock()
}

func (r *Runtime) RegisterPig(request RegisterPigRequest) (model.Pig, error) {
	code := strings.TrimSpace(request.Code)
	if code == "" {
		return model.Pig{}, errors.New("pig code is required")
	}
	if strings.Contains(code, "/") {
		return model.Pig{}, errors.New("pig code must not contain a slash")
	}
	stationID := strings.TrimSpace(request.Station)
	control := r.ensureControl()
	station, ok := control.Stations[stationID]
	if !ok || !station.Enabled {
		return model.Pig{}, errors.New("registration station is unavailable")
	}
	runRecord, ok := r.Runs.Get(request.RunID)
	if !ok {
		return model.Pig{}, fmt.Errorf("%w: %s", ErrRunNotFound, request.RunID)
	}
	receiveStation, received := r.receiveStations()[request.RunID]
	if !received {
		return model.Pig{}, fmt.Errorf("%w: run %s", ErrRunNotReceived, request.RunID)
	}
	if runRecord.State != model.StateReceiving &&
		runRecord.State != model.StateDepressurizing &&
		runRecord.State != model.StateComplete {
		return model.Pig{}, fmt.Errorf("%w: run %s state %s", ErrRunNotReceived, request.RunID, runRecord.State)
	}
	if stationID != receiveStation {
		return model.Pig{}, fmt.Errorf("%w: expected %s, got %s", ErrReceiveStationMismatch, receiveStation, stationID)
	}
	for _, existing := range r.Custody.Pigs() {
		if existing.Code == code {
			continue // the store surfaces the duplicate-code conflict
		}
		if existing.RunID == request.RunID {
			return model.Pig{}, fmt.Errorf("%w: %s for run %s", ErrRunAlreadyRegistered, existing.Code, request.RunID)
		}
	}
	pig := model.Pig{
		Code:           code,
		RunID:          request.RunID,
		ReceiveStation: receiveStation,
		RegisteredAt:   time.Now().UTC(),
	}
	registered, err := r.Custody.Register(pig)
	if err != nil {
		return model.Pig{}, err
	}
	r.touchRevision()
	if err := r.appendEvent(model.Event{
		RunID:   request.RunID,
		Kind:    "pig_registered",
		Payload: code + "|" + receiveStation,
		At:      pig.RegisteredAt,
	}); err != nil {
		return model.Pig{}, err
	}
	return registered, nil
}

func (r *Runtime) ReleaseHandover(code string, request ReleaseHandoverRequest) (model.Handover, error) {
	pig, ok := r.Custody.GetPig(code)
	if !ok {
		return model.Handover{}, custody.ErrPigNotFound
	}
	control := r.ensureControl()
	station, ok := control.Stations[strings.TrimSpace(request.Station)]
	if !ok || !station.Enabled {
		return model.Handover{}, errors.New("release station is unavailable")
	}
	if strings.TrimSpace(request.Crew) == "" {
		return model.Handover{}, errors.New("release crew is required")
	}
	if err := request.Seal.Validate(); err != nil {
		return model.Handover{}, err
	}
	at := request.ReleaseAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	handover, err := custody.Release(r.Custody, code, custody.ReleaseInput{
		Station: station.ID,
		Crew:    strings.TrimSpace(request.Crew),
		Seal:    request.Seal,
		At:      at.UTC(),
	})
	if err != nil {
		return model.Handover{}, err
	}
	r.touchRevision()
	if err := r.appendEvent(model.Event{
		RunID:   pig.RunID,
		Kind:    "handover_release",
		Epoch:   handover.Sequence,
		Payload: code + "|" + handover.ID,
		At:      at.UTC(),
	}); err != nil {
		return model.Handover{}, err
	}
	return handover, nil
}

func (r *Runtime) AcceptHandover(code, id string, request AcceptHandoverRequest) (model.Handover, error) {
	pig, ok := r.Custody.GetPig(code)
	if !ok {
		return model.Handover{}, custody.ErrPigNotFound
	}
	control := r.ensureControl()
	station, ok := control.Stations[strings.TrimSpace(request.Station)]
	if !ok || !station.Enabled {
		return model.Handover{}, errors.New("accept station is unavailable")
	}
	if strings.TrimSpace(request.Crew) == "" {
		return model.Handover{}, errors.New("accept crew is required")
	}
	if err := request.Seal.Validate(); err != nil {
		return model.Handover{}, err
	}
	at := request.AcceptAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	handover, err := custody.Accept(r.Custody, code, id, custody.AcceptInput{
		Station: station.ID,
		Crew:    strings.TrimSpace(request.Crew),
		Seal:    request.Seal,
		At:      at.UTC(),
	})
	if err != nil {
		return model.Handover{}, err
	}
	r.touchRevision()
	payload := code + "|" + id
	if handover.SealAnomaly {
		payload += "|seal_anomaly"
	}
	if err := r.appendEvent(model.Event{
		RunID:   pig.RunID,
		Kind:    "handover_accept",
		Epoch:   handover.Sequence,
		Payload: payload,
		At:      at.UTC(),
	}); err != nil {
		return model.Handover{}, err
	}
	return handover, nil
}

func (r *Runtime) Pigs() []model.Pig {
	pigs := r.Custody.Pigs()
	if pigs == nil {
		pigs = []model.Pig{}
	}
	return pigs
}

func (r *Runtime) PigChain(code string) (CustodyChain, bool) {
	pig, ok := r.Custody.GetPig(code)
	if !ok {
		return CustodyChain{}, false
	}
	handovers, _ := r.Custody.Chain(code)
	if handovers == nil {
		handovers = []model.Handover{}
	}
	chain := CustodyChain{
		Pig:       pig,
		Handovers: handovers,
	}
	if len(handovers) == 0 {
		chain.CurrentStation = pig.ReceiveStation
		return chain, true
	}
	last := handovers[len(handovers)-1]
	if last.Status == model.HandoverPending {
		chain.Pending = true
		chain.CurrentStation = last.ReleaseStation
		chain.CurrentCrew = last.ReleaseCrew
	} else {
		chain.CurrentStation = last.AcceptStation
		chain.CurrentCrew = last.AcceptCrew
	}
	return chain, true
}

// ReconcileCustody cross-checks receive records against registered pigs and
// their handover chains and returns every inconsistency it finds.
func (r *Runtime) ReconcileCustody() CustodyReport {
	control := r.ensureControl()
	pigs := r.Custody.Pigs()
	received := r.receiveStations()
	issues := make([]CustodyIssue, 0)

	registeredRuns := make(map[string]bool)
	for _, pig := range pigs {
		registeredRuns[pig.RunID] = true

		if _, ok := r.Runs.Get(pig.RunID); !ok {
			issues = append(issues, CustodyIssue{
				Kind:    IssueMissingRun,
				PigCode: pig.Code,
				RunID:   pig.RunID,
				Detail:  "pig references a run that does not exist",
			})
		}
		if station, ok := control.Stations[pig.ReceiveStation]; !ok || !station.Enabled {
			issues = append(issues, CustodyIssue{
				Kind:    IssueUnknownStation,
				PigCode: pig.Code,
				Detail:  fmt.Sprintf("receive station %s is unavailable", pig.ReceiveStation),
			})
		}
	}

	for runID := range received {
		if !registeredRuns[runID] {
			issues = append(issues, CustodyIssue{
				Kind:   IssueUnregisteredReceivedRun,
				RunID:  runID,
				Detail: "run was received but no pig is registered for it",
			})
		}
	}

	for _, pig := range pigs {
		chain, _ := r.Custody.Chain(pig.Code)
		var previous *model.Handover
		for i := range chain {
			h := chain[i]
			if h.Status == model.HandoverAccepted && h.SealAnomaly {
				issues = append(issues, CustodyIssue{
					Kind:       IssueSealMismatch,
					PigCode:    pig.Code,
					HandoverID: h.ID,
					Detail:     "accepted seal does not match released seal",
				})
			}
			if h.Status == model.HandoverPending {
				if runRecord, ok := r.Runs.Get(pig.RunID); ok && runRecord.State.Terminal() {
					issues = append(issues, CustodyIssue{
						Kind:       IssueStalePending,
						PigCode:    pig.Code,
						HandoverID: h.ID,
						RunID:      pig.RunID,
						Detail:     "handover is still pending after the run reached a terminal state",
					})
				}
			}
			for _, stationID := range []string{h.ReleaseStation, h.AcceptStation} {
				if stationID == "" {
					continue
				}
				if station, ok := control.Stations[stationID]; !ok || !station.Enabled {
					issues = append(issues, CustodyIssue{
						Kind:       IssueUnknownStation,
						PigCode:    pig.Code,
						HandoverID: h.ID,
						Detail:     fmt.Sprintf("handover references unavailable station %s", stationID),
					})
				}
			}
			switch {
			case previous == nil:
				if h.ReleaseStation != pig.ReceiveStation {
					issues = append(issues, CustodyIssue{
						Kind:       IssueChainGap,
						PigCode:    pig.Code,
						HandoverID: h.ID,
						Detail:     "first handover does not start at the pig receive station",
					})
				}
			case previous.Status != model.HandoverAccepted:
				issues = append(issues, CustodyIssue{
					Kind:       IssueChainGap,
					PigCode:    pig.Code,
					HandoverID: h.ID,
					Detail:     "handover follows a handover that was never accepted",
				})
			case h.ReleaseStation != previous.AcceptStation || h.ReleaseCrew != previous.AcceptCrew:
				issues = append(issues, CustodyIssue{
					Kind:       IssueChainGap,
					PigCode:    pig.Code,
					HandoverID: h.ID,
					Detail:     "release side does not match the previous acceptance side",
				})
			}
			previous = &chain[i]
		}
	}

	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Kind != issues[j].Kind {
			return issues[i].Kind < issues[j].Kind
		}
		if issues[i].RunID != issues[j].RunID {
			return issues[i].RunID < issues[j].RunID
		}
		if issues[i].PigCode != issues[j].PigCode {
			return issues[i].PigCode < issues[j].PigCode
		}
		return issues[i].HandoverID < issues[j].HandoverID
	})
	summary := make(map[string]int)
	for _, issue := range issues {
		summary[issue.Kind]++
	}
	return CustodyReport{
		GeneratedAt: time.Now().UTC(),
		Issues:      issues,
		Summary:     summary,
	}
}
