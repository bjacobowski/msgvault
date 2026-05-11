package cmd

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	_ "github.com/marcboeker/go-duckdb"
	"github.com/spf13/cobra"
	"github.com/wesm/msgvault/internal/query"
)

var queryFormat string

var queryCmd = &cobra.Command{
	Use:   "query [sql]",
	Short: "Run a SQL query against the analytics cache",
	Long: `Run arbitrary SQL against the Parquet analytics cache.

The following views are available:
  messages, participants, message_recipients, labels,
  message_labels, attachments, conversations, sources

Convenience views:
  v_messages   - messages with resolved sender and labels
  v_senders    - per-sender aggregates
  v_domains    - per-domain aggregates
  v_labels     - label name with message count and size
  v_threads    - per-conversation aggregates

Output formats:
  json   - JSON object with columns, rows, row_count (default)
  csv    - CSV with header row
  table  - Aligned text table

Examples:
  msgvault query "SELECT from_email, COUNT(*) AS n FROM v_messages GROUP BY 1 ORDER BY 2 DESC LIMIT 10"
  msgvault query --format csv "SELECT * FROM v_senders ORDER BY message_count DESC"
  msgvault query --format table "SELECT name, message_count FROM v_labels"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath := cfg.DatabaseDSN()
		analyticsDir := cfg.AnalyticsDir()

		staleness := cacheNeedsBuild(dbPath, analyticsDir)
		if staleness.NeedsBuild {
			fmt.Fprintf(os.Stderr,
				"Building analytics cache (%s)...\n",
				staleness.Reason)
			result, err := buildCache(
				dbPath, analyticsDir, staleness.FullRebuild,
			)
			if err != nil {
				return fmt.Errorf("build cache: %w", err)
			}
			if !result.Skipped {
				fmt.Fprintf(os.Stderr,
					"Cached %d messages.\n",
					result.ExportedCount)
			}
		}

		if !query.HasCompleteParquetData(analyticsDir) {
			return fmt.Errorf(
				"analytics cache is empty — sync some " +
					"messages first")
		}

		return executeQuery(
			analyticsDir, args[0], queryFormat, os.Stdout,
		)
	},
}

// executeQuery opens an in-memory DuckDB, registers views over
// the Parquet files in analyticsDir, runs the SQL, and writes
// the results in the requested format. When IsAgentMode() is true
// the user SQL is restricted to read-only statements and the DuckDB
// session is locked down against filesystem writes.
func executeQuery(
	analyticsDir, sqlStr, format string, w io.Writer,
) error {
	if IsAgentMode() {
		if err := validateAgentSQL(sqlStr); err != nil {
			return err
		}
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		return fmt.Errorf("open duckdb: %w", err)
	}
	defer func() { _ = db.Close() }()

	db.SetMaxOpenConns(1)

	threads := runtime.GOMAXPROCS(0)
	if _, err := db.Exec(
		fmt.Sprintf("SET threads = %d", threads),
	); err != nil {
		return fmt.Errorf("set threads: %w", err)
	}

	if err := query.RegisterViews(db, analyticsDir); err != nil {
		return fmt.Errorf("register views: %w", err)
	}

	if IsAgentMode() {
		// Block network egress (HTTPFileSystem, S3FileSystem) and
		// any further config changes. LocalFileSystem stays enabled
		// so Parquet view reads still work; protection against
		// COPY ... TO 'file' is enforced by validateAgentSQL above.
		// Order matters: SET disabled_filesystems must run after
		// RegisterViews so view DDL completes, then lock_configuration
		// freezes the session against the user's own SET attempts.
		if _, err := db.Exec(
			"SET disabled_filesystems = 'HTTPFileSystem,S3FileSystem'",
		); err != nil {
			return fmt.Errorf("agent-mode lockdown: %w", err)
		}
		if _, err := db.Exec("SET lock_configuration = true"); err != nil {
			return fmt.Errorf("agent-mode lock: %w", err)
		}
	}

	rows, err := db.Query(sqlStr)
	if err != nil {
		return fmt.Errorf("execute query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("get columns: %w", err)
	}

	var allRows [][]any
	for rows.Next() {
		row, scanErr := scanRow(cols, rows)
		if scanErr != nil {
			return scanErr
		}
		allRows = append(allRows, row)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate rows: %w", err)
	}

	switch format {
	case "json":
		return writeJSON(w, cols, allRows)
	case "csv":
		return writeCSV(w, cols, allRows)
	case "table":
		return writeTable(w, cols, allRows)
	default:
		return fmt.Errorf("unknown format %q (use json, csv, or table)", format)
	}
}

// scanRow scans a single row into a slice of interface{} values,
// converting []byte to string for clean serialization.
func scanRow(
	cols []string, rows *sql.Rows,
) ([]any, error) {
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, fmt.Errorf("scan row: %w", err)
	}
	for i, v := range vals {
		if b, ok := v.([]byte); ok {
			vals[i] = string(b)
		}
	}
	return vals, nil
}

func writeJSON(
	w io.Writer, cols []string, rows [][]any,
) error {
	result := map[string]any{
		"schema_version": SchemaVersion,
		"columns":        cols,
		"rows":           rows,
		"row_count":      len(rows),
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// displayVal formats a value for CSV/table output. SQL NULLs
// become empty strings; other values use fmt.Sprintf.
func displayVal(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func writeCSV(
	w io.Writer, cols []string, rows [][]any,
) error {
	cw := csv.NewWriter(w)

	if err := cw.Write(cols); err != nil {
		return fmt.Errorf("write csv header: %w", err)
	}

	for _, row := range rows {
		record := make([]string, len(row))
		for i, v := range row {
			record[i] = displayVal(v)
		}
		if err := cw.Write(record); err != nil {
			return fmt.Errorf("write csv row: %w", err)
		}
	}

	cw.Flush()
	return cw.Error()
}

func writeTable(
	w io.Writer, cols []string, rows [][]any,
) error {
	// Convert all values to strings for width calculation
	strRows := make([][]string, len(rows))
	for i, row := range rows {
		strRows[i] = make([]string, len(row))
		for j, v := range row {
			strRows[i][j] = displayVal(v)
		}
	}

	// Calculate column widths (min = header length)
	widths := make([]int, len(cols))
	for i, col := range cols {
		widths[i] = len(col)
	}
	for _, row := range strRows {
		for i, val := range row {
			if len(val) > widths[i] {
				widths[i] = len(val)
			}
		}
	}

	// Print header
	for i, col := range cols {
		if i > 0 {
			_, _ = fmt.Fprint(w, "  ")
		}
		_, _ = fmt.Fprintf(w, "%-*s", widths[i], col)
	}
	_, _ = fmt.Fprintln(w)

	// Print separator
	for i, width := range widths {
		if i > 0 {
			_, _ = fmt.Fprint(w, "  ")
		}
		_, _ = fmt.Fprint(w, strings.Repeat("-", width))
	}
	_, _ = fmt.Fprintln(w)

	// Print rows
	for _, row := range strRows {
		for i, val := range row {
			if i > 0 {
				_, _ = fmt.Fprint(w, "  ")
			}
			_, _ = fmt.Fprintf(w, "%-*s", widths[i], val)
		}
		_, _ = fmt.Fprintln(w)
	}

	// Print row count
	_, _ = fmt.Fprintf(w, "(%d rows)\n", len(rows))
	return nil
}

func init() {
	queryCmd.Flags().StringVar(
		&queryFormat, "format", "json",
		"Output format: json, csv, or table",
	)
}

// RegisterQuery registers the command(s) defined in this file
// with the given root command. The agent and full-surface binaries
// call this to opt this command in.
func RegisterQuery(root *cobra.Command) {
	root.AddCommand(queryCmd)
}

// agentReadVerbs are SQL statement-leading keywords accepted in
// agent mode. Any other leading verb (or a WITH-CTE that contains a
// banned keyword as a word) is rejected before the SQL ever reaches
// DuckDB.
var agentReadVerbs = map[string]bool{
	"select":    true,
	"with":      true,
	"explain":   true,
	"show":      true,
	"describe":  true,
	"summarize": true,
}

// agentBannedKeywords are SQL keywords that must not appear anywhere
// in a WITH-prefixed statement. CTEs can be used with DML in DuckDB
// (e.g. WITH t AS (…) INSERT INTO …), so the leading-verb check
// alone is insufficient when the verb is WITH.
var agentBannedKeywords = []string{
	"insert", "update", "delete", "merge", "upsert",
	"create", "drop", "alter", "truncate", "rename",
	"copy", "export", "import",
	"attach", "detach", "install", "load",
	"set", "reset", "pragma", "vacuum", "checkpoint",
}

// validateAgentSQL rejects any SQL that isn't read-only. It strips
// line and block comments, requires a single statement (no internal
// semicolons), checks the first keyword against agentReadVerbs, and
// for WITH-prefixed statements scans for banned keywords as
// word-boundary matches in the remainder.
func validateAgentSQL(sqlStr string) error {
	stripped := stripSQLComments(sqlStr)
	stripped = strings.TrimSpace(stripped)
	if stripped == "" {
		return fmt.Errorf("empty SQL statement")
	}

	// Reject multi-statement input. Trailing ';' is fine but anything
	// between two non-whitespace runs is not.
	trimmed := strings.TrimRight(stripped, "; \t\n\r")
	if strings.ContainsRune(trimmed, ';') {
		return fmt.Errorf("agent mode rejects multi-statement input")
	}

	first := strings.ToLower(firstWord(trimmed))
	if !agentReadVerbs[first] {
		return fmt.Errorf(
			"agent mode rejects %q: only %s statements are allowed",
			first, agentReadVerbsList(),
		)
	}

	if first == "with" {
		lower := strings.ToLower(trimmed)
		for _, bad := range agentBannedKeywords {
			if containsWord(lower, bad) {
				return fmt.Errorf(
					"agent mode rejects WITH-statement: contains banned keyword %q",
					bad,
				)
			}
		}
	}

	return nil
}

// stripSQLComments removes -- line comments and /* … */ block
// comments. Comments inside string literals are left alone; this is
// adequate for the simple agent-mode allowlist check (an attacker
// who can craft a string literal that hides a banned keyword still
// has to get past the first-word check).
func stripSQLComments(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	i := 0
	inStr := byte(0)
	for i < len(s) {
		c := s[i]
		if inStr != 0 {
			out.WriteByte(c)
			if c == inStr && (i == 0 || s[i-1] != '\\') {
				inStr = 0
			}
			i++
			continue
		}
		if c == '\'' || c == '"' {
			inStr = c
			out.WriteByte(c)
			i++
			continue
		}
		if c == '-' && i+1 < len(s) && s[i+1] == '-' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i += 2
			continue
		}
		out.WriteByte(c)
		i++
	}
	return out.String()
}

func firstWord(s string) string {
	for i, c := range s {
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '(' {
			return s[:i]
		}
	}
	return s
}

func containsWord(s, word string) bool {
	for {
		idx := strings.Index(s, word)
		if idx == -1 {
			return false
		}
		left := idx == 0 || !isIdentChar(s[idx-1])
		right := idx+len(word) == len(s) || !isIdentChar(s[idx+len(word)])
		if left && right {
			return true
		}
		s = s[idx+len(word):]
	}
}

func isIdentChar(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '_'
}

func agentReadVerbsList() string {
	verbs := make([]string, 0, len(agentReadVerbs))
	for v := range agentReadVerbs {
		verbs = append(verbs, strings.ToUpper(v))
	}
	// Stable order so error messages are deterministic in tests.
	sortedJoin := strings.Join(sortStrings(verbs), ", ")
	return sortedJoin
}

func sortStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
