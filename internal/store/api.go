package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/wesm/msgvault/internal/search"
)

// APIMessage represents a message for API responses.
type APIMessage struct {
	ID             int64
	ConversationID int64
	Subject        string
	From           string
	To             []string
	Cc             []string
	Bcc            []string
	SentAt         time.Time
	Snippet        string
	Labels         []string
	HasAttachments bool
	SizeEstimate   int64
	DeletedAt      *time.Time
	Body           string
	Headers        map[string]string
	Attachments    []APIAttachment
}

// APIAttachment represents attachment metadata for API responses.
type APIAttachment struct {
	Filename string
	MimeType string
	Size     int64
}

// APIAddress is one structured email participant. Distinct from a
// participant *row* (APIParticipant) — addresses appear inline on
// every message; participants are the canonical identities.
type APIAddress struct {
	Name    string
	Address string
}

// APIMessageMetaV2 holds the v2-specific fields that aren't already
// on APIMessage but are cheap to batch-load alongside list results.
// Populated by BatchMessageMetaV2 and merged in at the API layer so
// v1 list methods stay untouched.
type APIMessageMetaV2 struct {
	RFC822MessageID string
	SourceMessageID string
	AccountEmail    string
	MessageType     string
	ReceivedAt      time.Time
	AttachmentCount int
}

// APIMessageSummaryV2 is the list-mode shape: same identity and
// metadata as APIMessageV2 but without the body, attachments, or MIME-
// derived headers (in_reply_to, references). List endpoints avoid the
// per-row body fetch + MIME parse that the detail endpoint pays.
type APIMessageSummaryV2 struct {
	ID              int64
	RFC822MessageID string
	SourceMessageID string
	ThreadID        int64
	AccountEmail    string
	MessageType     string
	Subject         string
	Snippet         string
	From            *APIAddress
	To              []APIAddress
	Cc              []APIAddress
	SentAt          time.Time
	ReceivedAt      time.Time
	Labels          []string
	HasAttachments  bool
	AttachmentCount int
	SizeBytes       int64
	DeletedAt       *time.Time
}

// APIRecipientsV2 holds the structured participants for a single
// message — produced by BatchStructuredRecipients so list endpoints
// can hydrate many messages in one query.
type APIRecipientsV2 struct {
	From *APIAddress
	To   []APIAddress
	Cc   []APIAddress
	Bcc  []APIAddress
}

// APIAttachmentV2 is the per-message attachment summary used by the v2
// message endpoint. Carries the attachment id so consumers can deep
// link to /attachment/{id} or /api/v1/attachments/{id}/content.
type APIAttachmentV2 struct {
	ID          int64
	Filename    string
	MimeType    string
	SizeBytes   int64
	ContentHash string
}

// APIMessageV2 is the v2 message-detail shape. Adds the headers and
// IDs review apps actually care about (rfc822, in-reply-to,
// references, structured participants, thread id, account, attachment
// ids) that v1's APIMessage flattened away.
//
// Source mix: structured participants come from message_recipients +
// participants; in_reply_to / references / reply_to come from
// re-parsing the raw MIME blob via internal/mime; everything else is
// already on the messages / sources / labels tables.
type APIMessageV2 struct {
	ID              int64
	RFC822MessageID string
	SourceMessageID string
	ThreadID        int64
	AccountEmail    string
	MessageType     string
	Subject         string
	Snippet         string
	From            *APIAddress
	To              []APIAddress
	Cc              []APIAddress
	Bcc             []APIAddress
	ReplyTo         []APIAddress
	InReplyTo       string
	References      []string
	SentAt          time.Time
	ReceivedAt      time.Time
	Labels          []string
	HasAttachments  bool
	AttachmentCount int
	SizeBytes       int64
	DeletedAt       *time.Time
	BodyText        string
	BodyHTML        string
	Attachments     []APIAttachmentV2
}

// APICorpusFingerprint is the cheap "did the corpus change" digest
// returned by /api/v1/corpus/fingerprint.
//
// Recipe: sha256("<message_count>|<latest_sent_at_unix>|<max_id>").
// Stable as long as no compaction renumbers ids; changes on every
// sync. Useful for detecting that *something* changed about the corpus
// since an artifact was prepared. NOT a per-citation hash — for
// content-stable references see [[per-message-content-hash]] (declined
// for now; see PLAN-radical-roc-wishlist.md C1).
type APICorpusFingerprint struct {
	Fingerprint         string
	AsOf                time.Time
	MessageCount        int64
	LatestMessageSentAt time.Time
}

// APILabelCount is one row of the /api/v1/labels listing: a label name
// and the number of live messages bearing it. Aggregated across all
// sources — Gmail's INBOX, IMPORTANT, etc. collapse across accounts.
type APILabelCount struct {
	Name  string
	Count int64
}

// APIParticipant is the per-id participant detail used by
// /api/v1/participants/{id}. FirstSeen / LastSeen are zero when the
// participant exists in the participants table but has no recipient
// rows (rare; usually orphaned imports).
type APIParticipant struct {
	ID           int64
	Name         string
	Address      string // email when present, falls back to phone
	Domain       string
	MessageCount int64
	FirstSeen    time.Time
	LastSeen     time.Time
}

// APIAttachmentDetailV2 is the standalone attachment shape returned
// by /api/v2/attachments/{id}. Adds thread_id and account so a
// consumer that deep-linked to an attachment can recover the parent
// thread/account without another round-trip.
type APIAttachmentDetailV2 struct {
	ID           int64
	MessageID    int64
	ThreadID     int64
	AccountEmail string
	Filename     string
	MimeType     string
	SizeBytes    int64
	ContentHash  string
	StoragePath  string // path relative to the attachments dir
}

// APIParticipantV2 is the v2 participant shape. Same identity +
// aggregate fields as v1 plus is_user_account so review apps can
// distinguish a synced account holder from an external party
// (renders "you" vs "them" correctly).
type APIParticipantV2 struct {
	ID            int64
	Name          string
	Address       string
	Domain        string
	MessageCount  int64
	FirstSeen     time.Time
	LastSeen      time.Time
	IsUserAccount bool
}

// APIAttachmentDetail is the standalone attachment shape used by
// /api/v1/attachments/{id}. It carries everything the API server needs
// to locate, label, and serve the on-disk file. Distinct from
// APIAttachment (the per-message embedded summary) so message
// responses stay lean.
type APIAttachmentDetail struct {
	ID          int64
	MessageID   int64
	Filename    string
	MimeType    string
	Size        int64
	ContentHash string
	StoragePath string // path relative to the attachments dir: <ab>/<hash>
}

// APIThread represents a conversation/thread for API responses. Messages
// are ordered by sent_at ascending so review apps can render them
// top-to-bottom in chronological order.
type APIThread struct {
	ID           int64
	Subject      string
	MessageCount int64
	Participants []APIThreadParticipant
	Messages     []APIThreadMessage
}

// APIThreadParticipant identifies one party in a thread. Name may be empty
// for participants we only ever saw as a bare address.
type APIThreadParticipant struct {
	ID      int64
	Name    string
	Address string
}

// APIThreadMessage is the compact per-message summary used inside a thread
// response. Full bodies are fetched via /api/v1/messages/{id} or
// /api/v1/messages/{id}/body.
type APIThreadMessage struct {
	ID       int64
	SentAt   time.Time
	FromName string
	From     string
	Snippet  string
}

// ListMessages returns a paginated list of messages with batch-loaded recipients and labels.
func (s *Store) ListMessages(offset, limit int) ([]APIMessage, int64, error) {
	// Get total count. Use the canonical live-messages predicate so
	// source-deleted rows are excluded.
	var total int64
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM messages WHERE " + LiveMessagesWhere("", true),
	).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// Query messages with sender info
	query := fmt.Sprintf(`
		SELECT
			m.id,
			COALESCE(m.conversation_id, 0) as conversation_id,
			COALESCE(m.subject, '') as subject,
			COALESCE(p.email_address, '') as from_email,
			COALESCE(m.sent_at, m.received_at, m.internal_date) as sent_at,
			COALESCE(m.snippet, '') as snippet,
			m.has_attachments,
			m.size_estimate
		FROM messages m
		LEFT JOIN message_recipients mr ON mr.message_id = m.id AND mr.recipient_type = 'from'
		LEFT JOIN participants p ON p.id = mr.participant_id
		WHERE %s
		ORDER BY COALESCE(m.sent_at, m.received_at, m.internal_date) DESC
		LIMIT ? OFFSET ?
	`, LiveMessagesWhere("m", true))

	rows, err := s.db.Query(query, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	// Use scanMessageRows for robust date parsing
	messages, ids, err := scanMessageRows(rows)
	if err != nil {
		return nil, 0, err
	}

	if len(ids) == 0 {
		return messages, total, nil
	}

	// Batch-load recipients and labels for all messages
	if err := s.batchPopulate(messages, ids); err != nil {
		return nil, 0, err
	}

	return messages, total, nil
}

// GetMessage returns a single message with full details.
// Only this method accesses message_bodies (single PK lookup).
func (s *Store) GetMessage(id int64) (*APIMessage, error) {
	query := `
		SELECT
			m.id,
			COALESCE(m.conversation_id, 0) as conversation_id,
			COALESCE(m.subject, '') as subject,
			COALESCE(p.email_address, '') as from_email,
			COALESCE(m.sent_at, m.received_at, m.internal_date) as sent_at,
			COALESCE(m.snippet, '') as snippet,
			m.has_attachments,
			m.size_estimate,
			m.deleted_from_source_at
		FROM messages m
		LEFT JOIN message_recipients mr ON mr.message_id = m.id AND mr.recipient_type = 'from'
		LEFT JOIN participants p ON p.id = mr.participant_id
		WHERE m.id = ?
	`

	var m APIMessage
	var sentAtStr sql.NullString
	var deletedAtStr sql.NullString
	err := s.db.QueryRow(query, id).Scan(&m.ID, &m.ConversationID, &m.Subject, &m.From, &sentAtStr, &m.Snippet, &m.HasAttachments, &m.SizeEstimate, &deletedAtStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if sentAtStr.Valid && sentAtStr.String != "" {
		m.SentAt = parseSQLiteTime(sentAtStr.String)
	}
	if deletedAtStr.Valid && deletedAtStr.String != "" {
		deletedAt := parseSQLiteTime(deletedAtStr.String)
		m.DeletedAt = &deletedAt
	}

	// Get recipients (single message, per-row is fine)
	m.To, err = s.getRecipients(m.ID, "to")
	if err != nil {
		return nil, err
	}
	m.Cc, err = s.getRecipients(m.ID, "cc")
	if err != nil {
		return nil, err
	}
	m.Bcc, err = s.getRecipients(m.ID, "bcc")
	if err != nil {
		return nil, err
	}

	// Get labels (single message, per-row is fine)
	m.Labels, err = s.getLabels(m.ID)
	if err != nil {
		return nil, err
	}

	// Get body (single PK lookup — only place we touch message_bodies)
	var bodyText, bodyHTML sql.NullString
	err = s.db.QueryRow("SELECT body_text, body_html FROM message_bodies WHERE message_id = ?", id).Scan(&bodyText, &bodyHTML)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("get message body: %w", err)
	}
	if bodyText.Valid {
		m.Body = bodyText.String
	} else if bodyHTML.Valid {
		m.Body = bodyHTML.String
	}

	// Get attachments
	attRows, err := s.db.Query("SELECT filename, mime_type, size FROM attachments WHERE message_id = ?", id)
	if err == nil {
		defer func() { _ = attRows.Close() }()
		for attRows.Next() {
			var att APIAttachment
			if err := attRows.Scan(&att.Filename, &att.MimeType, &att.Size); err == nil {
				m.Attachments = append(m.Attachments, att)
			}
		}
	}

	m.Headers = make(map[string]string)

	return &m, nil
}

// GetCorpusFingerprint computes the cheap drift-detection digest in a
// single round-trip and returns the inputs alongside it so callers can
// surface useful "as-of" context.
//
// Implementation notes:
//   - COUNT, MAX(sent_at), MAX(id) are derived in one SELECT to keep
//     this endpoint sub-millisecond on multi-million-row corpora.
//   - Live messages only (source-deleted rows excluded) so the
//     fingerprint matches what the rest of the read API exposes.
//   - latest_sent_at is encoded as a Unix second to keep the digest
//     stable across timezone-string normalization.
func (s *Store) GetCorpusFingerprint() (*APICorpusFingerprint, error) {
	live := LiveMessagesWhere("", true)

	var (
		count       int64
		maxID       sql.NullInt64
		latestSent  sql.NullString
		fingerprint string
	)
	err := s.db.QueryRow(fmt.Sprintf(`
		SELECT
			COUNT(*),
			MAX(id),
			MAX(COALESCE(sent_at, received_at, internal_date))
		  FROM messages WHERE %s`, live),
	).Scan(&count, &maxID, &latestSent)
	if err != nil {
		return nil, fmt.Errorf("corpus fingerprint: %w", err)
	}

	fp := &APICorpusFingerprint{
		AsOf:         time.Now().UTC(),
		MessageCount: count,
	}
	if latestSent.Valid && latestSent.String != "" {
		fp.LatestMessageSentAt = parseSQLiteTime(latestSent.String)
	}

	// sha256("<count>|<latest_sent_unix>|<max_id>") — stable inputs only.
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d|%d|%d", count, fp.LatestMessageSentAt.Unix(), maxID.Int64)
	fingerprint = "sha256:" + hex.EncodeToString(h.Sum(nil))
	fp.Fingerprint = fingerprint
	return fp, nil
}

// ListLabels returns label names with their live-message counts,
// aggregated across all sources. Same-named labels from different
// sources collapse to one row.
func (s *Store) ListLabels() ([]APILabelCount, error) {
	query := fmt.Sprintf(`
		SELECT l.name, COUNT(ml.message_id) AS cnt
		  FROM labels l
		  JOIN message_labels ml ON ml.label_id = l.id
		  JOIN messages m ON m.id = ml.message_id
		 WHERE %s
		 GROUP BY l.name
		 ORDER BY cnt DESC, l.name ASC
	`, LiveMessagesWhere("m", true))

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []APILabelCount
	for rows.Next() {
		var lc APILabelCount
		if err := rows.Scan(&lc.Name, &lc.Count); err != nil {
			return nil, fmt.Errorf("scan label row: %w", err)
		}
		out = append(out, lc)
	}
	return out, rows.Err()
}

// ListMessagesByLabel returns a paginated list of live messages tagged
// with the given label name. Sort order matches ListMessages: newest
// first by sent_at.
func (s *Store) ListMessagesByLabel(name string, offset, limit int) ([]APIMessage, int64, error) {
	live := LiveMessagesWhere("m", true)

	var total int64
	err := s.db.QueryRow(fmt.Sprintf(`
		SELECT COUNT(*)
		  FROM messages m
		 WHERE EXISTS (
		     SELECT 1 FROM message_labels ml
		       JOIN labels l ON l.id = ml.label_id
		      WHERE ml.message_id = m.id AND l.name = ?
		 ) AND %s`, live), name).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count by label: %w", err)
	}

	query := fmt.Sprintf(`
		SELECT
			m.id,
			COALESCE(m.conversation_id, 0),
			COALESCE(m.subject, ''),
			COALESCE(p.email_address, ''),
			COALESCE(m.sent_at, m.received_at, m.internal_date),
			COALESCE(m.snippet, ''),
			m.has_attachments,
			m.size_estimate
		  FROM messages m
		  LEFT JOIN message_recipients mr ON mr.message_id = m.id AND mr.recipient_type = 'from'
		  LEFT JOIN participants p ON p.id = mr.participant_id
		 WHERE EXISTS (
		     SELECT 1 FROM message_labels ml
		       JOIN labels l ON l.id = ml.label_id
		      WHERE ml.message_id = m.id AND l.name = ?
		 ) AND %s
		 ORDER BY COALESCE(m.sent_at, m.received_at, m.internal_date) DESC
		 LIMIT ? OFFSET ?
	`, live)

	rows, err := s.db.Query(query, name, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list by label: %w", err)
	}
	defer func() { _ = rows.Close() }()

	messages, ids, err := scanMessageRows(rows)
	if err != nil {
		return nil, 0, err
	}
	if len(ids) > 0 {
		if err := s.batchPopulate(messages, ids); err != nil {
			return nil, 0, err
		}
	}
	return messages, total, nil
}

// GetParticipantByID resolves a participant id to its display profile
// (name, address, domain) plus aggregate stats (message count and
// first/last seen). Returns nil with no error when the id is unknown.
func (s *Store) GetParticipantByID(id int64) (*APIParticipant, error) {
	var p APIParticipant
	var name, email, phone, domain sql.NullString
	err := s.db.QueryRow(
		`SELECT id, COALESCE(display_name, ''), COALESCE(email_address, ''),
		        COALESCE(phone_number, ''), COALESCE(domain, '')
		   FROM participants WHERE id = ?`,
		id,
	).Scan(&p.ID, &name, &email, &phone, &domain)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get participant: %w", err)
	}
	p.Name = name.String
	p.Address = email.String
	if p.Address == "" {
		p.Address = phone.String
	}
	p.Domain = domain.String

	var firstSeen, lastSeen sql.NullString
	err = s.db.QueryRow(fmt.Sprintf(`
		SELECT COUNT(*),
		       MIN(COALESCE(m.sent_at, m.received_at, m.internal_date)),
		       MAX(COALESCE(m.sent_at, m.received_at, m.internal_date))
		  FROM messages m
		 WHERE EXISTS (
		     SELECT 1 FROM message_recipients mr
		      WHERE mr.message_id = m.id AND mr.participant_id = ?
		 ) AND %s`, LiveMessagesWhere("m", true)),
		id,
	).Scan(&p.MessageCount, &firstSeen, &lastSeen)
	if err != nil {
		return nil, fmt.Errorf("participant aggregates: %w", err)
	}
	if firstSeen.Valid && firstSeen.String != "" {
		p.FirstSeen = parseSQLiteTime(firstSeen.String)
	}
	if lastSeen.Valid && lastSeen.String != "" {
		p.LastSeen = parseSQLiteTime(lastSeen.String)
	}
	return &p, nil
}

// ListMessagesByParticipant returns a paginated list of live messages
// involving the given participant in any recipient_type (from, to, cc,
// bcc). Sort order matches ListMessages.
func (s *Store) ListMessagesByParticipant(id int64, offset, limit int) ([]APIMessage, int64, error) {
	live := LiveMessagesWhere("m", true)
	involves := `EXISTS (SELECT 1 FROM message_recipients mr WHERE mr.message_id = m.id AND mr.participant_id = ?)`

	var total int64
	err := s.db.QueryRow(
		fmt.Sprintf(`SELECT COUNT(*) FROM messages m WHERE %s AND %s`, involves, live),
		id,
	).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count by participant: %w", err)
	}

	query := fmt.Sprintf(`
		SELECT
			m.id,
			COALESCE(m.conversation_id, 0),
			COALESCE(m.subject, ''),
			COALESCE(p.email_address, ''),
			COALESCE(m.sent_at, m.received_at, m.internal_date),
			COALESCE(m.snippet, ''),
			m.has_attachments,
			m.size_estimate
		  FROM messages m
		  LEFT JOIN message_recipients mr_from ON mr_from.message_id = m.id AND mr_from.recipient_type = 'from'
		  LEFT JOIN participants p ON p.id = mr_from.participant_id
		 WHERE %s AND %s
		 ORDER BY COALESCE(m.sent_at, m.received_at, m.internal_date) DESC
		 LIMIT ? OFFSET ?
	`, involves, live)

	rows, err := s.db.Query(query, id, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list by participant: %w", err)
	}
	defer func() { _ = rows.Close() }()

	messages, ids, err := scanMessageRows(rows)
	if err != nil {
		return nil, 0, err
	}
	if len(ids) > 0 {
		if err := s.batchPopulate(messages, ids); err != nil {
			return nil, 0, err
		}
	}
	return messages, total, nil
}

// GetAttachmentByID looks up a single attachment by its primary key,
// returning the metadata plus on-disk location needed to serve the file.
// Returns nil with no error when the attachment does not exist.
func (s *Store) GetAttachmentByID(id int64) (*APIAttachmentDetail, error) {
	var d APIAttachmentDetail
	var filename, mime, hash sql.NullString
	var size sql.NullInt64
	err := s.db.QueryRow(
		`SELECT id, message_id, filename, mime_type, size, content_hash, storage_path
		   FROM attachments
		  WHERE id = ?`,
		id,
	).Scan(&d.ID, &d.MessageID, &filename, &mime, &size, &hash, &d.StoragePath)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get attachment: %w", err)
	}
	d.Filename = filename.String
	d.MimeType = mime.String
	d.Size = size.Int64
	d.ContentHash = hash.String
	return &d, nil
}

// GetAttachmentIDByHash resolves an SHA-256 content hash to the
// lowest-id attachment row that carries it, plus the total number of
// rows sharing the same hash. Returns (0, 0, nil) when no row matches.
//
// Attachment ids are per-database sqlite rowids — not portable across
// archives or stable across re-imports. The content hash is the
// stable cross-archive identifier; this lookup is the seam used by
// /api/v2/attachments/by-hash/{sha256}{,/content}. Sorting by id keeps
// the picked row deterministic (lowest id wins) so cache headers
// downstream stay consistent even when the same bytes appear on
// multiple messages.
func (s *Store) GetAttachmentIDByHash(hash string) (int64, int, error) {
	// MIN(id) is NULL when no rows match, so scan through sql.NullInt64
	// to avoid the "converting NULL to int64" error. COUNT(*) is always
	// a row, so this aggregate query returns exactly one tuple.
	var id sql.NullInt64
	var occurrences int
	err := s.db.QueryRow(
		`SELECT MIN(id), COUNT(*) FROM attachments WHERE content_hash = ?`,
		hash,
	).Scan(&id, &occurrences)
	if err != nil {
		return 0, 0, fmt.Errorf("attachment by hash: %w", err)
	}
	if !id.Valid || occurrences == 0 {
		return 0, 0, nil
	}
	return id.Int64, occurrences, nil
}

// GetThread returns the thread (conversation) with the given id, including
// its compact subject, deduplicated participants, and the per-message
// summaries needed to render a review-pane view. Returns nil with no
// error when no thread matches.
//
// Only live messages count toward MessageCount and appear in Messages;
// source-deleted rows are filtered via LiveMessagesWhere so the response
// matches what /api/v1/messages already returns.
func (s *Store) GetThread(id int64) (*APIThread, error) {
	t := &APIThread{ID: id}

	// Conversation header (subject lives in conversations.title for email).
	var title sql.NullString
	err := s.db.QueryRow(
		`SELECT COALESCE(title, '') FROM conversations WHERE id = ?`,
		id,
	).Scan(&title)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get thread header: %w", err)
	}
	t.Subject = title.String

	// Messages (live only), ordered chronologically.
	msgQuery := fmt.Sprintf(`
		SELECT
			m.id,
			COALESCE(p.display_name, '') as from_name,
			COALESCE(p.email_address, '') as from_email,
			COALESCE(m.sent_at, m.received_at, m.internal_date) as sent_at,
			COALESCE(m.snippet, '') as snippet
		FROM messages m
		LEFT JOIN message_recipients mr ON mr.message_id = m.id AND mr.recipient_type = 'from'
		LEFT JOIN participants p ON p.id = mr.participant_id
		WHERE m.conversation_id = ? AND %s
		ORDER BY sent_at ASC, m.id ASC
	`, LiveMessagesWhere("m", true))

	rows, err := s.db.Query(msgQuery, id)
	if err != nil {
		return nil, fmt.Errorf("get thread messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			tm        APIThreadMessage
			sentAtStr sql.NullString
		)
		if err := rows.Scan(&tm.ID, &tm.FromName, &tm.From, &sentAtStr, &tm.Snippet); err != nil {
			return nil, fmt.Errorf("scan thread message: %w", err)
		}
		if sentAtStr.Valid && sentAtStr.String != "" {
			tm.SentAt = parseSQLiteTime(sentAtStr.String)
		}
		t.Messages = append(t.Messages, tm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate thread messages: %w", err)
	}
	t.MessageCount = int64(len(t.Messages))

	// Participants: distinct senders + recipients across all live messages
	// in the thread. Cheaper than reading conversation_participants and
	// keeps the response self-consistent with Messages.
	partQuery := fmt.Sprintf(`
		SELECT DISTINCT p.id, COALESCE(p.display_name, ''), COALESCE(p.email_address, '')
		FROM participants p
		JOIN message_recipients mr ON mr.participant_id = p.id
		JOIN messages m ON m.id = mr.message_id
		WHERE m.conversation_id = ? AND %s
		ORDER BY p.id
	`, LiveMessagesWhere("m", true))

	prows, err := s.db.Query(partQuery, id)
	if err != nil {
		return nil, fmt.Errorf("get thread participants: %w", err)
	}
	defer func() { _ = prows.Close() }()

	for prows.Next() {
		var p APIThreadParticipant
		if err := prows.Scan(&p.ID, &p.Name, &p.Address); err != nil {
			return nil, fmt.Errorf("scan thread participant: %w", err)
		}
		t.Participants = append(t.Participants, p)
	}
	if err := prows.Err(); err != nil {
		return nil, fmt.Errorf("iterate thread participants: %w", err)
	}

	return t, nil
}

// GetMessageBodies returns the raw text and HTML body parts for a message,
// each empty if missing. Returns ("", "", nil) when the message has no
// recorded body (or doesn't exist) — callers needing to distinguish
// "no message" from "no body" should pair this with GetMessage.
//
// This is the only access path besides GetMessage that touches the
// message_bodies table; it preserves the small-B-tree invariant.
func (s *Store) GetMessageBodies(id int64) (text, html string, err error) {
	var t, h sql.NullString
	err = s.db.QueryRow(
		"SELECT body_text, body_html FROM message_bodies WHERE message_id = ?",
		id,
	).Scan(&t, &h)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("get message bodies: %w", err)
	}
	return t.String, h.String, nil
}

// GetMessagesSummariesByIDs returns summary-level (no body, no
// attachments) APIMessage rows for the supplied IDs in the same order
// as ids. Missing IDs are silently dropped — callers are expected to
// have already filtered for live messages, and a missing row in the
// summary set is just "ignore this hit". Recipients and labels are
// batch-loaded with the same shape as SearchMessages, so the worst
// case is 5 SQL round-trips regardless of len(ids). This is the
// designated hydration path for vector/hybrid search hits, where
// callers loop over many MessageIDs and never need body or
// attachments — calling GetMessage in that loop costs ~7 queries per
// hit (body + attachments + 3 recipients + labels + base) and
// dominates p50 search latency past a handful of results.
func (s *Store) GetMessagesSummariesByIDs(ids []int64) ([]APIMessage, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := fmt.Sprintf(`
		SELECT
			m.id,
			COALESCE(m.conversation_id, 0) as conversation_id,
			COALESCE(m.subject, '') as subject,
			COALESCE(p.email_address, '') as from_email,
			COALESCE(m.sent_at, m.received_at, m.internal_date) as sent_at,
			COALESCE(m.snippet, '') as snippet,
			m.has_attachments,
			m.size_estimate
		FROM messages m
		LEFT JOIN message_recipients mr ON mr.message_id = m.id AND mr.recipient_type = 'from'
		LEFT JOIN participants p ON p.id = mr.participant_id
		WHERE m.id IN (%s) AND %s
	`, strings.Join(placeholders, ","), LiveMessagesWhere("m", true))
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("get message summaries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	messages, foundIDs, err := scanMessageRows(rows)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, nil
	}
	if err := s.batchPopulate(messages, foundIDs); err != nil {
		return nil, err
	}

	// Re-order to match the caller's id order so search rank is
	// preserved end-to-end.
	indexByID := make(map[int64]int, len(messages))
	for i, m := range messages {
		indexByID[m.ID] = i
	}
	ordered := make([]APIMessage, 0, len(ids))
	for _, id := range ids {
		if idx, ok := indexByID[id]; ok {
			ordered = append(ordered, messages[idx])
		}
	}
	return ordered, nil
}

// SearchMessages searches messages using full-text search, with batch-loaded recipients and labels.
func (s *Store) SearchMessages(query string, offset, limit int) ([]APIMessage, int64, error) {
	ftsJoin, ftsWhere, ftsOrder, orderArgCount := s.dialect.FTSSearchClause()

	ftsQuery := fmt.Sprintf(`
		SELECT
			m.id,
			COALESCE(m.conversation_id, 0) as conversation_id,
			COALESCE(m.subject, '') as subject,
			COALESCE(p.email_address, '') as from_email,
			COALESCE(m.sent_at, m.received_at, m.internal_date) as sent_at,
			COALESCE(m.snippet, '') as snippet,
			m.has_attachments,
			m.size_estimate
		FROM messages m
		%s
		LEFT JOIN message_recipients mr ON mr.message_id = m.id AND mr.recipient_type = 'from'
		LEFT JOIN participants p ON p.id = mr.participant_id
		WHERE %s AND %s
		ORDER BY %s
		LIMIT ? OFFSET ?
	`, ftsJoin, ftsWhere, LiveMessagesWhere("m", true), ftsOrder)

	// Bind the search term once for WHERE, plus orderArgCount more times
	// for any ? placeholders the dialect put in the order-by fragment.
	searchArgs := make([]interface{}, 0, 3+orderArgCount)
	searchArgs = append(searchArgs, query)
	for i := 0; i < orderArgCount; i++ {
		searchArgs = append(searchArgs, query)
	}
	searchArgs = append(searchArgs, limit, offset)

	rows, err := s.db.Query(ftsQuery, searchArgs...)
	if err != nil {
		// FTS might not be available, fall back to LIKE search
		return s.searchMessagesLike(query, offset, limit)
	}
	defer func() { _ = rows.Close() }()

	messages, ids, err := scanMessageRows(rows)
	if err != nil {
		return nil, 0, err
	}

	if len(ids) == 0 {
		return []APIMessage{}, 0, nil
	}

	// Get total count
	var total int64
	countQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM messages m
		%s
		WHERE %s AND %s
	`, ftsJoin, ftsWhere, LiveMessagesWhere("m", true))
	if err := s.db.QueryRow(countQuery, query).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count FTS results: %w", err)
	}

	// Batch-load recipients and labels
	if err := s.batchPopulate(messages, ids); err != nil {
		return nil, 0, err
	}

	return messages, total, nil
}

// SearchMessagesQuery searches messages using a parsed query with
// support for structured operators (from:, to:, label:, etc.).
func (s *Store) SearchMessagesQuery(
	q *search.Query, offset, limit int,
) ([]APIMessage, int64, error) {
	var conditions []string
	var args []interface{}

	conditions = append(conditions, LiveMessagesWhere("m", true))

	// FTS text terms. ftsEnabled is the authoritative signal that FTS is
	// active — ftsJoin may be empty on dialects (e.g. PostgreSQL) whose
	// tsvector lives on the main table and needs no extra join.
	ftsEnabled := len(q.TextTerms) > 0
	var ftsJoin, ftsOrder, ftsExpr string
	var ftsOrderArgCount int
	if ftsEnabled {
		ftsExpr = buildFTSExpression(q.TextTerms)
		join, where, orderBy, orderArgCount := s.dialect.FTSSearchClause()
		ftsJoin = join
		ftsOrder = orderBy
		ftsOrderArgCount = orderArgCount
		conditions = append(conditions, where)
		args = append(args, ftsExpr)
	}

	// from: filter
	for _, addr := range q.FromAddrs {
		conditions = append(conditions, `EXISTS (
			SELECT 1 FROM message_recipients mr2
			JOIN participants p2 ON p2.id = mr2.participant_id
			WHERE mr2.message_id = m.id
			AND mr2.recipient_type = 'from'
			AND LOWER(p2.email_address) LIKE ? ESCAPE '\'
		)`)
		args = append(args,
			"%"+escapeLike(strings.ToLower(addr))+"%")
	}

	// to: filter
	for _, addr := range q.ToAddrs {
		conditions = append(conditions, `EXISTS (
			SELECT 1 FROM message_recipients mr2
			JOIN participants p2 ON p2.id = mr2.participant_id
			WHERE mr2.message_id = m.id
			AND mr2.recipient_type = 'to'
			AND LOWER(p2.email_address) LIKE ? ESCAPE '\'
		)`)
		args = append(args,
			"%"+escapeLike(strings.ToLower(addr))+"%")
	}

	// cc: filter
	for _, addr := range q.CcAddrs {
		conditions = append(conditions, `EXISTS (
			SELECT 1 FROM message_recipients mr2
			JOIN participants p2 ON p2.id = mr2.participant_id
			WHERE mr2.message_id = m.id
			AND mr2.recipient_type = 'cc'
			AND LOWER(p2.email_address) LIKE ? ESCAPE '\'
		)`)
		args = append(args,
			"%"+escapeLike(strings.ToLower(addr))+"%")
	}

	// bcc: filter
	for _, addr := range q.BccAddrs {
		conditions = append(conditions, `EXISTS (
			SELECT 1 FROM message_recipients mr2
			JOIN participants p2 ON p2.id = mr2.participant_id
			WHERE mr2.message_id = m.id
			AND mr2.recipient_type = 'bcc'
			AND LOWER(p2.email_address) LIKE ? ESCAPE '\'
		)`)
		args = append(args,
			"%"+escapeLike(strings.ToLower(addr))+"%")
	}

	// label: filter
	for _, lbl := range q.Labels {
		conditions = append(conditions, `EXISTS (
			SELECT 1 FROM message_labels ml2
			JOIN labels l2 ON l2.id = ml2.label_id
			WHERE ml2.message_id = m.id
			AND LOWER(l2.name) LIKE ? ESCAPE '\'
		)`)
		args = append(args,
			"%"+escapeLike(strings.ToLower(lbl))+"%")
	}

	// subject: filter
	for _, term := range q.SubjectTerms {
		conditions = append(conditions,
			`m.subject LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(term)+"%")
	}

	// has:attachment
	if q.HasAttachment != nil && *q.HasAttachment {
		conditions = append(conditions,
			"m.has_attachments = 1")
	}

	// larger: / smaller:
	if q.LargerThan != nil {
		conditions = append(conditions, "m.size_estimate > ?")
		args = append(args, *q.LargerThan)
	}
	if q.SmallerThan != nil {
		conditions = append(conditions, "m.size_estimate < ?")
		args = append(args, *q.SmallerThan)
	}

	// after: / before:
	if q.AfterDate != nil {
		conditions = append(conditions,
			"COALESCE(m.sent_at, m.received_at, m.internal_date) >= ?")
		args = append(args, q.AfterDate.Format(time.RFC3339))
	}
	if q.BeforeDate != nil {
		conditions = append(conditions,
			"COALESCE(m.sent_at, m.received_at, m.internal_date) < ?")
		args = append(args, q.BeforeDate.Format(time.RFC3339))
	}

	whereClause := strings.Join(conditions, " AND ")

	// Count query.
	countSQL := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM messages m
		%s
		WHERE %s
	`, ftsJoin, whereClause)

	var total int64
	if err := s.db.QueryRow(countSQL, args...).Scan(&total); err != nil {
		if ftsEnabled {
			return s.searchMessagesQueryNoFTS(q, offset, limit)
		}
		return nil, 0, fmt.Errorf("count search results: %w", err)
	}

	// Results query.
	orderBy := "COALESCE(m.sent_at, m.received_at, m.internal_date) DESC"
	if ftsEnabled {
		orderBy = ftsOrder + ", " + orderBy
	}
	searchSQL := fmt.Sprintf(`
		SELECT
			m.id,
			COALESCE(m.conversation_id, 0) as conversation_id,
			COALESCE(m.subject, '') as subject,
			COALESCE(p.email_address, '') as from_email,
			COALESCE(m.sent_at, m.received_at, m.internal_date) as sent_at,
			COALESCE(m.snippet, '') as snippet,
			m.has_attachments,
			m.size_estimate
		FROM messages m
		%s
		LEFT JOIN message_recipients mr
			ON mr.message_id = m.id AND mr.recipient_type = 'from'
		LEFT JOIN participants p ON p.id = mr.participant_id
		WHERE %s
		ORDER BY %s
		LIMIT ? OFFSET ?
	`, ftsJoin, whereClause, orderBy)

	// If the dialect's order-by fragment has ? placeholders, bind the FTS
	// expression that many extra times — right after the WHERE args and
	// before LIMIT/OFFSET so Rebind assigns them the correct positions.
	resultArgs := make([]interface{}, 0, len(args)+ftsOrderArgCount+2)
	resultArgs = append(resultArgs, args...)
	for i := 0; i < ftsOrderArgCount; i++ {
		resultArgs = append(resultArgs, ftsExpr)
	}
	resultArgs = append(resultArgs, limit, offset)
	rows, err := s.db.Query(searchSQL, resultArgs...)
	if err != nil {
		// FTS5 not available -- fall back if we used it.
		if ftsEnabled {
			return s.searchMessagesQueryNoFTS(q, offset, limit)
		}
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	messages, ids, err := scanMessageRows(rows)
	if err != nil {
		return nil, 0, err
	}

	if len(ids) > 0 {
		if err := s.batchPopulate(messages, ids); err != nil {
			return nil, 0, err
		}
	}

	return messages, total, nil
}

// buildFTSExpression builds an FTS5 MATCH expression from text terms.
func buildFTSExpression(terms []string) string {
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " AND ")
}

// searchMessagesQueryNoFTS is a fallback when FTS5 is unavailable.
func (s *Store) searchMessagesQueryNoFTS(
	q *search.Query, offset, limit int,
) ([]APIMessage, int64, error) {
	fallbackQ := *q
	fallbackQ.SubjectTerms = append(fallbackQ.SubjectTerms, q.TextTerms...)
	fallbackQ.TextTerms = nil
	return s.SearchMessagesQuery(&fallbackQ, offset, limit)
}

// escapeLike escapes SQL LIKE special characters (%, _) so they are
// matched literally. The escaped string should be used with ESCAPE '\'.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// searchMessagesLike is a fallback search using LIKE with batch-loaded recipients and labels.
func (s *Store) searchMessagesLike(query string, offset, limit int) ([]APIMessage, int64, error) {
	likePattern := "%" + escapeLike(query) + "%"

	countQuery := fmt.Sprintf(`
		SELECT COUNT(*) FROM messages
		WHERE %s
		AND (subject LIKE ? ESCAPE '\' OR snippet LIKE ? ESCAPE '\')
	`, LiveMessagesWhere("", true))
	var total int64
	if err := s.db.QueryRow(countQuery, likePattern, likePattern).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count search results: %w", err)
	}

	searchQuery := fmt.Sprintf(`
		SELECT
			m.id,
			COALESCE(m.conversation_id, 0) as conversation_id,
			COALESCE(m.subject, '') as subject,
			COALESCE(p.email_address, '') as from_email,
			COALESCE(m.sent_at, m.received_at, m.internal_date) as sent_at,
			COALESCE(m.snippet, '') as snippet,
			m.has_attachments,
			m.size_estimate
		FROM messages m
		LEFT JOIN message_recipients mr ON mr.message_id = m.id AND mr.recipient_type = 'from'
		LEFT JOIN participants p ON p.id = mr.participant_id
		WHERE %s
		AND (m.subject LIKE ? ESCAPE '\' OR m.snippet LIKE ? ESCAPE '\')
		ORDER BY COALESCE(m.sent_at, m.received_at, m.internal_date) DESC
		LIMIT ? OFFSET ?
	`, LiveMessagesWhere("m", true))

	rows, err := s.db.Query(searchQuery, likePattern, likePattern, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	messages, ids, err := scanMessageRows(rows)
	if err != nil {
		return nil, 0, err
	}

	if len(ids) == 0 {
		return messages, total, nil
	}

	// Batch-load recipients and labels
	if err := s.batchPopulate(messages, ids); err != nil {
		return nil, 0, err
	}

	return messages, total, nil
}

// scanMessageRows scans the standard 8-column message row set.
// Uses string scanning for dates to handle all SQLite datetime formats robustly.
func scanMessageRows(rows *loggedRows) ([]APIMessage, []int64, error) {
	var messages []APIMessage
	var ids []int64
	for rows.Next() {
		var m APIMessage
		var sentAtStr sql.NullString
		err := rows.Scan(&m.ID, &m.ConversationID, &m.Subject, &m.From, &sentAtStr, &m.Snippet, &m.HasAttachments, &m.SizeEstimate)
		if err != nil {
			return nil, nil, err
		}
		if sentAtStr.Valid && sentAtStr.String != "" {
			m.SentAt = parseSQLiteTime(sentAtStr.String)
		}
		messages = append(messages, m)
		ids = append(ids, m.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate messages: %w", err)
	}
	return messages, ids, nil
}

// parseSQLiteTime parses a datetime string from SQLite into time.Time.
// Uses the same comprehensive format list as dbTimeLayouts in sync.go.
func parseSQLiteTime(s string) time.Time {
	// Same formats as dbTimeLayouts - order matters: more specific first
	layouts := []string{
		"2006-01-02 15:04:05.999999999-07:00", // space-separated with fractional seconds and TZ
		"2006-01-02T15:04:05.999999999-07:00", // T-separated with fractional seconds and TZ
		"2006-01-02 15:04:05.999999999",       // space-separated with fractional seconds
		"2006-01-02T15:04:05.999999999",       // T-separated with fractional seconds
		"2006-01-02 15:04:05",                 // SQLite datetime('now') format
		"2006-01-02T15:04:05",                 // T-separated basic
		"2006-01-02 15:04",                    // space-separated without seconds
		"2006-01-02T15:04",                    // T-separated without seconds
		"2006-01-02",                          // date only
		time.RFC3339,                          // e.g., "2006-01-02T15:04:05Z"
		time.RFC3339Nano,                      // e.g., "2006-01-02T15:04:05.999999999Z07:00"
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// batchPopulate batch-loads recipients and labels for a slice of messages.
func (s *Store) batchPopulate(messages []APIMessage, ids []int64) error {
	recipientMap, err := s.batchGetRecipients(ids, "to")
	if err != nil {
		return err
	}
	ccMap, err := s.batchGetRecipients(ids, "cc")
	if err != nil {
		return err
	}
	bccMap, err := s.batchGetRecipients(ids, "bcc")
	if err != nil {
		return err
	}
	labelMap, err := s.batchGetLabels(ids)
	if err != nil {
		return err
	}
	for i := range messages {
		messages[i].To = recipientMap[messages[i].ID]
		messages[i].Cc = ccMap[messages[i].ID]
		messages[i].Bcc = bccMap[messages[i].ID]
		messages[i].Labels = labelMap[messages[i].ID]
	}
	return nil
}

// batchGetRecipients loads recipients for multiple messages in a single query.
func (s *Store) batchGetRecipients(messageIDs []int64, recipientType string) (map[int64][]string, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(messageIDs))
	args := make([]interface{}, 0, len(messageIDs)+1)
	for i, id := range messageIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	args = append(args, recipientType)

	query := fmt.Sprintf(`
		SELECT mr.message_id, COALESCE(p.email_address, '')
		FROM message_recipients mr
		JOIN participants p ON p.id = mr.participant_id
		WHERE mr.message_id IN (%s) AND mr.recipient_type = ?
	`, strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("batch get recipients: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make(map[int64][]string, len(messageIDs))
	for rows.Next() {
		var msgID int64
		var email string
		if err := rows.Scan(&msgID, &email); err != nil {
			return nil, fmt.Errorf("scan recipient: %w", err)
		}
		if email != "" {
			result[msgID] = append(result[msgID], email)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recipients: %w", err)
	}
	return result, nil
}

// batchGetLabels loads labels for multiple messages in a single query.
func (s *Store) batchGetLabels(messageIDs []int64) (map[int64][]string, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(messageIDs))
	args := make([]interface{}, 0, len(messageIDs))
	for i, id := range messageIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	query := fmt.Sprintf(`
		SELECT ml.message_id, l.name
		FROM message_labels ml
		JOIN labels l ON l.id = ml.label_id
		WHERE ml.message_id IN (%s)
	`, strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("batch get labels: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make(map[int64][]string, len(messageIDs))
	for rows.Next() {
		var msgID int64
		var name string
		if err := rows.Scan(&msgID, &name); err != nil {
			return nil, fmt.Errorf("scan label: %w", err)
		}
		result[msgID] = append(result[msgID], name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate labels: %w", err)
	}
	return result, nil
}

// Single-message helpers (still used by GetMessage for single PK lookups)

func (s *Store) getRecipients(messageID int64, recipientType string) ([]string, error) {
	query := `
		SELECT COALESCE(p.email_address, '')
		FROM message_recipients mr
		JOIN participants p ON p.id = mr.participant_id
		WHERE mr.message_id = ? AND mr.recipient_type = ?
	`
	rows, err := s.db.Query(query, messageID, recipientType)
	if err != nil {
		return nil, fmt.Errorf("get recipients: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var recipients []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, fmt.Errorf("scan recipient: %w", err)
		}
		if email != "" {
			recipients = append(recipients, email)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recipients: %w", err)
	}
	return recipients, nil
}

func (s *Store) getLabels(messageID int64) ([]string, error) {
	query := `
		SELECT l.name
		FROM message_labels ml
		JOIN labels l ON l.id = ml.label_id
		WHERE ml.message_id = ?
	`
	rows, err := s.db.Query(query, messageID)
	if err != nil {
		return nil, fmt.Errorf("get labels: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var labels []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan label: %w", err)
		}
		labels = append(labels, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate labels: %w", err)
	}
	return labels, nil
}
