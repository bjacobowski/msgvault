// Package hybridapi adapts the hybrid search engine for HTTP-facing
// callers. It owns the parse → BuildFilter → engine.Search →
// bulk-hydrate → score-map plumbing that previously lived inline in
// the /api/v1/search handler; lifting it out lets both /api/v1/search
// and /api/v2/search share one path without v2 importing v1.
//
// Run is intentionally HTTP-agnostic: it returns raw sentinel errors
// from the vector package (ErrNotEnabled, ErrIndexStale,
// ErrIndexBuilding, ErrEmbeddingTimeout) and the package-local
// ErrMissingFreeText. Callers map those to wire codes themselves —
// each API surface owns its own response envelope.
package hybridapi

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strings"

	"github.com/wesm/msgvault/internal/search"
	"github.com/wesm/msgvault/internal/store"
	"github.com/wesm/msgvault/internal/vector"
	"github.com/wesm/msgvault/internal/vector/hybrid"
)

// ErrMissingFreeText reports that a vector/hybrid request carried no
// free-text term, which the engine can't embed. Callers should return
// 400 with an error code explaining the caller can use FTS for
// filter-only queries.
var ErrMissingFreeText = errors.New("hybridapi: free-text term required for vector/hybrid")

// Hydrator is the narrow store surface Run needs — a single bulk
// summary lookup by id. Both *store.Store and the api package's
// MessageStore satisfy this.
type Hydrator interface {
	GetMessagesSummariesByIDs(ids []int64) ([]store.APIMessage, error)
}

// Request describes one hybrid/vector search call.
type Request struct {
	Query   string      // raw user query string (search.Parse'd internally)
	Mode    hybrid.Mode // "vector" | "hybrid"
	Limit   int         // top-k pool size
	Explain bool        // populate ScoresByID when true
}

// Result is the HTTP-agnostic search result. Callers wrap it in
// whichever response envelope their API surface uses.
type Result struct {
	// Msgs is the hydrated message rows in engine-ranked order. May be
	// empty if hydration failed (a logged-and-swallowed event matching
	// the v1 handler's behavior) or if every hit referred to a row
	// that vanished between Search and hydration.
	Msgs []store.APIMessage

	// ScoresByID is populated only when Request.Explain is true. The
	// map is keyed by MessageID and contains a non-nil entry for every
	// row in Msgs.
	ScoresByID map[int64]*ScoreBreakdown

	// Meta is the engine's per-call metadata (generation, pool
	// saturation).
	Meta hybrid.ResultMeta
}

// ScoreBreakdown exposes fused-score components for debugging. BM25,
// Vector, and RRF are pointer-typed so "not present in this signal"
// is distinguishable from a legitimate 0.0 score. In particular,
// mode=vector reports vector with no rrf (RRF requires two signals
// to fuse), and mode=fts reports bm25 with no rrf or vector — though
// fts callers don't use this path.
type ScoreBreakdown struct {
	RRF            *float64
	BM25           *float64
	Vector         *float64
	SubjectBoosted bool
}

// Run executes parse → BuildFilter → engine.Search → bulk-hydrate →
// optional score-map for a vector or hybrid search request.
//
// Error contract:
//   - ErrMissingFreeText: caller forgot or filtered all text terms; 400.
//   - vector.ErrNotEnabled, ErrIndexStale, ErrIndexBuilding,
//     ErrEmbeddingTimeout: 503-style infrastructure errors; map to the
//     appropriate error code on each API surface.
//   - filter-build or engine errors not in the list above: bubble up
//     unchanged; callers translate to 500.
//
// Hydration failure is *not* surfaced as an error — Run logs and
// returns an empty Msgs set with a nil error. This preserves the
// existing /api/v1/search posture (a transient hydration glitch
// shouldn't escalate a 200 search to a 500).
func Run(ctx context.Context, engine *hybrid.Engine, hydrator Hydrator, logger *slog.Logger, req Request) (Result, error) {
	parsed := search.Parse(req.Query)
	freeText := strings.Join(parsed.TextTerms, " ")
	if freeText == "" {
		return Result{}, ErrMissingFreeText
	}

	subjectTerms := make([]string, 0, len(parsed.TextTerms))
	for _, t := range parsed.TextTerms {
		subjectTerms = append(subjectTerms, strings.ToLower(t))
	}

	filter, err := engine.BuildFilter(ctx, parsed)
	if err != nil {
		return Result{}, err
	}

	hits, meta, err := engine.Search(ctx, hybrid.SearchRequest{
		Mode:         req.Mode,
		FreeText:     freeText,
		Filter:       filter,
		Limit:        req.Limit,
		SubjectTerms: subjectTerms,
		Explain:      req.Explain,
	})
	if err != nil {
		return Result{}, err
	}

	hitIDs := make([]int64, len(hits))
	for i, h := range hits {
		hitIDs[i] = h.MessageID
	}
	summaries, herr := hydrator.GetMessagesSummariesByIDs(hitIDs)
	if herr != nil {
		// Match v1 posture: a transient hydration glitch shouldn't
		// turn a successful search into a 500. Log and continue with
		// an empty hydration map; the loop below will skip every hit.
		if logger != nil {
			logger.Warn("hybridapi hydrate failed", "ids", len(hitIDs), "error", herr)
		}
		summaries = nil
	}
	byID := make(map[int64]store.APIMessage, len(summaries))
	for _, m := range summaries {
		byID[m.ID] = m
	}

	msgs := make([]store.APIMessage, 0, len(hits))
	var scores map[int64]*ScoreBreakdown
	if req.Explain {
		scores = make(map[int64]*ScoreBreakdown, len(hits))
	}
	for _, h := range hits {
		msg, ok := byID[h.MessageID]
		if !ok {
			// Hit referred to a row that disappeared between Search
			// and hydration (just-deleted, retired generation, etc.).
			// Drop it silently — same effect as the old per-hit
			// GetMessage returning nil.
			continue
		}
		msgs = append(msgs, msg)
		if req.Explain {
			scores[h.MessageID] = scoreFromHit(h)
		}
	}

	return Result{
		Msgs:       msgs,
		ScoresByID: scores,
		Meta:       meta,
	}, nil
}

// scoreFromHit packs a FusedHit's NaN-tolerant signal components into
// a ScoreBreakdown. NaN values mean "this signal wasn't present" and
// are omitted (left nil) rather than encoded as 0.
func scoreFromHit(h vector.FusedHit) *ScoreBreakdown {
	sb := &ScoreBreakdown{SubjectBoosted: h.SubjectBoosted}
	if !math.IsNaN(h.RRFScore) {
		v := h.RRFScore
		sb.RRF = &v
	}
	if !math.IsNaN(h.BM25Score) {
		v := h.BM25Score
		sb.BM25 = &v
	}
	if !math.IsNaN(h.VectorScore) {
		v := h.VectorScore
		sb.Vector = &v
	}
	return sb
}
