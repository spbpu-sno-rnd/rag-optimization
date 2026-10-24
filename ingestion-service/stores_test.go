package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQdrantCollectionAndPointContract(t *testing.T) {
	var checked bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/collections/documents":
			if r.Method == http.MethodGet {
				http.NotFound(w, r)
				return
			}
			if r.Method != http.MethodPut {
				t.Errorf("unexpected method %s", r.Method)
				return
			}
			var body struct {
				Vectors struct {
					Size     int    `json:"size"`
					Distance string `json:"distance"`
				} `json:"vectors"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Vectors.Size != 384 || body.Vectors.Distance != "Cosine" {
				t.Errorf("bad collection config: %+v", body.Vectors)
			}
			_, _ = fmt.Fprint(w, `{"result":true}`)
		case "/collections/documents/points":
			var body struct {
				Points []struct {
					ID      string    `json:"id"`
					Vector  []float32 `json:"vector"`
					Payload struct {
						ChunkID    string `json:"chunk_id"`
						DocumentID string `json:"document_id"`
						Text       string `json:"text"`
					} `json:"payload"`
				} `json:"points"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.Points) != 1 || body.Points[0].ID != "a" || body.Points[0].Payload.ChunkID != "a" || body.Points[0].Payload.DocumentID != "doc" || body.Points[0].Payload.Text != "text" || len(body.Points[0].Vector) != 384 {
				t.Errorf("bad point: %+v", body.Points)
			}
			checked = true
			_, _ = fmt.Fprint(w, `{"result":{"status":"completed"}}`)
		case "/collections/documents/points/count":
			_, _ = fmt.Fprint(w, `{"result":{"count":1}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	q := newQdrant(config{QdrantURL: server.URL, QdrantCollection: "documents"})
	if err := q.reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := q.upload(context.Background(), []chunk{{ID: "a", DocumentID: "doc", Text: "text", Vector: make([]float32, 384)}}); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("point not checked")
	}
}
