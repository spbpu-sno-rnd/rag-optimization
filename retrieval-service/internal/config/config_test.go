package config

import "testing"

func TestLoadRequiresNeo4jPassword(t *testing.T) {
	t.Setenv("NEO4J_PASSWORD", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded without NEO4J_PASSWORD")
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	t.Setenv("NEO4J_PASSWORD", "test-password")
	for _, key := range []string{"HTTP_ADDR", "INGESTION_URL", "INGESTION_TIMEOUT", "QDRANT_URL", "QDRANT_COLLECTION", "NEO4J_URI", "NEO4J_USERNAME"} {
		t.Setenv(key, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.QdrantCollection != "documents" || cfg.IngestionTimeout.String() != "10s" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadRejectsInvalidURLs(t *testing.T) {
	t.Setenv("NEO4J_PASSWORD", "test-password")
	t.Setenv("QDRANT_URL", "not-a-url")
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with invalid QDRANT_URL")
	}
}
