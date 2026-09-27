package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-036/internal/custody"
	"github.com/wyw14/solo-036/internal/model"
	"github.com/wyw14/solo-036/internal/service"
)

type custodianRequest struct {
	StationID string `json:"station_id"`
	Crew      string `json:"crew"`
	Keeper    string `json:"keeper"`
}

type collectPigRequest struct {
	PigID       string           `json:"pig_id"`
	StationID   string           `json:"station_id"`
	Crew        string           `json:"crew"`
	CollectedBy string           `json:"collected_by"`
	SealStatus  model.SealStatus `json:"seal_status"`
	SealNo      string           `json:"seal_no"`
	Location    string           `json:"location"`
	CollectedAt string           `json:"collected_at"`
	Note        string           `json:"note"`
}

type handoverRequest struct {
	To         custodianRequest `json:"to"`
	Location   string           `json:"location"`
	SealStatus model.SealStatus `json:"seal_status"`
	SealNo     string           `json:"seal_no"`
	HandedBy   string           `json:"handed_by"`
	HandedAt   string           `json:"handed_at"`
	Note       string           `json:"note"`
}

type receiptRequest struct {
	ReceivedBy string           `json:"received_by"`
	ReceivedAt string           `json:"received_at"`
	Location   string           `json:"location"`
	SealStatus model.SealStatus `json:"seal_status"`
	SealNo     string           `json:"seal_no"`
	Note       string           `json:"note"`
}

type rejectRequest struct {
	ReceivedBy   string `json:"received_by"`
	ReceivedAt   string `json:"received_at"`
	RejectReason string `json:"reject_reason"`
	Note         string `json:"note"`
}

func custodyErrorStatus(err error) int {
	switch {
	case errors.Is(err, custody.ErrHandoverPending),
		errors.Is(err, custody.ErrPigCollected),
		errors.Is(err, custody.ErrCustodianMatch):
		return http.StatusConflict
	case errors.Is(err, custody.ErrPigNotCollected):
		return http.StatusNotFound
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "not found"),
		strings.Contains(message, "no custody record"),
		strings.Contains(message, "has no collection"):
		return http.StatusNotFound
	case strings.Contains(message, "cannot"):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

func (h *Handler) CollectPig(w http.ResponseWriter, r *http.Request) {
	var request collectPigRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	collectedAt, err := parseObservedAt(request.CollectedAt)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	runID := chi.URLParam(r, "runID")
	stationID := request.StationID
	if strings.TrimSpace(stationID) == "" {
		stationID = h.receiverStationForRun(runID)
	}
	chain, err := h.runtime.RegisterCollection(runID, stationID, service.PigCollectionInput{
		PigID:       request.PigID,
		Crew:        request.Crew,
		CollectedBy: request.CollectedBy,
		SealStatus:  request.SealStatus,
		SealNo:      request.SealNo,
		Location:    request.Location,
		CollectedAt: collectedAt,
		Note:        request.Note,
	})
	if err != nil {
		writeError(w, r, custodyErrorStatus(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, chain)
}

// receiverStationForRun resolves the receiver station associated with a run
// from its route lease, falling back to the configured receiver station.
func (h *Handler) receiverStationForRun(runID string) string {
	operation, err := h.runtime.Operation(runID)
	if err != nil {
		return ""
	}
	if operation.Route.ToStation != "" {
		return operation.Route.ToStation
	}
	for _, station := range h.runtime.Snapshot().Stations {
		if station.Role == model.StationReceiver {
			return station.ID
		}
	}
	return ""
}

func (h *Handler) HandOverPig(w http.ResponseWriter, r *http.Request) {
	var request handoverRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	handedAt, err := parseObservedAt(request.HandedAt)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	record, err := h.runtime.HandOverPig(chi.URLParam(r, "pigID"), service.PigHandoverInput{
		To: model.Custodian{
			StationID: request.To.StationID,
			Crew:      request.To.Crew,
			Keeper:    request.To.Keeper,
		},
		Location:   request.Location,
		SealStatus: request.SealStatus,
		SealNo:     request.SealNo,
		HandedBy:   request.HandedBy,
		HandedAt:   handedAt,
		Note:       request.Note,
	})
	if err != nil {
		writeError(w, r, custodyErrorStatus(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, record)
}

func (h *Handler) ReceiveHandover(w http.ResponseWriter, r *http.Request) {
	var request receiptRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	receivedAt, err := parseObservedAt(request.ReceivedAt)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	record, err := h.runtime.ReceivePig(chi.URLParam(r, "pigID"), service.PigReceiptInput{
		HandoverID: chi.URLParam(r, "handoverID"),
		ReceivedBy: request.ReceivedBy,
		ReceivedAt: receivedAt,
		Location:   request.Location,
		SealStatus: request.SealStatus,
		SealNo:     request.SealNo,
		Note:       request.Note,
	})
	if err != nil {
		writeError(w, r, custodyErrorStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (h *Handler) RejectHandover(w http.ResponseWriter, r *http.Request) {
	var request rejectRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	rejectedAt, err := parseObservedAt(request.ReceivedAt)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	record, err := h.runtime.ReceivePig(chi.URLParam(r, "pigID"), service.PigReceiptInput{
		HandoverID:   chi.URLParam(r, "handoverID"),
		ReceivedBy:   request.ReceivedBy,
		ReceivedAt:   rejectedAt,
		Rejected:     true,
		RejectReason: request.RejectReason,
		Note:         request.Note,
	})
	if err != nil {
		writeError(w, r, custodyErrorStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (h *Handler) CustodyChain(w http.ResponseWriter, r *http.Request) {
	chain, err := h.runtime.CustodyChain(chi.URLParam(r, "pigID"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, chain)
}

func (h *Handler) CustodyChains(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.runtime.CustodyChains())
}

func (h *Handler) CustodyReport(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.runtime.CustodyReconcile())
}
