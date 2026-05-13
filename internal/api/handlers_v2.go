package api

// /api/v2 handlers. The v2 mount exists to fix the message-detail
// shape: v1 collapses the From/To/Cc/Bcc headers into "Name <email>"
// strings and omits a handful of fields downstream consumers care
// about (rfc822_message_id, in_reply_to, references, thread_id,
// account, attachment ids). v2 returns structured addresses and the
// full standard-metadata set without breaking any in-flight v1
// consumers.

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wesm/msgvault/internal/store"
)

// MessageDetailV2 is the v2 message-detail JSON shape. Every field
// downstream cared about is named and typed explicitly; nothing is
// collapsed into a display string. Empty slices/maps render as JSON
// arrays/objects rather than null so consumers can iterate without
// nil-checks. `omitempty` on optional-only fields keeps responses
// small for messages that don't have e.g. references or a Bcc list.
type MessageDetailV2 struct {
	ID              int64             `json:"id"`
	RFC822MessageID string            `json:"rfc822_message_id,omitempty"`
	SourceMessageID string            `json:"source_message_id,omitempty"`
	ThreadID        int64             `json:"thread_id,omitempty"`
	Account         string            `json:"account,omitempty"`
	MessageType     string            `json:"message_type,omitempty"`
	Subject         string            `json:"subject"`
	Snippet         string            `json:"snippet,omitempty"`
	From            *EmailAddressDTO  `json:"from,omitempty"`
	To              []EmailAddressDTO `json:"to"`
	Cc              []EmailAddressDTO `json:"cc"`
	Bcc             []EmailAddressDTO `json:"bcc"`
	ReplyTo         []EmailAddressDTO `json:"reply_to,omitempty"`
	InReplyTo       string            `json:"in_reply_to,omitempty"`
	References      []string          `json:"references,omitempty"`
	SentAt          string            `json:"sent_at,omitempty"`
	ReceivedAt      string            `json:"received_at,omitempty"`
	Labels          []string          `json:"labels"`
	HasAttachments  bool              `json:"has_attachments"`
	AttachmentCount int               `json:"attachment_count"`
	SizeBytes       int64             `json:"size_bytes,omitempty"`
	IsDeleted       bool              `json:"is_deleted"`
	DeletedAt       string            `json:"deleted_at,omitempty"`
	Body            MessageBodyDTO    `json:"body"`
	Attachments     []AttachmentDTOv2 `json:"attachments"`
}

// EmailAddressDTO is the {name, address} pair used everywhere v2
// represents an email participant.
type EmailAddressDTO struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// MessageBodyDTO carries both body parts in a single nested object so
// consumers don't have to peek at sibling fields.
type MessageBodyDTO struct {
	Text string `json:"text,omitempty"`
	HTML string `json:"html,omitempty"`
}

// AttachmentDTOv2 mirrors the v1 standalone-attachment JSON but lives
// inline on the message so callers don't need a second round-trip to
// list a message's attachments.
type AttachmentDTOv2 struct {
	ID          int64  `json:"id"`
	Filename    string `json:"filename,omitempty"`
	MimeType    string `json:"mime"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentHash string `json:"content_hash,omitempty"`
}

// handleGetMessageV2 serves /api/v2/messages/{id}.
func (s *Server) handleGetMessageV2(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Message ID must be a number")
		return
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}

	msg, err := s.store.GetMessageV2(id)
	if err != nil {
		s.logger.Error("failed to get message v2", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve message")
		return
	}
	if msg == nil {
		writeError(w, http.StatusNotFound, "not_found", "Message not found")
		return
	}
	writeJSON(w, http.StatusOK, messageDetailV2From(msg))
}

// handleGetMessageByRFC822IDV2 serves
// /api/v2/messages/by-rfc822-id/{rfc822_id}. The path segment must be
// percent-decoded — same chi.URLParam caveat as the v1 handler.
func (s *Server) handleGetMessageByRFC822IDV2(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "rfc822_id")
	rfcID, err := url.PathUnescape(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_rfc822_id", "RFC822 Message-ID is malformed")
		return
	}
	if rfcID == "" {
		writeError(w, http.StatusBadRequest, "invalid_rfc822_id", "RFC822 Message-ID is required")
		return
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}

	msg, err := s.store.GetMessageV2ByRFC822ID(rfcID)
	if err != nil {
		s.logger.Error("failed to get message v2 by rfc822 id", "rfc822_id", rfcID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve message")
		return
	}
	if msg == nil {
		writeError(w, http.StatusNotFound, "not_found", "Message not found")
		return
	}
	writeJSON(w, http.StatusOK, messageDetailV2From(msg))
}

// messageDetailV2From converts the store type into the JSON DTO,
// applying the v2 conventions (RFC3339 timestamps, empty-slice
// defaults so JSON shows [] not null, angle-bracketed Message-ID
// strings so consumers don't have to remember which header the
// underlying MIME parser stripped).
func messageDetailV2From(m *store.APIMessageV2) MessageDetailV2 {
	references := make([]string, 0, len(m.References))
	for _, r := range m.References {
		references = append(references, normalizeMessageID(r))
	}
	if len(references) == 0 {
		references = nil
	}
	d := MessageDetailV2{
		ID:              m.ID,
		RFC822MessageID: normalizeMessageID(m.RFC822MessageID),
		SourceMessageID: m.SourceMessageID,
		ThreadID:        m.ThreadID,
		Account:         m.AccountEmail,
		MessageType:     m.MessageType,
		Subject:         m.Subject,
		Snippet:         m.Snippet,
		InReplyTo:       normalizeMessageID(m.InReplyTo),
		References:      references,
		Labels:          stringsOrEmpty(m.Labels),
		HasAttachments:  m.HasAttachments,
		AttachmentCount: m.AttachmentCount,
		SizeBytes:       m.SizeBytes,
		IsDeleted:       m.DeletedAt != nil,
		Body: MessageBodyDTO{
			Text: m.BodyText,
			HTML: m.BodyHTML,
		},
	}
	if !m.SentAt.IsZero() {
		d.SentAt = m.SentAt.UTC().Format(time.RFC3339)
	}
	if !m.ReceivedAt.IsZero() {
		d.ReceivedAt = m.ReceivedAt.UTC().Format(time.RFC3339)
	}
	if m.DeletedAt != nil {
		d.DeletedAt = m.DeletedAt.UTC().Format(time.RFC3339)
	}
	if m.From != nil {
		d.From = &EmailAddressDTO{Name: m.From.Name, Address: m.From.Address}
	}
	d.To = addressesV2(m.To)
	d.Cc = addressesV2(m.Cc)
	d.Bcc = addressesV2(m.Bcc)
	d.ReplyTo = addressesV2(m.ReplyTo)

	d.Attachments = make([]AttachmentDTOv2, 0, len(m.Attachments))
	for _, a := range m.Attachments {
		d.Attachments = append(d.Attachments, AttachmentDTOv2{
			ID:          a.ID,
			Filename:    a.Filename,
			MimeType:    a.MimeType,
			SizeBytes:   a.SizeBytes,
			ContentHash: a.ContentHash,
		})
	}
	return d
}

// addressesV2 converts the store slice into the JSON DTO slice. Empty
// input returns an empty slice (not nil) so the JSON encoder emits
// [] for the canonical to/cc/bcc fields. ReplyTo uses omitempty so
// it can stay nil and be elided from the response.
func addressesV2(in []store.APIAddress) []EmailAddressDTO {
	out := make([]EmailAddressDTO, 0, len(in))
	for _, a := range in {
		out = append(out, EmailAddressDTO{Name: a.Name, Address: a.Address})
	}
	return out
}

// stringsOrEmpty returns its input or a new empty slice when nil, so
// JSON encoding emits [] for an empty labels field.
func stringsOrEmpty(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// normalizeMessageID returns a Message-ID-style string wrapped in
// angle brackets if it isn't already. Empty input stays empty.
//
// Why this exists: internal/mime strips `<>` from values it returns
// for the References header but leaves Message-ID / In-Reply-To
// bracketed. RFC 5322 spells msg-id with the angles, and the
// /by-rfc822-id lookup keys on the DB-stored value which is
// bracketed. Normalizing at the v2 boundary keeps consumers from
// having to track which fields lost their brackets.
func normalizeMessageID(s string) string {
	if s == "" {
		return s
	}
	if len(s) >= 2 && s[0] == '<' && s[len(s)-1] == '>' {
		return s
	}
	return "<" + s + ">"
}

// MessageSummaryV2 is the list-mode v2 shape — same identity and
// metadata as MessageDetailV2 minus body, attachments, and the MIME-
// derived headers (in_reply_to, references) that would force a raw
// MIME parse per row. Bcc is intentionally omitted; consumers
// rendering lists rarely need it, and dropping it keeps responses
// lean.
type MessageSummaryV2 struct {
	ID              int64             `json:"id"`
	RFC822MessageID string            `json:"rfc822_message_id,omitempty"`
	SourceMessageID string            `json:"source_message_id,omitempty"`
	ThreadID        int64             `json:"thread_id,omitempty"`
	Account         string            `json:"account,omitempty"`
	MessageType     string            `json:"message_type,omitempty"`
	Subject         string            `json:"subject"`
	Snippet         string            `json:"snippet,omitempty"`
	From            *EmailAddressDTO  `json:"from,omitempty"`
	To              []EmailAddressDTO `json:"to"`
	Cc              []EmailAddressDTO `json:"cc"`
	SentAt          string            `json:"sent_at,omitempty"`
	ReceivedAt      string            `json:"received_at,omitempty"`
	Labels          []string          `json:"labels"`
	HasAttachments  bool              `json:"has_attachments"`
	AttachmentCount int               `json:"attachment_count"`
	SizeBytes       int64             `json:"size_bytes,omitempty"`
	IsDeleted       bool              `json:"is_deleted"`
	DeletedAt       string            `json:"deleted_at,omitempty"`
}

// PaginatedMessagesResponseV2 wraps a paginated message-summary list.
// limit/offset match the v1 label/participant convention; the response
// always includes `total` so consumers can paginate without
// deduplicated-count tricks.
type PaginatedMessagesResponseV2 struct {
	Total    int64              `json:"total"`
	Offset   int                `json:"offset"`
	Limit    int                `json:"limit"`
	Messages []MessageSummaryV2 `json:"messages"`
}

// ThreadResponseV2 mirrors v1 ThreadResponse but embeds the cleaner
// MessageSummaryV2 shape so consumers can iterate a thread without
// dropping back to per-id detail calls just to get structured
// recipients.
type ThreadResponseV2 struct {
	ID           int64                  `json:"id"`
	Subject      string                 `json:"subject"`
	MessageCount int64                  `json:"message_count"`
	Participants []ThreadParticipantDTO `json:"participants"`
	Messages     []MessageSummaryV2     `json:"messages"`
}

// handleListMessagesV2 paginates the corpus with the v2 summary shape.
// limit/offset (default 50, clamped to maxPageSize).
func (s *Server) handleListMessagesV2(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}
	limit, offset := parseLimitOffset(r)

	msgs, total, err := s.store.ListMessages(offset, limit)
	if err != nil {
		s.logger.Error("list messages v2", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve messages")
		return
	}
	writeJSON(w, http.StatusOK, s.paginateV2(msgs, total, offset, limit))
}

// handleListMessagesByLabelV2 is the v2 take on
// /api/v1/labels/{name}/messages.
func (s *Server) handleListMessagesByLabelV2(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_name", "Label name is required")
		return
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}
	limit, offset := parseLimitOffset(r)

	msgs, total, err := s.store.ListMessagesByLabel(name, offset, limit)
	if err != nil {
		s.logger.Error("list by label v2", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve messages")
		return
	}
	writeJSON(w, http.StatusOK, s.paginateV2(msgs, total, offset, limit))
}

// handleListMessagesByParticipantV2 is the v2 take on
// /api/v1/participants/{id}/messages.
func (s *Server) handleListMessagesByParticipantV2(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Participant ID must be a number")
		return
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}
	limit, offset := parseLimitOffset(r)

	msgs, total, err := s.store.ListMessagesByParticipant(id, offset, limit)
	if err != nil {
		s.logger.Error("list by participant v2", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve messages")
		return
	}
	writeJSON(w, http.StatusOK, s.paginateV2(msgs, total, offset, limit))
}

// handleSearchV2 returns search hits as v2 summaries. Accepts the same
// `q` and `mode` (fts|vector|hybrid) parameters as /api/v1/search but
// always uses limit/offset for pagination consistency with the rest
// of v2.
func (s *Server) handleSearchV2(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "missing_query", "Query parameter 'q' is required")
		return
	}
	limit, offset := parseLimitOffset(r)

	// FTS path only — vector/hybrid require the search.Query plumbing
	// that the v1 handler builds. Keeping v2 to FTS for now means we
	// don't take a dependency on the embedder; vector/hybrid stay on
	// /api/v1/search until a v2 caller asks for them.
	msgs, total, err := s.store.SearchMessages(q, offset, limit)
	if err != nil {
		s.logger.Error("search v2", "q", q, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Search failed")
		return
	}
	writeJSON(w, http.StatusOK, s.paginateV2(msgs, total, offset, limit))
}

// handleGetThreadV2 returns the same thread shape as v1 but with
// MessageSummaryV2 entries — structured from/to/cc, thread_id,
// account, rfc822_message_id, etc.
func (s *Server) handleGetThreadV2(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Thread ID must be a number")
		return
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}

	th, err := s.store.GetThread(id)
	if err != nil {
		s.logger.Error("get thread v2", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve thread")
		return
	}
	if th == nil {
		writeError(w, http.StatusNotFound, "not_found", "Thread not found")
		return
	}

	// Pull message IDs and hydrate via ListMessagesByConversation... wait,
	// no such method exists. The store's APIThread.Messages already
	// carries enough to build summaries by re-fetching: do a single
	// GetMessagesSummariesByIDs to grab APIMessage rows, then enrich
	// structured recipients.
	ids := make([]int64, 0, len(th.Messages))
	for _, m := range th.Messages {
		ids = append(ids, m.ID)
	}
	msgs, err := s.store.GetMessagesSummariesByIDs(ids)
	if err != nil {
		s.logger.Error("thread v2 hydrate", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve thread messages")
		return
	}
	summaries, err := s.summariesV2(msgs)
	if err != nil {
		s.logger.Error("thread v2 enrich", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to enrich thread")
		return
	}
	// Preserve the chronological order GetThread returned: re-sort by
	// the id-order of th.Messages.
	indexOf := make(map[int64]int, len(th.Messages))
	for i, m := range th.Messages {
		indexOf[m.ID] = i
	}
	orderedSummaries := make([]MessageSummaryV2, len(summaries))
	for _, s := range summaries {
		if pos, ok := indexOf[s.ID]; ok {
			orderedSummaries[pos] = s
		}
	}

	resp := ThreadResponseV2{
		ID:           th.ID,
		Subject:      th.Subject,
		MessageCount: th.MessageCount,
		Participants: make([]ThreadParticipantDTO, 0, len(th.Participants)),
		Messages:     orderedSummaries,
	}
	for _, p := range th.Participants {
		resp.Participants = append(resp.Participants, ThreadParticipantDTO{
			ID: p.ID, Name: p.Name, Address: p.Address,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// paginateV2 is the shared shape-and-enrich step for list endpoints.
// Hydrates structured recipients for the page, converts to JSON
// summaries, wraps in the paginated envelope.
func (s *Server) paginateV2(msgs []APIMessage, total int64, offset, limit int) PaginatedMessagesResponseV2 {
	summaries, err := s.summariesV2(msgs)
	if err != nil {
		// Enrichment failure is non-fatal for list pages — emit
		// fallback summaries built from the v1 strings so a transient
		// participants-table glitch doesn't break the page. The
		// per-row recipients will just be empty.
		s.logger.Warn("paginateV2 enrich failed; falling back", "error", err)
		summaries = make([]MessageSummaryV2, len(msgs))
		for i, m := range msgs {
			summaries[i] = messageSummaryV2FromAPIMessage(m, nil, nil)
		}
	}
	return PaginatedMessagesResponseV2{
		Total: total, Offset: offset, Limit: limit, Messages: summaries,
	}
}

// summariesV2 enriches a list of v1 APIMessage rows with structured
// recipients AND the v2-only meta fields (rfc822_message_id, account,
// received_at, message_type, attachment_count, source_message_id) in
// two batched queries, then converts each row to a
// MessageSummaryV2. Recipients-batch failure is fatal; meta-batch
// failure is fatal too (we'd rather a 500 than silently drop fields
// the consumer is encoding citations against).
func (s *Server) summariesV2(msgs []APIMessage) ([]MessageSummaryV2, error) {
	if len(msgs) == 0 {
		return []MessageSummaryV2{}, nil
	}
	ids := make([]int64, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	rcps, err := s.store.BatchStructuredRecipients(ids)
	if err != nil {
		return nil, err
	}
	meta, err := s.store.BatchMessageMetaV2(ids)
	if err != nil {
		return nil, err
	}
	out := make([]MessageSummaryV2, len(msgs))
	for i, m := range msgs {
		out[i] = messageSummaryV2FromAPIMessage(m, rcps[m.ID], meta[m.ID])
	}
	return out, nil
}

// messageSummaryV2FromAPIMessage converts a v1 APIMessage row plus
// its (optional) structured recipients and (optional) v2 meta into a
// v2 summary DTO. Either side-channel arg may be nil — fallback paths
// (e.g. paginateV2 after a transient enrichment failure) pass nil and
// get a partial-but-valid summary.
func messageSummaryV2FromAPIMessage(m APIMessage, rcp *store.APIRecipientsV2, meta *store.APIMessageMetaV2) MessageSummaryV2 {
	s := MessageSummaryV2{
		ID:             m.ID,
		ThreadID:       m.ConversationID,
		Subject:        m.Subject,
		Snippet:        m.Snippet,
		Labels:         stringsOrEmpty(m.Labels),
		HasAttachments: m.HasAttachments,
		SizeBytes:      m.SizeEstimate,
		IsDeleted:      m.DeletedAt != nil,
	}
	if !m.SentAt.IsZero() {
		s.SentAt = m.SentAt.UTC().Format(time.RFC3339)
	}
	if m.DeletedAt != nil {
		s.DeletedAt = m.DeletedAt.UTC().Format(time.RFC3339)
	}

	if rcp != nil {
		if rcp.From != nil {
			s.From = &EmailAddressDTO{Name: rcp.From.Name, Address: rcp.From.Address}
		}
		s.To = addressesV2(rcp.To)
		s.Cc = addressesV2(rcp.Cc)
	} else {
		s.To = []EmailAddressDTO{}
		s.Cc = []EmailAddressDTO{}
	}

	if meta != nil {
		s.RFC822MessageID = normalizeMessageID(meta.RFC822MessageID)
		s.SourceMessageID = meta.SourceMessageID
		s.Account = meta.AccountEmail
		s.MessageType = meta.MessageType
		s.AttachmentCount = meta.AttachmentCount
		if !meta.ReceivedAt.IsZero() {
			s.ReceivedAt = meta.ReceivedAt.UTC().Format(time.RFC3339)
		}
	}
	return s
}

// AttachmentResponseV2 is the v2 standalone-attachment JSON. Same
// fields as the v1 AttachmentResponse plus thread_id, account, and
// inline_disposition so a deep-linked attachment view can render
// context without another round-trip.
type AttachmentResponseV2 struct {
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

// ParticipantResponseV2 is the v2 participant JSON. Adds
// is_user_account on top of the v1 fields so consumers can render
// "you" vs "them" without looking up the synced-accounts list.
type ParticipantResponseV2 struct {
	ID            int64  `json:"id"`
	Name          string `json:"name,omitempty"`
	Address       string `json:"address"`
	Domain        string `json:"domain,omitempty"`
	MessageCount  int64  `json:"message_count"`
	FirstSeen     string `json:"first_seen,omitempty"`
	LastSeen      string `json:"last_seen,omitempty"`
	IsUserAccount bool   `json:"is_user_account"`
}

// handleGetAttachmentV2 serves /api/v2/attachments/{id}.
func (s *Server) handleGetAttachmentV2(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Attachment ID must be a number")
		return
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}

	att, err := s.store.GetAttachmentByIDV2(id)
	if err != nil {
		s.logger.Error("get attachment v2", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve attachment")
		return
	}
	if att == nil {
		writeError(w, http.StatusNotFound, "not_found", "Attachment not found")
		return
	}
	writeJSON(w, http.StatusOK, AttachmentResponseV2{
		ID:                att.ID,
		MessageID:         att.MessageID,
		ThreadID:          att.ThreadID,
		Account:           att.AccountEmail,
		Filename:          att.Filename,
		MimeType:          att.MimeType,
		SizeBytes:         att.SizeBytes,
		ContentHash:       att.ContentHash,
		InlineDisposition: isInlineSafeMIME(att.MimeType),
	})
}

// handleGetParticipantV2 serves /api/v2/participants/{id}.
func (s *Server) handleGetParticipantV2(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Participant ID must be a number")
		return
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}

	p, err := s.store.GetParticipantByIDV2(id)
	if err != nil {
		s.logger.Error("get participant v2", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve participant")
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "not_found", "Participant not found")
		return
	}

	resp := ParticipantResponseV2{
		ID:            p.ID,
		Name:          p.Name,
		Address:       p.Address,
		Domain:        p.Domain,
		MessageCount:  p.MessageCount,
		IsUserAccount: p.IsUserAccount,
	}
	if !p.FirstSeen.IsZero() {
		resp.FirstSeen = p.FirstSeen.UTC().Format(time.RFC3339)
	}
	if !p.LastSeen.IsZero() {
		resp.LastSeen = p.LastSeen.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

// v2APIVersionHeader replaces the global v1 header for routes mounted
// under /api/v2.
func v2APIVersionHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-MsgVault-API", "v2")
		next.ServeHTTP(w, r)
	})
}
