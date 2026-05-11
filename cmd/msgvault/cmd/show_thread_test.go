package cmd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wesm/msgvault/internal/query"
)

// TestShowThreadJSON_Shape locks the JSON output structure: required
// top-level keys, schema_version=1, and a row shape that includes the
// fields agents consume (id, from_email, sent_at, labels).
func TestShowThreadJSON_Shape(t *testing.T) {
	anchor := &query.MessageDetail{
		ID:                   42,
		ConversationID:       100,
		SourceConversationID: "18ab2bdd69ac7ff6",
		Subject:              "Re: thread subject",
	}
	msgs := []query.MessageSummary{
		{
			ID:              10,
			SourceMessageID: "gmail-10",
			Subject:         "first",
			FromEmail:       "alice@example.com",
			FromName:        "Alice",
			SentAt:          time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
			SizeEstimate:    1024,
			Labels:          []string{"INBOX"},
		},
		{
			ID:              11,
			SourceMessageID: "gmail-11",
			Subject:         "Re: first",
			FromEmail:       "bob@example.com",
			SentAt:          time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
			SizeEstimate:    2048,
		},
	}

	restore := captureStdout(t)
	err := outputThreadJSON(anchor, msgs, false)
	out := restore()
	if err != nil {
		t.Fatalf("outputThreadJSON: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v\noutput=%s", err, out)
	}

	if got["schema_version"] != float64(SchemaVersion) {
		t.Errorf("schema_version = %v, want %d", got["schema_version"], SchemaVersion)
	}
	if got["conversation_id"] != float64(100) {
		t.Errorf("conversation_id = %v, want 100", got["conversation_id"])
	}
	if got["source_conversation_id"] != "18ab2bdd69ac7ff6" {
		t.Errorf("source_conversation_id = %v, want hex", got["source_conversation_id"])
	}
	if got["subject"] != "Re: thread subject" {
		t.Errorf("subject = %v", got["subject"])
	}
	if got["message_count"] != float64(2) {
		t.Errorf("message_count = %v, want 2", got["message_count"])
	}
	if got["truncated"] != false {
		t.Errorf("truncated = %v, want false", got["truncated"])
	}
	messages, ok := got["messages"].([]any)
	if !ok {
		t.Fatalf("messages is not an array: %T", got["messages"])
	}
	if len(messages) != 2 {
		t.Fatalf("messages length = %d, want 2", len(messages))
	}
	first := messages[0].(map[string]any)
	if first["id"] != float64(10) {
		t.Errorf("messages[0].id = %v, want 10", first["id"])
	}
	if first["from_email"] != "alice@example.com" {
		t.Errorf("messages[0].from_email = %v", first["from_email"])
	}
	if first["sent_at"] != "2026-01-01T09:00:00Z" {
		t.Errorf("messages[0].sent_at = %v", first["sent_at"])
	}
}

// TestShowThreadJSON_TruncatedFlag verifies the truncated flag rides
// through correctly when the engine returned the cap+1 batch.
func TestShowThreadJSON_TruncatedFlag(t *testing.T) {
	anchor := &query.MessageDetail{ConversationID: 5, Subject: "x"}
	restore := captureStdout(t)
	err := outputThreadJSON(anchor, []query.MessageSummary{}, true)
	out := restore()
	if err != nil {
		t.Fatalf("outputThreadJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["truncated"] != true {
		t.Errorf("truncated = %v, want true", got["truncated"])
	}
}

// TestShowThreadText_Contains verifies the human-readable text output
// includes the thread header line and at least one message row.
func TestShowThreadText_Contains(t *testing.T) {
	anchor := &query.MessageDetail{
		ID:                   42,
		ConversationID:       100,
		SourceConversationID: "abc123",
		Subject:              "Re: hello world",
	}
	msgs := []query.MessageSummary{
		{
			ID:        10,
			Subject:   "first",
			FromEmail: "alice@example.com",
			SentAt:    time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
		},
	}
	restore := captureStdout(t)
	err := outputThreadText(anchor, msgs, false)
	out := restore()
	if err != nil {
		t.Fatalf("outputThreadText: %v", err)
	}

	wants := []string{
		"Thread: Re: hello world",
		"Conversation ID: 100",
		"abc123",
		"Messages: 1",
		"alice@example.com",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q\nfull output:\n%s", w, out)
		}
	}
}

// TestShowThreadText_TruncatedNote shows the truncation hint when
// the engine signaled truncation.
func TestShowThreadText_TruncatedNote(t *testing.T) {
	anchor := &query.MessageDetail{ConversationID: 1, Subject: "x"}
	restore := captureStdout(t)
	err := outputThreadText(anchor, []query.MessageSummary{}, true)
	out := restore()
	if err != nil {
		t.Fatalf("outputThreadText: %v", err)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("expected truncation hint, got:\n%s", out)
	}
}
