package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"retrieval-service/internal/domain"
)

type Strategy string

const (
	StrategyVector Strategy = "vector"
	StrategyGraph  Strategy = "graph_local"
	StrategyHybrid Strategy = "hybrid"

	EmbeddingDimension = 384
	MaxSearchLimit     = 100
	defaultRRFConstant = 60
)

var (
	ErrInvalidRequest    = errors.New("invalid search request")
	ErrInvalidDependency = errors.New("search dependency is nil")
	ErrInvalidResult     = errors.New("invalid search result")
	ErrInvalidQueryData  = errors.New("query processor returned invalid data")
)

type SearchRequest struct {
	Query      string
	Strategy   Strategy
	Limit      int
	GraphDepth int
}

type QueryData struct {
	Embedding []float32
	Entities  []string
}

type QueryProcessor interface {
	Process(ctx context.Context, query string) (QueryData, error)
}

type VectorSearcher interface {
	Search(ctx context.Context, embedding []float32, limit int) ([]domain.SearchResult, error)
}

type GraphSearcher interface {
	Search(ctx context.Context, entities []string, depth, limit int) (domain.GraphSearchResults, error)
}

type SearchUseCase struct {
	processor QueryProcessor
	vector    VectorSearcher
	graph     GraphSearcher
}

func NewSearchUseCase(
	processor QueryProcessor,
	vector VectorSearcher,
	graph GraphSearcher,
) (*SearchUseCase, error) {
	if processor == nil || vector == nil || graph == nil {
		return nil, ErrInvalidDependency
	}

	return &SearchUseCase{
		processor: processor,
		vector:    vector,
		graph:     graph,
	}, nil
}

func (u *SearchUseCase) Search(ctx context.Context, request SearchRequest) (domain.SearchResponse, error) {
	if ctx == nil {
		return domain.SearchResponse{}, fmt.Errorf("%w: context is nil", ErrInvalidRequest)
	}
	if err := validateRequest(request); err != nil {
		return domain.SearchResponse{}, err
	}
	request.Query = strings.TrimSpace(request.Query)

	switch request.Strategy {
	case StrategyVector:
		return u.vectorSearch(ctx, request)
	case StrategyGraph:
		return u.graphSearch(ctx, request)
	case StrategyHybrid:
		return u.hybridSearch(ctx, request)
	default:
		return domain.SearchResponse{}, fmt.Errorf("%w: unsupported strategy %q", ErrInvalidRequest, request.Strategy)
	}
}

func validateRequest(request SearchRequest) error {
	if strings.TrimSpace(request.Query) == "" {
		return fmt.Errorf("%w: query must not be empty", ErrInvalidRequest)
	}
	if request.Limit < 1 {
		return fmt.Errorf("%w: limit must be positive", ErrInvalidRequest)
	}
	if request.Limit > MaxSearchLimit {
		return fmt.Errorf("%w: limit must not exceed %d", ErrInvalidRequest, MaxSearchLimit)
	}
	if request.Strategy != StrategyVector && request.Strategy != StrategyGraph && request.Strategy != StrategyHybrid {
		return fmt.Errorf("%w: unsupported strategy %q", ErrInvalidRequest, request.Strategy)
	}
	if request.Strategy != StrategyVector && (request.GraphDepth < 1 || request.GraphDepth > 2) {
		return fmt.Errorf("%w: graph depth must be 1 or 2", ErrInvalidRequest)
	}
	return nil
}

func (u *SearchUseCase) vectorSearch(ctx context.Context, request SearchRequest) (domain.SearchResponse, error) {
	queryData, err := u.processor.Process(ctx, request.Query)
	if err != nil {
		return domain.SearchResponse{}, fmt.Errorf("process query: %w", err)
	}
	if len(queryData.Embedding) != EmbeddingDimension {
		return domain.SearchResponse{}, fmt.Errorf("%w: embedding has dimension %d, want %d", ErrInvalidQueryData, len(queryData.Embedding), EmbeddingDimension)
	}
	results, err := u.vector.Search(ctx, queryData.Embedding, request.Limit)
	if err != nil {
		return domain.SearchResponse{}, fmt.Errorf("vector search: %w", err)
	}
	results, err = setSource(results, domain.SourceVector)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	return domain.SearchResponse{
		Results:      results,
		GraphFacts:   []domain.GraphFact{},
		VectorStatus: "ok",
		GraphStatus:  "skipped",
	}, nil
}

func (u *SearchUseCase) graphSearch(ctx context.Context, request SearchRequest) (domain.SearchResponse, error) {
	queryData, err := u.processor.Process(ctx, request.Query)
	if err != nil {
		return domain.SearchResponse{}, fmt.Errorf("process query: %w", err)
	}
	if len(queryData.Entities) == 0 {
		return domain.SearchResponse{
			Results:      []domain.SearchResult{},
			GraphFacts:   []domain.GraphFact{},
			VectorStatus: "skipped",
			GraphStatus:  "skipped",
		}, nil
	}
	graphResults, err := u.graph.Search(ctx, queryData.Entities, request.GraphDepth, request.Limit)
	if err != nil {
		return domain.SearchResponse{}, fmt.Errorf("graph search: %w", err)
	}
	graphResults.Chunks, err = setSource(graphResults.Chunks, domain.SourceGraph)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	return domain.SearchResponse{
		Results:      graphResults.Chunks,
		GraphFacts:   graphResults.Facts,
		VectorStatus: "skipped",
		GraphStatus:  "ok",
	}, nil
}

func (u *SearchUseCase) hybridSearch(ctx context.Context, request SearchRequest) (domain.SearchResponse, error) {
	queryData, err := u.processor.Process(ctx, request.Query)
	if err != nil {
		return domain.SearchResponse{}, fmt.Errorf("process query: %w", err)
	}
	if len(queryData.Embedding) != EmbeddingDimension {
		return domain.SearchResponse{}, fmt.Errorf("%w: embedding has dimension %d, want %d", ErrInvalidQueryData, len(queryData.Embedding), EmbeddingDimension)
	}

	var vectorResults, graphResults []domain.SearchResult
	graphFacts := make([]domain.GraphFact, 0)
	var vectorErr, graphErr error
	var group sync.WaitGroup
	group.Add(2)

	go func() {
		defer group.Done()
		results, searchErr := u.vector.Search(ctx, queryData.Embedding, request.Limit)
		if searchErr != nil {
			vectorErr = fmt.Errorf("vector search: %w", searchErr)
			return
		}
		vectorResults, vectorErr = setSource(results, domain.SourceVector)
	}()

	go func() {
		defer group.Done()
		if len(queryData.Entities) == 0 {
			return
		}
		results, searchErr := u.graph.Search(ctx, queryData.Entities, request.GraphDepth, request.Limit)
		if searchErr != nil {
			graphErr = fmt.Errorf("graph search: %w", searchErr)
			return
		}
		graphResults, graphErr = setSource(results.Chunks, domain.SourceGraph)
		if graphErr == nil {
			graphFacts = results.Facts
		}
	}()

	group.Wait()
	if vectorErr != nil {
		slog.WarnContext(ctx, "vector search failed; returning partial hybrid results", "error", vectorErr)
	}
	if graphErr != nil {
		slog.WarnContext(ctx, "graph search failed; returning partial hybrid results", "error", graphErr)
	}
	if vectorErr != nil && graphErr != nil {
		return domain.SearchResponse{}, fmt.Errorf("hybrid search failed: %w", errors.Join(vectorErr, graphErr))
	}

	response := domain.SearchResponse{
		Results:      reciprocalRankFusion(vectorResults, graphResults, request.Limit),
		GraphFacts:   graphFacts,
		Partial:      vectorErr != nil || graphErr != nil,
		VectorStatus: searchStatus(vectorErr, true),
		GraphStatus:  searchStatus(graphErr, len(queryData.Entities) > 0),
	}
	return response, nil
}

func searchStatus(err error, attempted bool) string {
	if err != nil {
		return "error"
	}
	if !attempted {
		return "skipped"
	}
	return "ok"
}

func setSource(results []domain.SearchResult, source domain.Source) ([]domain.SearchResult, error) {
	normalized := make([]domain.SearchResult, len(results))
	for i, result := range results {
		if strings.TrimSpace(result.ID) == "" {
			return nil, fmt.Errorf("%w: result at rank %d has an empty ID", ErrInvalidResult, i+1)
		}
		result.Sources = []domain.Source{source}
		normalized[i] = result
	}
	return normalized, nil
}

func reciprocalRankFusion(vectorResults, graphResults []domain.SearchResult, limit int) []domain.SearchResult {
	type fusedResult struct {
		result domain.SearchResult
		score  float64
	}
	fused := make(map[string]*fusedResult, len(vectorResults)+len(graphResults))
	for _, ranking := range [][]domain.SearchResult{vectorResults, graphResults} {
		seen := make(map[string]struct{}, len(ranking))
		for index, result := range ranking {
			if _, ok := seen[result.ID]; ok {
				continue
			}
			seen[result.ID] = struct{}{}

			item, ok := fused[result.ID]
			if !ok {
				result.Metadata = cloneMetadata(result.Metadata)
				item = &fusedResult{result: result}
				fused[result.ID] = item
			} else {
				item.result.Sources = appendSource(item.result.Sources, result.Sources...)
				if item.result.Text == "" {
					item.result.Text = result.Text
				}
				if item.result.DocumentID == "" {
					item.result.DocumentID = result.DocumentID
				}
				for key, value := range result.Metadata {
					if _, exists := item.result.Metadata[key]; !exists {
						if item.result.Metadata == nil {
							item.result.Metadata = make(map[string]string)
						}
						item.result.Metadata[key] = value
					}
				}
			}
			item.score += 1 / float64(defaultRRFConstant+index+1)
		}
	}

	results := make([]domain.SearchResult, 0, len(fused))
	for _, item := range fused {
		item.result.Score = item.score
		results = append(results, item.result)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].ID < results[j].ID
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

func appendSource(sources []domain.Source, additions ...domain.Source) []domain.Source {
	for _, addition := range additions {
		found := false
		for _, source := range sources {
			if source == addition {
				found = true
				break
			}
		}
		if !found {
			sources = append(sources, addition)
		}
	}
	return sources
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
