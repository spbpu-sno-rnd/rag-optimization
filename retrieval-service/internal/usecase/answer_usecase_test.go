package usecase

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"retrieval-service/internal/domain"
)

type answerSearchFunc func(context.Context, SearchRequest) (domain.SearchResponse, error)

func (f answerSearchFunc) Search(ctx context.Context, request SearchRequest) (domain.SearchResponse, error) {
	return f(ctx, request)
}

type answerGeneratorFunc func(context.Context, string, []domain.SearchResult) (domain.AnswerDraft, error)

func (f answerGeneratorFunc) Generate(ctx context.Context, query string, chunks []domain.SearchResult) (domain.AnswerDraft, error) {
	return f(ctx, query, chunks)
}

func TestAnswerUseCasePreservesLegacySearch(t *testing.T) {
	want := domain.SearchResponse{Results: []domain.SearchResult{{ID: "a", Text: "Evidence"}}, VectorStatus: "ok", GraphStatus: "skipped"}
	uc, err := NewAnswerUseCase(answerSearchFunc(func(_ context.Context, request SearchRequest) (domain.SearchResponse, error) {
		if request.Query != "Question" || request.Limit != 3 {
			t.Fatalf("search request changed: %+v", request)
		}
		return want, nil
	}), answerGeneratorFunc(func(context.Context, string, []domain.SearchResult) (domain.AnswerDraft, error) {
		t.Fatal("legacy search called model")
		return domain.AnswerDraft{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := uc.Search(context.Background(), SearchRequest{Query: "Question", Limit: 3})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy = %+v, %v", got, err)
	}
}

func TestAnswerUseCaseUsesOnlyReturnedChunks(t *testing.T) {
	chunks := []domain.SearchResult{{ID: "a", Text: "Atlas supplied processors."}, {ID: "b", Text: "Cedar supplied casings."}}
	uc, err := NewAnswerUseCase(answerSearchFunc(func(context.Context, SearchRequest) (domain.SearchResponse, error) {
		return domain.SearchResponse{Results: chunks, Partial: true, VectorStatus: "ok", GraphStatus: "error", GraphFacts: []domain.GraphFact{{EvidenceChunkIDs: []string{"outside"}}}}, nil
	}), answerGeneratorFunc(func(_ context.Context, query string, got []domain.SearchResult) (domain.AnswerDraft, error) {
		if query != "Who supplied parts?" || !reflect.DeepEqual(got, chunks) {
			t.Fatalf("generator context = %q %+v", query, got)
		}
		return domain.AnswerDraft{Facts: []domain.DraftFact{{Text: "Atlas supplied processors.", ChunkIDs: []string{"a"}}, {Text: "Cedar supplied casings.", ChunkIDs: []string{"b"}}}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := uc.Search(context.Background(), SearchRequest{Query: " Who supplied parts? ", GenerateAnswer: true})
	if err != nil || got.Answer == nil || got.Answer.Status != domain.AnswerComplete || len(got.Answer.Facts) != 2 || !got.Partial || got.GraphStatus != "error" || !reflect.DeepEqual(got.Results, chunks) {
		t.Fatalf("answer = %+v, %v", got, err)
	}
}

func TestAnswerUseCaseSkipsModelWithoutEvidence(t *testing.T) {
	for _, chunks := range [][]domain.SearchResult{nil, {{ID: "a", Text: " "}}, {{ID: " ", Text: "Evidence"}}} {
		uc, err := NewAnswerUseCase(answerSearchFunc(func(context.Context, SearchRequest) (domain.SearchResponse, error) {
			return domain.SearchResponse{Results: chunks}, nil
		}), answerGeneratorFunc(func(context.Context, string, []domain.SearchResult) (domain.AnswerDraft, error) {
			t.Fatal("empty evidence called model")
			return domain.AnswerDraft{}, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		got, err := uc.Search(context.Background(), SearchRequest{GenerateAnswer: true})
		if err != nil || got.Answer == nil || got.Answer.Status != domain.AnswerInsufficientData {
			t.Fatalf("empty answer = %+v, %v", got, err)
		}
	}
}

func TestAnswerUseCaseRetainsResultsWhenModelFails(t *testing.T) {
	uc, err := NewAnswerUseCase(answerSearchFunc(func(context.Context, SearchRequest) (domain.SearchResponse, error) {
		return domain.SearchResponse{Results: []domain.SearchResult{{ID: "a", Text: "Evidence"}}, VectorStatus: "ok"}, nil
	}), answerGeneratorFunc(func(context.Context, string, []domain.SearchResult) (domain.AnswerDraft, error) {
		return domain.AnswerDraft{}, errors.New("secret upstream detail")
	}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := uc.Search(context.Background(), SearchRequest{GenerateAnswer: true})
	if err != nil || got.Answer == nil || got.Answer.Status != domain.AnswerError || len(got.Answer.Facts) != 0 || len(got.Results) != 1 || got.VectorStatus != "ok" || got.Partial {
		t.Fatalf("failed model = %+v, %v", got, err)
	}
}

func TestAnswerUseCasePropagatesSearchFailure(t *testing.T) {
	want := errors.New("retrieval failed")
	uc, err := NewAnswerUseCase(answerSearchFunc(func(context.Context, SearchRequest) (domain.SearchResponse, error) {
		return domain.SearchResponse{}, want
	}), answerGeneratorFunc(func(context.Context, string, []domain.SearchResult) (domain.AnswerDraft, error) {
		t.Fatal("failed search called model")
		return domain.AnswerDraft{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uc.Search(context.Background(), SearchRequest{GenerateAnswer: true}); !errors.Is(err, want) {
		t.Fatalf("search error = %v", err)
	}
}

func TestAnswerUseCaseRejectsMissingDependencies(t *testing.T) {
	search := answerSearchFunc(func(context.Context, SearchRequest) (domain.SearchResponse, error) {
		return domain.SearchResponse{}, nil
	})
	generate := answerGeneratorFunc(func(context.Context, string, []domain.SearchResult) (domain.AnswerDraft, error) {
		return domain.AnswerDraft{}, nil
	})
	if _, err := NewAnswerUseCase(nil, generate); err == nil {
		t.Fatal("nil search accepted")
	}
	if _, err := NewAnswerUseCase(search, nil); err == nil {
		t.Fatal("nil generator accepted")
	}
}

func TestAnswerUseCaseHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	uc, err := NewAnswerUseCase(answerSearchFunc(func(context.Context, SearchRequest) (domain.SearchResponse, error) {
		return domain.SearchResponse{Results: []domain.SearchResult{{ID: "a", Text: "Evidence"}}}, nil
	}), answerGeneratorFunc(func(got context.Context, _ string, _ []domain.SearchResult) (domain.AnswerDraft, error) {
		cancel()
		return domain.AnswerDraft{}, got.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if _, err := uc.Search(ctx, SearchRequest{GenerateAnswer: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	if _, err := uc.Search(nil, SearchRequest{GenerateAnswer: true}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("nil context = %v", err)
	}
}
