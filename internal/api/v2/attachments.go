package v2

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// isHexSHA256 reports whether s is exactly 64 hex characters (the wire
// shape for a SHA-256 digest). Anything else gets a 400 before the
// store query — bounding the input keeps the by-hash endpoint from
// becoming a probe surface for arbitrary-shape input through the DB.
func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

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

// handleGetAttachmentByHash serves /api/v2/attachments/by-hash/{sha256}.
// Resolves the hash to the lowest-id attachment row carrying it and
// returns the same AttachmentDetail shape as the by-id endpoint, with
// an extra `occurrences` field set when the same bytes appear on
// multiple messages.
//
// Use case: stable cross-archive identifiers. Attachment ids are
// per-database sqlite rowids and shift across re-imports, but the
// content hash is the same bytes anywhere — so embedding URLs / cite
// references should key on the hash, not the id.
func (h *Handler) handleGetAttachmentByHash(w http.ResponseWriter, r *http.Request) {
	rawHash := chi.URLParam(r, "sha256")
	hash := strings.ToLower(rawHash)
	if !isHexSHA256(hash) {
		writeError(w, http.StatusBadRequest, "invalid_hash",
			"content hash must be 64 hex characters (SHA-256)")
		return
	}
	if h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}

	id, occurrences, err := h.Store.GetAttachmentIDByHash(hash)
	if err != nil {
		h.Logger.Error("attachment by hash lookup", "hash", hash, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve attachment hash")
		return
	}
	if id == 0 {
		writeError(w, http.StatusNotFound, "not_found", "No attachment matches that content hash")
		return
	}

	att, err := h.Store.GetAttachmentByIDV2(id)
	if err != nil {
		h.Logger.Error("attachment by hash hydrate", "hash", hash, "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve attachment")
		return
	}
	if att == nil {
		// Race: row vanished between hash lookup and detail fetch
		// (deletion during the request). Treat as a miss.
		writeError(w, http.StatusNotFound, "not_found", "No attachment matches that content hash")
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
		Occurrences:       occurrences,
	})
}
