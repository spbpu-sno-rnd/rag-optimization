package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr         string
	IngestionURL     string
	IngestionTimeout time.Duration
	OllamaURL        string
	OllamaModel      string
	AnswerTimeout    time.Duration
	QdrantURL        string
	QdrantCollection string
	QdrantAPIKey     string
	Neo4jURI         string
	Neo4jUsername    string
	Neo4jPassword    string
	Neo4jDatabase    string
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:         envOrDefault("HTTP_ADDR", ":8080"),
		IngestionURL:     envOrDefault("INGESTION_URL", "http://localhost:8081"),
		OllamaURL:        envOrDefault("OLLAMA_URL", "http://localhost:11434"),
		OllamaModel:      envOrDefault("OLLAMA_MODEL", "qwen2.5:3b"),
		QdrantURL:        envOrDefault("QDRANT_URL", "http://localhost:6333"),
		QdrantCollection: envOrDefault("QDRANT_COLLECTION", "documents"),
		QdrantAPIKey:     os.Getenv("QDRANT_API_KEY"),
		Neo4jURI:         envOrDefault("NEO4J_URI", "bolt://localhost:7687"),
		Neo4jUsername:    envOrDefault("NEO4J_USERNAME", "neo4j"),
		Neo4jPassword:    os.Getenv("NEO4J_PASSWORD"),
		Neo4jDatabase:    envOrDefault("NEO4J_DATABASE", "neo4j"),
	}

	timeout, err := time.ParseDuration(envOrDefault("INGESTION_TIMEOUT", "10s"))
	if err != nil || timeout <= 0 {
		return Config{}, fmt.Errorf("INGESTION_TIMEOUT must be a positive duration")
	}
	cfg.IngestionTimeout = timeout
	answerTimeout, err := time.ParseDuration(envOrDefault("ANSWER_TIMEOUT", "40s"))
	if err != nil || answerTimeout <= 0 {
		return Config{}, fmt.Errorf("ANSWER_TIMEOUT must be a positive duration")
	}
	cfg.AnswerTimeout = answerTimeout

	ollamaURL, err := url.Parse(cfg.OllamaURL)
	if err != nil || (ollamaURL.Scheme != "http" && ollamaURL.Scheme != "https") || ollamaURL.Hostname() == "" || ollamaURL.User != nil || ollamaURL.RawQuery != "" || ollamaURL.ForceQuery || strings.Contains(cfg.OllamaURL, "#") {
		return Config{}, fmt.Errorf("OLLAMA_URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}

	if cfg.Neo4jPassword == "" {
		return Config{}, fmt.Errorf("NEO4J_PASSWORD must be set")
	}
	for name, rawURL := range map[string]string{
		"INGESTION_URL": cfg.IngestionURL,
		"QDRANT_URL":    cfg.QdrantURL,
		"NEO4J_URI":     cfg.Neo4jURI,
	} {
		if err := validateURL(name, rawURL); err != nil {
			return Config{}, err
		}
	}
	if strings.TrimSpace(cfg.QdrantCollection) == "" {
		return Config{}, fmt.Errorf("QDRANT_COLLECTION must not be empty")
	}
	if strings.TrimSpace(cfg.Neo4jDatabase) == "" {
		return Config{}, fmt.Errorf("NEO4J_DATABASE must not be empty")
	}

	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func validateURL(name, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute URL with scheme and host", name)
	}
	return nil
}
