package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wesm/msgvault/internal/query"
	"github.com/wesm/msgvault/internal/query/querytest"
)

// controllerTestEnv encapsulates common setup for ActionController tests.
type controllerTestEnv struct {
	Ctrl *ActionController
	Dir  string
}

func newTestEnv(t *testing.T) *controllerTestEnv {
	t.Helper()
	dir := t.TempDir()
	return &controllerTestEnv{
		Ctrl: NewActionController(&querytest.MockEngine{}, dir),
		Dir:  dir,
	}
}

func TestExportAttachments_NilDetail(t *testing.T) {
	env := newTestEnv(t)
	cmd := env.Ctrl.ExportAttachments(nil, nil)
	if cmd != nil {
		t.Error("expected nil cmd for nil detail")
	}
}

func TestExportAttachments_NoSelection(t *testing.T) {
	env := newTestEnv(t)
	detail := &query.MessageDetail{
		Attachments: []query.AttachmentInfo{
			{ID: 1, Filename: "file.pdf", ContentHash: "abc123"},
		},
	}
	cmd := env.Ctrl.ExportAttachments(detail, map[int]bool{})
	if cmd != nil {
		t.Error("expected nil cmd for empty selection")
	}
}

func TestExportAttachments_ErrBehavior(t *testing.T) {
	tests := []struct {
		name        string
		attachments []query.AttachmentInfo
		wantErr     bool
	}{
		{
			name: "invalid content hash sets Err",
			attachments: []query.AttachmentInfo{
				{ID: 1, Filename: "file.pdf", ContentHash: ""},
			},
			wantErr: true,
		},
		{
			name: "missing file sets Err",
			attachments: []query.AttachmentInfo{
				{ID: 1, Filename: "file.pdf", ContentHash: "abc123def456"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			detail := &query.MessageDetail{
				ID:          1,
				Subject:     "Test",
				Attachments: tt.attachments,
			}
			selection := make(map[int]bool)
			for i := range tt.attachments {
				selection[i] = true
			}

			cmd := env.Ctrl.ExportAttachments(detail, selection)
			if cmd == nil {
				t.Fatal("expected non-nil cmd")
			}

			msg := cmd()
			result, ok := msg.(ExportResultMsg)
			if !ok {
				t.Fatalf("expected ExportResultMsg, got %T", msg)
			}

			if tt.wantErr && result.Err == nil {
				t.Error("expected Err to be set")
			}
			if !tt.wantErr && result.Err != nil {
				t.Errorf("expected Err to be nil, got %v", result.Err)
			}
		})
	}
}

func TestExportAttachments_PartialSuccess(t *testing.T) {
	// Partial success: one valid file exports, one missing file fails.
	// Err should be nil because stats.Count > 0 (some files succeeded).
	env := newTestEnv(t)

	// Clean up the zip file that gets created in current directory.
	// TODO: ExportAttachments should write to a configurable output directory.
	t.Cleanup(func() { _ = os.Remove("Test_1.zip") })

	// Create a valid attachment file (must be valid 64-char hex SHA-256 hash)
	validHash := "abc123def456abc123def456abc123def456abc123def456abc123def456abc1"
	missingHash := "def456abc123def456abc123def456abc123def456abc123def456abc123def4"
	attachmentsDir := filepath.Join(env.Dir, "attachments")
	hashDir := filepath.Join(attachmentsDir, validHash[:2])
	if err := os.MkdirAll(hashDir, 0o755); err != nil {
		t.Fatalf("failed to create hash dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hashDir, validHash), []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to write attachment: %v", err)
	}

	detail := &query.MessageDetail{
		ID:      1,
		Subject: "Test",
		Attachments: []query.AttachmentInfo{
			{ID: 1, Filename: "valid.pdf", ContentHash: validHash},
			{ID: 2, Filename: "missing.pdf", ContentHash: missingHash},
		},
	}
	selection := map[int]bool{0: true, 1: true}

	cmd := env.Ctrl.ExportAttachments(detail, selection)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}

	msg := cmd()
	result, ok := msg.(ExportResultMsg)
	if !ok {
		t.Fatalf("expected ExportResultMsg, got %T", msg)
	}

	// Partial success should NOT set Err
	if result.Err != nil {
		t.Errorf("expected Err to be nil for partial success, got %v", result.Err)
	}

	// Result should contain both success info and error details
	if result.Result == "" {
		t.Error("expected non-empty Result")
	}
}

func TestExportAttachments_FullSuccess(t *testing.T) {
	// Full success: all attachments export without errors.
	env := newTestEnv(t)

	// Clean up the zip file that gets created in current directory.
	// TODO: ExportAttachments should write to a configurable output directory.
	t.Cleanup(func() { _ = os.Remove("Test_1.zip") })

	// Create a valid attachment file (must be valid 64-char hex SHA-256 hash)
	validHash := "abc123def456abc123def456abc123def456abc123def456abc123def456abc1"
	attachmentsDir := filepath.Join(env.Dir, "attachments")
	hashDir := filepath.Join(attachmentsDir, validHash[:2])
	if err := os.MkdirAll(hashDir, 0o755); err != nil {
		t.Fatalf("failed to create hash dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hashDir, validHash), []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to write attachment: %v", err)
	}

	detail := &query.MessageDetail{
		ID:      1,
		Subject: "Test",
		Attachments: []query.AttachmentInfo{
			{ID: 1, Filename: "valid.pdf", ContentHash: validHash},
		},
	}
	selection := map[int]bool{0: true}

	cmd := env.Ctrl.ExportAttachments(detail, selection)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}

	msg := cmd()
	result, ok := msg.(ExportResultMsg)
	if !ok {
		t.Fatalf("expected ExportResultMsg, got %T", msg)
	}

	if result.Err != nil {
		t.Errorf("expected Err to be nil for full success, got %v", result.Err)
	}
	if result.Result == "" {
		t.Error("expected non-empty Result")
	}
}
