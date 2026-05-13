# PLAN: radical-roc wishlist

Source: `/home/bjaco/projects/radical-roc/.planning/msgvault-wishlist.md` (consumer
project's MUST/SHOULD/COULD tiering).

Branch: `omgnos`. Target: a small set of additions to the existing `serve`-mode
HTTP API (`internal/api/`) that satisfy radical-roc's review-app architecture.

---

## Current state (verified against `internal/api/server.go`)

Already shipped — wishlist items that are **no-ops**:

| Wishlist | Status | Notes |
|---|---|---|
| API versioning | done | All routes mounted under `/api/v1/` |
| `GET /health` (presence ping) | done | no auth, returns `{"status":"ok"}` |
| M2: typed message lookup w/ 404 JSON | done | `GET /api/v1/messages/{id}` returns `MessageDetail`, 404 on miss |
| S3: search | done | `GET /api/v1/search?q=...&mode=fts\|vector\|hybrid` |
| M1: CORS middleware | partial | code exists in `internal/api/middleware.go`; **disabled by default** unless `[server].cors_origins` is configured. Echoes `Origin` when wildcard or exact-match; handles `OPTIONS` preflight with 204. `null` origin works via `*`. |
| Auth posture | gotcha | `/api/v1/*` requires API key when `[server].api_key` is set. radical-roc artifacts can't carry a key without leaking it. Need a public-read posture. |
| Rate limit | gotcha | 10 req/s burst 20 per-IP. A review page rendering 50 citation badges in parallel will hit this. |

What's **actually missing**:

- M3 `/m/<id>` HTML view (no chrome)
- S1 dedicated body endpoint (body comes back inside `MessageDetail` today; no
  `Content-Type: text/html` body-only response)
- S2 threads — both `GET /api/v1/threads/{id}` and `/t/<id>` HTML
- S4 attachments — JSON metadata, content stream, `/attachment/<id>` HTML
- S5 corpus fingerprint
- S6 labels (JSON + `/l/<name>`)
- S7 participants (JSON + `/p/<id>`)
- C5 RFC822 Message-ID lookup (cheap; bundle with phase 1)

Wishlist items being **declined**:

- C1 per-message stability hash — defer until a concrete drift problem appears.
- C2 quote-range fragments — radical-roc can overlay client-side.
- C3 write API for labels — needs auth design first.
- C4 redacted snippet — wrong layer. Redaction policy lives in radical-roc.
- C6 SSE/WebSocket — irrelevant to frozen-snapshot review.

---

## Decisions (resolving the "concerns" raised earlier)

1. **Versioning** — keep `/api/v1/`. Add a `X-MsgVault-API: v1` response header
   on every JSON response. Bump to `/api/v2/` only on a breaking change to a
   shape that's already in radical-roc artifacts in the wild.

2. **Auth + CORS posture** — introduce a single config key
   `[server].public_read = true` that:
   - Enables CORS with `AllowedOrigins = ["null", "http://localhost:*",
     "http://127.0.0.1:*", "file://"]` (override via `cors_origins` if set).
   - Skips API-key auth on `GET`/`HEAD` of the **read** endpoints listed below,
     under the existing `/api/v1/` mount. Write endpoints (`/sync/*`,
     `/accounts`, `/auth/token/*`) keep auth.
   - Default remains **off** — opt-in for the radical-roc use case.

   Read endpoints exempted under `public_read`:
   `/health`, `/api/v1/stats`, `/api/v1/messages`, `/api/v1/messages/{id}`,
   `/api/v1/messages/{id}/body`, `/api/v1/messages/by-rfc822-id/*`,
   `/api/v1/threads/{id}`, `/api/v1/search`, `/api/v1/attachments/{id}`,
   `/api/v1/attachments/{id}/content`, `/api/v1/labels`,
   `/api/v1/labels/{name}/messages`, `/api/v1/participants/{id}`,
   `/api/v1/participants/{id}/messages`, `/api/v1/corpus/fingerprint`,
   and the HTML views `/m/{id}`, `/t/{id}`, `/p/{id}`, `/l/{name}`,
   `/attachment/{id}`.

3. **Rate limit** — bypass loopback (127.0.0.1, ::1) in
   `RateLimitMiddleware`. Keep the 10/s + burst 20 limit on non-loopback. No
   new knob.

4. **Search syntax** — document existing FTS5 semantics. `q` is passed to
   SQLite FTS5 MATCH; spaces = implicit AND; phrases via double-quotes. No
   change to behavior.

5. **HTML views** — server-side rendered with `html/template`, minimal CSS
   inline (no external deps), one shared base layout, no JS. Each view shows
   only its own content (no inbox sidebar). 404 renders an explicit
   "not found in this corpus" page, not the inbox shell.

6. **Attachment safety** — `/api/v1/attachments/{id}/content` always uses
   `Content-Disposition: attachment` **except** for an inline-safe MIME
   safelist: `application/pdf`, `image/png`, `image/jpeg`, `image/gif`,
   `image/webp`, `text/plain` — these get `inline`. HTML/SVG/JS/anything else
   is always served as a download.

---

## Phasing

Each phase = one PR. Order is dependency-driven: posture first, then APIs,
then HTML views (which reuse API handlers).

### Phase 1 — Posture (public-read, loopback rate-limit bypass, version header)

**Files:** `internal/config/config.go` (add `Server.PublicRead bool`),
`internal/api/server.go` (wire posture into router), `internal/api/middleware.go`
(loopback bypass; new `publicReadAuthMiddleware` that skips auth for
safelisted GET/HEAD paths when `PublicRead` is on), `cmd/msgvault/cmd/serve.go`
(maybe a `--public-read` flag).

**Tests:** middleware tests for CORS with `null` origin, loopback bypass,
public-read auth skip on read endpoints + auth-still-required on write.

**Acceptance:** with `public_read = true`, `curl -H 'Origin: null'
http://127.0.0.1:8080/api/v1/messages/1` returns 200 JSON with
`Access-Control-Allow-Origin: null` and no API key.

### Phase 2 — Message-by-RFC822-ID + body endpoint

**New endpoints:**
- `GET /api/v1/messages/by-rfc822-id/{rfc822_id}` → same shape as `/messages/{id}` (C5).
- `GET /api/v1/messages/{id}/body?format=html|text` → raw `text/html` or
  `text/plain` of the message body. Defaults to `html` when an HTML part
  exists, else `text`.

**Files:** `internal/store/store.go` (add `GetMessageByRFC822ID`), handler in
`internal/api/handlers.go`, route wiring.

**Acceptance:** body endpoint serves the right Content-Type; rfc822 lookup
returns 404 with the same JSON shape on miss.

### Phase 3 — Threads

**New endpoints:**
- `GET /api/v1/threads/{id}` → JSON `{id, subject, participants[],
  message_count, messages:[{id, sent_at, from, snippet}, ...]}`.
- `GET /t/{id}` → HTML view (clean, no chrome) rendering the thread as a
  vertical stack of message cards.

**Files:** `internal/store/store.go` (add `GetThread(id) (Thread, error)` if
not already present; otherwise extend `query` engine), handler, template.

**Acceptance:** Hitting a known thread renders all messages in order with
collapsible headers; 404 for unknown thread renders the not-found template.

### Phase 4 — Single-message HTML view (`/m/<id>`)

**New endpoint:** `GET /m/{id}` — clean view: headers (subject, from, to, cc,
sent_at, thread context line), body (HTML preferred), attachment list with
links to `/attachment/{id}`. Reuses the Phase 2/3 handlers internally.

**Files:** template, handler, route.

**Acceptance:** iframe-embeds cleanly into radical-roc spike 002 reviewer pane
without the inbox sidebar leaking in.

### Phase 5 — Attachments

**New endpoints:**
- `GET /api/v1/attachments/{id}` → JSON metadata `{id, filename, mime,
  size_bytes, message_id, content_hash}`.
- `GET /api/v1/attachments/{id}/content` → raw bytes with the inline-safelist
  rule from decision (6).
- `GET /attachment/{id}` → HTML preview page: iframes the content URL for
  PDFs/images; renders a download CTA for everything else.

**Files:** store has attachments in schema already; add lookup method, handler,
template. Reuse content-hash dedup that already exists.

**Acceptance:** PDF citation in radical-roc renders inline in an `<iframe>`;
HTML attachment downloads instead of executing.

### Phase 6 — Labels + Participants

**New endpoints (mirrored shape):**
- `GET /api/v1/labels` → `[{name, count}, ...]`.
- `GET /api/v1/labels/{name}/messages?limit=N&offset=M` → paginated.
- `GET /l/{name}` → HTML list.
- `GET /api/v1/participants/{id}` → `{id, name, address, message_count, first_seen, last_seen}`.
- `GET /api/v1/participants/{id}/messages?limit=N&offset=M` → paginated.
- `GET /p/{id}` → HTML list.

**Files:** store methods (labels and participants tables already exist),
handlers, two templates.

**Acceptance:** counts match `stats` totals; pagination caps respect existing
`maxPageSize = 500`.

### Phase 7 — Corpus fingerprint

**New endpoint:** `GET /api/v1/corpus/fingerprint` → `{fingerprint:
"sha256:...", as_of, message_count, latest_message_sent_at}`.

**Fingerprint recipe (commit to this and document):** `sha256(<message_count>
|| <latest_sent_at_unix> || <max(id)>)`. Stable as long as no compaction
renumbers ids. Document the failure mode: this changes on every sync, which
is fine for "did the corpus change at all" but not stable per-citation —
that's C1's job (not in scope).

**Files:** new handler; one new store query (cheap, single SQL).

**Acceptance:** two consecutive calls with no sync between them return the
same fingerprint.

### Phase 8 — Docs

`docs/PUBLIC_API.md` describing every endpoint above, the `public_read`
config, the CORS posture, the rate-limit bypass for loopback, and the
declined items (C1–C6) with one-line rationale each.

Update `CLAUDE.md` `serve` examples to mention `[server].public_read`.

---

## Out of scope (revisit if requested)

- C1, C2, C3, C4, C6 from the wishlist (see "Decisions" above).
- Any auth/identity beyond the existing API-key check.
- Pagination link headers (RFC 5988) — clients today read `total` + use
  offset/limit; not worth retrofitting unless radical-roc asks.

---

## Risk + rollback

- All new endpoints are additive. `public_read` defaults to `false`, so
  existing deployments are unchanged.
- The loopback rate-limit bypass is the one behavior change that affects
  default deployments. Acceptable because loopback already implies same-host
  trust; if someone runs `serve` exposed to a network they must rely on the
  `bind_addr` + `api_key` posture anyway, and that path is unchanged.
- Rollback per-phase = revert that phase's PR; phases 2–7 don't depend on
  each other once Phase 1 lands.
