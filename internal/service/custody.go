package service

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wyw14/solo-036/internal/custody"
	"github.com/wyw14/solo-036/internal/model"
)

type PigCollectionInput struct {
	PigID       string
	Crew        string
	CollectedBy string
	SealStatus  model.SealStatus
	SealNo      string
	Location    string
	CollectedAt time.Time
	Note        string
}

type PigHandoverInput struct {
	To         model.Custodian
	Location   string
	SealStatus model.SealStatus
	SealNo     string
	HandedBy   string
	HandedAt   time.Time
	Note       string
}

type PigReceiptInput struct {
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

func runAfterReceive(state model.RunState) bool {
	switch state {
	case model.StateReceiving, model.StateDepressurizing, model.StateComplete:
		return true
	default:
		return false
	}
}

// RegisterCollection records that a pig retrieved from a finished receiver run
// entered custody. The run must already have passed its receive step so that
// the collection record can be cross-checked against the receiving journal event.
func (r *Runtime) RegisterCollection(runID, stationID string, input PigCollectionInput) (model.CustodyChain, error) {
	runRecord, ok := r.Runs.Get(runID)
	if !ok {
		return model.CustodyChain{}, fmt.Errorf("run %s not found", runID)
	}
	if !runAfterReceive(runRecord.State) {
		return model.CustodyChain{}, fmt.Errorf("run state %s cannot register a collected pig", runRecord.State)
	}
	control := r.ensureControl()
	station, ok := control.Stations[strings.TrimSpace(stationID)]
	if !ok {
		return model.CustodyChain{}, fmt.Errorf("unknown collection station %s", stationID)
	}
	if station.Role != model.StationReceiver {
		return model.CustodyChain{}, fmt.Errorf("station %s is not a receiver", stationID)
	}
	collectedAt := input.CollectedAt
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}
	record, err := r.Custody.Collect(custody.CollectionInput{
		PigID:       input.PigID,
		RunID:       runID,
		StationID:   station.ID,
		Crew:        input.Crew,
		CollectedBy: input.CollectedBy,
		SealStatus:  input.SealStatus,
		SealNo:      input.SealNo,
		Location:    input.Location,
		CollectedAt: collectedAt,
		Note:        input.Note,
	})
	if err != nil {
		return model.CustodyChain{}, err
	}
	if err := r.appendEvent(model.Event{
		RunID:   runID,
		Kind:    "pig_collected",
		Payload: record.PigID,
		At:      record.CollectedAt,
	}); err != nil {
		return model.CustodyChain{}, fmt.Errorf("journal: %w", err)
	}
	r.bumpRevision()
	chain, _ := r.Custody.Chain(record.PigID)
	return chain, nil
}

func (r *Runtime) bumpRevision() {
	r.mu.Lock()
	if r.control == nil {
		r.control = defaultControlState()
	}
	r.control.Revision = model.NewRevision()
	r.mu.Unlock()
}

func (r *Runtime) HandOverPig(pigID string, input PigHandoverInput) (model.HandoverRecord, error) {
	control := r.ensureControl()
	toStation, ok := control.Stations[strings.TrimSpace(input.To.StationID)]
	if !ok {
		return model.HandoverRecord{}, fmt.Errorf("unknown destination station %s", input.To.StationID)
	}
	if !toStation.Enabled {
		return model.HandoverRecord{}, fmt.Errorf("destination station %s is disabled", toStation.ID)
	}
	handedAt := input.HandedAt
	if handedAt.IsZero() {
		handedAt = time.Now().UTC()
	}
	record, err := r.Custody.HandOver(custody.HandoverInput{
		PigID:      pigID,
		To:         input.To,
		Location:   input.Location,
		SealStatus: input.SealStatus,
		SealNo:     input.SealNo,
		HandedBy:   input.HandedBy,
		HandedAt:   handedAt,
		Note:       input.Note,
	})
	if err != nil {
		return model.HandoverRecord{}, err
	}
	current, _ := r.Custody.Current(pigID)
	if err := r.appendEvent(model.Event{
		RunID:   current.RunID,
		Kind:    "pig_handover",
		Payload: record.PigID + "/" + record.ID,
		At:      record.HandedAt,
	}); err != nil {
		return model.HandoverRecord{}, fmt.Errorf("journal: %w", err)
	}
	r.bumpRevision()
	return record, nil
}

func (r *Runtime) ReceivePig(pigID string, input PigReceiptInput) (model.HandoverRecord, error) {
	receivedAt := input.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	record, err := r.Custody.Receive(custody.ReceiptInput{
		PigID:        pigID,
		HandoverID:   input.HandoverID,
		ReceivedBy:   input.ReceivedBy,
		ReceivedAt:   receivedAt,
		Location:     input.Location,
		SealStatus:   input.SealStatus,
		SealNo:       input.SealNo,
		Note:         input.Note,
		Rejected:     input.Rejected,
		RejectReason: input.RejectReason,
	})
	if err != nil {
		return model.HandoverRecord{}, err
	}
	kind := "pig_received"
	if record.Rejected {
		kind = "pig_rejected"
	}
	closedAt := record.HandedAt
	if record.ReceivedAt != nil {
		closedAt = *record.ReceivedAt
	} else if record.RejectedAt != nil {
		closedAt = *record.RejectedAt
	}
	current, _ := r.Custody.Current(pigID)
	if err := r.appendEvent(model.Event{
		RunID:   current.RunID,
		Kind:    kind,
		Payload: record.PigID + "/" + record.ID,
		At:      closedAt,
	}); err != nil {
		return model.HandoverRecord{}, fmt.Errorf("journal: %w", err)
	}
	r.bumpRevision()
	return record, nil
}

func (r *Runtime) CustodyChain(pigID string) (model.CustodyChain, error) {
	chain, ok := r.Custody.Chain(pigID)
	if !ok {
		return model.CustodyChain{}, fmt.Errorf("pig %s has no custody record", pigID)
	}
	return chain, nil
}

func (r *Runtime) CustodyChains() []model.CustodyChain { return r.Custody.Chains() }

type receiveJournalEvent struct {
	At      time.Time
	Station string
}

// CustodyReconcile cross-checks pig collection/handover records against the
// operation journal: every receiving event must have a matching collection and
// vice versa, station and timing must agree, and seal discrepancies are surfaced.
// Pending (unreceived) handovers are listed separately and do not by themselves
// make the ledger inconsistent, but they block the next handover operationally.
func (r *Runtime) CustodyReconcile() model.CustodyReport {
	report := model.CustodyReport{
		CheckedAt: time.Now().UTC(),
		Findings:  make([]model.CustodyFinding, 0),
		Pending:   make([]model.HandoverRecord, 0),
	}
	received := make(map[string][]receiveJournalEvent)
	for _, event := range r.Journal.Events() {
		if event.Kind == "receiving" {
			received[event.RunID] = append(received[event.RunID], receiveJournalEvent{At: event.At, Station: event.Payload})
		}
	}
	collectedRuns := make(map[string]bool)
	for _, chain := range r.Custody.Chains() {
		collection := chain.Collection
		collectedRuns[collection.RunID] = true
		if _, ok := r.Runs.Get(collection.RunID); !ok {
			report.Findings = append(report.Findings, model.CustodyFinding{
				Kind:   "run_missing",
				PigID:  collection.PigID,
				RunID:  collection.RunID,
				Detail: "collection references a run that no longer exists",
			})
		}
		events := received[collection.RunID]
		if len(events) == 0 {
			report.Findings = append(report.Findings, model.CustodyFinding{
				Kind:   "collection_without_receive",
				PigID:  collection.PigID,
				RunID:  collection.RunID,
				Detail: "pig was collected but the run has no receiving journal event",
			})
		} else {
			matched := false
			for _, event := range events {
				if event.Station == collection.StationID {
					matched = true
					if collection.CollectedAt.Before(event.At) {
						report.Findings = append(report.Findings, model.CustodyFinding{
							Kind:   "collected_before_received",
							PigID:  collection.PigID,
							RunID:  collection.RunID,
							Detail: fmt.Sprintf("collected at %s before run receiving event at %s", collection.CollectedAt.Format(time.RFC3339), event.At.Format(time.RFC3339)),
						})
					}
					break
				}
			}
			if !matched {
				report.Findings = append(report.Findings, model.CustodyFinding{
					Kind:   "station_mismatch",
					PigID:  collection.PigID,
					RunID:  collection.RunID,
					Detail: fmt.Sprintf("collection station %s does not match receiving event station", collection.StationID),
				})
			}
		}
		for _, handover := range chain.Handovers {
			if handover.Pending() {
				report.Pending = append(report.Pending, handover)
				continue
			}
			if handover.Rejected {
				continue
			}
			if handover.ReceiveSealStatus != handover.SealStatus || handover.ReceiveSealNo != handover.SealNo {
				report.Findings = append(report.Findings, model.CustodyFinding{
					Kind:       "seal_discrepancy",
					PigID:      collection.PigID,
					RunID:      collection.RunID,
					HandoverID: handover.ID,
					Detail: fmt.Sprintf(
						"seal changed from %s/%s at handover to %s/%s at receipt",
						handover.SealStatus, handover.SealNo, handover.ReceiveSealStatus, handover.ReceiveSealNo,
					),
				})
			}
		}
	}
	for runID, events := range received {
		if collectedRuns[runID] {
			continue
		}
		report.Findings = append(report.Findings, model.CustodyFinding{
			Kind:   "receive_without_collection",
			RunID:  runID,
			Detail: fmt.Sprintf("run receiving event at %s has no pig collection record", events[0].At.Format(time.RFC3339)),
		})
	}
	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].PigID == report.Findings[j].PigID {
			return report.Findings[i].Kind < report.Findings[j].Kind
		}
		return report.Findings[i].PigID < report.Findings[j].PigID
	})
	sort.Slice(report.Pending, func(i, j int) bool { return report.Pending[i].HandedAt.Before(report.Pending[j].HandedAt) })
	report.Consistent = len(report.Findings) == 0
	return report
}

// autoRegisterReceivedPig creates a collection record on behalf of the receive
// workflow. Fields the operator did not supply fall back to receive-workflow
// defaults so the custody chain always starts from a real receiving event.
func (r *Runtime) autoRegisterReceivedPig(runID, stationID string, input PigCollectionInput, at time.Time) {
	if strings.TrimSpace(input.Crew) == "" {
		input.Crew = stationID + "-ops"
	}
	if strings.TrimSpace(input.CollectedBy) == "" {
		input.CollectedBy = "receive-workflow"
	}
	if input.SealStatus == "" {
		input.SealStatus = model.SealNone
	}
	if strings.TrimSpace(input.Location) == "" {
		input.Location = stationID + "/receiver-barrel"
	}
	if input.CollectedAt.IsZero() {
		input.CollectedAt = at
	}
	if input.Note == "" {
		input.Note = "auto-registered during receive workflow"
	}
	record, err := r.Custody.Collect(custody.CollectionInput{
		PigID:       input.PigID,
		RunID:       runID,
		StationID:   stationID,
		Crew:        input.Crew,
		CollectedBy: input.CollectedBy,
		SealStatus:  input.SealStatus,
		SealNo:      input.SealNo,
		Location:    input.Location,
		CollectedAt: input.CollectedAt,
		Note:        input.Note,
	})
	if err != nil {
		r.recordIncident(runID, "warning", fmt.Sprintf("auto-register pig %s: %s", input.PigID, err.Error()))
		return
	}
	_ = r.appendEvent(model.Event{
		RunID:   runID,
		Kind:    "pig_collected",
		Payload: record.PigID,
		At:      record.CollectedAt,
	})
}
