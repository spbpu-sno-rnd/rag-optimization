package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"retrieval-service/internal/client/ingestion"
	"retrieval-service/internal/client/ollama"
	"retrieval-service/internal/domain"
	"retrieval-service/internal/repository/vector"
	"retrieval-service/internal/usecase"
)

type answerIntegrationGraph struct {
	err error
}

func (g answerIntegrationGraph) Search(context.Context, []string, int, int) (domain.GraphSearchResults, error) {
	if g.err != nil {
		return domain.GraphSearchResults{}, g.err
	}
	return domain.GraphSearchResults{}, errors.New("graph must not run for a vector request")
}

type answerIntegrationSource struct {
	id, source string
}

type answerIntegrationFact struct {
	text, id, documentID, href, rendered string
}

// These are HTTP contract checks with scripted model output, not an evaluation of
// a real model. S01 and N01 are dev queries; the compound queries are synthetic
// so the held-out benchmark questions remain untouched.
func TestAnswerHTTPIntegration(t *testing.T) {
	const (
		partnerQuery = "Which company partnered with Acme to expand Aurora production?"
		partnerFact  = "Acme Corporation partnered with Northstar Ltd to expand production of Aurora."
		launchFact   = "In 2023, Acme Corporation launched the Aurora device."
		atlasFact    = "Atlas Components supplied processors to Orbit."
		cedarFact    = "Cedar Plastics supplied protective cases to Acme for Aurora."
		inventedFact = "Acme acquired Harbor Logistics in 2022."
	)
	launch := answerIntegrationSource{"launch-α", "01_acme_launch.md"}
	atlas := answerIntegrationSource{"atlas", "06_atlas_processors.md"}
	cedar := answerIntegrationSource{"cedar", "07_cedar_packaging.md"}
	partnerWant := answerIntegrationFact{
		text: partnerFact, id: "launch-α", documentID: "01_acme_launch", href: "#chunk-6c61756e63682dceb1",
		rendered: `Acme Corporation partnered with Northstar Ltd to expand production of Aurora\. [1](#chunk-6c61756e63682dceb1)`,
	}
	tests := []struct {
		name          string
		query         string
		sources       []answerIntegrationSource
		draft         domain.AnswerDraft
		modelError    bool
		unfinished    bool
		truncated     bool
		legacy        bool
		hybridPartial bool
		wantStatus    domain.AnswerStatus
		wantFacts     []answerIntegrationFact
		wantMissing   string
		wantCalls     int32
	}{
		{
			name: "one source with UTF8 chunk anchor", query: partnerQuery, sources: []answerIntegrationSource{launch},
			draft:      domain.AnswerDraft{Facts: []domain.DraftFact{{Text: partnerFact, ChunkIDs: []string{"launch-α"}}}},
			wantStatus: domain.AnswerComplete, wantFacts: []answerIntegrationFact{partnerWant}, wantCalls: 1,
		},
		{
			name: "two independent sources", query: "What did Atlas Components supply, and what did Cedar Plastics supply for Aurora?",
			sources: []answerIntegrationSource{atlas, cedar},
			draft: domain.AnswerDraft{Facts: []domain.DraftFact{
				{Text: atlasFact, ChunkIDs: []string{"atlas"}}, {Text: cedarFact, ChunkIDs: []string{"cedar"}},
			}},
			wantStatus: domain.AnswerComplete, wantCalls: 1,
			wantFacts: []answerIntegrationFact{
				{text: atlasFact, id: "atlas", documentID: "06_atlas_processors", href: "#chunk-61746c6173", rendered: `Atlas Components supplied processors to Orbit\. [1](#chunk-61746c6173)`},
				{text: cedarFact, id: "cedar", documentID: "07_cedar_packaging", href: "#chunk-6365646172", rendered: `Cedar Plastics supplied protective cases to Acme for Aurora\. [2](#chunk-6365646172)`},
			},
		},
		{
			name: "partial answer preserves known fact", query: "What did Acme launch in 2023, and what was Aurora production volume in 2024?",
			sources:    []answerIntegrationSource{launch},
			draft:      domain.AnswerDraft{Facts: []domain.DraftFact{{Text: launchFact, ChunkIDs: []string{"launch-α"}}}, Missing: []string{"Aurora production volume in 2024"}},
			wantStatus: domain.AnswerPartial, wantMissing: "Aurora production volume in 2024", wantCalls: 1,
			wantFacts: []answerIntegrationFact{{text: launchFact, id: "launch-α", documentID: "01_acme_launch", href: "#chunk-6c61756e63682dceb1", rendered: `In 2023\, Acme Corporation launched the Aurora device\. [1](#chunk-6c61756e63682dceb1)`}},
		},
		{
			name: "irrelevant nearest chunk is not an answer", query: "Did Acme acquire Harbor Logistics in 2022?", sources: []answerIntegrationSource{cedar},
			draft:      domain.AnswerDraft{Facts: []domain.DraftFact{}, Missing: []string{"Acme acquisition of Harbor Logistics in 2022"}},
			wantStatus: domain.AnswerInsufficientData, wantMissing: "Acme acquisition of Harbor Logistics in 2022", wantCalls: 1,
		},
		{
			name: "empty retrieval bypasses model", query: partnerQuery,
			wantStatus: domain.AnswerInsufficientData,
		},
		{
			name: "foreign evidence ID is filtered", query: partnerQuery, sources: []answerIntegrationSource{launch},
			draft: domain.AnswerDraft{Facts: []domain.DraftFact{
				{Text: partnerFact, ChunkIDs: []string{"launch-α"}}, {Text: partnerFact, ChunkIDs: []string{"not-retrieved"}},
			}},
			wantStatus: domain.AnswerPartial, wantFacts: []answerIntegrationFact{partnerWant}, wantCalls: 1,
			wantMissing: "Не удалось подтвердить часть ответа",
		},
		{
			name: "unsupported text with a valid ID is filtered", query: partnerQuery, sources: []answerIntegrationSource{launch},
			draft: domain.AnswerDraft{Facts: []domain.DraftFact{
				{Text: partnerFact, ChunkIDs: []string{"launch-α"}}, {Text: inventedFact, ChunkIDs: []string{"launch-α"}},
			}},
			wantStatus: domain.AnswerPartial, wantFacts: []answerIntegrationFact{partnerWant}, wantCalls: 1,
			wantMissing: "Не удалось подтвердить часть ответа",
		},
		{
			name: "model HTTP error keeps retrieval", query: partnerQuery, sources: []answerIntegrationSource{launch}, modelError: true,
			wantStatus: domain.AnswerError, wantCalls: 1,
		},
		{
			name: "unfinished envelope keeps retrieval", query: partnerQuery, sources: []answerIntegrationSource{launch}, unfinished: true,
			draft:      domain.AnswerDraft{Facts: []domain.DraftFact{{Text: partnerFact, ChunkIDs: []string{"launch-α"}}}},
			wantStatus: domain.AnswerError, wantCalls: 1,
		},
		{
			name: "token truncated envelope keeps retrieval", query: partnerQuery, sources: []answerIntegrationSource{launch}, truncated: true,
			draft:      domain.AnswerDraft{Facts: []domain.DraftFact{{Text: partnerFact, ChunkIDs: []string{"launch-α"}}}},
			wantStatus: domain.AnswerError, wantCalls: 1,
		},
		{
			name: "legacy search bypasses model", query: partnerQuery, sources: []answerIntegrationSource{launch}, legacy: true,
		},
		{
			name: "partial hybrid retrieval does not imply partial answer", query: partnerQuery, sources: []answerIntegrationSource{launch}, hybridPartial: true,
			draft:      domain.AnswerDraft{Facts: []domain.DraftFact{{Text: partnerFact, ChunkIDs: []string{"launch-α"}}}},
			wantStatus: domain.AnswerComplete, wantFacts: []answerIntegrationFact{partnerWant}, wantCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chunks := answerIntegrationCorpus(t, test.sources)
			var ingestionCalls, qdrantCalls, modelCalls atomic.Int32
			ingestionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ingestionCalls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/v1/query-context" {
					t.Errorf("ingestion request = %s %s", r.Method, r.URL.Path)
				}
				var request struct {
					Query string `json:"query"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Query != test.query {
					t.Errorf("ingestion query = %q, error = %v", request.Query, err)
				}
				embedding := make([]float32, 384)
				embedding[0] = 1
				writeJSON(w, http.StatusOK, map[string]any{"embedding": embedding, "entities": []string{"Acme"}})
			}))
			t.Cleanup(ingestionServer.Close)
			qdrantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				qdrantCalls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/collections/documents/points/query" {
					t.Errorf("Qdrant request = %s %s", r.Method, r.URL.Path)
				}
				var request struct {
					Query       []float32 `json:"query"`
					Limit       int       `json:"limit"`
					WithPayload bool      `json:"with_payload"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode Qdrant request: %v", err)
				}
				if len(request.Query) != 384 || request.Query[0] != 1 || request.Limit != 5 || !request.WithPayload {
					t.Errorf("Qdrant query: dimensions=%d limit=%d payload=%v", len(request.Query), request.Limit, request.WithPayload)
				}
				points := make([]map[string]any, 0, len(chunks))
				for _, chunk := range chunks {
					points = append(points, map[string]any{"id": chunk.ID, "score": chunk.Score, "payload": map[string]any{
						"chunk_id": chunk.ID, "text": chunk.Text, "document_id": chunk.DocumentID, "metadata": chunk.Metadata,
					}})
				}
				writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"points": points}, "status": "ok"})
			}))
			t.Cleanup(qdrantServer.Close)
			modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				modelCalls.Add(1)
				answerIntegrationCheckPrompt(t, r, test.query, chunks)
				if test.modelError {
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "fixture model unavailable"})
					return
				}
				draft := test.draft
				if draft.Facts == nil {
					draft.Facts = []domain.DraftFact{}
				}
				if draft.Missing == nil {
					draft.Missing = []string{}
				}
				content, err := json.Marshal(draft)
				if err != nil {
					t.Errorf("encode model draft: %v", err)
				}
				reason := "stop"
				if test.truncated {
					reason = "length"
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"message": map[string]string{"role": "assistant", "content": string(content)},
					"done":    !test.unfinished, "done_reason": reason,
				})
			}))
			t.Cleanup(modelServer.Close)
			processor, err := ingestion.NewClient(ingestionServer.URL, ingestionServer.Client())
			if err != nil {
				t.Fatal(err)
			}
			vectorStore, err := vector.NewQdrant(qdrantServer.URL, "documents", "", qdrantServer.Client())
			if err != nil {
				t.Fatal(err)
			}
			graphStore := answerIntegrationGraph{}
			if test.hybridPartial {
				graphStore.err = errors.New("fixture graph unavailable")
			}
			search, err := usecase.NewSearchUseCase(processor, vectorStore, graphStore)
			if err != nil {
				t.Fatal(err)
			}
			generator, err := ollama.NewClient(modelServer.URL, "fixture-model", modelServer.Client())
			if err != nil {
				t.Fatal(err)
			}
			answers, err := usecase.NewAnswerUseCase(search, generator)
			if err != nil {
				t.Fatal(err)
			}
			handler, err := NewHandler(answers)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(NewRouter(handler))
			t.Cleanup(server.Close)
			strategy := "vector"
			if test.hybridPartial {
				strategy = "hybrid"
			}
			request := map[string]any{"query": test.query, "strategy": strategy, "limit": 5, "graph_depth": 2}
			if !test.legacy {
				request["generate_answer"] = true
			}
			encoded, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.Client().Post(server.URL+"/v1/search", "application/json", bytes.NewReader(encoded))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("HTTP status = %d, want 200: %s", response.StatusCode, body)
			}
			var got domain.SearchResponse
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if ingestionCalls.Load() != 1 || qdrantCalls.Load() != 1 || modelCalls.Load() != test.wantCalls {
				t.Errorf("calls ingestion/Qdrant/Ollama = %d/%d/%d, want 1/1/%d", ingestionCalls.Load(), qdrantCalls.Load(), modelCalls.Load(), test.wantCalls)
			}
			wantGraphStatus := "skipped"
			if test.hybridPartial {
				wantGraphStatus = "error"
			}
			if got.Partial != test.hybridPartial || got.VectorStatus != "ok" || got.GraphStatus != wantGraphStatus || len(got.GraphFacts) != 0 {
				t.Errorf("retrieval statuses changed: partial=%v vector=%s graph=%s facts=%v", got.Partial, got.VectorStatus, got.GraphStatus, got.GraphFacts)
			}
			if len(got.Results) != len(chunks) {
				t.Fatalf("results = %d, want %d", len(got.Results), len(chunks))
			}
			for i, result := range got.Results {
				wantScore := chunks[i].Score
				if test.hybridPartial {
					wantScore = 0.01639344262295082
				}
				if result.ID != chunks[i].ID || result.Text != chunks[i].Text || result.DocumentID != chunks[i].DocumentID || !reflect.DeepEqual(result.Metadata, chunks[i].Metadata) || result.Score != wantScore || !reflect.DeepEqual(result.Sources, []domain.Source{domain.SourceVector}) {
					t.Errorf("retrieval result %d changed: %+v", i, result)
				}
			}
			if test.legacy {
				if got.Answer != nil || bytes.Contains(body, []byte(`"answer"`)) {
					t.Fatalf("legacy response gained an answer: %s", body)
				}
				return
			}
			if got.Answer == nil {
				t.Fatalf("answer missing: %s", body)
			}
			answer := got.Answer
			if answer.Status != test.wantStatus {
				t.Errorf("answer status = %s, want %s", answer.Status, test.wantStatus)
			}
			if len(answer.Facts) != len(test.wantFacts) {
				t.Fatalf("answer facts = %+v, want %d", answer.Facts, len(test.wantFacts))
			}
			for i, want := range test.wantFacts {
				fact := answer.Facts[i]
				if fact.Text != want.text || len(fact.Citations) != 1 {
					t.Fatalf("fact %d = %+v", i, fact)
				}
				citation := fact.Citations[0]
				if citation.ChunkID != want.id || citation.DocumentID != want.documentID || citation.Text != want.text || citation.Href != want.href {
					t.Errorf("citation %d = %+v, want chunk=%q document=%q href=%q", i, citation, want.id, want.documentID, want.href)
				}
				if !strings.Contains(answer.Text, want.rendered) {
					t.Errorf("fact and adjacent citation missing from Markdown: %q", answer.Text)
				}
				found := false
				for _, result := range got.Results {
					if result.ID == citation.ChunkID && result.DocumentID == citation.DocumentID && strings.Contains(result.Text, citation.Text) {
						found = true
					}
				}
				if !found {
					t.Errorf("citation has no supporting result: %+v", citation)
				}
			}
			if test.wantMissing != "" && !strings.Contains(strings.Join(answer.Missing, "\n"), test.wantMissing) {
				t.Errorf("missing = %v, want %q", answer.Missing, test.wantMissing)
			}
			if answer.Status == domain.AnswerComplete && len(answer.Missing) != 0 {
				t.Errorf("complete answer has missing facts: %v", answer.Missing)
			}
			if answer.Status == domain.AnswerInsufficientData && !strings.Contains(strings.ToLower(answer.Text), "недостаточно данных") {
				t.Errorf("insufficient answer text = %q", answer.Text)
			}
			if len(test.wantFacts) == 0 && strings.Contains(answer.Text, "](#chunk-") {
				t.Errorf("answer without facts contains a citation: %q", answer.Text)
			}
			if strings.Contains(answer.Text, inventedFact) || strings.Contains(answer.Text, "not-retrieved") {
				t.Errorf("untrusted model content survived: %q", answer.Text)
			}
		})
	}
}

func answerIntegrationCorpus(t *testing.T, sources []answerIntegrationSource) []domain.SearchResult {
	t.Helper()
	chunks := make([]domain.SearchResult, 0, len(sources))
	for i, source := range sources {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "benchmark", "corpus", source.source))
		if err != nil {
			t.Fatal(err)
		}
		scores := []float64{0.9, 0.8}
		chunks = append(chunks, domain.SearchResult{
			ID: source.id, Text: strings.TrimSpace(string(data)), DocumentID: strings.TrimSuffix(source.source, ".md"), Score: scores[i],
			Metadata: map[string]string{"source": source.source, "private": "must-not-enter-generation-context"},
		})
	}
	return chunks
}

func answerIntegrationCheckPrompt(t *testing.T, r *http.Request, query string, chunks []domain.SearchResult) {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
		t.Errorf("Ollama request = %s %s", r.Method, r.URL.Path)
	}
	var request struct {
		Model    string                           `json:"model"`
		Stream   bool                             `json:"stream"`
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Errorf("decode Ollama request: %v", err)
		return
	}
	if request.Model != "fixture-model" || request.Stream || len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" {
		t.Errorf("Ollama request envelope = %+v", request)
		return
	}
	var prompt struct {
		Query  string `json:"query"`
		Chunks []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"chunks"`
	}
	decoder := json.NewDecoder(strings.NewReader(request.Messages[1].Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&prompt); err != nil {
		t.Errorf("generation context has fields beyond query and chunk ID/text: %v", err)
		return
	}
	if prompt.Query != query || len(prompt.Chunks) != len(chunks) {
		t.Errorf("generation context query=%q chunks=%d", prompt.Query, len(prompt.Chunks))
		return
	}
	for i, chunk := range prompt.Chunks {
		if chunk.ID != chunks[i].ID || chunk.Text != chunks[i].Text {
			t.Errorf("generation chunk %d = %+v, want retrieved ID/text", i, chunk)
		}
	}
	if strings.Contains(request.Messages[1].Content, "must-not-enter-generation-context") {
		t.Error("retrieval metadata leaked into generation context")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Errorf("extra generation context: %v", err)
	}
}
