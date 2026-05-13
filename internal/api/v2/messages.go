package v2

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wesm/msgvault/internal/store"
)

// handleGetMessage serves /api/v2/messages/{id}.
func (h *Handler) handleGetMessage(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Message ID must be a number")
		return
	}
	if h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}

	msg, err := h.Store.GetMessageV2(id)
	if err != nil {
		h.Logger.Error("failed to get message v2", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve message")
		return
	}
	if msg == nil {
		writeError(w, http.StatusNotFound, "not_found", "Message not found")
		return
	}
	writeJSON(w, http.StatusOK, messageDetailFrom(msg))
}

// handleGetMessageByRFC822ID serves
// /api/v2/messages/by-rfc822-id/{rfc822_id}. The path segment must be
// percent-decoded — same chi.URLParam caveat as the v1 handler.
func (h *Handler) handleGetMessageByRFC822ID(w http.ResponseWriter, r *http.Request) {
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
	if h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}

	msg, err := h.Store.GetMessageV2ByRFC822ID(rfcID)
	if err != nil {
		h.Logger.Error("failed to get message v2 by rfc822 id", "rfc822_id", rfcID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve message")
		return
	}
	if msg == nil {
		writeError(w, http.StatusNotFound, "not_found", "Message not found")
		return
	}
	writeJSON(w, http.StatusOK, messageDetailFrom(msg))
}

// handleListMessages paginates the corpus with the v2 summary shape.
// limit/offset (default 50, clamped to maxPageSize).
func (h *Handler) handleListMessages(w http.ResponseWriter, r *http.Request) {
	if h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}
	limit, offset := parseLimitOffset(r)

	msgs, total, err := h.Store.ListMessages(offset, limit)
	if err != nil {
		h.Logger.Error("list messages v2", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve messages")
		return
	}
	writeJSON(w, http.StatusOK, h.paginate(msgs, total, offset, limit))
}

// handleListMessagesByLabel is the v2 take on
// /api/v1/labels/{name}/messages.
func (h *Handler) handleListMessagesByLabel(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_name", "Label name is required")
		return
	}
	if h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}
	limit, offset := parseLimitOffset(r)

	msgs, total, err := h.Store.ListMessagesByLabel(name, offset, limit)
	if err != nil {
		h.Logger.Error("list by label v2", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve messages")
		return
	}
	writeJSON(w, http.StatusOK, h.paginate(msgs, total, offset, limit))
}

// handleListMessagesByParticipant is the v2 take on
// /api/v1/participants/{id}/messages.
func (h *Handler) handleListMessagesByParticipant(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Participant ID must be a number")
		return
	}
	if h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Database not available")
		return
	}
	limit, offset := parseLimitOffset(r)

	msgs, total, err := h.Store.ListMessagesByParticipant(id, offset, limit)
	if err != nil {
		h.Logger.Error("list by participant v2", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve messages")
		return
	}
	writeJSON(w, http.StatusOK, h.paginate(msgs, total, offset, limit))
}

// messageDetailFrom converts the store type into the JSON DTO,
// applying the v2 conventions (RFC3339 timestamps, empty-slice
// defaults so JSON shows [] not null, angle-bracketed Message-ID
// strings so consumers don't have to remember which header the
// underlying MIME parser stripped).
func messageDetailFrom(m *store.APIMessageV2) MessageDetail {
	references := make([]string, 0, len(m.References))
	for _, r := range m.References {
		references = append(references, normalizeMessageID(r))
	}
	if len(references) == 0 {
		references = nil
	}
	d := MessageDetail{
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
		Body: MessageBody{
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
		d.From = &EmailAddress{Name: m.From.Name, Address: m.From.Address}
	}
	d.To = addresses(m.To)
	d.Cc = addresses(m.Cc)
	d.Bcc = addresses(m.Bcc)
	d.ReplyTo = addresses(m.ReplyTo)

	d.Attachments = make([]Attachment, 0, len(m.Attachments))
	for _, a := range m.Attachments {
		d.Attachments = append(d.Attachments, Attachment{
			ID:          a.ID,
			Filename:    a.Filename,
			MimeType:    a.MimeType,
			SizeBytes:   a.SizeBytes,
			ContentHash: a.ContentHash,
		})
	}
	return d
}

// paginate is the shared shape-and-enrich step for list endpoints.
// Hydrates structured recipients for the page, converts to JSON
// summaries, wraps in the paginated envelope.
func (h *Handler) paginate(msgs []store.APIMessage, total int64, offset, limit int) PaginatedMessages {
	summaries, err := h.summaries(msgs)
	if err != nil {
		// Enrichment failure is non-fatal for list pages — emit
		// fallback summaries built from the v1 strings so a transient
		// participants-table glitch doesn't break the page. The
		// per-row recipients will just be empty.
		h.Logger.Warn("paginate enrich failed; falling back", "error", err)
		summaries = make([]MessageSummary, len(msgs))
		for i, m := range msgs {
			summaries[i] = messageSummaryFromAPIMessage(m, nil, nil)
		}
	}
	return PaginatedMessages{
		Total: total, Offset: offset, Limit: limit, Messages: summaries,
	}
}

// summaries enriches a list of v1 APIMessage rows with structured
// recipients AND the v2-only meta fields (rfc822_message_id, account,
// received_at, message_type, attachment_count, source_message_id) in
// two batched queries, then converts each row to a MessageSummary.
// Recipients-batch failure is fatal; meta-batch failure is fatal too
// (we'd rather a 500 than silently drop fields the consumer is
// encoding citations against).
func (h *Handler) summaries(msgs []store.APIMessage) ([]MessageSummary, error) {
	if len(msgs) == 0 {
		return []MessageSummary{}, nil
	}
	ids := make([]int64, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	rcps, err := h.Store.BatchStructuredRecipients(ids)
	if err != nil {
		return nil, err
	}
	meta, err := h.Store.BatchMessageMetaV2(ids)
	if err != nil {
		return nil, err
	}
	out := make([]MessageSummary, len(msgs))
	for i, m := range msgs {
		out[i] = messageSummaryFromAPIMessage(m, rcps[m.ID], meta[m.ID])
	}
	return out, nil
}

// messageSummaryFromAPIMessage converts a v1 APIMessage row plus its
// (optional) structured recipients and (optional) v2 meta into a v2
// summary DTO. Either side-channel arg may be nil — fallback paths
// (e.g. paginate after a transient enrichment failure) pass nil and
// get a partial-but-valid summary.
func messageSummaryFromAPIMessage(m store.APIMessage, rcp *store.APIRecipientsV2, meta *store.APIMessageMetaV2) MessageSummary {
	s := MessageSummary{
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
			s.From = &EmailAddress{Name: rcp.From.Name, Address: rcp.From.Address}
		}
		s.To = addresses(rcp.To)
		s.Cc = addresses(rcp.Cc)
	} else {
		s.To = []EmailAddress{}
		s.Cc = []EmailAddress{}
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
