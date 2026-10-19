package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"retrieval-service/internal/client/ingestion"
	"retrieval-service/internal/config"
	deliveryhttp "retrieval-service/internal/delivery/http"
	"retrieval-service/internal/repository/graph"
	"retrieval-service/internal/repository/vector"
	"retrieval-service/internal/usecase"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startupCtx, cancelStartup := context.WithTimeout(ctx, 10*time.Second)
	defer cancelStartup()

	qdrantHTTPClient := &http.Client{Timeout: 10 * time.Second}
	qdrant, err := vector.NewQdrant(cfg.QdrantURL, cfg.QdrantCollection, cfg.QdrantAPIKey, qdrantHTTPClient)
	if err != nil {
		return fmt.Errorf("configure Qdrant: %w", err)
	}
	if err := qdrant.CheckCollection(startupCtx); err != nil {
		return fmt.Errorf("verify Qdrant collection: %w", err)
	}

	neo4jClient, err := graph.NewNeo4j(startupCtx, cfg.Neo4jURI, cfg.Neo4jUsername, cfg.Neo4jPassword, cfg.Neo4jDatabase)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := neo4jClient.Close(closeCtx); err != nil {
			logger.Error("close Neo4j driver", "error", err)
		}
	}()

	ingestionClient, err := ingestion.NewClient(
		cfg.IngestionURL,
		&http.Client{Timeout: cfg.IngestionTimeout},
	)
	if err != nil {
		return fmt.Errorf("configure ingestion client: %w", err)
	}
	searchUseCase, err := usecase.NewSearchUseCase(ingestionClient, qdrant, neo4jClient)
	if err != nil {
		return fmt.Errorf("configure search use case: %w", err)
	}
	handler, err := deliveryhttp.NewHandler(searchUseCase)
	if err != nil {
		return fmt.Errorf("configure HTTP handler: %w", err)
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           deliveryhttp.NewRouter(handler),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("HTTP server listening", "address", cfg.HTTPAddr)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down HTTP server: %w", err)
		}
		return nil
	}
}
