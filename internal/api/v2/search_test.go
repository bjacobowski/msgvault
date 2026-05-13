package v2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wesm/msgvault/internal/store"
	"github.com/wesm/msgvault/internal/vector"
	"github.com/wesm/msgvault/internal/vector/hybrid"
)

// fakeBackend is a minimal vector.Backend stub for v2 search tests.
// Mirrors the one in internal/api but trimmed to what these tests
// need.
type fakeBackend struct {
	active     *vector.Generation
	building   *vector.Generation
	searchHits []vector.Hit
}

func (f *fakeBackend) CreateGeneration(_ context.Context, _ string, _ int) (vector.GenerationID, error) {
	return 0, errors.New("not implemented")
}
func (f *fakeBackend) ActivateGeneration(_ context.Context, _ vector.GenerationID) error {
	return errors.New("not implemented")
}
func (f *fakeBackend) RetireGeneration(_ context.Context, _ vector.GenerationID) error {
	return errors.New("not implemented")
}
func (f *fakeBackend) ActiveGeneration(_ context.Context) (vector.Generation, error) {
	if f.active == nil {
		return vector.Generation{}, vector.ErrNoActiveGeneration
	}
	return *f.active, nil
}
func (f *fakeBackend) BuildingGeneration(_ context.Context) (*vector.Generation, error) {
	return f.building, nil
}
func (f *fakeBackend) Upsert(_ context.Context, _ vector.GenerationID, _ []vector.Chunk) error {
	return errors.New("not implemented")
}
func (f *fakeBackend) Search(_ context.Context, _ vector.GenerationID, _ []float32, _ int, _ vector.Filter) ([]vector.Hit, error) {
	return f.searchHits, nil
}
func (f *fakeBackend) Delete(_ context.Context, _ vector.GenerationID, _ []int64) error {
	return errors.New("not implemented")
}
func (f *fakeBackend) Stats(_ context.Context, _ vector.GenerationID) (vector.Stats, error) {
	return vector.Stats{}, nil
}
func (f *fakeBackend) LoadVector(_ context.Context, _ int64) ([]float32, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeBackend) Close() error { return nil }
func (f *fakeBackend) EnsureSeeded(_ context.Context, _ vector.GenerationID) error {
	return nil
}

type fakeEmbedder struct{}

func (fakeEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	for i := range inputs {
		v := make([]float32, 4)
		v[0] = 1
		out[i] = v
	}
	return out, nil
}

func newTestEngine(b vector.Backend) *hybrid.Engine {
	return hybrid.NewEngine(b, nil, fakeEmbedder{}, hybrid.Config{
		ExpectedFingerprint: "fake:4",
		RRFK:                60,
		KPerSignal:          10,
	})
}

// decodeJSON unmarshals the response body into a map so tests can
// assert on the exact top-level key set as the wire shape — not just
// that struct decoding succeeded. A typed decode like SearchResponse{}
// would happily ignore extra keys; a map-decode catches "we silently
// added a field".
func decodeJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	return m
}

func keySet(m map[string]any) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func assertKeys(t *testing.T, got map[string]bool, want []string) {
	t.Helper()
	for _, k := range want {
		if !got[k] {
			t.Errorf("missing top-level key %q in response", k)
		}
	}
	wantSet := make(map[string]bool, len(want))
	for _, k := range want {
		wantSet[k] = true
	}
	for k := range got {
		if !wantSet[k] {
			t.Errorf("unexpected top-level key %q in response", k)
		}
	}
}

func TestSearch_FTSShape(t *testing.T) {
	ms := &mockStore{
		messages: []store.APIMessage{{ID: 1, Subject: "Hi"}},
		total:    42,
	}
	ms.structuredRecipients = map[int64]*store.APIRecipientsV2{
		1: {From: &store.APIAddress{Address: "sender@example.com"}},
	}
	r := newTestRouter(t, ms)

	req := httptest.NewRequest("GET", "/api/v2/search?q=invoice&mode=fts&limit=5", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-MsgVault-API"); got != "v2" {
		t.Errorf("X-MsgVault-API = %q, want v2", got)
	}

	resp := decodeJSON(t, w)
	assertKeys(t, keySet(resp), []string{
		"query", "mode", "offset", "limit", "total", "returned", "messages",
	})

	if resp["mode"] != "fts" {
		t.Errorf("mode = %v, want fts", resp["mode"])
	}
	if resp["total"] == nil {
		t.Errorf("fts mode must emit a numeric total, not null")
	}
	if v, _ := resp["total"].(float64); int64(v) != 42 {
		t.Errorf("total = %v, want 42", resp["total"])
	}
	if v, _ := resp["returned"].(float64); int(v) != 1 {
		t.Errorf("returned = %v, want 1", resp["returned"])
	}

	// Confirm the message preserves the v2 summary shape.
	msgs, _ := resp["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	m0 := msgs[0].(map[string]any)
	from, _ := m0["from"].(map[string]any)
	if from["address"] != "sender@example.com" {
		t.Errorf("from not enriched: %+v", m0)
	}
}

func TestSearch_FTSWithOperators(t *testing.T) {
	// from:alice@example.com is a structured operator that v1 routes
	// through SearchMessagesQuery. v2 must match — otherwise the same
	// query has divergent semantics across the two surfaces.
	ms := &mockStore{
		messages: []store.APIMessage{{ID: 1}},
		total:    1,
	}
	r := newTestRouter(t, ms)

	req := httptest.NewRequest("GET", "/api/v2/search?q=from:alice@example.com+invoice&mode=fts", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if ms.searchMessagesQueryCalls != 1 || ms.searchMessagesCalls != 0 {
		t.Errorf("operator query must route through SearchMessagesQuery: q=%d simple=%d",
			ms.searchMessagesQueryCalls, ms.searchMessagesCalls)
	}
}

func TestSearch_HybridShape(t *testing.T) {
	ms := &mockStore{
		messages: []store.APIMessage{{ID: 10, Subject: "match"}},
	}
	ms.structuredRecipients = map[int64]*store.APIRecipientsV2{
		10: {From: &store.APIAddress{Address: "alice@example.com"}},
	}
	backend := &fakeBackend{
		active: &vector.Generation{
			ID: 1, Model: "fake", Dimension: 4,
			Fingerprint: "fake:4", State: vector.GenerationActive,
		},
		searchHits: []vector.Hit{{MessageID: 10, Score: 0.9, Rank: 1}},
	}
	r := newTestRouterWithEngine(t, ms, newTestEngine(backend), vector.Config{})

	// mode=vector exercises the same v2 response envelope as
	// mode=hybrid; the shape contract is identical. Using vector
	// keeps the test free of the FusingBackend dependency that
	// mode=hybrid requires from the vector.Backend implementation.
	req := httptest.NewRequest("GET", "/api/v2/search?q=invoice&mode=vector&explain=1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	resp := decodeJSON(t, w)
	assertKeys(t, keySet(resp), []string{
		"query", "mode", "offset", "limit", "total", "returned", "messages",
		"generation", "pool_saturated", "took_ms",
	})

	if resp["mode"] != "vector" {
		t.Errorf("mode = %v, want vector", resp["mode"])
	}
	if resp["total"] != nil {
		t.Errorf("vector/hybrid total must be JSON null; got %v", resp["total"])
	}
	if v, _ := resp["returned"].(float64); int(v) != 1 {
		t.Errorf("returned = %v, want 1", resp["returned"])
	}

	gen, _ := resp["generation"].(map[string]any)
	if gen == nil || gen["model"] != "fake" {
		t.Errorf("generation not propagated: %+v", resp["generation"])
	}

	msgs, _ := resp["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	m0 := msgs[0].(map[string]any)
	score, _ := m0["score"].(map[string]any)
	if score == nil {
		t.Errorf("explain=1 must populate per-hit score; got nil")
	} else if _, ok := score["vector"]; !ok {
		t.Errorf("score must include vector signal for mode=vector|hybrid; got %+v", score)
	}
}

func TestSearch_HybridShape_NoExplainHasNoScore(t *testing.T) {
	// Without explain=1, the score field must be omitted entirely
	// (omitempty), not emitted as null.
	ms := &mockStore{
		messages: []store.APIMessage{{ID: 10}},
	}
	backend := &fakeBackend{
		active: &vector.Generation{
			ID: 1, Model: "fake", Dimension: 4,
			Fingerprint: "fake:4", State: vector.GenerationActive,
		},
		searchHits: []vector.Hit{{MessageID: 10, Score: 0.9, Rank: 1}},
	}
	r := newTestRouterWithEngine(t, ms, newTestEngine(backend), vector.Config{})

	req := httptest.NewRequest("GET", "/api/v2/search?q=invoice&mode=vector", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := decodeJSON(t, w)
	msgs, _ := resp["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("empty messages")
	}
	m0 := msgs[0].(map[string]any)
	if _, present := m0["score"]; present {
		t.Errorf("score must be absent without explain=1; got %v", m0["score"])
	}
}

func TestSearch_HybridNotConfigured(t *testing.T) {
	r := newTestRouter(t, &mockStore{}) // no engine

	req := httptest.NewRequest("GET", "/api/v2/search?q=invoice&mode=hybrid", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	var er ErrorResponse
	_ = json.NewDecoder(w.Body).Decode(&er)
	if er.Error != "vector_not_enabled" {
		t.Errorf("error = %q, want vector_not_enabled", er.Error)
	}
}

func TestSearch_PaginationRejected(t *testing.T) {
	// vector/hybrid reject offset > 0 with pagination_unsupported and
	// a message that explains why. The rejection comes before any
	// engine work — even without a configured engine, the response
	// should be 400 pagination_unsupported, not 503 vector_not_enabled.
	r := newTestRouter(t, &mockStore{})

	req := httptest.NewRequest("GET", "/api/v2/search?q=invoice&mode=hybrid&offset=10", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	var er ErrorResponse
	_ = json.NewDecoder(w.Body).Decode(&er)
	if er.Error != "pagination_unsupported" {
		t.Errorf("error = %q, want pagination_unsupported", er.Error)
	}
	// The message must explain the reason, not just say "not supported"
	// — clients need to learn the rule from the first bad request.
	if er.Message == "" {
		t.Errorf("error message must explain why pagination is unsupported; got empty")
	}
}

func TestSearch_MissingFreeText(t *testing.T) {
	backend := &fakeBackend{
		active: &vector.Generation{
			ID: 1, Model: "fake", Dimension: 4,
			Fingerprint: "fake:4", State: vector.GenerationActive,
		},
	}
	r := newTestRouterWithEngine(t, &mockStore{}, newTestEngine(backend), vector.Config{})

	// from:alice@... is a structured operator. With no free-text term
	// for the embedder, the engine can't run; v2 should return 400
	// missing_free_text rather than fall through to a 500.
	req := httptest.NewRequest("GET", "/api/v2/search?q=from:alice@example.com&mode=hybrid", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	var er ErrorResponse
	_ = json.NewDecoder(w.Body).Decode(&er)
	if er.Error != "missing_free_text" {
		t.Errorf("error = %q, want missing_free_text", er.Error)
	}
}

func TestSearch_IndexBuilding(t *testing.T) {
	backend := &fakeBackend{
		building: &vector.Generation{
			ID: 1, Model: "fake", Dimension: 4,
			Fingerprint: "fake:4", State: vector.GenerationBuilding,
		},
	}
	r := newTestRouterWithEngine(t, &mockStore{}, newTestEngine(backend), vector.Config{})

	req := httptest.NewRequest("GET", "/api/v2/search?q=invoice&mode=hybrid", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	var er ErrorResponse
	_ = json.NewDecoder(w.Body).Decode(&er)
	if er.Error != "index_building" {
		t.Errorf("error = %q, want index_building", er.Error)
	}
}

func TestSearch_InvalidMode(t *testing.T) {
	r := newTestRouter(t, &mockStore{messages: []store.APIMessage{{ID: 1}}, total: 1})
	req := httptest.NewRequest("GET", "/api/v2/search?q=invoice&mode=nonsense", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}
