package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadRequiresNeo4jPassword(t *testing.T) {
	t.Setenv("NEO4J_PASSWORD", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded without NEO4J_PASSWORD")
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	t.Setenv("NEO4J_PASSWORD", "test-password")
	for _, key := range []string{"HTTP_ADDR", "INGESTION_URL", "INGESTION_TIMEOUT", "QDRANT_URL", "QDRANT_COLLECTION", "NEO4J_URI", "NEO4J_USERNAME", "OLLAMA_URL", "OLLAMA_MODEL", "ANSWER_TIMEOUT"} {
		t.Setenv(key, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.QdrantCollection != "documents" || cfg.IngestionTimeout.String() != "10s" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.OllamaURL != "http://localhost:11434" || cfg.OllamaModel != "qwen2.5:3b" || cfg.AnswerTimeout != 40*time.Second {
		t.Fatalf("unexpected answer generation defaults: URL=%q model=%q timeout=%v", cfg.OllamaURL, cfg.OllamaModel, cfg.AnswerTimeout)
	}
}

func TestLoadRejectsInvalidURLs(t *testing.T) {
	t.Setenv("NEO4J_PASSWORD", "test-password")
	t.Setenv("QDRANT_URL", "not-a-url")
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with invalid QDRANT_URL")
	}
}

func TestLoadRejectsInvalidAnswerGenerationConfig(t *testing.T) {
	for _, tt := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "relative URL", key: "OLLAMA_URL", value: "/api/generate"},
		{name: "missing host", key: "OLLAMA_URL", value: "http:///api"},
		{name: "unsupported scheme", key: "OLLAMA_URL", value: "ftp://localhost:11434"},
		{name: "credentials", key: "OLLAMA_URL", value: "http://user:password@localhost:11434"},
		{name: "query", key: "OLLAMA_URL", value: "http://localhost:11434?model=test"},
		{name: "empty query", key: "OLLAMA_URL", value: "http://localhost:11434?"},
		{name: "fragment", key: "OLLAMA_URL", value: "http://localhost:11434#generate"},
		{name: "empty fragment", key: "OLLAMA_URL", value: "http://localhost:11434#"},
		{name: "empty hostname", key: "OLLAMA_URL", value: "http://:11434"},
		{name: "invalid timeout", key: "ANSWER_TIMEOUT", value: "invalid"},
		{name: "zero timeout", key: "ANSWER_TIMEOUT", value: "0s"},
		{name: "negative timeout", key: "ANSWER_TIMEOUT", value: "-1s"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NEO4J_PASSWORD", "test-password")
			t.Setenv("OLLAMA_URL", "")
			t.Setenv("OLLAMA_MODEL", "")
			t.Setenv("ANSWER_TIMEOUT", "")
			t.Setenv(tt.key, tt.value)

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("Load() error = %v, want validation error for %s", err, tt.key)
			}
		})
	}
}

func TestLoadOverridesAnswerGenerationConfig(t *testing.T) {
	t.Setenv("NEO4J_PASSWORD", "test-password")
	t.Setenv("OLLAMA_URL", " https://ollama.example.test/models ")
	t.Setenv("OLLAMA_MODEL", " custom-model:v2 ")
	t.Setenv("ANSWER_TIMEOUT", "250ms")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.OllamaURL != "https://ollama.example.test/models" || cfg.OllamaModel != "custom-model:v2" || cfg.AnswerTimeout != 250*time.Millisecond {
		t.Fatalf("unexpected answer generation configuration: URL=%q model=%q timeout=%v", cfg.OllamaURL, cfg.OllamaModel, cfg.AnswerTimeout)
	}
}
