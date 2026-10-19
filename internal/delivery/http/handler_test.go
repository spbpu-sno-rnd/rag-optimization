package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"retrieval-service/internal/domain"
	"retrieval-service/internal/usecase"
)

type searcherFunc func(context.Context, usecase.SearchRequest) ([]domain.SearchResult, error)

func (f searcherFunc) Search(ctx context.Context, request usecase.SearchRequest) ([]domain.SearchResult, error) {
	return f(ctx, request)
}

func TestSearchHandlerUsesDefaultsAndReturnsResults(t *testing.T) {
	searcher := searcherFunc(func(_ context.Context, request usecase.SearchRequest) ([]domain.SearchResult, error) {
		if request.Query != "Where is Acme?" {
			t.Errorf("query = %q", request.Query)
		}
		if request.Strategy != usecase.StrategyHybrid || request.Limit != 5 || request.GraphDepth != 1 {
			t.Errorf("request defaults = %+v", request)
		}
		return []domain.SearchResult{{ID: "chunk-1", Text: "Found it"}}, nil
	})
	handler, err := NewHandler(searcher)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"query":"Where is Acme?"}`))
	response := httptest.NewRecorder()

	handler.Search(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	var body searchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Results) != 1 || body.Results[0].ID != "chunk-1" {
		t.Fatalf("response results = %#v", body.Results)
	}
}

func TestSearchHandlerRejectsInvalidBody(t *testing.T) {
	handler, err := NewHandler(searcherFunc(func(context.Context, usecase.SearchRequest) ([]domain.SearchResult, error) {
		t.Fatal("searcher called for invalid request")
		return nil, nil
	}))
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	for _, body := range []string{
		`{"query":`,
		`{"query":"question","unexpected":true}`,
		`{"query":"question"} {}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(body))
		response := httptest.NewRecorder()
		handler.Search(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("body %q status = %d, want %d", body, response.Code, http.StatusBadRequest)
		}
	}
}

func TestSearchHandlerMapsUseCaseErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "invalid request", err: usecase.ErrInvalidRequest, want: http.StatusBadRequest},
		{name: "internal error", err: errors.New("database unavailable"), want: http.StatusInternalServerError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, err := NewHandler(searcherFunc(func(context.Context, usecase.SearchRequest) ([]domain.SearchResult, error) {
				return nil, test.err
			}))
			if err != nil {
				t.Fatalf("NewHandler() error = %v", err)
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"query":"question"}`))
			response := httptest.NewRecorder()
			handler.Search(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
}

func TestHealthHandler(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	Health(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"ok"`) {
		t.Fatalf("health response = %d %s", response.Code, response.Body)
	}
}
