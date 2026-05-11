package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/wesm/msgvault/internal/query"
	"github.com/wesm/msgvault/internal/query/querytest"
)

// TestGotoBar_OpensWithColon verifies that pressing `:` at the
// aggregate level activates the goto bar.
func TestGotoBar_OpensWithColon(t *testing.T) {
	model := NewBuilder().Build()
	if model.gotoActive {
		t.Fatal("gotoActive should start false")
	}
	model, _ = sendKey(t, model, key(':'))
	if !model.gotoActive {
		t.Error("expected gotoActive=true after pressing ':'")
	}
}

// TestGotoBar_EscClosesWithoutLookup verifies Esc cancels and clears
// the input without dispatching a lookup.
func TestGotoBar_EscClosesWithoutLookup(t *testing.T) {
	model := NewBuilder().Build()
	model, _ = sendKey(t, model, key(':'))
	model.gotoInput.SetValue("12345")
	model, cmd := sendKey(t, model, keyEsc())
	if model.gotoActive {
		t.Error("expected gotoActive=false after Esc")
	}
	if model.gotoInput.Value() != "" {
		t.Errorf("expected input cleared, got %q", model.gotoInput.Value())
	}
	if cmd != nil {
		t.Error("Esc should not dispatch a command")
	}
}

// TestGotoBar_NumericIDLookup verifies that submitting a numeric ID
// calls engine.GetMessage and lands on the detail view.
func TestGotoBar_NumericIDLookup(t *testing.T) {
	want := &query.MessageDetail{ID: 42, Subject: "Hello"}
	var calledID int64
	eng := &querytest.MockEngine{
		GetMessageFunc: func(_ context.Context, id int64) (*query.MessageDetail, error) {
			calledID = id
			return want, nil
		},
	}
	model := NewBuilder().Build()
	model.engine = eng

	model, _ = sendKey(t, model, key(':'))
	model.gotoInput.SetValue("42")
	model, cmd := sendKey(t, model, keyEnter())

	if model.gotoActive {
		t.Error("goto bar should close on Enter")
	}
	if cmd == nil {
		t.Fatal("expected lookup cmd")
	}
	model, _ = sendMsg(t, model, cmd())
	if calledID != 42 {
		t.Errorf("GetMessage called with id=%d, want 42", calledID)
	}
	if model.level != levelMessageDetail {
		t.Errorf("level=%v, want levelMessageDetail", model.level)
	}
	if model.messageDetail == nil || model.messageDetail.ID != 42 {
		t.Errorf("messageDetail not populated correctly")
	}
}

// TestGotoBar_HexIDFallsBackToSourceLookup verifies that a non-numeric
// input falls through to GetMessageBySourceID.
func TestGotoBar_HexIDFallsBackToSourceLookup(t *testing.T) {
	want := &query.MessageDetail{ID: 7, Subject: "Gmail hex hit"}
	var calledSourceID string
	getMessageCalled := false
	eng := &querytest.MockEngine{
		GetMessageFunc: func(_ context.Context, _ int64) (*query.MessageDetail, error) {
			getMessageCalled = true
			return nil, nil
		},
		GetMessageBySourceIDFunc: func(_ context.Context, sid string) (*query.MessageDetail, error) {
			calledSourceID = sid
			return want, nil
		},
	}
	model := NewBuilder().Build()
	model.engine = eng

	model, _ = sendKey(t, model, key(':'))
	model.gotoInput.SetValue("18ab2bdd69ac7ff6")
	model, cmd := sendKey(t, model, keyEnter())
	if cmd == nil {
		t.Fatal("expected lookup cmd")
	}
	model, _ = sendMsg(t, model, cmd())

	if getMessageCalled {
		t.Error("non-numeric input should skip GetMessage")
	}
	if calledSourceID != "18ab2bdd69ac7ff6" {
		t.Errorf("GetMessageBySourceID called with %q, want %q",
			calledSourceID, "18ab2bdd69ac7ff6")
	}
	if model.level != levelMessageDetail {
		t.Errorf("level=%v, want levelMessageDetail", model.level)
	}
}

// TestGotoBar_ThreadPrefix verifies that `t:42` resolves the message
// by ID then dispatches the thread load. We test through to the
// gotoResultMsg handler; the actual loadThreadMessages cmd is its own
// async step covered by existing thread tests.
func TestGotoBar_ThreadPrefix(t *testing.T) {
	want := &query.MessageDetail{ID: 42, ConversationID: 99, Subject: "msg in thread"}
	eng := &querytest.MockEngine{
		GetMessageFunc: func(_ context.Context, _ int64) (*query.MessageDetail, error) {
			return want, nil
		},
	}
	model := NewBuilder().Build()
	model.engine = eng

	model, _ = sendKey(t, model, key(':'))
	model.gotoInput.SetValue("t:42")
	model, cmd := sendKey(t, model, keyEnter())
	if cmd == nil {
		t.Fatal("expected lookup cmd")
	}
	model, threadCmd := sendMsg(t, model, cmd())

	if model.level != levelThreadView {
		t.Errorf("level=%v, want levelThreadView", model.level)
	}
	if model.threadConversationID != 99 {
		t.Errorf("threadConversationID=%d, want 99", model.threadConversationID)
	}
	if threadCmd == nil {
		t.Error("expected loadThreadMessages cmd after navigating to thread view")
	}
}

// TestGotoBar_ThreadPrefix_NoConvID flashes "Message has no thread"
// when the resolved message has ConversationID=0.
func TestGotoBar_ThreadPrefix_NoConvID(t *testing.T) {
	want := &query.MessageDetail{ID: 42, ConversationID: 0}
	eng := &querytest.MockEngine{
		GetMessageFunc: func(_ context.Context, _ int64) (*query.MessageDetail, error) {
			return want, nil
		},
	}
	startLevel := levelAggregates
	model := NewBuilder().WithLevel(startLevel).Build()
	model.engine = eng

	model, _ = sendKey(t, model, key(':'))
	model.gotoInput.SetValue("thread:42")
	model, cmd := sendKey(t, model, keyEnter())
	model, _ = sendMsg(t, model, cmd())

	if model.level != startLevel {
		t.Errorf("level=%v, want unchanged %v", model.level, startLevel)
	}
	if model.flashMessage == "" {
		t.Error("expected flash message for thread-less message")
	}
}

// TestGotoBar_NotFound shows a flash and does not navigate when the
// engine returns nil for both lookups.
func TestGotoBar_NotFound(t *testing.T) {
	eng := &querytest.MockEngine{
		GetMessageFunc: func(_ context.Context, _ int64) (*query.MessageDetail, error) {
			return nil, nil
		},
		GetMessageBySourceIDFunc: func(_ context.Context, _ string) (*query.MessageDetail, error) {
			return nil, nil
		},
	}
	model := NewBuilder().Build()
	model.engine = eng
	startLevel := model.level

	model, _ = sendKey(t, model, key(':'))
	model.gotoInput.SetValue("999999")
	model, cmd := sendKey(t, model, keyEnter())
	model, _ = sendMsg(t, model, cmd())

	if model.level != startLevel {
		t.Errorf("level=%v, want unchanged %v", model.level, startLevel)
	}
	if model.flashMessage == "" {
		t.Error("expected flash for not-found")
	}
}

// TestGotoBar_LookupError flashes the error and stays in place.
func TestGotoBar_LookupError(t *testing.T) {
	eng := &querytest.MockEngine{
		GetMessageFunc: func(_ context.Context, _ int64) (*query.MessageDetail, error) {
			return nil, errors.New("kaboom")
		},
	}
	model := NewBuilder().Build()
	model.engine = eng
	startLevel := model.level

	model, _ = sendKey(t, model, key(':'))
	model.gotoInput.SetValue("42")
	model, cmd := sendKey(t, model, keyEnter())
	model, _ = sendMsg(t, model, cmd())

	if model.level != startLevel {
		t.Errorf("level should not change on error, got %v", model.level)
	}
	if model.flashMessage == "" {
		t.Error("expected flash on error")
	}
}

// TestGotoBar_EmptyInputIsNoop verifies that submitting an empty
// input clears the bar without dispatching a lookup.
func TestGotoBar_EmptyInputIsNoop(t *testing.T) {
	model := NewBuilder().Build()
	model, _ = sendKey(t, model, key(':'))
	model, cmd := sendKey(t, model, keyEnter())
	if model.gotoActive {
		t.Error("bar should close on empty submit")
	}
	if cmd != nil {
		t.Error("empty submit should not dispatch a cmd")
	}
}

// TestGotoBar_PrefixOnlyFlashes verifies that "t:" without an ID
// flashes a helpful error.
func TestGotoBar_PrefixOnlyFlashes(t *testing.T) {
	model := NewBuilder().Build()
	model, _ = sendKey(t, model, key(':'))
	model.gotoInput.SetValue("t:")
	model, _ = sendKey(t, model, keyEnter())
	if model.flashMessage == "" {
		t.Error("expected flash on prefix-only input")
	}
}
