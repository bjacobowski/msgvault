package hybridapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/wesm/msgvault/internal/store"
	"github.com/wesm/msgvault/internal/vector"
	"github.com/wesm/msgvault/internal/vector/hybrid"
)

// fakeBackend is the smallest possible vector.Backend stub. Tests
// either set active to drive a happy-path Search, or leave it nil to
// drive ErrNotEnabled, or set building to drive ErrIndexBuilding.
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

// fakeEmbedder returns a deterministic single vector per input.
type fakeEmbedder struct{ dim int }

func (e fakeEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	for i := range inputs {
		v := make([]float32, e.dim)
		v[0] = 1
		out[i] = v
	}
	return out, nil
}

// fakeHydrator returns canned summaries unless hydrateErr is set.
type fakeHydrator struct {
	rows        []store.APIMessage
	hydrateErr  error
	lastIDQuery []int64
}

func (h *fakeHydrator) GetMessagesSummariesByIDs(ids []int64) ([]store.APIMessage, error) {
	h.lastIDQuery = append([]int64(nil), ids...)
	if h.hydrateErr != nil {
		return nil, h.hydrateErr
	}
	byID := make(map[int64]store.APIMessage, len(h.rows))
	for _, r := range h.rows {
		byID[r.ID] = r
	}
	out := make([]store.APIMessage, 0, len(ids))
	for _, id := range ids {
		if r, ok := byID[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newEngine(b vector.Backend) *hybrid.Engine {
	return hybrid.NewEngine(b, nil, fakeEmbedder{dim: 4}, hybrid.Config{
		ExpectedFingerprint: "fake:4",
		RRFK:                60,
		KPerSignal:          10,
	})
}

func TestRun_SuccessReturnsHydratedHitsInOrder(t *testing.T) {
	backend := &fakeBackend{
		active: &vector.Generation{
			ID: 1, Model: "fake", Dimension: 4,
			Fingerprint: "fake:4", State: vector.GenerationActive,
		},
		searchHits: []vector.Hit{
			{MessageID: 10, Score: 0.9, Rank: 1},
			{MessageID: 20, Score: 0.8, Rank: 2},
		},
	}
	hyd := &fakeHydrator{rows: []store.APIMessage{
		{ID: 10, Subject: "first"},
		{ID: 20, Subject: "second"},
	}}

	res, err := Run(context.Background(), newEngine(backend), hyd, discardLogger(), Request{
		Query: "anything",
		Mode:  hybrid.ModeVector,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := len(res.Msgs), 2; got != want {
		t.Fatalf("Msgs len = %d, want %d", got, want)
	}
	if res.Msgs[0].ID != 10 || res.Msgs[1].ID != 20 {
		t.Errorf("rank order broken: %+v", res.Msgs)
	}
	if res.Meta.Generation.ID != 1 {
		t.Errorf("generation not propagated: %+v", res.Meta.Generation)
	}
	if res.ScoresByID != nil {
		t.Errorf("ScoresByID should be nil when Explain=false; got %v", res.ScoresByID)
	}
	if len(hyd.lastIDQuery) != 2 {
		t.Errorf("hydration must request every hit id in one call; got %v", hyd.lastIDQuery)
	}
}

func TestRun_ExplainPopulatesScores(t *testing.T) {
	backend := &fakeBackend{
		active: &vector.Generation{
			ID: 1, Model: "fake", Dimension: 4,
			Fingerprint: "fake:4", State: vector.GenerationActive,
		},
		searchHits: []vector.Hit{{MessageID: 10, Score: 0.9, Rank: 1}},
	}
	hyd := &fakeHydrator{rows: []store.APIMessage{{ID: 10}}}

	res, err := Run(context.Background(), newEngine(backend), hyd, discardLogger(), Request{
		Query:   "anything",
		Mode:    hybrid.ModeVector,
		Limit:   10,
		Explain: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	sb, ok := res.ScoresByID[10]
	if !ok || sb == nil {
		t.Fatalf("ScoresByID missing entry for hit; got %v", res.ScoresByID)
	}
	if sb.Vector == nil {
		t.Errorf("vector score must be present for mode=vector hit; got %+v", sb)
	}
	if sb.RRF != nil {
		t.Errorf("rrf must be absent for single-signal mode=vector; got %+v", sb.RRF)
	}
}

func TestRun_MissingFreeText(t *testing.T) {
	backend := &fakeBackend{}
	_, err := Run(context.Background(), newEngine(backend), &fakeHydrator{}, discardLogger(), Request{
		Query: "from:alice@example.com", // operator-only, no free text terms
		Mode:  hybrid.ModeHybrid,
		Limit: 10,
	})
	if !errors.Is(err, ErrMissingFreeText) {
		t.Errorf("err = %v, want ErrMissingFreeText", err)
	}
}

func TestRun_PassesThroughVectorSentinels(t *testing.T) {
	cases := []struct {
		name    string
		backend *fakeBackend
		wantErr error
	}{
		{
			name:    "ErrNotEnabled when no active and no building",
			backend: &fakeBackend{},
			wantErr: vector.ErrNotEnabled,
		},
		{
			name: "ErrIndexBuilding when no active but building present",
			backend: &fakeBackend{
				building: &vector.Generation{
					ID: 1, Model: "fake", Dimension: 4,
					Fingerprint: "fake:4", State: vector.GenerationBuilding,
				},
			},
			wantErr: vector.ErrIndexBuilding,
		},
		{
			name: "ErrIndexStale when active fingerprint differs",
			backend: &fakeBackend{
				active: &vector.Generation{
					ID: 1, Model: "wrong", Dimension: 8,
					Fingerprint: "wrong:8", State: vector.GenerationActive,
				},
			},
			wantErr: vector.ErrIndexStale,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Run(context.Background(), newEngine(c.backend), &fakeHydrator{}, discardLogger(), Request{
				Query: "anything",
				Mode:  hybrid.ModeVector,
				Limit: 10,
			})
			if !errors.Is(err, c.wantErr) {
				t.Errorf("err = %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestRun_HydrationFailureReturnsEmptyMsgsNoError(t *testing.T) {
	// Regression guard: the v1 /api/v1/search handler logged-and-swallowed
	// hydration failures, returning 200 with an empty results set rather
	// than 500. hybridapi.Run must preserve that contract — otherwise the
	// v1 wire shape changes when the hot path is rewired through this
	// package.
	backend := &fakeBackend{
		active: &vector.Generation{
			ID: 1, Model: "fake", Dimension: 4,
			Fingerprint: "fake:4", State: vector.GenerationActive,
		},
		searchHits: []vector.Hit{{MessageID: 10, Score: 0.9, Rank: 1}},
	}
	hyd := &fakeHydrator{hydrateErr: errors.New("transient db glitch")}

	res, err := Run(context.Background(), newEngine(backend), hyd, discardLogger(), Request{
		Query: "anything",
		Mode:  hybrid.ModeVector,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("hydration failure must not escalate to error; got %v", err)
	}
	if len(res.Msgs) != 0 {
		t.Errorf("hydration failure must produce empty Msgs; got %d", len(res.Msgs))
	}
	// Generation/meta still expected — the search itself succeeded.
	if res.Meta.Generation.ID != 1 {
		t.Errorf("meta should still propagate from a successful Search call; got %+v", res.Meta.Generation)
	}
}

func TestRun_DropsHitsThatVanishedBetweenSearchAndHydration(t *testing.T) {
	// Engine returns three hits, but only two rows hydrate (one row was
	// deleted between Search and the bulk lookup). Run must drop the
	// orphan hit silently — same effect as the v1 per-hit lookup
	// returning nil.
	backend := &fakeBackend{
		active: &vector.Generation{
			ID: 1, Model: "fake", Dimension: 4,
			Fingerprint: "fake:4", State: vector.GenerationActive,
		},
		searchHits: []vector.Hit{
			{MessageID: 10, Score: 0.9, Rank: 1},
			{MessageID: 20, Score: 0.8, Rank: 2}, // disappears
			{MessageID: 30, Score: 0.7, Rank: 3},
		},
	}
	hyd := &fakeHydrator{rows: []store.APIMessage{
		{ID: 10, Subject: "first"},
		{ID: 30, Subject: "third"},
	}}

	res, err := Run(context.Background(), newEngine(backend), hyd, discardLogger(), Request{
		Query: "anything",
		Mode:  hybrid.ModeVector,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := len(res.Msgs), 2; got != want {
		t.Fatalf("Msgs len = %d, want %d (orphan must be dropped)", got, want)
	}
	if res.Msgs[0].ID != 10 || res.Msgs[1].ID != 30 {
		t.Errorf("rank order broken: %+v", res.Msgs)
	}
}
