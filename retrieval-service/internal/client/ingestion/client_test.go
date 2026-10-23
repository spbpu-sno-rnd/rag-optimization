package ingestion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientProcess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/query-context" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embedding":[0.1,0.2],"entities":[" Acme ","","Acme","Supplier Y"]}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	got, err := client.Process(context.Background(), "question")
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if len(got.Embedding) != 2 || len(got.Entities) != 2 || got.Entities[0] != "Acme" || got.Entities[1] != "Supplier Y" {
		t.Fatalf("Process() = %+v", got)
	}
}

func TestClientProcessReportsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "embedding model unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Process(context.Background(), "question"); err == nil || !strings.Contains(err.Error(), "embedding model unavailable") {
		t.Fatalf("Process() error = %v, want error body from HTTP 503", err)
	}
}
