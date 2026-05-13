package store_test

// Real-DB tests for the store methods introduced for radical-roc's
// public-API surface (see docs/PUBLIC_API.md and
// .planning/PLAN-radical-roc-wishlist.md). The API package tests in
// internal/api/handlers_test.go exercise these methods via a mock
// store, so this file's job is narrow: verify the SQL is correct.

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/wesm/msgvault/internal/store"
	"github.com/wesm/msgvault/internal/testutil/storetest"
)

func TestStore_GetMessageByRFC822ID(t *testing.T) {
	f := storetest.New(t)

	sent := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	idA, err := f.Store.UpsertMessage(&store.Message{
		ConversationID:  f.ConvID,
		SourceID:        f.Source.ID,
		SourceMessageID: "src-a",
		MessageType:     "email",
		SizeEstimate:    1,
		RFC822MessageID: sql.NullString{String: "<rfc-1@example.com>", Valid: true},
		SentAt:          sql.NullTime{Time: sent, Valid: true},
	})
	if err != nil {
		t.Fatalf("upsert A: %v", err)
	}

	t.Run("hit returns matching message", func(t *testing.T) {
		msg, err := f.Store.GetMessageByRFC822ID("<rfc-1@example.com>")
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if msg == nil {
			t.Fatal("expected hit, got nil")
		}
		if msg.ID != idA {
			t.Errorf("ID = %d, want %d", msg.ID, idA)
		}
	})

	t.Run("miss returns nil", func(t *testing.T) {
		msg, err := f.Store.GetMessageByRFC822ID("<not-there@example.com>")
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if msg != nil {
			t.Errorf("expected nil, got %+v", msg)
		}
	})

	t.Run("multi-source dedup picks lowest id", func(t *testing.T) {
		// Insert a second message with the same RFC822 id in the same
		// source (with a different source_message_id to satisfy the
		// uniqueness constraint). Per docs: "lowest internal id wins".
		idB, err := f.Store.UpsertMessage(&store.Message{
			ConversationID:  f.ConvID,
			SourceID:        f.Source.ID,
			SourceMessageID: "src-b",
			MessageType:     "email",
			SizeEstimate:    1,
			RFC822MessageID: sql.NullString{String: "<rfc-1@example.com>", Valid: true},
		})
		if err != nil {
			t.Fatalf("upsert B: %v", err)
		}
		if idB <= idA {
			t.Fatalf("expected idB > idA for this test; got %d <= %d", idB, idA)
		}
		msg, err := f.Store.GetMessageByRFC822ID("<rfc-1@example.com>")
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if msg.ID != idA {
			t.Errorf("dedup winner = %d, want lower id %d", msg.ID, idA)
		}
	})
}

func TestStore_GetMessageBodies(t *testing.T) {
	f := storetest.New(t)

	t.Run("returns both parts when present", func(t *testing.T) {
		id := f.NewMessage().Create(t, f.Store)
		if err := f.Store.UpsertMessageBody(id,
			sql.NullString{String: "plaintext body", Valid: true},
			sql.NullString{String: "<p>html body</p>", Valid: true},
		); err != nil {
			t.Fatalf("upsert body: %v", err)
		}
		text, html, err := f.Store.GetMessageBodies(id)
		if err != nil {
			t.Fatalf("get bodies: %v", err)
		}
		if text != "plaintext body" || html != "<p>html body</p>" {
			t.Errorf("got (%q, %q)", text, html)
		}
	})

	t.Run("returns empty pair when no body row exists", func(t *testing.T) {
		id := f.NewMessage().Create(t, f.Store)
		text, html, err := f.Store.GetMessageBodies(id)
		if err != nil {
			t.Fatalf("get bodies: %v", err)
		}
		if text != "" || html != "" {
			t.Errorf("expected empty pair, got (%q, %q)", text, html)
		}
	})
}

func TestStore_GetThread(t *testing.T) {
	f := storetest.New(t)

	// Two messages in the default conversation, plus a sender participant.
	sender := f.EnsureParticipant("alice@example.com", "Alice", "example.com")
	recip := f.EnsureParticipant("bob@example.com", "Bob", "example.com")

	t1 := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 12, 11, 0, 0, 0, time.UTC)

	id1 := f.NewMessage().
		WithSubject("Re: hello").
		WithSnippet("first").
		WithSentAt(t1).
		Create(t, f.Store)
	id2 := f.NewMessage().
		WithSubject("Re: hello").
		WithSnippet("second").
		WithSentAt(t2).
		Create(t, f.Store)

	if err := f.Store.ReplaceMessageRecipients(id1, "from", []int64{sender}, []string{"Alice"}); err != nil {
		t.Fatalf("recip 1: %v", err)
	}
	if err := f.Store.ReplaceMessageRecipients(id1, "to", []int64{recip}, []string{"Bob"}); err != nil {
		t.Fatalf("recip 1 to: %v", err)
	}
	if err := f.Store.ReplaceMessageRecipients(id2, "from", []int64{recip}, []string{"Bob"}); err != nil {
		t.Fatalf("recip 2: %v", err)
	}
	if err := f.Store.ReplaceMessageRecipients(id2, "to", []int64{sender}, []string{"Alice"}); err != nil {
		t.Fatalf("recip 2 to: %v", err)
	}

	th, err := f.Store.GetThread(f.ConvID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if th == nil {
		t.Fatal("expected thread, got nil")
	}
	if th.MessageCount != 2 || len(th.Messages) != 2 {
		t.Errorf("MessageCount=%d, Messages=%d", th.MessageCount, len(th.Messages))
	}
	// Messages must be ordered chronologically.
	if th.Messages[0].ID != id1 || th.Messages[1].ID != id2 {
		t.Errorf("message order = [%d, %d], want [%d, %d]",
			th.Messages[0].ID, th.Messages[1].ID, id1, id2)
	}
	if th.Messages[0].From != "alice@example.com" {
		t.Errorf("first message From = %q, want alice@example.com", th.Messages[0].From)
	}
	// Participants must dedupe across messages — alice + bob, not 4 rows.
	if len(th.Participants) != 2 {
		t.Errorf("participants = %d, want 2", len(th.Participants))
	}

	t.Run("unknown thread returns nil", func(t *testing.T) {
		got, err := f.Store.GetThread(99999)
		if err != nil {
			t.Fatalf("GetThread: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})
}

func TestStore_GetAttachmentByID(t *testing.T) {
	f := storetest.New(t)

	msgID := f.NewMessage().Create(t, f.Store)
	if err := f.Store.UpsertAttachment(msgID, "report.pdf", "application/pdf",
		"ab/abcd1234", "abcd1234", 4096); err != nil {
		t.Fatalf("upsert attachment: %v", err)
	}

	// Find the attachment id via a direct SQL lookup — there's no public
	// "list attachments for message" method here, so we just SELECT.
	var attID int64
	if err := f.Store.DB().QueryRow(
		`SELECT id FROM attachments WHERE message_id = ?`, msgID,
	).Scan(&attID); err != nil {
		t.Fatalf("lookup attachment id: %v", err)
	}

	t.Run("hit returns metadata", func(t *testing.T) {
		got, err := f.Store.GetAttachmentByID(attID)
		if err != nil {
			t.Fatalf("GetAttachmentByID: %v", err)
		}
		if got == nil {
			t.Fatal("expected hit, got nil")
		}
		if got.Filename != "report.pdf" || got.MimeType != "application/pdf" ||
			got.Size != 4096 || got.ContentHash != "abcd1234" ||
			got.StoragePath != "ab/abcd1234" || got.MessageID != msgID {
			t.Errorf("payload mismatch: %+v", got)
		}
	})

	t.Run("miss returns nil", func(t *testing.T) {
		got, err := f.Store.GetAttachmentByID(99999)
		if err != nil {
			t.Fatalf("GetAttachmentByID: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})
}

func TestStore_LabelsListing(t *testing.T) {
	f := storetest.New(t)

	labels := f.EnsureLabels(map[string]string{
		"Label_INBOX":  "INBOX",
		"Label_REKKIE": "REKKIE",
	}, "system")

	id1 := f.NewMessage().Create(t, f.Store)
	id2 := f.NewMessage().Create(t, f.Store)
	id3 := f.NewMessage().Create(t, f.Store)
	if err := f.Store.AddMessageLabels(id1, []int64{labels["Label_INBOX"], labels["Label_REKKIE"]}); err != nil {
		t.Fatalf("add labels 1: %v", err)
	}
	if err := f.Store.AddMessageLabels(id2, []int64{labels["Label_INBOX"]}); err != nil {
		t.Fatalf("add labels 2: %v", err)
	}
	if err := f.Store.AddMessageLabels(id3, []int64{labels["Label_REKKIE"]}); err != nil {
		t.Fatalf("add labels 3: %v", err)
	}

	t.Run("ListLabels aggregates across messages", func(t *testing.T) {
		got, err := f.Store.ListLabels()
		if err != nil {
			t.Fatalf("ListLabels: %v", err)
		}
		counts := map[string]int64{}
		for _, l := range got {
			counts[l.Name] = l.Count
		}
		if counts["INBOX"] != 2 || counts["REKKIE"] != 2 {
			t.Errorf("counts = %+v, want INBOX=2 REKKIE=2", counts)
		}
	})

	t.Run("ListMessagesByLabel returns tagged messages", func(t *testing.T) {
		msgs, total, err := f.Store.ListMessagesByLabel("REKKIE", 0, 50)
		if err != nil {
			t.Fatalf("ListMessagesByLabel: %v", err)
		}
		if total != 2 || len(msgs) != 2 {
			t.Errorf("total=%d, returned=%d", total, len(msgs))
		}
		seen := map[int64]bool{}
		for _, m := range msgs {
			seen[m.ID] = true
		}
		if !seen[id1] || !seen[id3] {
			t.Errorf("expected ids %d and %d, got %+v", id1, id3, seen)
		}
	})

	t.Run("ListMessagesByLabel honours pagination", func(t *testing.T) {
		page1, total, err := f.Store.ListMessagesByLabel("REKKIE", 0, 1)
		if err != nil {
			t.Fatalf("page1: %v", err)
		}
		page2, _, err := f.Store.ListMessagesByLabel("REKKIE", 1, 1)
		if err != nil {
			t.Fatalf("page2: %v", err)
		}
		if total != 2 || len(page1) != 1 || len(page2) != 1 {
			t.Errorf("pagination broken: total=%d, p1=%d, p2=%d", total, len(page1), len(page2))
		}
		if page1[0].ID == page2[0].ID {
			t.Errorf("pages overlap: %d", page1[0].ID)
		}
	})

	t.Run("ListMessagesByLabel returns empty for unknown label", func(t *testing.T) {
		msgs, total, err := f.Store.ListMessagesByLabel("NOPE", 0, 50)
		if err != nil {
			t.Fatalf("ListMessagesByLabel: %v", err)
		}
		if total != 0 || len(msgs) != 0 {
			t.Errorf("unknown label should return empty, got total=%d", total)
		}
	})
}

func TestStore_Participants(t *testing.T) {
	f := storetest.New(t)

	alice := f.EnsureParticipant("alice@example.com", "Alice", "example.com")
	bob := f.EnsureParticipant("bob@example.com", "Bob", "example.com")

	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)

	id1 := f.NewMessage().WithSentAt(t1).Create(t, f.Store)
	id2 := f.NewMessage().WithSentAt(t2).Create(t, f.Store)

	// Alice sends both; Bob is on the to: line of message 2 only.
	if err := f.Store.ReplaceMessageRecipients(id1, "from", []int64{alice}, []string{"Alice"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Store.ReplaceMessageRecipients(id2, "from", []int64{alice}, []string{"Alice"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Store.ReplaceMessageRecipients(id2, "to", []int64{bob}, []string{"Bob"}); err != nil {
		t.Fatal(err)
	}

	t.Run("GetParticipantByID populates aggregates", func(t *testing.T) {
		got, err := f.Store.GetParticipantByID(alice)
		if err != nil {
			t.Fatalf("GetParticipantByID: %v", err)
		}
		if got == nil {
			t.Fatal("expected participant, got nil")
		}
		if got.Name != "Alice" || got.Address != "alice@example.com" || got.Domain != "example.com" {
			t.Errorf("identity wrong: %+v", got)
		}
		if got.MessageCount != 2 {
			t.Errorf("count = %d, want 2", got.MessageCount)
		}
		if !got.FirstSeen.Equal(t1) {
			t.Errorf("first_seen = %v, want %v", got.FirstSeen, t1)
		}
		if !got.LastSeen.Equal(t2) {
			t.Errorf("last_seen = %v, want %v", got.LastSeen, t2)
		}
	})

	t.Run("GetParticipantByID miss returns nil", func(t *testing.T) {
		got, err := f.Store.GetParticipantByID(99999)
		if err != nil {
			t.Fatalf("GetParticipantByID: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})

	t.Run("ListMessagesByParticipant covers any recipient_type", func(t *testing.T) {
		// Bob is only a 'to' recipient on id2 — must still be returned.
		msgs, total, err := f.Store.ListMessagesByParticipant(bob, 0, 50)
		if err != nil {
			t.Fatalf("ListMessagesByParticipant: %v", err)
		}
		if total != 1 || len(msgs) != 1 || msgs[0].ID != id2 {
			t.Errorf("got total=%d, msgs=%+v", total, msgs)
		}
	})
}

func TestStore_GetMessageV2(t *testing.T) {
	f := storetest.New(t)

	// Build a single message with structured recipients, body parts,
	// an attachment, a label, AND a real raw MIME blob so the in-reply-to
	// / references / reply-to extraction path is exercised end to end.
	alice := f.EnsureParticipant("alice@example.com", "Alice", "example.com")
	bob := f.EnsureParticipant("bob@example.com", "Bob", "example.com")
	carl := f.EnsureParticipant("carl@example.com", "Carl", "example.com")

	id, err := f.Store.UpsertMessage(&store.Message{
		ConversationID:  f.ConvID,
		SourceID:        f.Source.ID,
		SourceMessageID: "src-v2",
		MessageType:     "email",
		SizeEstimate:    1024,
		RFC822MessageID: sql.NullString{String: "<rfc-child@example.com>", Valid: true},
		Subject:         sql.NullString{String: "Re: hello", Valid: true},
		Snippet:         sql.NullString{String: "hi there", Valid: true},
		SentAt:          sql.NullTime{Time: time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC), Valid: true},
		HasAttachments:  true,
		AttachmentCount: 1,
	})
	if err != nil {
		t.Fatalf("upsert message: %v", err)
	}

	if err := f.Store.ReplaceMessageRecipients(id, "from", []int64{alice}, []string{"Alice"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Store.ReplaceMessageRecipients(id, "to", []int64{bob}, []string{"Bob"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Store.ReplaceMessageRecipients(id, "cc", []int64{carl}, []string{"Carl"}); err != nil {
		t.Fatal(err)
	}

	if err := f.Store.UpsertMessageBody(id,
		sql.NullString{String: "plain body", Valid: true},
		sql.NullString{String: "<p>html body</p>", Valid: true},
	); err != nil {
		t.Fatal(err)
	}

	if err := f.Store.UpsertAttachment(id, "report.pdf", "application/pdf",
		"ab/abcd1234", "abcd1234", 4096); err != nil {
		t.Fatal(err)
	}

	labels := f.EnsureLabels(map[string]string{"Label_INBOX": "INBOX"}, "system")
	if err := f.Store.AddMessageLabels(id, []int64{labels["Label_INBOX"]}); err != nil {
		t.Fatal(err)
	}

	// Real RFC822 with headers the v2 detail must extract.
	raw := []byte(strings.Join([]string{
		"From: Alice <alice@example.com>",
		"To: Bob <bob@example.com>",
		"Cc: Carl <carl@example.com>",
		"Reply-To: no-reply@example.com",
		"Subject: Re: hello",
		"Message-ID: <rfc-child@example.com>",
		"In-Reply-To: <rfc-parent@example.com>",
		"References: <rfc-root@example.com> <rfc-parent@example.com>",
		"Date: Tue, 12 May 2026 10:00:00 +0000",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"hi there",
	}, "\r\n"))
	if err := f.Store.UpsertMessageRaw(id, raw); err != nil {
		t.Fatalf("upsert raw: %v", err)
	}

	got, err := f.Store.GetMessageV2(id)
	if err != nil {
		t.Fatalf("GetMessageV2: %v", err)
	}
	if got == nil {
		t.Fatal("expected hit, got nil")
	}

	if got.RFC822MessageID != "<rfc-child@example.com>" ||
		got.SourceMessageID != "src-v2" ||
		got.ThreadID != f.ConvID ||
		got.AccountEmail != "test@example.com" ||
		got.MessageType != "email" {
		t.Errorf("ids/headers wrong: %+v", got)
	}
	if got.From == nil || got.From.Address != "alice@example.com" || got.From.Name != "Alice" {
		t.Errorf("structured From wrong: %+v", got.From)
	}
	if len(got.To) != 1 || got.To[0].Address != "bob@example.com" || got.To[0].Name != "Bob" {
		t.Errorf("structured To wrong: %+v", got.To)
	}
	if len(got.Cc) != 1 || got.Cc[0].Address != "carl@example.com" {
		t.Errorf("structured Cc wrong: %+v", got.Cc)
	}
	// In-Reply-To / References / Reply-To came from the raw MIME path.
	// internal/mime returns InReplyTo bracketed and References stripped;
	// the API layer (messageDetailV2From) re-brackets References before
	// emitting JSON.
	if got.InReplyTo != "<rfc-parent@example.com>" {
		t.Errorf("InReplyTo = %q", got.InReplyTo)
	}
	if len(got.References) != 2 || got.References[0] != "rfc-root@example.com" {
		t.Errorf("References wrong: %+v", got.References)
	}
	if len(got.ReplyTo) != 1 || got.ReplyTo[0].Address != "no-reply@example.com" {
		t.Errorf("ReplyTo wrong: %+v", got.ReplyTo)
	}
	if got.BodyText != "plain body" || got.BodyHTML != "<p>html body</p>" {
		t.Errorf("body wrong: text=%q html=%q", got.BodyText, got.BodyHTML)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].ID == 0 ||
		got.Attachments[0].Filename != "report.pdf" {
		t.Errorf("attachments wrong: %+v", got.Attachments)
	}
	if len(got.Labels) != 1 || got.Labels[0] != "INBOX" {
		t.Errorf("labels wrong: %+v", got.Labels)
	}
	if got.AttachmentCount != 1 || !got.HasAttachments {
		t.Errorf("attachment-count flags wrong: has=%v count=%d", got.HasAttachments, got.AttachmentCount)
	}
}

func TestStore_GetMessageV2_NoRawBlob(t *testing.T) {
	// When no raw MIME has been stored (older imports), in_reply_to /
	// references / reply_to should be empty rather than fail the
	// lookup. The rest of the v2 detail must still come back.
	f := storetest.New(t)
	id := f.NewMessage().WithSubject("plain").Create(t, f.Store)

	got, err := f.Store.GetMessageV2(id)
	if err != nil {
		t.Fatalf("GetMessageV2: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil v2 detail even without raw blob")
	}
	if got.InReplyTo != "" || len(got.References) != 0 || len(got.ReplyTo) != 0 {
		t.Errorf("expected empty MIME-derived headers; got in_reply_to=%q refs=%v replyto=%v",
			got.InReplyTo, got.References, got.ReplyTo)
	}
	if got.Subject != "plain" {
		t.Errorf("subject = %q, want %q", got.Subject, "plain")
	}
}

func TestStore_GetCorpusFingerprint(t *testing.T) {
	f := storetest.New(t)

	// One message → fingerprint computable; assert stability and the
	// expected response shape.
	id := f.NewMessage().WithSentAt(time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)).Create(t, f.Store)
	_ = id

	a, err := f.Store.GetCorpusFingerprint()
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if a == nil {
		t.Fatal("expected non-nil fingerprint even for one-message corpus")
	}
	if !strings.HasPrefix(a.Fingerprint, "sha256:") {
		t.Errorf("fingerprint = %q, want sha256: prefix", a.Fingerprint)
	}
	if a.MessageCount != 1 {
		t.Errorf("count = %d, want 1", a.MessageCount)
	}

	t.Run("stable across calls when no sync intervenes", func(t *testing.T) {
		b, err := f.Store.GetCorpusFingerprint()
		if err != nil {
			t.Fatalf("fingerprint 2: %v", err)
		}
		if a.Fingerprint != b.Fingerprint {
			t.Errorf("fingerprint drifted: %q vs %q", a.Fingerprint, b.Fingerprint)
		}
	})

	t.Run("changes when a new message lands", func(t *testing.T) {
		_ = f.NewMessage().Create(t, f.Store)
		c, err := f.Store.GetCorpusFingerprint()
		if err != nil {
			t.Fatalf("fingerprint 3: %v", err)
		}
		if c.Fingerprint == a.Fingerprint {
			t.Errorf("fingerprint should change on insert, stayed %q", c.Fingerprint)
		}
		if c.MessageCount != 2 {
			t.Errorf("count after insert = %d, want 2", c.MessageCount)
		}
	})
}
