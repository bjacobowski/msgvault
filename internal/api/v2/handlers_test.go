package v2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wesm/msgvault/internal/store"
	"github.com/wesm/msgvault/internal/vector"
	"github.com/wesm/msgvault/internal/vector/hybrid"
)

// newTestRouter mounts the v2 handler at /api/v2 with the version-header
// middleware, mirroring the production wiring in internal/api/server.go.
// Tests then issue requests against this router exactly as a real
// client would.
//
// engine is nil for tests that don't exercise the vector/hybrid search
// path; vector/hybrid handlers will return 503 vector_not_enabled in
// that case.
func newTestRouter(t *testing.T, ms *mockStore) chi.Router {
	return newTestRouterWithEngine(t, ms, nil, vector.Config{})
}

func newTestRouterWithEngine(t *testing.T, ms *mockStore, engine *hybrid.Engine, vcfg vector.Config) chi.Router {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/api/v2", func(r chi.Router) {
		r.Use(APIVersionHeader)
		NewHandler(ms, engine, vcfg, testLogger()).Register(r)
	})
	return r
}

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	got, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return got
}

func TestHandleGetMessage(t *testing.T) {
	ms := &mockStore{}
	deletedAt := mustParseTime(t, "2026-04-01T12:00:00Z")
	ms.messagesV2 = map[int64]*store.APIMessageV2{
		11134: {
			ID:              11134,
			RFC822MessageID: "<rfc-11134@example.com>",
			SourceMessageID: "src-11134",
			ThreadID:        2754,
			AccountEmail:    "user@example.com",
			MessageType:     "email",
			Subject:         "Re: hello",
			Snippet:         "first line",
			From:            &store.APIAddress{Name: "Alice", Address: "alice@example.com"},
			To:              []store.APIAddress{{Name: "Bob", Address: "bob@example.com"}},
			Cc:              []store.APIAddress{{Address: "cc@example.com"}},
			ReplyTo:         []store.APIAddress{{Address: "no-reply@example.com"}},
			InReplyTo:       "<rfc-parent@example.com>",
			References:      []string{"<rfc-root@example.com>", "<rfc-parent@example.com>"},
			SentAt:          mustParseTime(t, "2026-05-12T10:00:00Z"),
			ReceivedAt:      mustParseTime(t, "2026-05-12T10:00:05Z"),
			Labels:          []string{"INBOX"},
			HasAttachments:  true,
			AttachmentCount: 1,
			SizeBytes:       4096,
			DeletedAt:       &deletedAt,
			BodyText:        "plain body",
			BodyHTML:        "<p>html body</p>",
			Attachments: []store.APIAttachmentV2{
				{ID: 7, Filename: "report.pdf", MimeType: "application/pdf", SizeBytes: 4096, ContentHash: "abcd"},
			},
		},
	}
	r := newTestRouter(t, ms)

	t.Run("structured shape matches v2 contract", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v2/messages/11134", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		if got := w.Header().Get("X-MsgVault-API"); got != "v2" {
			t.Errorf("X-MsgVault-API = %q, want v2", got)
		}

		var resp MessageDetail
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}

		if resp.ID != 11134 || resp.ThreadID != 2754 || resp.RFC822MessageID != "<rfc-11134@example.com>" {
			t.Errorf("ids wrong: %+v", resp)
		}
		if resp.Account != "user@example.com" || resp.MessageType != "email" {
			t.Errorf("account/type wrong: %+v", resp)
		}
		if resp.From == nil || resp.From.Address != "alice@example.com" || resp.From.Name != "Alice" {
			t.Errorf("from wrong: %+v", resp.From)
		}
		if len(resp.To) != 1 || resp.To[0].Address != "bob@example.com" {
			t.Errorf("to wrong: %+v", resp.To)
		}
		if resp.InReplyTo != "<rfc-parent@example.com>" || len(resp.References) != 2 {
			t.Errorf("threading headers wrong: in_reply_to=%q references=%v",
				resp.InReplyTo, resp.References)
		}
		if !resp.IsDeleted || resp.DeletedAt != "2026-04-01T12:00:00Z" {
			t.Errorf("delete state wrong: is_deleted=%v deleted_at=%q", resp.IsDeleted, resp.DeletedAt)
		}
		if resp.Body.Text != "plain body" || resp.Body.HTML != "<p>html body</p>" {
			t.Errorf("nested body wrong: %+v", resp.Body)
		}
		if len(resp.Attachments) != 1 || resp.Attachments[0].ID != 7 {
			t.Errorf("attachments wrong: %+v", resp.Attachments)
		}
	})

	t.Run("empty collections render as [] not null", func(t *testing.T) {
		ms.messagesV2[1] = &store.APIMessageV2{ID: 1, Subject: "no recipients"}

		req := httptest.NewRequest("GET", "/api/v2/messages/1", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		body := w.Body.String()
		for _, field := range []string{`"to":[]`, `"cc":[]`, `"bcc":[]`, `"labels":[]`, `"attachments":[]`} {
			if !strings.Contains(body, field) {
				t.Errorf("expected %s in response; got: %s", field, body)
			}
		}
	})

	t.Run("404 on miss", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v2/messages/9999", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})
}

func TestHandleGetMessageByRFC822ID(t *testing.T) {
	ms := &mockStore{}
	ms.messagesV2 = map[int64]*store.APIMessageV2{
		11134: {ID: 11134, RFC822MessageID: "<x@example.com>"},
	}
	ms.rfc822Index = map[string]int64{
		"<017e01dce2ea$5bc034e0$13409ea0$@velawood.com>": 11134,
	}
	r := newTestRouter(t, ms)

	// Regression: $ in the segment must be percent-decoded — same bug
	// as the v1 endpoint, since v2 uses the same chi.URLParam pattern.
	req := httptest.NewRequest("GET",
		"/api/v2/messages/by-rfc822-id/%3C017e01dce2ea%245bc034e0%2413409ea0%24%40velawood.com%3E", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp MessageDetail
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != 11134 {
		t.Errorf("id = %d, want 11134", resp.ID)
	}
}

func TestHandleListMessages(t *testing.T) {
	ms := &mockStore{
		messages: []store.APIMessage{{ID: 1, Subject: "Hi"}},
		total:    1,
	}
	ms.structuredRecipients = map[int64]*store.APIRecipientsV2{
		1: {
			From: &store.APIAddress{Name: "Sender", Address: "sender@example.com"},
			To:   []store.APIAddress{{Address: "recipient@example.com"}},
		},
	}
	ms.messageMetaV2 = map[int64]*store.APIMessageMetaV2{
		1: {
			RFC822MessageID: "<rfc-1@example.com>",
			SourceMessageID: "src-1",
			AccountEmail:    "user@example.com",
			MessageType:     "email",
			AttachmentCount: 0,
		},
	}
	r := newTestRouter(t, ms)

	req := httptest.NewRequest("GET", "/api/v2/messages?limit=5", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-MsgVault-API"); got != "v2" {
		t.Errorf("X-MsgVault-API = %q, want v2", got)
	}

	var resp PaginatedMessages
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 || resp.Limit != 5 || len(resp.Messages) != 1 {
		t.Errorf("unexpected envelope: %+v", resp)
	}
	m := resp.Messages[0]
	if m.From == nil || m.From.Address != "sender@example.com" {
		t.Errorf("from not structured-enriched: %+v", m.From)
	}
	if len(m.To) != 1 || m.To[0].Address != "recipient@example.com" {
		t.Errorf("to not structured-enriched: %+v", m.To)
	}
	if m.RFC822MessageID != "<rfc-1@example.com>" {
		t.Errorf("rfc822 meta not enriched: %q", m.RFC822MessageID)
	}
	if m.Account != "user@example.com" {
		t.Errorf("account meta not enriched: %q", m.Account)
	}
	if m.MessageType != "email" {
		t.Errorf("message_type meta not enriched: %q", m.MessageType)
	}
}

func TestHandleListMessages_EmptyRecipientsRenderAsArrays(t *testing.T) {
	ms := &mockStore{
		messages: []store.APIMessage{{ID: 1, Subject: "Hi"}},
		total:    1,
	}
	// No structuredRecipients map seeded → enrichment returns empty.
	r := newTestRouter(t, ms)
	req := httptest.NewRequest("GET", "/api/v2/messages", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	body := w.Body.String()
	for _, field := range []string{`"to":[]`, `"cc":[]`, `"labels":[`} {
		if !strings.Contains(body, field) {
			t.Errorf("expected %s in response; got: %s", field, body)
		}
	}
}

func TestHandleListMessagesByLabel(t *testing.T) {
	ms := &mockStore{
		messages:        []store.APIMessage{{ID: 1, Subject: "Hi"}},
		labelMembership: map[string][]int64{"REKKIE": {1}},
	}
	ms.structuredRecipients = map[int64]*store.APIRecipientsV2{
		1: {From: &store.APIAddress{Address: "from@example.com"}},
	}
	r := newTestRouter(t, ms)

	req := httptest.NewRequest("GET", "/api/v2/labels/REKKIE/messages", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp PaginatedMessages
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 1 || len(resp.Messages) != 1 {
		t.Errorf("envelope wrong: %+v", resp)
	}
	if resp.Messages[0].From == nil || resp.Messages[0].From.Address != "from@example.com" {
		t.Errorf("from not enriched: %+v", resp.Messages[0])
	}
}

func TestHandleListMessagesByParticipant(t *testing.T) {
	ms := &mockStore{
		messages:              []store.APIMessage{{ID: 1, Subject: "Hi"}},
		participantMembership: map[int64][]int64{42: {1}},
	}
	ms.structuredRecipients = map[int64]*store.APIRecipientsV2{
		1: {To: []store.APIAddress{{Address: "to@example.com"}}},
	}
	r := newTestRouter(t, ms)

	req := httptest.NewRequest("GET", "/api/v2/participants/42/messages", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp PaginatedMessages
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 1 || resp.Messages[0].To[0].Address != "to@example.com" {
		t.Errorf("envelope wrong: %+v", resp)
	}
}

// TestHandleSearch_MissingQuery asserts the 400 contract for an empty
// q across both modes. Defined first so the rest of the search-test
// surface can focus on the success / mode-specific shape contracts.
func TestHandleSearch_MissingQuery(t *testing.T) {
	r := newTestRouter(t, &mockStore{})
	req := httptest.NewRequest("GET", "/api/v2/search", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleGetThread(t *testing.T) {
	ms := &mockStore{
		messages: []store.APIMessage{{ID: 1}},
	}
	ms.threads = map[int64]*store.APIThread{
		4421: {
			ID: 4421, Subject: "Re: hello", MessageCount: 1,
			Participants: []store.APIThreadParticipant{
				{ID: 10, Name: "Alice", Address: "alice@example.com"},
			},
			Messages: []store.APIThreadMessage{
				{ID: 1, From: "alice@example.com", FromName: "Alice",
					SentAt: mustParseTime(t, "2026-05-12T10:00:00Z"), Snippet: "Hi"},
			},
		},
	}
	ms.structuredRecipients = map[int64]*store.APIRecipientsV2{
		1: {
			From: &store.APIAddress{Name: "Alice", Address: "alice@example.com"},
			To:   []store.APIAddress{{Name: "Bob", Address: "bob@example.com"}},
		},
	}
	r := newTestRouter(t, ms)

	req := httptest.NewRequest("GET", "/api/v2/threads/4421", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-MsgVault-API"); got != "v2" {
		t.Errorf("X-MsgVault-API = %q, want v2", got)
	}

	var resp Thread
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != 4421 || resp.MessageCount != 1 || len(resp.Messages) != 1 {
		t.Errorf("envelope wrong: %+v", resp)
	}
	m := resp.Messages[0]
	if m.From == nil || m.From.Address != "alice@example.com" || m.From.Name != "Alice" {
		t.Errorf("thread message from not enriched: %+v", m.From)
	}
	if len(m.To) != 1 || m.To[0].Address != "bob@example.com" {
		t.Errorf("thread message to not enriched: %+v", m.To)
	}
}

func TestHandleGetAttachment(t *testing.T) {
	ms := &mockStore{}
	ms.attachmentsV2 = map[int64]*store.APIAttachmentDetailV2{
		7: {
			ID: 7, MessageID: 11134, ThreadID: 2754,
			AccountEmail: "user@example.com",
			Filename:     "report.pdf", MimeType: "application/pdf",
			SizeBytes: 4096, ContentHash: "abcd",
		},
		8: {
			ID: 8, MessageID: 11135, ThreadID: 2754,
			AccountEmail: "user@example.com",
			Filename:     "evil.html", MimeType: "text/html",
			SizeBytes: 1024,
		},
	}
	r := newTestRouter(t, ms)

	t.Run("PDF reports inline_disposition=true", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v2/attachments/7", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if got := w.Header().Get("X-MsgVault-API"); got != "v2" {
			t.Errorf("X-MsgVault-API = %q, want v2", got)
		}
		var resp AttachmentDetail
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.ID != 7 || resp.MessageID != 11134 || resp.ThreadID != 2754 {
			t.Errorf("ids wrong: %+v", resp)
		}
		if resp.Account != "user@example.com" {
			t.Errorf("account = %q", resp.Account)
		}
		if !resp.InlineDisposition {
			t.Errorf("PDF must report inline_disposition=true")
		}
	})

	t.Run("HTML attachment reports inline_disposition=false", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v2/attachments/8", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var resp AttachmentDetail
		_ = json.NewDecoder(w.Body).Decode(&resp)
		if resp.InlineDisposition {
			t.Errorf("HTML must report inline_disposition=false (XSS guard)")
		}
	})

	t.Run("miss returns 404 JSON", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v2/attachments/9999", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})
}

func TestHandleGetParticipant(t *testing.T) {
	first := mustParseTime(t, "2024-01-01T00:00:00Z")
	last := mustParseTime(t, "2026-05-13T00:00:00Z")
	ms := &mockStore{}
	ms.participantsV2 = map[int64]*store.APIParticipantV2{
		42: {
			ID: 42, Name: "User", Address: "user@example.com",
			Domain: "example.com", MessageCount: 100,
			FirstSeen: first, LastSeen: last,
			IsUserAccount: true,
		},
		43: {
			ID: 43, Name: "External", Address: "ext@somewhere.com",
			Domain: "somewhere.com", MessageCount: 5,
			IsUserAccount: false,
		},
	}
	r := newTestRouter(t, ms)

	t.Run("user-account participant flagged", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v2/participants/42", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		if got := w.Header().Get("X-MsgVault-API"); got != "v2" {
			t.Errorf("X-MsgVault-API = %q, want v2", got)
		}
		var resp Participant
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !resp.IsUserAccount {
			t.Errorf("expected is_user_account=true for synced address")
		}
		if resp.FirstSeen != "2024-01-01T00:00:00Z" {
			t.Errorf("first_seen = %q", resp.FirstSeen)
		}
	})

	t.Run("external participant not flagged", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v2/participants/43", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var resp Participant
		_ = json.NewDecoder(w.Body).Decode(&resp)
		if resp.IsUserAccount {
			t.Errorf("external participant should not be flagged as user account")
		}
	})

	t.Run("miss returns 404 JSON", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v2/participants/9999", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})
}
