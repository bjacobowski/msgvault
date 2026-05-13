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
