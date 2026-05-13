package v2

import (
	"net/http"
)

// handleSearch returns search hits as v2 summaries. v1's `q` parameter
// is accepted; pagination is limit/offset for consistency with the
// rest of v2.
//
// Commit 1 carries the previous FTS-only behavior forward verbatim;
// commit 3 will replace this with the unified fts|vector|hybrid
// surface and the SearchResponse shape.
func (h *Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	if h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "missing_query", "Query parameter 'q' is required")
		return
	}
	limit, offset := parseLimitOffset(r)

	msgs, total, err := h.Store.SearchMessages(q, offset, limit)
	if err != nil {
		h.Logger.Error("search v2", "q", q, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Search failed")
		return
	}
	writeJSON(w, http.StatusOK, h.paginate(msgs, total, offset, limit))
}
