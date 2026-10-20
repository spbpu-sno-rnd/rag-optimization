package usecase

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"

	"retrieval-service/internal/domain"
)

type Strategy string

const (
	StrategyVector Strategy = "vector"
	StrategyGraph  Strategy = "graph_local"
	StrategyHybrid Strategy = "hybrid"

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
	Search(ctx context.Context, entities []string, depth, limit int) ([]domain.SearchResult, error)
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

func (u *SearchUseCase) Search(ctx context.Context, request SearchRequest) ([]domain.SearchResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is nil", ErrInvalidRequest)
	}
	if err := validateRequest(request); err != nil {
		return nil, err
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
		return nil, fmt.Errorf("%w: unsupported strategy %q", ErrInvalidRequest, request.Strategy)
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

func (u *SearchUseCase) vectorSearch(ctx context.Context, request SearchRequest) ([]domain.SearchResult, error) {
	queryData, err := u.processor.Process(ctx, request.Query)
	if err != nil {
		return nil, fmt.Errorf("process query: %w", err)
	}
	if len(queryData.Embedding) == 0 {
		return nil, fmt.Errorf("%w: embedding is empty", ErrInvalidQueryData)
	}
	results, err := u.vector.Search(ctx, queryData.Embedding, request.Limit)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	return setSource(results, domain.SourceVector)
}

func (u *SearchUseCase) graphSearch(ctx context.Context, request SearchRequest) ([]domain.SearchResult, error) {
	queryData, err := u.processor.Process(ctx, request.Query)
	if err != nil {
		return nil, fmt.Errorf("process query: %w", err)
	}
	if len(queryData.Entities) == 0 {
		return []domain.SearchResult{}, nil
	}
	results, err := u.graph.Search(ctx, queryData.Entities, request.GraphDepth, request.Limit)
	if err != nil {
		return nil, fmt.Errorf("graph search: %w", err)
	}
	return setSource(results, domain.SourceGraph)
}

func (u *SearchUseCase) hybridSearch(ctx context.Context, request SearchRequest) ([]domain.SearchResult, error) {
	queryData, err := u.processor.Process(ctx, request.Query)
	if err != nil {
		return nil, fmt.Errorf("process query: %w", err)
	}
	if len(queryData.Embedding) == 0 {
		return nil, fmt.Errorf("%w: embedding is empty", ErrInvalidQueryData)
	}

	group, groupCtx := errgroup.WithContext(ctx)
	var vectorResults, graphResults []domain.SearchResult

	group.Go(func() error {
		results, err := u.vector.Search(groupCtx, queryData.Embedding, request.Limit)
		if err != nil {
			return fmt.Errorf("vector search: %w", err)
		}
		vectorResults, err = setSource(results, domain.SourceVector)
		return err
	})

	group.Go(func() error {
		if len(queryData.Entities) == 0 {
			graphResults = []domain.SearchResult{}
			return nil
		}
		results, err := u.graph.Search(groupCtx, queryData.Entities, request.GraphDepth, request.Limit)
		if err != nil {
			return fmt.Errorf("graph search: %w", err)
		}
		graphResults, err = setSource(results, domain.SourceGraph)
		return err
	})

	if err := group.Wait(); err != nil {
		return nil, err
	}
	return reciprocalRankFusion(vectorResults, graphResults, request.Limit), nil
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
