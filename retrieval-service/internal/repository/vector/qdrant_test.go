package vector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQdrantSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/collections/documents/points/query" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("api-key"); got != "secret" {
			t.Errorf("api-key = %q", got)
		}
		var body queryRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(body.Query) != 2 || body.Limit != 4 || !body.WithPayload {
			t.Errorf("query body = %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"points":[{"id":"chunk-1","score":0.8,"payload":{"chunk_id":"chunk-1","text":"A fact","document_id":"doc-1","metadata":{"year":"2023","page":4}}}]}}`))
	}))
	defer server.Close()

	client, err := NewQdrant(server.URL, "documents", "secret", &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewQdrant() error = %v", err)
	}
	results, err := client.Search(context.Background(), []float32{0.1, 0.2}, 4)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].ID != "chunk-1" || results[0].Text != "A fact" || results[0].Metadata["page"] != "4" {
		t.Fatalf("Search() results = %+v", results)
	}
}

func TestQdrantSearchRejectsMissingPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"points":[{"id":"point-1","score":0.8,"payload":{}}]}}`))
	}))
	defer server.Close()
	client, err := NewQdrant(server.URL, "documents", "", &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewQdrant() error = %v", err)
	}
	if _, err := client.Search(context.Background(), []float32{1}, 1); err == nil {
		t.Fatal("Search() succeeded without required payload")
	}
}

func TestQdrantSearchRejectsMismatchedChunkID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"points":[{"id":"point-id","score":0.8,"payload":{"chunk_id":"chunk-id","text":"A fact"}}]}}`))
	}))
	defer server.Close()

	client, err := NewQdrant(server.URL, "documents", "", &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewQdrant() error = %v", err)
	}
	if _, err := client.Search(context.Background(), []float32{1}, 1); err == nil {
		t.Fatal("Search() succeeded with mismatched point ID and chunk_id")
	}
}
