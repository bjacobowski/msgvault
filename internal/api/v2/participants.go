package v2

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// handleGetParticipant serves /api/v2/participants/{id}.
func (h *Handler) handleGetParticipant(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Participant ID must be a number")
		return
	}
	if h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}

	p, err := h.Store.GetParticipantByIDV2(id)
	if err != nil {
		h.Logger.Error("get participant v2", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve participant")
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "not_found", "Participant not found")
		return
	}

	resp := Participant{
		ID:            p.ID,
		Name:          p.Name,
		Address:       p.Address,
		Domain:        p.Domain,
		MessageCount:  p.MessageCount,
		IsUserAccount: p.IsUserAccount,
	}
	if !p.FirstSeen.IsZero() {
		resp.FirstSeen = p.FirstSeen.UTC().Format(time.RFC3339)
	}
	if !p.LastSeen.IsZero() {
		resp.LastSeen = p.LastSeen.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}
