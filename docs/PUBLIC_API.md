# msgvault public API

The `msgvault serve` daemon exposes an HTTP API at `127.0.0.1:8080` by
default. This document describes the endpoints that downstream
review apps (e.g. radical-roc) depend on, the security posture knobs
that govern how they're exposed, and the items declined in the current
milestone.

JSON endpoints are mounted under `/api/v1` and `/api/v2`; HTML
companion views are at the root (`/m/{id}`, `/t/{id}`, etc.).

## Auth and posture

### `[server].api_key`

When set, every `/api/v1/*` and `/api/v2/*` request must carry the key
in either `Authorization: Bearer …` or `X-API-Key`. When unset, the
server logs a warning and runs unauthenticated.

### `[server].public_read` (default `false`)

When `true`:

- GET/HEAD on the read endpoints listed below skip the API-key check
  even when one is configured. Writes (POST `/sync/*`, `/accounts`,
  `/auth/token/*`, `/query`) still require the key.
- If `[server].cors_origins` is unset, it defaults to `["*"]` so
  `file://` artifacts (`Origin: null`) can fetch citations. Override by
  setting `cors_origins` explicitly.

This is the posture review apps shipped as standalone HTML files need:
they can't carry a key without leaking it.

### Loopback rate-limit bypass

Per-IP rate limiting (10 req/s, burst 20) is enforced for non-loopback
clients only. Loopback (`127.0.0.0/8`, `::1`) requests are not throttled
— a local review app may fan out dozens of citation lookups in parallel.

### CORS

`OPTIONS` preflights respond `204 No Content` with the standard
`Access-Control-Allow-*` headers when an `Origin` matches the configured
allow-list (or `*`).

### API version header

Every response carries `X-MsgVault-API: v1` or `X-MsgVault-API: v2`
depending on which mount served it. Treat it as the contract version.
Both mounts coexist; `v1` artifacts in the wild stay readable forever.

### v1 vs v2 — which to use

| | v1 | v2 |
|---|---|---|
| Mount | `/api/v1/` | `/api/v2/` |
| `from` | `"Name <email>"` string | `{name, address}` object |
| `to`, `cc`, `bcc` | `["email", ...]` string array | `[{name, address}, ...]` |
| Thread reference | `conversation_id` | `thread_id` |
| `rfc822_message_id` field | absent (use `/by-rfc822-id` to query) | present, bracketed |
| `in_reply_to`, `references` | absent | present (from MIME parse) |
| `reply_to` | absent | present |
| `source_message_id`, `account` | absent | present |
| `message_type` | absent | present |
| `attachment_count` | absent (`has_attachments` bool only) | present |
| `is_deleted` flag | derived from `deleted_at` | explicit bool |
| Attachment IDs inline | absent | present (`attachments[].id`) |
| Body shape | `body` (str) + `body_html` (str) siblings | nested `body: {text, html}` |

Use **v2** for new work — it carries the full standard-metadata set
review apps actually need. v1 stays available indefinitely for
existing consumers.

v2 currently covers message list/detail/by-rfc822/body, thread,
label-filtered list, participant detail + filtered list, attachment
detail + content, attachment lookup by content hash, and unified
FTS/vector/hybrid search. The remaining v1 endpoints (`/stats`,
`/labels`, `/corpus/fingerprint`, and the HTML companion views) don't
suffer from the v1 shape problems and stay v1-only. They'll grow v2
variants only if a consumer hits an actual need.

### v2 list / search response shape

List responses and FTS search responses include a numeric `total`.
Vector and hybrid search return the same envelope but set `total` to
`null` because there is no global count over the ranked top-k pool;
`returned` is the number of hydrated hits in the current response.

```jsonc
{
  "query": "invoice",              // search only
  "mode": "fts",                   // search only: fts | vector | hybrid
  "total": 10337,
  "offset": 0,
  "limit": 50,
  "returned": 50,                  // search only
  "messages": [
    {
      "id": 11134,
      "rfc822_message_id": "<...>",
      "source_message_id": "...",
      "thread_id": 2754,
      "account": "user@gmail.com",
      "message_type": "email",
      "subject": "...",
      "snippet": "...",
      "from": { "name": "...", "address": "..." },
      "to":   [ {...}, ... ],
      "cc":   [ {...}, ... ],
      "sent_at": "RFC3339",
      "received_at": "RFC3339",
      "labels": [...],
      "has_attachments": true,
      "attachment_count": 1,
      "size_bytes": 12345,
      "is_deleted": false
    },
    ...
  ],

  // vector/hybrid search only
  "generation": { "id": 1, "model": "...", "dimension": 768, "fingerprint": "...", "state": "active" },
  "pool_saturated": false,
  "took_ms": 12
}
```

`bcc`, the body, the attachment list, and the MIME-derived headers
(`in_reply_to`, `references`, `reply_to`) are detail-only — they
require either an extra per-row table lookup or a raw-MIME parse, both
of which are too expensive to pay per page. Hit
`/api/v2/messages/{id}` for the full set.

## Read endpoints

| Method | Path | Purpose |
|---|---|---|
| GET | `/health` | Presence check; always public. |
| GET | `/api/v1/stats` | Corpus stats (totals + DB size). |
| GET | `/api/v1/messages` | Paginated message list. |
| GET | `/api/v1/messages/{id}` | Single message detail (v1 shape). |
| GET | `/api/v1/messages/{id}/body?format=html\|text` | Raw body bytes with the right `Content-Type`. |
| GET | `/api/v1/messages/{id}/inline` | CID-referenced inline MIME part. |
| GET | `/api/v1/messages/by-rfc822-id/{rfc822_id}` | Lookup by `Message-ID:` header (v1 shape). |
| GET | `/api/v1/threads/{id}` | Thread JSON with participants + message summaries. |
| GET | `/api/v1/attachments/{id}` | Attachment metadata. |
| GET | `/api/v1/attachments/{id}/content` | Raw attachment bytes (range/conditional). |
| GET | `/api/v1/labels` | `{name, count}` pairs. |
| GET | `/api/v1/labels/{name}/messages` | Messages tagged with a label. |
| GET | `/api/v1/participants/{id}` | Participant detail + aggregates. |
| GET | `/api/v1/participants/{id}/messages` | Messages involving a participant. |
| GET | `/api/v1/search?q=…` | FTS5/vector/hybrid search. |
| GET | `/api/v1/corpus/fingerprint` | Drift digest for the whole corpus. |
| GET | `/api/v2/messages` | Paginated message summaries (v2 shape, limit/offset). |
| GET | `/api/v2/messages/{id}` | Message detail (v2 shape — structured headers). |
| GET | `/api/v2/messages/{id}/body?format=html\|text` | Same body bytes as v1 — kept under v2 for path consistency. |
| GET | `/api/v2/messages/by-rfc822-id/{rfc822_id}` | Lookup by Message-ID (v2 shape). |
| GET | `/api/v2/threads/{id}` | Thread with v2 message-summary entries. |
| GET | `/api/v2/labels/{name}/messages` | Label-filtered list (v2 shape). |
| GET | `/api/v2/participants/{id}` | Participant detail with `is_user_account` flag. |
| GET | `/api/v2/participants/{id}/messages` | Participant-filtered list (v2 shape). |
| GET | `/api/v2/attachments/{id}` | Attachment metadata + `thread_id`, `account`, `inline_disposition`. |
| GET | `/api/v2/attachments/{id}/content` | Same bytes as v1, mounted under v2 for path consistency. |
| GET | `/api/v2/attachments/by-hash/{sha256}` | Attachment metadata by stable SHA-256 content hash. |
| GET | `/api/v2/attachments/by-hash/{sha256}/content` | Attachment bytes by stable SHA-256 content hash. |
| GET | `/api/v2/search?q=…&mode=fts\|vector\|hybrid` | Unified search returning v2 summaries. |

### HTML companions

Each of these renders a chrome-less HTML page suitable for `<iframe>`
embed inside a review pane. No JS, no external CSS, no inbox sidebar.

| Path | Purpose |
|---|---|
| `/m/{id}` | Single message: headers, body, attachment list, thread back-link. |
| `/t/{id}` | Thread: message-stack with per-message drill-down. |
| `/p/{id}` | Participant: identity, aggregates, message list. |
| `/l/{name}` | Label: tagged-message list. |
| `/attachment/{id}` | PDF/image preview or download CTA. |

404 pages render an explicit "not found in this corpus" message rather
than the inbox shell, so review apps can distinguish "corpus mismatch"
from "ambiguous response."

## Endpoint details

### `GET /api/v1/messages/{id}/body?format=html|text`

Defaults to `html` when an HTML part exists, `text` otherwise. Plain
text rendered into an `html` request is wrapped in `<pre>` with minimal
HTML escaping; raw HTML served as `text` is returned verbatim with
`Content-Type: text/plain; charset=utf-8`.

### `GET /api/v1/messages/by-rfc822-id/{rfc822_id}` and `GET /api/v2/messages/by-rfc822-id/{rfc822_id}`

`{rfc822_id}` must be percent-encoded by the caller — chi.URLParam
returns the raw encoded segment, and the handler `url.PathUnescape`s
it before the DB lookup. Real Outlook-style Message-IDs contain `$`
separators so this matters in practice.

Returns the same `MessageDetail` shape as the corresponding
`/messages/{id}` mount. When the same `Message-ID:` appears in
multiple synced accounts (e.g. the user received the same message at
two addresses) the lowest internal id wins. No dedicated index —
intended for low-volume citation traffic, not ingest.

### v2 message-detail JSON shape

```jsonc
{
  "id": 11134,
  "rfc822_message_id": "<...>",       // always bracketed, even if
                                       // the underlying MIME parser stripped them
  "source_message_id": "...",          // Gmail message id for round-trip
  "thread_id": 2754,
  "account": "user@gmail.com",         // which synced account this came from
  "message_type": "email",
  "subject": "...",
  "snippet": "...",
  "from":    { "name": "...", "address": "..." },
  "to":      [ { "name": "...", "address": "..." }, ... ],
  "cc":      [ ... ],
  "bcc":     [ ... ],
  "reply_to":[ ... ],                   // omitted when absent
  "in_reply_to": "<...>",              // omitted when absent
  "references": [ "<...>", ... ],      // omitted when empty
  "sent_at": "RFC3339",
  "received_at": "RFC3339",            // omitted when absent
  "labels": [ ... ],
  "has_attachments": true,
  "attachment_count": 1,
  "size_bytes": 12345,
  "is_deleted": false,
  "deleted_at": "RFC3339",             // present only when is_deleted = true
  "body": { "text": "...", "html": "..." },
  "attachments": [
    { "id": 7, "filename": "...", "mime": "...", "size_bytes": 4096, "content_hash": "..." }
  ]
}
```

`to`, `cc`, `bcc`, `labels`, and `attachments` are always emitted as
arrays (possibly empty) so consumers can iterate without nil checks.
`reply_to`, `in_reply_to`, `references`, `deleted_at`, `received_at`,
and the `from` object are omitted when empty.

`in_reply_to` / `references` / `reply_to` are extracted by re-parsing
the stored raw MIME blob on each request. The cost is one extra
zlib-decompress + MIME parse per detail call; fine for citation-lookup
traffic, would warrant denormalizing if hot. Messages imported before
raw MIME was persisted leave these fields empty rather than failing.

### `GET /api/v1/attachments/{id}/content`, `GET /api/v2/attachments/{id}/content`, and `GET /api/v2/attachments/by-hash/{sha256}/content`

`Content-Disposition` is governed by an inline-safe MIME safelist:

- `application/pdf`, `image/png`, `image/jpeg`, `image/gif`,
  `image/webp`, `text/plain` → `inline`
- Everything else → `attachment` (forces download)

HTML, SVG, and anything script-capable is always downloaded, keeping
the XSS surface flat. Range requests and conditional GETs are
delegated to `http.ServeContent`.

If the DB row exists but the on-disk blob is missing the response is
`410 Gone`; unknown ids are `404 Not Found`.

The by-hash content endpoint resolves `{sha256}` to the lowest-id
attachment row carrying that 64-character hex digest, then streams the
same bytes with the same headers as the by-id endpoint. Invalid hashes
return `400 invalid_hash`; hashes with no match return `404 not_found`.

### `GET /api/v2/attachments/by-hash/{sha256}`

Returns the same `AttachmentDetail` shape as `/api/v2/attachments/{id}`,
with an extra `occurrences` field when the same content hash appears on
multiple attachment rows. Use this endpoint for stable cross-archive
references: sqlite attachment ids are per-database rowids, while the
content hash follows the bytes across re-imports.

### `GET /api/v2/search?q=...&mode=fts|vector|hybrid`

Returns one stable `SearchResponse` envelope across all modes:
`query`, `mode`, `offset`, `limit`, `total`, `returned`, and
`messages` are always present. `generation`, `pool_saturated`, and
`took_ms` are present only for `mode=vector` and `mode=hybrid`.

`mode=fts` honors `limit` and `offset`; `total` is the global match
count. `mode=vector` and `mode=hybrid` rank a top-k relevance pool and
reject `offset > 0` with `400 pagination_unsupported`; raise `limit`
to widen the pool. For vector/hybrid responses, `total` is `null` and
`returned` is the hydrated hit count. `explain=1` adds a per-hit
`score` object when the ranking backend has signal details to expose.

### `GET /api/v1/corpus/fingerprint`

Recipe (committed to as part of the v1 contract):

```
sha256("<message_count>|<latest_sent_at_unix>|<max(id)>")
```

Stable as long as no compaction renumbers ids; changes on every sync.
Use it for "did the corpus change at all since this artifact was
prepared." It is **not** a per-citation stability hash — a renumbering
compaction would also invalidate the fingerprint. Per-message content
hashing is tracked separately (see "Declined" below).

### Pagination

- `/api/v1/messages` uses `page` + `page_size` (legacy).
- `/api/v1/labels/{name}/messages` and
  `/api/v1/participants/{id}/messages` use `limit` + `offset` directly,
  clamped to `[1, 500]` (default `50`).

Pick whichever endpoint you prefer; the responses always include
`total` so clients can paginate without hitting a deduplicated count.

## Declined in this milestone

| Wishlist | Status | Reason |
|---|---|---|
| C1 — per-message content hash | declined | Defer until a concrete drift problem appears. |
| C2 — quote-range fragments | declined | Client-side overlay (radical-roc) covers it. |
| C3 — write-back labels API | declined | Needs auth design first; revisit when shared. |
| C4 — redacted-snippet endpoint | declined | Policy belongs in the artifact builder, not msgvault. |
| C5 — `/api/v1/messages/by-rfc822-id` | **shipped** (Phase 2). | |
| C6 — SSE/WebSocket new-message stream | declined | Frozen-snapshot review doesn't need it. |

## Operational notes

- The default bind is `127.0.0.1`. Set `[server].bind_addr = "0.0.0.0"`
  to expose on a network — and set `[server].api_key` first.
- The CORS middleware echoes the request's `Origin` value when it
  matches the allow-list, including the literal string `null` for
  `file://` artifacts. This is the only way browsers will hand the
  response back to JS in a Slack-distributed HTML file.
- HTML views and the message body endpoint render stored content
  verbatim (`template.HTML`). msgvault sanitizes during MIME parsing,
  so this is safe under the existing trust model. If you ever import
  attacker-controlled content from a new source, audit that pipeline
  first.
