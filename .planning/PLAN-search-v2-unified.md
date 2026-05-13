# Plan: Unified `/api/v2/search` + v2 package carve-out

**Fixes:** `.planning/BUG-search-response-shape-by-mode.md` (Option 3)
**Also:** moves v2 into its own package while there are no external consumers — drops the `V2` type-name suffix in favor of protobuf-style package versioning.

## Goals

1. `/api/v2/search?mode=fts|vector|hybrid` returns one stable response shape.
2. v2 lives in `internal/api/v2/`; type names lose the `V2` suffix.
3. v1 (`/api/v1/*`) response bytes are unchanged.

## Package layout

```
internal/api/
├── server.go            (v1) mounts v2 routes by calling v2.NewHandler(...).Routes()
├── handlers.go          (v1) — v1 stays as-is except for the runHybridSearch extraction
├── handlers_v2.go       DELETED — contents move to internal/api/v2/
└── v2/
    ├── handler.go       Handler struct holding deps (store, hybridEngine, vectorCfg, logger)
    ├── types.go         MessageSummary, PaginatedMessagesResponse, ThreadResponse,
                         EmailAddress, SearchResponse, SearchHit, ScoreBreakdown, Generation,
                         ErrorResponse, ThreadParticipant
    ├── messages.go      list / get / by-rfc822-id / by-label / by-participant
    ├── threads.go       GetThread
    ├── search.go        Search (fts | vector | hybrid)
    ├── attachments.go   GetAttachment
    ├── shared.go        summaries(), paginate(), messageSummaryFromAPIMessage(),
                         writeJSON, writeError, parseLimitOffset
    └── *_test.go        v2 tests with their own mock store
```

**Cross-package deps:**
- `internal/api/v2` imports `internal/store`, `internal/search`, `internal/search/hybridapi` (new — see below), `internal/vector`, `internal/vector/hybrid`, `internal/config`.
- `internal/api/v2` MUST NOT import `internal/api`. Avoids cyclic import (v1 mounts v2).
- `internal/api` imports `internal/api/v2` only to mount routes.

**Shared helpers:** `writeJSON`/`writeError`/`parseLimitOffset`/`ErrorResponse` duplicated into `v2/shared.go` (≈30 lines). Avoids creating a third package for trivial helpers; can be extracted later if drift appears. Error-code/message parity between v1 and v2 is guarded by tests, not by abstracting the helpers.

**`/api/v2/messages/{id}/body`:** stays mounted from `server.go`, calling the existing v1 `s.handleMessageBody` directly. The body endpoint returns raw bytes with the right Content-Type — no JSON shape to break — and there's no value in dragging it through the v2 package. This route is the explicit exception to "v2 routes go through v2.Handler".

## Response shape (single source of truth)

```jsonc
{
  "query":           "...",
  "mode":            "fts" | "vector" | "hybrid",
  "offset":          0,
  "limit":           20,
  "total":           42 | null,     // fts: global total; vector/hybrid: null (no global count over a top-k pool)
  "returned":        12,            // always present — len(messages) after hydration
  "messages":        [SearchHit, ...],

  // vector/hybrid only — absent in fts
  "generation":      { "id", "model", "dimension", "fingerprint", "state" },
  "pool_saturated":  false,
  "took_ms":         12
}
```

**Why `total: null` + `returned` instead of `total = len(messages)`** (codex pushback): overloading `total` misleads generic pagers — they'd read a low `total` as "set exhausted." Explicit null says "no global total exists for this mode"; `returned` is the unambiguous "what you got back" signal.

`total` is `*int64` (omitempty off — we want `null` to appear on the wire for vector/hybrid).

`SearchHit = MessageSummary + Score *ScoreBreakdown` (omitempty; only present with `explain=1`).

`ScoreBreakdown` is **redefined in v2** (same JSON as v1's today). Keeps v2's schema boundary clean — future v1 debug-shape changes won't leak.

## Pagination

- `mode=fts`: `limit`/`offset` honored. v2 `mode=fts` also uses `SearchMessagesQuery` when `search.Parse(q).HasOperators()` is true — matches v1 semantics so operator-aware queries don't behave differently across v1/v2 fts.
- `mode=vector|hybrid`: reject `offset > 0` with `pagination_unsupported`. Error message includes the explicit reason ("vector/hybrid ranks a top-k relevance pool; stable random access beyond offset 0 is not defined — raise `limit` to widen the pool"). `limit` clamped by `vectorCfg.Search.MaxPageSizeHybridClamp()` when set.

## Helper extraction — new package `internal/search/hybridapi/`

The hybrid search path is ~100+ lines (parse → BuildFilter → engine.Search → bulk-hydrate → score map → timing). Duplicating it across v1 and v2 is too much surface to keep in sync; method-on-`*Server` doesn't work because v2 can't import `internal/api`. New package `internal/search/hybridapi/` owns the HTTP-agnostic plumbing:

```go
// internal/search/hybridapi/run.go
package hybridapi

// Hydrator is the narrow store dependency: bulk summary lookup by ID.
type Hydrator interface {
    GetMessagesSummariesByIDs(ids []int64) ([]store.APIMessage, error)
}

type Deps struct {
    Engine   *hybrid.Engine
    Store    Hydrator
    Logger   *slog.Logger
    Now      func() time.Time   // injected for deterministic took_ms in tests
}

type Result struct {
    Msgs       []store.APIMessage
    ScoresByID map[int64]*ScoreBreakdown   // populated when Request.Explain
    Meta       hybrid.SearchMeta
    TookMS     int64
}

type Request struct {
    Query   string
    Mode    string   // "vector" | "hybrid"
    Explain bool
    Limit   int
}

type ScoreBreakdown struct {
    RRF, BM25, Vector *float64
    SubjectBoosted    bool
}

// Run returns sentinel errors unchanged (vector.ErrNotEnabled,
// vector.ErrIndexStale, vector.ErrIndexBuilding, vector.ErrEmbeddingTimeout,
// ErrMissingFreeText). Hydration failure is non-fatal — logs and returns
// an empty Msgs set with a nil error (matches v1's current behavior).
func Run(ctx context.Context, deps Deps, req Request) (Result, error)

var ErrMissingFreeText = errors.New("hybridapi: missing free-text term")
```

HTTP error mapping stays in each API package (`internal/api/handlers.go` for v1, `internal/api/v2/search.go` for v2) so the wire codes are owned by whoever owns the wire shape. v1 and v2 each have a small ~15-line `writeHybridSearchError` that maps `hybridapi` sentinels to their respective `writeError` calls. Error-code/message parity between v1 and v2 is regression-guarded by tests.

- v1 `handleHybridSearch` becomes ~30 lines: call `hybridapi.Run`, on error call local `writeHybridSearchError`, on success wrap in `hybridSearchResponse` and convert `hybridapi.ScoreBreakdown` → v1 `scoreBreakdown`.
- v2 `Search` handler: same call path; on success wrap in `v2.SearchResponse` and convert to v2 `ScoreBreakdown`.
- **v1 byte-identity check**: existing v1 tests (`TestHandleSearch_HybridUsesBulkHydration`, `TestHandleSearch_HybridPoolSaturatedAlwaysEmitted`, `TestHandleSearch_HybridErrIndexBuilding`, `TestHandleSearch_HybridErrNotEnabled`, etc.) all keep passing without modification.

## v2 handler construction

```go
// internal/api/v2/handler.go
type Handler struct {
    Store        Store                 // narrow interface, defined here
    HybridEngine *hybrid.Engine
    VectorCfg    *vector.Config
    Logger       *slog.Logger
}

func (h *Handler) Routes() chi.Router { ... }

// internal/api/server.go (snippet)
r.Route("/api/v2", func(r chi.Router) {
    r.Use(v2APIVersionHeader)
    r.Use(s.publicReadOrAuth)
    v2h := &v2.Handler{Store: s.store, HybridEngine: s.hybridEngine, VectorCfg: s.vectorCfg, Logger: s.logger}
    r.Mount("/", v2h.Routes())
})
```

The `v2.Store` interface enumerates only the store methods v2 uses (BatchStructuredRecipients, BatchMessageMetaV2, GetMessagesSummariesByIDs, ListMessages, ListMessagesByLabel, ListMessagesByParticipant, GetThread, GetMessage, GetAttachment, SearchMessages, SearchMessagesQuery). This narrows the surface and makes v2 unit tests cheaper.

## Tests

In `internal/api/v2/`:
- **Existing v2 tests in `handlers_test.go`** (TestHandleListMessagesV2, TestHandleSearchV2, TestHandleGetThreadV2, TestV2RoutesStampV2VersionHeader, and every test decoding `MessageDetailV2`/`PaginatedMessagesResponseV2`/etc.) **move into the v2 package in commit 1, in the same change as the package carve-out**. They become `package v2_test` (or `package v2`), use a v2-local mock store, and reference unsuffixed type names. No temporary type aliases or shims — the suffix removal is atomic with the move.
- A small mock store specific to v2 lives in `v2/mockstore_test.go` (subset of the methods on `internal/api`'s `mockStore`).
- `Test_Mount_RoutesReachable` — sanity-check the `chi.Mount("/", v2h.Routes())` wiring: hit a no-param route (`/api/v2/messages`), a path-param route (`/api/v2/threads/{id}`), and confirm the existing `X-MsgVault-API: v2` header still stamps every response. Catches slash/redirect/double-match oddities from the mount seam.
- `Test_Body_StillReachable` — `/api/v2/messages/{id}/body` returns the right Content-Type and bytes (exercises the explicit-exception route still mounted from `server.go`).

New search-coverage tests added in commit 3:
- `Test_Search_FTSShape` — `mode=fts`, asserts exact top-level key set: `{query, mode, offset, limit, total, returned, messages}` and NO `generation`/`pool_saturated`/`took_ms`. `total` is a JSON number.
- `Test_Search_FTSWithOperators` — operator query routes through `SearchMessagesQuery`, mirrors v1 semantics.
- `Test_Search_HybridNotConfigured` — no engine wired → 503 `vector_not_enabled`.
- `Test_Search_HybridShape` — fake backend returning hits; asserts exact top-level key set: `{query, mode, offset, limit, total, returned, messages, generation, pool_saturated, took_ms}`. `total` is JSON `null`. Hit `score` present when `explain=1`.
- `Test_Search_PaginationRejected` — `offset=10&mode=hybrid` → 400 `pagination_unsupported`, error message explains why.
- `Test_Search_MissingFreeText` — filter-only query in hybrid mode → 400 `missing_free_text`.
- `Test_Search_IndexBuilding` — fake backend with Building gen only → 503 `index_building`.

Tests for v1 stay in `internal/api/handlers_test.go` untouched.

In `internal/search/hybridapi/`:
- Smoke test for `Run` with a fake `*hybrid.Engine` + fake `Hydrator`: success path, hydration-failure-returns-200-empty path (regression guard), sentinel error pass-through for each error class.

## Out of scope

- v1 wire shape unchanged.
- Dashboard client (`lib/msgvault/client.ts`, `SearchSchema`) — tracked in dashboard repo per bug report's coordination note.
- Extracting shared helpers (`writeJSON`/`writeError`/`parseLimitOffset`) into a third package — deferred until drift appears.

## Acceptance

1. `go test ./internal/api/... ./internal/api/v2/... ./internal/search/hybridapi/...` passes.
2. `go vet ./...` and `golangci-lint` clean.
3. **Exact top-level key set** for `/api/v2/search`:
   - `mode=fts` → `{query, mode, offset, limit, total, returned, messages}`. `total` is a number.
   - `mode=vector|hybrid` → `{query, mode, offset, limit, total, returned, messages, generation, pool_saturated, took_ms}`. `total` is `null`.
   - Asserted by `Test_Search_FTSShape` and `Test_Search_HybridShape`.
4. v1 responses byte-identical to today's (regression-guarded by existing v1 tests; no test in `handlers_test.go` modified except those moving with the v2 carve-out in commit 1).
5. v2 type names contain no `V2` suffix; v2 consumers refer to types as `v2.SearchResponse`, `v2.MessageSummary`, etc.

## Execution order (commits)

1. **Carve out v2 package** — move v2 handlers, types, and the entire v2 test surface (every test in `handlers_test.go` that decodes a `*V2` type or hits `/api/v2/*`) into `internal/api/v2/`. Drop `V2` suffixes from type names atomically with the move. Add the route-mount sanity tests (`Test_Mount_RoutesReachable`, `Test_Body_StillReachable`). No behavior change on the wire. Diff is large but mechanical; commit 1 must pass `go test ./...` on its own — no shims, no aliases.
2. **Add `internal/search/hybridapi/`** with `Run`, `Hydrator`, `Request`/`Result`/`ScoreBreakdown`, sentinels. Refactor v1 `handleHybridSearch` to call `hybridapi.Run` + local `writeHybridSearchError`. No wire change. Existing v1 hybrid tests verify byte identity. Add `hybridapi` smoke tests.
3. **Add v2 search** (fts + vector + hybrid) with the unified shape. Add `Test_Search_*` coverage.

Each commit is independently revertable.
