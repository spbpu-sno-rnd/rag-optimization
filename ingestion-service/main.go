package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type config struct {
	ModelURL, OllamaURL, OllamaModel                  string
	QdrantURL, QdrantCollection, QdrantAPIKey         string
	Neo4jURI, Neo4jUser, Neo4jPassword, Neo4jDatabase string
	Addr                                              string
	AliasesFile, RetrievalURL                         string
	MaxTokens, MaxConcurrent                          int
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func loadConfig() (config, error) {
	c := config{
		ModelURL:         env("MODEL_URL", "http://127.0.0.1:8082"),
		OllamaURL:        env("OLLAMA_URL", "http://127.0.0.1:11434"),
		OllamaModel:      env("OLLAMA_MODEL", "qwen2.5:3b"),
		QdrantURL:        env("QDRANT_URL", "http://127.0.0.1:6333"),
		QdrantCollection: env("QDRANT_COLLECTION", "documents"),
		QdrantAPIKey:     os.Getenv("QDRANT_API_KEY"),
		Neo4jURI:         env("NEO4J_URI", "bolt://127.0.0.1:7687"),
		Neo4jUser:        env("NEO4J_USERNAME", "neo4j"),
		Neo4jPassword:    os.Getenv("NEO4J_PASSWORD"),
		Neo4jDatabase:    env("NEO4J_DATABASE", "neo4j"),
		Addr:             env("INGESTION_ADDR", ":8081"),
		AliasesFile:      env("ALIASES_FILE", "corpus/aliases.json"),
		RetrievalURL:     env("RETRIEVAL_URL", "http://127.0.0.1:8080"),
	}
	var err error
	if c.MaxTokens, err = strconv.Atoi(env("CHUNK_MAX_TOKENS", "384")); err != nil || c.MaxTokens < 16 || c.MaxTokens > 512 {
		return c, errors.New("CHUNK_MAX_TOKENS must be 16..512")
	}
	if c.MaxConcurrent, err = strconv.Atoi(env("MAX_MODEL_REQUESTS", "2")); err != nil || c.MaxConcurrent < 1 || c.MaxConcurrent > 32 {
		return c, errors.New("MAX_MODEL_REQUESTS must be 1..32")
	}
	if c.Neo4jPassword == "" {
		return c, errors.New("NEO4J_PASSWORD must be set")
	}
	return c, nil
}

func main() {
	c, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	model := newModelClient(c)
	aliases, err := loadAliases(c.AliasesFile)
	if err != nil {
		log.Fatal(err)
	}
	if len(os.Args) > 1 && os.Args[1] == "rebuild" {
		if len(os.Args) != 5 || os.Args[2] != "--input" {
			log.Fatal("usage: ingestion-service rebuild --input DIR OUTPUT_JSONL")
		}
		if err := rebuild(ctx, c, model, aliases, os.Args[3], os.Args[4]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) != 1 && (len(os.Args) != 2 || os.Args[1] != "serve") {
		log.Fatal("usage: ingestion-service [serve|rebuild --input DIR OUTPUT_JSONL]")
	}
	server := &http.Server{Addr: c.Addr, Handler: queryHandler(model, aliases), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("query-context listening on %s", c.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func queryHandler(model *modelClient, aliases map[string]string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/query-context", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		var request struct {
			Query string `json:"query"`
		}
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err == nil {
			http.Error(w, "request must contain one JSON object", http.StatusBadRequest)
			return
		} else if !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return
		}
		request.Query = strings.TrimSpace(request.Query)
		if request.Query == "" {
			http.Error(w, "query must not be empty", http.StatusBadRequest)
			return
		}
		if len(request.Query) > 32<<10 {
			http.Error(w, "query is too long", http.StatusBadRequest)
			return
		}
		count, err := model.count(r.Context(), "query: "+request.Query)
		if err != nil {
			http.Error(w, "tokenizer unavailable: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		if count > 512 {
			http.Error(w, "query exceeds 512 model tokens", http.StatusBadRequest)
			return
		}
		embedding, err := model.embed(r.Context(), request.Query, "query")
		if err != nil {
			http.Error(w, "embedding unavailable: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		extracted, err := model.extract(r.Context(), request.Query, true)
		if err != nil {
			http.Error(w, "entity extraction unavailable: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		response := struct {
			Embedding []float32 `json:"embedding"`
			Entities  []string  `json:"entities"`
		}{embedding, canonicalEntities(request.Query, extracted.Entities, aliases)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	})
	return mux
}
