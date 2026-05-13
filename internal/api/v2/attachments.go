package v2

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// handleGetAttachment serves /api/v2/attachments/{id}.
func (h *Handler) handleGetAttachment(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Attachment ID must be a number")
		return
	}
	if h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}

	att, err := h.Store.GetAttachmentByIDV2(id)
	if err != nil {
		h.Logger.Error("get attachment v2", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve attachment")
		return
	}
	if att == nil {
		writeError(w, http.StatusNotFound, "not_found", "Attachment not found")
		return
	}
	writeJSON(w, http.StatusOK, AttachmentDetail{
		ID:                att.ID,
		MessageID:         att.MessageID,
		ThreadID:          att.ThreadID,
		Account:           att.AccountEmail,
		Filename:          att.Filename,
		MimeType:          att.MimeType,
		SizeBytes:         att.SizeBytes,
		ContentHash:       att.ContentHash,
		InlineDisposition: isInlineSafeMIME(att.MimeType),
	})
}
