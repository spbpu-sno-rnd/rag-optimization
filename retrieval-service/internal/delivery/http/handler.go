package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"retrieval-service/internal/domain"
	"retrieval-service/internal/usecase"
)

const maxRequestBodySize = 1 << 20

type Searcher interface {
	Search(ctx context.Context, request usecase.SearchRequest) (domain.SearchResponse, error)
}

type Handler struct {
	searcher Searcher
}

type searchRequest struct {
	Query      string           `json:"query"`
	Strategy   usecase.Strategy `json:"strategy,omitempty"`
	Limit      int              `json:"limit,omitempty"`
	GraphDepth int              `json:"graph_depth,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func NewHandler(searcher Searcher) (*Handler, error) {
	if searcher == nil {
		return nil, errors.New("searcher must not be nil")
	}
	return &Handler{searcher: searcher}, nil
}

func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var body searchRequest
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request body must contain a single JSON object")
		return
	}

	if body.Strategy == "" {
		body.Strategy = usecase.StrategyHybrid
	}
	if body.Limit == 0 {
		body.Limit = 5
	}
	if body.GraphDepth == 0 {
		body.GraphDepth = 1
	}

	results, err := h.searcher.Search(r.Context(), usecase.SearchRequest{
		Query:      body.Query,
		Strategy:   body.Strategy,
		Limit:      body.Limit,
		GraphDepth: body.GraphDepth,
	})
	if err != nil {
		if errors.Is(err, usecase.ErrInvalidRequest) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.ErrorContext(r.Context(), "search request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}

	writeJSON(w, http.StatusOK, results)
}

func Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("write HTTP response", "error", err)
	}
}
