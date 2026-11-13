package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"retrieval-service/internal/config"
	"retrieval-service/internal/domain"
	"retrieval-service/internal/usecase"
)

type startupSearch struct{}

func (startupSearch) Search(context.Context, usecase.SearchRequest) (domain.SearchResponse, error) {
	return domain.SearchResponse{Results: []domain.SearchResult{{ID: "a", Text: "Atlas supplied processors."}}, VectorStatus: "ok", GraphStatus: "skipped"}, nil
}

func TestNewHTTPHandlerConnectsAnswerGeneration(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "message": map[string]string{"role": "assistant", "content": `{"facts":[{"text":"Atlas supplied processors.","chunk_ids":["a"]}],"missing":[]}`}})
	}))
	defer model.Close()
	handler, err := newHTTPHandler(config.Config{OllamaURL: model.URL, OllamaModel: "qwen2.5:3b", AnswerTimeout: time.Second}, startupSearch{})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"query":"Who supplied processors?","generate_answer":true}`)))
	var got domain.SearchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || got.Answer == nil || got.Answer.Status != domain.AnswerComplete || len(got.Answer.Facts) != 1 || got.Answer.Facts[0].Citations[0].ChunkID != "a" {
		t.Fatalf("wired response = %d %s", response.Code, response.Body.String())
	}
}

func TestNewHTTPHandlerRejectsInvalidModelConfiguration(t *testing.T) {
	if _, err := newHTTPHandler(config.Config{OllamaURL: "bad", OllamaModel: "qwen2.5:3b", AnswerTimeout: time.Second}, startupSearch{}); err == nil {
		t.Fatal("accepted invalid Ollama configuration")
	}
}

func TestSearchWriteTimeoutAllowsGenerationBudget(t *testing.T) {
	for _, test := range []struct{ ingestion, answer, want time.Duration }{
		{10 * time.Second, 40 * time.Second, 80 * time.Second},
		{time.Second, time.Second, 60 * time.Second},
		{10 * time.Second, 2 * time.Minute, 160 * time.Second},
		{time.Duration(1<<63 - 1), time.Second, time.Duration(1<<63 - 1)},
	} {
		if got := searchWriteTimeout(config.Config{IngestionTimeout: test.ingestion, AnswerTimeout: test.answer}); got != test.want {
			t.Errorf("timeout = %v, want %v", got, test.want)
		}
	}
}
