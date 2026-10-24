package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

type modelClient struct {
	config config
	http   *http.Client
	slots  chan struct{}
}
type relation struct {
	Source         string   `json:"source"`
	RelationType   string   `json:"relation_type"`
	Target         string   `json:"target"`
	SourceChunkIDs []string `json:"source_chunk_ids,omitempty"`
}
type extraction struct {
	Entities  []string   `json:"entities"`
	Relations []relation `json:"relations"`
}

func newModelClient(c config) *modelClient {
	return &modelClient{config: c, http: &http.Client{Timeout: 75 * time.Second}, slots: make(chan struct{}, c.MaxConcurrent)}
}

func (m *modelClient) post(ctx context.Context, endpoint string, input any, output any) error {
	select {
	case m.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-m.slots }()
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(output)
}

func (m *modelClient) count(ctx context.Context, text string) (int, error) {
	var result struct {
		Tokens int `json:"tokens"`
	}
	err := m.post(ctx, strings.TrimRight(m.config.ModelURL, "/")+"/count", map[string]string{"text": text}, &result)
	if err != nil {
		return 0, err
	}
	if result.Tokens < 1 {
		return 0, fmt.Errorf("invalid token count")
	}
	return result.Tokens, nil
}

func (m *modelClient) embed(ctx context.Context, text, kind string) ([]float32, error) {
	var result struct {
		Embedding []float32 `json:"embedding"`
	}
	err := m.post(ctx, strings.TrimRight(m.config.ModelURL, "/")+"/embed", map[string]string{"text": text, "kind": kind}, &result)
	if err != nil {
		return nil, err
	}
	if len(result.Embedding) != 384 {
		return nil, fmt.Errorf("embedding has %d dimensions, want 384", len(result.Embedding))
	}
	for _, v := range result.Embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, fmt.Errorf("embedding contains non-finite value")
		}
	}
	return result.Embedding, nil
}

func (m *modelClient) extract(ctx context.Context, text string, query bool) (extraction, error) {
	prompt := "Extract explicitly mentioned named entities and directed semantic relationships supported by the text. Return only JSON. Never infer a relationship that is not stated. relation_type is a short lowercase snake_case predicate. If none are present, return empty arrays."
	if query {
		prompt = "Extract explicitly mentioned named entities from this search query. Return only JSON with entities array and empty relations array. If none, return empty arrays."
	}
	format := map[string]any{"type": "object", "properties": map[string]any{"entities": map[string]any{"type": "array", "items": map[string]string{"type": "string"}}, "relations": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"source": map[string]string{"type": "string"}, "relation_type": map[string]string{"type": "string"}, "target": map[string]string{"type": "string"}}, "required": []string{"source", "relation_type", "target"}}}}, "required": []string{"entities", "relations"}}
	input := map[string]any{"model": m.config.OllamaModel, "stream": false, "format": format, "options": map[string]any{"temperature": 0}, "messages": []map[string]string{{"role": "system", "content": prompt}, {"role": "user", "content": text}}}
	var result struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	err := m.post(ctx, strings.TrimRight(m.config.OllamaURL, "/")+"/api/chat", input, &result)
	if err != nil {
		return extraction{}, err
	}
	var extracted extraction
	if err := json.Unmarshal([]byte(result.Message.Content), &extracted); err != nil {
		return extraction{}, fmt.Errorf("decode extraction: %w", err)
	}
	if extracted.Entities == nil {
		extracted.Entities = []string{}
	}
	if extracted.Relations == nil {
		extracted.Relations = []relation{}
	}
	return extracted, nil
}
