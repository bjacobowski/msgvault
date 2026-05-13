package store

// v2 message-detail accessor. Builds APIMessageV2 by combining four
// data sources:
//
//   1. messages + sources + message_bodies tables (core fields, body)
//   2. message_recipients + participants (structured to/cc/bcc/from)
//   3. labels via getLabels
//   4. raw MIME blob, re-parsed via internal/mime, for the headers
//      that aren't denormalized into columns (in_reply_to, references,
//      reply_to)
//
// Step 4 is the only expensive bit — pulling the raw blob, zlib-
// decompressing, and re-parsing MIME. For the radical-roc review-app
// workload (a handful of citation lookups per opened artifact) it's
// well within budget; we don't cache. If detail-endpoint traffic ever
// becomes hot we should denormalize in_reply_to / references into
// columns rather than cache parsed structs.

import (
	"database/sql"
	"fmt"

	"github.com/wesm/msgvault/internal/mime"
)

// GetMessageV2 returns the fully populated v2 detail for a single
// internal message id, or (nil, nil) when the id is unknown.
func (s *Store) GetMessageV2(id int64) (*APIMessageV2, error) {
	return s.getMessageV2Where("m.id = ?", id)
}

// GetMessageV2ByRFC822ID is the cross-source by-Message-ID lookup.
// Lowest internal id wins on collision, mirroring the v1
// GetMessageByRFC822ID contract.
func (s *Store) GetMessageV2ByRFC822ID(rfc822ID string) (*APIMessageV2, error) {
	var id int64
	err := s.db.QueryRow(
		`SELECT id FROM messages WHERE rfc822_message_id = ? ORDER BY id LIMIT 1`,
		rfc822ID,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("v2 lookup by rfc822 id: %w", err)
	}
	return s.GetMessageV2(id)
}

// getMessageV2Where is the shared core: build the v2 detail starting
// from a row in `messages` that matches the given WHERE fragment.
// Callers supply the predicate so we don't grow a query method per
// lookup path.
func (s *Store) getMessageV2Where(where string, args ...any) (*APIMessageV2, error) {
	query := `
		SELECT
			m.id,
			COALESCE(m.rfc822_message_id, ''),
			m.source_message_id,
			COALESCE(m.conversation_id, 0),
			COALESCE(src.identifier, ''),
			COALESCE(m.message_type, ''),
			COALESCE(m.subject, ''),
			COALESCE(m.snippet, ''),
			COALESCE(m.sent_at, m.received_at, m.internal_date),
			COALESCE(m.received_at, ''),
			m.has_attachments,
			COALESCE(m.attachment_count, 0),
			m.size_estimate,
			m.deleted_from_source_at
		  FROM messages m
		  LEFT JOIN sources src ON src.id = m.source_id
		 WHERE ` + where

	var (
		v            APIMessageV2
		sentAtStr    sql.NullString
		recvAtStr    sql.NullString
		deletedAtStr sql.NullString
	)
	err := s.db.QueryRow(query, args...).Scan(
		&v.ID, &v.RFC822MessageID, &v.SourceMessageID, &v.ThreadID,
		&v.AccountEmail, &v.MessageType, &v.Subject, &v.Snippet,
		&sentAtStr, &recvAtStr, &v.HasAttachments, &v.AttachmentCount,
		&v.SizeBytes, &deletedAtStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("v2 message detail: %w", err)
	}
	if sentAtStr.Valid && sentAtStr.String != "" {
		v.SentAt = parseSQLiteTime(sentAtStr.String)
	}
	if recvAtStr.Valid && recvAtStr.String != "" {
		v.ReceivedAt = parseSQLiteTime(recvAtStr.String)
	}
	if deletedAtStr.Valid && deletedAtStr.String != "" {
		t := parseSQLiteTime(deletedAtStr.String)
		v.DeletedAt = &t
	}

	// Structured participants from message_recipients + participants.
	for _, role := range []string{"from", "to", "cc", "bcc"} {
		addrs, err := s.getStructuredRecipients(v.ID, role)
		if err != nil {
			return nil, fmt.Errorf("v2 recipients %s: %w", role, err)
		}
		switch role {
		case "from":
			if len(addrs) > 0 {
				a := addrs[0]
				v.From = &a
			}
		case "to":
			v.To = addrs
		case "cc":
			v.Cc = addrs
		case "bcc":
			v.Bcc = addrs
		}
	}

	// Labels via the same helper GetMessage uses.
	labels, err := s.getLabels(v.ID)
	if err != nil {
		return nil, fmt.Errorf("v2 labels: %w", err)
	}
	v.Labels = labels

	// Body text + html via the dedicated message_bodies accessor.
	text, html, err := s.GetMessageBodies(v.ID)
	if err != nil {
		return nil, fmt.Errorf("v2 bodies: %w", err)
	}
	v.BodyText = text
	v.BodyHTML = html

	// Attachments with ids.
	atts, err := s.getAttachmentsForMessage(v.ID)
	if err != nil {
		return nil, fmt.Errorf("v2 attachments: %w", err)
	}
	v.Attachments = atts

	// MIME-derived headers: in_reply_to, references, reply_to. Best
	// effort — a missing raw blob (older imports without raw MIME
	// stored) leaves these zero-valued rather than failing the lookup.
	if raw, err := s.GetMessageRaw(v.ID); err == nil && len(raw) > 0 {
		if parsed, perr := mime.Parse(raw); perr == nil {
			v.InReplyTo = parsed.InReplyTo
			v.References = parsed.References
			v.ReplyTo = mimeAddressesToAPI(parsed.ReplyTo)
		}
	}

	return &v, nil
}

// BatchMessageMetaV2 returns the v2-only metadata fields
// (rfc822_message_id, source_message_id, account email, message_type,
// received_at, attachment_count) for a slice of message ids in a
// single SQL round-trip. Pairs with BatchStructuredRecipients to let
// v2 list endpoints hydrate everything in two extra queries.
func (s *Store) BatchMessageMetaV2(ids []int64) (map[int64]*APIMessageMetaV2, error) {
	out := make(map[int64]*APIMessageMetaV2)
	if len(ids) == 0 {
		return out, nil
	}

	err := queryInChunks(s.db, ids, nil,
		`SELECT m.id,
		        COALESCE(m.rfc822_message_id, ''),
		        m.source_message_id,
		        COALESCE(m.message_type, ''),
		        COALESCE(m.received_at, ''),
		        COALESCE(m.attachment_count, 0),
		        COALESCE(src.identifier, '')
		   FROM messages m
		   LEFT JOIN sources src ON src.id = m.source_id
		  WHERE m.id IN (%s)`,
		func(rows *loggedRows) error {
			var (
				id        int64
				meta      APIMessageMetaV2
				recvAtStr sql.NullString
			)
			if err := rows.Scan(&id, &meta.RFC822MessageID, &meta.SourceMessageID,
				&meta.MessageType, &recvAtStr, &meta.AttachmentCount, &meta.AccountEmail); err != nil {
				return err
			}
			if recvAtStr.Valid && recvAtStr.String != "" {
				meta.ReceivedAt = parseSQLiteTime(recvAtStr.String)
			}
			m := meta
			out[id] = &m
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("batch message meta v2: %w", err)
	}
	return out, nil
}

// BatchStructuredRecipients returns the structured-address mapping
// for a slice of message ids in a single SQL round-trip. Used by v2
// list endpoints to hydrate many messages without N+1 queries.
//
// Missing ids are simply absent from the returned map — callers are
// responsible for handling messages that have no recipient rows
// (e.g. a row in `messages` with no `message_recipients`). Bcc is
// included even though list responses don't surface it today, so
// future consumers can opt in without a schema change.
func (s *Store) BatchStructuredRecipients(ids []int64) (map[int64]*APIRecipientsV2, error) {
	out := make(map[int64]*APIRecipientsV2)
	if len(ids) == 0 {
		return out, nil
	}

	err := queryInChunks(s.db, ids, nil,
		`SELECT mr.message_id, mr.recipient_type,
		        COALESCE(p.display_name, ''), COALESCE(p.email_address, '')
		   FROM message_recipients mr
		   JOIN participants p ON p.id = mr.participant_id
		  WHERE mr.message_id IN (%s)
		  ORDER BY mr.message_id, mr.id`,
		func(rows *loggedRows) error {
			var (
				msgID int64
				role  string
				addr  APIAddress
			)
			if err := rows.Scan(&msgID, &role, &addr.Name, &addr.Address); err != nil {
				return err
			}
			entry, ok := out[msgID]
			if !ok {
				entry = &APIRecipientsV2{}
				out[msgID] = entry
			}
			switch role {
			case "from":
				if entry.From == nil {
					a := addr
					entry.From = &a
				}
			case "to":
				entry.To = append(entry.To, addr)
			case "cc":
				entry.Cc = append(entry.Cc, addr)
			case "bcc":
				entry.Bcc = append(entry.Bcc, addr)
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("batch structured recipients: %w", err)
	}
	return out, nil
}

// getStructuredRecipients reads message_recipients + participants for
// the given role and returns each address as a structured pair.
func (s *Store) getStructuredRecipients(messageID int64, role string) ([]APIAddress, error) {
	rows, err := s.db.Query(`
		SELECT COALESCE(p.display_name, ''), COALESCE(p.email_address, '')
		  FROM message_recipients mr
		  JOIN participants p ON p.id = mr.participant_id
		 WHERE mr.message_id = ? AND mr.recipient_type = ?
		 ORDER BY mr.id`,
		messageID, role,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []APIAddress
	for rows.Next() {
		var a APIAddress
		if err := rows.Scan(&a.Name, &a.Address); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// getAttachmentsForMessage returns the attachment summaries for a
// single message, including the attachment id so consumers can deep
// link.
func (s *Store) getAttachmentsForMessage(messageID int64) ([]APIAttachmentV2, error) {
	rows, err := s.db.Query(
		`SELECT id, COALESCE(filename, ''), COALESCE(mime_type, ''), COALESCE(size, 0), COALESCE(content_hash, '')
		   FROM attachments WHERE message_id = ? ORDER BY id`,
		messageID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []APIAttachmentV2
	for rows.Next() {
		var a APIAttachmentV2
		if err := rows.Scan(&a.ID, &a.Filename, &a.MimeType, &a.SizeBytes, &a.ContentHash); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// mimeAddressesToAPI converts the mime package's address slice into
// the API's structured-address slice. Returns nil for nil so callers
// can omit empty fields with `omitempty`.
func mimeAddressesToAPI(in []mime.Address) []APIAddress {
	if len(in) == 0 {
		return nil
	}
	out := make([]APIAddress, 0, len(in))
	for _, a := range in {
		out = append(out, APIAddress{Name: a.Name, Address: a.Email})
	}
	return out
}
