package v2

import (
	"log/slog"
	"os"

	"github.com/wesm/msgvault/internal/search"
	"github.com/wesm/msgvault/internal/store"
)

// testLogger returns a logger for tests that discards output below
// Error so test runs stay quiet.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// mockStore implements v2.Store for the v2 handler tests. It is a
// trimmed equivalent of internal/api's test mock — only the methods
// the v2 surface actually calls are present.
type mockStore struct {
	// messagesV2 is keyed by message id and drives /api/v2/messages/{id}
	// and /api/v2/messages/by-rfc822-id/{rfc822_id}.
	messagesV2 map[int64]*store.APIMessageV2

	// rfc822Index maps an RFC822 Message-ID string to the message id
	// it resolves to under by-rfc822-id lookups.
	rfc822Index map[string]int64

	// messages and total back ListMessages and the search/list
	// derivation paths used by paginate.
	messages []store.APIMessage
	total    int64

	// labelMembership and participantMembership are id-lists keyed by
	// label name / participant id.
	labelMembership       map[string][]int64
	participantMembership map[int64][]int64

	// structuredRecipients drives BatchStructuredRecipients.
	structuredRecipients map[int64]*store.APIRecipientsV2

	// messageMetaV2 drives BatchMessageMetaV2.
	messageMetaV2 map[int64]*store.APIMessageMetaV2

	// threads drives GetThread.
	threads map[int64]*store.APIThread

	// attachmentsV2 and participantsV2 drive the v2 attachment- and
	// participant-detail endpoints.
	attachmentsV2  map[int64]*store.APIAttachmentDetailV2
	participantsV2 map[int64]*store.APIParticipantV2
}

func (m *mockStore) GetMessageV2(id int64) (*store.APIMessageV2, error) {
	if m.messagesV2 == nil {
		return nil, nil
	}
	v, ok := m.messagesV2[id]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (m *mockStore) GetMessageV2ByRFC822ID(rfc822ID string) (*store.APIMessageV2, error) {
	id, ok := m.rfc822Index[rfc822ID]
	if !ok {
		return nil, nil
	}
	return m.GetMessageV2(id)
}

func (m *mockStore) ListMessages(offset, limit int) ([]store.APIMessage, int64, error) {
	return m.messages, m.total, nil
}

func (m *mockStore) ListMessagesByLabel(name string, offset, limit int) ([]store.APIMessage, int64, error) {
	return m.subsetMessages(m.labelMembership[name], offset, limit)
}

func (m *mockStore) ListMessagesByParticipant(id int64, offset, limit int) ([]store.APIMessage, int64, error) {
	return m.subsetMessages(m.participantMembership[id], offset, limit)
}

func (m *mockStore) GetThread(id int64) (*store.APIThread, error) {
	if m.threads == nil {
		return nil, nil
	}
	t, ok := m.threads[id]
	if !ok {
		return nil, nil
	}
	return t, nil
}

func (m *mockStore) GetMessagesSummariesByIDs(ids []int64) ([]store.APIMessage, error) {
	byID := make(map[int64]store.APIMessage, len(m.messages))
	for _, msg := range m.messages {
		byID[msg.ID] = msg
	}
	out := make([]store.APIMessage, 0, len(ids))
	for _, id := range ids {
		if msg, ok := byID[id]; ok {
			out = append(out, msg)
		}
	}
	return out, nil
}

func (m *mockStore) SearchMessages(query string, offset, limit int) ([]store.APIMessage, int64, error) {
	return m.messages, m.total, nil
}

func (m *mockStore) SearchMessagesQuery(q *search.Query, offset, limit int) ([]store.APIMessage, int64, error) {
	return m.messages, m.total, nil
}

func (m *mockStore) BatchStructuredRecipients(ids []int64) (map[int64]*store.APIRecipientsV2, error) {
	out := make(map[int64]*store.APIRecipientsV2, len(ids))
	if m.structuredRecipients == nil {
		return out, nil
	}
	for _, id := range ids {
		if rcp, ok := m.structuredRecipients[id]; ok {
			out[id] = rcp
		}
	}
	return out, nil
}

func (m *mockStore) BatchMessageMetaV2(ids []int64) (map[int64]*store.APIMessageMetaV2, error) {
	out := make(map[int64]*store.APIMessageMetaV2, len(ids))
	if m.messageMetaV2 == nil {
		return out, nil
	}
	for _, id := range ids {
		if meta, ok := m.messageMetaV2[id]; ok {
			out[id] = meta
		}
	}
	return out, nil
}

func (m *mockStore) GetAttachmentByIDV2(id int64) (*store.APIAttachmentDetailV2, error) {
	if m.attachmentsV2 == nil {
		return nil, nil
	}
	a, ok := m.attachmentsV2[id]
	if !ok {
		return nil, nil
	}
	return a, nil
}

func (m *mockStore) GetParticipantByIDV2(id int64) (*store.APIParticipantV2, error) {
	if m.participantsV2 == nil {
		return nil, nil
	}
	p, ok := m.participantsV2[id]
	if !ok {
		return nil, nil
	}
	return p, nil
}

// subsetMessages emulates LIMIT/OFFSET pagination against an in-memory
// id list, used by the label and participant listing tests.
func (m *mockStore) subsetMessages(ids []int64, offset, limit int) ([]store.APIMessage, int64, error) {
	byID := make(map[int64]store.APIMessage, len(m.messages))
	for _, msg := range m.messages {
		byID[msg.ID] = msg
	}
	total := int64(len(ids))
	if offset >= len(ids) {
		return nil, total, nil
	}
	end := offset + limit
	if end > len(ids) {
		end = len(ids)
	}
	out := make([]store.APIMessage, 0, end-offset)
	for _, id := range ids[offset:end] {
		if msg, ok := byID[id]; ok {
			out = append(out, msg)
		}
	}
	return out, total, nil
}
