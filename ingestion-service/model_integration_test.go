//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQueryContextRealE5(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"message":{"content":"{\"entities\":[\"Acme Corp\"],\"relations\":[]}"}}`)
	}))
	defer ollama.Close()
	model := newModelClient(config{ModelURL: "http://127.0.0.1:8082", OllamaURL: ollama.URL, OllamaModel: "test", MaxConcurrent: 2})
	response := httptest.NewRecorder()
	queryHandler(model, map[string]string{"acme corp": "Acme", "acme": "Acme"}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/query-context", strings.NewReader(`{"query":"What did Acme Corp launch?"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Embedding []float32 `json:"embedding"`
		Entities  []string  `json:"entities"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Embedding) != 384 || len(result.Entities) != 1 || result.Entities[0] != "Acme" {
		t.Fatalf("bad query context: %d dimensions, entities %v", len(result.Embedding), result.Entities)
	}
}
