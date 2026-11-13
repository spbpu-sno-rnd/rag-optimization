package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"retrieval-service/internal/domain"
)

type Searcher interface {
	Search(context.Context, SearchRequest) (domain.SearchResponse, error)
}

type AnswerGenerator interface {
	Generate(context.Context, string, []domain.SearchResult) (domain.AnswerDraft, error)
}

type AnswerUseCase struct {
	searcher  Searcher
	generator AnswerGenerator
}

func NewAnswerUseCase(searcher Searcher, generator AnswerGenerator) (*AnswerUseCase, error) {
	if searcher == nil || generator == nil {
		return nil, fmt.Errorf("answer searcher and generator must not be nil")
	}
	return &AnswerUseCase{searcher: searcher, generator: generator}, nil
}

// Search preserves retrieval results and adds a validated answer only when requested.
func (u *AnswerUseCase) Search(ctx context.Context, request SearchRequest) (domain.SearchResponse, error) {
	if ctx == nil {
		return domain.SearchResponse{}, fmt.Errorf("%w: context is nil", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return domain.SearchResponse{}, err
	}
	response, err := u.searcher.Search(ctx, request)
	if err != nil || !request.GenerateAnswer {
		return response, err
	}
	chunks := make([]domain.SearchResult, 0, len(response.Results))
	for _, chunk := range response.Results {
		if strings.TrimSpace(chunk.ID) != "" && strings.TrimSpace(chunk.Text) != "" {
			chunks = append(chunks, chunk)
		}
	}
	if len(chunks) == 0 {
		if response.VectorStatus == "error" && response.GraphStatus != "ok" || response.GraphStatus == "error" && response.VectorStatus != "ok" {
			response.Answer = generationErrorAnswer()
			return response, nil
		}
		answer := BuildGroundedAnswer(request.Query, domain.AnswerDraft{}, nil)
		response.Answer = &answer
		return response, nil
	}
	draft, err := u.generator.Generate(ctx, strings.TrimSpace(request.Query), chunks)
	if err := ctx.Err(); err != nil {
		return domain.SearchResponse{}, err
	}
	if err != nil {
		slog.WarnContext(ctx, "answer generation failed; returning retrieval results", "error", err)
		response.Answer = generationErrorAnswer()
		return response, nil
	}
	answer := BuildGroundedAnswer(request.Query, draft, chunks)
	response.Answer = &answer
	return response, nil
}

func generationErrorAnswer() *domain.Answer {
	return &domain.Answer{
		Status: domain.AnswerError,
		Text:   "Не удалось сформировать ответ из-за ошибки сервиса. Доступные фрагменты приведены в результатах поиска.",
		Facts:  []domain.AnswerFact{}, Missing: []string{},
	}
}
