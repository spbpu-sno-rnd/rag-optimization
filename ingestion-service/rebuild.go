package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"syscall"
	"time"
)

func searchStopped(ctx context.Context, retrievalURL string) error {
	parsed, err := url.Parse(retrievalURL)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("RETRIEVAL_URL must use HTTP")
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("RETRIEVAL_URL must include a host")
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(checkCtx, "tcp", net.JoinHostPort(parsed.Hostname(), port))
	if err == nil {
		conn.Close()
		return fmt.Errorf("retrieval-service still responds at %s; stop it before rebuilding", retrievalURL)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, syscall.Errno(10061)) {
		return fmt.Errorf("cannot confirm retrieval-service is stopped: %w", err)
	}
	return nil
}

func rebuild(ctx context.Context, c config, model *modelClient, aliases map[string]string, input, output string) error {
	if err := searchStopped(ctx, c.RetrievalURL); err != nil {
		return err
	}
	chunks, relations, err := readCorpus(ctx, input, model, aliases, c.MaxTokens)
	if err != nil {
		return err
	}
	if err := validateEvidence(chunks, relations); err != nil {
		return err
	}
	if err := writeJSONL(output, chunks); err != nil {
		return err
	}
	if err := searchStopped(ctx, c.RetrievalURL); err != nil {
		return err
	}
	graph, err := newGraph(ctx, c)
	if err != nil {
		return fmt.Errorf("connect Neo4j: %w", err)
	}
	defer func() { _ = graph.close(context.Background()) }()
	qdrant := newQdrant(c)
	if err := qdrant.reset(ctx); err != nil {
		return fmt.Errorf("reset Qdrant: %w", err)
	}
	if err := graph.reset(ctx); err != nil {
		return err
	}
	if err := qdrant.upload(ctx, chunks); err != nil {
		return fmt.Errorf("upload Qdrant: %w", err)
	}
	if err := graph.upload(ctx, chunks, relations); err != nil {
		return fmt.Errorf("upload Neo4j: %w", err)
	}
	_, _ = fmt.Fprintf(os.Stdout, "rebuilt %d chunks and %d relations; JSONL: %s\n", len(chunks), len(relations), output)
	return nil
}
