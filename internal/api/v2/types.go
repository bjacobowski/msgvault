// Package v2 implements the /api/v2 HTTP handlers and JSON contract.
// v2 exists to fix the v1 message-detail shape: v1 collapses From/To/Cc/Bcc
// into "Name <email>" strings and omits a handful of fields downstream
// consumers care about (rfc822_message_id, in_reply_to, references,
// thread_id, account, attachment ids). v2 returns structured addresses
// and the full standard-metadata set without breaking in-flight v1
// consumers.
//
// Type names in this package deliberately have no V2 suffix — version is
// expressed by the package path, not by repeating "v2" in every type.
package v2

// MessageDetail is the v2 message-detail JSON shape. Every field
// downstream cared about is named and typed explicitly; nothing is
// collapsed into a display string. Empty slices/maps render as JSON
// arrays/objects rather than null so consumers can iterate without
// nil-checks. `omitempty` on optional-only fields keeps responses
// small for messages that don't have e.g. references or a Bcc list.
type MessageDetail struct {
	ID              int64          `json:"id"`
	RFC822MessageID string         `json:"rfc822_message_id,omitempty"`
	SourceMessageID string         `json:"source_message_id,omitempty"`
	ThreadID        int64          `json:"thread_id,omitempty"`
	Account         string         `json:"account,omitempty"`
	MessageType     string         `json:"message_type,omitempty"`
	Subject         string         `json:"subject"`
	Snippet         string         `json:"snippet,omitempty"`
	From            *EmailAddress  `json:"from,omitempty"`
	To              []EmailAddress `json:"to"`
	Cc              []EmailAddress `json:"cc"`
	Bcc             []EmailAddress `json:"bcc"`
	ReplyTo         []EmailAddress `json:"reply_to,omitempty"`
	InReplyTo       string         `json:"in_reply_to,omitempty"`
	References      []string       `json:"references,omitempty"`
	SentAt          string         `json:"sent_at,omitempty"`
	ReceivedAt      string         `json:"received_at,omitempty"`
	Labels          []string       `json:"labels"`
	HasAttachments  bool           `json:"has_attachments"`
	AttachmentCount int            `json:"attachment_count"`
	SizeBytes       int64          `json:"size_bytes,omitempty"`
	IsDeleted       bool           `json:"is_deleted"`
	DeletedAt       string         `json:"deleted_at,omitempty"`
	Body            MessageBody    `json:"body"`
	Attachments     []Attachment   `json:"attachments"`
}

// EmailAddress is the {name, address} pair used everywhere v2
// represents an email participant.
type EmailAddress struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// MessageBody carries both body parts in a single nested object so
// consumers don't have to peek at sibling fields.
type MessageBody struct {
	Text string `json:"text,omitempty"`
	HTML string `json:"html,omitempty"`
}

// Attachment is the inline-on-message attachment shape: same fields as
// the standalone-attachment JSON minus the cross-message context, so a
// message-detail response can list attachments without a second
// round-trip.
type Attachment struct {
	ID          int64  `json:"id"`
	Filename    string `json:"filename,omitempty"`
	MimeType    string `json:"mime"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentHash string `json:"content_hash,omitempty"`
}

// MessageSummary is the list-mode v2 shape — same identity and
// metadata as MessageDetail minus body, attachments, and the MIME-
// derived headers (in_reply_to, references) that would force a raw
// MIME parse per row. Bcc is intentionally omitted; consumers
// rendering lists rarely need it, and dropping it keeps responses
// lean.
type MessageSummary struct {
	ID              int64          `json:"id"`
	RFC822MessageID string         `json:"rfc822_message_id,omitempty"`
	SourceMessageID string         `json:"source_message_id,omitempty"`
	ThreadID        int64          `json:"thread_id,omitempty"`
	Account         string         `json:"account,omitempty"`
	MessageType     string         `json:"message_type,omitempty"`
	Subject         string         `json:"subject"`
	Snippet         string         `json:"snippet,omitempty"`
	From            *EmailAddress  `json:"from,omitempty"`
	To              []EmailAddress `json:"to"`
	Cc              []EmailAddress `json:"cc"`
	SentAt          string         `json:"sent_at,omitempty"`
	ReceivedAt      string         `json:"received_at,omitempty"`
	Labels          []string       `json:"labels"`
	HasAttachments  bool           `json:"has_attachments"`
	AttachmentCount int            `json:"attachment_count"`
	SizeBytes       int64          `json:"size_bytes,omitempty"`
	IsDeleted       bool           `json:"is_deleted"`
	DeletedAt       string         `json:"deleted_at,omitempty"`
}

// PaginatedMessages wraps a paginated message-summary list. limit/offset
// match the v1 label/participant convention; the response always
// includes `total` so consumers can paginate without deduplicated-count
// tricks.
type PaginatedMessages struct {
	Total    int64            `json:"total"`
	Offset   int              `json:"offset"`
	Limit    int              `json:"limit"`
	Messages []MessageSummary `json:"messages"`
}

// Thread mirrors v1's thread response but embeds the cleaner
// MessageSummary shape so consumers can iterate a thread without
// dropping back to per-id detail calls just to get structured
// recipients.
type Thread struct {
	ID           int64               `json:"id"`
	Subject      string              `json:"subject"`
	MessageCount int64               `json:"message_count"`
	Participants []ThreadParticipant `json:"participants"`
	Messages     []MessageSummary    `json:"messages"`
}

// ThreadParticipant names a single party in a thread.
type ThreadParticipant struct {
	ID      int64  `json:"id"`
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// AttachmentDetail is the v2 standalone-attachment JSON. Same fields as
// the v1 AttachmentResponse plus thread_id, account, and
// inline_disposition so a deep-linked attachment view can render
// context without another round-trip.
type AttachmentDetail struct {
	ID                int64  `json:"id"`
	MessageID         int64  `json:"message_id"`
	ThreadID          int64  `json:"thread_id,omitempty"`
	Account           string `json:"account,omitempty"`
	Filename          string `json:"filename,omitempty"`
	MimeType          string `json:"mime"`
	SizeBytes         int64  `json:"size_bytes"`
	ContentHash       string `json:"content_hash,omitempty"`
	InlineDisposition bool   `json:"inline_disposition"`
}

// Participant is the v2 participant JSON. Adds is_user_account on top
// of the v1 fields so consumers can render "you" vs "them" without
// looking up the synced-accounts list.
type Participant struct {
	ID            int64  `json:"id"`
	Name          string `json:"name,omitempty"`
	Address       string `json:"address"`
	Domain        string `json:"domain,omitempty"`
	MessageCount  int64  `json:"message_count"`
	FirstSeen     string `json:"first_seen,omitempty"`
	LastSeen      string `json:"last_seen,omitempty"`
	IsUserAccount bool   `json:"is_user_account"`
}

// ErrorResponse is the v2 error envelope. Field shape matches v1 byte-
// for-byte so a single typed client can decode both surfaces; this is
// duplicated rather than imported to keep v2 free of any back-edge into
// internal/api.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// SearchResponse is the unified /api/v2/search shape across fts,
// vector, and hybrid modes. The same top-level key set is emitted
// regardless of mode, with three optional metadata fields populated
// only for vector/hybrid responses; this gives typed clients exactly
// one Zod schema (or equivalent) for the whole search surface.
//
// Total is a pointer because the two pagination models differ: FTS
// returns a global total count over the corpus, while vector/hybrid
// returns a top-k relevance pool with no global total. Rather than
// overload Total with len(messages) for vector/hybrid (which would
// mislead generic pagers into thinking the result set is exhausted),
// we emit `"total": null` and let Returned carry the "what you got
// back" signal unambiguously.
type SearchResponse struct {
	Query         string      `json:"query"`
	Mode          string      `json:"mode"`
	Offset        int         `json:"offset"`
	Limit         int         `json:"limit"`
	Total         *int64      `json:"total"`
	Returned      int         `json:"returned"`
	Messages      []SearchHit `json:"messages"`
	Generation    *Generation `json:"generation,omitempty"`
	PoolSaturated *bool       `json:"pool_saturated,omitempty"`
	TookMS        *int64      `json:"took_ms,omitempty"`
}

// SearchHit is a single search result — a MessageSummary plus, for
// explain=1 requests, the per-signal score breakdown the engine used
// to rank the hit.
type SearchHit struct {
	MessageSummary
	Score *ScoreBreakdown `json:"score,omitempty"`
}

// ScoreBreakdown exposes the fused-score components for a hit. RRF,
// BM25, and Vector are pointer-typed so a missing signal is
// distinguishable from a legitimate 0.0 score. In particular,
// mode=vector reports vector with no rrf (RRF requires two signals to
// fuse), and mode=fts hits never carry a score (fts is unscored at
// this surface).
//
// Defined locally in v2 — even though the field set happens to match
// v1's debug shape today, the explicit duplication keeps the v2 wire
// contract independent of v1 evolution.
type ScoreBreakdown struct {
	RRF            *float64 `json:"rrf,omitempty"`
	BM25           *float64 `json:"bm25,omitempty"`
	Vector         *float64 `json:"vector,omitempty"`
	SubjectBoosted bool     `json:"subject_boosted,omitempty"`
}

// Generation summarizes the active vector-index generation used to
// answer a vector/hybrid query. Returned only for those modes.
type Generation struct {
	ID          int64  `json:"id"`
	Model       string `json:"model"`
	Dimension   int    `json:"dimension"`
	Fingerprint string `json:"fingerprint"`
	State       string `json:"state"`
}
