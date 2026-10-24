package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestSplitChunksRespectsTokenizerBudget(t *testing.T) {
	count := func(_ context.Context, text string) (int, error) { return len([]rune(text)) + 2, nil }
	parts, err := splitChunks(context.Background(), "Alpha beta. Gamma delta. Epsilon zeta.", 22, count)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 2 {
		t.Fatalf("expected several chunks, got %q", parts)
	}
	for _, part := range parts {
		n, _ := count(context.Background(), "passage: "+part)
		if n > 22 {
			t.Fatalf("chunk has %d tokens: %q", n, part)
		}
	}
	if strings.Join(strings.Fields(strings.Join(parts, " ")), " ") != "Alpha beta. Gamma delta. Epsilon zeta." {
		t.Fatalf("text was lost: %q", parts)
	}
}

func TestSplitChunksBoundsTokenizerRequest(t *testing.T) {
	text := strings.Repeat("word ", 1500)
	count := func(_ context.Context, value string) (int, error) {
		if len([]rune(value)) > 4110 {
			return 0, fmt.Errorf("tokenizer request too large")
		}
		return len(strings.Fields(value)) + 2, nil
	}
	parts, err := splitChunks(context.Background(), text, 384, count)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 2 {
		t.Fatalf("expected several chunks, got %d", len(parts))
	}
	if strings.Join(strings.Fields(strings.Join(parts, " ")), " ") != strings.TrimSpace(text) {
		t.Fatal("text was lost")
	}
}

func TestCanonicalAliasesAndEvidence(t *testing.T) {
	aliases := map[string]string{"acme corp": "Acme", "acme": "Acme", "northstar": "Northstar Supplies"}
	got := canonicalEntities("Acme Corp partnered with Northstar.", []string{"Acme Corp", "Acme Corp", "invented"}, aliases)
	if !reflect.DeepEqual(got, []string{"Acme", "Northstar Supplies"}) {
		t.Fatalf("entities: %v", got)
	}
	chunks := []chunk{{ID: "a"}}
	if err := validateEvidence(chunks, []relation{{SourceChunkIDs: []string{"missing"}}}); err == nil {
		t.Fatal("missing evidence was accepted")
	}
	if err := validateEvidence(chunks, []relation{{SourceChunkIDs: []string{"a"}}}); err != nil {
		t.Fatal(err)
	}
	merged := mergeRelations([]relation{{Source: "Acme", RelationType: "partnered_with", Target: "Northstar", SourceChunkIDs: []string{"a"}}, {Source: "Acme", RelationType: "partnered_with", Target: "Northstar", SourceChunkIDs: []string{"a"}}})
	if len(merged) != 1 || !reflect.DeepEqual(merged[0].SourceChunkIDs, []string{"a"}) {
		t.Fatalf("duplicate evidence: %+v", merged)
	}
}

func TestQueryContextContract(t *testing.T) {
	vector := make([]float32, 384)
	vector[0] = 1
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/count":
			_, _ = fmt.Fprint(w, `{"tokens":12}`)
		case "/embed":
			_ = json.NewEncoder(w).Encode(map[string]any{"embedding": vector})
		case "/api/chat":
			_, _ = fmt.Fprint(w, `{"message":{"content":"{\"entities\":[],\"relations\":[]}"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer modelServer.Close()
	m := newModelClient(config{ModelURL: modelServer.URL, OllamaURL: modelServer.URL, OllamaModel: "fake", MaxConcurrent: 2})
	handler := queryHandler(m, nil)
	for _, input := range []string{`{}`, `{"query":" "}`, `{"query":"x","extra":1}`, `{"query":"x"}{"query":"y"}`} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/query-context", strings.NewReader(input)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: HTTP %d", input, response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/query-context", strings.NewReader(`{"query":"What happened?"}`)))
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
	if len(result.Embedding) != 384 || result.Entities == nil || len(result.Entities) != 0 {
		t.Fatalf("bad response: %d dimensions, entities %v", len(result.Embedding), result.Entities)
	}
}

func TestQueryContextModelFailures(t *testing.T) {
	for _, failure := range []string{"/embed", "/api/chat"} {
		t.Run(failure, func(t *testing.T) {
			modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == failure {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				switch r.URL.Path {
				case "/count":
					_, _ = fmt.Fprint(w, `{"tokens":12}`)
				case "/embed":
					_ = json.NewEncoder(w).Encode(map[string]any{"embedding": make([]float32, 384)})
				case "/api/chat":
					_, _ = fmt.Fprint(w, `{"message":{"content":"{\"entities\":[],\"relations\":[]}"}}`)
				}
			}))
			defer modelServer.Close()
			m := newModelClient(config{ModelURL: modelServer.URL, OllamaURL: modelServer.URL, MaxConcurrent: 1})
			response := httptest.NewRecorder()
			queryHandler(m, nil).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/query-context", strings.NewReader(`{"query":"Acme?"}`)))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("HTTP %d", response.Code)
			}
		})
	}
}
