package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRebuildRequiresSearchStopped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	if err := searchStopped(context.Background(), server.URL); err == nil {
		t.Fatal("running retrieval was accepted")
	}
	server.Close()
	if err := searchStopped(context.Background(), server.URL); err != nil {
		t.Fatalf("stopped retrieval was rejected: %v", err)
	}
}
