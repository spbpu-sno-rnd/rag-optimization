package usecase

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"retrieval-service/internal/domain"
)

type testProcessor struct {
	data QueryData
	err  error
}

func (p testProcessor) Process(context.Context, string) (QueryData, error) {
	return p.data, p.err
}

type testVectorSearcher struct {
	results []domain.SearchResult
	err     error
}

func (s testVectorSearcher) Search(context.Context, []float32, int) ([]domain.SearchResult, error) {
	return s.results, s.err
}

type testGraphSearcher struct {
	results []domain.SearchResult
	err     error
}

func testEmbedding() []float32 {
	return make([]float32, EmbeddingDimension)
}

func (s testGraphSearcher) Search(context.Context, []string, int, int) ([]domain.SearchResult, error) {
	return s.results, s.err
}

func newTestUseCase(t *testing.T, processor QueryProcessor, vector VectorSearcher, graph GraphSearcher) *SearchUseCase {
	t.Helper()
	service, err := NewSearchUseCase(processor, vector, graph)
	if err != nil {
		t.Fatalf("NewSearchUseCase() error = %v", err)
	}
	return service
}

func TestSearchVector(t *testing.T) {
	want := []domain.SearchResult{{ID: "chunk-1", Text: "result", Score: 0.9}}
	service := newTestUseCase(t, testProcessor{data: QueryData{Embedding: testEmbedding()}}, testVectorSearcher{results: want}, testGraphSearcher{})

	got, err := service.Search(context.Background(), SearchRequest{
		Query: "question", Strategy: StrategyVector, Limit: 3,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if got[0].Score != 0.9 || !reflect.DeepEqual(got[0].Sources, []domain.Source{domain.SourceVector}) {
		t.Fatalf("Search() result = %#v", got[0])
	}
}

func TestSearchGraphWithoutEntities(t *testing.T) {
	service := newTestUseCase(t, testProcessor{}, testVectorSearcher{}, testGraphSearcher{})

	got, err := service.Search(context.Background(), SearchRequest{
		Query: "question", Strategy: StrategyGraph, Limit: 3, GraphDepth: 1,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search() results = %#v, want none", got)
	}
}

func TestSearchHybridFusesRankings(t *testing.T) {
	service := newTestUseCase(
		t,
		testProcessor{data: QueryData{Embedding: testEmbedding(), Entities: []string{"Acme"}}},
		testVectorSearcher{results: []domain.SearchResult{
			{ID: "shared", Text: "shared result", Score: 0.9},
			{ID: "vector-only", Text: "vector result", Score: 0.8},
		}},
		testGraphSearcher{results: []domain.SearchResult{
			{ID: "shared", Text: "shared result"},
			{ID: "graph-only", Text: "graph result"},
		}},
	)

	got, err := service.Search(context.Background(), SearchRequest{
		Query: "question", Strategy: StrategyHybrid, Limit: 3, GraphDepth: 2,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Search() returned %d results, want 3", len(got))
	}
	if got[0].ID != "shared" || !reflect.DeepEqual(got[0].Sources, []domain.Source{domain.SourceVector, domain.SourceGraph}) {
		t.Fatalf("top fused result = %#v", got[0])
	}
	if got[1].ID != "graph-only" || got[2].ID != "vector-only" {
		t.Fatalf("fused result ordering = %q, %q, %q", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestSearchRejectsInvalidRequest(t *testing.T) {
	service := newTestUseCase(t, testProcessor{}, testVectorSearcher{}, testGraphSearcher{})
	tests := []SearchRequest{
		{Strategy: StrategyVector, Limit: 1},
		{Query: "question", Strategy: StrategyVector},
		{Query: "question", Strategy: StrategyVector, Limit: MaxSearchLimit + 1},
		{Query: "question", Strategy: StrategyGraph, Limit: 1, GraphDepth: 3},
		{Query: "question", Strategy: "unknown", Limit: 1},
	}

	for _, request := range tests {
		if _, err := service.Search(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("Search(%+v) error = %v, want ErrInvalidRequest", request, err)
		}
	}
}

func TestSearchPropagatesVectorError(t *testing.T) {
	wantErr := errors.New("vector database unavailable")
	service := newTestUseCase(t, testProcessor{data: QueryData{Embedding: testEmbedding()}}, testVectorSearcher{err: wantErr}, testGraphSearcher{})

	_, err := service.Search(context.Background(), SearchRequest{
		Query: "question", Strategy: StrategyVector, Limit: 1,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Search() error = %v, want wrapped vector error", err)
	}
}

func TestSearchRejectsUnexpectedEmbeddingDimension(t *testing.T) {
	service := newTestUseCase(t, testProcessor{data: QueryData{Embedding: []float32{0.1}}}, testVectorSearcher{}, testGraphSearcher{})

	_, err := service.Search(context.Background(), SearchRequest{
		Query: "question", Strategy: StrategyVector, Limit: 1,
	})
	if !errors.Is(err, ErrInvalidQueryData) {
		t.Fatalf("Search() error = %v, want ErrInvalidQueryData", err)
	}
}
