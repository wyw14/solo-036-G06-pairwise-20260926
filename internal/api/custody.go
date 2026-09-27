package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-036/internal/model"
	"github.com/wyw14/solo-036/internal/service"
)

type registerPigRequest struct {
	Code    string `json:"code"`
	RunID   string `json:"run_id"`
	Station string `json:"station"`
}

type releaseHandoverRequest struct {
	Station    string             `json:"station"`
	Crew       string             `json:"crew"`
	Seal       model.SealSnapshot `json:"seal"`
	ReleasedAt string             `json:"released_at"`
}

type acceptHandoverRequest struct {
	Station    string             `json:"station"`
	Crew       string             `json:"crew"`
	Seal       model.SealSnapshot `json:"seal"`
	AcceptedAt string             `json:"accepted_at"`
}

func custodyError(w http.ResponseWriter, r *http.Request, err error) {
	writeError(w, r, service.CustodyHTTPStatus(err), err)
}

func parseOptionalTime(value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, true
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func (h *Handler) RegisterPig(w http.ResponseWriter, r *http.Request) {
	var request registerPigRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	pig, err := h.runtime.RegisterPig(service.RegisterPigRequest{
		Code:    request.Code,
		RunID:   request.RunID,
		Station: request.Station,
	})
	if err != nil {
		custodyError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, pig)
}

func (h *Handler) ListPigs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.runtime.Pigs())
}

func (h *Handler) PigChain(w http.ResponseWriter, r *http.Request) {
	chain, ok := h.runtime.PigChain(chi.URLParam(r, "code"))
	if !ok {
		writeError(w, r, http.StatusNotFound, fmt.Errorf("pig %s not found", chi.URLParam(r, "code")))
		return
	}
	writeJSON(w, http.StatusOK, chain)
}

func (h *Handler) ReleaseHandover(w http.ResponseWriter, r *http.Request) {
	var request releaseHandoverRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	releasedAt, ok := parseOptionalTime(request.ReleasedAt)
	if !ok {
		writeError(w, r, http.StatusBadRequest, errors.New("released_at must use RFC3339 format"))
		return
	}
	handover, err := h.runtime.ReleaseHandover(chi.URLParam(r, "code"), service.ReleaseHandoverRequest{
		Station:   request.Station,
		Crew:      request.Crew,
		Seal:      request.Seal,
		ReleaseAt: releasedAt,
	})
	if err != nil {
		custodyError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, handover)
}

func (h *Handler) AcceptHandover(w http.ResponseWriter, r *http.Request) {
	var request acceptHandoverRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	acceptedAt, ok := parseOptionalTime(request.AcceptedAt)
	if !ok {
		writeError(w, r, http.StatusBadRequest, errors.New("accepted_at must use RFC3339 format"))
		return
	}
	handover, err := h.runtime.AcceptHandover(
		chi.URLParam(r, "code"),
		chi.URLParam(r, "id"),
		service.AcceptHandoverRequest{
			Station:  request.Station,
			Crew:     request.Crew,
			Seal:     request.Seal,
			AcceptAt: acceptedAt,
		},
	)
	if err != nil {
		custodyError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, handover)
}

func (h *Handler) CustodyReconciliation(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.runtime.ReconcileCustody())
}
