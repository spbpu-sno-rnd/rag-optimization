//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCorpusWithRealE5AndExtractedRelations(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"message":{"content":"{\"entities\":[\"Acme Corporation\",\"Northstar Ltd\"],\"relations\":[{\"source\":\"Acme Corporation\",\"relation_type\":\"partnered_with\",\"target\":\"Northstar Ltd\"}]}"}}`)
	}))
	defer ollama.Close()
	aliases, err := loadAliases(filepath.Join("corpus", "aliases.json"))
	if err != nil {
		t.Fatal(err)
	}
	model := newModelClient(config{ModelURL: "http://127.0.0.1:8082", OllamaURL: ollama.URL, OllamaModel: "test", MaxConcurrent: 2})
	chunks, relations, err := readCorpus(context.Background(), "corpus", model, aliases, 384)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 {
		t.Fatalf("expected three chunks, got %d", len(chunks))
	}
	if len(relations) != 1 || relations[0].Source != "Acme" || relations[0].Target != "Northstar Supplies" || relations[0].RelationType != "partnered_with" {
		t.Fatalf("unexpected relations: %+v", relations)
	}
	if err := validateEvidence(chunks, relations); err != nil {
		t.Fatal(err)
	}
	for _, chunk := range chunks {
		if len(chunk.Vector) != 384 || chunk.Source == "" || chunk.Metadata["source"] != chunk.Source {
			t.Fatalf("bad chunk: %+v", chunk)
		}
	}
	path := filepath.Join(t.TempDir(), "chunks.jsonl")
	if err := writeJSONL(path, chunks); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	for i := 0; i < len(chunks); i++ {
		var decoded chunk
		if err := decoder.Decode(&decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.ID != chunks[i].ID || decoded.DocumentID != chunks[i].DocumentID || decoded.Text != chunks[i].Text {
			t.Fatalf("JSONL mismatch: %+v", decoded)
		}
	}
}
