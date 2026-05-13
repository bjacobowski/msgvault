# msgvault public API

The `msgvault serve` daemon exposes an HTTP API at `127.0.0.1:8080` by
default. This document describes the endpoints that downstream
review apps (e.g. radical-roc) depend on, the security posture knobs
that govern how they're exposed, and the items declined in the current
milestone.

All endpoints are mounted under `/api/v1`; HTML companion views are at
the root (`/m/{id}`, `/t/{id}`, etc.).

## Auth and posture

### `[server].api_key`

When set, every `/api/v1/*` request must carry the key in either
`Authorization: Bearer …` or `X-API-Key`. When unset, the server logs a
warning and runs unauthenticated.

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

Every response carries `X-MsgVault-API: v1`. Treat it as the contract
version. Breaking changes will increment to `v2` and live under a new
`/api/v2/` mount; `v1` artifacts in the wild stay readable.

## Read endpoints

| Method | Path | Purpose |
|---|---|---|
| GET | `/health` | Presence check; always public. |
| GET | `/api/v1/stats` | Corpus stats (totals + DB size). |
| GET | `/api/v1/messages` | Paginated message list. |
| GET | `/api/v1/messages/{id}` | Single message detail (JSON). |
| GET | `/api/v1/messages/{id}/body?format=html\|text` | Raw body bytes with the right `Content-Type`. |
| GET | `/api/v1/messages/{id}/inline` | CID-referenced inline MIME part. |
| GET | `/api/v1/messages/by-rfc822-id/{rfc822_id}` | Lookup by `Message-ID:` header. |
| GET | `/api/v1/threads/{id}` | Thread JSON with participants + message summaries. |
| GET | `/api/v1/attachments/{id}` | Attachment metadata. |
| GET | `/api/v1/attachments/{id}/content` | Raw attachment bytes (range/conditional). |
| GET | `/api/v1/labels` | `{name, count}` pairs. |
| GET | `/api/v1/labels/{name}/messages` | Messages tagged with a label. |
| GET | `/api/v1/participants/{id}` | Participant detail + aggregates. |
| GET | `/api/v1/participants/{id}/messages` | Messages involving a participant. |
| GET | `/api/v1/search?q=…` | FTS5/vector/hybrid search. |
| GET | `/api/v1/corpus/fingerprint` | Drift digest for the whole corpus. |

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

### `GET /api/v1/messages/by-rfc822-id/{rfc822_id}`

`{rfc822_id}` should be URL-encoded — chi decodes the path segment.
Returns the same `MessageDetail` shape as `/messages/{id}`. When the
same `Message-ID:` appears in multiple synced accounts (e.g. the user
received the same message at two addresses) the lowest internal id
wins. No dedicated index — intended for low-volume citation traffic,
not ingest.

### `GET /api/v1/attachments/{id}/content`

`Content-Disposition` is governed by an inline-safe MIME safelist:

- `application/pdf`, `image/png`, `image/jpeg`, `image/gif`,
  `image/webp`, `text/plain` → `inline`
- Everything else → `attachment` (forces download)

HTML, SVG, and anything script-capable is always downloaded, keeping
the XSS surface flat. Range requests and conditional GETs are
delegated to `http.ServeContent`.

If the DB row exists but the on-disk blob is missing the response is
`410 Gone`; unknown ids are `404 Not Found`.

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
