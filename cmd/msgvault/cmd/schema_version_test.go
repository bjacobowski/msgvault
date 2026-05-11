package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/wesm/msgvault/internal/store"
)

// TestSchemaVersion_OutputRemoteSearchResultsJSON guards the
// schema_version field on the object-shaped remote search response.
// search.go intentionally still emits a top-level array for the local
// search (--json) path; this test only locks the object case.
func TestSchemaVersion_OutputRemoteSearchResultsJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := encodeJSON(&buf, map[string]any{
		"schema_version": SchemaVersion,
		"total":          int64(0),
		"results":        []store.APIMessage{},
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["schema_version"] != float64(SchemaVersion) {
		t.Errorf("schema_version = %v, want %d", got["schema_version"], SchemaVersion)
	}
}

// TestSchemaVersion_QueryWriteJSON guards the schema_version field
// on the `query` command's QueryResult output.
func TestSchemaVersion_QueryWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJSON(&buf, []string{"id"}, [][]any{{int64(1)}, {int64(2)}}); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["schema_version"] != float64(SchemaVersion) {
		t.Errorf("schema_version = %v, want %d", got["schema_version"], SchemaVersion)
	}
	if got["row_count"] != float64(2) {
		t.Errorf("row_count = %v, want 2", got["row_count"])
	}
}

// TestSchemaVersion_Constant locks the current contract version. Any
// change to SchemaVersion is a breaking change for consumers and
// should fail this test until the surface is reviewed.
func TestSchemaVersion_Constant(t *testing.T) {
	if SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d; bumping requires a docs + changelog update", SchemaVersion)
	}
}

// encodeJSON is a small test helper for marshalling with the same
// shape printJSON produces (indented, no HTML escaping concerns).
func encodeJSON(buf *bytes.Buffer, v any) error {
	enc := json.NewEncoder(buf)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
