package v2

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/wesm/msgvault/internal/search"
	"github.com/wesm/msgvault/internal/search/hybridapi"
	"github.com/wesm/msgvault/internal/store"
	"github.com/wesm/msgvault/internal/vector"
	"github.com/wesm/msgvault/internal/vector/hybrid"
)

// handleSearch is the unified v2 search endpoint. It accepts the same
// `q` and `mode` parameters as /api/v1/search (fts|vector|hybrid) and
// emits one stable SearchResponse shape across all three modes:
//
//   - Top-level keys {query, mode, offset, limit, total, returned,
//     messages} are always present.
//   - Vector/hybrid additionally include {generation, pool_saturated,
//     took_ms}.
//   - Total is a *int64: a number for fts (global corpus count), null
//     for vector/hybrid (no global total over a top-k relevance pool).
//
// This is the fix for the v1 contract break that motivated /api/v2:
// v1's `mode` parameter swung the wire shape between two
// incompatible envelopes (messages/total vs results/returned), which
// broke any typed client that switched modes. v2's unified shape lets
// a single Zod schema (or equivalent) decode every successful
// response.
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

	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "fts"
	}
	explain := r.URL.Query().Get("explain") == "1"
	limit, offset := parseLimitOffset(r)

	switch mode {
	case "fts":
		h.searchFTS(w, q, offset, limit)
	case "vector", "hybrid":
		// Pagination rejection comes before any vector engine work so
		// the error message is consistent regardless of whether the
		// engine would have succeeded — clients learn the rule from
		// the first bad request.
		if offset > 0 {
			writeError(w, http.StatusBadRequest, "pagination_unsupported",
				"mode=vector|hybrid ranks a top-k relevance pool; stable random access beyond offset 0 is not defined — raise `limit` to widen the pool")
			return
		}
		if h.Engine == nil {
			writeError(w, http.StatusServiceUnavailable, "vector_not_enabled",
				"vector search is not configured on this server")
			return
		}
		if maxPage := h.VectorCfg.Search.MaxPageSizeHybridClamp(); maxPage > 0 && limit > maxPage {
			limit = maxPage
		}
		h.searchHybrid(w, r, q, mode, explain, limit)
	default:
		writeError(w, http.StatusBadRequest, "invalid_mode",
			"mode must be one of fts|vector|hybrid, got "+strconv.Quote(mode))
	}
}

// searchFTS runs the FTS path. Mirrors v1's operator detection so a
// query with structured operators (from:, to:, label:, has:, ...) is
// routed through SearchMessagesQuery and a plain query goes through
// the simpler SearchMessages. Without this parity, the same query
// would behave differently on /api/v1/search and /api/v2/search.
func (h *Handler) searchFTS(w http.ResponseWriter, q string, offset, limit int) {
	parsedQuery := search.Parse(q)
	parsedQuery.HideDeleted = true

	var (
		msgs  []store.APIMessage
		total int64
		err   error
	)
	if parsedQuery.HasOperators() {
		msgs, total, err = h.Store.SearchMessagesQuery(parsedQuery, offset, limit)
	} else {
		msgs, total, err = h.Store.SearchMessages(q, offset, limit)
	}
	if err != nil {
		h.Logger.Error("v2 fts search", "q", q, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Search failed")
		return
	}

	hits, err := h.searchHits(msgs, nil)
	if err != nil {
		h.Logger.Error("v2 fts enrich", "q", q, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to enrich search results")
		return
	}

	t := total
	writeJSON(w, http.StatusOK, SearchResponse{
		Query:    q,
		Mode:     "fts",
		Offset:   offset,
		Limit:    limit,
		Total:    &t,
		Returned: len(hits),
		Messages: hits,
	})
}

// searchHybrid runs the vector or hybrid path through the shared
// hybridapi.Run helper. Sentinel errors are translated to v2 wire
// codes here (each API surface owns its own envelope).
func (h *Handler) searchHybrid(w http.ResponseWriter, r *http.Request, q, mode string, explain bool, limit int) {
	start := time.Now()
	result, err := hybridapi.Run(r.Context(), h.Engine, h.Store, h.Logger, hybridapi.Request{
		Query:   q,
		Mode:    hybrid.Mode(mode),
		Limit:   limit,
		Explain: explain,
	})
	if err != nil {
		h.writeHybridErr(w, q, mode, err)
		return
	}

	hits, herr := h.searchHits(result.Msgs, result.ScoresByID)
	if herr != nil {
		h.Logger.Error("v2 hybrid enrich", "q", q, "mode", mode, "error", herr)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to enrich search results")
		return
	}

	tookMS := time.Since(start).Milliseconds()
	poolSat := result.Meta.PoolSaturated
	gen := &Generation{
		ID:          int64(result.Meta.Generation.ID),
		Model:       result.Meta.Generation.Model,
		Dimension:   result.Meta.Generation.Dimension,
		Fingerprint: result.Meta.Generation.Fingerprint,
		State:       string(result.Meta.Generation.State),
	}
	writeJSON(w, http.StatusOK, SearchResponse{
		Query:         q,
		Mode:          mode,
		Offset:        0, // vector/hybrid reject offset>0 upstream
		Limit:         limit,
		Total:         nil, // explicit JSON null — no global total over a top-k pool
		Returned:      len(hits),
		Messages:      hits,
		Generation:    gen,
		PoolSaturated: &poolSat,
		TookMS:        &tookMS,
	})
}

// searchHits enriches the message rows into v2 summaries (structured
// recipients + v2 meta fields) and attaches per-hit Score blocks from
// scoresByID when present. scoresByID may be nil — fts hits carry no
// score.
func (h *Handler) searchHits(msgs []store.APIMessage, scoresByID map[int64]*hybridapi.ScoreBreakdown) ([]SearchHit, error) {
	summaries, err := h.summaries(msgs)
	if err != nil {
		return nil, err
	}
	out := make([]SearchHit, len(summaries))
	for i, s := range summaries {
		hit := SearchHit{MessageSummary: s}
		if scoresByID != nil {
			if sb := scoresByID[s.ID]; sb != nil {
				hit.Score = &ScoreBreakdown{
					RRF:            sb.RRF,
					BM25:           sb.BM25,
					Vector:         sb.Vector,
					SubjectBoosted: sb.SubjectBoosted,
				}
			}
		}
		out[i] = hit
	}
	return out, nil
}

// writeHybridErr maps hybridapi sentinel errors to v2 wire codes.
// Duplicated from internal/api's v1 mapper rather than imported to
// keep v2 free of any back-edge into the v1 package.
func (h *Handler) writeHybridErr(w http.ResponseWriter, q, mode string, err error) {
	switch {
	case errors.Is(err, hybridapi.ErrMissingFreeText):
		writeError(w, http.StatusBadRequest, "missing_free_text",
			"mode=vector|hybrid requires at least one free-text term; use mode=fts for filter-only queries")
	case errors.Is(err, vector.ErrNotEnabled):
		writeError(w, http.StatusServiceUnavailable, "vector_not_enabled",
			"vector search is not configured")
	case errors.Is(err, vector.ErrIndexStale):
		writeError(w, http.StatusServiceUnavailable, "index_stale",
			"the vector index does not match the configured model; run `msgvault build-embeddings --full-rebuild`")
	case errors.Is(err, vector.ErrIndexBuilding):
		writeError(w, http.StatusServiceUnavailable, "index_building",
			"the initial vector index is still being built")
	case errors.Is(err, vector.ErrEmbeddingTimeout):
		writeError(w, http.StatusServiceUnavailable, "embedding_timeout",
			"the embedding endpoint did not respond in time; retry, or raise [vector.embeddings].timeout")
	default:
		h.Logger.Error("v2 hybrid search failed", "query", q, "mode", mode, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "search failed")
	}
}
