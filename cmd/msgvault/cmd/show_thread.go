package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/wesm/msgvault/internal/query"
	"github.com/wesm/msgvault/internal/store"
)

var (
	showThreadJSON  bool
	showThreadLimit int
)

// defaultShowThreadLimit caps the number of messages returned for a
// single thread to keep output bounded. Matches the TUI's
// defaultThreadMessageLimit so the two surfaces produce the same
// truncation behavior.
const defaultShowThreadLimit = 1000

var showThreadCmd = &cobra.Command{
	Use:   "show-thread <id>",
	Short: "Show all messages in a thread",
	Long: `Show all messages in a thread (conversation) by an anchor message's ID.

<id> can be:
  - An internal numeric message ID (e.g. 12345)
  - A Gmail message hex ID (e.g. 18ab2bdd69ac7ff6)

The command resolves the message, then lists every other message in
the same conversation, sorted oldest-first. This mirrors the TUI's
"T" keypress on a message detail view.

Examples:
  msgvault show-thread 12345
  msgvault show-thread 18ab2bdd69ac7ff6 --json
  msgvault show-thread 12345 --limit 50`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := MustBeLocal("show-thread"); err != nil {
			return err
		}
		return showLocalThread(cmd, args[0])
	},
}

func showLocalThread(cmd *cobra.Command, idStr string) error {
	dbPath := cfg.DatabaseDSN()
	s, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.InitSchema(); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	if err := runStartupMigrations(s); err != nil {
		return fmt.Errorf("startup migrations: %w", err)
	}

	engine := query.NewSQLiteEngine(s.DB())

	// Resolve anchor message (numeric or Gmail hex), mirroring show-message.
	var anchor *query.MessageDetail
	if id, perr := strconv.ParseInt(idStr, 10, 64); perr == nil {
		anchor, err = engine.GetMessage(cmd.Context(), id)
		if err != nil {
			return fmt.Errorf("get message: %w", err)
		}
	}
	if anchor == nil {
		anchor, err = engine.GetMessageBySourceID(cmd.Context(), idStr)
		if err != nil {
			return fmt.Errorf("get message: %w", err)
		}
	}
	if anchor == nil {
		return fmt.Errorf("message not found: %s", idStr)
	}
	if anchor.ConversationID == 0 {
		return fmt.Errorf("message %d has no conversation/thread", anchor.ID)
	}

	limit := showThreadLimit
	if limit <= 0 {
		limit = defaultShowThreadLimit
	}

	convID := anchor.ConversationID
	filter := query.MessageFilter{
		ConversationID: &convID,
		Sorting:        query.MessageSorting{Field: query.MessageSortByDate, Direction: query.SortAsc},
		Pagination:     query.Pagination{Limit: limit + 1}, // +1 to detect truncation
	}
	msgs, err := engine.ListMessages(cmd.Context(), filter)
	if err != nil {
		return fmt.Errorf("list thread messages: %w", err)
	}
	truncated := false
	if len(msgs) > limit {
		msgs = msgs[:limit]
		truncated = true
	}

	if showThreadJSON {
		return outputThreadJSON(anchor, msgs, truncated)
	}
	return outputThreadText(anchor, msgs, truncated)
}

func outputThreadText(anchor *query.MessageDetail, msgs []query.MessageSummary, truncated bool) error {
	fmt.Println("═══════════════════════════════════════════════════════════════════════════════")
	fmt.Printf("Thread: %s\n", anchor.Subject)
	fmt.Printf("Conversation ID: %d  (source: %s)\n", anchor.ConversationID, anchor.SourceConversationID)
	fmt.Printf("Messages: %d", len(msgs))
	if truncated {
		fmt.Printf(" (truncated; use --limit to raise)")
	}
	fmt.Println()
	fmt.Println("───────────────────────────────────────────────────────────────────────────────")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tDATE\tFROM\tSUBJECT\tSIZE")
	for _, m := range msgs {
		date := m.SentAt.Format("2006-01-02 15:04")
		from := truncate(m.FromEmail, 32)
		subj := truncate(m.Subject, 50)
		size := formatSize(m.SizeEstimate)
		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", m.ID, date, from, subj, size)
	}
	_ = w.Flush()
	return nil
}

func outputThreadJSON(anchor *query.MessageDetail, msgs []query.MessageSummary, truncated bool) error {
	rows := make([]map[string]any, len(msgs))
	for i, m := range msgs {
		rows[i] = map[string]any{
			"id":                m.ID,
			"source_message_id": m.SourceMessageID,
			"subject":           m.Subject,
			"snippet":           m.Snippet,
			"from_email":        m.FromEmail,
			"from_name":         m.FromName,
			"sent_at":           m.SentAt.Format(time.RFC3339),
			"size_estimate":     m.SizeEstimate,
			"has_attachments":   m.HasAttachments,
			"attachment_count":  m.AttachmentCount,
			"labels":            m.Labels,
		}
	}
	output := map[string]any{
		"schema_version":         SchemaVersion,
		"conversation_id":        anchor.ConversationID,
		"source_conversation_id": anchor.SourceConversationID,
		"subject":                anchor.Subject,
		"message_count":          len(msgs),
		"truncated":              truncated,
		"messages":               rows,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

func init() {
	showThreadCmd.Flags().BoolVar(&showThreadJSON, "json", false, "Output as JSON")
	showThreadCmd.Flags().IntVar(&showThreadLimit, "limit", defaultShowThreadLimit, "Maximum messages to return")
}

// RegisterShowThread registers the command with the given root.
// Both binaries' main() call this; show-thread is agent-safe.
func RegisterShowThread(root *cobra.Command) {
	root.AddCommand(showThreadCmd)
}
