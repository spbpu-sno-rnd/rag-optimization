package ingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"retrieval-service/internal/usecase"
)

const maxResponseBodySize = 4 << 20

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type queryRequest struct {
	Query string `json:"query"`
}

type queryResponse struct {
	Embedding []float32 `json:"embedding"`
	Entities  []string  `json:"entities"`
}

func NewClient(baseURL string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("ingestion base URL must not be empty")
	}
	if httpClient == nil {
		return nil, fmt.Errorf("HTTP client must not be nil")
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
	}, nil
}

func (c *Client) Process(ctx context.Context, query string) (usecase.QueryData, error) {
	body, err := json.Marshal(queryRequest{Query: query})
	if err != nil {
		return usecase.QueryData{}, fmt.Errorf("encode query processing request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/query-context", bytes.NewReader(body))
	if err != nil {
		return usecase.QueryData{}, fmt.Errorf("create query processing request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return usecase.QueryData{}, fmt.Errorf("call ingestion query processor: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBodySize))
		if readErr != nil {
			return usecase.QueryData{}, fmt.Errorf("read ingestion error response (HTTP %d): %w", response.StatusCode, readErr)
		}
		message := strings.TrimSpace(string(body))
		if message == "" {
			return usecase.QueryData{}, fmt.Errorf("ingestion query processor returned HTTP %d", response.StatusCode)
		}
		return usecase.QueryData{}, fmt.Errorf("ingestion query processor returned HTTP %d: %s", response.StatusCode, message)
	}

	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBodySize))
	var result queryResponse
	if err := decoder.Decode(&result); err != nil {
		return usecase.QueryData{}, fmt.Errorf("decode ingestion query response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return usecase.QueryData{}, fmt.Errorf("ingestion query response must contain one JSON object")
	}

	entities := make([]string, 0, len(result.Entities))
	seen := make(map[string]struct{}, len(result.Entities))
	for _, entity := range result.Entities {
		entity = strings.TrimSpace(entity)
		if entity == "" {
			continue
		}
		if _, exists := seen[entity]; exists {
			continue
		}
		seen[entity] = struct{}{}
		entities = append(entities, entity)
	}

	return usecase.QueryData{
		Embedding: result.Embedding,
		Entities:  entities,
	}, nil
}
