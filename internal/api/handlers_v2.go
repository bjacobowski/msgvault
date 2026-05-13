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

// v2APIVersionHeader replaces the global v1 header for routes mounted
// under /api/v2.
func v2APIVersionHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-MsgVault-API", "v2")
		next.ServeHTTP(w, r)
	})
}
