package v2

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// handleGetThread returns the same thread shape as v1 but with v2
// MessageSummary entries — structured from/to/cc, thread_id, account,
// rfc822_message_id, etc.
func (h *Handler) handleGetThread(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Thread ID must be a number")
		return
	}
	if h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}

	th, err := h.Store.GetThread(id)
	if err != nil {
		h.Logger.Error("get thread v2", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve thread")
		return
	}
	if th == nil {
		writeError(w, http.StatusNotFound, "not_found", "Thread not found")
		return
	}

	// Pull message IDs and hydrate with a single
	// GetMessagesSummariesByIDs to grab APIMessage rows, then enrich
	// structured recipients.
	ids := make([]int64, 0, len(th.Messages))
	for _, m := range th.Messages {
		ids = append(ids, m.ID)
	}
	msgs, err := h.Store.GetMessagesSummariesByIDs(ids)
	if err != nil {
		h.Logger.Error("thread v2 hydrate", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve thread messages")
		return
	}
	summaries, err := h.summaries(msgs)
	if err != nil {
		h.Logger.Error("thread v2 enrich", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to enrich thread")
		return
	}
	// Preserve the chronological order GetThread returned: re-sort by
	// the id-order of th.Messages.
	indexOf := make(map[int64]int, len(th.Messages))
	for i, m := range th.Messages {
		indexOf[m.ID] = i
	}
	orderedSummaries := make([]MessageSummary, len(summaries))
	for _, s := range summaries {
		if pos, ok := indexOf[s.ID]; ok {
			orderedSummaries[pos] = s
		}
	}

	resp := Thread{
		ID:           th.ID,
		Subject:      th.Subject,
		MessageCount: th.MessageCount,
		Participants: make([]ThreadParticipant, 0, len(th.Participants)),
		Messages:     orderedSummaries,
	}
	for _, p := range th.Participants {
		resp.Participants = append(resp.Participants, ThreadParticipant{
			ID: p.ID, Name: p.Name, Address: p.Address,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}
