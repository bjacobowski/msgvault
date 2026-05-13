# Bug: `/api/v1/search` response shape varies by `mode`, breaking typed clients

**Severity:** Medium — confirmed broken in msgvault-dashboard (default search mode is `hybrid`, dashboard throws on parse)
**Surfaced by:** msgvault-dashboard M6 smoke, 2026-05-13
**Filed:** 2026-05-13

## Repro

```bash
curl -s "http://localhost:8090/api/v1/search?q=invoice&mode=fts&page_size=1" | jq 'keys'
# → ["messages","page","page_size","query","total"]

curl -s "http://localhost:8090/api/v1/search?q=invoice&mode=hybrid&page_size=1" | jq 'keys'
# → ["generation","mode","pool_saturated","query","results","returned","took_ms"]
```

## Expected

One endpoint with one mode parameter should return one stable response shape. A typed client should write `SearchResponse = z.object({ ... })` once and parse every successful response across modes.

## Actual

The shape diverges by `mode`:

| Field | `mode=fts` | `mode=vector` / `mode=hybrid` |
|---|---|---|
| Hits array | `messages: [...]` | `results: [...]` |
| Pagination | `total`, `page`, `page_size` | `returned` (count only — no total, no offset) |
| Metadata | — | `generation`, `pool_saturated`, `took_ms` |

Source: `internal/api/handlers.go` — FTS path emits the `messagesPage` struct; `handleHybridSearch` emits `hybridSearchResponse` (struct at handlers.go:~120; writeJSON inside `handleHybridSearch`).

The pagination contract also diverges: `mode=hybrid` rejects `page > 1` with `pagination_unsupported`, so clients can't write one paging loop that covers both modes.

## Impact

- **msgvault-dashboard** has `SearchSchema` modeled on the FTS shape (`messages`, `total`). The dashboard's default mode is **Hybrid**, so the default search has been throwing a Zod parse error since hybrid started returning 200 (the old `500 internal_error` masked this for a while). Repro: load `/`, type any query, press Enter → red error overlay referencing `data.messages.length`.
- Any other typed client will hit the same wall when it switches modes.

## Suggested fixes — pick one

### Option 1: Unify under one shape (preferred)

Make hybrid/vector emit `messages: [...]` with the same per-hit shape as FTS, and put the extra metadata (`generation`, `pool_saturated`, `took_ms`) alongside. Populate `total` with the returned count when no global count is computable. Clients get one Zod schema, one rendering path.

Smallest behavioral surprise: existing FTS clients are unchanged; vector/hybrid clients learn one new shape but it's an additive change (extra fields appear; no field is renamed for the FTS callers).

### Option 2: Document the split + add a discriminator

Add `kind: "fts" | "vector_pool"` to every response so clients can switch cleanly on a single discriminator. Less work backend-side, more work on every client.

### Option 3: Move hybrid/vector to v2

`/api/v2/search` already exists but is FTS-only (`handlers_v2.go:handleSearchV2` line ~362 has a comment: *"FTS path only — vector/hybrid require the search.Query plumbing... vector/hybrid stay on /api/v1/search until a v2 caller asks for them."*). This is the moment a v2 caller asks. Add the missing plumbing and have v2 search emit a single shape across all modes. Leaves v1 untouched as a compatibility surface.

## Recommendation

Option 3 if v2 is the strategic API for new clients — keeps v1 as the legacy surface and lets v2 ship with a clean response contract day one. Option 1 if there's no v2 commitment for vector/hybrid in the near term.

## Coordination note for msgvault-dashboard

Once this is fixed:
- `lib/msgvault/schemas.ts` needs a `SearchSchema` patch to accept the new shape.
- `components/search/search-results.tsx` needs a small render update (currently hardcoded to `data.messages`).
- If we go with Option 3, the dashboard's `lib/msgvault/client.ts` will need a v2 caller for hybrid/vector instead of patching v1.

The dashboard's settings page already handles `vector_search.active_generation` correctly across the upcoming chunking changes (#323) because `EmbeddingCount` is preserved as `COUNT(DISTINCT message_id)`. No coordination needed there.
