package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type qdrantStore struct {
	baseURL, collection, apiKey string
	client                      *http.Client
}

func newQdrant(c config) *qdrantStore {
	return &qdrantStore{strings.TrimRight(c.QdrantURL, "/"), c.QdrantCollection, c.QdrantAPIKey, &http.Client{Timeout: 30 * time.Second}}
}

func (q *qdrantStore) call(ctx context.Context, method, path string, body any, result any, allowed ...int) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, q.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if q.apiKey != "" {
		req.Header.Set("api-key", q.apiKey)
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	for _, status := range allowed {
		if resp.StatusCode == status {
			if result == nil {
				return nil
			}
			return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(result)
		}
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("Qdrant %s %s returned HTTP %d: %s", method, path, resp.StatusCode, detail)
}

func (q *qdrantStore) collectionPath() string { return "/collections/" + url.PathEscape(q.collection) }

func (q *qdrantStore) reset(ctx context.Context) error {
	path := q.collectionPath()
	var existing any
	err := q.call(ctx, http.MethodGet, path, nil, &existing, http.StatusOK)
	if err == nil {
		if err := q.call(ctx, http.MethodDelete, path, nil, nil, http.StatusOK); err != nil {
			return err
		}
	} else if !strings.Contains(err.Error(), "HTTP 404") {
		return err
	}
	return q.call(ctx, http.MethodPut, path, map[string]any{"vectors": map[string]any{"size": 384, "distance": "Cosine"}}, nil, http.StatusOK)
}

func (q *qdrantStore) upload(ctx context.Context, chunks []chunk) error {
	type point struct {
		ID      string         `json:"id"`
		Vector  []float32      `json:"vector"`
		Payload map[string]any `json:"payload"`
	}
	for start := 0; start < len(chunks); start += 128 {
		end := start + 128
		if end > len(chunks) {
			end = len(chunks)
		}
		points := make([]point, 0, end-start)
		for _, c := range chunks[start:end] {
			points = append(points, point{c.ID, c.Vector, map[string]any{"chunk_id": c.ID, "document_id": c.DocumentID, "text": c.Text, "metadata": c.Metadata}})
		}
		if err := q.call(ctx, http.MethodPut, q.collectionPath()+"/points?wait=true", map[string]any{"points": points}, nil, http.StatusOK); err != nil {
			return err
		}
	}
	var counted struct {
		Result struct {
			Count int `json:"count"`
		} `json:"result"`
	}
	if err := q.call(ctx, http.MethodPost, q.collectionPath()+"/points/count", map[string]bool{"exact": true}, &counted, http.StatusOK); err != nil {
		return err
	}
	if counted.Result.Count != len(chunks) {
		return fmt.Errorf("Qdrant has %d points, expected %d", counted.Result.Count, len(chunks))
	}
	return nil
}

type graphStore struct {
	driver   neo4j.DriverWithContext
	database string
}

func newGraph(ctx context.Context, c config) (*graphStore, error) {
	driver, err := neo4j.NewDriverWithContext(c.Neo4jURI, neo4j.BasicAuth(c.Neo4jUser, c.Neo4jPassword, ""))
	if err != nil {
		return nil, err
	}
	if err := driver.VerifyConnectivity(ctx); err != nil {
		_ = driver.Close(ctx)
		return nil, err
	}
	return &graphStore{driver, c.Neo4jDatabase}, nil
}

func (g *graphStore) close(ctx context.Context) error { return g.driver.Close(ctx) }

func (g *graphStore) write(ctx context.Context, query string, params map[string]any) error {
	session := g.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite, DatabaseName: g.database})
	defer session.Close(ctx)
	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		result, err := tx.Run(ctx, query, params)
		if err != nil {
			return nil, err
		}
		return result.Consume(ctx)
	})
	return err
}

func (g *graphStore) reset(ctx context.Context) error {
	for _, query := range []string{
		"MATCH (n) WHERE n:Chunk OR n:Document OR n:Entity DETACH DELETE n",
		"CREATE CONSTRAINT chunk_id IF NOT EXISTS FOR (c:Chunk) REQUIRE c.id IS UNIQUE",
		"CREATE CONSTRAINT document_id IF NOT EXISTS FOR (d:Document) REQUIRE d.id IS UNIQUE",
		"CREATE CONSTRAINT entity_name IF NOT EXISTS FOR (e:Entity) REQUIRE e.name IS UNIQUE",
	} {
		if err := g.write(ctx, query, nil); err != nil {
			return fmt.Errorf("reset Neo4j: %w", err)
		}
	}
	return nil
}

func (g *graphStore) upload(ctx context.Context, chunks []chunk, relations []relation) error {
	for start := 0; start < len(chunks); start += 100 {
		end := start + 100
		if end > len(chunks) {
			end = len(chunks)
		}
		rows := make([]map[string]any, 0, end-start)
		for _, c := range chunks[start:end] {
			rows = append(rows, map[string]any{"id": c.ID, "document_id": c.DocumentID, "text": c.Text, "entities": c.Entities})
		}
		query := `UNWIND $rows AS row
MERGE (d:Document {id:row.document_id})
CREATE (c:Chunk {id:row.id, text:row.text})
CREATE (d)-[:HAS_CHUNK]->(c)
WITH c, row UNWIND row.entities AS name
MERGE (e:Entity {name:name})
CREATE (e)-[:MENTIONED_IN]->(c)`
		if err := g.write(ctx, query, map[string]any{"rows": rows}); err != nil {
			return fmt.Errorf("write chunks to Neo4j: %w", err)
		}
	}
	for start := 0; start < len(relations); start += 100 {
		end := start + 100
		if end > len(relations) {
			end = len(relations)
		}
		rows := make([]map[string]any, 0, end-start)
		for _, r := range relations[start:end] {
			rows = append(rows, map[string]any{"source": r.Source, "target": r.Target, "relation_type": r.RelationType, "source_chunk_ids": r.SourceChunkIDs})
		}
		query := `UNWIND $rows AS row
MATCH (source:Entity {name:row.source}), (target:Entity {name:row.target})
CREATE (source)-[:RELATED_TO {relation_type:row.relation_type, source_chunk_ids:row.source_chunk_ids}]->(target)`
		if err := g.write(ctx, query, map[string]any{"rows": rows}); err != nil {
			return fmt.Errorf("write relations to Neo4j: %w", err)
		}
	}
	return g.verify(ctx, len(chunks), len(relations))
}

func (g *graphStore) verify(ctx context.Context, wantChunks, wantRelations int) error {
	session := g.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead, DatabaseName: g.database})
	defer session.Close(ctx)
	for _, check := range []struct {
		query string
		want  int
	}{
		{`MATCH (c:Chunk) RETURN count(c) AS n`, wantChunks},
		{`MATCH ()-[r:RELATED_TO]->() RETURN count(r) AS n`, wantRelations},
		{`MATCH ()-[r:RELATED_TO]->() UNWIND r.source_chunk_ids AS id OPTIONAL MATCH (c:Chunk {id:id}) WITH c WHERE c IS NULL RETURN count(*) AS n`, 0},
	} {
		result, err := session.Run(ctx, check.query, nil)
		if err != nil {
			return err
		}
		if !result.Next(ctx) {
			return fmt.Errorf("Neo4j verification returned no row: %w", result.Err())
		}
		value, _ := result.Record().Get("n")
		actual, ok := value.(int64)
		if !ok || actual != int64(check.want) {
			return fmt.Errorf("Neo4j verification failed: got %v, expected %d", value, check.want)
		}
		if err := result.Err(); err != nil {
			return err
		}
	}
	return nil
}
