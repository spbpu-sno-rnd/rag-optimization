package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"retrieval-service/internal/domain"
)

const maxResponseBodySize = 16 << 20

type Qdrant struct {
	baseURL    string
	collection string
	apiKey     string
	httpClient *http.Client
}

type queryRequest struct {
	Query       []float32 `json:"query"`
	Limit       int       `json:"limit"`
	WithPayload bool      `json:"with_payload"`
}

type queryResponse struct {
	Result struct {
		Points []struct {
			ID      json.RawMessage `json:"id"`
			Score   float64         `json:"score"`
			Payload map[string]any  `json:"payload"`
		} `json:"points"`
	} `json:"result"`
}

func NewQdrant(baseURL, collection, apiKey string, httpClient *http.Client) (*Qdrant, error) {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(collection) == "" {
		return nil, fmt.Errorf("Qdrant URL and collection must not be empty")
	}
	if httpClient == nil {
		return nil, fmt.Errorf("HTTP client must not be nil")
	}
	return &Qdrant{
		baseURL:    strings.TrimRight(baseURL, "/"),
		collection: collection,
		apiKey:     apiKey,
		httpClient: httpClient,
	}, nil
}

func (q *Qdrant) CheckCollection(ctx context.Context) error {
	endpoint := q.baseURL + "/collections/" + url.PathEscape(q.collection)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create Qdrant collection check: %w", err)
	}
	if q.apiKey != "" {
		request.Header.Set("api-key", q.apiKey)
	}
	response, err := q.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("check Qdrant collection: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("Qdrant collection check returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (q *Qdrant) Search(ctx context.Context, embedding []float32, limit int) ([]domain.SearchResult, error) {
	body, err := json.Marshal(queryRequest{Query: embedding, Limit: limit, WithPayload: true})
	if err != nil {
		return nil, fmt.Errorf("encode Qdrant query: %w", err)
	}
	endpoint := q.baseURL + "/collections/" + url.PathEscape(q.collection) + "/points/query"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Qdrant request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if q.apiKey != "" {
		request.Header.Set("api-key", q.apiKey)
	}

	response, err := q.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call Qdrant: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Qdrant returned HTTP %d", response.StatusCode)
	}

	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBodySize))
	var result queryResponse
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode Qdrant response: %w", err)
	}

	results := make([]domain.SearchResult, 0, len(result.Result.Points))
	for index, point := range result.Result.Points {
		id := stringPayload(point.Payload, "chunk_id")
		if id == "" {
			id = decodePointID(point.ID)
		}
		text := stringPayload(point.Payload, "text")
		if strings.TrimSpace(id) == "" || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("Qdrant point at rank %d is missing chunk_id or text payload", index+1)
		}

		results = append(results, domain.SearchResult{
			ID:         id,
			Text:       text,
			DocumentID: stringPayload(point.Payload, "document_id"),
			Metadata:   stringMetadata(point.Payload["metadata"]),
			Score:      point.Score,
		})
	}
	return results, nil
}

func stringPayload(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, _ := payload[key].(string)
	return value
}

func decodePointID(id json.RawMessage) string {
	if len(id) == 0 || string(id) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(id, &text); err == nil {
		return text
	}
	return string(id)
}

func stringMetadata(value any) map[string]string {
	values, ok := value.(map[string]any)
	if !ok || len(values) == 0 {
		return nil
	}
	metadata := make(map[string]string, len(values))
	for key, value := range values {
		switch typed := value.(type) {
		case string:
			metadata[key] = typed
		case float64, bool:
			metadata[key] = fmt.Sprint(typed)
		}
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}
